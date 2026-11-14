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
	"testing"
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
