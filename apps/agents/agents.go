// Package agents is the juice-ui app for managing juice-agents — the sibling
// juice-family daemon (repo juice-agent) that acts as one kernel login and
// answers the tasks addressed to it. Each agent lives in its own home,
// $JUICE_HOME/agents/<login>/, the login being handle@kernel (agent
// requirements.md §11, ecosystem-standard §2).
//
// Unlike apps/chat, this app has no dedicated kernel account: it is a local
// filesystem editor and a client of the process supervisor, not a kernel-HTTP feature. It
//
//   - creates a home with a blank agent.md;
//   - edits agent.md and heartbeat.md, the texts of an agent the operator
//     writes, and config.json through the form of the daemon's own schema of
//     it, $JUICE_HOME/schemas/juice-agent.schema.json, written at every start
//     (agent requirements §11); the daemon reads agent.md at every job, the
//     others at its next start;
//   - declares `juice-agent serve <login>` to the process supervisor, and asks
//     it to start and stop the daemon serving a login, whoever started it.
//
// "Running" is the daemon's own word: it holds `serve.lock` in its home for as
// long as it serves (agent requirements §11, as a kernel holds its home,
// ecosystem-standard §4), so an agent served from a terminal is seen, stopped
// and protected like one the supervisor started, whose own word covers the moment
// before a child takes the lock.
//
// The daemon holds its own session in the installation's client records. The
// UI makes that session as `juice auth login <login>` would — from the password
// the operator types at New agent and at every Start, kept nowhere — and never
// reads one (requirements.md §5, ecosystem-standard §4). The agent's account is
// otherwise managed as the user it is: the operator switches to it (§5). A daemon
// that exits states why in its log, and the page shows that line. The session,
// the home and the credential are written or removed only while no daemon of
// that login is alive.
package agents

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"github.com/daios-ai/juice-ui/apps"
	"github.com/daios-ai/juice-ui/client"
	"github.com/daios-ai/juice-ui/supervisor"
)

//go:embed templates/*.html
var tmplFS embed.FS

// binEnv overrides the juice-agent binary path (otherwise the supervisor resolves
// it from $JUICE_HOME/bin, then PATH).
const binEnv = "JUICE_AGENT_BIN"

// The files of an agent home the UI edits (agent requirements.md §11), and
// the daemon's schema of config.json.
const (
	agentFile     = "agent.md"
	heartbeatFile = "heartbeat.md"
	configFile    = "config.json"
	schemaFile    = "juice-agent.schema.json"
)

// A handle names the home directory, so it is validated strictly: lowercase
// letters, digits, '-' and '_', starting alphanumeric. This forbids '/', '.', and
// '..', making path traversal impossible before the containment check in agentDir.
// agentsIcon is the app's menu icon (a small robot), inline so no core sprite
// edit is needed.
const agentsIcon = template.HTML(`<svg class="ico" aria-hidden="true" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="4" y="8" width="16" height="12" rx="2"/><path d="M12 8V4"/><circle cx="12" cy="3" r="1"/><path d="M9 13h.01M15 13h.01"/><path d="M2 14v2M22 14v2"/></svg>`)

var handleRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// text is a file of an agent home the operator writes in their own words: its
// name, its title as the page says it, and whether the daemon reads it only at
// its start (agent requirements §9, §10).
type text struct {
	File, Title string
	AtStart     bool
}

// texts are those files: agent.md, the instructions, read at every job, and
// heartbeat.md, the duties of the heartbeat, read once at start.
var texts = []text{
	{File: agentFile, Title: "instructions"},
	{File: heartbeatFile, Title: "heartbeat", AtStart: true},
}

// Label is the title as a field's label says it.
func (t text) Label() string { return capitalize(t.Title) }

// textOf is the text a file name names, if any.
func textOf(file string) (text, bool) {
	for _, t := range texts {
		if t.File == file {
			return t, true
		}
	}
	return text{}, false
}

var (
	errBadHandle = errors.New("invalid agent handle")
	errExists    = errors.New("agent already exists")
	errNotFound  = errors.New("no such agent")
	errStale     = errors.New("the configuration changed since this page was loaded; reload it")
)

// Agents is the app. It holds no runtime state: the process supervisor runs the
// daemons, each a program declared here.
type Agents struct {
	deps      apps.Deps
	assistant string     // the assistant's password, from the installer, until the assistant is made
	root      string     // $JUICE_HOME/agents; overridable in tests
	client    string     // $JUICE_HOME/client, the records an agent's session lives in
	schema    string     // $JUICE_HOME/schemas/juice-agent.schema.json, the daemon's schema of config.json
	mu        sync.Mutex // one lifecycle post at a time (create, start, stop, delete)
}

// New constructs the app. Root resolves on Start.
func New() *Agents { return &Agents{} }

// WithAssistant hands the app the password the installer asked for the
// assistant; Start makes the assistant with it and forgets it.
func (a *Agents) WithAssistant(password string) { a.assistant = password }

func (a *Agents) Slug() string { return "agents" }

