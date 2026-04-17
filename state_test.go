package main

import (
	"path/filepath"
	"testing"
	"time"
)

func newFixtureState() *State {
	return &State{
		agents:    make(map[string]*Agent),
		tunnels:   make(map[string]*Tunnel),
		anyWaiter: make(map[string]chan anyMsg),
		writes:    make(chan writeOp, 128),
		stop:      make(chan struct{}),
	}
}

func mustRegister(t *testing.T, s *State, name string, pid int) {
	t.Helper()
	resp := s.opRegister(Request{Args: map[string]any{"name": name, "pid": float64(pid)}})
	if !resp.OK {
		t.Fatalf("register %s failed: %+v", name, resp)
	}
}

func findSessionBySID(t *testing.T, data any, sid string) map[string]any {
	t.Helper()
	rows, ok := data.([]any)
	if !ok {
		t.Fatalf("sessions data type=%T, want []any", data)
	}
	for _, row := range rows {
		m, ok := row.(map[string]any)
		if !ok {
			continue
		}
		if got, _ := m["sid"].(string); got == sid {
			return m
		}
	}
	t.Fatalf("sid %s not found in sessions", sid)
	return nil
}

func TestStateRegisterRenewAgents(t *testing.T) {
	s := newFixtureState()

	reg := s.opRegister(Request{Args: map[string]any{"name": "alice", "pid": float64(1001)}})
	if !reg.OK {
		t.Fatalf("register failed: %+v", reg)
	}
	data := reg.Data.(map[string]any)
	if _, err := time.Parse(time.RFC3339, data["expires_at"].(string)); err != nil {
		t.Fatalf("parse expires_at: %v", err)
	}

	s.mu.Lock()
	s.agents["alice"].ExpiresAt = time.Now().Add(-time.Second)
	s.mu.Unlock()

	renew := s.opRenew(Request{Args: map[string]any{"from": "alice"}})
	if !renew.OK {
		t.Fatalf("renew failed: %+v", renew)
	}
	rdata := renew.Data.(map[string]any)
	expires2, err := time.Parse(time.RFC3339, rdata["expires_at"].(string))
	if err != nil {
		t.Fatalf("parse renewed expires_at: %v", err)
	}
	if expires2.Before(time.Now().Add(9 * time.Minute)) {
		t.Fatalf("expected renewed expiry to be near lease horizon, got %s", expires2)
	}

	unknown := s.opRenew(Request{Args: map[string]any{"from": "nobody"}})
	if unknown.OK || unknown.Code != CodeNotFound {
		t.Fatalf("renew unknown should be not_found: %+v", unknown)
	}

	agents := s.opAgents()
	if !agents.OK {
		t.Fatalf("agents failed: %+v", agents)
	}
	rows, ok := agents.Data.([]any)
	if !ok || len(rows) != 1 {
		t.Fatalf("agents rows=%T len=%d, want 1", agents.Data, len(rows))
	}
}

func TestStateTunnelAndSessions(t *testing.T) {
	s := newFixtureState()
	mustRegister(t, s, "alice", 1)
	mustRegister(t, s, "bob", 2)
	mustRegister(t, s, "carol", 3)

	notReg := s.opTunnel(Request{Args: map[string]any{"from": "ghost", "peer": "bob"}})
	if notReg.OK || notReg.Code != CodeNotFound {
		t.Fatalf("expected caller not found: %+v", notReg)
	}

	self := s.opTunnel(Request{Args: map[string]any{"from": "alice", "peer": "alice"}})
	if self.OK {
		t.Fatalf("self tunnel should fail: %+v", self)
	}

	ab := s.opTunnel(Request{Args: map[string]any{"from": "alice", "peer": "bob"}})
	if !ab.OK {
		t.Fatalf("open alice-bob tunnel: %+v", ab)
	}
	abSID := ab.Data.(map[string]any)["sid"].(string)

	bc := s.opTunnel(Request{Args: map[string]any{"from": "bob", "peer": "carol"}})
	if !bc.OK {
		t.Fatalf("open bob-carol tunnel: %+v", bc)
	}
	bcSID := bc.Data.(map[string]any)["sid"].(string)

	s.mu.Lock()
	abTunnel := s.tunnels[abSID]
	s.mu.Unlock()
	out := abTunnel.send(s, "alice", "hello bob", 5*time.Millisecond)
	if out.OK || out.Code != CodeTimeout {
		t.Fatalf("send timeout expected for setup send: %+v", out)
	}

	sessions := s.opSessions(Request{Args: map[string]any{"from": "bob"}})
	if !sessions.OK {
		t.Fatalf("sessions failed: %+v", sessions)
	}
	abRow := findSessionBySID(t, sessions.Data, abSID)
	if pending := int(abRow["pending_for_me"].(int)); pending != 1 {
		t.Fatalf("pending_for_me on %s = %d, want 1", abSID, pending)
	}
	bcRow := findSessionBySID(t, sessions.Data, bcSID)
	if peer := bcRow["peer"].(string); peer != "carol" {
		t.Fatalf("peer on %s=%s, want carol", bcSID, peer)
	}
}

