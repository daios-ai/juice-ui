package server

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
)

// Steps is what the gate drives: the kernel package's loop and first boot.
type Steps interface {
	// Ensure runs until the kernel answers, reporting each state; it blocks in
	// the needs-create state until created is closed.
	Ensure(ctx context.Context, report func(state, detail string), created <-chan struct{})
	// Create makes this installation's kernel and returns the recovery phrase.
	Create(world, handle, password string) (phrase string, err error)
	// Local reports whether the configured kernel is this installation's.
	Local() bool
}

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

	mu     sync.Mutex
	state  string
	detail string
	phrase string // the first boot's recovery phrase, held until acknowledged
	err    string // the last Create error, shown with the form
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
	return kernelView{State: g.state, Detail: g.detail, URL: g.url, Local: g.local(r), Error: g.err,
		Command: "juice kernel serve play", World: r.FormValue("world"), Handle: r.FormValue("handle")}
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
	_ = g.render.Render(w, http.StatusOK, "kernel", Page{Title: "Kernel", Path: r.URL.Path, Data: v})
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
	case world == "" || handle == "":
		fail = "A world and a kernel name are required."
	case len(pw) < 8:
		fail = "The password needs at least 8 characters."
	case pw != pw2:
		fail = "The passwords don't match."
	}
	if fail == "" {
		phrase, err := g.steps.Create(world, handle, pw)
		if err != nil {
			fail = err.Error()
		} else {
			g.mu.Lock()
			g.phrase, g.err = phrase, ""
			g.mu.Unlock()
			g.renderPhrase(w, phrase)
			return
		}
	}
	g.mu.Lock()
	g.err = fail
	g.mu.Unlock()
	v := g.view(r)
	_ = g.render.Render(w, http.StatusUnprocessableEntity, "kernel", Page{Title: "Kernel", Data: v})
}

// postCreated is the phrase's acknowledgement: the phrase is dropped, the loop
// resumes, and the person is sent to the root, which the application serves
// once the kernel answers.
func (g *Gate) postCreated(w http.ResponseWriter, r *http.Request) {
	if !g.local(r) || !g.sameOrigin(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	g.mu.Lock()
	g.phrase = ""
	g.mu.Unlock()
	g.once.Do(func() { close(g.created) })
	http.Redirect(w, r, "/", http.StatusSeeOther)
}
