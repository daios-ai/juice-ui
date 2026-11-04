// Package chat is the conversation surface of juice-ui: rooms of people and
// agents, every member a kernel principal, every entry a JSON document.
//
// The package holds two halves with one boundary. The chat service
// (service.go, deliver.go, store.go) is an HTTP server: the kernel wraps its
// endpoints as the actions chat@<kernel> owns, a member reads a room from it
// with its chat key, and it imports nothing of the UI. The app (this file)
// presents it: every room command is a run of one of those actions as the
// viewer, every room read a request to the service, and joining goes through
// the kernel alone, so the service could be served from another program.
//
// A post is a run of msg/post as the member, who the kernel identifies by the
// chat key it connected (delegated_bearer, injected on apps.CallerKeyHeader).
// While that call runs the service creates one kernel task per recipient — the
// member whose address is the message's first word, or every other member —
// carrying what the room said since that member's previous delivery; the
// kernel shows it to the member once the member has approved chat (U52).
// Completing the task is the answer: its onward, chat's reply action, settles
// it in the room.
//
// The UI joins the principals of its installation itself: the person signed
// in, and every agent with a home under $JUICE_HOME/agents, with that agent's
// own session. A session lands on its conversation with the assistant.
//
// Chat is itself a kernel user, chat@<kernel>, provisioned by the seam's
// ServiceAccount; its actions are reconciled at every start. State in
// app state: the account password, the upstream secret its private actions
// carry, and the auth revision it last pushed.
package chat

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/daios-ai/juice-ui/apps"
	"github.com/daios-ai/juice-ui/client"
)

//go:embed templates/*.html
var tmplFS embed.FS

// readTimeout bounds one read of the service.
const readTimeout = 10 * time.Second

// chatHandle is chat's own account; its address is Deps.Address(chatHandle).
const chatHandle = "chat"

// assistantHandle is the agent a session lands in conversation with; the
// agents app makes it.
const assistantHandle = "assistant"

// dbName is the store, in the component home; legacyDBName the previous
// application's, imported once and never written.
const (
	dbName       = "rooms.db"
	legacyDBName = "chat.db"
)

// Chat is the app.
type Chat struct {
	deps   apps.Deps
	svc    *Service
	sa     *apps.ServiceAccount
	st     chatState
	dbPath string       // test override; "" resolves from the home
	httpc  *http.Client // how the UI reaches the service for a read
}

type chatState struct {
	Password string `json:"password"`
	Secret   string `json:"secret"` // the bearer the private actions carry, proving the caller is the kernel
	AuthRev  string `json:"auth_rev"`
}

func New() *Chat { return &Chat{httpc: &http.Client{Timeout: readTimeout}} }

func (c *Chat) Slug() string { return "chat" }

// Nav contributes no destination: chat is the workspace itself, its rooms
// the sidebar's rail (Rail).
func (c *Chat) Nav(me string, superuser bool) []apps.NavEntry { return nil }

// Landing is where a session begins: its conversation with the assistant.
func (c *Chat) Landing() string { return "/chat" }

func (c *Chat) Templates() fs.FS { return tmplFS }

func (c *Chat) Mount(mux *http.ServeMux, d apps.Deps) {
	c.deps = d
	mux.HandleFunc("GET /chat", d.RequireAuth(c.getLanding))
	mux.HandleFunc("POST /chat/rooms", d.RequireAuth(c.postCreateRoom))
	mux.HandleFunc("GET /chat/room/{id}", d.RequireAuth(c.getRoom))
	mux.HandleFunc("GET /chat/room/{id}/entries", d.RequireAuth(c.getEntries))
	mux.HandleFunc("POST /chat/room/{id}/message", d.RequireAuth(c.postMessage))
	mux.HandleFunc("POST /chat/room/{id}/rename", d.RequireAuth(c.postRename))
	mux.HandleFunc("POST /chat/room/{id}/delete", d.RequireAuth(c.postDeleteRoom))
	mux.HandleFunc("POST /chat/room/{id}/leave", d.RequireAuth(c.postLeave))
	mux.HandleFunc("POST /chat/room/{id}/add-member", d.RequireAuth(c.postAddMember))
	mux.HandleFunc("POST /chat/room/{id}/remove-member", d.RequireAuth(c.postRemoveMember))
	d.Watch("chat", c.watch)
	// The kernel calls these, server to server, with no browser and no CSRF token.
	mux.Handle("/_kernel/chat/", http.StripPrefix("/_kernel/chat", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.svc.ServeHTTP(w, r)
	})))
}

