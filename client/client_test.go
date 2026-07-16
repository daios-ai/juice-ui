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

func TestListTransactionsPagination(t *testing.T) {
	f := newFake()
	defer f.close()
	// limit>0 → a single server page: the kernel caps, the client sends ?limit and
	// returns the page as-is (no client-side slice).
	f.reply = `[{"id":"A"},{"id":"B"}]`
	got, err := f.client().ListTransactions(context.Background(), "tok", "", 2)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.lastPath, "limit=2") {
		t.Errorf("limit not sent: %s", f.lastPath)
	}
	if len(got) != 2 {
		t.Errorf("single page = %+v", got)
	}
	// limit==0 → fetch ALL by paging with the ceiling; a short first page (2 < 200)
	// ends the loop in one request, and the process scope is carried on every page.
	f.reply = `[{"id":"X"},{"id":"Y"}]`
	all, err := f.client().ListTransactions(context.Background(), "tok", "P1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.lastPath, "limit=200") || !strings.Contains(f.lastPath, "process_id=P1") {
		t.Errorf("get-all must page with the ceiling limit + process scope: %s", f.lastPath)
	}
	if len(all) != 2 {
		t.Errorf("get-all one page = %+v", all)
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

func TestMeDecodesConnectors(t *testing.T) {
	f := newFake()
	defer f.close()
	// New /v1/me: a directory-grouped `connectors` tree (each node carries backing
	// connections + per-action grants with provider_key) plus a `connections`
	// inventory that carries provider_key and surfaces unused accounts.
	f.reply = `{"handle":"@alice",
	 "connectors":[{"directory":"@chat","connections":[{"provider":"api.stripe.com","actions":1,"unused":false,"provider_key":"oauth:PK","created_at":"2026-07-07T00:00:00Z"}],
	   "actions":[{"action":"@chat/pay","scopes":["pay.write"],"provider_key":"oauth:PK","created_at":"2026-07-07T00:00:00Z"}]}],
	 "connections":[{"provider":"api.stripe.com","actions":1,"unused":false,"provider_key":"oauth:PK","created_at":"2026-07-07T00:00:00Z"},
	   {"provider":"mail.host","actions":0,"unused":true,"provider_key":"bearer:mail.host","created_at":"2026-07-07T00:00:00Z"}]}`
	u, err := f.client().Me(context.Background(), "tok")
	if err != nil {
		t.Fatal(err)
	}
	if len(u.Connectors) != 1 || u.Connectors[0].Directory != "@chat" {
		t.Fatalf("connectors = %+v", u.Connectors)
	}
	c := u.Connectors[0]
	if len(c.Actions) != 1 || c.Actions[0].Action != "@chat/pay" || c.Actions[0].ProviderKey != "oauth:PK" {
		t.Fatalf("connector actions = %+v", c.Actions)
	}
	if len(c.Connections) != 1 || c.Connections[0].Provider != "api.stripe.com" {
		t.Fatalf("connector connections = %+v", c.Connections)
	}
	// The inventory carries provider_key (for account disconnect) and the unused account.
	if len(u.Connections) != 2 || u.Connections[0].ProviderKey != "oauth:PK" {
		t.Fatalf("connections = %+v", u.Connections)
	}
	if !u.Connections[1].Unused || u.Connections[1].ProviderKey != "bearer:mail.host" {
		t.Fatalf("unused account not surfaced: %+v", u.Connections[1])
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
	vis := "public"
	if _, err := f.client().UpdateAction(context.Background(), "tok", "a1", UpdateActionInput{
		Source: &src, Method: "POST", Auth: json.RawMessage(`{"scheme":"delegated_bearer"}`), Visibility: &vis,
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
	if _, err := f.client().UpdateAction(context.Background(), "tok", "a1", UpdateActionInput{Visibility: &vis}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(f.lastBody), `"auth"`) {
		t.Errorf("empty auth must be omitted: %s", f.lastBody)
	}
}

func TestCallbackCreateStep(t *testing.T) {
	var gotCap, gotAuth, gotBody, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotCap = r.Header.Get("X-Juice-Capability")
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{"id":"sentinel-1","status":"waiting"}`))
	}))
	defer srv.Close()

	// baseURL is deliberately bogus — the callback must target the passed URL, not baseURL.
	c := New("http://127.0.0.1:1", nil)
	st, err := c.CallbackCreateStep(context.Background(), srv.URL, "trace-1.sig",
		CreateStepInput{ActionID: "sink", RequiredCaller: "@chat"})
	if err != nil {
		t.Fatalf("CallbackCreateStep: %v", err)
	}
	if st.ID != "sentinel-1" {
		t.Errorf("step id = %q, want sentinel-1", st.ID)
	}
	if gotPath != "/v1/steps" {
		t.Errorf("path = %q, want /v1/steps", gotPath)
	}
	if gotCap != "trace-1.sig" {
		t.Errorf("X-Juice-Capability = %q, want trace-1.sig", gotCap)
	}
	if gotAuth != "" {
		t.Errorf("must NOT send a bearer; Authorization = %q", gotAuth)
	}
	if strings.Contains(gotBody, "trace_id") {
		t.Errorf("body must NOT carry trace_id (capability IS the trace): %s", gotBody)
	}
	if !strings.Contains(gotBody, `"action_id":"sink"`) || !strings.Contains(gotBody, `"required_caller":"@chat"`) {
		t.Errorf("body = %s", gotBody)
	}
}

func TestTransfer(t *testing.T) {
	f := newFake()
	defer f.srv.Close()
	f.reply = `{"id":"L1","amount":10,"from_handle":"@me","to_handle":"@ana","operator_handle":"@me","created_at":"2024-01-01T00:00:00Z"}`
	e, err := f.client().Transfer(context.Background(), "tok", "@ana", 10, "lunch", "")
	if err != nil {
		t.Fatal(err)
	}
	if f.lastMethod != "POST" || f.lastPath != "/v1/transfers" {
		t.Errorf("transfer hit %s %s", f.lastMethod, f.lastPath)
	}
	body := string(f.lastBody)
	if !strings.Contains(body, `"recipient":"@ana"`) || !strings.Contains(body, `"amount":10`) || !strings.Contains(body, `"reason":"lunch"`) {
		t.Errorf("transfer body = %s", body)
	}
	if e.ToHandle != "@ana" || e.Amount != 10 {
		t.Errorf("transfer entry = %+v", e)
	}
}

func TestLedger(t *testing.T) {
	f := newFake()
	defer f.srv.Close()
	f.reply = `[{"id":"L1","amount":100,"to_handle":"@me","operator_handle":"@sys","created_at":"2024-01-01T00:00:00Z"}]`
	out, err := f.client().Ledger(context.Background(), "tok", 20, 0)
	if err != nil {
		t.Fatal(err)
	}
	if f.lastMethod != "GET" || f.lastPath != "/v1/ledger?limit=20" {
		t.Errorf("ledger hit %s %s", f.lastMethod, f.lastPath)
	}
	if len(out) != 1 || out[0].Amount != 100 || out[0].ToHandle != "@me" {
		t.Errorf("ledger = %+v", out)
	}
}

func TestAdminRename(t *testing.T) {
	f := newFake()
	defer f.srv.Close()
	if err := f.client().AdminRename(context.Background(), "tok", "@old", "new"); err != nil {
		t.Fatal(err)
	}
	if f.lastMethod != "POST" || f.lastPath != "/control/users/@old/rename" {
		t.Errorf("rename hit %s %s", f.lastMethod, f.lastPath)
	}
	if !strings.Contains(string(f.lastBody), `"new_handle":"@new"`) {
		t.Errorf("rename body = %s (want normalized @new)", string(f.lastBody))
	}
}
