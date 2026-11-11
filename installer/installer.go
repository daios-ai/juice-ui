// Package installer is Juice-UI's first start, served before the application
// (requirements.md §1): a sequence of steps in the shape OS installers use —
// pages that ask and change nothing, a summary, one step that does the work as
// jobs with progress, and a finish — chosen once from what the installation
// holds. It owns the sequence, its state, its routes and its templates, and
// reaches the kernel and the application's look only through Kernel and
// Chrome, so it imports nothing of this repository.
package installer

import (
	"context"
	"embed"
	"errors"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

//go:embed templates/*.html
var tmplFS embed.FS

// Templates are the installer's page and partials, for the application's
// renderer, which renders them in its layout.
func Templates() fs.FS { return tmplFS }

// Component is a program the installation runs, at the release this UI was
// verified against.
type Component struct{ Name, Release string }

// World is one network a kernel can serve, as the kernel lists it.
type World struct{ Name, Description, Rail, Symbol, Error string }

// Kernel is this machine's installation as the installer drives it.
type Kernel interface {
	Local() bool                                                          // the configured kernel is this machine's
	Serving(ctx context.Context) bool                                     // a kernel answers at the configured address
	Booted() (world string, booted bool, err error)                       // the kernel home for that address, and whether a kernel lives in it
	Due(answers bool) ([]Component, error)                                // what to install now
	Install(ctx context.Context, c Component) error                       // one component, verified
	CanCreate() error                                                     // the kernel binary can perform a first boot
	Worlds() ([]World, error)                                             // the networks a new kernel may serve
	Create(world, handle, password, recoveryKey string, models any) error // the first boot, with the models chosen
	Start(ctx context.Context) error                                      // the supervisor serves the kernel; returns once it answers
	// Models reads the models page: with form nil, held as it stands, or the
	// kernel's default models when held is nil; with a form, the answers over
	// held, and whether they may be kept (done) or what to say. The setup is
	// opaque here, held from its page to the first boot and given to Create.
	Models(ctx context.Context, form url.Values, held any) (setup any, msg string, done bool)
	ModelsView(ctx context.Context, setup any) any // what the page and the summary render
}

// Chrome is the application's look, and the recovery phrase it makes as signup
// does.
type Chrome interface {
	Page(w http.ResponseWriter, status int, page, title string, data any) error
	Fragment(w http.ResponseWriter, status int, name string, data any) error
	NewPhrase() (phrase, publicKey string, err error)
}

// Options configure an installer.
type Options struct {
	Kernel Kernel
	Chrome Chrome
	URL    string // the kernel's address
	Listen string // the UI's own address
	Home   string // $JUICE_HOME
	Log    *slog.Logger
}

// Step is one entry of a sequence: a page, which asks and changes nothing, or a
// job step, which runs its jobs in order as soon as it is entered. An installer
// step is drawn in the installer's frame; the one launch step that ends every
// sequence is not drawn at all while it runs within holdTimeout.
type Step struct {
	ID, Title string
	Installer bool
	Page      bool
	Back      bool                                          // a page that offers Back to the page before it
	Submit    func(form url.Values) (next bool, msg string) // a page's answers stored; whether to go on, or what to say
	Jobs      func() []Job                                  // a job step's work
}

// Job is one piece of a job step's work.
type Job struct {
	Title  string
	Run    func(ctx context.Context) error
	Retry  bool   // a failure is tried again after retryPause; otherwise it waits for Try again
	Return string // a page a failure returns to, with its words
}

// ModelsRefresh is where the models page asks for a block's model field
// again, when its provider, address or key changes.
const ModelsRefresh = "/install/models/model"

// retryPause is how long a failed job that retries waits before its next
// attempt. A variable so tests shorten it.
var retryPause = 10 * time.Second

// holdTimeout is how long a page request waits for the launch before it is
// answered with what the launch is doing. A variable so tests shorten it.
var holdTimeout = 15 * time.Second

// job is a job as shown: its title, its state, and a failure's words.
type job struct {
	Title, State, Err string // State: "pending", "running", "done", "failed"
	Retry             bool
}

// Installer serves the first start's steps until the sequence ends.
type Installer struct {
	o      Options
	ready  chan struct{}
	once   sync.Once
	served chan struct{} // closed once the application holds the listener
	sOnce  sync.Once

	mu    sync.Mutex
	ctx   context.Context
	seq   []Step
	at    int
	run   []Job  // the current job step's jobs
	jobs  []job  // and how each stands
	gen   int    // the job run in progress; a run that is not the current one changes nothing
	err   string // a page's refusal, or the words of a job that returned to it
	world string
	handle,
	password string // held from its page to the first boot, never rendered
	phrase,
	pub string
	models any // the models chosen, with their keys: held from their page to the first boot
}

// New builds the installer.
func New(o Options) *Installer {
	if o.Log == nil {
		o.Log = slog.Default()
	}
	return &Installer{o: o, ready: make(chan struct{}), served: make(chan struct{})}
}

// Served is called once the application holds the listener, so a request held
// for the launch is sent on to it.
func (in *Installer) Served() { in.sOnce.Do(func() { close(in.served) }) }

// Ready is closed once the sequence has ended; the application then takes the
// listener.
func (in *Installer) Ready() <-chan struct{} { return in.ready }

// Run chooses the sequence from the installation's facts and enters its first
// step. Its jobs run until ctx ends.
func (in *Installer) Run(ctx context.Context) {
	seq, err := in.choose(ctx)
	in.mu.Lock()
	defer in.mu.Unlock()
	in.ctx = ctx
	if err != nil {
		// No sequence: the page states why and the installation never ends,
		// since the application would be handed a listener with no kernel.
		in.err = err.Error()
		in.o.Log.Error("installer", "err", err)
		return
	}
	in.seq = seq
	in.enter(0)
}

// choose is the sequence the facts call for. It is the installer's steps this
// machine needs, which may be none, then the launch. Download comes when a
// component is missing; the pages and Install when a kernel is: the kernel is
// this machine's, no home exists for its address, and nothing answers there (one
// that answers without a home here is followed, never created over, since a
// creation would collide with its port). The launch starts this installation's
// kernel, or waits for one that is not this machine's to start.
func (in *Installer) choose(ctx context.Context) ([]Step, error) {
	k := in.o.Kernel
	if !k.Local() {
		return []Step{in.launch(false)}, nil
	}
	answers := k.Serving(ctx)
	_, booted, berr := k.Booted()
	ours := booted || berr != nil
	creating := !ours && !answers
	var seq []Step
	if dl, ok := in.download(answers && !ours, creating); ok {
		seq = append(seq, dl)
	}
	if creating {
		phrase, pub, err := in.o.Chrome.NewPhrase()
		if err != nil {
			return nil, errors.New("couldn't set up account recovery; restart Juice to try again")
		}
		in.phrase, in.pub = phrase, pub
		seq = append(seq, in.worldPage(), in.kernelPage(), in.modelsPage(), in.phrasePage(), in.summaryPage(), in.install())
	}
	return append(seq, in.launch(ours || creating)), nil
}

// download installs the components due, and before a first boot checks that the
// kernel program can perform one; ok is false when there is nothing to do. An
// installation that cannot be read is one job that reads it again, and installs
// what is due, when it is tried again.
func (in *Installer) download(followed, creating bool) (Step, bool) {
	k := in.o.Kernel
	var jobs []Job
	due, err := k.Due(followed)
	if err != nil {
		jobs = append(jobs, Job{Title: "Read the installation", Run: func(ctx context.Context) error {
			due, err := k.Due(followed)
			for _, c := range due {
				if err == nil {
					err = k.Install(ctx, c)
				}
			}
			return err
		}})
	}
	for _, c := range due {
		jobs = append(jobs, Job{Title: "Install " + c.Name + " " + c.Release, Retry: true,
			Run: func(ctx context.Context) error { return k.Install(ctx, c) }})
	}
	if creating && (len(jobs) > 0 || k.CanCreate() != nil) {
		jobs = append(jobs, Job{Title: "Check the kernel", Run: func(context.Context) error { return k.CanCreate() }})
	}
	return Step{ID: "download", Title: "Download", Installer: true, Jobs: func() []Job { return jobs }}, len(jobs) > 0
}

func (in *Installer) worldPage() Step {
	return Step{ID: "world", Title: "World", Installer: true, Page: true, Submit: answer(func(f url.Values) string {
		w := f.Get("world")
		ws, err := in.o.Kernel.Worlds()
		if err != nil {
			return err.Error()
		}
		for _, x := range ws {
			if x.Name == w && x.Error == "" {
				in.world = w
				return ""
			}
		}
		return "Choose a world."
	})}
}

func (in *Installer) kernelPage() Step {
	return Step{ID: "kernel", Title: "Kernel", Installer: true, Page: true, Back: true, Submit: answer(func(f url.Values) string {
		in.handle = strings.TrimSpace(f.Get("handle"))
		pw, pw2 := f.Get("password"), f.Get("confirm_password")
		switch {
		case in.handle == "":
			return "A kernel name is required."
		case len(pw) < 8:
			return "The password needs at least 8 characters."
		case pw != pw2:
			return "The passwords don't match."
		}
		in.password = pw
		return ""
	})}
}

// modelsPage chooses the kernel's language models. "List models" keeps the
// page with what the chosen providers serve; Next goes on only with models
// the kernel can use, or none.
func (in *Installer) modelsPage() Step {
	return Step{ID: "models", Title: "Models", Installer: true, Page: true, Back: true, Submit: func(f url.Values) (bool, string) {
		setup, msg, done := in.o.Kernel.Models(in.ctx, f, in.models)
		in.models = setup
		return done, msg
	}}
}

func (in *Installer) phrasePage() Step {
	return Step{ID: "phrase", Title: "Recovery phrase", Installer: true, Page: true, Back: true, Submit: answer(func(f url.Values) string {
		if f.Get("understand") == "" {
			return "Confirm that you have saved the phrase."
		}
		return ""
	})}
}

func (in *Installer) summaryPage() Step {
	return Step{ID: "summary", Title: "Summary", Installer: true, Page: true, Back: true, Submit: answer(func(url.Values) string { return "" })}
}

// answer is a page that goes on unless it has something to say.
func answer(f func(url.Values) string) func(url.Values) (bool, string) {
	return func(v url.Values) (bool, string) {
		msg := f(v)
		return msg == "", msg
	}
}

// install is the point of no return: the first boot. One that made no kernel
// returns to the kernel page with the kernel's words, where the password is
// typed again; one that made a kernel goes on to the launch, which starts it.
func (in *Installer) install() Step {
	return Step{ID: "install", Title: "Install", Installer: true, Jobs: func() []Job {
		return []Job{{Title: "Create the kernel", Return: "kernel", Run: func(context.Context) error {
			in.mu.Lock()
			world, handle, pw, pub, models := in.world, in.handle, in.password, in.pub, in.models
			in.password = ""
			in.mu.Unlock()
			return in.o.Kernel.Create(world, handle, pw, pub, models)
		}}}
	}}
}

// launch ends every sequence: it starts this installation's kernel, or waits
// for one that is not this machine's to start, and then the application takes
// the listener. It is the same on every launch, after the installer or without
// it.
func (in *Installer) launch(start bool) Step {
	if start {
		return Step{ID: "start", Title: "Start", Jobs: func() []Job {
			return []Job{{Title: "Start the kernel", Retry: true, Run: in.o.Kernel.Start}}
		}}
	}
	return Step{ID: "wait", Title: "Wait", Jobs: func() []Job {
		return []Job{{Title: "Wait for " + in.o.URL, Run: func(ctx context.Context) error {
			for !in.o.Kernel.Serving(ctx) {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(2 * time.Second):
				}
			}
			return nil
		}}}
	}}
}

