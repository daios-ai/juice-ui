package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alexedwards/scs/v2"
	"github.com/daios-ai/juice-ui/apps"
	"github.com/daios-ai/juice-ui/client"
	"github.com/daios-ai/juice-ui/session"
)

// testWiring is what a test may vary about the wiring: which kernel the records
// name, and how quickly the identity is re-read.
type testWiring struct {
	recordedKey string
	statePath   string // state.json, "" in memory
	badge       time.Duration
	config      Configuration
	apps        []apps.App
	now         func() time.Time // the clock sessions age by; nil is the wall clock
	version     string
	updates     Updates
	parts       []Part
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
	var tmpl []fs.FS
	for _, a := range w.apps {
		tmpl = append(tmpl, a.Templates())
	}
	r, err := NewRenderer(identity, tmpl...)
	if err != nil {
		t.Fatal(err)
	}
	sessions := session.NewManager(scs.New(), c)
	if w.now != nil {
		sessions.Now = w.now
	}
	h, err := New(Options{
		Apps:          w.apps,
		Client:        c,
		Sessions:      sessions,
		Renderer:      r,
		Logger:        log,
		CSRFKey:       bytes.Repeat([]byte("k"), 32),
		Secure:        false,
		Identity:      identity,
		BadgeInterval: w.badge,
		Config:        w.config,
		Version:       w.version,
		Updates:       w.updates,
		Parts:         w.parts,
		StatePath:     w.statePath,
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
	for _, path := range []string{"/", "/browse", "/workshop", "/inbox", "/settings", "/wallet", "/processes", "/transactions", "/actions?ref=vendor@bravo/x", "/actions/panel?ref=vendor@bravo/x"} {
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
	var told atomic.Int32 // revocations the other kernel received
	wrap := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" && swapped.Load() {
			w.Write([]byte(`{"status":"ok","handle":"other","public_key":"OTHERKEY","network":"play","network_fingerprint":"D2","decimals":0}`))
			return
		}
		if r.URL.Path == "/v1/auth/logout" {
			told.Add(1)
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
	if told.Load() != 0 {
		t.Error("the refresh token must never be sent to a kernel that did not issue it")
	}
}

// revokingKernel is the fake kernel with its revocations counted.
func revokingKernel(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	base := authKernel()
	t.Cleanup(base.Close)
	var n atomic.Int32
	k := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/v1/auth/logout" {
			n.Add(1)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		base.Config.Handler.ServeHTTP(w, r)
	}))
	t.Cleanup(k.Close)
	return k, &n
}

// clock is a settable clock for the sessions of a test server.
type clock struct{ ns atomic.Int64 }

func newClock() *clock                   { c := &clock{}; c.ns.Store(time.Unix(1_000_000, 0).UnixNano()); return c }
func (c *clock) now() time.Time          { return time.Unix(0, c.ns.Load()) }
func (c *clock) advance(d time.Duration) { c.ns.Add(int64(d)) }

// sessionServer is a BFF on the revoking kernel whose sessions age by c.
func sessionServer(t *testing.T, c *clock) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	k, revoked := revokingKernel(t)
	srv := httptest.NewServer(newTestHandler(t, k.URL, func(w *testWiring) { w.now = c.now }))
	t.Cleanup(srv.Close)
	return srv, revoked
}

