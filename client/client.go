package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
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
// integer credits. Verified live: id, handle, email, available, locked.
type User struct {
	ID        string `json:"id"`
	Handle    string `json:"handle"`
	Email     string `json:"email"`
	Available int64  `json:"available"`
	Locked    int64  `json:"locked"`
}

// Action carries both id (for management) and action ("@owner/name", for
// calling) per R8. source is RawMessage because it is a URL string for kind=http
// but an object for OpenAPI-imported actions.
type Action struct {
	ID           string          `json:"id"`
	Action       string          `json:"action"`
	OwnerUserID  string          `json:"owner_user_id"`
	OwnerHandle  string          `json:"owner_handle"`
	Name         string          `json:"name"`
	Kind         string          `json:"kind"`
	Active       bool            `json:"active"`
	Public       bool            `json:"public"`
	Price        int64           `json:"price"`
	Description  string          `json:"description"`
	InputSchema  json.RawMessage `json:"input_schema,omitempty"`
	OutputSchema json.RawMessage `json:"output_schema,omitempty"`
	Source       json.RawMessage `json:"source,omitempty"`
	CreatedAt    string          `json:"created_at"`
	UpdatedAt    string          `json:"updated_at"`
}

// Ref returns "@owner/name" for this action. The kernel's computed Action
// field is null on the live kernel (verified 2026-06-04), so we derive it
// from OwnerHandle (which already carries "@") and Name.
func (a Action) Ref() string { return a.OwnerHandle + "/" + a.Name }

// CreateActionInput is the body for POST /v1/actions. Schemas are inline JSON.
type CreateActionInput struct {
	Name         string          `json:"name"`
	Kind         string          `json:"kind"`
	Source       string          `json:"source,omitempty"`
	Description  string          `json:"description,omitempty"`
	Price        int64           `json:"price"`
	InputSchema  json.RawMessage `json:"input_schema,omitempty"`
	OutputSchema json.RawMessage `json:"output_schema,omitempty"`
}

// UpdateActionInput is the body for PUT /v1/actions/{id}. All fields optional.
type UpdateActionInput struct {
	Price        *int64          `json:"price,omitempty"`
	Description  *string         `json:"description,omitempty"`
	Source       *string         `json:"source,omitempty"`
	InputSchema  json.RawMessage `json:"input_schema,omitempty"`
	OutputSchema json.RawMessage `json:"output_schema,omitempty"`
}

// Process is a funded session. The kernel returns process_id on create but id on
// show (verified live); Ident unifies them.
type Process struct {
	ID          string `json:"id"`
	PID         string `json:"process_id"`
	OwnerUserID string `json:"owner_user_id"`
	Available   int64  `json:"available"`
	Locked      int64  `json:"locked"`
	Status      string `json:"status"`
	TraceID     string `json:"trace_id"`
	CreatedAt   string `json:"created_at"`
	EndedAt     string `json:"ended_at"`
}

// Ident returns the process identifier regardless of which field the kernel
// populated.
func (p Process) Ident() string {
	if p.ID != "" {
		return p.ID
	}
	return p.PID
}

// CallRequest is the body for POST /v1/call. Args defaults to {} (R4.7).
type CallRequest struct {
	ProcessID     string          `json:"process_id"`
	Action        string          `json:"action"`
	Args          json.RawMessage `json:"args"`
	ParentTraceID string          `json:"parent_trace_id,omitempty"`
}

// CallReply is the success response of POST /v1/call.
type CallReply struct {
	Result  json.RawMessage `json:"result"`
	TxID    string          `json:"tx_id"`
	TraceID string          `json:"trace_id"`
}

// Transaction is the settled record of a call. gross/net/fee are integer
// credits. result is null on failure; reason is set on failure.
type Transaction struct {
	ID            string          `json:"id"`
	ProcessID     string          `json:"process_id"`
	TraceID       string          `json:"trace_id"`
	ParentTraceID string          `json:"parent_trace_id"`
	OwnerUserID   string          `json:"owner_user_id"`
	SubjectUserID string          `json:"subject_user_id"`
	TargetUserID  string          `json:"target_user_id"`
	ActionID      string          `json:"action_id"`
	Args          json.RawMessage `json:"args"`
	Result        json.RawMessage `json:"result"`
	Status        string          `json:"status"`
	Gross         int64           `json:"gross"`
	Net           int64           `json:"net"`
	Fee           int64           `json:"fee"`
	Reason        string          `json:"reason"`
	StartedAt     string          `json:"started_at"`
	EndedAt       string          `json:"ended_at"`
}