// Start provisions the account and secret once, opens the store, and signs
// in as chat in the background; once signed in it reconciles the actions and
// joins the installation's agents.
func (c *Chat) Start(ctx context.Context) error {
	if raw, err := c.deps.LoadAppState(c.Slug()); err == nil && len(raw) > 0 {
		_ = json.Unmarshal(raw, &c.st)
	}
	changed := false
	for _, f := range []*string{&c.st.Password, &c.st.Secret} {
		if *f == "" {
			*f, changed = newSecret(), true
		}
	}
	if changed {
		if err := c.saveState(); err != nil {
			return fmt.Errorf("chat: persist credentials: %w", err)
		}
	}
	path, legacy := c.paths()
	store, err := Open(path, legacy)
	if err != nil {
		return fmt.Errorf("chat: open store: %w", err)
	}
	c.svc = &Service{Store: store, Self: c.self, Secret: c.st.Secret, CallerKey: apps.CallerKeyHeader, Kernel: c.deps.Client, Log: c.deps.Logger}
	c.sa = apps.NewServiceAccount(c.deps, chatHandle, c.st.Password)
	return c.sa.Start(ctx, c.setup)
}

// setup runs each time chat signs in: the actions first, since joining
// connects a key to them, then every local agent.
func (c *Chat) setup(ctx context.Context) error {
	if err := c.ensureActions(ctx); err != nil {
		return err
	}
	c.joinAgents(ctx)
	return nil
}

func (c *Chat) Stop(ctx context.Context) error {
	if c.sa != nil {
		c.sa.Stop()
	}
	if c.svc != nil {
		return c.svc.Close()
	}
	return nil
}

func (c *Chat) saveState() error {
	b, _ := json.Marshal(c.st)
	return c.deps.SaveAppState(c.Slug(), b)
}

// paths resolves the store and the legacy database: in memory when there is
// no persistent state, else in the component home.
func (c *Chat) paths() (path, legacy string) {
	if c.dbPath != "" {
		return c.dbPath, ""
	}
	if c.deps.StatePath == "" {
		return "", ""
	}
	home := c.deps.Home
	if home == "" {
		home = filepath.Dir(c.deps.StatePath)
	}
	_ = os.MkdirAll(home, 0o700)
	return filepath.Join(home, dbName), filepath.Join(home, legacyDBName)
}

func (c *Chat) self() string { return c.deps.Address(chatHandle) }

// base is where the service answers: what the actions' sources point at, and
// what the UI's reads dial, so a service served elsewhere is this one value.
func (c *Chat) base() string { return strings.TrimRight(c.deps.PublicURL, "/") + "/_kernel/chat/" }

func (c *Chat) log() *slog.Logger {
	if c.deps.Logger != nil {
		return c.deps.Logger
	}
	return slog.Default()
}

// ---- the actions ----

// authRev names the auth configuration pushed: the kernel never reads it
// back, so a change is forced through by a revision kept in app state.
func (c *Chat) authRev() string {
	h := sha256.New()
	for _, k := range c.svc.Contracts() {
		h.Write(k.auth)
	}
	return hex.EncodeToString(h.Sum(nil)[:8])
}

// ensureActions makes each action exist as its contract says, pointed at
// this interface, and deletes any other action chat owns. A changed contract
// is pushed whole and re-enabled, which revokes members' grants; the UI
// joins its principals again (setup, and every chat page). Failures accumulate so one does not
// starve the rest; the revision is recorded only after a clean pass.
func (c *Chat) ensureActions(ctx context.Context) error {
	rev := c.authRev()
	force := c.st.AuthRev != rev
	base := c.base()
	err := c.sa.Do(ctx, func(tok string) error {
		existing, err := c.deps.Client.ListActions(ctx, tok, c.self())
		if err != nil {
			return err
		}
		have := map[string]client.Action{}
		for _, a := range existing {
			have[a.Name] = a
		}
		var errs []error
		wanted := map[string]bool{}
		for _, k := range c.svc.Contracts() {
			wanted[k.name] = true
			errs = append(errs, c.ensure(ctx, tok, k, base+k.name, have, force))
		}
		for name, a := range have {
			if !wanted[name] {
				if _, err := c.deps.Client.DeleteAction(ctx, tok, a.ID); err != nil {
					errs = append(errs, fmt.Errorf("chat: delete %s: %w", name, err))
				}
			}
		}
		return errors.Join(errs...)
	})
	if err != nil {
		return err
	}
	if force {
		c.st.AuthRev = rev
		_ = c.saveState()
	}
	return nil
}

