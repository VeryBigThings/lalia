package main

import (
	"path/filepath"
	"testing"
	"time"
)

func newFixtureState() *State {
	return &State{
		agents:    make(map[string]*Agent),
		nameIdx:   make(map[string]string),
		channels:  make(map[string]*Channel),
		rooms:     make(map[string]*Room),
		plans:     make(map[string]*Plan),
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
	a := s.agentByName("alice")
	if a == nil {
		s.mu.Unlock()
		t.Fatalf("alice not found after register")
	}
	a.ExpiresAt = time.Now().Add(-time.Second)
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
	if expires2.Before(time.Now().Add(leaseTTL - time.Minute)) {
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

// TestChannelTellReadPeek covers the basic P2P lifecycle: tell creates a
// channel on first use, the peer reads it with non-blocking read, peek is
// non-destructive, and a second read returns empty.
func TestChannelTellReadPeek(t *testing.T) {
	s := newFixtureState()
	mustRegister(t, s, "alice", 1)
	mustRegister(t, s, "bob", 2)

	// tell alice→bob
	tell := s.opTell(Request{Args: map[string]any{"from": "alice", "peer": "bob", "body": "hello bob"}})
	if !tell.OK {
		t.Fatalf("tell failed: %+v", tell)
	}

	// bob peeks: sees pending message without consuming.
	peek := s.opPeek(Request{Args: map[string]any{"from": "bob", "peer": "alice"}})
	if !peek.OK {
		t.Fatalf("peek failed: %+v", peek)
	}
	pmsgs := peek.Data.(map[string]any)["messages"].([]any)
	if len(pmsgs) != 1 {
		t.Fatalf("peek count=%d, want 1", len(pmsgs))
	}

	// bob reads: consumes it (non-blocking).
	read := s.opRead(Request{Args: map[string]any{"from": "bob", "peer": "alice", "timeout": float64(0)}})
	if !read.OK {
		t.Fatalf("read failed: %+v", read)
	}
	body, _ := read.Data.(map[string]any)["body"].(string)
	if body != "hello bob" {
		t.Fatalf("read body=%q, want hello bob", body)
	}

	// read again → empty.
	again := s.opRead(Request{Args: map[string]any{"from": "bob", "peer": "alice", "timeout": float64(0)}})
	if !again.OK {
		t.Fatalf("read again failed: %+v", again)
	}
	m := again.Data.(map[string]any)
	if _, has := m["body"]; has {
		t.Fatalf("second read should be empty, got %+v", m)
	}
}

// TestChannelConsecutiveTellsPreservedOrder verifies the turn-FSM removal:
// one side can send multiple messages in a row and the peer reads them in
// send order.
func TestChannelConsecutiveTellsPreservedOrder(t *testing.T) {
	s := newFixtureState()
	mustRegister(t, s, "alice", 1)
	mustRegister(t, s, "bob", 2)

	for _, body := range []string{"one", "two", "three"} {
		resp := s.opTell(Request{Args: map[string]any{"from": "alice", "peer": "bob", "body": body}})
		if !resp.OK {
			t.Fatalf("tell %s failed: %+v", body, resp)
		}
	}

	got := []string{}
	for i := 0; i < 3; i++ {
		resp := s.opRead(Request{Args: map[string]any{"from": "bob", "peer": "alice", "timeout": float64(0)}})
		if !resp.OK {
			t.Fatalf("read %d failed: %+v", i, resp)
		}
		body, _ := resp.Data.(map[string]any)["body"].(string)
		got = append(got, body)
	}
	want := []string{"one", "two", "three"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("read order got=%v, want=%v", got, want)
		}
	}
}

// TestChannelBlockingRead blocks on read until a tell lands, then returns.
func TestChannelBlockingRead(t *testing.T) {
	s := newFixtureState()
	mustRegister(t, s, "alice", 1)
	mustRegister(t, s, "bob", 2)

	done := make(chan Response, 1)
	go func() {
		done <- s.opRead(Request{Args: map[string]any{"from": "bob", "peer": "alice", "timeout": float64(2)}})
	}()
	time.Sleep(25 * time.Millisecond)

	tell := s.opTell(Request{Args: map[string]any{"from": "alice", "peer": "bob", "body": "late"}})
	if !tell.OK {
		t.Fatalf("tell failed: %+v", tell)
	}

	resp := <-done
	if !resp.OK {
		t.Fatalf("blocking read failed: %+v", resp)
	}
	if body, _ := resp.Data.(map[string]any)["body"].(string); body != "late" {
		t.Fatalf("body=%q, want late", body)
	}
}

// TestChannelReadTimeoutEmpty verifies that read with timeout returns empty
// without error when nothing arrives in time.
func TestChannelReadTimeoutEmpty(t *testing.T) {
	s := newFixtureState()
	mustRegister(t, s, "alice", 1)
	mustRegister(t, s, "bob", 2)

	resp := s.opRead(Request{Args: map[string]any{"from": "bob", "peer": "alice", "timeout": float64(0)}})
	if !resp.OK {
		t.Fatalf("read timeout should return OK+empty: %+v", resp)
	}
	m := resp.Data.(map[string]any)
	if _, has := m["body"]; has {
		t.Fatalf("expected empty, got %+v", m)
	}
}

// TestChannelRejectsUnregisteredPeer ensures a tell to an unknown peer
// fails with CodeNotFound.
func TestChannelRejectsUnregisteredPeer(t *testing.T) {
	s := newFixtureState()
	mustRegister(t, s, "alice", 1)

	resp := s.opTell(Request{Args: map[string]any{"from": "alice", "peer": "ghost", "body": "hi"}})
	if resp.OK || resp.Code != CodeNotFound {
		t.Fatalf("tell to unknown peer should be not_found: %+v", resp)
	}
}

// TestChannelsListing verifies the "channels" op returns the caller's
// peer-pair channels with pending counts.
func TestChannelsListing(t *testing.T) {
	s := newFixtureState()
	mustRegister(t, s, "alice", 1)
	mustRegister(t, s, "bob", 2)
	mustRegister(t, s, "carol", 3)

	s.opTell(Request{Args: map[string]any{"from": "alice", "peer": "bob", "body": "x"}})
	s.opTell(Request{Args: map[string]any{"from": "alice", "peer": "carol", "body": "y"}})

	list := s.opChannels(Request{Args: map[string]any{"from": "alice"}})
	if !list.OK {
		t.Fatalf("channels failed: %+v", list)
	}
	rows := list.Data.([]any)
	if len(rows) != 2 {
		t.Fatalf("alice should have 2 channels, got %d", len(rows))
	}
}

// TestHistoryChannelPeerOnly: non-peer cannot read the transcript.
func TestHistoryChannelPeerOnly(t *testing.T) {
	s := newFixtureState()
	mustRegister(t, s, "alice", 1)
	mustRegister(t, s, "bob", 2)
	mustRegister(t, s, "carol", 3)

	s.opTell(Request{Args: map[string]any{"from": "alice", "peer": "bob", "body": "m1"}})
	s.opTell(Request{Args: map[string]any{"from": "bob", "peer": "alice", "body": "m2"}})

	h := s.opHistory(Request{Args: map[string]any{"from": "alice", "peer": "bob"}})
	if !h.OK {
		t.Fatalf("history failed: %+v", h)
	}
	msgs := h.Data.(map[string]any)["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("history len=%d, want 2", len(msgs))
	}

	// carol is not a peer of alice↔bob; she asks about her own peer-pair
	// with bob, which has no messages yet.
	h2 := s.opHistory(Request{Args: map[string]any{"from": "carol", "peer": "bob"}})
	if h2.OK || h2.Code != CodeNotFound {
		t.Fatalf("carol reading carol↔bob (empty) should be not_found: %+v", h2)
	}
}

// TestStateSweepExpiresAgentAndReleasesWaiter: an agent whose lease expires
// has its hanging read released immediately so its client returns.
func TestStateSweepExpiresAgentAndReleasesWaiter(t *testing.T) {
	t.Setenv("LESCHE_WORKSPACE", filepath.Join(t.TempDir(), "workspace"))
	s := newFixtureState()
	now := time.Now()

	aliceID := "01ALICE00000000000000000000"
	bobID := "01BOB000000000000000000000"
	s.mu.Lock()
	s.agents[aliceID] = &Agent{AgentID: aliceID, Name: "alice", ExpiresAt: now.Add(-time.Second), LastSeenAt: now.Add(-time.Minute)}
	s.nameIdx["alice"] = aliceID
	s.agents[bobID] = &Agent{AgentID: bobID, Name: "bob", ExpiresAt: now.Add(time.Hour), LastSeenAt: now}
	s.nameIdx["bob"] = bobID
	s.mu.Unlock()
	// open channel alice-bob, register alice's waiter manually.
	ch := s.getOrCreateChannel("alice", "bob")

	readDone := make(chan Response, 1)
	go func() {
		readDone <- ch.read("alice", 3*time.Second)
	}()
	time.Sleep(25 * time.Millisecond)

	s.sweep()

	select {
	case resp := <-readDone:
		if resp.OK || resp.Code != CodePeerClosed {
			t.Fatalf("alice's hanging read after sweep should be peer_closed: %+v", resp)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("alice's hanging read did not return after sweep")
	}

	s.mu.Lock()
	aliceStill := s.agentByName("alice") != nil
	bobStill := s.agentByName("bob") != nil
	s.mu.Unlock()
	if aliceStill {
		t.Fatalf("alice should have been dropped")
	}
	if !bobStill {
		t.Fatalf("bob should still exist")
	}
}

// TestOpUnregisterReleasesWaitersAndEvicts: unregister drops the agent,
// releases their hanging channel read with peer_closed, and evicts them
// from rooms.
func TestOpUnregisterReleasesWaitersAndEvicts(t *testing.T) {
	t.Setenv("LESCHE_HOME", t.TempDir())
	t.Setenv("LESCHE_WORKSPACE", filepath.Join(t.TempDir(), "workspace"))
	s := newFixtureState()
	mustRegister(t, s, "alice", 1)
	mustRegister(t, s, "bob", 2)

	if _, err := loadPrivateKey("alice"); err != nil {
		t.Fatalf("alice key should exist after register: %v", err)
	}

	room := newRoom("ops", "", "alice")
	room.members["bob"] = true
	s.rooms["ops"] = room

	ch := s.getOrCreateChannel("alice", "bob")
	readDone := make(chan Response, 1)
	go func() {
		readDone <- ch.read("alice", 3*time.Second)
	}()
	time.Sleep(25 * time.Millisecond)

	resp := s.opUnregister(Request{Args: map[string]any{"from": "alice"}})
	if !resp.OK {
		t.Fatalf("unregister failed: %+v", resp)
	}

	select {
	case r := <-readDone:
		if r.OK || r.Code != CodePeerClosed {
			t.Fatalf("hanging read after unregister should be peer_closed: %+v", r)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("hanging read did not return after unregister")
	}

	s.mu.Lock()
	aliceStill := s.agentByName("alice") != nil
	s.mu.Unlock()
	if aliceStill {
		t.Fatalf("alice should have been removed from agents")
	}

	room.mu.Lock()
	_, stillMember := room.members["alice"]
	room.mu.Unlock()
	if stillMember {
		t.Fatalf("alice should have been evicted from room")
	}

	if _, err := loadPrivateKey("alice"); err == nil {
		t.Fatalf("alice key should have been deleted on unregister")
	}

	// second unregister is a NotFound.
	again := s.opUnregister(Request{Args: map[string]any{"from": "alice"}})
	if again.OK || again.Code != CodeNotFound {
		t.Fatalf("unregister twice should be not_found: %+v", again)
	}
}

// TestStateSweepEvictsExpiredAgentsFromRooms: room membership tracks agent
// lifetime.
func TestStateSweepEvictsExpiredAgentsFromRooms(t *testing.T) {
	t.Setenv("LESCHE_WORKSPACE", filepath.Join(t.TempDir(), "workspace"))
	s := newFixtureState()
	now := time.Now()

	aliceID := "01ALICE00000000000000000000"
	bobID := "01BOB000000000000000000000"
	s.mu.Lock()
	s.agents[aliceID] = &Agent{AgentID: aliceID, Name: "alice", ExpiresAt: now.Add(-time.Second), LastSeenAt: now.Add(-time.Minute)}
	s.nameIdx["alice"] = aliceID
	s.agents[bobID] = &Agent{AgentID: bobID, Name: "bob", ExpiresAt: now.Add(time.Hour), LastSeenAt: now}
	s.nameIdx["bob"] = bobID
	s.mu.Unlock()

	room := newRoom("ops", "", "alice")
	room.members["bob"] = true
	s.rooms["ops"] = room

	s.sweep()

	room.mu.Lock()
	_, aliceMember := room.members["alice"]
	_, bobMember := room.members["bob"]
	room.mu.Unlock()
	if aliceMember {
		t.Fatalf("alice should have been evicted")
	}
	if !bobMember {
		t.Fatalf("bob should remain")
	}

	if len(s.writes) == 0 {
		t.Fatalf("expected sweep to queue MEMBERS.md persistence after eviction")
	}
}