// Stats is GET /v1/stats/{action_id}.
type Stats struct {
	ActionID    string  `json:"action_id"`
	Uses        int64   `json:"uses"`
	Successes   int64   `json:"successes"`
	Failures    int64   `json:"failures"`
	RatingCount int64   `json:"rating_count"`
	PriceMean   float64 `json:"price_mean"`
	LatencyMean float64 `json:"latency_mean"`
	RatingMean  float64 `json:"rating_mean"`
	LastUsedAt  string  `json:"last_used_at"`
}

// Rating is one entry of GET /v1/actions/{id}/ratings (list may be null).
type Rating struct {
	ID          string `json:"id"`
	RatedTxID   string `json:"rated_tx_id"`
	RaterUserID string `json:"rater_user_id"`
	Rating      int    `json:"rating"`
	CreatedAt   string `json:"created_at"`
}

// Listener subscribes a target action to a source user's named events. Both IDs
// are UUIDs on the wire (R6.7.1).
type Listener struct {
	ID             string `json:"id"`
	OwnerUserID    string `json:"owner_user_id"`
	SourceUserID   string `json:"source_user_id"`
	EventName      string `json:"event_name"`
	TargetActionID string `json:"target_action_id"`
	Active         bool   `json:"active"`
	CreatedAt      string `json:"created_at"`
}

// Event is a pending event for a listener.
type Event struct {
	ID             string          `json:"id"`
	ListenerID     string          `json:"listener_id"`
	Args           json.RawMessage `json:"args"`
	CausingTraceID string          `json:"causing_trace_id"`
	CreatedAt      string          `json:"created_at"`
}

// EmitResult is the response of POST /v1/events/emit.
type EmitResult struct {
	EventIDs []string `json:"event_ids"`
}

// ImportResult is the response of POST /v1/actions/import. Go's JSON decoding is
// case-insensitive, so these tags match the kernel's capitalized keys.
type ImportResult struct {
	Created     []Action `json:"created"`
	Updated     []Action `json:"updated"`
	Unchanged   []Action `json:"unchanged"`
	Deactivated []Action `json:"deactivated"`
}

// WellKnown is GET /.well-known/juice-kernel.json.
type WellKnown struct {
	BaseURL   string `json:"base_url"`
	Handle    string `json:"handle"`
	PublicKey string `json:"public_key"`
}