// enter makes step i current, under mu: a page waits for its post; a job step
// starts its jobs; past the last step the sequence ends.
func (in *Installer) enter(i int) {
	in.at = i
	if i >= len(in.seq) {
		in.password, in.phrase, in.pub, in.models = "", "", "", nil
		in.once.Do(func() { close(in.ready) })
		in.o.Log.Info("installer", "step", "end")
		return
	}
	st := in.seq[i]
	in.o.Log.Info("installer", "step", st.ID)
	if st.ID == "kernel" {
		in.password = "" // typed again whenever the page is shown again
	}
	if st.ID == "models" && in.models == nil {
		in.models, _, _ = in.o.Kernel.Models(in.ctx, nil, nil) // the kernel's default models
	}
	if st.Page {
		return
	}
	in.run = st.Jobs()
	in.jobs = make([]job, len(in.run))
	for n, j := range in.run {
		in.jobs[n] = job{Title: j.Title, State: "pending", Retry: j.Retry}
	}
	in.gen++
	go in.runJobs(in.gen, in.run, 0)
}

// runJobs runs a job step's jobs from the one at from, in order; the last done
// enters the next step.
func (in *Installer) runJobs(gen int, jobs []Job, from int) {
	for n := from; n < len(jobs); n++ {
		j := jobs[n]
		for {
			if !in.mark(gen, n, "running", "") {
				return
			}
			err := j.Run(in.ctx)
			if err == nil {
				break
			}
			if !in.mark(gen, n, "failed", err.Error()) {
				return
			}
			in.o.Log.Warn("installer", "job", j.Title, "err", err)
			if j.Return != "" {
				in.mu.Lock()
				if gen == in.gen {
					in.err = err.Error()
					in.enter(in.index(j.Return))
				}
				in.mu.Unlock()
				return
			}
			if !j.Retry {
				return // Try again restarts it
			}
			select {
			case <-in.ctx.Done():
				return
			case <-time.After(retryPause):
			}
		}
		if !in.mark(gen, n, "done", "") {
			return
		}
	}
	in.mu.Lock()
	if gen == in.gen {
		in.enter(in.at + 1)
	}
	in.mu.Unlock()
}

