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
// Names are bare (requirements.md §4) and this client never rewrites one: "@"
// separates a kernel, so a sigiled handle, ref, or selector is input the kernel
// rejects (verified live 2026-08-05) and that typed error is the validation.
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
	ID          string       `json:"id"`
	Handle      string       `json:"handle"`
	Description string       `json:"description"`
	Available   int64        `json:"available"`
	Locked      int64        `json:"locked"`
	Connectors  []Connector  `json:"connectors"`  // directory-grouped consent tree (§8)
	Connections []Connection `json:"connections"` // full upstream-account inventory
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
	Action      string   `json:"action"` // owner/name of the granted action
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

// Action carries both id (for management) and action ("owner/name", for
// calling) per R8. For kind=http, Source is a JSON-encoded descriptor string
// (present on single reads, omitted on list reads); do not interpret it for
// display. Instead, kind=http reads carry a decomposed HTTP object (absent for
// other kinds), which is the structured view the UI renders. Identity is by
// handle: the kernel returns owner_handle (not a user id) on every read.
type Action struct {
	ID          string `json:"id"`
	Action      string `json:"action"`
	OwnerHandle string `json:"owner_handle"`
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	Active      bool   `json:"active"`
	// Visibility is the kernel's three-way caller-scope: "private" (owner only),
	// "local" (any local caller of this kernel, never peers), or "public" (anyone,
	// and the only value served in manifests/gossip). It replaces the old boolean
	// `public`. Empty on a legacy kernel; treat empty as "private" (the default).
	Visibility   string          `json:"visibility"`
	Price        int64           `json:"price"`
	Description  string          `json:"description"`
	InputSchema  json.RawMessage `json:"input_schema,omitempty"`
	OutputSchema json.RawMessage `json:"output_schema,omitempty"`
	Source       json.RawMessage `json:"source,omitempty"`
	HTTP         *HTTPSpec       `json:"http,omitempty"`
	// AuthScheme is the upstream auth scheme (e.g. "delegated_bearer"), and
	// RequiresGrant reports whether a per-caller grant is needed to run the action.
	// The kernel exposes these even though the auth config/secrets stay write-only
	// (R9), so a client can tell an action needs a grant, and of which kind.
	AuthScheme    string `json:"auth_scheme,omitempty"`
	RequiresGrant bool   `json:"requires_grant,omitempty"`
	// Effect names a privileged execution effect from the action's signed
	// contract — "transfer" on a value-bearing action (verified live 2026-08-03
	// on sys/transfer), empty on every ordinary action. It is the ONLY signal
	// that completing a step on this action spends the completer's own money
	// (requirements.md §6.3): never key on the action's name.
	Effect string `json:"effect,omitempty"`
	// QuoteHash fingerprints the terms quoted here — action id, effect,
	// description, both schemas, and price (kernel §4, v0.12.15). Pin it on a run
	// and a call whose terms moved is refused with terms_changed BEFORE funds are
	// locked, at zero charge, so a displayed price is binding rather than
	// advisory. Carried on action reads, listings, and sys/lookup rows; because
	// it hashes the STABLE id, a discovered hit and the proxy it resolves to
	// hash identically, which is what lets a first cross-kernel call bind.
	QuoteHash string `json:"quote_hash,omitempty"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// IsTransfer reports whether this action bears the value-transfer effect —
// the payment-step and wallet-send signal (requirements.md §6.3, §6.5).
func (a Action) IsTransfer() bool { return a.Effect == "transfer" }

// Ref returns "owner/name" for this action (a remote proxy resolves as
// "owner@kernel/name" in the kernel-computed field). The kernel populates the
// computed Action field, but we derive from OwnerHandle + Name so a partial
// record (e.g. a lookup result without the computed field) still resolves.
func (a Action) Ref() string {
	if a.Action != "" {
		return a.Action
	}
	return a.OwnerHandle + "/" + a.Name
}

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
	Kind         string          `json:"kind"`
	Source       string          `json:"source,omitempty"`
	Method       string          `json:"method,omitempty"`
	Params       []HTTPParam     `json:"params,omitempty"`
	Description  string          `json:"description,omitempty"`
	Price        int64           `json:"price"`
	InputSchema  json.RawMessage `json:"input_schema,omitempty"`
	OutputSchema json.RawMessage `json:"output_schema,omitempty"`
	Auth         json.RawMessage `json:"auth,omitempty"`
}

// UpdateActionInput is the body for PUT /v1/actions/{id}. All fields optional.
type UpdateActionInput struct {
	Price        *int64          `json:"price,omitempty"`
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
}

// Process is a funded computation lifetime. Processes are auto-created by run.
// The kernel identifies the owner by handle and flags a process parked on a
// remote receipt (funds locked on a possibly-offline peer) via AwaitingReceipt.
type Process struct {
	ID                   string `json:"id"`
	OwnerHandle          string `json:"owner_handle"`
	Available            int64  `json:"available"`
	Locked               int64  `json:"locked"`
	Status               string `json:"status"`
	AwaitingReceipt      bool   `json:"awaiting_receipt"`
	AwaitingReceiptSince string `json:"awaiting_receipt_since,omitempty"`
	CreatedAt            string `json:"created_at"`
	EndedAt              string `json:"ended_at"`
}

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
// identified by handle (owner=payer, caller=requester, target=payee); the kernel
// never returns raw user ids.
type Transaction struct {
	ID            string             `json:"id"`
	ProcessID     string             `json:"process_id"`
	TraceID       string             `json:"trace_id"`
	ParentTraceID string             `json:"parent_trace_id"`
	OwnerHandle   string             `json:"owner_handle"`
	CallerHandle  string             `json:"caller_handle"`
	TargetHandle  string             `json:"target_handle"`
	ActionID      string             `json:"action_id"`
	ActionName    string             `json:"action_name"`
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
}

// ReceiptVerification is the kernel's immutable receipt-verification report.
// The exact per-check object is kernel-owned, so the BFF renders it as JSON.
type ReceiptVerification struct {
	Valid  bool            `json:"valid"`
	Checks json.RawMessage `json:"checks"`
}

// Stats is GET /v1/stats/{action_id}.
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

// Step is a suspended computation boundary: a pre-baked future Call that a
// designated caller completes by supplying the remaining input. The required
// caller is identified by handle; the computed action ("owner/name") is present
// on both list and single reads. WaitingOnPeer flags a waiting step whose caller
// is a federation peer (work parked on a possibly-offline kernel).
type Step struct {
	ID                   string          `json:"id"`
	ParentTraceID        string          `json:"parent_trace_id"`
	RequiredCallerHandle string          `json:"required_caller_handle"`
	OwnerHandle          string          `json:"owner_handle"` // process owner/payer; chat doorbells carry chat
	ActionID             string          `json:"action_id"`
	Action               string          `json:"action"`
	Price                int64           `json:"price"`
	PartialArgs          json.RawMessage `json:"partial_args"`
	Status               string          `json:"status"` // waiting|running|done|cancelled
	WaitingOnPeer        bool            `json:"waiting_on_peer"`
	TxID                 string          `json:"tx_id"`
	CreatedAt            string          `json:"created_at"`
	CreatedBy            string          `json:"created_by"` // creating action owner/name (from the parent trace)
	// AllowedInput is the kernel-derived completion schema on a waiting step
	// (action.input_schema minus the keys sealed in partial_args), served so the
	// completer never needs to read the target action — which may be private and
	// 403 (verified live 2026-08-03; an approval-only step serves an empty
	// object schema). Absent on non-waiting steps.
	AllowedInput json.RawMessage `json:"allowed_input,omitempty"`
}

// Ref returns "owner/name" for this step's next action.
func (s Step) Ref() string { return s.Action }

// StepCompleteReply is the response of POST /v1/steps/{id}/complete.
// The kernel also returns receipt_id and gross but they are not used by the BFF.
type StepCompleteReply struct {
	Result  json.RawMessage `json:"result"`
	TxID    string          `json:"tx_id"`
	TraceID string          `json:"trace_id"`
	StepID  string          `json:"step_id"`
}

// ImportResult is the response of POST /v1/actions/import. Go's JSON decoding is
// case-insensitive, so these tags match the kernel's capitalized keys.
type ImportResult struct {
	Created     []Action `json:"created"`
	Updated     []Action `json:"updated"`
	Unchanged   []Action `json:"unchanged"`
	Deactivated []Action `json:"deactivated"`
}

// Health is GET /health (open): the identity banner a client pins before it
// trusts a server (API C13). Beyond naming the kernel it fixes the money unit —
// Decimals is the world's base-unit scale and Network its name — and, on a world
// whose money has addresses, RailAddress is where this kernel is paid.
type Health struct {
	Status        string `json:"status"`
	Handle        string `json:"handle"`
	PublicKey     string `json:"public_key"`
	Network       string `json:"network"`
	NetworkDigest string `json:"network_digest"`
	Decimals      int    `json:"decimals"`
	RailAddress   string `json:"rail_address"`
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

// PlanGrants returns the consent plan for a selector (owner, owner/path, or a
// full owner/name). Authenticated as the caller whose coverage is planned.
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
func (c *Client) CompleteGrant(ctx context.Context, token, state, code string) ([]string, error) {
	var resp struct {
		Actions []string `json:"actions"`
	}
	err := c.do(ctx, http.MethodPost, "/v1/grants/complete", token, map[string]string{"state": state, "code": code}, &resp)
	return resp.Actions, err
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

// LedgerEntry is one direct balance movement (deposit, withdrawal, or transfer)
// from GET /v1/ledger. from_handle is absent on a deposit, to_handle on a
// withdrawal; operator_handle is sys for admin ops and the sender for a transfer.
type LedgerEntry struct {
	ID             string `json:"id"`
	Amount         int64  `json:"amount"`
	Reason         string `json:"reason,omitempty"`
	ExternalKey    string `json:"external_key,omitempty"`
	OperatorHandle string `json:"operator_handle,omitempty"`
	FromHandle     string `json:"from_handle,omitempty"`
	ToHandle       string `json:"to_handle,omitempty"`
	CreatedAt      string `json:"created_at"`
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

// The bare POST /v1/transfers endpoint is deliberately not wrapped here: the
// UI's one send-money surface runs the kernel's sys/transfer action through
// Run, so every user-facing money movement flows through the same receipted
// call machinery (requirements.md §6.5).

// ---- actions ----

// ListActions lists actions. owner and name are optional filters ("" to omit).
// The kernel returns active rows by default (unauthenticated → active public;
// authenticated → + own active; superuser → all owners' active); pass all=true
// (?all=1) to include the caller's own inactive/private rows — required to show
// a user their inactive drafts.
//
// limit>0 returns a single server page of that size at the given offset (newest
// first). limit==0 returns the FULL set: the kernel pages every list endpoint
// with a default of 50 (ceiling 200, §R6), so an unpaged call would silently
// truncate — the workshop and chat's own-action reconciliation need every row,
// so we page through with offset.
func (c *Client) ListActions(ctx context.Context, token, owner, name string, all bool, limit, offset int) ([]Action, error) {
	if limit > 0 {
		return c.actionsPage(ctx, token, owner, name, all, limit, offset)
	}
	const page = 200 // server ceiling
	var out []Action
	for off := 0; ; off += page {
		batch, err := c.actionsPage(ctx, token, owner, name, all, page, off)
		if err != nil {
			return out, err
		}
		out = append(out, batch...)
		if len(batch) < page {
			return out, nil
		}
	}
}

// actionsPage fetches one page of actions (GET /v1/actions?…&limit=&offset=).
func (c *Client) actionsPage(ctx context.Context, token, owner, name string, all bool, limit, offset int) ([]Action, error) {
	q := url.Values{}
	if owner != "" {
		q.Set("owner", owner)
	}
	if name != "" {
		q.Set("name", name)
	}
	if all {
		q.Set("all", "1") // include the caller's own inactive/private rows (else active-only)
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	if offset > 0 {
		q.Set("offset", strconv.Itoa(offset))
	}
	path := "/v1/actions"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	var a []Action
	err := c.do(ctx, http.MethodGet, path, token, nil, &a)
	return a, err
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

func (c *Client) UpdateAction(ctx context.Context, token, id string, in UpdateActionInput) (Action, error) {
	var a Action
	err := c.do(ctx, http.MethodPut, "/v1/actions/"+url.PathEscape(id), token, in, &a)
	return a, err
}

func (c *Client) EnableAction(ctx context.Context, token, id string) error {
	return c.do(ctx, http.MethodPost, "/v1/actions/"+url.PathEscape(id)+"/enable", token, nil, nil)
}

func (c *Client) DisableAction(ctx context.Context, token, id string) error {
	return c.do(ctx, http.MethodPost, "/v1/actions/"+url.PathEscape(id)+"/disable", token, nil, nil)
}

func (c *Client) DeleteAction(ctx context.Context, token, id string) error {
	return c.do(ctx, http.MethodDelete, "/v1/actions/"+url.PathEscape(id), token, nil, nil)
}

func (c *Client) ImportOpenAPI(ctx context.Context, token, specURL string) (ImportResult, error) {
	var r ImportResult
	err := c.do(ctx, http.MethodPost, "/v1/actions/import", token, map[string]string{"spec_url": specURL}, &r)
	return r, err
}

func (c *Client) UnimportOpenAPI(ctx context.Context, token, specURL, name string) ([]Action, error) {
	body := map[string]string{"spec_url": specURL}
	if name != "" {
		body["name"] = name
	}
	var a []Action
	err := c.do(ctx, http.MethodPost, "/v1/actions/unimport", token, body, &a)
	return a, err
}

func (c *Client) ActionStats(ctx context.Context, token, actionID string) (Stats, error) {
	var s Stats
	err := c.do(ctx, http.MethodGet, "/v1/stats/"+url.PathEscape(actionID), token, nil, &s)
	return s, err
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

func (c *Client) ListProcesses(ctx context.Context, token string) ([]Process, error) {
	var p []Process
	err := c.do(ctx, http.MethodGet, "/v1/processes", token, nil, &p)
	return p, err
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
// process when the root call returns and no steps remain outstanding.
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

// ---- steps ----

func (c *Client) ListSteps(ctx context.Context, token, processID, status string) ([]Step, error) {
	q := url.Values{}
	if processID != "" {
		q.Set("process_id", processID)
	}
	if status != "" {
		q.Set("status", status)
	}
	path := "/v1/steps"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	var s []Step
	err := c.do(ctx, http.MethodGet, path, token, nil, &s)
	return s, err
}

func (c *Client) GetStep(ctx context.Context, token, id string) (Step, error) {
	var s Step
	err := c.do(ctx, http.MethodGet, "/v1/steps/"+url.PathEscape(id), token, nil, &s)
	return s, err
}

// CreateStepInput is the body for POST /v1/steps: a waiting step is a funded
// continuation bound to the funding trace, addressed to a required caller.
// Action is the completion target as "owner/name" or a raw id, exactly as
// /v1/run takes it — the kernel rejects the older action_id field outright
// (verified live 2026-08-05: "action is required").
type CreateStepInput struct {
	TraceID        string          `json:"trace_id"`
	Action         string          `json:"action"`
	RequiredCaller string          `json:"required_caller"`
	PartialArgs    json.RawMessage `json:"partial_args"`
}

// CreateStep creates a waiting step (POST /v1/steps → 201).
func (c *Client) CreateStep(ctx context.Context, token string, in CreateStepInput) (Step, error) {
	if len(in.PartialArgs) == 0 {
		in.PartialArgs = json.RawMessage(`{}`)
	}
	var s Step
	err := c.do(ctx, http.MethodPost, "/v1/steps", token, in, &s)
	return s, err
}

// Capability-callback headers the kernel injects into every kind=http dispatch
// (juicedocs §9 out-of-kernel composition): the executing action composes by calling
// back with the capability.
const (
	CapabilityHeader = "X-Juice-Capability" // <trace_id>.<sig> — the trace to compose on
	CallbackHeader   = "X-Juice-Callback"   // base URL to call the composition API back on
)

// CallbackCreateStep creates a step via the trace-capability the kernel injected into
// a kind=http dispatch, rather than via a session token. It POSTs to
// {callbackBase}/v1/steps carrying `capability` in the X-Juice-Capability header (NOT a
// bearer) with NO trace_id — the capability IS the trace. Used by the chat/outbox
// handler to create the sentinel step that holds the outbox seed open. Valid only while
// the dispatch's trace is unsettled (call it during the dispatch, before returning).
func (c *Client) CallbackCreateStep(ctx context.Context, callbackBase, capability string, in CreateStepInput) (Step, error) {
	if len(in.PartialArgs) == 0 {
		in.PartialArgs = json.RawMessage(`{}`)
	}
	body, err := json.Marshal(map[string]any{
		"action":          in.Action,
		"required_caller": in.RequiredCaller,
		"partial_args":    in.PartialArgs,
	})
	if err != nil {
		return Step{}, fmt.Errorf("marshal request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(callbackBase, "/")+"/v1/steps", bytes.NewReader(body))
	if err != nil {
		return Step{}, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(CapabilityHeader, capability)
	resp, err := c.http.Do(req)
	if err != nil {
		return Step{}, &KernelError{Kind: KindUnavailable, KernelMessage: "callback: " + err.Error()}
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return Step{}, fmt.Errorf("read response: %w", err)
	}
	if cerr := classify(resp.StatusCode, raw); cerr != nil {
		return Step{}, cerr
	}
	var s Step
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &s); err != nil {
			return Step{}, fmt.Errorf("decode response: %w", err)
		}
	}
	return s, nil
}

// CompleteStep completes a waiting step. args is always sent in the body (the
// kernel returns ErrInvalidInput when the field is absent); nil/empty is
// normalized to {}.
func (c *Client) CompleteStep(ctx context.Context, token, id string, args json.RawMessage) (StepCompleteReply, error) {
	body := struct {
		Args json.RawMessage `json:"args"`
	}{Args: emptyArgs(args)}
	var r StepCompleteReply
	err := c.do(ctx, http.MethodPost, "/v1/steps/"+url.PathEscape(id)+"/complete", token, body, &r)
	return r, err
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
