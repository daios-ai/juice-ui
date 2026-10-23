package apps

import (
	"context"
	"io/fs"
	"net/http"
	"strings"
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
	if n := feat.Nav("sys", true); len(n) != 1 {
		t.Fatalf("superuser Nav = %d entries, want 1", len(n))
	}
	if n := feat.Nav("alice", false); n != nil {
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

// TestNavEntryCarriesLive documents the seam field an app sets to have its
// badge kept current by the heartbeat, under its slug.
func TestNavEntryCarriesLive(t *testing.T) {
	e := NavEntry{Slug: "chat", Live: true, Badge: 3}
	if !e.Live || e.Badge != 3 {
		t.Fatalf("NavEntry badge fields = %+v", e)
	}
}

// Badge is the heartbeat's answer for a nav badge: every copy of the badge by
// class, and the watch input naming the count now shown; empty when zero so
// the badge hides.
func TestBadgeAnswer(t *testing.T) {
	got := string(Badge("chat", "Chat", 3))
	for _, want := range []string{`class="badge badge-chat"`, `aria-label="Chat"`, `hx-swap-oob="outerHTML:.badge-chat"`, `>3</span>`,
		`id="watch-chat" name="slow" value="chat:badge:3" hx-swap-oob="true"`} {
		if !strings.Contains(got, want) {
			t.Errorf("Badge missing %q: %s", want, got)
		}
	}
	if got := string(Badge("inbox", "Waiting tasks", 0)); !strings.Contains(got, `hx-swap-oob="outerHTML:.badge-inbox"></span>`) || !strings.Contains(got, `value="inbox:badge:0"`) {
		t.Errorf("zero badge must render empty: %s", got)
	}
}
