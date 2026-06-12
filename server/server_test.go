package server

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/alexedwards/scs/v2"
	"github.com/daios-ai/juice-ui/client"
	"github.com/daios-ai/juice-ui/session"
)

// newTestHandler builds the full BFF handler pointed at the given kernel URL.
func newTestHandler(t *testing.T, kernelURL string) http.Handler {
	t.Helper()
	r, err := NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	c := client.New(kernelURL, nil)
	sm := scs.New()
	h, err := New(Options{
		Client:   c,
		Sessions: session.NewManager(sm, c),
		Renderer: r,
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		CSRFKey:  bytes.Repeat([]byte("k"), 32),
		Secure:   false,
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

func TestUnauthRedirectsToLogin(t *testing.T) {
	srv := httptest.NewServer(newTestHandler(t, "http://127.0.0.1:1"))
	defer srv.Close()
	cl := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for _, path := range []string{"/", "/discover", "/studio", "/processes", "/transactions", "/steps"} {
		resp, err := cl.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/login" {
			t.Errorf("%s: status=%d loc=%q want 303 /login", path, resp.StatusCode, resp.Header.Get("Location"))
		}
	}
}

func TestThemeStampedNoFlash(t *testing.T) {
	srv := httptest.NewServer(newTestHandler(t, "http://127.0.0.1:1"))
	defer srv.Close()
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/login", nil)
	req.AddCookie(&http.Cookie{Name: "theme", Value: "dark"})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `data-theme="dark"`) {
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

