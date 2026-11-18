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
// own session. A session lands on its newest room; a viewer with none gets one
// with the assistant.
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
	joinName       = "join"
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

// assistantHandle is the agent a viewer's first room is made with; the agents
// app makes it.
const assistantHandle = "assistant"

// Chat is the app.
type Chat struct {
	deps     apps.Deps
	sa       *apps.ServiceAccount
	st       chatState
	register func(login string) error // `juice-chat register`; a test sets its own

	mu      sync.Mutex
	changes map[string]int64        // per address, what this UI changed: see signal
	labels  map[string]client.Party // per chat, its kernel as the viewer's kernel last named it: see called
	names   map[string]naming       // per principal, the viewer's kernel's name for it: see name
}

type chatState struct {
	Password string `json:"password"`
}

func New() *Chat {
	return &Chat{changes: map[string]int64{}, labels: map[string]client.Party{}, names: map[string]naming{}}
}

func (c *Chat) Slug() string { return "chat" }

// Nav contributes no destination: chat is the workspace itself, its rooms
// the sidebar's rail (Rail).
func (c *Chat) Nav(me string, superuser bool) []apps.NavEntry { return nil }

// Landing is where a session begins: its newest room (getLanding).
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
	if err := c.deps.Client.RecordLogin(ctx, filepath.Join(c.root(), "client"), c.login(), c.sa.Tokens()); err != nil {
		return err
	}
	if err := c.serve(ctx); err != nil {
		return err
	}
	if err := c.register(c.login()); err != nil {
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

// self is this kernel's chat, chat@<key>: the UI identifies every chat by its
// kernel's key, which no petname changes, in room addresses, URLs, the rail and
// every comparison, and shows it by label. login is chat's login, by this
// kernel's name.
func (c *Chat) self() string  { return c.deps.KeyAddress(chatHandle) }
func (c *Chat) login() string { return c.deps.Address(chatHandle) }

// chat is the chat at service as a party, as the viewer's kernel last named
// it: this kernel's chat by this kernel's own name, else the requester as learn
// recorded it, else by its key. A page draws it, and its kernel
// (client.Party.Kernel), by _name; a sentence by shown.
func (c *Chat) chat(service string) client.Party {
	if service == c.self() {
		return client.Party{Address: c.login(), Principal: service}
	}
	c.mu.Lock()
	p, ok := c.labels[service]
	c.mu.Unlock()
	if !ok {
		p = client.Party{Address: service, Principal: service}
	}
	return p
}

// shown is the chat at service as a sentence names it.
func (c *Chat) shown(service string) string { return c.chat(service).Shown() }

// learn records a requester that is a chat, as the viewer's kernel names it,
// and answers the requester by key.
func (c *Chat) learn(q client.Party) string {
	chat := q.Keyed()
	if strings.HasPrefix(chat, chatHandle+"@") {
		c.mu.Lock()
		c.labels[chat] = q
		c.mu.Unlock()
	}
	return chat
}

// naming is the viewer's kernel's name for a principal, and when it was given.
type naming struct {
	party client.Party
	at    time.Time
}

// nameKept is how long the viewer's kernel's name for a principal is kept
// before the kernel is asked again: a name changes by a rename or a petname
// bound, and asking may dial the principal's kernel.
const nameKept = 10 * time.Minute

// A request waits only for the kernel it is about, and for any other at most
// wait: what it draws from another source is what was last read of it, and
// that source is read on its own (requirements §6.1, the heartbeat). readBound
// bounds one such read, which may run past the request that began it.
const (
	wait      = time.Second
	readBound = 15 * time.Second
)

// name gives each party of a reply of chat the name the viewer's kernel gives
// its principal, as every page of this UI names a party (ui-grammar "Names").
// Chat holds a member by principal; the address it sends is its own kernel's
// spelling, which is chat's data and no name the viewer's kernel gave. The
// kernel is asked by sys/resolve, which may dial the principal's kernel, so a
// name is asked past the request: one kept, even past nameKept, is drawn as
// kept and asked again; one never seen is waited for at most wait, then
// spelled by key with the nickname its kernel gives itself
// (client.Party.Keyed) until its answer arrives. A principal is stamped as it
// is asked, so it is not asked twice, and a failed ask leaves the stamp. A seat
// not bound yet has no principal and keeps what was typed for it.
func (c *Chat) name(r *http.Request, ps []*client.Party) {
	c.ask(r, ps)
	c.kept(ps)
}

// ask asks the viewer's kernel for the name of each party's principal that is
// not kept or is kept past nameKept, as name says, and waits for those never
// seen at most wait. It draws nothing: what is kept is applied by kept, where
// a party is drawn, so a copy keeps no name and one arriving late is drawn.
func (c *Chat) ask(r *http.Request, ps []*client.Party) {
	now := time.Now()
	ask, unseen := map[string]bool{}, false
	c.mu.Lock()
	for _, p := range ps {
		n, ok := c.names[p.Principal]
		if p.Principal == "" || ask[p.Principal] || ok && now.Sub(n.at) <= nameKept {
			continue
		}
		if !ok {
			n.party, unseen = client.Party{Address: p.Keyed(), Principal: p.Principal, Nickname: p.Nickname}, true
		}
		c.names[p.Principal] = naming{n.party, now}
		ask[p.Principal] = true
	}
	c.mu.Unlock()
	if len(ask) > 0 {
		done := make(chan struct{})
		ctx := context.WithoutCancel(r.Context())
		go func() {
			defer close(done)
			_ = c.deps.Sessions.Do(ctx, func(tok string) error {
				var wg sync.WaitGroup
				for id := range ask {
					wg.Add(1)
					go func() {
						defer wg.Done()
						got, err := c.resolve(ctx, tok, id)
						if err != nil {
							c.log().Warn("chat: name", "principal", id, "err", err)
							return
						}
						c.mu.Lock()
						c.names[id] = naming{got, time.Now()}
						c.mu.Unlock()
					}()
				}
				wg.Wait()
				return nil
			})
		}()
		if unseen {
			select {
			case <-done:
			case <-time.After(wait):
			}
		}
	}
}

// kept gives each party the name kept for its principal, asking nothing.
func (c *Chat) kept(ps []*client.Party) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, p := range ps {
		if n, ok := c.names[p.Principal]; ok && p.Principal != "" {
			*p = n.party
		}
	}
}

// resolve is the viewer's kernel's name for a principal, by sys/resolve,
// within readBound.
func (c *Chat) resolve(ctx context.Context, tok, principal string) (client.Party, error) {
	ctx, cancel := context.WithTimeout(ctx, readBound)
	defer cancel()
	args, _ := json.Marshal(map[string]string{"address": principal})
	rep, err := c.exec(ctx, tok, client.Resolve, args, "")
	if err != nil {
		return client.Party{}, err
	}
	var p client.Party
	if err := json.Unmarshal(rep.Result, &p); err != nil || p.Principal == "" {
		return client.Party{}, fmt.Errorf("sys/resolve: %s", rep.Result)
	}
	return p, nil
}

// parties is every party of the groups, to name in place.
func parties(groups ...[]client.Party) []*client.Party {
	var out []*client.Party
	for _, g := range groups {
		for i := range g {
			out = append(out, &g[i])
		}
	}
	return out
}

// nameRead names the parties a room page draws of a room read: the room's
// members and invited, the entries' authors, those it waits on, and more, the
// request's own (a post's members not reached). Who sent each invitation its
// chat holds is asked in the same batch, so the request waits for names at
// most once, and left as read: the rail draws it (drawn).
func (c *Chat) nameRead(r *http.Request, read *roomRead, more ...*client.Party) {
	ps := append(parties(read.Room.Members, read.Room.Invited, read.Waiting), more...)
	for i := range read.Entries {
		ps = append(ps, &read.Entries[i].Author)
	}
	c.ask(r, append(slices.Clip(ps), inviters(read.Invitations)...))
	c.kept(ps)
}

// inviters is who sent each invitation, to ask or name in place.
func inviters(invites []inviteRow) []*client.Party {
	ps := make([]*client.Party, len(invites))
	for i := range invites {
		ps[i] = &invites[i].From
	}
	return ps
}

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

