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
		{"rename", func() error { return s.RenameRoom("@bob", r.ID, "x") }},
		{"delete", func() error { return s.DeleteRoom("@bob", r.ID) }},
		{"add", func() error { return s.AddMember("@bob", r.ID, "@dave") }},
		{"remove", func() error { return s.RemoveMember("@bob", r.ID, "@alice") }},
	} {
		if err := tc.fn(); !errors.Is(err, ErrForbidden) {
			t.Errorf("%s by non-creator err = %v, want ErrForbidden", tc.name, err)
		}
	}
	// creator succeeds
	if err := s.RenameRoom("@alice", r.ID, "renamed"); err != nil {
		t.Fatalf("creator rename: %v", err)
	}
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
	if got.Name != "renamed" || len(got.Members) != 2 {
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
	if err := s.RenameRoom("@alice", r.ID, "x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("op on deleted room err = %v, want ErrNotFound", err)
	}
}

func TestUnreadAndWatermark(t *testing.T) {
	s := newStore(t)
	r, _ := s.CreateRoom("@alice", "n", []string{"@bob"})
	s.PostMessage("@alice", r.ID, "a1")
	s.PostMessage("@alice", r.ID, "a2")
	m3, _ := s.PostMessage("@alice", r.ID, "a3")
	// bob's own messages never count as unread for him.
	s.PostMessage("@bob", r.ID, "b1")

	rooms, _ := s.RoomsFor("@bob")
	if len(rooms) != 1 || rooms[0].Unread != 3 {
		t.Fatalf("bob unread = %d, want 3 (excludes own): %+v", rooms[0].Unread, rooms)
	}
	if n, _ := s.UnreadTotal("@bob"); n != 3 {
		t.Errorf("UnreadTotal(bob) = %d, want 3", n)
	}
	if n, _ := s.UnreadTotal("@alice"); n != 1 {
		t.Errorf("UnreadTotal(alice) = %d, want 1 (bob's message)", n)
	}
	// bob reads up to m3.
	if err := s.MarkRead("@bob", r.ID, m3.Seq); err != nil {
		t.Fatalf("MarkRead: %v", err)
	}
	if n, _ := s.UnreadTotal("@bob"); n != 0 {
		t.Errorf("after read UnreadTotal(bob) = %d, want 0", n)
	}
	// a new message re-raises unread.
	s.PostMessage("@alice", r.ID, "a4")
	if n, _ := s.UnreadTotal("@bob"); n != 1 {
		t.Errorf("after new message unread = %d, want 1", n)
	}
	// watermark never regresses.
	if err := s.MarkRead("@bob", r.ID, 1); err != nil {
		t.Fatalf("MarkRead lower: %v", err)
	}
	if n, _ := s.UnreadTotal("@bob"); n != 1 {
		t.Errorf("watermark regressed: unread = %d, want 1", n)
	}
	if err := s.MarkRead("@carol", r.ID, 1); !errors.Is(err, ErrNotMember) {
		t.Errorf("non-member MarkRead err = %v, want ErrNotMember", err)
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