// Nav contributes the Agents destination, to the operator alone, its badge the
// decisions waiting on the agents (decisions). Everything this app does happens on
// the UI host — it writes the installation's files and runs a long-lived
// daemon — so it is an operator surface, and an entry a non-superuser could not
// use would be an invitation to a 403.
func (a *Agents) Nav(me string, superuser bool) []apps.NavEntry {
	if me == "" || !superuser {
		return nil
	}
	return []apps.NavEntry{{Slug: "agents", Label: "Agents", Href: "/agents", Icon: agentsIcon, Match: "/agents", Live: true}}
}

func (a *Agents) Templates() fs.FS { return tmplFS }

// Mount gates every route on the SUPERUSER, not merely on a session. The
// authority exercised here is the operator's machine: these handlers scaffold
// and delete directories under the installation root, write the files a daemon
// reads, and have the supervisor run that daemon. A kernel account
// confers none of that, and while any signed-in user could reach them, one
// account could edit and stop another's agent, and anyone could start a process
// on the host.
func (a *Agents) Mount(mux *http.ServeMux, d apps.Deps) {
	a.deps = d
	mux.HandleFunc("GET /agents", d.RequireSuperuser(a.getIndex))
	mux.HandleFunc("POST /agents", d.RequireSuperuser(a.postCreate))
	mux.HandleFunc("GET /agents/{handle}", d.RequireSuperuser(a.getDetail))
	d.Watch("agent", a.watch)
	d.Watch("agents", a.watchBadge)
	mux.HandleFunc("POST /agents/{handle}/files/{file}", d.RequireSuperuser(a.postSaveFile))
	mux.HandleFunc("POST /agents/{handle}/config", d.RequireSuperuser(a.postConfig))
	mux.HandleFunc("POST /agents/{handle}/config/json", d.RequireSuperuser(a.postConfigJSON))
	mux.HandleFunc("POST /agents/{handle}/config/form", d.RequireSuperuser(a.postConfigForm))
	mux.HandleFunc("POST /agents/{handle}/start", d.RequireSuperuser(a.postStart))
	mux.HandleFunc("POST /agents/{handle}/stop", d.RequireSuperuser(a.postStop))
	mux.HandleFunc("POST /agents/{handle}/delete", d.RequireSuperuser(a.postDelete))
	mux.HandleFunc("POST /agents/{handle}/password", d.RequireSuperuser(a.postPassword))
	mux.HandleFunc("POST /agents/{handle}/fund/review", d.RequireSuperuser(a.postFundReview))
	mux.HandleFunc("POST /agents/{handle}/fund", d.RequireSuperuser(a.postFund))
}

// Start resolves the agent root and declares the agents. Each path is left
// untouched when already set (tests inject them).
func (a *Agents) Start(ctx context.Context) error {
	if a.root == "" {
		a.root = defaultRoot(a.deps.Home)
	}
	home := filepath.Dir(a.root)
	if a.client == "" {
		a.client = filepath.Join(home, "client")
	}
	if a.schema == "" {
		a.schema = filepath.Join(home, "schemas", schemaFile)
	}
	a.ensureAssistant(ctx)
	a.adopt()
	return nil
}

// programName is an agent's name as the supervisor knows it.
func programName(login string) string { return "agent-" + login }

// declare gives the supervisor handle's daemon, once, running when on and
// otherwise stopped. After that the program's file says whether it runs.
func (a *Agents) declare(handle string, on bool) (string, error) {
	dir, err := a.agentDir(handle)
	if err != nil {
		return "", err
	}
	login := a.deps.Address(handle)
	name := programName(login)
	if _, ok := a.deps.Programs.Status(name); ok {
		return name, nil
	}
	bin := os.Getenv(binEnv)
	if bin == "" {
		bin = "juice-agent"
	}
	return name, a.deps.Programs.Set(name, supervisor.Program{
		Command:  []string{bin, "serve", login},
		Env:      map[string]string{"JUICE_HOME": filepath.Dir(a.root)},
		Lock:     filepath.Join(dir, "serve.lock"),
		Disabled: !on,
	})
}

// start runs handle's daemon now, and from now on at every start.
func (a *Agents) start(handle string) error {
	name, err := a.declare(handle, false)
	if err != nil {
		return err
	}
	return a.deps.Programs.Start(name)
}

// adopt declares every agent home of this kernel that has no program, as an
// installation made before the supervisor holds: one with a session runs. Whether
// its lock is held now says nothing, since an old daemon may have exited with the
// kernel under it.
func (a *Agents) adopt() {
	if a.deps.Programs == nil || a.deps.Address == nil {
		return
	}
	entries, _ := os.ReadDir(a.root)
	suffix := strings.TrimPrefix(a.deps.Address("x"), "x")
	for _, e := range entries {
		h, ok := strings.CutSuffix(e.Name(), suffix)
		if !e.IsDir() || suffix == "" || !ok || !validHandle(h) {
			continue
		}
		_, err := os.Stat(a.credPath(h))
		if _, err := a.declare(h, err == nil); err != nil {
			a.log().Warn("agents: adopt", "handle", h, "err", err)
		}
	}
}

// assistantHandle is the agent a viewer's first room is made with
// (requirements §6.7).
const assistantHandle = "assistant"

// agentsState is the app's state: the assistant's kernel password an earlier
// release made and kept, until the operator sets one (postPassword). Nothing
// writes it any more.
type agentsState struct {
	AssistantPassword string `json:"assistant_password"`
}

