package chat

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/daios-ai/juice-ui/client"
)

// The actions, by name under chat@<kernel>/.
const (
	indexName      = "index"
	registerName   = "register"
	validateName   = "validate"
	postName       = "msg/post"
	replyName      = "reply"
	roomCreateName = "room/create"
	roomRenameName = "room/rename"
	roomAddName    = "room/add"
	roomRemoveName = "room/remove"
	roomLeaveName  = "room/leave"
	roomDeleteName = "room/delete"
	roomListName   = "room/list"
	roomReadName   = "room/read"
)

// CallerKey is the header the kernel injects a member's chat key on.
const CallerKey = "X-Juice-Caller-Key"

// What a reply may hold. The kernel refuses a reply over 4 MiB (measured on
// v0.17.1), so a read stays well under it whatever the rooms hold.
const (
	maxMessage = 65536   // characters in one post or answer
	maxEntries = 1 << 20 // bytes of entries in one read
	maxRooms   = 1 << 18 // bytes of rooms in one read
)

// usage is what chat/index answers: the one place a member learns chat.
// KERNEL is replaced by this kernel's name.
const usage = "Chat rooms for people and agents; every member is an address, handle@KERNEL. " +
	"You receive a post as a task carrying what the room said since your previous task. Answer by completing the task: your answer appears in the room. " +
	"To ask another member something, run chat@KERNEL/msg/post with {thread_id: the room, message: your text} and write that member's address as the first word, " +
	"as in \"critic@KERNEL, which is best?\"; only that member receives it. A post that starts with no address reaches every other member. " +
	"While you hold tasks you may post once per task; then complete them. " +
	"Any member can start a room with chat@KERNEL/room/create, and leave one with chat@KERNEL/room/leave. " +
	"A member reads with chat@KERNEL/room/list and chat@KERNEL/room/read. " +
	"The room's creator can rename it with chat@KERNEL/room/rename, change its members with chat@KERNEL/room/add and chat@KERNEL/room/remove, and delete it with chat@KERNEL/room/delete. " +
	"Members of this installation are joined by its UI. A member elsewhere joins by approving chat@KERNEL as a requester, running chat@KERNEL/register with its own address, " +
	"completing the verification task, and connecting chat@KERNEL with the key returned."

// Service is the chat service: the HTTP endpoints the kernel wraps as the
// actions of each provider that registered it, and the store behind them. The
// first segment of a request's path is that provider, so one server serves
// several kernels with nothing kernel-specific in its configuration.
type Service struct {
	Store  *Store
	Secret string         // the bearer the private actions carry, proving the caller is the kernel
	Kernel *client.Client // creates tasks through the capability of a dispatch
	Log    *slog.Logger
}

func (s *Service) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

// Close releases the store.
func (s *Service) Close() error { return s.Store.Close() }

// named writes the provider and its kernel into a text saying chat@KERNEL.
func named(text, self string) string {
	_, kernel, _ := strings.Cut(self, "@")
	return strings.ReplaceAll(strings.ReplaceAll(text, "chat@KERNEL", self), "@KERNEL", "@"+kernel)
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
	textMessage = `"message":{"type":"string","maxLength":65536,` + descr + `"The text. A member's address as the first word, as in \"critic@KERNEL, which is best?\", reaches that member only; any other text reaches every other member."}`
	threadProp  = `"thread_id":{"type":"string",` + descr + `"The room."}`
	memberProp  = `"member":{"type":"string",` + descr + `"The member's address, handle@kernel."}`
	roomProp    = `"room":` + roomObj
	roomObj     = `{"type":"object","additionalProperties":false,` + descr + `"The room as it now stands.","properties":{` +
		threadProp + `,"name":{"type":"string",` + descr + `"The room's name."},"creator":{"type":"string",` + descr + `"The member that made the room."},` +
		`"members":{"type":"array",` + descr + `"The room's members.","items":{"type":"string"}},"created_at":{"type":"string",` + descr + `"When the room was made."},` +
		`"unread":{"type":"boolean",` + descr + `"Whether the room holds entries the caller has not read."},"last_at":{"type":"string",` + descr + `"When the room was last spoken in."}}}`
	okProp    = `"ok":{"type":"boolean",` + descr + `"True when it was done."}`
	roomsProp = `"rooms":{"type":"array",` + descr + `"The caller's rooms, newest activity first.","items":` + roomObj + `}`
)

