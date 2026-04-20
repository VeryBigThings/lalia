package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
)

// mustInitRepoForIntegration creates a git repo with one commit for tests
// that drive task_publish through the real daemon.
func mustInitRepoForIntegration(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "--initial-branch=main", ".")
	if err := os.WriteFile(filepath.Join(dir, "README"), []byte("seed\n"), 0644); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	run("add", "README")
	run("commit", "-m", "init")
	return dir
}

// TestRoomsGCArchivePersistsAcrossRestart: `rooms gc` archives a merged-room;
// after daemon restart the room is still archived and new posts are rejected.
// Guards the invariant that archive state lives in SQLite, not derived from
// task status at boot.
func TestRoomsGCArchivePersistsAcrossRestart(t *testing.T) {
	laliaHome := setupIntegrationEnv(t)
	defer stopDaemonForHome(t, laliaHome)

	repoRoot := mustInitRepoForIntegration(t)

	mustRequest(t, "register", map[string]any{
		"name": "sup", "pid": float64(9001), "role": "supervisor",
		"project": "gc-test", "repo_root": repoRoot,
	})
	mustRequest(t, "register", map[string]any{"name": "alice", "pid": float64(9002), "role": "worker"})

	pub := mustRequest(t, "task_publish", map[string]any{
		"from": "sup", "project": "gc-test", "repo_root": repoRoot,
		"workstreams": []any{map[string]any{"slug": "feat-done", "branch": "feat/done", "brief": "done"}},
	})
	if !pub.OK {
		t.Fatalf("task_publish: %+v", pub)
	}
	if r := mustRequest(t, "task_status", map[string]any{
		"from": "sup", "project": "gc-test", "slug": "feat-done", "status": "merged",
	}); !r.OK {
		t.Fatalf("task_status merged: %+v", r)
	}

	postLive := mustRequest(t, "post", map[string]any{"from": "sup", "room": "feat-done", "body": "post-merge note"})
	if !postLive.OK {
		t.Fatalf("post to live merged room should succeed, got %+v", postLive)
	}

	gc := mustRequest(t, "rooms_gc", map[string]any{"from": "sup"})
	if !gc.OK {
		t.Fatalf("rooms_gc: %+v", gc)
	}

	restartDaemon(t, laliaHome)

	mustRequest(t, "register", map[string]any{
		"name": "sup", "pid": float64(9003), "role": "supervisor",
		"project": "gc-test", "repo_root": repoRoot,
	})
	mustRequest(t, "register", map[string]any{"name": "alice", "pid": float64(9004), "role": "worker"})

	postAfter := mustRequest(t, "post", map[string]any{"from": "sup", "room": "feat-done", "body": "should fail"})
	if postAfter.OK {
		t.Fatalf("post to archived room after restart should fail, got %+v", postAfter)
	}
}

// restartDaemon stops the current daemon and triggers a fresh one on the
// next request() call. Returns once the old socket is gone.
func restartDaemon(t *testing.T, laliaHome string) {
	t.Helper()
	stopDaemonForHome(t, laliaHome)
}

// TestMailboxPeerSurvivesRestart: tell → restart → read returns the message.
func TestMailboxPeerSurvivesRestart(t *testing.T) {
	laliaHome := setupIntegrationEnv(t)
	defer stopDaemonForHome(t, laliaHome)

	mustRequest(t, "register", map[string]any{"name": "alice", "pid": float64(1001)})
	mustRequest(t, "register", map[string]any{"name": "bob", "pid": float64(1002)})

	tell := mustRequest(t, "tell", map[string]any{"from": "alice", "peer": "bob", "body": "hello after restart"})
	if !tell.OK {
		t.Fatalf("tell failed: %+v", tell)
	}

	restartDaemon(t, laliaHome)

	// Re-register so agents are known to the new daemon (also renews lease).
	mustRequest(t, "register", map[string]any{"name": "alice", "pid": float64(1003)})
	mustRequest(t, "register", map[string]any{"name": "bob", "pid": float64(1004)})

	read := mustRequest(t, "read", map[string]any{"from": "bob", "peer": "alice", "timeout": float64(0)})
	if !read.OK {
		t.Fatalf("read after restart failed: %+v", read)
	}
	body, _ := read.Data.(map[string]any)["body"].(string)
	if body != "hello after restart" {
		t.Fatalf("expected 'hello after restart', got %q", body)
	}
}

// TestMailboxRoomSurvivesRestart: post → restart → member read returns the message.
func TestMailboxRoomSurvivesRestart(t *testing.T) {
	laliaHome := setupIntegrationEnv(t)
	defer stopDaemonForHome(t, laliaHome)

	mustRequest(t, "register", map[string]any{"name": "alice", "pid": float64(2001)})
	mustRequest(t, "register", map[string]any{"name": "bob", "pid": float64(2002)})
	mustRequest(t, "room_create", map[string]any{"from": "alice", "name": "general", "desc": "test room"})
	mustRequest(t, "join", map[string]any{"from": "bob", "room": "general"})
	mustRequest(t, "post", map[string]any{"from": "alice", "room": "general", "body": "room msg after restart"})

	restartDaemon(t, laliaHome)

	mustRequest(t, "register", map[string]any{"name": "alice", "pid": float64(2003)})
	mustRequest(t, "register", map[string]any{"name": "bob", "pid": float64(2004)})

	read := mustRequest(t, "read", map[string]any{"from": "bob", "room": "general", "timeout": float64(0)})
	if !read.OK {
		t.Fatalf("room read after restart failed: %+v", read)
	}
	msgs, _ := read.Data.(map[string]any)["messages"].([]any)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	body, _ := msgs[0].(map[string]any)["body"].(string)
	if body != "room msg after restart" {
		t.Fatalf("expected 'room msg after restart', got %q", body)
	}
}

