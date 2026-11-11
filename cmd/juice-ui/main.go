// Command juice-ui is the entrypoint for the Juice backend-for-frontend: it
// loads configuration from the environment, wires the kernel client, the
// server-side session manager, CSRF protection, and the router, then serves.
//
// Before the application serves, the installer runs (requirements.md §1): the
// listener answers at once with its steps while the components are installed
// and the kernel is created, started or awaited, and the application takes the
// listener when the installer's sequence ends.
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
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/alexedwards/scs/v2"
	"github.com/daios-ai/juice-ui/apps"
	"github.com/daios-ai/juice-ui/apps/agents"
	"github.com/daios-ai/juice-ui/apps/chat"
	"github.com/daios-ai/juice-ui/client"
	"github.com/daios-ai/juice-ui/installer"
	"github.com/daios-ai/juice-ui/kernel"
	"github.com/daios-ai/juice-ui/models"
	"github.com/daios-ai/juice-ui/server"
	"github.com/daios-ai/juice-ui/session"
	"github.com/daios-ai/juice-ui/supervisor"
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

// wiring is what both the installer and the application are built from: one
// client, one identity, one renderer. Nothing here asks the kernel anything.
type wiring struct {
	kernel   *client.Client
	identity *server.KernelIdentity
	apps     []apps.App
	renderer *server.Renderer
	// config is this installation's kernel configuration; nil when the kernel
	// is not this installation's.
	config server.Configuration
	// programs is the process supervisor, handed to the installation and the apps.
	programs apps.Programs
}

// wire constructs the client, the identity (pinned to the recorded key when
// the kernel is named, pinned later from /health when it is a bare address),
// the apps and the renderer.
func wire(cfg Config, log *slog.Logger) (*wiring, error) {
	w := &wiring{kernel: client.New(cfg.KernelBaseURL, &http.Client{Timeout: 5 * time.Minute})}
	w.identity = server.NewKernelIdentity(w.kernel, cfg.KernelPublicKey, cfg.BadgeInterval, log)

	// App modules. Each is self-contained; the core only knows the apps.App
	// seam. Chat is where a session lands; it provisions its own service
	// account on first run and persists it in state.json — no chat
	// configuration required.
	var appTemplates []fs.FS
	ch := chat.New()
	w.apps = append(w.apps, ch)
	appTemplates = append(appTemplates, ch.Templates())
	// The agents app manages sibling juice-agents (homes under $JUICE_HOME/agents
	// and their serve processes). It owns no kernel account and no persisted state.
	ag := agents.New()
	w.apps = append(w.apps, ag)
	appTemplates = append(appTemplates, ag.Templates())

	// The renderer must include each enabled app's templates so its pages
	// render in the shared layout.
	// The installer's steps render in the same layout.
	appTemplates = append(appTemplates, installer.Templates())
	r, err := server.NewRenderer(w.identity, appTemplates...)
	if err != nil {
		return nil, fmt.Errorf("load templates: %w", err)
	}
	w.renderer = r
	return w, nil
}

