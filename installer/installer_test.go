package installer

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/daios-ai/juice-ui/server"
)

// fakeKernel is the installation as a test arranges it.
type fakeKernel struct {
	mu        sync.Mutex
	remote    bool
	answers   bool
	booted    bool
	bootErr   error
	due       []Component
	failFirst int // installs refused before they succeed
	installed []string
	canCreate error
	createErr error
	makes     bool          // a failing Create still leaves a kernel
	hold      chan struct{} // Create waits on it, when set
	created   []string      // world handle password key, per Create
	dueAsked  []bool
	started   int
}

func (k *fakeKernel) Local() bool { return !k.remote }
func (k *fakeKernel) Serving(context.Context) bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.answers
}
func (k *fakeKernel) Booted() (string, bool, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	return "play", k.booted, k.bootErr
}
func (k *fakeKernel) Due(answers bool) ([]Component, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.dueAsked = append(k.dueAsked, answers)
	return k.due, nil
}
func (k *fakeKernel) Install(_ context.Context, c Component) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.failFirst > 0 {
		k.failFirst--
		return errors.New("could not download " + c.Name + ": 503")
	}
	k.installed = append(k.installed, c.Name)
	return nil
}
func (k *fakeKernel) CanCreate() error {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.canCreate
}
func (k *fakeKernel) Worlds() ([]World, error) {
	return []World{
		{Name: "play", Description: "no real money", Rail: "manual", Symbol: "fUSD"},
		{Name: "polygon", Description: "USDT0 on Polygon", Rail: "evm", Symbol: "USDT0"},
		{Name: "broken", Error: "parse world file"},
	}, nil
}
func (k *fakeKernel) Create(world, handle, password, key string) error {
	if k.hold != nil {
		<-k.hold
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	k.created = append(k.created, strings.Join([]string{world, handle, password, key}, " "))
	if k.createErr != nil && !k.makes {
		return k.createErr
	}
	k.booted, k.answers = true, true
	return nil
}
func (k *fakeKernel) Start(context.Context) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.started++
	k.answers = true
	return nil
}

// chrome renders through the application's own renderer, as the UI does.
type chrome struct {
	r      *server.Renderer
	phrase string
}

func (c chrome) Page(w http.ResponseWriter, status int, page, title string, data any) error {
	return c.r.Render(w, status, page, server.Page{Title: title, Data: data})
}
func (c chrome) Fragment(w http.ResponseWriter, status int, name string, data any) error {
	return c.r.Fragment(w, status, name, data)
}
func (c chrome) NewPhrase() (string, string, error) { return c.phrase, "PUBKEY", nil }

const phrase = "abandon ability able about above absent absorb abstract absurd abuse access accident"

