package server

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
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
	keys    []string
	err     error
	listErr error
	report  func(state, detail string)
}

func (f *fakeSteps) Ensure(ctx context.Context, report func(state, detail string), created <-chan struct{}) {
	f.mu.Lock()
	f.report = report
	f.mu.Unlock()
	<-ctx.Done()
}
func (f *fakeSteps) Create(world, handle, password, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.created = append(f.created, world+"/"+handle+"/"+password)
	f.keys = append(f.keys, key)
	return f.err
}
func (f *fakeSteps) Local() bool { return f.local }
func (f *fakeSteps) Worlds() ([]World, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listErr != nil {
		return nil, f.listErr
	}
	return []World{{Name: "arbitrum-one", Description: "USDT0 on Arbitrum One"}, {Name: "bad", Error: "parse world file"}, {Name: "play", Description: "no real money"}}, nil
}

func newGate(t *testing.T, listen string) (*Gate, *fakeSteps) {
	t.Helper()
	r, err := NewRenderer(playKernel)
	if err != nil {
		t.Fatal(err)
	}
	steps := &fakeSteps{local: true}
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
		if !strings.Contains(body, want) || !strings.Contains(body, `hx-get="/kernel/status"`) || !strings.Contains(body, "Juice installation") {
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
	if !strings.Contains(body, `action="/kernel/create"`) || !strings.Contains(body, `name="handle"`) || !strings.Contains(body, `<option value="play" selected>play — no real money</option>`) ||
		!strings.Contains(body, `<option value="arbitrum-one">arbitrum-one — USDT0 on Arbitrum One</option>`) || !strings.Contains(body, `<option value="bad" disabled>bad — parse world file</option>`) {
		t.Errorf("a local request gets the form, the kernel's worlds to choose from, one it would refuse disabled with its error: %s", body)
	}
	if strings.Contains(body, "hx-get") {
		t.Error("the form is not polled: nothing changes until it is posted, and a swap would erase it")
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
	unlisted := url.Values{"world": {"../x"}, "handle": {"alpha"}, "password": {"secret123"}, "confirm_password": {"secret123"}}
	if rec := do(t, h, http.MethodPost, "/kernel/create", unlisted, nil); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "Choose a world") || len(steps.created) != 0 {
		t.Errorf("a world the installation does not hold is refused: %d", rec.Code)
	}
	refused := url.Values{"world": {"bad"}, "handle": {"alpha"}, "password": {"secret123"}, "confirm_password": {"secret123"}}
	if rec := do(t, h, http.MethodPost, "/kernel/create", refused, nil); rec.Code != http.StatusUnprocessableEntity || len(steps.created) != 0 {
		t.Errorf("a world the kernel would refuse is refused: %d", rec.Code)
	}
	steps.mu.Lock()
	steps.listErr = errors.New("juice kernel worlds: unreadable")
	steps.mu.Unlock()
	if rec := do(t, h, http.MethodPost, "/kernel/create", form, nil); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "juice kernel worlds: unreadable") || strings.Contains(rec.Body.String(), "<option") || len(steps.created) != 0 {
		t.Errorf("a failing list is the form's error, with nothing to choose: %d %s", rec.Code, rec.Body.String())
	}
}

// The gate generates the phrase, as signup does, and passes the kernel its
// public key; a failed attempt keeps the phrase, so the attempt that succeeds
// presents the same key. The phrase is shown, on every path, until
// acknowledged; the acknowledgement drops it, releases the loop and sends the
// person to the root. The password and the phrase reach no log.
func TestGateHoldsThePhraseUntilAcknowledged(t *testing.T) {
	var logBuf strings.Builder
	r, _ := NewRenderer(playKernel)
	steps := &fakeSteps{local: true, err: errorString("the kernel did not start: transport refused")}
	g := NewGate(r, steps, "http://127.0.0.1:4040", "127.0.0.1:8080", slog.New(slog.NewTextHandler(&logBuf, nil)))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go g.Run(ctx)
	time.Sleep(20 * time.Millisecond)
	h := g.Handler()
	steps.report("needs-create", "")
	released := func() bool {
		select {
		case <-g.created:
			return true
		default:
			return false
		}
	}
	if do(t, h, http.MethodPost, "/kernel/created", url.Values{"understand": {"on"}}, nil); released() {
		t.Fatal("an acknowledgement with no phrase pending releases nothing")
	}
	form := url.Values{"world": {"play"}, "handle": {"alpha"}, "password": {"secret123"}, "confirm_password": {"secret123"}}
	if rec := do(t, h, http.MethodPost, "/kernel/create", form, nil); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("a failed boot re-renders the form: %d", rec.Code)
	}
	steps.err = nil
	rec := do(t, h, http.MethodPost, "/kernel/create", form, nil)
	words := regexp.MustCompile(`<li>([a-z]+)</li>`).FindAllStringSubmatch(rec.Body.String(), -1)
	var phrase []string
	for _, m := range words {
		phrase = append(phrase, m[1])
	}
	if rec.Code != 200 || len(phrase) != 12 || !strings.Contains(rec.Body.String(), `action="/kernel/created"`) {
		t.Fatalf("the phrase page: %d %s", rec.Code, rec.Body.String())
	}
	pub, err := recoveryPubKey(strings.Join(phrase, " "))
	if err != nil {
		t.Fatalf("the phrase is a BIP-39 mnemonic: %v", err)
	}
	if len(steps.keys) != 2 || steps.keys[0] != pub || steps.keys[1] != pub {
		t.Errorf("every attempt presents the key of the phrase shown: %v, want %s", steps.keys, pub)
	}
	if len(steps.created) != 2 || steps.created[1] != "play/alpha/secret123" {
		t.Errorf("Create called with %v", steps.created)
	}
	shown := "<li>" + phrase[0] + "</li>"
	if b := do(t, h, http.MethodGet, "/elsewhere", nil, nil).Body.String(); !strings.Contains(b, shown) {
		t.Error("every path keeps the phrase until it is acknowledged")
	}
	if b := do(t, h, http.MethodGet, "/", nil, map[string]string{"X-Forwarded-For": "203.0.113.9"}).Body.String(); strings.Contains(b, "phrase-words") {
		t.Error("the phrase is shown only to the local request")
	}
	if do(t, h, http.MethodPost, "/kernel/created", nil, nil); released() || !strings.Contains(do(t, h, http.MethodGet, "/", nil, nil).Body.String(), shown) {
		t.Fatal("without the I-understand box the phrase stays and the loop waits")
	}
	rec = do(t, h, http.MethodPost, "/kernel/created", url.Values{"understand": {"on"}}, nil)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/" {
		t.Errorf("acknowledgement redirects to the root: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	if !released() {
		t.Fatal("acknowledgement releases the loop")
	}
	if b := do(t, h, http.MethodGet, "/", nil, nil).Body.String(); strings.Contains(b, "phrase-words") {
		t.Error("the phrase is dropped once acknowledged")
	}
	l := logBuf.String()
	for _, w := range append(phrase, "secret123") {
		if strings.Contains(l, " "+w+" ") || strings.Contains(l, "secret123") {
			t.Fatalf("the log holds neither the password nor the phrase: %s", l)
		}
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