// get makes a request without following a redirect; hx marks it an htmx one.
func get(t *testing.T, cl *http.Client, u string, hx bool) (*http.Response, string) {
	t.Helper()
	cl.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	req, _ := http.NewRequest(http.MethodGet, u, nil)
	if hx {
		req.Header.Set("HX-Request", "true")
	}
	resp, err := cl.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

// flashOf is the one-shot notice the login page will show.
func flashOf(t *testing.T, cl *http.Client, base string) string {
	t.Helper()
	u, _ := url.Parse(base)
	for _, c := range cl.Jar.Cookies(u) {
		if c.Name == "flash" {
			v, _ := url.QueryUnescape(c.Value)
			return v
		}
	}
	return ""
}

// The heartbeat observes the idle rule: a page left open is signed out at the
// limit, told why, and its refresh token revoked at the kernel.
func TestIdleSessionIsSignedOutByTheHeartbeat(t *testing.T) {
	c := newClock()
	srv, revoked := sessionServer(t, c)
	cl := loginClient(t, srv.URL)

	c.advance(session.IdleLimit - time.Minute)
	if resp, _ := get(t, cl, srv.URL+"/tick", true); resp.StatusCode != 200 || resp.Header.Get("HX-Redirect") != "" {
		t.Fatalf("before the limit the tick serves: %d %q", resp.StatusCode, resp.Header.Get("HX-Redirect"))
	}
	c.advance(time.Minute)
	resp, _ := get(t, cl, srv.URL+"/tick", true)
	if resp.Header.Get("HX-Redirect") != "/login" {
		t.Fatalf("at the limit the tick signs out: HX-Redirect %q", resp.Header.Get("HX-Redirect"))
	}
	if f := flashOf(t, cl, srv.URL); f != "info|You were signed out after 30 minutes of inactivity." {
		t.Errorf("flash = %q", f)
	}
	if revoked.Load() != 1 {
		t.Errorf("revocations = %d, want 1", revoked.Load())
	}
	if resp, _ := get(t, cl, srv.URL+"/", false); resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/login" {
		t.Errorf("the session is gone: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	if _, body := get(t, cl, srv.URL+"/login", false); !strings.Contains(body, `<div class="banner banner-info" role="status"><span class="stretch">You were signed out after 30 minutes of inactivity.</span><a class="modal-close" href="/login" data-dismiss`) {
		t.Error("the login page heads its column with the notice, a dismissable info banner")
	}
}

// The heartbeat is not activity; a request the person made is.
func TestHeartbeatIsNotActivity(t *testing.T) {
	c := newClock()
	srv, _ := sessionServer(t, c)

	idle := loginClient(t, srv.URL)
	c.advance(20 * time.Minute)
	get(t, idle, srv.URL+"/tick", true)
	c.advance(20 * time.Minute)
	if resp, _ := get(t, idle, srv.URL+"/tick", true); resp.Header.Get("HX-Redirect") != "/login" {
		t.Error("ticks alone must not keep a session alive")
	}

	busy := loginClient(t, srv.URL)
	c.advance(20 * time.Minute)
	get(t, busy, srv.URL+"/wallet", false)
	c.advance(20 * time.Minute)
	if resp, _ := get(t, busy, srv.URL+"/tick", true); resp.Header.Get("HX-Redirect") != "" {
		t.Error("a page the person opened 20 minutes ago keeps the session")
	}
}

// The idle warning is a watch: the dialog opens a minute before the limit,
// Stay signed in closes it and extends the session, and an unchanged state is
// answered with nothing.
func TestIdleWarningOpensAndCloses(t *testing.T) {
	c := newClock()
	srv, _ := sessionServer(t, c)
	cl := loginClient(t, srv.URL)
	_, page := get(t, cl, srv.URL+"/wallet", false)
	if strings.Count(page, `id="watch-session"`) != 1 || !strings.Contains(page, `value="session::"`) || strings.Contains(page, `class="modal-confirm" aria-labelledby="idle-msg" hx-swap-oob="true" open`) {
		t.Fatal("a page carries the closed warning and its watch once")
	}

	c.advance(session.IdleLimit - session.Warning)
	_, body := get(t, cl, srv.URL+"/tick?watch=session::", true)
	if !strings.Contains(body, `<dialog id="idle" class="modal-confirm" aria-labelledby="idle-msg" hx-swap-oob="true" open>`) || !strings.Contains(body, `value="session::soon"`) {
		t.Fatalf("a minute before the limit the warning opens: %s", body)
	}
	if _, body := get(t, cl, srv.URL+"/tick?watch=session::soon", true); body != "" {
		t.Fatalf("an open warning is not answered again: %q", body)
	}

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/session", nil)
	req.Header.Set("HX-Request", "true")
	req.Header.Set("X-CSRF-Token", csrfTokenFrom(t, page))
	resp, err := cl.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || strings.Contains(string(b), "hx-swap-oob=\"true\" open") || !strings.Contains(string(b), `value="session::"`) {
		t.Fatalf("Stay signed in answers the closed warning: %d %s", resp.StatusCode, b)
	}
	if _, body := get(t, cl, srv.URL+"/tick?watch=session::soon", true); !strings.Contains(body, `value="session::"`) {
		t.Fatalf("another tab's open warning closes: %q", body)
	}
	c.advance(session.IdleLimit - session.Warning - time.Second)
	if resp, body := get(t, cl, srv.URL+"/tick?watch=session::", true); resp.Header.Get("HX-Redirect") != "" || body != "" {
		t.Fatalf("Stay signed in moved the limit: %q %q", resp.Header.Get("HX-Redirect"), body)
	}
}

// A session in use still ends at the age limit, told why.
func TestAgedSessionIsSignedOut(t *testing.T) {
	c := newClock()
	srv, revoked := sessionServer(t, c)
	cl := loginClient(t, srv.URL)
	const step = 20 * time.Minute
	for i := 1; i < int(session.AgeLimit/step); i++ {
		c.advance(step)
		if resp, _ := get(t, cl, srv.URL+"/wallet", false); resp.StatusCode != 200 {
			t.Fatalf("step %d: a session in use serves, got %d", i, resp.StatusCode)
		}
	}
	c.advance(step)
	resp, _ := get(t, cl, srv.URL+"/wallet", false)
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/login" {
		t.Fatalf("at the age limit: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	if f := flashOf(t, cl, srv.URL); f != "info|You were signed out after 12 hours. Sign in again." {
		t.Errorf("flash = %q", f)
	}
	if revoked.Load() != 1 {
		t.Errorf("revocations = %d, want 1", revoked.Load())
	}
}

// Sign out revokes the refresh token at the kernel (R5.4).
func TestSignOutRevokesTheRefreshToken(t *testing.T) {
	srv, revoked := sessionServer(t, newClock())
	cl := loginClient(t, srv.URL)
	_, page := get(t, cl, srv.URL+"/wallet", false)
	form := url.Values{"gorilla.csrf.Token": {csrfTokenFrom(t, page)}}
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/logout", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := cl.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.Header.Get("Location") != "/login" || revoked.Load() != 1 {
		t.Errorf("sign out: %q, revocations %d", resp.Header.Get("Location"), revoked.Load())
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

// The UI composes one address — handle@<this kernel> — and reads one fact off
// an action address: whether its kernel segment is this kernel's name. Both
// fail closed while the kernel's name is unknown.
func TestKernelIdentityAddressAndLocal(t *testing.T) {
	k := &KernelIdentity{id: client.Health{Handle: "alpha", PublicKey: "PK"}, fetched: time.Now(), ttl: time.Hour}
	if got := k.Address("ana"); got != "ana@alpha" {
		t.Errorf("Address = %q", got)
	}
	if got := k.KeyAddress("ana"); got != "ana@PK" {
		t.Errorf("KeyAddress = %q", got)
	}
	for ref, want := range map[string]bool{
		"sys@alpha/llm/chat": true,
		"ana@alpha/echo":     true,
		"seller@bravo/greet": false,
		"eve@alphabet/x":     false,
		"bare/name":          false,
	} {
		if got := k.Local(ref); got != want {
			t.Errorf("Local(%q) = %v, want %v", ref, got, want)
		}
	}
	unknown := &KernelIdentity{}
	if unknown.Address("ana") != "" || unknown.KeyAddress("ana") != "" || unknown.Local("ana@/x") {
		t.Error("an unknown kernel composes nothing and owns nothing")
	}
}

// Discover is current on its results; an action page is reached from the
// launcher or a row, and belongs to no group.
func TestActiveGroup(t *testing.T) {
	for path, want := range map[string]string{
		"/browse": "browse", "/actions": "", "/actions/panel": "",
		"/inbox": "inbox", "/transactions": "wallet", "/workshop": "workshop",
	} {
		if got := activeGroup(path); got != want {
			t.Errorf("activeGroup(%q) = %q, want %q", path, got, want)
		}
	}
}

// A tick the page abandoned — it navigated while the request was in flight —
// arrives with its context cancelled; its failed health read says nothing
// about the kernel and must not sign the person out.
func TestAnAbandonedTickKeepsTheSession(t *testing.T) {
	k, revoked := revokingKernel(t)
	h := newTestHandler(t, k.URL)
	srv := httptest.NewServer(h)
	defer srv.Close()
	cl := loginClient(t, srv.URL)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest(http.MethodGet, "/tick", nil).WithContext(ctx)
	req.Header.Set("HX-Request", "true")
	u, _ := url.Parse(srv.URL)
	for _, c := range cl.Jar.Cookies(u) {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Header().Get("HX-Redirect") != "" || revoked.Load() != 0 {
		t.Fatalf("an abandoned tick signed out: %q, revocations %d", rec.Header().Get("HX-Redirect"), revoked.Load())
	}
	if resp, _ := get(t, cl, srv.URL+"/wallet", false); resp.StatusCode != 200 {
		t.Errorf("the session must survive an abandoned tick: %d", resp.StatusCode)
	}
}

// rotatingKernel is the fake kernel with a real token lifecycle on top: the
// login's pair (at, rt) is rotated once to (at1, rt1), a dead refresh token is
// refused, and /v1/me refuses "at" once it has expired. holdMe parks the next
// /v1/me presented with "at", holdRefresh the next refresh and holdLogout the
// next revocation, until the test releases them, so the test orders separate
// HTTP requests.
type rotatingKernel struct {
	*httptest.Server
	expired             atomic.Bool
	holdMe, holdRefresh chan chan struct{} // the test receives a release channel when a request parks
	holdLogout          chan chan struct{}
	arm                 func(me, refresh, logout bool)
	mu                  sync.Mutex
	refreshes           int
	revoked             []string
}

// status makes a request off the test goroutine, where t.Fatal may not be
// called: the status, or 0 when the request failed.
func status(cl *http.Client, u string) int {
	resp, err := cl.Get(u)
	if err != nil {
		return 0
	}
	resp.Body.Close()
	return resp.StatusCode
}

func newRotatingKernel(t *testing.T) *rotatingKernel {
	t.Helper()
	base := authKernel()
	t.Cleanup(base.Close)
	k := &rotatingKernel{holdMe: make(chan chan struct{}), holdRefresh: make(chan chan struct{}), holdLogout: make(chan chan struct{})}
	var meHeld, refreshHeld, logoutHeld atomic.Bool
	park := func(armed *atomic.Bool, hold chan chan struct{}) {
		if armed.CompareAndSwap(true, false) {
			release := make(chan struct{})
			hold <- release
			<-release
		}
	}
	k.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/me" && r.Header.Get("Authorization") == "Bearer at":
			if k.expired.Load() {
				w.WriteHeader(http.StatusUnauthorized)
				w.Write([]byte(`{"code":"unauthenticated","error":"token expired"}`))
				return
			}
			park(&meHeld, k.holdMe)
		case r.URL.Path == "/v1/auth/refresh":
			park(&refreshHeld, k.holdRefresh)
			var in struct {
				RefreshToken string `json:"refresh_token"`
			}
			_ = json.NewDecoder(r.Body).Decode(&in)
			k.mu.Lock()
			defer k.mu.Unlock()
			if in.RefreshToken != "rt" || k.refreshes > 0 {
				w.WriteHeader(http.StatusUnauthorized)
				w.Write([]byte(`{"code":"unauthenticated","error":"refresh token revoked"}`))
				return
			}
			k.refreshes++
			w.Write([]byte(`{"access_token":"at1","refresh_token":"rt1"}`))
			return
		case r.URL.Path == "/v1/auth/logout":
			park(&logoutHeld, k.holdLogout)
			var in struct {
				RefreshToken string `json:"refresh_token"`
			}
			_ = json.NewDecoder(r.Body).Decode(&in)
			k.mu.Lock()
			k.revoked = append(k.revoked, in.RefreshToken)
			k.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
			return
		}
		base.Config.Handler.ServeHTTP(w, r)
	}))
	t.Cleanup(k.Server.Close)
	k.arm = func(me, refresh, logout bool) { meHeld.Store(me); refreshHeld.Store(refresh); logoutHeld.Store(logout) }
	return k
}