// start runs an installer over k, listening on loopback.
func start(t *testing.T, k *fakeKernel) (*Installer, http.Handler) {
	t.Helper()
	retryPause = 20 * time.Millisecond
	t.Cleanup(func() { retryPause = 10 * time.Second })
	r, err := server.NewRenderer(nil, Templates())
	if err != nil {
		t.Fatal(err)
	}
	in := New(Options{Kernel: k, Chrome: chrome{r: r, phrase: phrase}, URL: "http://127.0.0.1:4040",
		Listen: "127.0.0.1:8080", Home: "/home/x/.juice", Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	in.Run(ctx)
	return in, in.Handler()
}

// do sends one request from this machine, unless hdr says otherwise.
func do(t *testing.T, h http.Handler, method, path string, form url.Values, hdr map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req := httptest.NewRequest(method, path, body)
	req.RemoteAddr = "127.0.0.1:5555"
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func page(t *testing.T, h http.Handler) string {
	t.Helper()
	return do(t, h, http.MethodGet, "/", nil, nil).Body.String()
}

// next posts a page's answers.
func next(t *testing.T, h http.Handler, step string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	if form == nil {
		form = url.Values{}
	}
	form.Set("step", step)
	return do(t, h, http.MethodPost, "/install/next", form, nil)
}

// current is the current step's ID, "" once the sequence has ended.
func current(in *Installer) string {
	in.mu.Lock()
	defer in.mu.Unlock()
	if in.at >= len(in.seq) {
		return ""
	}
	return in.seq[in.at].ID
}

func waitFor(t *testing.T, in *Installer, id string) {
	t.Helper()
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if current(in) == id {
			return
		}
	}
	t.Fatalf("step is %q, want %q", current(in), id)
}

func ids(in *Installer) string {
	var out []string
	for _, st := range in.seq {
		out = append(out, st.ID)
	}
	return strings.Join(out, ",")
}

// The sequence is the one the facts call for, tested in order: a kernel
// elsewhere is waited for; a home for the address is started; a kernel that
// answers with no home here is followed, its binary not installed; otherwise a
// kernel is created.
func TestTheFactsChooseTheSequence(t *testing.T) {
	for _, c := range []struct {
		k    *fakeKernel
		want string
	}{
		{&fakeKernel{remote: true}, "wait"},
		{&fakeKernel{booted: true}, "prepare,start"},
		{&fakeKernel{bootErr: errors.New("damaged")}, "prepare,start"},
		{&fakeKernel{answers: true}, "prepare,wait"},
		{&fakeKernel{}, "prepare,world,kernel,phrase,summary,install,done"},
	} {
		in, _ := start(t, c.k)
		if got := ids(in); got != c.want {
			t.Errorf("%+v: sequence %s, want %s", c.k, got, c.want)
		}
	}
	k := &fakeKernel{answers: true}
	start(t, k)
	time.Sleep(50 * time.Millisecond)
	k.mu.Lock()
	defer k.mu.Unlock()
	if len(k.dueAsked) != 1 || !k.dueAsked[0] || len(k.created) != 0 {
		t.Errorf("a followed kernel: due asked %v, created %v", k.dueAsked, k.created)
	}
}

// A fresh installation walks every step in order. The pages change nothing;
// Install answers at once while the first boot runs, its job shown running;
// the first boot gets the password and the phrase's key; the phrase is shown on
// its page alone and is gone when the sequence ends; the listener is handed
// over only after Open Juice.
func TestAFreshInstallWalksEveryStep(t *testing.T) {
	k := &fakeKernel{due: []Component{{"juice", "v0.18.1"}, {"juice-agent", "v0.10.0"}}, hold: make(chan struct{})}
	in, h := start(t, k)
	waitFor(t, in, "world")
	if k.installed == nil || strings.Join(k.installed, ",") != "juice,juice-agent" {
		t.Errorf("installed %v", k.installed)
	}
	b := page(t, h)
	for _, want := range []string{`aria-current="step">World`, `name="world"`, `<option value="play" selected>play · fUSD</option>`, `<dt class="mono">play</dt><dd>no real money</dd>`,
		`<option value="broken" disabled>broken</option>`, `<dt class="mono">broken</dt><dd>parse world file</dd>`, "Components</li>"} {
		if !strings.Contains(b, want) {
			t.Errorf("world page lacks %q", want)
		}
	}
	if strings.Contains(b, `formaction="/install/back"`) {
		t.Error("the first page has no Back")
	}
	next(t, h, "world", url.Values{"world": {"polygon"}})
	waitFor(t, in, "kernel")
	next(t, h, "kernel", url.Values{"handle": {"alpha"}, "password": {"secret123"}, "confirm_password": {"secret123"}})
	waitFor(t, in, "phrase")
	if b := page(t, h); !strings.Contains(b, "<li>abandon</li>") || !strings.Contains(b, `name="understand"`) {
		t.Error("the phrase page shows the words")
	}
	next(t, h, "phrase", url.Values{"understand": {"on"}})
	waitFor(t, in, "summary")
	b = page(t, h)
	for _, want := range []string{"polygon", "alpha", "/home/x/.juice/kernels/polygon", "Losing that directory loses the money", ">Install</button>"} {
		if !strings.Contains(b, want) {
			t.Errorf("summary lacks %q", want)
		}
	}
	if len(k.created) != 0 {
		t.Fatal("nothing is created before Install")
	}
	began := time.Now()
	if rec := next(t, h, "summary", nil); rec.Code != http.StatusSeeOther || time.Since(began) > time.Second {
		t.Fatalf("Install answers at once: %d after %v", rec.Code, time.Since(began))
	}
	waitFor(t, in, "install")
	var b2 string
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if b2 = do(t, h, http.MethodGet, "/install/status?step=install", nil, nil).Body.String(); strings.Contains(b2, `<li class="running">`) {
			break
		}
	}
	if !strings.Contains(b2, `<span>Create the kernel</span></div>`) || !strings.Contains(b2, `class="pending"`) {
		t.Errorf("the job shows running, the next pending: %s", b2)
	}
	next(t, h, "summary", nil) // a second Install starts nothing
	close(k.hold)
	waitFor(t, in, "done")
	if len(k.created) != 1 || k.created[0] != "polygon alpha secret123 PUBKEY" || k.started != 1 {
		t.Errorf("created %v, started %d", k.created, k.started)
	}
	if rec := do(t, h, http.MethodGet, "/install/status?step=install", nil, nil); rec.Header().Get("HX-Redirect") != "/" {
		t.Error("the status sends a page on once its step ends")
	}
	select {
	case <-in.Ready():
		t.Fatal("ready before Open Juice")
	default:
	}
	b = page(t, h)
	if !strings.Contains(b, ">Open Juice</button>") || strings.Contains(b, "abandon") || strings.Contains(b, "secret123") {
		t.Error("the done page: Open Juice, and neither the phrase nor the password")
	}
	next(t, h, "done", nil)
	select {
	case <-in.Ready():
	case <-time.After(time.Second):
		t.Fatal("Open Juice ends the sequence")
	}
	if in.phrase != "" || in.password != "" || in.pub != "" {
		t.Error("the secrets leave memory with the sequence")
	}
}

// A page that refuses its answers is shown again with what to say and what was
// typed, never a password; it changes nothing.
func TestAPageRefusesItsAnswers(t *testing.T) {
	in, h := start(t, &fakeKernel{})
	waitFor(t, in, "world")
	for _, w := range []string{"", "broken", "elsewhere"} {
		if rec := next(t, h, "world", url.Values{"world": {w}}); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "Choose a world.") {
			t.Errorf("world %q: %d", w, rec.Code)
		}
	}
	next(t, h, "world", url.Values{"world": {"play"}})
	waitFor(t, in, "kernel")
	for form, want := range map[[3]string]string{
		{"", "secret123", "secret123"}:      "A kernel name is required.",
		{"alpha", "short", "short"}:         "at least 8 characters",
		{"alpha", "secret123", "secret124"}: "don&#39;t match",
	} {
		rec := next(t, h, "kernel", url.Values{"handle": {form[0]}, "password": {form[1]}, "confirm_password": {form[2]}})
		b := rec.Body.String()
		if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(b, want) || strings.Contains(b, "secret12") ||
			!strings.Contains(b, `value="`+form[0]+`"`) {
			t.Errorf("%v: %d, lacks %q or shows the password", form, rec.Code, want)
		}
	}
	if current(in) != "kernel" {
		t.Error("a refusal stays on its page")
	}
	waitFor(t, in, "kernel")
	next(t, h, "kernel", url.Values{"handle": {"alpha"}, "password": {"secret123"}, "confirm_password": {"secret123"}})
	waitFor(t, in, "phrase")
	if rec := next(t, h, "phrase", nil); rec.Code != http.StatusUnprocessableEntity {
		t.Error("the phrase page needs its box")
	}
}