// TestMailboxNoDoubleDelivery: tell → read → restart → next read is empty.
func TestMailboxNoDoubleDelivery(t *testing.T) {
	laliaHome := setupIntegrationEnv(t)
	defer stopDaemonForHome(t, laliaHome)

	mustRequest(t, "register", map[string]any{"name": "alice", "pid": float64(3001)})
	mustRequest(t, "register", map[string]any{"name": "bob", "pid": float64(3002)})
	mustRequest(t, "tell", map[string]any{"from": "alice", "peer": "bob", "body": "read before restart"})

	// Bob reads and consumes the message.
	read := mustRequest(t, "read", map[string]any{"from": "bob", "peer": "alice", "timeout": float64(1)})
	if !read.OK {
		t.Fatalf("first read failed: %+v", read)
	}
	body, _ := read.Data.(map[string]any)["body"].(string)
	if body != "read before restart" {
		t.Fatalf("expected 'read before restart', got %q", body)
	}

	restartDaemon(t, laliaHome)

	mustRequest(t, "register", map[string]any{"name": "alice", "pid": float64(3003)})
	mustRequest(t, "register", map[string]any{"name": "bob", "pid": float64(3004)})

	// Second read must return empty — no double delivery.
	empty := mustRequest(t, "read", map[string]any{"from": "bob", "peer": "alice", "timeout": float64(0)})
	if !empty.OK {
		t.Fatalf("second read failed: %+v", empty)
	}
	if m, ok := empty.Data.(map[string]any); ok {
		if _, has := m["body"]; has {
			t.Fatalf("second read should return empty, got body: %+v", m)
		}
	}
}

// TestMailboxRoomOverflowSurvivesRestart: drop-oldest counter is preserved
// across a daemon restart.
func TestMailboxRoomOverflowSurvivesRestart(t *testing.T) {
	laliaHome := setupIntegrationEnv(t)
	defer stopDaemonForHome(t, laliaHome)

	mustRequest(t, "register", map[string]any{"name": "alice", "pid": float64(4001)})
	mustRequest(t, "register", map[string]any{"name": "bob", "pid": float64(4002)})
	mustRequest(t, "room_create", map[string]any{"from": "alice", "name": "busy", "desc": "overflow test"})
	mustRequest(t, "join", map[string]any{"from": "bob", "room": "busy"})

	// Post roomMailboxLimit+2 messages to force 2 drops.
	total := roomMailboxLimit + 2
	for i := 0; i < total; i++ {
		mustRequest(t, "post", map[string]any{"from": "alice", "room": "busy", "body": "msg"})
	}

	// Verify dropped counter before restart.
	peek := mustRequest(t, "peek", map[string]any{"from": "bob", "room": "busy"})
	if !peek.OK {
		t.Fatalf("peek failed: %+v", peek)
	}
	droppedBefore, _ := peek.Data.(map[string]any)["dropped"].(float64)
	if int(droppedBefore) != 2 {
		t.Fatalf("expected 2 dropped before restart, got %v", droppedBefore)
	}

	restartDaemon(t, laliaHome)

	mustRequest(t, "register", map[string]any{"name": "alice", "pid": float64(4003)})
	mustRequest(t, "register", map[string]any{"name": "bob", "pid": float64(4004)})

	// Read and check the notice says 2 dropped.
	read := mustRequest(t, "read", map[string]any{"from": "bob", "room": "busy", "timeout": float64(0)})
	if !read.OK {
		t.Fatalf("room read after restart failed: %+v", read)
	}
	msgs, _ := read.Data.(map[string]any)["messages"].([]any)
	if len(msgs) == 0 {
		t.Fatalf("expected messages after restart, got none")
	}
	// First entry should be the dropped notice.
	first := msgs[0].(map[string]any)
	if typ, _ := first["type"].(string); typ != "notice" {
		t.Fatalf("expected notice as first message, got type=%q", typ)
	}
	if d, _ := first["dropped"].(float64); int(d) != 2 {
		t.Fatalf("expected dropped=2 in notice, got %v", d)
	}
	// Remaining entries should be the non-dropped messages.
	if len(msgs)-1 != roomMailboxLimit {
		t.Fatalf("expected %d non-notice messages, got %d", roomMailboxLimit, len(msgs)-1)
	}
}

// TestMailboxConcurrentDeliveryNoLoss: concurrent tells during a simulated
// restart window — no message is lost, no duplicate delivered.
func TestMailboxConcurrentDeliveryNoLoss(t *testing.T) {
	laliaHome := setupIntegrationEnv(t)
	defer stopDaemonForHome(t, laliaHome)

	mustRequest(t, "register", map[string]any{"name": "alice", "pid": float64(5001)})
	mustRequest(t, "register", map[string]any{"name": "bob", "pid": float64(5002)})

	const n = 10
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_ = func() *Response {
				r, err := request("tell", map[string]any{"from": "alice", "peer": "bob", "body": "concurrent"})
				if err != nil || !r.OK {
					return nil
				}
				return r
			}()
		}(i)
	}
	wg.Wait()

	restartDaemon(t, laliaHome)

	mustRequest(t, "register", map[string]any{"name": "alice", "pid": float64(5003)})
	mustRequest(t, "register", map[string]any{"name": "bob", "pid": float64(5004)})

	// Drain all messages; count them.
	received := 0
	for {
		r := mustRequest(t, "read", map[string]any{"from": "bob", "peer": "alice", "timeout": float64(0)})
		if !r.OK {
			break
		}
		if _, ok := r.Data.(map[string]any)["body"]; !ok {
			break
		}
		received++
	}
	if received != n {
		t.Fatalf("expected %d messages after restart, got %d", n, received)
	}
}
