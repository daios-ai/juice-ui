// Package daemon is the @chat chat server: a self-contained, Juice-agnostic REST
// service over a durable store (rooms, membership, an append-only message log,
// per-user read watermarks). It knows nothing of the kernel, tokens, or grants —
// caller authentication is *injected* (an auth hook handed to New), so the same
// service runs standalone behind any auth layer.
//
// Two route classes, two audiences:
//
//   - Kernel-facing writes (KernelHandler): fixed URLs, args in the JSON body; the
//     caller is whatever the injected auth hook returns for the request. These are
//     the @chat/* action sources. They enforce authorization (creator-only admin
//     ops, member-only posting) and return a real success or failure, synchronously.
//   - Admin (reached only through the in-process Client): the read/provision
//     surface — list rooms + unread, read a room, page history, mark read — gated by
//     the admin secret.
package daemon

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
)

// adminHeader carries the admin secret on the provisioning/read routes.
const adminHeader = "X-Chat-Admin"

// Op is one chat operation exposed as a kernel-facing write route at "/<Name>"
// and, correspondingly, registered by the plugin as the Juice action
// "@chat/<Name>". It is the action's full public contract: a natural-language
// Description plus JSON-Schema args and success response — written to be sufficient
// for @sys/lookup and LLM function calling (kernel requirements §3).
type Op struct {
	Name         string
	Description  string
	InputSchema  json.RawMessage
	OutputSchema json.RawMessage
}

// Success-response schemas. Business rejections are returned as non-2xx (a run
// failure, not output-validated), so the schemas describe only the 2xx body.
const (
	outOK   = `{"type":"object","properties":{"ok":{"type":"boolean","description":"True when the operation was applied."}}}`
	outRoom = `{"type":"object","properties":{"ok":{"type":"boolean","description":"True when the room was created."},"room_id":{"type":"string","description":"Id of the newly created room; use it to address the room in later calls."}}}`
	outMsg  = `{"type":"object","properties":{"ok":{"type":"boolean","description":"True when the message was posted."},"message_id":{"type":"string","description":"Id of the newly posted message; use it to delete the message later."}}}`
	// outRead is the read action's response: the room's recent messages, oldest first.
	outRead = `{"type":"object","properties":{` +
		`"ok":{"type":"boolean","description":"True on success."},` +
		`"messages":{"type":"array","description":"The room's recent messages, oldest first.","items":{"type":"object","properties":{` +
		`"id":{"type":"string","description":"Id of the message."},` +
		`"seq":{"type":"integer","description":"Per-room sequence number; pass the largest one back as `+"`after`"+` to fetch only newer messages next time."},` +
		`"from":{"type":"string","description":"Handle of the sender."},` +
		`"text":{"type":"string","description":"The message text."},` +
		`"at":{"type":"string","description":"RFC 3339 timestamp when it was posted."}}}}}}`
)