func (c *Chat) ensure(ctx context.Context, tok string, k contract, source string, have map[string]client.Action, force bool) error {
	vis, title, desc := k.vis, k.title, k.desc
	a, ok := have[k.name]
	if !ok {
		na, err := c.deps.Client.CreateAction(ctx, tok, client.CreateActionInput{
			Name: k.name, Title: k.title, Kind: "http", Source: source, Method: "POST",
			Description: k.desc, Price: 0, InputSchema: k.in, OutputSchema: k.out, Auth: k.auth,
		})
		if err != nil {
			return fmt.Errorf("chat: create %s: %w", k.name, err)
		}
		if _, err := c.deps.Client.EnableAction(ctx, tok, na.ID); err != nil {
			return fmt.Errorf("chat: enable %s: %w", k.name, err)
		}
		if vis != "private" {
			if _, err := c.deps.Client.UpdateAction(ctx, tok, na.ID, client.UpdateActionInput{Visibility: &vis}); err != nil {
				return fmt.Errorf("chat: visibility %s: %w", k.name, err)
			}
		}
		return nil
	}
	if force || drifted(a, k, source) {
		if _, err := c.deps.Client.UpdateAction(ctx, tok, a.ID, client.UpdateActionInput{
			Source: &source, Method: "POST", InputSchema: k.in, OutputSchema: k.out,
			Title: &title, Description: &desc, Visibility: &vis, Auth: k.auth,
		}); err != nil {
			return fmt.Errorf("chat: reconcile %s: %w", k.name, err)
		}
		if _, err := c.deps.Client.EnableAction(ctx, tok, a.ID); err != nil {
			return fmt.Errorf("chat: enable %s: %w", k.name, err)
		}
		return nil
	}
	// A title alone is quoted, not executed: sending it alone keeps the
	// action live and its grants intact.
	if a.Title != k.title {
		if _, err := c.deps.Client.UpdateAction(ctx, tok, a.ID, client.UpdateActionInput{Title: &title}); err != nil {
			return fmt.Errorf("chat: retitle %s: %w", k.name, err)
		}
	}
	if !a.Active {
		if _, err := c.deps.Client.EnableAction(ctx, tok, a.ID); err != nil {
			return fmt.Errorf("chat: enable %s: %w", k.name, err)
		}
	}
	if a.Vis() != vis {
		if _, err := c.deps.Client.UpdateAction(ctx, tok, a.ID, client.UpdateActionInput{Visibility: &vis}); err != nil {
			return fmt.Errorf("chat: visibility %s: %w", k.name, err)
		}
	}
	return nil
}

// drifted reports a difference on the fields the kernel serves back.
func drifted(a client.Action, k contract, source string) bool {
	if a.HTTP == nil || a.HTTP.URL != source || a.Description != k.desc {
		return true
	}
	var want struct{ Scheme string }
	_ = json.Unmarshal(k.auth, &want)
	if a.AuthScheme != want.Scheme || a.RequiresGrant != (want.Scheme == "delegated_bearer") {
		return true
	}
	return !schemaEqual(a.InputSchema, k.in) || !schemaEqual(a.OutputSchema, k.out)
}

func schemaEqual(x, y json.RawMessage) bool {
	var xv, yv any
	if json.Unmarshal(x, &xv) != nil || json.Unmarshal(y, &yv) != nil {
		return false
	}
	return reflect.DeepEqual(xv, yv)
}

// ---- joining ----

// join makes member a chat member with its own session token, by the path any
// member takes, through the kernel alone: approve chat as a requester (so the
// kernel shows it chat's tasks, U52), run register, complete the verification
// task it creates, and connect the key at chat's root, which covers every action
// a member runs. It returns the key. A failure at any step leaves a key the member
// already connected working: no key is ever retired.
func (c *Chat) join(ctx context.Context, token, member string) (string, error) {
	if err := c.deps.Client.DecideRequester(ctx, token, c.self(), "approve"); err != nil {
		return "", fmt.Errorf("approve %s: %w", c.self(), err)
	}
	args, _ := json.Marshal(map[string]string{"handle": member})
	rep, err := c.deps.Client.Run(ctx, token, c.self()+"/"+registerName, args, "")
	if err != nil {
		return "", fmt.Errorf("register: %w", err)
	}
	var reg struct {
		APIKey string `json:"api_key"`
		TaskID string `json:"task_id"`
	}
	if err := json.Unmarshal(rep.Result, &reg); err != nil || reg.APIKey == "" || reg.TaskID == "" {
		return "", fmt.Errorf("register answered %s", rep.Result)
	}
	if _, err := c.deps.Client.CompleteTask(ctx, token, reg.TaskID, nil); err != nil {
		return "", fmt.Errorf("verify: %w", err)
	}
	if _, err := c.deps.Client.ConnectGrant(ctx, token, c.self(), "", reg.APIKey); err != nil {
		return "", fmt.Errorf("connect %s: %w", c.self(), err)
	}
	return reg.APIKey, nil
}

// covered reports whether the session's user holds a grant on every action of
// chat a member runs, as the kernel's own plan states it per action: an action
// added since the member joined leaves the member uncovered.
func (c *Chat) covered(ctx context.Context, token string) (bool, error) {
	plan, err := c.deps.Client.PlanGrants(ctx, token, c.self())
	if err != nil {
		return false, err
	}
	for _, g := range plan.Groups {
		for _, a := range g.Actions {
			if !a.Granted {
				return false, nil
			}
		}
	}
	return len(plan.Groups) > 0, nil
}

// joinViewer joins the signed-in person unless they are covered and the session
// holds their key, which only a join can give it: the kernel never reads a
// connected token back. The key stays in the session, for the reads.
func (c *Chat) joinViewer(r *http.Request) error {
	ctx := r.Context()
	_, held := c.deps.Sessions.AppSecret(ctx, c.Slug())
	return c.deps.Sessions.Do(ctx, func(tok string) error {
		if held {
			if ok, err := c.covered(ctx, tok); err != nil || ok {
				return err
			}
		}
		key, err := c.join(ctx, tok, c.deps.Me(r))
		if err != nil {
			return err
		}
		c.deps.Sessions.PutAppSecret(ctx, c.Slug(), key)
		return nil
	})
}

