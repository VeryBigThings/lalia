package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// mustOpenTestQueue opens a Queue in a temp dir and registers cleanup.
func mustOpenTestQueue(t *testing.T) (*Queue, string) {
	t.Helper()
	dir := t.TempDir()
	q, err := openQueue(dir)
	if err != nil {
		t.Fatalf("openQueue: %v", err)
	}
	t.Cleanup(func() { q.db.Close() })
	return q, dir
}

// seedRoomTranscript writes transcript .md files to <workspace>/rooms/<name>/.
func seedRoomTranscript(t *testing.T, workspace, room string, msgs []RoomMessage) {
	t.Helper()
	dir := filepath.Join(workspace, "rooms", room)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	for _, m := range msgs {
		name := fmt.Sprintf("%06d-%s.md", m.Seq, safePathSegment(m.From))
		data := renderRoomMsg(m)
		if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
			t.Fatalf("write transcript file: %v", err)
		}
	}
}

// TestLoadRoomsRehydratesLogAndSeq: two transcript files on disk + SQLite room
// row → loadRooms must populate r.log with 2 entries in seq order and r.seq==2.
func TestLoadRoomsRehydratesLogAndSeq(t *testing.T) {
	workspace := t.TempDir()
	t.Setenv("KOPOS_WORKSPACE", workspace)
	q, _ := mustOpenTestQueue(t)

	now := time.Now().Truncate(time.Second)
	msgs := []RoomMessage{
		{Seq: 1, Room: "feat-alpha", From: "sup", TS: now, Body: "bundle content"},
		{Seq: 2, Room: "feat-alpha", From: "worker", TS: now.Add(time.Minute), Body: "ack"},
	}

	if err := q.roomUpsert("feat-alpha", "test room", "sup", now); err != nil {
		t.Fatalf("roomUpsert: %v", err)
	}
	if err := q.roomAddMember("feat-alpha", "sup"); err != nil {
		t.Fatalf("roomAddMember sup: %v", err)
	}
	if err := q.roomAddMember("feat-alpha", "worker"); err != nil {
		t.Fatalf("roomAddMember worker: %v", err)
	}
	seedRoomTranscript(t, workspace, "feat-alpha", msgs)

	s := newFixtureState()
	s.queue = q
	if err := s.loadRooms(); err != nil {
		t.Fatalf("loadRooms: %v", err)
	}

	s.mu.Lock()
	r, ok := s.rooms["feat-alpha"]
	s.mu.Unlock()
	if !ok {
		t.Fatal("room feat-alpha not loaded")
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.seq != 2 {
		t.Fatalf("r.seq = %d, want 2", r.seq)
	}
	if len(r.log) != 2 {
		t.Fatalf("len(r.log) = %d, want 2", len(r.log))
	}
	if r.log[0].Seq != 1 || r.log[0].From != "sup" || r.log[0].Body != "bundle content" {
		t.Fatalf("r.log[0] = %+v, want seq=1 from=sup body=bundle content", r.log[0])
	}
	if r.log[1].Seq != 2 || r.log[1].From != "worker" {
		t.Fatalf("r.log[1] = %+v, want seq=2 from=worker", r.log[1])
	}
}

// TestLoadRoomsHandlesMissingTranscriptDir: room row in SQLite but no rooms/<slug>/
// directory on disk → loadRooms must succeed and leave an empty log.
func TestLoadRoomsHandlesMissingTranscriptDir(t *testing.T) {
	workspace := t.TempDir()
	t.Setenv("KOPOS_WORKSPACE", workspace)
	q, _ := mustOpenTestQueue(t)

	now := time.Now()
	if err := q.roomUpsert("feat-beta", "no transcript", "sup", now); err != nil {
		t.Fatalf("roomUpsert: %v", err)
	}
	if err := q.roomAddMember("feat-beta", "sup"); err != nil {
		t.Fatalf("roomAddMember: %v", err)
	}
	// Intentionally do NOT create rooms/feat-beta/ on disk.

	s := newFixtureState()
	s.queue = q
	if err := s.loadRooms(); err != nil {
		t.Fatalf("loadRooms: %v", err)
	}

	s.mu.Lock()
	r, ok := s.rooms["feat-beta"]
	s.mu.Unlock()
	if !ok {
		t.Fatal("room feat-beta not in state")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.seq != 0 {
		t.Fatalf("r.seq = %d, want 0", r.seq)
	}
	if len(r.log) != 0 {
		t.Fatalf("len(r.log) = %d, want 0", len(r.log))
	}
}

// TestLoadRoomsSkipsMalformedTranscriptFiles: one valid file and one corrupted
// file → only the valid message lands in r.log.
func TestLoadRoomsSkipsMalformedTranscriptFiles(t *testing.T) {
	workspace := t.TempDir()
	t.Setenv("KOPOS_WORKSPACE", workspace)
	q, _ := mustOpenTestQueue(t)

	now := time.Now().Truncate(time.Second)
	if err := q.roomUpsert("feat-gamma", "partial transcript", "sup", now); err != nil {
		t.Fatalf("roomUpsert: %v", err)
	}
	if err := q.roomAddMember("feat-gamma", "sup"); err != nil {
		t.Fatalf("roomAddMember: %v", err)
	}

	roomDir := filepath.Join(workspace, "rooms", "feat-gamma")
	if err := os.MkdirAll(roomDir, 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// Write one valid message file.
	good := RoomMessage{Seq: 1, Room: "feat-gamma", From: "sup", TS: now, Body: "hello"}
	goodData := renderRoomMsg(good)
	if err := os.WriteFile(filepath.Join(roomDir, "000001-sup.md"), goodData, 0600); err != nil {
		t.Fatalf("write good file: %v", err)
	}

	// Write one broken file (no frontmatter).
	if err := os.WriteFile(filepath.Join(roomDir, "000002-sup.md"), []byte("not frontmatter\n"), 0600); err != nil {
		t.Fatalf("write bad file: %v", err)
	}

	s := newFixtureState()
	s.queue = q
	if err := s.loadRooms(); err != nil {
		t.Fatalf("loadRooms: %v", err)
	}

	s.mu.Lock()
	r, ok := s.rooms["feat-gamma"]
	s.mu.Unlock()
	if !ok {
		t.Fatal("room feat-gamma not in state")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.log) != 1 {
		t.Fatalf("len(r.log) = %d, want 1 (skipped malformed)", len(r.log))
	}
	if r.seq != 1 {
		t.Fatalf("r.seq = %d, want 1", r.seq)
	}
}

// TestParseRoomMsgFileRoundTrip: renderRoomMsg then parseRoomMsgFile should
// return the original fields unchanged.
func TestParseRoomMsgFileRoundTrip(t *testing.T) {
	tmp := t.TempDir()
	now := time.Now().Truncate(time.Second)
	orig := RoomMessage{
		Seq:  42,
		Room: "my-room",
		From: "alice",
		TS:   now,
		Body: "hello world\nsecond line",
	}
	path := filepath.Join(tmp, "000042-alice.md")
	if err := os.WriteFile(path, renderRoomMsg(orig), 0600); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := parseRoomMsgFile(path)
	if err != nil {
		t.Fatalf("parseRoomMsgFile: %v", err)
	}
	if got.Seq != orig.Seq {
		t.Errorf("Seq: got %d, want %d", got.Seq, orig.Seq)
	}
	if got.Room != orig.Room {
		t.Errorf("Room: got %q, want %q", got.Room, orig.Room)
	}
	if got.From != orig.From {
		t.Errorf("From: got %q, want %q", got.From, orig.From)
	}
	if !got.TS.Equal(orig.TS) {
		t.Errorf("TS: got %v, want %v", got.TS, orig.TS)
	}
	if got.Body != orig.Body {
		t.Errorf("Body: got %q, want %q", got.Body, orig.Body)
	}
}

// TestRepublishBundleSurvivesRestart: publish → post follow-up → daemon restart
// → kopos history returns both messages.
func TestRepublishBundleSurvivesRestart(t *testing.T) {
	koposHome := setupIntegrationEnv(t)
	defer stopDaemonForHome(t, koposHome)

	repoRoot := mustInitRepoForIntegration(t)

	mustRequest(t, "register", map[string]any{
		"name": "sup", "pid": float64(8001), "role": "supervisor",
		"project": "restart-test", "repo_root": repoRoot,
	})
	mustRequest(t, "register", map[string]any{"name": "worker", "pid": float64(8002), "role": "worker"})

	pub := mustRequest(t, "task_publish", map[string]any{
		"from": "sup", "project": "restart-test", "repo_root": repoRoot,
		"workstreams": []any{map[string]any{
			"slug": "feat-restart", "branch": "feat/restart", "brief": "restart test",
		}},
	})
	if !pub.OK {
		t.Fatalf("task_publish: %+v", pub)
	}

	// Worker claims the task (joins the room).
	claim := mustRequest(t, "task_claim", map[string]any{
		"from": "worker", "slug": "feat-restart", "project": "restart-test",
	})
	if !claim.OK {
		t.Fatalf("task_claim: %+v", claim)
	}

	// Post a follow-up in the room (second message after the bundle).
	post := mustRequest(t, "post", map[string]any{
		"from": "sup", "room": "feat-restart", "body": "follow-up note",
	})
	if !post.OK {
		t.Fatalf("post follow-up: %+v", post)
	}

	restartDaemon(t, koposHome)

	// Re-register after restart.
	mustRequest(t, "register", map[string]any{
		"name": "sup", "pid": float64(8003), "role": "supervisor",
		"project": "restart-test", "repo_root": repoRoot,
	})
	mustRequest(t, "register", map[string]any{"name": "worker", "pid": float64(8004), "role": "worker"})

	// History must return both the bundle post and the follow-up.
	hist := mustRequest(t, "history", map[string]any{
		"from": "sup", "room": "feat-restart",
	})
	if !hist.OK {
		t.Fatalf("room_history after restart: %+v", hist)
	}
	data, _ := hist.Data.(map[string]any)
	msgs, _ := data["messages"].([]any)
	if len(msgs) < 2 {
		t.Fatalf("expected ≥2 messages in history after restart, got %d", len(msgs))
	}
}
