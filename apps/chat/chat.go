// Package chat is the conversation surface of juice-ui: rooms of people and
// agents, every member a kernel principal, every entry a JSON document.
//
// Chat itself is a program of its own, juice-chat, an HTTP server the kernel
// wraps as the actions chat@<kernel> owns. This app starts and stops that
// program, as the UI starts the kernel and the agents, and otherwise reaches it
// only by running those actions through the kernel as the viewer. It knows the
// provider's address, the actions' names and their JSON shapes.
//
// The UI joins the principals of its installation itself: the person signed
// in, and every agent with a home under $JUICE_HOME/agents, with that agent's
// own session. A session lands on its conversation with the assistant.
//
// A run writes a transaction, so a read is run on a gesture, or when the kernel
// shows that something changed for the viewer: the heartbeat compares the
// viewer's open deliveries, a kernel read that writes nothing, and never runs an
// action to find out.
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
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/daios-ai/juice-ui/apps"
	"github.com/daios-ai/juice-ui/client"
	"github.com/daios-ai/juice-ui/supervisor"
)

//go:embed templates/*.html
var tmplFS embed.FS

// binEnv overrides the juice-chat binary path.
const binEnv = "JUICE_CHAT_BIN"

// The actions the UI runs, by name under chat@<kernel>/.
const (
	registerName   = "register"
	postName       = "msg/post"
	replyName      = "reply"
	roomCreateName = "room/create"
	roomDirectName = "room/direct"
	roomRenameName = "room/rename"
	roomAddName    = "room/add"
	roomRemoveName = "room/remove"
	roomLeaveName  = "room/leave"
	roomDeleteName = "room/delete"
	roomListName   = "room/list"
	roomReadName   = "room/read"
)

// chatHandle is chat's own account; its address is Deps.Address(chatHandle).
const chatHandle = "chat"

// assistantHandle is the agent a session lands in conversation with; the
// agents app makes it.
const assistantHandle = "assistant"

// Chat is the app.
type Chat struct {
	deps     apps.Deps
	sa       *apps.ServiceAccount
	st       chatState
	register func(login string) error // `juice-chat register`; a test sets its own

	mu      sync.Mutex
	changes map[string]int64 // per address, what this UI changed: see signal
}

type chatState struct {
	Password string `json:"password"`
}

func New() *Chat { return &Chat{changes: map[string]int64{}} }

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
	mux.HandleFunc("POST /chat/room/{id}/message", d.RequireAuth(c.postMessage))
	mux.HandleFunc("POST /chat/room/{id}/rename", d.RequireAuth(c.postRename))
	mux.HandleFunc("POST /chat/room/{id}/delete", d.RequireAuth(c.postDeleteRoom))
	mux.HandleFunc("POST /chat/room/{id}/leave", d.RequireAuth(c.postLeave))
	mux.HandleFunc("POST /chat/room/{id}/add-member", d.RequireAuth(c.postAddMember))
	mux.HandleFunc("POST /chat/room/{id}/remove-member", d.RequireAuth(c.postRemoveMember))
	d.Watch("chat", c.watch)
}

// Start provisions the account's password once and signs in as chat in the
// background; once signed in it brings the chat server up.
func (c *Chat) Start(ctx context.Context) error {
	if raw, err := c.deps.LoadAppState(c.Slug()); err == nil && len(raw) > 0 {
		_ = json.Unmarshal(raw, &c.st)
	}
	if c.st.Password == "" {
		c.st.Password = newSecret()
		b, _ := json.Marshal(c.st)
		if err := c.deps.SaveAppState(c.Slug(), b); err != nil {
			return fmt.Errorf("chat: persist credentials: %w", err)
		}
	}
	if c.register == nil {
		c.register = c.runRegister
	}
	c.sa = apps.NewServiceAccount(c.deps, chatHandle, c.st.Password)
	return c.sa.Start(ctx, c.setup)
}

