// Package client is the typed HTTP client for the Juice kernel API. It is the
// sole client of the kernel (R1.1): the rest of the BFF talks to the kernel
// only through this package, never by shelling out, reading the kernel DB, or
// importing kernel packages.
//
// Verified against the running kernel on 2026-06-03 and re-gated against the
// v0.12 kernel on 2026-08-03 and 2026-08-05 (see comments throughout and in
// errors.go). docs/API.md is the design contract; where the live kernel
// diverges, the verified behavior is recorded here and is authoritative.
//
// Every name is an address (requirements.md §4): a user is handle@kernel and an
// action owner@kernel/name, on this kernel too. This client passes names through
// verbatim and never composes, strips, or rewrites one; a bare name is input the
// kernel refuses (verified live 2026-09-26), and that typed error is the
// validation.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Client is the typed Juice kernel HTTP client. Every method takes the access
// token explicitly (open endpoints take ""), so the session layer controls
// token injection, refresh, and retry (R5.2). The client holds no session state.
type Client struct {
	baseURL string
	http    *http.Client
}

// New returns a Client for the kernel at baseURL. If hc is nil a default client
// with a sane timeout is used. Redirects are not followed: the PKCE authorize
// endpoint may answer with a 302 Location, which we must read rather than chase.
func New(baseURL string, hc *http.Client) *Client {
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{baseURL: baseURL, http: hc}
}

// ---- DTOs (snake_case wire tags; structured fields stay inline JSON, R4.1/R4.2) ----

// Tokens is an access/refresh token pair. Stored server-side only (R1.2).
type Tokens struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
}

// User is the self profile (GET /v1/me) and the create-user response. Money is
// integer credits. The kernel retired email in v0.9; Description is the free-text
// "about" an account may set (empty until set).
type User struct {
	ID                string       `json:"id"`
	Address           string       `json:"address"` // handle@kernel
	Description       string       `json:"description"`
	Available         int64        `json:"available"`
	Locked            int64        `json:"locked"`
	BlockchainAddress string       `json:"blockchain_address,omitempty"` // the caller's registered payout address
	Connectors        []Connector  `json:"connectors"`                   // directory-grouped consent tree (§8)
	Connections       []Connection `json:"connections"`                  // full upstream-account inventory
}

// Connector is one node of the directory-grouped consent tree in GET /v1/me: a
// folder (the granted actions' shared ref prefix, e.g. chat or chat/inbox), the
// upstream account(s) that back it (usually one), and the per-action grants under
// it. The directory is a display grouping only — it never gates a credential.
type Connector struct {
	Directory   string       `json:"directory"`
	Connections []Connection `json:"connections"`
	Actions     []Grant      `json:"actions"`
}

// Grant is a per-action delegated consent (a node under a Connector in GET /v1/me):
// a consent record only — never any token material. ProviderKey is the opaque key
// of the backing Connection (omitted on an unbackfilled legacy grant).
type Grant struct {
	Action      string   `json:"action"` // the granted action's address
	Scopes      []string `json:"scopes"`
	ProviderKey string   `json:"provider_key,omitempty"`
	CreatedAt   string   `json:"created_at"`
}

// Connection is a user's upstream account credential as summarised by GET /v1/me
// (§8): one per (user, provider), shared by every Grant that points at it. The
// token itself is never returned. Provider is the display host/label. ProviderKey
// is the opaque account key — it also addresses DELETE /v1/grants?account=; treat
// it as opaque, never parse it. Unused is true when no Grant currently points at
// it (the credential is kept for re-consent).
type Connection struct {
	Provider    string `json:"provider"`
	Actions     int    `json:"actions"` // number of grants pointing at this connection
	Unused      bool   `json:"unused"`
	ProviderKey string `json:"provider_key,omitempty"`
	CreatedAt   string `json:"created_at"`
}

// HTTPParam binds an action input field to a location in the upstream HTTP
// request. Verified live (2026-06-30): the kernel serializes params as an array
// of {name, in} objects, with in in {path, query, body}.
type HTTPParam struct {
	Name string `json:"name"`
	In   string `json:"in"`
}

// HTTPSpec is the decomposed view of a kind=http action's request (R8): the verb,
// the full URL (with {placeholders}), and the field bindings. Identical shape for
// manually-created and OpenAPI-imported actions. Present only on kind=http reads
// (absent for other kinds, hence a pointer); Params is omitted when the action
// uses implicit routing.
type HTTPSpec struct {
	Method string      `json:"method"`
	URL    string      `json:"url"`
	Params []HTTPParam `json:"params,omitempty"`
}

// Action carries both id (for management) and action (its address,
// owner@kernel/name, for calling) per R8. For kind=http, Source is a JSON-encoded descriptor string
// (present on single reads, omitted on list reads); do not interpret it for
// display. Instead, kind=http reads carry a decomposed HTTP object (absent for
// other kinds), which is the structured view the UI renders. The owner is the
// address's first segment; the kernel serves no separate owner field.
type Action struct {
	ID     string `json:"id"`
	Action string `json:"action"`
	Name   string `json:"name"`
	// Title is the one line a person reads in a list (kernel D4: required,
	// trimmed, 1–80 characters). It is part of the quoted terms.
	Title  string `json:"title"`
	Kind   string `json:"kind"`
	Active bool   `json:"active"`
	// Visibility is the kernel's three-way caller-scope: "private" (owner only),
	// "local" (any local caller of this kernel, never peers), or "public" (anyone,
	// and the only value served in manifests/gossip). It replaces the old boolean
	// `public`. Empty on a legacy kernel; treat empty as "private" (the default).
	Visibility string `json:"visibility"`
	// Discovered marks a catalog row this kernel knows only through gossip
	// from a peer (kernel D16): it has no local id, RemoteActionID is the
	// peer's own id for it, and its price is indicative until resolution.
	Discovered     bool            `json:"discovered,omitempty"`
	RemoteActionID string          `json:"remote_action_id,omitempty"`
	Price          int64           `json:"price"`
	Description    string          `json:"description"`
	InputSchema    json.RawMessage `json:"input_schema,omitempty"`
	OutputSchema   json.RawMessage `json:"output_schema,omitempty"`
	Source         json.RawMessage `json:"source,omitempty"`
	HTTP           *HTTPSpec       `json:"http,omitempty"`
	// AuthScheme is the upstream auth scheme (e.g. "delegated_bearer"), and
	// RequiresGrant reports whether a per-caller grant is needed to run the action.
	// The kernel exposes these even though the auth config/secrets stay write-only
	// (R9), so a client can tell an action needs a grant, and of which kind.
	AuthScheme    string `json:"auth_scheme,omitempty"`
	RequiresGrant bool   `json:"requires_grant,omitempty"`
	// Effect names a privileged execution effect from the action's signed
	// contract — "transfer" on a value-bearing action (verified live 2026-08-03
	// on sys/transfer), empty on every ordinary action. It is the ONLY signal
	// that completing a task on this action spends the completer's own money
	// (requirements.md §6.3): never key on the action's name.
	Effect string `json:"effect,omitempty"`
	// QuoteHash fingerprints the terms quoted here — action id, effect, title,
	// description, both schemas, and price (kernel P2). Pin it on a run
	// and a call whose terms moved is refused with terms_changed BEFORE funds are
	// locked, at zero charge, so a displayed price is binding rather than
	// advisory. Carried on action reads, listings, and sys/lookup rows; because
	// it hashes the STABLE id, a discovered hit and the proxy it resolves to
	// hash identically, which is what lets a first cross-kernel call bind.
	QuoteHash string `json:"quote_hash,omitempty"`
	// Evidence is what is known about the action, carried on a detail read and
	// on every sys/lookup row — never on a list row (verified live).
	Evidence  *ActionEvidence `json:"evidence,omitempty"`
	CreatedAt string          `json:"created_at"`
	UpdatedAt string          `json:"updated_at"`
}

