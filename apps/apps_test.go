package apps

import (
	"context"
	"io/fs"
	"net/http"
	"path/filepath"
	"runtime"
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

// TestNavEntryCarriesBadgeURL documents the seam field an app sets to get a live,
// core-polled badge.
func TestNavEntryCarriesBadgeURL(t *testing.T) {
	e := NavEntry{Slug: "chat", BadgeURL: "/chat/badge", Badge: 3}
	if e.BadgeURL != "/chat/badge" || e.Badge != 3 {
		t.Fatalf("NavEntry badge fields = %+v", e)
	}
}

func TestUserDataDir(t *testing.T) {
	getenv := func(m map[string]string) func(string) string {
		return func(k string) string { return m[k] }
	}
	if runtime.GOOS == "linux" {
		// XDG_DATA_HOME wins when set.
		got, err := UserDataDir(getenv(map[string]string{"XDG_DATA_HOME": "/xdg"}))
		if err != nil || got != filepath.Join("/xdg", "juice-ui") {
			t.Errorf("XDG_DATA_HOME: got %q err %v", got, err)
		}
		// Else ~/.local/share.
		got, err = UserDataDir(getenv(map[string]string{"HOME": "/home/x"}))
		if err != nil || got != filepath.Join("/home/x", ".local", "share", "juice-ui") {
			t.Errorf("HOME fallback: got %q err %v", got, err)
		}
		// Neither set → error, not a bogus relative path.
		if _, err := UserDataDir(getenv(nil)); err == nil {
			t.Error("expected error when neither XDG_DATA_HOME nor HOME is set")
		}
		return
	}
	// Non-Linux: coincides with the config dir; just require a non-empty, absolute path.
	got, err := UserDataDir(getenv(nil))
	if err != nil || !filepath.IsAbs(got) {
		t.Errorf("UserDataDir = %q err %v, want an absolute path", got, err)
	}
}