// mark records job n's state, reporting false once the run is not current.
func (in *Installer) mark(gen, n int, state, msg string) bool {
	in.mu.Lock()
	defer in.mu.Unlock()
	if gen != in.gen || in.ctx.Err() != nil {
		return false
	}
	in.jobs[n].State, in.jobs[n].Err = state, msg
	return true
}

func (in *Installer) index(id string) int {
	for n, st := range in.seq {
		if st.ID == id {
			return n
		}
	}
	return len(in.seq)
}

// Handler serves the current step on every path, its posts and the job
// fragment. The application's wrapper (static assets, security headers) goes
// around it.
func (in *Installer) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /install/status", in.getStatus)
	mux.HandleFunc("POST /install/next", in.post(in.next))
	mux.HandleFunc("POST /install/back", in.post(in.back))
	mux.HandleFunc("POST /install/retry", in.post(in.retry))
	mux.HandleFunc("POST "+ModelsRefresh, in.postModel)
	mux.HandleFunc("/", in.getStep)
	return mux
}

// postModel answers one block's model field of the models page, for the
// answers as posted, with the models the chosen provider lists; the answers
// are held, as List models holds them. Only the person at this machine asks,
// and only while the models page is current.
func (in *Installer) postModel(w http.ResponseWriter, r *http.Request) {
	if !in.local(r) || !sameOrigin(r) {
		http.Error(w, "Juice is installed at this machine", http.StatusForbidden)
		return
	}
	_ = r.ParseForm()
	in.mu.Lock()
	if in.seq == nil || in.at >= len(in.seq) || in.seq[in.at].ID != "models" {
		in.mu.Unlock()
		http.Error(w, "the models page is not current", http.StatusConflict)
		return
	}
	in.models, _, _ = in.o.Kernel.Models(in.ctx, r.PostForm, in.models)
	view := in.o.Kernel.ModelsView(in.ctx, in.models)
	in.mu.Unlock()
	_ = in.o.Chrome.Fragment(w, http.StatusOK, "_models_refresh", struct {
		Form  any
		Field string
	}{view, r.PostFormValue("block")})
}

