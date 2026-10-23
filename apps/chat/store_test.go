package chat

import (
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
)

func mustStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open("", "")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func raw(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func TestRoomsAndMembership(t *testing.T) {
	s := mustStore(t)
	r, err := s.CreateRoom("ann@k", "cafe", []string{"bob@k", "helper@k", ""})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Members) != 3 {
		t.Fatalf("members = %v", r.Members)
	}
	if err := s.AddMember("bob@k", r.ID, "critic@k"); !errors.Is(err, ErrForbidden) {
		t.Errorf("a member adding = %v, want forbidden", err)
	}
	if err := s.AddMember("ann@k", r.ID, "critic@k"); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveMember("ann@k", r.ID, "ann@k"); !errors.Is(err, ErrForbidden) {
		t.Errorf("creator removing itself = %v", err)
	}
	if err := s.Leave("ann@k", r.ID); !errors.Is(err, ErrForbidden) {
		t.Errorf("creator leaving = %v", err)
	}
	if err := s.Leave("bob@k", r.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Leave("eve@k", r.ID); !errors.Is(err, ErrNotMember) {
		t.Errorf("outsider leaving = %v", err)
	}
	if _, _, err := s.Room("bob@k", r.ID); !errors.Is(err, ErrNotMember) {
		t.Errorf("a departed member reads = %v", err)
	}
	if _, _, err := s.Room("ann@k", "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown room = %v", err)
	}
	if err := s.DeleteRoom("critic@k", r.ID); !errors.Is(err, ErrForbidden) {
		t.Errorf("a member deleting = %v", err)
	}
	if err := s.DeleteRoom("ann@k", r.ID); err != nil {
		t.Fatal(err)
	}
	rooms, _ := s.Rooms("ann@k")
	if len(rooms) != 0 {
		t.Errorf("rooms after delete = %v", rooms)
	}
}

// A post is refused once the author holds a delivery and has posted since;
// its depth is one more than the deepest delivery held, and nothing when
// none is held. Answering releases both.
func TestPostHoldsOncePerDelivery(t *testing.T) {
	s := mustStore(t)
	r, _ := s.CreateRoom("ann@k", "cafe", []string{"helper@k"})
	e, depth, err := s.Post(r.ID, "ann@k", raw("hi"))
	if err != nil || depth != 0 || e.Seq != 1 {
		t.Fatalf("first post: seq=%d depth=%d err=%v", e.Seq, depth, err)
	}
	if _, _, err := s.Post(r.ID, "eve@k", raw("x")); !errors.Is(err, ErrNotMember) {
		t.Errorf("outsider post = %v", err)
	}
	if _, _, err := s.Post(r.ID, "ann@k", json.RawMessage(`{bad`)); !errors.Is(err, ErrBadInput) {
		t.Errorf("invalid body = %v", err)
	}
	if err := s.Delivered("helper@k", r.ID, e.Seq, 7, 0); err != nil {
		t.Fatal(err)
	}
	if _, depth, err := s.Post(r.ID, "helper@k", raw("one")); err != nil || depth != 1 {
		t.Fatalf("held post: depth=%d err=%v", depth, err)
	}
	if _, _, err := s.Post(r.ID, "helper@k", raw("two")); !errors.Is(err, ErrHeld) {
		t.Fatalf("second held post = %v, want ErrHeld", err)
	}
	if err := s.Answer("helper@k", r.ID, 7, raw("done")); err != nil {
		t.Fatal(err)
	}
	if _, depth, err := s.Post(r.ID, "helper@k", raw("three")); err != nil || depth != 0 {
		t.Fatalf("post after answer: depth=%d err=%v", depth, err)
	}
	_, es, _ := s.Room("ann@k", r.ID)
	if len(es) != 4 || string(es[2].Body) != `"done"` || es[2].Author != "helper@k" {
		t.Errorf("entries = %+v", es)
	}
}

// A delivery carries the entries after the member's delivered cursor, by
// others, up to the post; the cursor then moves. A range is bounded to the
// newest.
func TestRangeAndCursors(t *testing.T) {
	s := mustStore(t)
	r, _ := s.CreateRoom("ann@k", "cafe", []string{"bob@k", "helper@k"})
	for i, who := range []string{"ann@k", "helper@k", "bob@k", "ann@k"} {
		if _, _, err := s.Post(r.ID, who, raw(i)); err != nil {
			t.Fatal(err)
		}
	}
	es, err := s.Range("helper@k", r.ID, 4, carried)
	if err != nil || len(es) != 3 || es[0].Seq != 1 || es[2].Seq != 4 {
		t.Fatalf("range = %+v err=%v", es, err)
	}
	if err := s.Delivered("helper@k", r.ID, 4, 1, 0); err != nil {
		t.Fatal(err)
	}
	if es, _ := s.Range("helper@k", r.ID, 4, carried); len(es) != 0 {
		t.Errorf("range after delivery = %+v", es)
	}
	s.Post(r.ID, "bob@k", raw("late"))
	if es, _ := s.Range("helper@k", r.ID, 5, 1); len(es) != 1 || es[0].Seq != 5 {
		t.Errorf("bounded range = %+v", es)
	}
	if n, _ := s.NextDelivery(); n != 1 {
		t.Errorf("first delivery id = %d", n)
	}
	if n, _ := s.NextDelivery(); n != 2 {
		t.Errorf("second delivery id = %d", n)
	}
}

