package agents

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/daios-ai/juice-ui/client"
)

// fakeKernel answers the five routes a sign-in and an account creation use. The
// password it accepts is "pw"; it records the handles created and every path
// asked, and answers /v1/me with the address it was told to. A handle starting
// with "taken", or listed in held, already exists.
type fakeKernel struct {
	srv     *httptest.Server
	mu      sync.Mutex
	created []string
	paths   []string
	held    map[string]bool
	me      string
	health  string
}

func newKernel(t *testing.T) *fakeKernel {
	t.Helper()
	k := &fakeKernel{me: "research@testnet",
		health: `{"status":"ok","handle":"testnet","public_key":"KEY","network":"play","network_fingerprint":"FP","decimals":6,"symbol":"fUSD"}`}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(k.health)) })
	mux.HandleFunc("POST /v1/users", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		k.mu.Lock()
		dup := strings.HasPrefix(body["handle"], "taken") || k.held[body["handle"]]
		k.created = append(k.created, body["handle"])
		k.mu.Unlock()
		if dup {
			http.Error(w, `{"error":"account: already exists"}`, http.StatusUnprocessableEntity)
			return
		}
		w.Write([]byte(`{"id":"U1","address":{"address":"` + body["handle"] + `"}}`))
	})
	mux.HandleFunc("POST /v1/auth/authorize", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["password"] != "pw" {
			http.Error(w, `{"error":"invalid credentials"}`, http.StatusUnauthorized)
			return
		}
		w.Write([]byte(`{"redirect":"?code=C"}`))
	})
	mux.HandleFunc("POST /v1/auth/token", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"access_token":"AT","refresh_token":"RT"}`))
	})
	mux.HandleFunc("GET /v1/me", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"id":"P1","address":{"address":"` + k.me + `"}}`))
	})
	k.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		k.mu.Lock()
		k.paths = append(k.paths, r.Method+" "+r.URL.Path)
		k.mu.Unlock()
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(k.srv.Close)
	return k
}

// kernelApp is testApp with a fake kernel and a client directory.
func kernelApp(t *testing.T) (*Agents, *fakeKernel, *fakePrograms, *capture) {
	t.Helper()
	a, fl, cap := testApp(t)
	k := newKernel(t)
	a.client = filepath.Join(t.TempDir(), "client")
	a.deps.Client = client.New(k.srv.URL, nil)
	return a, k, fl, cap
}

func readJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func kernelRecord(t *testing.T, a *Agents) map[string]any {
	t.Helper()
	return readJSON(t, filepath.Join(a.client, "config.json"))["kernels"].(map[string]any)["testnet"].(map[string]any)
}

// A sign-in writes what `juice auth login` writes (ecosystem-standard §4): the
// kernel under the name it calls itself, with its five fields, and the login's
// credential at 0600 with the session and the principal. Nothing names
// `current`, which belongs to a person at a terminal.
func TestSignInWritesTheClientRecords(t *testing.T) {
	a, k, _, _ := kernelApp(t)
	if err := a.signIn(context.Background(), "research", "pw"); err != nil {
		t.Fatal(err)
	}
	cred := a.credPath("research")
	st, err := os.Stat(cred)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("credential: %v, mode %v", err, st.Mode())
	}
	want := map[string]any{"token": "AT", "refresh_token": "RT", "principal_id": "P1"}
	if got := readJSON(t, cred); len(got) != 3 || got["token"] != "AT" || got["refresh_token"] != "RT" || got["principal_id"] != "P1" {
		t.Fatalf("credential = %v, want %v", got, want)
	}
	cfg := readJSON(t, filepath.Join(a.client, "config.json"))
	if _, has := cfg["current"]; has {
		t.Fatal("the UI must not select a login")
	}
	rec := kernelRecord(t, a)
	if rec["endpoint"] != k.srv.URL || rec["public_key"] != "KEY" || rec["world_fingerprint"] != "FP" ||
		rec["network"] != "play" || rec["decimals"] != 6.0 || rec["symbol"] != "fUSD" || len(rec) != 6 {
		t.Fatalf("kernel record = %v", rec)
	}
	// A second sign-in replaces the session in place.
	if err := a.signIn(context.Background(), "research", "pw"); err != nil {
		t.Fatal(err)
	}
	if got := readJSON(t, cred); got["token"] != "AT" {
		t.Fatalf("second sign-in: %v", got)
	}
}

