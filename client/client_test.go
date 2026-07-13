package client

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeKernel records the last request and replies with a canned status+body.
type fakeKernel struct {
	srv        *httptest.Server
	lastMethod string
	lastPath   string
	lastBody   []byte
	lastAuth   string
	lastRID    string
	status     int
	reply      string
}

func newFake() *fakeKernel {
	f := &fakeKernel{status: 200, reply: "{}"}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.lastMethod = r.Method
		f.lastPath = r.URL.RequestURI()
		f.lastAuth = r.Header.Get("Authorization")
		f.lastRID = r.Header.Get("X-Request-Id")
		f.lastBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(f.status)
		io.WriteString(w, f.reply)
	}))
	return f
}

func (f *fakeKernel) client() *Client { return New(f.srv.URL, nil) }
func (f *fakeKernel) close()          { f.srv.Close() }

func TestHealthAndWellKnown(t *testing.T) {
	f := newFake()
	defer f.close()
	f.reply = `{"status":"ok"}`
	h, err := f.client().Health(context.Background())
	if err != nil || h.Status != "ok" {
		t.Fatalf("Health = %+v, %v", h, err)
	}
	f.reply = `{"base_url":"","handle":"@sys","public_key":"abc"}`
	w, err := f.client().WellKnown(context.Background())
	if err != nil || w.Handle != "@sys" || w.PublicKey != "abc" {
		t.Fatalf("WellKnown = %+v, %v", w, err)
	}
}

func TestAuthorizeParsesCode(t *testing.T) {
	f := newFake()
	defer f.close()
	f.reply = `{"redirect":"?code=THECODE"}`
	code, err := f.client().Authorize(context.Background(), "alice", "pw", "challenge")
	if err != nil {
		t.Fatal(err)
	}
	if code != "THECODE" {
		t.Errorf("code = %q want THECODE", code)
	}
	if f.lastPath != "/v1/auth/authorize" || f.lastMethod != http.MethodPost {
		t.Errorf("unexpected request %s %s", f.lastMethod, f.lastPath)
	}
	// handle normalized to @alice; redirect_uri not sent.
	if !strings.Contains(string(f.lastBody), `"@alice"`) {
		t.Errorf("handle not normalized: %s", f.lastBody)
	}
	if strings.Contains(string(f.lastBody), "redirect_uri") {
		t.Errorf("redirect_uri should be omitted: %s", f.lastBody)
	}
}

func TestAuthorizeFullURLRedirect(t *testing.T) {
	f := newFake()
	defer f.close()
	f.reply = `{"redirect":"http://localhost:8080/cb?code=XYZ&state=1"}`
	code, err := f.client().Authorize(context.Background(), "@bob", "pw", "ch")
	if err != nil || code != "XYZ" {
		t.Fatalf("code=%q err=%v", code, err)
	}
}

func TestBearerAndRequestID(t *testing.T) {
	f := newFake()
	defer f.close()
	f.reply = `{"id":"u1","handle":"@alice"}`
	ctx := WithRequestID(context.Background(), "req-123")
	if _, err := f.client().Me(ctx, "tok-abc"); err != nil {
		t.Fatal(err)
	}
	if f.lastAuth != "Bearer tok-abc" {
		t.Errorf("auth header = %q", f.lastAuth)
	}
	if f.lastRID != "req-123" {
		t.Errorf("request id = %q", f.lastRID)
	}
}