// Unread is the viewer's own read cursor against the room's newest entry;
// the list puts unread rooms first.
func TestRoomsUnreadOrdering(t *testing.T) {
	s := mustStore(t)
	quiet, _ := s.CreateRoom("ann@k", "quiet", nil)
	busy, _ := s.CreateRoom("ann@k", "busy", []string{"bob@k"})
	s.Post(busy.ID, "bob@k", raw("hey"))
	s.Post(quiet.ID, "ann@k", raw("me"))
	s.MarkRead("ann@k", quiet.ID, 1)
	rooms, _ := s.Rooms("ann@k")
	if len(rooms) != 2 || rooms[0].ID != busy.ID || !rooms[0].Unread || rooms[1].Unread {
		t.Fatalf("rooms = %+v", rooms)
	}
	s.MarkRead("ann@k", busy.ID, 1)
	rooms, _ = s.Rooms("ann@k")
	if rooms[0].Unread || rooms[1].Unread {
		t.Errorf("after reading: %+v", rooms)
	}
	es, _ := s.After("ann@k", busy.ID, 0)
	if len(es) != 1 {
		t.Errorf("after = %+v", es)
	}
}

func TestKeys(t *testing.T) {
	s := mustStore(t)
	key, id, err := s.RegisterKey("ann@k")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Member(key); ok {
		t.Error("a pending key must not resolve")
	}
	if err := s.ActivateKey("nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown key id = %v", err)
	}
	if err := s.ActivateKey(id); err != nil {
		t.Fatal(err)
	}
	if m, ok := s.Member(key); !ok || m != "ann@k" {
		t.Errorf("Member = %q %v", m, ok)
	}
	if _, ok := s.Member("other"); ok {
		t.Error("unknown key resolved")
	}
}

// A fresh store beside the previous application's database takes its live
// rooms, members and messages once, text becoming a JSON string; keys are
// not taken, and the old file is untouched.
func TestImportsTheLegacyDatabaseOnce(t *testing.T) {
	dir := t.TempDir()
	legacy := filepath.Join(dir, "chat.db")
	old, err := sql.Open("sqlite", legacy)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`CREATE TABLE rooms (id TEXT PRIMARY KEY, name TEXT, creator TEXT, created_at TEXT, deleted INTEGER DEFAULT 0)`,
		`CREATE TABLE members (room_id TEXT, handle TEXT)`,
		`CREATE TABLE messages (room_id TEXT, seq INTEGER, id TEXT, sender TEXT, text TEXT, at TEXT, deleted INTEGER DEFAULT 0)`,
		`CREATE TABLE tokens (hash TEXT, username TEXT, active INTEGER, reg_id TEXT, created_at TEXT)`,
		`INSERT INTO rooms VALUES ('r1','general','ann@k','2026-01-01T00:00:00Z',0), ('r2','gone','ann@k','2026-01-01T00:00:00Z',1)`,
		`INSERT INTO members VALUES ('r1','ann@k'), ('r1','bob@k'), ('r2','ann@k')`,
		`INSERT INTO messages VALUES ('r1',1,'m1','ann@k','hello','2026-01-02T00:00:00Z',0), ('r1',2,'m2','bob@k','bye','2026-01-03T00:00:00Z',1), ('r2',1,'m3','ann@k','x','2026-01-02T00:00:00Z',0)`,
		`INSERT INTO tokens VALUES ('h','ann@k',1,'reg','2026-01-01T00:00:00Z')`,
	} {
		if _, err := old.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	old.Close()
	path := filepath.Join(dir, "rooms.db")
	s, err := Open(path, legacy)
	if err != nil {
		t.Fatal(err)
	}
	rooms, _ := s.Rooms("ann@k")
	if len(rooms) != 1 || rooms[0].Name != "general" || len(rooms[0].Members) != 2 {
		t.Fatalf("imported rooms = %+v", rooms)
	}
	_, es, _ := s.Room("bob@k", "r1")
	if len(es) != 1 || string(es[0].Body) != `"hello"` || es[0].Author != "ann@k" {
		t.Fatalf("imported entries = %+v", es)
	}
	if _, ok := s.Member("h"); ok {
		t.Error("a legacy key was taken")
	}
	s.Close()
	// Opening again imports nothing more: the file exists.
	old, _ = sql.Open("sqlite", legacy)
	old.Exec(`INSERT INTO rooms VALUES ('r3','later','ann@k','2026-01-05T00:00:00Z',0)`)
	old.Close()
	s, err = Open(path, legacy)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if rooms, _ := s.Rooms("ann@k"); len(rooms) != 1 {
		t.Errorf("second open imported again: %+v", rooms)
	}
}
