// Package chat is the conversation surface of juice-ui: rooms of people and
// agents, every member a kernel principal, every entry a JSON document.
//
// A post is a run of chat's action msg/post as the member, who the kernel
// identifies by the chat key it connected (delegated_bearer, injected on
// apps.CallerKeyHeader). While that call runs chat creates one kernel task
// per recipient — the members the message names with @, or every other
// member when it names nobody — carrying what the room said since that
// member's previous delivery; the kernel shows it to the member once the
// member has approved chat (U52). Completing the task is the answer: its
// onward, chat's reply action, appends the answer to the room. Rooms,
// membership and the key life cycle are the store's; the kernel holds no
// chat state beyond the tasks in flight.
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
	"crypto/subtle"
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
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/daios-ai/juice-ui/apps"
	"github.com/daios-ai/juice-ui/client"
)

//go:embed templates/*.html
var tmplFS embed.FS

// chatHandle is chat's own account; its address is Deps.Address(chatHandle).
const chatHandle = "chat"

// The actions, by name under chat@<kernel>/.
const (
	indexName    = "index"
	registerName = "register"
	validateName = "validate"
	postName     = "msg/post"
	replyName    = "reply"
)

// dbName is the store, in the component home; legacyDBName the previous
// application's, imported once and never written.
const (
	dbName       = "rooms.db"
	legacyDBName = "chat.db"
)

const chatIcon = template.HTML(`<svg class="ico" aria-hidden="true" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M21 15a2 2 0 0 1-2 2H7l-4 4V5a2 2 0 0 1 2-2h14a2 2 0 0 1 2 2z"/></svg>`)

// usage is what chat/index answers: the one place a member learns chat.
const usage = "Chat rooms for people and agents. To post, run chat@KERNEL/msg/post with {thread_id: the room, message: text or a JSON document}. " +
	"Name members with @, as in \"@critic, which is best?\": only the members you name receive the post; a post naming nobody reaches every other member. " +
	"You receive a post as a task carrying what the room said since you last heard from it; your answer to that task appears in the room and notifies nobody. " +
	"To ask a member something, post naming it. While you hold a task you may post once; then answer the task. " +
	"Joining: approve chat@KERNEL, run chat@KERNEL/register, complete its verification task, then connect chat@KERNEL/msg with the key it returned."

// Chat is the app.
type Chat struct {
	deps   apps.Deps
	store  *Store
	sa     *apps.ServiceAccount
	st     chatState
	dbPath string // test override; "" resolves from the home
}

type chatState struct {
	Password string `json:"password"`
	Secret   string `json:"secret"` // the bearer the private actions carry, proving the caller is the kernel
	AuthRev  string `json:"auth_rev"`
}

func New() *Chat { return &Chat{} }

func (c *Chat) Slug() string { return "chat" }

// Nav leads the sidebar: chat is where a session lands. The badge counts the
// rooms with unread entries, kept by the heartbeat.
func (c *Chat) Nav(me string, superuser bool) []apps.NavEntry {
	if me == "" {
		return nil
	}
	return []apps.NavEntry{{Slug: "chat", Label: "Chat", Href: "/chat", Icon: chatIcon, Match: "/chat", Live: true, Lead: true}}
}

func (c *Chat) Templates() fs.FS { return tmplFS }

func (c *Chat) Mount(mux *http.ServeMux, d apps.Deps) {
	c.deps = d
	mux.HandleFunc("GET /chat", d.RequireAuth(c.getIndex))
	mux.HandleFunc("POST /chat/connect", d.RequireAuth(c.postConnect))
	mux.HandleFunc("POST /chat/rooms", d.RequireAuth(c.postCreateRoom))
	mux.HandleFunc("GET /chat/room/{id}", d.RequireAuth(c.getRoom))
	mux.HandleFunc("GET /chat/room/{id}/entries", d.RequireAuth(c.getEntries))
	mux.HandleFunc("POST /chat/room/{id}/message", d.RequireAuth(c.postMessage))
	mux.HandleFunc("POST /chat/room/{id}/delete", d.RequireAuth(c.postDeleteRoom))
	mux.HandleFunc("POST /chat/room/{id}/leave", d.RequireAuth(c.postLeave))
	mux.HandleFunc("POST /chat/room/{id}/add-member", d.RequireAuth(c.postAddMember))
	mux.HandleFunc("POST /chat/room/{id}/remove-member", d.RequireAuth(c.postRemoveMember))
	d.Watch("chat", c.watch)
	// The kernel calls these, server to server, with no browser and no CSRF token.
	mux.Handle("/_kernel/chat/", http.StripPrefix("/_kernel/chat", http.HandlerFunc(c.kernel)))
}

