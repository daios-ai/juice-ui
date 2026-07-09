// Package chat is the juice-ui integration for the @chat application (see
// apps/chat/daemon and docs/apps.md) — the reference implementation of the app
// seam.
//
// The @chat application (apps/chat/daemon) is a self-contained Juice user that
// registers its own free (price-0) actions and projects its own transaction
// ledger into rooms and messages. This package runs it in-process and fronts it:
// it contributes the "chat" nav entry, the /chat pages, and templates; it reads
// rooms/messages straight from the app (in-process Go calls) and issues writes as
// the signed-in user via free `run @chat/*` calls (the kernel records the
// unforgeable caller, which the app attributes).
//
// The @chat account is provisioned on first run and its password is persisted in
// juice-ui.json via the seam's app config store — no chat environment variables.
// juice-ui core stays free of chat specifics; this package plugs in only through
// the app seam.
package chat

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"net/url"
	"strings"

	"github.com/daios-ai/juice-ui/apps"
	"github.com/daios-ai/juice-ui/apps/chat/daemon"
	"github.com/daios-ai/juice-ui/client"
)

//go:embed templates/*.html
var tmplFS embed.FS

// chatHandle is the application's service-account handle.
const chatHandle = "@chat"

// chatIcon is the plugin's own nav icon (a speech bubble), inline so no core
// sprite edit is needed.
const chatIcon = template.HTML(`<svg class="ico" aria-hidden="true" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M21 15a2 2 0 0 1-2 2H7l-4 4V5a2 2 0 0 1 2-2h14a2 2 0 0 1 2 2z"/></svg>`)

// Chat is the chat UI plugin. It owns the in-process @chat application.
type Chat struct {
	deps   apps.Deps
	daemon *daemon.Daemon
}

// New constructs the chat plugin. It needs no configuration — it provisions the
// @chat account itself on Start and reaches the kernel through Deps.
func New() *Chat { return &Chat{} }

func (c *Chat) Slug() string { return "chat" }

// Nav contributes the "chat" sidebar entry (with the plugin's own icon) to every
// signed-in user.
func (c *Chat) Nav(handle string, superuser bool) []apps.NavEntry {
	if handle == "" {
		return nil
	}
	return []apps.NavEntry{{Slug: "chat", Label: "chat", Href: "/chat", Icon: chatIcon, Match: "/chat"}}
}

func (c *Chat) Templates() fs.FS { return tmplFS }

// Mount registers the browser routes (auth-gated) and the kernel-facing ack
// endpoint (under /_kernel/, CSRF-exempt — the kernel calls it for each action).
func (c *Chat) Mount(mux *http.ServeMux, d apps.Deps) {
	c.deps = d
	mux.HandleFunc("GET /chat", d.RequireAuth(c.getIndex))
	mux.HandleFunc("GET /chat/room/{id}", d.RequireAuth(c.getRoom))
	mux.HandleFunc("GET /chat/room/{id}/messages", d.RequireAuth(c.getMessages))
	mux.HandleFunc("POST /chat/rooms", d.RequireAuth(c.postCreateRoom))
	mux.HandleFunc("POST /chat/room/{id}/message", d.RequireAuth(c.postMessage))
	mux.HandleFunc("POST /chat/room/{id}/rename", d.RequireAuth(c.postRename))
	mux.HandleFunc("POST /chat/room/{id}/delete", d.RequireAuth(c.postDeleteRoom))
	mux.HandleFunc("POST /chat/room/{id}/leave", d.RequireAuth(c.postLeave))
	mux.HandleFunc("POST /chat/room/{id}/add-member", d.RequireAuth(c.postAddMember))
	mux.HandleFunc("POST /chat/room/{id}/remove-member", d.RequireAuth(c.postRemoveMember))
	mux.HandleFunc("POST /chat/room/{id}/message/{mid}/delete", d.RequireAuth(c.postDeleteMessage))
	mux.HandleFunc("POST /_kernel/chat/ack", c.ack)
}