// Back returns to the page before, on pages that offer it; the kernel page
// shown again holds no password. Once Install has begun nothing goes back, and
// a post from a step that is not current changes nothing.
func TestBackGoesToThePageBefore(t *testing.T) {
	k := &fakeKernel{hold: make(chan struct{})}
	in, h := start(t, k)
	waitFor(t, in, "world")
	next(t, h, "world", url.Values{"world": {"play"}})
	waitFor(t, in, "kernel")
	next(t, h, "kernel", url.Values{"handle": {"alpha"}, "password": {"secret123"}, "confirm_password": {"secret123"}})
	waitFor(t, in, "phrase")
	do(t, h, http.MethodPost, "/install/back", url.Values{"step": {"phrase"}}, nil)
	waitFor(t, in, "kernel")
	if in.password != "" || !strings.Contains(page(t, h), `value="alpha"`) {
		t.Error("the kernel page shown again keeps the name and drops the password")
	}
	do(t, h, http.MethodPost, "/install/back", url.Values{"step": {"kernel"}}, nil)
	waitFor(t, in, "world")
	do(t, h, http.MethodPost, "/install/back", url.Values{"step": {"world"}}, nil)
	waitFor(t, in, "world")
	next(t, h, "kernel", url.Values{"handle": {"x"}}) // not current
	waitFor(t, in, "world")

	next(t, h, "world", url.Values{"world": {"play"}})
	next(t, h, "kernel", url.Values{"handle": {"alpha"}, "password": {"secret123"}, "confirm_password": {"secret123"}})
	next(t, h, "phrase", url.Values{"understand": {"on"}})
	next(t, h, "summary", nil)
	waitFor(t, in, "install")
	do(t, h, http.MethodPost, "/install/back", url.Values{"step": {"install"}}, nil)
	do(t, h, http.MethodPost, "/install/back", url.Values{"step": {"summary"}}, nil)
	if current(in) != "install" {
		t.Error("nothing goes back once Install has begun")
	}
	close(k.hold)
	waitFor(t, in, "done")
	if strings.Contains(page(t, h), `formaction="/install/back"`) {
		t.Error("the done page has no Back")
	}
}

