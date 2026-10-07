package server

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeSteps is a kernel package stand-in: it reports what the test scripts.
type fakeSteps struct {
	mu      sync.Mutex
	local   bool
	created []string
	phrase  string
	err     error
	report  func(state, detail string)
}

func (f *fakeSteps) Ensure(ctx context.Context, report func(state, detail string), created <-chan struct{}) {
	f.mu.Lock()
	f.report = report
	f.mu.Unlock()
	<-ctx.Done()
}
func (f *fakeSteps) Create(world, handle, password string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.created = append(f.created, world+"/"+handle+"/"+password)
	return f.phrase, f.err
}
func (f *fakeSteps) Local() bool { return f.local }

func newGate(t *testing.T, listen string) (*Gate, *fakeSteps) {
	t.Helper()
	r, err := NewRenderer(playKernel)
	if err != nil {
		t.Fatal(err)
	}
	steps := &fakeSteps{local: true, phrase: "one two three four five six seven eight nine ten eleven twelve"}
	g := NewGate(r, steps, "http://127.0.0.1:4040", listen, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go g.Run(ctx)
	for i := 0; i < 50; i++ {
		steps.mu.Lock()
		ok := steps.report != nil
		steps.mu.Unlock()
		if ok {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	return g, steps
}

func do(t *testing.T, h http.Handler, method, path string, form url.Values, hdr map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req := httptest.NewRequest(method, path, body)
	req.RemoteAddr = "127.0.0.1:5555"
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// The screen says what the gate is doing, on every path; the status fragment
// redirects to the root once the kernel answers.
func TestGateScreenFollowsTheState(t *testing.T) {
	g, steps := newGate(t, "127.0.0.1:8080")
	h := g.Handler()
	for state, want := range map[string]string{
		"installing": "Installing the kernel", "starting": "Starting the kernel",
		"waiting": "Waiting for", "failed": "could not bind",
	} {
		steps.report(state, map[string]string{"installing": "v0.15.6", "failed": "could not bind :4040"}[state])
		body := do(t, h, http.MethodGet, "/anything/at/all", nil, nil).Body.String()
		if !strings.Contains(body, want) || !strings.Contains(body, `hx-get="/kernel/status"`) {
			t.Errorf("%s: screen missing %q", state, want)
		}
	}
	rec := do(t, h, http.MethodGet, "/kernel/status", nil, nil)
	if rec.Header().Get("HX-Redirect") != "" {
		t.Error("no redirect before the kernel answers")
	}
	steps.report("serving", "")
	select {
	case <-g.Ready():
	default:
		t.Fatal("serving must close Ready")
	}
	if rec := do(t, h, http.MethodGet, "/kernel/status", nil, nil); rec.Header().Get("HX-Redirect") != "/" {
		t.Error("serving redirects the poll to the root")
	}
}

// The first boot is a local act: the form and its posts exist only for a
// loopback listener, a loopback peer, no proxy header and a same-origin post.
func TestGateFirstBootIsLocal(t *testing.T) {
	g, steps := newGate(t, "127.0.0.1:8080")
	h := g.Handler()
	steps.report("needs-create", "")
	body := do(t, h, http.MethodGet, "/", nil, nil).Body.String()
	if !strings.Contains(body, `action="/kernel/create"`) || !strings.Contains(body, `name="handle"`) || !strings.Contains(body, `value="play"`) {
		t.Errorf("a local request gets the form: %s", body)
	}
	if b := do(t, h, http.MethodGet, "/", nil, map[string]string{"X-Forwarded-For": "203.0.113.9"}).Body.String(); strings.Contains(b, `action="/kernel/create"`) || !strings.Contains(b, "juice kernel serve") {
		t.Error("a proxied request names the terminal command")
	}
	form := url.Values{"world": {"play"}, "handle": {"alpha"}, "password": {"secret123"}, "confirm_password": {"secret123"}}
	for name, hdr := range map[string]map[string]string{
		"cross-site": {"Sec-Fetch-Site": "cross-site"}, "proxied": {"X-Forwarded-For": "203.0.113.9"},
	} {
		if rec := do(t, h, http.MethodPost, "/kernel/create", form, hdr); rec.Code != http.StatusForbidden {
			t.Errorf("%s post must be refused, got %d", name, rec.Code)
		}
	}
	remote := httptest.NewRequest(http.MethodPost, "/kernel/create", strings.NewReader(form.Encode()))
	remote.RemoteAddr = "10.0.0.9:4444"
	remote.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, remote)
	if rec.Code != http.StatusForbidden {
		t.Errorf("a remote peer must be refused, got %d", rec.Code)
	}
	wide, _ := newGate(t, "0.0.0.0:8080")
	if b := do(t, wide.Handler(), http.MethodGet, "/", nil, nil).Body.String(); strings.Contains(b, `action="/kernel/create"`) {
		t.Error("a UI listening on every interface offers no form")
	}
	if len(steps.created) != 0 {
		t.Error("no refused post reaches Create")
	}
	bad := url.Values{"world": {"play"}, "handle": {"alpha"}, "password": {"secret123"}, "confirm_password": {"other"}}
	if rec := do(t, h, http.MethodPost, "/kernel/create", bad, nil); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "don&#39;t match") || len(steps.created) != 0 {
		t.Errorf("unequal passwords re-render the form: %d", rec.Code)
	}
}

// A created kernel shows its phrase, on every path, until acknowledged; the
// acknowledgement drops it, releases the loop and sends the person to the root.
// The password and the phrase reach no log.
func TestGateHoldsThePhraseUntilAcknowledged(t *testing.T) {
	var logBuf strings.Builder
	r, _ := NewRenderer(playKernel)
	steps := &fakeSteps{local: true, phrase: "one two three four five six seven eight nine ten eleven twelve"}
	g := NewGate(r, steps, "http://127.0.0.1:4040", "127.0.0.1:8080", slog.New(slog.NewTextHandler(&logBuf, nil)))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go g.Run(ctx)
	time.Sleep(20 * time.Millisecond)
	h := g.Handler()
	steps.report("needs-create", "")
	form := url.Values{"world": {"play"}, "handle": {"alpha"}, "password": {"secret123"}, "confirm_password": {"secret123"}}
	rec := do(t, h, http.MethodPost, "/kernel/create", form, nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "<li>twelve</li>") || !strings.Contains(rec.Body.String(), `action="/kernel/created"`) {
		t.Fatalf("the phrase page: %d %s", rec.Code, rec.Body.String())
	}
	if len(steps.created) != 1 || steps.created[0] != "play/alpha/secret123" {
		t.Errorf("Create called with %v", steps.created)
	}
	if b := do(t, h, http.MethodGet, "/elsewhere", nil, nil).Body.String(); !strings.Contains(b, "<li>twelve</li>") {
		t.Error("every path keeps the phrase until it is acknowledged")
	}
	if b := do(t, h, http.MethodGet, "/", nil, map[string]string{"X-Forwarded-For": "203.0.113.9"}).Body.String(); strings.Contains(b, "<li>twelve</li>") {
		t.Error("the phrase is shown only to the local request")
	}
	select {
	case <-g.created:
		t.Fatal("the loop must wait for the acknowledgement")
	default:
	}
	rec = do(t, h, http.MethodPost, "/kernel/created", url.Values{"understand": {"on"}}, nil)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/" {
		t.Errorf("acknowledgement redirects to the root: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	select {
	case <-g.created:
	default:
		t.Fatal("acknowledgement releases the loop")
	}
	if b := do(t, h, http.MethodGet, "/", nil, nil).Body.String(); strings.Contains(b, "<li>twelve</li>") {
		t.Error("the phrase is dropped once acknowledged")
	}
	if l := logBuf.String(); strings.Contains(l, "secret123") || strings.Contains(l, "twelve") {
		t.Errorf("the log holds neither the password nor the phrase: %s", l)
	}
}

// A Create the kernel refuses re-renders the form with the kernel's words.
func TestGateShowsTheKernelsRefusal(t *testing.T) {
	g, steps := newGate(t, "127.0.0.1:8080")
	steps.err = errorString("the kernel did not start: bootstrap: kernel name refused")
	steps.report("needs-create", "")
	form := url.Values{"world": {"play"}, "handle": {"Bad"}, "password": {"secret123"}, "confirm_password": {"secret123"}}
	rec := do(t, g.Handler(), http.MethodPost, "/kernel/create", form, nil)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "kernel name refused") || !strings.Contains(rec.Body.String(), `action="/kernel/create"`) {
		t.Errorf("refusal re-renders the form with the message: %d %s", rec.Code, rec.Body.String())
	}
}

type errorString string

func (e errorString) Error() string { return string(e) }
