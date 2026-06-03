// Command juice-ui is the entrypoint for the Juice backend-for-frontend: it
// loads configuration from the environment, wires the kernel client, the
// server-side session manager, CSRF protection, and the router, then serves.
package main

import (
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/alexedwards/scs/v2"
	"github.com/daios-ai/juice-ui/client"
	"github.com/daios-ai/juice-ui/server"
	"github.com/daios-ai/juice-ui/session"
)

// Build metadata, injected at link time via -ldflags "-X main.version=... -X main.commit=...".
var (
	version = "dev"
	commit  = "unknown"
)

// Config is the BFF's runtime configuration, loaded from the environment with
// safe local defaults (R2.4). No secrets are committed.
type Config struct {
	KernelBaseURL string
	ListenAddr    string
	SessionSecret string // optional; derives the CSRF key. Empty -> ephemeral key.
	Secure        bool   // Secure flag on cookies; enable behind HTTPS.
}

// LoadConfig reads configuration via getenv (injected for testability) and
// validates it, returning a clear error for invalid startup configuration.
func LoadConfig(getenv func(string) string) (Config, error) {
	cfg := Config{
		KernelBaseURL: orDefault(getenv("JUICE_KERNEL_URL"), "http://127.0.0.1:4040"),
		ListenAddr:    orDefault(getenv("JUICE_UI_ADDR"), "127.0.0.1:8080"),
		SessionSecret: strings.TrimSpace(getenv("JUICE_UI_SESSION_SECRET")),
		Secure:        strings.EqualFold(strings.TrimSpace(getenv("JUICE_UI_SECURE")), "true"),
	}
	u, err := url.Parse(cfg.KernelBaseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return Config{}, fmt.Errorf("JUICE_KERNEL_URL must be an http(s) URL, got %q", cfg.KernelBaseURL)
	}
	if cfg.ListenAddr == "" {
		return Config{}, fmt.Errorf("JUICE_UI_ADDR must not be empty")
	}
	return cfg, nil
}

func orDefault(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}

// csrfKey derives a 32-byte CSRF authentication key. A configured secret is
// hashed to 32 bytes so any length is accepted; absent a secret, an ephemeral
// random key is generated (sessions reset on restart) and the caller is warned.
func csrfKey(secret string) (key []byte, ephemeral bool, err error) {
	if secret != "" {
		sum := sha256.Sum256([]byte(secret))
		return sum[:], false, nil
	}
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		return nil, false, err
	}
	return k, true, nil
}

// build wires all dependencies into an http.Handler. Separated from main for
// testability.
func build(cfg Config, log *slog.Logger) (http.Handler, error) {
	kernel := client.New(cfg.KernelBaseURL, &http.Client{Timeout: 60 * time.Second})

	sm := scs.New()
	sm.Lifetime = 24 * time.Hour
	sm.IdleTimeout = 2 * time.Hour
	sm.Cookie.Name = "juice_session"
	sm.Cookie.HttpOnly = true
	sm.Cookie.Secure = cfg.Secure
	sm.Cookie.SameSite = http.SameSiteLaxMode
	sm.Cookie.Path = "/"

	sessions := session.NewManager(sm, kernel)

	renderer, err := server.NewRenderer()
	if err != nil {
		return nil, fmt.Errorf("load templates: %w", err)
	}
	key, ephemeral, err := csrfKey(cfg.SessionSecret)
	if err != nil {
		return nil, err
	}
	if ephemeral {
		log.Warn("JUICE_UI_SESSION_SECRET not set; using an ephemeral key — sessions reset on restart and this is unsafe for production")
	}
	return server.New(server.Options{
		Client:   kernel,
		Sessions: sessions,
		Renderer: renderer,
		Logger:   log,
		CSRFKey:  key,
		Secure:   cfg.Secure,
	})
}

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "--version" || os.Args[1] == "-version" || os.Args[1] == "version") {
		fmt.Printf("juice-ui %s (%s)\n", version, commit)
		return
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	cfg, err := LoadConfig(os.Getenv)
	if err != nil {
		log.Error("invalid configuration", "err", err)
		os.Exit(1)
	}
	handler, err := build(cfg, log)
	if err != nil {
		log.Error("startup failed", "err", err)
		os.Exit(1)
	}
	log.Info("juice-ui listening", "version", version, "commit", commit, "addr", cfg.ListenAddr, "kernel", cfg.KernelBaseURL, "secure", cfg.Secure)
	srv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}
	if err := srv.ListenAndServe(); err != nil {
		log.Error("server stopped", "err", err)
		os.Exit(1)
	}
}