// Contracts are chat's actions. Every schema is canonical: an object naming
// its properties is closed, and every property is described. The two onwards
// are private: the kernel checks an onward's visibility against the task's
// creator, chat, so a member completes the task without being able to run the
// action itself and speak as somebody else.
func contracts(self, secret string) []contract {
	delegated, _ := json.Marshal(map[string]any{"scheme": "delegated_bearer", "config": map[string]any{"header": CallerKey, "template": "{token}"}})
	bearer, _ := json.Marshal(map[string]any{"scheme": "bearer", "secrets": map[string]any{"token": secret}})
	obj := func(props string, required ...string) json.RawMessage {
		req := ""
		if len(required) > 0 {
			b, _ := json.Marshal(required)
			req = `,"required":` + string(b)
		}
		return json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{` + props + `}` + req + `}`)
	}
	roomOut, okOut := obj(roomProp), obj(okProp)
	ks := []contract{
		{name: indexName, title: "Chat", vis: "public",
			desc: "What chat is and how a member posts, receives and joins.",
			in:   obj(""), out: obj(`"usage":{"type":"string",` + descr + `"How to use chat."}`)},
		{name: registerName, title: "Join chat", vis: "public",
			desc: "Request a chat key for an address. The key activates when that address completes the verification task this creates; then connect it to chat@KERNEL.",
			in:   obj(`"handle":{"type":"string",`+descr+`"The address to issue a key for, handle@kernel."}`, "handle"),
			out:  obj(`"api_key":{"type":"string",` + descr + `"The chat key; connect it with juice user connect chat@KERNEL --token."},"task_id":{"type":"string",` + descr + `"The verification task sent to the address."}`)},
		{name: validateName, title: "Activate a chat key", vis: "private", auth: bearer,
			desc: "Activates a key; the onward of the verification task, run when the address completes it.",
			in:   obj(`"key_id":{"type":"string",`+descr+`"The registration the task names."}`, "key_id"),
			out:  obj(`"ok":{"type":"boolean",` + descr + `"True when the key is active."}`)},
		{name: postName, title: "Post to a chat room", vis: "public", auth: delegated,
			desc: "Speak in a chat room you are a member of, for instance to ask another member something: begin with that member's address, as in \"critic@KERNEL, which is best?\", and only that member receives it; text that begins otherwise reaches every other member. Use this, not a direct message, to reach a member of the room. A task from chat is answered by completing it, not by posting. See chat@KERNEL/index.",
			in:   obj(`"thread_id":{"type":"string",`+descr+`"The room."},`+textMessage, "thread_id", "message"),
			out: obj(`"delivered":{"type":"integer",` + descr + `"How many members received it."},` +
				`"undelivered":{"type":"array",` + descr + `"The members it could not be delivered to, by address.","items":{"type":"string"}}`)},
		{name: replyName, title: "Answer in a chat room", vis: "private", auth: bearer,
			desc: "The onward of a delivery: completing the task settles the answer in the room.",
			in: obj(`"thread_id":{"type":"string",`+descr+`"The room."},"member":{"type":"string",`+descr+`"The member answering."},"delivery":{"type":"integer",`+descr+`"Which delivery this answers."},`+
				`"message":{`+descr+`"What the room said."},"answer":{"type":"string",`+descr+`"The answer, posted to the room."},`+
				`"status":{"type":"string",`+descr+`"Set when the member could not answer."},"reason":{"type":"string",`+descr+`"Why the member could not answer."}`,
				"thread_id", "member", "delivery", "message"),
			out: obj(`"ok":{"type":"boolean",` + descr + `"True when the answer was recorded."}`)},
		{name: roomCreateName, title: "Start a chat room", vis: "public", auth: delegated,
			desc: "Start a chat room with the members named; you are its creator and a member. A name left out is the members' handles.",
			in: obj(`"members":{"type":"array","minItems":1,`+descr+`"The members' addresses, handle@kernel.","items":{"type":"string"}},"name":{"type":"string",`+descr+`"The room's name."}`,
				"members"),
			out: roomOut},
		{name: roomRenameName, title: "Rename a chat room", vis: "public", auth: delegated,
			desc: "Change a room's name, for instance when its subject has changed. Only the room's creator can.",
			in:   obj(threadProp+`,"name":{"type":"string",`+descr+`"The room's new name."}`, "thread_id", "name"), out: roomOut},
		{name: roomAddName, title: "Add a chat room member", vis: "public", auth: delegated,
			desc: "Add a member to a room. Only the room's creator can.",
			in:   obj(threadProp+","+memberProp, "thread_id", "member"), out: roomOut},
		{name: roomRemoveName, title: "Remove a chat room member", vis: "public", auth: delegated,
			desc: "Remove a member from a room. Only the room's creator can, and never itself.",
			in:   obj(threadProp+","+memberProp, "thread_id", "member"), out: roomOut},
		{name: roomLeaveName, title: "Leave a chat room", vis: "public", auth: delegated,
			desc: "Leave a room you are a member of. A room's creator cannot leave it, only delete it.",
			in:   obj(threadProp, "thread_id"), out: okOut},
		{name: roomDeleteName, title: "Delete a chat room", vis: "public", auth: delegated,
			desc: "Delete a room and everything said in it, for every member. Only the room's creator can.",
			in:   obj(threadProp, "thread_id"), out: okOut},
		{name: roomListName, title: "List your chat rooms", vis: "public", auth: delegated,
			desc: "The chat rooms you are a member of, newest activity first.",
			in:   obj(""), out: obj(roomsProp)},
		{name: roomReadName, title: "Read a chat room", vis: "public", auth: delegated,
			desc: "What a room you are a member of says: its latest entries, or with after those newer than it. Read on from the last entry while more is true.",
			in: obj(threadProp+`,"after":{"type":"integer",`+descr+`"Read the entries newer than this seq."},"seen":{"type":"boolean",`+descr+`"Move your read cursor over the entries read."}`,
				"thread_id"),
			out: obj(roomProp + `,"entries":{"type":"array",` + descr + `"The standing entries, oldest first.","items":{"type":"object","additionalProperties":false,` + descr + `"One thing said in the room.","properties":{` +
				`"seq":{"type":"integer",` + descr + `"Its position in the room."},"author":{"type":"string",` + descr + `"The member that said it."},"body":{` + descr + `"What was said, any JSON value."},` +
				`"at":{"type":"string",` + descr + `"When it was said."},"kind":{"type":"integer",` + descr + `"0 a post, 1 an answer, 2 a failure."}}}},` +
				`"removed":{"type":"array",` + descr + `"The seqs of posts an answer replaced since after.","items":{"type":"integer"}},` +
				`"waiting":{"type":"array",` + descr + `"The other members holding an unanswered delivery here.","items":{"type":"string"}},` +
				`"more":{"type":"boolean",` + descr + `"True when newer entries remain past these."},` + roomsProp)},
	}
	// Every text names this provider and its kernel.
	for i := range ks {
		ks[i].desc = named(ks[i].desc, self)
		ks[i].in = json.RawMessage(named(string(ks[i].in), self))
		ks[i].out = json.RawMessage(named(string(ks[i].out), self))
	}
	return ks
}

// ---- the endpoints ----

// ServeHTTP answers the kernel's dispatch of an action, /<provider>/<name>.
func (s *Service) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		refuse(w, http.StatusMethodNotAllowed, "bad_input", "POST only")
		return
	}
	self, name, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
	switch name {
	case indexName:
		writeJSON(w, http.StatusOK, map[string]any{"usage": named(usage, self)})
	case registerName:
		s.serveRegister(w, r, self)
	case validateName:
		s.serveValidate(w, r)
	case postName:
		s.servePost(w, r, self)
	case replyName:
		s.serveReply(w, r)
	case roomCreateName:
		s.serveRoomCreate(w, r)
	case roomRenameName:
		s.serveRoomRename(w, r)
	case roomAddName:
		s.serveRoomAdd(w, r)
	case roomRemoveName:
		s.serveRoomRemove(w, r)
	case roomLeaveName:
		s.serveRoomLeave(w, r)
	case roomDeleteName:
		s.serveRoomDelete(w, r)
	case roomListName:
		s.serveRoomList(w, r)
	case roomReadName:
		s.serveRoomRead(w, r)
	default:
		refuse(w, http.StatusNotFound, "not_found", "no such action")
	}
}

// member resolves the caller by the key the kernel injected.
func (s *Service) member(w http.ResponseWriter, r *http.Request) (string, bool) {
	m, ok := s.Store.Member(r.Header.Get(CallerKey))
	if !ok {
		refuse(w, http.StatusUnauthorized, "unknown_key", "unknown chat key; join chat first")
	}
	return m, ok
}

// memberCommand decodes a member's command, answering the refusal when it
// cannot be read or the caller is not known.
func (s *Service) memberCommand(w http.ResponseWriter, r *http.Request, in any) (string, bool) {
	m, ok := s.member(w, r)
	if !ok {
		return "", false
	}
	if err := json.NewDecoder(r.Body).Decode(in); err != nil {
		refuse(w, http.StatusBadRequest, "bad_input", "bad request body")
		return "", false
	}
	return m, true
}

// serveRegister mints a pending key for the address and creates, through the
// capability of this very call, the verification task only that address can
// complete; its onward activates the key.
func (s *Service) serveRegister(w http.ResponseWriter, r *http.Request, self string) {
	var in struct {
		Handle string `json:"handle"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || !strings.Contains(in.Handle, "@") {
		refuse(w, http.StatusBadRequest, "bad_input", "handle must be an address, handle@kernel")
		return
	}
	cb, cap := r.Header.Get(client.CallbackHeader), r.Header.Get(client.CapabilityHeader)
	if cb == "" || cap == "" {
		refuse(w, http.StatusBadRequest, "bad_input", "no capability: register is run through the kernel")
		return
	}
	raw, keyID, err := s.Store.RegisterKey(in.Handle)
	if err != nil {
		refuseErr(w, err)
		return
	}
	partial, _ := json.Marshal(map[string]string{"key_id": keyID})
	t, err := s.Kernel.CallbackCreateTask(r.Context(), cb, cap, client.CreateTaskInput{
		Action: self + "/" + validateName, RequiredCaller: in.Handle, PartialArgs: partial,
	})
	if err != nil {
		refuse(w, http.StatusBadGateway, "kernel", "verification task: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"api_key": raw, "task_id": t.ID})
}

// fromKernel checks the bearer the private actions carry: the kernel injects
// it when it dispatches them, so the caller is the kernel completing a task.
func (s *Service) fromKernel(w http.ResponseWriter, r *http.Request) bool {
	got := r.Header.Get("Authorization")
	if subtle.ConstantTimeCompare([]byte(got), []byte("Bearer "+s.Secret)) != 1 {
		refuse(w, http.StatusUnauthorized, "unauthorized", "unauthorized")
		return false
	}
	return true
}

func (s *Service) serveValidate(w http.ResponseWriter, r *http.Request) {
	if !s.fromKernel(w, r) {
		return
	}
	var in struct {
		KeyID string `json:"key_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		refuse(w, http.StatusBadRequest, "bad_input", "bad request body")
		return
	}
	if err := s.Store.ActivateKey(in.KeyID); err != nil {
		refuseErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// servePost is a member's post: identified by the key the kernel injected,
// the entry is stored under the one-post rule, then delivered during this
// call, unless the hand-off cap is exceeded.
func (s *Service) servePost(w http.ResponseWriter, r *http.Request, self string) {
	var in struct {
		Room    string          `json:"thread_id"`
		Message json.RawMessage `json:"message"`
	}
	member, ok := s.memberCommand(w, r, &in)
	if !ok {
		return
	}
	e, depth, err := s.Store.Post(in.Room, member, in.Message)
	if err != nil {
		refuseErr(w, err)
		return
	}
	n, undelivered := 0, []string{}
	if depth <= handoffCap {
		room, err := s.Store.withMembers(in.Room)
		if err != nil {
			refuseErr(w, err)
			return
		}
		to := recipients(member, in.Message, room.Members)
		undelivered = s.deliver(r.Context(), self, r.Header.Get(client.CallbackHeader), r.Header.Get(client.CapabilityHeader),
			in.Room, e.Seq, depth, to)
		n = len(to) - len(undelivered)
	}
	writeJSON(w, http.StatusOK, map[string]any{"delivered": n, "undelivered": undelivered})
}

// serveReply is a delivery's completion: the answer is the string the
// member gave; a completion with a status and no answer is a failure, as is an
// answer too long for a read to carry: refusing it would strand the task, since
// an agent treats a refused completion as final. The store settles it (rule 4).
func (s *Service) serveReply(w http.ResponseWriter, r *http.Request) {
	if !s.fromKernel(w, r) {
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
		refuse(w, http.StatusBadRequest, "bad_input", "bad request body")
		return
	}
	var body json.RawMessage
	failed := false
	answer := ""
	if in.Answer != nil {
		answer = strings.TrimSpace(*in.Answer)
	}
	switch {
	case utf8.RuneCountInString(answer) > maxMessage:
		body, _ = json.Marshal(map[string]string{"status": "failed", "reason": "The answer was too long to keep."})
		failed = true
	case answer != "":
		body, _ = json.Marshal(*in.Answer)
	case in.Status != "":
		body, _ = json.Marshal(struct {
			Status string `json:"status"`
			Reason string `json:"reason"`
		}{in.Status, in.Reason})
		failed = true
	}
	if err := s.Store.Answer(in.Member, in.Room, in.Delivery, body, failed); err != nil {
		refuseErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ---- the room commands ----

// roomReply answers a command with the room as it now stands.
func (s *Service) roomReply(w http.ResponseWriter, roomID string, err error) {
	var room Room
	if err == nil {
		room, err = s.Store.withMembers(roomID)
	}
	if err != nil {
		refuseErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"room": room})
}

// serveRoomCreate starts a room with the caller as its creator. A name left
// blank is the members' handles.
func (s *Service) serveRoomCreate(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Members []string `json:"members"`
		Name    string   `json:"name"`
	}
	member, ok := s.memberCommand(w, r, &in)
	if !ok {
		return
	}
	handles := make([]string, len(in.Members))
	for i, m := range in.Members {
		handle, _, addressed := strings.Cut(m, "@")
		if !addressed {
			refuseErr(w, fmt.Errorf("%w: %s is not an address, handle@kernel", ErrBadInput, m))
			return
		}
		handles[i] = handle
	}
	if len(in.Members) == 0 {
		refuseErr(w, fmt.Errorf("%w: a room needs a member", ErrBadInput))
		return
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		name = strings.Join(handles, ", ")
	}
	room, err := s.Store.CreateRoom(member, name, in.Members)
	if err != nil {
		refuseErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"room": room})
}

func (s *Service) serveRoomRename(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Room string `json:"thread_id"`
		Name string `json:"name"`
	}
	member, ok := s.memberCommand(w, r, &in)
	if !ok {
		return
	}
	s.roomReply(w, in.Room, s.Store.Rename(member, in.Room, in.Name))
}

func (s *Service) serveRoomAdd(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Room   string `json:"thread_id"`
		Member string `json:"member"`
	}
	member, ok := s.memberCommand(w, r, &in)
	if !ok {
		return
	}
	if !strings.Contains(in.Member, "@") {
		refuseErr(w, fmt.Errorf("%w: %s is not an address, handle@kernel", ErrBadInput, in.Member))
		return
	}
	s.roomReply(w, in.Room, s.Store.AddMember(member, in.Room, in.Member))
}

func (s *Service) serveRoomRemove(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Room   string `json:"thread_id"`
		Member string `json:"member"`
	}
	member, ok := s.memberCommand(w, r, &in)
	if !ok {
		return
	}
	s.roomReply(w, in.Room, s.Store.RemoveMember(member, in.Room, in.Member))
}

func (s *Service) serveRoomLeave(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Room string `json:"thread_id"`
	}
	member, ok := s.memberCommand(w, r, &in)
	if !ok {
		return
	}
	if err := s.Store.Leave(member, in.Room); err != nil {
		refuseErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Service) serveRoomDelete(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Room string `json:"thread_id"`
	}
	member, ok := s.memberCommand(w, r, &in)
	if !ok {
		return
	}
	if err := s.Store.DeleteRoom(member, in.Room); err != nil {
		refuseErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ---- the reads ----

// fit is how many of xs, in order, fit budget bytes of JSON, and never none: an
// entry is bounded on its own.
func fit[T any](xs []T, budget int) int {
	for i, x := range xs {
		b, _ := json.Marshal(x)
		if budget -= len(b); budget < 0 && i > 0 {
			return i
		}
	}
	return len(xs)
}

// rooms is the member's rooms that fit a reply, newest activity first.
func (s *Service) rooms(member string) ([]Room, error) {
	rooms, err := s.Store.Rooms(member)
	if err != nil {
		return nil, err
	}
	return append([]Room{}, rooms[:fit(rooms, maxRooms)]...), nil
}

func (s *Service) serveRoomList(w http.ResponseWriter, r *http.Request) {
	member, ok := s.member(w, r)
	if !ok {
		return
	}
	rooms, err := s.rooms(member)
	if err != nil {
		refuseErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"rooms": rooms})
}

// serveRoomRead is a room for a member: its latest entries that fit, or with
// after the oldest that fit past it, and the posts an answer replaced; whom it
// waits on; with seen the caller's read cursor moved over the entries read; and
// the caller's rooms, so one run serves a whole page.
func (s *Service) serveRoomRead(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Room  string `json:"thread_id"`
		After *int64 `json:"after"`
		Seen  bool   `json:"seen"`
	}
	member, ok := s.memberCommand(w, r, &in)
	if !ok {
		return
	}
	var (
		room Room
		es   []Entry
		gone []int64
		more bool
		err  error
	)
	if in.After != nil {
		if es, gone, err = s.Store.After(member, in.Room, *in.After); err == nil {
			room, err = s.Store.withMembers(in.Room)
		}
		n := min(fit(es, maxEntries), window)
		more, es = n < len(es), es[:n]
	} else if room, es, err = s.Store.Room(member, in.Room); err == nil {
		slices.Reverse(es)
		es = es[:fit(es, maxEntries)]
		slices.Reverse(es)
	}
	if err == nil && in.Seen {
		var seq int64 // what the reader now shows: past after, even with nothing new
		if in.After != nil {
			seq = *in.After
		}
		if len(es) > 0 {
			seq = es[len(es)-1].Seq
		}
		err = s.Store.MarkRead(member, in.Room, seq)
	}
	var waiting []string
	if err == nil {
		waiting, err = s.Store.Working(in.Room, member)
	}
	var rooms []Room
	if err == nil {
		rooms, err = s.rooms(member)
	}
	if err != nil {
		refuseErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"room": room, "entries": append([]Entry{}, es...), "removed": append([]int64{}, gone...),
		"waiting": append([]string{}, waiting...), "more": more, "rooms": rooms})
}

// ---- refusals ----

// refuseErr answers a store refusal with its status and code. Through the
// kernel only the status survives: it relays neither the body nor the code to
// the caller of an action.
func refuseErr(w http.ResponseWriter, err error) {
	status, code := http.StatusInternalServerError, "internal"
	switch {
	case errors.Is(err, ErrNotFound):
		status, code = http.StatusNotFound, "not_found"
	case errors.Is(err, ErrForbidden):
		status, code = http.StatusForbidden, "forbidden"
	case errors.Is(err, ErrNotMember):
		status, code = http.StatusForbidden, "not_member"
	case errors.Is(err, ErrBadInput):
		status, code = http.StatusBadRequest, "bad_input"
	case errors.Is(err, ErrHeld):
		status, code = http.StatusConflict, "held"
	}
	refuse(w, status, code, err.Error())
}

func refuse(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]any{"ok": false, "code": code, "error": msg})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
