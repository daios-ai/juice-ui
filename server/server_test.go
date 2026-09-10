package server

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alexedwards/scs/v2"
	"github.com/daios-ai/juice-ui/client"
	"github.com/daios-ai/juice-ui/session"
)

// testWiring is what a test may vary about the wiring: which kernel the records
// name, and how quickly the identity is re-read.
type testWiring struct {
	recordedKey string
	badge       time.Duration
}

// newTestHandler builds the BFF against a fake kernel, wired exactly as
// production is: one identity, built first, feeding both the renderer's money
// unit and the server's notion of which kernel this is.
func newTestHandler(t *testing.T, kernelURL string, opts ...func(*testWiring)) http.Handler {
	t.Helper()
	w := testWiring{badge: 30 * time.Second}
	for _, opt := range opts {
		opt(&w)
	}
	c := client.New(kernelURL, nil)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	identity := NewKernelIdentity(c, w.recordedKey, w.badge, log)
	r, err := NewRenderer(identity.Unit)
	if err != nil {
		t.Fatal(err)
	}
	sm := scs.New()
	h, err := New(Options{
		Client:        c,
		Sessions:      session.NewManager(sm, c),
		Renderer:      r,
		Logger:        log,
		CSRFKey:       bytes.Repeat([]byte("k"), 32),
		Secure:        false,
		Identity:      identity,
		BadgeInterval: w.badge,
	})
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestCSRFBlocksStateChangingPost(t *testing.T) {
	srv := httptest.NewServer(newTestHandler(t, "http://127.0.0.1:1"))
	defer srv.Close()
	// POST without a CSRF token must be rejected by gorilla/csrf (R5.5).
	resp, err := http.PostForm(srv.URL+"/theme", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("CSRF-less POST status = %d want 403", resp.StatusCode)
	}
}

// §5: every surface is signed-in. The kernel registers its natives `local`
// (v0.12.23), so an anonymous caller is served an empty catalog and sys/lookup
// refuses a token-less call outright — an anonymous storefront would be a blank
// page with a search box that cannot answer. Only login, signup and recovery
// are reachable without a session.
func TestUnauthRedirectsToLogin(t *testing.T) {
	srv := httptest.NewServer(newTestHandler(t, "http://127.0.0.1:1"))
	defer srv.Close()
	cl := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for _, path := range []string{"/", "/browse", "/workshop", "/inbox", "/settings", "/wallet", "/processes", "/transactions", "/discovered?ref=vendor@bravo/x"} {
		resp, err := cl.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/login" {
			t.Errorf("%s: status=%d loc=%q want 303 /login", path, resp.StatusCode, resp.Header.Get("Location"))
		}
	}
	// The complement: the sign-in surfaces stay open, or a locked-out user has
	// no way back in.
	for _, path := range []string{"/login", "/recover"} {
		resp, err := cl.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("%s must stay reachable signed-out, got %d", path, resp.StatusCode)
		}
	}
}

func TestThemeStampedNoFlash(t *testing.T) {
	srv := httptest.NewServer(newTestHandler(t, "http://127.0.0.1:1"))
	defer srv.Close()
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/login", nil)
	req.AddCookie(&http.Cookie{Name: "theme", Value: "dracula"})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `data-theme="dracula"`) {
		t.Error("explicit theme cookie not stamped onto the document (would cause flash)")
	}
}

func TestSidebarCollapseStamped(t *testing.T) {
	srv := httptest.NewServer(newTestHandler(t, "http://127.0.0.1:1"))
	defer srv.Close()
	// No cookie -> expanded (no data-sidebar attribute).
	r1, err := http.Get(srv.URL + "/login")
	if err != nil {
		t.Fatal(err)
	}
	plain, _ := io.ReadAll(r1.Body)
	r1.Body.Close()
	if strings.Contains(string(plain), "data-sidebar") {
		t.Error("expanded sidebar should not stamp data-sidebar")
	}
	// Cookie -> collapsed, stamped server-side (no flash, works without JS).
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/login", nil)
	req.AddCookie(&http.Cookie{Name: "sidebar", Value: "collapsed"})
	r2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(r2.Body)
	r2.Body.Close()
	if !strings.Contains(string(body), `data-sidebar="collapsed"`) {
		t.Error("collapsed sidebar cookie not stamped onto the document")
	}
}