// Start provisions the account and secret once, opens the store, and signs
// in as chat in the background, reconciling the actions when it does.
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
	c.store = store
	c.sa = apps.NewServiceAccount(c.deps, chatHandle, c.st.Password)
	return c.sa.Start(ctx, c.ensureActions)
}

func (c *Chat) Stop(ctx context.Context) error {
	if c.sa != nil {
		c.sa.Stop()
	}
	if c.store != nil {
		return c.store.Close()
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

func (c *Chat) self() string        { return c.deps.Address(chatHandle) }
func (c *Chat) msgSelector() string { return c.self() + "/msg" }

func (c *Chat) log() *slog.Logger {
	if c.deps.Logger != nil {
		return c.deps.Logger
	}
	return slog.Default()
}

// ---- the actions ----

// contract is one of chat's actions as the kernel should hold it.
type contract struct {
	name, title, desc, vis string
	auth                   json.RawMessage
	in, out                json.RawMessage
}

const (
	descr      = `"description":`
	anyMessage = `"message":{` + descr + `"The message: text, or a JSON document. Members are named with @."}`
)

// contracts are the five actions. Every schema is canonical: an object
// naming its properties is closed, and every property is described. The two
// onwards are private: the kernel checks an onward's visibility against the
// task's creator, chat, so a member completes the task without being able to
// run the action itself and speak as somebody else.
func (c *Chat) contracts() []contract {
	member := apps.CallerKeyHeader
	delegated, _ := json.Marshal(map[string]any{"scheme": "delegated_bearer", "config": map[string]any{"header": member, "template": "{token}"}})
	bearer, _ := json.Marshal(map[string]any{"scheme": "bearer", "secrets": map[string]any{"token": c.st.Secret}})
	obj := func(props string, required ...string) json.RawMessage {
		req := ""
		if len(required) > 0 {
			b, _ := json.Marshal(required)
			req = `,"required":` + string(b)
		}
		return json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{` + props + `}` + req + `}`)
	}
	return []contract{
		{name: indexName, title: "Chat", vis: "public",
			desc: "What chat is and how a member posts, receives and joins.",
			in:   obj(""), out: obj(`"usage":{"type":"string",` + descr + `"How to use chat."}`)},
		{name: registerName, title: "Join chat", vis: "public",
			desc: "Request a chat key for an address. The key activates when that address completes the verification task this creates; then connect it to chat@KERNEL/msg.",
			in:   obj(`"handle":{"type":"string",`+descr+`"The address to issue a key for, handle@kernel."}`, "handle"),
			out:  obj(`"api_key":{"type":"string",` + descr + `"The chat key; connect it with juice user connect chat@KERNEL/msg --token."},"task_id":{"type":"string",` + descr + `"The verification task sent to the address."}`)},
		{name: validateName, title: "Activate a chat key", vis: "private", auth: bearer,
			desc: "Activates a key; the onward of the verification task, run when the address completes it.",
			in:   obj(`"key_id":{"type":"string",`+descr+`"The registration the task names."}`, "key_id"),
			out:  obj(`"ok":{"type":"boolean",` + descr + `"True when the key is active."}`)},
		{name: postName, title: "Post to a chat room", vis: "public", auth: delegated,
			desc: "Post a message to a room you are a member of. The members named with @ receive it; a message naming nobody reaches every other member. See chat@KERNEL/index.",
			in:   obj(`"thread_id":{"type":"string",`+descr+`"The room."},`+anyMessage, "thread_id", "message"),
			out:  obj(`"delivered":{"type":"integer",` + descr + `"How many members received it."}`)},
		{name: replyName, title: "Answer in a chat room", vis: "private", auth: bearer,
			desc: "The onward of a delivery: completing the task appends the answer to the room.",
			in: obj(`"thread_id":{"type":"string",`+descr+`"The room."},"member":{"type":"string",`+descr+`"The member answering."},"delivery":{"type":"integer",`+descr+`"Which delivery this answers."},`+
				`"message":{`+descr+`"What the room said."},"answer":{"type":"string",`+descr+`"The answer, posted to the room."},`+
				`"status":{"type":"string",`+descr+`"Set when the member could not answer."},"reason":{"type":"string",`+descr+`"Why the member could not answer."}`,
				"thread_id", "member", "delivery", "message"),
			out: obj(`"ok":{"type":"boolean",` + descr + `"True when the answer was recorded."}`)},
	}
}

// authRev names the auth configuration pushed: the kernel never reads it
// back, so a change is forced through by a revision kept in app state.
func (c *Chat) authRev() string {
	h := sha256.New()
	for _, k := range c.contracts() {
		h.Write(k.auth)
	}
	return hex.EncodeToString(h.Sum(nil)[:8])
}

// ensureActions makes each action exist as its contract says, pointed at
// this interface, and deletes any other action chat owns. A changed contract
// is pushed whole and re-enabled, which revokes members' grants; they
// connect again from the rooms page. Failures accumulate so one does not
// starve the rest; the revision is recorded only after a clean pass.
func (c *Chat) ensureActions(ctx context.Context) error {
	rev := c.authRev()
	force := c.st.AuthRev != rev
	base := strings.TrimRight(c.deps.PublicURL, "/") + "/_kernel/chat/"
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
		for _, k := range c.contracts() {
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

// ---- the kernel's calls ----

// kernel answers the kernel's dispatch of chat's actions.
func (c *Chat) kernel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "POST only"})
		return
	}
	switch r.URL.Path {
	case "/" + indexName:
		writeJSON(w, http.StatusOK, map[string]any{"usage": strings.ReplaceAll(usage, "chat@KERNEL", c.self())})
	case "/" + registerName:
		c.serveRegister(w, r)
	case "/" + validateName:
		c.serveValidate(w, r)
	case "/" + postName:
		c.servePost(w, r)
	case "/" + replyName:
		c.serveReply(w, r)
	default:
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "no such action"})
	}
}

// serveRegister mints a pending key for the address and creates, through the
// capability of this very call, the verification task only that address can
// complete; its onward activates the key.
func (c *Chat) serveRegister(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Handle string `json:"handle"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || !strings.Contains(in.Handle, "@") {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "handle must be an address, handle@kernel"})
		return
	}
	cb, cap := r.Header.Get(client.CallbackHeader), r.Header.Get(client.CapabilityHeader)
	if cb == "" || cap == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "no capability: register is run through the kernel"})
		return
	}
	raw, keyID, err := c.store.RegisterKey(in.Handle)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	partial, _ := json.Marshal(map[string]string{"key_id": keyID})
	t, err := c.deps.Client.CallbackCreateTask(r.Context(), cb, cap, client.CreateTaskInput{
		Action: c.self() + "/" + validateName, RequiredCaller: in.Handle, PartialArgs: partial,
	})
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"ok": false, "error": "verification task: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"api_key": raw, "task_id": t.ID})
}

