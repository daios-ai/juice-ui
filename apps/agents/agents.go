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
//   - starts `juice-agent serve <login>` as a supervised child and stops it
//     with SIGTERM; "running" is whether the tracked child is alive.
//
// The daemon holds its own session, made by the operator with
// `juice auth login <login>`; the UI never reads or writes client credentials
// (requirements.md §5). A daemon that exits states why in its log, and the page
// shows that line.
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
	deps apps.Deps
	root string      // $JUICE_HOME/agents; overridable in tests
	logs string      // $JUICE_HOME/ui/agents, one <login>.log per daemon run
	sup  *supervisor // process supervision; overridable in tests (fake launcher)
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
	mux.HandleFunc("GET /agents/{handle}/status", d.RequireSuperuser(a.getStatus))
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
	if a.sup == nil {
		a.sup = newSupervisor(osLauncher{bin: agentBin(home), home: home})
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
		out = append(out, a.status(r, h))
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

// remove deletes handle's home and the log of its last run.
func (a *Agents) remove(handle string) error {
	dir, err := a.agentDir(handle)
	if err != nil {
		return err
	}
	if err := os.Remove(a.logPath(handle)); err != nil && !os.IsNotExist(err) {
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

// status is an agent's state as the shared _agent_status fragment renders it,
// in a list row, in the polled fragment and on the detail page alike.
type status struct {
	Handle   string
	Running  bool
	CSRF     template.HTML // the fragment carries its own Stop/Start form (rendered outside the Page envelope)
	LastLine string        // the daemon's exit error from its current run, when stopped
}

func (a *Agents) status(r *http.Request, handle string) status {
	st := status{Handle: handle, Running: a.sup.running(handle), CSRF: a.deps.CSRFField(r)}
	if !st.Running {
		st.LastLine = exitError(a.logPath(handle))
	}
	return st
}

type listView struct {
	Agents   []status
	OpenForm bool
}

type detailView struct {
	status
	Agent    string // agent.md
	Config   string // config.json; "" when absent
	OpenForm string // the ?form= value; selects which dialog renders [open]

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
	// Activity is what this agent has been party to, as the kernel serves it to
	// the viewer. It is the point of the section: an operator can raise an agent
	// and, without this, never see what it spends.
	Activity []client.Transaction
}

// ---- handlers ----

func (a *Agents) getIndex(w http.ResponseWriter, r *http.Request) {
	rows, err := a.list(r)
	if a.deps.HandleErr(w, r, err) {
		return
	}
	a.deps.RenderPage(w, r, http.StatusOK, "agents", "agents", listView{
		Agents:   rows,
		OpenForm: r.URL.Query().Get("form") == "agent-create",
	})
}

func (a *Agents) postCreate(w http.ResponseWriter, r *http.Request) {
	// Taken as typed: a sigiled handle is ill-formed input, not a handle silently
	// meaning the bare one (requirements.md §4; the chat daemon's checkHandles
	// refuses it the same way at its own boundary).
	handle := strings.TrimSpace(r.FormValue("handle"))
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
	agent, _ := a.readFile(handle, agentFile)
	config, _ := a.readFile(handle, configFile)
	account, activity := a.account(r, handle)
	a.deps.RenderPage(w, r, http.StatusOK, "agent", "agents", detailView{
		status:   a.status(r, handle),
		Agent:    agent,
		Config:   config,
		OpenForm: r.URL.Query().Get("form"),
		Account:  account,
		Activity: activity,
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

// postStart spawns `juice-agent serve <login>`; the daemon brings its own
// session, and a refusal is read back from its log by the status.
func (a *Agents) postStart(w http.ResponseWriter, r *http.Request) {
	handle := r.PathValue("handle")
	if !a.exists(handle) {
		a.notFound(w, r)
		return
	}
	back := returnPath(r, "/agents/"+handle)
	if err := a.sup.start(handle, a.deps.Address(handle), a.logPath(handle)); err != nil {
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
	_ = a.deps.RenderFragment(w, http.StatusOK, "_agent_status", a.status(r, handle))
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

// account reads the agent's kernel account and what it has been party to, with
// the viewer's own token. Both are best-effort decoration: an agent home is a
// directory on this machine and may name no principal at all — never created,
// or created on another kernel — and the page must still render.
//
// The principal is the handle's address on this kernel, the login its home is
// named by. Nothing is computed
// here. The balance is the kernel's row for that address and the rows are the
// transactions it serves this viewer; the page arranges them and states no
// figure the kernel did not.
func (a *Agents) account(r *http.Request, handle string) (*client.AdminUser, []client.Transaction) {
	if a.deps.Sessions == nil || a.deps.Client == nil {
		return nil, nil // no kernel to ask: the section says there is no account
	}
	ctx := r.Context()
	address := a.deps.Address(handle)
	var acct *client.AdminUser
	var rows []client.Transaction
	_ = a.deps.Sessions.Do(ctx, func(tok string) error {
		u, err := a.deps.Client.AdminGetUser(ctx, tok, address)
		if err != nil {
			return nil // no principal by that name: the section says so
		}
		acct = &u
		// The whole history, because the kernel filters transactions by process
		// and not by party: a bounded read would be the most recent N rows of
		// the WHOLE kernel, and an idle agent would then be reported as having
		// done nothing, which is a false statement about money rather than a
		// short list.
		txs, err := a.deps.Client.ListTransactions(ctx, tok, "", 0)
		if err != nil {
			return nil
		}
		for _, t := range txs {
			if t.Owner == address || t.Caller == address || t.Target == address {
				rows = append(rows, t)
			}
			if len(rows) == agentActivityRows {
				break
			}
		}
		return nil
	})
	return acct, rows
}

// agentActivityRows is how much of an agent's history the page shows before
// sending the reader to the ledger that holds all of it.
const agentActivityRows = 10