// heldPassword is the password an earlier release kept for handle, "" when none.
func (a *Agents) heldPassword(handle string) string {
	if handle != assistantHandle || a.deps.LoadAppState == nil {
		return ""
	}
	var st agentsState
	if raw, err := a.deps.LoadAppState(a.Slug()); err == nil && len(raw) > 0 {
		_ = json.Unmarshal(raw, &st)
	}
	return st.AssistantPassword
}

// ensureAssistant makes the assistant with the password the installer asked
// for, when there is one and no assistant yet, as New agent makes any agent
// (make). The password is used once and forgotten. Never fatal: what fails is
// logged, and the operator can make it from the page.
func (a *Agents) ensureAssistant(ctx context.Context) {
	a.mu.Lock()
	defer a.mu.Unlock()
	pw := a.assistant
	a.assistant = ""
	if pw == "" || a.deps.Client == nil || a.exists(assistantHandle) {
		return
	}
	if err := a.make(ctx, assistantHandle, pw); err != nil {
		a.log().Warn("agents: assistant", "err", err)
	}
}

// make makes an agent from its handle and password: its account when the
// kernel has none (one it holds is adopted only when the password signs in),
// its session, its home, and its program, started. New agent and the
// installer's assistant both come here.
func (a *Agents) make(ctx context.Context, handle, password string) error {
	// Headless, as the chat account: no recovery key.
	if _, err := a.deps.Client.CreateUser(ctx, a.deps.Address(handle), password, ""); err != nil && !accountExists(err) {
		return err
	}
	toks, err := a.deps.Client.SignIn(ctx, a.deps.Address(handle), password)
	if err != nil {
		return err
	}
	if err := a.deps.Client.RecordLogin(ctx, a.client, a.deps.Address(handle), toks); err != nil {
		return err
	}
	if err := a.create(handle); err != nil {
		return err
	}
	return a.start(handle)
}

// newKey is a fresh random key, naming one transfer so it happens once.
func newKey() string {
	var b [18]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func (a *Agents) log() *slog.Logger {
	if a.deps.Logger != nil {
		return a.deps.Logger
	}
	return slog.Default()
}

// Stop stops nothing: the supervisor stops every program when it closes.
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

// cards are this kernel's agents (apps.AgentLogins) as the fleet's control
// pane draws them: those that need the operator first — stopped on an error,
// unable to work, or with decisions waiting — then by handle.
func (a *Agents) cards(r *http.Request) ([]card, error) {
	prices := map[string]int64{} // each model's price, read once for the page
	var out []card
	for _, login := range apps.AgentLogins(a.root, a.kernel()) {
		h := strings.TrimSuffix(login, "@"+a.kernel())
		if !validHandle(h) {
			continue
		}
		c, err := a.card(r, h, prices)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Attention() && !out[j].Attention() })
	return out, nil
}

// card reads an agent as its row and its page draw it: its program's state,
// its account, what one job needs, and what its own session shows.
func (a *Agents) card(r *http.Request, handle string, prices map[string]int64) (card, error) {
	st, err := a.status(r, handle)
	if err != nil {
		return card{}, err
	}
	c := card{status: st, Account: a.account(r, handle), Self: a.self(r.Context(), handle)}
	c.Ready = a.readiness(r, handle, c.Account, prices)
	c.Short = c.Ready.Short
	if a.deps.Sessions != nil {
		c.Accounts = a.deps.Sessions.Accounts(r.Context())
	}
	return c, nil
}

// kernel is this kernel's name, "" while unknown.
func (a *Agents) kernel() string { return strings.TrimPrefix(a.deps.Address("x"), "x@") }

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

// writeFile replaces a file of the agent's home whole, by a temp file renamed
// over it, so a daemon starting meanwhile reads the old file or the new one.
// Which files a request may name is its handler's to decide.
func (a *Agents) writeFile(handle, file string, content []byte) error {
	dir, err := a.agentDir(handle)
	if err != nil {
		return err
	}
	if _, err := os.Stat(dir); err != nil {
		return errNotFound
	}
	tmp, err := os.CreateTemp(dir, file+".*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(content); err == nil {
		err = tmp.Close()
	} else {
		_ = tmp.Close()
	}
	if err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(dir, file))
}

// editor edits config.json through the daemon's schema of it, absent until an
// agent of this installation first starts; the schema pointer is kept as
// saved. Without the schema the form is the JSON editor alone.
func (a *Agents) editor() apps.ConfigEditor {
	schema, _ := os.ReadFile(a.schema)
	return a.deps.ConfigEditor(schema, "$schema")
}

