// Package agents is the juice-ui app for managing juice-agents — the sibling
// juice-family daemon (repo juice-agent) that acts as one kernel login and
// answers the tasks addressed to it. Each agent lives in its own home,
// $JUICE_HOME/agents/<login>/, the login being handle@kernel (agent
// requirements.md §11, ecosystem-standard §2).
//
// Unlike apps/chat, this app has no dedicated kernel account: it is a local
// filesystem editor and child-process supervisor, not a kernel-HTTP feature. It
//
//   - creates a home with a blank agent.md;
//   - edits agent.md and the optional config.json, which the daemon never
//     writes (a blank config.json is no file: the daemon's defaults);
//   - starts `juice-agent serve <login>` detached, and stops the daemon
//     serving a login with SIGTERM, whoever started it.
//
// "Running" is the daemon's own word: it holds `serve.lock` in its home for as
// long as it serves (agent requirements §11, as a kernel holds its home,
// ecosystem-standard §3), so an agent served from a terminal is seen, stopped
// and protected like one the UI started. The UI keeps only the children it
// spawned, for the moment before a child takes the lock. A daemon outlives the
// UI, as the kernel does.
//
// The daemon holds its own session in the installation's client records. The
// UI makes that session as `juice auth login <login>` would — from a password
// typed at New agent, or at Start when the login has none, and kept nowhere —
// and never reads one (requirements.md §5, ecosystem-standard §4). A daemon
// that exits states why in its log, and the page shows that line. The session,
// the home and the credential are written or removed only while no daemon of
// that login is alive.
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
	"sync"

	"github.com/daios-ai/juice-ui/apps"
	"github.com/daios-ai/juice-ui/client"
)

//go:embed templates/*.html
var tmplFS embed.FS

// binEnv overrides the juice-agent binary path (otherwise resolved from
// $JUICE_HOME/bin, then PATH).
const binEnv = "JUICE_AGENT_BIN"

// The files of an agent home the UI edits (agent requirements.md §11).
const (
	agentFile  = "agent.md"
	configFile = "config.json"
)

// agentsIcon is the app's own nav icon (a small robot), inline so no core sprite
// edit is needed (mirrors apps/chat).
const agentsIcon = template.HTML(`<svg class="ico" aria-hidden="true" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="4" y="8" width="16" height="12" rx="2"/><path d="M12 8V4"/><circle cx="12" cy="3" r="1"/><path d="M9 13h.01M15 13h.01"/><path d="M2 14v2M22 14v2"/></svg>`)

// A handle names the home directory, so it is validated strictly: lowercase
// letters, digits, '-' and '_', starting alphanumeric. This forbids '/', '.', and
// '..', making path traversal impossible before the containment check in agentDir.
var handleRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

var editable = map[string]bool{agentFile: true, configFile: true}

var (
	errBadHandle = errors.New("invalid agent handle")
	errBadConfig = errors.New("config.json must be valid JSON")
	errExists    = errors.New("agent already exists")
	errNotFound  = errors.New("no such agent")
)

// Agents is the app. Its only mutable runtime state is the supervisor's process
// table; nothing is persisted (no app-state, no store).
type Agents struct {
	deps   apps.Deps
	root   string      // $JUICE_HOME/agents; overridable in tests
	logs   string      // $JUICE_HOME/ui/agents, one <login>.log per daemon run
	client string      // $JUICE_HOME/client, the records an agent's session lives in
	sup    *supervisor // the children this UI spawned; overridable in tests (fake launcher)
	mu     sync.Mutex  // one lifecycle post at a time (create, start, stop, delete)
}

// New constructs the app. Root and supervisor resolve on Start.
func New() *Agents { return &Agents{} }

func (a *Agents) Slug() string { return "agents" }

// Nav contributes the "agents" sidebar entry (no badge — this app has no async
// attention signal), to the operator alone. Everything this app does happens on
// the UI host — it writes the installation's files and spawns a long-lived
// daemon — so it is an operator surface, and an entry a non-superuser could not
// use would be an invitation to a 403.
func (a *Agents) Nav(me string, superuser bool) []apps.NavEntry {
	if me == "" || !superuser {
		return nil
	}
	return []apps.NavEntry{{Slug: "agents", Label: "Agents", Href: "/agents", Icon: agentsIcon, Match: "/agents"}}
}

