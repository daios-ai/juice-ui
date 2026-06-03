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
	acts, err := f.client().ListActions(context.Background(), "tok", "alice", "greet")
	if err != nil {
		t.Fatal(err)
	}
	if len(acts) != 1 || acts[0].Action != "@alice/greet" {
		t.Fatalf("acts = %+v", acts)
	}
	if !strings.Contains(f.lastPath, "owner=%40alice") || !strings.Contains(f.lastPath, "name=greet") {
		t.Errorf("filters not encoded: %s", f.lastPath)
	}
}

func TestListActionsNullBecomesEmpty(t *testing.T) {
	f := newFake()
	defer f.close()
	f.reply = `null` // kernel returns null for empty lists (verified live)
	acts, err := f.client().ListActions(context.Background(), "tok", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(acts) != 0 {
		t.Errorf("expected empty, got %+v", acts)
	}
}

func TestCallDefaultsArgsAndEncodesRef(t *testing.T) {
	f := newFake()
	defer f.close()
	f.reply = `{"result":{"ok":true},"tx_id":"t1","trace_id":"tr1"}`
	rep, err := f.client().Call(context.Background(), "tok", CallRequest{
		ProcessID: "p1",
		Action:    "@sys/llm/chat",
	})
	if err != nil {
		t.Fatal(err)
	}
	if rep.TxID != "t1" {
		t.Errorf("tx = %q", rep.TxID)
	}
	// args defaulted to {} and action with "/" passed through verbatim in body.
	if !strings.Contains(string(f.lastBody), `"args":{}`) {
		t.Errorf("args not defaulted: %s", f.lastBody)
	}
	if !strings.Contains(string(f.lastBody), `"@sys/llm/chat"`) {
		t.Errorf("action ref mangled: %s", f.lastBody)
	}
}

func TestProcessIdentUnifiesFields(t *testing.T) {
	// create returns process_id; show returns id.
	created := Process{}
	_ = json.Unmarshal([]byte(`{"process_id":"p-create","available":0}`), &created)
	if created.Ident() != "p-create" {
		t.Errorf("create Ident = %q", created.Ident())
	}
	shown := Process{}
	_ = json.Unmarshal([]byte(`{"id":"p-show","status":"open"}`), &shown)
	if shown.Ident() != "p-show" {
		t.Errorf("show Ident = %q", shown.Ident())
	}
}

func TestStartProcessSendsFunds(t *testing.T) {
	f := newFake()
	defer f.close()
	f.status = 201
	f.reply = `{"process_id":"p1","available":300,"trace_id":"tr"}`
	p, err := f.client().StartProcess(context.Background(), "tok", 300)
	if err != nil {
		t.Fatal(err)
	}
	if p.Available != 300 || p.Ident() != "p1" {
		t.Fatalf("process = %+v", p)
	}
	if !strings.Contains(string(f.lastBody), `"funds":300`) {
		t.Errorf("funds not sent: %s", f.lastBody)
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
	_, err := f.client().StartProcess(context.Background(), "tok", 1000)
	ke, ok := AsKernelError(err)
	if !ok || ke.Kind != KindInsufficientFunds {
		t.Fatalf("expected insufficient funds, got %v", err)
	}
}

func TestRevokeACLPathParams(t *testing.T) {
	f := newFake()
	defer f.close()
	f.status = 204
	if err := f.client().RevokeACL(context.Background(), "tok", "act1", "subj1", "call"); err != nil {
		t.Fatal(err)
	}
	if f.lastMethod != http.MethodDelete {
		t.Errorf("method = %s", f.lastMethod)
	}
	if f.lastPath != "/v1/actions/act1/acl/subj1/call" {
		t.Errorf("path = %s", f.lastPath)
	}
	if len(f.lastBody) != 0 {
		t.Errorf("DELETE must carry no body, got %s", f.lastBody)
	}
}

func TestEmitNeverSendsSource(t *testing.T) {
	f := newFake()
	defer f.close()
	f.reply = `{"event_ids":["e1"]}`
	res, err := f.client().EmitEvent(context.Background(), "tok", "order.created", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.EventIDs) != 1 {
		t.Errorf("event ids = %+v", res.EventIDs)
	}
	if strings.Contains(string(f.lastBody), "source_user_id") {
		t.Errorf("emit must not send source_user_id: %s", f.lastBody)
	}
	if !strings.Contains(string(f.lastBody), `"args":{}`) {
		t.Errorf("args not defaulted: %s", f.lastBody)
	}
}

func TestImportResultCaseInsensitive(t *testing.T) {
	f := newFake()
	defer f.close()
	// Kernel emits capitalized keys (per flows.sh); Go decode is case-insensitive.
	f.reply = `{"Created":[{"id":"a1","name":"x"}],"Updated":[]}`
	res, err := f.client().ImportOpenAPI(context.Background(), "tok", "http://spec")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Created) != 1 || res.Created[0].ID != "a1" {
		t.Fatalf("import result = %+v", res)
	}
}