// The token pair is not a request's to save: a request that loaded the
// session before another rotated its tokens, and finishes after, leaves the
// rotated pair in place, so the next request needs no refresh and survives.
func TestARotationSurvivesAStaleSave(t *testing.T) {
	k := newRotatingKernel(t)
	srv := httptest.NewServer(newTestHandler(t, k.URL))
	defer srv.Close()
	cl := loginClient(t, srv.URL)

	cl.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	k.arm(true, false, false)
	slow := make(chan int)
	go func() { slow <- status(cl, srv.URL+"/wallet") }()
	release := <-k.holdMe // A has loaded its copy, holding "at", and waits on the kernel
	k.expired.Store(true)
	if resp, _ := get(t, cl, srv.URL+"/wallet", false); resp.StatusCode != 200 {
		t.Fatalf("B refreshes and serves: %d", resp.StatusCode)
	}
	close(release)
	if code := <-slow; code != 200 {
		t.Fatalf("A serves: %d", code)
	}
	if resp, _ := get(t, cl, srv.URL+"/wallet", false); resp.StatusCode != 200 {
		t.Fatalf("C after A's late save: %d %q, want the session alive", resp.StatusCode, resp.Header.Get("Location"))
	}
	if k.refreshes != 1 {
		t.Errorf("refreshes = %d, want 1", k.refreshes)
	}
}

