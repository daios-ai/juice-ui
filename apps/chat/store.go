package chat

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver, registered as "sqlite"
)

// Store is chat's state: rooms and their members, the entries said in them,
// the keys members post with, each member's cursors in a room, and the
// deliveries a member holds. It knows nothing of the kernel: who may do what
// is decided here by membership alone, and every state change that must hold
// together is one transaction.
type Store struct {
	db *sql.DB
}

// The store's refusals, mapped to a status by the handlers.
var (
	ErrNotFound  = errors.New("not found")
	ErrForbidden = errors.New("forbidden")
	ErrNotMember = errors.New("not a member")
	ErrBadInput  = errors.New("bad input")
	ErrHeld      = errors.New("a post is already pending; answer the delivery first")
)

// Room is a conversation among its members. Unread and LastAt are filled by
// Rooms, for the list.
type Room struct {
	ID        string
	Name      string
	Creator   string
	Members   []string
	CreatedAt string
	Unread    bool
	LastAt    string
}

// Entry is one thing said in a room. Body is any JSON value; a sentence is a
// JSON string.
type Entry struct {
	Seq    int64
	Author string
	Body   json.RawMessage
	At     string
}

const schema = `
CREATE TABLE IF NOT EXISTS rooms (
  id         TEXT PRIMARY KEY,
  name       TEXT NOT NULL,
  creator    TEXT NOT NULL,
  created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS members (
  room_id TEXT NOT NULL,
  member  TEXT NOT NULL,
  PRIMARY KEY (room_id, member)
);
CREATE TABLE IF NOT EXISTS entries (
  room_id TEXT NOT NULL,
  seq     INTEGER NOT NULL,
  author  TEXT NOT NULL,
  body    TEXT NOT NULL,
  at      TEXT NOT NULL,
  PRIMARY KEY (room_id, seq)
);
CREATE TABLE IF NOT EXISTS keys (
  hash   TEXT PRIMARY KEY,
  member TEXT NOT NULL,
  active INTEGER NOT NULL DEFAULT 0,
  key_id TEXT NOT NULL UNIQUE
);
CREATE TABLE IF NOT EXISTS cursors (
  room_id   TEXT NOT NULL,
  member    TEXT NOT NULL,
  delivered INTEGER NOT NULL DEFAULT 0,
  read      INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (room_id, member)
);
CREATE TABLE IF NOT EXISTS chain (
  room_id  TEXT NOT NULL,
  member   TEXT NOT NULL,
  delivery INTEGER NOT NULL,
  depth    INTEGER NOT NULL,
  posted   INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (room_id, member, delivery)
);
CREATE TABLE IF NOT EXISTS seqs (name TEXT PRIMARY KEY, n INTEGER NOT NULL);
INSERT OR IGNORE INTO seqs(name, n) VALUES ('delivery', 0);`

// window bounds what a read returns: a room's latest entries.
const window = 200

// Open opens the store at path, creating it, or a private in-memory store
// when path is "". When the file does not exist yet and legacy names the
// previous chat application's database, its rooms, members and messages are
// imported once; the legacy file itself is never written.
func Open(path, legacy string) (*Store, error) {
	fresh := path == ""
	if !fresh {
		_, err := os.Stat(path)
		fresh = os.IsNotExist(err)
	}
	dsn := path
	if path == "" {
		dsn = ":memory:"
	}
	db, err := sql.Open("sqlite", dsn+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // one connection: writes serialize, and a memory store stays one database
	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("chat store schema: %w", err)
	}
	s := &Store{db: db}
	if fresh && path != "" && legacy != "" {
		if _, err := os.Stat(legacy); err == nil {
			if err := s.importLegacy(legacy); err != nil {
				_ = db.Close()
				return nil, fmt.Errorf("chat store: import %s: %w", legacy, err)
			}
		}
	}
	return s, nil
}

// Close releases the database.
func (s *Store) Close() error { return s.db.Close() }