// chatState is the plugin's persisted config (its @chat account password).
type chatState struct {
	Password string `json:"password"`
}

// Start provisions the @chat account (once) and brings the in-process application
// up. On first run it generates and persists a password; thereafter it reuses the
// stored one. The application authenticates, registers its actions, and projects
// the ledger in the background, so juice-ui starts even if the kernel is briefly
// unavailable.
func (c *Chat) Start(ctx context.Context) error {
	var st chatState
	if raw, err := c.deps.LoadAppState(c.Slug()); err == nil && len(raw) > 0 {
		_ = json.Unmarshal(raw, &st)
	}
	if st.Password == "" {
		st.Password = newPassword()
		b, _ := json.Marshal(st)
		if err := c.deps.SaveAppState(c.Slug(), b); err != nil {
			return fmt.Errorf("chat: persist @chat credentials: %w", err)
		}
	}
	source := strings.TrimRight(c.deps.PublicURL, "/") + "/_kernel/chat/ack"
	c.daemon = daemon.New(c.deps.Client, chatHandle, st.Password, source)
	c.daemon.Start(ctx)
	return nil
}

// Stop halts the application.
func (c *Chat) Stop(ctx context.Context) error {
	if c.daemon != nil {
		c.daemon.Stop()
	}
	return nil
}