// ActionEvidence is the evidence block of an action read: this kernel's own
// record, the provider's gossiped report (absent when the provider is this
// kernel), and every other kernel's report. The three are labelled by
// provenance and never summed (kernel §D16, U39).
type ActionEvidence struct {
	LocalExperience  *Stats     `json:"local_experience"`
	ProviderReported *Evidence  `json:"provider_reported"`
	ObservedByOthers []Evidence `json:"observed_by_others"`
	RetainedCap      int        `json:"retained_cap"`
}

// Experience is this kernel's own record of the action, nil where it holds none
// (a list row, or a discovered action nothing local backs).
func (a Action) Experience() *Stats {
	if a.Evidence == nil {
		return nil
	}
	return a.Evidence.LocalExperience
}

// Reports groups what kernels other than this one say about the action, each
// report under its issuer — the provider's own first — for the _evidence
// partial. Empty on an action no one else has reported on.
func (a Action) Reports() []IssuerEvidence {
	if a.Evidence == nil {
		return nil
	}
	var out []IssuerEvidence
	if p := a.Evidence.ProviderReported; p != nil {
		out = append(out, IssuerEvidence{Issuer: p.IssuerPublicKey, Self: true, Rows: []Evidence{*p}})
	}
	for _, e := range a.Evidence.ObservedByOthers {
		out = append(out, IssuerEvidence{Issuer: e.IssuerPublicKey, Rows: []Evidence{e}})
	}
	return out
}

// IsTransfer reports whether this action bears the value-transfer effect —
// the payment-task signal (requirements.md §6.3, §6.5).
func (a Action) IsTransfer() bool { return a.Effect == "transfer" }

// Ref returns the action's address, owner@kernel/name, as the kernel serves it.
func (a Action) Ref() string { return a.Action }

// Vis returns the normalized visibility ("private", "local", or "public"),
// defaulting an empty value (legacy kernel) to "private" — the kernel default.
func (a Action) Vis() string {
	if a.Visibility == "" {
		return "private"
	}
	return a.Visibility
}

// IsPublic / IsLocal / IsPrivate are template helpers for the visibility chip
// and workshop control.
func (a Action) IsPublic() bool  { return a.Vis() == "public" }
func (a Action) IsLocal() bool   { return a.Vis() == "local" }
func (a Action) IsPrivate() bool { return a.Vis() == "private" }

// CreateActionInput is the body for POST /v1/actions. Schemas are inline JSON.
// Auth is write-only upstream credential config {scheme, config, secrets}; never returned by the kernel.
type CreateActionInput struct {
	Name         string          `json:"name"`
	Title        string          `json:"title"`
	Kind         string          `json:"kind"`
	Source       string          `json:"source,omitempty"`
	Method       string          `json:"method,omitempty"`
	Params       []HTTPParam     `json:"params,omitempty"`
	Description  string          `json:"description,omitempty"`
	Price        int64           `json:"price"`
	InputSchema  json.RawMessage `json:"input_schema,omitempty"`
	OutputSchema json.RawMessage `json:"output_schema,omitempty"`
	Auth         json.RawMessage `json:"auth,omitempty"`
	// WasmArtifact is the compiled module, base64, for kind=wasm. It is what
	// sys/tinygo/compile returns, and without it a person can compile in the UI
	// and then have nowhere to put the result.
	WasmArtifact string `json:"wasm_artifact,omitempty"`
}

// UpdateActionInput is the body for PUT /v1/actions. All fields but the target are optional.
type UpdateActionInput struct {
	// Target is the action id or owner/path this update names; set by UpdateAction.
	Target       string          `json:"target"`
	Price        *int64          `json:"price,omitempty"`
	Title        *string         `json:"title,omitempty"`
	Description  *string         `json:"description,omitempty"`
	Source       *string         `json:"source,omitempty"`
	Method       string          `json:"method,omitempty"`
	Params       []HTTPParam     `json:"params,omitempty"`
	InputSchema  json.RawMessage `json:"input_schema,omitempty"`
	OutputSchema json.RawMessage `json:"output_schema,omitempty"`
	// Visibility sets the three-way caller-scope ("private"/"local"/"public").
	// Replaces the old boolean `public`.
	Visibility *string         `json:"visibility,omitempty"`
	Auth       json.RawMessage `json:"auth,omitempty"`
	// WasmArtifact replaces the compiled module of a kind=wasm action.
	WasmArtifact string `json:"wasm_artifact,omitempty"`
}

// Process is a funded computation lifetime. Processes are auto-created by run.
// The kernel identifies the owner by address and flags a process parked on a
// remote receipt (funds locked on a possibly-offline peer) via AwaitingReceipt.
type Process struct {
	ID                   string `json:"id"`
	Owner                string `json:"owner"`
	Available            int64  `json:"available"`
	Locked               int64  `json:"locked"`
	Status               string `json:"status"`
	AwaitingReceipt      bool   `json:"awaiting_receipt"`
	AwaitingReceiptSince string `json:"awaiting_receipt_since,omitempty"`
	CreatedAt            string `json:"created_at"`
	EndedAt              string `json:"ended_at"`
}

// Endable reports a process its owner may force-end: open, and not awaiting a
// peer's receipt, which the kernel refuses to cancel since the peer may already
// have earned it (kernel §D19).
func (p Process) Endable() bool { return p.Status == "open" && !p.AwaitingReceipt }

// RunRequest is the body for POST /v1/run.
type RunRequest struct {
	Action    string          `json:"action"`
	Args      json.RawMessage `json:"args"`
	QuoteHash string          `json:"quote_hash,omitempty"`
}

