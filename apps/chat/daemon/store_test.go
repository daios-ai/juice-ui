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

// TestInboxConsumeAcrossRooms checks the receive contract: unseen messages from
// others across all the caller's rooms, each tagged with its room, marked seen on
// return, with empty-room memberships still listed.
func TestInboxConsumeAcrossRooms(t *testing.T) {
	s := newStore(t)
	a, _ := s.CreateRoom("@alice", "A", []string{"@bob"})
	b, _ := s.CreateRoom("@alice", "B", []string{"@bob"})
	empty, _ := s.CreateRoom("@alice", "C", []string{"@bob"}) // no messages
	s.PostMessage("@alice", a.ID, "a1")
	s.PostMessage("@bob", a.ID, "bob-own") // bob's own → not unseen for bob
	s.PostMessage("@alice", b.ID, "b1")
	s.PostMessage("@alice", b.ID, "b2")

	rooms, msgs, err := s.Inbox("@bob")
	if err != nil {
		t.Fatal(err)
	}
	if len(rooms) != 3 {
		t.Fatalf("rooms = %d, want 3 (incl. empty)", len(rooms))
	}
	if len(msgs) != 3 {
		t.Fatalf("msgs = %d, want 3 (a1,b1,b2; own excluded): %+v", len(msgs), msgs)
	}
	for _, m := range msgs {
		if m.RoomID == "" {
			t.Errorf("inbox message missing room_id: %+v", m)
		}
	}
	sawEmpty := false
	for _, r := range rooms {
		if r.ID == empty.ID {
			sawEmpty = true
			if r.Unread != 0 {
				t.Errorf("empty room unread = %d, want 0", r.Unread)
			}
		}
	}
	if !sawEmpty {
		t.Error("empty-room membership not listed")
	}

	// Consume-on-read: a second call returns nothing new.
	if _, m2, _ := s.Inbox("@bob"); len(m2) != 0 {
		t.Errorf("second inbox = %d, want 0 (consumed)", len(m2))
	}
	// A new message re-raises it.
	s.PostMessage("@alice", a.ID, "a2")
	_, m3, _ := s.Inbox("@bob")
	if len(m3) != 1 || m3[0].Text != "a2" {
		t.Fatalf("after new post inbox = %+v", m3)
	}
}

// TestInboxPagesLargeBacklog checks the per-room cap: a flood pages across calls,
// the watermark advances only over what was returned, and unread reports the true
// backlog (which exceeds a capped page).
func TestInboxPagesLargeBacklog(t *testing.T) {
	s := newStore(t)
	r, _ := s.CreateRoom("@alice", "big", []string{"@bob"})
	total := recentWindow + 5
	for i := 0; i < total; i++ {
		s.PostMessage("@alice", r.ID, "m")
	}
	rooms, msgs, _ := s.Inbox("@bob")
	if len(msgs) != recentWindow {
		t.Fatalf("first page = %d, want %d (capped)", len(msgs), recentWindow)
	}
	if rooms[0].Unread != total {
		t.Errorf("unread = %d, want %d (full backlog, not the page)", rooms[0].Unread, total)
	}
	if _, m2, _ := s.Inbox("@bob"); len(m2) != 5 {
		t.Fatalf("second page = %d, want 5 (resume)", len(m2))
	}
	if _, m3, _ := s.Inbox("@bob"); len(m3) != 0 {
		t.Errorf("third page = %d, want 0", len(m3))
	}
}

func TestInboxNonMemberEmpty(t *testing.T) {
	s := newStore(t)
	r, _ := s.CreateRoom("@alice", "n", []string{"@bob"})
	s.PostMessage("@alice", r.ID, "hi")
	rooms, msgs, err := s.Inbox("@carol")
	if err != nil {
		t.Fatal(err)
	}
	if len(rooms) != 0 || len(msgs) != 0 {
		t.Fatalf("non-member inbox rooms=%d msgs=%d, want 0/0", len(rooms), len(msgs))
	}
}

// TestMemberHandles checks the grant-sweep source: distinct members of live rooms
// only (a member of a deleted room is excluded).
func TestMemberHandles(t *testing.T) {
	s := newStore(t)
	s.CreateRoom("@alice", "A", []string{"@bob", "@carol"})
	s.CreateRoom("@bob", "B", []string{"@alice"})
	del, _ := s.CreateRoom("@alice", "D", []string{"@dave"})
	s.DeleteRoom("@alice", del.ID)

	hs, err := s.MemberHandles()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, h := range hs {
		got[h] = true
	}
	for _, w := range []string{"@alice", "@bob", "@carol"} {
		if !got[w] {
			t.Errorf("missing member %q in %v", w, hs)
		}
	}
	if got["@dave"] {
		t.Errorf("@dave (deleted-room-only) must be excluded: %v", hs)
	}
}

// TestRoomsForOrdering checks the list contract: unread rooms sort first, then by
// most-recent activity, with an empty room falling back to its creation time.
func TestRoomsForOrdering(t *testing.T) {
	s := newStore(t)
	// A: unread for bob, but its activity is the OLDEST.
	a, _ := s.CreateRoom("@alice", "A", []string{"@bob"})
	s.PostMessage("@alice", a.ID, "hi-a")
	// B: bob's own message → not unread for bob; activity newer than A.
	b, _ := s.CreateRoom("@alice", "B", []string{"@bob"})
	s.PostMessage("@bob", b.ID, "hi-b")
	// C: empty room, created last → newest last_at (falls back to created_at).
	c, _ := s.CreateRoom("@alice", "C", []string{"@bob"})

	rooms, err := s.RoomsFor("@bob")
	if err != nil {
		t.Fatal(err)
	}
	if len(rooms) != 3 {
		t.Fatalf("got %d rooms, want 3", len(rooms))
	}
	// Unread first (A), despite being the least recent; then the read rooms by
	// recency: C (created after B's message) before B.
	if rooms[0].ID != a.ID {
		t.Errorf("unread room not first: got %q", rooms[0].Name)
	}
	if rooms[1].ID != c.ID || rooms[2].ID != b.ID {
		t.Errorf("read rooms not recency-ordered: got %q, %q want C, B", rooms[1].Name, rooms[2].Name)
	}
	for _, rm := range rooms {
		if rm.LastAt == "" {
			t.Errorf("room %q has empty LastAt", rm.Name)
		}
	}
	// The empty room's LastAt is its CreatedAt.
	if rooms[1].LastAt != rooms[1].CreatedAt {
		t.Errorf("empty room LastAt %q != CreatedAt %q", rooms[1].LastAt, rooms[1].CreatedAt)
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
