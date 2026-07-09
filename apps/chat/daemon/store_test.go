package daemon

import "testing"

// ev builds an event with the given fields for driving the store in tests.
func ev(tx, action, caller string, args eventArgs) event {
	return event{TxID: tx, Action: action, Caller: caller, At: tx, Args: args}
}

// TestProjectionAuthorizes replays the same scenario the live prototype used and
// asserts attribution + authorization: only the creator manages the room, only
// members post, only a message's sender deletes it, and a member who left can no
// longer post.
func TestProjectionAuthorizes(t *testing.T) {
	s := NewStore()
	room := "r1"
	s.Apply(ev(room, "create-room", "@alice", eventArgs{Name: "general", Members: []string{"@bob", "@carol"}}))
	s.Apply(ev("m1", "message", "@alice", eventArgs{Room: room, Text: "hi all"}))
	s.Apply(ev("m2", "message", "@bob", eventArgs{Room: room, Text: "hey"}))
	s.Apply(ev("m3", "message", "@carol", eventArgs{Room: room, Text: "to be deleted"}))
	s.Apply(ev("d1", "delete-message", "@carol", eventArgs{Room: room, Message: "m3"}))   // sender deletes own → ok
	s.Apply(ev("x1", "rename-room", "@bob", eventArgs{Room: room, Name: "hacked"}))       // non-creator → dropped
	s.Apply(ev("x2", "rename-room", "@alice", eventArgs{Room: room, Name: "general-v2"})) // creator → ok
	s.Apply(ev("x3", "add-member", "@alice", eventArgs{Room: room, Member: "@dave"}))
	s.Apply(ev("x4", "leave-room", "@carol", eventArgs{Room: room}))
	s.Apply(ev("m4", "message", "@carol", eventArgs{Room: room, Text: "after leaving"})) // non-member → dropped

	rm, msgs, ok := s.Room(room, "@alice")
	if !ok {
		t.Fatal("room not found for member")
	}
	if rm.Name != "general-v2" {
		t.Errorf("name = %q, want general-v2 (creator rename applied, non-creator dropped)", rm.Name)
	}
	if rm.Creator != "@alice" {
		t.Errorf("creator = %q", rm.Creator)
	}
	wantMembers := map[string]bool{"@alice": true, "@bob": true, "@dave": true} // carol left, dave added
	if len(rm.Members) != len(wantMembers) {
		t.Fatalf("members = %v, want %v", rm.Members, wantMembers)
	}
	for _, m := range rm.Members {
		if !wantMembers[m] {
			t.Errorf("unexpected member %q", m)
		}
	}
	// Non-deleted messages: hi all (@alice), hey (@bob). "to be deleted" is soft-deleted; "after leaving" dropped.
	if len(msgs) != 2 {
		t.Fatalf("visible messages = %d (%v), want 2", len(msgs), msgs)
	}
	if msgs[0].From != "@alice" || msgs[0].Text != "hi all" {
		t.Errorf("msg[0] = %+v", msgs[0])
	}
	if msgs[1].From != "@bob" || msgs[1].Text != "hey" {
		t.Errorf("msg[1] = %+v, want @bob/hey (attribution)", msgs[1])
	}
}

func TestCreatorCannotLeave(t *testing.T) {
	s := NewStore()
	s.Apply(ev("r", "create-room", "@alice", eventArgs{Name: "n", Members: []string{"@bob"}}))
	s.Apply(ev("l", "leave-room", "@alice", eventArgs{Room: "r"})) // creator → dropped (would orphan the room)
	rm, _, ok := s.Room("r", "@alice")
	if !ok {
		t.Fatal("creator wrongly removed from their own room")
	}
	if !rm.isMember("@alice") {
		t.Errorf("creator no longer a member after leave: %v", rm.Members)
	}
}

func TestDeleteMessageIsSenderOnly(t *testing.T) {
	s := NewStore()
	s.Apply(ev("r", "create-room", "@alice", eventArgs{Name: "n", Members: []string{"@bob"}}))
	s.Apply(ev("m", "message", "@bob", eventArgs{Room: "r", Text: "bob's message"}))
	s.Apply(ev("d", "delete-message", "@alice", eventArgs{Room: "r", Message: "m"})) // not the sender → dropped
	_, msgs, _ := s.Room("r", "@bob")
	if len(msgs) != 1 {
		t.Fatalf("message wrongly deleted by non-sender: %v", msgs)
	}
}

func TestDeleteRoomHidesItAndGatesReads(t *testing.T) {
	s := NewStore()
	s.Apply(ev("r", "create-room", "@alice", eventArgs{Name: "n", Members: []string{"@bob"}}))
	s.Apply(ev("d", "delete-room", "@bob", eventArgs{Room: "r"})) // non-creator → not deleted
	if got := s.RoomsFor("@alice"); len(got) != 1 {
		t.Fatalf("room wrongly deleted by non-creator: %v", got)
	}
	s.Apply(ev("d2", "delete-room", "@alice", eventArgs{Room: "r"})) // creator → deleted
	if got := s.RoomsFor("@alice"); len(got) != 0 {
		t.Fatalf("deleted room still listed: %v", got)
	}
	if _, _, ok := s.Room("r", "@alice"); ok {
		t.Fatal("deleted room still readable")
	}
}

func TestApplyIsIdempotent(t *testing.T) {
	s := NewStore()
	e := ev("r", "create-room", "@alice", eventArgs{Name: "n"})
	s.Apply(e)
	s.Apply(e) // replay
	if got := s.RoomsFor("@alice"); len(got) != 1 {
		t.Fatalf("replay created a duplicate: %v", got)
	}
	if !s.Applied("r") {
		t.Fatal("Applied not tracked")
	}
}

func TestReadsAreMemberGated(t *testing.T) {
	s := NewStore()
	s.Apply(ev("r", "create-room", "@alice", eventArgs{Name: "n", Members: []string{"@bob"}}))
	if got := s.RoomsFor("@carol"); got != nil {
		t.Fatalf("non-member sees room in list: %v", got)
	}
	if _, _, ok := s.Room("r", "@carol"); ok {
		t.Fatal("non-member can open room")
	}
}
