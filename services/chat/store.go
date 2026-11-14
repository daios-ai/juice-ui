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
	"slices"
	"strings"
	"time"

	"github.com/daios-ai/juice-ui/client"
	_ "modernc.org/sqlite" // pure-Go SQLite driver, registered as "sqlite"
)

// Store is chat's state: rooms and their members, the entries said in them,
// the keys members post with, each member's cursors in a room, the deliveries
// a member holds, and how the kernel last named each member. Who may do what is
// decided here by membership alone, and every state change that must hold
// together is one transaction.
//
// A member is held by its principal, user_id@kernel_key, which the kernel
// gives it and which names it on every kernel and through a rename. A store made
// before 0.72.0 holds its members by address, handle@kernel, until each is bound
// to its principal (Bind, Seat).
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
	ErrAlone     = errors.New("you are its only member; delete it instead")
)

// Room is a conversation among its members. A member is added, then invited
// once its invitation is delivered, and joined once it accepts: Members are the
// joined, who read, post and are delivered to; Invited are those who have not
// accepted yet, delivered or not. Unread and LastAt are filled by Rooms, for
// the list.
type Room struct {
	ID        string   `json:"thread_id"`
	Name      string   `json:"name"`
	Creator   string   `json:"creator"`
	Members   []string `json:"members"`
	Invited   []string `json:"invited"`
	CreatedAt string   `json:"created_at"`
	Unread    bool     `json:"unread"`
	LastAt    string   `json:"last_at"`
}

// Entry is one thing said in a room. Body is any JSON value; a sentence is a
// JSON string. Kind says how it came: a post, a delivery's answer, or a
// delivery's failure; it is stored, never read off the body.
type Entry struct {
	Seq    int64           `json:"seq"`
	Author string          `json:"author"`
	Body   json.RawMessage `json:"body"`
	At     string          `json:"at"`
	Kind   int             `json:"kind"`
}

// The kinds of entry.
const (
	KindPost   = 0
	KindAnswer = 1
	KindFailed = 2
)

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
  state   TEXT NOT NULL DEFAULT 'joined',
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
INSERT OR IGNORE INTO seqs(name, n) VALUES ('delivery', 0);
CREATE TABLE IF NOT EXISTS accounts (
  id       TEXT PRIMARY KEY,
  address  TEXT NOT NULL,
  nickname TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS legacy (
  address TEXT PRIMARY KEY,
  id      TEXT NOT NULL
);
DELETE FROM keys WHERE active = 0;`

// columns are added to a store made by an earlier version, in place. A
// delivery held before chain.upto existed covered the room as it stood then,
// so its upto is the room's last seq: the member's earlier posts are never
// settled by it. A member of a store made before invitations was joined.
var columns = []struct{ table, def, backfill string }{
	{"chain", "upto INTEGER NOT NULL DEFAULT 0", `UPDATE chain SET upto = COALESCE((SELECT MAX(seq) FROM entries e WHERE e.room_id = chain.room_id), 0)`},
	{"entries", "kind INTEGER NOT NULL DEFAULT 0", ""},
	{"entries", "replaced_by INTEGER NOT NULL DEFAULT 0", ""},
	{"members", "state TEXT NOT NULL DEFAULT 'joined'", ""},
}

func migrate(db *sql.DB) error {
	for _, c := range columns {
		if _, err := db.Exec(`ALTER TABLE ` + c.table + ` ADD COLUMN ` + c.def); err != nil {
			if strings.Contains(err.Error(), "duplicate column name") {
				continue
			}
			return err
		}
		if c.backfill != "" {
			if _, err := db.Exec(c.backfill); err != nil {
				return err
			}
		}
	}
	return nil
}

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
	if err := migrate(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("chat store migration: %w", err)
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

// Move brings a store made elsewhere to path, once: a store in WAL mode can hold
// committed entries only in its log, so the log is folded into the database file
// and that one file is renamed. An interruption before the rename leaves the old
// store whole for the next start. With a store at both paths it refuses, rather
// than merging, and with the old store in use it refuses too.
func Move(from, path string) error {
	_, ferr := os.Stat(from)
	_, perr := os.Stat(path)
	switch {
	case ferr != nil:
		return nil
	case perr == nil:
		return fmt.Errorf("a chat store is at both %s and %s", from, path)
	}
	db, err := sql.Open("sqlite", from+"?_pragma=busy_timeout(5000)")
	if err != nil {
		return err
	}
	var busy, logged, moved int
	err = db.QueryRow(`PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&busy, &logged, &moved)
	if cerr := db.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("chat store: checkpoint %s: %w", from, err)
	}
	if st, err := os.Stat(from + "-wal"); busy != 0 || (err == nil && st.Size() > 0) {
		return fmt.Errorf("chat store: %s is in use", from)
	}
	return os.Rename(from, path)
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
	id, err := insertRoom(tx, creator, name, members)
	if err != nil {
		return Room{}, err
	}
	if err := tx.Commit(); err != nil {
		return Room{}, err
	}
	return s.withMembers(id)
}