// joinAgent joins a local agent, when it holds no chat connector, with the
// session in its credential file. The file is read, and refreshed when the
// kernel refuses its token, under the file's exclusive lock, as every program
// sharing a login does (ecosystem-standard §4).
func (c *Chat) joinAgent(ctx context.Context, login string) error {
	if !c.isLocalAgent(login) {
		return fmt.Errorf("%s has no home here", login)
	}
	f, err := os.OpenFile(filepath.Join(c.root(), "client", "credentials", login+".json"), os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("%s holds no session: %w", login, err)
	}
	defer f.Close() // releases the lock
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	cred := map[string]string{}
	if b, err := io.ReadAll(f); err != nil || json.Unmarshal(b, &cred) != nil {
		return fmt.Errorf("%s: unreadable credential", login)
	}
	me, err := c.deps.Client.Me(ctx, cred["token"])
	if errors.Is(err, client.ErrUnauthenticated) {
		toks, rerr := c.deps.Client.Refresh(ctx, cred["refresh_token"])
		if rerr != nil {
			return fmt.Errorf("%s: session expired: %w", login, rerr)
		}
		cred["token"], cred["refresh_token"] = toks.AccessToken, toks.RefreshToken
		b, _ := json.Marshal(cred)
		if err := f.Truncate(0); err != nil {
			return err
		}
		if _, err := f.WriteAt(b, 0); err != nil {
			return err
		}
		me, err = c.deps.Client.Me(ctx, cred["token"])
	}
	if err != nil {
		return err
	}
	if me.Address != login {
		return fmt.Errorf("the session in %s.json is %s", login, me.Address)
	}
	if ok, err := c.covered(ctx, cred["token"]); err != nil || ok {
		return err
	}
	_, err = c.join(ctx, cred["token"], login)
	return err
}

// joinAgents joins every local agent; one that cannot be joined is logged
// and joined again at the next sign-in, landing or room change.
func (c *Chat) joinAgents(ctx context.Context) {
	for _, login := range c.agents() {
		if err := c.joinAgent(ctx, login); err != nil {
			c.log().Warn("chat: join agent", "agent", login, "err", err)
		}
	}
}

// root is the installation's home, $JUICE_HOME, holding ui/, agents/ and
// client/ (ecosystem-standard §2).
func (c *Chat) root() string { return filepath.Dir(c.deps.Home) }

// agents lists this kernel's agents: the homes under $JUICE_HOME/agents named
// by a login on this kernel.
func (c *Chat) agents() []string {
	if c.deps.Home == "" {
		return nil
	}
	dirs, err := os.ReadDir(filepath.Join(c.root(), "agents"))
	if err != nil {
		return nil
	}
	var out []string
	for _, d := range dirs {
		if d.IsDir() && c.isLocalAgent(d.Name()) {
			out = append(out, d.Name())
		}
	}
	sort.Strings(out)
	return out
}

// isLocalAgent reports a home under $JUICE_HOME/agents for login, a login on
// this kernel.
func (c *Chat) isLocalAgent(login string) bool {
	handle, ok := strings.CutSuffix(login, "@"+c.kernelName())
	if c.deps.Home == "" || !ok || handle == "" || filepath.Base(login) != login {
		return false
	}
	st, err := os.Stat(filepath.Join(c.root(), "agents", login))
	return err == nil && st.IsDir()
}

// kernelName is the name this kernel calls itself.
func (c *Chat) kernelName() string {
	_, k, _ := strings.Cut(c.self(), "@")
	return k
}

// joinFailed is a refused join as a person reads it.
func joinFailed(member string, err error) string {
	detail := err.Error()
	if ke, ok := client.AsKernelError(err); ok {
		detail = ke.UserMessage()
	}
	return member + " is not connected to chat: " + detail
}

// ---- the service, as the UI reaches it ----

// roomRow and entryRow are the service's replies as the UI reads them.
type roomRow struct {
	ID      string   `json:"thread_id"`
	Name    string   `json:"name"`
	Creator string   `json:"creator"`
	Members []string `json:"members"`
	Unread  bool     `json:"unread"`
}

type entryRow struct {
	Seq    int64           `json:"seq"`
	Author string          `json:"author"`
	Body   json.RawMessage `json:"body"`
	At     string          `json:"at"`
}

// roomRead is the service's read of one room.
type roomRead struct {
	Room    roomRow    `json:"room"`
	Entries []entryRow `json:"entries"`
	Removed []int64    `json:"removed"`
	Waiting []string   `json:"waiting"`
}

// run runs one of chat's actions as the viewer and decodes its result into
// out, when there is an out. A viewer the kernel says holds no grant on it is
// joined, and it runs once more.
func (c *Chat) run(r *http.Request, name string, args any, out any) error {
	body, err := json.Marshal(args)
	if err != nil {
		return err
	}
	var rep client.CallReply
	once := func() error {
		return c.deps.Sessions.Do(r.Context(), func(tok string) error {
			var e error
			rep, e = c.deps.Client.Run(r.Context(), tok, c.self()+"/"+name, body, "")
			return e
		})
	}
	err = once()
	if ke, ok := client.AsKernelError(err); ok && ke.Kind == client.KindGrantRequired {
		if jerr := c.joinViewer(r); jerr != nil {
			return jerr
		}
		err = once()
	}
	if err != nil || out == nil {
		return err
	}
	return json.Unmarshal(rep.Result, out)
}

