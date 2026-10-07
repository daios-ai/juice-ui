// Command juice-ui is the entrypoint for the Juice backend-for-frontend: it
// loads configuration from the environment, wires the kernel client, the
// server-side session manager, CSRF protection, and the router, then serves.
//
// Before the application serves, the kernel gate runs (requirements.md §1): the
// listener answers at once with the kernel screen while the kernel is
// installed, created, started or awaited, and the application takes the
// listener the moment the kernel answers.
package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/alexedwards/scs/v2"
	"github.com/daios-ai/juice-ui/apps"
	"github.com/daios-ai/juice-ui/apps/agents"
	"github.com/daios-ai/juice-ui/apps/chat"
	"github.com/daios-ai/juice-ui/client"
	"github.com/daios-ai/juice-ui/kernel"
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

// wiring is what both the gate and the application are built from: one
// client, one identity, one renderer. Nothing here asks the kernel anything.
type wiring struct {
	kernel   *client.Client
	identity *server.KernelIdentity
	apps     []apps.App
	renderer *server.Renderer
	// configSchema is the kernel's schema of its config.json; "" when the
	// kernel is not this installation's.
	configSchema string
}

// wire constructs the client, the identity (pinned to the recorded key when
// the kernel is named, pinned later from /health when it is a bare address),
// the apps and the renderer.
func wire(cfg Config, log *slog.Logger) (*wiring, error) {
	w := &wiring{kernel: client.New(cfg.KernelBaseURL, &http.Client{Timeout: 5 * time.Minute})}
	w.identity = server.NewKernelIdentity(w.kernel, cfg.KernelPublicKey, cfg.BadgeInterval, log)

	// App modules. Each is self-contained; the core only knows the apps.App
	// seam. Chat runs the in-process chat application, which provisions its own
	// service account on first run and persists it in state.json — no chat
	// configuration required.
	var appTemplates []fs.FS
	ch := chat.New()
	w.apps = append(w.apps, ch)
	appTemplates = append(appTemplates, ch.Templates())
	// The agents app manages sibling juice-agents (files under $JUICE_HOME/agent
	// and their serve processes). It owns no kernel account and no persisted state.
	ag := agents.New()
	w.apps = append(w.apps, ag)
	appTemplates = append(appTemplates, ag.Templates())

	// The renderer must include each enabled app's templates so its pages
	// render in the shared layout.
	r, err := server.NewRenderer(w.identity, appTemplates...)
	if err != nil {
		return nil, fmt.Errorf("load templates: %w", err)
	}
	w.renderer = r
	return w, nil
}

// build wires all dependencies into the application's http.Handler. Separated
// from main for testability.
func build(w *wiring, cfg Config, log *slog.Logger) (http.Handler, error) {
	// The UI must know WHICH kernel it is for before anything is keyed on it: the
	// app state, the session pin, and the unit every amount is read in. A named
	// kernel says so from the client records, offline. A bare address says
	// nothing, so the kernel is asked once — and a UI that cannot identify its
	// kernel does not start, rather than working under a guess.
	if cfg.KernelPublicKey == "" {
		id, err := server.AwaitKernel(w.kernel, 10*time.Second)
		if err != nil {
			return nil, fmt.Errorf("kernel at %s could not be identified (%w); name a kernel with JUICE_KERNEL or `kernel` in config.json, or start the kernel", cfg.KernelBaseURL, err)
		}
		w.identity.Pin(id.PublicKey)
		log.Info("kernel identified", "handle", id.Handle, "network", id.Network, "key", client.ShortKey(id.PublicKey))
	}
	// Read /health now, so the kernel's name is known before any app composes
	// an address with it; a kernel that is down is read again on the next
	// request, and an app's own retry waits for that.
	w.identity.Get(context.Background())

	sm := scs.New()
	sm.Lifetime = 24 * time.Hour
	sm.IdleTimeout = 2 * time.Hour
	sm.Cookie.Name = "juice_session"
	sm.Cookie.HttpOnly = true
	sm.Cookie.Secure = cfg.Secure
	sm.Cookie.SameSite = http.SameSiteLaxMode
	sm.Cookie.Path = "/"

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
		Client:         w.kernel,
		Sessions:       session.NewManager(sm, w.kernel),
		Renderer:       w.renderer,
		Logger:         log,
		CSRFKey:        key,
		Secure:         cfg.Secure,
		TrustedOrigins: cfg.TrustedOrigins,
		PublicURL:      publicURL,
		Apps:           w.apps,
		StatePath:      cfg.StatePath,
		Home:           cfg.Home,
		BadgeInterval:  cfg.BadgeInterval,
		Identity:       w.identity,
		KernelName:     cfg.Kernel,
		ConfigSchema:   w.configSchema,
	})
}

// steps adapts the kernel package to the gate's Steps; each package keeps its
// own types.
type steps struct{ *kernel.Installation }

func (s steps) Ensure(ctx context.Context, report func(state, detail string), created <-chan struct{}) {
	s.Installation.Ensure(ctx, func(st kernel.State, d string) { report(string(st), d) }, created)
}

func (s steps) Worlds() ([]server.World, error) {
	ws, err := s.Installation.Worlds()
	out := make([]server.World, len(ws))
	for n, w := range ws {
		out[n] = server.World(w)
	}
	return out, err
}

// switcher is the listener's handler: the gate first, the application once it
// is built. One listener, swapped atomically, so the address answers from the
// first moment and never changes hands.
type switcher struct{ h atomic.Pointer[http.Handler] }

func (s *switcher) Store(h http.Handler) { s.h.Store(&h) }
func (s *switcher) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	(*s.h.Load()).ServeHTTP(w, r)
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
	w, err := wire(cfg, log)
	if err != nil {
		log.Error("startup failed", "err", err)
		os.Exit(1)
	}
	kernelURL, _ := url.Parse(cfg.KernelBaseURL) // validated by LoadConfig
	inst := &kernel.Installation{Home: cfg.Root, URL: kernelURL, Log: log}
	gate := server.NewGate(w.renderer, steps{inst}, cfg.KernelBaseURL, cfg.ListenAddr, log)

	var sw switcher
	sw.Store(gate.Handler())
	srv := &http.Server{Addr: cfg.ListenAddr, Handler: &sw, ReadHeaderTimeout: 10 * time.Second}
	served := make(chan error, 1)
	go func() { served <- srv.ListenAndServe() }()
	log.Info("juice-ui listening", "version", version, "commit", commit, "addr", cfg.ListenAddr, "kernel", cfg.KernelBaseURL, "secure", cfg.Secure)

	go gate.Run(context.Background())
	select {
	case <-gate.Ready():
	case err := <-served:
		log.Error("server stopped", "err", err)
		os.Exit(1)
	}
	// The kernel writes its schema of config.json in this installation on every
	// serve; a kernel elsewhere may be another release, so it has none here.
	if inst.Local() {
		w.configSchema = filepath.Join(cfg.Root, "schemas", "config.schema.json")
	}
	handler, err := build(w, cfg, log)
	if err != nil {
		log.Error("startup failed", "err", err)
		os.Exit(1)
	}
	sw.Store(gate.Behind(handler))
	log.Info("juice-ui serving", "kernel", cfg.KernelBaseURL)
	if err := <-served; err != nil {
		log.Error("server stopped", "err", err)
		os.Exit(1)
	}
}
