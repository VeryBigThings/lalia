package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

type writeOp struct {
	queueID   int64 // SQLite row ID; 0 means not persisted to queue
	attempts  int   // local copy of queue.attempts for dead-letter threshold check
	relPath   string
	content   []byte
	commitMsg string
}

func ensureWorkspace() error {
	ws := workspacePath()
	if err := os.MkdirAll(ws, 0700); err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(ws, ".git", "HEAD")); os.IsNotExist(err) {
		// clean partial state if any (e.g., from a crashed prior init)
		_ = os.RemoveAll(filepath.Join(ws, ".git"))
		cmd := exec.Command("git", "-C", ws, "init", "-q", "-b", "main")
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("git init: %s: %w", string(out), err)
		}
		// set a default identity if user has none at repo level; harmless if already set globally
		_ = exec.Command("git", "-C", ws, "config", "user.email", "daemon@kopos.local").Run()
		_ = exec.Command("git", "-C", ws, "config", "user.name", "kopos").Run()

		readme := []byte("# kopos workspace\n\nAgent coordination log. Managed by the kopos daemon.\n")
		if err := os.WriteFile(filepath.Join(ws, "README.md"), readme, 0600); err != nil {
			return err
		}
		_ = exec.Command("git", "-C", ws, "add", "README.md").Run()
		_ = exec.Command("git", "-C", ws, "commit", "-q", "-m", "init workspace").Run()
	}
	return nil
}

// enqueueWrite inserts the op into the SQLite queue synchronously (so it is
// durable before we return to the caller), then forwards it to the writer
// goroutine for async git commit.
//
// If the SQLite insert fails the write is still forwarded to the in-memory
// channel as a best-effort fallback: the write will be attempted but will NOT
// survive a daemon crash. This is an explicit graceful-degradation path — the
// caller's request is not failed. If stronger guarantees are required, callers
// should treat a non-zero queueID in the op as the durability signal.
func (s *State) enqueueWrite(relPath string, content []byte, commitMsg string) {
	op := writeOp{relPath: relPath, content: content, commitMsg: commitMsg}
	if s.queue != nil {
		id, err := s.queue.insert(relPath, content, commitMsg)
		if err != nil {
			fmt.Fprintln(os.Stderr, "queue insert:", err)
			// fall through: still attempt the write; just won't survive a crash
		} else {
			op.queueID = id
		}
	}
	s.writes <- op
}

// flushPendingWrites synchronously commits any write-queue entries that were
// durably inserted before the last shutdown but never committed to git.
// Called during newState() before loadRooms so transcript files are on disk.
func (s *State) flushPendingWrites() {
	if s.queue == nil {
		return
	}
	rows, err := s.queue.pending()
	if err != nil {
		fmt.Fprintln(os.Stderr, "queue flush:", err)
		return
	}
	if len(rows) == 0 {
		return
	}
	ws := workspacePath()
	fmt.Fprintf(os.Stderr, "flushing %d pending queue entries before boot\n", len(rows))
	for _, r := range rows {
		s.commitWrite(ws, writeOp{
			queueID:   r.id,
			attempts:  r.attempts,
			relPath:   r.relPath,
			content:   r.content,
			commitMsg: r.commitMsg,
		})
	}
}

func (s *State) runWriter() {
	s.wg.Add(1)
	defer func() {
		if s.queue != nil {
			if err := s.queue.close(); err != nil {
				fmt.Fprintln(os.Stderr, "queue close:", err)
			}
		}
		s.wg.Done()
	}()

	for op := range s.writes {
		s.commitWrite(workspacePath(), op)
	}
}

// commitWrite writes the file, commits it to git, then removes the queue row.
// On failure the queue row is left in place so the next startup can replay it.
// After maxAttempts failures the row is moved to queue_dead to prevent
// infinite replay loops caused by persistent errors (corrupt git repo, FS full,
// bad relPath, etc.).
func (s *State) commitWrite(ws string, op writeOp) {
	fail := func(label string, detail string) {
		fmt.Fprintf(os.Stderr, "%s: %s\n", label, detail)
		if s.queue == nil || op.queueID <= 0 {
			return
		}
		if err := s.queue.bumpAttempts(op.queueID); err != nil {
			fmt.Fprintln(os.Stderr, "queue bump attempts:", err)
			return
		}
		op.attempts++ // keep local copy in sync
		if op.attempts >= maxAttempts {
			fmt.Fprintf(os.Stderr, "queue: row %d failed %d times, moving to dead-letter\n", op.queueID, op.attempts)
			if err := s.queue.deadLetter(queueRow{
				id: op.queueID, relPath: op.relPath,
				content: op.content, commitMsg: op.commitMsg,
				attempts: op.attempts,
			}); err != nil {
				fmt.Fprintln(os.Stderr, "queue dead-letter:", err)
			}
		}
	}

	full := filepath.Join(ws, op.relPath)
	if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
		fail("mkdir", err.Error())
		return
	}
	if err := os.WriteFile(full, op.content, 0600); err != nil {
		fail("write", err.Error())
		return
	}
	if out, err := exec.Command("git", "-C", ws, "add", op.relPath).CombinedOutput(); err != nil {
		fail("git add", string(out)+": "+err.Error())
		return
	}
	if out, err := exec.Command("git", "-C", ws, "commit", "-q", "-m", op.commitMsg).CombinedOutput(); err != nil {
		fail("git commit", string(out)+": "+err.Error())
		return
	}
	// Delete from queue only after a successful git commit.
	if s.queue != nil && op.queueID > 0 {
		if err := s.queue.delete(op.queueID); err != nil {
			fmt.Fprintln(os.Stderr, "queue delete:", err)
		}
	}
}

