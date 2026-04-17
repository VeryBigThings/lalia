package main

import (
	"fmt"
	"sync"
	"time"
)

type Agent struct {
	Name       string    `json:"name"`
	PID        int       `json:"pid"`
	StartedAt  time.Time `json:"started_at"`
	LastSeenAt time.Time `json:"last_seen_at"`
	ExpiresAt  time.Time `json:"expires_at"`
	Pubkey     string    `json:"pubkey"` // hex-encoded Ed25519 public key
}

// Lease duration. Any command from the agent renews; sweeper drops expired.
const leaseTTL = 10 * time.Minute
const sweepInterval = 30 * time.Second

type State struct {
	mu      sync.Mutex
	agents  map[string]*Agent
	tunnels map[string]*Tunnel
	rooms   map[string]*Room

	// state-level any-waiters: one per agent, 1-buffered
	anyWaiter map[string]chan anyMsg

	writes chan writeOp
	wg     sync.WaitGroup
	stop   chan struct{}
}

type anyMsg struct {
	sid string
	msg Message
}

func newState() (*State, error) {
	s := &State{
		agents:    make(map[string]*Agent),
		tunnels:   make(map[string]*Tunnel),
		rooms:     make(map[string]*Room),
		anyWaiter: make(map[string]chan anyMsg),
		writes:    make(chan writeOp, 128),
		stop:      make(chan struct{}),
	}
	if err := ensureWorkspace(); err != nil {
		return nil, err
	}
	if err := s.loadRegistry(); err != nil {
		return nil, err
	}
	return s, nil
}

// renewLease extends the lease on an agent and updates last-seen.
// Called on every request that carries a "from" identity.
// Writes the updated record back to the workspace asynchronously.
func (s *State) renewLease(name string) {
	if name == "" {
		return
	}
	s.mu.Lock()
	a, ok := s.agents[name]
	if !ok {
		s.mu.Unlock()
		return
	}
	now := time.Now()
	a.LastSeenAt = now
	a.ExpiresAt = now.Add(leaseTTL)
	snapshot := *a
	s.mu.Unlock()
	s.persistAgent(&snapshot)
}

// startSweeper runs a goroutine that drops expired agents and closes their tunnels.
func (s *State) startSweeper() {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		tick := time.NewTicker(sweepInterval)
		defer tick.Stop()
		for {
			select {
			case <-s.stop:
				return
			case <-tick.C:
				s.sweep()
			}
		}
	}()
}

func (s *State) sweep() {
	now := time.Now()
	s.mu.Lock()
	var expired []string
	for name, a := range s.agents {
		if now.After(a.ExpiresAt) {
			expired = append(expired, name)
		}
	}
	if len(expired) == 0 {
		s.mu.Unlock()
		return
	}
	// close tunnels involving expired peers
	var doomedTunnels []*Tunnel
	for _, t := range s.tunnels {
		for _, name := range expired {
			if t.PeerA == name || t.PeerB == name {
				doomedTunnels = append(doomedTunnels, t)
				break
			}
		}
	}
	for _, name := range expired {
		delete(s.agents, name)
	}
	rooms := make([]*Room, 0, len(s.rooms))
	for _, r := range s.rooms {
		rooms = append(rooms, r)
	}
	s.mu.Unlock()

	for _, t := range doomedTunnels {
		t.close("peer lease expired")
	}
	expiredSet := make(map[string]struct{}, len(expired))
	for _, name := range expired {
		expiredSet[name] = struct{}{}
	}
	for _, r := range rooms {
		if r.removeMembers(expiredSet) {
			s.persistRoomMembers(r)
		}
	}
	for _, name := range expired {
		s.removeAgentFile(name)
	}
}

func (s *State) requestStop() {
	select {
	case <-s.stop:
	default:
		close(s.stop)
	}
	// close all tunnels so blocked waiters release
	s.mu.Lock()
	for _, t := range s.tunnels {
		t.closeAll("daemon shutting down")
	}
	s.mu.Unlock()
	close(s.writes)
}

func (s *State) waitWriterDone() { s.wg.Wait() }

