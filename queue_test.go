package main

import (
	"os"
	"path/filepath"
	"testing"
)

// newTestQueue opens a fresh queue in a temp directory.
func newTestQueue(t *testing.T) (*Queue, string) {
	t.Helper()
	dir := t.TempDir()
	q, err := openQueue(dir)
	if err != nil {
		t.Fatalf("openQueue: %v", err)
	}
	t.Cleanup(func() { q.close() })
	return q, dir
}

// TestQueueInsertPersistsBeforeCommit verifies that insert is synchronous:
// the row exists in SQLite the instant insert returns, before any git commit
// has happened (writer goroutine not running in this test).
func TestQueueInsertPersistsBeforeCommit(t *testing.T) {
	q, _ := newTestQueue(t)

	id, err := q.insert("registry/alice.json", []byte(`{"name":"alice"}`), "register alice")
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	if id <= 0 {
		t.Fatalf("expected positive row ID, got %d", id)
	}

	rows, err := q.pending()
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 pending row, got %d", len(rows))
	}
	r := rows[0]
	if r.id != id {
		t.Errorf("row ID mismatch: got %d want %d", r.id, id)
	}
	if r.relPath != "registry/alice.json" {
		t.Errorf("relPath mismatch: %q", r.relPath)
	}
	if string(r.content) != `{"name":"alice"}` {
		t.Errorf("content mismatch: %q", r.content)
	}
	if r.commitMsg != "register alice" {
		t.Errorf("commitMsg mismatch: %q", r.commitMsg)
	}
}

// TestQueueClearsAfterDelete verifies that delete removes the row so a
// subsequent pending() call returns nothing (simulating a successful git commit).
func TestQueueClearsAfterDelete(t *testing.T) {
	q, _ := newTestQueue(t)

	id, err := q.insert("tunnels/abc/SESSION.md", []byte("# session"), "tunnel open")
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	if err := q.delete(id); err != nil {
		t.Fatalf("delete: %v", err)
	}

	rows, err := q.pending()
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("expected 0 pending rows after delete, got %d", len(rows))
	}
}

// TestQueueCrashReplay simulates a crash: a row is inserted but not deleted
// (git commit never happened). On the next startup the queue is re-opened from
// the same file and pending() must still return that row.
func TestQueueCrashReplay(t *testing.T) {
	dir := t.TempDir()

	// First "run": insert but do not delete (simulate crash before git commit).
	var savedID int64
	func() {
		q, err := openQueue(dir)
		if err != nil {
			t.Fatalf("openQueue first run: %v", err)
		}
		defer q.close()
		id, err := q.insert("registry/bob.json", []byte(`{"name":"bob"}`), "register bob")
		if err != nil {
			t.Fatalf("insert: %v", err)
		}
		savedID = id
	}()

	// Second "run": re-open queue from same dir, row must still be there.
	q2, err := openQueue(dir)
	if err != nil {
		t.Fatalf("openQueue second run: %v", err)
	}
	defer q2.close()

	rows, err := q2.pending()
	if err != nil {
		t.Fatalf("pending after reopen: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 surviving row, got %d", len(rows))
	}
	if rows[0].id != savedID {
		t.Errorf("row ID mismatch: got %d want %d", rows[0].id, savedID)
	}
	if string(rows[0].content) != `{"name":"bob"}` {
		t.Errorf("content mismatch: %q", rows[0].content)
	}

	// Simulate successful replay: delete and confirm empty.
	if err := q2.delete(savedID); err != nil {
		t.Fatalf("delete after replay: %v", err)
	}
	rows2, err := q2.pending()
	if err != nil {
		t.Fatalf("pending after replay+delete: %v", err)
	}
	if len(rows2) != 0 {
		t.Fatalf("expected 0 rows after replay+delete, got %d", len(rows2))
	}
}

// TestQueueSchemaMigration verifies that openQueue is idempotent: calling it
// twice on the same path (CREATE TABLE IF NOT EXISTS) works without error and
// preserves existing data.
func TestQueueSchemaMigration(t *testing.T) {
	dir := t.TempDir()

	q1, err := openQueue(dir)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	id, err := q1.insert("a/b.json", []byte("{}"), "msg")
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	q1.close()

	// Re-open: schema creation must be idempotent and data preserved.
	q2, err := openQueue(dir)
	if err != nil {
		t.Fatalf("second open: %v", err)
	}
	defer q2.close()

	rows, err := q2.pending()
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	if len(rows) != 1 || rows[0].id != id {
		t.Fatalf("expected row %d to survive reopen, got %v", id, rows)
	}
}

// TestQueueInsertOrderPreserved verifies that pending() returns rows in
// insertion order (FIFO), which is the correct replay order.
func TestQueueInsertOrderPreserved(t *testing.T) {
	q, _ := newTestQueue(t)

	msgs := []string{"first", "second", "third"}
	for _, m := range msgs {
		if _, err := q.insert("f/"+m, []byte(m), m); err != nil {
			t.Fatalf("insert %q: %v", m, err)
		}
	}

	rows, err := q.pending()
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	if len(rows) != len(msgs) {
		t.Fatalf("expected %d rows, got %d", len(msgs), len(rows))
	}
	for i, r := range rows {
		if r.commitMsg != msgs[i] {
			t.Errorf("row %d: expected commitMsg %q, got %q", i, msgs[i], r.commitMsg)
		}
	}
}

// TestQueueDBFileCreated verifies that openQueue creates the queue.db file
// at the expected path inside the given directory.
func TestQueueDBFileCreated(t *testing.T) {
	dir := t.TempDir()
	q, err := openQueue(dir)
	if err != nil {
		t.Fatalf("openQueue: %v", err)
	}
	defer q.close()

	dbPath := filepath.Join(dir, "queue.db")
	if _, err := os.Stat(dbPath); err != nil {
		t.Errorf("expected queue.db at %s: %v", dbPath, err)
	}
}
