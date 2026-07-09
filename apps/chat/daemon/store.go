package daemon

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver, registered as "sqlite"
)

// Store is the chat server's durable state: users and their keys, rooms,
// membership, an append-only per-room message log, and each user's per-room read
// watermark. It is the source of truth (the kernel ledger is only an audit
// trail). It is Juice-agnostic — plain SQLite behind a small typed API — so the
// daemon it backs could run as a standalone REST service unchanged.
//
// Reads are bounded and indexed: a room's messages are keyed by a monotonic
// per-room sequence, so listing is a windowed range scan, never a full-log read,
// and unread is a count past the watermark.
type Store struct {
	db *sql.DB
}

// Sentinel errors the REST layer maps to HTTP statuses.
var (
	ErrNotFound  = errors.New("not found")    // 404
	ErrForbidden = errors.New("forbidden")    // 403 — authenticated but not permitted
	ErrNotMember = errors.New("not a member") // 403 — caller is not in the room
	ErrBadInput  = errors.New("bad input")    // 400
)

// Room is a chat room (a DM is a two-member room). Unread is filled only in list
// views (RoomsFor); it is 0 elsewhere.
type Room struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Creator   string   `json:"creator"` // @handle
	Members   []string `json:"members"`
	CreatedAt string   `json:"created_at"`
	Unread    int      `json:"unread"`
}

// Message is one posted message. Seq is the monotonic per-room position used for
// incremental reads, history paging, and the read watermark.
type Message struct {
	ID   string `json:"id"`
	Seq  int64  `json:"seq"`
	From string `json:"from"` // @handle of the sender (unforgeable — the kernel-injected key)
	Text string `json:"text"`
	At   string `json:"at"`
}

const schema = `
CREATE TABLE IF NOT EXISTS rooms (
  id         TEXT PRIMARY KEY,
  name       TEXT NOT NULL,
  creator    TEXT NOT NULL,
  created_at TEXT NOT NULL,
  deleted    INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS members (
  room_id TEXT NOT NULL,
  handle  TEXT NOT NULL,
  PRIMARY KEY (room_id, handle)
);
CREATE TABLE IF NOT EXISTS messages (
  room_id TEXT NOT NULL,
  seq     INTEGER NOT NULL,
  id      TEXT NOT NULL,
  sender  TEXT NOT NULL,
  text    TEXT NOT NULL,
  at      TEXT NOT NULL,
  deleted INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (room_id, seq)
);
CREATE INDEX IF NOT EXISTS idx_messages_id ON messages(room_id, id);
CREATE TABLE IF NOT EXISTS reads (
  handle    TEXT NOT NULL,
  room_id   TEXT NOT NULL,
  watermark INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (handle, room_id)
);`

// recentWindow bounds how many of a room's latest messages a plain read returns;
// older messages are paged in via History. It keeps every read O(window), never
// proportional to a room's full history.
const recentWindow = 200

// Open opens (creating if needed) the SQLite store at path and applies the
// schema. An empty path opens a private in-memory database (for tests).
func Open(path string) (*Store, error) {
	dsn := path
	if path == "" {
		dsn = ":memory:"
	}
	dsn += "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(off)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// A single connection serialises writes and keeps an in-memory DB alive for
	// the process lifetime (each connection would otherwise get its own).
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("chat store schema: %w", err)
	}
	return &Store{db: db}, nil
}

// Close releases the database.
func (s *Store) Close() error { return s.db.Close() }

// ---- rooms & membership ----