// readError is a read the service refused, by the status it answered.
type readError struct {
	Status int
	Msg    string
}

// Error is the service's own sentence: it can reach a person, as the reason a
// fill was refused, and a raw status never does.
func (e *readError) Error() string { return e.Msg }

// read reads from the service as the viewer, with the chat key the session
// holds. A session holding none joins first, as does one whose key the service
// no longer knows.
func (c *Chat) read(r *http.Request, path string, q url.Values, out any) error {
	key, ok := c.deps.Sessions.AppSecret(r.Context(), c.Slug())
	if !ok {
		if err := c.joinViewer(r); err != nil {
			return err
		}
		key, _ = c.deps.Sessions.AppSecret(r.Context(), c.Slug())
	}
	err := c.readWith(r.Context(), key, path, q, out)
	var re *readError
	if errors.As(err, &re) && re.Status == http.StatusUnauthorized {
		c.deps.Sessions.DropAppSecret(r.Context(), c.Slug())
		if err := c.joinViewer(r); err != nil {
			return err
		}
		key, _ = c.deps.Sessions.AppSecret(r.Context(), c.Slug())
		err = c.readWith(r.Context(), key, path, q, out)
	}
	return err
}

func (c *Chat) readWith(ctx context.Context, key, path string, q url.Values, out any) error {
	target := c.base() + path
	if len(q) > 0 {
		target += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return err
	}
	req.Header.Set(apps.CallerKeyHeader, key)
	resp, err := c.httpc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var body struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&body)
		return &readError{Status: resp.StatusCode, Msg: body.Error}
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// rooms is the viewer's rooms, for the rail on any page. A session not yet
// joined has none to show: it is joined on the next chat page, and the rail is
// never what makes it so.
func (c *Chat) rooms(r *http.Request) ([]roomRow, error) {
	if _, ok := c.deps.Sessions.AppSecret(r.Context(), c.Slug()); !ok {
		return nil, nil
	}
	var out struct {
		Rooms []roomRow `json:"rooms"`
	}
	err := c.read(r, roomListPath, nil, &out)
	return out.Rooms, err
}

// readRoom is one room for the viewer: its latest entries, or with after those
// newer than it; seen moves the viewer's read cursor over them.
func (c *Chat) readRoom(r *http.Request, roomID string, after int64, seen bool) (roomRead, error) {
	q := url.Values{"thread_id": {roomID}}
	if after >= 0 {
		q.Set("after", strconv.FormatInt(after, 10))
	}
	if seen {
		q.Set("seen", "true")
	}
	var out roomRead
	err := c.read(r, roomReadPath, q, &out)
	return out, err
}

// latest asks readRoom for the room's latest entries.
const latest = -1

// refusal is the sentence for a run the kernel or the service refused, "" for
// anything else. The kernel relays no refusal's body, only the status the
// service answered, so the sentence is the UI's, one per status; input the
// action's schema refused is the kernel's own reason.
func refusal(err error) string {
	ke, ok := client.AsKernelError(err)
	if !ok {
		return ""
	}
	if ke.Kind == client.KindInvalidInput {
		return ke.KernelMessage
	}
	switch ke.UpstreamStatus() {
	case http.StatusBadRequest:
		return "That is not valid here."
	case http.StatusUnauthorized:
		return "You have not joined chat yet."
	case http.StatusForbidden:
		return "You may not do that in this room."
	case http.StatusNotFound:
		return "This room no longer exists."
	case http.StatusConflict:
		return "A post is already pending; answer the delivery first."
	}
	return ""
}

// ---- the browser ----

// railView backs the rail: the viewer's rooms, the one in view, and the
// new-conversation dialog with the installation's agents to choose from.
type railView struct {
	Rooms     []roomRow
	Digest    string // what the heartbeat compares the rail by
	Current   string
	Open      bool
	Agents    []string
	Path      string
	CSRFField template.HTML
	OOB       bool // the heartbeat's out-of-band answer: the rail alone, replacing the one shown
}

// roomView backs the room page. The composer is the form of Post, the post
// action's input schema as the kernel serves it, with Locked, the room, sealed
// by the page's address.
type roomView struct {
	Room     roomRow
	Me       string
	Others   []string // the members other than the viewer: whom [@] and a leading address name
	IsOwner  bool
	OpenForm string
	Entries  entriesView
	Post     json.RawMessage
	Locked   json.RawMessage
}

// entriesView backs the room's entries and the fragments that append to
// them; Removed are the posts an answer replaced, for the page to drop.
type entriesView struct {
	RoomID  string
	Entries []entryRow
	Removed []int64
	After   int64
	Working []string // the members holding an unanswered delivery here
	Error   string
}