// fromKernel checks the bearer the private actions carry: the kernel injects
// it when it dispatches them, so the caller is the kernel completing a task.
func (c *Chat) fromKernel(w http.ResponseWriter, r *http.Request) bool {
	got := r.Header.Get("Authorization")
	if subtle.ConstantTimeCompare([]byte(got), []byte("Bearer "+c.st.Secret)) != 1 {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"ok": false, "error": "unauthorized"})
		return false
	}
	return true
}

func (c *Chat) serveValidate(w http.ResponseWriter, r *http.Request) {
	if !c.fromKernel(w, r) {
		return
	}
	var in struct {
		KeyID string `json:"key_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "bad request body"})
		return
	}
	if err := c.store.ActivateKey(in.KeyID); err != nil {
		writeJSON(w, statusFor(err), map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// servePost is a member's post: identified by the key the kernel injected,
// the entry is stored under the one-post rule, then delivered during this
// call, unless the hand-off cap is exceeded.
func (c *Chat) servePost(w http.ResponseWriter, r *http.Request) {
	member, ok := c.store.Member(r.Header.Get(apps.CallerKeyHeader))
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"ok": false, "error": "unknown chat key; join chat first"})
		return
	}
	var in struct {
		Room    string          `json:"thread_id"`
		Message json.RawMessage `json:"message"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "bad request body"})
		return
	}
	e, depth, err := c.store.Post(in.Room, member, in.Message)
	if err != nil {
		writeJSON(w, statusFor(err), map[string]any{"ok": false, "error": err.Error()})
		return
	}
	n := 0
	if depth <= handoffCap {
		room, err := c.store.room(in.Room)
		if err == nil {
			room.Members, err = c.store.members(in.Room)
		}
		if err != nil {
			writeJSON(w, statusFor(err), map[string]any{"ok": false, "error": err.Error()})
			return
		}
		n = c.deliver(r.Context(), r.Header.Get(client.CallbackHeader), r.Header.Get(client.CapabilityHeader),
			in.Room, e.Seq, depth, recipients(member, in.Message, room.Members))
	}
	writeJSON(w, http.StatusOK, map[string]any{"delivered": n})
}