// CallReply is the success response of POST /v1/run.
type CallReply struct {
	Result    json.RawMessage `json:"result"`
	TxID      string          `json:"tx_id"`
	TraceID   string          `json:"trace_id"`
	ReceiptID string          `json:"receipt_id"`
}

// TransactionRating is the embedded rating on a transaction response.
type TransactionRating struct {
	Value int    `json:"value"`
	Note  string `json:"note"`
}

// Transaction is the settled record of a call. gross/net/fee/refund are integer
// credits. result is null on failure; reason is set on failure. Parties are
// identified by address (owner=payer, caller=requester, target=payee; a peer
// kernel as a party is its name alone), and the action by its address as it
// was when the call was made; the kernel never returns raw user ids.
type Transaction struct {
	ID            string             `json:"id"`
	ProcessID     string             `json:"process_id"`
	TraceID       string             `json:"trace_id"`
	ParentTraceID string             `json:"parent_trace_id"`
	Owner         string             `json:"owner"`
	Caller        string             `json:"caller"`
	Target        string             `json:"target"`
	ActionID      string             `json:"action_id"`
	Action        string             `json:"action"`
	Args          json.RawMessage    `json:"args"`
	Result        json.RawMessage    `json:"result"`
	Status        string             `json:"status"`
	Gross         int64              `json:"gross"`
	Net           int64              `json:"net"`
	Fee           int64              `json:"fee"`
	Refund        int64              `json:"refund"`
	Reason        string             `json:"reason"`
	Rating        *TransactionRating `json:"rating"`
	StartedAt     string             `json:"started_at"`
	EndedAt       string             `json:"ended_at"`
	// A cross-kernel call carries the peer's signed receipt and the TicketID both
	// kernels know its obligation by (kernel §P10). On such a call Net is the
	// obligation returned to the caller and Fee the import fee retained here —
	// NOT the provider's revenue, which is the peer's own economics inside
	// RemoteReceiptJSON. RemoteReceiptHash is set iff the call crossed a boundary.
	RemoteActionID    string `json:"remote_action_id,omitempty"`
	RemoteReceiptHash string `json:"remote_receipt_hash,omitempty"`
	RemoteSignerKey   string `json:"remote_signer_key,omitempty"`
	TicketID          string `json:"ticket_id,omitempty"`
	// Payment is set on a transfer to a user of another kernel: the state of this
	// kernel's payment to that kernel (pending, announced). It says the money
	// left, never that the recipient was credited.
	Payment string `json:"payment,omitempty"`
}

// IsRemote reports a call that crossed a kernel boundary: it settles per call on
// the rail, so its cost breakdown reads differently (§6.5).
func (t Transaction) IsRemote() bool { return t.RemoteReceiptHash != "" }

// ReceiptVerification is the kernel's immutable receipt-verification report.
// The exact per-check object is kernel-owned, so the BFF renders it as JSON.
type ReceiptVerification struct {
	Valid  bool            `json:"valid"`
	Checks json.RawMessage `json:"checks"`
}

// Stats is this kernel's own record of an action: the evidence block's
// local_experience.
type Stats struct {
	ActionID        string  `json:"action_id"`
	Uses            int64   `json:"uses"`
	Successes       int64   `json:"successes"`
	Failures        int64   `json:"failures"`
	RatingCount     int64   `json:"rating_count"`
	LatencyEstimate float64 `json:"latency_estimate"`
	RatingEstimate  float64 `json:"rating_estimate"`
	LastUsedAt      string  `json:"last_used_at"`
}

// Rating is one entry of GET /v1/actions/{id}/ratings (list may be null): the
// kernel's public projection — value, note, time and nothing else. The rater,
// transaction, and receipt are withheld (verified live 2026-08-05), so a rating
// is readable wherever the action is, anonymously for a public one.
type Rating struct {
	Value     int    `json:"value"`
	Note      string `json:"note,omitempty"`
	CreatedAt string `json:"created_at"`
}

// Task is a suspended computation boundary: a pre-baked future Call that a
// designated caller completes by supplying the remaining input. Parties are
// addresses. A task this kernel holds names its completion target (Action) and
// the action that created it (CreatedBy); a task held on another kernel for
// someone here names neither, and its Owner is the holding kernel's name.
// WaitingOnPeer flags a waiting task whose caller is on another kernel.
type Task struct {
	ID             string          `json:"id"`
	RequiredCaller string          `json:"required_caller"` // the completer's address
	Owner          string          `json:"owner"`           // who asked: the process owner, or the holding kernel
	Action         string          `json:"action"`
	CreatedBy      string          `json:"created_by"`
	Price          int64           `json:"price"`
	PartialArgs    json.RawMessage `json:"partial_args"`
	Status         string          `json:"status"` // waiting|running|done|cancelled
	WaitingOnPeer  bool            `json:"waiting_on_peer"`
	TxID           string          `json:"tx_id"`
	CreatedAt      string          `json:"created_at"`
	// AllowedInput is the kernel-derived completion schema on a waiting task
	// (action.input_schema minus the keys sealed in partial_args), served so the
	// completer never needs to read the target action, which may be private.
	AllowedInput json.RawMessage `json:"allowed_input,omitempty"`
}

// Waiting reports a task that can still be completed or declined.
func (t Task) Waiting() bool { return t.Status == "waiting" }

// TaskCompleteReply is the response of POST /v1/tasks/{id}/complete. A task
// held on another kernel completes there: ReceiptID is then empty and TxID is
// that kernel's transaction, which this kernel does not serve.
type TaskCompleteReply struct {
	Result    json.RawMessage `json:"result"`
	TxID      string          `json:"tx_id"`
	TraceID   string          `json:"trace_id"`
	ReceiptID string          `json:"receipt_id"`
	TaskID    string          `json:"task_id"`
}

// ImportResult is the response of POST /v1/actions/import. Go's JSON decoding is
// case-insensitive, so these tags match the kernel's capitalized keys.
type ImportResult struct {
	Created     []Action          `json:"created"`
	Updated     []Action          `json:"updated"`
	Unchanged   []Action          `json:"unchanged"`
	Deactivated []Action          `json:"deactivated"`
	Rejected    []ImportRejection `json:"rejected"`
	Notices     []ImportNotice    `json:"notices"`
}

// ImportRejection is one operation the kernel would not install, and why —
// a missing description, an unsupported body, an ambiguous success schema. The
// wizard shows them: an operation silently absent reads as a kernel fault.
type ImportRejection struct {
	Key      string `json:"key"`
	Location string `json:"location"` // METHOD /path in the document
	Reason   string `json:"reason"`
}

