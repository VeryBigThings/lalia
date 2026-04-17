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
const leaseTTL = 30 * time.Minute
const sweepInterval = 30 * time.Second

type State struct {
	mu       sync.Mutex
	agents   map[string]*Agent
	channels map[string]*Channel
	rooms    map[string]*Room

	// state-level any-waiters: one per agent, 1-buffered
	anyWaiter map[string]chan anyMsg

	writes chan writeOp
	queue  *Queue
	wg     sync.WaitGroup
	stop   chan struct{}
}

type anyMsg struct {
	kind   string // "peer" or "room"
	target string // peer name (for kind=peer) or room name (for kind=room)
	msg    Message
}

func newState() (*State, error) {
	s := &State{
		agents:    make(map[string]*Agent),
		channels:  make(map[string]*Channel),
		rooms:     make(map[string]*Room),
		anyWaiter: make(map[string]chan anyMsg),
		writes:    make(chan writeOp, 128),
		stop:      make(chan struct{}),
	}
	if err := ensureWorkspace(); err != nil {
		return nil, err
	}
	q, err := openQueue(leschDir())
	if err != nil {
		return nil, fmt.Errorf("write queue: %w", err)
	}
	s.queue = q
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

// startSweeper runs a goroutine that drops expired agents and releases
// their in-flight waiters across channels and rooms.
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
	channels := make([]*Channel, 0, len(s.channels))
	for _, c := range s.channels {
		channels = append(channels, c)
	}
	rooms := make([]*Room, 0, len(s.rooms))
	for _, r := range s.rooms {
		rooms = append(rooms, r)
	}
	for _, name := range expired {
		delete(s.agents, name)
	}
	s.mu.Unlock()

	expiredSet := make(map[string]struct{}, len(expired))
	for _, name := range expired {
		expiredSet[name] = struct{}{}
	}
	for _, c := range channels {
		for name := range expiredSet {
			if c.PeerA == name || c.PeerB == name {
				c.releaseWaiter(name, "lease expired")
			}
		}
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
	// release all channel and room waiters so blocked reads return.
	s.mu.Lock()
	channels := make([]*Channel, 0, len(s.channels))
	for _, c := range s.channels {
		channels = append(channels, c)
	}
	s.mu.Unlock()
	for _, c := range channels {
		c.mu.Lock()
		for name, ch := range c.waiter {
			ch <- waitResult{err: "daemon shutting down", code: CodePeerClosed}
			delete(c.waiter, name)
		}
		c.mu.Unlock()
	}
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
	case "unregister":
		return s.opUnregister(req)
	case "agents":
		return s.opAgents()
	case "rooms":
		return s.opRooms(req)
	case "room_create":
		return s.opRoomCreate(req)
	case "join":
		return s.opJoin(req)
	case "leave":
		return s.opLeave(req)
	case "participants":
		return s.opParticipants(req)
	case "post":
		return s.opPost(req)
	case "tell":
		return s.opTell(req)
	case "read":
		return s.opRead(req)
	case "peek":
		return s.opPeek(req)
	case "read-any":
		return s.opReadAny(req)
	case "history":
		return s.opHistory(req)
	case "channels":
		return s.opChannels(req)
	case "renew":
		return s.opRenew(req)
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

// opUnregister drops the caller from the agent registry, releases their
// in-flight channel waiters, evicts them from every room, removes
// their registry file, and deletes their private key on disk. A later
// register under the same name generates a fresh keypair and a new
// pubkey — existing signatures do not survive. Request is signed and
// authenticated through the dispatch pre-switch.
func (s *State) opUnregister(req Request) Response {
	from, _ := req.Args["from"].(string)
	if from == "" {
		return Response{Error: "from required"}
	}
	s.mu.Lock()
	if _, ok := s.agents[from]; !ok {
		s.mu.Unlock()
		return Response{Error: "not registered: " + from, Code: CodeNotFound}
	}
	delete(s.agents, from)
	delete(s.anyWaiter, from)
	channels := make([]*Channel, 0, len(s.channels))
	for _, c := range s.channels {
		if c.PeerA == from || c.PeerB == from {
			channels = append(channels, c)
		}
	}
	rooms := make([]*Room, 0, len(s.rooms))
	for _, r := range s.rooms {
		rooms = append(rooms, r)
	}
	s.mu.Unlock()

	for _, c := range channels {
		c.releaseWaiter(from, "unregistered")
	}
	evict := map[string]struct{}{from: {}}
	for _, r := range rooms {
		if r.removeMembers(evict) {
			s.persistRoomMembers(r)
		}
	}
	s.removeAgentFile(from)
	_ = removeKey(from)

	return Response{OK: true, Data: map[string]any{"name": from}}
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

// opChannels lists the caller's peer-pair channels.
func (s *State) opChannels(req Request) Response {
	from, _ := req.Args["from"].(string)
	if from == "" {
		return Response{Error: "from required"}
	}
	out := []any{}
	for _, c := range s.listChannels(from) {
		c.mu.Lock()
		peer := other(c, from)
		pendingCount := len(c.mailbox[from])
		out = append(out, map[string]any{
			"peer":           peer,
			"pending_for_me": pendingCount,
			"msg_count":      c.seq,
		})
		c.mu.Unlock()
	}
	return Response{OK: true, Data: out}
}

// opTell sends a message to a peer. Async — returns immediately.
func (s *State) opTell(req Request) Response {
	from, _ := req.Args["from"].(string)
	peer, _ := req.Args["peer"].(string)
	body, _ := req.Args["body"].(string)
	if from == "" || peer == "" {
		return Response{Error: "from and peer required"}
	}
	if from == peer {
		return Response{Error: "cannot tell self"}
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
	s.mu.Unlock()
	ch := s.getOrCreateChannel(from, peer)
	return ch.tell(s, from, body)
}

// opRead consumes the next message from the caller's mailbox on the named
// peer or room. Disambiguates by looking at the `peer` or `room` arg.
func (s *State) opRead(req Request) Response {
	from, _ := req.Args["from"].(string)
	peer, _ := req.Args["peer"].(string)
	room, _ := req.Args["room"].(string)
	timeoutF, _ := req.Args["timeout"].(float64)
	timeout := int(timeoutF)
	if from == "" {
		return Response{Error: "from required"}
	}
	if peer == "" && room == "" {
		return Response{Error: "peer or room required"}
	}
	if peer != "" && room != "" {
		return Response{Error: "specify peer or room, not both"}
	}
	if peer != "" {
		s.mu.Lock()
		if _, ok := s.agents[peer]; !ok {
			s.mu.Unlock()
			return Response{Error: "peer not registered: " + peer, Code: CodeNotFound}
		}
		s.mu.Unlock()
		ch := s.getOrCreateChannel(from, peer)
		return ch.read(from, time.Duration(timeout)*time.Second)
	}
	return s.roomRead(from, room, time.Duration(timeout)*time.Second)
}

// opPeek returns pending messages without consuming. Works for peers and rooms.
func (s *State) opPeek(req Request) Response {
	from, _ := req.Args["from"].(string)
	peer, _ := req.Args["peer"].(string)
	room, _ := req.Args["room"].(string)
	if from == "" {
		return Response{Error: "from required"}
	}
	if peer == "" && room == "" {
		return Response{Error: "peer or room required"}
	}
	if peer != "" && room != "" {
		return Response{Error: "specify peer or room, not both"}
	}
	if peer != "" {
		s.mu.Lock()
		if _, ok := s.agents[peer]; !ok {
			s.mu.Unlock()
			return Response{Error: "peer not registered: " + peer, Code: CodeNotFound}
		}
		_, hasChannel := s.channels[channelKey(from, peer)]
		s.mu.Unlock()
		if !hasChannel {
			return Response{OK: true, Data: map[string]any{"peer": peer, "messages": []any{}}}
		}
		ch := s.getOrCreateChannel(from, peer)
		return ch.peek(from)
	}
	return s.roomPeek(from, room)
}

// opReadAny blocks until any peer-channel or room mailbox delivers a
// message to the caller. Returns the first inbound along with its source
// ({kind: "peer"|"room", target: name}).
func (s *State) opReadAny(req Request) Response {
	from, _ := req.Args["from"].(string)
	timeoutF, _ := req.Args["timeout"].(float64)
	timeout := int(timeoutF)
	if timeout <= 0 {
		timeout = 300
	}
	if from == "" {
		return Response{Error: "from required"}
	}
	// First pass: drain any pending messages from channels or rooms.
	s.mu.Lock()
	for _, c := range s.channels {
		if c.PeerA != from && c.PeerB != from {
			continue
		}
		c.mu.Lock()
		if q := c.mailbox[from]; len(q) > 0 {
			msg := q[0]
			c.mailbox[from] = q[1:]
			peer := other(c, from)
			c.mu.Unlock()
			s.mu.Unlock()
			return anyResponse("peer", peer, msg)
		}
		c.mu.Unlock()
	}
	for _, r := range s.rooms {
		r.mu.Lock()
		if !r.members[from] {
			r.mu.Unlock()
			continue
		}
		if q := r.mailbox[from]; len(q) > 0 {
			msg := q[0]
			r.mailbox[from] = q[1:]
			roomName := r.Name
			r.mu.Unlock()
			s.mu.Unlock()
			return anyResponse("room", roomName, toPeerMsg(msg))
		}
		r.mu.Unlock()
	}
	if _, ok := s.anyWaiter[from]; ok {
		s.mu.Unlock()
		return Response{Error: "another read-any is already pending for " + from, Code: CodeError}
	}
	ch := make(chan anyMsg, 1)
	s.anyWaiter[from] = ch
	s.mu.Unlock()

	select {
	case am := <-ch:
		return anyResponse(am.kind, am.target, am.msg)
	case <-time.After(time.Duration(timeout) * time.Second):
		s.mu.Lock()
		delete(s.anyWaiter, from)
		s.mu.Unlock()
		return Response{Error: "timeout waiting for any channel", Code: CodeTimeout}
	}
}

func anyResponse(kind, target string, msg Message) Response {
	return Response{OK: true, Data: map[string]any{
		"kind":   kind,
		"target": target,
		"seq":    msg.Seq,
		"from":   msg.From,
		"body":   msg.Body,
		"ts":     msg.TS.Format(time.RFC3339),
	}}
}

// opHistory returns the transcript for a peer-pair channel or a room.
func (s *State) opHistory(req Request) Response {
	from, _ := req.Args["from"].(string)
	peer, _ := req.Args["peer"].(string)
	room, _ := req.Args["room"].(string)
	sinceF, _ := req.Args["since"].(float64)
	limitF, _ := req.Args["limit"].(float64)
	since := int(sinceF)
	limit := int(limitF)
	if from == "" {
		return Response{Error: "from required"}
	}
	if peer == "" && room == "" {
		return Response{Error: "peer or room required"}
	}
	if peer != "" && room != "" {
		return Response{Error: "specify peer or room, not both"}
	}
	if peer != "" {
		s.mu.Lock()
		c, ok := s.channels[channelKey(from, peer)]
		s.mu.Unlock()
		if !ok {
			return Response{Error: "channel not found: " + peer, Code: CodeNotFound}
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.PeerA != from && c.PeerB != from {
			return Response{Error: "channel not found: " + peer, Code: CodeNotFound}
		}
		out := sliceHistory(c.log, since, limit)
		return Response{OK: true, Data: map[string]any{
			"peer":     peer,
			"messages": out,
		}}
	}
	return s.roomHistory(from, room, since, limit)
}

func sliceHistory(log []Message, since, limit int) []any {
	out := make([]any, 0, len(log))
	for _, m := range log {
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
	return out
}

// deliverAny signals the any-waiter for `to` with (kind, target, msg) if one
// exists. Returns true if the waiter consumed the message; caller should
// then NOT enqueue to the mailbox.
func (s *State) deliverAny(to, kind, target string, msg Message) bool {
	s.mu.Lock()
	ch, ok := s.anyWaiter[to]
	if ok {
		delete(s.anyWaiter, to)
	}
	s.mu.Unlock()
	if !ok {
		return false
	}
	ch <- anyMsg{kind: kind, target: target, msg: msg}
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

func (s *State) opStop() Response {
	go func() {
		time.Sleep(50 * time.Millisecond)
		s.requestStop()
	}()
	return Response{OK: true}
}
