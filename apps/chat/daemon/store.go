package daemon

import (
	"sort"
	"sync"
)

// Room is a chat room (a DM is just a two-member room). Its ID is the tx id of
// the create-room event that created it; Creator is the caller of that event.
type Room struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Creator   string   `json:"creator"` // @handle
	Members   []string `json:"members"`
	Deleted   bool     `json:"-"`
	CreatedAt string   `json:"created_at"`
}

// isMember reports membership.
func (r *Room) isMember(handle string) bool {
	for _, m := range r.Members {
		if m == handle {
			return true
		}
	}
	return false
}

// Message is one posted message. ID is the tx id of the message event; From is
// the caller of that event (unforgeable).
type Message struct {
	ID      string `json:"id"`
	From    string `json:"from"`
	Text    string `json:"text"`
	At      string `json:"at"`
	Deleted bool   `json:"-"`
}

// Store is the in-memory projection of the @chat transaction ledger: rooms,
// members, and messages, rebuilt by replaying events. It is the authoritative
// read model; the durable truth is the kernel ledger. Safe for concurrent use.
type Store struct {
	mu      sync.RWMutex
	rooms   map[string]*Room      // room id -> room
	msgs    map[string][]*Message // room id -> messages, in application order
	applied map[string]bool       // event tx ids already applied (idempotent)
}

// NewStore returns an empty store.
func NewStore() *Store {
	return &Store{rooms: map[string]*Room{}, msgs: map[string][]*Message{}, applied: map[string]bool{}}
}

// event is one projected ledger entry: a @chat action call.
type event struct {
	TxID   string
	Action string // the action name, e.g. "create-room"
	Caller string // @handle of the caller (from tx caller_handle)
	At     string // started_at
	Args   eventArgs
}

// eventArgs holds the decoded argument fields across all @chat actions (only the
// relevant ones are set per action).
type eventArgs struct {
	Name    string   `json:"name"`
	Members []string `json:"members"`
	Room    string   `json:"room"`
	Text    string   `json:"text"`
	Member  string   `json:"member"`
	Message string   `json:"message"`
}

// Applied reports whether an event tx has already been projected.
func (s *Store) Applied(txID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.applied[txID]
}

// Apply projects one ledger event into the store, enforcing attribution and
// authorization. Unauthorized events are recorded as applied (so they are not
// retried) but leave state unchanged. Apply is idempotent per event tx id.
func (s *Store) Apply(e event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.applied[e.TxID] {
		return
	}
	s.applied[e.TxID] = true

	switch e.Action {
	case "create-room":
		members := []string{e.Caller}
		for _, m := range e.Args.Members {
			if m != "" && m != e.Caller {
				members = append(members, m)
			}
		}
		s.rooms[e.TxID] = &Room{ID: e.TxID, Name: e.Args.Name, Creator: e.Caller, Members: members, CreatedAt: e.At}
	case "message":
		if r := s.liveRoom(e.Args.Room); r != nil && r.isMember(e.Caller) {
			s.msgs[r.ID] = append(s.msgs[r.ID], &Message{ID: e.TxID, From: e.Caller, Text: e.Args.Text, At: e.At})
		}
	case "rename-room":
		if r := s.liveRoom(e.Args.Room); r != nil && r.Creator == e.Caller {
			r.Name = e.Args.Name
		}
	case "delete-room":
		if r := s.liveRoom(e.Args.Room); r != nil && r.Creator == e.Caller {
			r.Deleted = true
		}
	case "add-member":
		if r := s.liveRoom(e.Args.Room); r != nil && r.Creator == e.Caller && e.Args.Member != "" && !r.isMember(e.Args.Member) {
			r.Members = append(r.Members, e.Args.Member)
		}
	case "remove-member":
		if r := s.liveRoom(e.Args.Room); r != nil && r.Creator == e.Caller {
			r.removeMember(e.Args.Member)
		}
	case "leave-room":
		// The creator can't leave (the room would be left unmanaged) — they delete
		// it instead. Any other member may leave.
		if r := s.liveRoom(e.Args.Room); r != nil && r.Creator != e.Caller {
			r.removeMember(e.Caller)
		}
	case "delete-message":
		if r := s.liveRoom(e.Args.Room); r != nil {
			for _, m := range s.msgs[r.ID] {
				if m.ID == e.Args.Message && m.From == e.Caller {
					m.Deleted = true
				}
			}
		}
	}
}

// liveRoom returns a non-deleted room by id, or nil.
func (s *Store) liveRoom(id string) *Room {
	r := s.rooms[id]
	if r == nil || r.Deleted {
		return nil
	}
	return r
}

func (r *Room) removeMember(handle string) {
	out := r.Members[:0]
	for _, m := range r.Members {
		if m != handle {
			out = append(out, m)
		}
	}
	r.Members = out
}

// RoomsFor returns the live rooms handle is a member of, newest first.
func (s *Store) RoomsFor(handle string) []Room {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []Room
	for _, r := range s.rooms {
		if !r.Deleted && r.isMember(handle) {
			out = append(out, *r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt > out[j].CreatedAt })
	return out
}

// Room returns a live room by id (if handle is a member) and its non-deleted
// messages oldest-first. ok is false if the room is missing, deleted, or handle
// is not a member.
func (s *Store) Room(id, handle string) (Room, []Message, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r := s.rooms[id]
	if r == nil || r.Deleted || !r.isMember(handle) {
		return Room{}, nil, false
	}
	var msgs []Message
	for _, m := range s.msgs[id] {
		if !m.Deleted {
			msgs = append(msgs, *m)
		}
	}
	return *r, msgs, true
}