// A sign-out that lands while a refresh is in flight stays a sign-out: the
// refresh's late answer recreates nothing, and the pair it carries, which
// nobody now holds, is revoked at the kernel.
func TestASignOutDuringARefreshRevokesTheNewPair(t *testing.T) {
	k := newRotatingKernel(t)
	srv := httptest.NewServer(newTestHandler(t, k.URL))
	defer srv.Close()
	cl := loginClient(t, srv.URL)
	_, page := get(t, cl, srv.URL+"/wallet", false)
	token := csrfTokenFrom(t, page)

	k.expired.Store(true)
	k.arm(false, true, false)
	done := make(chan struct{})
	go func() { status(cl, srv.URL+"/wallet"); close(done) }()
	release := <-k.holdRefresh // A's refresh is at the kernel
	form := url.Values{"gorilla.csrf.Token": {token}}
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/logout", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := cl.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	close(release)
	<-done

	if resp, _ := get(t, cl, srv.URL+"/", false); resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/login" {
		t.Errorf("the signed-out session came back: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if !slices.Contains(k.revoked, "rt1") {
		t.Errorf("revoked %v, want the orphaned rt1 among them", k.revoked)
	}
}

// A refresh that stores its pair while a sign-out's revocation is at the
// kernel leaves no live pair behind: the sign-out takes the entry before it
// calls, so the refresh finds none and revokes its own.
func TestARefreshDuringASignOutsRevocationIsRevoked(t *testing.T) {
	k := newRotatingKernel(t)
	srv := httptest.NewServer(newTestHandler(t, k.URL))
	defer srv.Close()
	cl := loginClient(t, srv.URL)
	_, page := get(t, cl, srv.URL+"/wallet", false)
	token := csrfTokenFrom(t, page)

	k.expired.Store(true)
	k.arm(false, true, true)
	refreshed := make(chan struct{})
	go func() { status(cl, srv.URL+"/wallet"); close(refreshed) }()
	releaseRefresh := <-k.holdRefresh // A's refresh is at the kernel
	signedOut := make(chan struct{})
	go func() {
		form := url.Values{"gorilla.csrf.Token": {token}}
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/logout", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if resp, err := cl.Do(req); err == nil {
			resp.Body.Close()
		}
		close(signedOut)
	}()
	releaseLogout := <-k.holdLogout // the sign-out's revocation is at the kernel
	close(releaseRefresh)
	<-refreshed
	close(releaseLogout)
	<-signedOut

	k.mu.Lock()
	defer k.mu.Unlock()
	if !slices.Contains(k.revoked, "rt1") {
		t.Errorf("revoked %v, want the rotated rt1 among them", k.revoked)
	}
}