// Ops is the chat server's kernel-facing write surface — the single source of
// truth for the daemon's routes, the plugin's action registration, and the
// public action contract.
var Ops = []Op{
	{
		Name:         "create-room",
		Description:  "Create a chat room — a named conversation the caller owns — and add the given members alongside the caller. Returns the new room's id.",
		InputSchema:  json.RawMessage(`{"type":"object","properties":{"name":{"type":"string","description":"Display name for the room (for example: general)."},"members":{"type":"array","items":{"type":"string"},"description":"Handles of users to add as members besides the caller, for example @bob and @carol. May be empty."}}}`),
		OutputSchema: json.RawMessage(outRoom),
	},
	{
		Name:         "delete-room",
		Description:  "Delete a chat room for everyone. Only the room's owner may delete it.",
		InputSchema:  json.RawMessage(`{"type":"object","properties":{"room":{"type":"string","description":"Id of the room to delete."}}}`),
		OutputSchema: json.RawMessage(outOK),
	},
	{
		Name:         "add-member",
		Description:  "Add a user to a chat room. Only the room's owner may add members.",
		InputSchema:  json.RawMessage(`{"type":"object","properties":{"room":{"type":"string","description":"Id of the room."},"member":{"type":"string","description":"Handle of the user to add, for example @bob."}}}`),
		OutputSchema: json.RawMessage(outOK),
	},
	{
		Name:         "remove-member",
		Description:  "Remove a member from a chat room. Only the room's owner may remove members, and the owner cannot remove themselves.",
		InputSchema:  json.RawMessage(`{"type":"object","properties":{"room":{"type":"string","description":"Id of the room."},"member":{"type":"string","description":"Handle of the member to remove."}}}`),
		OutputSchema: json.RawMessage(outOK),
	},
	{
		Name:         "leave-room",
		Description:  "Leave a chat room you are a member of. The owner cannot leave; they delete the room instead.",
		InputSchema:  json.RawMessage(`{"type":"object","properties":{"room":{"type":"string","description":"Id of the room to leave."}}}`),
		OutputSchema: json.RawMessage(outOK),
	},
	{
		Name:         "message",
		Description:  "Post a message to a chat room. Only a member of the room may post. Returns the new message's id.",
		InputSchema:  json.RawMessage(`{"type":"object","properties":{"room":{"type":"string","description":"Id of the room to post to."},"text":{"type":"string","description":"The message text to post."}}}`),
		OutputSchema: json.RawMessage(outMsg),
	},
	{
		Name:         "delete-message",
		Description:  "Delete a message you sent. Only the message's original sender may delete it.",
		InputSchema:  json.RawMessage(`{"type":"object","properties":{"room":{"type":"string","description":"Id of the room the message is in."},"message":{"type":"string","description":"Id of the message to delete (as returned by the message action)."}}}`),
		OutputSchema: json.RawMessage(outOK),
	},
	{
		Name:         "read",
		Description:  "Read recent messages in a chat room you are a member of, oldest first. Poll this after a chat notification to see what was said. Pass `after` (a message seq) to fetch only messages newer than it; omit it for the recent window.",
		InputSchema:  json.RawMessage(`{"type":"object","properties":{"room":{"type":"string","description":"Id of the room to read."},"after":{"type":"integer","description":"Return only messages with a seq greater than this (from a prior read); omit or 0 for the recent window."}}}`),
		OutputSchema: json.RawMessage(outRead),
	},
}

// Auth identifies the caller of a kernel-facing request (returning ok=false when
// it can't). It is injected so the daemon stays agnostic about *how* callers are
// authenticated (juice-ui hands it a stateless-token verifier; a standalone
// deployment supplies its own).
type Auth func(r *http.Request) (handle string, ok bool)

// NotifyEvent describes a just-committed message so an injected Notifier can
// deliver a doorbell to each recipient. Members includes the sender; the plugin
// excludes it.
type NotifyEvent struct {
	RoomID   string
	RoomName string
	Sender   string
	Members  []string
}

// Notifier is called after a message commits so the plugin can ring each member
// (in juice-ui, by minting a Juice step). It is injected — like Auth — so the
// daemon stays Juice-agnostic: it invokes the hook and makes no kernel call
// itself. A standalone deployment passes nil.
type Notifier func(ev NotifyEvent)

// Daemon is the running chat server: the store, an injected caller authenticator,
// and its two route sets.
type Daemon struct {
	store       *Store
	adminSecret string
	auth        Auth
	notify      Notifier
	kernelMux   *http.ServeMux
	adminMux    *http.ServeMux
}

// New wraps an open store into a chat server. auth authenticates callers on the
// kernel-facing write routes; adminSecret gates the admin read routes; notify (may
// be nil) is invoked after each successful post to deliver doorbells.
func New(store *Store, adminSecret string, auth Auth, notify Notifier) *Daemon {
	d := &Daemon{store: store, adminSecret: adminSecret, auth: auth, notify: notify}
	d.routes()
	return d
}

