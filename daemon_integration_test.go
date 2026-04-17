package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Test helper mode: when request()/spawnDaemon launches this test binary with
// "--daemon", run the real daemon entrypoint instead of the test harness.
func init() {
	for _, a := range os.Args[1:] {
		if a == "--daemon" {
			runDaemon()
			os.Exit(0)
		}
	}
}

func setupIntegrationEnv(t *testing.T) string {
	t.Helper()
	base, err := os.MkdirTemp("", "ls-")
	if err != nil {
		t.Fatalf("mktemp base dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })

	home := filepath.Join(base, "h")
	koposHome := filepath.Join(base, "lh")
	workspace := filepath.Join(base, "w")
	if err := os.MkdirAll(home, 0700); err != nil {
		t.Fatalf("mkdir home: %v", err)
	}
	t.Setenv("HOME", home)
	t.Setenv("KOPOS_HOME", koposHome)
	t.Setenv("KOPOS_WORKSPACE", workspace)
	return koposHome
}

func stopDaemonForHome(t *testing.T, koposHome string) {
	t.Helper()
	pidFile := filepath.Join(koposHome, "pid")
	data, err := os.ReadFile(pidFile)
	if err != nil {
		return
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return
	}
	_ = proc.Signal(syscall.SIGTERM)

	sock := filepath.Join(koposHome, "sock")
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(sock); os.IsNotExist(err) {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func mustRequest(t *testing.T, op string, args map[string]any) *Response {
	t.Helper()
	resp, err := request(op, args)
	if err != nil {
		t.Fatalf("request %s err: %v", op, err)
	}
	return resp
}

type requestResult struct {
	resp *Response
	err  error
}

// TestIntegrationChannelFlow verifies the end-to-end peer-to-peer flow:
// register, tell, read, consecutive tells without turn enforcement, and
// read-any across channels.
func TestIntegrationChannelFlow(t *testing.T) {
	koposHome := setupIntegrationEnv(t)
	defer stopDaemonForHome(t, koposHome)

	ra := mustRequest(t, "register", map[string]any{"name": "alice", "pid": float64(101)})
	if !ra.OK {
		t.Fatalf("register alice failed: %+v", ra)
	}
	rb := mustRequest(t, "register", map[string]any{"name": "bob", "pid": float64(102)})
	if !rb.OK {
		t.Fatalf("register bob failed: %+v", rb)
	}

	// alice tells bob three times in a row (previously illegal under turn FSM).
	for _, body := range []string{"hi bob", "follow up", "one more"} {
		resp := mustRequest(t, "tell", map[string]any{"from": "alice", "peer": "bob", "body": body})
		if !resp.OK {
			t.Fatalf("tell %s failed: %+v", body, resp)
		}
	}

	// bob reads them in order.
	want := []string{"hi bob", "follow up", "one more"}
	for i, expected := range want {
		resp := mustRequest(t, "read", map[string]any{"from": "bob", "peer": "alice", "timeout": float64(1)})
		if !resp.OK {
			t.Fatalf("read %d failed: %+v", i, resp)
		}
		body, _ := resp.Data.(map[string]any)["body"].(string)
		if body != expected {
			t.Fatalf("read %d body=%q, want %q", i, body, expected)
		}
	}

	// bob reads again → empty (non-error).
	empty := mustRequest(t, "read", map[string]any{"from": "bob", "peer": "alice", "timeout": float64(0)})
	if !empty.OK {
		t.Fatalf("empty read should be OK: %+v", empty)
	}
	if m, _ := empty.Data.(map[string]any); m != nil {
		if _, has := m["body"]; has {
			t.Fatalf("empty read should not have body: %+v", m)
		}
	}

	// history between alice and bob.
	history := mustRequest(t, "history", map[string]any{"from": "alice", "peer": "bob"})
	if !history.OK {
		t.Fatalf("history failed: %+v", history)
	}
	msgs := history.Data.(map[string]any)["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("history msgs=%d, want 3", len(msgs))
	}

	// channels listing for alice.
	chans := mustRequest(t, "channels", map[string]any{"from": "alice"})
	if !chans.OK {
		t.Fatalf("channels failed: %+v", chans)
	}
	if rows, _ := chans.Data.([]any); len(rows) != 1 {
		t.Fatalf("alice channels=%d, want 1", len(rows))
	}

	// read-any: kick off a blocking read-any for bob, tell him from alice,
	// confirm bob's read-any returned with kind=peer target=alice.
	resultCh := make(chan requestResult, 1)
	go func() {
		r, err := request("read-any", map[string]any{"from": "bob", "timeout": float64(3)})
		resultCh <- requestResult{resp: r, err: err}
	}()
	time.Sleep(50 * time.Millisecond)
	tellAgain := mustRequest(t, "tell", map[string]any{"from": "alice", "peer": "bob", "body": "for read-any"})
	if !tellAgain.OK {
		t.Fatalf("tell for read-any failed: %+v", tellAgain)
	}
	res := <-resultCh
	if res.err != nil {
		t.Fatalf("read-any err: %v", res.err)
	}
	if !res.resp.OK {
		t.Fatalf("read-any failed: %+v", res.resp)
	}
	am := res.resp.Data.(map[string]any)
	if kind, _ := am["kind"].(string); kind != "peer" {
		t.Fatalf("read-any kind=%q, want peer", kind)
	}
	if tgt, _ := am["target"].(string); tgt != "alice" {
		t.Fatalf("read-any target=%q, want alice", tgt)
	}
	if body, _ := am["body"].(string); body != "for read-any" {
		t.Fatalf("read-any body=%q, want for read-any", body)
	}
}

// TestIntegrationRoomFlow verifies room post + read + read-any.
func TestIntegrationRoomFlow(t *testing.T) {
	koposHome := setupIntegrationEnv(t)
	defer stopDaemonForHome(t, koposHome)

	mustRequest(t, "register", map[string]any{"name": "alice", "pid": float64(201)})
	mustRequest(t, "register", map[string]any{"name": "bob", "pid": float64(202)})

	if !mustRequest(t, "room_create", map[string]any{"from": "alice", "name": "eng"}).OK {
		t.Fatalf("room_create failed")
	}
	if !mustRequest(t, "join", map[string]any{"from": "bob", "room": "eng"}).OK {
		t.Fatalf("bob join failed")
	}
	if !mustRequest(t, "post", map[string]any{"from": "alice", "room": "eng", "body": "ship friday"}).OK {
		t.Fatalf("post failed")
	}

	read := mustRequest(t, "read", map[string]any{"from": "bob", "room": "eng", "timeout": float64(0)})
	if !read.OK {
		t.Fatalf("read room failed: %+v", read)
	}
	msgs := read.Data.(map[string]any)["messages"].([]any)
	if len(msgs) != 1 {
		t.Fatalf("read msgs=%d, want 1", len(msgs))
	}
}

// TestIntegrationUnauthorizedCaller: an unregistered agent is rejected on
// authenticated ops.
func TestIntegrationUnauthorizedCaller(t *testing.T) {
	koposHome := setupIntegrationEnv(t)
	defer stopDaemonForHome(t, koposHome)

	mustRequest(t, "register", map[string]any{"name": "alice", "pid": float64(301)})
	// ghost is not registered and has no key file; the client refuses to
	// sign. Either client-side key-load error or a server-side unauthorized
	// response is acceptable — both prove unauthorized calls don't land.
	resp, err := request("tell", map[string]any{"from": "ghost", "peer": "alice", "body": "hi"})
	if err == nil {
		if resp.OK {
			t.Fatalf("ghost tell should have failed: %+v", resp)
		}
		if resp.Code != CodeUnauthorized {
			t.Fatalf("expected unauthorized, got %+v", resp)
		}
	}
}
