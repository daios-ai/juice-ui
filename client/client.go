package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
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
	Public       *bool           `json:"public,omitempty"`
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

// TransactionRating is the embedded rating on a transaction response.
type TransactionRating struct {
	Value int    `json:"value"`
	Note  string `json:"note"`
}

// Transaction is the settled record of a call. gross/net/fee are integer
// credits. result is null on failure; reason is set on failure.
type Transaction struct {
	ID            string             `json:"id"`
	ProcessID     string             `json:"process_id"`
	TraceID       string             `json:"trace_id"`
	ParentTraceID string             `json:"parent_trace_id"`
	OwnerUserID   string             `json:"owner_user_id"`
	CallerUserID  string             `json:"caller_user_id"`
	TargetUserID  string             `json:"target_user_id"`
	ActionID      string             `json:"action_id"`
	ActionName    string             `json:"action_name"`
	Args          json.RawMessage    `json:"args"`
	Result        json.RawMessage    `json:"result"`
	Status        string             `json:"status"`
	Gross         int64              `json:"gross"`
	Net           int64              `json:"net"`
	Fee           int64              `json:"fee"`
	Reason        string             `json:"reason"`
	Rating        *TransactionRating `json:"rating"`
	StartedAt     string             `json:"started_at"`
	EndedAt       string             `json:"ended_at"`
}

// Stats is GET /v1/stats/{action_id}.
type Stats struct {
	ActionID    string  `json:"action_id"`
	Uses        int64   `json:"uses"`
	Successes   int64   `json:"successes"`
	Failures    int64   `json:"failures"`
	RatingCount int64   `json:"rating_count"`
	CostEstimate    float64 `json:"cost_estimate"`
	LatencyEstimate float64 `json:"latency_estimate"`
	RatingEstimate  float64 `json:"rating_estimate"`
	LastUsedAt  string  `json:"last_used_at"`
}

// Rating is one entry of GET /v1/actions/{id}/ratings (list may be null).
type Rating struct {
	ID          string `json:"id"`
	RatedTxID   string `json:"rated_tx_id"`
	RaterUserID string `json:"rater_user_id"`
	Rating      int    `json:"rating"`
	Note        string `json:"note,omitempty"`
	CreatedAt   string `json:"created_at"`
}

// Step is a suspended computation boundary: a pre-baked future Call that a
// designated caller completes by supplying the remaining input.
type Step struct {
	ID                   string          `json:"id"`
	ProcessID            string          `json:"process_id"`
	ParentTraceID        string          `json:"parent_trace_id"`
	RequiredCallerUserID string          `json:"required_caller_user_id"`
	NextActionID         string          `json:"next_action_id"`
	Action               string          `json:"action"` // computed @owner/name; absent in list responses
	PartialArgs          json.RawMessage `json:"partial_args"`
	InputSchema          json.RawMessage `json:"input_schema"`
	Status               string          `json:"status"` // waiting|running|done
	TxID                 string          `json:"tx_id"`
	CreatedAt            string          `json:"created_at"`
}

// Ref returns "@owner/name" for this step's next action. The kernel returns the
// action field only on single-step GET and with a double-@ quirk (verified
// 2026-06-08): "@@sys/lookup" instead of "@sys/lookup". We strip the extra "@".
func (s Step) Ref() string {
	if strings.HasPrefix(s.Action, "@@") {
		return s.Action[1:]
	}
	return s.Action
}

// CreateStepInput is the body for POST /v1/steps.
type CreateStepInput struct {
	ProcessID      string          `json:"process_id"`
	Action         string          `json:"action"`          // @owner/name
	PartialArgs    json.RawMessage `json:"partial_args"`
	InputSchema    json.RawMessage `json:"input_schema"`
	RequiredCaller string          `json:"required_caller"` // @handle
	ParentTraceID  string          `json:"parent_trace_id,omitempty"`
}

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

// RateTransaction submits a 0/1 rating with an optional note for a transaction.
func (c *Client) RateTransaction(ctx context.Context, token, id string, rating int, note string) error {
	body := struct {
		Rating int    `json:"rating"`
		Note   string `json:"note,omitempty"`
	}{Rating: rating, Note: note}
	return c.do(ctx, http.MethodPost, "/v1/transactions/"+url.PathEscape(id)+"/rate", token, body, nil)
}

// ---- steps ----

func (c *Client) CreateStep(ctx context.Context, token string, in CreateStepInput) (Step, error) {
	var s Step
	err := c.do(ctx, http.MethodPost, "/v1/steps", token, in, &s)
	return s, err
}

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