// Health is GET /health.
type Health struct {
	Status string `json:"status"`
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
		return fmt.Errorf("kernel request: %w", err)
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

func (c *Client) WellKnown(ctx context.Context) (WellKnown, error) {
	var w WellKnown
	err := c.do(ctx, http.MethodGet, "/.well-known/juice-kernel.json", "", nil, &w)
	return w, err
}

// ---- authentication ----

// Authorize performs the PKCE authorize step. We omit redirect_uri so the kernel
// answers 200 with {"redirect":"?code=..."} (verified live); the code is parsed
// out of that query. password and code_challenge (S256) are passed through.
func (c *Client) Authorize(ctx context.Context, handle, password, codeChallenge string) (string, error) {
	body := map[string]string{
		"handle":         normalizeHandle(handle),
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

// ---- users ----

func (c *Client) CreateUser(ctx context.Context, handle, email, password string) (User, error) {
	body := map[string]string{"handle": normalizeHandle(handle), "email": email, "password": password}
	var u User
	err := c.do(ctx, http.MethodPost, "/v1/users", "", body, &u)
	return u, err
}

func (c *Client) Me(ctx context.Context, token string) (User, error) {
	var u User
	err := c.do(ctx, http.MethodGet, "/v1/me", token, nil, &u)
	return u, err
}

// ---- actions ----

// ListActions lists public, active actions. owner and name are optional filters
// ("" to omit). Verified live: this returns only active+public actions, even for
// the authenticated owner; there is no HTTP surface for an owner's inactive
// actions (R10.3).
func (c *Client) ListActions(ctx context.Context, token, owner, name string) ([]Action, error) {
	q := url.Values{}
	if owner != "" {
		q.Set("owner", normalizeHandle(owner))
	}
	if name != "" {
		q.Set("name", name)
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

// GrantACL grants a permission to a subject user. subjectUserID MUST be a UUID:
// the kernel does not accept @handles here (verified live, R10.4).
func (c *Client) GrantACL(ctx context.Context, token, id, subjectUserID, permission string) error {
	body := map[string]string{"subject_user_id": subjectUserID, "permission": permission}
	return c.do(ctx, http.MethodPost, "/v1/actions/"+url.PathEscape(id)+"/acl", token, body, nil)
}

func (c *Client) RevokeACL(ctx context.Context, token, id, subjectID, permission string) error {
	path := "/v1/actions/" + url.PathEscape(id) + "/acl/" + url.PathEscape(subjectID) + "/" + url.PathEscape(permission)
	return c.do(ctx, http.MethodDelete, path, token, nil, nil)
}

func (c *Client) GrantAll(ctx context.Context, token, id string) error {
	return c.do(ctx, http.MethodPost, "/v1/actions/"+url.PathEscape(id)+"/grant-all", token, nil, nil)
}

func (c *Client) RevokeAll(ctx context.Context, token, id string) error {
	return c.do(ctx, http.MethodPost, "/v1/actions/"+url.PathEscape(id)+"/revoke-all", token, nil, nil)
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

func (c *Client) ActionRatings(ctx context.Context, token, id string) ([]Rating, error) {
	var r []Rating
	err := c.do(ctx, http.MethodGet, "/v1/actions/"+url.PathEscape(id)+"/ratings", token, nil, &r)
	return r, err
}

// ---- processes (funded sessions) ----

func (c *Client) StartProcess(ctx context.Context, token string, funds int64) (Process, error) {
	var p Process
	err := c.do(ctx, http.MethodPost, "/v1/processes", token, map[string]int64{"funds": funds}, &p)
	return p, err
}

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

func (c *Client) FundProcess(ctx context.Context, token, id string, funds int64) (Process, error) {
	var p Process
	err := c.do(ctx, http.MethodPost, "/v1/processes/"+url.PathEscape(id)+"/fund", token, map[string]int64{"funds": funds}, &p)
	return p, err
}

func (c *Client) EndProcess(ctx context.Context, token, id string) error {
	return c.do(ctx, http.MethodPost, "/v1/processes/"+url.PathEscape(id)+"/end", token, nil, nil)
}

// ---- call ----

func (c *Client) Call(ctx context.Context, token string, req CallRequest) (CallReply, error) {
	req.Args = emptyArgs(req.Args)
	var r CallReply
	err := c.do(ctx, http.MethodPost, "/v1/call", token, req, &r)
	return r, err
}

// ---- transactions ----

func (c *Client) ListTransactions(ctx context.Context, token, processID string) ([]Transaction, error) {
	path := "/v1/transactions"
	if processID != "" {
		path += "?" + url.Values{"process_id": {processID}}.Encode()
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

// RateTransaction submits a 0/1 rating for a transaction.
func (c *Client) RateTransaction(ctx context.Context, token, id string, rating int) error {
	return c.do(ctx, http.MethodPost, "/v1/transactions/"+url.PathEscape(id)+"/rate", token, map[string]int{"rating": rating}, nil)
}

// ---- listeners & events ----

func (c *Client) CreateListener(ctx context.Context, token, sourceUserID, eventName, targetActionID string) (Listener, error) {
	body := map[string]string{"source_user_id": sourceUserID, "event_name": eventName, "target_action_id": targetActionID}
	var l Listener
	err := c.do(ctx, http.MethodPost, "/v1/listeners", token, body, &l)
	return l, err
}

func (c *Client) ListListeners(ctx context.Context, token string) ([]Listener, error) {
	var l []Listener
	err := c.do(ctx, http.MethodGet, "/v1/listeners", token, nil, &l)
	return l, err
}

func (c *Client) GetListener(ctx context.Context, token, id string) (Listener, error) {
	var l Listener
	err := c.do(ctx, http.MethodGet, "/v1/listeners/"+url.PathEscape(id), token, nil, &l)
	return l, err
}

func (c *Client) DeleteListener(ctx context.Context, token, id string) error {
	return c.do(ctx, http.MethodDelete, "/v1/listeners/"+url.PathEscape(id), token, nil, nil)
}

// EmitEvent emits an event as the authenticated user. source_user_id is never
// sent (R4.7/C12). Args defaults to {}.
func (c *Client) EmitEvent(ctx context.Context, token, eventName string, args json.RawMessage) (EmitResult, error) {
	body := struct {
		EventName string          `json:"event_name"`
		Args      json.RawMessage `json:"args"`
	}{eventName, emptyArgs(args)}
	var r EmitResult
	err := c.do(ctx, http.MethodPost, "/v1/events/emit", token, body, &r)
	return r, err
}

func (c *Client) ListEvents(ctx context.Context, token, listenerID string) ([]Event, error) {
	var e []Event
	err := c.do(ctx, http.MethodGet, "/v1/listeners/"+url.PathEscape(listenerID)+"/events", token, nil, &e)
	return e, err
}

// ConsumeEvent consumes a pending event through a funded process, returning a
// call reply.
func (c *Client) ConsumeEvent(ctx context.Context, token, eventID, processID, parentTraceID string) (CallReply, error) {
	body := map[string]string{"process_id": processID}
	if parentTraceID != "" {
		body["parent_trace_id"] = parentTraceID
	}
	var r CallReply
	err := c.do(ctx, http.MethodPost, "/v1/events/"+url.PathEscape(eventID)+"/consume", token, body, &r)
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