func (a *Agents) Templates() fs.FS { return tmplFS }

// Mount gates every route on the SUPERUSER, not merely on a session. The
// authority exercised here is the operator's machine: these handlers scaffold
// and delete directories under the installation root, write the files a daemon
// reads, and spawn that daemon as a child of this process. A kernel account
// confers none of that, and while any signed-in user could reach them, one
// account could edit and stop another's agent, and anyone could start a process
// on the host.
func (a *Agents) Mount(mux *http.ServeMux, d apps.Deps) {
	a.deps = d
	mux.HandleFunc("GET /agents", d.RequireSuperuser(a.getIndex))
	mux.HandleFunc("POST /agents", d.RequireSuperuser(a.postCreate))
	mux.HandleFunc("GET /agents/{handle}", d.RequireSuperuser(a.getDetail))
	d.Watch("agent", a.watch)
	mux.HandleFunc("POST /agents/{handle}/files/{file}", d.RequireSuperuser(a.postSaveFile))
	mux.HandleFunc("POST /agents/{handle}/start", d.RequireSuperuser(a.postStart))
	mux.HandleFunc("POST /agents/{handle}/stop", d.RequireSuperuser(a.postStop))
	mux.HandleFunc("POST /agents/{handle}/delete", d.RequireSuperuser(a.postDelete))
}

// Start resolves the agent root, the log directory and the supervisor. Each is
// left untouched when already set (tests inject them).
func (a *Agents) Start(ctx context.Context) error {
	if a.root == "" {
		a.root = defaultRoot(a.deps.Home)
	}
	home := filepath.Dir(a.root)
	if a.logs == "" {
		a.logs = filepath.Join(home, "ui", "agents")
	}
	if a.client == "" {
		a.client = filepath.Join(home, "client")
	}
	if a.sup == nil {
		a.sup = newSupervisor(osLauncher{bin: agentBin(home), home: home})
	}
	return nil
}

// Stop stops nothing: a daemon outlives the UI as the kernel does, detached in
// its own session, and the next UI finds it through its lock.
func (a *Agents) Stop(ctx context.Context) error { return nil }

// ---- store (filesystem, traversal-guarded) ----

// agentDir resolves handle's home under root, named by its login on this
// kernel, rejecting anything that is not a direct, validly-named child of root
// and any handle while the kernel's name is unknown.
func (a *Agents) agentDir(handle string) (string, error) {
	login := a.deps.Address(handle)
	if !validHandle(handle) || login == "" {
		return "", errBadHandle
	}
	dir := filepath.Join(a.root, login)
	if filepath.Dir(dir) != filepath.Clean(a.root) {
		return "", errBadHandle
	}
	return dir, nil
}

func validHandle(h string) bool { return len(h) <= 64 && handleRe.MatchString(h) }

// logPath is where handle's daemon writes its current run.
func (a *Agents) logPath(handle string) string {
	return filepath.Join(a.logs, a.deps.Address(handle)+".log")
}

// list returns this kernel's agents under root: the homes named by one of its
// logins, the page reading each account on this kernel.
func (a *Agents) list(r *http.Request) ([]status, error) {
	entries, err := os.ReadDir(a.root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	suffix := strings.TrimPrefix(a.deps.Address("x"), "x")
	var out []status
	for _, e := range entries {
		h, ok := strings.CutSuffix(e.Name(), suffix)
		if !e.IsDir() || suffix == "" || !ok || !validHandle(h) {
			continue
		}
		st, err := a.status(r, h)
		if err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Handle < out[j].Handle })
	return out, nil
}

