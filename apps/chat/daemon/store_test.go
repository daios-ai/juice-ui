package daemon

import (
	"errors"
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
	r, err := s.CreateRoom("@alice", "general", []string{"@bob", "@bob", "", "@alice"})
	if err != nil {
		t.Fatalf("CreateRoom: %v", err)
	}
	if r.Creator != "@alice" || r.Name != "general" {
		t.Fatalf("room = %+v", r)
	}
	// creator + bob, deduped, creator first, no blanks/dupes.
	if len(r.Members) != 2 || r.Members[0] != "@alice" || r.Members[1] != "@bob" {
		t.Fatalf("members = %v", r.Members)
	}
	if _, _, err := s.Room("@carol", r.ID); !errors.Is(err, ErrNotMember) {
		t.Errorf("non-member read err = %v, want ErrNotMember", err)
	}
	if rooms, _ := s.RoomsFor("@carol"); len(rooms) != 0 {
		t.Errorf("non-member sees room in list: %v", rooms)
	}
	if _, err := s.CreateRoom("@alice", "", nil); !errors.Is(err, ErrBadInput) {
		t.Errorf("blank name err = %v, want ErrBadInput", err)
	}
}

func TestAdminOpsAreCreatorOnly(t *testing.T) {
	s := newStore(t)
	r, _ := s.CreateRoom("@alice", "n", []string{"@bob"})
	// non-creator attempts
	for _, tc := range []struct {
		name string
		fn   func() error
	}{
		{"delete", func() error { return s.DeleteRoom("@bob", r.ID) }},
		{"add", func() error { return s.AddMember("@bob", r.ID, "@dave") }},
		{"remove", func() error { return s.RemoveMember("@bob", r.ID, "@alice") }},
	} {
		if err := tc.fn(); !errors.Is(err, ErrForbidden) {
			t.Errorf("%s by non-creator err = %v, want ErrForbidden", tc.name, err)
		}
	}
	// creator succeeds
	if err := s.AddMember("@alice", r.ID, "@dave"); err != nil {
		t.Fatalf("creator add: %v", err)
	}
	if err := s.RemoveMember("@alice", r.ID, "@dave"); err != nil {
		t.Fatalf("creator remove: %v", err)
	}
	if err := s.RemoveMember("@alice", r.ID, "@alice"); !errors.Is(err, ErrBadInput) {
		t.Errorf("removing creator err = %v, want ErrBadInput", err)
	}
	got, _, _ := s.Room("@alice", r.ID)
	if got.Name != "n" || len(got.Members) != 2 {
		t.Fatalf("after ops room = %+v", got)
	}
}

func TestLeaveRoomRules(t *testing.T) {
	s := newStore(t)
	r, _ := s.CreateRoom("@alice", "n", []string{"@bob"})
	if err := s.LeaveRoom("@alice", r.ID); !errors.Is(err, ErrForbidden) {
		t.Errorf("creator leave err = %v, want ErrForbidden", err)
	}
	if err := s.LeaveRoom("@carol", r.ID); !errors.Is(err, ErrNotMember) {
		t.Errorf("non-member leave err = %v, want ErrNotMember", err)
	}
	if err := s.LeaveRoom("@bob", r.ID); err != nil {
		t.Fatalf("member leave: %v", err)
	}
	if rooms, _ := s.RoomsFor("@bob"); len(rooms) != 0 {
		t.Errorf("left member still lists room: %v", rooms)
	}
}

func TestPostMessageMemberGatedAndAttributed(t *testing.T) {
	s := newStore(t)
	r, _ := s.CreateRoom("@alice", "n", []string{"@bob"})
	if _, err := s.PostMessage("@carol", r.ID, "hi"); !errors.Is(err, ErrNotMember) {
		t.Errorf("non-member post err = %v, want ErrNotMember", err)
	}
	if _, err := s.PostMessage("@alice", r.ID, ""); !errors.Is(err, ErrBadInput) {
		t.Errorf("empty text err = %v, want ErrBadInput", err)
	}
	m1, err := s.PostMessage("@alice", r.ID, "first")
	if err != nil || m1.Seq != 1 || m1.From != "@alice" {
		t.Fatalf("m1 = %+v err=%v", m1, err)
	}
	m2, _ := s.PostMessage("@bob", r.ID, "second")
	if m2.Seq != 2 {
		t.Errorf("seq not monotonic: %d", m2.Seq)
	}
	_, msgs, _ := s.Room("@bob", r.ID)
	if len(msgs) != 2 || msgs[0].Text != "first" || msgs[1].From != "@bob" {
		t.Fatalf("messages = %+v", msgs)
	}
}

func TestDeleteMessageIsSenderOnly(t *testing.T) {
	s := newStore(t)
	r, _ := s.CreateRoom("@alice", "n", []string{"@bob"})
	m, _ := s.PostMessage("@bob", r.ID, "bob's")
	if err := s.DeleteMessage("@alice", r.ID, m.ID); !errors.Is(err, ErrForbidden) {
		t.Errorf("non-sender delete err = %v, want ErrForbidden", err)
	}
	if err := s.DeleteMessage("@bob", r.ID, m.ID); err != nil {
		t.Fatalf("sender delete: %v", err)
	}
	_, msgs, _ := s.Room("@bob", r.ID)
	if len(msgs) != 0 {
		t.Errorf("deleted message still visible: %+v", msgs)
	}
}