// sessionStore is the scs store of browser sessions. The Manager judges both
// limits (session/session.go); scs only discards. Its deadline is set at
// login, and a record evicted at the age limit itself would be gone when the
// gate came to end it, so the record outlives the session by the idle limit:
// long enough for the heartbeat, or a return, to find it aged and end it with
// the notice and the revocation. Its own idle rule is off, because it would
// count the heartbeat as activity.
func sessionStore(secure bool) *scs.SessionManager {
	sm := scs.New()
	sm.Lifetime = session.AgeLimit + session.IdleLimit
	sm.IdleTimeout = 0
	sm.Cookie.Name = "juice_session"
	sm.Cookie.HttpOnly = true
	sm.Cookie.Secure = secure
	sm.Cookie.SameSite = http.SameSiteLaxMode
	sm.Cookie.Path = "/"
	return sm
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

	sessions := session.NewManager(sessionStore(cfg.Secure), w.kernel)
	go sessions.Sweep(context.Background(), time.Minute)

	key, ephemeral, err := csrfKey(cfg.SessionSecret)
	if err != nil {
		return nil, err
	}
	if ephemeral {
		log.Warn("JUICE_UI_SESSION_SECRET not set; using an ephemeral key — sessions reset on restart and this is unsafe for production")
	}
	return server.New(server.Options{
		Client:         w.kernel,
		Sessions:       sessions,
		Renderer:       w.renderer,
		Logger:         log,
		CSRFKey:        key,
		Secure:         cfg.Secure,
		TrustedOrigins: cfg.TrustedOrigins,
		PublicURL:      cfg.PublicURL,
		Apps:           w.apps,
		StatePath:      cfg.StatePath,
		Home:           cfg.Home,
		Programs:       w.programs,
		BadgeInterval:  cfg.BadgeInterval,
		Identity:       w.identity,
		KernelName:     cfg.Kernel,
		Config:         w.config,
	})
}

// installation adapts the kernel package to the installer's Kernel; each
// package keeps its own types.
type installation struct{ *kernel.Installation }

func (k installation) Due(answers bool) ([]installer.Component, error) {
	due, err := k.Installation.Due(answers)
	out := make([]installer.Component, len(due))
	for n, c := range due {
		out[n] = installer.Component{Name: c.Name, Release: c.Release}
	}
	return out, err
}

func (k installation) Install(ctx context.Context, c installer.Component) error {
	for _, kc := range kernel.Components {
		if kc.Name == c.Name {
			return k.Installation.Install(ctx, kc)
		}
	}
	return fmt.Errorf("no component %s", c.Name)
}

func (k installation) Worlds() ([]installer.World, error) {
	ws, err := k.Installation.Worlds()
	out := make([]installer.World, len(ws))
	for n, w := range ws {
		out[n] = installer.World{Name: w.Name, Description: w.Description, Rail: w.Rail, Symbol: w.Symbol, Error: w.Error}
	}
	return out, err
}

// Create is the first boot, with the models chosen on their page written into
// the kernel's first configuration.
func (k installation) Create(world, handle, password, recoveryKey string, m any) error {
	var extra map[string]any
	if s, ok := m.(models.Setup); ok {
		extra = map[string]any{}
		models.Apply(extra, s)
	}
	return k.Installation.Create(world, handle, password, recoveryKey, extra)
}

// Models reads the installer's models page through package models: the
// kernel's own default models, a list asked for, or answers checked with the
// providers.
func (k installation) Models(ctx context.Context, form url.Values, held any) (any, string, bool) {
	h, ok := held.(models.Setup)
	switch {
	case form == nil && !ok:
		return models.From(models.Defaults(k.Schema())), "", false
	case form == nil:
		return h, "", false
	}
	s := models.Read(form)
	s.Keep(h)
	if form.Has("list") {
		return s, "", false
	}
	if err := models.Check(ctx, s); err != nil {
		return s, err.Error(), false
	}
	return s, "", true
}

func (installation) ModelsView(ctx context.Context, setup any) any {
	s, _ := setup.(models.Setup)
	f := models.View(ctx, s, s) // the installer holds what was typed
	f.Refresh(installer.ModelsRefresh)
	return f
}

// chrome is the application's renderer as the installer uses it.
type chrome struct{ r *server.Renderer }

func (c chrome) Page(w http.ResponseWriter, status int, page, title string, data any) error {
	return c.r.Render(w, status, page, server.Page{Title: title, Data: data})
}

func (c chrome) Fragment(w http.ResponseWriter, status int, name string, data any) error {
	return c.r.Fragment(w, status, name, data)
}

func (chrome) NewPhrase() (string, string, error) { return server.NewRecoveryPhrase() }