// The kernel record follows `juice kernel add`: a kernel is its key. A record
// with the same key is the same kernel, refreshed from /health — a moved
// endpoint, a field an older client never stored, a stale symbol — while
// `current` and the other kernels are kept; a record with another key is
// refused and nothing is written, the credential included.
func TestRecordKernelFollowsKernelAdd(t *testing.T) {
	a, k, _, _ := kernelApp(t)
	path := filepath.Join(a.client, "config.json")
	write := func(rec string) {
		t.Helper()
		_ = os.MkdirAll(a.client, 0o700)
		_ = os.WriteFile(path, []byte(`{"current":"alice@other","kernels":{"other":{"endpoint":"http://o","public_key":"O"},"testnet":`+rec+`}}`), 0o600)
	}
	write(`{"endpoint":"http://old:1","public_key":"KEY","network":"play","decimals":6,"symbol":"fUSDT"}`)
	if err := a.signIn(context.Background(), "research", "pw"); err != nil {
		t.Fatal(err)
	}
	cfg := readJSON(t, path)
	if cfg["current"] != "alice@other" || cfg["kernels"].(map[string]any)["other"].(map[string]any)["public_key"] != "O" {
		t.Fatalf("other fields must be kept: %v", cfg)
	}
	rec := kernelRecord(t, a)
	if rec["endpoint"] != k.srv.URL || rec["world_fingerprint"] != "FP" || rec["symbol"] != "fUSD" || len(rec) != 6 {
		t.Fatalf("the same key is refreshed from /health: %v", rec)
	}
	write(`{"endpoint":"` + k.srv.URL + `","public_key":"OTHER","world_fingerprint":"FP"}`)
	_ = os.Remove(a.credPath("research"))
	before, _ := os.ReadFile(path)
	if err := a.signIn(context.Background(), "research", "pw"); err == nil || !strings.Contains(err.Error(), "another key") {
		t.Fatalf("want refusal, got %v", err)
	}
	if after, _ := os.ReadFile(path); string(before) != string(after) {
		t.Fatal("a refused record must not be written")
	}
	if _, err := os.Stat(a.credPath("research")); !os.IsNotExist(err) {
		t.Fatal("no credential on a refused kernel record")
	}
}

// The record's read-modify-write waits for the installation's config.json.lock,
// the lock the command line holds while it writes the same file.
func TestRecordKernelHoldsTheLock(t *testing.T) {
	a, _, _, _ := kernelApp(t)
	_ = os.MkdirAll(a.client, 0o700)
	held, err := os.OpenFile(filepath.Join(a.client, "config.json.lock"), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_ = syscall.Flock(int(held.Fd()), syscall.LOCK_EX)
	done := make(chan error, 1)
	go func() { done <- a.deps.Client.RecordKernel(context.Background(), a.client) }()
	select {
	case err := <-done:
		t.Fatalf("wrote under another writer's lock: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	_ = held.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(a.client, "config.json")); err != nil {
		t.Fatal("written once the lock was released")
	}
}

// A refused password, and a session the kernel answers for another address,
// write nothing.
func TestSignInRefusalsWriteNothing(t *testing.T) {
	a, k, _, _ := kernelApp(t)
	if err := a.signIn(context.Background(), "research", "wrong"); !errors.Is(err, client.ErrUnauthenticated) {
		t.Fatalf("want the kernel's 401, got %v", err)
	}
	k.me = "someone@testnet"
	if err := a.signIn(context.Background(), "research", "pw"); err == nil || !strings.Contains(err.Error(), "someone@testnet") {
		t.Fatalf("want a refusal naming the other address, got %v", err)
	}
	if _, err := os.Stat(a.client); !os.IsNotExist(err) {
		t.Fatal("a refused sign-in must write nothing")
	}
}