// ImportNotice is one place where the stored contract differs from the
// document because the kernel folded a form into its schema subset (a 3.0
// nullable, say). The operation is installed; the note says what changed.
type ImportNotice struct {
	Key      string `json:"key"`
	Location string `json:"location"`
	Note     string `json:"note"`
}

// Health is GET /health (open): the identity banner a client pins before it
// trusts a server (API C13). Beyond naming the kernel it fixes the money unit —
// Decimals is the world's base-unit scale and Symbol what that money is called,
// "credits" or "USDC" — and, on a world whose money has addresses,
// BlockchainAddress is where this kernel is paid and Token the contract that
// money is. Handle is the kernel's own name: the kernel segment of every local
// address.
type Health struct {
	Status             string `json:"status"`
	Handle             string `json:"handle"`
	PublicKey          string `json:"public_key"`
	Network            string `json:"network"`
	NetworkFingerprint string `json:"network_fingerprint"`
	Decimals           int    `json:"decimals"`
	Symbol             string `json:"symbol"`
	Token              string `json:"token"`
	BlockchainAddress  string `json:"blockchain_address"`
}

// KeyLen is the length of a kernel's public key: 32 bytes, base64url-encoded.
// A name of exactly this length is a key rather than a petname or a handle —
// the three name classes are disjoint by shape (kernel §D15).
const KeyLen = 43

// ShortKey abbreviates a kernel's public key for display. A key is the
// canonical global name and unreadable in full, so it shows head…tail: both
// ends carry distinguishing signal, and the result is still the value to
// eyeball against the original (no hashing). This is the single definition, so
// one kernel abbreviates identically everywhere it is named — an error
// sentence, the header, a card.
func ShortKey(key string) string {
	if len(key) <= 14 {
		return key
	}
	return key[:10] + "…" + key[len(key)-4:]
}

// ---- core request helper ----

// do performs an HTTP request to the kernel, injecting the bearer token and a
// request id (from context, if present), marshaling body, and decoding the
// response into out. Non-2xx responses are classified into a *KernelError.
func (c *Client) do(ctx context.Context, method, path, token string, body, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal request: %w", err)
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, rdr)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if rid := RequestIDFromContext(ctx); rid != "" {
		req.Header.Set("X-Request-Id", rid)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		// No HTTP response at all — the kernel is unreachable (down, restarting, or a
		// timeout). Type it so callers can surface "kernel unreachable" instead of a
		// generic failure or a misleading "invalid credentials".
		return &KernelError{Kind: KindUnavailable, KernelMessage: "kernel request: " + err.Error()}
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	if cerr := classify(resp.StatusCode, raw); cerr != nil {
		return cerr
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
	}
	return nil
}

// emptyArgs returns "{}" when args is empty, per R4.7.
func emptyArgs(args json.RawMessage) json.RawMessage {
	if len(args) == 0 {
		return json.RawMessage(`{}`)
	}
	return args
}

// ---- server / metadata (open) ----

func (c *Client) Health(ctx context.Context) (Health, error) {
	var h Health
	err := c.do(ctx, http.MethodGet, "/health", "", nil, &h)
	return h, err
}

// ---- authentication ----

// Authorize performs the PKCE authorize step. We omit redirect_uri so the kernel
// answers 200 with {"redirect":"?code=..."} (verified live); the code is parsed
// out of that query. password and code_challenge (S256) are passed through.
func (c *Client) Authorize(ctx context.Context, handle, password, codeChallenge string) (string, error) {
	body := map[string]string{
		"handle":         handle,
		"password":       password,
		"code_challenge": codeChallenge,
	}
	var resp struct {
		Redirect string `json:"redirect"`
	}
	if err := c.do(ctx, http.MethodPost, "/v1/auth/authorize", "", body, &resp); err != nil {
		return "", err
	}
	return codeFromRedirect(resp.Redirect)
}

// codeFromRedirect extracts the "code" query parameter from a redirect value
// such as "?code=XXX" or a full URL.
func codeFromRedirect(redirect string) (string, error) {
	u, err := url.Parse(redirect)
	if err != nil {
		return "", fmt.Errorf("parse redirect: %w", err)
	}
	code := u.Query().Get("code")
	if code == "" {
		return "", fmt.Errorf("no code in authorize redirect")
	}
	return code, nil
}

// ExchangeCode swaps an auth code + verifier for a token pair.
func (c *Client) ExchangeCode(ctx context.Context, code, verifier string) (Tokens, error) {
	body := map[string]string{
		"grant_type":    "authorization_code",
		"code":          code,
		"code_verifier": verifier,
	}
	var t Tokens
	err := c.do(ctx, http.MethodPost, "/v1/auth/token", "", body, &t)
	return t, err
}

// Refresh rotates the token pair. The kernel invalidates the old refresh token,
// so callers must use single-flight refresh (R5.2).
func (c *Client) Refresh(ctx context.Context, refreshToken string) (Tokens, error) {
	var t Tokens
	err := c.do(ctx, http.MethodPost, "/v1/auth/refresh", "", map[string]string{"refresh_token": refreshToken}, &t)
	return t, err
}

// Logout revokes the refresh token.
func (c *Client) Logout(ctx context.Context, refreshToken string) error {
	return c.do(ctx, http.MethodPost, "/v1/auth/logout", "", map[string]string{"refresh_token": refreshToken}, nil)
}

// RecoverStart begins seed-phrase recovery (§12): it returns a single-use,
// TTL-bound nonce the caller signs with the account's recovery key. Unauthenticated
// (a locked-out user has no token). ErrInvalidState if no recovery key is enrolled.
func (c *Client) RecoverStart(ctx context.Context, handle string) (string, error) {
	var resp struct {
		Nonce string `json:"nonce"`
	}
	err := c.do(ctx, http.MethodPost, "/v1/auth/recover/start", "", map[string]string{"handle": handle}, &resp)
	return resp.Nonce, err
}

// RecoverComplete resets the password by proving possession of the recovery key:
// signature is base64url(Ed25519-sign over JCS({"recovery_challenge": nonce})).
// Unauthenticated; a bad signature or expired nonce is ErrUnauthorized.
func (c *Client) RecoverComplete(ctx context.Context, handle, nonce, signature, password string) error {
	body := map[string]string{
		"handle":    handle,
		"nonce":     nonce,
		"signature": signature,
		"password":  password,
	}
	return c.do(ctx, http.MethodPost, "/v1/auth/recover/complete", "", body, nil)
}

// ---- grants and connections (delegated consent, §8) ----

// GrantPlan is the consent plan for a selector (GET /v1/grants/plan): the caller's
// callable delegated actions grouped by upstream account, plus counts of actions
// skipped because the caller is loginless or may not call them. A client shows the
// plan as the consent act — the actions to be connected (kernel requirement §14).
type GrantPlan struct {
	Groups            []GrantGroup `json:"groups"`
	SkippedLoginless  int          `json:"skipped_loginless"`
	SkippedUncallable int          `json:"skipped_uncallable"`
}