// postForm posts a page's form fields with its CSRF token, not following a
// redirect; extra headers name an htmx or script request.
func postForm(t *testing.T, cl *http.Client, u, page string, form url.Values, headers ...string) *http.Response {
	t.Helper()
	form.Set("gorilla.csrf.Token", csrfTokenFrom(t, page))
	req, _ := http.NewRequest(http.MethodPost, u, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	cl.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := cl.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp
}

// A session holds accounts (requirements §5): a signed-in session signs in to
// another through the Sign in dialog — the entry screen is for a signed-out
// visitor alone — which a refusal answers open with the reason and the handle
// typed, and a sign-in adds the account and puts it in use; the avatar menu
// lists the others and switches to one; what a page offers follows the account
// in use; sign-out ends them all and revokes every pair.
func TestASessionSwitchesAccounts(t *testing.T) {
	srv, revoked := sessionServer(t, newClock())
	cl := loginClientAs(t, srv.URL, "sys")
	if resp, _ := get(t, cl, srv.URL+"/login", false); resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") == "/login" {
		t.Fatalf("a signed-in person is not shown the entry screen: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	_, wallet := get(t, cl, srv.URL+"/wallet", false)
	if !strings.Contains(wallet, `<dialog id="signin" class="modal-form" aria-label="Sign in"`) || !strings.Contains(wallet, `data-modal="signin"`) ||
		!strings.Contains(wallet, `icons.svg#log-in"/></svg>Sign in</a>`) || strings.Contains(wallet, "Add account") || strings.Contains(wallet, "Create account") {
		t.Fatal("a signed-in page carries the Sign in dialog, closed, which the avatar menu opens")
	}
	if _, opened := get(t, cl, srv.URL+"/wallet?form=signin&handle=alice", false); !strings.Contains(opened, `<dialog id="signin" class="modal-form" open`) || !strings.Contains(opened, `value="alice"`) {
		t.Fatal("without script ?form=signin&handle= opens the dialog with the handle filled in")
	}
	signIn := func(password string, headers ...string) (*http.Response, string) {
		t.Helper()
		form := url.Values{"handle": {"alice"}, "password": {password}, "gorilla.csrf.Token": {csrfTokenFrom(t, wallet)}}
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/login", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Referer", srv.URL+"/wallet?tab=x")
		for i := 0; i+1 < len(headers); i += 2 {
			req.Header.Set(headers[i], headers[i+1])
		}
		resp, err := cl.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp, string(b)
	}
	resp, body := signIn("wrongpass", "HX-Request", "true")
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `<dialog id="signin" class="modal-form" open aria-label="Sign in" hx-swap-oob="true">`) ||
		!strings.Contains(body, "Invalid handle or password.") || !strings.Contains(body, `value="alice"`) || !strings.Contains(body, `href="/wallet" data-modal-close`) {
		t.Fatalf("a refusal answers the dialog open with the reason and the handle: %d %s", resp.StatusCode, body)
	}
	if resp, _ := signIn("wrongpass"); resp.Header.Get("Location") != "/wallet?form=signin&handle=alice" || flashOf(t, cl, srv.URL) != "error|Invalid handle or password." {
		t.Fatalf("without script a refusal is a flash on the page, the dialog open: %q", resp.Header.Get("Location"))
	}
	if resp, _ := signIn("pw", "HX-Request", "true"); resp.Header.Get("HX-Redirect") == "" {
		t.Fatal("a sign-in in the dialog opens the landing")
	}
	_, page := get(t, cl, srv.URL+"/wallet", false)
	if !strings.Contains(page, `<meta name="account" content="alice@testnet">`) || !strings.Contains(page, `name="juice_account" value="alice@testnet"`) {
		t.Fatal("the account added is in use, and the page names it in its meta and its forms")
	}
	if !strings.Contains(page, `action="/switch"`) || !strings.Contains(page, `name="address" value="sys@testnet"`) {
		t.Error("the avatar menu switches to the other account")
	}
	if resp, _ := get(t, cl, srv.URL+"/admin", false); resp.StatusCode != http.StatusNotFound {
		t.Errorf("Admin follows the account in use: %d", resp.StatusCode)
	}
	resp = postForm(t, cl, srv.URL+"/switch", page, url.Values{"address": {"sys@testnet"}})
	if resp.Header.Get("Location") != "/" {
		t.Fatalf("a switch opens the landing: %q", resp.Header.Get("Location"))
	}
	if resp, _ := get(t, cl, srv.URL+"/admin", false); resp.StatusCode != http.StatusOK {
		t.Errorf("the superuser's Admin is back: %d", resp.StatusCode)
	}
	_, page = get(t, cl, srv.URL+"/wallet", false)
	postForm(t, cl, srv.URL+"/switch", page, url.Values{"address": {"nobody@testnet"}})
	if f := flashOf(t, cl, srv.URL); f != "error|That account is not signed in here." {
		t.Errorf("only an account the session holds: %q", f)
	}
	_, page = get(t, cl, srv.URL+"/wallet", false)
	if resp := postForm(t, cl, srv.URL+"/logout", page, url.Values{}); resp.Header.Get("Location") != "/login" || revoked.Load() != 2 {
		t.Errorf("sign-out ends both accounts: %q, revocations %d", resp.Header.Get("Location"), revoked.Load())
	}
}

