package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func recordsKernel(t *testing.T, tokens map[string]string) *Client {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/me", func(w http.ResponseWriter, r *http.Request) {
		addr, ok := tokens[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]
		if !ok {
			http.Error(w, `{"error":"token expired"}`, http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"address":{"address":"` + addr + `"}}`))
	})
	mux.HandleFunc("POST /v1/auth/refresh", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"access_token":"new","refresh_token":"next"}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return New(srv.URL, nil)
}

// A login's session runs fn; a token the kernel refuses is refreshed in the file
// first; a file holding another login is refused.
func TestWithLogin(t *testing.T) {
	dir := t.TempDir()
	c := recordsKernel(t, map[string]string{"new": "ann@k", "bobs": "bob@k"})
	path := filepath.Join(dir, "credentials", "ann@k.json")
	if err := c.WithLogin(context.Background(), dir, "ann@k", nil); err == nil {
		t.Fatal("no file is no session")
	}
	if err := WriteCredential(path, Tokens{AccessToken: "old", RefreshToken: "r"}, "P"); err != nil {
		t.Fatal(err)
	}
	var used string
	if err := c.WithLogin(context.Background(), dir, "ann@k", func(tok string) error { used = tok; return nil }); err != nil || used != "new" {
		t.Fatalf("used %q: %v", used, err)
	}
	cred := map[string]string{}
	b, _ := os.ReadFile(path)
	_ = json.Unmarshal(b, &cred)
	if cred["token"] != "new" || cred["refresh_token"] != "next" || cred["principal_id"] != "P" {
		t.Errorf("credential = %v", cred)
	}
	want := errors.New("fn")
	if err := c.WithLogin(context.Background(), dir, "ann@k", func(string) error { return want }); !errors.Is(err, want) {
		t.Errorf("fn's error: %v", err)
	}
	_ = WriteCredential(path, Tokens{AccessToken: "bobs"}, "P")
	if err := c.WithLogin(context.Background(), dir, "ann@k", func(string) error { return nil }); err == nil || !strings.Contains(err.Error(), "bob@k") {
		t.Errorf("another login's session: %v", err)
	}
}

func TestKernelEndpoint(t *testing.T) {
	dir := t.TempDir()
	if _, err := KernelEndpoint(dir, "alpha"); err == nil {
		t.Error("no file")
	}
	_ = os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"kernels":{"alpha":{"endpoint":"http://k"}}}`), 0o600)
	if e, err := KernelEndpoint(dir, "alpha"); err != nil || e != "http://k" {
		t.Errorf("alpha = %q %v", e, err)
	}
	if _, err := KernelEndpoint(dir, "beta"); err == nil {
		t.Error("an unrecorded kernel")
	}
}