// create makes a new home holding a blank agent.md, never truncating one that
// exists. config.json stays absent: the daemon's defaults.
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
	f, err := os.OpenFile(filepath.Join(dir, agentFile), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil && !os.IsExist(err) {
		return err
	}
	if f != nil {
		_ = f.Close()
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

// writeFile writes one editable file. A blank config.json removes the file,
// restoring the daemon's defaults; any other config.json must parse as JSON —
// the UI rejects malformed config as the daemon does, never inventing its own.
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
	path := filepath.Join(dir, file)
	if file == configFile && strings.TrimSpace(content) == "" {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	if file == configFile && !json.Valid([]byte(content)) {
		return errBadConfig
	}
	return os.WriteFile(path, []byte(content), 0o600)
}

// remove deletes handle's home, the log of its last run and its credential.
func (a *Agents) remove(handle string) error {
	dir, err := a.agentDir(handle)
	if err != nil {
		return err
	}
	for _, p := range []string{a.logPath(handle), a.credPath(handle)} {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return err
		}
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

// status is an agent's state as the shared _agent_status fragment renders it,
// in a list row, in the polled fragment and on the detail page alike.
type status struct {
	Handle   string
	Running  bool
	CSRF     template.HTML // the fragment carries its own Stop/Start form (rendered outside the Page envelope)
	LastLine string        // the daemon's exit error from its current run, when stopped

	// AskPassword: Start opens the password dialog, because the login has no
	// session to serve on — no credential file, or the last run ended with the
	// daemon's own advice to log in. With a session, Start is one click.
	AskPassword bool
	// State is the one word the page's heartbeat carries for this agent —
	// running, ask or stopped — so the block is swapped only when it changes.
	State string
	OOB   bool // a heartbeat answer: the block replaces itself in place
}

func (a *Agents) status(r *http.Request, handle string) (status, error) {
	running, err := a.running(handle)
	st := status{Handle: handle, Running: running, CSRF: a.deps.CSRFField(r)}
	if !running {
		st.LastLine = exitError(a.logPath(handle))
		_, statErr := os.Stat(a.credPath(handle))
		st.AskPassword = os.IsNotExist(statErr) || strings.Contains(st.LastLine, "juice auth login")
	}
	switch {
	case running:
		st.State = "running"
	case st.AskPassword:
		st.State = "ask"
	default:
		st.State = "stopped"
	}
	return st, err
}

// watch answers the heartbeat for one agent: its status block when its state
// differs from the one shown, nothing otherwise, and nothing to anyone but
// the operator (the routes it mirrors are the operator's).
func (a *Agents) watch(r *http.Request, handle, shown string) (template.HTML, error) {
	if !a.deps.Superuser(r) || !a.exists(handle) {
		return "", nil
	}
	st, err := a.status(r, handle)
	if err != nil || st.State == shown {
		return "", err
	}
	st.OOB = true
	return a.deps.Fragment("_agent_status", st)
}

// running is whether a daemon serves handle: one holds the home's lock, or a
// child this UI spawned a moment ago is alive and has not taken it yet. An
// unreadable lock is an error, never "stopped".
func (a *Agents) running(handle string) (bool, error) {
	dir, err := a.agentDir(handle)
	if err != nil {
		return false, err
	}
	h, err := held(dir)
	return h || a.sup.alive(handle), err
}

type listView struct {
	Agents   []status
	OpenForm bool
	Error    string // why the New agent dialog came back open
	Handle   string // the handle as typed, kept in that dialog
}

type detailView struct {
	status
	Agent    string // agent.md
	Config   string // config.json; "" when absent
	OpenForm string // the ?form= value; selects which dialog renders [open]
	Error    string // why the Sign in dialog came back open

	// An agent IS a user: it holds its own principal on this kernel, funded like
	// anybody's, and spends it (ecosystem-standard §6). Account is that account
	// as the operator reads it, and nil when this home has no kernel account yet
	// — a scaffolded agent that has never been created is a directory, not a
	// principal, and saying so is better than showing a zero.
	//
	// This is read with the VIEWER's own token. The app is the operator's (they
	// alone reach it), so the read is an ordinary superuser read of a user row,
	// not the UI holding somebody else's credential.
	Account *client.AdminUser
}

// ---- handlers ----

func (a *Agents) getIndex(w http.ResponseWriter, r *http.Request) {
	a.renderList(w, r, http.StatusOK, listView{OpenForm: r.URL.Query().Get("form") == "agent-create"})
}

func (a *Agents) renderList(w http.ResponseWriter, r *http.Request, code int, v listView) {
	rows, err := a.list(r)
	if a.deps.HandleErr(w, r, err) {
		return
	}
	v.Agents = rows
	a.deps.RenderPage(w, r, code, "agents", "agents", v)
}

// postCreate makes an agent from a handle and its kernel password: the account
// when the kernel has none, the session, the home, and the daemon. Each refusal
// returns the dialog open with the handle kept and the password gone.
func (a *Agents) postCreate(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	// Taken as typed: a sigiled handle is ill-formed input, not a handle silently
	// meaning the bare one (requirements.md §4; the chat daemon's checkHandles
	// refuses it the same way at its own boundary).
	handle, password := strings.TrimSpace(r.FormValue("handle")), r.FormValue("password")
	refuse := func(msg string) {
		a.renderList(w, r, http.StatusUnprocessableEntity, listView{OpenForm: true, Error: msg, Handle: handle})
	}
	switch {
	case !validHandle(handle):
		refuse("A handle is lowercase letters, digits, - or _.")
		return
	case a.exists(handle):
		refuse("That handle already exists.")
		return
	case password == "":
		refuse("Kernel password is required.")
		return
	}
	ctx := r.Context()
	// Headless, as the chat account: no recovery key. A handle the kernel
	// already holds is adopted, and the password proves it on sign-in.
	if _, err := a.deps.Client.CreateUser(ctx, a.deps.Address(handle), password, ""); err != nil && !accountExists(err) {
		refuse(signInMessage(err))
		return
	}
	if err := a.signIn(ctx, handle, password); err != nil {
		refuse(signInMessage(err))
		return
	}
	if err := a.create(handle); err != nil {
		a.deps.HandleErr(w, r, err)
		return
	}
	if err := a.sup.start(handle, a.deps.Address(handle), a.logPath(handle)); err != nil {
		a.deps.FlashRedirect(w, r, "/agents/"+handle, "error", "Could not start: "+err.Error())
		return
	}
	a.deps.FlashRedirect(w, r, "/agents/"+handle, "success", "Agent created.")
}

// accountExists reads the kernel's refusal of a handle it already holds.
func accountExists(err error) bool {
	ke, ok := client.AsKernelError(err)
	return ok && ke.Kind == client.KindInvalidInput && strings.Contains(ke.KernelMessage, "already exists")
}

// signInMessage is a refused sign-in as the dialog states it.
func signInMessage(err error) string {
	if errors.Is(err, client.ErrUnauthenticated) {
		return "Password is incorrect."
	}
	if ke, ok := client.AsKernelError(err); ok {
		return ke.UserMessage()
	}
	return err.Error()
}

func (a *Agents) getDetail(w http.ResponseWriter, r *http.Request) {
	handle := r.PathValue("handle")
	if !a.exists(handle) {
		a.notFound(w, r)
		return
	}
	a.renderDetail(w, r, http.StatusOK, handle, r.URL.Query().Get("form"), "")
}

func (a *Agents) renderDetail(w http.ResponseWriter, r *http.Request, code int, handle, form, errMsg string) {
	st, err := a.status(r, handle)
	if a.deps.HandleErr(w, r, err) {
		return
	}
	agent, _ := a.readFile(handle, agentFile)
	config, _ := a.readFile(handle, configFile)
	a.deps.RenderPage(w, r, code, "agent", "agents", detailView{
		status:   st,
		Agent:    agent,
		Config:   config,
		OpenForm: form,
		Error:    errMsg,
		Account:  a.account(r, handle),
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

// The lifecycle posts run one at a time under a.mu, so two posts for one
// handle — a stale Start carrying a password against a Delete, two Starts —
// cannot interleave between the running check and the act.

func (a *Agents) postDelete(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	handle := r.PathValue("handle")
	if !a.exists(handle) {
		a.notFound(w, r)
		return
	}
	running, err := a.running(handle)
	if a.deps.HandleErr(w, r, err) {
		return
	}
	if running {
		a.deps.FlashRedirect(w, r, "/agents/"+handle, "error", "Stop the agent first.")
		return
	}
	if err := a.remove(handle); err != nil {
		a.deps.HandleErr(w, r, err)
		return
	}
	a.deps.FlashRedirect(w, r, "/agents", "success", "Agent deleted.")
}

// postStart spawns `juice-agent serve <login>` unless a daemon already serves
// it. A password in the post makes the login's session first (an agent with
// none, or whose session died); a refusal returns the dialog open and spawns
// nothing. The daemon brings its own session, and a refusal of its own is read
// back from its log by the status.
func (a *Agents) postStart(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	handle := r.PathValue("handle")
	if !a.exists(handle) {
		a.notFound(w, r)
		return
	}
	back := returnPath(r, "/agents/"+handle)
	running, err := a.running(handle)
	if a.deps.HandleErr(w, r, err) {
		return
	}
	if running {
		http.Redirect(w, r, back, http.StatusSeeOther)
		return
	}
	if pw := r.FormValue("password"); pw != "" {
		if err := a.signIn(r.Context(), handle, pw); err != nil {
			a.renderDetail(w, r, http.StatusUnprocessableEntity, handle, "start", signInMessage(err))
			return
		}
	}
	if err := a.sup.start(handle, a.deps.Address(handle), a.logPath(handle)); err != nil {
		a.deps.FlashRedirect(w, r, back, "error", "Could not start: "+err.Error())
		return
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// postStop signals the daemon serving handle, whoever started it: the process
// in the home's lock, else the child this UI spawned.
func (a *Agents) postStop(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	handle := r.PathValue("handle")
	dir, err := a.agentDir(handle)
	if err != nil {
		a.notFound(w, r)
		return
	}
	var pid int
	h, err := held(dir)
	if err == nil && h {
		pid, err = lockedPid(dir)
	}
	if err == nil {
		err = a.sup.stop(handle, pid)
	}
	if a.deps.HandleErr(w, r, err) {
		return
	}
	http.Redirect(w, r, returnPath(r, "/agents/"+handle), http.StatusSeeOther)
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

// defaultRoot resolves $JUICE_HOME/agents from the UI home ($JUICE_HOME/ui),
// falling back to JUICE_HOME / ~/.juice when home is empty.
func defaultRoot(home string) string {
	if home != "" {
		return filepath.Join(filepath.Dir(home), "agents")
	}
	base := os.Getenv("JUICE_HOME")
	if base == "" {
		if h, err := os.UserHomeDir(); err == nil {
			base = filepath.Join(h, ".juice")
		}
	}
	return filepath.Join(base, "agents")
}

// agentBin resolves the juice-agent executable: the explicit override, else
// the installation's own bin/, which is where the installer puts the family's
// programs and where a component looks for a sibling (ecosystem-standard §4),
// else PATH.
func agentBin(juiceHome string) string {
	if p := os.Getenv(binEnv); p != "" {
		return p
	}
	if p, err := exec.LookPath(filepath.Join(juiceHome, "bin", "juice-agent")); err == nil {
		return p
	}
	if p, err := exec.LookPath("juice-agent"); err == nil {
		return p
	}
	return ""
}

// account reads the agent's kernel account with the viewer's own token: the
// kernel's row for the handle's address on this kernel, the login its home is
// named by. It is best-effort decoration — a home is a directory on this
// machine and may name no principal at all, never created or created on
// another kernel — and the page must still render, saying so. The kernel
// filters transactions by process, not by party, so the agent's history is not
// read here; the transactions page holds it.
func (a *Agents) account(r *http.Request, handle string) *client.AdminUser {
	if a.deps.Sessions == nil || a.deps.Client == nil {
		return nil // no kernel to ask: the section says there is no account
	}
	ctx := r.Context()
	var acct *client.AdminUser
	_ = a.deps.Sessions.Do(ctx, func(tok string) error {
		if u, err := a.deps.Client.AdminGetUser(ctx, tok, a.deps.Address(handle)); err == nil {
			acct = &u
		}
		return nil // no principal by that name: the section says so
	})
	return acct
}
