package chat

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
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

// A member holding n deliveries may have n standing posts since the oldest
// of them; the next is refused. A post's depth is one more than the deepest
// delivery held, and zero when none is held. A settlement releases both.
func TestPostCountsPerDelivery(t *testing.T) {
	s := mustStore(t)
	r, _ := s.CreateRoom("ann@k", "cafe", []string{"helper@k", "critic@k"})
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
	s.Post(r.ID, "ann@k", raw("and"))
	must(t, s.Delivered("helper@k", r.ID, 1, 7, 0))
	must(t, s.Delivered("helper@k", r.ID, 2, 8, 1))
	if _, depth, err := s.Post(r.ID, "helper@k", raw("critic@k one")); err != nil || depth != 2 {
		t.Fatalf("held post: depth=%d err=%v", depth, err)
	}
	if _, _, err := s.Post(r.ID, "helper@k", raw("critic@k two")); err != nil {
		t.Fatalf("two deliveries, two posts: %v", err)
	}
	if _, _, err := s.Post(r.ID, "helper@k", raw("three")); !errors.Is(err, ErrHeld) {
		t.Fatalf("third post = %v, want ErrHeld", err)
	}
	must(t, s.Answer("helper@k", r.ID, 7, raw("done"), false))
	if _, _, err := s.Post(r.ID, "helper@k", raw("still held")); !errors.Is(err, ErrHeld) {
		t.Fatalf("one delivery left, two posts since it: %v", err)
	}
	must(t, s.Answer("helper@k", r.ID, 8, nil, false))
	if _, depth, err := s.Post(r.ID, "helper@k", raw("free")); err != nil || depth != 0 {
		t.Fatalf("post after settling: depth=%d err=%v", depth, err)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// standing is the room's entries as author:body/kind, and the seqs a page
// that showed everything after seq 0 must remove.
func standing(t *testing.T, s *Store, room string) (string, []int64) {
	t.Helper()
	es, gone, err := s.After("ann@k", room, 0)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range es {
		out = append(out, e.Author+":"+string(e.Body)+"/"+strconv.Itoa(e.Kind))
	}
	return strings.Join(out, "|"), gone
}

// Settling a delivery: with nothing said since, the answer or the failure is
// appended; an answer replaces the member's posts since that address nobody,
// hiding them and naming them for removal, never reusing a seq; after a
// hand-off only, the answer adds nothing; a failure after any post adds
// nothing; a nil body adds nothing; posts before the delivery are untouched.
func TestAnswerSettles(t *testing.T) {
	s := mustStore(t)
	r, _ := s.CreateRoom("ann@k", "cafe", []string{"bot@k", "critic@k"})
	ask := func(text string, d int64) {
		e, _, err := s.Post(r.ID, "ann@k", raw(text))
		must(t, err)
		must(t, s.Delivered("bot@k", r.ID, e.Seq, d, 0))
	}
	post := func(text string) { _, _, err := s.Post(r.ID, "bot@k", raw(text)); must(t, err) }

	ask("q1", 1)
	must(t, s.Answer("bot@k", r.ID, 1, raw("a1"), false))
	ask("q2", 2)
	post("wait")
	must(t, s.Answer("bot@k", r.ID, 2, raw("a2"), false))
	ask("q3", 3)
	post("critic@k, which?")
	must(t, s.Answer("bot@k", r.ID, 3, raw("I asked critic."), false))
	ask("q4", 4)
	post("a4")
	must(t, s.Answer("bot@k", r.ID, 4, raw(map[string]string{"status": "failed", "reason": "repeat-loop"}), true))
	ask("q5", 5)
	must(t, s.Answer("bot@k", r.ID, 5, raw(map[string]string{"status": "failed", "reason": "budget"}), true))
	ask("q6", 6)
	must(t, s.Answer("bot@k", r.ID, 6, nil, false))
	must(t, s.Answer("bot@k", r.ID, 99, raw("unheld"), false))

	got, gone := standing(t, s, r.ID)
	want := `ann@k:"q1"/0|bot@k:"a1"/1|ann@k:"q2"/0|bot@k:"a2"/1|ann@k:"q3"/0|bot@k:"critic@k, which?"/0|ann@k:"q4"/0|bot@k:"a4"/0|ann@k:"q5"/0|bot@k:{"reason":"budget","status":"failed"}/2|ann@k:"q6"/0|bot@k:"unheld"/1`
	if got != want {
		t.Errorf("room =\n%s\nwant\n%s", got, want)
	}
	if len(gone) != 1 || gone[0] != 4 {
		t.Errorf("removed = %v, want the hidden placeholder 4", gone)
	}
	if _, gone, _ := s.After("ann@k", r.ID, 5); len(gone) != 0 {
		t.Errorf("a page past the answer removes nothing: %v", gone)
	}
	var seqs []int64
	rows, _ := s.db.Query(`SELECT seq FROM entries WHERE room_id = ? ORDER BY seq`, r.ID)
	for rows.Next() {
		var q int64
		rows.Scan(&q)
		seqs = append(seqs, q)
	}
	rows.Close()
	for i, q := range seqs {
		if q != int64(i+1) {
			t.Fatalf("seqs = %v: a row was deleted or a seq reused", seqs)
		}
	}
	if es, _ := s.Range("critic@k", r.ID, 100, carried); strings.Contains(fmt.Sprint(es), `wait`) {
		t.Error("a hidden post is never delivered")
	}
}

// The direct room of two is found, or made once, named by the other's
// handle; a room with a third member is not it; concurrent first visits make
// one room.
func TestDirect(t *testing.T) {
	s := mustStore(t)
	s.CreateRoom("ann@k", "group", []string{"assistant@k", "bob@k"})
	var wg sync.WaitGroup
	ids := make([]string, 8)
	for i := range ids {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r, err := s.Direct("ann@k", "assistant@k")
			if err != nil {
				t.Error(err)
			}
			ids[i] = r.ID
		}(i)
	}
	wg.Wait()
	for _, id := range ids {
		if id != ids[0] {
			t.Fatalf("two rooms: %v", ids)
		}
	}
	r, _, err := s.Room("ann@k", ids[0])
	if err != nil || r.Name != "assistant" || strings.Join(r.Members, ",") != "ann@k,assistant@k" {
		t.Fatalf("direct = %+v %v", r, err)
	}
	if b, _ := s.Direct("bob@k", "assistant@k"); b.ID == ids[0] {
		t.Error("another viewer has its own room")
	}
	if _, err := s.Direct("ann@k", "ann@k"); !errors.Is(err, ErrBadInput) {
		t.Errorf("a room with oneself = %v", err)
	}
}

// Room ids are words, adjective-animal-NN.
func TestWordID(t *testing.T) {
	re := regexp.MustCompile(`^[a-z]+-[a-z]+-[1-9][0-9]$`)
	for i := 0; i < 200; i++ {
		if id := wordID(); !re.MatchString(id) {
			t.Fatalf("id %q", id)
		}
	}
	r, _ := mustStore(t).CreateRoom("ann@k", "cafe", nil)
	if !re.MatchString(r.ID) {
		t.Errorf("room id %q", r.ID)
	}
}

// v034 is the store as 0.34.0 made it.
const v034 = `
CREATE TABLE rooms (id TEXT PRIMARY KEY, name TEXT NOT NULL, creator TEXT NOT NULL, created_at TEXT NOT NULL);
CREATE TABLE members (room_id TEXT NOT NULL, member TEXT NOT NULL, PRIMARY KEY (room_id, member));
CREATE TABLE entries (room_id TEXT NOT NULL, seq INTEGER NOT NULL, author TEXT NOT NULL, body TEXT NOT NULL, at TEXT NOT NULL, PRIMARY KEY (room_id, seq));
CREATE TABLE keys (hash TEXT PRIMARY KEY, member TEXT NOT NULL, active INTEGER NOT NULL DEFAULT 0, key_id TEXT NOT NULL UNIQUE);
CREATE TABLE cursors (room_id TEXT NOT NULL, member TEXT NOT NULL, delivered INTEGER NOT NULL DEFAULT 0, read INTEGER NOT NULL DEFAULT 0, PRIMARY KEY (room_id, member));
CREATE TABLE chain (room_id TEXT NOT NULL, member TEXT NOT NULL, delivery INTEGER NOT NULL, depth INTEGER NOT NULL, posted INTEGER NOT NULL DEFAULT 0, PRIMARY KEY (room_id, member, delivery));
CREATE TABLE seqs (name TEXT PRIMARY KEY, n INTEGER NOT NULL);
INSERT INTO seqs VALUES ('delivery', 5);
INSERT INTO rooms VALUES ('0a1b2c', 'main', 'ann@k', '2026-10-23T22:00:00Z');
INSERT INTO members VALUES ('0a1b2c', 'ann@k'), ('0a1b2c', 'bot@k');
INSERT INTO entries VALUES ('0a1b2c', 1, 'ann@k', '"hi"', '2026-10-23T22:01:00Z'), ('0a1b2c', 2, 'bot@k', '"hello"', '2026-10-23T22:02:00Z'), ('0a1b2c', 3, 'ann@k', '"hmm"', '2026-10-23T22:03:00Z');
INSERT INTO chain VALUES ('0a1b2c', 'bot@k', 5, 0, 0);`

// A store made by 0.34.0 gains its columns in place, keeps its rows and its
// hex room, and a delivery held across the upgrade settles nothing said
// before it; opening again changes nothing.
func TestMigratesA034Store(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rooms.db")
	old, err := sql.Open("sqlite", path)
	must(t, err)
	_, err = old.Exec(v034)
	must(t, err)
	old.Close()
	for i := 0; i < 2; i++ {
		s, err := Open(path, "")
		must(t, err)
		if i == 0 {
			must(t, s.Answer("bot@k", "0a1b2c", 5, raw("ok"), false))
		}
		got, _ := standing(t, s, "0a1b2c")
		if want := `ann@k:"hi"/0|bot@k:"hello"/0|ann@k:"hmm"/0|bot@k:"ok"/1`; got != want {
			t.Fatalf("open %d: room =\n%s\nwant\n%s", i, got, want)
		}
		if n, _ := s.NextDelivery(); n <= 5 {
			t.Errorf("delivery ids continue: %d", n)
		}
		s.Close()
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
// Rooms come newest activity first, an unread one marked where it stands.
func TestRoomsUnreadOrdering(t *testing.T) {
	s := mustStore(t)
	quiet, _ := s.CreateRoom("ann@k", "quiet", nil)
	busy, _ := s.CreateRoom("ann@k", "busy", []string{"bob@k"})
	s.Post(busy.ID, "bob@k", raw("hey"))
	s.Post(quiet.ID, "ann@k", raw("me"))
	s.MarkRead("ann@k", quiet.ID, 1)
	rooms, _ := s.Rooms("ann@k")
	if len(rooms) != 2 || rooms[0].ID != quiet.ID || rooms[0].Unread || rooms[1].ID != busy.ID || !rooms[1].Unread {
		t.Fatalf("rooms = %+v", rooms)
	}
	s.MarkRead("ann@k", busy.ID, 1)
	rooms, _ = s.Rooms("ann@k")
	if rooms[0].Unread || rooms[1].Unread {
		t.Errorf("after reading: %+v", rooms)
	}
	es, _, _ := s.After("ann@k", busy.ID, 0)
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

// Working is who holds an unanswered delivery in the room, never the viewer,
// and nobody once it is answered.
func TestWorking(t *testing.T) {
	s := mustStore(t)
	r, _ := s.CreateRoom("ann@k", "cafe", []string{"bob@k", "cy@k"})
	s.Delivered("cy@k", r.ID, 1, 7, 0)
	s.Delivered("bob@k", r.ID, 1, 8, 0)
	if ws, _ := s.Working(r.ID, "ann@k"); strings.Join(ws, ",") != "bob@k,cy@k" {
		t.Errorf("working = %v", ws)
	}
	if ws, _ := s.Working(r.ID, "bob@k"); strings.Join(ws, ",") != "cy@k" {
		t.Errorf("the viewer is not among them: %v", ws)
	}
	s.Answer("bob@k", r.ID, 8, raw("done"), false)
	if ws, _ := s.Working(r.ID, "ann@k"); strings.Join(ws, ",") != "cy@k" {
		t.Errorf("an answered delivery is no longer held: %v", ws)
	}
}
