package daemon

import (
	"context"
	"errors"
	"testing"
)

func TestClientReads(t *testing.T) {
	d := newDaemon(t)
	c := NewClient(d)
	ctx := context.Background()

	// Set up room + messages through the store, then read via the client.
	r, _ := d.store.CreateRoom("@alice", "general", []string{"@bob"})
	d.store.PostMessage("@alice", r.ID, "one")
	d.store.PostMessage("@alice", r.ID, "two")

	rooms, err := c.RoomsFor(ctx, "@bob")
	if err != nil || len(rooms) != 1 || rooms[0].Name != "general" {
		t.Fatalf("RoomsFor(bob) = %+v err=%v", rooms, err)
	}
	room, msgs, err := c.Room(ctx, "@bob", r.ID)
	if err != nil || room.Name != "general" || len(msgs) != 2 || msgs[0].Text != "one" {
		t.Fatalf("Room = %+v msgs=%+v err=%v", room, msgs, err)
	}
}

func TestClientMapsErrors(t *testing.T) {
	d := newDaemon(t)
	c := NewClient(d)
	ctx := context.Background()
	// Non-member reading a room maps 403 back to a sentinel.
	r, _ := d.store.CreateRoom("@alice", "n", nil)
	if _, _, err := c.Room(ctx, "@carol", r.ID); !errors.Is(err, ErrForbidden) {
		t.Errorf("non-member Room err = %v, want ErrForbidden", err)
	}
	// Missing room → ErrNotFound.
	if _, _, err := c.Room(ctx, "@alice", "ghost"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing Room err = %v, want ErrNotFound", err)
	}
}

func TestClientRejectsWrongSecret(t *testing.T) {
	d := newDaemon(t)
	c := NewClient(d)
	c.secret = "wrong"
	if _, err := c.RoomsFor(context.Background(), "@alice"); err == nil {
		t.Fatal("expected error with wrong admin secret")
	}
}
