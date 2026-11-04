package chat

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

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
	roomDirectName = "room/direct"
	roomRenameName = "room/rename"
	roomAddName    = "room/add"
	roomRemoveName = "room/remove"
	roomLeaveName  = "room/leave"
	roomDeleteName = "room/delete"
)

// The service's two reads, by path. They are not actions: a run is an
// execution with a transaction and a receipt, and reading a room is neither.
const (
	roomListPath = "room/list"
	roomReadPath = "room/read"
)

// usage is what chat/index answers: the one place a member learns chat.
// KERNEL is replaced by this kernel's name.
const usage = "Chat rooms for people and agents; every member is an address, handle@KERNEL. " +
	"You receive a post as a task carrying what the room said since your previous task. Answer by completing the task: your answer appears in the room. " +
	"To ask another member something, run chat@KERNEL/msg/post with {thread_id: the room, message: your text} and write that member's address as the first word, " +
	"as in \"critic@KERNEL, which is best?\"; only that member receives it. A post that starts with no address reaches every other member. " +
	"While you hold tasks you may post once per task; then complete them. " +
	"Any member can start a room with chat@KERNEL/room/create or chat@KERNEL/room/direct, and leave one with chat@KERNEL/room/leave. " +
	"The room's creator can rename it with chat@KERNEL/room/rename, change its members with chat@KERNEL/room/add and chat@KERNEL/room/remove, and delete it with chat@KERNEL/room/delete. " +
	"Members of this installation are joined by its UI. A member elsewhere joins by approving chat@KERNEL as a requester, running chat@KERNEL/register with its own address, " +
	"completing the verification task, and connecting chat@KERNEL with the key returned."

// Service is the chat service: the HTTP endpoints the kernel wraps as chat's
// actions, the two reads, and the store behind them. It knows nothing of the
// UI that may present it, and could be served from another program: the
// browser reaches it only through the kernel or over HTTP.
type Service struct {
	Store     *Store
	Self      func() string  // chat's own address; "" while the kernel's name is unknown
	Secret    string         // the bearer the private actions carry, proving the caller is the kernel
	CallerKey string         // the header the kernel injects a member's chat key on
	Kernel    *client.Client // creates tasks through the capability of a dispatch
	Log       *slog.Logger
}

func (s *Service) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

// Close releases the store.
func (s *Service) Close() error { return s.Store.Close() }

func (s *Service) kernelName() string {
	_, k, _ := strings.Cut(s.Self(), "@")
	return k
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
	threadProp  = `"thread_id":{"type":"string",` + descr + `"The room."}`
	memberProp  = `"member":{"type":"string",` + descr + `"The member's address, handle@kernel."}`
	roomProp    = `"room":{"type":"object","additionalProperties":false,` + descr + `"The room as it now stands.","properties":{` +
		threadProp + `,"name":{"type":"string",` + descr + `"The room's name."},"creator":{"type":"string",` + descr + `"The member that made the room."},` +
		`"members":{"type":"array",` + descr + `"The room's members.","items":{"type":"string"}},"created_at":{"type":"string",` + descr + `"When the room was made."},` +
		`"unread":{"type":"boolean",` + descr + `"Whether the room holds entries the caller has not read."},"last_at":{"type":"string",` + descr + `"When the room was last spoken in."}}}`
	okProp = `"ok":{"type":"boolean",` + descr + `"True when it was done."}`
)

// Contracts are chat's actions. Every schema is canonical: an object naming
// its properties is closed, and every property is described. The two onwards
// are private: the kernel checks an onward's visibility against the task's
// creator, chat, so a member completes the task without being able to run the
// action itself and speak as somebody else.
func (s *Service) Contracts() []contract {
	delegated, _ := json.Marshal(map[string]any{"scheme": "delegated_bearer", "config": map[string]any{"header": s.CallerKey, "template": "{token}"}})
	bearer, _ := json.Marshal(map[string]any{"scheme": "bearer", "secrets": map[string]any{"token": s.Secret}})
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
			out:  obj(`"delivered":{"type":"integer",` + descr + `"How many members received it."}`)},
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
		{name: roomDirectName, title: "Open a direct chat", vis: "public", auth: delegated,
			desc: "The room whose members are exactly you and the member named, started when there is none.",
			in:   obj(memberProp, "member"), out: roomOut},
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
	}
	// Every text names this kernel where it says KERNEL.
	here := "@" + s.kernelName()
	for i := range ks {
		ks[i].desc = strings.ReplaceAll(ks[i].desc, "@KERNEL", here)
		ks[i].in = json.RawMessage(strings.ReplaceAll(string(ks[i].in), "@KERNEL", here))
		ks[i].out = json.RawMessage(strings.ReplaceAll(string(ks[i].out), "@KERNEL", here))
	}
	return ks
}

// ---- the endpoints ----

