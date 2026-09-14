package agents

import (
	"html/template"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alexedwards/scs/v2"
	"github.com/daios-ai/juice-ui/apps"
	"github.com/daios-ai/juice-ui/client"
	"github.com/daios-ai/juice-ui/session"
)

// capture records what a handler asked the core to render or flash, so tests
// assert on the template name + view model (no real templates or kernel needed),
// mirroring apps/chat/chat_test.go.
type capture struct {
	page, title                  string
	fragment                     string
	status                       int
	data                         any
	err                          error
	flashTo, flashKind, flashMsg string
}

func testApp(t *testing.T) (*Agents, *fakeLauncher, *capture) {
	t.Helper()
	fl := &fakeLauncher{}
	a := &Agents{root: t.TempDir(), sup: newSupervisor(fl)}
	cap := &capture{}
	a.deps = apps.Deps{
		Handle:    func(r *http.Request) string { return "alice" },
		CSRFField: func(r *http.Request) template.HTML { return template.HTML("<input name=csrf>") },
		RenderPage: func(w http.ResponseWriter, r *http.Request, status int, page, title string, data any) {
			cap.page, cap.title, cap.status, cap.data = page, title, status, data
		},
		RenderFragment: func(w http.ResponseWriter, status int, name string, data any) error {
			cap.fragment, cap.status, cap.data = name, status, data
			return nil
		},
		HandleErr: func(w http.ResponseWriter, r *http.Request, err error) bool {
			if err != nil {
				cap.err = err
				return true
			}
			return false
		},
		FlashRedirect: func(w http.ResponseWriter, r *http.Request, to, kind, msg string) {
			cap.flashTo, cap.flashKind, cap.flashMsg = to, kind, msg
		},
	}
	return a, fl, cap
}

func postForm(path, body string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return req
}

// ---- store ----

func TestCreateScaffolds(t *testing.T) {
	a, _, _ := testApp(t)
	if err := a.create("research"); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(a.root, "research")
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		t.Fatalf("home not a dir: %v", err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Errorf("home perm = %o, want 700", info.Mode().Perm())
	}
	for _, f := range scaffold {
		fi, err := os.Stat(filepath.Join(dir, f))
		if err != nil {
			t.Fatalf("scaffold %s missing: %v", f, err)
		}
		if fi.Size() != 0 {
			t.Errorf("%s not blank", f)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("%s perm = %o, want 600", f, fi.Mode().Perm())
		}
	}
	// The UI must not write config.json — the daemon dumps it on first serve.
	if _, err := os.Stat(filepath.Join(dir, configFile)); !os.IsNotExist(err) {
		t.Error("config.json should not be created by the UI")
	}
}

func TestCreateExistingIsRefusedWithoutTruncation(t *testing.T) {
	a, _, _ := testApp(t)
	if err := a.create("x"); err != nil {
		t.Fatal(err)
	}
	if err := a.writeFile("x", personaFile, "hand-authored"); err != nil {
		t.Fatal(err)
	}
	if err := a.create("x"); err != errExists {
		t.Fatalf("second create = %v, want errExists", err)
	}
	got, _ := a.readFile("x", personaFile)
	if got != "hand-authored" {
		t.Fatalf("persona was clobbered: %q", got)
	}
}

func TestWriteAndReadEditableFiles(t *testing.T) {
	a, _, _ := testApp(t)
	_ = a.create("x")
	cases := map[string]string{
		personaFile:   "You are helpful.",
		knowledgeFile: "- fact one",
		configFile:    `{"max_turns":8}`,
	}
	for f, content := range cases {
		if err := a.writeFile("x", f, content); err != nil {
			t.Fatalf("write %s: %v", f, err)
		}
		got, err := a.readFile("x", f)
		if err != nil || got != content {
			t.Fatalf("read %s = %q (%v), want %q", f, got, err, content)
		}
	}
}

func TestWriteConfigRejectsInvalidJSON(t *testing.T) {
	a, _, _ := testApp(t)
	_ = a.create("x")
	_ = a.writeFile("x", configFile, `{"ok":true}`)
	if err := a.writeFile("x", configFile, "not json"); err != errBadConfig {
		t.Fatalf("write bad config = %v, want errBadConfig", err)
	}
	got, _ := a.readFile("x", configFile)
	if got != `{"ok":true}` {
		t.Fatalf("invalid config overwrote the file: %q", got)
	}
}

