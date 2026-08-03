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
	"testing"

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
	r, err := NewRenderer()
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

// TestPaletteRegistryInPage verifies the command registry is embedded as inert
// JSON and gated: admin commands appear only for sys.
func TestPaletteRegistryInPage(t *testing.T) {
	kernel := authKernel()
	defer kernel.Close()
	srv := httptest.NewServer(newTestHandler(t, kernel.URL))
	defer srv.Close()

	alice := getBody(t, loginClientAs(t, srv.URL, "alice"), srv.URL+"/browse")
	if !strings.Contains(alice, `id="command-registry"`) {
		t.Error("page must embed the command registry")
	}
	if !strings.Contains(alice, `application/json`) {
		t.Error("command registry must be an inert application/json script block")
	}
	if strings.Contains(alice, `"admin users"`) {
		t.Error("non-superuser registry must not include admin commands")
	}

	sys := getBody(t, loginClientAs(t, srv.URL, "sys"), srv.URL+"/browse")
	if !strings.Contains(sys, `admin users`) {
		t.Error("superuser registry must include admin commands")
	}
}