// recordKernelServer answers /health and /v1/me as kernel "alpha" with key K,
// /v1/me naming *me.
func recordKernelServer(t *testing.T, me *string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/me", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":"P1","address":{"address":"` + *me + `"}}`))
	})
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"handle":"alpha","public_key":"K","network_fingerprint":"F","network":"play","decimals":6,"symbol":"fUSD"}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// RecordLogin writes what `juice auth login` writes (ecosystem-standard §4):
// the kernel under the name it calls itself, with its six fields, and the
// login's credential at 0600 with the session and the principal; nothing
// names `current`, which belongs to a person at a terminal. A session the
// kernel answers for another account writes nothing.
func TestRecordLogin(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "client")
	me := "someone@alpha"
	srv := recordKernelServer(t, &me)
	c := New(srv.URL, nil)
	if err := c.RecordLogin(context.Background(), dir, "ann@alpha", Tokens{AccessToken: "a", RefreshToken: "r"}); err == nil || !strings.Contains(err.Error(), "someone@alpha") {
		t.Fatalf("another account's session: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("a refused session writes nothing")
	}
	me = "ann@alpha"
	if err := c.RecordLogin(context.Background(), dir, "ann@alpha", Tokens{AccessToken: "a", RefreshToken: "r"}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "credentials", "ann@alpha.json")
	st, err := os.Stat(path)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("credential: %v", err)
	}
	cred := map[string]string{}
	b, _ := os.ReadFile(path)
	_ = json.Unmarshal(b, &cred)
	if len(cred) != 3 || cred["token"] != "a" || cred["refresh_token"] != "r" || cred["principal_id"] != "P1" {
		t.Errorf("credential = %v", cred)
	}
	cfg := map[string]json.RawMessage{}
	b, _ = os.ReadFile(filepath.Join(dir, "config.json"))
	_ = json.Unmarshal(b, &cfg)
	if _, has := cfg["current"]; has {
		t.Error("the UI must not select a login")
	}
	var kernels map[string]map[string]any
	_ = json.Unmarshal(cfg["kernels"], &kernels)
	if rec := kernels["alpha"]; rec["endpoint"] != srv.URL || rec["public_key"] != "K" || rec["world_fingerprint"] != "F" ||
		rec["network"] != "play" || rec["decimals"] != 6.0 || rec["symbol"] != "fUSD" || len(rec) != 6 {
		t.Errorf("kernel record = %v", rec)
	}
}

// The kernel record follows `juice kernel add`: a kernel is its key. A record
// with the same key is the same kernel, refreshed from /health — a moved
// endpoint, a field an older client never stored, a stale symbol — while
// `current` and the other kernels are kept; a record with another key is
// refused and nothing is written.
func TestRecordKernelFollowsKernelAdd(t *testing.T) {
	dir := t.TempDir()
	me := "ann@alpha"
	srv := recordKernelServer(t, &me)
	c := New(srv.URL, nil)
	path := filepath.Join(dir, "config.json")
	write := func(rec string) {
		_ = os.WriteFile(path, []byte(`{"current":"bob@other","kernels":{"other":{"endpoint":"http://o","public_key":"O"},"alpha":`+rec+`}}`), 0o600)
	}
	write(`{"endpoint":"http://old:1","public_key":"K","network":"play","decimals":6,"symbol":"fUSDT"}`)
	if err := c.RecordKernel(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Current string                    `json:"current"`
		Kernels map[string]map[string]any `json:"kernels"`
	}
	b, _ := os.ReadFile(path)
	_ = json.Unmarshal(b, &cfg)
	if cfg.Current != "bob@other" || cfg.Kernels["other"]["public_key"] != "O" {
		t.Fatalf("other fields must be kept: %s", b)
	}
	if rec := cfg.Kernels["alpha"]; rec["endpoint"] != srv.URL || rec["world_fingerprint"] != "F" || rec["symbol"] != "fUSD" || len(rec) != 6 {
		t.Fatalf("the same key is refreshed from /health: %v", rec)
	}
	write(`{"endpoint":"` + srv.URL + `","public_key":"OTHER"}`)
	before, _ := os.ReadFile(path)
	if err := c.RecordKernel(context.Background(), dir); err == nil || !strings.Contains(err.Error(), "another key") {
		t.Fatalf("want refusal, got %v", err)
	}
	if after, _ := os.ReadFile(path); string(before) != string(after) {
		t.Fatal("a refused record must not be written")
	}
}

// The record's read-modify-write waits for the installation's config.json.lock,
// the lock the command line holds while it writes the same file.
func TestRecordKernelHoldsTheLock(t *testing.T) {
	dir := t.TempDir()
	me := "ann@alpha"
	c := New(recordKernelServer(t, &me).URL, nil)
	held, err := os.OpenFile(filepath.Join(dir, "config.json.lock"), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_ = syscall.Flock(int(held.Fd()), syscall.LOCK_EX)
	done := make(chan error, 1)
	go func() { done <- c.RecordKernel(context.Background(), dir) }()
	select {
	case err := <-done:
		t.Fatalf("wrote under another writer's lock: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	_ = held.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