// GrantGroup is one upstream-account group within a plan. ProviderKey is the
// stable key (bearer:<host> | oauth:<token_url>|<client_id>) used to address the
// account; Provider is its display host/label. Covered is true when an existing
// Connection already grants the whole group with no further consent.
type GrantGroup struct {
	ProviderKey string            `json:"provider_key"`
	Provider    string            `json:"provider"`
	Scheme      string            `json:"scheme"`
	Scopes      []string          `json:"scopes"`
	Connected   bool              `json:"connected"`
	Covered     bool              `json:"covered"`
	Actions     []GrantPlanAction `json:"actions"`
}

// GrantPlanAction is one action in a group, with whether it is already granted.
type GrantPlanAction struct {
	ActionID string `json:"action_id"`
	Action   string `json:"action"`
	Granted  bool   `json:"granted"`
}

// PlanGrants returns the consent plan for a selector (owner@kernel,
// owner@kernel/path, or a full action address). Authenticated as the caller whose coverage is planned.
func (c *Client) PlanGrants(ctx context.Context, token, selector string) (GrantPlan, error) {
	q := url.Values{"selector": {selector}}
	var p GrantPlan
	err := c.do(ctx, http.MethodGet, "/v1/grants/plan?"+q.Encode(), token, nil, &p)
	return p, err
}

// StartGrant begins delegated-OAuth consent for a selector's provider group.
// provider disambiguates when the selector spans groups (may be empty for a
// single group). It returns granted=true (with an empty authorizeURL) when the
// caller's Connection already covers the group so no browser round-trip is needed;
// otherwise it returns the provider authorize URL and the opaque state to match on
// the callback. Authenticated as the grantor.
func (c *Client) StartGrant(ctx context.Context, token, selector, provider, redirectURI string) (authorizeURL, state string, granted bool, err error) {
	body := map[string]string{"selector": selector, "redirect_uri": redirectURI}
	if provider != "" {
		body["provider"] = provider
	}
	var resp struct {
		Status       string `json:"status"`
		State        string `json:"state"`
		AuthorizeURL string `json:"authorize_url"`
	}
	if err = c.do(ctx, http.MethodPost, "/v1/grants/start", token, body, &resp); err != nil {
		return "", "", false, err
	}
	return resp.AuthorizeURL, resp.State, resp.Status == "granted", nil
}

// CompleteGrant exchanges the provider code for the group's grants. state must be
// the value from StartGrant. Returns the actions that were granted.
// It answers `{status:"granted", actions}` when the exchange completed, and
// `{status:"pending"}` when it has not yet — the device flow polls, and a code
// flow may also answer so. Pending is reported as such: treating it as granted
// would tell a person an account is connected when the next call will still
// answer grant_required.
func (c *Client) CompleteGrant(ctx context.Context, token, state, code string) ([]string, bool, error) {
	var resp struct {
		Status  string   `json:"status"`
		Actions []string `json:"actions"`
	}
	err := c.do(ctx, http.MethodPost, "/v1/grants/complete", token, map[string]string{"state": state, "code": code}, &resp)
	return resp.Actions, resp.Status == "pending", err
}

// ConnectGrant attaches a static per-caller token to a delegated_bearer selector's
// group (POST /v1/grants) — the non-OAuth twin of start/complete. One call mints a
// grant per action in the group against one Connection; provider disambiguates
// when the selector spans groups (may be empty for a single group). Authenticated
// as the grantor. Idempotent: a re-connect overwrites in place. Returns the actions
// that were granted.
func (c *Client) ConnectGrant(ctx context.Context, token, selector, provider, staticToken string) ([]string, error) {
	body := map[string]string{"selector": selector, "token": staticToken}
	if provider != "" {
		body["provider"] = provider
	}
	var resp struct {
		Actions []string `json:"actions"`
	}
	err := c.do(ctx, http.MethodPost, "/v1/grants", token, body, &resp)
	return resp.Actions, err
}

// DeleteGrant revokes the caller's grants matching a selector (the grants only;
// the Connection is kept for re-consent).
func (c *Client) DeleteGrant(ctx context.Context, token, selector string) error {
	q := url.Values{"selector": {selector}}
	return c.do(ctx, http.MethodDelete, "/v1/grants?"+q.Encode(), token, nil, nil)
}

// DeleteConnection deletes the caller's upstream account by provider_key and
// cascades every grant that pointed at it. account is the stable provider_key
// (bearer:<host> | oauth:<token_url>|<client_id>).
func (c *Client) DeleteConnection(ctx context.Context, token, account string) error {
	q := url.Values{"account": {account}}
	return c.do(ctx, http.MethodDelete, "/v1/grants?"+q.Encode(), token, nil, nil)
}

// ---- users ----

// CreateUser registers a password account. recoveryPubKey is the base64url
// Ed25519 recovery key derived client-side from the seed phrase (§12); pass ""
// to enroll no recovery key (headless service accounts).
func (c *Client) CreateUser(ctx context.Context, handle, password, recoveryPubKey string) (User, error) {
	body := map[string]string{"handle": handle, "password": password}
	if recoveryPubKey != "" {
		body["recovery_public_key"] = recoveryPubKey
	}
	var u User
	err := c.do(ctx, http.MethodPost, "/v1/users", "", body, &u)
	return u, err
}

func (c *Client) Me(ctx context.Context, token string) (User, error) {
	var u User
	err := c.do(ctx, http.MethodGet, "/v1/me", token, nil, &u)
	return u, err
}

// UpdateMeInput is the PUT /v1/me body. Description is a pointer so a caller can
// send "" to clear the about text (distinct from "leave unchanged" = nil).
type UpdateMeInput struct {
	Description     *string `json:"description,omitempty"`
	CurrentPassword string  `json:"current_password,omitempty"`
	Password        string  `json:"password,omitempty"`
}

func (c *Client) UpdateMe(ctx context.Context, token string, in UpdateMeInput) (User, error) {
	var u User
	err := c.do(ctx, http.MethodPut, "/v1/me", token, in, &u)
	return u, err
}

// LedgerEntry is one movement between two accounts, from GET /v1/ledger: a
// deposit, a withdrawal, a transfer, delivered value, and what settlement moved —
// a provider's payout, the operator's fee, an obligation (kernel §D4). An account's
// balance is the sum of its entries. A side is absent only where money crosses
// the rail; Reason names the transaction that caused the movement, or the
// obligation it settles.
type LedgerEntry struct {
	Amount      int64  `json:"amount"`
	Reason      string `json:"reason,omitempty"`
	ExternalKey string `json:"external_key,omitempty"`
	Operator    string `json:"operator,omitempty"`
	From        string `json:"from,omitempty"`
	To          string `json:"to,omitempty"`
	CreatedAt   string `json:"created_at"`
}

