package server

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/alexedwards/scs/v2"
	"github.com/daios-ai/juice-ui/client"
	"github.com/daios-ai/juice-ui/session"
)

// TestOriginAllowlist verifies the CSRF Origin check: a same-origin or
// operator-trusted Origin passes, an untrusted one is rejected with the
// actionable message (never a raw "Forbidden - origin invalid").
func TestOriginAllowlist(t *testing.T) {
	c := client.New("http://127.0.0.1:1", nil)
	sm := scs.New()
	h, err := New(Options{
		Client:         c,
		Sessions:       session.NewManager(sm, c),
		Renderer:       mustRenderer(t),
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		CSRFKey:        bytes.Repeat([]byte("k"), 32),
		Secure:         false,
		TrustedOrigins: []string{"trusted.example:4321"},
		Identity:       NewKernelIdentity(c, "KTEST", time.Minute, slog.New(slog.NewTextHandler(io.Discard, nil))),
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	defer srv.Close()

	jar, _ := cookiejar.New(nil)
	cl := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	page := getBody(t, cl, srv.URL+"/login")
	tok := csrfTokenFrom(t, page)

	post := func(origin string) *http.Response {
		form := url.Values{"theme": {"dracula"}, "gorilla.csrf.Token": {tok}}
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/theme", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		resp, err := cl.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}

	// Same-origin (the test server's own host) passes.
	if resp := post(srv.URL); resp.StatusCode == http.StatusForbidden {
		resp.Body.Close()
		t.Error("same-origin POST must not be rejected")
	} else {
		resp.Body.Close()
	}
	// Operator-trusted host passes.
	if resp := post("http://trusted.example:4321"); resp.StatusCode == http.StatusForbidden {
		resp.Body.Close()
		t.Error("trusted-origin POST must not be rejected")
	} else {
		resp.Body.Close()
	}
	// Untrusted host is rejected with the actionable message.
	resp := post("http://evil.example")
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("untrusted origin = %d want 403", resp.StatusCode)
	}
	if !strings.Contains(string(body), "JUICE_UI_TRUSTED_ORIGINS") {
		t.Error("origin rejection must render the actionable message naming the env var")
	}
}

func mustRenderer(t *testing.T) *Renderer {
	t.Helper()
	r, err := NewRenderer(playKernel)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// TestCSPHeaderPresent verifies the strict Content-Security-Policy (and related
// hardening headers) are served on every response, per requirements §8.
func TestCSPHeaderPresent(t *testing.T) {
	srv := httptest.NewServer(newTestHandler(t, "http://127.0.0.1:1"))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/login")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	csp := resp.Header.Get("Content-Security-Policy")
	for _, want := range []string{"script-src 'self'", "frame-ancestors 'none'", "object-src 'none'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP missing %q: %q", want, csp)
		}
	}
	// Referrer-Policy MUST NOT be "no-referrer": that makes browsers send
	// `Origin: null` on same-origin form POSTs, which gorilla/csrf rejects and
	// which would break every login. "same-origin" still leaks no referrer to
	// other origins while preserving the Origin header for same-origin requests.
	if rp := resp.Header.Get("Referrer-Policy"); rp != "same-origin" {
		t.Errorf("Referrer-Policy = %q want same-origin (no-referrer breaks CSRF via Origin: null)", rp)
	}
	if resp.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Error("missing X-Content-Type-Options: nosniff")
	}
}

// Images from other hosts are admitted on a page that shows a message's
// images alone, a room's (showImages); every other page keeps images to this
// server, and the rest of the policy is the same on both.
func TestOnlyARoomAdmitsWebImages(t *testing.T) {
	kernel := authKernel()
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClient(t, srv.URL)
	for _, p := range []string{"/login", "/wallet", "/browse", "/settings"} {
		resp, err := cl.Get(srv.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if c := resp.Header.Get("Content-Security-Policy"); !strings.Contains(c, "img-src 'self' data:;") || strings.Contains(c, "https:") {
			t.Errorf("%s admits images from elsewhere: %q", p, c)
		}
	}
	rec := httptest.NewRecorder()
	showImages(rec)
	room := rec.Header().Get("Content-Security-Policy")
	if !strings.Contains(room, "img-src 'self' data: https:;") || strings.Replace(room, " https:", "", 1) != csp(false) {
		t.Errorf("a room admits https images and nothing else more: %q", room)
	}
}

// TestFormQueryOpensDialogNoJS verifies the no-JS floating-form fallback: a
// whitelisted ?form= renders the matching dialog with the [open] attribute.
func TestFormQueryOpensDialogNoJS(t *testing.T) {
	kernel := authKernel()
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()
	cl := loginClient(t, srv.URL)

	body := getBody(t, cl, srv.URL+"/workshop?form=action-create")
	if !strings.Contains(body, `id="form-action-create"`) {
		t.Fatal("action page missing the create dialog")
	}
	if !strings.Contains(body, `class="modal-form" open`) {
		t.Error("?form=action-create must render the dialog with the open attribute (no-JS fallback)")
	}

	// A non-whitelisted form value must NOT open any dialog.
	body2 := getBody(t, cl, srv.URL+"/actions?form=bogus")
	if strings.Contains(body2, `class="modal-form" open`) {
		t.Error("unknown ?form value must not open a dialog")
	}
}

// Every script request a page makes names the account it was rendered for
// (requirements §5): htmx's through the one configRequest handler, and every
// native request, GET or POST, by its own header, so a page opened before a
// switch neither reads nor acts as the account switched to.
func TestEveryScriptRequestNamesItsAccount(t *testing.T) {
	for _, f := range []string{"static/app.js", "../wallet/wallet.mjs"} {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		js := string(src)
		if f == "static/app.js" && !strings.Contains(js, `e.detail.headers["X-Juice-Account"] = account`) {
			t.Error("htmx's requests must carry the page's account")
		}
		if f == "static/app.js" && !strings.Contains(js, `r.status === 412 ? (location.reload(), null)`) {
			t.Error("the catalog's request reloads a page of another account on 412")
		}
		for _, loc := range regexp.MustCompile(`\bfetch(Fn)?\(`).FindAllStringIndex(js, -1) {
			call := js[loc[0]:] // the call, to the end of its statement
			if end := strings.Index(call, ";\n"); end > 0 {
				call = call[:end]
			}
			if !strings.Contains(call, "X-Juice-Account") {
				t.Errorf("%s: a script request names no account: %s", f, call)
			}
		}
	}
}
