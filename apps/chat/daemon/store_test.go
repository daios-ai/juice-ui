package daemon

import (
	"errors"
	"strings"
	"testing"
)

// newStore opens a fresh in-memory store for a test.
func newStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open("")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestCreateRoomMembersAndReadGate(t *testing.T) {
	s := newStore(t)
	r, err := s.CreateRoom("alice", "general", []string{"bob", "bob", "", "alice"})
	if err != nil {
		t.Fatalf("CreateRoom: %v", err)
	}
	if r.Creator != "alice" || r.Name != "general" {
		t.Fatalf("room = %+v", r)
	}
	// creator + bob, deduped, creator first, no blanks/dupes.
	if len(r.Members) != 2 || r.Members[0] != "alice" || r.Members[1] != "bob" {
		t.Fatalf("members = %v", r.Members)
	}
	if _, _, err := s.Room("carol", r.ID); !errors.Is(err, ErrNotMember) {
		t.Errorf("non-member read err = %v, want ErrNotMember", err)
	}
	if rooms, _ := s.RoomsFor("carol"); len(rooms) != 0 {
		t.Errorf("non-member sees room in list: %v", rooms)
	}
	if _, err := s.CreateRoom("alice", "", nil); !errors.Is(err, ErrBadInput) {
		t.Errorf("blank name err = %v, want ErrBadInput", err)
	}
}

func TestAdminOpsAreCreatorOnly(t *testing.T) {
	s := newStore(t)
	r, _ := s.CreateRoom("alice", "n", []string{"bob"})
	// non-creator attempts
	for _, tc := range []struct {
		name string
		fn   func() error
	}{
		{"delete", func() error { return s.DeleteRoom("bob", r.ID) }},
		{"add", func() error { return s.AddMember("bob", r.ID, "dave") }},
		{"remove", func() error { return s.RemoveMember("bob", r.ID, "alice") }},
	} {
		if err := tc.fn(); !errors.Is(err, ErrForbidden) {
			t.Errorf("%s by non-creator err = %v, want ErrForbidden", tc.name, err)
		}
	}
	// creator succeeds
	if err := s.AddMember("alice", r.ID, "dave"); err != nil {
		t.Fatalf("creator add: %v", err)
	}
	if err := s.RemoveMember("alice", r.ID, "dave"); err != nil {
		t.Fatalf("creator remove: %v", err)
	}
	if err := s.RemoveMember("alice", r.ID, "alice"); !errors.Is(err, ErrBadInput) {
		t.Errorf("removing creator err = %v, want ErrBadInput", err)
	}
	got, _, _ := s.Room("alice", r.ID)
	if got.Name != "n" || len(got.Members) != 2 {
		t.Fatalf("after ops room = %+v", got)
	}
}

func TestLeaveRoomRules(t *testing.T) {
	s := newStore(t)
	r, _ := s.CreateRoom("alice", "n", []string{"bob"})
	if err := s.LeaveRoom("alice", r.ID); !errors.Is(err, ErrForbidden) {
		t.Errorf("creator leave err = %v, want ErrForbidden", err)
	}
	if err := s.LeaveRoom("carol", r.ID); !errors.Is(err, ErrNotMember) {
		t.Errorf("non-member leave err = %v, want ErrNotMember", err)
	}
	if err := s.LeaveRoom("bob", r.ID); err != nil {
		t.Fatalf("member leave: %v", err)
	}
	if rooms, _ := s.RoomsFor("bob"); len(rooms) != 0 {
		t.Errorf("left member still lists room: %v", rooms)
	}
}

func TestPostMessageMemberGatedAndAttributed(t *testing.T) {
	s := newStore(t)
	r, _ := s.CreateRoom("alice", "n", []string{"bob"})
	if _, err := s.PostMessage("carol", r.ID, "hi"); !errors.Is(err, ErrNotMember) {
		t.Errorf("non-member post err = %v, want ErrNotMember", err)
	}
	if _, err := s.PostMessage("alice", r.ID, ""); !errors.Is(err, ErrBadInput) {
		t.Errorf("empty text err = %v, want ErrBadInput", err)
	}
	m1, err := s.PostMessage("alice", r.ID, "first")
	if err != nil || m1.Seq != 1 || m1.From != "alice" {
		t.Fatalf("m1 = %+v err=%v", m1, err)
	}
	m2, _ := s.PostMessage("bob", r.ID, "second")
	if m2.Seq != 2 {
		t.Errorf("seq not monotonic: %d", m2.Seq)
	}
	_, msgs, _ := s.Room("bob", r.ID)
	if len(msgs) != 2 || msgs[0].Text != "first" || msgs[1].From != "bob" {
		t.Fatalf("messages = %+v", msgs)
	}
}