// switcher is the listener's handler: the installer first, the application once it
// is built. One listener, swapped atomically, so the address answers from the
// first moment and never changes hands.
type switcher struct{ h atomic.Pointer[http.Handler] }

func (s *switcher) Store(h http.Handler) { s.h.Store(&h) }
func (s *switcher) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	(*s.h.Load()).ServeHTTP(w, r)
}

func main() { os.Exit(run()) }

// run is the program, returning its exit code, so that every way out closes the
// supervisor: stopping the UI stops the programs it runs.
func run() int {
	if len(os.Args) > 1 && (os.Args[1] == "--version" || os.Args[1] == "-version" || os.Args[1] == "version") {
		fmt.Printf("juice-ui %s (%s)\n", version, commit)
		return 0
	}
	log := slog.New(newLogHandler(os.Stderr))
	cfg, err := LoadConfig(os.Getenv, os.ReadFile)
	if err != nil {
		log.Error("invalid configuration", "err", err)
		return 1
	}
	log.Info("config resolved", "home", cfg.Home, "state", cfg.StatePath, "badge_interval", cfg.BadgeInterval)
	for _, n := range cfg.Notices {
		log.Warn("migration: " + n)
	}
	// SIGTERM or SIGINT ends the UI gracefully, during the installer as well.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	sup, err := supervisor.Open(cfg.Root)
	if err != nil {
		log.Error("startup failed", "err", err)
		return 1
	}
	defer sup.Close()
	w, err := wire(cfg, log)
	if err != nil {
		log.Error("startup failed", "err", err)
		return 1
	}
	w.programs = sup
	kernelURL, _ := url.Parse(cfg.KernelBaseURL) // validated by LoadConfig
	inst := &kernel.Installation{Home: cfg.Root, URL: kernelURL, Log: log, Programs: sup, Ctx: ctx}
	defer inst.WaitBoot() // ctx has ended: a first boot in flight is ending
	in := installer.New(installer.Options{Kernel: installation{inst}, Chrome: chrome{w.renderer},
		URL: cfg.KernelBaseURL, Listen: cfg.ListenAddr, Home: cfg.Root, Log: log})

	var sw switcher
	sw.Store(server.Shell(in.Handler()))
	srv := &http.Server{Addr: cfg.ListenAddr, Handler: &sw, ReadHeaderTimeout: 10 * time.Second}
	served := make(chan error, 1)
	go func() { served <- srv.ListenAndServe() }()
	log.Info("juice-ui listening", "version", version, "commit", commit, "addr", cfg.ListenAddr, "kernel", cfg.KernelBaseURL, "secure", cfg.Secure)

	go in.Run(ctx)
	select {
	case <-in.Ready():
	case err := <-served:
		log.Error("server stopped", "err", err)
		stop()
		return 1
	case <-ctx.Done():
		shutdown(srv, w.apps, log)
		return 0
	}
	// Only this installation's kernel is configured here: a kernel elsewhere
	// is its operator's, edited and restarted where it runs.
	if inst.Local() {
		w.config = inst
	}
	handler, err := build(w, cfg, log)
	if err != nil {
		log.Error("startup failed", "err", err)
		stop()
		return 1
	}
	sw.Store(in.Behind(handler))
	in.Served() // a request held for the launch is sent on to the application
	log.Info("juice-ui serving", "kernel", cfg.KernelBaseURL)
	select {
	case err := <-served:
		log.Error("server stopped", "err", err)
		stop()
		shutdown(srv, w.apps, log)
		return 1
	case <-ctx.Done():
	}
	shutdown(srv, w.apps, log)
	return 0
}

// shutdown drains the listener, then stops every app, all within one deadline. The
// supervisor closes after it, when run returns.
func shutdown(srv *http.Server, mods []apps.App, log *slog.Logger) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
	for _, a := range mods {
		if err := a.Stop(ctx); err != nil {
			log.Warn("app stop failed", "app", a.Slug(), "err", err)
		}
	}
}
