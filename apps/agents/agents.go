// Package agents is the juice-ui app for managing juice-agents — the sibling
// juice-family daemon (repo juice-agent) that logs into the kernel as an ordinary
// user and solves tasks with the action catalog. One binary hosts many named
// agents, each isolated in its own home under $JUICE_HOME/agent/agents/<handle>/
// (persona.md, knowledge.md, memory.md, config.json, data/).
//
// Unlike apps/chat, this app has no dedicated kernel account: it is a local
// filesystem editor and child-process supervisor, not a kernel-HTTP feature. It
//
//   - manages the agent homes (create scaffolds blank persona/knowledge/memory;
//     the daemon dumps a standard config.json on first serve — the UI never owns
//     that schema);
//   - edits persona.md/knowledge.md/config.json in place;
//   - browses the learned memory.md (read-only — the daemon owns it);
//   - starts/stops the daemon as a supervised child process. The daemon has no
//     stop command or status endpoint (agent requirements.md §11), so start spawns
//     `juice-agent @<handle> serve --password-stdin` and stop sends SIGTERM;
//     "running" is whether the tracked child is alive.
//
// The agent's kernel password is required to start it. It lives only in the start
// request body, is piped to the child's stdin, and is then discarded — never
// persisted, never logged, never rendered. A UI restart therefore stops every
// agent and needs the password re-entered to restart it.
package agents

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"html/template"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/daios-ai/juice-ui/apps"
	"github.com/daios-ai/juice-ui/client"
)

//go:embed templates/*.html
var tmplFS embed.FS

// binEnv overrides the juice-agent binary path (otherwise resolved on PATH).
const binEnv = "JUICE_AGENT_BIN"

// The fixed per-agent files (agent requirements.md §11). memory.md is learned by
// the daemon and shown read-only; config.json is dumped by the daemon on first
// serve and edited here only when present.
const (
	personaFile   = "persona.md"
	knowledgeFile = "knowledge.md"
	memoryFile    = "memory.md"
	configFile    = "config.json"
)

// agentsIcon is the app's own nav icon (a small robot), inline so no core sprite
// edit is needed (mirrors apps/chat).
const agentsIcon = template.HTML(`<svg class="ico" aria-hidden="true" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="4" y="8" width="16" height="12" rx="2"/><path d="M12 8V4"/><circle cx="12" cy="3" r="1"/><path d="M9 13h.01M15 13h.01"/><path d="M2 14v2M22 14v2"/></svg>`)

// A handle names the home directory, so it is validated strictly: lowercase
// letters, digits, '-' and '_', starting alphanumeric. This forbids '/', '.', and
// '..', making path traversal impossible before the containment check in agentDir.
var handleRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// scaffold is written blank on create; editable are the files the UI may write.
var (
	scaffold = []string{personaFile, knowledgeFile, memoryFile}
	editable = map[string]bool{personaFile: true, knowledgeFile: true, configFile: true}
)

var (
	errBadHandle = errors.New("invalid agent handle")
	errBadConfig = errors.New("config.json must be valid JSON")
	errExists    = errors.New("agent already exists")
	errNotFound  = errors.New("no such agent")
)

// Agents is the app. Its only mutable runtime state is the supervisor's process
// table; nothing is persisted (no app-state, no store).
type Agents struct {
	deps apps.Deps
	root string      // $JUICE_HOME/agent/agents; overridable in tests
	sup  *supervisor // process supervision; overridable in tests (fake launcher)
}

// New constructs the app. Root and supervisor resolve on Start.
func New() *Agents { return &Agents{} }

func (a *Agents) Slug() string { return "agents" }

// Nav contributes the "agents" sidebar entry (no badge — this app has no async
// attention signal). Self-gated on a signed-in user, like apps/chat.
func (a *Agents) Nav(handle string, superuser bool) []apps.NavEntry {
	if handle == "" {
		return nil
	}
	return []apps.NavEntry{{Slug: "agents", Label: "agents", Href: "/agents", Icon: agentsIcon, Match: "/agents"}}
}

func (a *Agents) Templates() fs.FS { return tmplFS }