func TestPollAppendsNewAndPropagatesDeletes(t *testing.T) {
	s := newStore(t)
	r, _ := s.CreateRoom("alice", "n", []string{"bob"})
	s.PostMessage("alice", r.ID, "one")            // seq 1
	m2, _ := s.PostMessage("alice", r.ID, "two")   // seq 2
	m3, _ := s.PostMessage("alice", r.ID, "three") // seq 3

	// A viewer holding through seq 1 gets only the newer two, oldest-first; nothing
	// is deleted yet.
	added, deleted, err := s.Poll("bob", r.ID, 1)
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if len(added) != 2 || added[0].ID != m2.ID || added[1].ID != m3.ID {
		t.Fatalf("added = %+v, want [seq2, seq3]", added)
	}
	if len(deleted) != 0 {
		t.Fatalf("deleted = %v, want none", deleted)
	}

	// Sender deletes an already-shown message (seq 2). A caught-up viewer (after=3)
	// gets no appends but still learns of the deletion, so an append-only view
	// removes it.
	if err := s.DeleteMessage("alice", r.ID, m2.ID); err != nil {
		t.Fatalf("DeleteMessage: %v", err)
	}
	added, deleted, err = s.Poll("bob", r.ID, 3)
	if err != nil {
		t.Fatalf("Poll after delete: %v", err)
	}
	if len(added) != 0 {
		t.Fatalf("added after delete = %+v, want none", added)
	}
	if len(deleted) != 1 || deleted[0] != m2.ID {
		t.Fatalf("deleted = %v, want [%s]", deleted, m2.ID)
	}

	// Non-members are refused, like every other read.
	if _, _, err := s.Poll("carol", r.ID, 0); !errors.Is(err, ErrNotMember) {
		t.Errorf("non-member Poll err = %v, want ErrNotMember", err)
	}
}

func TestDeleteMessageIsSenderOnly(t *testing.T) {
	s := newStore(t)
	r, _ := s.CreateRoom("alice", "n", []string{"bob"})
	m, _ := s.PostMessage("bob", r.ID, "bob's")
	if err := s.DeleteMessage("alice", r.ID, m.ID); !errors.Is(err, ErrForbidden) {
		t.Errorf("non-sender delete err = %v, want ErrForbidden", err)
	}
	if err := s.DeleteMessage("bob", r.ID, m.ID); err != nil {
		t.Fatalf("sender delete: %v", err)
	}
	_, msgs, _ := s.Room("bob", r.ID)
	if len(msgs) != 0 {
		t.Errorf("deleted message still visible: %+v", msgs)
	}
}