// Withdrawal is one payout of the caller's own credits. The id is minted by the
// caller, so a replay returns the same row and a double submit moves nothing.
// A withdrawal snapshots its destination when it is made: registering another
// address never redirects one already under way (kernel §D23).
type Withdrawal struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	Amount      int64  `json:"amount"`
	Credit      int64  `json:"credit"`
	Status      string `json:"status"`
	TxHash      string `json:"tx_hash,omitempty"`
	Reason      string `json:"reason,omitempty"`
	CreatedAt   string `json:"created_at"`
	FinalizedAt string `json:"finalized_at,omitempty"`
	Party       string `json:"party,omitempty"`
}

// Withdraw pays credits out to the address the caller registered. The id is the
// row's own: replaying it returns that row rather than paying twice.
func (c *Client) Withdraw(ctx context.Context, token, id string, amount int64, reason string) (Withdrawal, error) {
	body := map[string]any{"id": id, "amount": amount}
	if reason != "" {
		body["reason"] = reason
	}
	var wd Withdrawal
	err := c.do(ctx, http.MethodPost, "/v1/withdrawals", token, body, &wd)
	return wd, err
}

// ListWithdrawals lists the caller's own withdrawals and where each stands.
//
// It pages to exhaustion. Every kernel list endpoint pages (default 50, ceiling
// 200, §R6), withdrawals included — measured: with four rows, ?limit=2&offset=2
// answers the third and fourth. An unpaged read would silently lose a person's
// fifty-first withdrawal, and a wallet that hides money is worse than one that
// takes a second request to show it.
func (c *Client) ListWithdrawals(ctx context.Context, token string) ([]Withdrawal, error) {
	return listAll[Withdrawal](ctx, c, token, "/v1/withdrawals", nil)
}

// listAll reads a kernel list to exhaustion. Every list endpoint pages (default
// 50, ceiling 200, §R6), so an unpaged read silently loses the fifty-first row;
// this asks for full pages until a short one says the list is done. q carries
// the list's own filters and is not modified.
func listAll[T any](ctx context.Context, c *Client, token, path string, q url.Values) ([]T, error) {
	const page = 200 // server ceiling
	var out []T
	for offset := 0; ; offset += page {
		pq := url.Values{}
		for k, v := range q {
			pq[k] = v
		}
		pq.Set("limit", strconv.Itoa(page))
		if offset > 0 {
			pq.Set("offset", strconv.Itoa(offset))
		}
		var batch []T
		if err := c.do(ctx, http.MethodGet, path+"?"+pq.Encode(), token, nil, &batch); err != nil {
			return out, err
		}
		out = append(out, batch...)
		if len(batch) < page {
			return out, nil
		}
	}
}

// AddressResult is the outcome of registering a payout address: the canonical
// form the kernel stored, and how many payments already received from that
// sender it attributed to the caller.
type AddressResult struct {
	Address    string `json:"blockchain_address"`
	Attributed int    `json:"attributed"`
}

// RegisterAddress proves control of a payout address. The signature is over the
// kernel's own registration message, produced by the wallet holding the address —
// the kernel credits the finalized sender, never whoever reports a payment, so
// the proof is what makes a payment yours (kernel §D23). The account's password
// rides with it, as on a password change: a session alone never decides where
// withdrawals go, and a wrong one is 401 (kernel §D9).
func (c *Client) RegisterAddress(ctx context.Context, token, address, signature, currentPassword string) (AddressResult, error) {
	var out AddressResult
	err := c.do(ctx, http.MethodPut, "/v1/me/blockchain-address", token, map[string]string{
		"blockchain_address": address,
		"signature":          signature,
		"current_password":   currentPassword,
	}, &out)
	return out, err
}

// Ledger lists the caller's own balance movements (newest first), paginated.
func (c *Client) Ledger(ctx context.Context, token string, limit, offset int) ([]LedgerEntry, error) {
	q := url.Values{}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	if offset > 0 {
		q.Set("offset", strconv.Itoa(offset))
	}
	path := "/v1/ledger"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	var out []LedgerEntry
	err := c.do(ctx, http.MethodGet, path, token, nil, &out)
	return out, err
}

// ---- actions ----

// ListActions reads the catalog (GET /v1/actions), every page: the caller's
// own actions in every state, every other live action it may call, and, for a
// session, the actions discovered on peers (marked Discovered). owner, when
// set, narrows it to that owner's rows. Rows come in title order.
//
// The kernel pages after scoping (limit ceiling 200) and promises only that no
// row is skipped or repeated, not that a page is full, so the read continues
// by offset until a page comes back empty.
func (c *Client) ListActions(ctx context.Context, token, owner string) ([]Action, error) {
	const page = 200 // server ceiling
	var out []Action
	for off := 0; ; off += page {
		batch, err := c.actionsPage(ctx, token, owner, page, off)
		if err != nil {
			return out, err
		}
		if len(batch) == 0 {
			return out, nil
		}
		out = append(out, batch...)
	}
}

// actionsPage fetches one page of the catalog (GET /v1/actions?…&limit=&offset=).
func (c *Client) actionsPage(ctx context.Context, token, owner string, limit, offset int) ([]Action, error) {
	q := url.Values{}
	if owner != "" {
		q.Set("owner", owner)
	}
	q.Set("limit", strconv.Itoa(limit))
	if offset > 0 {
		q.Set("offset", strconv.Itoa(offset))
	}
	var a []Action
	err := c.do(ctx, http.MethodGet, "/v1/actions?"+q.Encode(), token, nil, &a)
	return a, err
}

// ResolveRef resolves one reference exactly as a call would (GET /v1/actions?ref=):
// the action named, else that path's index child. A kernel-qualified reference
// (owner@kernel/name) is resolved WITH ITS HOME KERNEL over federation, which
// caches a local proxy — so a discovered action becomes an ordinary priced row
// with a live quote, at the cost of one round trip and no spend. The kernel
// answers with 0 or 1 rows, so a miss is an empty list rather than an error.
func (c *Client) ResolveRef(ctx context.Context, token, ref string) (Action, bool, error) {
	var out []Action
	if err := c.do(ctx, http.MethodGet, "/v1/actions?ref="+url.QueryEscape(ref), token, nil, &out); err != nil {
		return Action{}, false, err
	}
	if len(out) == 0 {
		return Action{}, false, nil
	}
	return out[0], true, nil
}