// remove deletes handle's home, the log of its last run and its credential.
func (a *Agents) remove(handle string) error {
	dir, err := a.agentDir(handle)
	if err != nil {
		return err
	}
	if err := os.Remove(a.credPath(handle)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return os.RemoveAll(dir)
}

func (a *Agents) exists(handle string) bool {
	return validHandle(handle) && apps.IsAgent(a.root, a.kernel(), a.deps.Address(handle))
}

// ---- view models ----

// status is an agent's program's state, as its row and its page state it.
type status struct {
	Handle   string
	Running  bool
	CSRF     template.HTML // the row's commands carry it (a heartbeat answer has no Page envelope)
	LastLine string        // the daemon's exit error from its current run, when stopped
	// State is the token the heartbeat carries for this agent's row: running
	// or stopped and, when it has an exit line, a digest of it; the row's pill
	// and commands are answered only when either changes.
	State string
	OOB   bool // a heartbeat answer: the pill and the commands replace themselves in place
	// Short is what stops a running agent working: "suspended", "no funds" or
	// "budget too low" (readiness), "" when nothing does.
	Short string
	// Ready is what one job needs: the model, its call's price and the budget
	// per job, stated on its page.
	Ready readiness
}

// Pill is the agent's one state word: stopped, else what stops it working,
// else running.
func (s status) Pill() string {
	switch {
	case !s.Running:
		return "stopped"
	case s.Short != "":
		return s.Short
	}
	return "running"
}

// card is an agent as its row in the fleet's list (_agent_row,
// _agent_dialogs) and its page draw it: its program's state, its account,
// what its own session shows, and the state of its dialogs.
type card struct {
	status
	// An agent IS a user: it holds its own principal on this kernel, funded
	// like anybody's, and spends it (ecosystem-standard §6). Account is that
	// account as the operator reads it, with the operator's own token, and nil
	// when this home has no kernel account yet.
	Account  *client.AdminUser
	Self     self
	Accounts []string // the accounts the session holds: Switch is one click to them
	OpenForm string   // the dialog that renders [open]
	Error    string   // why it came back open
	Fund     fundView // the Fund dialogs' state
}

// Attention reports an agent that needs the operator: stopped on an error,
// running but unable to work, or with decisions waiting.
func (c card) Attention() bool {
	return (!c.Running && c.LastLine != "") || (c.Running && c.Short != "") || c.Self.Decisions > 0
}

// self is what an agent's own session shows: the decisions waiting on it and
// when it last acted.
type self struct {
	Decisions  int
	LastActive string // the start of its newest transaction, "" when none
}

func (a *Agents) status(r *http.Request, handle string) (status, error) {
	running, err := a.running(handle)
	st := status{Handle: handle, Running: running, CSRF: a.deps.CSRFField(r), State: "stopped"}
	if running {
		st.State = "running"
	} else {
		prog, _ := a.deps.Programs.Status(programName(a.deps.Address(handle)))
		st.LastLine = exitError(prog.Log)
		if prog.Err != "" {
			st.LastLine = prog.Err
		}
	}
	if st.LastLine != "" {
		h := fnv.New32a()
		_, _ = h.Write([]byte(st.LastLine))
		st.State += fmt.Sprintf(".%08x", h.Sum32())
	}
	return st, err
}

// watch answers the heartbeat for one agent's row: its pill and its commands
// when its program's state differs from the one shown, nothing otherwise, and
// nothing to anyone but the operator (the routes it mirrors are the
// operator's).
func (a *Agents) watch(r *http.Request, handle, shown string) (template.HTML, error) {
	if !a.deps.Superuser(r) || !a.exists(handle) {
		return "", nil
	}
	st, err := a.status(r, handle)
	if err != nil || st.State == shown {
		return "", err
	}
	c, err := a.card(r, handle, map[string]int64{})
	if err != nil {
		return "", err
	}
	c.OOB = true
	pill, err := a.deps.Fragment("_agent_state", c)
	if err != nil {
		return "", err
	}
	commands, err := a.deps.Fragment("_agent_commands", c)
	return pill + commands, err
}

// watchBadge keeps the Agents row's badge current: the decisions waiting on
// every agent, for the operator alone.
func (a *Agents) watchBadge(r *http.Request, key, shown string) (template.HTML, error) {
	if key != "badge" || !a.deps.Superuser(r) {
		return "", nil
	}
	n := 0
	for _, login := range apps.AgentLogins(a.root, a.kernel()) {
		n += a.self(r.Context(), strings.TrimSuffix(login, "@"+a.kernel())).Decisions
	}
	if strconv.Itoa(n) == shown {
		return "", nil
	}
	return apps.Badge("agents", "Decisions waiting", n), nil
}

// self reads what an agent's own session shows: the decisions waiting on it —
// requesters awaiting approval, whose tasks the kernel withholds (U52), and
// waiting tasks addressed to it that take no input, an invitation above all,
// which its program cannot do and the operator decides by switching to it —
// and the start of its newest transaction. An agent with no session shows none.
func (a *Agents) self(ctx context.Context, handle string) self {
	var out self
	if a.deps.Client == nil {
		return out
	}
	login := a.deps.Address(handle)
	_ = a.deps.Client.WithLogin(ctx, a.client, login, func(tok string) error {
		reqs, err := a.deps.Client.ListRequesters(ctx, tok)
		if err != nil {
			return err
		}
		for _, q := range reqs {
			if q.Status == "pending" {
				out.Decisions++
			}
		}
		tasks, err := a.deps.Client.WaitingTasks(ctx, tok)
		if err != nil {
			return err
		}
		for _, t := range tasks {
			if t.Waiting() && t.RequiredCaller.Address == login && takesNoInput(t.AllowedInput) {
				out.Decisions++
			}
		}
		if txs, err := a.deps.Client.TransactionsPage(ctx, tok, "", 1, 0); err == nil && len(txs) > 0 {
			out.LastActive = txs[0].StartedAt
		}
		return nil
	})
	return out
}

// takesNoInput reports a completion schema that declares no field at all.
func takesNoInput(schema json.RawMessage) bool {
	var s struct {
		Properties *map[string]json.RawMessage `json:"properties"`
	}
	return json.Unmarshal(schema, &s) == nil && s.Properties != nil && len(*s.Properties) == 0
}

// running is whether a daemon serves handle: one holds the home's lock, or the
// supervisor's child is alive and has not taken it yet. An unreadable lock is an
// error, never "stopped".
func (a *Agents) running(handle string) (bool, error) {
	dir, err := a.agentDir(handle)
	if err != nil {
		return false, err
	}
	h, err := supervisor.Held(filepath.Join(dir, "serve.lock"))
	st, _ := a.deps.Programs.Status(programName(a.deps.Address(handle)))
	return h || st.Running, err
}

// errHeld: a daemon holds the home's lock.
var errHeld = errors.New("a daemon holds serve.lock")

// hold takes home's serve.lock — the daemon's own lock, so a daemon that starts
// meanwhile is refused by its own rule — and returns the file holding it, held
// until closed. errHeld when a daemon holds it.
func hold(home string) (*os.File, error) {
	f, err := os.OpenFile(filepath.Join(home, "serve.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, errHeld
		}
		return nil, err
	}
	return f, nil
}

// exitError is the daemon's exit reason from its last run: the log's last
// non-empty line when it is the daemon's `error: …` line, else "". A daemon
// that was stopped ends on its own "stopped" event, which states no reason.
func exitError(logPath string) string {
	b, _ := os.ReadFile(logPath)
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if last := lines[len(lines)-1]; strings.HasPrefix(last, "error:") {
		return last
	}
	return ""
}

type listView struct {
	Agents   []card
	OpenForm bool
	Error    string // why the New agent dialog came back open
	Handle   string // the handle as typed, kept in that dialog
}

// detailView is an agent's page: its card, and its settings.
type detailView struct {
	card
	Held    bool       // an earlier release keeps this agent's password: Set password
	Texts   []textView // agent.md and heartbeat.md, in the order of texts
	Config  any        // config.json as the editor's form; an absent file is {}
	Version string     // the file's version, carried by the form and checked by a save
}

// textView is one of the agent's texts with what it holds.
type textView struct {
	text
	Content string
}

// First is the text's first line, as its settings row shows it.
func (v textView) First() string {
	for _, l := range strings.Split(v.Content, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			if len([]rune(l)) > 60 {
				l = string([]rune(l)[:60]) + "…"
			}
			return l
		}
	}
	return ""
}

