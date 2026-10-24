// Package chat is the conversation surface of juice-ui: rooms of people and
// agents, every member a kernel principal, every entry a JSON document.
//
// A post is a run of chat's action msg/post as the member, who the kernel
// identifies by the chat key it connected (delegated_bearer, injected on
// apps.CallerKeyHeader). While that call runs chat creates one kernel task
// per recipient — the member whose address is the message's first word, or
// every other member — carrying what the room said since that member's
// previous delivery; the kernel shows it to the member once the member has
// approved chat (U52). Completing the task is the answer: its onward, chat's
// reply action, settles it in the room. Rooms, membership and the key life
// cycle are the store's; the kernel holds no chat state beyond the tasks in
// flight.
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
	"crypto/subtle"
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

	"github.com/daios-ai/juice-ui/apps"
	"github.com/daios-ai/juice-ui/client"
)

//go:embed templates/*.html
var tmplFS embed.FS

// chatHandle is chat's own account; its address is Deps.Address(chatHandle).
const chatHandle = "chat"

// assistantHandle is the agent a session lands in conversation with; the
// agents app makes it.
const assistantHandle = "assistant"

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

// usage is what chat/index answers: the one place a member learns chat.
// KERNEL is replaced by this kernel's name.
const usage = "Chat rooms for people and agents; every member is an address, handle@KERNEL. " +
	"You receive a post as a task carrying what the room said since your previous task. Answer by completing the task: your answer appears in the room. " +
	"To ask another member something, run chat@KERNEL/msg/post with {thread_id: the room, message: your text} and write that member's address as the first word, " +
	"as in \"critic@KERNEL, which is best?\"; only that member receives it. A post that starts with no address reaches every other member. " +
	"While you hold tasks you may post once per task; then complete them. " +
	"Members of this installation are joined by its UI. A member elsewhere joins by approving chat@KERNEL as a requester, running chat@KERNEL/register with its own address, " +
	"completing the verification task, and connecting chat@KERNEL/msg with the key returned."

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
	mux.HandleFunc("POST /chat/room/{id}/delete", d.RequireAuth(c.postDeleteRoom))
	mux.HandleFunc("POST /chat/room/{id}/leave", d.RequireAuth(c.postLeave))
	mux.HandleFunc("POST /chat/room/{id}/add-member", d.RequireAuth(c.postAddMember))
	mux.HandleFunc("POST /chat/room/{id}/remove-member", d.RequireAuth(c.postRemoveMember))
	d.Watch("chat", c.watch)
	// The kernel calls these, server to server, with no browser and no CSRF token.
	mux.Handle("/_kernel/chat/", http.StripPrefix("/_kernel/chat", http.HandlerFunc(c.kernel)))
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
	c.store = store
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
	descr       = `"description":`
	textMessage = `"message":{"type":"string",` + descr + `"The text. A member's address as the first word, as in \"critic@KERNEL, which is best?\", reaches that member only; any other text reaches every other member."}`
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
	ks := []contract{
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
			desc: "Speak in a chat room you are a member of, for instance to ask another member something: begin with that member's address, as in \"critic@KERNEL, which is best?\", and only that member receives it; text that begins otherwise reaches every other member. Use this, not a direct message, to reach a member of the room. A task from chat is answered by completing it, not by posting. See chat@KERNEL/index.",
			in:   obj(`"thread_id":{"type":"string",`+descr+`"The room."},`+textMessage, "thread_id", "message"),
			out:  obj(`"delivered":{"type":"integer",` + descr + `"How many members received it."}`)},
		{name: replyName, title: "Answer in a chat room", vis: "private", auth: bearer,
			desc: "The onward of a delivery: completing the task settles the answer in the room.",
			in: obj(`"thread_id":{"type":"string",`+descr+`"The room."},"member":{"type":"string",`+descr+`"The member answering."},"delivery":{"type":"integer",`+descr+`"Which delivery this answers."},`+
				`"message":{`+descr+`"What the room said."},"answer":{"type":"string",`+descr+`"The answer, posted to the room."},`+
				`"status":{"type":"string",`+descr+`"Set when the member could not answer."},"reason":{"type":"string",`+descr+`"Why the member could not answer."}`,
				"thread_id", "member", "delivery", "message"),
			out: obj(`"ok":{"type":"boolean",` + descr + `"True when the answer was recorded."}`)},
	}
	// Every text names this kernel where it says KERNEL.
	here := "@" + c.kernelName()
	for i := range ks {
		ks[i].desc = strings.ReplaceAll(ks[i].desc, "@KERNEL", here)
		ks[i].in = json.RawMessage(strings.ReplaceAll(string(ks[i].in), "@KERNEL", here))
		ks[i].out = json.RawMessage(strings.ReplaceAll(string(ks[i].out), "@KERNEL", here))
	}
	return ks
}