func (c *Client) GetAction(ctx context.Context, token, id string) (Action, error) {
	var a Action
	err := c.do(ctx, http.MethodGet, "/v1/actions/"+url.PathEscape(id), token, nil, &a)
	return a, err
}

func (c *Client) CreateAction(ctx context.Context, token string, in CreateActionInput) (Action, error) {
	var a Action
	err := c.do(ctx, http.MethodPost, "/v1/actions", token, in, &a)
	return a, err
}

// A mutation names a TARGET: an action id, which is exactly that row, or
// owner/path, which is the action at that path and every action beneath it
// (bob/mail reaches bob/mail/send, never bob/mailer). The kernel returns the
// rows it wrote, in name order, because one target may name a whole application
// — so the caller re-renders from the answer rather than predicting it.
//
// Visibility, price and auth apply to a subtree; a description, a schema or a
// source needs a target that resolves to one action, and the kernel refuses the
// rest. A target matching nothing is a 404.
func (c *Client) UpdateAction(ctx context.Context, token, target string, in UpdateActionInput) ([]Action, error) {
	in.Target = target
	var a []Action
	err := c.do(ctx, http.MethodPut, "/v1/actions", token, in, &a)
	return a, err
}

func (c *Client) EnableAction(ctx context.Context, token, target string) ([]Action, error) {
	return c.actionLifecycle(ctx, token, "/v1/actions/enable", target)
}

func (c *Client) DisableAction(ctx context.Context, token, target string) ([]Action, error) {
	return c.actionLifecycle(ctx, token, "/v1/actions/disable", target)
}

func (c *Client) actionLifecycle(ctx context.Context, token, path, target string) ([]Action, error) {
	var a []Action
	err := c.do(ctx, http.MethodPost, path, token, map[string]string{"target": target}, &a)
	return a, err
}

// DeleteAction soft-deletes a target, keeping its history.
func (c *Client) DeleteAction(ctx context.Context, token, target string) ([]Action, error) {
	var a []Action
	err := c.do(ctx, http.MethodDelete, "/v1/actions?target="+url.QueryEscape(target), token, nil, &a)
	return a, err
}

// ImportOpenAPI installs one OpenAPI document as the application at name: one
// action per representable operation, and the operation keyed `index` becomes
// its root. The URL is given once — a later import by name alone re-reads the
// recorded document and reconciles it, keeping each action's id, history,
// credentials and any price the owner set. There is no unimport: an application
// is switched off or removed with the ordinary verbs over its path.
// auth is the upstream credential configuration for the WHOLE application, set
// at import (API.md: `POST /v1/actions/import {name[, spec_url, auth]}`). Without
// it an imported application that needs a credential cannot be given one from
// here at all, since auth is write-only and every later `PUT` would have to name
// the subtree by hand. Nil sends no auth, which is the ordinary case.
func (c *Client) ImportOpenAPI(ctx context.Context, token, name, specURL string, auth json.RawMessage) (ImportResult, error) {
	body := map[string]any{"name": name}
	if specURL != "" {
		body["spec_url"] = specURL
	}
	if len(auth) > 0 {
		body["auth"] = auth
	}
	var r ImportResult
	err := c.do(ctx, http.MethodPost, "/v1/actions/import", token, body, &r)
	return r, err
}

// ActionRatings lists an action's ratings (newest first). limit>0 fetches a
// single page of that size at offset; limit==0 fetches the FULL set (paging with
// the ceiling) so the UI can slice its own pages — the kernel would otherwise
// truncate at its default of 50.
func (c *Client) ActionRatings(ctx context.Context, token, id string, limit, offset int) ([]Rating, error) {
	if limit > 0 {
		return c.ratingsPage(ctx, token, id, limit, offset)
	}
	const page = 200 // server ceiling
	var out []Rating
	for off := 0; ; off += page {
		batch, err := c.ratingsPage(ctx, token, id, page, off)
		if err != nil {
			return out, err
		}
		out = append(out, batch...)
		if len(batch) < page {
			return out, nil
		}
	}
}

func (c *Client) ratingsPage(ctx context.Context, token, id string, limit, offset int) ([]Rating, error) {
	q := url.Values{}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	if offset > 0 {
		q.Set("offset", strconv.Itoa(offset))
	}
	path := "/v1/actions/" + url.PathEscape(id) + "/ratings"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	var r []Rating
	err := c.do(ctx, http.MethodGet, path, token, nil, &r)
	return r, err
}

// ---- processes ----

// ListProcesses lists the caller's processes to exhaustion: the open ones, and
// the closed ones too when all is set — the kernel answers only what is open
// unless asked (?all=1).
func (c *Client) ListProcesses(ctx context.Context, token string, all bool) ([]Process, error) {
	q := url.Values{}
	if all {
		q.Set("all", "1")
	}
	return listAll[Process](ctx, c, token, "/v1/processes", q)
}

func (c *Client) GetProcess(ctx context.Context, token, id string) (Process, error) {
	var p Process
	err := c.do(ctx, http.MethodGet, "/v1/processes/"+url.PathEscape(id), token, nil, &p)
	return p, err
}

func (c *Client) EndProcess(ctx context.Context, token, id string) error {
	return c.do(ctx, http.MethodPost, "/v1/processes/"+url.PathEscape(id)+"/end", token, nil, nil)
}

// ---- run ----

// Run executes an action via POST /v1/run. The kernel atomically creates a
// process funded with exactly action.price, issues the call, and closes the
// process when the root call returns and no tasks remain outstanding.
// Run executes an action. quoteHash, when non-empty, PINS the terms the caller
// was shown (kernel §4): the kernel refuses with terms_changed before locking
// any funds if the price, description or schemas have moved since, so a
// displayed price is binding rather than advisory. Empty leaves the older
// behaviour — the call runs at whatever the terms now are.
func (c *Client) Run(ctx context.Context, token, action string, args json.RawMessage, quoteHash string) (CallReply, error) {
	req := RunRequest{Action: action, Args: emptyArgs(args), QuoteHash: quoteHash}
	var r CallReply
	err := c.do(ctx, http.MethodPost, "/v1/run", token, req, &r)
	return r, err
}

// ---- transactions ----

// ListTransactions lists the caller's transactions (GET /v1/transactions),
// optionally scoped to a process. limit>0 returns a single server page of that
// size (newest first). limit==0 returns the FULL set: the kernel pages every list
// endpoint with a default of 50 (ceiling 200, §R6), so an unpaged call would
// silently truncate — earnings, the full ledger, and process views need all rows,
// so we page through with offset.
func (c *Client) ListTransactions(ctx context.Context, token, processID string, limit int) ([]Transaction, error) {
	if limit > 0 {
		return c.txPage(ctx, token, processID, limit, 0)
	}
	const page = 200 // server ceiling
	var all []Transaction
	for offset := 0; ; offset += page {
		batch, err := c.txPage(ctx, token, processID, page, offset)
		if err != nil {
			return all, err
		}
		all = append(all, batch...)
		if len(batch) < page {
			return all, nil
		}
	}
}