// readiness is whether an agent can do one job: one model call of the action
// its config reasons with, at that action's live price, against its balance
// and its budget per job. Call is 0 when the config names no model.
type readiness struct {
	Reason client.Party // the model action its config reasons with, as its config names it
	Call   int64
	Budget int64
	Short  string
}

// readiness reads the agent's config and its model's price (once per page in
// prices) and says what stops it working, "" when nothing does.
func (a *Agents) readiness(r *http.Request, handle string, acct *client.AdminUser, prices map[string]int64) readiness {
	var cfg struct {
		Reason string `json:"reason"`
		Budget *int64 `json:"default_budget"`
	}
	raw, _ := a.readFile(handle, configFile)
	_ = json.Unmarshal([]byte(raw), &cfg)
	rd := readiness{Reason: client.Party{Address: cfg.Reason}}
	if cfg.Budget != nil {
		rd.Budget = *cfg.Budget
	}
	if cfg.Reason != "" && a.deps.Sessions != nil {
		price, ok := prices[cfg.Reason]
		if !ok {
			_ = a.deps.Sessions.Do(r.Context(), func(tok string) error {
				if row, found, err := a.deps.Client.ResolveRef(r.Context(), tok, cfg.Reason); err == nil && found {
					price = row.Price
				}
				return nil // a model that cannot be read is no claim either way
			})
			prices[cfg.Reason] = price
		}
		rd.Call = price
	}
	switch {
	case acct == nil:
	case acct.Suspended():
		rd.Short = "suspended"
	case acct.Available < max(rd.Call, 1):
		rd.Short = "no funds"
	case cfg.Budget != nil && rd.Budget < rd.Call:
		rd.Short = "budget too low"
	}
	return rd
}

// fundView is a funding in progress: the amount typed, and once reviewed the
// transfer's price and quote, read of its row, and the key that makes the
// transfer happen once however often it is sent.
type fundView struct {
	Typed  string
	Amount int64
	Price  int64
	Quote  string
	Key    string
	Error  string
}

// Total is what the operator pays: the amount and the transfer's price.
func (f fundView) Total() int64 { return f.Amount + f.Price }

// ---- handlers ----

func (a *Agents) getIndex(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	a.renderList(w, r, http.StatusOK, listView{OpenForm: q.Get("form") == "agent-create"}, func(c *card) {
		if c.Handle == q.Get("handle") {
			c.OpenForm = q.Get("form")
		}
	})
}

// renderList renders the fleet's list; at, when not nil, sets the dialog state
// of the agent a command was given to there.
func (a *Agents) renderList(w http.ResponseWriter, r *http.Request, code int, v listView, at func(*card)) {
	cards, err := a.cards(r)
	if a.deps.HandleErr(w, r, err) {
		return
	}
	for i := range cards {
		if at != nil {
			at(&cards[i])
		}
	}
	v.Agents = cards
	a.deps.RenderPage(w, r, code, "agents", "Agents", v)
}