// Behind is the listener's handler once the application serves: the job
// fragment stays the installer's, so a page still open is sent to the
// application by its next poll, and every other path is the application's.
func (in *Installer) Behind(app http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /install/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("HX-Redirect", "/")
	})
	mux.Handle("/", app)
	return mux
}

// view is what a step renders.
type view struct {
	Installing               bool // the installer's frame; otherwise the launch's message
	Steps                    []stepView
	At                       int // the current step's place, from 1; 0 with no sequence
	Step, Title              string
	Page, Back, Local, Ended bool
	Error, URL, Command      string
	Jobs                     []job
	Failed                   bool // a job waits for Try again
	Worlds                   []World
	World                    World // the chosen one, on the summary
	Chosen, Handle, Home     string
	Words                    []string
	Phrase                   string
	Models                   any // the models page, and the summary's rows
}

type stepView struct { // State: "done", "current", "todo"
	N            int
	Title, State string
}

// view builds the current step's view, under mu.
func (in *Installer) view(r *http.Request) view {
	v := view{Local: in.local(r), URL: in.o.URL, Command: "juice kernel serve play", Error: in.err,
		Chosen: in.world, Handle: in.handle}
	// The rail counts the installer's steps alone.
	for n, st := range in.seq {
		if !st.Installer {
			continue
		}
		s := "todo"
		if n < in.at {
			s = "done"
		} else if n == in.at {
			s = "current"
			v.At = len(v.Steps) + 1
		}
		v.Steps = append(v.Steps, stepView{N: len(v.Steps) + 1, Title: st.Title, State: s})
	}
	if in.seq == nil {
		return v // not chosen yet: the fragment polls until it is
	}
	if in.at >= len(in.seq) {
		v.Ended = true // the application is taking the listener
		return v
	}
	st := in.seq[in.at]
	v.Installing = st.Installer
	v.Step, v.Title, v.Page, v.Back = st.ID, st.Title, st.Page, st.Back
	v.Jobs = append([]job(nil), in.jobs...)
	for _, j := range v.Jobs {
		v.Failed = v.Failed || (j.State == "failed" && !j.Retry)
	}
	if !v.Local {
		return v
	}
	switch st.ID {
	case "world", "summary":
		ws, err := in.o.Kernel.Worlds()
		if err != nil && v.Error == "" {
			v.Error = err.Error()
		}
		v.Worlds = ws
		for _, w := range ws {
			if w.Name == in.world {
				v.World = w
			}
		}
		if v.Chosen == "" {
			v.Chosen = "play"
		}
		v.Home = filepath.Join(in.o.Home, "kernels", in.world)
	case "phrase":
		v.Words = strings.Fields(in.phrase)
		v.Phrase = strings.Join(v.Words, " ")
	}
	if st.ID == "models" || st.ID == "summary" {
		v.Models = in.o.Kernel.ModelsView(in.ctx, in.models)
	}
	return v
}