func TestDeleteRoomHidesAndGates(t *testing.T) {
	s := newStore(t)
	r, _ := s.CreateRoom("@alice", "n", []string{"@bob"})
	if err := s.DeleteRoom("@alice", r.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if rooms, _ := s.RoomsFor("@alice"); len(rooms) != 0 {
		t.Errorf("deleted room still listed: %v", rooms)
	}
	if _, _, err := s.Room("@alice", r.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleted room read err = %v, want ErrNotFound", err)
	}
	if err := s.AddMember("@alice", r.ID, "@dave"); !errors.Is(err, ErrNotFound) {
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
	a, _ := s.CreateRoom("@alice", "A", []string{"@bob"})
	s.PostMessage("@alice", a.ID, "hi-a")
	// B: newer activity than A.
	b, _ := s.CreateRoom("@alice", "B", []string{"@bob"})
	s.PostMessage("@alice", b.ID, "hi-b")
	// C: empty room, created last → newest last_at (falls back to created_at).
	c, _ := s.CreateRoom("@alice", "C", []string{"@bob"})

	rooms, err := s.RoomsFor("@bob")
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
	r, _ := s.CreateRoom("@alice", "n", nil)
	total := recentWindow + 25
	var midSeq int64
	for i := 0; i < total; i++ {
		m, _ := s.PostMessage("@alice", r.ID, "m")
		if i == 10 {
			midSeq = m.Seq
		}
	}
	_, msgs, _ := s.Room("@alice", r.ID)
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
	older, err := s.History("@alice", r.ID, midSeq, 5)
	if err != nil || len(older) != 5 {
		t.Fatalf("History: %d msgs err=%v", len(older), err)
	}
	if older[len(older)-1].Seq != midSeq-1 {
		t.Errorf("history newest = %d, want %d", older[len(older)-1].Seq, midSeq-1)
	}
	if _, err := s.History("@carol", r.ID, midSeq, 5); !errors.Is(err, ErrNotMember) {
		t.Errorf("non-member history err = %v, want ErrNotMember", err)
	}
}

func TestNotificationReserveCoalesceAndClear(t *testing.T) {
	s := newStore(t)
	// First reserve for (handle, room) wins; a second (a coalesced re-post) loses.
	ok, err := s.ReserveNotification("@bob", "r1")
	if err != nil || !ok {
		t.Fatalf("first reserve = %v, %v; want true", ok, err)
	}
	if ok, _ := s.ReserveNotification("@bob", "r1"); ok {
		t.Errorf("second reserve granted; want coalesced (false)")
	}
	// A different room, and a different member, are independent slots.
	if ok, _ := s.ReserveNotification("@bob", "r2"); !ok {
		t.Errorf("reserve for other room denied")
	}
	if ok, _ := s.ReserveNotification("@carol", "r1"); !ok {
		t.Errorf("reserve for other member denied")
	}
	// Ref round-trips; empty until set.
	if ref, _ := s.GetNotification("@bob", "r1"); ref != "" {
		t.Errorf("ref before set = %q, want empty", ref)
	}
	if err := s.SetNotificationRef("@bob", "r1", "step-9"); err != nil {
		t.Fatalf("SetNotificationRef: %v", err)
	}
	if ref, _ := s.GetNotification("@bob", "r1"); ref != "step-9" {
		t.Errorf("ref = %q, want step-9", ref)
	}
	// Clearing frees the slot so the next message re-rings.
	if err := s.ClearNotification("@bob", "r1"); err != nil {
		t.Fatalf("ClearNotification: %v", err)
	}
	if ref, _ := s.GetNotification("@bob", "r1"); ref != "" {
		t.Errorf("ref after clear = %q, want empty", ref)
	}
	if ok, _ := s.ReserveNotification("@bob", "r1"); !ok {
		t.Errorf("reserve after clear denied; want re-ring")
	}
	// No row for an unknown pair reads as empty, not an error.
	if ref, err := s.GetNotification("@nobody", "rx"); err != nil || ref != "" {
		t.Errorf("GetNotification(unknown) = %q, %v; want \"\", nil", ref, err)
	}
}

func TestRecipientsListsLiveMembers(t *testing.T) {
	s := newStore(t)
	r, err := s.CreateRoom("@alice", "general", []string{"@bob"})
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
	r, _ := s.CreateRoom("@alice", "general", []string{"@bob"})
	s.PostMessage("@alice", r.ID, "one")
	s.PostMessage("@bob", r.ID, "two")
	m3, _ := s.PostMessage("@alice", r.ID, "three")

	// after=0 → the recent window, oldest-first, all senders (not consume-on-read).
	msgs, err := s.Messages("@bob", r.ID, 0)
	if err != nil {
		t.Fatalf("Messages: %v", err)
	}
	if len(msgs) != 3 || msgs[0].Text != "one" || msgs[2].Text != "three" {
		t.Fatalf("recent read = %+v", msgs)
	}
	// A second read returns the same (stateless — reading marks nothing).
	if again, _ := s.Messages("@bob", r.ID, 0); len(again) != 3 {
		t.Errorf("read is not stateless: got %d, want 3", len(again))
	}
	// after=<2nd seq> → only newer, oldest-first.
	after := msgs[1].Seq
	newer, _ := s.Messages("@bob", r.ID, after)
	if len(newer) != 1 || newer[0].ID != m3.ID {
		t.Errorf("after-filter = %+v, want just the third message", newer)
	}
	// Non-members cannot read.
	if _, err := s.Messages("@carol", r.ID, 0); !errors.Is(err, ErrNotMember) {
		t.Errorf("non-member read err = %v, want ErrNotMember", err)
	}
}