func TestListActionsEncodesFilters(t *testing.T) {
	f := newFake()
	defer f.close()
	f.reply = `[{"id":"a1","action":"@alice/greet","name":"greet"}]`
	acts, err := f.client().ListActions(context.Background(), "tok", "alice", "greet", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(acts) != 1 || acts[0].Action != "@alice/greet" {
		t.Fatalf("acts = %+v", acts)
	}
	if !strings.Contains(f.lastPath, "owner=%40alice") || !strings.Contains(f.lastPath, "name=greet") {
		t.Errorf("filters not encoded: %s", f.lastPath)
	}
	// all=true includes the caller's own inactive/private rows.
	if !strings.Contains(f.lastPath, "all=1") {
		t.Errorf("all flag not encoded: %s", f.lastPath)
	}
}

func TestMeDecodesGrants(t *testing.T) {
	f := newFake()
	defer f.close()
	f.reply = `{"handle":"@alice","grants":[{"action":"@sys/gmail-send","scopes":["send"],"created_at":"2026-07-07T00:00:00Z"}],"connections":[{"provider":"127.0.0.1:8080","actions":2,"unused":false,"created_at":"2026-07-07T00:00:00Z"}]}`
	u, err := f.client().Me(context.Background(), "tok")
	if err != nil {
		t.Fatal(err)
	}
	if len(u.Grants) != 1 || u.Grants[0].Action != "@sys/gmail-send" || len(u.Grants[0].Scopes) != 1 {
		t.Fatalf("grants = %+v", u.Grants)
	}
	if len(u.Connections) != 1 || u.Connections[0].Provider != "127.0.0.1:8080" || u.Connections[0].Actions != 2 {
		t.Fatalf("connections = %+v", u.Connections)
	}
}

func TestGrantMethods(t *testing.T) {
	f := newFake()
	defer f.close()

	// StartGrant posts a selector (+provider) and returns the authorize URL when a
	// browser round-trip is needed (status != granted).
	f.reply = `{"state":"S1","authorize_url":"https://prov/auth?x=1"}`
	url, state, granted, err := f.client().StartGrant(context.Background(), "tok", "@sys/gmail", "oauth:x", "http://ui/grants/callback")
	if err != nil || url != "https://prov/auth?x=1" || state != "S1" || granted {
		t.Fatalf("start = %q %q granted=%v %v", url, state, granted, err)
	}
	if f.lastAuth != "Bearer tok" {
		t.Errorf("StartGrant must send the access token, got %q", f.lastAuth)
	}
	if f.lastPath != "/v1/grants/start" {
		t.Errorf("start path = %q", f.lastPath)
	}
	if !strings.Contains(string(f.lastBody), `"selector":"@sys/gmail"`) || !strings.Contains(string(f.lastBody), `"provider":"oauth:x"`) {
		t.Errorf("start body = %s", f.lastBody)
	}
	// An already-covered account grants instantly with no authorize URL.
	f.reply = `{"status":"granted","actions":["@sys/gmail"]}`
	_, _, granted, err = f.client().StartGrant(context.Background(), "tok", "@sys/gmail", "", "http://ui/grants/callback")
	if err != nil || !granted {
		t.Fatalf("instant-granted start: granted=%v err=%v", granted, err)
	}

	f.reply = `{"actions":["@sys/gmail"]}`
	acts, err := f.client().CompleteGrant(context.Background(), "tok", "S1", "CODE")
	if err != nil || len(acts) != 1 || acts[0] != "@sys/gmail" {
		t.Fatalf("complete = %v %v", acts, err)
	}
	if !strings.Contains(string(f.lastBody), `"state":"S1"`) || !strings.Contains(string(f.lastBody), `"code":"CODE"`) {
		t.Errorf("complete body = %s", f.lastBody)
	}

	// DeleteGrant revokes by selector; DeleteConnection deletes by provider_key.
	if err := f.client().DeleteGrant(context.Background(), "tok", "@sys/gmail-send"); err != nil {
		t.Fatal(err)
	}
	if f.lastMethod != "DELETE" || !strings.Contains(f.lastPath, "selector=%40sys%2Fgmail-send") {
		t.Errorf("delete = %s %s", f.lastMethod, f.lastPath)
	}
	if err := f.client().DeleteConnection(context.Background(), "tok", "bearer:127.0.0.1:8080"); err != nil {
		t.Fatal(err)
	}
	if f.lastMethod != "DELETE" || !strings.Contains(f.lastPath, "account=bearer%3A127.0.0.1%3A8080") {
		t.Errorf("delete connection = %s %s", f.lastMethod, f.lastPath)
	}

	// ConnectGrant posts a static token for a selector's group and returns the
	// actions granted.
	f.reply = `{"status":"connected","actions":["@chat/message","@chat/inbox"]}`
	acts, err = f.client().ConnectGrant(context.Background(), "tok", "@chat", "", "KEY123")
	if err != nil || len(acts) != 2 {
		t.Fatalf("connect = %v %v", acts, err)
	}
	if f.lastMethod != "POST" || f.lastPath != "/v1/grants" {
		t.Errorf("connect = %s %s", f.lastMethod, f.lastPath)
	}
	if !strings.Contains(string(f.lastBody), `"selector":"@chat"`) || !strings.Contains(string(f.lastBody), `"token":"KEY123"`) {
		t.Errorf("connect body = %s", f.lastBody)
	}
	if strings.Contains(string(f.lastBody), `"provider"`) {
		t.Errorf("empty provider must be omitted, got %s", f.lastBody)
	}

	// PlanGrants fetches the consent plan for a selector.
	f.reply = `{"groups":[{"provider_key":"bearer:h","provider":"h","scheme":"delegated_bearer","actions":[{"action":"@chat/message","granted":false}]}],"skipped_uncallable":1}`
	plan, err := f.client().PlanGrants(context.Background(), "tok", "@chat")
	if err != nil || len(plan.Groups) != 1 || plan.Groups[0].ProviderKey != "bearer:h" || plan.SkippedUncallable != 1 {
		t.Fatalf("plan = %+v %v", plan, err)
	}
	if f.lastMethod != "GET" || !strings.Contains(f.lastPath, "/v1/grants/plan?selector=%40chat") {
		t.Errorf("plan path = %s %s", f.lastMethod, f.lastPath)
	}
}

func TestStepDecodesCreatedBy(t *testing.T) {
	f := newFake()
	defer f.close()
	f.reply = `{"id":"S1","action":"@a/target","created_by":"@a/maker","status":"waiting"}`
	st, err := f.client().GetStep(context.Background(), "tok", "S1")
	if err != nil {
		t.Fatal(err)
	}
	if st.CreatedBy != "@a/maker" {
		t.Errorf("CreatedBy = %q", st.CreatedBy)
	}
}

func TestListActionsNullBecomesEmpty(t *testing.T) {
	f := newFake()
	defer f.close()
	f.reply = `null` // kernel returns null for empty lists (verified live)
	acts, err := f.client().ListActions(context.Background(), "tok", "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(acts) != 0 {
		t.Errorf("expected empty, got %+v", acts)
	}
}

func TestRunSendsActionAndArgs(t *testing.T) {
	f := newFake()
	defer f.close()
	f.reply = `{"result":{"ok":true},"tx_id":"t1","trace_id":"tr1","receipt_id":"r1"}`
	rep, err := f.client().Run(context.Background(), "tok", "@sys/llm/chat", nil)
	if err != nil {
		t.Fatal(err)
	}
	if rep.TxID != "t1" {
		t.Errorf("tx = %q", rep.TxID)
	}
	if rep.ReceiptID != "r1" {
		t.Errorf("receipt_id = %q", rep.ReceiptID)
	}
	// args nil → defaulted to {} and action with "/" passed through verbatim in body.
	if !strings.Contains(string(f.lastBody), `"args":{}`) {
		t.Errorf("args not defaulted: %s", f.lastBody)
	}
	if !strings.Contains(string(f.lastBody), `"@sys/llm/chat"`) {
		t.Errorf("action ref mangled: %s", f.lastBody)
	}
	if f.lastPath != "/v1/run" || f.lastMethod != http.MethodPost {
		t.Errorf("unexpected request %s %s", f.lastMethod, f.lastPath)
	}
}

func TestEndProcess204NoBody(t *testing.T) {
	f := newFake()
	defer f.close()
	f.status = 204
	f.reply = ""
	if err := f.client().EndProcess(context.Background(), "tok", "p1"); err != nil {
		t.Fatalf("EndProcess: %v", err)
	}
}

func TestErrorResponseClassified(t *testing.T) {
	f := newFake()
	defer f.close()
	f.status = 402
	f.reply = `{"error":"insufficient user balance"}`
	_, err := f.client().Run(context.Background(), "tok", "@sys/lookup", nil)
	ke, ok := AsKernelError(err)
	if !ok || ke.Kind != KindInsufficientFunds {
		t.Fatalf("expected insufficient funds, got %v", err)
	}
}

func TestCompleteStepAlwaysSendsArgs(t *testing.T) {
	f := newFake()
	defer f.close()
	f.reply = `{"result":{},"tx_id":"t1","trace_id":"tr1","step_id":"s1"}`
	// nil args → normalized to {}
	r, err := f.client().CompleteStep(context.Background(), "tok", "s1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if r.TxID != "t1" || r.StepID != "s1" {
		t.Errorf("reply = %+v", r)
	}
	if !strings.Contains(string(f.lastBody), `"args":{}`) {
		t.Errorf("args must always be sent: %s", f.lastBody)
	}
	if f.lastPath != "/v1/steps/s1/complete" || f.lastMethod != "POST" {
		t.Errorf("unexpected request %s %s", f.lastMethod, f.lastPath)
	}
}

func TestStepRef(t *testing.T) {
	// The kernel now returns a clean single-@ computed action on list and single
	// reads (the old @@ double-prefix quirk is fixed), so Ref() is the field itself.
	s := Step{Action: "@sys/lookup"}
	if got := s.Ref(); got != "@sys/lookup" {
		t.Errorf("Ref() = %q want @sys/lookup", got)
	}
	// Empty action.
	if got := (Step{}).Ref(); got != "" {
		t.Errorf("Ref() empty = %q want empty", got)
	}
}

func TestImportResultCaseInsensitive(t *testing.T) {
	f := newFake()
	defer f.close()
	// Kernel emits capitalized keys (verified live); Go decode is case-insensitive.
	f.reply = `{"Created":[{"id":"a1","name":"x"}],"Updated":[]}`
	res, err := f.client().ImportOpenAPI(context.Background(), "tok", "http://spec")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Created) != 1 || res.Created[0].ID != "a1" {
		t.Fatalf("import result = %+v", res)
	}
}