func TestDeleteRoomHidesAndGates(t *testing.T) {
	s := newStore(t)
	r, _ := s.CreateRoom("alice", "n", []string{"bob"})
	if err := s.DeleteRoom("alice", r.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if rooms, _ := s.RoomsFor("alice"); len(rooms) != 0 {
		t.Errorf("deleted room still listed: %v", rooms)
	}
	if _, _, err := s.Room("alice", r.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleted room read err = %v, want ErrNotFound", err)
	}
	if err := s.AddMember("alice", r.ID, "dave"); !errors.Is(err, ErrNotFound) {
		t.Errorf("op on deleted room err = %v, want ErrNotFound", err)
	}
}

// TestRoomsForOrdering checks the list contract: rooms sort by most-recent
// activity, an empty room falling back to its creation time. The store no longer
// computes unread (that is a Juice-steps concern the plugin fills), so Room.Unread
// is always 0 here.
func TestRoomsForOrdering(t *testing.T) {
	s := newStore(t)
	// A: oldest activity.
	a, _ := s.CreateRoom("alice", "A", []string{"bob"})
	s.PostMessage("alice", a.ID, "hi-a")
	// B: newer activity than A.
	b, _ := s.CreateRoom("alice", "B", []string{"bob"})
	s.PostMessage("alice", b.ID, "hi-b")
	// C: empty room, created last → newest last_at (falls back to created_at).
	c, _ := s.CreateRoom("alice", "C", []string{"bob"})

	rooms, err := s.RoomsFor("bob")
	if err != nil {
		t.Fatal(err)
	}
	if len(rooms) != 3 {
		t.Fatalf("got %d rooms, want 3", len(rooms))
	}
	// Most-recent first: C (created after B's message) > B > A.
	if rooms[0].ID != c.ID || rooms[1].ID != b.ID || rooms[2].ID != a.ID {
		t.Errorf("not recency-ordered: got %q, %q, %q; want C, B, A", rooms[0].Name, rooms[1].Name, rooms[2].Name)
	}
	for _, rm := range rooms {
		if rm.LastAt == "" {
			t.Errorf("room %q has empty LastAt", rm.Name)
		}
		if rm.Unread != 0 {
			t.Errorf("room %q Unread = %d, want 0 (store no longer computes unread)", rm.Name, rm.Unread)
		}
	}
	// The empty room's LastAt is its CreatedAt.
	if rooms[0].LastAt != rooms[0].CreatedAt {
		t.Errorf("empty room LastAt %q != CreatedAt %q", rooms[0].LastAt, rooms[0].CreatedAt)
	}
}

func TestRecentWindowAndHistory(t *testing.T) {
	s := newStore(t)
	r, _ := s.CreateRoom("alice", "n", nil)
	total := recentWindow + 25
	var midSeq int64
	for i := 0; i < total; i++ {
		m, _ := s.PostMessage("alice", r.ID, "m")
		if i == 10 {
			midSeq = m.Seq
		}
	}
	_, msgs, _ := s.Room("alice", r.ID)
	if len(msgs) != recentWindow {
		t.Fatalf("recent window = %d, want %d", len(msgs), recentWindow)
	}
	// oldest-first, and the window is the *latest* messages.
	if msgs[len(msgs)-1].Seq != int64(total) {
		t.Errorf("last message seq = %d, want %d (latest in window)", msgs[len(msgs)-1].Seq, total)
	}
	if msgs[0].Seq >= msgs[1].Seq {
		t.Error("messages not oldest-first")
	}
	// history before the window start returns older messages, oldest-first.
	older, err := s.History("alice", r.ID, midSeq, 5)
	if err != nil || len(older) != 5 {
		t.Fatalf("History: %d msgs err=%v", len(older), err)
	}
	if older[len(older)-1].Seq != midSeq-1 {
		t.Errorf("history newest = %d, want %d", older[len(older)-1].Seq, midSeq-1)
	}
	if _, err := s.History("carol", r.ID, midSeq, 5); !errors.Is(err, ErrNotMember) {
		t.Errorf("non-member history err = %v, want ErrNotMember", err)
	}
}

func TestRecipientsListsLiveMembers(t *testing.T) {
	s := newStore(t)
	r, err := s.CreateRoom("alice", "general", []string{"bob"})
	if err != nil {
		t.Fatalf("CreateRoom: %v", err)
	}
	name, members, err := s.Recipients(r.ID)
	if err != nil || name != "general" {
		t.Fatalf("Recipients name = %q, err %v", name, err)
	}
	if len(members) != 2 {
		t.Errorf("members = %v, want alice+bob", members)
	}
	if _, _, err := s.Recipients("nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Recipients(unknown) err = %v, want ErrNotFound", err)
	}
}

func TestMessagesReadPath(t *testing.T) {
	s := newStore(t)
	r, _ := s.CreateRoom("alice", "general", []string{"bob"})
	s.PostMessage("alice", r.ID, "one")
	s.PostMessage("bob", r.ID, "two")
	m3, _ := s.PostMessage("alice", r.ID, "three")

	// after=0 → the recent window, oldest-first, all senders (not consume-on-read).
	msgs, err := s.Messages("bob", r.ID, 0)
	if err != nil {
		t.Fatalf("Messages: %v", err)
	}
	if len(msgs) != 3 || msgs[0].Text != "one" || msgs[2].Text != "three" {
		t.Fatalf("recent read = %+v", msgs)
	}
	// A second read returns the same (stateless — reading marks nothing).
	if again, _ := s.Messages("bob", r.ID, 0); len(again) != 3 {
		t.Errorf("read is not stateless: got %d, want 3", len(again))
	}
	// after=<2nd seq> → only newer, oldest-first.
	after := msgs[1].Seq
	newer, _ := s.Messages("bob", r.ID, after)
	if len(newer) != 1 || newer[0].ID != m3.ID {
		t.Errorf("after-filter = %+v, want just the third message", newer)
	}
	// Non-members cannot read.
	if _, err := s.Messages("carol", r.ID, 0); !errors.Is(err, ErrNotMember) {
		t.Errorf("non-member read err = %v, want ErrNotMember", err)
	}
}