// setup runs each time chat signs in, and again after a failure. It records the
// provider's session where any program finds a login, declares the server to the
// supervisor, has it register the provider's actions, and joins every local agent,
// since joining connects a key to those actions.
func (c *Chat) setup(ctx context.Context) error {
	if c.deps.Home == "" || c.deps.Programs == nil {
		return errors.New("chat: this UI has no home to run the chat server in")
	}
	toks := c.sa.Tokens()
	me, err := c.deps.Client.Me(ctx, toks.AccessToken)
	if err != nil {
		return err
	}
	records := filepath.Join(c.root(), "client")
	if err := c.deps.Client.RecordKernel(ctx, records); err != nil {
		return err
	}
	if err := client.WriteCredential(filepath.Join(records, "credentials", c.self()+".json"), toks, me.ID); err != nil {
		return err
	}
	if err := c.serve(ctx); err != nil {
		return err
	}
	if err := c.register(c.self()); err != nil {
		return err
	}
	c.joinAgents(ctx)
	return nil
}

// Stop ends the sign-in loop. The supervisor stops the chat server when it closes.
func (c *Chat) Stop(ctx context.Context) error {
	if c.sa != nil {
		c.sa.Stop()
	}
	return nil
}

func (c *Chat) self() string { return c.deps.Address(chatHandle) }

func (c *Chat) log() *slog.Logger {
	if c.deps.Logger != nil {
		return c.deps.Logger
	}
	return slog.Default()
}

// ---- the program ----

const programName = "chat"

// serve declares the chat server to the supervisor, once (after that the program's
// file is the operator's), and returns once its child holds the server's lock. The
// supervisor is the UI's one contact with the server.
func (c *Chat) serve(ctx context.Context) error {
	lock := filepath.Join(c.root(), "services", "chat", "serve.lock")
	if _, ok := c.deps.Programs.Status(programName); !ok {
		bin := os.Getenv(binEnv)
		if bin == "" {
			bin = "juice-chat"
		}
		err := c.deps.Programs.Set(programName, supervisor.Program{
			Command: []string{bin, "serve"},
			Env:     map[string]string{"JUICE_HOME": c.root()},
			Lock:    lock,
		})
		if err != nil {
			return err
		}
	}
	var st supervisor.Status
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); {
		st, _ = c.deps.Programs.Status(programName)
		if st.Disabled { // the operator runs it
			return nil
		}
		if st.Err != "" {
			return fmt.Errorf("juice-chat: %s", st.Err)
		}
		if held, err := supervisor.Held(lock); err != nil || (st.Running && held) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
	return fmt.Errorf("juice-chat did not start; see %s", st.Log)
}