func (a *Agents) Mount(mux *http.ServeMux, d apps.Deps) {
	a.deps = d
	mux.HandleFunc("GET /agents", d.RequireAuth(a.getIndex))
	mux.HandleFunc("POST /agents", d.RequireAuth(a.postCreate))
	mux.HandleFunc("GET /agents/{handle}", d.RequireAuth(a.getDetail))
	mux.HandleFunc("GET /agents/{handle}/status", d.RequireAuth(a.getStatus))
	mux.HandleFunc("POST /agents/{handle}/files/{file}", d.RequireAuth(a.postSaveFile))
	mux.HandleFunc("POST /agents/{handle}/start", d.RequireAuth(a.postStart))
	mux.HandleFunc("POST /agents/{handle}/stop", d.RequireAuth(a.postStop))
	mux.HandleFunc("POST /agents/{handle}/delete", d.RequireAuth(a.postDelete))
}

// Start resolves the agent root (sibling of the UI home) and the supervisor. Both
// are left untouched when already set (tests inject them).
func (a *Agents) Start(ctx context.Context) error {
	if a.root == "" {
		a.root = defaultRoot(a.deps.Home)
	}
	if a.sup == nil {
		a.sup = newSupervisor(osLauncher{bin: agentBin(), home: juiceRoot(a.root)})
	}
	return nil
}

// Stop signals every running agent on graceful shutdown.
func (a *Agents) Stop(ctx context.Context) error {
	if a.sup != nil {
		a.sup.stopAll()
	}
	return nil
}

// ---- store (filesystem, traversal-guarded) ----

// agentDir resolves handle's home under root, rejecting anything that is not a
// direct, validly-named child of root.
func (a *Agents) agentDir(handle string) (string, error) {
	if !validHandle(handle) {
		return "", errBadHandle
	}
	dir := filepath.Join(a.root, handle)
	if filepath.Dir(dir) != filepath.Clean(a.root) {
		return "", errBadHandle
	}
	return dir, nil
}

func validHandle(h string) bool { return len(h) <= 64 && handleRe.MatchString(h) }

// list returns the agents under root, newest state of "running" per the supervisor.
func (a *Agents) list() ([]agentRow, error) {
	entries, err := os.ReadDir(a.root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []agentRow
	for _, e := range entries {
		if !e.IsDir() || !validHandle(e.Name()) {
			continue
		}
		out = append(out, agentRow{Handle: e.Name(), Running: a.sup.running(e.Name())})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Handle < out[j].Handle })
	return out, nil
}