// getStep renders the current step. While the launch runs, a page request is
// held instead, and answered by the application once it holds the listener; it
// is answered with what the launch is doing only when a launch job fails or the
// launch outlasts holdTimeout.
func (in *Installer) getStep(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet && in.hold(r.Context()) {
		select {
		case <-in.served:
		case <-time.After(holdTimeout):
		case <-r.Context().Done():
			return
		}
		http.Redirect(w, r, r.URL.RequestURI(), http.StatusSeeOther)
		return
	}
	in.render(w, r, http.StatusOK)
}

// hold waits while the launch runs, reporting whether the sequence ended. It
// stops at an installer step, a failed job, a failed start of the package, or
// holdTimeout.
func (in *Installer) hold(ctx context.Context) bool {
	deadline := time.After(holdTimeout)
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		in.mu.Lock()
		stop := in.seq == nil && in.err != ""
		if in.seq != nil && in.at < len(in.seq) {
			stop = in.seq[in.at].Installer
			for _, j := range in.jobs {
				stop = stop || j.State == "failed"
			}
		}
		in.mu.Unlock()
		if stop {
			return false
		}
		select {
		case <-in.ready:
			return true
		case <-deadline:
			return false
		case <-ctx.Done():
			return false
		case <-tick.C:
		}
	}
}

