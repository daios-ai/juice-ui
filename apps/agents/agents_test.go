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
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/alexedwards/scs/v2"
	"github.com/daios-ai/juice-ui/apps"
	"github.com/daios-ai/juice-ui/client"
	"github.com/daios-ai/juice-ui/server"
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
	if err := a.writeFile("x", agentFile, []byte("hand-authored")); err != nil {
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

// Every file of a home is written whole, by a file renamed over it, so a
// daemon starting meanwhile reads the old file or the new one, never part.
func TestWriteReplacesByRename(t *testing.T) {
	a, _, _ := testApp(t)
	_ = a.create("x")
	for _, f := range []string{agentFile, heartbeatFile, configFile} {
		if err := a.writeFile("x", f, []byte("one")); err != nil {
			t.Fatalf("%s: write: %v", f, err)
		}
		dir, _ := a.agentDir("x")
		before, _ := os.Stat(filepath.Join(dir, f))
		if err := a.writeFile("x", f, []byte("two")); err != nil {
			t.Fatalf("%s: write: %v", f, err)
		}
		after, _ := os.Stat(filepath.Join(dir, f))
		if got, err := a.readFile("x", f); err != nil || got != "two" || os.SameFile(before, after) {
			t.Errorf("%s: read = %q (%v), replaced by rename: %v", f, got, err, !os.SameFile(before, after))
		}
	}
	if err := a.writeFile("ghost", agentFile, []byte("x")); err != errNotFound {
		t.Errorf("a home that does not exist is not made: %v", err)
	}
}

// The fleet's cards are this kernel's agents, those that need the operator
// first — stopped on an error — then by handle; a stray file, another kernel's
// agent and a bare-named home are none.
func TestCardsPutWhatNeedsTheOperatorFirst(t *testing.T) {
	a, fl, _ := testApp(t)
	for _, h := range []string{"zebra", "alpha", "mid"} {
		_ = a.create(h)
		_ = a.start(h)
	}
	_ = os.WriteFile(filepath.Join(a.root, "notes.txt"), []byte("x"), 0o600)
	_ = os.Mkdir(filepath.Join(a.root, "beta@other"), 0o700)
	_ = os.Mkdir(filepath.Join(a.root, "gamma"), 0o700)
	cards, err := a.cards(httptest.NewRequest(http.MethodGet, "/agents", nil))
	if err != nil {
		t.Fatal(err)
	}
	if got := handles(cards); got != "alpha,mid,zebra" {
		t.Fatalf("cards = %s, want by handle", got)
	}
	fl.up["agent-zebra@testnet"] = false
	_ = os.WriteFile(a.logPath("zebra"), []byte("error: no session for zebra@testnet\n"), 0o600)
	cards, _ = a.cards(httptest.NewRequest(http.MethodGet, "/agents", nil))
	if got := handles(cards); got != "zebra,alpha,mid" || !cards[0].Attention() || cards[1].Attention() {
		t.Fatalf("cards = %s, want the stopped one first", got)
	}
}

func handles(cards []card) string {
	var out []string
	for _, c := range cards {
		out = append(out, c.Handle)
	}
	return strings.Join(out, ",")
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
		{"handle=research&password=pw&confirm_password=pw2", "research", "don't match"},
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
	a.postCreate(httptest.NewRecorder(), postForm("/agents", "handle=research&password=pw&confirm_password=pw"))
	if cap.flashKind != "success" || cap.flashTo != "/agents?form=fund&handle=research" {
		t.Fatalf("create lands on Fund: %q to %q (%+v)", cap.flashKind, cap.flashTo, cap.data)
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
	a.postCreate(httptest.NewRecorder(), postForm("/agents", "handle=taken&password=pw&confirm_password=pw"))
	if cap.flashKind != "success" || !a.exists("taken") {
		t.Fatalf("adopting an existing account: %q %+v", cap.flashKind, cap.data)
	}
}

// A wrong password comes back in the dialog as the kernel's refusal, and
// nothing is made.
func TestPostCreateWrongPassword(t *testing.T) {
	a, _, fl, cap := kernelApp(t)
	a.postCreate(httptest.NewRecorder(), postForm("/agents", "handle=research&password=nope&confirm_password=nope"))
	if msg := refused(t, cap, "research"); msg != "Password is incorrect." {
		t.Fatalf("error = %q", msg)
	}
	if a.exists("research") || len(fl.started) != 0 {
		t.Fatal("a refused sign-in makes no home and starts nothing")
	}
}

// Start always asks for the password (requirements §6.1): none, or a wrong
// one, returns the Start dialog open and changes nothing; the right one
// records the session and starts the daemon; an agent already running is left
// as it is, nothing signed in.
func TestPostStartAlwaysAsks(t *testing.T) {
	a, k, fl, cap := kernelApp(t)
	_ = a.create("research")
	post := func(body string) *httptest.ResponseRecorder {
		req := postForm("/agents/research/start", body)
		req.SetPathValue("handle", "research")
		rec := httptest.NewRecorder()
		a.postStart(rec, req)
		return rec
	}
	for _, body := range []string{"", "password=nope"} {
		post(body)
		v := opened(cap)
		if cap.status != http.StatusUnprocessableEntity || v.OpenForm != "start" || v.Error != "Password is incorrect." || len(fl.started) != 0 {
			t.Fatalf("%q: want the Start dialog back open at 422 and nothing started, got %d %+v", body, cap.status, cap.data)
		}
		if _, err := os.Stat(a.credPath("research")); !os.IsNotExist(err) {
			t.Fatal("a refused password writes no session")
		}
	}
	post("password=pw")
	if cap.err != nil || len(fl.started) != 1 {
		t.Fatalf("start: %v started=%d", cap.err, len(fl.started))
	}
	if _, err := os.Stat(a.credPath("research")); err != nil {
		t.Fatal("the session is written before the start")
	}
	k.paths = nil
	if rec := post("password=pw"); rec.Code != http.StatusSeeOther || len(fl.started) != 1 || len(k.paths) != 0 {
		t.Fatalf("Start on a running agent does nothing: %d started=%d %v", rec.Code, len(fl.started), k.paths)
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
	_ = a.writeFile("research", agentFile, []byte("hello"))
	req = httptest.NewRequest(http.MethodGet, "/agents/research", nil)
	req.SetPathValue("handle", "research")
	a.getDetail(httptest.NewRecorder(), req)
	if cap.err != nil {
		t.Fatalf("unexpected err: %v", cap.err)
	}
	dv, ok := cap.data.(detailView)
	if !ok || dv.Handle != "research" || len(dv.Texts) != 2 || dv.Texts[0].File != agentFile || dv.Texts[0].Content != "hello" ||
		dv.Texts[1].File != heartbeatFile || dv.Texts[1].Content != "" || dv.Config != "form of " || dv.Version != "v0" {
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
	// The heartbeat's duties are read at the daemon's start, so a running
	// agent's save says so, as a configuration's does; agent.md is read at
	// every job.
	save := func(file string) string {
		t.Helper()
		req := postForm("/agents/research/files/"+file, "content=Check+the+inbox.")
		req.SetPathValue("handle", "research")
		req.SetPathValue("file", file)
		a.postSaveFile(httptest.NewRecorder(), req)
		return cap.flashMsg
	}
	if msg := save(heartbeatFile); msg != "Saved." {
		t.Errorf("stopped: %q", msg)
	}
	if got, _ := a.readFile("research", heartbeatFile); got != "Check the inbox." {
		t.Fatalf("heartbeat.md = %q", got)
	}
	dir, _ := a.agentDir("research")
	lock, err := hold(dir)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintf(lock, "%d\n", os.Getpid())
	if msg := save(heartbeatFile); msg != "Saved. Stop and start research to apply." {
		t.Errorf("running, heartbeat.md: %q", msg)
	}
	if msg := save(agentFile); msg != "Saved." {
		t.Errorf("running, agent.md: %q", msg)
	}
	lock.Close()
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
	if cap.flashKind != "success" || cap.flashMsg != "Saved. Stop and start research to apply." {
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
	a, _, fl, cap := kernelApp(t)
	_ = a.create("research")
	req := postForm("/agents/research/start", "password=pw")
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

// One status shape renders a list row, the page and the row's heartbeat
// answer, its pill and its commands; a stopped agent carries its daemon's
// exit error from the current run.
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
	if h, _ := a.watch(req, "research", "running"); h != "<_agent_state><_agent_commands>" {
		t.Fatalf("watch = %q", h)
	}
	st := cap.data.(card)
	if st.LastLine != want || !strings.HasPrefix(st.State, "stopped.") || !st.OOB || st.Pill() != "stopped" {
		t.Errorf("heartbeat status = %+v", st)
	}
	if h, _ := a.watch(req, "research", st.State); h != "" {
		t.Errorf("an unchanged state must not be swapped, got %q", h)
	}
	// The same state with another exit line is a change: the token names the
	// line too, so a failed start between two ticks is not hidden.
	_ = os.WriteFile(a.logPath("research"), []byte("error: another juice-agent is already serving research@testnet\n"), 0o600)
	if h, _ := a.watch(req, "research", st.State); h != "<_agent_state><_agent_commands>" {
		t.Errorf("a new exit line must be swapped, got %q", h)
	}
	if st2 := cap.data.(card); st2.State == st.State || !strings.HasPrefix(st2.State, "stopped.") {
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
	if st, _ := a.status(req, "research"); !st.Running || st.LastLine != "" || st.Pill() != "running" {
		t.Errorf("running status = %+v", st)
	}
	if (status{Running: true, Short: "no funds"}).Pill() != "no funds" || (status{Short: "no funds"}).Pill() != "stopped" {
		t.Error("one word: stopped, else what stops it working, else running")
	}
}

func TestPostStopAndStatusFragment(t *testing.T) {
	a, fl, cap := testApp(t)
	_ = a.create("research")
	_ = a.start("research")

	// The heartbeat reports running.
	req := httptest.NewRequest(http.MethodGet, "/tick", nil)
	_, _ = a.watch(req, "research", "stopped")
	if cap.fragment != "_agent_commands" {
		t.Fatalf("fragment = %q", cap.fragment)
	}
	if sv, ok := cap.data.(card); !ok || !sv.Running || sv.State != "running" {
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
	if len(entries) != 1 || entries[0].Href != "/agents" || !entries[0].Live {
		t.Fatalf("nav = %+v", entries)
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
	if len(superuser) != 13 {
		t.Errorf("every route must be gated: %d of 13", len(superuser))
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
		_, _ = a.deps.Sessions.Login(r.Context(), client.Tokens{AccessToken: "t", RefreshToken: "r"}, "sys@testnet", session.Pin{})
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
	a, _, fl, cap := kernelApp(t)
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

// The assistant is made with the password the installer asked for, once: the
// account, the session, the home, the daemon; the password is then forgotten,
// and a second pass changes nothing. Nothing is generated or kept.
func TestEnsureAssistantUsesTheInstallersPassword(t *testing.T) {
	a, k, fl, saves := assistantApp(t, "")
	a.ensureAssistant(context.Background())
	if len(k.created) != 0 || a.exists("assistant") {
		t.Fatal("without the installer's password no assistant is made")
	}
	a.WithAssistant("pw")
	a.ensureAssistant(context.Background())
	if len(k.created) != 1 || k.created[0] != "assistant@testnet" || !a.exists("assistant") {
		t.Fatalf("created = %v", k.created)
	}
	if _, err := os.Stat(a.credPath("assistant")); err != nil {
		t.Fatalf("no session: %v", err)
	}
	if len(fl.started) != 1 || fl.started[0].login != "assistant@testnet" {
		t.Fatalf("started = %+v", fl.started)
	}
	if a.assistant != "" || *saves != 0 {
		t.Fatal("the password is used once, forgotten and written nowhere")
	}
	a.ensureAssistant(context.Background())
	if len(k.created) != 1 || len(fl.started) != 1 {
		t.Fatalf("second pass: created %v, started %+v", k.created, fl.started)
	}
}

// A handle the kernel already holds is adopted only when the password signs
// in; otherwise nothing is made.
func TestEnsureAssistantAdoptsOnlyItsOwnAccount(t *testing.T) {
	a, k, fl, _ := assistantApp(t, "")
	k.held = map[string]bool{"assistant@testnet": true}
	a.WithAssistant("pw")
	a.ensureAssistant(context.Background())
	if !a.exists("assistant") || len(fl.started) != 1 {
		t.Fatal("its own account is adopted")
	}
	b, k2, fl2, _ := assistantApp(t, "")
	k2.held = map[string]bool{"assistant@testnet": true}
	b.WithAssistant("not-it")
	b.ensureAssistant(context.Background())
	if b.exists("assistant") || len(fl2.started) != 0 {
		t.Fatal("another's account is not adopted")
	}
}

// The app generates no password and writes none: the one it may hold is an
// earlier release's, which Set password replaces and forgets.
func TestNoPasswordIsGenerated(t *testing.T) {
	src, err := os.ReadFile("agents.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, banned := range []string{"newPassword", "AssistantPassword =", "AssistantPassword:"} {
		if strings.Contains(string(src), banned) {
			t.Errorf("agents.go makes or keeps a password: %q", banned)
		}
	}
}

// Set password replaces a password an earlier release kept: it signs in with
// the kept one, changes it to the one typed twice, ends that session and
// forgets the kept one; refused when the two differ, and offered only where a
// password is kept.
func TestSetPasswordReplacesAKeptOne(t *testing.T) {
	a, k, _, saves := assistantApp(t, `{"assistant_password":"pw"}`)
	_ = a.create("assistant")
	var changed string
	k.onUpdate = func(body map[string]string) { changed = body["current_password"] + ">" + body["password"] }
	post := func(handle, body string) {
		req := postForm("/agents/"+handle+"/password", body)
		req.SetPathValue("handle", handle)
		a.postPassword(httptest.NewRecorder(), req)
	}
	cap := a.deps.RenderPage
	_ = cap
	post("assistant", "password=newpass1&confirm_password=other")
	if changed != "" || *saves != 0 {
		t.Fatal("two different passwords change nothing")
	}
	post("assistant", "password=newpass1&confirm_password=newpass1")
	if changed != "pw>newpass1" || *saves != 1 || a.heldPassword("assistant") != "" {
		t.Fatalf("changed %q, saves %d, held %q", changed, *saves, a.heldPassword("assistant"))
	}
	if !slices.Contains(k.paths, "POST /v1/auth/logout") {
		t.Error("the session it signed in with is ended")
	}
	_ = a.create("research")
	k.paths = nil
	post("research", "password=newpass1&confirm_password=newpass1")
	if len(k.paths) != 0 {
		t.Error("an agent with no kept password is offered no Set password")
	}
}

// What waits on an agent that its program cannot do is counted with its own
// session: requesters awaiting approval and waiting tasks addressed to it that
// take no input; a delivery it answers, another's task and an agent with no
// session count nothing. The menu's badge is their total, the operator's alone.
func TestDecisionsAreWhatTheProgramCannotDo(t *testing.T) {
	a, k, _, cap := kernelApp(t)
	_ = a.create("research")
	if a.self(context.Background(), "research") != (self{}) {
		t.Fatal("no session counts none")
	}
	_ = client.WriteCredential(a.credPath("research"), client.Tokens{AccessToken: "AT", RefreshToken: "RT"}, "P1")
	k.requesters = `[{"requester":{"address":"chat@beta"},"status":"pending"},{"requester":{"address":"chat@alpha"},"status":"approved"}]`
	k.tasks = `[{"id":"1","status":"waiting","required_caller":{"address":"research@testnet"},"allowed_input":{"type":"object","properties":{}}},
		{"id":"2","status":"waiting","required_caller":{"address":"research@testnet"},"allowed_input":{"type":"object","properties":{"answer":{"type":"string"}}}},
		{"id":"3","status":"waiting","required_caller":{"address":"other@testnet"},"allowed_input":{"type":"object","properties":{}}},
		{"id":"4","status":"waiting","required_caller":{"address":"research@testnet"},"allowed_input":{"type":"object"}}]`
	if got := a.self(context.Background(), "research"); got.Decisions != 2 || got.LastActive != "2026-11-17T10:00:00Z" {
		t.Fatalf("self = %+v, want a pending requester and an invitation, and the newest transaction's start", got)
	}
	h, _ := a.watchBadge(httptest.NewRequest(http.MethodGet, "/tick", nil), "badge", "")
	if !strings.Contains(string(h), ">2<") {
		t.Errorf("the menu's badge: %s", h)
	}
	if h, _ := a.watchBadge(httptest.NewRequest(http.MethodGet, "/tick", nil), "badge", "2"); h != "" {
		t.Error("an unchanged count is not swapped")
	}
	a.deps.Superuser = func(*http.Request) bool { return false }
	if h, _ := a.watchBadge(httptest.NewRequest(http.MethodGet, "/tick", nil), "badge", ""); h != "" {
		t.Error("the operator's alone")
	}
	_ = cap
}

// fundKernel is a kernel for funding: the agent's account at balance, the
// transfer's row and the model's at their prices, and each run recorded.
type fundKernel struct {
	mu       sync.Mutex
	balance  int64
	transfer int64 // sys/transfer's price; its quote is "QT<price>"
	model    int64 // the price of the model the config reasons with
	refs     []string
	runs     []client.RunRequest
	answer   string // what the next run answers instead, once: a status and a body
}

func (k *fundKernel) quote() string { return fmt.Sprint("QT", k.transfer) }

func fundApp(t *testing.T) (*Agents, *fundKernel, *capture, func(*http.Request, http.HandlerFunc)) {
	t.Helper()
	k := &fundKernel{balance: 0, transfer: 50, model: 1200}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/admin/users/{address}", func(w http.ResponseWriter, r *http.Request) {
		k.mu.Lock()
		defer k.mu.Unlock()
		fmt.Fprintf(w, `{"address":{"address":%q,"principal":"p"},"available":%d,"locked":0}`, r.PathValue("address"), k.balance)
	})
	mux.HandleFunc("GET /v1/actions", func(w http.ResponseWriter, r *http.Request) {
		k.mu.Lock()
		defer k.mu.Unlock()
		ref := r.URL.Query().Get("ref")
		k.refs = append(k.refs, ref)
		switch ref {
		case client.Transfer:
			fmt.Fprintf(w, `[{"id":"T","name":"transfer","price":%d,"quote_hash":%q}]`, k.transfer, k.quote())
		case "sys@testnet/llm/chat":
			fmt.Fprintf(w, `[{"id":"M","name":"llm/chat","price":%d,"quote_hash":"QM"}]`, k.model)
		default:
			w.Write([]byte(`[]`))
		}
	})
	mux.HandleFunc("POST /v1/run", func(w http.ResponseWriter, r *http.Request) {
		var in client.RunRequest
		_ = json.NewDecoder(r.Body).Decode(&in)
		k.mu.Lock()
		defer k.mu.Unlock()
		if k.answer != "" {
			status, body, _ := strings.Cut(k.answer, " ")
			k.answer = ""
			code, _ := strconv.Atoi(status)
			w.WriteHeader(code)
			w.Write([]byte(body))
			return
		}
		if in.QuoteHash != k.quote() {
			w.WriteHeader(http.StatusConflict)
			fmt.Fprintf(w, `{"code":"terms_changed","error":"the terms changed","meta":{"price":"%d","quote_hash":%q}}`, k.transfer, k.quote())
			return
		}
		k.runs = append(k.runs, in)
		w.Write([]byte(`{"result":{"amount":1},"tx_id":"tx","charge":50}`))
	})
	kernel := httptest.NewServer(mux)
	t.Cleanup(kernel.Close)
	a, _, cap := testApp(t)
	if err := a.create("research"); err != nil {
		t.Fatal(err)
	}
	cl := client.New(kernel.URL, nil)
	sm := scs.New()
	a.deps.Client, a.deps.Sessions = cl, session.NewManager(sm, cl)
	a.deps.Amount = func(s string) (int64, error) { return strconv.ParseInt(s, 10, 64) }
	a.deps.Money = func(n int64) string { return fmt.Sprint(n, " u") }
	serve := func(r *http.Request, h http.HandlerFunc) {
		sm.LoadAndSave(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = a.deps.Sessions.Login(r.Context(), client.Tokens{AccessToken: "t", RefreshToken: "r"}, "sys@testnet", session.Pin{})
			r.SetPathValue("handle", "research")
			h(w, r)
		})).ServeHTTP(httptest.NewRecorder(), r)
	}
	return a, k, cap, serve
}

// Fund is Review, then the confirm: Review refuses an amount that is not one,
// and otherwise names the amount, the transfer's price read of its row, and
// carries its quote and a key; the confirm runs sys/transfer once, pinned to
// that quote and named by that key, the agent the target and nothing else run.
// Terms that moved since are reviewed again at the new ones, under a new key.
func TestFundIsReviewThenOnePinnedTransfer(t *testing.T) {
	a, k, cap, serve := fundApp(t)
	for _, bad := range []string{"", "0", "-5", "1.5x"} {
		serve(postForm("/agents/research/fund/review", "amount="+bad), a.postFundReview)
		if v := opened(cap); cap.status != http.StatusUnprocessableEntity || v.OpenForm != "fund" || v.Fund.Error == "" {
			t.Errorf("%q must be refused in the Fund dialog: %d %+v", bad, cap.status, v.Fund)
		}
	}
	serve(postForm("/agents/research/fund/review", "amount=100000"), a.postFundReview)
	v := opened(cap)
	if v.OpenForm != "fund-review" || v.Fund.Amount != 100000 || v.Fund.Price != 50 || v.Fund.Total() != 100050 || v.Fund.Quote != "QT50" || v.Fund.Key == "" {
		t.Fatalf("review: %+v", v)
	}
	if len(k.runs) != 0 {
		t.Fatalf("review runs nothing: %+v", k.runs)
	}
	key := v.Fund.Key
	form := "amount=100000&quote=QT50&key=" + key
	serve(postForm("/agents/research/fund", form), a.postFund)
	if len(k.runs) != 1 || k.runs[0].Action != client.Transfer || k.runs[0].ExternalKey != key || string(k.runs[0].Args) != `{"amount":100000,"target":"research@testnet"}` {
		t.Fatalf("one pinned, keyed transfer to the agent: %+v", k.runs)
	}
	if cap.flashKind != "success" || cap.flashTo != "/agents" || cap.flashMsg != "Funded 100000 u · research@testnet." {
		t.Errorf("funded: %q %q %q", cap.flashKind, cap.flashTo, cap.flashMsg)
	}
	k.mu.Lock()
	k.transfer = 60
	k.mu.Unlock()
	serve(postForm("/agents/research/fund", form), a.postFund)
	v = opened(cap)
	if len(k.runs) != 1 || v.OpenForm != "fund-review" || v.Fund.Price != 60 || v.Fund.Quote != "QT60" || v.Fund.Key == key || v.Fund.Key == "" {
		t.Errorf("moved terms are reviewed again under a new key: %+v %+v", v.Fund, k.runs)
	}
	serve(postForm("/agents/research/fund", "amount=100000"), a.postFund)
	if len(k.runs) != 1 || cap.flashTo != "/agents?form=fund&handle=research" {
		t.Errorf("a confirm without its review runs nothing: %+v %q", k.runs, cap.flashTo)
	}
}

// An agent says what stops it working, from its config and the live price of
// the model it reasons with: no funds below one model call, budget too low
// when its budget per job is below one; nothing with a config naming no model
// and a balance; the list reads each model's price once.
func TestAnAgentSaysWhatStopsItWorking(t *testing.T) {
	a, k, cap, serve := fundApp(t)
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(a.writeFile("research", configFile, []byte(`{"reason":"sys@testnet/llm/chat","default_budget":100}`)))
	detail := func() readiness {
		serve(httptest.NewRequest(http.MethodGet, "/agents/research", nil), a.getDetail)
		v, _ := cap.data.(detailView)
		return v.Ready
	}
	for _, c := range []struct {
		balance, budget int64
		want            string
	}{{0, 50000, "no funds"}, {1199, 50000, "no funds"}, {1200, 100, "budget too low"}, {1200, 1199, "budget too low"}, {1200, 1200, ""}, {100000, 50000, ""}} {
		k.mu.Lock()
		k.balance = c.balance
		k.mu.Unlock()
		must(a.writeFile("research", configFile, []byte(fmt.Sprintf(`{"reason":"sys@testnet/llm/chat","default_budget":%d}`, c.budget))))
		if rd := detail(); rd.Short != c.want || rd.Call != 1200 || rd.Budget != c.budget {
			t.Errorf("balance %d budget %d: %+v, want %q", c.balance, c.budget, rd, c.want)
		}
	}
	must(a.writeFile("research", configFile, []byte(`{}`)))
	if rd := detail(); rd.Short != "" || rd.Call != 0 {
		t.Errorf("no model named, a balance: %+v", rd)
	}
	k.mu.Lock()
	k.balance = 0
	k.mu.Unlock()
	if rd := detail(); rd.Short != "no funds" {
		t.Errorf("no model named, nothing held: %+v", rd)
	}
	must(a.create("second"))
	for _, h := range []string{"research", "second"} {
		must(a.writeFile(h, configFile, []byte(`{"reason":"sys@testnet/llm/chat","default_budget":50000}`)))
	}
	k.mu.Lock()
	k.refs = nil
	k.mu.Unlock()
	serve(httptest.NewRequest(http.MethodGet, "/agents", nil), a.getIndex)
	lv, _ := cap.data.(listView)
	if len(lv.Agents) != 2 || lv.Agents[0].Short != "no funds" || lv.Agents[1].Short != "no funds" || len(k.refs) != 1 {
		t.Errorf("the list: %+v, %d price reads", lv.Agents, len(k.refs))
	}
}

// A transfer whose outcome is not known — the kernel parked it, or did not
// answer — returns to the confirm with the same amount, quote and key, so
// pressing Fund again asks under that key and moves money once; a definite
// refusal reopens the amount, and its next Review mints a new key.
func TestAnUnknownFundingKeepsItsKey(t *testing.T) {
	a, k, cap, serve := fundApp(t)
	form := "amount=100000&quote=QT50&price=50&key=K1"
	for _, answer := range []string{
		`504 {"code":"timeout","error":"the call has not settled"}`,
		`409 {"code":"invalid_state","error":"a run under this key has not settled"}`,
	} {
		k.mu.Lock()
		k.answer = answer
		k.mu.Unlock()
		serve(postForm("/agents/research/fund", form), a.postFund)
		v := opened(cap)
		if v.OpenForm != "fund-review" || v.Fund.Key != "K1" || v.Fund.Quote != "QT50" || v.Fund.Amount != 100000 || v.Fund.Price != 50 || v.Fund.Error == "" {
			t.Errorf("%s: the confirm again, same key: %+v", answer, v.Fund)
		}
	}
	serve(postForm("/agents/research/fund", form), a.postFund)
	if len(k.runs) != 1 || k.runs[0].ExternalKey != "K1" || cap.flashKind != "success" {
		t.Errorf("Fund again goes under the same key: %+v %q", k.runs, cap.flashKind)
	}
	k.mu.Lock()
	k.answer = `402 {"code":"insufficient_funds","error":"your balance is 0.00 fUSD"}`
	k.mu.Unlock()
	serve(postForm("/agents/research/fund", "amount=100000&quote=QT50&price=50&key=K2"), a.postFund)
	if v := opened(cap); v.OpenForm != "fund" || v.Fund.Key != "" || v.Fund.Error == "" {
		t.Errorf("a refusal reopens the amount, the key gone: %+v", v.Fund)
	}
}

// opened is the agent whose dialog a command given in the fleet's list left
// open over the list.
func opened(cap *capture) card {
	lv, _ := cap.data.(listView)
	for _, c := range lv.Agents {
		if c.OpenForm != "" {
			return c
		}
	}
	return card{}
}

// fakeKernel answers the five routes a sign-in and an account creation use. The
// password it accepts is "pw"; it records the handles created and every path
// asked, and answers /v1/me with the address it was told to. A handle starting
// with "taken", or listed in held, already exists.
type fakeKernel struct {
	srv     *httptest.Server
	mu      sync.Mutex
	created []string
	paths   []string
	held    map[string]bool
	me      string
	health  string
	// What the agent's own session reads, and what a password change sends.
	requesters, tasks string
	onUpdate          func(map[string]string)
}

func newKernel(t *testing.T) *fakeKernel {
	t.Helper()
	k := &fakeKernel{me: "research@testnet",
		health: `{"status":"ok","handle":"testnet","blockchain_address":"KEY","network":"play","network_fingerprint":"FP","decimals":6,"symbol":"fUSD"}`}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(k.health)) })
	mux.HandleFunc("POST /v1/users", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		k.mu.Lock()
		dup := strings.HasPrefix(body["handle"], "taken") || k.held[body["handle"]]
		k.created = append(k.created, body["handle"])
		k.mu.Unlock()
		if dup {
			http.Error(w, `{"error":"account: already exists"}`, http.StatusUnprocessableEntity)
			return
		}
		w.Write([]byte(`{"id":"U1","address":{"address":"` + body["handle"] + `"}}`))
	})
	mux.HandleFunc("POST /v1/auth/authorize", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["password"] != "pw" {
			http.Error(w, `{"error":"invalid credentials"}`, http.StatusUnauthorized)
			return
		}
		w.Write([]byte(`{"redirect":"?code=C"}`))
	})
	mux.HandleFunc("POST /v1/auth/token", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"access_token":"AT","refresh_token":"RT"}`))
	})
	mux.HandleFunc("GET /v1/me", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"id":"P1","address":{"address":"` + k.me + `"}}`))
	})
	mux.HandleFunc("PUT /v1/me", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if k.onUpdate != nil {
			k.onUpdate(body)
		}
		w.Write([]byte(`{"id":"P1","address":{"address":"` + k.me + `"}}`))
	})
	mux.HandleFunc("POST /v1/auth/logout", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("GET /v1/me/requesters", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(or(k.requesters, "[]"))) })
	mux.HandleFunc("GET /v1/transactions", func(w http.ResponseWriter, r *http.Request) {
		if k.requesters == "" { // an agent with nothing set up has done nothing
			w.Write([]byte(`[]`))
			return
		}
		w.Write([]byte(`[{"id":"T2","started_at":"2026-11-17T10:00:00Z"},{"id":"T1","started_at":"2026-11-16T10:00:00Z"}]`))
	})
	mux.HandleFunc("GET /v1/tasks", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("offset") != "" && r.URL.Query().Get("offset") != "0" {
			w.Write([]byte(`[]`))
			return
		}
		w.Write([]byte(or(k.tasks, "[]")))
	})
	k.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		k.mu.Lock()
		k.paths = append(k.paths, r.Method+" "+r.URL.Path)
		k.mu.Unlock()
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(k.srv.Close)
	return k
}