// Waiting names who the room waits on: "Waiting on a", "Waiting on a and
// b", "Waiting on a, b and c"; "" when nobody. It states an open delivery,
// never activity, which chat cannot observe.
func (v entriesView) Waiting() string {
	switch n := len(v.Working); n {
	case 0:
		return ""
	case 1:
		return "Waiting on " + v.Working[0]
	default:
		return "Waiting on " + strings.Join(v.Working[:n-1], ", ") + " and " + v.Working[n-1]
	}
}

// getLanding is where a session lands: the viewer's conversation with the
// assistant, made on the first visit, else the newest room, else nothing yet
// beside the rail. The viewer and the assistant are joined here when they are
// not yet.
func (c *Chat) getLanding(w http.ResponseWriter, r *http.Request) {
	me := c.deps.Me(r)
	if err := c.joinViewer(r); err != nil {
		c.deps.RenderPage(w, r, http.StatusOK, "landing", "Conversations", landingView{Error: joinFailed(me, err)})
		return
	}
	assistant := c.deps.Address(assistantHandle)
	if me != assistant && c.isLocalAgent(assistant) {
		var out struct {
			Room roomRow `json:"room"`
		}
		if c.deps.HandleErr(w, r, c.run(r, roomDirectName, map[string]string{"member": assistant}, &out)) {
			return
		}
		to := "/chat/room/" + url.PathEscape(out.Room.ID)
		if err := c.joinAgent(r.Context(), assistant); err != nil {
			c.deps.FlashRedirect(w, r, to, "error", joinFailed(assistant, err))
			return
		}
		http.Redirect(w, r, to, http.StatusSeeOther)
		return
	}
	rooms, err := c.rooms(r)
	if c.deps.HandleErr(w, r, err) {
		return
	}
	if len(rooms) > 0 {
		http.Redirect(w, r, "/chat/room/"+url.PathEscape(rooms[0].ID), http.StatusSeeOther)
		return
	}
	c.deps.RenderPage(w, r, http.StatusOK, "landing", "Conversations", landingView{})
}

// landingView backs the page a viewer with no conversation lands on.
type landingView struct{ Error string }

// Rail is the sidebar's list of the viewer's rooms, newest activity first,
// with the new-conversation dialog, open on ?form=new from any page.
func (c *Chat) Rail(r *http.Request) (template.HTML, error) { return c.rail(r, false) }

// rail renders the rail; oob renders it as the heartbeat's answer.
func (c *Chat) rail(r *http.Request, oob bool) (template.HTML, error) {
	rooms, err := c.rooms(r)
	if err != nil {
		return "", err
	}
	current := ""
	if id, ok := strings.CutPrefix(r.URL.Path, "/chat/room/"); ok {
		current, _, _ = strings.Cut(id, "/")
	}
	return c.deps.Fragment("_rail", railView{
		Rooms: rooms, Digest: railDigest(rooms), Current: current, Open: r.URL.Query().Get("form") == "new",
		Agents: c.agents(), Path: r.URL.Path, CSRFField: c.deps.CSRFField(r), OOB: oob,
	})
}

// Context is a room's recent conversation as the planner reads it: the
// viewer's entries as the user's, every other member's as the assistant's,
// named; a document as its JSON text.
func (c *Chat) Context(r *http.Request, roomID string) ([]client.Message, error) {
	me := c.deps.Me(r)
	read, err := c.readRoom(r, roomID, latest, false)
	if err != nil {
		return nil, err
	}
	es := read.Entries
	if len(es) > contextEntries {
		es = es[len(es)-contextEntries:]
	}
	out := make([]client.Message, 0, len(es))
	for _, e := range es {
		text := string(e.Body)
		var str string
		if json.Unmarshal(e.Body, &str) == nil {
			text = str
		}
		if e.Author == me {
			out = append(out, client.Message{Role: "user", Content: text})
			continue
		}
		out = append(out, client.Message{Role: "assistant", Content: e.Author + ": " + text})
	}
	return out, nil
}

// contextEntries is how much of a room the planner reads.
const contextEntries = 20

func (c *Chat) getRoom(w http.ResponseWriter, r *http.Request) {
	if err := c.joinViewer(r); err != nil {
		c.log().Warn("chat: join viewer", "err", err)
	}
	me, id := c.deps.Me(r), r.PathValue("id")
	read, err := c.readRoom(r, id, latest, true)
	if err != nil {
		var re *readError
		if !errors.As(err, &re) || re.Status == http.StatusForbidden || re.Status == http.StatusNotFound {
			err = &client.KernelError{Kind: client.KindNotFound}
		}
		c.deps.HandleErr(w, r, err)
		return
	}
	c.completeDeliveries(r, me, id)
	post, err := c.postSchema(r)
	if c.deps.HandleErr(w, r, err) {
		return
	}
	locked, _ := json.Marshal(map[string]string{"thread_id": id})
	var others []string
	for _, m := range read.Room.Members {
		if m != me {
			others = append(others, m)
		}
	}
	c.deps.RenderPage(w, r, http.StatusOK, "room", "Chat", roomView{
		Room: read.Room, Me: me, Others: others, IsOwner: read.Room.Creator == me, OpenForm: r.URL.Query().Get("form"),
		Entries: entriesView{RoomID: id, Entries: read.Entries, After: maxSeq(read.Entries), Working: read.Waiting},
		Post:    post, Locked: locked,
	})
}