// postSchema is the post action's input schema, the composer's form.
func (c *Chat) postSchema() json.RawMessage {
	for _, k := range c.contracts() {
		if k.name == postName {
			return k.in
		}
	}
	return nil
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
// is pushed whole and re-enabled, which revokes members' grants; the UI
// joins its principals again (setup, and every chat page). Failures accumulate so one does not
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
		writeJSON(w, http.StatusOK, map[string]any{"usage": strings.ReplaceAll(usage, "@KERNEL", "@"+c.kernelName())})
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

// serveReply is a delivery's completion: the answer is the string the
// member gave; a completion with a status and no answer is a failure. The
// store settles it (rule 4).
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
	failed := false
	answer := ""
	if in.Answer != nil {
		answer = strings.TrimSpace(*in.Answer)
	}
	switch {
	case answer != "":
		body, _ = json.Marshal(*in.Answer)
	case in.Status != "":
		body, _ = json.Marshal(struct {
			Status string `json:"status"`
			Reason string `json:"reason"`
		}{in.Status, in.Reason})
		failed = true
	}
	if err := c.store.Answer(in.Member, in.Room, in.Delivery, body, failed); err != nil {
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

// ---- joining ----

// join makes member a chat member with its own session token: approve chat
// as a requester (so the kernel shows it chat's tasks, U52), mint and
// activate a key, and connect the key to chat's msg actions (so the kernel
// injects it on a post). The UI holds the session, so no verification task
// is needed: that is for members elsewhere, through register.
func (c *Chat) join(ctx context.Context, token, member string) error {
	if err := c.deps.Client.DecideRequester(ctx, token, c.self(), "approve"); err != nil {
		return fmt.Errorf("approve %s: %w", c.self(), err)
	}
	raw, keyID, err := c.store.RegisterKey(member)
	if err != nil {
		return err
	}
	if err := c.store.ActivateKey(keyID); err != nil {
		return err
	}
	if _, err := c.deps.Client.ConnectGrant(ctx, token, c.msgSelector(), "", raw); err != nil {
		return fmt.Errorf("connect %s: %w", c.msgSelector(), err)
	}
	return nil
}

// joinViewer joins the signed-in person when they hold no chat connector.
func (c *Chat) joinViewer(r *http.Request) error {
	return c.deps.Sessions.Do(r.Context(), func(tok string) error {
		me, err := c.deps.Client.Me(r.Context(), tok)
		if err != nil || me.HasConnector(c.msgSelector()) {
			return err
		}
		return c.join(r.Context(), tok, me.Address)
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
	if me.HasConnector(c.msgSelector()) {
		return nil
	}
	return c.join(ctx, cred["token"], login)
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

// ---- the browser ----

// railView backs the rail: the viewer's rooms, the one in view, and the
// new-conversation dialog with the installation's agents to choose from.
type railView struct {
	Rooms     []Room
	Digest    string // what the heartbeat compares the rail by
	Current   string
	Open      bool
	Agents    []string
	Path      string
	CSRFField template.HTML
	OOB       bool // the heartbeat's out-of-band answer: the rail alone, replacing the one shown
}

// roomView backs the room page. The composer is the form of Post, the post
// action's input schema, with Locked, the room, sealed by the page's address.
type roomView struct {
	Room     Room
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
	Entries []Entry
	Removed []int64
	After   int64
	Working []string // the members holding an unanswered delivery here
	Error   string
}

// Answering names who the room is waiting on: "a is answering", "a and b
// are answering", "a, b and c are answering"; "" when nobody.
func (v entriesView) Answering() string {
	switch n := len(v.Working); n {
	case 0:
		return ""
	case 1:
		return v.Working[0] + " is answering"
	default:
		return strings.Join(v.Working[:n-1], ", ") + " and " + v.Working[n-1] + " are answering"
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
		room, err := c.store.Direct(me, assistant)
		if c.deps.HandleErr(w, r, err) {
			return
		}
		to := "/chat/room/" + url.PathEscape(room.ID)
		if err := c.joinAgent(r.Context(), assistant); err != nil {
			c.deps.FlashRedirect(w, r, to, "error", joinFailed(assistant, err))
			return
		}
		http.Redirect(w, r, to, http.StatusSeeOther)
		return
	}
	rooms, err := c.store.Rooms(me)
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
	me := c.deps.Me(r)
	rooms, err := c.store.Rooms(me)
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
	_, es, err := c.store.Room(me, roomID)
	if err != nil {
		return nil, err
	}
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
	room, es, err := c.store.Room(me, id)
	if err != nil {
		c.deps.HandleErr(w, r, &client.KernelError{Kind: client.KindNotFound})
		return
	}
	c.seen(r, me, id, maxSeq(es))
	locked, _ := json.Marshal(map[string]string{"thread_id": id})
	var others []string
	for _, m := range room.Members {
		if m != me {
			others = append(others, m)
		}
	}
	c.deps.RenderPage(w, r, http.StatusOK, "room", "Chat", roomView{
		Room: room, Me: me, Others: others, IsOwner: room.Creator == me, OpenForm: r.URL.Query().Get("form"),
		Entries: entriesView{RoomID: id, Entries: es, After: maxSeq(es), Working: c.working(id, me)},
		Post:    c.postSchema(), Locked: locked,
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

// tail is the room's entries after seq, the posts they replaced, and the
// cursor to show next.
func (c *Chat) tail(r *http.Request, roomID string, after int64) entriesView {
	me := c.deps.Me(r)
	es, gone, _ := c.store.After(me, roomID, after)
	if n := maxSeq(es); n > after {
		after = n
	}
	return entriesView{RoomID: roomID, Entries: es, Removed: gone, After: after, Working: c.working(roomID, me)}
}

// working is who is answering in a room: the members other than the viewer
// that hold an unanswered delivery there.
func (c *Chat) working(roomID, me string) []string {
	ws, err := c.store.Working(roomID, me)
	if err != nil {
		c.log().Warn("chat: working", "room", roomID, "err", err)
	}
	return ws
}

// watch answers the heartbeat: the rail when a room appeared, went or was
// read (shown is a digest of the rail it rendered), and a room with its
// entries after the shown seq and who is answering. A room in a visible tab
// is seen.
func (c *Chat) watch(r *http.Request, key, shown string) (template.HTML, error) {
	me := c.deps.Me(r)
	if key == railKey {
		rooms, err := c.store.Rooms(me)
		if err != nil {
			return "", err
		}
		if d := railDigest(rooms); d == shown {
			return "", nil
		}
		return c.rail(r, true)
	}
	after, _ := strconv.ParseInt(shown, 10, 64)
	v := c.tail(r, key, after)
	if len(v.Entries) > 0 && r.Header.Get("X-Visible") != "0" {
		c.seen(r, me, key, v.After)
	}
	if len(v.Entries) == 0 && len(v.Removed) == 0 {
		return c.deps.Fragment("_chat_working", v)
	}
	return c.deps.Fragment("_chat_tick", v)
}

// railKey is the rail's watch: a room id is three words, never this one.
const railKey = "rail"

// railDigest names the rail a page shows: its rooms, their names and which
// are unread, so the heartbeat swaps the rail only when one of them changed.
func railDigest(rooms []Room) string {
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
	body, _ := json.Marshal(fields)
	err = c.deps.Sessions.Do(r.Context(), func(tok string) error {
		_, e := c.deps.Client.Run(r.Context(), tok, c.self()+"/"+postName, body, "")
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
	if c.deps.IsHTMX(r) {
		after, _ := strconv.ParseInt(r.FormValue("after"), 10, 64)
		_ = c.deps.RenderFragment(w, http.StatusOK, "_chat_poll", c.tail(r, id, after))
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

// reasonOf is why a post was refused: the kernel's own reason for input its
// schema refuses, or chat's refusal carried back as an execution failure; ""
// for anything else.
func reasonOf(err error) string {
	ke, ok := client.AsKernelError(err)
	if ok && ke.Kind == client.KindInvalidInput {
		return ke.KernelMessage
	}
	if !ok || ke.Kind != client.KindExecutionFailed {
		return ""
	}
	var body struct{ Error string }
	if i := strings.Index(ke.KernelMessage, "{"); i < 0 || json.Unmarshal([]byte(ke.KernelMessage[i:]), &body) != nil {
		return ""
	}
	return body.Error
}

// postCreateRoom starts a conversation with the participants chosen: the
// agents ticked and the addresses typed, the viewer included. A name left
// blank is the participants' handles. The local agents among them are joined.
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
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		handles := make([]string, len(members))
		for i, m := range members {
			handles[i], _, _ = strings.Cut(m, "@")
		}
		name = strings.Join(handles, ", ")
	}
	room, err := c.store.CreateRoom(c.deps.Me(r), name, members)
	if c.deps.HandleErr(w, r, err) {
		return
	}
	c.joinMembers(w, r, members, "/chat/room/"+url.PathEscape(room.ID))
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
	if err := c.store.AddMember(c.deps.Me(r), r.PathValue("id"), m); err != nil {
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