// The states of a member: added until the kernel takes its invitation, then
// invited until it accepts, then joined.
const (
	stateAdded   = "added"
	stateInvited = "invited"
	stateJoined  = "joined"
)

// insertRoom writes a room under a fresh word id, its creator joined and the
// other members added.
func insertRoom(tx *sql.Tx, creator, name string, members []string) (string, error) {
	var id string
	for {
		id = wordID()
		var n int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM rooms WHERE id = ?`, id).Scan(&n); err != nil {
			return "", err
		}
		if n == 0 {
			break
		}
	}
	if _, err := tx.Exec(`INSERT INTO rooms(id, name, creator, created_at) VALUES(?,?,?,?)`, id, name, creator, now()); err != nil {
		return "", err
	}
	if _, err := tx.Exec(`INSERT INTO members(room_id, member, state) VALUES(?,?,?)`, id, creator, stateJoined); err != nil {
		return "", err
	}
	for _, m := range members {
		if m == "" {
			continue
		}
		if _, err := tx.Exec(`INSERT OR IGNORE INTO members(room_id, member, state) VALUES(?,?,?)`, id, m, stateAdded); err != nil {
			return "", err
		}
	}
	return id, nil
}

func (s *Store) withMembers(id string) (Room, error) {
	r, err := s.room(id)
	if err != nil {
		return Room{}, err
	}
	if r.Members, err = s.members(id); err != nil {
		return Room{}, err
	}
	r.Invited, err = s.invited(id)
	return r, err
}

// Rename changes the room's name; the creator only. A name is never blank.
func (s *Store) Rename(actor, roomID, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return ErrBadInput
	}
	if _, err := s.requireCreator(actor, roomID); err != nil {
		return err
	}
	_, err := s.db.Exec(`UPDATE rooms SET name = ? WHERE id = ?`, name, roomID)
	return err
}

// AddMember adds member to the room; the creator only. It reports whether the
// member's invitation is still to be delivered: a member added now, or added
// earlier and never reached. A member invited or joined is left as it is, so
// one invitation is ever outstanding.
func (s *Store) AddMember(actor, roomID, member string) (bool, error) {
	if member == "" {
		return false, ErrBadInput
	}
	if _, err := s.requireCreator(actor, roomID); err != nil {
		return false, err
	}
	if _, err := s.db.Exec(`INSERT OR IGNORE INTO members(room_id, member, state) VALUES(?,?,?)`, roomID, member, stateAdded); err != nil {
		return false, err
	}
	var state string
	err := s.db.QueryRow(`SELECT state FROM members WHERE room_id = ? AND member = ?`, roomID, member).Scan(&state)
	return state == stateAdded, err
}

// Join is an invitation accepted: the member becomes joined, from any state. A
// member not in the room, never added or since removed, or having declined, is
// not found, so no invitation outlives its answer.
func (s *Store) Join(roomID, member string) error {
	res, err := s.db.Exec(`UPDATE members SET state = ? WHERE room_id = ? AND member = ?`, stateJoined, roomID, member)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Invited records that the kernel took member's invitation.
func (s *Store) Invited(roomID, member string) error {
	_, err := s.db.Exec(`UPDATE members SET state = ? WHERE room_id = ? AND member = ? AND state = ?`, stateInvited, roomID, member, stateAdded)
	return err
}

// Added is the room's members whose invitation the kernel has not taken yet.
func (s *Store) Added(roomID string) ([]string, error) { return s.inState(roomID, stateAdded) }

// Invitation is a room a member is invited to, as that member reads it: the
// room, its name, and its creator, who invited.
type Invitation struct {
	ID   string `json:"thread_id"`
	Name string `json:"name"`
	From string `json:"from"`
}

// Invitations is the rooms member is added or invited to, oldest first.
func (s *Store) Invitations(member string) ([]Invitation, error) {
	rows, err := s.db.Query(`SELECT r.id, r.name, r.creator FROM rooms r JOIN members m ON m.room_id = r.id
		WHERE m.member = ? AND m.state != ? ORDER BY r.created_at, r.id`, member, stateJoined)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Invitation{}
	for rows.Next() {
		var i Invitation
		if err := rows.Scan(&i.ID, &i.Name, &i.From); err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

// RemoveMember removes member from the room, invited or joined; the creator
// only, never itself.
func (s *Store) RemoveMember(actor, roomID, member string) error {
	if _, err := s.requireCreator(actor, roomID); err != nil {
		return err
	}
	if member == actor {
		return ErrForbidden
	}
	return s.drop(roomID, member)
}

// Leave removes actor from the room. An invited member leaving is the
// invitation declined. The creator leaving hands the room to the joined member
// held longest, in the same transaction; with no other joined member it is
// refused and nothing changes, since only Delete deletes a room.
func (s *Store) Leave(actor, roomID string) error {
	r, err := s.room(roomID)
	if err != nil {
		return err
	}
	if !s.inRoom(roomID, actor) {
		return ErrNotMember
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if r.Creator == actor {
		var heir string
		err := tx.QueryRow(`SELECT member FROM members WHERE room_id = ? AND state = ? AND member != ? ORDER BY rowid LIMIT 1`, roomID, stateJoined, actor).Scan(&heir)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrAlone
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE rooms SET creator = ? WHERE id = ?`, heir, roomID); err != nil {
			return err
		}
	}
	if err := dropIn(tx, roomID, actor); err != nil {
		return err
	}
	return tx.Commit()
}