func TestReceiptVerification(t *testing.T) {
	f := newFake()
	defer f.close()
	f.reply = `{"valid":true,"checks":{"receipt_hash":true}}`
	v, err := f.client().ReceiptVerification(context.Background(), "tok", "tx/1")
	if err != nil {
		t.Fatal(err)
	}
	if !v.Valid || !strings.Contains(string(v.Checks), "receipt_hash") {
		t.Fatalf("verification = %+v", v)
	}
	if f.lastPath != "/v1/transactions/tx%2F1/receipt-verification" || f.lastMethod != http.MethodGet {
		t.Errorf("unexpected request %s %s", f.lastMethod, f.lastPath)
	}
}

func TestUpdateMeSendsFields(t *testing.T) {
	f := newFake()
	defer f.close()
	f.reply = `{"id":"U1","handle":"@alice","email":"new@test.com"}`
	inp := UpdateMeInput{Email: "new@test.com"}
	u, err := f.client().UpdateMe(context.Background(), "tok", inp)
	if err != nil {
		t.Fatal(err)
	}
	if u.Email != "new@test.com" {
		t.Errorf("email = %q", u.Email)
	}
	if f.lastMethod != http.MethodPut || f.lastPath != "/v1/me" {
		t.Errorf("unexpected request %s %s", f.lastMethod, f.lastPath)
	}
	var sent UpdateMeInput
	if err := json.Unmarshal(f.lastBody, &sent); err != nil {
		t.Fatal(err)
	}
	if sent.Email != "new@test.com" {
		t.Errorf("sent email = %q", sent.Email)
	}
	if sent.CurrentPassword != "" || sent.Password != "" {
		t.Error("omitempty: password fields must be absent when empty")
	}
}