// Close releases the store.
func (d *Daemon) Close() error { return d.store.Close() }

// KernelHandler is the kernel-facing write surface, mounted by the plugin under
// the CSRF-exempt /_kernel/chat/ prefix so the kernel can call it server-to-server.
func (d *Daemon) KernelHandler() http.Handler { return d.kernelMux }

// kargs holds the decoded fields across all write actions (only the relevant
// ones are set per action).
type kargs struct {
	Name    string   `json:"name"`
	Members []string `json:"members"`
	Room    string   `json:"room"`
	Text    string   `json:"text"`
	Member  string   `json:"member"`
	Message string   `json:"message"`
	After   int64    `json:"after"`
}

func (d *Daemon) routes() {
	d.kernelMux = http.NewServeMux()
	d.kernelMux.HandleFunc("POST /create-room", d.write(func(caller string, a kargs) (map[string]any, error) {
		r, err := d.store.CreateRoom(caller, a.Name, a.Members)
		return map[string]any{"ok": true, "room_id": r.ID}, err
	}))
	d.kernelMux.HandleFunc("POST /delete-room", d.write(func(caller string, a kargs) (map[string]any, error) {
		return okBody(), d.store.DeleteRoom(caller, a.Room)
	}))
	d.kernelMux.HandleFunc("POST /add-member", d.write(func(caller string, a kargs) (map[string]any, error) {
		return okBody(), d.store.AddMember(caller, a.Room, a.Member)
	}))
	d.kernelMux.HandleFunc("POST /remove-member", d.write(func(caller string, a kargs) (map[string]any, error) {
		return okBody(), d.store.RemoveMember(caller, a.Room, a.Member)
	}))
	d.kernelMux.HandleFunc("POST /leave-room", d.write(func(caller string, a kargs) (map[string]any, error) {
		return okBody(), d.store.LeaveRoom(caller, a.Room)
	}))
	d.kernelMux.HandleFunc("POST /message", d.write(func(caller string, a kargs) (map[string]any, error) {
		m, err := d.store.PostMessage(caller, a.Room, a.Text)
		if err == nil {
			d.fireNotify(caller, a.Room)
		}
		return map[string]any{"ok": true, "message_id": m.ID}, err
	}))
	d.kernelMux.HandleFunc("POST /delete-message", d.write(func(caller string, a kargs) (map[string]any, error) {
		return okBody(), d.store.DeleteMessage(caller, a.Room, a.Message)
	}))
	d.kernelMux.HandleFunc("POST /read", d.write(func(caller string, a kargs) (map[string]any, error) {
		msgs, err := d.store.Messages(caller, a.Room, a.After)
		if err != nil {
			return nil, err
		}
		if msgs == nil {
			msgs = []Message{}
		}
		return map[string]any{"ok": true, "messages": msgs}, nil
	}))

	d.adminMux = http.NewServeMux()
	d.adminMux.HandleFunc("GET /admin/rooms", d.admin(d.listRooms))
	d.adminMux.HandleFunc("GET /admin/rooms/{id}", d.admin(d.getRoom))
	d.adminMux.HandleFunc("GET /admin/rooms/{id}/history", d.admin(d.getHistory))
	d.adminMux.HandleFunc("POST /admin/rooms/{id}/notify/reserve", d.admin(d.reserveNotification))
	d.adminMux.HandleFunc("GET /admin/rooms/{id}/notify", d.admin(d.getNotification))
	d.adminMux.HandleFunc("PUT /admin/rooms/{id}/notify", d.admin(d.setNotification))
	d.adminMux.HandleFunc("DELETE /admin/rooms/{id}/notify", d.admin(d.clearNotification))
}

// fireNotify builds a NotifyEvent from the room's live membership and hands it to
// the injected Notifier (if any). Best-effort: a lookup failure or nil hook simply
// delivers no doorbell — the message is already committed and the watermark-based
// unread count is unaffected.
func (d *Daemon) fireNotify(sender, roomID string) {
	if d.notify == nil {
		return
	}
	name, members, err := d.store.Recipients(roomID)
	if err != nil {
		return
	}
	d.notify(NotifyEvent{RoomID: roomID, RoomName: name, Sender: sender, Members: members})
}