// runRegister is `juice-chat register LOGIN`, a command that ends by itself.
func (c *Chat) runRegister(login string) error {
	bin := apps.Bin(binEnv, c.root(), "juice-chat")
	if bin == "" {
		return fmt.Errorf("juice-chat binary not found (install it or set %s)", binEnv)
	}
	cmd := exec.Command(bin, "register", login)
	cmd.Env = append(os.Environ(), "JUICE_HOME="+c.root())
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("juice-chat register: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

// ---- joining ----

// join makes member a chat member with its own session token, by the path any
// member takes, through the kernel alone: approve chat as a requester (so the
// kernel shows it chat's tasks, U52), run register, complete the verification
// task it creates, and connect the key at chat's root, which covers every action
// a member runs. A failure at any step leaves a key the member already connected
// working: no key is ever retired.
func (c *Chat) join(ctx context.Context, token, member string) error {
	if err := c.deps.Client.DecideRequester(ctx, token, c.self(), "approve"); err != nil {
		return fmt.Errorf("approve %s: %w", c.self(), err)
	}
	args, _ := json.Marshal(map[string]string{"handle": member})
	rep, err := c.deps.Client.Run(ctx, token, c.self()+"/"+registerName, args, "")
	if err != nil {
		return fmt.Errorf("register: %w", err)
	}
	var reg struct {
		APIKey string `json:"api_key"`
		TaskID string `json:"task_id"`
	}
	if err := json.Unmarshal(rep.Result, &reg); err != nil || reg.APIKey == "" || reg.TaskID == "" {
		return fmt.Errorf("register answered %s", rep.Result)
	}
	if _, err := c.deps.Client.CompleteTask(ctx, token, reg.TaskID, nil); err != nil {
		return fmt.Errorf("verify: %w", err)
	}
	if _, err := c.deps.Client.ConnectGrant(ctx, token, c.self(), "", reg.APIKey); err != nil {
		return fmt.Errorf("connect %s: %w", c.self(), err)
	}
	return nil
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

// joinViewer joins the signed-in person unless the kernel's plan shows them
// covered.
func (c *Chat) joinViewer(r *http.Request) error {
	ctx := r.Context()
	return c.deps.Sessions.Do(ctx, func(tok string) error {
		if ok, err := c.covered(ctx, tok); err != nil || ok {
			return err
		}
		return c.join(ctx, tok, c.deps.Me(r))
	})
}

// joinAgent joins a local agent, when it is not covered, with the session in its
// credential file.
func (c *Chat) joinAgent(ctx context.Context, login string) error {
	if !c.isLocalAgent(login) {
		return fmt.Errorf("%s has no home here", login)
	}
	return c.deps.Client.WithLogin(ctx, filepath.Join(c.root(), "client"), login, func(tok string) error {
		if ok, err := c.covered(ctx, tok); err != nil || ok {
			return err
		}
		return c.join(ctx, tok, login)
	})
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

// ---- chat, as the UI reaches it ----

// roomRow and entryRow are chat's replies as the UI reads them.
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

// roomRead is the reply of room/read.
type roomRead struct {
	Room    roomRow    `json:"room"`
	Entries []entryRow `json:"entries"`
	Removed []int64    `json:"removed"`
	Waiting []string   `json:"waiting"`
	More    bool       `json:"more"`
	Rooms   []roomRow  `json:"rooms"`
}

// call runs one of chat's actions as the viewer and decodes its result into out,
// when there is an out.
func (c *Chat) call(r *http.Request, name string, args, out any) error {
	body, err := json.Marshal(args)
	if err != nil {
		return err
	}
	var rep client.CallReply
	if err := c.deps.Sessions.Do(r.Context(), func(tok string) (e error) {
		rep, e = c.deps.Client.Run(r.Context(), tok, c.self()+"/"+name, body, "")
		return e
	}); err != nil || out == nil {
		return err
	}
	return json.Unmarshal(rep.Result, out)
}

// run is call for what the viewer does. A viewer the kernel holds no grant for,
// or whose key chat does not know, is joined, and it runs once more.
func (c *Chat) run(r *http.Request, name string, args, out any) error {
	err := c.call(r, name, args, out)
	if ke, ok := client.AsKernelError(err); ok && (ke.Kind == client.KindGrantRequired || ke.UpstreamStatus() == http.StatusUnauthorized) {
		if jerr := c.deps.Sessions.Do(r.Context(), func(tok string) error { return c.join(r.Context(), tok, c.deps.Me(r)) }); jerr != nil {
			return jerr
		}
		err = c.call(r, name, args, out)
	}
	if err == nil && name != roomReadName {
		c.changed(c.deps.Me(r))
	}
	return err
}

// readRoom is one room for the viewer: its latest entries, or with after those
// newer than it; seen moves the viewer's read cursor over them.
func (c *Chat) readRoom(r *http.Request, roomID string, after int64, seen bool) (roomRead, error) {
	args := map[string]any{"thread_id": roomID, "seen": seen}
	if after != latest {
		args["after"] = after
	}
	var out roomRead
	err := c.run(r, roomReadName, args, &out)
	return out, err
}

// latest asks readRoom for the room's latest entries.
const latest = -1

// listRooms is the viewer's rooms, as a run that never joins: the rail is not
// what makes a viewer a member.
func (c *Chat) listRooms(r *http.Request, signal string) ([]roomRow, error) {
	var out struct {
		Rooms []roomRow `json:"rooms"`
	}
	err := c.call(r, roomListName, struct{}{}, &out)
	if err == nil {
		c.remember(r, out.Rooms, signal)
	}
	return out.Rooms, err
}

// railCache is what the session keeps for the rail: the rooms last read, and the
// signal read before that run, so a change since is caught by the next tick.
type railCache struct {
	Rooms  []roomRow `json:"rooms"`
	Signal string    `json:"signal"`
}

func (c *Chat) remember(r *http.Request, rooms []roomRow, signal string) {
	b, _ := json.Marshal(railCache{rooms, signal})
	c.deps.Sessions.PutAppCache(r.Context(), c.Slug(), string(b))
}

// ---- what changed ----

// deliveries is the viewer's open deliveries: those addressed to them, and those
// they own, being the posts of theirs still unanswered. A kernel read; it writes
// nothing.
func (c *Chat) deliveries(r *http.Request) ([]client.Task, error) {
	me := c.deps.Me(r)
	var out []client.Task
	err := c.deps.Sessions.Do(r.Context(), func(tok string) error {
		tasks, e := c.deps.Client.OpenTasks(r.Context(), tok)
		out = nil
		for _, t := range tasks {
			if t.Action == c.self()+"/"+replyName && (t.Owner == me || t.RequiredCaller == me) {
				out = append(out, t)
			}
		}
		return e
	})
	return out, err
}

// changed counts a change this UI made for an address.
func (c *Chat) changed(me string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.changes[me]++
}

// signal names what the viewer's chat stands at: their open deliveries, and the
// changes this UI made for them. The deliveries alone can return to an earlier
// set, when one arrives and another tab settles it between two ticks; a task
// never reopens, so every other change moves them.
func (c *Chat) signal(me string, tasks []client.Task) string {
	ids := make([]string, len(tasks))
	for i, t := range tasks {
		ids[i] = t.ID
	}
	sort.Strings(ids)
	c.mu.Lock()
	n := c.changes[me]
	c.mu.Unlock()
	return digest(fmt.Sprint(n, ids))
}

func digest(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:6])
}

// held reports a delivery the viewer can settle in a room.
func held(t client.Task, me, roomID string) bool {
	var pa struct {
		Room string `json:"thread_id"`
	}
	_ = json.Unmarshal(t.PartialArgs, &pa)
	return t.Waiting() && t.RequiredCaller == me && pa.Room == roomID
}

// settle completes the viewer's waiting deliveries for a room just read, with
// nothing to say, since the room is what they carried, and answers the tasks left
// open. It follows a read that succeeded: a completion chat cannot receive would
// spend the task and leave the delivery held there.
func (c *Chat) settle(r *http.Request, roomID string, tasks []client.Task) []client.Task {
	me := c.deps.Me(r)
	open := tasks[:0:0]
	for _, t := range tasks {
		if !held(t, me, roomID) || c.deps.Sessions.Do(r.Context(), func(tok string) error {
			_, e := c.deps.Client.CompleteTask(r.Context(), tok, t.ID, nil)
			return e
		}) != nil {
			open = append(open, t)
		}
	}
	if len(open) != len(tasks) {
		c.changed(me)
	}
	return open
}

// watchState is what a page's watch says it shows: the room's last entry, the
// signal of the last read that succeeded, the rail's digest, and after a failed
// read how many failed and when the next may run.
type watchState struct {
	After  int64
	Signal string
	Rail   string
	Fails  int
	Retry  int64
}

func (w watchState) String() string {
	return fmt.Sprintf("%d.%s.%s.%d.%d", w.After, w.Signal, w.Rail, w.Fails, w.Retry)
}

func parseWatch(s string) (w watchState) {
	p := strings.Split(s, ".")
	w.After, _ = strconv.ParseInt(p[0], 10, 64)
	if len(p) != 5 {
		return w
	}
	w.Signal, w.Rail = p[1], p[2]
	w.Fails, _ = strconv.Atoi(p[3])
	w.Retry, _ = strconv.ParseInt(p[4], 10, 64)
	return w
}

// due reports whether a page should read: a failed read is owed once its delay
// has passed, whatever the signal says by then; otherwise, when its signal is not
// the kernel's.
func (w watchState) due(signal string) bool {
	if w.Fails > 0 {
		return time.Now().Unix() >= w.Retry
	}
	return signal != w.Signal
}

// failed is the state after a failed read. It acknowledges nothing, so the page
// reads again until it has shown the change, 5 s later, then 10, 20, 40, and every
// 60 s, so a chat that is down is not run on every tick.
func (w watchState) failed() watchState {
	delay := min(5<<min(w.Fails, 4), 60)
	w.Fails, w.Retry = w.Fails+1, time.Now().Unix()+int64(delay)
	return w
}

// watchView is one watch input of a page: the rail's on a page that is not a
// room, the room's on a room page.
type watchView struct {
	Key   string
	State watchState
}

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
	if ke.UpstreamDown() {
		return "Chat is not answering. Try again shortly."
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
	Watch     *watchView // nil on a room page, whose own watch keeps the rail too
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
	Watch    watchView
	Post     json.RawMessage
	Locked   json.RawMessage
}

// entriesView backs the room's entries and the fragments that append to
// them; Removed are the posts an answer replaced, for the page to drop.
type entriesView struct {
	RoomID  string
	Entries []entryRow
	Removed []int64
	Watch   watchView
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
	tasks, err := c.deliveries(r)
	if c.deps.HandleErr(w, r, err) {
		return
	}
	rooms, err := c.listRooms(r, c.signal(me, tasks))
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

// Rail is the sidebar's list of the viewer's rooms, newest activity first, with
// the new-conversation dialog, open on ?form=new from any page. It is what the
// session last read, so a page costs no run; a session that holds nothing reads
// once, and one not yet joined shows no rooms.
func (c *Chat) Rail(r *http.Request) (template.HTML, error) {
	var cache railCache
	if b, ok := c.deps.Sessions.AppCache(r.Context(), c.Slug()); ok {
		_ = json.Unmarshal([]byte(b), &cache)
	} else {
		tasks, err := c.deliveries(r)
		if err != nil {
			return "", err
		}
		cache.Signal = c.signal(c.deps.Me(r), tasks)
		cache.Rooms, err = c.listRooms(r, cache.Signal)
		if ke, ok := client.AsKernelError(err); ok && (ke.Kind == client.KindGrantRequired || ke.UpstreamStatus() == http.StatusUnauthorized) {
			err = nil
		}
		if err != nil {
			return "", err
		}
	}
	id, onRoom := strings.CutPrefix(r.URL.Path, "/chat/room/")
	if onRoom {
		id, _, _ = strings.Cut(id, "/")
		return c.rail(r, cache.Rooms, id, nil, false)
	}
	return c.rail(r, cache.Rooms, "", &watchView{railKey, watchState{Signal: cache.Signal, Rail: railDigest(cache.Rooms)}}, false)
}

// rail renders the rail, naming the room in view; oob renders it as the
// heartbeat's answer, whose watch stands apart.
func (c *Chat) rail(r *http.Request, rooms []roomRow, current string, watch *watchView, oob bool) (template.HTML, error) {
	path := r.URL.Path
	if current != "" {
		path = "/chat/room/" + current
	}
	return c.deps.Fragment("_rail", railView{Rooms: rooms, Watch: watch, Current: current, Open: r.URL.Query().Get("form") == "new",
		Agents: c.agents(), Path: path, CSRFField: c.deps.CSRFField(r), OOB: oob})
}

// Context is a room's recent conversation as the planner reads it: the
// viewer's entries as the user's, every other member's as the assistant's,
// named; a document as its JSON text.
func (c *Chat) Context(r *http.Request, roomID string) ([]client.Message, error) {
	me := c.deps.Me(r)
	read, err := c.readRoom(r, roomID, latest, false)
	if reason := refusal(err); reason != "" {
		return nil, errors.New(reason)
	}
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
	me, id := c.deps.Me(r), r.PathValue("id")
	tasks, err := c.deliveries(r)
	if c.deps.HandleErr(w, r, err) {
		return
	}
	read, err := c.readRoom(r, id, latest, true)
	if err != nil {
		if roomGone(err) {
			err = &client.KernelError{Kind: client.KindNotFound}
		}
		c.deps.HandleErr(w, r, err)
		return
	}
	signal := c.signal(me, c.settle(r, id, tasks))
	c.remember(r, read.Rooms, signal)
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
		Entries: entriesView{RoomID: id, Entries: read.Entries, Working: read.Waiting},
		Watch:   watchView{id, watchState{After: maxSeq(read.Entries), Signal: signal, Rail: railDigest(read.Rooms)}},
		Post:    post, Locked: locked,
	})
}

// roomGone reports a refusal saying the viewer has no such room: it is gone, or
// they are no longer in it.
func roomGone(err error) bool {
	ke, ok := client.AsKernelError(err)
	return ok && (ke.UpstreamStatus() == http.StatusNotFound || ke.UpstreamStatus() == http.StatusForbidden)
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

// watch answers the heartbeat. It runs an action only when the kernel shows the
// viewer's chat moved past what the page last read, or a visible room holds a
// delivery for the viewer, which a tab read while hidden left open.
func (c *Chat) watch(r *http.Request, key, shown string) (template.HTML, error) {
	st := parseWatch(shown)
	tasks, err := c.deliveries(r)
	if err != nil {
		return "", err
	}
	me := c.deps.Me(r)
	if key == railKey {
		if signal := c.signal(me, tasks); st.due(signal) {
			return c.advanceRail(r, st, signal)
		}
		return "", nil
	}
	visible := r.Header.Get("X-Visible") != "0"
	pending := false
	for _, t := range tasks {
		pending = pending || (visible && st.Fails == 0 && held(t, me, key))
	}
	if !pending && !st.due(c.signal(me, tasks)) {
		return "", nil
	}
	return c.advance(r, key, st, tasks, visible)
}

// advanceRail brings a page that is not a room to the rooms chat now holds.
func (c *Chat) advanceRail(r *http.Request, st watchState, signal string) (template.HTML, error) {
	rooms, err := c.listRooms(r, signal)
	if err != nil {
		c.log().Warn("chat: list rooms", "err", err)
		return c.deps.Fragment("_chat_watch", watchView{railKey, st.failed()})
	}
	next := watchState{Signal: signal, Rail: railDigest(rooms)}
	out, err := c.deps.Fragment("_chat_watch", watchView{railKey, next})
	if err != nil || next.Rail == st.Rail {
		return out, err
	}
	rail, err := c.rail(r, rooms, "", nil, true)
	return out + rail, err
}

// advance brings a room page to what chat now holds: its new entries, the posts
// they replaced, whom it waits on, the rail when it changed, and the watch. A room
// seen has its deliveries settled. A read that leaves more keeps the old signal,
// so the next tick reads on. A room gone answers the signal: there is nothing more
// to show, and retrying would only write failures.
func (c *Chat) advance(r *http.Request, roomID string, st watchState, tasks []client.Task, seen bool) (template.HTML, error) {
	me := c.deps.Me(r)
	read, err := c.readRoom(r, roomID, st.After, seen)
	if err != nil {
		c.log().Warn("chat: read room", "room", roomID, "err", err)
		if roomGone(err) {
			st.Signal, st.Fails = c.signal(me, tasks), 0
		} else {
			st = st.failed()
		}
		return c.deps.Fragment("_chat_watch", watchView{roomID, st})
	}
	if seen {
		tasks = c.settle(r, roomID, tasks)
	}
	signal := c.signal(me, tasks)
	c.remember(r, read.Rooms, signal)
	next := watchState{After: max(st.After, maxSeq(read.Entries)), Signal: signal, Rail: railDigest(read.Rooms)}
	if read.More {
		next.Signal = st.Signal
	}
	out, err := c.deps.Fragment("_chat_tick", entriesView{RoomID: roomID, Entries: read.Entries, Removed: read.Removed,
		Working: read.Waiting, Watch: watchView{roomID, next}})
	if err != nil || next.Rail == st.Rail {
		return out, err
	}
	rail, err := c.rail(r, read.Rooms, roomID, nil, true)
	return out + rail, err
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
	if !c.deps.IsHTMX(r) {
		http.Redirect(w, r, "/chat/room/"+url.PathEscape(id), http.StatusSeeOther)
		return
	}
	tasks, err := c.deliveries(r)
	if c.deps.HandleErrFragment(w, r, err) {
		return
	}
	after, _ := strconv.ParseInt(r.FormValue("after"), 10, 64)
	out, err := c.advance(r, id, watchState{After: after}, tasks, true)
	if c.deps.HandleErrFragment(w, r, err) {
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(out))
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