// txPage fetches one page of transactions (GET /v1/transactions?limit=&offset=).
func (c *Client) txPage(ctx context.Context, token, processID string, limit, offset int) ([]Transaction, error) {
	q := url.Values{}
	if processID != "" {
		q.Set("process_id", processID)
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	if offset > 0 {
		q.Set("offset", strconv.Itoa(offset))
	}
	path := "/v1/transactions"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	var t []Transaction
	err := c.do(ctx, http.MethodGet, path, token, nil, &t)
	return t, err
}

func (c *Client) GetTransaction(ctx context.Context, token, id string) (Transaction, error) {
	var t Transaction
	err := c.do(ctx, http.MethodGet, "/v1/transactions/"+url.PathEscape(id), token, nil, &t)
	return t, err
}

func (c *Client) ReceiptVerification(ctx context.Context, token, id string) (ReceiptVerification, error) {
	var v ReceiptVerification
	err := c.do(ctx, http.MethodGet, "/v1/transactions/"+url.PathEscape(id)+"/receipt-verification", token, nil, &v)
	return v, err
}

// RateTransaction submits a 0/1 rating with an optional note for a transaction.
func (c *Client) RateTransaction(ctx context.Context, token, id string, rating int, note string) error {
	body := struct {
		Rating int    `json:"rating"`
		Note   string `json:"note,omitempty"`
	}{Rating: rating, Note: note}
	return c.do(ctx, http.MethodPost, "/v1/transactions/"+url.PathEscape(id)+"/rate", token, body, nil)
}

// ---- tasks ----

// ListTasks lists the tasks the caller may see, to exhaustion: those with the
// given status, or every task when status is empty — the kernel answers only
// open ones (waiting, running) unless asked (?all=1).
func (c *Client) ListTasks(ctx context.Context, token, status string) ([]Task, error) {
	q := url.Values{}
	if status != "" {
		q.Set("status", status)
	} else {
		q.Set("all", "1")
	}
	return listAll[Task](ctx, c, token, "/v1/tasks", q)
}

func (c *Client) GetTask(ctx context.Context, token, id string) (Task, error) {
	var t Task
	err := c.do(ctx, http.MethodGet, "/v1/tasks/"+url.PathEscape(id), token, nil, &t)
	return t, err
}

// CreateTaskInput is the body for POST /v1/tasks: a waiting task is a funded
// continuation bound to the funding trace, addressed to a required caller.
// Action is the completion target as an address or a raw id, exactly as
// /v1/run takes it.
type CreateTaskInput struct {
	TraceID        string          `json:"trace_id"`
	Action         string          `json:"action"`
	RequiredCaller string          `json:"required_caller"`
	PartialArgs    json.RawMessage `json:"partial_args"`
}

// CreateTask creates a waiting task (POST /v1/tasks → 201).
func (c *Client) CreateTask(ctx context.Context, token string, in CreateTaskInput) (Task, error) {
	if len(in.PartialArgs) == 0 {
		in.PartialArgs = json.RawMessage(`{}`)
	}
	var t Task
	err := c.do(ctx, http.MethodPost, "/v1/tasks", token, in, &t)
	return t, err
}

// Capability-callback headers the kernel injects into every kind=http dispatch
// (juicedocs §9 out-of-kernel composition): the executing action composes by calling
// back with the capability.
const (
	CapabilityHeader = "X-Juice-Capability" // <trace_id>.<sig> — the trace to compose on
	CallbackHeader   = "X-Juice-Callback"   // base URL to call the composition API back on
)

// CallbackCreateTask creates a task via the trace-capability the kernel injected into
// a kind=http dispatch, rather than via a session token. It POSTs to
// {callbackBase}/v1/tasks carrying `capability` in the X-Juice-Capability header (NOT a
// bearer) with NO trace_id — the capability IS the trace. Used by the chat/outbox
// handler to create the sentinel task that holds the outbox seed open. Valid only while
// the dispatch's trace is unsettled (call it during the dispatch, before returning).
func (c *Client) CallbackCreateTask(ctx context.Context, callbackBase, capability string, in CreateTaskInput) (Task, error) {
	if len(in.PartialArgs) == 0 {
		in.PartialArgs = json.RawMessage(`{}`)
	}
	body, err := json.Marshal(map[string]any{
		"action":          in.Action,
		"required_caller": in.RequiredCaller,
		"partial_args":    in.PartialArgs,
	})
	if err != nil {
		return Task{}, fmt.Errorf("marshal request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(callbackBase, "/")+"/v1/tasks", bytes.NewReader(body))
	if err != nil {
		return Task{}, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(CapabilityHeader, capability)
	resp, err := c.http.Do(req)
	if err != nil {
		return Task{}, &KernelError{Kind: KindUnavailable, KernelMessage: "callback: " + err.Error()}
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return Task{}, fmt.Errorf("read response: %w", err)
	}
	if cerr := classify(resp.StatusCode, raw); cerr != nil {
		return Task{}, cerr
	}
	var t Task
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &t); err != nil {
			return Task{}, fmt.Errorf("decode response: %w", err)
		}
	}
	return t, nil
}

// CompleteTask completes a waiting task. args is always sent in the body (the
// kernel returns ErrInvalidInput when the field is absent); nil/empty is
// normalized to {}.
func (c *Client) CompleteTask(ctx context.Context, token, id string, args json.RawMessage) (TaskCompleteReply, error) {
	body := struct {
		Args json.RawMessage `json:"args"`
	}{Args: emptyArgs(args)}
	var r TaskCompleteReply
	err := c.do(ctx, http.MethodPost, "/v1/tasks/"+url.PathEscape(id)+"/complete", token, body, &r)
	return r, err
}

// CancelTask declines a waiting task: it is cancelled and its reserved price
// returns to the payer. The kernel accepts it from the task's required caller or
// its process owner, and refuses anyone else and any task no longer waiting.
func (c *Client) CancelTask(ctx context.Context, token, id string) (Task, error) {
	var t Task
	err := c.do(ctx, http.MethodPost, "/v1/tasks/"+url.PathEscape(id)+"/cancel", token, nil, &t)
	return t, err
}

// ---- request id propagation ----

type ctxKey int

const requestIDKey ctxKey = 0

// WithRequestID returns a context carrying a request id that the client
// forwards to the kernel as X-Request-Id, tying BFF and kernel logs together
// (R8.4).
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey, id)
}

// RequestIDFromContext returns the request id stored by WithRequestID, or "".
func RequestIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey).(string)
	return id
}