func TestStateHistoryAccessAndFilters(t *testing.T) {
	s := newFixtureState()
	mustRegister(t, s, "alice", 1)
	mustRegister(t, s, "bob", 2)
	mustRegister(t, s, "carol", 3)

	open := s.opTunnel(Request{Args: map[string]any{"from": "alice", "peer": "bob"}})
	if !open.OK {
		t.Fatalf("open tunnel: %+v", open)
	}
	sid := open.Data.(map[string]any)["sid"].(string)

	s.mu.Lock()
	tun := s.tunnels[sid]
	tun.log = []Message{
		{Seq: 1, SID: sid, From: "alice", TS: time.Now().Add(-2 * time.Second), Body: "one"},
		{Seq: 2, SID: sid, From: "bob", TS: time.Now().Add(-1 * time.Second), Body: "two"},
		{Seq: 3, SID: sid, From: "alice", TS: time.Now(), Body: "three"},
	}
	tun.seq = 3
	s.mu.Unlock()

	nonPeer := s.opHistory(Request{Args: map[string]any{"from": "carol", "sid": sid}})
	if nonPeer.OK || nonPeer.Code != CodeNotFound {
		t.Fatalf("non-peer history should be not_found: %+v", nonPeer)
	}

	h := s.opHistory(Request{Args: map[string]any{"from": "bob", "sid": sid, "since": float64(1), "limit": float64(1)}})
	if !h.OK {
		t.Fatalf("peer history failed: %+v", h)
	}
	msgs := h.Data.(map[string]any)["messages"].([]any)
	if len(msgs) != 1 {
		t.Fatalf("history len=%d, want 1", len(msgs))
	}
	m := msgs[0].(map[string]any)
	if seq := int(m["seq"].(int)); seq != 3 {
		t.Fatalf("history seq=%d, want 3", seq)
	}
}

func TestStateSweepExpiresAgentAndClosesTunnel(t *testing.T) {
	t.Setenv("LESCHE_WORKSPACE", filepath.Join(t.TempDir(), "workspace"))
	s := newFixtureState()
	now := time.Now()

	s.agents["alice"] = &Agent{Name: "alice", ExpiresAt: now.Add(-time.Second), LastSeenAt: now.Add(-time.Minute)}
	s.agents["bob"] = &Agent{Name: "bob", ExpiresAt: now.Add(time.Hour), LastSeenAt: now}

	tun := newTunnel("sid-expire", "alice", "bob")
	s.tunnels[tun.SID] = tun

	s.sweep()

	s.mu.Lock()
	_, aliceStill := s.agents["alice"]
	_, bobStill := s.agents["bob"]
	s.mu.Unlock()
	if aliceStill {
		t.Fatalf("alice should have expired and been removed")
	}
	if !bobStill {
		t.Fatalf("bob should still exist")
	}

	tun.mu.Lock()
	closed := tun.closed
	hangup := tun.hangup
	tun.mu.Unlock()
	if !closed || hangup != "peer lease expired" {
		t.Fatalf("tunnel close mismatch: closed=%v hangup=%q", closed, hangup)
	}

	await := tun.await("bob", 10*time.Millisecond)
	if await.OK || await.Code != CodePeerClosed {
		t.Fatalf("await after sweep-close should be peer_closed: %+v", await)
	}
}