func TestTokenRegisterActivateLookup(t *testing.T) {
	s := newStore(t)

	rawKey, regID, err := s.RegisterToken("agent")
	if err != nil {
		t.Fatalf("RegisterToken: %v", err)
	}
	if rawKey == "" || regID == "" || rawKey == regID {
		t.Fatalf("register returned rawKey=%q regID=%q", rawKey, regID)
	}

	// A freshly registered key resolves to its handle but is NOT yet active.
	u, active, err := s.LookupToken(hashToken(rawKey))
	if err != nil || u != "agent" || active {
		t.Fatalf("pending lookup = (%q, %v, %v), want (agent, false, nil)", u, active, err)
	}

	// Activation flips it live.
	if err := s.ActivateToken(regID); err != nil {
		t.Fatalf("ActivateToken: %v", err)
	}
	if u, active, _ := s.LookupToken(hashToken(rawKey)); u != "agent" || !active {
		t.Errorf("active lookup = (%q, %v), want (agent, true)", u, active)
	}

	// An unknown key is an unauthenticated caller, not an error.
	if u, active, err := s.LookupToken(hashToken("nope")); err != nil || u != "" || active {
		t.Errorf("unknown lookup = (%q, %v, %v), want ('', false, nil)", u, active, err)
	}
	// Activating an unknown registration is ErrNotFound.
	if err := s.ActivateToken("no-such-reg"); !errors.Is(err, ErrNotFound) {
		t.Errorf("activate unknown reg = %v, want ErrNotFound", err)
	}
	// The raw key must never be recoverable from the store: only its hash is stored.
	var got string
	if err := s.db.QueryRow(`SELECT hash FROM tokens WHERE reg_id = ?`, regID).Scan(&got); err != nil {
		t.Fatalf("read hash: %v", err)
	}
	if got == rawKey {
		t.Error("raw key stored verbatim; want only its hash")
	}
}

// TestTokenReconnectRotatesKey: a reconnect (register→activate a second time for
// the same handle) rotates the key — the new one is active, the old is
// deactivated, and exactly one active row remains. Guards the orphan-key leak
// that the kernel-side disconnect could never clean up.
func TestTokenReconnectRotatesKey(t *testing.T) {
	s := newStore(t)

	key1, reg1, err := s.RegisterToken("ana")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ActivateToken(reg1); err != nil {
		t.Fatal(err)
	}
	// Reconnect: a fresh registration + activation for the same handle.
	key2, reg2, err := s.RegisterToken("ana")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ActivateToken(reg2); err != nil {
		t.Fatal(err)
	}

	// The new key is live; the old key is no longer active (rotated out).
	if _, active, _ := s.LookupToken(hashToken(key2)); !active {
		t.Error("reconnected key must be active")
	}
	if _, active, _ := s.LookupToken(hashToken(key1)); active {
		t.Error("the previous key must be deactivated on reconnect (no orphan)")
	}
	// Exactly one active row for the handle.
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM tokens WHERE username = ? AND active = 1`, "ana").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("active key rows for ana = %d, want exactly 1", n)
	}
}

// Rows stored before principals were addresses are qualified once on this
// kernel: bare handles gain "@kernel", a member stored under both forms
// collapses to one, addresses are untouched, and a second pass changes nothing.
func TestQualifyStoredHandles(t *testing.T) {
	s := newStore(t)
	room, err := s.CreateRoom("alice", "general", []string{"bob", "bob@k", "carol@peer"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PostMessage("alice", room.ID, "hi"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := s.Qualify("k"); err != nil {
			t.Fatal(err)
		}
	}
	r, msgs, err := s.Room("alice@k", room.ID)
	if err != nil {
		t.Fatal(err)
	}
	if r.Creator != "alice@k" || len(msgs) != 1 || msgs[0].From != "alice@k" {
		t.Errorf("creator and sender qualified: %+v %+v", r, msgs)
	}
	if got := strings.Join(r.Members, " "); got != "alice@k bob@k carol@peer" {
		t.Errorf("members = %q", got)
	}
}
