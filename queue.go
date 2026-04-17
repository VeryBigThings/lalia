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
}

// openQueue opens (or creates) the SQLite queue at <dir>/queue.db in WAL mode.
func openQueue(dir string) (*Queue, error) {
	path := filepath.Join(dir, "queue.db")
	db, err := sql.Open("sqlite", path+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("open queue db: %w", err)
	}
	db.SetMaxOpenConns(1) // serialise all access; avoids SQLITE_BUSY under WAL
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS queue (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		rel_path   TEXT    NOT NULL,
		content    BLOB    NOT NULL,
		commit_msg TEXT    NOT NULL
	)`); err != nil {
		db.Close()
		return nil, fmt.Errorf("create queue table: %w", err)
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
	rows, err := q.db.Query(`SELECT id, rel_path, content, commit_msg FROM queue ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []queueRow
	for rows.Next() {
		var r queueRow
		if err := rows.Scan(&r.id, &r.relPath, &r.content, &r.commitMsg); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
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
