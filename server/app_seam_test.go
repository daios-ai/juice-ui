package server

import (
	"bytes"
	"context"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/alexedwards/scs/v2"
	"github.com/daios-ai/juice-ui/apps"
	"github.com/daios-ai/juice-ui/client"
	"github.com/daios-ai/juice-ui/session"
)

// seamFake is a minimal app module for exercising the core seam.
type seamFake struct{ started bool }

func (f *seamFake) Slug() string { return "seam" }
func (f *seamFake) Nav(handle string, superuser bool) []apps.NavEntry {
	return []apps.NavEntry{{Slug: "seam", Label: "Seam", Href: "/seam", Match: "/seam"}}
}
func (f *seamFake) Mount(mux *http.ServeMux, d apps.Deps) {
	mux.HandleFunc("GET /seam", d.RequireAuth(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
}
func (f *seamFake) Templates() fs.FS {
	return fstest.MapFS{"templates/_seampartial.html": {Data: []byte(`{{define "_seampartial"}}seam-ok{{end}}`)}}
}
func (f *seamFake) Start(ctx context.Context) error { f.started = true; return nil }
func (f *seamFake) Stop(ctx context.Context) error  { return nil }

// TestAppSeamMerges proves the renderer merges a app's partials into the
// shared base set, so app pages can reference them.
func TestAppSeamMerges(t *testing.T) {
	fake := &seamFake{}
	r, err := NewRenderer(playKernel, fake.Templates())
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	if err := r.Fragment(rec, http.StatusOK, "_seampartial", nil); err != nil {
		t.Fatalf("app partial not merged: %v", err)
	}
	if !strings.Contains(rec.Body.String(), "seam-ok") {
		t.Fatalf("app partial did not render: %q", rec.Body.String())
	}
}

// TestAppSeamMountsAndStarts proves server.New starts each app and mounts
// its routes behind auth (a route that exists redirects to login when
// unauthenticated; a missing route would 404).
func TestAppSeamMountsAndStarts(t *testing.T) {
	fake := &seamFake{}
	r, err := NewRenderer(playKernel, fake.Templates())
	if err != nil {
		t.Fatal(err)
	}
	c := client.New("http://127.0.0.1:1", nil)
	sm := scs.New()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	h, err := New(Options{
		Client:   c,
		Sessions: session.NewManager(sm, c),
		Renderer: r,
		Logger:   log,
		CSRFKey:  bytes.Repeat([]byte("k"), 32),
		Secure:   false,
		Apps:     []apps.App{fake},
		Identity: NewKernelIdentity(c, "KTEST", time.Minute, log),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !fake.started {
		t.Error("app Start was not called")
	}
	srv := httptest.NewServer(h)
	defer srv.Close()
	cl := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := cl.Get(srv.URL + "/seam")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		t.Fatal("app route /seam not mounted (404)")
	}
	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("unauthenticated /seam status = %d, want 303 redirect to login", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/login" {
		t.Errorf("redirect Location = %q, want /login", loc)
	}
}
