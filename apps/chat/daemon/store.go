package daemon

import (
	"crypto/rand"
	"crypto/sha256"
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

// Room is a chat room (a DM is a two-member room). Unread is a display flag (0/1)
// the plugin sets from the viewer's outstanding doorbell steps; the store never
// computes it (unread is a Juice-steps concern, not a store concern).
type Room struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Creator   string   `json:"creator"` // @handle
	Members   []string `json:"members"`
	CreatedAt string   `json:"created_at"`
	Unread    int      `json:"unread"`
	// LastAt is the timestamp of the room's most recent live message (falling back
	// to CreatedAt for an empty room). Filled only in list views (RoomsFor), where
	// it drives recency ordering; empty elsewhere.
	LastAt string `json:"last_at,omitempty"`
}

// Message is one posted message. Seq is the monotonic per-room position used for
// incremental reads, history paging, and the read watermark.
type Message struct {
	ID   string `json:"id"`
	Seq  int64  `json:"seq"`
	From string `json:"from"` // @handle of the sender (unforgeable — the kernel-injected key)
	Text string `json:"text"`
	At   string `json:"at"`
	// RoomID is set only by Inbox (which spans rooms), so a caller can address a
	// reply; omitted by the per-room reads where the room is already known.
	RoomID string `json:"room_id,omitempty"`
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
CREATE TABLE IF NOT EXISTS tokens (
  hash       TEXT PRIMARY KEY,
  username   TEXT NOT NULL,
  active     INTEGER NOT NULL DEFAULT 0,
  reg_id     TEXT NOT NULL UNIQUE,
  created_at TEXT NOT NULL
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

// requireMember loads a live room and checks actor is a member of it.
func (s *Store) requireMember(actor, roomID string) (Room, error) {
	r, err := s.liveRoom(s.db, roomID)
	if err != nil {
		return Room{}, err
	}
	if !s.isMember(roomID, actor) {
		return Room{}, ErrNotMember
	}
	return r, nil
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
	if _, err := s.requireMember(actor, roomID); err != nil {
		return Message{}, err
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
	r, err := s.requireMember(viewer, roomID)
	if err != nil {
		return Room{}, nil, err
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
	if _, err := s.requireMember(viewer, roomID); err != nil {
		return nil, err
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

// Messages is the member-facing READ path (the twin of PostMessage): up to
// recentWindow of the room's most recent live messages with seq > after
// (after=0 → the recent window), oldest-first; member only. It is stateless — no
// watermark, no marking-seen — because unread is tracked as Juice steps, so reading
// is a pure query. A non-UI member (e.g. an agent) fetches room content this way.
func (s *Store) Messages(viewer, roomID string, after int64) ([]Message, error) {
	if _, err := s.requireMember(viewer, roomID); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(`SELECT id, seq, sender, text, at FROM messages
		WHERE room_id = ? AND deleted = 0 AND seq > ? ORDER BY seq DESC LIMIT ?`,
		roomID, after, recentWindow)
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

// Poll is the incremental live read the conversation view uses instead of
// re-rendering the window each tick: `added` is live messages with seq > after (to
// append, oldest-first); `deleted` is the ids of recently soft-deleted messages (a
// delete flips a flag on an existing row, so an append-only read would otherwise
// miss it) — bounded to the recent window, since older ones scroll out of view.
// Member only; stateless. An OOB-remove of an absent id is a client no-op, so the
// scan need not be exact.
func (s *Store) Poll(viewer, roomID string, after int64) (added []Message, deleted []string, err error) {
	if _, err = s.requireMember(viewer, roomID); err != nil {
		return nil, nil, err
	}
	rows, err := s.db.Query(`SELECT id, seq, sender, text, at FROM messages
		WHERE room_id = ? AND deleted = 0 AND seq > ? ORDER BY seq DESC LIMIT ?`,
		roomID, after, recentWindow)
	if err != nil {
		return nil, nil, err
	}
	if added, err = scanMessages(rows); err != nil {
		return nil, nil, err
	}
	reverse(added)
	drows, err := s.db.Query(`SELECT id FROM messages
		WHERE room_id = ? AND deleted = 1 AND seq > ? ORDER BY seq DESC LIMIT ?`,
		roomID, after-recentWindow, recentWindow)
	if err != nil {
		return nil, nil, err
	}
	defer drows.Close()
	for drows.Next() {
		var id string
		if err = drows.Scan(&id); err != nil {
			return nil, nil, err
		}
		deleted = append(deleted, id)
	}
	return added, deleted, drows.Err()
}

// RoomsFor returns the live rooms viewer is a member of, most-recent-activity
// first. Unread is NOT computed here: unread is a Juice-steps concern (the plugin
// fills Room.Unread from the viewer's outstanding doorbell steps), so the store
// stays a pure message log + membership.
func (s *Store) RoomsFor(viewer string) ([]Room, error) {
	// last_at is the newest live message's timestamp (a one-row seek on the
	// (room_id, seq) primary key), falling back to created_at for an empty room;
	// both are RFC3339 text, so lexical DESC is chronological.
	rows, err := s.db.Query(`
		SELECT r.id, r.name, r.creator, r.created_at,
		  COALESCE((SELECT m2.at FROM messages m2
		     WHERE m2.room_id = r.id AND m2.deleted = 0
		     ORDER BY m2.seq DESC LIMIT 1), r.created_at) AS last_at
		FROM rooms r
		JOIN members mem ON mem.room_id = r.id AND mem.handle = ?
		WHERE r.deleted = 0
		ORDER BY last_at DESC, r.created_at DESC`, viewer)
	if err != nil {
		return nil, err
	}
	var out []Room
	for rows.Next() {
		var r Room
		if err := rows.Scan(&r.ID, &r.Name, &r.Creator, &r.CreatedAt, &r.LastAt); err != nil {
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

// Recipients returns a live room's display name and its member handles — the
// daemon builds a post-message NotifyEvent from it. ErrNotFound if the room is
// gone.
func (s *Store) Recipients(roomID string) (name string, members []string, err error) {
	if err = s.db.QueryRow(`SELECT name FROM rooms WHERE id = ? AND deleted = 0`, roomID).Scan(&name); err != nil {
		if err == sql.ErrNoRows {
			return "", nil, ErrNotFound
		}
		return "", nil, err
	}
	members, err = s.members(s.db, roomID)
	return name, members, err
}

// ---- api tokens (register / verify) ----
//
// A token is @chat's own per-caller credential: the caller registers a @handle,
// receives an opaque API key (stored here only as its hash), and — once the handle
// owner accepts the verification step — the key is activated. The kernel injects the
// key on every @chat/msg/* call; the daemon's auth hook recovers the caller by hashing
// it and looking up the (active) row. Registration and verification are the plugin's
// job (they need Juice); the store only mints, activates, and resolves.

// RegisterToken mints a pending (inactive) API key for username, storing only its
// hash. It returns the raw key (shown to the caller once) and the reg_id used to
// activate it. A standalone daemon can register directly; the plugin drives it from
// the open @chat/register action.
func (s *Store) RegisterToken(username string) (rawKey, regID string, err error) {
	if username == "" {
		return "", "", ErrBadInput
	}
	rawKey, regID = newToken(), newToken()
	if _, err = s.db.Exec(`INSERT INTO tokens(hash, username, active, reg_id, created_at)
		VALUES(?,?,0,?,?)`, hashToken(rawKey), username, regID, now()); err != nil {
		return "", "", err
	}
	return rawKey, regID, nil
}

// ActivateToken flips the pending key identified by regID to active. ErrNotFound if
// no such registration exists. Called when the verification step is completed.
func (s *Store) ActivateToken(regID string) error {
	if regID == "" {
		return ErrBadInput
	}
	res, err := s.db.Exec(`UPDATE tokens SET active = 1 WHERE reg_id = ?`, regID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ResolveToken maps a RAW API key (as the kernel injects it) to its username and
// active flag, hashing it first — the daemon's auth hook calls this. An empty or
// unknown key resolves to ("", false, nil), an unauthenticated caller.
func (s *Store) ResolveToken(raw string) (username string, active bool, err error) {
	if raw == "" {
		return "", false, nil
	}
	return s.LookupToken(hashToken(raw))
}

// LookupToken resolves a token hash to its username and active flag. A miss returns
// ("", false, nil) — an unknown key is an unauthenticated caller, not an error.
func (s *Store) LookupToken(hash string) (username string, active bool, err error) {
	var act int
	err = s.db.QueryRow(`SELECT username, active FROM tokens WHERE hash = ?`, hash).Scan(&username, &act)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return username, act == 1, nil
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

// newToken returns a 24-byte cryptographically-random hex string, used for API
// keys, registration ids, and room/message ids.
func newToken() string {
	var b [24]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// hashToken returns the hex SHA-256 of a raw API key. Only the hash is persisted, so
// a store leak never exposes usable keys.
func hashToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}
