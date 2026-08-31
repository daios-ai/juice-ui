// Command juice-ui is the entrypoint for the Juice backend-for-frontend: it
// loads configuration from the environment, wires the kernel client, the
// server-side session manager, CSRF protection, and the router, then serves.
package main

import (
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/alexedwards/scs/v2"
	"github.com/daios-ai/juice-ui/apps"
	"github.com/daios-ai/juice-ui/apps/agents"
	"github.com/daios-ai/juice-ui/apps/chat"
	"github.com/daios-ai/juice-ui/client"
	"github.com/daios-ai/juice-ui/server"
	"github.com/daios-ai/juice-ui/session"
)

// Build metadata, injected at link time via -ldflags "-X main.version=... -X main.commit=...".
var (
	version = "dev"
	commit  = "unknown"
)

// Config and its loader live in config.go.

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
	kernel := client.New(cfg.KernelBaseURL, &http.Client{Timeout: 5 * time.Minute})

	sm := scs.New()
	sm.Lifetime = 24 * time.Hour
	sm.IdleTimeout = 2 * time.Hour
	sm.Cookie.Name = "juice_session"
	sm.Cookie.HttpOnly = true
	sm.Cookie.Secure = cfg.Secure
	sm.Cookie.SameSite = http.SameSiteLaxMode
	sm.Cookie.Path = "/"

	sessions := session.NewManager(sm, kernel)

	// App modules. Each is self-contained; the core only knows the apps.App
	// seam. Chat runs the in-process chat application, which provisions its own
	// service account on first run and persists it in state.json — no chat
	// configuration required.
	var appList []apps.App
	var appTemplates []fs.FS
	ch := chat.New()
	appList = append(appList, ch)
	appTemplates = append(appTemplates, ch.Templates())
	// The agents app manages sibling juice-agents (files under $JUICE_HOME/agent
	// and their serve processes). It owns no kernel account and no persisted state.
	ag := agents.New()
	appList = append(appList, ag)
	appTemplates = append(appTemplates, ag.Templates())

	// The renderer must include each enabled app's templates so its pages
	// render in the shared layout.
	renderer, err := server.NewRenderer(appTemplates...)
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
	// Resolve a concrete base URL so in-process apps can register kind=http
	// action sources the kernel calls back (defaults to the listen address for
	// local use; operators behind a proxy set JUICE_UI_PUBLIC_URL).
	publicURL := cfg.PublicURL
	if publicURL == "" {
		publicURL = "http://" + cfg.ListenAddr
	}
	return server.New(server.Options{
		Client:         kernel,
		Sessions:       sessions,
		Renderer:       renderer,
		Logger:         log,
		CSRFKey:        key,
		Secure:         cfg.Secure,
		TrustedOrigins: cfg.TrustedOrigins,
		PublicURL:      publicURL,
		Apps:           appList,
		StatePath:      cfg.StatePath,
		Home:           cfg.Home,
		BadgeInterval:  cfg.BadgeInterval,
	})
}

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "--version" || os.Args[1] == "-version" || os.Args[1] == "version") {
		fmt.Printf("juice-ui %s (%s)\n", version, commit)
		return
	}
	log := slog.New(newLogHandler(os.Stderr))
	cfg, err := LoadConfig(os.Getenv, os.ReadFile)
	if err != nil {
		log.Error("invalid configuration", "err", err)
		os.Exit(1)
	}
	log.Info("config resolved", "home", cfg.Home, "state", cfg.StatePath, "badge_interval", cfg.BadgeInterval)
	for _, n := range cfg.Notices {
		log.Warn("migration: " + n)
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