func TestActionHTTPUnmarshal(t *testing.T) {
	f := newFake()
	defer f.close()
	// kind=http single read carries the decomposed http object (verified live).
	f.reply = `{"id":"a1","name":"weather","kind":"http","auth_scheme":"delegated_bearer","requires_grant":true,"http":{"method":"GET","url":"https://api.x/{city}","params":[{"name":"city","in":"path"},{"name":"units","in":"query"}]}}`
	a, err := f.client().GetAction(context.Background(), "tok", "a1")
	if err != nil {
		t.Fatal(err)
	}
	if a.HTTP == nil {
		t.Fatal("HTTP must be populated for kind=http")
	}
	// The kernel reports the auth scheme + grant requirement (auth config stays
	// write-only).
	if a.AuthScheme != "delegated_bearer" || !a.RequiresGrant {
		t.Errorf("auth fields = %q %v, want delegated_bearer true", a.AuthScheme, a.RequiresGrant)
	}
	if a.HTTP.Method != "GET" || a.HTTP.URL != "https://api.x/{city}" {
		t.Errorf("http = %+v", a.HTTP)
	}
	if len(a.HTTP.Params) != 2 || a.HTTP.Params[0] != (HTTPParam{Name: "city", In: "path"}) {
		t.Errorf("params = %+v", a.HTTP.Params)
	}

	// Non-http kinds omit http entirely -> nil pointer.
	f.reply = `{"id":"t1","name":"time","kind":"native"}`
	a, err = f.client().GetAction(context.Background(), "tok", "t1")
	if err != nil {
		t.Fatal(err)
	}
	if a.HTTP != nil {
		t.Errorf("HTTP must be nil for non-http kind, got %+v", a.HTTP)
	}

	// Implicit routing: http present but params omitted.
	f.reply = `{"id":"a2","name":"ping","kind":"http","http":{"method":"POST","url":"https://api.x/ping"}}`
	a, err = f.client().GetAction(context.Background(), "tok", "a2")
	if err != nil {
		t.Fatal(err)
	}
	if a.HTTP == nil || a.HTTP.Params != nil {
		t.Errorf("expected http with nil params, got %+v", a.HTTP)
	}
}