// A first boot that made no kernel returns to the kernel page with the kernel's
// words and no password held; the next attempt boots with the password typed
// then, and the same phrase. One that made a kernel goes on.
func TestAFailedFirstBootReturnsToTheKernelPage(t *testing.T) {
	k := &fakeKernel{createErr: errors.New("kernel name refused")}
	in, h := start(t, k)
	waitFor(t, in, "world")
	walk := func(pw string) {
		next(t, h, "world", url.Values{"world": {"play"}})
		next(t, h, "kernel", url.Values{"handle": {"Bad"}, "password": {pw}, "confirm_password": {pw}})
		next(t, h, "phrase", url.Values{"understand": {"on"}})
		next(t, h, "summary", nil)
	}
	walk("secret123")
	waitFor(t, in, "kernel")
	if b := page(t, h); !strings.Contains(b, "kernel name refused") || !strings.Contains(b, `value="Bad"`) || in.password != "" {
		t.Error("the kernel page with the kernel's words and no password")
	}
	k.mu.Lock()
	k.createErr = nil
	k.mu.Unlock()
	next(t, h, "kernel", url.Values{"handle": {"alpha"}, "password": {"another12"}, "confirm_password": {"another12"}})
	waitFor(t, in, "phrase")
	if !strings.Contains(page(t, h), "<li>abandon</li>") {
		t.Error("the same phrase")
	}
	next(t, h, "phrase", url.Values{"understand": {"on"}})
	next(t, h, "summary", nil)
	waitFor(t, in, "done")
	if len(k.created) != 2 || k.created[1] != "play alpha another12 PUBKEY" {
		t.Errorf("created %v", k.created)
	}

	made := &fakeKernel{createErr: errors.New("did not answer"), makes: true}
	in, h = start(t, made)
	waitFor(t, in, "world")
	next(t, h, "world", url.Values{"world": {"play"}})
	next(t, h, "kernel", url.Values{"handle": {"alpha"}, "password": {"secret123"}, "confirm_password": {"secret123"}})
	next(t, h, "phrase", url.Values{"understand": {"on"}})
	next(t, h, "summary", nil)
	waitFor(t, in, "done")
}

// A failed download is shown and tried again; a failure that does not retry
// waits for Try again, which runs it again from where it failed.
func TestAFailedJobIsShownAndTriedAgain(t *testing.T) {
	k := &fakeKernel{due: []Component{{"juice-agent", "v0.10.0"}}, failFirst: 1, booted: true}
	in, _ := start(t, k)
	waitFor(t, in, "")
	if strings.Join(k.installed, ",") != "juice-agent" || k.started != 1 {
		t.Errorf("installed %v, started %d", k.installed, k.started)
	}

	c := &fakeKernel{canCreate: errors.New("the installed kernel is v0.16.0, and a new kernel needs v0.18.1")}
	in, h := start(t, c)
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if strings.Contains(page(t, h), "Try again") {
			break
		}
	}
	b := do(t, h, http.MethodGet, "/install/status?step=prepare", nil, nil).Body.String()
	if !strings.Contains(b, "a new kernel needs v0.18.1") || !strings.Contains(b, `action="/install/retry"`) || current(in) != "prepare" {
		t.Fatalf("the failure waits for Try again: %s", b)
	}
	c.mu.Lock()
	c.canCreate = nil
	c.mu.Unlock()
	do(t, h, http.MethodPost, "/install/retry", url.Values{"step": {"prepare"}}, nil)
	waitFor(t, in, "world")
}

