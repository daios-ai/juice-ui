package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestAppStorePersistAndReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "juice-ui.json")
	a, err := newAppStore(path)
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
	b, err := newAppStore(path)
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

func TestAppStoreMissingFileAndInMemory(t *testing.T) {
	// Missing file is not an error.
	if _, err := newAppStore(filepath.Join(t.TempDir(), "absent.json")); err != nil {
		t.Fatalf("missing file should be ok: %v", err)
	}
	// In-memory store (path "") persists within the process but writes nothing.
	a, _ := newAppStore("")
	if err := a.save("x", json.RawMessage(`1`)); err != nil {
		t.Fatalf("in-memory save: %v", err)
	}
	if string(a.load("x")) != "1" {
		t.Errorf("in-memory load = %q", a.load("x"))
	}
}
