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
// A viewer's chats are this kernel's, which joining approves, and every chat on
// another kernel they connected to; a chat elsewhere that has addressed them and
// is neither connected nor blocked asks to be. A member added to a room is
// invited, and joins by accepting; chat's own list holds the invitations. The UI
// accepts for the installation's own agents when it adds them. A room is named
// by its address, thread@kernel, the kernel whose chat holds it; every room, at
// home or elsewhere, takes one path.
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
	"slices"
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
	roomRenameName = "room/rename"
	roomAddName    = "room/add"
	roomRemoveName = "room/remove"
	roomLeaveName  = "room/leave"
	roomJoinName   = "room/join"
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
	mux.HandleFunc("GET /chat/room/{id}/rename", d.RequireAuth(c.getRename))
	mux.HandleFunc("POST /chat/room/{id}/rename", d.RequireAuth(c.postRename))
	mux.HandleFunc("POST /chat/room/{id}/delete", d.RequireAuth(c.postDeleteRoom))
	mux.HandleFunc("POST /chat/room/{id}/leave", d.RequireAuth(c.postLeave))
	mux.HandleFunc("POST /chat/room/{id}/add-member", d.RequireAuth(c.postAddMember))
	mux.HandleFunc("POST /chat/room/{id}/remove-member", d.RequireAuth(c.postRemoveMember))
	mux.HandleFunc("POST /chat/chats/{chat}/{verb}", d.RequireAuth(c.postChat))
	mux.HandleFunc("POST /chat/invites/{room}/{verb}", d.RequireAuth(c.postInvite))
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

// join makes member a member of the chat at service, with its own session
// token, by the path any member takes, through the kernel alone: approve chat as
// a requester (so the kernel shows it chat's tasks, U52), run register, complete
// the verification task it creates, and connect the key at chat's root, which
// covers every action a member runs. The same four steps join a chat on another
// kernel, which the member's own kernel forwards. A failure at any step leaves a
// key the member already connected working: no key is ever retired.
func (c *Chat) join(ctx context.Context, token, service, member string) error {
	if err := c.deps.Client.DecideRequester(ctx, token, service, "approve"); err != nil {
		return fmt.Errorf("approve %s: %w", service, err)
	}
	args, _ := json.Marshal(map[string]string{"handle": member})
	rep, err := c.deps.Client.Run(ctx, token, service+"/"+registerName, args, "")
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
	if _, err := c.deps.Client.ConnectGrant(ctx, token, service, "", reg.APIKey); err != nil {
		return fmt.Errorf("connect %s: %w", service, err)
	}
	return nil
}

