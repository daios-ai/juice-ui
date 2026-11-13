package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/daios-ai/juice-ui/client"
	chat "github.com/daios-ai/juice-ui/services/chat"
)

func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().String()
}

// First start writes the schema and every key; a later start reads the file over
// the defaults, and a key the schema does not know stops it.
func TestSetupWritesAndReadsTheConfiguration(t *testing.T) {
	h := homeAt(t.TempDir())
	cfg, err := h.setup()
	if err != nil || cfg.ListenAddr != "127.0.0.1:8090" || cfg.base() != "http://127.0.0.1:8090" {
		t.Fatalf("defaults: %+v %v", cfg, err)
	}
	if _, err := os.Stat(filepath.Join(h.root, "schemas", schemaFile)); err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if b, _ := os.ReadFile(h.path("config.json")); json.Unmarshal(b, &doc) != nil || doc["listen_addr"] != "127.0.0.1:8090" || doc["$schema"] == nil {
		t.Fatalf("every key is written: %v", doc)
	}
	var sch struct {
		Properties map[string]struct {
			Description string `json:"description"`
		} `json:"properties"`
	}
	if err := json.Unmarshal([]byte(schema), &sch); err != nil || len(sch.Properties) != 3 {
		t.Fatalf("schema: %v", err)
	}
	_ = os.WriteFile(h.path("config.json"), []byte(`{"public_url":"https://chat.example/"}`), 0o600)
	if cfg, err := h.setup(); err != nil || cfg.base() != "https://chat.example" || cfg.ListenAddr != "127.0.0.1:8090" {
		t.Errorf("a key left out takes its default: %+v %v", cfg, err)
	}
	_ = os.WriteFile(h.path("config.json"), []byte(`{"nope":1}`), 0o600)
	if _, err := h.setup(); err == nil || !strings.Contains(err.Error(), "nope") {
		t.Errorf("an unknown key: %v", err)
	}
}

// One server holds a home, its process id in the lock; a dispatch reaches the
// service; a store left under ui/ has moved; the end of ctx drains it and frees
// the lock.
func TestServeHoldsItsHomeAndMovesTheStore(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "ui"), 0o700); err != nil {
		t.Fatal(err)
	}
	old, err := chat.Open(filepath.Join(root, "ui", "rooms.db"), "")
	if err != nil {
		t.Fatal(err)
	}
	room, err := old.CreateRoom("ann@k", "cafe", []string{"bob@k"})
	if err != nil {
		t.Fatal(err)
	}
	_ = old.Close()

	h := homeAt(root)
	cfg, err := h.setup()
	if err != nil {
		t.Fatal(err)
	}
	cfg.ListenAddr = freeAddr(t)
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	done := make(chan error, 1)
	go func() { done <- serve(ctx, h, cfg) }()
	var resp *http.Response
	for range 100 {
		if resp, err = http.Post(cfg.base()+"/chat@k/index", "application/json", strings.NewReader("{}")); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("index: %d", resp.StatusCode)
	}
	b, _ := os.ReadFile(h.path("serve.lock"))
	if strings.TrimSpace(string(b)) != strconv.Itoa(os.Getpid()) {
		t.Errorf("serve.lock = %q", b)
	}
	if _, err := h.lock(); err == nil {
		t.Error("a second server takes a held lock")
	}
	if _, err := os.Stat(filepath.Join(root, "ui", "rooms.db")); !os.IsNotExist(err) {
		t.Errorf("the store stays under ui/: %v", err)
	}
	stop()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	f, err := h.lock()
	if err != nil {
		t.Fatalf("the lock is free: %v", err)
	}
	_ = f.Close()
	moved, err := chat.Open(h.path("rooms.db"), "")
	if err != nil {
		t.Fatal(err)
	}
	defer moved.Close()
	if got, _, err := moved.Room("ann@k", room.ID); err != nil || got.Name != "cafe" {
		t.Errorf("the room moved with the store: %+v %v", got, err)
	}
}

// register reads the kernel and the login from the installation's client records
// and registers the actions at the service's own base.
func TestRegisterUsesTheClientRecords(t *testing.T) {
	var created []client.CreateActionInput
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/me", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{"address":"chat@alpha"}`)) })
	mux.HandleFunc("GET /v1/actions", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`[]`)) })
	mux.HandleFunc("POST /v1/actions", func(w http.ResponseWriter, r *http.Request) {
		var in client.CreateActionInput
		_ = json.NewDecoder(r.Body).Decode(&in)
		created = append(created, in)
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{"id":"a"}`))
	})
	mux.HandleFunc("/v1/actions/enable", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`[]`)) })
	mux.HandleFunc("PUT /v1/actions", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`[]`)) })
	k := httptest.NewServer(mux)
	defer k.Close()

	root := t.TempDir()
	records := filepath.Join(root, "client")
	if err := client.WriteCredential(filepath.Join(records, "credentials", "chat@alpha.json"), client.Tokens{AccessToken: "t", RefreshToken: "r"}, "P"); err != nil {
		t.Fatal(err)
	}
	cfgFile, _ := json.Marshal(map[string]any{"kernels": map[string]any{"alpha": map[string]string{"endpoint": k.URL}}})
	if err := os.WriteFile(filepath.Join(records, "config.json"), cfgFile, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := run(context.Background(), []string{"register", "chat@alpha"}, root); err != nil {
		t.Fatal(err)
	}
	if len(created) != 15 || !strings.HasPrefix(created[0].Source, "http://127.0.0.1:8090/chat@alpha/") {
		t.Fatalf("created %d: %+v", len(created), created[0].Source)
	}
	st, err := chat.LoadState(homeAt(root).path("state.json"))
	if err != nil || st.AuthRev["chat@alpha"] == "" {
		t.Errorf("the revision is kept per provider: %+v %v", st, err)
	}
	if err := run(context.Background(), []string{"register", "bare"}, root); err == nil {
		t.Error("a bare login is refused")
	}
	if err := run(context.Background(), []string{"nonsense"}, root); err == nil || !strings.Contains(err.Error(), "usage") {
		t.Errorf("usage: %v", err)
	}
	if err := run(context.Background(), []string{"--version"}, root); err != nil {
		t.Error(err)
	}
}
