package server

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
)

// Steps is what the gate drives: the kernel package's loop and first boot.
type Steps interface {
	// Ensure runs until the kernel answers, reporting each state; it blocks in
	// the needs-create state until created is closed.
	Ensure(ctx context.Context, report func(state, detail string), created <-chan struct{})
	// Create makes this installation's kernel, enrolling recoveryKey for sys,
	// and returns once it answers.
	Create(world, handle, password, recoveryKey string) error
	// Worlds are the networks this installation can serve, as the kernel
	// lists them.
	Worlds() ([]World, error)
	// Local reports whether the configured kernel is this installation's.
	Local() bool
}

// World is one network the kernel lists: a name, its description, or the
// error the kernel would give serving it.
type World struct{ Name, Description, Error string }

// Gate serves the kernel screen while the kernel is installed, created,
// started or awaited (requirements.md §1), and nothing else: every path is the
// screen until Ready. It knows pages; the Steps know processes.
type Gate struct {
	render  *Renderer
	steps   Steps
	url     string
	listen  string
	log     *slog.Logger
	created chan struct{}
	once    sync.Once
	ready   chan struct{}
	readyMu sync.Once

	mu       sync.Mutex
	state    string
	detail   string
	mnemonic string // the first boot's recovery phrase, one for every attempt
	phrase   string // the mnemonic once the kernel answers, held until acknowledged
	err      string // the last Create error, shown with the form
}

// NewGate builds the gate for the kernel at url; listen is the UI's own address.
func NewGate(r *Renderer, steps Steps, url, listen string, log *slog.Logger) *Gate {
	if log == nil {
		log = slog.Default()
	}
	return &Gate{render: r, steps: steps, url: url, listen: listen, log: log,
		created: make(chan struct{}), ready: make(chan struct{}), state: "waiting", detail: url}
}

// Run drives the steps until the kernel answers.
func (g *Gate) Run(ctx context.Context) { g.steps.Ensure(ctx, g.report, g.created) }

// Ready is closed once the kernel answers; the application then takes the
// listener.
func (g *Gate) Ready() <-chan struct{} { return g.ready }

func (g *Gate) report(state, detail string) {
	g.mu.Lock()
	g.state, g.detail = state, detail
	g.mu.Unlock()
	g.log.Info("kernel gate", "state", state, "detail", detail)
	if state == "serving" {
		g.readyMu.Do(func() { close(g.ready) })
	}
}

// kernelView is the screen's data.
type kernelView struct {
	State, Detail, URL string
	Local              bool // the request may create the kernel here
	Command            string
	Error              string
	World, Handle      string
	Worlds             []World
}

// Handler serves the screen on every path, the status fragment, and the two
// first-boot posts, under the application's security headers.
func (g *Gate) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(StaticFS()))))
	mux.HandleFunc("GET /kernel/status", g.getStatus)
	mux.HandleFunc("POST /kernel/create", g.postCreate)
	mux.HandleFunc("POST /kernel/created", g.postCreated)
	mux.HandleFunc("/", g.getScreen)
	return securityHeaders(mux)
}

// local reports a request from the person at this machine: the UI listens on
// loopback, the peer is loopback, and no proxy announced itself. Only such a
// request may create the kernel or read its phrase (§1).
func (g *Gate) local(r *http.Request) bool {
	lh, _, err := net.SplitHostPort(g.listen)
	if err != nil || !(lh == "" || lh == "localhost" || net.ParseIP(lh) != nil && net.ParseIP(lh).IsLoopback()) {
		return false
	}
	if lh == "" {
		return false // every interface: not a desktop
	}
	rh, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil || net.ParseIP(rh) == nil || !net.ParseIP(rh).IsLoopback() {
		return false
	}
	return r.Header.Get("Forwarded") == "" && r.Header.Get("X-Forwarded-For") == ""
}

// sameOrigin refuses a cross-site post (fetch metadata, else the Origin host).
func (g *Gate) sameOrigin(r *http.Request) bool {
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" {
		return site == "same-origin" || site == "none"
	}
	if o := r.Header.Get("Origin"); o != "" {
		u, err := url.Parse(o)
		return err == nil && u.Host == r.Host
	}
	return true
}

func (g *Gate) view(r *http.Request) kernelView {
	g.mu.Lock()
	defer g.mu.Unlock()
	v := kernelView{State: g.state, Detail: g.detail, URL: g.url, Local: g.local(r), Error: g.err,
		Command: "juice kernel serve play", World: r.FormValue("world"), Handle: r.FormValue("handle")}
	if v.State == "needs-create" {
		var err error
		if v.Worlds, err = g.steps.Worlds(); err != nil {
			v.Error = err.Error()
		}
	}
	return v
}