// ack is the kernel-facing endpoint every @chat action points at: a pure
// acknowledgement, since state derives from the ledger, not this callback.
func (c *Chat) ack(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"ok":true}`))
}

// ---- view models ----

type indexView struct {
	Rooms    []daemon.Room
	OpenForm bool
}

type roomView struct {
	Room     daemon.Room
	Me       string
	IsOwner  bool
	OpenForm string   // which ?form= modal to render open (no-JS path); "" = none
	Msgs     msgsView // the message list, also rendered on its own as an htmx fragment
}

// msgsView is the data for the _chat_messages partial, shared by the full page and
// the live-update fragment. CSRF is carried here because a fragment has no Page
// envelope.
type msgsView struct {
	RoomID   string
	Me       string
	Messages []daemon.Message
	CSRF     template.HTML
}

// ---- browser handlers ----

func (c *Chat) getIndex(w http.ResponseWriter, r *http.Request) {
	me := c.deps.Handle(r)
	c.deps.RenderPage(w, r, http.StatusOK, "chat", "chat", indexView{
		Rooms:    c.daemon.Rooms(me),
		OpenForm: r.URL.Query().Get("form") == "room-create",
	})
}

func (c *Chat) getRoom(w http.ResponseWriter, r *http.Request) {
	me := c.deps.Handle(r)
	room, msgs, ok := c.daemon.Room(r.PathValue("id"), me)
	if !ok {
		c.deps.HandleErr(w, r, &client.KernelError{Kind: client.KindNotFound})
		return
	}
	c.deps.RenderPage(w, r, http.StatusOK, "chat_conversation", "chat", roomView{
		Room: room, Me: me, IsOwner: room.Creator == me,
		OpenForm: r.URL.Query().Get("form"),
		Msgs:     msgsView{RoomID: room.ID, Me: me, Messages: msgs, CSRF: c.deps.CSRFField(r)},
	})
}

// getMessages renders just the message list, for the conversation page's htmx
// poll. Reads the in-process store (no kernel call); a non-member gets an empty
// list (no leak).
func (c *Chat) getMessages(w http.ResponseWriter, r *http.Request) {
	me := c.deps.Handle(r)
	id := r.PathValue("id")
	_, msgs, _ := c.daemon.Room(id, me)
	_ = c.deps.RenderFragment(w, http.StatusOK, "_chat_messages", msgsView{
		RoomID: id, Me: me, Messages: msgs, CSRF: c.deps.CSRFField(r),
	})
}

func (c *Chat) postCreateRoom(w http.ResponseWriter, r *http.Request) {
	name := roomName(r.FormValue("name"))
	if name == "" {
		c.deps.FlashRedirect(w, r, "/chat", "error", "A room needs a name.")
		return
	}
	reply, err := c.run(r, "create-room", map[string]any{"name": name, "members": parseHandles(r.FormValue("members"))})
	if c.deps.HandleErr(w, r, err) {
		return
	}
	http.Redirect(w, r, "/chat/room/"+url.PathEscape(reply.TxID), http.StatusSeeOther)
}

func (c *Chat) postMessage(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	text := strings.TrimSpace(r.FormValue("text"))
	if text != "" {
		_, err := c.run(r, "message", map[string]any{"room": id, "text": text})
		if c.deps.HandleErr(w, r, err) {
			return
		}
	}
	http.Redirect(w, r, "/chat/room/"+url.PathEscape(id), http.StatusSeeOther)
}

func (c *Chat) postRename(w http.ResponseWriter, r *http.Request) {
	c.opRedirect(w, r, "rename-room", map[string]any{"room": r.PathValue("id"), "name": roomName(r.FormValue("name"))})
}

func (c *Chat) postDeleteRoom(w http.ResponseWriter, r *http.Request) {
	if _, err := c.run(r, "delete-room", map[string]any{"room": r.PathValue("id")}); c.deps.HandleErr(w, r, err) {
		return
	}
	http.Redirect(w, r, "/chat", http.StatusSeeOther)
}

func (c *Chat) postLeave(w http.ResponseWriter, r *http.Request) {
	if _, err := c.run(r, "leave-room", map[string]any{"room": r.PathValue("id")}); c.deps.HandleErr(w, r, err) {
		return
	}
	http.Redirect(w, r, "/chat", http.StatusSeeOther)
}

func (c *Chat) postAddMember(w http.ResponseWriter, r *http.Request) {
	c.opRedirect(w, r, "add-member", map[string]any{"room": r.PathValue("id"), "member": strings.TrimSpace(r.FormValue("member"))})
}

func (c *Chat) postRemoveMember(w http.ResponseWriter, r *http.Request) {
	c.opRedirect(w, r, "remove-member", map[string]any{"room": r.PathValue("id"), "member": strings.TrimSpace(r.FormValue("member"))})
}

func (c *Chat) postDeleteMessage(w http.ResponseWriter, r *http.Request) {
	c.opRedirect(w, r, "delete-message", map[string]any{"room": r.PathValue("id"), "message": r.PathValue("mid")})
}

// ---- helpers ----

// run executes a @chat action as the signed-in user (free; the kernel records the
// caller for the application to attribute), then folds the new event in so it is
// visible on the redirect that follows.
func (c *Chat) run(r *http.Request, action string, args map[string]any) (client.CallReply, error) {
	var reply client.CallReply
	err := c.deps.Sessions.Do(r.Context(), func(tok string) error {
		var e error
		reply, e = c.deps.Client.Run(r.Context(), tok, "@chat/"+action, mustJSON(args))
		return e
	})
	if err == nil && c.daemon != nil {
		_ = c.daemon.Poll(r.Context())
	}
	return reply, err
}

// opRedirect runs an action against the current room and redirects back to it.
func (c *Chat) opRedirect(w http.ResponseWriter, r *http.Request, action string, args map[string]any) {
	if _, err := c.run(r, action, args); c.deps.HandleErr(w, r, err) {
		return
	}
	http.Redirect(w, r, "/chat/room/"+url.PathEscape(r.PathValue("id")), http.StatusSeeOther)
}

// roomName sanitizes a display room name (trimmed; empty rejected).
func roomName(s string) string { return strings.TrimSpace(s) }

// parseHandles splits a free-form member list into @handles.
func parseHandles(s string) []string {
	var out []string
	for _, f := range strings.FieldsFunc(s, func(r rune) bool { return r == ' ' || r == ',' || r == '\n' || r == '\t' || r == '\r' }) {
		if strings.HasPrefix(f, "@") {
			out = append(out, f)
		}
	}
	return out
}

// newPassword returns a strong random password for the @chat service account.
func newPassword() string {
	var b [24]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}