// TestActionPeerState checks the remote-proxy health hint round-trips (offline /
// unfunded), and is empty for a healthy or non-remote action.
func TestActionPeerState(t *testing.T) {
	f := newFake()
	defer f.close()
	f.reply = `{"id":"r1","name":"greet","kind":"remote_proxy","peer_state":"offline"}`
	a, err := f.client().GetAction(context.Background(), "tok", "r1")
	if err != nil {
		t.Fatal(err)
	}
	if a.PeerState != "offline" {
		t.Errorf("peer_state = %q, want offline", a.PeerState)
	}
	// Healthy remote action omits peer_state.
	f.reply = `{"id":"r2","name":"greet","kind":"remote_proxy"}`
	a, _ = f.client().GetAction(context.Background(), "tok", "r2")
	if a.PeerState != "" {
		t.Errorf("healthy peer_state = %q, want empty", a.PeerState)
	}
}

func TestCreateActionMarshalsMethodParams(t *testing.T) {
	f := newFake()
	defer f.close()
	f.reply = `{"id":"a1","name":"weather","kind":"http"}`
	in := CreateActionInput{
		Name:   "weather",
		Kind:   "http",
		Source: "https://api.x/{city}",
		Method: "GET",
		Params: []HTTPParam{{Name: "city", In: "path"}, {Name: "units", In: "query"}},
	}
	if _, err := f.client().CreateAction(context.Background(), "tok", in); err != nil {
		t.Fatal(err)
	}
	var sent CreateActionInput
	if err := json.Unmarshal(f.lastBody, &sent); err != nil {
		t.Fatal(err)
	}
	if sent.Method != "GET" || len(sent.Params) != 2 || sent.Params[1] != (HTTPParam{Name: "units", In: "query"}) {
		t.Errorf("sent = %+v", sent)
	}

	// omitempty: implicit routing sends neither method nor params.
	f.reply = `{"id":"a2","name":"plain","kind":"http"}`
	if _, err := f.client().CreateAction(context.Background(), "tok", CreateActionInput{Name: "plain", Kind: "http"}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(f.lastBody), `"method"`) || strings.Contains(string(f.lastBody), `"params"`) {
		t.Errorf("empty method/params must be omitted: %s", f.lastBody)
	}
}

func TestUpdateActionSendsSourceAndAuth(t *testing.T) {
	f := newFake()
	defer f.close()
	f.reply = `{"id":"a1","name":"x","kind":"http"}`
	src := "https://ui/_kernel/chat/message"
	pub := true
	if _, err := f.client().UpdateAction(context.Background(), "tok", "a1", UpdateActionInput{
		Source: &src, Method: "POST", Auth: json.RawMessage(`{"scheme":"delegated_bearer"}`), Public: &pub,
	}); err != nil {
		t.Fatal(err)
	}
	body := string(f.lastBody)
	if f.lastMethod != "PUT" || !strings.Contains(body, `"source":"https://ui/_kernel/chat/message"`) {
		t.Errorf("update body = %s (%s)", body, f.lastMethod)
	}
	if !strings.Contains(body, `"auth":{"scheme":"delegated_bearer"}`) {
		t.Errorf("auth not sent in update: %s", body)
	}
	// omitempty: an update with no auth omits the field.
	f.reply = `{"id":"a1","name":"x","kind":"http"}`
	if _, err := f.client().UpdateAction(context.Background(), "tok", "a1", UpdateActionInput{Public: &pub}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(f.lastBody), `"auth"`) {
		t.Errorf("empty auth must be omitted: %s", f.lastBody)
	}
}