// reopen answers a command given in the fleet's list with its dialog open
// again over the list, saying errMsg, with the Fund state.
func (a *Agents) reopen(w http.ResponseWriter, r *http.Request, code int, handle, form, errMsg string, fund fundView) {
	a.renderList(w, r, code, listView{}, func(c *card) {
		if c.Handle == handle {
			c.OpenForm, c.Error, c.Fund = form, errMsg, fund
		}
	})
}

// postCreate makes an agent from a handle and the password the operator chose
// for it, typed twice (make). Each refusal returns the dialog open with the
// handle kept and the passwords gone.
func (a *Agents) postCreate(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	// Taken as typed: a sigiled handle is ill-formed input, not a handle silently
	// meaning the bare one (requirements.md §4; the chat daemon's checkHandles
	// refuses it the same way at its own boundary).
	handle, password := strings.TrimSpace(r.FormValue("handle")), r.FormValue("password")
	refuse := func(msg string) {
		a.renderList(w, r, http.StatusUnprocessableEntity, listView{OpenForm: true, Error: msg, Handle: handle}, nil)
	}
	switch {
	case !validHandle(handle):
		refuse("A handle is lowercase letters, digits, - or _.")
		return
	case a.exists(handle):
		refuse("That handle already exists.")
		return
	case password == "":
		refuse("A password is required.")
		return
	case password != r.FormValue("confirm_password"):
		refuse("The passwords don't match.")
		return
	}
	if err := a.make(r.Context(), handle, password); err != nil {
		refuse(signInMessage(err))
		return
	}
	a.deps.FlashRedirect(w, r, "/agents?form=fund&handle="+handle, "success", "Agent created.")
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

// renderDetail renders the agent's page: its state, its facts and its
// settings, the dialog form open with errMsg.
func (a *Agents) renderDetail(w http.ResponseWriter, r *http.Request, code int, handle, form, errMsg string) {
	c, err := a.card(r, handle, map[string]int64{})
	if a.deps.HandleErr(w, r, err) {
		return
	}
	c.OpenForm, c.Error = form, errMsg
	var tv []textView
	for _, t := range texts {
		content, _ := a.readFile(handle, t.File)
		tv = append(tv, textView{t, content})
	}
	file, err := a.readFile(handle, configFile)
	if a.deps.HandleErr(w, r, err) {
		return
	}
	config, err := a.editor().Form([]byte(file))
	if a.deps.HandleErr(w, r, err) {
		return
	}
	a.deps.RenderPage(w, r, code, "agent", handle, detailView{
		card:    c,
		Held:    a.heldPassword(handle) != "",
		Texts:   tv,
		Config:  config,
		Version: a.editor().Version([]byte(file)),
	})
}

// postFundReview reads the amount and the transfer's terms and asks for the
// confirm naming what the operator pays: the amount and the transfer's price.
// It mints the key the transfer is sent under, so a double submit moves money
// once (the withdrawal's pattern, wallet.NewWithdrawalID).
func (a *Agents) postFundReview(w http.ResponseWriter, r *http.Request) {
	handle := r.PathValue("handle")
	if !a.exists(handle) {
		a.notFound(w, r)
		return
	}
	f := fundView{Typed: strings.TrimSpace(r.FormValue("amount"))}
	amount, err := a.deps.Amount(f.Typed)
	if err == nil && amount <= 0 {
		err = errors.New("enter an amount")
	}
	if err != nil {
		f.Error = capitalize(err.Error()) + "."
		a.reopen(w, r, http.StatusUnprocessableEntity, handle, "fund", "", f)
		return
	}
	f.Amount = amount
	err = a.deps.Sessions.Do(r.Context(), func(tok string) error {
		row, found, e := a.deps.Client.ResolveRef(r.Context(), tok, client.Transfer)
		if e == nil && !found {
			e = errors.New("this kernel offers no transfer")
		}
		f.Price, f.Quote = row.Price, row.QuoteHash
		return e
	})
	if err != nil {
		a.fundRefused(w, r, handle, f, err)
		return
	}
	f.Key = newKey()
	a.reopen(w, r, http.StatusOK, handle, "fund-review", "", f)
}

// postFund moves the amount reviewed from the operator to the agent: one run
// of sys/transfer, pinned to the quote reviewed and named by its key. Terms
// that moved are reviewed again at the new ones, under a new key, nothing
// having moved.
func (a *Agents) postFund(w http.ResponseWriter, r *http.Request) {
	handle := r.PathValue("handle")
	if !a.exists(handle) {
		a.notFound(w, r)
		return
	}
	price, _ := strconv.ParseInt(r.FormValue("price"), 10, 64)
	f := fundView{Typed: r.FormValue("amount"), Quote: r.FormValue("quote"), Key: r.FormValue("key"), Price: price}
	amount, err := a.deps.Amount(f.Typed)
	if err != nil || amount <= 0 || f.Quote == "" || f.Key == "" {
		a.deps.FlashRedirect(w, r, "/agents?form=fund&handle="+handle, "error", "Review the amount first.")
		return
	}
	f.Amount = amount
	args, _ := json.Marshal(map[string]any{"target": a.deps.Address(handle), "amount": amount})
	err = a.deps.Sessions.Do(r.Context(), func(tok string) error {
		_, e := a.deps.Client.RunOnce(r.Context(), tok, client.Transfer, args, f.Quote, f.Key)
		return e
	})
	if ke, ok := client.AsKernelError(err); ok && ke.Kind == client.KindTermsChanged {
		f.Price, _ = ke.MetaInt64("price")
		f.Quote, f.Key = ke.MetaString("quote_hash"), newKey()
		f.Error = "The transfer's price changed; nothing was sent."
		a.reopen(w, r, http.StatusConflict, handle, "fund-review", "", f)
		return
	}
	if err != nil && unknownOutcome(err) {
		// The transfer may have happened: the same key asks again, and the
		// kernel answers a repeat with the first run's outcome.
		f.Error = "The transfer's outcome is not known yet. Fund again sends nothing twice."
		a.reopen(w, r, http.StatusOK, handle, "fund-review", "", f)
		return
	}
	if err != nil {
		a.fundRefused(w, r, handle, f, err)
		return
	}
	a.deps.FlashRedirect(w, r, "/agents", "success", "Funded "+a.deps.Money(amount)+" · "+a.deps.Address(handle)+".")
}

// unknownOutcome reports a run whose outcome is not known: no answer reached
// us, the kernel sent the call and it has not settled, or a run under the same
// key has not settled yet. Anything else is a refusal that moved nothing.
func unknownOutcome(err error) bool {
	ke, ok := client.AsKernelError(err)
	if !ok {
		return true
	}
	switch ke.Kind {
	case client.KindTimeout, client.KindUnavailable, client.KindInternal:
		return true
	}
	return ke.Status == http.StatusConflict && ke.Kind != client.KindTermsChanged
}

// fundRefused returns the Fund dialog open on the kernel's reason, the amount
// kept; a lapsed session goes where any other does.
func (a *Agents) fundRefused(w http.ResponseWriter, r *http.Request, handle string, f fundView, err error) {
	if errors.Is(err, client.ErrUnauthenticated) {
		a.deps.HandleErr(w, r, err)
		return
	}
	f.Error, f.Quote, f.Key = capitalize(err.Error())+".", "", ""
	if ke, ok := client.AsKernelError(err); ok {
		f.Error = ke.UserMessage()
	}
	a.reopen(w, r, http.StatusUnprocessableEntity, handle, "fund", "", f)
}

// postSaveFile writes one of the agent's texts; no other file is this route's.
func (a *Agents) postSaveFile(w http.ResponseWriter, r *http.Request) {
	handle := r.PathValue("handle")
	t, ok := textOf(r.PathValue("file"))
	if !ok || !a.exists(handle) {
		a.notFound(w, r)
		return
	}
	content := strings.ReplaceAll(r.FormValue("content"), "\r\n", "\n")
	if err := a.writeFile(handle, t.File, []byte(content)); a.deps.HandleErr(w, r, err) {
		return
	}
	msg := "Saved."
	if t.AtStart {
		msg = a.saved(handle)
	}
	a.deps.FlashRedirect(w, r, "/agents/"+handle, "success", msg)
}

// saved is the flash of a save of a file the daemon reads at its start: a
// running agent applies it once stopped and started.
func (a *Agents) saved(handle string) string {
	if running, _ := a.running(handle); running {
		return "Saved. Stop and start " + handle + " to apply."
	}
	return "Saved."
}

// config reads the agent's config.json for its three handlers: "" when
// absent, an error when unreadable, never an empty file in its place.
func (a *Agents) config(w http.ResponseWriter, r *http.Request) (handle string, file []byte, ok bool) {
	handle = r.PathValue("handle")
	if !a.exists(handle) {
		a.notFound(w, r)
		return "", nil, false
	}
	content, err := a.readFile(handle, configFile)
	if a.deps.HandleErr(w, r, err) {
		return "", nil, false
	}
	return handle, []byte(content), true
}

// postConfig writes the form over the file the page was rendered from. The
// daemon reads the file at its next start, which the flash of a running
// agent says; a stale version is refused, since a daemon's start adds the
// keys a file lacks and a daemon started from a terminal is not under a.mu.
func (a *Agents) postConfig(w http.ResponseWriter, r *http.Request) {
	handle, file, ok := a.config(w, r)
	if !ok {
		return
	}
	back := "/agents/" + handle
	editor := a.editor()
	var to []byte
	var changed bool
	err := errStale
	if r.FormValue("version") == editor.Version(file) {
		to, changed, err = editor.Read(r, file)
	}
	if err != nil {
		a.deps.FlashRedirect(w, r, back, "error", capitalize(err.Error())+".")
		return
	}
	if !changed {
		a.deps.FlashRedirect(w, r, back, "info", "Nothing changed.")
		return
	}
	if err := a.writeFile(handle, configFile, append(to, '\n')); a.deps.HandleErr(w, r, err) {
		return
	}
	a.deps.FlashRedirect(w, r, back, "success", a.saved(handle))
}

func (a *Agents) postConfigJSON(w http.ResponseWriter, r *http.Request) {
	if _, file, ok := a.config(w, r); ok {
		a.editor().ToJSON(w, r, file)
	}
}

func (a *Agents) postConfigForm(w http.ResponseWriter, r *http.Request) {
	if _, file, ok := a.config(w, r); ok {
		a.editor().ToForm(w, r, file)
	}
}

// capitalize upper-cases a message's first letter, as the core flashes one.
func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
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
	dir, err := a.agentDir(handle)
	if a.deps.HandleErr(w, r, err) {
		return
	}
	// The program goes first, so nothing restarts the daemon, and then the home's
	// lock is held through the removal: a daemon started meanwhile is refused by
	// its own rule, and none is left holding a deleted lock.
	running, err := a.running(handle)
	if a.deps.HandleErr(w, r, err) {
		return
	}
	if running {
		a.deps.FlashRedirect(w, r, "/agents/"+handle, "error", "Stop the agent first.")
		return
	}
	if err := a.deps.Programs.Remove(programName(a.deps.Address(handle))); a.deps.HandleErr(w, r, err) {
		return
	}
	lock, err := hold(dir)
	if errors.Is(err, errHeld) {
		a.deps.FlashRedirect(w, r, "/agents/"+handle, "error", "Stop the agent first.")
		return
	}
	if a.deps.HandleErr(w, r, err) {
		return
	}
	defer lock.Close()
	if err := a.remove(handle); err != nil {
		a.deps.HandleErr(w, r, err)
		return
	}
	a.deps.FlashRedirect(w, r, "/agents", "success", "Agent deleted.")
}

