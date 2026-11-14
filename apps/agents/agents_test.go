package agents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/alexedwards/scs/v2"
	"github.com/daios-ai/juice-ui/apps"
	"github.com/daios-ai/juice-ui/client"
	"github.com/daios-ai/juice-ui/session"
	"github.com/daios-ai/juice-ui/supervisor"
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

	// The editor the app asked for, and what it was handed: the fake answers
	// a form naming the file, a version of its length, and a fixed write.
	schema  string
	hidden  []string
	file    string
	to      string // what Read answers; "" answers unchanged
	switchd string // ToJSON / ToForm, with the file they were given
}

func (c *capture) Form(file []byte) (any, error) {
	c.file = string(file)
	return "form of " + c.file, nil
}
func (c *capture) Version(file []byte) string { return fmt.Sprint("v", len(file)) }
func (c *capture) Read(r *http.Request, file []byte) ([]byte, bool, error) {
	c.file = string(file)
	return []byte(c.to), c.to != "", nil
}
func (c *capture) ToJSON(w http.ResponseWriter, r *http.Request, file []byte) {
	c.switchd = "json:" + string(file)
}
func (c *capture) ToForm(w http.ResponseWriter, r *http.Request, file []byte) {
	c.switchd = "form:" + string(file)
}

func testApp(t *testing.T) (*Agents, *fakePrograms, *capture) {
	t.Helper()
	fl := &fakePrograms{logs: t.TempDir()}
	a := &Agents{root: t.TempDir(), client: t.TempDir(), schema: filepath.Join(t.TempDir(), schemaFile)}
	cap := &capture{}
	a.deps = apps.Deps{
		Programs: fl,
		ConfigEditor: func(schema json.RawMessage, hidden ...string) apps.ConfigEditor {
			cap.schema, cap.hidden = string(schema), hidden
			return cap
		},
		Me:        func(r *http.Request) string { return "sys@testnet" },
		Address:   func(h string) string { return h + "@testnet" },
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
	a.deps.HandleErrFragment = a.deps.HandleErr
	a.deps.Superuser = func(*http.Request) bool { return true }
	a.deps.Fragment = func(name string, data any) (template.HTML, error) {
		cap.fragment, cap.data = name, data
		return template.HTML("<" + name + ">"), nil
	}
	return a, fl, cap
}

func postForm(path, body string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return req
}

// ---- store ----

// A home is named by the agent's login and holds a blank agent.md; config.json
// stays absent (the daemon's defaults) and nothing else is written.
func TestCreateScaffolds(t *testing.T) {
	a, _, _ := testApp(t)
	if err := a.create("research"); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(a.root, "research@testnet")
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		t.Fatalf("home not a dir: %v", err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Errorf("home perm = %o, want 700", info.Mode().Perm())
	}
	fi, err := os.Stat(filepath.Join(dir, agentFile))
	if err != nil || fi.Size() != 0 || fi.Mode().Perm() != 0o600 {
		t.Fatalf("agent.md must exist, blank, 0600: %v %+v", err, fi)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Errorf("only agent.md is written, got %d entries", len(entries))
	}
}

func TestCreateExistingIsRefusedWithoutTruncation(t *testing.T) {
	a, _, _ := testApp(t)
	if err := a.create("x"); err != nil {
		t.Fatal(err)
	}
	if err := a.writeFile("x", agentFile, "hand-authored"); err != nil {
		t.Fatal(err)
	}
	if err := a.create("x"); err != errExists {
		t.Fatalf("second create = %v, want errExists", err)
	}
	got, _ := a.readFile("x", agentFile)
	if got != "hand-authored" {
		t.Fatalf("agent.md was clobbered: %q", got)
	}
}

func TestWriteAndReadEditableFiles(t *testing.T) {
	a, _, _ := testApp(t)
	_ = a.create("x")
	if err := a.writeFile("x", agentFile, "You are helpful."); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got, err := a.readFile("x", agentFile); err != nil || got != "You are helpful." {
		t.Fatalf("read = %q (%v)", got, err)
	}
	// config.json is not the file route's: it has the editor (§6.1).
	if err := a.writeFile("x", configFile, "{}"); err != errBadHandle {
		t.Fatalf("writeFile(config.json) = %v, want refused", err)
	}
}

func TestListSorted(t *testing.T) {
	a, _, _ := testApp(t)
	_ = a.create("zebra")
	_ = a.create("alpha")
	// A stray file, another kernel's agent and a bare-named home are not this
	// kernel's agents.
	_ = os.WriteFile(filepath.Join(a.root, "notes.txt"), []byte("x"), 0o600)
	_ = os.Mkdir(filepath.Join(a.root, "beta@other"), 0o700)
	_ = os.Mkdir(filepath.Join(a.root, "gamma"), 0o700)
	rows, err := a.list(httptest.NewRequest(http.MethodGet, "/agents", nil))
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
	if _, err := os.Stat(filepath.Join(a.root, "x@testnet")); !os.IsNotExist(err) {
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
	// A valid handle resolves to its login, a direct child of root.
	dir, err := a.agentDir("ok")
	if err != nil || dir != filepath.Join(a.root, "ok@testnet") {
		t.Fatalf("agentDir(ok) = %q, %v", dir, err)
	}
}

// While the kernel's name is unknown there is no login, so no home is named.
func TestAgentDirRefusesUnknownKernel(t *testing.T) {
	a, _, _ := testApp(t)
	a.deps.Address = func(string) string { return "" }
	if _, err := a.agentDir("ok"); err != errBadHandle {
		t.Fatalf("agentDir with no kernel name = %v, want errBadHandle", err)
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
	// Each row carries a CSRF field: its status cell holds the Start/Stop form.
	if lv.Agents[0].CSRF == "" {
		t.Error("row missing CSRF for its inline form")
	}
}

// refused reads the New agent dialog as it came back: open, at 422, with its
// reason and the handle as typed, and never the password.
func refused(t *testing.T, cap *capture, handle string) string {
	t.Helper()
	v, ok := cap.data.(listView)
	if !ok || cap.page != "agents" || cap.status != http.StatusUnprocessableEntity || !v.OpenForm || v.Handle != handle || v.Error == "" {
		t.Fatalf("want the dialog back open at 422 with the handle kept, got %s %d %+v", cap.page, cap.status, cap.data)
	}
	return v.Error
}

// Ill-formed input is refused before any kernel call: an invalid handle, a
// sigiled one (refused, not rewritten to the bare one — §4; rewriting it would
// make "@research" and "research" the same name here and nowhere else), a home
// that exists, and no password.
func TestPostCreateRefusesBeforeTheKernel(t *testing.T) {
	a, k, _, cap := kernelApp(t)
	_ = a.create("taken")
	for _, c := range []struct{ body, handle, msg string }{
		{"handle=Bad%21&password=pw", "Bad!", "lowercase"},
		{"handle=%40research&password=pw", "@research", "lowercase"},
		{"handle=taken&password=pw", "taken", "already exists"},
		{"handle=research", "research", "password"},
	} {
		a.postCreate(httptest.NewRecorder(), postForm("/agents", c.body))
		if msg := refused(t, cap, c.handle); !strings.Contains(msg, c.msg) {
			t.Fatalf("%s: error = %q, want %q", c.body, msg, c.msg)
		}
	}
	if len(k.paths) != 0 {
		t.Fatalf("no kernel call before the input is whole: %v", k.paths)
	}
	if _, err := os.Stat(filepath.Join(a.root, "research@testnet")); !os.IsNotExist(err) {
		t.Fatal("a refused create makes no home")
	}
}

// New agent creates the account, signs it in, makes the home and starts the
// daemon; a handle the kernel already holds is adopted, not created twice.
func TestPostCreateMakesAccountSessionHomeAndStarts(t *testing.T) {
	a, k, fl, cap := kernelApp(t)
	a.postCreate(httptest.NewRecorder(), postForm("/agents", "handle=research&password=pw"))
	if cap.flashKind != "success" || cap.flashTo != "/agents/research" {
		t.Fatalf("create flash = %q to %q (%+v)", cap.flashKind, cap.flashTo, cap.data)
	}
	if len(k.created) != 1 || k.created[0] != "research@testnet" {
		t.Fatalf("created = %v", k.created)
	}
	if _, err := os.Stat(a.credPath("research")); err != nil {
		t.Fatal("the session must be written")
	}
	if _, err := os.Stat(filepath.Join(a.root, "research@testnet", agentFile)); err != nil {
		t.Fatal("the home must be made")
	}
	if len(fl.started) != 1 || fl.started[0].login != "research@testnet" {
		t.Fatalf("the daemon must start: %+v", fl.started)
	}
	// An account the kernel holds already: adopted on its password.
	k.me = "taken@testnet"
	a.postCreate(httptest.NewRecorder(), postForm("/agents", "handle=taken&password=pw"))
	if cap.flashKind != "success" || !a.exists("taken") {
		t.Fatalf("adopting an existing account: %q %+v", cap.flashKind, cap.data)
	}
}

// A wrong password comes back in the dialog as the kernel's refusal, and
// nothing is made.
func TestPostCreateWrongPassword(t *testing.T) {
	a, _, fl, cap := kernelApp(t)
	a.postCreate(httptest.NewRecorder(), postForm("/agents", "handle=research&password=nope"))
	if msg := refused(t, cap, "research"); msg != "Password is incorrect." {
		t.Fatalf("error = %q", msg)
	}
	if a.exists("research") || len(fl.started) != 0 {
		t.Fatal("a refused sign-in makes no home and starts nothing")
	}
}

// Start with a password makes the login's session first, then spawns; a wrong
// password returns the Start dialog open and spawns nothing; while a daemon is
// alive the post is a no-op and writes no credential.
func TestPostStartWithPassword(t *testing.T) {
	a, _, fl, cap := kernelApp(t)
	_ = a.create("research")
	post := func(body string) {
		req := postForm("/agents/research/start", body)
		req.SetPathValue("handle", "research")
		a.postStart(httptest.NewRecorder(), req)
	}
	post("password=nope")
	v, ok := cap.data.(detailView)
	if !ok || cap.status != http.StatusUnprocessableEntity || v.OpenForm != "start" || v.Error != "Password is incorrect." || len(fl.started) != 0 {
		t.Fatalf("want the Start dialog back open at 422 and nothing spawned, got %d %+v", cap.status, cap.data)
	}
	post("password=pw")
	if cap.err != nil || len(fl.started) != 1 {
		t.Fatalf("start with a password: %v started=%d", cap.err, len(fl.started))
	}
	st, err := os.Stat(a.credPath("research"))
	if err != nil {
		t.Fatal("the session must be written before the spawn")
	}
	fl.drain = true
	_ = fl.Stop(programName("research@testnet"))
	post("password=pw")
	if len(fl.started) != 1 {
		t.Fatalf("a draining daemon is alive: no second spawn, started=%d", len(fl.started))
	}
	if st2, _ := os.Stat(a.credPath("research")); st2.ModTime() != st.ModTime() {
		t.Fatal("no credential is written while the agent runs")
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
	_ = a.writeFile("research", agentFile, "hello")
	req = httptest.NewRequest(http.MethodGet, "/agents/research", nil)
	req.SetPathValue("handle", "research")
	a.getDetail(httptest.NewRecorder(), req)
	if cap.err != nil {
		t.Fatalf("unexpected err: %v", cap.err)
	}
	dv, ok := cap.data.(detailView)
	if !ok || dv.Handle != "research" || dv.Agent != "hello" || dv.Config != "form of " || dv.Version != "v0" {
		t.Fatalf("detailView = %+v", cap.data)
	}
}

func TestPostSaveFile(t *testing.T) {
	a, _, cap := testApp(t)
	_ = a.create("research")
	// Save agent.md.
	req := postForm("/agents/research/files/agent.md", "content=You+are+concise.")
	req.SetPathValue("handle", "research")
	req.SetPathValue("file", agentFile)
	a.postSaveFile(httptest.NewRecorder(), req)
	if cap.flashKind != "success" {
		t.Fatalf("save flash = %q", cap.flashKind)
	}
	if got, _ := a.readFile("research", agentFile); got != "You are concise." {
		t.Fatalf("agent.md = %q", got)
	}
	// A file of the earlier layout is not an editable target → not found.
	cap.err = nil
	req = postForm("/agents/research/files/memory.md", "content=tamper")
	req.SetPathValue("handle", "research")
	req.SetPathValue("file", "memory.md")
	a.postSaveFile(httptest.NewRecorder(), req)
	if cap.err == nil {
		t.Fatal("saving memory.md should be rejected")
	}
	// config.json is not this route's.
	cap.err = nil
	req = postForm("/agents/research/files/config.json", "content={}")
	req.SetPathValue("handle", "research")
	req.SetPathValue("file", configFile)
	a.postSaveFile(httptest.NewRecorder(), req)
	if cap.err == nil {
		t.Fatal("config.json is saved by its own handler alone")
	}
}

// config.json is edited as the form of the daemon's schema of it (§6.1,
// §6.6): the page carries the editor's form over the file and its version;
// a save writes what the editor reads, whole, by rename; a stale version, an
// unchanged form and an unreadable file write nothing; a running agent's flash
// says the file applies at its next start; the switch gets the file.
func TestConfigIsTheDaemonsSchemaForm(t *testing.T) {
	a, _, cap := testApp(t)
	_ = a.create("research")
	dir, _ := a.agentDir("research")
	path := filepath.Join(dir, configFile)
	_ = os.WriteFile(a.schema, []byte(`{"type":"object"}`), 0o600)
	_ = os.WriteFile(path, []byte(`{"max_turns":32}`), 0o600)

	get := func() detailView {
		req := httptest.NewRequest(http.MethodGet, "/agents/research", nil)
		req.SetPathValue("handle", "research")
		a.getDetail(httptest.NewRecorder(), req)
		dv, _ := cap.data.(detailView)
		return dv
	}
	dv := get()
	if cap.schema != `{"type":"object"}` || strings.Join(cap.hidden, ",") != "$schema" {
		t.Errorf("the editor is the daemon's schema with $schema hidden: %q %v", cap.schema, cap.hidden)
	}
	if dv.Config != `form of {"max_turns":32}` || dv.Version != "v16" {
		t.Errorf("Config = %v, Version = %q", dv.Config, dv.Version)
	}

	post := func(body string, h http.HandlerFunc) {
		cap.flashKind, cap.flashMsg, cap.err = "", "", nil
		req := postForm("/agents/research/config", body)
		req.SetPathValue("handle", "research")
		h(httptest.NewRecorder(), req)
	}
	cap.to = `{"max_turns": 8}`
	post("version=v16", a.postConfig)
	if b, _ := os.ReadFile(path); cap.flashKind != "success" || cap.flashMsg != "Saved." || string(b) != "{\"max_turns\": 8}\n" {
		t.Errorf("save: flash %q %q, file %q", cap.flashKind, cap.flashMsg, b)
	}
	if left, _ := filepath.Glob(filepath.Join(dir, configFile+".*")); len(left) != 0 {
		t.Errorf("a temp file remains: %v", left)
	}
	post("version=v16", a.postConfig)
	if cap.flashKind != "error" || !strings.Contains(cap.flashMsg, "reload it") {
		t.Errorf("a stale version: flash %q %q", cap.flashKind, cap.flashMsg)
	}
	cap.to = ""
	post("version=v17", a.postConfig)
	if cap.flashKind != "info" || cap.flashMsg != "Nothing changed." {
		t.Errorf("unchanged: flash %q %q", cap.flashKind, cap.flashMsg)
	}
	post("", a.postConfigJSON)
	post("", a.postConfigForm)
	if cap.switchd != "form:{\"max_turns\": 8}\n" {
		t.Errorf("the switch reads the file: %q", cap.switchd)
	}

	// A running agent applies the file at its next start.
	lock, err := hold(dir)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintf(lock, "%d\n", os.Getpid())
	cap.to = `{"max_turns": 9}`
	post("version=v17", a.postConfig)
	lock.Close()
	if cap.flashKind != "success" || cap.flashMsg != "Saved; the agent applies it at its next start." {
		t.Errorf("running: flash %q %q", cap.flashKind, cap.flashMsg)
	}

	// An unreadable file is an error, never an empty form saved over.
	_ = os.Remove(path)
	_ = os.Mkdir(path, 0o700)
	if get(); cap.err == nil {
		t.Error("an unreadable config.json renders an error")
	}
	post("version=v0", a.postConfig)
	if cap.err == nil || cap.flashKind != "" {
		t.Errorf("an unreadable config.json is not saved over: err %v, flash %q", cap.err, cap.flashKind)
	}

	// No schema yet: the editor is asked with none.
	_ = os.Remove(path)
	_ = os.Remove(a.schema)
	cap.err = nil
	if get(); cap.schema != "" || cap.err != nil {
		t.Errorf("without a schema file the editor gets none: %q %v", cap.schema, cap.err)
	}
}

// Start asks for nothing: the daemon is run as the agent's login, with its own
// session, and writes this run's log where the status reads it back.
func TestPostStartRunsTheLogin(t *testing.T) {
	a, fl, cap := testApp(t)
	_ = a.create("research")
	req := postForm("/agents/research/start", "")
	req.SetPathValue("handle", "research")
	rec := httptest.NewRecorder()
	a.postStart(rec, req)
	if rec.Code != http.StatusSeeOther || cap.flashMsg != "" || len(fl.started) != 1 {
		t.Fatalf("start: code=%d flash=%q started=%d", rec.Code, cap.flashMsg, len(fl.started))
	}
	p := fl.progs["agent-research@testnet"]
	if got := strings.Join(p.Command, " "); got != "juice-agent serve research@testnet" {
		t.Fatalf("command = %s", got)
	}
	dir, _ := a.agentDir("research")
	if p.Env["JUICE_HOME"] != filepath.Dir(a.root) || len(p.Env) != 1 || p.Lock != filepath.Join(dir, "serve.lock") || p.Disabled {
		t.Fatalf("program = %+v", p)
	}
	if !fl.running("research@testnet") {
		t.Fatal("want running after start")
	}
}

// One status shape renders everywhere the fragment does — a list row, the
// polled fragment and the detail page — and a stopped agent carries its
// daemon's exit error from the current run.
func TestStatusShapeIsShared(t *testing.T) {
	a, _, cap := testApp(t)
	_ = a.create("research")
	_ = os.WriteFile(a.logPath("research"), []byte("{\"event\":\"login\"}\nerror: no session for research@testnet\n"), 0o600)
	const want = "error: no session for research@testnet"

	a.getIndex(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/agents", nil))
	if lv := cap.data.(listView); lv.Agents[0].LastLine != want {
		t.Errorf("list row LastLine = %q", lv.Agents[0].LastLine)
	}
	req := httptest.NewRequest(http.MethodGet, "/tick", nil)
	if h, _ := a.watch(req, "research", "running"); h != "<_agent_status>" {
		t.Fatalf("watch = %q", h)
	}
	st := cap.data.(status)
	if st.LastLine != want || !strings.HasPrefix(st.State, "ask.") || !st.OOB {
		t.Errorf("heartbeat status = %+v", st)
	}
	if h, _ := a.watch(req, "research", st.State); h != "" {
		t.Errorf("an unchanged state must not be swapped, got %q", h)
	}
	// The same state with another exit line is a change: the token names the
	// line too, so a failed start between two ticks is not hidden.
	_ = os.WriteFile(a.logPath("research"), []byte("error: another juice-agent is already serving research@testnet\n"), 0o600)
	if h, _ := a.watch(req, "research", st.State); h != "<_agent_status>" {
		t.Errorf("a new exit line must be swapped, got %q", h)
	}
	if st2 := cap.data.(status); st2.State == st.State || !strings.HasPrefix(st2.State, "ask.") {
		t.Errorf("token = %q, was %q", st2.State, st.State)
	}
	_ = os.WriteFile(a.logPath("research"), []byte("{\"event\":\"login\"}\nerror: no session for research@testnet\n"), 0o600)
	a.deps.Superuser = func(*http.Request) bool { return false }
	if h, _ := a.watch(req, "research", "running"); h != "" {
		t.Errorf("nothing to anyone but the operator, got %q", h)
	}
	a.deps.Superuser = func(*http.Request) bool { return true }
	req = httptest.NewRequest(http.MethodGet, "/agents/research", nil)
	req.SetPathValue("handle", "research")
	a.getDetail(httptest.NewRecorder(), req)
	if dv := cap.data.(detailView); dv.LastLine != want {
		t.Errorf("detail LastLine = %q", dv.LastLine)
	}
	// A running agent states no exit.
	_ = a.start("research")
	if st, _ := a.status(req, "research"); !st.Running || st.LastLine != "" {
		t.Errorf("running status = %+v", st)
	}
}

func TestPostStopAndStatusFragment(t *testing.T) {
	a, fl, cap := testApp(t)
	_ = a.create("research")
	_ = a.start("research")

	// The heartbeat reports running.
	req := httptest.NewRequest(http.MethodGet, "/tick", nil)
	_, _ = a.watch(req, "research", "stopped")
	if cap.fragment != "_agent_status" {
		t.Fatalf("fragment = %q", cap.fragment)
	}
	if sv, ok := cap.data.(status); !ok || !sv.Running || sv.State != "running" {
		t.Fatalf("statusView = %+v", cap.data)
	}

	// Stop: a plain redirect, no banner; the heartbeat states what follows.
	req = postForm("/agents/research/stop", "")
	req.SetPathValue("handle", "research")
	a.postStop(httptest.NewRecorder(), req)
	if fl.running("research@testnet") {
		t.Fatal("want not running after stop")
	}
	if !fl.progs["agent-research@testnet"].Disabled {
		t.Fatal("a stopped agent stays stopped")
	}
}

// Delete is refused while the daemon is alive — stopped or not, a draining
// process still reads its home and its credential — and removes home, log and
// credential once it has exited.
func TestPostDeleteRequiresStopped(t *testing.T) {
	a, fl, cap := testApp(t)
	a.client = t.TempDir()
	_ = a.create("research")
	_ = client.WriteCredential(a.credPath("research"), client.Tokens{}, "P1")
	_ = a.start("research")
	fl.drain = true
	_ = fl.Stop(programName("research@testnet"))
	req := postForm("/agents/research/delete", "")
	req.SetPathValue("handle", "research")
	a.postDelete(httptest.NewRecorder(), req)
	if cap.flashKind != "error" || !a.exists("research") {
		t.Fatalf("delete while alive: %q %q", cap.flashKind, cap.flashMsg)
	}
	fl.up["agent-research@testnet"] = false
	_ = os.WriteFile(a.logPath("research"), []byte("error: x\n"), 0o600)
	a.postDelete(httptest.NewRecorder(), req)
	if cap.flashKind != "success" {
		t.Fatalf("delete flash = %q %q", cap.flashKind, cap.flashMsg)
	}
	if _, ok := fl.progs["agent-research@testnet"]; ok {
		t.Error("the program goes with the agent")
	}
	for _, p := range []string{filepath.Join(a.root, "research@testnet"), a.credPath("research"), a.logPath("research")} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s not removed on delete", p)
		}
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
	if len(entries) != 1 || entries[0].Href != "/agents" || entries[0].Live {
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
	if got := defaultRoot("/home/u/.juice/ui"); got != filepath.FromSlash("/home/u/.juice/agents") {
		t.Fatalf("defaultRoot = %q", got)
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
		Watch: func(string, apps.Watcher) {},
	}
	New().Mount(http.NewServeMux(), d)
	if len(auth) != 0 {
		t.Errorf("a host-acting app must gate on the operator, not on any session (%d routes used RequireAuth)", len(auth))
	}
	if len(superuser) != 10 {
		t.Errorf("every route must be gated: %d of 10", len(superuser))
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
}

// The agent's home is named by its login; its principal is that handle's
// address on this kernel, and the page reads that one row and nothing else:
// the kernel filters transactions by process, not by party, so its history is
// the transactions page, never a scan of the whole ledger here.
func TestAgentAccountIsOneReadByAddress(t *testing.T) {
	mux := http.NewServeMux()
	var paths []string
	mux.HandleFunc("GET /v1/admin/users/{address}", func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		w.Write([]byte(`{"address":{"address":"research@testnet","principal":"research@testnet"},"available":3000000,"locked":0}`))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		http.NotFound(w, r)
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
		_ = a.deps.Sessions.Login(r.Context(), client.Tokens{AccessToken: "t", RefreshToken: "r"}, "sys@testnet", session.Pin{})
		r.SetPathValue("handle", "research")
		a.getDetail(w, r)
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/agents/research", nil))

	v, ok := cap.data.(detailView)
	if !ok {
		t.Fatalf("view = %T", cap.data)
	}
	if len(paths) != 1 || paths[0] != "/v1/admin/users/research@testnet" {
		t.Errorf("one read, by address: %v", paths)
	}
	if v.Account == nil || v.Account.Available != 3000000 {
		t.Fatalf("the agent's account must be read: %+v", v.Account)
	}
}

// Running is the daemon's own word, its lock in the home, whoever started it: a
// locked home is running with no child of the supervisor's, Start starts nothing,
// Delete is refused, and Stop goes to the supervisor, which stops the holder. A
// lock nobody holds is a stale number; a lock that cannot be read is an error,
// never "stopped".
func TestRunningByTheLock(t *testing.T) {
	a, fl, cap := testApp(t)
	_ = a.create("research")
	dir, _ := a.agentDir("research")
	lock := filepath.Join(dir, "serve.lock")
	f, err := os.OpenFile(lock, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	if running, err := a.running("research"); err != nil || !running {
		t.Fatalf("a held lock is running: %v %v", running, err)
	}
	post := func(path string, h http.HandlerFunc) {
		req := postForm(path, "")
		req.SetPathValue("handle", "research")
		h(httptest.NewRecorder(), req)
	}
	post("/agents/research/start", a.postStart)
	if len(fl.started) != 0 || cap.err != nil {
		t.Fatalf("Start on a served agent starts nothing: started=%d %v", len(fl.started), cap.err)
	}
	post("/agents/research/delete", a.postDelete)
	if cap.flashKind != "error" || !a.exists("research") {
		t.Fatalf("Delete on a served agent is refused: %q", cap.flashKind)
	}
	post("/agents/research/stop", a.postStop)
	if cap.err != nil || !fl.progs["agent-research@testnet"].Disabled || fl.stops != 1 {
		t.Fatalf("Stop declares the agent, disabled, and stops it: %v %+v %d", cap.err, fl.progs, fl.stops)
	}
	// Released: the number is stale, nobody serves.
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	if running, err := a.running("research"); err != nil || running {
		t.Fatalf("a free lock is stopped: %v %v", running, err)
	}
	// An unreadable lock is an error, in the list and in the fragment.
	_ = os.Chmod(lock, 0)
	cap.err = nil
	a.getIndex(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/agents", nil))
	if cap.err == nil {
		t.Fatal("an unreadable lock must fail the page, not read as stopped")
	}
}

// Start asks for the password only when the login has no session to serve
// on: no credential file, or a last run ended on the daemon's own advice.
func TestStartAsksForThePasswordWithoutASession(t *testing.T) {
	a, _, _ := testApp(t)
	_ = a.create("research")
	req := httptest.NewRequest(http.MethodGet, "/agents/research", nil)
	if st, _ := a.status(req, "research"); !st.AskPassword {
		t.Fatal("no credential: ask")
	}
	_ = client.WriteCredential(a.credPath("research"), client.Tokens{AccessToken: "t"}, "P1")
	if st, _ := a.status(req, "research"); st.AskPassword {
		t.Fatal("a credential and a clean log: one click")
	}
	_ = os.WriteFile(a.logPath("research"), []byte("error: no session for research@testnet: run juice auth login research@testnet\n"), 0o600)
	if st, _ := a.status(req, "research"); !st.AskPassword {
		t.Fatal("the daemon said to log in: ask")
	}
}

// The supervisor finds a bare name in the installation's bin/, then PATH; the
// explicit override is written into the program.
func TestTheBinaryOverrideIsTheProgramsCommand(t *testing.T) {
	a, fl, _ := testApp(t)
	_ = a.create("research")
	t.Setenv(binEnv, "/opt/juice-agent")
	if err := a.start("research"); err != nil {
		t.Fatal(err)
	}
	if got := fl.progs["agent-research@testnet"].Command[0]; got != "/opt/juice-agent" {
		t.Errorf("command = %s", got)
	}
}

// Why a program could not be started is what the page says.
func TestTheSupervisorsErrorIsTheLastLine(t *testing.T) {
	a, fl, _ := testApp(t)
	_ = a.create("research")
	_ = a.start("research")
	fl.up["agent-research@testnet"] = false
	fl.errs = "exec: \"juice-agent\": executable file not found"
	st, _ := a.status(httptest.NewRequest(http.MethodGet, "/agents/research", nil), "research")
	if st.LastLine != fl.errs {
		t.Errorf("last line = %q", st.LastLine)
	}
}

// adopt carries over an installation made before the supervisor: every home of
// this kernel gets a program, one with a session running and one without stopped,
// and a program the agent already has is left as it says.
func TestAdoptDeclaresTheExistingAgents(t *testing.T) {
	a, fl, _ := testApp(t)
	_ = a.create("withsession")
	_ = a.create("nosession")
	_ = a.create("stopped")
	_ = os.MkdirAll(filepath.Join(a.root, "other@elsewhere"), 0o700)
	_ = client.WriteCredential(a.credPath("withsession"), client.Tokens{}, "P1")
	_ = client.WriteCredential(a.credPath("stopped"), client.Tokens{}, "P2")
	if _, err := a.declare("stopped", false); err != nil {
		t.Fatal(err)
	}
	a.adopt()
	if p, ok := fl.progs["agent-withsession@testnet"]; !ok || p.Disabled {
		t.Errorf("with a session it runs: %+v", p)
	}
	if p, ok := fl.progs["agent-nosession@testnet"]; !ok || !p.Disabled {
		t.Errorf("without one it is stopped: %+v", p)
	}
	if !fl.progs["agent-stopped@testnet"].Disabled {
		t.Error("a program the agent has says whether it runs")
	}
	if len(fl.progs) != 3 {
		t.Errorf("another kernel's agent is not this UI's: %+v", fl.progs)
	}
}

// hold takes the home's lock and keeps it until closed: a second hold, and the
// probe, see it held; a daemon's lock refuses hold with errHeld; a home with no
// lock file yet is free. (Delete holds it through the removal.)
func TestHoldTakesTheLockUntilClosed(t *testing.T) {
	home := t.TempDir()
	lock := filepath.Join(home, "serve.lock")
	f, err := hold(home)
	if err != nil {
		t.Fatalf("a home with no lock file is free: %v", err)
	}
	if _, err := hold(home); !errors.Is(err, errHeld) {
		t.Fatalf("second hold = %v, want errHeld", err)
	}
	if h, err := supervisor.Held(lock); err != nil || !h {
		t.Fatalf("held = %v %v", h, err)
	}
	_ = f.Close()
	if h, err := supervisor.Held(lock); err != nil || h {
		t.Fatalf("after close held = %v %v", h, err)
	}
	if _, err := hold(filepath.Join(home, "absent")); err == nil || errors.Is(err, errHeld) {
		t.Fatalf("a missing home is an error, not held: %v", err)
	}
}

// Only the daemon's own `error:` line reads as an exit reason.
func TestExitError(t *testing.T) {
	log := filepath.Join(t.TempDir(), "a.log")
	if got := exitError(log); got != "" {
		t.Fatalf("no log states no reason, got %q", got)
	}
	_ = os.WriteFile(log, []byte("{\"event\":\"login\"}\nerror: no session for a@k\n"), 0o600)
	if got := exitError(log); got != "error: no session for a@k" {
		t.Fatalf("exitError = %q", got)
	}
	_ = os.WriteFile(log, []byte("error: old\n{\"event\":\"stopped\"}\n"), 0o600)
	if got := exitError(log); got != "" {
		t.Fatalf("a later run replaces the old refusal, got %q", got)
	}
}

// startRec is one start the fake supervisor saw.
type startRec struct{ login string }

// fakePrograms stands in for the supervisor.
type fakePrograms struct {
	mu      sync.Mutex
	logs    string
	progs   map[string]supervisor.Program
	up      map[string]bool
	started []startRec
	stops   int
	drain   bool // a daemon that keeps running after SIGTERM to finish its tasks
	errs    string
}

func (f *fakePrograms) begin(name string) {
	if f.up == nil {
		f.up = map[string]bool{}
	}
	if !f.up[name] {
		f.up[name] = true
		f.started = append(f.started, startRec{f.progs[name].Command[2]})
	}
}

func (f *fakePrograms) Set(name string, p supervisor.Program) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.progs == nil {
		f.progs = map[string]supervisor.Program{}
	}
	f.progs[name] = p
	if !p.Disabled {
		f.begin(name)
	}
	return nil
}

func (f *fakePrograms) Remove(name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.progs, name)
	delete(f.up, name)
	_ = os.Remove(filepath.Join(f.logs, name+".log"))
	return nil
}

func (f *fakePrograms) Start(name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := f.progs[name]
	p.Disabled = false
	f.progs[name] = p
	f.begin(name)
	return nil
}

func (f *fakePrograms) Stop(name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := f.progs[name]
	p.Disabled = true
	f.progs[name] = p
	f.stops++
	if f.up == nil {
		f.up = map[string]bool{}
	}
	f.up[name] = f.drain
	return nil
}

func (f *fakePrograms) Status(name string) (supervisor.Status, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.progs[name]
	return supervisor.Status{Running: f.up[name], Log: filepath.Join(f.logs, name+".log"), Err: f.errs}, ok
}

func (f *fakePrograms) running(login string) bool {
	st, _ := f.Status(programName(login))
	return st.Running
}

// logPath is where handle's program writes.
func (a *Agents) logPath(handle string) string {
	st, _ := a.deps.Programs.Status(programName(a.deps.Address(handle)))
	return st.Log
}

// assistantApp is kernelApp with the app's state kept in memory, seeded with
// state; saves counts the writes.
func assistantApp(t *testing.T, state string) (*Agents, *fakeKernel, *fakePrograms, *int) {
	t.Helper()
	a, k, fl, _ := kernelApp(t)
	k.me = "assistant@testnet"
	saves := 0
	a.deps.LoadAppState = func(string) (json.RawMessage, error) { return json.RawMessage(state), nil }
	a.deps.SaveAppState = func(_ string, v json.RawMessage) error { state = string(v); saves++; return nil }
	return a, k, fl, &saves
}

// The assistant is made once: the account under the kept password, the
// session, the home, the daemon. A second pass changes nothing.
func TestEnsureAssistantMakesAndStartsIt(t *testing.T) {
	a, k, fl, saves := assistantApp(t, `{"assistant_password":"pw"}`)
	a.ensureAssistant(context.Background())
	if len(k.created) != 1 || k.created[0] != "assistant@testnet" {
		t.Fatalf("created = %v", k.created)
	}
	if !a.exists("assistant") {
		t.Fatal("no home")
	}
	if _, err := os.Stat(a.credPath("assistant")); err != nil {
		t.Fatalf("no session: %v", err)
	}
	if len(fl.started) != 1 || fl.started[0].login != "assistant@testnet" {
		t.Fatalf("started = %+v", fl.started)
	}
	if *saves != 0 {
		t.Fatal("a kept password is not written again")
	}
	a.ensureAssistant(context.Background())
	if len(k.created) != 1 || len(fl.started) != 1 {
		t.Fatalf("second pass: created %v, started %+v", k.created, fl.started)
	}
}

// Without a kept password one is made and saved before the account, and
// the next pass uses the same one.
func TestEnsureAssistantKeepsItsPassword(t *testing.T) {
	a, k, _, saves := assistantApp(t, "")
	a.ensureAssistant(context.Background())
	if *saves != 1 || len(k.created) != 1 {
		t.Fatalf("saves %d, created %v", *saves, k.created)
	}
	a.ensureAssistant(context.Background())
	if *saves != 1 {
		t.Fatal("the password is made once")
	}
}

// A handle the kernel already holds is adopted only when the kept password
// signs in; otherwise nothing is written.
func TestEnsureAssistantAdoptsOnlyItsOwnAccount(t *testing.T) {
	a, k, fl, _ := assistantApp(t, `{"assistant_password":"pw"}`)
	k.held = map[string]bool{"assistant@testnet": true}
	a.ensureAssistant(context.Background())
	if !a.exists("assistant") || len(fl.started) != 1 {
		t.Fatal("its own account is adopted")
	}
	b, k2, fl2, _ := assistantApp(t, `{"assistant_password":"not-it"}`)
	k2.held = map[string]bool{"assistant@testnet": true}
	b.ensureAssistant(context.Background())
	if b.exists("assistant") || len(fl2.started) != 0 {
		t.Fatal("another's account is not adopted")
	}
}

// An assistant with no program, holding a session, is started; one without a
// session is left; one a person stopped stays stopped.
func TestEnsureAssistantStartsAStoppedOne(t *testing.T) {
	a, _, fl, _ := assistantApp(t, `{"assistant_password":"pw"}`)
	if err := a.create("assistant"); err != nil {
		t.Fatal(err)
	}
	a.ensureAssistant(context.Background())
	if len(fl.started) != 0 {
		t.Fatal("no session, no start")
	}
	if err := a.signIn(context.Background(), "assistant", "pw"); err != nil {
		t.Fatal(err)
	}
	a.ensureAssistant(context.Background())
	if len(fl.started) != 1 {
		t.Fatal("an assistant with a session is started")
	}
	_ = fl.Stop(programName("assistant@testnet"))
	a.ensureAssistant(context.Background())
	if fl.running("assistant@testnet") || len(fl.started) != 1 {
		t.Fatal("a stopped assistant stays stopped")
	}
}