// join makes the session's user a member of the chat at service, by the path
// any member takes, through the kernel alone: approve chat as a requester (so
// the kernel shows it chat's tasks, U52), run join, which issues a key to the
// caller the kernel names, and connect the key at chat's root, which covers
// every action a member runs. The same three steps join a chat on another
// kernel, which the user's own kernel forwards. A key connected before keeps
// working: no key is ever retired.
func (c *Chat) join(ctx context.Context, token, service string) error {
	if err := c.deps.Client.DecideRequester(ctx, token, service, "approve"); err != nil {
		return fmt.Errorf("approve %s: %w", service, err)
	}
	rep, err := c.exec(ctx, token, service+"/"+joinName, json.RawMessage(`{}`), "")
	if err != nil {
		return fmt.Errorf("join: %w", err)
	}
	var out struct {
		APIKey string `json:"api_key"`
	}
	if err := json.Unmarshal(rep.Result, &out); err != nil || out.APIKey == "" {
		return fmt.Errorf("join answered %s", rep.Result)
	}
	if err := c.deps.Client.ConnectGrant(ctx, token, service, "", out.APIKey); err != nil {
		return fmt.Errorf("connect %s: %w", service, err)
	}
	return nil
}

// rejoin restores the session's user's membership of the chat at service. A
// change of chat's contract, or a disconnect, removes the grant and keeps the
// key, so the kernel grants it again and chat is approved again. Only a key chat
// no longer knows (fresh), or the kernel's answer that it keeps none (invalid
// input, measured on v0.20.0, at home and elsewhere), has the user join anew;
// any other failure is returned, since joining anew would replace the kept key.
// Keeping the key keeps the member: a member of a store made before 0.72.0 is
// bound to its principal by that key alone.
func (c *Chat) rejoin(ctx context.Context, token, service string, fresh bool) error {
	if !fresh {
		_, _, granted, err := c.deps.Client.StartGrant(ctx, token, service, "", "")
		if ke, ok := client.AsKernelError(err); err != nil && (!ok || ke.Kind != client.KindInvalidInput) {
			return err
		}
		if granted {
			return c.deps.Client.DecideRequester(ctx, token, service, "approve")
		}
	}
	return c.join(ctx, token, service)
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
		return c.deps.Reconnect(r, c.self(), func() error { return c.rejoin(ctx, tok, c.self(), false) })
	})
}

// joinAgent joins a local agent to this kernel's chat, when it is not covered,
// with the session in its credential file.
func (c *Chat) joinAgent(ctx context.Context, login string) error {
	return c.asAgent(ctx, login, func(string) error { return nil })
}

// seat joins a local agent to a room here, as its operator adding it is its
// consent: chat approved as its requester, so the kernel shows it chat's tasks
// (U52), and its invitation completed, which joins it. Both are free to the
// agent, the room's creator having paid for the invitation, and neither needs
// a chat key: an agent receives and answers deliveries without one (measured
// on v0.21.1). Only an agent holding no invitation runs room/join, which needs
// its key, so that path joins it to chat first (asAgent).
func (c *Chat) seat(ctx context.Context, login, thread string) error {
	if !c.isLocalAgent(login) {
		return fmt.Errorf("%s has no home here", login)
	}
	invited := false
	err := c.deps.Client.WithLogin(ctx, filepath.Join(c.root(), "client"), login, func(tok string) error {
		if err := c.deps.Client.DecideRequester(ctx, tok, c.self(), "approve"); err != nil {
			return fmt.Errorf("approve %s: %w", c.self(), err)
		}
		tasks, err := c.deps.Client.OpenTasks(ctx, tok)
		if err != nil {
			return err
		}
		for _, t := range invitations(tasks, login, c.self(), thread) {
			if _, err := c.deps.Client.CompleteTask(ctx, tok, t.ID, nil); err != nil {
				return fmt.Errorf("accept: %w", err)
			}
			invited = true
		}
		return nil
	})
	if err != nil || invited {
		return err
	}
	return c.asAgent(ctx, login, func(tok string) error {
		args, _ := json.Marshal(map[string]string{"thread_id": thread})
		if _, err := c.exec(ctx, tok, c.self()+"/"+roomJoinName, args, ""); err != nil {
			return fmt.Errorf("accept: %w", err)
		}
		return nil
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
			if err := c.rejoin(ctx, tok, c.self(), false); err != nil {
				return err
			}
		}
		return then(tok)
	})
}

// joinAgents joins every local agent; one that cannot be joined is logged
// and joined again at the next start, or when a room seats it (seat).
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

// agents lists this kernel's agents (apps.AgentLogins).
func (c *Chat) agents() []string {
	if c.deps.Home == "" {
		return nil
	}
	return apps.AgentLogins(filepath.Join(c.root(), "agents"), c.kernelName())
}

// isLocalAgent reports whether login is one of this kernel's agents.
func (c *Chat) isLocalAgent(login string) bool {
	return c.deps.Home != "" && apps.IsAgent(filepath.Join(c.root(), "agents"), c.kernelName(), login)
}

// Serves is false for an agent's account: its conversations are its program's,
// and a page of its rooms would settle its deliveries (rule 7), bill its reads
// and post in its name.
func (c *Chat) Serves(r *http.Request) bool { return !c.isLocalAgent(c.deps.Me(r)) }

// kernelName is the name this kernel calls itself.
func (c *Chat) kernelName() string { return kernelOf(c.login()) }

// joinFailed is a refused join as a person reads it.
func joinFailed(member string, err error) string {
	var ke *client.KernelError
	if !errors.As(err, &ke) {
		return member + " is not connected to chat: " + err.Error()
	}
	if ke.Kind == client.KindInsufficientFunds {
		return member + " has too little balance to join chat."
	}
	return member + " is not connected to chat: " + ke.UserMessage()
}

// ---- chat, as the UI reaches it ----