// postStart starts a stopped agent with the password the operator types,
// every time (requirements §6.1): the password signs in, and only then is the
// session recorded and the daemon started, so a wrong password changes nothing.
// An agent already running is left as it is.
func (a *Agents) postStart(w http.ResponseWriter, r *http.Request) {
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
		http.Redirect(w, r, "/agents", http.StatusSeeOther)
		return
	}
	toks, err := a.deps.Client.SignIn(r.Context(), a.deps.Address(handle), r.FormValue("password"))
	if err != nil {
		a.reopen(w, r, http.StatusUnprocessableEntity, handle, "start", signInMessage(err), fundView{})
		return
	}
	if err := a.deps.Client.RecordLogin(r.Context(), a.client, a.deps.Address(handle), toks); err != nil {
		a.reopen(w, r, http.StatusUnprocessableEntity, handle, "start", signInMessage(err), fundView{})
		return
	}
	if err := a.start(handle); err != nil {
		a.deps.FlashRedirect(w, r, "/agents", "error", "Could not start: "+err.Error())
		return
	}
	http.Redirect(w, r, "/agents", http.StatusSeeOther)
}

// postPassword sets the password of an agent whose password an earlier
// release made and kept: it signs in with the kept one, changes it to the one
// typed twice, ends that session and forgets the kept one. A command of its
// own, not folded into Start: an agent account has no recovery phrase, so a
// mistyped password must not become the account's unseen.
func (a *Agents) postPassword(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	handle := r.PathValue("handle")
	held := a.heldPassword(handle)
	if !a.exists(handle) || held == "" {
		a.notFound(w, r)
		return
	}
	password := r.FormValue("password")
	switch {
	case password == "":
		a.renderDetail(w, r, http.StatusUnprocessableEntity, handle, "password", "A password is required.")
		return
	case password != r.FormValue("confirm_password"):
		a.renderDetail(w, r, http.StatusUnprocessableEntity, handle, "password", "The passwords don't match.")
		return
	}
	ctx := r.Context()
	toks, err := a.deps.Client.SignIn(ctx, a.deps.Address(handle), held)
	if err == nil {
		_, err = a.deps.Client.UpdateMe(ctx, toks.AccessToken, client.UpdateMeInput{CurrentPassword: held, Password: password})
		_ = a.deps.Client.Logout(context.WithoutCancel(ctx), toks.RefreshToken)
	}
	if err != nil {
		a.renderDetail(w, r, http.StatusUnprocessableEntity, handle, "password", signInMessage(err))
		return
	}
	if err := a.deps.SaveAppState(a.Slug(), json.RawMessage(`{}`)); a.deps.HandleErr(w, r, err) {
		return
	}
	a.deps.FlashRedirect(w, r, "/agents/"+handle, "success", "Password set.")
}

// postStop has the supervisor stop the daemon serving handle, whoever started it,
// and keep it stopped across restarts.
func (a *Agents) postStop(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	handle := r.PathValue("handle")
	if !a.exists(handle) {
		a.notFound(w, r)
		return
	}
	if err := a.stop(handle); a.deps.HandleErr(w, r, err) {
		return
	}
	http.Redirect(w, r, "/agents", http.StatusSeeOther)
}

// stop has the supervisor stop handle's daemon and keep it stopped.
func (a *Agents) stop(handle string) error {
	name, err := a.declare(handle, false)
	if err != nil {
		return err
	}
	return a.deps.Programs.Stop(name)
}

// credPath is the credential file of handle's login (ecosystem-standard §4).
func (a *Agents) credPath(handle string) string {
	return filepath.Join(a.client, "credentials", a.deps.Address(handle)+".json")
}

func (a *Agents) notFound(w http.ResponseWriter, r *http.Request) {
	a.deps.HandleErr(w, r, client.ErrNotFound)
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