// drop removes a member and what the room holds for it.
func (s *Store) drop(roomID, member string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := dropIn(tx, roomID, member); err != nil {
		return err
	}
	return tx.Commit()
}

// dropIn is drop within a transaction.
func dropIn(tx *sql.Tx, roomID, member string) error {
	for _, q := range []string{
		`DELETE FROM members WHERE room_id = ? AND member = ?`,
		`DELETE FROM cursors WHERE room_id = ? AND member = ?`,
		`DELETE FROM chain WHERE room_id = ? AND member = ?`,
	} {
		if _, err := tx.Exec(q, roomID, member); err != nil {
			return err
		}
	}
	return nil
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

// isMember reports a joined member; inRoom one invited or joined.
func (s *Store) isMember(roomID, member string) bool {
	var n int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM members WHERE room_id = ? AND member = ? AND state = ?`, roomID, member, stateJoined).Scan(&n)
	return n > 0
}

func (s *Store) inRoom(roomID, member string) bool {
	var n int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM members WHERE room_id = ? AND member = ?`, roomID, member).Scan(&n)
	return n > 0
}

// members is the room's joined members, invited the others, each in the
// order of the address chat names it by.
func (s *Store) members(roomID string) ([]string, error) { return s.inState(roomID, stateJoined) }

func (s *Store) invited(roomID string) ([]string, error) {
	return s.listMembers(`SELECT m.member FROM members m LEFT JOIN accounts a ON a.id = m.member
		WHERE m.room_id = ? AND m.state != ? ORDER BY COALESCE(a.address, m.member)`, roomID, stateJoined)
}

func (s *Store) inState(roomID, state string) ([]string, error) {
	return s.listMembers(`SELECT m.member FROM members m LEFT JOIN accounts a ON a.id = m.member
		WHERE m.room_id = ? AND m.state = ? ORDER BY COALESCE(a.address, m.member)`, roomID, state)
}