// covered reports whether the session's user holds a grant on every action of
// the chat at service a member runs, as the kernel's own plan states it per
// action: an action added since the member joined leaves the member uncovered.
func (c *Chat) covered(ctx context.Context, token, service string) (bool, error) {
	plan, err := c.deps.Client.PlanGrants(ctx, token, service)
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

// joinViewer joins the signed-in person to this kernel's chat unless the
// kernel's plan shows them covered.
func (c *Chat) joinViewer(r *http.Request) error {
	ctx := r.Context()
	return c.deps.Sessions.Do(ctx, func(tok string) error {
		if ok, err := c.covered(ctx, tok, c.self()); err != nil || ok {
			return err
		}
		return c.join(ctx, tok, c.self(), c.deps.Me(r))
	})
}

// joinAgent joins a local agent to this kernel's chat, when it is not covered,
// with the session in its credential file.
func (c *Chat) joinAgent(ctx context.Context, login string) error {
	return c.asAgent(ctx, login, func(string) error { return nil })
}

// seat joins a local agent and accepts its invitation to a room here, with its
// own session: an agent has nobody to accept, and its operator adding it is the
// consent, as it is for joining. The invitation's task is completed too, so the
// agent's daemon is not handed it as work.
func (c *Chat) seat(ctx context.Context, login, thread string) error {
	return c.asAgent(ctx, login, func(tok string) error {
		args, _ := json.Marshal(map[string]string{"thread_id": thread})
		if _, err := c.deps.Client.Run(ctx, tok, c.self()+"/"+roomJoinName, args, ""); err != nil {
			return fmt.Errorf("accept: %w", err)
		}
		tasks, err := c.deps.Client.OpenTasks(ctx, tok)
		for _, t := range invitations(tasks, login, c.self(), thread) {
			_, _ = c.deps.Client.CompleteTask(ctx, tok, t.ID, nil)
		}
		return err
	})
}

// asAgent runs then with a local agent's session, once the agent is joined.
func (c *Chat) asAgent(ctx context.Context, login string, then func(tok string) error) error {
	if !c.isLocalAgent(login) {
		return fmt.Errorf("%s has no home here", login)
	}
	return c.deps.Client.WithLogin(ctx, filepath.Join(c.root(), "client"), login, func(tok string) error {
		if ok, err := c.covered(ctx, tok, c.self()); err != nil {
			return err
		} else if !ok {
			if err := c.join(ctx, tok, c.self(), login); err != nil {
				return err
			}
		}
		return then(tok)
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

// roomRow and entryRow are chat's replies as the UI reads them. Service is the
// chat that holds the room, chat@kernel, set by the UI from the chat it read the
// room from; Elsewhere is that kernel when it is not this one, for the page to
// name.
type roomRow struct {
	ID        string   `json:"thread_id"`
	Name      string   `json:"name"`
	Creator   string   `json:"creator"`
	Members   []string `json:"members"`
	Invited   []string `json:"invited"`
	Unread    bool     `json:"unread"`
	LastAt    string   `json:"last_at"`
	Service   string   `json:"service"`
	Elsewhere string   `json:"elsewhere,omitempty"`
}

// Addr is the room's address, thread@kernel: a room is named as everything in
// Juice is, by the kernel whose chat holds it.
func (r roomRow) Addr() string { return roomAddr(r.Service, r.ID) }

// roomAddr is the address of the thread the chat at service holds.
func roomAddr(service, thread string) string { return thread + "@" + kernelOf(service) }

// kernelOf is an address's kernel, what follows its @.
func kernelOf(addr string) string {
	_, k, _ := strings.Cut(addr, "@")
	return k
}

// splitRoom reads a room's address: the chat that holds it and its thread there.
// A bare thread names no room.
func splitRoom(addr string) (service, thread string, ok bool) {
	thread, kernel, ok := strings.Cut(addr, "@")
	if !ok || thread == "" || kernel == "" || strings.ContainsAny(kernel, "@/") {
		return "", "", false
	}
	return chatHandle + "@" + kernel, thread, true
}

// chatOf is the chat holding the room a request's path names; this kernel's
// for a request that names none.
func (c *Chat) chatOf(r *http.Request) string {
	if service, _, ok := splitRoom(r.PathValue("id")); ok {
		return service
	}
	return c.self()
}

// tag marks rooms as held by the chat at service.
func (c *Chat) tag(rooms []roomRow, service string) []roomRow {
	elsewhere := ""
	if service != c.self() {
		elsewhere = kernelOf(service)
	}
	for i := range rooms {
		rooms[i].Service, rooms[i].Elsewhere = service, elsewhere
	}
	return rooms
}

// byActivity orders rooms newest activity first, across chats; rooms of one
// chat keep the order it gave them.
func byActivity(rooms []roomRow) []roomRow {
	at := func(r roomRow) time.Time {
		t, _ := time.Parse(time.RFC3339Nano, r.LastAt)
		return t
	}
	sort.SliceStable(rooms, func(i, j int) bool { return at(rooms[i]).After(at(rooms[j])) })
	return rooms
}

type entryRow struct {
	Seq    int64           `json:"seq"`
	Author string          `json:"author"`
	Body   json.RawMessage `json:"body"`
	At     string          `json:"at"`
}

// roomRead is the reply of room/read.
type roomRead struct {
	Room        roomRow     `json:"room"`
	Entries     []entryRow  `json:"entries"`
	Removed     []int64     `json:"removed"`
	Waiting     []string    `json:"waiting"`
	More        bool        `json:"more"`
	Rooms       []roomRow   `json:"rooms"`
	Invitations []inviteRow `json:"invitations"`
}

// inviteRow is an invitation to a room as chat lists it: the room, its name, and
// its creator, who invited; Service and Elsewhere are set by the UI, as a room's.
type inviteRow struct {
	ID        string `json:"thread_id"`
	Name      string `json:"name"`
	From      string `json:"from"`
	Service   string `json:"service"`
	Elsewhere string `json:"elsewhere,omitempty"`
}

// Addr is the address of the room the invitation is to.
func (i inviteRow) Addr() string { return roomAddr(i.Service, i.ID) }

// call runs one of the actions of the chat at service as the viewer and decodes
// its result into out, when there is an out.
func (c *Chat) call(r *http.Request, service, name string, args, out any) error {
	body, err := json.Marshal(args)
	if err != nil {
		return err
	}
	var rep client.CallReply
	if err := c.deps.Sessions.Do(r.Context(), func(tok string) (e error) {
		rep, e = c.deps.Client.Run(r.Context(), tok, service+"/"+name, body, "")
		return e
	}); err != nil || out == nil {
		return err
	}
	return json.Unmarshal(rep.Result, out)
}

// run is call for what the viewer does, which recovers from the two refusals
// that mean nothing ran, each once, in whichever order the kernel gives them. A
// chat whose contract moved since the kernel cached it is refused at no charge,
// and the next call re-resolves it. A viewer the kernel holds no grant for, or
// whose key chat does not know, is joined again: at home, since opening a room
// joins; elsewhere only while the viewer's own kernel still lists the grant for
// that action, which is a change of chat's contract revoking it on the kernel
// holding chat. A grant the viewer removed, as in Settings, is never restored.
func (c *Chat) run(r *http.Request, service, name string, args, out any) error {
	if service != c.self() {
		var mine []string
		if err := c.deps.Sessions.Do(r.Context(), func(tok string) (err error) {
			mine, _, err = c.chats(r, tok)
			return err
		}); err != nil {
			return err
		}
		if !slices.Contains(mine, service) {
			return errNotTheirs
		}
	}
	err := c.call(r, service, name, args, out)
	for refreshed, joined := false, false; ; {
		ke, ok := client.AsKernelError(err)
		switch {
		case ok && ke.IsRefreshable() && !refreshed:
			refreshed = true
		case ok && (ke.Kind == client.KindGrantRequired || ke.UpstreamStatus() == http.StatusUnauthorized) && !joined && (service == c.self() || c.grantHeld(r, service+"/"+name)):
			joined = true
			if jerr := c.deps.Sessions.Do(r.Context(), func(tok string) error { return c.join(r.Context(), tok, service, c.deps.Me(r)) }); jerr != nil {
				return jerr
			}
		default:
			if err == nil && name != roomReadName && name != roomListName {
				c.changed(c.deps.Me(r))
			}
			return err
		}
		err = c.call(r, service, name, args, out)
	}
}

// grantHeld reports whether the viewer's kernel lists a grant on action.
func (c *Chat) grantHeld(r *http.Request, action string) bool {
	me, err := c.deps.Viewer(r)
	for _, cn := range me.Connectors {
		for _, g := range cn.Actions {
			if g.Action == action {
				return err == nil
			}
		}
	}
	return false
}

// errNotTheirs is a run on a chat elsewhere the viewer is not connected to, or
// no longer is: none of their rooms is there.
var errNotTheirs = errors.New("not one of the viewer's chats")

// readRoom is one room for the viewer: its latest entries, or with after those
// newer than it; seen moves the viewer's read cursor over them.
func (c *Chat) readRoom(r *http.Request, service, thread string, after int64, seen bool) (roomRead, error) {
	args := map[string]any{"thread_id": thread, "seen": seen}
	if after != latest {
		args["after"] = after
	}
	var out roomRead
	if err := c.run(r, service, roomReadName, args, &out); err != nil {
		return out, err
	}
	out.Room = c.tag([]roomRow{out.Room}, service)[0]
	out.Rooms, out.Invitations = c.tag(out.Rooms, service), c.tagInvites(out.Invitations, service)
	return out, nil
}

// tagInvites marks invitations as held by the chat at service.
func (c *Chat) tagInvites(invites []inviteRow, service string) []inviteRow {
	for i, room := range c.tag(make([]roomRow, len(invites)), service) {
		invites[i].Service, invites[i].Elsewhere = room.Service, room.Elsewhere
	}
	return invites
}

// latest asks readRoom for the room's latest entries.
const latest = -1

// downRow is a chat of the viewer's that could not be read, and why.
type downRow struct {
	Service string `json:"service"`
	Reason  string `json:"reason"`
	err     error
}

// rooms is what the chats named hold for the viewer: their rooms, newest
// activity first, their invitations, and the chats that could not be read,
// which hide no other chat's.
func (c *Chat) rooms(r *http.Request, services []string) railCache {
	var rail railCache
	for _, service := range services {
		var out struct {
			Rooms       []roomRow   `json:"rooms"`
			Invitations []inviteRow `json:"invitations"`
		}
		if err := c.run(r, service, roomListName, struct{}{}, &out); err != nil {
			c.log().Warn("chat: list rooms", "chat", service, "err", err)
			rail.Down = append(rail.Down, downRow{service, c.why(err, service), err})
			continue
		}
		rail.Rooms = append(rail.Rooms, c.tag(out.Rooms, service)...)
		rail.Invites = append(rail.Invites, c.tagInvites(out.Invitations, service)...)
	}
	rail.Rooms = byActivity(rail.Rooms)
	return rail
}

// railWith is the rail after a read of one chat's room: the rooms and
// invitations that read returned, and every other chat's listed afresh.
func (c *Chat) railWith(r *http.Request, service string, read roomRead, services []string) railCache {
	var others []string
	for _, s := range services {
		if s != service {
			others = append(others, s)
		}
	}
	rail := c.rooms(r, others)
	rail.Rooms = byActivity(append(rail.Rooms, read.Rooms...))
	rail.Invites = append(rail.Invites, read.Invitations...)
	return rail
}

// railCache is what the session keeps for the rail, and what the rail shows:
// the chats asking to be connected, from the pulse; the invitations and rooms
// last read and the chats that could not be; and the signal read before that
// run, so a change since is caught by the next tick.
type railCache struct {
	Asking  []askRow    `json:"asking"`
	Invites []inviteRow `json:"invites"`
	Rooms   []roomRow   `json:"rooms"`
	Down    []downRow   `json:"down"`
	Signal  string      `json:"signal"`
}

// askRow is a chat elsewhere asking to be connected, by its address, and Name,
// its kernel as the viewer's kernel names it.
type askRow struct {
	Service string `json:"service"`
	Name    string `json:"name"`
}

// railOf is the rail after a read, rail, with the chats asking as the pulse
// holds them and the signal read before it.
func (c *Chat) railOf(r *http.Request, p pulse, rail railCache, signal string) railCache {
	shown, _ := c.cachedRail(r)
	rail.Signal = signal
	for _, service := range p.Asking {
		rail.Asking = append(rail.Asking, c.named(r, service, shown.Asking))
	}
	return rail
}

// invitations are the waiting invitation tasks among tasks addressed to me
// from the chat at service, to the room thread.
func invitations(tasks []client.Task, me, service, thread string) []client.Task {
	var out []client.Task
	for _, t := range tasks {
		if a := argsOf(t); t.Waiting() && t.RequiredCaller == me && t.Requester == service && a.Room == thread && a.From != "" {
			out = append(out, t)
		}
	}
	return out
}

// named is a chat asking as the viewer's kernel names it. A chat on a kernel
// this one has never called is addressed by its key: reading its index once is
// the kernel's way to make it known, which binds its name, and the chat is
// addressed by that name from then on, its requester included. A kernel that
// cannot be reached keeps its key, shown shortened, and is not dialled again
// while the rail shown holds it, so a page never waits on it twice.
func (c *Chat) named(r *http.Request, service string, shown []askRow) askRow {
	k := kernelOf(service)
	if len(k) != 43 || strings.Contains(k, ".") {
		return askRow{service, k}
	}
	if i := slices.IndexFunc(shown, func(a askRow) bool { return a.Service == service }); i >= 0 {
		return shown[i]
	}
	var a client.Action
	var found bool
	err := c.deps.Sessions.Do(r.Context(), func(tok string) (e error) {
		a, found, e = c.deps.Client.ResolveRef(r.Context(), tok, service+"/index")
		return e
	})
	if err != nil || !found {
		return askRow{service, k[:8] + "…"}
	}
	owner, _, _ := strings.Cut(a.Action, "/")
	return askRow{owner, kernelOf(owner)}
}

func (c *Chat) cachedRail(r *http.Request) (railCache, bool) {
	var cache railCache
	b, ok := c.deps.Sessions.AppCache(r.Context(), c.Slug())
	if ok {
		_ = json.Unmarshal([]byte(b), &cache)
	}
	return cache, ok
}

func (c *Chat) remember(r *http.Request, rail railCache) {
	b, _ := json.Marshal(rail)
	c.deps.Sessions.PutAppCache(r.Context(), c.Slug(), string(b))
}

// ---- what changed ----

// pulse is what the kernel shows of the viewer's chats, read on every tick
// without running an action, four reads that write nothing: the chats they
// joined and those asking to be (chats); their open tasks from the chats joined,
// deliveries and invitations, those addressed to them and those they own, being
// their posts and invitations still unanswered; and their open processes, since
// a post in a room elsewhere holds its process open here until the deliveries it
// made there are answered.
type pulse struct {
	Services []string
	Asking   []string
	Tasks    []client.Task
	Procs    []string
}

func (c *Chat) pulse(r *http.Request) (pulse, error) {
	ctx, me := r.Context(), c.deps.Me(r)
	var p pulse
	err := c.deps.Sessions.Do(ctx, func(tok string) error {
		p = pulse{}
		var err error
		if p.Services, p.Asking, err = c.chats(r, tok); err != nil {
			return err
		}
		tasks, err := c.deps.Client.OpenTasks(ctx, tok)
		if err != nil {
			return err
		}
		for _, t := range tasks {
			if (t.Owner == me || t.RequiredCaller == me) && argsOf(t).Room != "" && slices.Contains(p.Services, t.Requester) {
				p.Tasks = append(p.Tasks, t)
			}
		}
		procs, err := c.deps.Client.OpenProcesses(ctx, tok)
		for _, pr := range procs {
			p.Procs = append(p.Procs, pr.ID)
		}
		return err
	})
	return p, err
}

// chats is the viewer's chats, by the requesters whose handle is chat: joined,
// this kernel's first when they approved it, and every chat elsewhere they
// approved and hold a grant on, being connected; and asking, every other chat
// elsewhere that has addressed them and is not blocked. Connecting approves
// and grants, disconnecting revokes both, and a grant removed anywhere else, as
// in Settings, leaves the chat asking again, since nothing joins a chat
// elsewhere but Connect. The grants come from the request's one read of the
// viewer, which the page's balance shares.
func (c *Chat) chats(r *http.Request, tok string) (joined, asking []string, err error) {
	reqs, err := c.deps.Client.ListRequesters(r.Context(), tok)
	if err != nil {
		return nil, nil, err
	}
	me, err := c.deps.Viewer(r)
	if err != nil {
		return nil, nil, err
	}
	granted := map[string]bool{}
	for _, cn := range me.Connectors {
		for _, g := range cn.Actions {
			owner, _, _ := strings.Cut(g.Action, "/")
			granted[owner] = true
		}
	}
	for _, q := range reqs {
		switch {
		case !strings.HasPrefix(q.Requester, chatHandle+"@") || q.Status == "blocked":
		case q.Status == "approved" && (q.Requester == c.self() || granted[q.Requester]):
			joined = append(joined, q.Requester)
		case q.Requester != c.self():
			asking = append(asking, q.Requester)
		}
	}
	sort.Slice(joined, func(i, j int) bool {
		a, b := joined[i], joined[j]
		return a == c.self() || (b != c.self() && a < b)
	})
	sort.Strings(asking)
	return joined, asking, nil
}

// taskArgs is what a task of chat's carries: the room, for a delivery and an
// invitation alike; and for an invitation the room's name and who invited.
type taskArgs struct {
	Room string `json:"thread_id"`
	Name string `json:"name"`
	From string `json:"from"`
}

func argsOf(t client.Task) (a taskArgs) {
	_ = json.Unmarshal(t.PartialArgs, &a)
	return a
}

// changed counts a change this UI made for an address.
func (c *Chat) changed(me string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.changes[me]++
}

// signal names what the viewer's chats stand at: the chats, their open
// deliveries and processes, and the changes this UI made for them. The
// deliveries alone can return to an earlier set, when one arrives and another
// tab settles it between two ticks; a task never reopens, so every other change
// moves them. A chat connected, disconnected, blocked or asking moves the chats.
func (c *Chat) signal(me string, p pulse) string {
	ids := make([]string, len(p.Tasks))
	for i, t := range p.Tasks {
		ids[i] = t.ID
	}
	sort.Strings(ids)
	procs := slices.Clone(p.Procs)
	sort.Strings(procs)
	c.mu.Lock()
	n := c.changes[me]
	c.mu.Unlock()
	return digest(fmt.Sprint(n, ids, procs, p.Services, p.Asking))
}

func digest(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:6])
}

// held reports a delivery the viewer can settle in a room; an invitation is
// answered by Accept or Decline alone.
func held(t client.Task, me, service, thread string) bool {
	a := argsOf(t)
	return t.Waiting() && t.RequiredCaller == me && t.Requester == service && a.Room == thread && a.From == ""
}

// settle completes the viewer's waiting deliveries for a room just read, with
// nothing to say, since the room is what they carried, and the invitation tasks
// for it left open, the viewer being in it; and answers the tasks left open. It
// follows a read that succeeded: a completion chat cannot receive would spend the
// task and leave the delivery held there.
func (c *Chat) settle(r *http.Request, service, thread string, tasks []client.Task) []client.Task {
	me := c.deps.Me(r)
	invited := invitations(tasks, me, service, thread)
	open := tasks[:0:0]
	for _, t := range tasks {
		if !held(t, me, service, thread) && !slices.ContainsFunc(invited, func(i client.Task) bool { return i.ID == t.ID }) || c.deps.Sessions.Do(r.Context(), func(tok string) error {
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

// settled is the watch after a read that drew the rail: acknowledged, or, while
// a chat could not be read, still owed after the back-off from prev, so a chat
// that answers again is shown without waiting for another change.
func (w watchState) settled(prev watchState, down []downRow) watchState {
	if len(down) == 0 {
		return w
	}
	f := prev.failed()
	w.Signal, w.Fails, w.Retry = prev.Signal, f.Fails, f.Retry
	return w
}

// watchView is one watch input of a page: the rail's on a page that is not a
// room, the room's on a room page, keyed by the room's address.
type watchView struct {
	Key   string
	State watchState
}

// refusal is the sentence for a run the kernel or the service refused, "" for
// anything else. The kernel relays no refusal's body, only the status the
// service answered, so the sentence is the UI's, one per status; input the
// action's schema refused is the kernel's own reason. A chat on another kernel
// relays not even its status, and one whose kernel cannot be reached is not
// answering, as an endpoint down at home is. A call that left and has no answer
// yet is parked by the kernel, which retries it until it settles, so it is
// neither failed nor done.
func refusal(err error, service string) string {
	if errors.Is(err, errNotTheirs) {
		return service + " is no longer one of your chats."
	}
	ke, ok := client.AsKernelError(err)
	if !ok {
		return ""
	}
	if ke.Kind == client.KindInvalidInput {
		return ke.KernelMessage
	}
	if ke.UpstreamDown() || ke.Kind == client.KindPeerUnreachable {
		return service + " is not answering. Try again shortly."
	}
	if ke.Kind == client.KindTimeout {
		return service + " has not answered yet. It is done once it does."
	}
	if ke.Kind == client.KindGrantRequired {
		return "You disconnected this from " + service + " in Settings."
	}
	if ke.RemoteFailed() {
		return service + " refused or failed; a chat on another kernel gives no reason."
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

// why is a chat that could not be read, as a person reads it.
func (c *Chat) why(err error, service string) string {
	if reason := refusal(err, service); reason != "" {
		return reason
	}
	if ke, ok := client.AsKernelError(err); ok {
		return ke.UserMessage()
	}
	return err.Error()
}

// roomGone reports a refusal saying the viewer has no such room: it is gone,
// they are no longer in it, or its chat is no longer theirs.
func roomGone(err error) bool {
	ke, ok := client.AsKernelError(err)
	return errors.Is(err, errNotTheirs) || ok && (ke.UpstreamStatus() == http.StatusNotFound || ke.UpstreamStatus() == http.StatusForbidden)
}

// gone reports that a command or read failed because the room, or with
// invited the invitation to it, is no longer the viewer's. The chat at home says
// so by its status. A chat elsewhere relays none, and a refusal reads as a
// failure of that chat, so its list is asked once: a list that answers without
// the room among the rooms, or the invitations, confirms it gone; one that fails
// too leaves it standing, and nothing is taken for gone. A chat no longer the
// viewer's ends a room, never an invitation, which only its chat can withdraw.
func (c *Chat) gone(r *http.Request, service, thread string, err error, invited bool) bool {
	if errors.Is(err, errNotTheirs) {
		return !invited
	}
	if roomGone(err) {
		return true
	}
	ke, ok := client.AsKernelError(err)
	if !ok || !ke.RemoteFailed() {
		return false
	}
	listed := c.rooms(r, []string{service})
	if len(listed.Down) > 0 {
		return false
	}
	for _, room := range listed.Rooms {
		if room.ID == thread && !invited {
			return false
		}
	}
	for _, i := range listed.Invites {
		if i.ID == thread && invited {
			return false
		}
	}
	return true
}

// ---- the browser ----

// newRoomView backs the new-conversation dialog: the installation's agents to
// choose from, open on ?form=new, and open with the reason and what was chosen
// and typed when a start was refused.
type newRoomView struct {
	Open      bool
	Error     string
	Agents    []agentChoice
	Members   string
	Name      string
	Path      string
	CSRFField template.HTML
}

type agentChoice struct {
	Address string
	Chosen  bool
}

// railView backs the rail: the chats asking to be connected, the invitations,
// the chats that could not be read, the viewer's rooms, the room in view by its
// address, and the dialogs a page that is not a room holds. Home is this
// kernel's chat, which is never disconnected.
type railView struct {
	railCache
	Me        string
	Home      string
	New       newRoomView
	Rename    renameView
	Watch     *watchView // nil on a room page, whose own watch keeps the rail too
	Current   string
	Path      string
	CSRFField template.HTML
	OOB       bool // the heartbeat's out-of-band answer: the rail alone, replacing the one shown
}

// railItem is a row of the rail that is not a room: a chat asking to be
// connected, an invitation, a chat that could not be read. The whole row opens
// its menu. Label is its name, Muted the kernel after it, Alert marks a chat
// that could not be read, Title what the row's title and a screen reader add,
// New weights it as an unread room is.
type railItem struct {
	ID, Label, Muted, Title string
	Alert, New              bool
	Commands                []railCommand
}

// railCommand is one item of a rail row's menu: a form posting to Action, asked
// first when Confirm is set (the question, its mark added by the template),
// after a divider when Divider is.
type railCommand struct {
	Action, Label, Confirm string
	Danger, Divider        bool
	CSRFField              template.HTML
}

// Requests is the rows above the rooms: each chat asking, then each invitation.
func (v railView) Requests() []railItem {
	var out []railItem
	for _, a := range v.Asking {
		title := "" // the whole address, when the name shown is a key shortened
		if a.Name != kernelOf(a.Service) {
			title = a.Service
		}
		out = append(out, railItem{ID: "ask-" + a.Service, Label: "chat@" + a.Name, Title: title, New: true, Commands: []railCommand{
			{Action: "/chat/chats/" + a.Service + "/connect", Label: "Connect", CSRFField: v.CSRFField},
			v.chatVerb(a.Service, a.Name, "block", true)}})
	}
	for _, i := range v.Invites {
		out = append(out, railItem{ID: "invite-" + i.Addr(), Label: i.Name, Muted: i.Elsewhere, Title: i.From, New: true, Commands: []railCommand{
			{Action: "/chat/invites/" + i.Addr() + "/accept", Label: "Accept", CSRFField: v.CSRFField},
			{Action: "/chat/invites/" + i.Addr() + "/decline", Label: "Decline…", Confirm: "Decline " + i.Name, Divider: true, CSRFField: v.CSRFField}}})
	}
	return out
}

// Unread is the rows under the rooms: each chat that could not be read, its
// reason its title; one elsewhere can be disconnected or blocked.
func (v railView) Unread() []railItem {
	var out []railItem
	for _, d := range v.Down {
		item := railItem{ID: "down-" + d.Service, Label: d.Service, Title: d.Reason, Alert: true}
		if d.Service != v.Home {
			k := kernelOf(d.Service)
			item.Commands = []railCommand{v.chatVerb(d.Service, k, "disconnect", false), v.chatVerb(d.Service, k, "block", false)}
		}
		out = append(out, item)
	}
	return out
}

// Drop is a room's command ending the viewer's part in it: Delete for its
// creator, Leave for any other member, each asked first, naming the room.
func (v railView) Drop(room roomRow) railCommand {
	if room.Creator == v.Me {
		return railCommand{Action: "/chat/room/" + room.Addr() + "/delete", Label: "Delete…", Confirm: "Delete " + room.Name + " for everyone", Danger: true, Divider: true, CSRFField: v.CSRFField}
	}
	return railCommand{Action: "/chat/room/" + room.Addr() + "/leave", Label: "Leave…", Confirm: "Leave " + room.Name, Divider: true, CSRFField: v.CSRFField}
}

// Elsewhere is the commands a room held elsewhere adds to its menu: its chat
// disconnected or blocked.
func (v railView) Elsewhere(room roomRow) []railCommand {
	return []railCommand{v.chatVerb(room.Service, room.Elsewhere, "disconnect", true), v.chatVerb(room.Service, room.Elsewhere, "block", false)}
}

// chatVerb is the menu's command disconnecting or blocking the chat at service,
// named for the viewer by its kernel's name, each asked first.
func (v railView) chatVerb(service, name, verb string, divider bool) railCommand {
	label := map[string]string{"disconnect": "Disconnect", "block": "Block"}[verb]
	return railCommand{Action: "/chat/chats/" + service + "/" + verb, Label: label + " " + name + "…", Confirm: label + " " + name,
		Danger: verb == "block", Divider: divider, CSRFField: v.CSRFField}
}

// membersView backs the members dialog: open when Open, and open with the reason
// and the address typed when a change was refused.
type membersView struct {
	Room      roomRow
	Me        string
	Open      bool
	Error     string
	Typed     string
	CSRFField template.HTML
}

// renameView backs the rename dialog: closed and empty with no Room, else open
// on it, with the reason a rename was refused.
type renameView struct {
	Room      *roomRow
	Error     string
	CSRFField template.HTML
}

// roomView backs the room page. The composer is the form of Post, the post
// action's input schema as the kernel serves it, with Locked, the room, sealed
// by the page's address.
type roomView struct {
	Room    roomRow
	Me      string
	Others  []string // the members other than the viewer: whom [@] and a leading address name
	Members membersView
	Rename  renameView
	Entries entriesView
	Watch   watchView
	Post    json.RawMessage
	Locked  json.RawMessage
}

// entriesView backs the room's entries and the fragments that append to
// them; Removed are the posts an answer replaced, for the page to drop. Error
// is the line beside the composer: why a post was refused, or whom one posted
// did not reach.
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
	if len(v.Working) == 0 {
		return ""
	}
	return "Waiting on " + andList(v.Working)
}

// andList joins names as a sentence does: "a", "a and b", "a, b and c".
func andList(names []string) string {
	if n := len(names); n > 1 {
		return strings.Join(names[:n-1], ", ") + " and " + names[n-1]
	}
	return strings.Join(names, "")
}

// undelivered is the line saying whom a post, or an invitation, did not reach
// yet, "" when it reached everyone it was for.
func undelivered(members []string) string {
	if len(members) == 0 {
		return ""
	}
	return "Not delivered to " + andList(members) + "."
}

// getLanding is where a session lands: the viewer's conversation with the
// assistant, the room of exactly those two, started on the first visit; else the
// newest room; else nothing yet beside the rail. The viewer and the assistant are
// joined here when they are not yet.
func (c *Chat) getLanding(w http.ResponseWriter, r *http.Request) {
	me := c.deps.Me(r)
	if err := c.joinViewer(r); err != nil {
		c.deps.RenderPage(w, r, http.StatusOK, "landing", "Conversations", landingView{Error: joinFailed(me, err)})
		return
	}
	p, err := c.pulse(r)
	if c.deps.HandleErr(w, r, err) {
		return
	}
	listed := c.rooms(r, p.Services)
	for _, d := range listed.Down {
		if d.Service == c.self() && c.deps.HandleErr(w, r, d.err) { // its rooms unknown, the assistant's room cannot be found
			return
		}
	}
	c.remember(r, c.railOf(r, p, listed, c.signal(me, p)))
	assistant := c.deps.Address(assistantHandle)
	if me != assistant && c.isLocalAgent(assistant) {
		room, seated, err := c.assistantRoom(r, listed.Rooms, assistant)
		if c.deps.HandleErr(w, r, err) {
			return
		}
		to := "/chat/room/" + url.PathEscape(room)
		join := func() error { return c.joinAgent(r.Context(), assistant) }
		if !seated {
			_, thread, _ := splitRoom(room)
			join = func() error { return c.seat(r.Context(), assistant, thread) }
		}
		if err := join(); err != nil {
			c.deps.FlashRedirect(w, r, to, "error", joinFailed(assistant, err))
			return
		}
		http.Redirect(w, r, to, http.StatusSeeOther)
		return
	}
	if len(listed.Rooms) > 0 {
		http.Redirect(w, r, "/chat/room/"+url.PathEscape(listed.Rooms[0].Addr()), http.StatusSeeOther)
		return
	}
	c.deps.RenderPage(w, r, http.StatusOK, "landing", "Conversations", landingView{})
}

// assistantRoom is the address of the room of exactly the viewer and the
// assistant at home, started when there is none, and whether the assistant has
// joined it: one that has not yet accepted is seated next.
func (c *Chat) assistantRoom(r *http.Request, rooms []roomRow, assistant string) (string, bool, error) {
	for _, room := range rooms {
		if all := append(slices.Clone(room.Members), room.Invited...); room.Service == c.self() && len(all) == 2 && slices.Contains(all, assistant) {
			return room.Addr(), slices.Contains(room.Members, assistant), nil
		}
	}
	var out struct {
		Room roomRow `json:"room"`
	}
	err := c.run(r, c.self(), roomCreateName, map[string]any{"members": []string{assistant}, "name": assistantHandle}, &out)
	return roomAddr(c.self(), out.Room.ID), false, err
}

// landingView backs the page a viewer with no conversation lands on.
type landingView struct{ Error string }

// Rail is the sidebar's list of the viewer's rooms, newest activity first, with
// the new-conversation dialog, open on ?form=new from any page. It is what the
// session last read, so a page costs no run; a session that holds nothing reads
// once, and one not yet joined shows no rooms.
func (c *Chat) Rail(r *http.Request) (template.HTML, error) {
	cache, ok := c.cachedRail(r)
	if !ok {
		p, err := c.pulse(r)
		if err != nil {
			return "", err
		}
		cache = c.railOf(r, p, c.rooms(r, p.Services), c.signal(c.deps.Me(r), p))
		c.remember(r, cache)
	}
	room, onRoom := strings.CutPrefix(r.URL.Path, "/chat/room/")
	if onRoom {
		room, _, _ = strings.Cut(room, "/")
		return c.rail(r, cache, room, nil, false)
	}
	st := watchState{Signal: cache.Signal, Rail: railDigest(cache)}.settled(watchState{Signal: cache.Signal}, cache.Down)
	return c.rail(r, cache, "", &watchView{railKey, st}, false)
}

// rail renders the rail, naming the room in view by its address; oob renders it
// as the heartbeat's answer, whose watch stands apart.
func (c *Chat) rail(r *http.Request, cache railCache, current string, watch *watchView, oob bool) (template.HTML, error) {
	path := r.URL.Path
	if current != "" {
		path = "/chat/room/" + current
	}
	csrf := c.deps.CSRFField(r)
	return c.deps.Fragment("_rail", railView{railCache: cache, Me: c.deps.Me(r), Home: c.self(), Rename: renameView{CSRFField: csrf}, Watch: watch, Current: current,
		New: newRoomView{Open: r.URL.Query().Get("form") == "new", Agents: c.agentChoices(nil), Path: path, CSRFField: csrf}, Path: path, CSRFField: csrf, OOB: oob})
}

// Context is a room's recent conversation as the planner reads it: the
// viewer's entries as the user's, every other member's as the assistant's,
// named; a document as its JSON text.
func (c *Chat) Context(r *http.Request, room string) ([]client.Message, error) {
	me := c.deps.Me(r)
	service, thread, ok := splitRoom(room)
	if !ok {
		return nil, errors.New("This room no longer exists.")
	}
	read, err := c.readRoom(r, service, thread, latest, false)
	if reason := refusal(err, service); reason != "" {
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
	me, room := c.deps.Me(r), r.PathValue("id")
	service, thread, ok := splitRoom(room)
	if !ok {
		c.deps.HandleErr(w, r, &client.KernelError{Kind: client.KindNotFound})
		return
	}
	p, err := c.pulse(r)
	if c.deps.HandleErr(w, r, err) {
		return
	}
	read, err := c.readRoom(r, service, thread, latest, true)
	if err != nil {
		if c.gone(r, service, thread, err, false) {
			err = &client.KernelError{Kind: client.KindNotFound}
		}
		c.deps.HandleErr(w, r, err)
		return
	}
	p.Tasks = c.settle(r, service, thread, p.Tasks)
	signal := c.signal(me, p)
	rail := c.railOf(r, p, c.railWith(r, service, read, p.Services), signal)
	c.remember(r, rail)
	post, err := c.postSchema(r, service)
	if c.deps.HandleErr(w, r, err) {
		return
	}
	locked, _ := json.Marshal(map[string]string{"thread_id": thread})
	rename := renameView{CSRFField: c.deps.CSRFField(r)}
	if r.URL.Query().Get("form") == "rename" && read.Room.Creator == me {
		rename.Room = &read.Room
	}
	var others []string
	for _, m := range read.Room.Members {
		if m != me {
			others = append(others, m)
		}
	}
	st := watchState{After: maxSeq(read.Entries), Signal: signal, Rail: railDigest(rail)}.settled(watchState{After: maxSeq(read.Entries), Signal: signal}, rail.Down)
	c.deps.RenderPage(w, r, http.StatusOK, "room", "Chat", roomView{
		Room: read.Room, Me: me, Others: others, Rename: rename,
		Members: membersView{Room: read.Room, Me: me, Open: r.URL.Query().Get("form") == "members", CSRFField: c.deps.CSRFField(r)},
		Entries: entriesView{RoomID: room, Entries: read.Entries, Working: read.Waiting},
		Watch:   watchView{room, st},
		Post:    post, Locked: locked,
	})
}

// postSchema is the post action's input schema, the composer's form, as the
// kernel serves it for the chat at service.
func (c *Chat) postSchema(r *http.Request, service string) (json.RawMessage, error) {
	var a client.Action
	err := c.deps.Sessions.Do(r.Context(), func(tok string) error {
		var e error
		var found bool
		a, found, e = c.deps.Client.ResolveRef(r.Context(), tok, service+"/"+postName)
		if e == nil && !found {
			e = &client.KernelError{Kind: client.KindNotFound}
		}
		return e
	})
	return a.InputSchema, err
}

// watch answers the heartbeat. It runs an action only when the kernel shows the
// viewer's chats moved past what the page last read, or a visible room holds a
// delivery for the viewer, which a tab read while hidden left open.
func (c *Chat) watch(r *http.Request, key, shown string) (template.HTML, error) {
	st := parseWatch(shown)
	p, err := c.pulse(r)
	if err != nil {
		return "", err
	}
	me := c.deps.Me(r)
	if key == railKey {
		if signal := c.signal(me, p); st.due(signal) {
			return c.advanceRail(r, st, p, signal)
		}
		return "", nil
	}
	service, thread, ok := splitRoom(key)
	if !ok {
		return "", nil
	}
	visible := r.Header.Get("X-Visible") != "0"
	pending := false
	for _, t := range p.Tasks {
		pending = pending || (visible && st.Fails == 0 && held(t, me, service, thread))
	}
	if !pending && !st.due(c.signal(me, p)) {
		return "", nil
	}
	return c.advance(r, key, st, p, visible, "")
}

// advanceRail brings a page that is not a room to the rooms its chats now hold.
func (c *Chat) advanceRail(r *http.Request, st watchState, p pulse, signal string) (template.HTML, error) {
	rail := c.railOf(r, p, c.rooms(r, p.Services), signal)
	c.remember(r, rail)
	next := watchState{Signal: signal, Rail: railDigest(rail)}.settled(st, rail.Down)
	out, err := c.deps.Fragment("_chat_watch", watchView{railKey, next})
	if err != nil || next.Rail == st.Rail {
		return out, err
	}
	h, err := c.rail(r, rail, "", nil, true)
	return out + h, err
}

// advance brings a room page to what its chat now holds: its new entries, the
// posts they replaced, whom it waits on, the rail when it changed, and the watch.
// A room seen has its deliveries settled. A read that leaves more keeps the old
// signal, so the next tick reads on. A room gone sends the page to the landing:
// there is nothing more to show, and a delivery of it still open would read it
// again on every tick. note is the line for beside the composer, "" for none.
func (c *Chat) advance(r *http.Request, room string, st watchState, p pulse, seen bool, note string) (template.HTML, error) {
	me := c.deps.Me(r)
	service, thread, _ := splitRoom(room)
	read, err := c.readRoom(r, service, thread, st.After, seen)
	if err != nil {
		c.log().Warn("chat: read room", "room", room, "err", err)
		if c.gone(r, service, thread, err, false) {
			return "", apps.Moved("/chat")
		}
		return c.deps.Fragment("_chat_watch", watchView{room, st.failed()})
	}
	if seen {
		p.Tasks = c.settle(r, service, thread, p.Tasks)
	}
	signal := c.signal(me, p)
	rail := c.railOf(r, p, c.railWith(r, service, read, p.Services), signal)
	c.remember(r, rail)
	next := watchState{After: max(st.After, maxSeq(read.Entries)), Signal: signal, Rail: railDigest(rail)}
	if read.More {
		next.Signal = st.Signal
	}
	next = next.settled(st, rail.Down)
	out, err := c.deps.Fragment("_chat_tick", entriesView{RoomID: room, Entries: read.Entries, Removed: read.Removed,
		Working: read.Waiting, Watch: watchView{room, next}, Error: note})
	if err != nil || next.Rail == st.Rail {
		return out, err
	}
	h, err := c.rail(r, rail, room, nil, true)
	return out + h, err
}

// railKey is the rail's watch: a room's key is its address, never this one.
const railKey = "rail"

// railDigest names the rail a page shows: the chats asking, the invitations,
// its rooms, their names and which are unread, and the chats that could not be
// read, so the heartbeat swaps the rail only when one of them changed.
func railDigest(rail railCache) string {
	h := sha256.New()
	for _, a := range rail.Asking {
		fmt.Fprintf(h, "%s\x00%s\x02", a.Service, a.Name)
	}
	for _, i := range rail.Invites {
		fmt.Fprintf(h, "%s\x00%s\x03", i.Addr(), i.Name)
	}
	for _, r := range rail.Rooms {
		fmt.Fprintf(h, "%s\x00%s\x00%t\x00", r.Addr(), r.Name, r.Unread)
	}
	for _, d := range rail.Down {
		fmt.Fprintf(h, "%s\x00%s\x01", d.Service, d.Reason)
	}
	return hex.EncodeToString(h.Sum(nil)[:6])
}

// postMessage runs the post action of the room's chat as the viewer with the
// composer's fields: what the form carries, and the room from the path, which
// the form shows sealed and never sends. The browser and an agent take one path.
// A post that did not reach every member it was for says whom it missed.
func (c *Chat) postMessage(w http.ResponseWriter, r *http.Request) {
	room := r.PathValue("id")
	service, thread, ok := splitRoom(room)
	if !ok {
		c.composeFailed(w, r, room, "This room no longer exists.")
		return
	}
	args, err := c.deps.FormArgs(r)
	if err != nil {
		c.composeFailed(w, r, room, err.Error())
		return
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(args, &fields) != nil || fields == nil {
		c.composeFailed(w, r, room, "The JSON must be an object.")
		return
	}
	fields["thread_id"], _ = json.Marshal(thread)
	var posted struct {
		Undelivered []string `json:"undelivered"`
	}
	if err := c.run(r, service, postName, fields, &posted); err != nil {
		if reason := refusal(err, service); reason != "" {
			c.composeFailed(w, r, room, reason)
			return
		}
		if ke, ok := client.AsKernelError(err); ok && c.deps.IsHTMX(r) && ke.Kind != client.KindUnauthenticated {
			c.composeFailed(w, r, room, ke.UserMessage())
			return
		}
		if c.deps.HandleErr(w, r, err) {
			return
		}
	}
	note := undelivered(posted.Undelivered)
	if !c.deps.IsHTMX(r) {
		if note != "" {
			c.deps.FlashRedirect(w, r, "/chat/room/"+url.PathEscape(room), "error", note)
			return
		}
		http.Redirect(w, r, "/chat/room/"+url.PathEscape(room), http.StatusSeeOther)
		return
	}
	p, err := c.pulse(r)
	if c.deps.HandleErrFragment(w, r, err) {
		return
	}
	after, _ := strconv.ParseInt(r.FormValue("after"), 10, 64)
	out, err := c.advance(r, room, watchState{After: after}, p, true, note)
	if c.deps.HandleErrFragment(w, r, err) {
		return
	}
	writeHTML(w, out)
}

func writeHTML(w http.ResponseWriter, h template.HTML) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(h))
}

// composeFailed shows why a post was refused, beside the composer. An htmx
// refusal is 422, so the composer keeps the draft (it resets on success only).
func (c *Chat) composeFailed(w http.ResponseWriter, r *http.Request, room, reason string) {
	if c.deps.IsHTMX(r) {
		_ = c.deps.RenderFragment(w, http.StatusUnprocessableEntity, "_chat_refused", entriesView{RoomID: room, Error: reason})
		return
	}
	c.deps.FlashRedirect(w, r, "/chat/room/"+url.PathEscape(room), "error", reason)
}

// postCreateRoom starts a conversation with the participants chosen: the
// agents ticked and the addresses typed, the viewer included, in this kernel's
// chat. A name left blank is the participants' handles, which the service
// composes. The local agents among them are joined.
func (c *Chat) postCreateRoom(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	members := append(r.Form["agent"], strings.Fields(strings.ReplaceAll(r.FormValue("members"), ",", " "))...)
	if len(members) == 0 {
		c.newRoomRefused(w, r, "Choose who the conversation is with.")
		return
	}
	for _, m := range members {
		if !strings.Contains(m, "@") {
			c.newRoomRefused(w, r, m+" is not an address (handle@kernel).")
			return
		}
	}
	args := map[string]any{"members": members}
	if name := strings.TrimSpace(r.FormValue("name")); name != "" {
		args["name"] = name
	}
	var out struct {
		Room      roomRow  `json:"room"`
		Uninvited []string `json:"uninvited"`
	}
	err := c.run(r, c.self(), roomCreateName, args, &out)
	if reason := refusal(err, c.self()); reason != "" {
		c.newRoomRefused(w, r, reason)
		return
	}
	if err != nil {
		c.then(w, r, err, "/chat")
		return
	}
	c.joinMembers(w, r, members, out.Room.ID, undelivered(out.Uninvited))
}

// newRoomRefused says why a conversation could not start: in the dialog, with what
// was chosen and typed, or without script as a flash on the landing.
func (c *Chat) newRoomRefused(w http.ResponseWriter, r *http.Request, reason string) {
	if !c.deps.IsHTMX(r) {
		c.deps.FlashRedirect(w, r, "/chat?form=new", "error", reason)
		return
	}
	_ = c.deps.RenderFragment(w, http.StatusOK, "_new_room", newRoomView{Open: true, Error: reason, Agents: c.agentChoices(r.Form["agent"]),
		Members: r.FormValue("members"), Name: r.FormValue("name"), Path: "/chat", CSRFField: c.deps.CSRFField(r)})
}

// agentChoices is the installation's agents, those chosen ticked.
func (c *Chat) agentChoices(chosen []string) []agentChoice {
	agents := c.agents()
	out := make([]agentChoice, len(agents))
	for i, a := range agents {
		out[i] = agentChoice{a, slices.Contains(chosen, a)}
	}
	return out
}

// getRename answers the rename dialog, open on a room the viewer made, from the
// rooms the rail already shows: opening it runs nothing.
func (c *Chat) getRename(w http.ResponseWriter, r *http.Request) {
	room := c.shownRoom(r)
	if room.Creator != c.deps.Me(r) {
		http.NotFound(w, r)
		return
	}
	_ = c.deps.RenderFragment(w, http.StatusOK, "_rename", renameView{Room: &room, CSRFField: c.deps.CSRFField(r)})
}

// shownRoom is the room of the request's path as the rail last read it, which
// costs no run; the zero room but for its address when the rail does not hold it.
func (c *Chat) shownRoom(r *http.Request) roomRow {
	addr := r.PathValue("id")
	cache, _ := c.cachedRail(r)
	for _, room := range cache.Rooms {
		if room.Addr() == addr {
			return room
		}
	}
	service, thread, _ := splitRoom(addr)
	return c.tag([]roomRow{{ID: thread}}, service)[0]
}

// pageWatch is the chat watch a command's form carried: the room in view's, or
// the rail's on any other page.
func pageWatch(r *http.Request) (string, watchState) {
	for _, v := range []string{r.FormValue("watch"), r.FormValue("slow")} {
		if rest, ok := strings.CutPrefix(v, "chat:"); ok {
			key, shown, _ := strings.Cut(rest, ":")
			return key, parseWatch(shown)
		}
	}
	return railKey, watchState{}
}

// refresh reads after a command, whatever the watch says is due, since the
// command may have moved nothing the signal counts and a watch that failed waits
// for its retry; and answers as a tick does. A read that fails answers the watch
// failed, and the heartbeat retries.
func (c *Chat) refresh(r *http.Request, key string, st watchState) (template.HTML, error) {
	p, err := c.pulse(r)
	if err != nil {
		c.log().Warn("chat: open tasks", "err", err)
		return c.deps.Fragment("_chat_watch", watchView{key, st.failed()})
	}
	if key == railKey {
		return c.advanceRail(r, st, p, c.signal(c.deps.Me(r), p))
	}
	return c.advance(r, key, st, p, true, "")
}

// postRename runs the rename action of the room's chat as the viewer; the
// service holds that only the room's creator may. With script the page answers
// in place: the dialog closes, or comes back open with the reason, and the rail
// follows at once.
func (c *Chat) postRename(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.FormValue("name"))
	if c.deps.IsHTMX(r) {
		c.renameInPlace(w, r, name)
		return
	}
	if name == "" {
		c.deps.FlashRedirect(w, r, roomURL(r), "error", "A room needs a name.")
		return
	}
	service, thread, _ := splitRoom(r.PathValue("id"))
	c.then(w, r, c.run(r, service, roomRenameName, map[string]string{"thread_id": thread, "name": name}, nil), roomURL(r))
}

func (c *Chat) renameInPlace(w http.ResponseWriter, r *http.Request, name string) {
	room := r.PathValue("id")
	service, thread, _ := splitRoom(room)
	refused := func(reason string) {
		shown := c.tag([]roomRow{{ID: thread, Name: name}}, service)[0]
		_ = c.deps.RenderFragment(w, http.StatusOK, "_rename", renameView{Room: &shown, Error: reason, CSRFField: c.deps.CSRFField(r)})
	}
	if name == "" {
		refused("A room needs a name.")
		return
	}
	var out struct {
		Room roomRow `json:"room"`
	}
	err := c.run(r, service, roomRenameName, map[string]string{"thread_id": thread, "name": name}, &out)
	if reason := refusal(err, service); reason != "" {
		refused(reason)
		return
	}
	if err != nil {
		c.then(w, r, err, roomURL(r))
		return
	}
	key, st := pageWatch(r)
	closed, err := c.deps.Fragment("_rename", renameView{CSRFField: c.deps.CSRFField(r)})
	if c.deps.HandleErrFragment(w, r, err) {
		return
	}
	if key == room {
		h, err := c.deps.Fragment("_crumb", c.tag([]roomRow{out.Room}, service)[0])
		if c.deps.HandleErrFragment(w, r, err) {
			return
		}
		closed += h
	}
	h, err := c.refresh(r, key, st)
	if c.deps.HandleErrFragment(w, r, err) {
		return
	}
	writeHTML(w, closed+h)
}

func (c *Chat) postDeleteRoom(w http.ResponseWriter, r *http.Request) { c.drop(w, r, roomDeleteName) }

func (c *Chat) postLeave(w http.ResponseWriter, r *http.Request) { c.drop(w, r, roomLeaveName) }

// drop deletes or leaves a room. With script the rail answers in place, unless
// the page shows the room, which is then gone from under it; a room already gone
// is done.
func (c *Chat) drop(w http.ResponseWriter, r *http.Request, action string) {
	room := r.PathValue("id")
	service, thread, _ := splitRoom(room)
	err := c.run(r, service, action, map[string]string{"thread_id": thread}, nil)
	if !c.deps.IsHTMX(r) || (err != nil && !c.gone(r, service, thread, err, false)) {
		c.then(w, r, err, "/chat")
		return
	}
	key, st := pageWatch(r)
	if key == room {
		c.deps.Redirect(w, r, "/chat")
		return
	}
	c.answer(w, r, key, st)
}

// postAddMember and postRemoveMember run the actions of the room's chat as the
// viewer. Done, the page reloads with the dialog open, since the composer names
// the members too, and says whom the invitation did not reach yet; refused, the
// dialog comes back open with the reason. A local agent added to a room at home
// is seated: joined to this kernel's chat and its invitation accepted.
func (c *Chat) postAddMember(w http.ResponseWriter, r *http.Request) {
	service, thread, _ := splitRoom(r.PathValue("id"))
	m := strings.TrimSpace(r.FormValue("member"))
	if !strings.Contains(m, "@") {
		c.membersRefused(w, r, "A member is an address, handle@kernel.", m)
		return
	}
	var out struct {
		Uninvited []string `json:"uninvited"`
	}
	err := c.run(r, service, roomAddName, map[string]string{"thread_id": thread, "member": m}, &out)
	if reason := refusal(err, service); reason != "" {
		c.membersRefused(w, r, reason, m)
		return
	}
	if err != nil {
		c.then(w, r, err, roomURL(r))
		return
	}
	if service == c.self() && c.isLocalAgent(m) {
		if err := c.seat(r.Context(), m, thread); err != nil {
			c.deps.FlashRedirect(w, r, roomURL(r), "error", joinFailed(m, err))
			return
		}
	}
	if note := undelivered(out.Uninvited); note != "" {
		c.deps.FlashRedirect(w, r, roomURL(r)+"?form=members", "info", note)
		return
	}
	c.deps.Redirect(w, r, roomURL(r)+"?form=members")
}

func (c *Chat) postRemoveMember(w http.ResponseWriter, r *http.Request) {
	service, thread, _ := splitRoom(r.PathValue("id"))
	args := map[string]string{"thread_id": thread, "member": strings.TrimSpace(r.FormValue("member"))}
	err := c.run(r, service, roomRemoveName, args, nil)
	if reason := refusal(err, service); reason != "" {
		c.membersRefused(w, r, reason, "")
		return
	}
	if err != nil {
		c.then(w, r, err, roomURL(r))
		return
	}
	c.deps.Redirect(w, r, roomURL(r)+"?form=members")
}

// membersRefused says why a change of members was refused: in the dialog, or
// without script as a flash on the room.
func (c *Chat) membersRefused(w http.ResponseWriter, r *http.Request, reason, typed string) {
	if !c.deps.IsHTMX(r) {
		c.deps.FlashRedirect(w, r, roomURL(r), "error", reason)
		return
	}
	_ = c.deps.RenderFragment(w, http.StatusOK, "_members", membersView{Room: c.shownRoom(r), Me: c.deps.Me(r), Open: true, Error: reason, Typed: typed, CSRFField: c.deps.CSRFField(r)})
}

// joinMembers seats the local agents among members in the room just started at
// home, and opens it, naming the first that could not be seated, else whom the
// invitation did not reach yet.
func (c *Chat) joinMembers(w http.ResponseWriter, r *http.Request, members []string, thread, note string) {
	to := "/chat/room/" + url.PathEscape(roomAddr(c.self(), thread))
	for _, m := range members {
		if !c.isLocalAgent(m) {
			continue
		}
		if err := c.seat(r.Context(), m, thread); err != nil {
			c.deps.FlashRedirect(w, r, to, "error", joinFailed(m, err))
			return
		}
	}
	if note != "" {
		c.deps.FlashRedirect(w, r, to, "info", note)
		return
	}
	c.deps.Redirect(w, r, to)
}

// ---- the viewer's chats and invitations ----

// postChat connects, disconnects or blocks a chat elsewhere, one that has
// addressed the viewer. Connect is join: approve, register, verify, connect; one
// that fails leaves a chat that was asking as it was. Disconnect deletes the
// grant and returns the chat to asking; block deletes the grant and refuses the
// chat. A grant held on a kernel that does not answer, or answers late, is gone
// here at once and the disconnect goes on, and the page says that kernel still
// holds it, as Settings does. A failure is said
// on the page it was sent from; a page showing a room of a chat disconnected
// leaves for the landing.
func (c *Chat) postChat(w http.ResponseWriter, r *http.Request) {
	ctx, me := r.Context(), c.deps.Me(r)
	service, verb := r.PathValue("chat"), r.PathValue("verb")
	unanswered, noChat := false, errors.New("no such chat")
	err := c.deps.Sessions.Do(ctx, func(tok string) error {
		reqs, err := c.deps.Client.ListRequesters(ctx, tok)
		if err != nil {
			return err
		}
		i := slices.IndexFunc(reqs, func(q client.Requester) bool { return q.Requester == service })
		if service == c.self() || !strings.HasPrefix(service, chatHandle+"@") || i < 0 {
			return noChat
		}
		ungrant := func() error {
			err := c.deps.Client.DeleteGrant(ctx, tok, service)
			ke, ok := client.AsKernelError(err)
			unanswered = ok && (ke.Kind == client.KindPeerUnreachable || ke.Kind == client.KindTimeout)
			if unanswered || ok && ke.Kind == client.KindNotFound {
				return nil
			}
			return err
		}
		switch verb {
		case "connect":
			err := c.join(ctx, tok, service, me)
			if err != nil && reqs[i].Status == "pending" {
				_ = c.deps.Client.DecideRequester(ctx, tok, service, "revoke")
			}
			return err
		case "disconnect", "block":
			if err := ungrant(); err != nil {
				return err
			}
			return c.deps.Client.DecideRequester(ctx, tok, service, map[string]string{"disconnect": "revoke", "block": "block"}[verb])
		}
		return noChat
	})
	if errors.Is(err, noChat) {
		c.deps.HandleErr(w, r, client.ErrNotFound)
		return
	}
	if err != nil {
		c.failed(w, r, service, err)
		return
	}
	c.changed(me)
	key, st := pageWatch(r)
	inView, _, _ := splitRoom(key)
	to := back(r)
	if verb != "connect" && inView == service {
		to = "/chat"
	}
	if unanswered {
		c.deps.FlashRedirect(w, r, to, "info", "Disconnected "+service+" here. Its kernel did not answer; disconnect again once it does.")
		return
	}
	if !c.deps.IsHTMX(r) || to != back(r) {
		c.deps.Redirect(w, r, to)
		return
	}
	c.answer(w, r, key, st)
}

// postInvite accepts or declines an invitation to a room, as chat's own list
// holds it: room/join or room/leave, run as the viewer, and then the viewer's
// invitation tasks for the room are completed or declined, which only clears
// them. A failure destroys nothing: the invitation counts as withdrawn only when
// its chat says so, at home by its status, elsewhere by a list that answers
// without it; anything else is said on the page it was sent from, and the
// invitation stands.
func (c *Chat) postInvite(w http.ResponseWriter, r *http.Request) {
	room, verb := r.PathValue("room"), r.PathValue("verb")
	service, thread, ok := splitRoom(room)
	action := map[string]string{"accept": roomJoinName, "decline": roomLeaveName}[verb]
	if !ok || action == "" {
		c.deps.HandleErr(w, r, client.ErrNotFound)
		return
	}
	err := c.run(r, service, action, map[string]string{"thread_id": thread}, nil)
	withdrawn := err != nil && c.gone(r, service, thread, err, true)
	if err != nil && !withdrawn {
		c.failed(w, r, service, err)
		return
	}
	c.clearInvites(r, service, thread, verb == "accept" && !withdrawn)
	switch {
	case withdrawn:
		c.deps.FlashRedirect(w, r, back(r), "error", "That invitation was withdrawn.")
	case verb == "accept":
		c.deps.Redirect(w, r, "/chat/room/"+url.PathEscape(room))
	case !c.deps.IsHTMX(r):
		c.deps.Redirect(w, r, back(r))
	default:
		key, st := pageWatch(r)
		c.answer(w, r, key, st)
	}
}

// clearInvites completes, after a join, or declines the viewer's invitation
// tasks for a room, so none is left waiting in their inbox. One that cannot be
// is settled at the room's next read, as a delivery is, or answered there.
func (c *Chat) clearInvites(r *http.Request, service, thread string, joined bool) {
	ctx, me := r.Context(), c.deps.Me(r)
	_ = c.deps.Sessions.Do(ctx, func(tok string) error {
		tasks, err := c.deps.Client.OpenTasks(ctx, tok)
		for _, t := range invitations(tasks, me, service, thread) {
			if joined {
				_, _ = c.deps.Client.CompleteTask(ctx, tok, t.ID, nil)
			} else {
				_, _ = c.deps.Client.CancelTask(ctx, tok, t.ID)
			}
		}
		return err
	})
	c.changed(me)
}

// failed says why a command on the chat at service was refused, on the page it
// was sent from, where the gesture was made.
func (c *Chat) failed(w http.ResponseWriter, r *http.Request, service string, err error) {
	reason := refusal(err, service)
	var ke *client.KernelError
	if reason == "" && errors.As(err, &ke) {
		if reason = refusal(ke, service); reason == "" {
			reason = ke.UserMessage()
		}
	}
	if reason == "" {
		reason = err.Error()
	}
	c.deps.FlashRedirect(w, r, back(r), "error", reason)
}

// back is the page a command was sent from, as htmx names it, by its path
// alone; the landing without script.
func back(r *http.Request) string {
	if u, err := url.Parse(r.Header.Get("HX-Current-URL")); err == nil && strings.HasPrefix(u.Path, "/") && !strings.HasPrefix(u.Path, "//") {
		return u.Path
	}
	return "/chat"
}

// answer is a command's reply in place: the page brought to what its chats now
// hold, as a tick does.
func (c *Chat) answer(w http.ResponseWriter, r *http.Request, key string, st watchState) {
	h, err := c.refresh(r, key, st)
	if c.deps.HandleErrFragment(w, r, err) {
		return
	}
	writeHTML(w, h)
}

// then redirects after a command: a room gone is the not-found page, any other
// refusal a flash on the page it was asked from, anything else the error page,
// which a command answered in place could not show, so that one is a flash too.
func (c *Chat) then(w http.ResponseWriter, r *http.Request, err error, to string) {
	if err == nil {
		c.deps.Redirect(w, r, to)
		return
	}
	if ke, ok := client.AsKernelError(err); ok && ke.UpstreamStatus() == http.StatusNotFound {
		c.deps.HandleErr(w, r, &client.KernelError{Kind: client.KindNotFound})
		return
	}
	if reason := refusal(err, c.chatOf(r)); reason != "" {
		c.deps.FlashRedirect(w, r, to, "error", reason)
		return
	}
	if ke, ok := client.AsKernelError(err); ok && c.deps.IsHTMX(r) && ke.Kind != client.KindUnauthenticated {
		c.deps.FlashRedirect(w, r, to, "error", ke.UserMessage())
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