// create scaffolds a new home with blank persona/knowledge/memory, never
// truncating a pre-existing file. config.json is left to the daemon's first-serve
// dump.
func (a *Agents) create(handle string) error {
	dir, err := a.agentDir(handle)
	if err != nil {
		return err
	}
	if _, err := os.Stat(dir); err == nil {
		return errExists
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	for _, f := range scaffold {
		fh, err := os.OpenFile(filepath.Join(dir, f), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			if os.IsExist(err) {
				continue
			}
			return err
		}
		_ = fh.Close()
	}
	return nil
}

// readFile returns the file's content, or "" when absent (an unprovisioned file
// is empty, not an error).
func (a *Agents) readFile(handle, file string) (string, error) {
	dir, err := a.agentDir(handle)
	if err != nil {
		return "", err
	}
	b, err := os.ReadFile(filepath.Join(dir, file))
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	return string(b), nil
}

// writeFile writes one editable file (memory.md is not editable). config.json must
// parse as JSON — the UI rejects malformed config exactly as the daemon does,
// never inventing config of its own.
func (a *Agents) writeFile(handle, file, content string) error {
	if !editable[file] {
		return errBadHandle
	}
	dir, err := a.agentDir(handle)
	if err != nil {
		return err
	}
	if _, err := os.Stat(dir); err != nil {
		return errNotFound
	}
	if file == configFile && !json.Valid([]byte(content)) {
		return errBadConfig
	}
	return os.WriteFile(filepath.Join(dir, file), []byte(content), 0o600)
}

func (a *Agents) remove(handle string) error {
	dir, err := a.agentDir(handle)
	if err != nil {
		return err
	}
	return os.RemoveAll(dir)
}

func (a *Agents) exists(handle string) bool {
	dir, err := a.agentDir(handle)
	if err != nil {
		return false
	}
	_, err = os.Stat(dir)
	return err == nil
}

// ---- view models ----

type agentRow struct {
	Handle  string
	Running bool
	CSRF    template.HTML // the row's status cell carries a Stop form (rendered outside the Page envelope)
}

// startForm is the shared "Start" modal model (list rows and the detail page render
// the same _agent_start partial, so the control is identical everywhere).
type startForm struct {
	Handle    string
	CSRF      template.HTML
	Open      bool   // server-render [open] for the ?form=start no-JS path
	CloseHref string // where the no-JS close link points
}

type listView struct {
	Agents   []agentRow
	Starts   []startForm // one Start modal per agent, keyed by handle
	OpenForm bool
}

type detailView struct {
	Handle    string
	Running   bool
	Persona   string
	Knowledge string
	Memory    string
	Config    string
	HasConfig bool
	OpenForm  string        // the ?form= value; selects which dialog renders [open]
	CSRF      template.HTML // for the status fragment rendered inline (no Page envelope)
	Start     startForm     // the shared Start modal
}

type statusView struct {
	Handle  string
	Running bool
	CSRF    template.HTML // the status fragment carries a Stop form (polled outside the Page envelope)
}

// ---- handlers ----

func (a *Agents) getIndex(w http.ResponseWriter, r *http.Request) {
	rows, err := a.list()
	if a.deps.HandleErr(w, r, err) {
		return
	}
	// Each row's status cell renders the same _agent_status fragment (Stop form +
	// Start trigger) as the detail page, so it needs a CSRF field and a Start modal.
	csrf := a.deps.CSRFField(r)
	starts := make([]startForm, len(rows))
	for i := range rows {
		rows[i].CSRF = csrf
		starts[i] = startForm{Handle: rows[i].Handle, CSRF: csrf, CloseHref: "/agents"}
	}
	a.deps.RenderPage(w, r, http.StatusOK, "agents", "agents", listView{
		Agents:   rows,
		Starts:   starts,
		OpenForm: r.URL.Query().Get("form") == "agent-create",
	})
}

func (a *Agents) postCreate(w http.ResponseWriter, r *http.Request) {
	handle := strings.TrimPrefix(strings.TrimSpace(r.FormValue("handle")), "@")
	if !validHandle(handle) {
		a.deps.FlashRedirect(w, r, "/agents", "error", "A handle is lowercase letters, digits, - or _.")
		return
	}
	if err := a.create(handle); err != nil {
		if errors.Is(err, errExists) {
			a.deps.FlashRedirect(w, r, "/agents", "error", "That handle already exists.")
			return
		}
		a.deps.HandleErr(w, r, err)
		return
	}
	a.deps.FlashRedirect(w, r, "/agents/"+handle, "success", "Agent created.")
}

func (a *Agents) getDetail(w http.ResponseWriter, r *http.Request) {
	handle := r.PathValue("handle")
	if !a.exists(handle) {
		a.notFound(w, r)
		return
	}
	persona, _ := a.readFile(handle, personaFile)
	knowledge, _ := a.readFile(handle, knowledgeFile)
	memory, _ := a.readFile(handle, memoryFile)
	config, _ := a.readFile(handle, configFile)
	dir, _ := a.agentDir(handle)
	_, statErr := os.Stat(filepath.Join(dir, configFile))
	csrf := a.deps.CSRFField(r)
	open := r.URL.Query().Get("form")
	a.deps.RenderPage(w, r, http.StatusOK, "agent", "agents", detailView{
		Handle:    handle,
		Running:   a.sup.running(handle),
		Persona:   persona,
		Knowledge: knowledge,
		Memory:    memory,
		Config:    config,
		HasConfig: statErr == nil,
		OpenForm:  open,
		CSRF:      csrf,
		Start:     startForm{Handle: handle, CSRF: csrf, Open: open == "start", CloseHref: "/agents/" + handle},
	})
}

func (a *Agents) postSaveFile(w http.ResponseWriter, r *http.Request) {
	handle := r.PathValue("handle")
	file := r.PathValue("file")
	if !editable[file] || !a.exists(handle) {
		a.notFound(w, r)
		return
	}
	content := strings.ReplaceAll(r.FormValue("content"), "\r\n", "\n")
	if err := a.writeFile(handle, file, content); err != nil {
		if errors.Is(err, errBadConfig) {
			a.deps.FlashRedirect(w, r, "/agents/"+handle, "error", "config.json must be valid JSON — not saved.")
			return
		}
		a.deps.HandleErr(w, r, err)
		return
	}
	a.deps.FlashRedirect(w, r, "/agents/"+handle, "success", "Saved.")
}

func (a *Agents) postDelete(w http.ResponseWriter, r *http.Request) {
	handle := r.PathValue("handle")
	if !a.exists(handle) {
		a.notFound(w, r)
		return
	}
	_ = a.sup.stop(handle)
	if err := a.remove(handle); err != nil {
		a.deps.HandleErr(w, r, err)
		return
	}
	a.deps.FlashRedirect(w, r, "/agents", "success", "Agent deleted.")
}

// postStart spawns the daemon. The password is read from the body, handed to the
// supervisor (which pipes it to the child's stdin), and then out of scope — it is
// never persisted, logged, or rendered.
func (a *Agents) postStart(w http.ResponseWriter, r *http.Request) {
	handle := r.PathValue("handle")
	if !a.exists(handle) {
		a.notFound(w, r)
		return
	}
	back := returnPath(r, "/agents/"+handle)
	password := r.FormValue("password")
	if password == "" {
		a.deps.FlashRedirect(w, r, back, "error", "The agent's kernel password is required to start it.")
		return
	}
	if err := a.sup.start(handle, password); err != nil {
		a.deps.FlashRedirect(w, r, back, "error", "Could not start: "+err.Error())
		return
	}
	a.deps.FlashRedirect(w, r, back, "success", "Starting…")
}

func (a *Agents) postStop(w http.ResponseWriter, r *http.Request) {
	handle := r.PathValue("handle")
	if !validHandle(handle) {
		a.notFound(w, r)
		return
	}
	_ = a.sup.stop(handle)
	a.deps.FlashRedirect(w, r, returnPath(r, "/agents/"+handle), "success", "Stopping…")
}

// getStatus serves the htmx status fragment the detail page polls.
func (a *Agents) getStatus(w http.ResponseWriter, r *http.Request) {
	handle := r.PathValue("handle")
	if !validHandle(handle) {
		http.NotFound(w, r)
		return
	}
	_ = a.deps.RenderFragment(w, http.StatusOK, "_agent_status", statusView{
		Handle:  handle,
		Running: a.sup.running(handle),
		CSRF:    a.deps.CSRFField(r),
	})
}

func (a *Agents) notFound(w http.ResponseWriter, r *http.Request) {
	a.deps.HandleErr(w, r, client.ErrNotFound)
}

// returnPath resolves where to redirect after a lifecycle action so it lands back
// on the page it was triggered from (the list or the agent's detail). Only a local
// /agents path from the Referer is honored; anything else falls back — never an
// open redirect (the host is discarded).
func returnPath(r *http.Request, fallback string) string {
	ref := r.Header.Get("Referer")
	if ref == "" {
		return fallback
	}
	u, err := url.Parse(ref)
	if err != nil {
		return fallback
	}
	if u.Path == "/agents" || strings.HasPrefix(u.Path, "/agents/") {
		return u.Path
	}
	return fallback
}

// ---- root/binary resolution ----

// defaultRoot resolves $JUICE_HOME/agent/agents from the UI home ($JUICE_HOME/ui),
// falling back to JUICE_HOME / ~/.juice when home is empty.
func defaultRoot(home string) string {
	if home != "" {
		return filepath.Join(filepath.Dir(home), "agent", "agents")
	}
	base := os.Getenv("JUICE_HOME")
	if base == "" {
		if h, err := os.UserHomeDir(); err == nil {
			base = filepath.Join(h, ".juice")
		}
	}
	return filepath.Join(base, "agent", "agents")
}

// juiceRoot is the JUICE_HOME handed to a spawned daemon: the grandparent of the
// agents root ($JUICE_HOME/agent/agents -> $JUICE_HOME).
func juiceRoot(root string) string { return filepath.Dir(filepath.Dir(root)) }

func agentBin() string {
	if p := os.Getenv(binEnv); p != "" {
		return p
	}
	if p, err := exec.LookPath("juice-agent"); err == nil {
		return p
	}
	return ""
}