func (in *Installer) render(w http.ResponseWriter, r *http.Request, status int) {
	in.mu.Lock()
	v := in.view(r)
	in.mu.Unlock()
	title := "Juice"
	if v.Installing {
		title = "Juice installation"
	}
	_ = in.o.Chrome.Page(w, status, "install", title, v)
}

// getStatus is a job step's fragment, polled while it runs; once the step has
// changed it sends the page to what is current.
func (in *Installer) getStatus(w http.ResponseWriter, r *http.Request) {
	in.mu.Lock()
	v := in.view(r)
	in.mu.Unlock()
	if v.Ended || v.Step != r.URL.Query().Get("step") || v.Page {
		w.Header().Set("HX-Redirect", "/")
		return
	}
	_ = in.o.Chrome.Fragment(w, http.StatusOK, "_install_jobs", v)
}

// post guards a step's post: only the person at this machine moves the
// installation, and only from the step that is current. Its outcome is reached
// by a redirect, so a reload or a second submit changes nothing; a page that
// refuses its answers is shown again with what to say.
func (in *Installer) post(act func(r *http.Request, st Step) (refused bool)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !in.local(r) || !sameOrigin(r) {
			http.Error(w, "Juice is installed at this machine", http.StatusForbidden)
			return
		}
		in.mu.Lock()
		refused := in.seq != nil && in.at < len(in.seq) && in.seq[in.at].ID == r.PostFormValue("step") &&
			act(r, in.seq[in.at])
		in.mu.Unlock()
		if refused {
			in.render(w, r, http.StatusUnprocessableEntity)
			return
		}
		http.Redirect(w, r, "/", http.StatusSeeOther)
	}
}

// next stores a page's answers and goes on, or keeps the page with what to
// say. Under mu.
func (in *Installer) next(r *http.Request, st Step) bool {
	if !st.Page {
		return false
	}
	next, msg := st.Submit(r.PostForm)
	if in.err = msg; msg != "" {
		return true
	}
	if next {
		in.enter(in.at + 1)
	}
	return false
}

// back returns to the page before, which a page offering Back always has.
func (in *Installer) back(_ *http.Request, st Step) bool {
	if st.Page && st.Back && in.at > 0 && in.seq[in.at-1].Page {
		in.err = ""
		in.enter(in.at - 1)
	}
	return false
}

// retry runs again a job step's jobs from the one that failed and waits.
func (in *Installer) retry(_ *http.Request, st Step) bool {
	if st.Page {
		return false
	}
	for n, j := range in.jobs {
		if j.State == "failed" && !j.Retry {
			in.gen++
			in.jobs[n].State, in.jobs[n].Err = "pending", ""
			go in.runJobs(in.gen, in.run, n)
			break
		}
	}
	return false
}

// local reports a request from the person at this machine: the UI listens on
// loopback, the peer is loopback, and no proxy announced itself. Only such a
// request is shown a page's form, or the phrase.
func (in *Installer) local(r *http.Request) bool {
	lh, _, err := net.SplitHostPort(in.o.Listen)
	if err != nil || lh == "" || !(lh == "localhost" || net.ParseIP(lh) != nil && net.ParseIP(lh).IsLoopback()) {
		return false
	}
	rh, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil || net.ParseIP(rh) == nil || !net.ParseIP(rh).IsLoopback() {
		return false
	}
	return r.Header.Get("Forwarded") == "" && r.Header.Get("X-Forwarded-For") == ""
}

// sameOrigin refuses a cross-site post (fetch metadata, else the Origin host).
func sameOrigin(r *http.Request) bool {
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" {
		return site == "same-origin" || site == "none"
	}
	if o := r.Header.Get("Origin"); o != "" {
		u, err := url.Parse(o)
		return err == nil && u.Host == r.Host
	}
	return true
}