// postSchema is the post action's input schema, the composer's form, as the
// kernel serves it.
func (c *Chat) postSchema(r *http.Request) (json.RawMessage, error) {
	var a client.Action
	err := c.deps.Sessions.Do(r.Context(), func(tok string) error {
		var e error
		var found bool
		a, found, e = c.deps.Client.ResolveRef(r.Context(), tok, c.self()+"/"+postName)
		if e == nil && !found {
			e = &client.KernelError{Kind: client.KindNotFound}
		}
		return e
	})
	return a.InputSchema, err
}

// completeDeliveries completes the viewer's waiting deliveries for a room in
// view, with nothing to say: the room is what they carried.
func (c *Chat) completeDeliveries(r *http.Request, me, roomID string) {
	_ = c.deps.Sessions.Do(r.Context(), func(tok string) error {
		tasks, err := c.deps.Client.ListTasks(r.Context(), tok, "waiting")
		if err != nil {
			return err
		}
		for _, t := range tasks {
			if t.RequiredCaller != me || t.Action != c.self()+"/"+replyName {
				continue
			}
			var pa struct {
				Room string `json:"thread_id"`
			}
			if _ = json.Unmarshal(t.PartialArgs, &pa); pa.Room == roomID {
				_, _ = c.deps.Client.CompleteTask(r.Context(), tok, t.ID, nil)
			}
		}
		return nil
	})
}

// getEntries appends what was said after the shown seq (the composer's
// hx-include carries it).
func (c *Chat) getEntries(w http.ResponseWriter, r *http.Request) {
	after, _ := strconv.ParseInt(r.FormValue("after"), 10, 64)
	_ = c.deps.RenderFragment(w, http.StatusOK, "_chat_poll", c.tail(r, r.PathValue("id"), after, false))
}

// tail is the room's entries after seq, the posts they replaced, who it waits
// on, and the cursor to show next.
func (c *Chat) tail(r *http.Request, roomID string, after int64, seen bool) entriesView {
	read, err := c.readRoom(r, roomID, after, seen)
	if err != nil {
		c.log().Warn("chat: read room", "room", roomID, "err", err)
	}
	if n := maxSeq(read.Entries); n > after {
		after = n
	}
	return entriesView{RoomID: roomID, Entries: read.Entries, Removed: read.Removed, After: after, Working: read.Waiting}
}

// watch answers the heartbeat: the rail when a room appeared, went or was
// read (shown is a digest of the rail it rendered), and a room with its
// entries after the shown seq and who it waits on. A room in a visible tab
// is seen.
func (c *Chat) watch(r *http.Request, key, shown string) (template.HTML, error) {
	if key == railKey {
		rooms, err := c.rooms(r)
		if err != nil {
			return "", err
		}
		if d := railDigest(rooms); d == shown {
			return "", nil
		}
		return c.rail(r, true)
	}
	after, _ := strconv.ParseInt(shown, 10, 64)
	visible := r.Header.Get("X-Visible") != "0"
	v := c.tail(r, key, after, visible)
	if len(v.Entries) > 0 && visible {
		c.completeDeliveries(r, c.deps.Me(r), key)
	}
	if len(v.Entries) == 0 && len(v.Removed) == 0 {
		return c.deps.Fragment("_working", v)
	}
	return c.deps.Fragment("_chat_tick", v)
}

// railKey is the rail's watch: a room id is three words, never this one.
const railKey = "rail"

// railDigest names the rail a page shows: its rooms, their names and which
// are unread, so the heartbeat swaps the rail only when one of them changed.
func railDigest(rooms []roomRow) string {
	h := sha256.New()
	for _, r := range rooms {
		fmt.Fprintf(h, "%s\x00%s\x00%t\x00", r.ID, r.Name, r.Unread)
	}
	return hex.EncodeToString(h.Sum(nil)[:6])
}