func (g *Gate) getScreen(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	phrase := g.phrase
	g.mu.Unlock()
	if phrase != "" && g.local(r) {
		g.renderPhrase(w, phrase)
		return
	}
	v := g.view(r)
	if v.World == "" {
		v.World = "play"
	}
	_ = g.render.Render(w, http.StatusOK, "kernel", Page{Title: "Juice installation", Path: r.URL.Path, Data: v})
}

func (g *Gate) getStatus(w http.ResponseWriter, r *http.Request) {
	v := g.view(r)
	if v.State == "serving" {
		w.Header().Set("HX-Redirect", "/")
		w.WriteHeader(http.StatusOK)
		return
	}
	_ = g.render.Fragment(w, http.StatusOK, "_kernel_status", v)
}

func (g *Gate) renderPhrase(w http.ResponseWriter, phrase string) {
	_ = g.render.Render(w, http.StatusOK, "recovery_phrase", Page{Title: "Recovery phrase",
		Data: recoveryPhraseView{Words: strings.Fields(phrase), Action: "/kernel/created"}})
}

func (g *Gate) postCreate(w http.ResponseWriter, r *http.Request) {
	if !g.local(r) || !g.sameOrigin(r) {
		http.Error(w, "the kernel is created at this machine", http.StatusForbidden)
		return
	}
	world, handle := strings.TrimSpace(r.FormValue("world")), strings.TrimSpace(r.FormValue("handle"))
	pw, pw2 := r.FormValue("password"), r.FormValue("confirm_password")
	var fail string
	switch {
	case !g.servable(world):
		fail = "Choose a world."
	case handle == "":
		fail = "A kernel name is required."
	case len(pw) < 8:
		fail = "The password needs at least 8 characters."
	case pw != pw2:
		fail = "The passwords don't match."
	}
	if fail == "" {
		fail = g.create(w, world, handle, pw)
		if fail == "" {
			return
		}
	}
	g.mu.Lock()
	g.err = fail
	g.mu.Unlock()
	v := g.view(r)
	_ = g.render.Render(w, http.StatusUnprocessableEntity, "kernel", Page{Title: "Juice installation", Data: v})
}

// servable reports whether the kernel lists world and would serve it; the
// name becomes the kernel's home directory, so the form's choices alone do not
// bound it.
func (g *Gate) servable(world string) bool {
	ws, err := g.steps.Worlds()
	return err == nil && slices.ContainsFunc(ws, func(w World) bool { return w.Name == world && w.Error == "" })
}

// create runs the first boot with the recovery key of one phrase, generated
// here as at signup (recovery.go) and kept across failed attempts: a kernel
// that enrolled the key and then failed to serve is no longer at its first
// boot, ignores another key, and is recovered by this phrase alone. On success
// it renders the phrase page; otherwise it returns what to show with the form.
func (g *Gate) create(w http.ResponseWriter, world, handle, pw string) string {
	g.mu.Lock()
	if g.mnemonic == "" {
		m, err := generateMnemonic()
		if err != nil {
			g.mu.Unlock()
			return recoverySetupFailed
		}
		g.mnemonic = m
	}
	mnemonic := g.mnemonic
	g.mu.Unlock()
	pub, err := recoveryPubKey(mnemonic)
	if err != nil {
		return recoverySetupFailed
	}
	if err := g.steps.Create(world, handle, pw, pub); err != nil {
		return err.Error()
	}
	g.mu.Lock()
	g.phrase, g.err = mnemonic, ""
	g.mu.Unlock()
	g.renderPhrase(w, mnemonic)
	return ""
}

// postCreated is the phrase's acknowledgement, under signup's rule: it counts
// only while a phrase is pending and with the "I understand" box; otherwise
// the root shows whatever is pending. Acknowledged, the phrase is dropped, the
// loop resumes, and the root is served by the application once the kernel
// answers.
func (g *Gate) postCreated(w http.ResponseWriter, r *http.Request) {
	if !g.local(r) || !g.sameOrigin(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	g.mu.Lock()
	if g.phrase != "" && r.FormValue("understand") != "" {
		g.phrase = ""
		g.once.Do(func() { close(g.created) })
	}
	g.mu.Unlock()
	http.Redirect(w, r, "/", http.StatusSeeOther)
}