func TestMemoryIsNotWritable(t *testing.T) {
	a, _, _ := testApp(t)
	_ = a.create("x")
	if err := a.writeFile("x", memoryFile, "tamper"); err == nil {
		t.Fatal("memory.md must not be writable via the UI")
	}
}

func TestListSorted(t *testing.T) {
	a, _, _ := testApp(t)
	_ = a.create("zebra")
	_ = a.create("alpha")
	// A stray non-agent file under root is ignored.
	_ = os.WriteFile(filepath.Join(a.root, "notes.txt"), []byte("x"), 0o600)
	rows, err := a.list()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].Handle != "alpha" || rows[1].Handle != "zebra" {
		t.Fatalf("list = %+v, want [alpha zebra]", rows)
	}
}

func TestRemove(t *testing.T) {
	a, _, _ := testApp(t)
	_ = a.create("x")
	if err := a.remove("x"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(a.root, "x")); !os.IsNotExist(err) {
		t.Fatal("home not removed")
	}
}

// ---- security: handle validation / traversal ----

func TestValidHandle(t *testing.T) {
	good := []string{"research", "a", "triage-2", "chat_bot", "x9"}
	bad := []string{"", "..", ".", "a/b", "../etc", "config.json", "-x", "_x", "A", "ab*", strings.Repeat("a", 65), "@agent"}
	for _, h := range good {
		if !validHandle(h) {
			t.Errorf("validHandle(%q) = false, want true", h)
		}
	}
	for _, h := range bad {
		if validHandle(h) {
			t.Errorf("validHandle(%q) = true, want false", h)
		}
	}
}

func TestAgentDirRejectsTraversal(t *testing.T) {
	a, _, _ := testApp(t)
	for _, h := range []string{"..", "a/b", "../x", "config.json", ""} {
		if _, err := a.agentDir(h); err == nil {
			t.Errorf("agentDir(%q) accepted a bad handle", h)
		}
	}
	// A valid handle resolves to a direct child of root.
	dir, err := a.agentDir("ok")
	if err != nil || filepath.Dir(dir) != filepath.Clean(a.root) {
		t.Fatalf("agentDir(ok) = %q, %v", dir, err)
	}
}

// ---- handlers ----