// kernelApp is testApp with a fake kernel and a client directory.
func kernelApp(t *testing.T) (*Agents, *fakeKernel, *fakePrograms, *capture) {
	t.Helper()
	a, fl, cap := testApp(t)
	k := newKernel(t)
	a.client = filepath.Join(t.TempDir(), "client")
	a.deps.Client = client.New(k.srv.URL, nil)
	return a, k, fl, cap
}

func readJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func kernelRecord(t *testing.T, a *Agents) map[string]any {
	t.Helper()
	return readJSON(t, filepath.Join(a.client, "config.json"))["kernels"].(map[string]any)["testnet"].(map[string]any)
}

func or(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// An agent is a row of the fleet's record list: its handle linking to its
// page and its one pill, its balance and when it last acted, each labelled for
// the stacked layout, and its commands in one order (Start or Stop, an icon;
// Fund; Sign in or Switch with the count of decisions), with one set of
// dialogs per agent, their ids its own. Nothing else: no tile, no facts line.
// Its page states its name, its pill and the facts the row leaves out, then
// Settings, and holds none of the row's commands.
func TestAnAgentIsARowAndItsPageItsFacts(t *testing.T) {
	r, err := server.NewRenderer(nil, New().Templates())
	if err != nil {
		t.Fatal(err)
	}
	render := func(page string, p server.Page) string {
		t.Helper()
		rec := httptest.NewRecorder()
		if err := r.Render(rec, http.StatusOK, page, p); err != nil {
			t.Fatalf("%s: %v", page, err)
		}
		return rec.Body.String()
	}
	acct := &client.AdminUser{Address: client.Party{Address: "helper@alpha", Principal: "helper@alpha"}, Available: 4990000, Locked: 20000}
	form := server.BuildForm(json.RawMessage(`{"type":"object","properties":{"max_turns":{"type":"integer"}}}`))
	ready := readiness{Reason: client.Party{Address: "sys@alpha/llm/chat"}, Call: 1200, Budget: 50000}
	running := card{status: status{Handle: "helper", Running: true, State: "running", Short: "no funds", CSRF: "<input name=csrf>", Ready: ready},
		Account: acct, Self: self{Decisions: 2, LastActive: "2026-11-17T10:00:00Z"}}
	stopped := card{status: status{Handle: "scout", State: "stopped", LastLine: "error: x", CSRF: "<input name=csrf>"}, Account: &client.AdminUser{Address: client.Party{Address: "scout@alpha"}}}
	list := render("agents", server.Page{Me: "sys@alpha", Data: listView{Agents: []card{running, stopped}}})
	for _, want := range []string{`<table class="records">`, `<th>Agent</th><th class="money">Balance</th><th>Active</th><th></th>`,
		`<td><a class="ref mono" href="/agents/helper">helper</a> <span id="agent-state-helper"><input type="hidden" name="watch" value="agent:helper:running"><span class="pill status-danger">no funds</span></span></td>`,
		`<td class="money" data-label="Balance">`, `<td data-label="Active">`, `<div class="cluster" id="agent-commands-helper">`,
		`class="btn-icon btn-danger" type="submit" aria-label="Stop helper"`, `action="/agents/helper/stop"`,
		`<a class="btn" href="/agents?form=fund&handle=helper" data-modal="form-fund-helper">Fund</a>`,
		`<a class="btn" href="?form=signin&handle=helper" data-modal="signin" data-fill="helper">Sign in<span class="badge" aria-label="Waiting">2</span></a>`,
		`id="form-start-helper"`, `id="form-fund-helper"`,
		`<a class="btn-icon" href="/agents?form=start&handle=scout" data-modal="form-start-scout" aria-label="Start scout"`, `<td data-label="Active">never</td>`, "New agent"} {
		if !strings.Contains(list, want) {
			t.Errorf("the list lacks %q", want)
		}
	}
	for _, not := range []string{"tile", "per job", "error: x", `name="back"`, "form-switch", "agent-card"} {
		if strings.Contains(list, not) {
			t.Errorf("the list has %q", not)
		}
	}
	if strings.Count(list, `id="form-start-helper"`) != 1 || strings.Count(list, `<tr>`) != 3 {
		t.Error("one row and one set of dialogs per agent")
	}
	page := render("agent", server.Page{Me: "sys@alpha", Accounts: []string{"sys@alpha"}, CSRFField: "<input name=csrf>",
		Data: detailView{card: running, Held: true, Texts: []textView{{texts[0], "\nYou are concise.\nMore."}, {texts[1], ""}}, Config: form}})
	for _, want := range []string{`<h1><span class="ref mono">helper</span> <span class="pill status-danger">no funds</span></h1>`,
		`<dt>Balance</dt>`, `<dt>Parked</dt>`, `<dt>Model</dt>`, " a call", `<dt>Budget</dt>`, " per job", `<dt>Active</dt>`,
		`<h2>Settings</h2>`, `<span>instructions</span><span class="detail">You are concise.</span>`,
		`<span>heartbeat</span><span class="detail">empty</span>`, `href="/agents/helper?form=heartbeat.md" data-modal="form-heartbeat.md"`,
		`<dialog id="form-agent.md"`, `aria-label="Edit instructions"`, `action="/agents/helper/files/agent.md"`, ">\nYou are concise.\nMore.</textarea>",
		`<dialog id="form-heartbeat.md"`, `aria-label="Edit heartbeat"`, `action="/agents/helper/files/heartbeat.md"`, `<label for="text-heartbeat.md">Heartbeat</label>`,
		`data-modal="form-config"`, `<dialog id="form-config"`, `<span>password</span>`} {
		if !strings.Contains(page, want) {
			t.Errorf("the page lacks %q", want)
		}
	}
	for _, not := range []string{"form-start-helper", "form-fund-helper", `action="/agents/helper/stop"`, `data-fill="helper"`, "Delete agent", `name="watch" value="agent:`} {
		if strings.Contains(page, not) {
			t.Errorf("the page has %q", not)
		}
	}
	held := running
	held.Accounts = []string{"sys@alpha", "helper@alpha"}
	list = render("agents", server.Page{Me: "sys@alpha", CSRFField: "<input name=csrf>", Data: listView{Agents: []card{held}}})
	if !strings.Contains(list, `action="/switch"`) || strings.Contains(list, `data-fill="helper"`) {
		t.Error("an account the session holds is one click away, and asks no password")
	}
	stoppedPage := render("agent", server.Page{Me: "sys@alpha", CSRFField: "<input name=csrf>", Data: detailView{card: stopped, Config: form}})
	if !strings.Contains(stoppedPage, "<dt>Exit</dt>") || !strings.Contains(stoppedPage, "error: x") || !strings.Contains(stoppedPage, `data-confirm="Delete scout, leaving `) {
		t.Error("a stopped agent's page states its exit line and offers delete, naming the balance left")
	}
	bare := render("agents", server.Page{Me: "sys@alpha", Data: listView{Agents: []card{{status: status{Handle: "x", State: "stopped"}}}}})
	if strings.Contains(bare, `data-fill="x"`) || strings.Contains(bare, "form-fund-x") {
		t.Error("a home with no account offers no sign-in and no fund")
	}
}

// A command is given in the fleet's list and answers there: a refusal
// re-renders the list with that agent's dialog open and its reason, a success
// returns to it; without script ?form=&handle= opens that agent's dialog.
func TestACommandAnswersInTheList(t *testing.T) {
	a, _, fl, cap := kernelApp(t)
	_ = a.create("research")
	post := func(path, body string) *httptest.ResponseRecorder {
		req := postForm(path, body)
		req.SetPathValue("handle", "research")
		rec := httptest.NewRecorder()
		switch {
		case strings.HasSuffix(path, "/start"):
			a.postStart(rec, req)
		case strings.HasSuffix(path, "/stop"):
			a.postStop(rec, req)
		}
		return rec
	}
	post("/agents/research/start", "password=nope")
	if c := opened(cap); cap.page != "agents" || cap.status != http.StatusUnprocessableEntity || c.Handle != "research" ||
		c.OpenForm != "start" || c.Error != "Password is incorrect." {
		t.Fatalf("a refusal in the list: %s %d %+v", cap.page, cap.status, cap.data)
	}
	if rec := post("/agents/research/start", "password=pw"); rec.Header().Get("Location") != "/agents" || len(fl.started) != 1 {
		t.Fatalf("a success returns to the list: %q", rec.Header().Get("Location"))
	}
	if rec := post("/agents/research/stop", ""); rec.Header().Get("Location") != "/agents" {
		t.Fatalf("Stop returns to the list: %q", rec.Header().Get("Location"))
	}
	a.getIndex(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/agents?form=fund&handle=research", nil))
	if c := opened(cap); c.Handle != "research" || c.OpenForm != "fund" {
		t.Fatalf("without script the link opens that agent's dialog: %+v", cap.data)
	}
}