// importLegacy copies the previous application's live rooms, members and
// messages, a message's text becoming a JSON string body. Keys are not
// copied: a member connects again.
func (s *Store) importLegacy(path string) error {
	old, err := sql.Open("sqlite", path+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return err
	}
	defer old.Close()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := old.Query(`SELECT id, name, creator, created_at FROM rooms WHERE deleted = 0`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id, name, creator, at string
		if err := rows.Scan(&id, &name, &creator, &at); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT OR IGNORE INTO rooms(id, name, creator, created_at) VALUES(?,?,?,?)`, id, name, creator, at); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	rows, err = old.Query(`SELECT m.room_id, m.handle FROM members m JOIN rooms r ON r.id = m.room_id WHERE r.deleted = 0`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var room, member string
		if err := rows.Scan(&room, &member); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT OR IGNORE INTO members(room_id, member) VALUES(?,?)`, room, member); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	rows, err = old.Query(`SELECT m.room_id, m.seq, m.sender, m.text, m.at FROM messages m JOIN rooms r ON r.id = m.room_id WHERE r.deleted = 0 AND m.deleted = 0`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var room, author, text, at string
		var seq int64
		if err := rows.Scan(&room, &seq, &author, &text, &at); err != nil {
			return err
		}
		body, _ := json.Marshal(text)
		if _, err := tx.Exec(`INSERT OR IGNORE INTO entries(room_id, seq, author, body, at) VALUES(?,?,?,?,?)`, room, seq, author, string(body), at); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return tx.Commit()
}

// ---- rooms and members ----

// CreateRoom makes a room owned by creator holding creator and members.
func (s *Store) CreateRoom(creator, name string, members []string) (Room, error) {
	if name == "" || creator == "" {
		return Room{}, ErrBadInput
	}
	tx, err := s.db.Begin()
	if err != nil {
		return Room{}, err
	}
	defer func() { _ = tx.Rollback() }()
	r := Room{ID: newToken(), Name: name, Creator: creator, CreatedAt: now()}
	if _, err := tx.Exec(`INSERT INTO rooms(id, name, creator, created_at) VALUES(?,?,?,?)`, r.ID, r.Name, r.Creator, r.CreatedAt); err != nil {
		return Room{}, err
	}
	for _, m := range append([]string{creator}, members...) {
		if m == "" {
			continue
		}
		if _, err := tx.Exec(`INSERT OR IGNORE INTO members(room_id, member) VALUES(?,?)`, r.ID, m); err != nil {
			return Room{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return Room{}, err
	}
	r.Members, err = s.members(r.ID)
	return r, err
}

// AddMember adds member to the room; the creator only.
func (s *Store) AddMember(actor, roomID, member string) error {
	if member == "" {
		return ErrBadInput
	}
	if _, err := s.requireCreator(actor, roomID); err != nil {
		return err
	}
	_, err := s.db.Exec(`INSERT OR IGNORE INTO members(room_id, member) VALUES(?,?)`, roomID, member)
	return err
}

// RemoveMember removes member from the room; the creator only, never itself.
func (s *Store) RemoveMember(actor, roomID, member string) error {
	if _, err := s.requireCreator(actor, roomID); err != nil {
		return err
	}
	if member == actor {
		return ErrForbidden
	}
	return s.drop(roomID, member)
}

// Leave removes actor from the room; a member that is not the creator.
func (s *Store) Leave(actor, roomID string) error {
	r, err := s.requireMember(actor, roomID)
	if err != nil {
		return err
	}
	if r.Creator == actor {
		return ErrForbidden
	}
	return s.drop(roomID, actor)
}

// drop removes a member and what the room holds for it.
func (s *Store) drop(roomID, member string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, q := range []string{
		`DELETE FROM members WHERE room_id = ? AND member = ?`,
		`DELETE FROM cursors WHERE room_id = ? AND member = ?`,
		`DELETE FROM chain WHERE room_id = ? AND member = ?`,
	} {
		if _, err := tx.Exec(q, roomID, member); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// DeleteRoom removes the room and everything in it; the creator only.
func (s *Store) DeleteRoom(actor, roomID string) error {
	if _, err := s.requireCreator(actor, roomID); err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, t := range []string{"members", "entries", "cursors", "chain", "rooms"} {
		col := "room_id"
		if t == "rooms" {
			col = "id"
		}
		if _, err := tx.Exec(`DELETE FROM `+t+` WHERE `+col+` = ?`, roomID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) room(roomID string) (Room, error) {
	var r Room
	err := s.db.QueryRow(`SELECT id, name, creator, created_at FROM rooms WHERE id = ?`, roomID).Scan(&r.ID, &r.Name, &r.Creator, &r.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Room{}, ErrNotFound
	}
	return r, err
}

func (s *Store) requireCreator(actor, roomID string) (Room, error) {
	r, err := s.room(roomID)
	if err != nil {
		return Room{}, err
	}
	if r.Creator != actor {
		return Room{}, ErrForbidden
	}
	return r, nil
}

func (s *Store) requireMember(actor, roomID string) (Room, error) {
	r, err := s.room(roomID)
	if err != nil {
		return Room{}, err
	}
	if !s.isMember(roomID, actor) {
		return Room{}, ErrNotMember
	}
	return r, nil
}

func (s *Store) isMember(roomID, member string) bool {
	var n int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM members WHERE room_id = ? AND member = ?`, roomID, member).Scan(&n)
	return n > 0
}

func (s *Store) members(roomID string) ([]string, error) {
	rows, err := s.db.Query(`SELECT member FROM members WHERE room_id = ? ORDER BY member`, roomID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var m string
		if err := rows.Scan(&m); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ---- reads ----

// Rooms lists the viewer's rooms, the ones with something unread first, then
// by last activity.
func (s *Store) Rooms(viewer string) ([]Room, error) {
	rows, err := s.db.Query(`
		SELECT r.id, r.name, r.creator, r.created_at,
		       COALESCE((SELECT MAX(seq) FROM entries e WHERE e.room_id = r.id), 0) > COALESCE((SELECT read FROM cursors c WHERE c.room_id = r.id AND c.member = ?), 0),
		       COALESCE((SELECT MAX(at) FROM entries e WHERE e.room_id = r.id), r.created_at)
		FROM rooms r JOIN members m ON m.room_id = r.id AND m.member = ?
		ORDER BY 5 DESC, 6 DESC`, viewer, viewer)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Room
	for rows.Next() {
		var r Room
		if err := rows.Scan(&r.ID, &r.Name, &r.Creator, &r.CreatedAt, &r.Unread, &r.LastAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		if out[i].Members, err = s.members(out[i].ID); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Room is a room with its members and its latest entries, oldest first; a
// member only.
func (s *Store) Room(viewer, roomID string) (Room, []Entry, error) {
	r, err := s.requireMember(viewer, roomID)
	if err != nil {
		return Room{}, nil, err
	}
	if r.Members, err = s.members(roomID); err != nil {
		return Room{}, nil, err
	}
	es, err := s.entries(`room_id = ?`, roomID)
	return r, es, err
}

// After is the room's entries newer than seq, oldest first; a member only.
func (s *Store) After(viewer, roomID string, seq int64) ([]Entry, error) {
	if _, err := s.requireMember(viewer, roomID); err != nil {
		return nil, err
	}
	return s.entries(`room_id = ? AND seq > ?`, roomID, seq)
}

// entries reads the newest entries satisfying where, at most the window,
// and returns them oldest first.
func (s *Store) entries(where string, args ...any) ([]Entry, error) {
	return s.query(`SELECT seq, author, body, at FROM entries WHERE `+where+` ORDER BY seq DESC LIMIT ?`, append(args, window)...)
}

func (s *Store) query(q string, args ...any) ([]Entry, error) {
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Entry
	for rows.Next() {
		var e Entry
		var body string
		if err := rows.Scan(&e.Seq, &e.Author, &body, &e.At); err != nil {
			return nil, err
		}
		e.Body = json.RawMessage(body)
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

// MarkRead records that member has seen the room up to seq.
func (s *Store) MarkRead(member, roomID string, seq int64) error {
	_, err := s.db.Exec(`INSERT INTO cursors(room_id, member, read) VALUES(?,?,?)
		ON CONFLICT(room_id, member) DO UPDATE SET read = MAX(read, excluded.read)`, roomID, member, seq)
	return err
}

// ---- posting and delivery ----

// Post appends what author says, in one transaction with the rule that a
// member holding a delivery in the room posts once and then answers: it is
// refused with ErrHeld when the author holds one and has already posted. It
// returns the entry and the post's depth: 0 when the author holds nothing,
// else one more than the deepest delivery held.
func (s *Store) Post(roomID, author string, body json.RawMessage) (Entry, int64, error) {
	if len(body) == 0 || !json.Valid(body) {
		return Entry{}, 0, ErrBadInput
	}
	if _, err := s.requireMember(author, roomID); err != nil {
		return Entry{}, 0, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return Entry{}, 0, err
	}
	defer func() { _ = tx.Rollback() }()
	var held int
	var depth int64
	var posted bool
	if err := tx.QueryRow(`SELECT COUNT(*), COALESCE(MAX(depth), -1) + 1, COALESCE(MAX(posted), 0)
		FROM chain WHERE room_id = ? AND member = ?`, roomID, author).Scan(&held, &depth, &posted); err != nil {
		return Entry{}, 0, err
	}
	if held > 0 && posted {
		return Entry{}, 0, ErrHeld
	}
	e, err := appendEntry(tx, roomID, author, body)
	if err != nil {
		return Entry{}, 0, err
	}
	if held > 0 {
		if _, err := tx.Exec(`UPDATE chain SET posted = 1 WHERE room_id = ? AND member = ?`, roomID, author); err != nil {
			return Entry{}, 0, err
		}
	}
	return e, depth, tx.Commit()
}

func appendEntry(tx *sql.Tx, roomID, author string, body json.RawMessage) (Entry, error) {
	e := Entry{Author: author, Body: body, At: now()}
	if err := tx.QueryRow(`SELECT COALESCE(MAX(seq), 0) + 1 FROM entries WHERE room_id = ?`, roomID).Scan(&e.Seq); err != nil {
		return Entry{}, err
	}
	_, err := tx.Exec(`INSERT INTO entries(room_id, seq, author, body, at) VALUES(?,?,?,?,?)`, roomID, e.Seq, author, string(body), e.At)
	return e, err
}

// Range is what a delivery to member carries: the entries with seq after the
// member's delivered cursor up to upto, by others than the member, at most
// limit of the newest, oldest first.
func (s *Store) Range(member, roomID string, upto int64, limit int) ([]Entry, error) {
	return s.query(`SELECT seq, author, body, at FROM entries
		WHERE room_id = ? AND seq <= ? AND author != ?
		  AND seq > COALESCE((SELECT delivered FROM cursors WHERE room_id = ? AND member = ?), 0)
		ORDER BY seq DESC LIMIT ?`, roomID, upto, member, roomID, member, limit)
}

// NextDelivery hands out the next delivery id; never reused.
func (s *Store) NextDelivery() (int64, error) {
	var n int64
	err := s.db.QueryRow(`UPDATE seqs SET n = n + 1 WHERE name = 'delivery' RETURNING n`).Scan(&n)
	return n, err
}

// Delivered records a delivery the kernel accepted: the member's delivered
// cursor moves to upto and the member holds the delivery at depth.
func (s *Store) Delivered(member, roomID string, upto, delivery, depth int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`INSERT INTO cursors(room_id, member, delivered) VALUES(?,?,?)
		ON CONFLICT(room_id, member) DO UPDATE SET delivered = MAX(delivered, excluded.delivered)`, roomID, member, upto); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT OR REPLACE INTO chain(room_id, member, delivery, depth, posted) VALUES(?,?,?,?,0)`, roomID, member, delivery, depth); err != nil {
		return err
	}
	return tx.Commit()
}

// Answer records a completed delivery: the member no longer holds it, its
// posted mark in the room is cleared, and body, when given, is appended as
// the member's entry. All in one transaction.
func (s *Store) Answer(member, roomID string, delivery int64, body json.RawMessage) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`DELETE FROM chain WHERE room_id = ? AND member = ? AND delivery = ?`, roomID, member, delivery); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE chain SET posted = 0 WHERE room_id = ? AND member = ?`, roomID, member); err != nil {
		return err
	}
	if body != nil {
		if !json.Valid(body) {
			return ErrBadInput
		}
		var n int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM members WHERE room_id = ? AND member = ?`, roomID, member).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			if _, err := appendEntry(tx, roomID, member, body); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

// ---- keys ----

// RegisterKey mints a pending key for member and returns it with the id its
// activation names. Only the key's hash is kept.
func (s *Store) RegisterKey(member string) (raw, keyID string, err error) {
	if member == "" {
		return "", "", ErrBadInput
	}
	raw, keyID = newToken(), newToken()
	_, err = s.db.Exec(`INSERT INTO keys(hash, member, active, key_id) VALUES(?,?,0,?)`, hashKey(raw), member, keyID)
	return raw, keyID, err
}

// ActivateKey activates the pending key registered under keyID.
func (s *Store) ActivateKey(keyID string) error {
	res, err := s.db.Exec(`UPDATE keys SET active = 1 WHERE key_id = ?`, keyID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Member resolves a raw key to the member it was activated for.
func (s *Store) Member(raw string) (string, bool) {
	var member string
	var active bool
	err := s.db.QueryRow(`SELECT member, active FROM keys WHERE hash = ?`, hashKey(raw)).Scan(&member, &active)
	return member, err == nil && active && member != ""
}

// ---- helpers ----

func now() string { return time.Now().UTC().Format(time.RFC3339Nano) }

// newToken is 24 random bytes in hex: keys, key ids and room ids.
func newToken() string {
	var b [24]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func hashKey(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}