func (s *State) dispatch(req Request) Response {
	// register is unauthenticated by design — first-to-claim binds the pubkey
	// for that name via ensureKey (which reuses an existing key if present).
	// every other op that carries `from` must be signed with that pubkey.
	if req.Op != "register" && req.Op != "stop" && req.Op != "agents" {
		if from, ok := req.Args["from"].(string); ok && from != "" {
			s.mu.Lock()
			a, known := s.agents[from]
			var pubHex string
			if known {
				pubHex = a.Pubkey
			}
			s.mu.Unlock()
			if !known {
				return Response{Error: "not registered: " + from, Code: CodeUnauthorized}
			}
			if pubHex == "" {
				return Response{Error: "agent " + from + " has no pubkey on file; re-register to acquire one", Code: CodeUnauthorized}
			}
			if err := verifyRequest(pubHex, req.Args); err != nil {
				return Response{Error: "signature rejected: " + err.Error(), Code: CodeUnauthorized}
			}
		}
	}
	// lease renewal on any request that carries a caller identity
	if from, ok := req.Args["from"].(string); ok && from != "" {
		s.renewLease(from)
	}
	switch req.Op {
	case "register":
		return s.opRegister(req)
	case "agents":
		return s.opAgents()
	case "rooms":
		return s.opRooms(req)
	case "room_create", "room-create":
		return s.opRoomCreate(req)
	case "join":
		return s.opJoin(req)
	case "leave":
		return s.opLeave(req)
	case "participants":
		return s.opParticipants(req)
	case "post":
		return s.opPost(req)
	case "inbox":
		return s.opInbox(req)
	case "peek":
		return s.opPeek(req)
	case "sessions":
		return s.opSessions(req)
	case "history":
		return s.opHistory(req)
	case "tunnel":
		return s.opTunnel(req)
	case "send":
		return s.opSend(req)
	case "await":
		return s.opAwait(req)
	case "await-any":
		return s.opAwaitAny(req)
	case "renew":
		return s.opRenew(req)
	case "close":
		return s.opClose(req)
	case "stop":
		return s.opStop()
	default:
		return Response{Error: "unknown op: " + req.Op}
	}
}

func (s *State) opRegister(req Request) Response {
	name, _ := req.Args["name"].(string)
	pidF, _ := req.Args["pid"].(float64)
	pid := int(pidF)
	if name == "" {
		return Response{Error: "name required"}
	}
	pub, _, err := ensureKey(name)
	if err != nil {
		return Response{Error: "keygen failed: " + err.Error()}
	}
	pubHex := fmt.Sprintf("%x", pub)
	now := time.Now()
	s.mu.Lock()
	a, ok := s.agents[name]
	if !ok {
		a = &Agent{Name: name, StartedAt: now}
		s.agents[name] = a
	}
	a.PID = pid
	a.LastSeenAt = now
	a.ExpiresAt = now.Add(leaseTTL)
	a.Pubkey = pubHex
	snapshot := *a
	s.mu.Unlock()
	s.persistAgent(&snapshot)
	return Response{OK: true, Data: map[string]any{
		"name":       name,
		"expires_at": snapshot.ExpiresAt.Format(time.RFC3339),
		"pubkey":     pubHex,
	}}
}

func (s *State) opRenew(req Request) Response {
	from, _ := req.Args["from"].(string)
	if from == "" {
		return Response{Error: "from required"}
	}
	s.mu.Lock()
	a, ok := s.agents[from]
	if !ok {
		s.mu.Unlock()
		return Response{Error: "not registered: " + from, Code: CodeNotFound}
	}
	now := time.Now()
	a.LastSeenAt = now
	a.ExpiresAt = now.Add(leaseTTL)
	snapshot := *a
	s.mu.Unlock()
	s.persistAgent(&snapshot)
	return Response{OK: true, Data: map[string]any{
		"expires_at": snapshot.ExpiresAt.Format(time.RFC3339),
	}}
}

func (s *State) opSessions(req Request) Response {
	from, _ := req.Args["from"].(string)
	if from == "" {
		return Response{Error: "from required"}
	}
	s.mu.Lock()
	out := []any{}
	for _, t := range s.tunnels {
		if t.PeerA != from && t.PeerB != from {
			continue
		}
		t.mu.Lock()
		peer := t.PeerB
		if from == t.PeerB {
			peer = t.PeerA
		}
		pendingCount := len(t.mailbox[from])
		out = append(out, map[string]any{
			"sid":            t.SID,
			"peer":           peer,
			"turn":           t.turn,
			"your_turn":      t.turn == from,
			"closed":         t.closed,
			"pending_for_me": pendingCount,
			"msg_count":      t.seq,
		})
		t.mu.Unlock()
	}
	s.mu.Unlock()
	return Response{OK: true, Data: out}
}

