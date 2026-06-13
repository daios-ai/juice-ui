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

func TestCreateStepSendsFields(t *testing.T) {
	f := newFake()
	defer f.close()
	f.status = 201
	f.reply = `{"id":"s1","process_id":"p1","required_caller_user_id":"u1","next_action_id":"a1","status":"waiting","created_at":"2026-01-01T00:00:00Z"}`
	in := CreateStepInput{
		ProcessID:      "p1",
		Action:         "@sys/lookup",
		PartialArgs:    json.RawMessage(`{"q":"hi"}`),
		InputSchema:    json.RawMessage(`{"type":"object"}`),
		RequiredCaller: "@alice",
	}
	s, err := f.client().CreateStep(context.Background(), "tok", in)
	if err != nil {
		t.Fatal(err)
	}
	if s.ID != "s1" || s.Status != "waiting" {
		t.Errorf("step = %+v", s)
	}
	if f.lastPath != "/v1/steps" || f.lastMethod != "POST" {
		t.Errorf("unexpected request %s %s", f.lastMethod, f.lastPath)
	}
	if !strings.Contains(string(f.lastBody), `"@sys/lookup"`) {
		t.Errorf("action not sent: %s", f.lastBody)
	}
	if !strings.Contains(string(f.lastBody), `"@alice"`) {
		t.Errorf("required_caller not sent: %s", f.lastBody)
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

func TestStepRefStripsDoubleAt(t *testing.T) {
	// Kernel returns @@owner/name; Ref() strips the extra @.
	s := Step{Action: "@@sys/lookup"}
	if got := s.Ref(); got != "@sys/lookup" {
		t.Errorf("Ref() = %q want @sys/lookup", got)
	}
	// Single @ is returned as-is.
	s2 := Step{Action: "@sys/lookup"}
	if got := s2.Ref(); got != "@sys/lookup" {
		t.Errorf("Ref() single-@ = %q want @sys/lookup", got)
	}
	// Empty action.
	s3 := Step{}
	if got := s3.Ref(); got != "" {
		t.Errorf("Ref() empty = %q want empty", got)
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
