package main

import (
	"database/sql"
	"fmt"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// Queue is an SQLite-backed write queue that persists writeOp entries between
// client ack and git commit. A daemon crash between those two points is safe:
// on next startup, replayQueue drains any surviving rows through the writer.
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
