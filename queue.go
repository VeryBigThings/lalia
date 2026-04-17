package main

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// Queue is an SQLite-backed write queue that persists writeOp entries between
// client ack and git commit. A daemon crash between those two points is safe:
// on next startup, replayQueue drains any surviving rows through the writer.
// It also holds the mailbox and mailbox_dropped tables for unread-state persistence.
type Queue struct {
	db *sql.DB
}

// queueRow is a single pending entry loaded from the queue on startup replay.
type queueRow struct {
	id        int64
	relPath   string
	content   []byte
	commitMsg string
	attempts  int
}

// mailboxRow is one unread mailbox entry loaded on daemon startup for replay.
type mailboxRow struct {
	recipient string
	kind      string // "peer" or "room"
	target    string // sender name (kind=peer) or room name (kind=room)
	seq       int
	fromName  string
	ts        string // RFC3339
	body      string
}

// mailboxDroppedRow is a persisted dropped-counter entry for a room mailbox.
type mailboxDroppedRow struct {
	recipient string
	kind      string
	target    string
	dropped   int
}

// openQueue opens (or creates) the SQLite queue at <dir>/queue.db in WAL mode.
func openQueue(dir string) (*Queue, error) {
	path := filepath.Join(dir, "queue.db")
	// modernc.org/sqlite uses _pragma=name(value) DSN form; the mattn-style
	// _journal_mode= and _busy_timeout= keys are silently ignored by this driver.
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("open queue db: %w", err)
	}
	db.SetMaxOpenConns(1) // serialise all access; with WAL this also avoids SQLITE_BUSY
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS queue (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		rel_path   TEXT    NOT NULL,
		content    BLOB    NOT NULL,
		commit_msg TEXT    NOT NULL,
		attempts   INTEGER NOT NULL DEFAULT 0
	)`); err != nil {
		db.Close()
		return nil, fmt.Errorf("create queue table: %w", err)
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS queue_dead (
		id         INTEGER PRIMARY KEY,
		rel_path   TEXT    NOT NULL,
		content    BLOB    NOT NULL,
		commit_msg TEXT    NOT NULL,
		attempts   INTEGER NOT NULL,
		failed_at  TEXT    NOT NULL
	)`); err != nil {
		db.Close()
		return nil, fmt.Errorf("create queue_dead table: %w", err)
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS mailbox_rooms (
		name       TEXT    PRIMARY KEY,
		desc       TEXT    NOT NULL DEFAULT '',
		created_by TEXT    NOT NULL,
		created_at TEXT    NOT NULL
	)`); err != nil {
		db.Close()
		return nil, fmt.Errorf("create mailbox_rooms table: %w", err)
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS mailbox_room_members (
		room   TEXT NOT NULL,
		member TEXT NOT NULL,
		PRIMARY KEY (room, member)
	)`); err != nil {
		db.Close()
		return nil, fmt.Errorf("create mailbox_room_members table: %w", err)
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS mailbox (
		id        INTEGER PRIMARY KEY AUTOINCREMENT,
		recipient TEXT    NOT NULL,
		kind      TEXT    NOT NULL,
		target    TEXT    NOT NULL,
		seq       INTEGER NOT NULL,
		from_name TEXT    NOT NULL,
		ts        TEXT    NOT NULL,
		body      TEXT    NOT NULL
	)`); err != nil {
		db.Close()
		return nil, fmt.Errorf("create mailbox table: %w", err)
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS mailbox_dropped (
		recipient TEXT    NOT NULL,
		kind      TEXT    NOT NULL,
		target    TEXT    NOT NULL,
		dropped   INTEGER NOT NULL DEFAULT 0,
		PRIMARY KEY (recipient, kind, target)
	)`); err != nil {
		db.Close()
		return nil, fmt.Errorf("create mailbox_dropped table: %w", err)
	}
	return &Queue{db: db}, nil
}