// A page opened as one account can do nothing as another (requirements §5):
// a form says nothing was done; a heartbeat of any lane, a fragment and the
// catalog file's request are sent to the landing before anything runs; a
// request naming no account, a navigation, acts as the account in use.
func TestAPageOfAnotherAccountDoesNothing(t *testing.T) {
	var kernelCalls atomic.Int32
	base := authKernel()
	t.Cleanup(base.Close)
	k := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			kernelCalls.Add(1)
		}
		base.Config.Handler.ServeHTTP(w, r)
	}))
	t.Cleanup(k.Close)
	srv := httptest.NewServer(newTestHandler(t, k.URL))
	t.Cleanup(srv.Close)
	cl := loginClientAs(t, srv.URL, "sys")
	_, entry := get(t, cl, srv.URL+"/wallet", false)
	postForm(t, cl, srv.URL+"/login", entry, url.Values{"handle": {"alice"}, "password": {"pw"}})
	_, stale := get(t, cl, srv.URL+"/wallet", false) // open as alice
	postForm(t, cl, srv.URL+"/switch", stale, url.Values{"address": {"sys@testnet"}})

	resp := postForm(t, cl, srv.URL+"/session", stale, url.Values{"juice_account": {"alice@testnet"}})
	if resp.Header.Get("Location") != "/" || !strings.HasPrefix(flashOf(t, cl, srv.URL), "error|Nothing was done: that page was open as alice@testnet") {
		t.Errorf("a form of another account: %q %q", resp.Header.Get("Location"), flashOf(t, cl, srv.URL))
	}
	before := kernelCalls.Load()
	// The wallet's deposit request (wallet.mjs), a script POST naming its page's
	// account by header, prepares nothing for the account switched to.
	if resp := postForm(t, cl, srv.URL+"/wallet/fund", stale, url.Values{"amount": {"1"}}, "X-Juice-Account", "alice@testnet"); resp.StatusCode != http.StatusPreconditionFailed {
		t.Errorf("a stale wallet's deposit request: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	for _, lane := range []string{"watch=session::", "slow=inbox:badge:", "apart=chat:rail:"} {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/tick?"+lane, nil)
		req.Header.Set("HX-Request", "true")
		req.Header.Set("X-Juice-Account", "alice@testnet")
		resp, err := cl.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.Header.Get("HX-Redirect") != "/" || len(b) != 0 {
			t.Errorf("%s of another account: redirect %q, body %q", lane, resp.Header.Get("HX-Redirect"), b)
		}
	}
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/search/catalog.json", nil)
	req.Header.Set("X-Juice-Account", "alice@testnet")
	resp, err := cl.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusPreconditionFailed || resp.Header.Get("Location") != "" {
		t.Errorf("the catalog for another account's page is refused with no redirect, which fetch would follow carrying the header: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	if n := kernelCalls.Load() - before; n != 0 {
		t.Errorf("a page of another account reached the kernel %d times", n)
	}
	req, _ = http.NewRequest(http.MethodGet, srv.URL+"/search/catalog.json", nil)
	req.Header.Set("X-Juice-Account", "sys@testnet")
	if resp, err = cl.Do(req); err != nil || resp.StatusCode != http.StatusOK {
		t.Errorf("the account in use is served: %v %v", resp.StatusCode, err)
	}
	resp.Body.Close()
}