// ---- kernel-facing writes ----

// write adapts a store operation into an authenticated handler: identify the
// caller via the injected auth hook, decode args, apply, and render a real success
// or typed failure.
func (d *Daemon) write(op func(caller string, a kargs) (map[string]any, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		caller, ok := d.auth(r)
		if !ok {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"ok": false, "error": "unauthenticated caller"})
			return
		}
		var a kargs
		if err := json.NewDecoder(r.Body).Decode(&a); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "bad request body"})
			return
		}
		body, err := op(caller, a)
		if err != nil {
			writeJSON(w, statusFor(err), map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, body)
	}
}

// ---- admin (provisioning + reads) ----

// admin gates a handler on the admin secret (constant-time compare).
func (d *Daemon) admin(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		got := r.Header.Get(adminHeader)
		if subtle.ConstantTimeCompare([]byte(got), []byte(d.adminSecret)) != 1 {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
			return
		}
		h(w, r)
	}
}

func (d *Daemon) listRooms(w http.ResponseWriter, r *http.Request) {
	rooms, err := d.store.RoomsFor(r.URL.Query().Get("handle"))
	if err != nil {
		writeErr(w, err)
		return
	}
	if rooms == nil {
		rooms = []Room{}
	}
	writeJSON(w, http.StatusOK, rooms)
}

func (d *Daemon) getRoom(w http.ResponseWriter, r *http.Request) {
	room, msgs, err := d.store.Room(r.URL.Query().Get("handle"), r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	if msgs == nil {
		msgs = []Message{}
	}
	writeJSON(w, http.StatusOK, roomView{Room: room, Messages: msgs})
}

func (d *Daemon) getHistory(w http.ResponseWriter, r *http.Request) {
	before, _ := strconv.ParseInt(r.URL.Query().Get("before"), 10, 64)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	msgs, err := d.store.History(r.URL.Query().Get("handle"), r.PathValue("id"), before, limit)
	if err != nil {
		writeErr(w, err)
		return
	}
	if msgs == nil {
		msgs = []Message{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"messages": msgs})
}

func (d *Daemon) reserveNotification(w http.ResponseWriter, r *http.Request) {
	ok, err := d.store.ReserveNotification(r.URL.Query().Get("handle"), r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"reserved": ok})
}

func (d *Daemon) getNotification(w http.ResponseWriter, r *http.Request) {
	ref, err := d.store.GetNotification(r.URL.Query().Get("handle"), r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ref": ref})
}

func (d *Daemon) setNotification(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Ref string `json:"ref"`
	}
	_ = json.NewDecoder(r.Body).Decode(&in)
	if err := d.store.SetNotificationRef(r.URL.Query().Get("handle"), r.PathValue("id"), in.Ref); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, okBody())
}

func (d *Daemon) clearNotification(w http.ResponseWriter, r *http.Request) {
	if err := d.store.ClearNotification(r.URL.Query().Get("handle"), r.PathValue("id")); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, okBody())
}

// roomView is the admin read shape for a single room.
type roomView struct {
	Room     Room      `json:"room"`
	Messages []Message `json:"messages"`
}

// ---- helpers ----

func okBody() map[string]any { return map[string]any{"ok": true} }

func statusFor(err error) int {
	switch {
	case errors.Is(err, ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, ErrForbidden), errors.Is(err, ErrNotMember):
		return http.StatusForbidden
	case errors.Is(err, ErrBadInput):
		return http.StatusBadRequest
	default:
		return http.StatusInternalServerError
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeErr renders a store error at its mapped status — the shared tail of the
// admin read handlers.
func writeErr(w http.ResponseWriter, err error) {
	writeJSON(w, statusFor(err), map[string]any{"error": err.Error()})
}