// roomRow and entryRow are chat's replies as the UI reads them: members as
// parties, named as chat's kernel names them, and the creator by principal.
// Service is the chat that holds the room, chat@kernel, set by the UI from the
// chat it read the room from; Chat is that chat as a party when it is not this
// kernel's, for the page to name its kernel and copy its address; Mine says the
// viewer created it.
type roomRow struct {
	ID      string         `json:"thread_id"`
	Name    string         `json:"name"`
	Creator string         `json:"creator"`
	Members []client.Party `json:"members"`
	Invited []client.Party `json:"invited"`
	Unread  bool           `json:"unread"`
	LastAt  string         `json:"last_at"`
	Service string         `json:"service"`
	Chat    *client.Party  `json:"chat,omitempty"`
	Mine    bool           `json:"mine,omitempty"`
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
func (c *Chat) tag(rooms []roomRow, service, me string) []roomRow {
	var elsewhere *client.Party
	if service != c.self() {
		p := c.chat(service)
		elsewhere = &p
	}
	for i := range rooms {
		rooms[i].Service, rooms[i].Chat, rooms[i].Mine = service, elsewhere, me != "" && rooms[i].Creator == me
	}
	return rooms
}

// principal is the viewer's principal, by which chat names a room's creator.
func (c *Chat) principal(r *http.Request) string {
	me, _ := c.deps.Viewer(r)
	return me.Address.Principal
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
	Author client.Party    `json:"author"`
	Body   json.RawMessage `json:"body"`
	At     string          `json:"at"`
}

// roomRead is the reply of room/read.
type roomRead struct {
	Room        roomRow        `json:"room"`
	Entries     []entryRow     `json:"entries"`
	Removed     []int64        `json:"removed"`
	Waiting     []client.Party `json:"waiting"`
	More        bool           `json:"more"`
	Rooms       []roomRow      `json:"rooms"`
	Invitations []inviteRow    `json:"invitations"`
}

// inviteRow is an invitation to a room as chat lists it: the room, its name, and
// its creator, who invited; Service and Chat are set by the UI, as a room's.
type inviteRow struct {
	ID      string        `json:"thread_id"`
	Name    string        `json:"name"`
	From    client.Party  `json:"from"`
	Service string        `json:"service"`
	Chat    *client.Party `json:"chat,omitempty"`
}

// Addr is the address of the room the invitation is to.
func (i inviteRow) Addr() string { return roomAddr(i.Service, i.ID) }

// call runs one of the actions of the chat at service as the viewer and decodes
// its result into out, when there is an out. A money button posts the quote it
// named as quote, for the action it names as quoted, and that run is pinned to
// it; any other run of the request reads its own terms.
func (c *Chat) call(r *http.Request, service, name string, args, out any) error {
	body, err := json.Marshal(args)
	if err != nil {
		return err
	}
	var rep client.CallReply
	if err := c.deps.Sessions.Do(r.Context(), func(tok string) (e error) {
		action, shown := service+"/"+name, ""
		if r.PostFormValue("quoted") == action {
			shown = r.PostFormValue("quote")
		}
		rep, e = c.exec(r.Context(), tok, action, body, shown)
		return e
	}); err != nil || out == nil {
		return err
	}
	return json.Unmarshal(rep.Result, out)
}

// exec is the one way chat runs an action: with tok, the session of the user
// or the agent the caller acts for, pinned to the terms of the action's row,
// so the kernel never charges more than was read. Without a quote the person
// was shown (shown, from a money button) it reads the row and runs only when
// the price is minuscule (Deps.Unasked); a dearer action runs nothing and
// errPriced names its terms. Terms that moved since are taken from the
// refusal, which charged nothing, and run once more by the same rule; a quote
// the person was shown and that moved is offered again.
func (c *Chat) exec(ctx context.Context, tok, action string, args json.RawMessage, shown string) (client.CallReply, error) {
	quote := shown
	if quote == "" {
		a, ok, err := c.deps.Client.ResolveRef(ctx, tok, action)
		if err != nil {
			return client.CallReply{}, err
		}
		if !ok {
			return client.CallReply{}, &client.KernelError{Kind: client.KindNotFound, Status: http.StatusNotFound, Subject: "action", KernelMessage: action + " not found"}
		}
		if !c.deps.Unasked(a.Price) {
			return client.CallReply{}, c.priced(action, a.Price, a.QuoteHash)
		}
		quote = a.QuoteHash
	}
	rep, err := c.deps.Client.Run(ctx, tok, action, args, quote)
	if ke, ok := client.AsKernelError(err); ok && ke.Kind == client.KindTermsChanged {
		price, _ := ke.MetaInt64("price")
		if shown != "" || !c.deps.Unasked(price) {
			return rep, c.priced(action, price, ke.MetaString("quote_hash"))
		}
		rep, err = c.deps.Client.Run(ctx, tok, action, args, ke.MetaString("quote_hash"))
	}
	return rep, err
}

// errPriced is a run not made: its price is not minuscule and nobody was
// asked. It carries the terms read, for a money button to name and pin.
type errPriced struct {
	Action string
	Price  int64
	Quote  string
	Text   string // the price as the person reads it
}

func (e *errPriced) Error() string { return "not run unasked: it costs " + e.Text }

func (c *Chat) priced(action string, price int64, quote string) *errPriced {
	return &errPriced{action, price, quote, c.deps.Money(price)}
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
			fresh := ke.Kind != client.KindGrantRequired // chat refused the key itself
			if jerr := c.deps.Sessions.Do(r.Context(), func(tok string) error {
				return c.deps.Reconnect(r, service, func() error { return c.rejoin(r.Context(), tok, service, fresh) })
			}); jerr != nil {
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
			if g.Action.Keyed() == action {
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
	me := c.principal(r)
	out.Room = c.tag([]roomRow{out.Room}, service, me)[0]
	out.Rooms, out.Invitations = c.tag(out.Rooms, service, me), c.tagInvites(out.Invitations, service)
	return out, nil
}

// readOf is a read of one room as the entry of its chat, begun at at: the
// chat's rooms and invitations that room/read answers beside the room.
func (c *Chat) readOf(service string, read roomRead, at int64, signal string) map[string]chatRead {
	return map[string]chatRead{service: {Rooms: read.Rooms, Invites: read.Invitations, Chat: c.chat(service), reading: reading{At: at, Signal: signal}}}
}

// tagInvites marks invitations as held by the chat at service.
func (c *Chat) tagInvites(invites []inviteRow, service string) []inviteRow {
	for i, room := range c.tag(make([]roomRow, len(invites)), service, "") {
		invites[i].Service, invites[i].Chat = room.Service, room.Chat
	}
	return invites
}

// latest asks readRoom for the room's latest entries.
const latest = -1

// downRow is a chat of the viewer's that could not be read, and why.
type downRow struct {
	Service string       `json:"service"`
	Chat    client.Party `json:"chat"` // as the page names it
	Reason  string       `json:"reason"`
}

// reading is when a source was last read: when that read began, so an older
// read never replaces a newer one; the signal it answered; until when a read
// in flight holds it, so it is not read twice; and after a failed read how many
// failed and when the next may run. A room's watch and each chat of the rail
// keep one, and back off by the one rule (due, failed).
type reading struct {
	At     int64  `json:"at"` // unix nanoseconds
	Signal string `json:"signal"`
	Busy   int64  `json:"busy,omitempty"` // unix nanoseconds
	Fails  int    `json:"fails,omitempty"`
	Retry  int64  `json:"retry,omitempty"` // unix seconds
}

// due reports whether a source should be read: never while a read holds it; a
// failed read is owed once its delay has passed, whatever the signal says by
// then; otherwise, when its signal is not the kernel's.
func (g reading) due(signal string) bool {
	if time.Now().UnixNano() < g.Busy {
		return false
	}
	if g.Fails > 0 {
		return time.Now().Unix() >= g.Retry
	}
	return signal != g.Signal
}

// failed is the reading after a failed read. It acknowledges nothing, so the
// source is read again until it answers, 5 s later, then 10, 20, 40, and every
// 60 s, so a source that is down is not read on every tick.
func (g reading) failed() reading {
	delay := min(5<<min(g.Fails, 4), 60)
	g.Fails, g.Retry = g.Fails+1, time.Now().Unix()+int64(delay)
	return g
}

// chatRead is what the session keeps of one chat: its rooms and invitations as
// last read, or why it could not be read, and its reading. Chat is the chat as
// a page names it.
type chatRead struct {
	Rooms   []roomRow    `json:"rooms"`
	Invites []inviteRow  `json:"invites"`
	Chat    client.Party `json:"chat"`
	Reason  string       `json:"reason,omitempty"` // "" unless the last read failed
	reading
}

// railCache is what the session keeps for the rail: the chats asking, as the
// pulse last held them; the viewer's chats in the pulse's order, this kernel's
// first; and what was last read of each, by chat. Each chat is read on its own
// and written alone (put), so no read waits for another chat or undoes one.
type railCache struct {
	Asking []client.Party      `json:"asking"`
	Chats  []string            `json:"chats"`
	Read   map[string]chatRead `json:"read"`
}

// railShown is what the rail shows of the cache: the chats asking, the
// invitations, the rooms newest activity first, and the chats that could not
// be read, each chat's in the order of the chats.
type railShown struct {
	Asking  []client.Party
	Invites []inviteRow
	Rooms   []roomRow
	Down    []downRow
}

func (cache railCache) shown() railShown {
	v := railShown{Asking: cache.Asking}
	for _, chat := range cache.Chats {
		e := cache.Read[chat]
		if e.Reason != "" {
			v.Down = append(v.Down, downRow{chat, e.Chat, e.Reason})
			continue
		}
		v.Rooms = append(v.Rooms, e.Rooms...)
		v.Invites = append(v.Invites, e.Invites...)
	}
	v.Rooms = byActivity(v.Rooms)
	return v
}

// drawn is what the rail shows of the cache, each invitation's sender named as
// the viewer's kernel names it now: the cache keeps the sender as chat sent it,
// so a name kept after the read is drawn without reading the chat again.
func (c *Chat) drawn(cache railCache) railShown {
	v := cache.shown() // its invitations a slice of its own
	c.kept(inviters(v.Invites))
	return v
}

// unknown reports a chat of the viewer's whose rooms are not known: it could
// not be read, or has not answered yet.
func (cache railCache) unknown() bool {
	for _, chat := range cache.Chats {
		if e := cache.Read[chat]; e.At == 0 || e.Reason != "" {
			return true
		}
	}
	return false
}

// put makes read the entry of its chat, unless the chat is no longer the
// viewer's or a newer read began since. A failed read keeps the signal last
// answered and backs the chat off.
func (cache *railCache) put(chat string, read chatRead) {
	prev := cache.Read[chat]
	if !slices.Contains(cache.Chats, chat) || read.At <= prev.At {
		return
	}
	if read.Reason != "" {
		f := prev.reading.failed()
		read.reading = reading{At: read.At, Signal: prev.Signal, Fails: f.Fails, Retry: f.Retry}
	}
	if cache.Read == nil {
		cache.Read = map[string]chatRead{}
	}
	cache.Read[chat] = read
}

// list is one chat's rooms and invitations for the viewer, read now, as the
// entry of that chat; a chat that could not be read is its reason, and the
// error is returned beside it for a caller that acts on its kind.
func (c *Chat) list(r *http.Request, chat, signal string) (chatRead, error) {
	at := time.Now().UnixNano()
	var out struct {
		Rooms       []roomRow   `json:"rooms"`
		Invitations []inviteRow `json:"invitations"`
	}
	if err := c.run(r, chat, roomListName, struct{}{}, &out); err != nil {
		c.log().Warn("chat: list rooms", "chat", chat, "err", err)
		return chatRead{Chat: c.chat(chat), Reason: c.why(err, chat), reading: reading{At: at}}, err
	}
	invites := c.tagInvites(out.Invitations, chat)
	c.ask(r, inviters(invites))
	return chatRead{Rooms: c.tag(out.Rooms, chat, c.principal(r)), Invites: invites, Chat: c.chat(chat), reading: reading{At: at, Signal: signal}}, nil
}

// sidebar brings the session's copy to the pulse and returns it: the chats and
// those asking as the pulse holds them, a chat no longer the viewer's dropped.
// fresh is what this request read of its own chats, put as their entries; with
// others every other chat that is due is read too (refresh), and without, each
// stays as last read.
func (c *Chat) sidebar(r *http.Request, p pulse, signal string, fresh map[string]chatRead, others bool) railCache {
	c.update(r, func(cache *railCache) {
		cache.Asking, cache.Chats = p.Asking, p.Services
		for chat := range cache.Read {
			if !slices.Contains(cache.Chats, chat) {
				delete(cache.Read, chat)
			}
		}
		for chat, read := range fresh {
			cache.put(chat, read)
		}
	})
	if others {
		c.refresh(r, p, signal)
	}
	cache, _ := c.cachedRail(r)
	return cache
}

// refresh reads each of the viewer's chats that is due, each on its own:
// claimed, so no other request reads it meanwhile; listed past the request,
// within readBound; and put as its entry when it answers. It returns once all
// have answered or after wait, whichever is first; a chat slower than that is
// drawn by a later request.
func (c *Chat) refresh(r *http.Request, p pulse, signal string) {
	ctx := context.WithoutCancel(r.Context())
	var wg sync.WaitGroup
	for _, chat := range p.Services {
		if !c.claim(r, chat, signal) {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			rctx, cancel := context.WithTimeout(ctx, readBound)
			defer cancel()
			rr := r.WithContext(rctx)
			read, _ := c.list(rr, chat, signal)
			c.update(rr, func(cache *railCache) { cache.put(chat, read) })
		}()
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(wait):
	}
}

// claim marks a chat of the viewer's that is due as being read, and reports
// whether it was.
func (c *Chat) claim(r *http.Request, chat, signal string) (claimed bool) {
	c.update(r, func(cache *railCache) {
		e := cache.Read[chat]
		if !slices.Contains(cache.Chats, chat) || !e.due(signal) {
			return
		}
		e.Busy = time.Now().Add(readBound).UnixNano()
		if cache.Read == nil {
			cache.Read = map[string]chatRead{}
		}
		cache.Read[chat], claimed = e, true
	})
	return claimed
}

// invitations are the waiting invitation tasks among tasks addressed to me
// from the chat at service, to the room thread.
func invitations(tasks []client.Task, me, service, thread string) []client.Task {
	var out []client.Task
	for _, t := range tasks {
		if a := argsOf(t); t.Waiting() && t.RequiredCaller.Address == me && t.Requester.Keyed() == service && a.Room == thread && a.From != "" {
			out = append(out, t)
		}
	}
	return out
}

// cachedRail is the session's copy of the rail, and whether it holds one.
func (c *Chat) cachedRail(r *http.Request) (railCache, bool) {
	var cache railCache
	b, ok := c.deps.Sessions.AppCache(r.Context(), c.Slug())
	if ok {
		_ = json.Unmarshal([]byte(b), &cache)
	}
	return cache, ok
}

// update changes the session's copy of the rail in one step under the
// session's lock, so concurrent requests each change what the one before left.
func (c *Chat) update(r *http.Request, fn func(*railCache)) {
	c.deps.Sessions.UpdateAppCache(r.Context(), c.Slug(), func(old string) string {
		var cache railCache
		_ = json.Unmarshal([]byte(old), &cache)
		fn(&cache)
		b, _ := json.Marshal(cache)
		return string(b)
	})
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
	Asking   []client.Party
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
			if (t.Owner.Address == me || t.RequiredCaller.Address == me) && argsOf(t).Room != "" && slices.Contains(p.Services, t.Requester.Keyed()) {
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
func (c *Chat) chats(r *http.Request, tok string) (joined []string, asking []client.Party, err error) {
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
			owner, _, _ := strings.Cut(g.Action.Keyed(), "/")
			granted[owner] = true
		}
	}
	for _, q := range reqs {
		chat := c.learn(q.Requester)
		switch {
		case !strings.HasPrefix(chat, chatHandle+"@") || q.Status == "blocked":
		case q.Status == "approved" && (chat == c.self() || granted[chat]):
			joined = append(joined, chat)
		case chat != c.self() && !(q.Status == "pending" && c.deps.Ignored(r, q.Requester)):
			asking = append(asking, q.Requester)
		}
	}
	sort.Slice(joined, func(i, j int) bool {
		a, b := joined[i], joined[j]
		return a == c.self() || (b != c.self() && a < b)
	})
	sort.Slice(asking, func(i, j int) bool { return asking[i].Keyed() < asking[j].Keyed() })
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
	return t.Waiting() && t.RequiredCaller.Address == me && t.Requester.Keyed() == service && a.Room == thread && a.From == ""
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

// watchState is what a room page's watch says it shows: the room's last
// entry, and the reading of the room: the signal of the last read that
// succeeded, and after a failed read how many failed and when the next may run.
type watchState struct {
	After int64
	reading
}

func (w watchState) String() string {
	return fmt.Sprintf("%d.%s.%d.%d", w.After, w.Signal, w.Fails, w.Retry)
}

func parseWatch(s string) (w watchState) {
	p := strings.Split(s, ".")
	w.After, _ = strconv.ParseInt(p[0], 10, 64)
	if len(p) != 4 {
		return w
	}
	w.Signal = p[1]
	w.Fails, _ = strconv.Atoi(p[2])
	w.Retry, _ = strconv.ParseInt(p[3], 10, 64)
	return w
}

// failed is the watch after a failed read of its room.
func (w watchState) failed() watchState {
	w.reading = w.reading.failed()
	return w
}

// watchView is a room page's watch input, keyed by the room's address.
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
	var priced *errPriced
	if errors.As(err, &priced) {
		return "This costs " + priced.Text + ", more than is paid without asking."
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
	case http.StatusUnprocessableEntity:
		return "A room holds at most 32 members."
	}
	return ""
}

// why is a chat that could not be read, as a person reads it.
func (c *Chat) why(err error, service string) string {
	if reason := refusal(err, c.shown(service)); reason != "" {
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
	listed, _ := c.list(r, service, "")
	if listed.Reason != "" {
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
	Price     string // a creation priced above what runs unasked, its quote and action
	Quote     string
	Quoted    string
	Agents    []agentChoice
	Members   string
	Name      string
	Path      string
	CSRFField template.HTML
}

type agentChoice struct {
	Agent  client.Party
	Chosen bool
}

// railView backs the rail: the chats asking to be connected, the invitations,
// the chats that could not be read, the viewer's rooms, the room in view by its
// address, the rail's watch, and the dialogs a page that is not a room holds.
// Home is this kernel's chat, which is never disconnected.
type railView struct {
	railShown
	Digest    string // what the rail shows, as its watch carries it (railDigest)
	Me        string
	Home      string
	New       newRoomView
	Rename    renameView
	Current   string
	Path      string
	CSRFField template.HTML
	OOB       bool                      // the heartbeat's out-of-band answer: the rail alone, replacing the one shown
	Label     func(client.Party) string // a party in a command's words (apps.Deps.Label)
}

// railItem is a rail entry that is not a room: a request (a chat asking to be
// connected, an invitation), or a chat that could not be read. Chat is the
// chat it is about, drawn by _name; an invitation names its Room, and the
// chat's kernel after it when the room is elsewhere; Title is what its title
// and a screen reader add; a request says what it is in Detail, an invitation
// who sent it in From.
type railItem struct {
	ID, Room, Title, Detail string
	Chat, From              *client.Party
	Commands                []railCommand
}

// railCommand is one command of a rail entry: a form posting to Action, asked
// first when Confirm is set (the question, its mark added by the template). In
// a menu it is an item, after a divider when Divider is, a danger one when
// Danger. On a request it is a button in the request's row submitting its form
// by id, Form, through the form attribute (ui-grammar §5.1), the request's
// answer, Primary, solid.
type railCommand struct {
	Action, Label, Confirm, Form string
	Danger, Divider, Primary     bool
	CSRFField                    template.HTML
}

// Requests is what waits on the viewer above the rooms, each saying what it is
// with its buttons in view, its answer first (Element's Invites, LinkedIn's
// invitations): each chat asking to be connected, then each invitation, naming
// who sent it.
func (v railView) Requests() []railItem {
	var out []railItem
	for _, a := range v.Asking {
		chat, service := a, a.Keyed()
		out = append(out, v.request(railItem{ID: "ask-" + service, Chat: &chat, Detail: "Connection request"},
			railCommand{Action: "/chat/chats/" + service + "/approve", Label: "Approve"},
			railCommand{Action: "/chat/chats/" + service + "/ignore", Label: "Ignore"}))
	}
	for _, i := range v.Invites {
		from := i.From
		out = append(out, v.request(railItem{ID: "invite-" + i.Addr(), Room: i.Name, Chat: i.Chat, Detail: "Invitation from", From: &from},
			railCommand{Action: "/chat/invites/" + i.Addr() + "/accept", Label: "Accept"},
			railCommand{Action: "/chat/invites/" + i.Addr() + "/decline", Label: "Decline", Confirm: "Decline " + i.Name + ", which only its creator can invite you to again"}))
	}
	return out
}

// request is item with its two commands, answer and refusal, each a button
// submitting its own form, the answer the primary.
func (v railView) request(item railItem, answer, refusal railCommand) railItem {
	answer.Primary = true
	for _, c := range []*railCommand{&answer, &refusal} {
		c.Form, c.CSRFField = c.Action, v.CSRFField
	}
	item.Commands = []railCommand{answer, refusal}
	return item
}

// Unread is the rows under the rooms: each chat that could not be read, its
// reason its title; one elsewhere can be disconnected or blocked.
func (v railView) Unread() []railItem {
	var out []railItem
	for _, d := range v.Down {
		chat := d.Chat
		item := railItem{ID: "down-" + d.Service, Chat: &chat, Title: d.Reason}
		if d.Service != v.Home {
			item.Commands = []railCommand{v.disconnect(chat, false)}
		}
		out = append(out, item)
	}
	return out
}

// Drop is a room's commands ending the viewer's part in it, each asked first
// in one question naming what it costs (grammar §5.8): Leave for a member; for its creator Leave, which hands the
// room to another member, while one is joined, and Delete.
func (v railView) Drop(room roomRow) []railCommand {
	leave := railCommand{Action: "/chat/room/" + room.Addr() + "/leave", Label: "Leave…", Confirm: "Leave " + room.Name, Divider: true, CSRFField: v.CSRFField}
	if !room.Mine {
		return []railCommand{leave}
	}
	del := railCommand{Action: "/chat/room/" + room.Addr() + "/delete", Label: "Delete…", Confirm: "Delete " + room.Name + " and its history for everyone", Danger: true, Divider: true, CSRFField: v.CSRFField}
	if len(room.Members) < 2 {
		return []railCommand{del}
	}
	leave.Confirm = "Leave " + room.Name + " and make another member its creator"
	del.Divider = false
	return []railCommand{leave, del}
}

// Elsewhere is the command a room held elsewhere adds to its menu: its chat
// disconnected.
func (v railView) Elsewhere(room roomRow) []railCommand {
	return []railCommand{v.disconnect(*room.Chat, true)}
}

// disconnect is the menu's command disconnecting the chat at service, named
// for the viewer by its kernel's label. It asks nothing: it destroys nothing,
// and Connect restores the chat with every room (Apple HIG, Gmail: only what
// cannot be undone is asked).
func (v railView) disconnect(chat client.Party, divider bool) railCommand {
	return railCommand{Action: "/chat/chats/" + chat.Keyed() + "/disconnect", Label: "Disconnect " + v.Label(chat.Kernel()), Divider: divider, CSRFField: v.CSRFField}
}

// membersView backs the members dialog: open when Open, and open with the reason
// and the address typed when a change was refused.
type membersView struct {
	Room      roomRow
	Me        string // the viewer's principal
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
	Working []string // the members holding an unanswered delivery here, as a row names them (Chat.labelled)
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

// labelled is each party as a compact place names it (apps.Deps.Label): a
// line of the room, like an author's, adds a key only once two kernels claim
// one nickname.
func (c *Chat) labelled(ps []client.Party) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = c.deps.Label(p)
	}
	return out
}

// andList joins names as a sentence does: "a", "a and b", "a, b and c".
func andList(names []string) string {
	if n := len(names); n > 1 {
		return strings.Join(names[:n-1], ", ") + " and " + names[n-1]
	}
	return strings.Join(names, "")
}

// undelivered is the line saying whom a post, or an invitation, did not reach
// yet, "" when it reached everyone it was for, its members named here.
func (c *Chat) undelivered(r *http.Request, members []client.Party) string {
	c.name(r, parties(members))
	return notDelivered(c.labelled(members))
}

// notDelivered is that line for members already named.
func notDelivered(names []string) string {
	if len(names) == 0 {
		return ""
	}
	return "Not delivered to " + andList(names) + "."
}

// getLanding is where a session lands. Each local agent still invited to a
// room here is seated first, so one whose seat failed is seated at the next
// visit; then the newest room opens. The rooms are the rail's, as the session
// last read them; a session that holds none reads every chat, and waits for
// them at most wait. A viewer with no room at all, as every chat read says,
// gets one with the assistant, named New room; a chat that could not be read,
// or has not answered yet, may hold rooms, so then nothing is made. The viewer
// is joined here when not yet; an agent is joined by the seat that needs it.
func (c *Chat) getLanding(w http.ResponseWriter, r *http.Request) {
	me := c.deps.Me(r)
	if err := c.joinViewer(r); err != nil {
		if c.toWallet(w, r, err) {
			return
		}
		c.deps.RenderPage(w, r, http.StatusOK, "landing", "Rooms", landingView{Error: joinFailed(me, err)})
		return
	}
	p, err := c.pulse(r)
	if c.deps.HandleErr(w, r, err) {
		return
	}
	// The viewer's own chat is read now, as a room's page reads its chat, so
	// rooms show on the first visit after money arrives; a fresh answer
	// replaces a failed read the session still holds.
	signal := c.signal(me, p)
	fresh := map[string]chatRead{}
	if slices.Contains(p.Services, c.self()) {
		read, err := c.list(r, c.self(), signal)
		if c.toWallet(w, r, err) {
			return
		}
		fresh[c.self()] = read
	}
	_, held := c.cachedRail(r)
	rail := c.sidebar(r, p, signal, fresh, !held)
	listed := rail.shown()
	note := c.seatInvited(r, listed.Rooms)
	assistant := c.deps.Address(assistantHandle)
	var to string
	switch {
	case len(listed.Rooms) > 0:
		to = listed.Rooms[0].Addr()
	case !rail.unknown() && me != assistant && c.isLocalAgent(assistant):
		var out struct {
			Room roomRow `json:"room"`
		}
		err := c.run(r, c.self(), roomCreateName, map[string]any{"members": []string{assistant}, "name": firstRoomName}, &out)
		if errors.As(err, new(*errPriced)) {
			c.deps.RenderPage(w, r, http.StatusOK, "landing", "Rooms", landingView{Error: refusal(err, c.shown(c.self()))})
			return
		}
		if c.toWallet(w, r, err) {
			return
		}
		if c.deps.HandleErr(w, r, err) {
			return
		}
		if err := c.seat(r.Context(), assistant, out.Room.ID); err != nil && note == "" {
			note = joinFailed(assistant, err)
		}
		c.changed(me)
		to = roomAddr(c.self(), out.Room.ID)
	default:
		c.deps.RenderPage(w, r, http.StatusOK, "landing", "Rooms", landingView{Error: note, Unread: rail.unknown()})
		return
	}
	to = "/chat/room/" + url.PathEscape(to)
	if note != "" {
		c.deps.FlashRedirect(w, r, to, "error", note)
		return
	}
	http.Redirect(w, r, to, http.StatusSeeOther)
}

// toWallet sends the viewer to the wallet when chat refused them for funds on
// the way into their rooms — the landing's join, list and first room, and a
// room's read — since none opens without money and the wallet says how it
// comes in.
func (c *Chat) toWallet(w http.ResponseWriter, r *http.Request, err error) bool {
	ke, ok := client.AsKernelError(err)
	if !ok || ke.Kind != client.KindInsufficientFunds {
		return false
	}
	c.deps.FlashRedirect(w, r, "/wallet", "error", "Not enough balance to open your rooms.")
	return true
}

// firstRoomName is the name of the room made for a viewer who has none.
const firstRoomName = "New room"

// seatInvited seats each local agent still invited to a room here, as a room
// made or joined with it seats it; the first that cannot be is named.
func (c *Chat) seatInvited(r *http.Request, rooms []roomRow) (note string) {
	seated := false
	for _, room := range rooms {
		if room.Service != c.self() {
			continue
		}
		for _, m := range room.Invited {
			if !c.isLocalAgent(m.Address) {
				continue
			}
			if err := c.seat(r.Context(), m.Address, room.ID); err != nil {
				if note == "" {
					note = joinFailed(m.Address, err)
				}
				continue
			}
			seated = true
		}
	}
	if seated {
		c.changed(c.deps.Me(r))
	}
	return note
}

// landingView backs the page a viewer with no room listed lands on; Unread
// says a chat could not be read, or has not answered yet, so rooms may exist
// that the rail cannot show.
type landingView struct {
	Error  string
	Unread bool
}

// Rail is the sidebar's list of the viewer's rooms, newest activity first, with
// the new-conversation dialog, open on ?form=new from any page. It is what the
// session last read, so a page runs nothing and waits for no kernel; the rail's
// watch, in the heartbeat's lane apart, reads the chats and fills it, at once
// for a session that holds nothing yet.
func (c *Chat) Rail(r *http.Request) (template.HTML, error) {
	cache, _ := c.cachedRail(r)
	return c.rail(r, cache, false)
}

// rail renders the rail with its watch, naming the room in view by its
// address; oob renders it as an answer in place, the rail alone replacing the
// one shown, for the page the answer is to (back).
func (c *Chat) rail(r *http.Request, cache railCache, oob bool) (template.HTML, error) {
	path := r.URL.Path
	if oob {
		path = back(r)
	}
	current, onRoom := strings.CutPrefix(path, "/chat/room/")
	if current, _, _ = strings.Cut(current, "/"); !onRoom {
		current = ""
	}
	csrf := c.deps.CSRFField(r)
	shown := c.drawn(cache)
	newRoom, answered := r.Context().Value(newRoomKey{}).(newRoomView)
	if !answered {
		newRoom = newRoomView{Open: r.URL.Query().Get("form") == "new", Agents: c.agentChoices(nil), Path: path, CSRFField: csrf}
	}
	return c.deps.Fragment("_rail", railView{railShown: shown, Digest: railDigest(shown), Me: c.deps.Me(r), Home: c.self(), Rename: renameView{CSRFField: csrf}, Current: current,
		New: newRoom, Path: path, CSRFField: csrf, OOB: oob, Label: c.deps.Label})
}

// Context is a room's recent conversation as the planner reads it: the
// viewer's entries as the user's, every other member's as the assistant's,
// named; a document as its JSON text.
func (c *Chat) Context(r *http.Request, room string) ([]client.Message, error) {
	me := c.principal(r)
	service, thread, ok := splitRoom(room)
	if !ok {
		return nil, errors.New("This room no longer exists.")
	}
	read, err := c.readRoom(r, service, thread, latest, false)
	if reason := refusal(err, c.shown(service)); reason != "" {
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
		if e.Author.Principal == me {
			out = append(out, client.Message{Role: "user", Content: text})
			continue
		}
		out = append(out, client.Message{Role: "assistant", Content: e.Author.Address + ": " + text})
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
	at := time.Now().UnixNano()
	read, err := c.readRoom(r, service, thread, latest, true)
	if err != nil {
		if c.toWallet(w, r, err) {
			return
		}
		if c.gone(r, service, thread, err, false) {
			c.relist(r, p, service)
			err = &client.KernelError{Kind: client.KindNotFound}
		}
		c.deps.HandleErr(w, r, err)
		return
	}
	p.Tasks = c.settle(r, service, thread, p.Tasks)
	signal := c.signal(me, p)
	var others []string // the members a post addresses, in chat's spelling
	for _, m := range read.Room.Members {
		if m.Principal != c.principal(r) {
			others = append(others, m.Address)
		}
	}
	c.nameRead(r, &read)
	c.sidebar(r, p, signal, c.readOf(service, read, at, signal), false)
	c.deps.ShowImages(w)
	post, err := c.postSchema(r, service)
	if c.deps.HandleErr(w, r, err) {
		return
	}
	locked, _ := json.Marshal(map[string]string{"thread_id": thread})
	rename := renameView{CSRFField: c.deps.CSRFField(r)}
	if r.URL.Query().Get("form") == "rename" && read.Room.Mine {
		rename.Room = &read.Room
	}
	st := watchState{After: maxSeq(read.Entries), reading: reading{Signal: signal}}
	c.deps.RenderPage(w, r, http.StatusOK, "room", "Chat", roomView{
		Room: read.Room, Me: me, Others: others, Rename: rename,
		Members: membersView{Room: read.Room, Me: c.principal(r), Open: r.URL.Query().Get("form") == "members", CSRFField: c.deps.CSRFField(r)},
		Entries: entriesView{RoomID: room, Entries: read.Entries, Working: c.labelled(read.Waiting)},
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
// delivery for the viewer, which a tab read while hidden left open. The rail's
// watch, in the lane apart, reads each chat that is due, on its own, and
// answers the rail when what the session holds differs from what the page
// shows; a room's reads its room alone.
func (c *Chat) watch(r *http.Request, key, shown string) (template.HTML, error) {
	p, err := c.pulse(r)
	if err != nil {
		return "", err
	}
	me := c.deps.Me(r)
	if key == railKey {
		rail := c.sidebar(r, p, c.signal(me, p), nil, true)
		if railDigest(c.drawn(rail)) == shown {
			return "", nil
		}
		return c.rail(r, rail, true)
	}
	st := parseWatch(shown)
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
	return c.advance(r, key, st, p, visible, nil)
}

// advance brings a room page to what its chat now holds: its new entries, the
// posts they replaced, whom it waits on, and the watch; and the rail when the
// read changed what the session holds of its chat. A room seen has its
// deliveries settled. A read that leaves more keeps the old signal, so the next
// tick reads on. A room gone sends the page to the landing, its chat relisted
// first so the landing does not open it again: there is nothing more to show,
// and a delivery of it still open would read it again on every tick. missed
// is whom the request's post did not reach, said beside the composer, named
// in the read's one batch so the request waits for names at most once.
func (c *Chat) advance(r *http.Request, room string, st watchState, p pulse, seen bool, missed []client.Party) (template.HTML, error) {
	me := c.deps.Me(r)
	service, thread, _ := splitRoom(room)
	at := time.Now().UnixNano()
	read, err := c.readRoom(r, service, thread, st.After, seen)
	if err != nil {
		c.log().Warn("chat: read room", "room", room, "err", err)
		if c.gone(r, service, thread, err, false) {
			c.relist(r, p, service)
			return "", apps.Moved("/chat")
		}
		return c.deps.Fragment("_chat_watch", watchView{room, st.failed()})
	}
	if seen {
		p.Tasks = c.settle(r, service, thread, p.Tasks)
	}
	signal := c.signal(me, p)
	before, _ := c.cachedRail(r)
	c.nameRead(r, &read, parties(missed)...)
	rail := c.sidebar(r, p, signal, c.readOf(service, read, at, signal), false)
	next := watchState{After: max(st.After, maxSeq(read.Entries)), reading: reading{Signal: signal}}
	if read.More {
		next.Signal = st.Signal
	}
	out, err := c.deps.Fragment("_chat_tick", entriesView{RoomID: room, Entries: read.Entries, Removed: read.Removed,
		Working: c.labelled(read.Waiting), Watch: watchView{room, next}, Error: notDelivered(c.labelled(missed))})
	if err != nil || railDigest(c.drawn(rail)) == railDigest(c.drawn(before)) {
		return out, err
	}
	h, err := c.rail(r, rail, true)
	return out + h, err
}

// railKey is the rail's watch: a room's key is its address, never this one.
const railKey = "rail"

// railDigest names the rail a page shows (drawn): the chats asking, the
// invitations and who sent them as named, its rooms, their names and which are
// unread, and the chats that could not be read, so the heartbeat swaps the rail
// only when one of them changed, a name arriving late included.
func railDigest(rail railShown) string {
	h := sha256.New()
	for _, a := range rail.Asking {
		fmt.Fprintf(h, "%s\x00%s\x02", a.Keyed(), a.Label())
	}
	for _, i := range rail.Invites {
		fmt.Fprintf(h, "%s\x00%s\x00%s\x03", i.Addr(), i.Name, i.From.Label())
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
		Undelivered []client.Party `json:"undelivered"`
	}
	if err := c.run(r, service, postName, fields, &posted); err != nil {
		if ke, ok := client.AsKernelError(err); ok && ke.UpstreamStatus() == http.StatusUnprocessableEntity {
			c.composeFailed(w, r, room, c.overFull(r, room))
			return
		}
		if reason := refusal(err, c.shown(service)); reason != "" {
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
	if !c.deps.IsHTMX(r) {
		if note := c.undelivered(r, posted.Undelivered); note != "" {
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
	out, err := c.advance(r, room, watchState{After: after}, p, true, posted.Undelivered)
	if c.deps.HandleErrFragment(w, r, err) {
		return
	}
	writeHTML(w, out)
}

func writeHTML(w http.ResponseWriter, h template.HTML) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(h))
}

// overFull is why a post in a room made before the member limit is refused:
// the room holds more members than a room may, counted as the session last
// read it.
func (c *Chat) overFull(r *http.Request, room string) string {
	cache, _ := c.cachedRail(r)
	for _, row := range cache.shown().Rooms {
		if row.Addr() == room {
			return fmt.Sprintf("This room has %d members; a room holds at most 32. Remove members to post.", len(row.Members)+len(row.Invited))
		}
	}
	return "This room has more than 32 members; a room holds at most 32. Remove members to post."
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
		c.newRoomRefused(w, r, "Choose the room's members.")
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
		Room      roomRow        `json:"room"`
		Uninvited []client.Party `json:"uninvited"`
	}
	err := c.run(r, c.self(), roomCreateName, args, &out)
	if priced := new(errPriced); errors.As(err, &priced) {
		c.newRoomAt(w, r, priced)
		return
	}
	if reason := refusal(err, c.shown(c.self())); reason != "" {
		c.newRoomRefused(w, r, reason)
		return
	}
	if err != nil {
		c.then(w, r, err, "/chat")
		return
	}
	c.joinMembers(w, r, members, out.Room.ID, c.undelivered(r, out.Uninvited))
}

// newRoomRefused says why a conversation could not start: in the dialog, with what
// was chosen and typed, or without script as a flash on the landing.
func (c *Chat) newRoomRefused(w http.ResponseWriter, r *http.Request, reason string) {
	if !c.deps.IsHTMX(r) {
		c.deps.FlashRedirect(w, r, "/chat?form=new", "error", reason)
		return
	}
	c.newRoom(w, r, newRoomView{Error: reason})
}

// newRoomAt offers the creation again on a money button naming its price and
// pinned to its quote: chat's operator priced it above what runs unasked.
func (c *Chat) newRoomAt(w http.ResponseWriter, r *http.Request, p *errPriced) {
	c.newRoom(w, r, newRoomView{Price: p.Text, Quote: p.Quote, Quoted: p.Action})
}

// newRoom answers the dialog open again with what was chosen and typed: in
// place under htmx; without script, the page drawn with the rail's dialog open
// on it, the one dialog carrying v on the request it is drawn for.
func (c *Chat) newRoom(w http.ResponseWriter, r *http.Request, v newRoomView) {
	v.Open, v.Agents, v.Members, v.Name, v.Path, v.CSRFField = true, c.agentChoices(r.Form["agent"]), r.FormValue("members"), r.FormValue("name"), "/chat", c.deps.CSRFField(r)
	if c.deps.IsHTMX(r) {
		_ = c.deps.RenderFragment(w, http.StatusOK, "_new_room", v)
		return
	}
	r = r.WithContext(context.WithValue(r.Context(), newRoomKey{}, v))
	c.deps.RenderPage(w, r, http.StatusOK, "landing", "Rooms", landingView{})
}

// newRoomKey carries a New room dialog answered without script to the rail
// that draws it.
type newRoomKey struct{}

// agentChoices is the installation's agents, those chosen ticked.
func (c *Chat) agentChoices(chosen []string) []agentChoice {
	agents := c.agents()
	out := make([]agentChoice, len(agents))
	for i, a := range agents {
		out[i] = agentChoice{client.Party{Address: a}, slices.Contains(chosen, a)}
	}
	return out
}

// getRename answers the rename dialog, open on a room the viewer made, from the
// rooms the rail already shows: opening it runs nothing.
func (c *Chat) getRename(w http.ResponseWriter, r *http.Request) {
	room := c.shownRoom(r)
	if !room.Mine {
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
	for _, room := range cache.shown().Rooms {
		if room.Addr() == addr {
			c.name(r, parties(room.Members, room.Invited))
			return room
		}
	}
	service, thread, _ := splitRoom(addr)
	return c.tag([]roomRow{{ID: thread}}, service, "")[0]
}

// pageWatch is the chat watch a command's form carried, and what it shows: the
// room in view's, or the rail's on any other page.
func pageWatch(r *http.Request) (key, shown string) {
	for _, v := range []string{r.FormValue("watch"), r.FormValue("apart")} {
		if rest, ok := strings.CutPrefix(v, "chat:"); ok {
			key, shown, _ = strings.Cut(rest, ":")
			return key, shown
		}
	}
	return railKey, ""
}

// reply is a command's answer in place, which waits only for the chat the
// command ran on. On the page of a room of that chat, the room is brought to
// what it now holds, as a tick does, whatever the watch says is due, since the
// command may have moved nothing the signal counts and a watch that failed
// waits for its retry; the room's read carries its chat's rooms. Anywhere else
// the chat is listed afresh (relist) and the rail answers; a room in view on
// another chat is left to its own watch. A read that fails answers the room's
// watch failed, and the heartbeat retries.
func (c *Chat) reply(r *http.Request, chat, key, shown string) (template.HTML, error) {
	p, err := c.pulse(r)
	if err != nil {
		c.log().Warn("chat: open tasks", "err", err)
		if key == railKey {
			return "", nil
		}
		return c.deps.Fragment("_chat_watch", watchView{key, parseWatch(shown).failed()})
	}
	if inView, _, _ := splitRoom(key); key != railKey && chat == inView {
		return c.advance(r, key, parseWatch(shown), p, true, nil)
	}
	rail := c.relist(r, p, chat)
	if key == railKey && railDigest(c.drawn(rail)) == shown {
		return "", nil
	}
	return c.rail(r, rail, true)
}

// relist lists chat afresh and puts it as its entry in the session's copy,
// which it returns, so whatever is drawn next, in place or after a redirect,
// shows what a command on it did or a room found gone; it waits for that chat
// alone. A chat no longer the viewer's is dropped from the copy as the pulse
// says.
func (c *Chat) relist(r *http.Request, p pulse, chat string) railCache {
	signal := c.signal(c.deps.Me(r), p)
	fresh := map[string]chatRead{}
	if slices.Contains(p.Services, chat) {
		fresh[chat], _ = c.list(r, chat, signal)
	}
	return c.sidebar(r, p, signal, fresh, false)
}

// relistFirst relists chat ahead of a redirect: a command that leaves the page,
// or a room found gone, lands on a page drawn from a copy that knows it.
// Without the pulse the copy stays, and the heartbeat brings it.
func (c *Chat) relistFirst(r *http.Request, chat string) {
	if p, err := c.pulse(r); err == nil {
		c.relist(r, p, chat)
	}
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
		shown := c.tag([]roomRow{{ID: thread, Name: name}}, service, "")[0]
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
	if reason := refusal(err, c.shown(service)); reason != "" {
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
		h, err := c.deps.Fragment("_crumb", c.tag([]roomRow{out.Room}, service, c.principal(r))[0])
		if c.deps.HandleErrFragment(w, r, err) {
			return
		}
		closed += h
	}
	h, err := c.reply(r, service, key, st)
	if c.deps.HandleErrFragment(w, r, err) {
		return
	}
	writeHTML(w, closed+h)
}

func (c *Chat) postDeleteRoom(w http.ResponseWriter, r *http.Request) { c.drop(w, r, roomDeleteName) }

func (c *Chat) postLeave(w http.ResponseWriter, r *http.Request) { c.drop(w, r, roomLeaveName) }

// drop deletes or leaves a room. With script the rail answers in place, unless
// the page shows the room, which is then gone from under it; a room already gone
// is done. A page left for the landing finds the room's chat relisted, so the
// landing does not open the room again.
func (c *Chat) drop(w http.ResponseWriter, r *http.Request, action string) {
	room := r.PathValue("id")
	service, thread, _ := splitRoom(room)
	err := c.run(r, service, action, map[string]string{"thread_id": thread}, nil)
	if ke, ok := client.AsKernelError(err); ok && action == roomLeaveName && ke.UpstreamStatus() == http.StatusConflict {
		name := c.shownRoom(r).Name
		if read, err := c.readRoom(r, service, thread, latest, false); name == "" && err == nil {
			name = read.Room.Name
		}
		c.deps.FlashRedirect(w, r, back(r), "error", "You are its only member. Delete "+name+" instead.")
		return
	}
	if err != nil && !c.gone(r, service, thread, err, false) {
		c.then(w, r, err, "/chat")
		return
	}
	key, st := pageWatch(r)
	if !c.deps.IsHTMX(r) || key == room {
		c.relistFirst(r, service)
		c.deps.Redirect(w, r, "/chat")
		return
	}
	c.answer(w, r, service, key, st)
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
		Uninvited []client.Party `json:"uninvited"`
	}
	err := c.run(r, service, roomAddName, map[string]string{"thread_id": thread, "member": m}, &out)
	if reason := refusal(err, c.shown(service)); reason != "" {
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
	if note := c.undelivered(r, out.Uninvited); note != "" {
		c.deps.FlashRedirect(w, r, roomURL(r)+"?form=members", "info", note)
		return
	}
	c.deps.Redirect(w, r, roomURL(r)+"?form=members")
}

func (c *Chat) postRemoveMember(w http.ResponseWriter, r *http.Request) {
	service, thread, _ := splitRoom(r.PathValue("id"))
	args := map[string]string{"thread_id": thread, "member": strings.TrimSpace(r.FormValue("member"))}
	err := c.run(r, service, roomRemoveName, args, nil)
	if reason := refusal(err, c.shown(service)); reason != "" {
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
	_ = c.deps.RenderFragment(w, http.StatusOK, "_members", membersView{Room: c.shownRoom(r), Me: c.principal(r), Open: true, Error: reason, Typed: typed, CSRFField: c.deps.CSRFField(r)})
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

// postChat approves, ignores or disconnects a chat elsewhere, one that has
// addressed the viewer. Approve is connect, as the inbox's Approve of the same
// chat is (Approve). Disconnect deletes the grant and returns the chat to
// asking. A grant held on a kernel that does not answer, or answers late, is gone
// here at once and the disconnect goes on, and the page says that kernel still
// holds it, as Settings does. A failure is said
// on the page it was sent from; a page showing a room of a chat disconnected
// leaves for the landing.
func (c *Chat) postChat(w http.ResponseWriter, r *http.Request) {
	ctx, me := r.Context(), c.deps.Me(r)
	service, verb := r.PathValue("chat"), r.PathValue("verb")
	note, noChat := "", errors.New("no such chat")
	err := c.deps.Sessions.Do(ctx, func(tok string) error {
		reqs, err := c.deps.Client.ListRequesters(ctx, tok)
		if err != nil {
			return err
		}
		i := slices.IndexFunc(reqs, func(q client.Requester) bool { return c.learn(q.Requester) == service })
		if service == c.self() || !strings.HasPrefix(service, chatHandle+"@") || i < 0 {
			return noChat
		}
		// revoke returns the chat to pending, where To do lists
		// it; this kernel records it whoever is offline.
		revoke := func() error {
			err := c.deps.Client.DecideRequester(ctx, tok, service, "revoke")
			if ke, ok := client.AsKernelError(err); ok && ke.Unanswered() {
				return nil
			}
			return err
		}
		switch verb {
		case "approve":
			return c.connect(r, tok, reqs[i])
		case "ignore", "disconnect":
			if verb == "disconnect" {
				n, err := c.deps.Disconnect(r, service, c.shown(service))
				if ke, ok := client.AsKernelError(err); err != nil && (!ok || ke.Kind != client.KindNotFound) {
					return err
				}
				note = n
			}
			if reqs[i].Status != "pending" {
				if err := revoke(); err != nil {
					return err
				}
			}
			return c.deps.Ignore(r, reqs[i].Requester)
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
	if verb != "approve" && inView == service {
		to = "/chat"
	}
	if note != "" {
		c.deps.FlashRedirect(w, r, to, "info", note)
		return
	}
	if !c.deps.IsHTMX(r) || to != back(r) {
		c.deps.Redirect(w, r, to)
		return
	}
	c.answer(w, r, service, key, st)
}

// connect approves the chat elsewhere that q is and connects the viewer to it:
// rejoin, the key kept, or approve, join and connect, on the viewer's ordered
// path. One that fails leaves a chat that was pending as it was.
func (c *Chat) connect(r *http.Request, tok string, q client.Requester) error {
	ctx, service := r.Context(), c.learn(q.Requester)
	err := c.deps.Reconnect(r, service, func() error { return c.rejoin(ctx, tok, service, false) })
	if err != nil && q.Status == "pending" {
		_ = c.deps.Client.DecideRequester(ctx, tok, service, "revoke")
	}
	return err
}

// Approve is the inbox's Approve of the requester at address, by address or
// principal: a chat elsewhere is connected, as the rail's Approve connects it,
// so one decision has one effect wherever it is taken, and a failure is said
// as the rail says it. Any other requester is not handled here.
func (c *Chat) Approve(r *http.Request, address string) (handled bool, err error) {
	ctx, service := r.Context(), ""
	err = c.deps.Sessions.Do(ctx, func(tok string) error {
		handled = false
		reqs, err := c.deps.Client.ListRequesters(ctx, tok)
		if err != nil {
			return err
		}
		for _, q := range reqs {
			service = c.learn(q.Requester)
			if (q.Requester.Address == address || q.Requester.Principal == address) && service != c.self() && strings.HasPrefix(service, chatHandle+"@") {
				handled = true
				return c.connect(r, tok, q)
			}
		}
		return nil
	})
	switch {
	case handled && err == nil:
		c.changed(c.deps.Me(r))
	case handled:
		err = said{c.reason(service, err), err}
	}
	return handled, err
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
		c.relistFirst(r, service)
		c.deps.FlashRedirect(w, r, back(r), "error", "That invitation was withdrawn.")
	case verb == "accept":
		c.deps.Redirect(w, r, "/chat/room/"+url.PathEscape(room))
	case !c.deps.IsHTMX(r):
		c.relistFirst(r, service)
		c.deps.Redirect(w, r, back(r))
	default:
		key, st := pageWatch(r)
		c.answer(w, r, service, key, st)
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
	c.deps.FlashRedirect(w, r, back(r), "error", c.reason(service, err))
}

// reason is a failure of the chat at service as a page says it.
func (c *Chat) reason(service string, err error) string {
	reason := refusal(err, c.shown(service))
	var ke *client.KernelError
	if reason == "" && errors.As(err, &ke) {
		if reason = refusal(ke, c.shown(service)); reason == "" {
			reason = ke.UserMessage()
		}
	}
	if reason == "" {
		reason = err.Error()
	}
	return reason
}

// said is a failure as a page says it, the error it came from beneath.
type said struct {
	text string
	err  error
}

func (e said) Error() string { return e.text }
func (e said) Unwrap() error { return e.err }

// back is the page a command was sent from, as htmx names it, by its path
// alone; the landing without script.
func back(r *http.Request) string {
	if u, err := url.Parse(r.Header.Get("HX-Current-URL")); err == nil && strings.HasPrefix(u.Path, "/") && !strings.HasPrefix(u.Path, "//") {
		return u.Path
	}
	return "/chat"
}

// answer writes a command's reply in place on chat (reply).
func (c *Chat) answer(w http.ResponseWriter, r *http.Request, chat, key, shown string) {
	h, err := c.reply(r, chat, key, shown)
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
	if reason := refusal(err, c.shown(c.chatOf(r))); reason != "" {
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
