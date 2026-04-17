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
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	t.Setenv("HOME", home)
	t.Setenv("LESCHE_WORKSPACE", workspace)
	return home
}

func stopDaemonForHome(t *testing.T, home string) {
	t.Helper()
	pidFile := filepath.Join(home, ".lesche", "pid")
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

	sock := filepath.Join(home, ".lesche", "sock")
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

func sidFrom(resp *Response) string {
	if resp == nil || resp.Data == nil {
		return ""
	}
	m, _ := resp.Data.(map[string]any)
	if m == nil {
		return ""
	}
	sid, _ := m["sid"].(string)
	return sid
}

type requestResult struct {
	resp *Response
	err  error
}

func TestIntegrationRequestFlow(t *testing.T) {
	home := setupIntegrationEnv(t)
	defer stopDaemonForHome(t, home)

	ra := mustRequest(t, "register", map[string]any{"name": "alice", "pid": float64(101)})
	if !ra.OK {
		t.Fatalf("register alice failed: %+v", ra)
	}
	rb := mustRequest(t, "register", map[string]any{"name": "bob", "pid": float64(102)})
	if !rb.OK {
		t.Fatalf("register bob failed: %+v", rb)
	}

	renew := mustRequest(t, "renew", map[string]any{"from": "alice"})
	if !renew.OK {
		t.Fatalf("renew alice failed: %+v", renew)
	}

	agents := mustRequest(t, "agents", nil)
	if !agents.OK {
		t.Fatalf("agents failed: %+v", agents)
	}
	rows, ok := agents.Data.([]any)
	if !ok || len(rows) < 2 {
		t.Fatalf("agents rows=%T len=%d, want >=2", agents.Data, len(rows))
	}

	open := mustRequest(t, "tunnel", map[string]any{"from": "alice", "peer": "bob"})
	if !open.OK {
		t.Fatalf("open tunnel failed: %+v", open)
	}
	sid := sidFrom(open)
	if sid == "" {
		t.Fatalf("empty sid from tunnel response: %+v", open)
	}

	bobSendResult := make(chan requestResult, 1)
	go func() {
		first, err := request("await", map[string]any{"from": "bob", "sid": sid, "timeout": float64(3)})
		if err != nil {
			bobSendResult <- requestResult{err: err}
			return
		}
		if !first.OK {
			bobSendResult <- requestResult{resp: first}
			return
		}
		body, _ := first.Data.(map[string]any)["body"].(string)
		if body != "hi bob" {
			bobSendResult <- requestResult{resp: &Response{Error: "unexpected bob await body: " + body}}
			return
		}
		r, err := request("send", map[string]any{"from": "bob", "sid": sid, "body": "hi alice", "timeout": float64(1)})
		bobSendResult <- requestResult{resp: r, err: err}
	}()

	aliceSend := mustRequest(t, "send", map[string]any{"from": "alice", "sid": sid, "body": "hi bob", "timeout": float64(3)})
	if !aliceSend.OK {
		t.Fatalf("alice send failed: %+v", aliceSend)
	}
	if body := aliceSend.Data.(map[string]any)["body"].(string); body != "hi alice" {
		t.Fatalf("alice send returned body=%q, want hi alice", body)
	}

	bobSendRes := <-bobSendResult
	if bobSendRes.err != nil {
		t.Fatalf("bob send path error: %v", bobSendRes.err)
	}
	bobSend := bobSendRes.resp
	if bobSend.OK || bobSend.Code != CodeTimeout {
		t.Fatalf("bob send should timeout waiting for follow-up: %+v", bobSend)
	}

	history := mustRequest(t, "history", map[string]any{"from": "alice", "sid": sid, "since": float64(0), "limit": float64(0)})
	if !history.OK {
		t.Fatalf("history failed: %+v", history)
	}
	msgs := history.Data.(map[string]any)["messages"].([]any)
	if len(msgs) < 2 {
		t.Fatalf("history messages=%d, want >=2", len(msgs))
	}

	mustRequest(t, "register", map[string]any{"name": "carol", "pid": float64(103)})
	nonPeer := mustRequest(t, "history", map[string]any{"from": "carol", "sid": sid})
	if nonPeer.OK || nonPeer.Code != CodeNotFound {
		t.Fatalf("non-peer history should be not_found: %+v", nonPeer)
	}

	awaitAnyCh := make(chan requestResult, 1)
	go func() {
		r, err := request("await-any", map[string]any{"from": "bob", "timeout": float64(3)})
		awaitAnyCh <- requestResult{resp: r, err: err}
	}()
	time.Sleep(50 * time.Millisecond)

	sendForAny := mustRequest(t, "send", map[string]any{"from": "alice", "sid": sid, "body": "for await-any", "timeout": float64(1)})
	if sendForAny.OK || sendForAny.Code != CodeTimeout {
		t.Fatalf("alice send for await-any should timeout: %+v", sendForAny)
	}

	anyRes := <-awaitAnyCh
	if anyRes.err != nil {
		t.Fatalf("await-any request error: %v", anyRes.err)
	}
	anyResp := anyRes.resp
	if !anyResp.OK {
		t.Fatalf("await-any failed: %+v", anyResp)
	}
	anyData := anyResp.Data.(map[string]any)
	if gotSID := anyData["sid"].(string); gotSID != sid {
		t.Fatalf("await-any sid=%q, want %q", gotSID, sid)
	}
	if gotBody := anyData["body"].(string); gotBody != "for await-any" {
		t.Fatalf("await-any body=%q, want for await-any", gotBody)
	}

	sessions := mustRequest(t, "sessions", map[string]any{"from": "bob"})
	if !sessions.OK {
		t.Fatalf("sessions failed: %+v", sessions)
	}
	rows2 := sessions.Data.([]any)
	if len(rows2) == 0 {
		t.Fatalf("sessions for bob should include tunnel %s", sid)
	}

	closed := mustRequest(t, "close", map[string]any{"from": "alice", "sid": sid})
	if !closed.OK {
		t.Fatalf("close failed: %+v", closed)
	}

	afterClose := mustRequest(t, "await", map[string]any{"from": "bob", "sid": sid, "timeout": float64(1)})
	if afterClose.OK || afterClose.Code != CodePeerClosed {
		t.Fatalf("await after close should be peer_closed: %+v", afterClose)
	}
}
