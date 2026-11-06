package chat

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
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

// A room's name is its creator's to change, and is never blank; a stranger, a
// member and a room that is not there are each refused, and the rail's list
// shows the new name.
func TestRename(t *testing.T) {
	s := mustStore(t)
	r, err := s.CreateRoom("ann@k", "cafe", []string{"bob@k"})
	must(t, err)
	must(t, s.Rename("ann@k", r.ID, "  tea room  "))
	rooms, err := s.Rooms("bob@k")
	must(t, err)
	if len(rooms) != 1 || rooms[0].Name != "tea room" {
		t.Errorf("rooms = %+v", rooms)
	}
	for who, want := range map[string]error{"bob@k": ErrForbidden, "eve@k": ErrForbidden} {
		if err := s.Rename(who, r.ID, "mine"); !errors.Is(err, want) {
			t.Errorf("%s renames: %v", who, err)
		}
	}
	if err := s.Rename("ann@k", r.ID, "   "); !errors.Is(err, ErrBadInput) {
		t.Errorf("a blank name: %v", err)
	}
	if err := s.Rename("ann@k", "no-such-1", "x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("no such room: %v", err)
	}
	room, _, err := s.Room("ann@k", r.ID)
	must(t, err)
	if room.Name != "tea room" {
		t.Errorf("a refusal changed the name: %q", room.Name)
	}
}

// No key is ever retired: register is open and names any address, so a second
// registration, pending or active, leaves a member's working key working.
func TestARegistrationRetiresNoKey(t *testing.T) {
	s := mustStore(t)
	first, id, err := s.RegisterKey("ann@k")
	must(t, err)
	must(t, s.ActivateKey(id))
	if _, _, err := s.RegisterKey("ann@k"); err != nil { // anyone's, never completed
		t.Fatal(err)
	}
	if m, ok := s.Member(first); !ok || m != "ann@k" {
		t.Errorf("a pending registration revoked the key: %q %v", m, ok)
	}
	second, id2, err := s.RegisterKey("ann@k")
	must(t, err)
	must(t, s.ActivateKey(id2))
	for _, k := range []string{first, second} {
		if m, ok := s.Member(k); !ok || m != "ann@k" {
			t.Errorf("both keys work: %q %v", m, ok)
		}
	}
}

// A room reads as its action's output schema declares, members a list.
func TestARoomsWireShape(t *testing.T) {
	s := mustStore(t)
	r, err := s.CreateRoom("ann@k", "cafe", nil)
	must(t, err)
	b, _ := json.Marshal(r)
	for _, want := range []string{`"thread_id":"` + r.ID + `"`, `"name":"cafe"`, `"creator":"ann@k"`, `"members":["ann@k"]`, `"unread":false`, `"last_at":""`, `"created_at":"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("%s lacks %s", b, want)
		}
	}
	b, _ = json.Marshal(Entry{Seq: 1, Author: "a@k", Body: json.RawMessage(`"x"`), At: "t", Kind: KindAnswer})
	if string(b) != `{"seq":1,"author":"a@k","body":"x","at":"t","kind":1}` {
		t.Errorf("entry = %s", b)
	}
}

// A store in WAL mode can hold committed entries only in its log. Moving it folds
// the log in first, so nothing is lost; with the store in use, or a store at the
// destination, it refuses; with nothing to move it does nothing.
func TestMoveKeepsWhatTheLogHolds(t *testing.T) {
	dir := t.TempDir()
	from, to := filepath.Join(dir, "old.db"), filepath.Join(dir, "new.db")
	if err := Move(from, to); err != nil {
		t.Fatal(err)
	}
	s, err := Open(from, "")
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.CreateRoom("ann@k", "cafe", []string{"bob@k"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Post(r.ID, "ann@k", raw("in the log")); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(from + "-wal"); err != nil || fi.Size() == 0 {
		t.Fatalf("the entry should be in the log: %v", err)
	}
	// The process died here: its log is left behind.
	saved, _ := os.ReadFile(from + "-wal")
	snapshot, _ := os.ReadFile(from)
	_ = s.Close()
	_ = os.WriteFile(from, snapshot, 0o600)
	_ = os.WriteFile(from+"-wal", saved, 0o600)
	if err := Move(from, to); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(from); !os.IsNotExist(err) {
		t.Errorf("the old store stays: %v", err)
	}
	moved, err := Open(to, "")
	if err != nil {
		t.Fatal(err)
	}
	defer moved.Close()
	if _, es, err := moved.Room("ann@k", r.ID); err != nil || len(es) != 1 || string(es[0].Body) != `"in the log"` {
		t.Errorf("after the move: %+v %v", es, err)
	}
	if err := os.WriteFile(from, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Move(from, to); err == nil || !strings.Contains(err.Error(), "both") {
		t.Errorf("a store at both paths: %v", err)
	}
}