// servesApp is a conversations app that serves every account but
// helper@testnet, with a route and a watcher of its own.
type servesApp struct {
	convApp
	deps *apps.Deps
}

func (a servesApp) Slug() string                { return "rooms" }
func (a servesApp) Landing() string             { return "/rooms" }
func (a servesApp) Serves(r *http.Request) bool { return a.deps.Me(r) != "helper@testnet" }
func (a servesApp) Rail(*http.Request) (template.HTML, error) {
	return `<nav class="rail">rooms</nav>`, nil
}
func (a servesApp) Mount(mux *http.ServeMux, d apps.Deps) {
	*a.deps = d
	mux.HandleFunc("GET /rooms", d.RequireAuth(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("rooms")) }))
	d.Watch("rooms", func(*http.Request, string, string) (template.HTML, error) { return "<i>ticked</i>", nil })
}

// The core asks the conversations app whom it serves, once: an account it
// does not serve lands on Discover, has no rail, and its routes and watchers
// are closed to it.
func TestConversationsServeTheirAccountsAlone(t *testing.T) {
	kernel := authKernel()
	t.Cleanup(kernel.Close)
	app := servesApp{deps: &apps.Deps{}}
	srv := httptest.NewServer(newTestHandler(t, kernel.URL, func(w *testWiring) { w.apps = []apps.App{app} }))
	t.Cleanup(srv.Close)
	for handle, served := range map[string]bool{"alice": true, "helper": false} {
		cl := loginClientAs(t, srv.URL, handle)
		resp, _ := get(t, cl, srv.URL+"/", false)
		_, page := get(t, cl, srv.URL+"/wallet", false)
		_, room := get(t, cl, srv.URL+"/rooms", false)
		_, tick := get(t, cl, srv.URL+"/tick?watch=rooms:x:", true)
		if served != (resp.Header.Get("Location") == "/rooms") || served != strings.Contains(page, "rooms</nav>") ||
			served != (room == "rooms") || served != strings.Contains(tick, "ticked") {
			t.Errorf("%s (served %v): landing %q, rail %v, route %q, tick %q", handle, served, resp.Header.Get("Location"), strings.Contains(page, "rooms</nav>"), room, tick)
		}
	}
}