// ServeHTTP answers the kernel's dispatch of chat's actions, and a member's
// two reads.
func (s *Service) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		switch r.URL.Path {
		case "/" + roomListPath:
			s.serveRoomList(w, r)
		case "/" + roomReadPath:
			s.serveRoomRead(w, r)
		default:
			refuse(w, http.StatusNotFound, "not_found", "no such read")
		}
		return
	}
	if r.Method != http.MethodPost {
		refuse(w, http.StatusMethodNotAllowed, "bad_input", "GET or POST only")
		return
	}
	switch r.URL.Path {
	case "/" + indexName:
		writeJSON(w, http.StatusOK, map[string]any{"usage": strings.ReplaceAll(usage, "@KERNEL", "@"+s.kernelName())})
	case "/" + registerName:
		s.serveRegister(w, r)
	case "/" + validateName:
		s.serveValidate(w, r)
	case "/" + postName:
		s.servePost(w, r)
	case "/" + replyName:
		s.serveReply(w, r)
	case "/" + roomCreateName:
		s.serveRoomCreate(w, r)
	case "/" + roomDirectName:
		s.serveRoomDirect(w, r)
	case "/" + roomRenameName:
		s.serveRoomRename(w, r)
	case "/" + roomAddName:
		s.serveRoomAdd(w, r)
	case "/" + roomRemoveName:
		s.serveRoomRemove(w, r)
	case "/" + roomLeaveName:
		s.serveRoomLeave(w, r)
	case "/" + roomDeleteName:
		s.serveRoomDelete(w, r)
	default:
		refuse(w, http.StatusNotFound, "not_found", "no such action")
	}
}

// member resolves the caller by the key on the request: the kernel injected
// it on a run, and a reader presents it on a read.
func (s *Service) member(w http.ResponseWriter, r *http.Request) (string, bool) {
	m, ok := s.Store.Member(r.Header.Get(s.CallerKey))
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
func (s *Service) serveRegister(w http.ResponseWriter, r *http.Request) {
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
		Action: s.Self() + "/" + validateName, RequiredCaller: in.Handle, PartialArgs: partial,
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
func (s *Service) servePost(w http.ResponseWriter, r *http.Request) {
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
	n := 0
	if depth <= handoffCap {
		room, err := s.Store.withMembers(in.Room)
		if err != nil {
			refuseErr(w, err)
			return
		}
		n = s.deliver(r.Context(), r.Header.Get(client.CallbackHeader), r.Header.Get(client.CapabilityHeader),
			in.Room, e.Seq, depth, recipients(member, in.Message, room.Members))
	}
	writeJSON(w, http.StatusOK, map[string]any{"delivered": n})
}

// serveReply is a delivery's completion: the answer is the string the
// member gave; a completion with a status and no answer is a failure. The
// store settles it (rule 4).
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

func (s *Service) serveRoomDirect(w http.ResponseWriter, r *http.Request) {
	var in struct {
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
	room, err := s.Store.Direct(member, in.Member)
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

func (s *Service) serveRoomList(w http.ResponseWriter, r *http.Request) {
	member, ok := s.member(w, r)
	if !ok {
		return
	}
	rooms, err := s.Store.Rooms(member)
	if err != nil {
		refuseErr(w, err)
		return
	}
	if rooms == nil {
		rooms = []Room{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"rooms": rooms})
}

// serveRoomRead is a room for a member: its latest entries, or with after the
// standing entries newer than it and the posts an answer replaced; whom it
// waits on; and with seen the caller's read cursor moved over the entries read.
func (s *Service) serveRoomRead(w http.ResponseWriter, r *http.Request) {
	member, ok := s.member(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	id := q.Get("thread_id")
	var (
		room Room
		es   []Entry
		gone []int64
		err  error
	)
	if q.Has("after") {
		after, perr := strconv.ParseInt(q.Get("after"), 10, 64)
		if perr != nil {
			refuseErr(w, fmt.Errorf("%w: after must be an integer", ErrBadInput))
			return
		}
		if es, gone, err = s.Store.After(member, id, after); err == nil {
			room, err = s.Store.withMembers(id)
		}
	} else {
		room, es, err = s.Store.Room(member, id)
	}
	if err == nil && q.Get("seen") == "true" && len(es) > 0 {
		err = s.Store.MarkRead(member, id, es[len(es)-1].Seq)
	}
	var waiting []string
	if err == nil {
		waiting, err = s.Store.Working(id, member)
	}
	if err != nil {
		refuseErr(w, err)
		return
	}
	if es == nil {
		es = []Entry{}
	}
	if gone == nil {
		gone = []int64{}
	}
	if waiting == nil {
		waiting = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"room": room, "entries": es, "removed": gone, "waiting": waiting})
}

// ---- refusals ----

// refuseErr answers a store refusal with its status and code. Through the
// kernel only the status survives: it relays neither the body nor the code to
// the caller of an action; a read carries them whole.
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