func (s *State) opAwaitAny(req Request) Response {
	from, _ := req.Args["from"].(string)
	timeoutF, _ := req.Args["timeout"].(float64)
	timeout := int(timeoutF)
	if timeout <= 0 {
		timeout = 300
	}
	if from == "" {
		return Response{Error: "from required"}
	}
	// first pass: look for pending messages in any existing tunnel.
	s.mu.Lock()
	for _, t := range s.tunnels {
		if t.PeerA != from && t.PeerB != from {
			continue
		}
		t.mu.Lock()
		if q := t.mailbox[from]; len(q) > 0 {
			msg := q[0]
			t.mailbox[from] = q[1:]
			sid := t.SID
			t.mu.Unlock()
			s.mu.Unlock()
			return Response{OK: true, Data: map[string]any{
				"sid":  sid,
				"seq":  msg.Seq,
				"from": msg.From,
				"body": msg.Body,
				"ts":   msg.TS.Format(time.RFC3339),
			}}
		}
		t.mu.Unlock()
	}
	// no pending; register any-waiter.
	ch := make(chan anyMsg, 1)
	if prev, ok := s.anyWaiter[from]; ok {
		// another await-any already pending; displace with peer_closed-ish
		// but simpler: reject second concurrent await-any.
		_ = prev
		s.mu.Unlock()
		return Response{Error: "another await-any is already pending for " + from, Code: CodeError}
	}
	s.anyWaiter[from] = ch
	s.mu.Unlock()

	select {
	case am := <-ch:
		return Response{OK: true, Data: map[string]any{
			"sid":  am.sid,
			"seq":  am.msg.Seq,
			"from": am.msg.From,
			"body": am.msg.Body,
			"ts":   am.msg.TS.Format(time.RFC3339),
		}}
	case <-time.After(time.Duration(timeout) * time.Second):
		s.mu.Lock()
		delete(s.anyWaiter, from)
		s.mu.Unlock()
		return Response{Error: "timeout waiting for any tunnel", Code: CodeTimeout}
	}
}