// CreateRoom creates a room owned by creator, with creator plus any given member
// handles. Returns the stored room.
func (s *Store) CreateRoom(creator, name string, members []string) (Room, error) {
	if creator == "" || name == "" {
		return Room{}, ErrBadInput
	}
	id, at := newToken(), now()
	tx, err := s.db.Begin()
	if err != nil {
		return Room{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`INSERT INTO rooms(id, name, creator, created_at) VALUES(?,?,?,?)`, id, name, creator, at); err != nil {
		return Room{}, err
	}
	all := dedupeMembers(creator, members)
	for _, m := range all {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO members(room_id, handle) VALUES(?,?)`, id, m); err != nil {
			return Room{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return Room{}, err
	}
	return Room{ID: id, Name: name, Creator: creator, Members: all, CreatedAt: at}, nil
}

// liveRoom loads a non-deleted room (without members), or ErrNotFound.
func (s *Store) liveRoom(q queryer, id string) (Room, error) {
	var r Room
	var deleted int
	err := q.QueryRow(`SELECT id, name, creator, created_at, deleted FROM rooms WHERE id = ?`, id).
		Scan(&r.ID, &r.Name, &r.Creator, &r.CreatedAt, &deleted)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && deleted == 1) {
		return Room{}, ErrNotFound
	}
	return r, err
}

// requireCreator loads a live room and checks actor owns it.
func (s *Store) requireCreator(actor, roomID string) (Room, error) {
	r, err := s.liveRoom(s.db, roomID)
	if err != nil {
		return Room{}, err
	}
	if r.Creator != actor {
		return Room{}, ErrForbidden
	}
	return r, nil
}

// RenameRoom renames a room; creator only.
func (s *Store) RenameRoom(actor, roomID, name string) error {
	if name == "" {
		return ErrBadInput
	}
	if _, err := s.requireCreator(actor, roomID); err != nil {
		return err
	}
	_, err := s.db.Exec(`UPDATE rooms SET name = ? WHERE id = ?`, name, roomID)
	return err
}

// DeleteRoom soft-deletes a room; creator only.
func (s *Store) DeleteRoom(actor, roomID string) error {
	if _, err := s.requireCreator(actor, roomID); err != nil {
		return err
	}
	_, err := s.db.Exec(`UPDATE rooms SET deleted = 1 WHERE id = ?`, roomID)
	return err
}

// AddMember adds a member; creator only.
func (s *Store) AddMember(actor, roomID, member string) error {
	if member == "" {
		return ErrBadInput
	}
	if _, err := s.requireCreator(actor, roomID); err != nil {
		return err
	}
	_, err := s.db.Exec(`INSERT OR IGNORE INTO members(room_id, handle) VALUES(?,?)`, roomID, member)
	return err
}

// RemoveMember removes a member; creator only. The creator cannot be removed
// (they delete the room instead).
func (s *Store) RemoveMember(actor, roomID, member string) error {
	r, err := s.requireCreator(actor, roomID)
	if err != nil {
		return err
	}
	if member == "" || member == r.Creator {
		return ErrBadInput
	}
	_, err = s.db.Exec(`DELETE FROM members WHERE room_id = ? AND handle = ?`, roomID, member)
	return err
}

// LeaveRoom removes actor from a room. The creator cannot leave (it would orphan
// the room); they delete it instead.
func (s *Store) LeaveRoom(actor, roomID string) error {
	r, err := s.liveRoom(s.db, roomID)
	if err != nil {
		return err
	}
	if r.Creator == actor {
		return ErrForbidden
	}
	if !s.isMember(roomID, actor) {
		return ErrNotMember
	}
	_, err = s.db.Exec(`DELETE FROM members WHERE room_id = ? AND handle = ?`, roomID, actor)
	return err
}

// isMember reports whether handle is in a room.
func (s *Store) isMember(roomID, handle string) bool {
	var one int
	err := s.db.QueryRow(`SELECT 1 FROM members WHERE room_id = ? AND handle = ?`, roomID, handle).Scan(&one)
	return err == nil
}

// members returns a room's member handles.
func (s *Store) members(q queryer, roomID string) ([]string, error) {
	rows, err := q.Query(`SELECT handle FROM members WHERE room_id = ? ORDER BY handle`, roomID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// ---- messages ----

// PostMessage appends a message; members only. Returns the stored message with
// its assigned per-room sequence.
func (s *Store) PostMessage(actor, roomID, text string) (Message, error) {
	if text == "" {
		return Message{}, ErrBadInput
	}
	if _, err := s.liveRoom(s.db, roomID); err != nil {
		return Message{}, err
	}
	if !s.isMember(roomID, actor) {
		return Message{}, ErrNotMember
	}
	tx, err := s.db.Begin()
	if err != nil {
		return Message{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var seq int64
	if err := tx.QueryRow(`SELECT COALESCE(MAX(seq),0)+1 FROM messages WHERE room_id = ?`, roomID).Scan(&seq); err != nil {
		return Message{}, err
	}
	m := Message{ID: newToken(), Seq: seq, From: actor, Text: text, At: now()}
	if _, err := tx.Exec(`INSERT INTO messages(room_id, seq, id, sender, text, at) VALUES(?,?,?,?,?,?)`,
		roomID, m.Seq, m.ID, m.From, m.Text, m.At); err != nil {
		return Message{}, err
	}
	if err := tx.Commit(); err != nil {
		return Message{}, err
	}
	return m, nil
}

// DeleteMessage soft-deletes a message; sender only.
func (s *Store) DeleteMessage(actor, roomID, msgID string) error {
	if _, err := s.liveRoom(s.db, roomID); err != nil {
		return err
	}
	var sender string
	err := s.db.QueryRow(`SELECT sender FROM messages WHERE room_id = ? AND id = ? AND deleted = 0`, roomID, msgID).Scan(&sender)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if sender != actor {
		return ErrForbidden
	}
	_, err = s.db.Exec(`UPDATE messages SET deleted = 1 WHERE room_id = ? AND id = ?`, roomID, msgID)
	return err
}

// ---- reads (member-gated) ----

// Room returns a room (with members) and its most recent messages oldest-first,
// bounded to the recent window; member only.
func (s *Store) Room(viewer, roomID string) (Room, []Message, error) {
	r, err := s.liveRoom(s.db, roomID)
	if err != nil {
		return Room{}, nil, err
	}
	if !s.isMember(roomID, viewer) {
		return Room{}, nil, ErrNotMember
	}
	if r.Members, err = s.members(s.db, roomID); err != nil {
		return Room{}, nil, err
	}
	msgs, err := s.recent(roomID)
	return r, msgs, err
}

// recent returns the last recentWindow non-deleted messages, oldest-first.
func (s *Store) recent(roomID string) ([]Message, error) {
	rows, err := s.db.Query(`SELECT id, seq, sender, text, at FROM messages
		WHERE room_id = ? AND deleted = 0 ORDER BY seq DESC LIMIT ?`, roomID, recentWindow)
	if err != nil {
		return nil, err
	}
	msgs, err := scanMessages(rows)
	if err != nil {
		return nil, err
	}
	reverse(msgs)
	return msgs, nil
}

// History returns up to limit non-deleted messages before beforeSeq, oldest-first,
// for scrollback; member only.
func (s *Store) History(viewer, roomID string, beforeSeq int64, limit int) ([]Message, error) {
	if _, err := s.liveRoom(s.db, roomID); err != nil {
		return nil, err
	}
	if !s.isMember(roomID, viewer) {
		return nil, ErrNotMember
	}
	if limit <= 0 || limit > recentWindow {
		limit = recentWindow
	}
	rows, err := s.db.Query(`SELECT id, seq, sender, text, at FROM messages
		WHERE room_id = ? AND deleted = 0 AND seq < ? ORDER BY seq DESC LIMIT ?`, roomID, beforeSeq, limit)
	if err != nil {
		return nil, err
	}
	msgs, err := scanMessages(rows)
	if err != nil {
		return nil, err
	}
	reverse(msgs)
	return msgs, nil
}

// RoomsFor returns the live rooms viewer is a member of, most-recently-created
// first, each with the viewer's unread count.
func (s *Store) RoomsFor(viewer string) ([]Room, error) {
	rows, err := s.db.Query(`
		SELECT r.id, r.name, r.creator, r.created_at,
		  (SELECT COUNT(*) FROM messages m
		     WHERE m.room_id = r.id AND m.deleted = 0 AND m.sender <> ?
		       AND m.seq > COALESCE((SELECT watermark FROM reads WHERE handle = ? AND room_id = r.id), 0)) AS unread
		FROM rooms r
		JOIN members mem ON mem.room_id = r.id AND mem.handle = ?
		WHERE r.deleted = 0
		ORDER BY r.created_at DESC`, viewer, viewer, viewer)
	if err != nil {
		return nil, err
	}
	var out []Room
	for rows.Next() {
		var r Room
		if err := rows.Scan(&r.ID, &r.Name, &r.Creator, &r.CreatedAt, &r.Unread); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	// Close the cursor before issuing the per-room member queries: the store
	// runs on a single connection, so a nested query under an open cursor would
	// deadlock.
	rows.Close()
	for i := range out {
		if out[i].Members, err = s.members(s.db, out[i].ID); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// UnreadTotal is the viewer's unread count across all their rooms (the nav badge).
func (s *Store) UnreadTotal(viewer string) (int, error) {
	var n int
	err := s.db.QueryRow(`
		SELECT COUNT(*) FROM messages m
		JOIN members mem ON mem.room_id = m.room_id AND mem.handle = ?
		JOIN rooms r ON r.id = m.room_id AND r.deleted = 0
		WHERE m.deleted = 0 AND m.sender <> ?
		  AND m.seq > COALESCE((SELECT watermark FROM reads WHERE handle = ? AND room_id = m.room_id), 0)`,
		viewer, viewer, viewer).Scan(&n)
	return n, err
}

// MarkRead advances viewer's read watermark for a room to at least seq; member
// only. A watermark never moves backwards.
func (s *Store) MarkRead(viewer, roomID string, seq int64) error {
	if _, err := s.liveRoom(s.db, roomID); err != nil {
		return err
	}
	if !s.isMember(roomID, viewer) {
		return ErrNotMember
	}
	_, err := s.db.Exec(`
		INSERT INTO reads(handle, room_id, watermark) VALUES(?,?,?)
		ON CONFLICT(handle, room_id) DO UPDATE SET watermark = MAX(watermark, excluded.watermark)`,
		viewer, roomID, seq)
	return err
}

// ---- helpers ----

// queryer is the shared subset of *sql.DB and *sql.Tx used by read helpers.
type queryer interface {
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
}

func scanMessages(rows *sql.Rows) ([]Message, error) {
	defer rows.Close()
	var out []Message
	for rows.Next() {
		var m Message
		if err := rows.Scan(&m.ID, &m.Seq, &m.From, &m.Text, &m.At); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func reverse(m []Message) {
	for i, j := 0, len(m)-1; i < j; i, j = i+1, j-1 {
		m[i], m[j] = m[j], m[i]
	}
}

// dedupeMembers returns creator followed by the distinct non-empty members,
// creator first and never duplicated.
func dedupeMembers(creator string, members []string) []string {
	out := []string{creator}
	seen := map[string]bool{creator: true}
	for _, m := range members {
		if m == "" || seen[m] {
			continue
		}
		seen[m] = true
		out = append(out, m)
	}
	return out
}

// now is the current UTC time as an RFC 3339 string.
func now() string { return time.Now().UTC().Format(time.RFC3339Nano) }

// newToken returns a 24-byte cryptographically-random hex string, used for user
// keys and room/message ids.
func newToken() string {
	var b [24]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
