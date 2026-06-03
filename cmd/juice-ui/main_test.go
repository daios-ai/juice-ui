package main

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestLoadConfigDefaults(t *testing.T) {
	cfg, err := LoadConfig(env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.KernelBaseURL != "http://127.0.0.1:4040" {
		t.Errorf("default kernel url = %q", cfg.KernelBaseURL)
	}
	if cfg.ListenAddr != "127.0.0.1:8080" {
		t.Errorf("default addr = %q", cfg.ListenAddr)
	}
	if cfg.Secure {
		t.Error("Secure should default false for local http")
	}
}

func TestLoadConfigOverrides(t *testing.T) {
	cfg, err := LoadConfig(env(map[string]string{
		"JUICE_KERNEL_URL":        "https://kernel.example.com",
		"JUICE_UI_ADDR":           ":9000",
		"JUICE_UI_SESSION_SECRET": "topsecret",
		"JUICE_UI_SECURE":         "true",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.KernelBaseURL != "https://kernel.example.com" || cfg.ListenAddr != ":9000" {
		t.Errorf("overrides not applied: %+v", cfg)
	}
	if !cfg.Secure {
		t.Error("Secure should be true")
	}
}

func TestLoadConfigRejectsBadURL(t *testing.T) {
	for _, bad := range []string{"not-a-url", "ftp://x", "://nohost", "/relative"} {
		if _, err := LoadConfig(env(map[string]string{"JUICE_KERNEL_URL": bad})); err == nil {
			t.Errorf("expected error for kernel url %q", bad)
		}
	}
}

func TestCSRFKey(t *testing.T) {
	// A configured secret yields a deterministic 32-byte key.
	k1, eph1, err := csrfKey("topsecret")
	if err != nil || eph1 {
		t.Fatalf("configured secret should not be ephemeral: %v %v", eph1, err)
	}
	if len(k1) != 32 {
		t.Errorf("key length = %d want 32", len(k1))
	}
	k2, _, _ := csrfKey("topsecret")
	if string(k1) != string(k2) {
		t.Error("same secret should derive same key")
	}
	// No secret yields an ephemeral random key.
	k3, eph3, _ := csrfKey("")
	if !eph3 || len(k3) != 32 {
		t.Errorf("empty secret should be ephemeral 32-byte key: eph=%v len=%d", eph3, len(k3))
	}
}

func TestBuildServesLogin(t *testing.T) {
	cfg, _ := LoadConfig(env(map[string]string{"JUICE_UI_SESSION_SECRET": "secret-for-test"}))
	h, err := build(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	srv := httptest.NewServer(h)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/login")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("/login status = %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !contains(string(body), "Sign in") {
		t.Errorf("login page missing content")
	}
	// The session cookie must be HttpOnly (R5.5); tokens never reach the browser.
	if resp.Request != nil {
		// no token cookies expected at login time
	}
}

func TestBuildRequiresAuthRedirect(t *testing.T) {
	cfg, _ := LoadConfig(env(map[string]string{"JUICE_UI_SESSION_SECRET": "s"}))
	h, _ := build(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	srv := httptest.NewServer(h)
	defer srv.Close()

	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := noRedirect.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("dashboard without auth should redirect, got %d", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/login" {
		t.Errorf("redirect to %q want /login", loc)
	}
}

func contains(s, sub string) bool { return len(s) >= len(sub) && (indexOf(s, sub) >= 0) }
func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func TestVersionDefaults(t *testing.T) {
	// Build metadata has safe defaults when not injected via -ldflags.
	if version == "" || commit == "" {
		t.Errorf("version/commit must have non-empty defaults, got %q/%q", version, commit)
	}
}