func TestRequestIDHeaderSet(t *testing.T) {
	srv := httptest.NewServer(newTestHandler(t, "http://127.0.0.1:1"))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/login")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.Header.Get("X-Request-Id") == "" {
		t.Error("X-Request-Id not set on response (R8.4)")
	}
}

func TestStaticServed(t *testing.T) {
	srv := httptest.NewServer(newTestHandler(t, "http://127.0.0.1:1"))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/static/app.css")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("static css status = %d", resp.StatusCode)
	}
}

// The bare /favicon.ico probe (older browsers, and default requests) serves the
// raster fallback so it never 404s.
func TestFaviconServed(t *testing.T) {
	srv := httptest.NewServer(newTestHandler(t, "http://127.0.0.1:1"))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/favicon.ico")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("/favicon.ico status = %d want 200", resp.StatusCode)
	}
}

// A credential travels only to the server that issued it: the session records
// the kernel's key and network digest at login, and an address that later
// answers for a different kernel ends the session rather than spending on the
// wrong ledger (§5, ecosystem-standard §5).
func TestSessionRefusesADifferentKernel(t *testing.T) {
	base := authKernel()
	defer base.Close()
	var swapped atomic.Bool
	wrap := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" && swapped.Load() {
			w.Write([]byte(`{"status":"ok","handle":"other","public_key":"OTHERKEY","network":"play","network_digest":"D2","decimals":0}`))
			return
		}
		base.Config.Handler.ServeHTTP(w, r)
	}))
	defer wrap.Close()
	// A short identity interval: the kernel is re-read within a heartbeat, which
	// is the window in which a swapped server is noticed.
	srv := httptest.NewServer(newTestHandler(t, wrap.URL, func(w *testWiring) { w.badge = time.Nanosecond }))
	defer srv.Close()

	cl := loginClient(t, srv.URL)
	if body := getBody(t, cl, srv.URL+"/wallet"); !strings.Contains(body, "wallet") {
		t.Fatal("the session must work against the kernel it signed in to")
	}
	swapped.Store(true)

	cl.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := cl.Get(srv.URL + "/wallet")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/login" {
		t.Errorf("a changed kernel must end the session: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
}

// A kernel that cannot identify itself cannot be signed in to: pinning after the
// fact would record whatever answered rather than what was trusted.
func TestLoginRefusedWhenTheKernelCannotBeIdentified(t *testing.T) {
	base := authKernel()
	defer base.Close()
	wrap := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		base.Config.Handler.ServeHTTP(w, r)
	}))
	defer wrap.Close()
	srv := httptest.NewServer(newTestHandler(t, wrap.URL))
	defer srv.Close()

	jar, _ := cookiejar.New(nil)
	cl := &http.Client{Jar: jar}
	page := getBody(t, cl, srv.URL+"/login")
	form := url.Values{"handle": {"alice"}, "password": {"pw"}, "gorilla.csrf.Token": {csrfTokenFrom(t, page)}}
	resp, err := cl.PostForm(srv.URL+"/login", form)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "Sign in") {
		t.Errorf("an unidentifiable kernel must not authenticate a session: %s", body)
	}
}

// The records say which kernel this UI is for. An address answering with another
// key is not that kernel, whatever port it holds (ecosystem-standard §5): nothing
// is trusted from it, and a login is refused rather than pinned to a stranger.
func TestRecordedKeyRefusesAnotherKernel(t *testing.T) {
	kernel := authKernel() // presents ABCDEF…abcd
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL, func(w *testWiring) { w.recordedKey = "SOMEOTHERKEY" }))
	defer srv.Close()

	jar, _ := cookiejar.New(nil)
	cl := &http.Client{Jar: jar}
	page := getBody(t, cl, srv.URL+"/login")
	if strings.Contains(page, "testnet") {
		t.Error("a server that is not the recorded kernel must not be named as if it were")
	}
	form := url.Values{"handle": {"alice"}, "password": {"pw"}, "gorilla.csrf.Token": {csrfTokenFrom(t, page)}}
	resp, err := cl.PostForm(srv.URL+"/login", form)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "Sign in") {
		t.Errorf("login against a mismatched kernel must be refused: %s", body)
	}
}