// postMessage runs chat's post action as the viewer with the composer's
// fields: what the form carries, and the room from the path, which the form
// shows sealed and never sends. The browser and an agent take one path.
func (c *Chat) postMessage(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	args, err := c.deps.FormArgs(r)
	if err != nil {
		c.composeFailed(w, r, id, err.Error())
		return
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(args, &fields) != nil || fields == nil {
		c.composeFailed(w, r, id, "The JSON must be an object.")
		return
	}
	fields["thread_id"], _ = json.Marshal(id)
	if err := c.run(r, postName, fields, nil); err != nil {
		if reason := refusal(err); reason != "" {
			c.composeFailed(w, r, id, reason)
			return
		}
		if c.deps.HandleErr(w, r, err) {
			return
		}
	}
	if c.deps.IsHTMX(r) {
		after, _ := strconv.ParseInt(r.FormValue("after"), 10, 64)
		_ = c.deps.RenderFragment(w, http.StatusOK, "_chat_poll", c.tail(r, id, after, false))
		return
	}
	http.Redirect(w, r, "/chat/room/"+url.PathEscape(id), http.StatusSeeOther)
}

// composeFailed shows why a post was refused, beside the composer. An htmx
// refusal is 422, so the composer keeps the draft (it resets on success only).
func (c *Chat) composeFailed(w http.ResponseWriter, r *http.Request, roomID, reason string) {
	if c.deps.IsHTMX(r) {
		_ = c.deps.RenderFragment(w, http.StatusUnprocessableEntity, "_chat_refused", entriesView{RoomID: roomID, Error: reason})
		return
	}
	c.deps.FlashRedirect(w, r, "/chat/room/"+url.PathEscape(roomID), "error", reason)
}

// postCreateRoom starts a conversation with the participants chosen: the
// agents ticked and the addresses typed, the viewer included. A name left
// blank is the participants' handles, which the service composes. The local
// agents among them are joined.
func (c *Chat) postCreateRoom(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	members := append(r.Form["agent"], strings.Fields(strings.ReplaceAll(r.FormValue("members"), ",", " "))...)
	if len(members) == 0 {
		c.deps.FlashRedirect(w, r, "/chat?form=new", "error", "Choose who the conversation is with.")
		return
	}
	for _, m := range members {
		if !strings.Contains(m, "@") {
			c.deps.FlashRedirect(w, r, "/chat?form=new", "error", m+" is not an address (handle@kernel).")
			return
		}
	}
	args := map[string]any{"members": members}
	if name := strings.TrimSpace(r.FormValue("name")); name != "" {
		args["name"] = name
	}
	var out struct {
		Room roomRow `json:"room"`
	}
	if err := c.run(r, roomCreateName, args, &out); err != nil {
		c.then(w, r, err, "/chat?form=new")
		return
	}
	c.joinMembers(w, r, members, "/chat/room/"+url.PathEscape(out.Room.ID))
}

// postRename runs chat's rename action as the viewer; the service holds that
// only the room's creator may, and the rail follows through the heartbeat.
func (c *Chat) postRename(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		c.deps.FlashRedirect(w, r, roomURL(r), "error", "A room needs a name.")
		return
	}
	args := map[string]string{"thread_id": r.PathValue("id"), "name": name}
	c.then(w, r, c.run(r, roomRenameName, args, nil), roomURL(r))
}

func (c *Chat) postDeleteRoom(w http.ResponseWriter, r *http.Request) {
	c.then(w, r, c.run(r, roomDeleteName, map[string]string{"thread_id": r.PathValue("id")}, nil), "/chat")
}

func (c *Chat) postLeave(w http.ResponseWriter, r *http.Request) {
	c.then(w, r, c.run(r, roomLeaveName, map[string]string{"thread_id": r.PathValue("id")}, nil), "/chat")
}

func (c *Chat) postAddMember(w http.ResponseWriter, r *http.Request) {
	m := strings.TrimSpace(r.FormValue("member"))
	if !strings.Contains(m, "@") {
		c.deps.FlashRedirect(w, r, roomURL(r), "error", "A member is an address, handle@kernel.")
		return
	}
	if err := c.run(r, roomAddName, map[string]string{"thread_id": r.PathValue("id"), "member": m}, nil); err != nil {
		c.then(w, r, err, roomURL(r))
		return
	}
	c.joinMembers(w, r, []string{m}, roomURL(r))
}

// joinMembers joins the local agents among members and redirects, naming
// the first that could not be joined.
func (c *Chat) joinMembers(w http.ResponseWriter, r *http.Request, members []string, to string) {
	for _, m := range members {
		if !c.isLocalAgent(m) {
			continue
		}
		if err := c.joinAgent(r.Context(), m); err != nil {
			c.deps.FlashRedirect(w, r, to, "error", joinFailed(m, err))
			return
		}
	}
	http.Redirect(w, r, to, http.StatusSeeOther)
}

func (c *Chat) postRemoveMember(w http.ResponseWriter, r *http.Request) {
	args := map[string]string{"thread_id": r.PathValue("id"), "member": strings.TrimSpace(r.FormValue("member"))}
	c.then(w, r, c.run(r, roomRemoveName, args, nil), roomURL(r))
}

// then redirects after a command: a room gone is the not-found page, any other
// refusal a flash on the page it was asked from, anything else the error page.
func (c *Chat) then(w http.ResponseWriter, r *http.Request, err error, to string) {
	if err == nil {
		http.Redirect(w, r, to, http.StatusSeeOther)
		return
	}
	if ke, ok := client.AsKernelError(err); ok && ke.UpstreamStatus() == http.StatusNotFound {
		c.deps.HandleErr(w, r, &client.KernelError{Kind: client.KindNotFound})
		return
	}
	if reason := refusal(err); reason != "" {
		c.deps.FlashRedirect(w, r, to, "error", reason)
		return
	}
	c.deps.HandleErr(w, r, err)
}

func roomURL(r *http.Request) string { return "/chat/room/" + url.PathEscape(r.PathValue("id")) }

func maxSeq(es []entryRow) int64 {
	if len(es) == 0 {
		return 0
	}
	return es[len(es)-1].Seq
}

func newSecret() string {
	var b [24]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
