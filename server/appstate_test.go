package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestAppStorePersistAndReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "juice-ui.json")
	a, err := newAppStore(path, "K")
	if err != nil {
		t.Fatal(err)
	}
	if a.load("chat") != nil {
		t.Fatal("empty store should return nil")
	}
	if err := a.save("chat", json.RawMessage(`{"password":"secret"}`)); err != nil {
		t.Fatalf("save: %v", err)
	}

	// The file is 0600 and holds the namespaced blob.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("perms = %o, want 600", perm)
	}

	// A fresh store reloads what was saved.
	b, err := newAppStore(path, "K")
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Password string `json:"password"`
	}
	if err := json.Unmarshal(b.load("chat"), &got); err != nil {
		t.Fatalf("reload unmarshal: %v", err)
	}
	if got.Password != "secret" {
		t.Errorf("reloaded password = %q", got.Password)
	}
}

// TestAppStoreCreatesParentDir checks the fresh-install fix: save creates its
// component home before writing, rather than failing when the directory is absent.
func TestAppStoreCreatesParentDir(t *testing.T) {
	// A path whose parent (…/juice/ui) does not exist yet.
	path := filepath.Join(t.TempDir(), "juice", "ui", "state.json")
	a, err := newAppStore(path, "K")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.save("chat", json.RawMessage(`{"ok":true}`)); err != nil {
		t.Fatalf("save into a missing dir should create it: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("state file not written: %v", err)
	}
}

func TestAppStoreMissingFileAndInMemory(t *testing.T) {
	// Missing file is not an error.
	if _, err := newAppStore(filepath.Join(t.TempDir(), "absent.json"), "K"); err != nil {
		t.Fatalf("missing file should be ok: %v", err)
	}
	// In-memory store (path "") persists within the process but writes nothing.
	a, _ := newAppStore("", "K")
	if err := a.save("x", json.RawMessage(`1`)); err != nil {
		t.Fatalf("in-memory save: %v", err)
	}
	if string(a.load("x")) != "1" {
		t.Errorf("in-memory load = %q", a.load("x"))
	}
}

// Durable state is keyed by the kernel it was provisioned on, never by a name
// (ecosystem-standard §4): a secret minted on one kernel is not found — and so
// not replayed — on another, and both kernels' state survives in one file.
func TestAppStoreIsKeyedByKernel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	a, err := newAppStore(path, "KEYA")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.save("chat", json.RawMessage(`{"password":"on-a"}`)); err != nil {
		t.Fatal(err)
	}
	b, err := newAppStore(path, "KEYB")
	if err != nil {
		t.Fatal(err)
	}
	if b.load("chat") != nil {
		t.Error("kernel B must not see state provisioned on kernel A")
	}
	if err := b.save("chat", json.RawMessage(`{"password":"on-b"}`)); err != nil {
		t.Fatal(err)
	}
	a2, _ := newAppStore(path, "KEYA")
	var pw struct{ Password string }
	if err := json.Unmarshal(a2.load("chat"), &pw); err != nil || pw.Password != "on-a" {
		t.Errorf("kernel A's state must survive kernel B's save: %s", a2.load("chat"))
	}
	raw, _ := os.ReadFile(path)
	var f struct {
		Kernels map[string]json.RawMessage `json:"kernels"`
		Apps    map[string]json.RawMessage `json:"apps"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	if len(f.Kernels) != 2 || f.Apps != nil {
		t.Errorf("the file is keyed by kernel and carries no unkeyed apps: %s", raw)
	}
}

// A state.json written before kernel keying is adopted by the current kernel —
// once, because such a file was always provisioned on the kernel the UI was
// pointed at — and rewritten keyed on the next save.
func TestAppStoreAdoptsLegacyStateOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte(`{"apps":{"chat":{"password":"old"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	a, err := newAppStore(path, "KEYA")
	if err != nil {
		t.Fatal(err)
	}
	if !a.Adopted || string(a.load("chat")) != `{"password":"old"}` {
		t.Errorf("legacy apps must be adopted under the current kernel: adopted=%v blob=%s", a.Adopted, a.load("chat"))
	}
	if err := a.save("chat", a.load("chat")); err != nil {
		t.Fatal(err)
	}
	b, _ := newAppStore(path, "KEYA")
	if b.Adopted {
		t.Error("once rewritten keyed, nothing is left to adopt")
	}
	c, _ := newAppStore(path, "KEYB")
	if c.load("chat") != nil {
		t.Error("the adopted state belongs to the kernel that adopted it")
	}
}