// An account that holds no money, available and parked both zero, is empty,
// and an empty account's app is its wallet: it lands there, has no rail, and
// the conversations app's routes and watchers are closed to it. Parked money
// is held, and an account whose read failed is not called empty. Its wallet's
// funded watch answers nothing while it is empty and sends the page to the
// landing once money arrives.
func TestAnEmptyAccountsAppIsItsWallet(t *testing.T) {
	var available, locked atomic.Int64
	var fail atomic.Bool
	mux := http.NewServeMux()
	fakeHealth(mux)
	mux.HandleFunc("POST /v1/auth/authorize", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"redirect":"?code=C"}`)) })
	mux.HandleFunc("POST /v1/auth/token", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"access_token":"at","refresh_token":"rt"}`))
	})
	mux.HandleFunc("GET /v1/me", func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		fmt.Fprintf(w, `{"id":"U1","address":{"address":"alice@testnet","principal":"alice@testnet"},"available":%d,"locked":%d}`, available.Load(), locked.Load())
	})
	for _, p := range []string{"GET /v1/withdrawals", "GET /v1/processes", "GET /v1/ledger", "GET /v1/tasks", "GET /v1/actions", "GET /v1/transactions"} {
		mux.HandleFunc(p, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`[]`)) })
	}
	kernel := httptest.NewServer(mux)
	t.Cleanup(kernel.Close)
	app := servesApp{deps: &apps.Deps{}}
	srv := httptest.NewServer(newTestHandler(t, kernel.URL, func(w *testWiring) { w.apps = []apps.App{app} }))
	t.Cleanup(srv.Close)
	cl := loginClientAs(t, srv.URL, "alice")
	for _, tc := range []struct {
		name            string
		available, held int64
		fail, served    bool
	}{
		{"empty", 0, 0, false, false},
		{"funded", 7, 0, false, true},
		{"parked only", 0, 3, false, true},
		{"unread", 0, 0, true, true},
	} {
		available.Store(tc.available)
		locked.Store(tc.held)
		fail.Store(tc.fail)
		resp, _ := get(t, cl, srv.URL+"/", false)
		landing := "/wallet"
		if tc.served {
			landing = "/rooms"
		}
		if got := resp.Header.Get("Location"); got != landing {
			t.Errorf("%s: lands on %q, want %q", tc.name, got, landing)
		}
		if tc.fail {
			continue
		}
		_, page := get(t, cl, srv.URL+"/wallet", false)
		_, room := get(t, cl, srv.URL+"/rooms", false)
		_, tick := get(t, cl, srv.URL+"/tick?watch=rooms:x:", true)
		if tc.served != strings.Contains(page, "rooms</nav>") || tc.served != (room == "rooms") || tc.served != strings.Contains(tick, "ticked") {
			t.Errorf("%s (served %v): rail %v, route %q, tick %q", tc.name, tc.served, strings.Contains(page, "rooms</nav>"), room, tick)
		}
		funded, _ := get(t, cl, srv.URL+"/tick?watch=funded::", true)
		if moved := funded.Header.Get("HX-Redirect"); (moved == "/") != tc.served {
			t.Errorf("%s: the funded watch sends the page to %q", tc.name, moved)
		}
	}
}