func (s *State) opHistory(req Request) Response {
	from, _ := req.Args["from"].(string)
	sid, _ := req.Args["sid"].(string)
	sinceF, _ := req.Args["since"].(float64)
	limitF, _ := req.Args["limit"].(float64)
	since := int(sinceF)
	limit := int(limitF)
	if from == "" {
		return Response{Error: "from required"}
	}
	s.mu.Lock()
	t, ok := s.tunnels[sid]
	s.mu.Unlock()
	if !ok {
		// same error as "you are not a peer" so agents cannot enumerate sids they are not in.
		return Response{Error: "tunnel not found: " + sid, Code: CodeNotFound}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.PeerA != from && t.PeerB != from {
		return Response{Error: "tunnel not found: " + sid, Code: CodeNotFound}
	}
	out := make([]any, 0, len(t.log))
	for _, m := range t.log {
		if since > 0 && m.Seq <= since {
			continue
		}
		out = append(out, map[string]any{
			"seq":  m.Seq,
			"from": m.From,
			"ts":   m.TS.Format(time.RFC3339),
			"body": m.Body,
		})
	}
	if limit > 0 && len(out) > limit {
		out = out[len(out)-limit:]
	}
	return Response{OK: true, Data: map[string]any{
		"sid":      sid,
		"peer_a":   t.PeerA,
		"peer_b":   t.PeerB,
		"closed":   t.closed,
		"messages": out,
	}}
}

// deliverAny signals the any-waiter for `to` with (sid, msg) if one exists.
// Called by tunnel.send when it drops a message into `to`'s mailbox (no per-tunnel waiter).
// Returns true if an any-waiter consumed the message (caller should then NOT enqueue to mailbox).
func (s *State) deliverAny(to, sid string, msg Message) bool {
	s.mu.Lock()
	ch, ok := s.anyWaiter[to]
	if ok {
		delete(s.anyWaiter, to)
	}
	s.mu.Unlock()
	if !ok {
		return false
	}
	ch <- anyMsg{sid: sid, msg: msg}
	return true
}

func (s *State) opAgents() Response {
	s.mu.Lock()
	defer s.mu.Unlock()
	data := make([]any, 0, len(s.agents))
	for _, a := range s.agents {
		data = append(data, map[string]any{
			"name":         a.Name,
			"pid":          a.PID,
			"started_at":   a.StartedAt.Format(time.RFC3339),
			"last_seen_at": a.LastSeenAt.Format(time.RFC3339),
			"expires_at":   a.ExpiresAt.Format(time.RFC3339),
		})
	}
	return Response{OK: true, Data: data}
}

func (s *State) opTunnel(req Request) Response {
	from, _ := req.Args["from"].(string)
	peer, _ := req.Args["peer"].(string)
	if from == "" || peer == "" {
		return Response{Error: "from and peer required"}
	}
	if from == peer {
		return Response{Error: "cannot tunnel with self"}
	}
	s.mu.Lock()
	if _, ok := s.agents[from]; !ok {
		s.mu.Unlock()
		return Response{Error: "caller not registered: " + from, Code: CodeNotFound}
	}
	if _, ok := s.agents[peer]; !ok {
		s.mu.Unlock()
		return Response{Error: "peer not registered: " + peer, Code: CodeNotFound}
	}
	sid := newSID()
	t := newTunnel(sid, from, peer)
	s.tunnels[sid] = t
	s.mu.Unlock()

	openedAt := time.Now().Format(time.RFC3339)
	session := fmt.Sprintf("# tunnel %s\n\npeers: %s, %s\nopened_at: %s\n", sid, from, peer, openedAt)
	s.enqueueWrite("tunnels/"+sid+"/SESSION.md", []byte(session), fmt.Sprintf("tunnel open: %s (%s ↔ %s)", sid, from, peer))

	return Response{OK: true, Data: map[string]any{"sid": sid, "peer": peer, "turn": from}}
}

func (s *State) opSend(req Request) Response {
	from, _ := req.Args["from"].(string)
	sid, _ := req.Args["sid"].(string)
	body, _ := req.Args["body"].(string)
	timeoutF, _ := req.Args["timeout"].(float64)
	timeout := int(timeoutF)
	if timeout <= 0 {
		timeout = 300
	}
	s.mu.Lock()
	t, ok := s.tunnels[sid]
	s.mu.Unlock()
	if !ok {
		return Response{Error: "tunnel not found: " + sid, Code: CodeNotFound}
	}
	return t.send(s, from, body, time.Duration(timeout)*time.Second)
}

func (s *State) opAwait(req Request) Response {
	from, _ := req.Args["from"].(string)
	sid, _ := req.Args["sid"].(string)
	timeoutF, _ := req.Args["timeout"].(float64)
	timeout := int(timeoutF)
	if timeout <= 0 {
		timeout = 300
	}
	s.mu.Lock()
	t, ok := s.tunnels[sid]
	s.mu.Unlock()
	if !ok {
		return Response{Error: "tunnel not found: " + sid, Code: CodeNotFound}
	}
	return t.await(from, time.Duration(timeout)*time.Second)
}

func (s *State) opClose(req Request) Response {
	from, _ := req.Args["from"].(string)
	sid, _ := req.Args["sid"].(string)
	s.mu.Lock()
	t, ok := s.tunnels[sid]
	s.mu.Unlock()
	if !ok {
		return Response{Error: "tunnel not found: " + sid, Code: CodeNotFound}
	}
	reason := fmt.Sprintf("%s hung up", from)
	t.close(reason)
	closedAt := time.Now().Format(time.RFC3339)
	content := fmt.Sprintf("# tunnel %s\n\npeers: %s, %s\nclosed_at: %s\nclosed_by: %s\n", sid, t.PeerA, t.PeerB, closedAt, from)
	s.enqueueWrite("tunnels/"+sid+"/CLOSED.md", []byte(content), fmt.Sprintf("tunnel close: %s (by %s)", sid, from))
	return Response{OK: true}
}

func (s *State) opStop() Response {
	go func() {
		time.Sleep(50 * time.Millisecond)
		s.requestStop()
		// trigger accept loop exit by closing socket is done by the signal handler;
		// here we self-signal via the writer shutdown and the accept loop's next iteration after close.
	}()
	return Response{OK: true}
}