func TestGetIndexRendersList(t *testing.T) {
	a, _, cap := testApp(t)
	_ = a.create("research")
	a.getIndex(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/agents", nil))
	if cap.page != "agents" {
		t.Fatalf("page = %q, want agents", cap.page)
	}
	lv, ok := cap.data.(listView)
	if !ok || len(lv.Agents) != 1 || lv.Agents[0].Handle != "research" {
		t.Fatalf("listView = %+v", cap.data)
	}
	// Each row carries a CSRF field (its status cell holds the inline Stop form) and
	// a matching Start modal, so the list controls mirror the detail page.
	if lv.Agents[0].CSRF == "" {
		t.Error("row missing CSRF for its inline Stop form")
	}
	if len(lv.Starts) != 1 || lv.Starts[0].Handle != "research" {
		t.Fatalf("Starts = %+v, want one for research", lv.Starts)
	}
}

func TestPostCreateValidatesAndScaffolds(t *testing.T) {
	a, _, cap := testApp(t)
	// Invalid handle → error flash, nothing created.
	a.postCreate(httptest.NewRecorder(), postForm("/agents", "handle=Bad%21"))
	if cap.flashKind != "error" {
		t.Fatalf("bad handle flash = %q, want error", cap.flashKind)
	}
	if _, err := os.Stat(filepath.Join(a.root, "Bad!")); !os.IsNotExist(err) {
		t.Fatal("invalid handle should create nothing")
	}
	// A sigiled handle is refused, not rewritten to the bare one (§4): rewriting
	// it would make "@research" and "research" the same name here and nowhere
	// else, which is the dialect the kernel's grammar forbids.
	a.postCreate(httptest.NewRecorder(), postForm("/agents", "handle=%40research"))
	if cap.flashKind != "error" {
		t.Fatalf("sigiled handle flash = %q, want error", cap.flashKind)
	}
	if _, err := os.Stat(filepath.Join(a.root, "research")); !os.IsNotExist(err) {
		t.Fatal("sigiled handle should create nothing")
	}
	// Bare handle → created, success flash to detail.
	a.postCreate(httptest.NewRecorder(), postForm("/agents", "handle=research"))
	if cap.flashKind != "success" || cap.flashTo != "/agents/research" {
		t.Fatalf("create flash = %q to %q", cap.flashKind, cap.flashTo)
	}
	if _, err := os.Stat(filepath.Join(a.root, "research")); err != nil {
		t.Fatalf("home not created: %v", err)
	}
}

func TestPostCreateDuplicate(t *testing.T) {
	a, _, cap := testApp(t)
	_ = a.create("research")
	a.postCreate(httptest.NewRecorder(), postForm("/agents", "handle=research"))
	if cap.flashKind != "error" || !strings.Contains(cap.flashMsg, "already") {
		t.Fatalf("duplicate flash = %q %q", cap.flashKind, cap.flashMsg)
	}
}

func TestGetDetailNotFoundAndFound(t *testing.T) {
	a, _, cap := testApp(t)
	// Missing agent → HandleErr (not found).
	req := httptest.NewRequest(http.MethodGet, "/agents/ghost", nil)
	req.SetPathValue("handle", "ghost")
	a.getDetail(httptest.NewRecorder(), req)
	if cap.err == nil {
		t.Fatal("want not-found error for missing agent")
	}
	// Existing agent → detail page with files.
	cap.err = nil
	_ = a.create("research")
	_ = a.writeFile("research", personaFile, "hello")
	req = httptest.NewRequest(http.MethodGet, "/agents/research", nil)
	req.SetPathValue("handle", "research")
	a.getDetail(httptest.NewRecorder(), req)
	if cap.err != nil {
		t.Fatalf("unexpected err: %v", cap.err)
	}
	dv, ok := cap.data.(detailView)
	if !ok || dv.Handle != "research" || dv.Persona != "hello" {
		t.Fatalf("detailView = %+v", cap.data)
	}
	if dv.HasConfig {
		t.Error("HasConfig should be false before the daemon dumps config")
	}
}

func TestPostSaveFile(t *testing.T) {
	a, _, cap := testApp(t)
	_ = a.create("research")
	// Save persona.
	req := postForm("/agents/research/files/persona.md", "content=You+are+concise.")
	req.SetPathValue("handle", "research")
	req.SetPathValue("file", personaFile)
	a.postSaveFile(httptest.NewRecorder(), req)
	if cap.flashKind != "success" {
		t.Fatalf("save flash = %q", cap.flashKind)
	}
	if got, _ := a.readFile("research", personaFile); got != "You are concise." {
		t.Fatalf("persona = %q", got)
	}
	// memory.md is not an editable target → not found, unchanged.
	cap.err = nil
	req = postForm("/agents/research/files/memory.md", "content=tamper")
	req.SetPathValue("handle", "research")
	req.SetPathValue("file", memoryFile)
	a.postSaveFile(httptest.NewRecorder(), req)
	if cap.err == nil {
		t.Fatal("saving memory.md should be rejected")
	}
	// Invalid config → error flash, not saved.
	cap.flashKind = ""
	req = postForm("/agents/research/files/config.json", "content=nope")
	req.SetPathValue("handle", "research")
	req.SetPathValue("file", configFile)
	a.postSaveFile(httptest.NewRecorder(), req)
	if cap.flashKind != "error" {
		t.Fatalf("bad config flash = %q", cap.flashKind)
	}
}

func TestPostStartPipesPasswordAndNeverLeaks(t *testing.T) {
	a, fl, cap := testApp(t)
	_ = a.create("research")
	const secret = "s3cret-pass-xyz"

	// Missing password → refused, launcher untouched.
	req := postForm("/agents/research/start", "password=")
	req.SetPathValue("handle", "research")
	a.postStart(httptest.NewRecorder(), req)
	if cap.flashKind != "error" || len(fl.started) != 0 {
		t.Fatalf("empty password should refuse: kind=%q started=%d", cap.flashKind, len(fl.started))
	}

	// With password → launched; the password reaches the launcher (child stdin)…
	req = postForm("/agents/research/start", "password="+secret)
	req.SetPathValue("handle", "research")
	a.postStart(httptest.NewRecorder(), req)
	if len(fl.started) != 1 || fl.started[0].password != secret {
		t.Fatalf("password not handed to launcher: %+v", fl.started)
	}
	if !a.sup.running("research") {
		t.Fatal("want running after start")
	}
	// …and never appears in what the user sees.
	if strings.Contains(cap.flashMsg, secret) {
		t.Fatalf("password leaked into flash: %q", cap.flashMsg)
	}
}

func TestPostStopAndStatusFragment(t *testing.T) {
	a, _, cap := testApp(t)
	_ = a.create("research")
	_ = a.sup.start("research", "pw")

	// Status fragment reflects running.
	req := httptest.NewRequest(http.MethodGet, "/agents/research/status", nil)
	req.SetPathValue("handle", "research")
	a.getStatus(httptest.NewRecorder(), req)
	if cap.fragment != "_agent_status" {
		t.Fatalf("fragment = %q", cap.fragment)
	}
	if sv, ok := cap.data.(statusView); !ok || !sv.Running {
		t.Fatalf("statusView = %+v", cap.data)
	}

	// Stop.
	req = postForm("/agents/research/stop", "")
	req.SetPathValue("handle", "research")
	a.postStop(httptest.NewRecorder(), req)
	if a.sup.running("research") {
		t.Fatal("want not running after stop")
	}
}

func TestPostDeleteStopsAndRemoves(t *testing.T) {
	a, fl, cap := testApp(t)
	_ = a.create("research")
	_ = a.sup.start("research", "pw")
	req := postForm("/agents/research/delete", "")
	req.SetPathValue("handle", "research")
	a.postDelete(httptest.NewRecorder(), req)
	if cap.flashKind != "success" {
		t.Fatalf("delete flash = %q", cap.flashKind)
	}
	if fl.procs[0].stops != 1 {
		t.Error("delete should stop a running agent")
	}
	if _, err := os.Stat(filepath.Join(a.root, "research")); !os.IsNotExist(err) {
		t.Error("home not removed on delete")
	}
}

// ---- seam identity ----

// This app acts on the UI HOST — it writes the installation's files and spawns a
// daemon — so it is an operator surface and offers itself to nobody else. An
// entry an ordinary account could not use would be an invitation to a 403, and
// an app that merely hid its entry while serving the routes would be hiding a
// privilege rather than withholding one (the routes are gated too, in Mount).
func TestNavIsOperatorOnly(t *testing.T) {
	a := New()
	if a.Nav("", false) != nil {
		t.Fatal("anonymous nav must be nil")
	}
	if a.Nav("alice", false) != nil {
		t.Fatal("an ordinary account must not be offered a host-acting app")
	}
	entries := a.Nav("sys", true)
	if len(entries) != 1 || entries[0].Href != "/agents" || entries[0].BadgeURL != "" {
		t.Fatalf("nav = %+v", entries)
	}
}

func TestReturnPath(t *testing.T) {
	cases := []struct{ referer, want string }{
		{"", "/agents/x"},                                       // no referer → fallback
		{"http://ui.local/agents", "/agents"},                   // from the list
		{"http://ui.local/agents/research", "/agents/research"}, // from a detail page
		{"http://evil.example/phish", "/agents/x"},              // off-site → fallback (no open redirect)
		{"http://ui.local/run", "/agents/x"},                    // other local page → fallback
	}
	for _, c := range cases {
		r := httptest.NewRequest(http.MethodPost, "/agents/x/stop", nil)
		if c.referer != "" {
			r.Header.Set("Referer", c.referer)
		}
		if got := returnPath(r, "/agents/x"); got != c.want {
			t.Errorf("returnPath(referer=%q) = %q, want %q", c.referer, got, c.want)
		}
	}
}

func TestDefaultRoot(t *testing.T) {
	if got := defaultRoot("/home/u/.juice/ui"); got != filepath.FromSlash("/home/u/.juice/agent/agents") {
		t.Fatalf("defaultRoot = %q", got)
	}
	if got := juiceRoot(filepath.FromSlash("/home/u/.juice/agent/agents")); got != filepath.FromSlash("/home/u/.juice") {
		t.Fatalf("juiceRoot = %q", got)
	}
}

// Every route this app serves acts on the UI host, so Mount MUST gate each one
// on the operator rather than on any signed-in session. The wrapper is recorded
// per route: a handler that slipped through RequireAuth would let one account
// edit and stop another's agent, and let anyone spawn a process on the machine
// the interface runs on.
func TestMountGatesEveryRouteOnTheSuperuser(t *testing.T) {
	var superuser, auth []string
	d := apps.Deps{
		RequireSuperuser: func(h http.HandlerFunc) http.HandlerFunc {
			superuser = append(superuser, "x")
			return h
		},
		RequireAuth: func(h http.HandlerFunc) http.HandlerFunc {
			auth = append(auth, "x")
			return h
		},
	}
	New().Mount(http.NewServeMux(), d)
	if len(auth) != 0 {
		t.Errorf("a host-acting app must gate on the operator, not on any session (%d routes used RequireAuth)", len(auth))
	}
	if len(superuser) != 8 {
		t.Errorf("every route must be gated: %d of 8", len(superuser))
	}
}

// An agent IS a user: it holds its own principal, is funded like anybody's, and
// spends it. Without its account on this page an operator can raise an economic
// actor and never see its economy, which is the one fact they most need about
// something spending on their machine.
//
// With no kernel to ask, the view carries no account at all — and the template
// then says there is none, rather than rendering a zero balance. A scaffolded
// home is a directory, not a principal.
func TestAgentViewCarriesNoAccountWithoutAKernel(t *testing.T) {
	a, _, cap := testApp(t)
	if err := a.create("research"); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/agents/research", nil)
	req.SetPathValue("handle", "research")
	a.getDetail(httptest.NewRecorder(), req)
	v, ok := cap.data.(detailView)
	if !ok {
		t.Fatalf("view = %T", cap.data)
	}
	if v.Account != nil {
		t.Errorf("no kernel means no account read, not an invented one: %+v", v.Account)
	}
	if len(v.Activity) != 0 {
		t.Errorf("and no activity: %+v", v.Activity)
	}
}

// The activity rows must resolve their actions. Without the map every row
// renders "(deleted)" — a plain untruth about the agent's own history, and one
// no test would catch from the view model alone.
func TestAgentActivityResolvesItsActions(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/admin/users/{handle}", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"handle":"research","available":3000000,"locked":0}`))
	})
	mux.HandleFunc("GET /v1/transactions", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[{"id":"T1","action_id":"A1","owner_handle":"research","caller_handle":"research",
		 "target_handle":"sys","status":"success","started_at":"2026-09-14T10:00:00Z"}]`))
	})
	mux.HandleFunc("GET /v1/actions/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"id":"A1","action":"sys/time","owner_handle":"sys","name":"time","kind":"native","active":true}`))
	})
	kernel := httptest.NewServer(mux)
	defer kernel.Close()

	a, _, cap := testApp(t)
	if err := a.create("research"); err != nil {
		t.Fatal(err)
	}
	cl := client.New(kernel.URL, nil)
	sm := scs.New()
	a.deps.Client, a.deps.Sessions = cl, session.NewManager(sm, cl)

	h := sm.LoadAndSave(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = a.deps.Sessions.Login(r.Context(), client.Tokens{AccessToken: "t", RefreshToken: "r"}, "sys", session.Pin{})
		r.SetPathValue("handle", "research")
		a.getDetail(w, r)
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/agents/research", nil))

	v, ok := cap.data.(detailView)
	if !ok {
		t.Fatalf("view = %T", cap.data)
	}
	if v.Account == nil || v.Account.Available != 3000000 {
		t.Fatalf("the agent's account must be read: %+v", v.Account)
	}
	if len(v.Activity) != 1 {
		t.Fatalf("the agent's rows must be found: %+v", v.Activity)
	}
	if act, ok := v.Actions["A1"]; !ok || act.ID == "" {
		t.Errorf("each row's action must be resolved, or it renders as deleted: %+v", v.Actions)
	}
}
