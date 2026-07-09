package apps

import (
	"context"
	"io/fs"
	"net/http"
	"testing"
	"testing/fstest"
)

// fakeApp is a minimal App used to prove the interface is satisfiable by
// an out-of-tree module and that the core can drive it.
type fakeApp struct {
	started, stopped bool
	nav              []NavEntry
}

func (f *fakeApp) Slug() string { return "fake" }
func (f *fakeApp) Nav(handle string, superuser bool) []NavEntry {
	if superuser {
		return f.nav
	}
	return nil
}
func (f *fakeApp) Mount(mux *http.ServeMux, d Deps) {
	mux.HandleFunc("GET /fake", func(w http.ResponseWriter, r *http.Request) {})
}
func (f *fakeApp) Templates() fs.FS {
	return fstest.MapFS{"templates/_fake.html": {Data: []byte(`{{define "_fake"}}x{{end}}`)}}
}
func (f *fakeApp) Start(ctx context.Context) error { f.started = true; return nil }
func (f *fakeApp) Stop(ctx context.Context) error  { f.stopped = true; return nil }

func TestAppInterface(t *testing.T) {
	var feat App = &fakeApp{nav: []NavEntry{{Slug: "fake", Label: "fake", Href: "/fake", Match: "/fake"}}}

	if got := feat.Slug(); got != "fake" {
		t.Fatalf("Slug = %q, want fake", got)
	}
	// Self-gating: superuser sees the entry, others don't.
	if n := feat.Nav("@sys", true); len(n) != 1 {
		t.Fatalf("superuser Nav = %d entries, want 1", len(n))
	}
	if n := feat.Nav("@alice", false); n != nil {
		t.Fatalf("non-superuser Nav = %v, want nil", n)
	}
	// Templates are reachable and glob-able the way the renderer expects.
	if files, _ := fs.Glob(feat.Templates(), "templates/*.html"); len(files) != 1 {
		t.Fatalf("Templates glob = %v, want 1 file", files)
	}
	// Lifecycle hooks run.
	if err := feat.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := feat.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if f := feat.(*fakeApp); !f.started || !f.stopped {
		t.Fatalf("lifecycle not invoked: started=%v stopped=%v", f.started, f.stopped)
	}
}