// listMembers is the members a query of one room and one state names.
func (s *Store) listMembers(q, roomID, state string) ([]string, error) {
	rows, err := s.db.Query(q, roomID, state)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
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

// Rooms lists the viewer's rooms, newest activity first.
func (s *Store) Rooms(viewer string) ([]Room, error) {
	rows, err := s.db.Query(`
		SELECT r.id, r.name, r.creator, r.created_at,
		       COALESCE((SELECT MAX(seq) FROM entries e WHERE e.room_id = r.id), 0) > COALESCE((SELECT read FROM cursors c WHERE c.room_id = r.id AND c.member = ?), 0),
		       COALESCE((SELECT MAX(at) FROM entries e WHERE e.room_id = r.id), r.created_at)
		FROM rooms r JOIN members m ON m.room_id = r.id AND m.member = ? AND m.state = ?
		ORDER BY 6 DESC, r.id`, viewer, viewer, stateJoined)
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
		if out[i].Invited, err = s.invited(out[i].ID); err != nil {
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
	if r, err = s.withMembers(roomID); err != nil {
		return Room{}, nil, err
	}
	es, err := s.entries(`room_id = ?`, roomID)
	return r, es, err
}

// After is the oldest standing entries newer than seq, up to one past the
// window, so a caller sees whether more remain, oldest first, and the seqs of the posts replaced by an answer newer than seq, which a page showing
// them removes; a member only.
func (s *Store) After(viewer, roomID string, seq int64) ([]Entry, []int64, error) {
	if _, err := s.requireMember(viewer, roomID); err != nil {
		return nil, nil, err
	}
	es, err := s.query(`SELECT * FROM (SELECT seq, author, body, at, kind FROM entries WHERE replaced_by = 0 AND room_id = ? AND seq > ? ORDER BY seq LIMIT ?) ORDER BY seq DESC`, roomID, seq, window+1)
	if err != nil {
		return nil, nil, err
	}
	rows, err := s.db.Query(`SELECT seq FROM entries WHERE room_id = ? AND replaced_by > ? ORDER BY seq`, roomID, seq)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var gone []int64
	for rows.Next() {
		var q int64
		if err := rows.Scan(&q); err != nil {
			return nil, nil, err
		}
		gone = append(gone, q)
	}
	return es, gone, rows.Err()
}

// entries reads the newest standing entries satisfying where, at most the
// window, and returns them oldest first. A replaced post never stands.
func (s *Store) entries(where string, args ...any) ([]Entry, error) {
	return s.query(`SELECT seq, author, body, at, kind FROM entries WHERE replaced_by = 0 AND `+where+` ORDER BY seq DESC LIMIT ?`, append(args, window)...)
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
		if err := rows.Scan(&e.Seq, &e.Author, &body, &e.At, &e.Kind); err != nil {
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

// Post appends what author says, in one transaction with the rule of one post
// per held delivery: refused with ErrHeld when the author's standing posts
// since its oldest held delivery are as many as the deliveries it holds. It
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
	var held, depth, since int64
	if err := tx.QueryRow(`SELECT COUNT(*), COALESCE(MAX(depth), -1) + 1, COALESCE(MIN(upto), 0)
		FROM chain WHERE room_id = ? AND member = ?`, roomID, author).Scan(&held, &depth, &since); err != nil {
		return Entry{}, 0, err
	}
	if held > 0 {
		var posts int64
		if err := tx.QueryRow(`SELECT COUNT(*) FROM entries
			WHERE room_id = ? AND author = ? AND kind = ? AND replaced_by = 0 AND seq > ?`,
			roomID, author, KindPost, since).Scan(&posts); err != nil {
			return Entry{}, 0, err
		}
		if posts >= held {
			return Entry{}, 0, ErrHeld
		}
	}
	e, err := appendEntry(tx, roomID, author, body, KindPost)
	if err != nil {
		return Entry{}, 0, err
	}
	return e, depth, tx.Commit()
}

func appendEntry(tx *sql.Tx, roomID, author string, body json.RawMessage, kind int) (Entry, error) {
	e := Entry{Author: author, Body: body, At: now(), Kind: kind}
	if err := tx.QueryRow(`SELECT COALESCE(MAX(seq), 0) + 1 FROM entries WHERE room_id = ?`, roomID).Scan(&e.Seq); err != nil {
		return Entry{}, err
	}
	_, err := tx.Exec(`INSERT INTO entries(room_id, seq, author, body, at, kind) VALUES(?,?,?,?,?,?)`, roomID, e.Seq, author, string(body), e.At, kind)
	return e, err
}

// Range is what a delivery to member carries: the standing entries with seq
// after the member's delivered cursor up to upto, by others than the member,
// at most limit of the newest, oldest first.
func (s *Store) Range(member, roomID string, upto int64, limit int) ([]Entry, error) {
	return s.query(`SELECT seq, author, body, at, kind FROM entries
		WHERE room_id = ? AND seq <= ? AND author != ? AND replaced_by = 0
		  AND seq > COALESCE((SELECT delivered FROM cursors WHERE room_id = ? AND member = ?), 0)
		ORDER BY seq DESC LIMIT ?`, roomID, upto, member, roomID, member, limit)
}

// Working is who holds an unanswered delivery in the room, other than viewer:
// the members the room is waiting on, in the order of their addresses.
func (s *Store) Working(roomID, viewer string) ([]string, error) {
	rows, err := s.db.Query(`SELECT DISTINCT c.member, COALESCE(a.address, c.member) FROM chain c LEFT JOIN accounts a ON a.id = c.member
		WHERE c.room_id = ? AND c.member != ? ORDER BY 2`, roomID, viewer)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var m, address string
		if err := rows.Scan(&m, &address); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// NextDelivery hands out the next delivery id; never reused.
func (s *Store) NextDelivery() (int64, error) {
	var n int64
	err := s.db.QueryRow(`UPDATE seqs SET n = n + 1 WHERE name = 'delivery' RETURNING n`).Scan(&n)
	return n, err
}

// Delivered records a delivery the kernel accepted: the member's delivered
// cursor moves to upto, and the member holds the delivery, which covered the
// room up to upto, at depth.
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
	if _, err := tx.Exec(`INSERT OR REPLACE INTO chain(room_id, member, delivery, depth, upto) VALUES(?,?,?,?,?)`, roomID, member, delivery, depth, upto); err != nil {
		return err
	}
	return tx.Commit()
}

// Answer settles a completed delivery in one transaction: the member no
// longer holds it, and what the member posted since the delivery decides what
// the room gains. A nil body appends nothing. A failure (failed) is appended
// only when the member has posted nothing since. An answer is appended when
// the member has posted nothing since, or in place of its posts that address
// nobody, which it marks replaced; when everything posted since addresses a
// member (a hand-off), the answer adds nothing. A delivery the store no
// longer holds settles nothing said before it. Nothing is ever deleted, so
// seqs only grow.
func (s *Store) Answer(member, roomID string, delivery int64, body json.RawMessage, failed bool) error {
	if body != nil && !json.Valid(body) {
		return ErrBadInput
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var since int64
	err = tx.QueryRow(`SELECT upto FROM chain WHERE room_id = ? AND member = ? AND delivery = ?`, roomID, member, delivery).Scan(&since)
	held := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM chain WHERE room_id = ? AND member = ? AND delivery = ?`, roomID, member, delivery); err != nil {
		return err
	}
	if body == nil {
		return tx.Commit()
	}
	members, err := txMembers(tx, roomID)
	if err != nil {
		return err
	}
	if !slices.ContainsFunc(members, func(m client.Party) bool { return ID(m) == member }) {
		return tx.Commit()
	}
	var unaddressed []int64
	posted := false
	if held {
		rows, err := tx.Query(`SELECT seq, body FROM entries
			WHERE room_id = ? AND author = ? AND kind = ? AND replaced_by = 0 AND seq > ?`, roomID, member, KindPost, since)
		if err != nil {
			return err
		}
		for rows.Next() {
			var seq int64
			var b string
			if err := rows.Scan(&seq, &b); err != nil {
				rows.Close()
				return err
			}
			posted = true
			if addressed(json.RawMessage(b), members) == "" {
				unaddressed = append(unaddressed, seq)
			}
		}
		rows.Close()
	}
	if posted && (failed || len(unaddressed) == 0) {
		return tx.Commit()
	}
	kind := KindAnswer
	if failed {
		kind = KindFailed
	}
	e, err := appendEntry(tx, roomID, member, body, kind)
	if err != nil {
		return err
	}
	for _, q := range unaddressed {
		if _, err := tx.Exec(`UPDATE entries SET replaced_by = ? WHERE room_id = ? AND seq = ?`, e.Seq, roomID, q); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// txMembers is the room's joined members, each with its address, which a
// post may address.
func txMembers(tx *sql.Tx, roomID string) ([]client.Party, error) {
	rows, err := tx.Query(`SELECT m.member, COALESCE(a.address, m.member) FROM members m LEFT JOIN accounts a ON a.id = m.member
		WHERE m.room_id = ? AND m.state = ? ORDER BY m.member`, roomID, stateJoined)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []client.Party
	for rows.Next() {
		var p client.Party
		if err := rows.Scan(&p.Principal, &p.Address); err != nil {
			return nil, err
		}
		if !IsPrincipal(p.Principal) {
			p.Principal = ""
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ---- keys ----

// IssueKey issues member a key. Only its hash is kept.
func (s *Store) IssueKey(member string) (string, error) {
	raw := newToken()
	_, err := s.db.Exec(`INSERT INTO keys(hash, member, active, key_id) VALUES(?,?,1,?)`, hashKey(raw), member, newToken())
	return raw, err
}

// Member resolves a raw key to the member it was issued to.
func (s *Store) Member(raw string) (string, bool) {
	var member string
	var active bool
	err := s.db.QueryRow(`SELECT member, active FROM keys WHERE hash = ?`, hashKey(raw)).Scan(&member, &active)
	return member, err == nil && active && member != ""
}

// ---- principals ----

// IsPrincipal reports a principal, user_id@kernel_key: what the kernel names a
// user by on every kernel.
func IsPrincipal(m string) bool {
	id, k, ok := strings.Cut(m, "@")
	return ok && len(k) == client.KeyLen && len(id) == 36 && strings.Count(id, "-") == 4
}

// ID is what the store holds a member by: its principal, or the address a
// member not bound yet is held by.
func ID(p client.Party) string {
	if p.Principal != "" {
		return p.Principal
	}
	return p.Address
}

// execer is a database or a transaction.
type execer interface {
	Exec(string, ...any) (sql.Result, error)
}

// Named records how the kernel names a principal, as it last said.
func (s *Store) Named(p client.Party) error { return nameOf(s.db, p) }

func nameOf(q execer, p client.Party) error {
	_, err := q.Exec(`INSERT INTO accounts(id, address, nickname) VALUES(?,?,?)
		ON CONFLICT(id) DO UPDATE SET address = excluded.address, nickname = excluded.nickname`, p.Principal, p.Address, p.Nickname)
	return err
}

// Parties is each member as a reply names it, in order: a principal as the
// kernel last named it, or by itself while the kernel has not; a member not
// bound yet by its address alone.
func (s *Store) Parties(ids []string) ([]client.Party, error) {
	out := make([]client.Party, len(ids))
	for i, id := range ids {
		out[i] = client.Party{Address: id}
		if !IsPrincipal(id) {
			continue
		}
		out[i].Principal = id
		err := s.db.QueryRow(`SELECT address, nickname FROM accounts WHERE id = ?`, id).Scan(&out[i].Address, &out[i].Nickname)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
	}
	return out, nil
}

// Bind makes a member held by address, the key of which p presented, the
// principal p: every row the address holds moves to the principal, merged with
// what it holds already, and the address is kept as the principal's, so a task
// sealed with the address before still finds its member.
func (s *Store) Bind(address string, p client.Party) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := moveSeats(tx, address, p, ""); err != nil {
		return err
	}
	for _, q := range []string{
		`INSERT INTO cursors(room_id, member, delivered, read) SELECT room_id, ?, delivered, read FROM cursors WHERE member = ? AND true
			ON CONFLICT(room_id, member) DO UPDATE SET delivered = MAX(delivered, excluded.delivered), read = MAX(read, excluded.read)`,
		`DELETE FROM cursors WHERE member = ?2 AND ?1 != ?2`,
		`UPDATE entries SET author = ? WHERE author = ?`,
		`UPDATE chain SET member = ? WHERE member = ?`,
		`UPDATE keys SET member = ? WHERE member = ?`,
		`UPDATE rooms SET creator = ? WHERE creator = ?`,
		`INSERT OR REPLACE INTO legacy(id, address) VALUES(?,?)`,
	} {
		if _, err := tx.Exec(q, p.Principal, address); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Seat makes the seat added in room under an address the principal p, to
// which chat's kernel resolved that address, and reports whether the seat is
// still to be invited: p may have been invited or joined there already.
func (s *Store) Seat(roomID, address string, p client.Party) (bool, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := moveSeats(tx, address, p, roomID); err != nil {
		return false, err
	}
	var state string
	if err := tx.QueryRow(`SELECT state FROM members WHERE room_id = ? AND member = ?`, roomID, p.Principal).Scan(&state); err != nil {
		return false, err
	}
	return state == stateAdded, tx.Commit()
}

// moveSeats moves address's seats, in room or in every room when room is "",
// to the principal p, recorded as the kernel named it. Where p is seated
// already, the seat keeps the further state of the two: joined, then invited,
// then added.
func moveSeats(tx *sql.Tx, address string, p client.Party, room string) error {
	if err := nameOf(tx, p); err != nil {
		return err
	}
	const rank = `CASE %s WHEN 'joined' THEN 2 WHEN 'invited' THEN 1 ELSE 0 END`
	if _, err := tx.Exec(`INSERT INTO members(room_id, member, state) SELECT room_id, ?1, state FROM members WHERE member = ?2 AND (?3 = '' OR room_id = ?3)
		ON CONFLICT(room_id, member) DO UPDATE SET state = CASE WHEN `+fmt.Sprintf(rank, "excluded.state")+` > `+fmt.Sprintf(rank, "members.state")+` THEN excluded.state ELSE members.state END`,
		p.Principal, address, room); err != nil {
		return err
	}
	_, err := tx.Exec(`DELETE FROM members WHERE member = ?2 AND (?3 = '' OR room_id = ?3) AND ?1 != ?2`, p.Principal, address, room)
	return err
}

// Legacy is the principal a member held by address was bound to.
func (s *Store) Legacy(address string) (string, bool) {
	var id string
	err := s.db.QueryRow(`SELECT id FROM legacy WHERE address = ?`, address).Scan(&id)
	return id, err == nil
}

// ---- helpers ----

func now() string { return time.Now().UTC().Format(time.RFC3339Nano) }

// newToken is 24 random bytes in hex: keys and key ids.
func newToken() string {
	var b [24]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func hashKey(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// Room ids are words, which a language model copies exactly: an agent posts
// by copying the room id, and long hex ids came back with one character
// changed or dropped in a third of the replies measured.
var (
	idAdjectives = []string{"amber", "brisk", "cedar", "dusky", "ember", "frost", "gold", "hazel", "ivory", "jade",
		"lunar", "maple", "noble", "olive", "pearl", "quiet", "rusty", "solar", "tidal", "urban"}
	idAnimals = []string{"otter", "falcon", "badger", "heron", "lynx", "marten", "osprey", "panda", "raven", "salmon",
		"tiger", "walrus", "bison", "crane", "dingo", "eagle", "gecko", "ibis", "koala", "llama"}
)

// wordID is a room id, adjective-animal-NN: 36,000 of them.
func wordID() string {
	var b [3]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("%s-%s-%d", idAdjectives[int(b[0])%len(idAdjectives)], idAnimals[int(b[1])%len(idAnimals)], 10+int(b[2])%90)
}
