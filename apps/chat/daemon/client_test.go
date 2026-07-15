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

func TestClientNotifications(t *testing.T) {
	d := newDaemon(t)
	c := NewClient(d)
	ctx := context.Background()

	// Reserve coalesces, ref round-trips, clear frees the slot — all over the
	// in-process REST client the plugin uses.
	if ok, err := c.ReserveNotification(ctx, "@bob", "r1"); err != nil || !ok {
		t.Fatalf("first reserve = %v, %v; want true", ok, err)
	}
	if ok, _ := c.ReserveNotification(ctx, "@bob", "r1"); ok {
		t.Errorf("second reserve granted; want false (coalesced)")
	}
	if err := c.SetNotificationRef(ctx, "@bob", "r1", "step-9"); err != nil {
		t.Fatalf("SetNotificationRef: %v", err)
	}
	if ref, _ := c.GetNotification(ctx, "@bob", "r1"); ref != "step-9" {
		t.Errorf("GetNotification = %q, want step-9", ref)
	}
	if err := c.ClearNotification(ctx, "@bob", "r1"); err != nil {
		t.Fatalf("ClearNotification: %v", err)
	}
	if ref, _ := c.GetNotification(ctx, "@bob", "r1"); ref != "" {
		t.Errorf("ref after clear = %q, want empty", ref)
	}
	if ok, _ := c.ReserveNotification(ctx, "@bob", "r1"); !ok {
		t.Errorf("reserve after clear denied; want re-ring")
	}
}