// serveReply is a delivery's completion: the member no longer holds it, and
// its answer, or the reason it has none, goes into the room.
func (c *Chat) serveReply(w http.ResponseWriter, r *http.Request) {
	if !c.fromKernel(w, r) {
		return
	}
	var in struct {
		Room     string  `json:"thread_id"`
		Member   string  `json:"member"`
		Delivery int64   `json:"delivery"`
		Answer   *string `json:"answer"`
		Status   string  `json:"status"`
		Reason   string  `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "bad request body"})
		return
	}
	var body json.RawMessage
	switch {
	case in.Answer != nil && *in.Answer != "":
		body, _ = json.Marshal(*in.Answer)
	case in.Status != "":
		body, _ = json.Marshal(map[string]string{"status": in.Status, "reason": in.Reason})
	}
	if err := c.store.Answer(in.Member, in.Room, in.Delivery, body); err != nil {
		writeJSON(w, statusFor(err), map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func statusFor(err error) int {
	switch {
	case errors.Is(err, ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, ErrForbidden), errors.Is(err, ErrNotMember):
		return http.StatusForbidden
	case errors.Is(err, ErrBadInput):
		return http.StatusBadRequest
	case errors.Is(err, ErrHeld):
		return http.StatusConflict
	}
	return http.StatusInternalServerError
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// ---- the browser ----

type indexView struct {
	Connected bool
	Rooms     []Room
	Agents    []string // the installation's agents, offered as members
	OpenForm  bool
}

type roomView struct {
	Room     Room
	Me       string
	IsOwner  bool
	OpenForm string
	Entries  entriesView
}

// card is one entry with what its rendering needs: the body is text, a
// form (a JSON Schema), or any other value shown as the view.
type card struct {
	Entry  Entry
	RoomID string
	CSRF   template.HTML
	Text   string
	Schema json.RawMessage
}

func (k card) Kind() string {
	switch {
	case k.Schema != nil:
		return "form"
	case k.Text != "" || string(k.Entry.Body) == `""`:
		return "text"
	}
	return "value"
}

func makeCards(es []Entry, roomID string, csrf template.HTML) []card {
	out := make([]card, len(es))
	for i, e := range es {
		k := card{Entry: e, RoomID: roomID, CSRF: csrf}
		var s string
		if json.Unmarshal(e.Body, &s) == nil {
			k.Text = s
		} else if isSchema(e.Body) {
			k.Schema = e.Body
		}
		out[i] = k
	}
	return out
}

// isSchema recognizes a JSON Schema by its $schema key beside a type or
// properties: an entry that asks for something.
func isSchema(body json.RawMessage) bool {
	var m map[string]json.RawMessage
	if json.Unmarshal(body, &m) != nil || m["$schema"] == nil {
		return false
	}
	return m["type"] != nil || m["properties"] != nil
}

// entriesView backs the room's entries and the fragments that append to them.
type entriesView struct {
	RoomID string
	Cards  []card
	After  int64
	Error  string
}

func (c *Chat) getIndex(w http.ResponseWriter, r *http.Request) {
	if !c.connected(r) {
		c.deps.RenderPage(w, r, http.StatusOK, "chat", "Chat", indexView{})
		return
	}
	rooms, err := c.store.Rooms(c.deps.Me(r))
	if c.deps.HandleErr(w, r, err) {
		return
	}
	c.deps.RenderPage(w, r, http.StatusOK, "chat", "Chat", indexView{
		Connected: true, Rooms: rooms, Agents: c.agents(), OpenForm: r.URL.Query().Get("form") == "new",
	})
}

// agents lists the installation's agent logins, from their homes under
// $JUICE_HOME/agents (ecosystem-standard §2), so a room can take them in.
func (c *Chat) agents() []string {
	if c.deps.Home == "" {
		return nil
	}
	dirs, err := os.ReadDir(filepath.Join(filepath.Dir(c.deps.Home), "agents"))
	if err != nil {
		return nil
	}
	var out []string
	for _, d := range dirs {
		if d.IsDir() && strings.Contains(d.Name(), "@") {
			out = append(out, d.Name())
		}
	}
	sort.Strings(out)
	return out
}

// connected reports whether the viewer holds the chat/msg grant, read from
// the kernel's own connectors: the state `juice user connect` creates.
func (c *Chat) connected(r *http.Request) bool {
	ok := false
	_ = c.deps.Sessions.Do(r.Context(), func(tok string) error {
		me, err := c.deps.Client.Me(r.Context(), tok)
		if err != nil {
			return err
		}
		sel := c.msgSelector()
		for _, con := range me.Connectors {
			if con.Directory == sel {
				ok = true
			}
			for _, g := range con.Actions {
				if strings.HasPrefix(g.Action, sel+"/") {
					ok = true
				}
			}
		}
		return nil
	})
	return ok
}

// postConnect joins the viewer as any member joins: approve chat as a
// requester, register, complete the verification task, connect the key.
func (c *Chat) postConnect(w http.ResponseWriter, r *http.Request) {
	if c.connected(r) {
		http.Redirect(w, r, "/chat", http.StatusSeeOther)
		return
	}
	me := c.deps.Me(r)
	stage := "approve"
	err := c.deps.Sessions.Do(r.Context(), func(tok string) error {
		if err := c.deps.Client.DecideRequester(r.Context(), tok, c.self(), "approve"); err != nil {
			return err
		}
		stage = "register"
		args, _ := json.Marshal(map[string]string{"handle": me})
		reply, err := c.deps.Client.Run(r.Context(), tok, c.self()+"/"+registerName, args, "")
		if err != nil {
			return err
		}
		var res struct {
			APIKey string `json:"api_key"`
			TaskID string `json:"task_id"`
		}
		if err := json.Unmarshal(reply.Result, &res); err != nil || res.APIKey == "" || res.TaskID == "" {
			return fmt.Errorf("register returned no key")
		}
		stage = "verification"
		if _, err := c.deps.Client.CompleteTask(r.Context(), tok, res.TaskID, nil); err != nil {
			return err
		}
		stage = "connect"
		_, err = c.deps.Client.ConnectGrant(r.Context(), tok, c.msgSelector(), "", res.APIKey)
		return err
	})
	if err != nil && !c.connected(r) {
		detail := err.Error()
		if ke, ok := client.AsKernelError(err); ok {
			detail = ke.UserMessage()
		}
		c.deps.FlashRedirect(w, r, "/chat", "error", "Could not connect ("+stage+"): "+detail)
		return
	}
	http.Redirect(w, r, "/chat", http.StatusSeeOther)
}

func (c *Chat) getRoom(w http.ResponseWriter, r *http.Request) {
	if !c.connected(r) {
		http.Redirect(w, r, "/chat", http.StatusSeeOther)
		return
	}
	me, id := c.deps.Me(r), r.PathValue("id")
	room, es, err := c.store.Room(me, id)
	if err != nil {
		c.deps.HandleErr(w, r, &client.KernelError{Kind: client.KindNotFound})
		return
	}
	c.seen(r, me, id, maxSeq(es))
	csrf := c.deps.CSRFField(r)
	c.deps.RenderPage(w, r, http.StatusOK, "room", "Chat", roomView{
		Room: room, Me: me, IsOwner: room.Creator == me, OpenForm: r.URL.Query().Get("form"),
		Entries: entriesView{RoomID: id, Cards: makeCards(es, id, csrf), After: maxSeq(es)},
	})
}

// seen records the viewer has the room in view: the read cursor moves and
// the viewer's waiting deliveries for it are completed, with nothing to say.
func (c *Chat) seen(r *http.Request, me, roomID string, upto int64) {
	_ = c.store.MarkRead(me, roomID, upto)
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
	_ = c.deps.RenderFragment(w, http.StatusOK, "_chat_poll", c.tail(r, r.PathValue("id"), after))
}

// tail is the room's entries after seq, with the cursor to show next.
func (c *Chat) tail(r *http.Request, roomID string, after int64) entriesView {
	me := c.deps.Me(r)
	es, _ := c.store.After(me, roomID, after)
	if n := maxSeq(es); n > after {
		after = n
	}
	return entriesView{RoomID: roomID, Cards: makeCards(es, roomID, c.deps.CSRFField(r)), After: after}
}

// watch answers the heartbeat: the badge with the count of rooms holding
// something unread, and a room with its entries after the shown seq. A room
// in a visible tab is seen.
func (c *Chat) watch(r *http.Request, key, shown string) (template.HTML, error) {
	me := c.deps.Me(r)
	if key == "badge" {
		rooms, _ := c.store.Rooms(me)
		n := 0
		for _, room := range rooms {
			if room.Unread {
				n++
			}
		}
		if strconv.Itoa(n) == shown {
			return "", nil
		}
		return apps.Badge("chat", "Chat", n), nil
	}
	after, _ := strconv.ParseInt(shown, 10, 64)
	v := c.tail(r, key, after)
	if len(v.Cards) == 0 {
		return "", nil
	}
	if r.Header.Get("X-Visible") != "0" {
		c.seen(r, me, key, v.After)
	}
	return c.deps.Fragment("_chat_tick", v)
}

// postMessage posts as the viewer: the composer's text as a JSON string, its
// JSON editor's document, or a filled form's value, through chat's own post
// action, so the browser and an agent take the same path.
func (c *Chat) postMessage(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := r.ParseForm(); err != nil {
		c.deps.HandleErr(w, r, err)
		return
	}
	var body json.RawMessage
	switch {
	case r.Form.Has("text"):
		if t := strings.TrimSpace(r.FormValue("text")); t != "" {
			body, _ = json.Marshal(t)
		}
	case r.Form.Has("has_fields"):
		var err error
		if body, err = c.deps.FormArgs(r); err != nil {
			c.composeFailed(w, r, id, err.Error())
			return
		}
	default:
		raw := strings.TrimSpace(r.FormValue("raw_args"))
		if raw != "" && !json.Valid([]byte(raw)) {
			c.composeFailed(w, r, id, "Not valid JSON.")
			return
		}
		body = json.RawMessage(raw)
	}
	if len(body) > 0 {
		args, _ := json.Marshal(map[string]any{"thread_id": id, "message": body})
		err := c.deps.Sessions.Do(r.Context(), func(tok string) error {
			_, e := c.deps.Client.Run(r.Context(), tok, c.self()+"/"+postName, args, "")
			return e
		})
		if err != nil {
			if reason := reasonOf(err); reason != "" {
				c.composeFailed(w, r, id, reason)
				return
			}
			if c.deps.HandleErr(w, r, err) {
				return
			}
		}
	}
	if c.deps.IsHTMX(r) {
		after, _ := strconv.ParseInt(r.FormValue("after"), 10, 64)
		_ = c.deps.RenderFragment(w, http.StatusOK, "_chat_poll", c.tail(r, id, after))
		return
	}
	http.Redirect(w, r, "/chat/room/"+url.PathEscape(id), http.StatusSeeOther)
}

// composeFailed shows why a post was refused, beside the composer.
func (c *Chat) composeFailed(w http.ResponseWriter, r *http.Request, roomID, reason string) {
	if c.deps.IsHTMX(r) {
		_ = c.deps.RenderFragment(w, http.StatusOK, "_chat_refused", entriesView{RoomID: roomID, Error: reason})
		return
	}
	c.deps.FlashRedirect(w, r, "/chat/room/"+url.PathEscape(roomID), "error", reason)
}

// reasonOf is chat's own refusal carried back through the kernel as an
// execution failure, or "" for anything else.
func reasonOf(err error) string {
	ke, ok := client.AsKernelError(err)
	if !ok || ke.Kind != client.KindExecutionFailed {
		return ""
	}
	var body struct{ Error string }
	if i := strings.Index(ke.KernelMessage, "{"); i < 0 || json.Unmarshal([]byte(ke.KernelMessage[i:]), &body) != nil {
		return ""
	}
	return body.Error
}

func (c *Chat) postCreateRoom(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		c.deps.FlashRedirect(w, r, "/chat", "error", "A room needs a name.")
		return
	}
	members := append(r.Form["agent"], strings.Fields(strings.ReplaceAll(r.FormValue("members"), ",", " "))...)
	for _, m := range members {
		if !strings.Contains(m, "@") {
			c.deps.FlashRedirect(w, r, "/chat", "error", m+" is not an address (handle@kernel).")
			return
		}
	}
	room, err := c.store.CreateRoom(c.deps.Me(r), name, members)
	if c.deps.HandleErr(w, r, err) {
		return
	}
	http.Redirect(w, r, "/chat/room/"+url.PathEscape(room.ID), http.StatusSeeOther)
}

func (c *Chat) postDeleteRoom(w http.ResponseWriter, r *http.Request) {
	c.then(w, r, c.store.DeleteRoom(c.deps.Me(r), r.PathValue("id")), "/chat")
}

func (c *Chat) postLeave(w http.ResponseWriter, r *http.Request) {
	c.then(w, r, c.store.Leave(c.deps.Me(r), r.PathValue("id")), "/chat")
}

func (c *Chat) postAddMember(w http.ResponseWriter, r *http.Request) {
	m := strings.TrimSpace(r.FormValue("member"))
	if !strings.Contains(m, "@") {
		c.deps.FlashRedirect(w, r, roomURL(r), "error", "A member is an address, handle@kernel.")
		return
	}
	c.then(w, r, c.store.AddMember(c.deps.Me(r), r.PathValue("id"), m), roomURL(r))
}

func (c *Chat) postRemoveMember(w http.ResponseWriter, r *http.Request) {
	c.then(w, r, c.store.RemoveMember(c.deps.Me(r), r.PathValue("id"), strings.TrimSpace(r.FormValue("member"))), roomURL(r))
}

// then redirects after a store operation, a refusal as a flash.
func (c *Chat) then(w http.ResponseWriter, r *http.Request, err error, to string) {
	switch {
	case err == nil:
		http.Redirect(w, r, to, http.StatusSeeOther)
	case errors.Is(err, ErrNotFound):
		c.deps.HandleErr(w, r, &client.KernelError{Kind: client.KindNotFound})
	case statusFor(err) == http.StatusInternalServerError:
		c.deps.HandleErr(w, r, err)
	default:
		c.deps.FlashRedirect(w, r, to, "error", err.Error())
	}
}

func roomURL(r *http.Request) string { return "/chat/room/" + url.PathEscape(r.PathValue("id")) }

func maxSeq(es []Entry) int64 {
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