// insert persists a writeOp to the queue and returns the assigned row ID.
// The row ID is carried through the writeOp so the writer can delete it after
// a successful git commit.
func (q *Queue) insert(relPath string, content []byte, commitMsg string) (int64, error) {
	res, err := q.db.Exec(
		`INSERT INTO queue (rel_path, content, commit_msg) VALUES (?, ?, ?)`,
		relPath, content, commitMsg,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// pending returns all rows from the queue ordered by insertion order.
// Used on startup to replay entries that survived a crash.
func (q *Queue) pending() ([]queueRow, error) {
	rows, err := q.db.Query(`SELECT id, rel_path, content, commit_msg, attempts FROM queue ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []queueRow
	for rows.Next() {
		var r queueRow
		if err := rows.Scan(&r.id, &r.relPath, &r.content, &r.commitMsg, &r.attempts); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// bumpAttempts increments the attempt counter for the given row.
func (q *Queue) bumpAttempts(id int64) error {
	_, err := q.db.Exec(`UPDATE queue SET attempts = attempts + 1 WHERE id = ?`, id)
	return err
}

// maxAttempts is the number of commit failures after which a queue row is
// moved to queue_dead so it stops blocking future replays.
const maxAttempts = 3

// deadLetter moves a row from queue to queue_dead. Called when attempts
// exceed maxAttempts. Returns the error from the move operation, not from
// the original failure — callers should log both.
func (q *Queue) deadLetter(r queueRow) error {
	tx, err := q.db.Begin()
	if err != nil {
		return err
	}
	_, err = tx.Exec(
		`INSERT OR IGNORE INTO queue_dead (id, rel_path, content, commit_msg, attempts, failed_at)
		 VALUES (?, ?, ?, ?, ?, datetime('now'))`,
		r.id, r.relPath, r.content, r.commitMsg, r.attempts,
	)
	if err != nil {
		tx.Rollback()
		return err
	}
	if _, err := tx.Exec(`DELETE FROM queue WHERE id = ?`, r.id); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

// delete removes the row with the given ID after a successful git commit.
func (q *Queue) delete(id int64) error {
	_, err := q.db.Exec(`DELETE FROM queue WHERE id = ?`, id)
	return err
}

// close shuts down the database connection.
func (q *Queue) close() error {
	return q.db.Close()
}

// mailboxAppend inserts one unread message into the mailbox table.
func (q *Queue) mailboxAppend(recipient, kind, target string, seq int, fromName string, ts time.Time, body string) error {
	_, err := q.db.Exec(
		`INSERT INTO mailbox (recipient, kind, target, seq, from_name, ts, body) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		recipient, kind, target, seq, fromName, ts.Format(time.RFC3339), body,
	)
	return err
}

// mailboxDeleteOne removes a single row identified by (recipient, kind, target, seq).
func (q *Queue) mailboxDeleteOne(recipient, kind, target string, seq int) error {
	_, err := q.db.Exec(
		`DELETE FROM mailbox WHERE recipient=? AND kind=? AND target=? AND seq=?`,
		recipient, kind, target, seq,
	)
	return err
}

// mailboxConsumeAll deletes all mailbox rows and the dropped counter for the
// given (recipient, kind, target) — used when roomRead drains the full inbox.
func (q *Queue) mailboxConsumeAll(recipient, kind, target string) error {
	tx, err := q.db.Begin()
	if err != nil {
		return err
	}
	if _, err := tx.Exec(
		`DELETE FROM mailbox WHERE recipient=? AND kind=? AND target=?`,
		recipient, kind, target,
	); err != nil {
		tx.Rollback()
		return err
	}
	if _, err := tx.Exec(
		`DELETE FROM mailbox_dropped WHERE recipient=? AND kind=? AND target=?`,
		recipient, kind, target,
	); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

// mailboxDropOldest deletes the row identified by seq (the message being
// dropped due to overflow) and atomically increments the dropped counter.
func (q *Queue) mailboxDropOldest(recipient, kind, target string, seq int) error {
	tx, err := q.db.Begin()
	if err != nil {
		return err
	}
	if _, err := tx.Exec(
		`DELETE FROM mailbox WHERE recipient=? AND kind=? AND target=? AND seq=?`,
		recipient, kind, target, seq,
	); err != nil {
		tx.Rollback()
		return err
	}
	if _, err := tx.Exec(
		`INSERT INTO mailbox_dropped (recipient, kind, target, dropped) VALUES (?, ?, ?, 1)
		 ON CONFLICT(recipient, kind, target) DO UPDATE SET dropped = dropped + 1`,
		recipient, kind, target,
	); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

// mailboxRows returns all unread mailbox entries ordered by insertion (FIFO).
func (q *Queue) mailboxRows() ([]mailboxRow, error) {
	rows, err := q.db.Query(
		`SELECT recipient, kind, target, seq, from_name, ts, body FROM mailbox ORDER BY id`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []mailboxRow
	for rows.Next() {
		var r mailboxRow
		if err := rows.Scan(&r.recipient, &r.kind, &r.target, &r.seq, &r.fromName, &r.ts, &r.body); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// mailboxDropped returns all persisted dropped-counter rows.
func (q *Queue) mailboxDropped() ([]mailboxDroppedRow, error) {
	rows, err := q.db.Query(
		`SELECT recipient, kind, target, dropped FROM mailbox_dropped`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []mailboxDroppedRow
	for rows.Next() {
		var r mailboxDroppedRow
		if err := rows.Scan(&r.recipient, &r.kind, &r.target, &r.dropped); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

type roomRecord struct {
	name      string
	desc      string
	createdBy string
	createdAt string
}

// roomUpsert inserts or updates a room definition.
func (q *Queue) roomUpsert(name, desc, createdBy string, createdAt time.Time) error {
	_, err := q.db.Exec(
		`INSERT INTO mailbox_rooms (name, desc, created_by, created_at) VALUES (?, ?, ?, ?)
		 ON CONFLICT(name) DO UPDATE SET desc=excluded.desc, created_by=excluded.created_by`,
		name, desc, createdBy, createdAt.Format(time.RFC3339),
	)
	return err
}

// roomAddMember persists a room membership.
func (q *Queue) roomAddMember(room, member string) error {
	_, err := q.db.Exec(
		`INSERT OR IGNORE INTO mailbox_room_members (room, member) VALUES (?, ?)`,
		room, member,
	)
	return err
}

// roomRemoveMember removes a room membership.
func (q *Queue) roomRemoveMember(room, member string) error {
	_, err := q.db.Exec(
		`DELETE FROM mailbox_room_members WHERE room=? AND member=?`,
		room, member,
	)
	return err
}

// roomDeleteAll removes a room and all its members and mailbox entries.
func (q *Queue) roomDeleteAll(room string) error {
	tx, err := q.db.Begin()
	if err != nil {
		return err
	}
	for _, table := range []string{"mailbox_rooms", "mailbox_room_members"} {
		col := "name"
		if table != "mailbox_rooms" {
			col = "room"
		}
		if _, err := tx.Exec("DELETE FROM "+table+" WHERE "+col+"=?", room); err != nil {
			tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

// roomRows returns all persisted room definitions.
func (q *Queue) roomRows() ([]roomRecord, error) {
	rows, err := q.db.Query(`SELECT name, desc, created_by, created_at FROM mailbox_rooms`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []roomRecord
	for rows.Next() {
		var r roomRecord
		if err := rows.Scan(&r.name, &r.desc, &r.createdBy, &r.createdAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// roomMemberRows returns all (room, member) pairs.
func (q *Queue) roomMemberRows() (map[string][]string, error) {
	rows, err := q.db.Query(`SELECT room, member FROM mailbox_room_members`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string][]string)
	for rows.Next() {
		var room, member string
		if err := rows.Scan(&room, &member); err != nil {
			return nil, err
		}
		out[room] = append(out[room], member)
	}
	return out, rows.Err()
}