// A kernel that answers with no home here is followed: no page is offered,
// nothing is created, and the listener is handed over once it answers.
func TestAFollowedKernelIsNotCreatedOver(t *testing.T) {
	k := &fakeKernel{answers: true}
	in, h := start(t, k)
	select {
	case <-in.Ready():
	case <-time.After(3 * time.Second):
		t.Fatal("a kernel that answers ends the sequence")
	}
	if b := page(t, h); strings.Contains(b, "<form") {
		t.Error("no page is offered")
	}
	if len(k.created) != 0 || k.started != 0 {
		t.Errorf("created %v, started %d", k.created, k.started)
	}
}

// A page's form and the phrase are the person's at this machine: a request
// through a proxy is told the terminal command and may not post.
func TestOnlyThisMachineInstalls(t *testing.T) {
	in, h := start(t, &fakeKernel{})
	waitFor(t, in, "world")
	far := map[string]string{"X-Forwarded-For": "203.0.113.9"}
	if b := do(t, h, http.MethodGet, "/", nil, far).Body.String(); strings.Contains(b, `name="world"`) || !strings.Contains(b, "juice kernel serve play") {
		t.Error("a request from elsewhere sees the command, not the form")
	}
	if rec := do(t, h, http.MethodPost, "/install/next", url.Values{"step": {"world"}, "world": {"play"}}, far); rec.Code != http.StatusForbidden {
		t.Errorf("a post from elsewhere: %d", rec.Code)
	}
	if rec := do(t, h, http.MethodPost, "/install/next", url.Values{"step": {"world"}, "world": {"play"}}, map[string]string{"Sec-Fetch-Site": "cross-site"}); rec.Code != http.StatusForbidden {
		t.Errorf("a cross-site post: %d", rec.Code)
	}
	next(t, h, "world", url.Values{"world": {"play"}})
	next(t, h, "kernel", url.Values{"handle": {"alpha"}, "password": {"secret123"}, "confirm_password": {"secret123"}})
	waitFor(t, in, "phrase")
	if b := do(t, h, http.MethodGet, "/", nil, far).Body.String(); strings.Contains(b, "abandon") {
		t.Error("the phrase is shown at this machine alone")
	}
}

// Once the application serves, a page still open is sent to it by its poll.
func TestBehindSendsAnOpenPageOn(t *testing.T) {
	in, _ := start(t, &fakeKernel{remote: true})
	app := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "app") })
	h := in.Behind(app)
	if rec := do(t, h, http.MethodGet, "/install/status?step=wait", nil, nil); rec.Header().Get("HX-Redirect") != "/" {
		t.Error("the status redirects")
	}
	if b := do(t, h, http.MethodGet, "/chat", nil, nil).Body.String(); b != "app" {
		t.Error("every other path is the application's")
	}
}

// The installer imports nothing of this repository, and only the entrypoint
// imports it: the installer is kept apart from the server it runs before.
func TestTheInstallerStandsApart(t *testing.T) {
	src, _ := filepath.Glob("*.go")
	for _, f := range src {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, _ := os.ReadFile(f)
		if strings.Contains(string(b), `"github.com/daios-ai/juice-ui/`) {
			t.Errorf("%s imports this repository", f)
		}
	}
	all, _ := filepath.Glob("../*/*.go")
	more, _ := filepath.Glob("../*/*/*.go")
	for _, f := range append(all, more...) {
		if strings.HasSuffix(f, "_test.go") || strings.HasPrefix(f, "../installer/") || strings.HasPrefix(f, "../cmd/juice-ui/") {
			continue
		}
		b, _ := os.ReadFile(f)
		if strings.Contains(string(b), `"github.com/daios-ai/juice-ui/installer"`) {
			t.Errorf("%s imports the installer", f)
		}
	}
}
