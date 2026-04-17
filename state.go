package main

import (
	"fmt"
	"sync"
	"time"
)

type Agent struct {
	AgentID    string    `json:"agent_id"`           // ULID, stable for the life of the keypair
	Name       string    `json:"name"`               // display name, not unique
	Pubkey     string    `json:"pubkey"`             // hex-encoded Ed25519 public key
	Role       string    `json:"role,omitempty"`     // "supervisor" | "worker" | ""
	Harness    string    `json:"harness,omitempty"`  // claude-code | codex | cursor | …
	Model      string    `json:"model,omitempty"`    // e.g. claude-opus-4-7
	Project    string    `json:"project,omitempty"`  // resolved from git remote or cwd
	RepoURL    string    `json:"repo_url,omitempty"` // full remote URL when available
	Worktree   string    `json:"worktree,omitempty"` // basename of cwd
	Branch     string    `json:"branch,omitempty"`   // git rev-parse --abbrev-ref HEAD
	CWD        string    `json:"cwd,omitempty"`      // full path the agent is running from
	PID        int       `json:"pid"`
	StartedAt  time.Time `json:"started_at"`
	LastSeenAt time.Time `json:"last_seen_at"`
	ExpiresAt  time.Time `json:"expires_at"`
}

// Lease duration. Any command from the agent renews; sweeper drops expired.
const leaseTTL = 60 * time.Minute
const sweepInterval = 30 * time.Second

type State struct {
	mu       sync.Mutex
	agents   map[string]*Agent // keyed by agent_id (ULID)
	nameIdx  map[string]string // name → agent_id (multiple IDs per name possible)
	channels map[string]*Channel
	rooms    map[string]*Room
	plans    map[string]*Plan // keyed by project_id

	// state-level any-waiters: one per agent_id, 1-buffered
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
		nameIdx:   make(map[string]string),
		channels:  make(map[string]*Channel),
		rooms:     make(map[string]*Room),
		plans:     make(map[string]*Plan),
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
	if err := s.loadPlans(); err != nil {
		return nil, err
	}
	if err := s.loadRooms(); err != nil {
		return nil, err
	}
	if err := s.replayMailbox(); err != nil {
		return nil, err
	}
	return s, nil
}

// renewLease extends the lease on an agent and updates last-seen.
// Called on every request that carries a "from" identity (name).
// Writes the updated record back to the workspace asynchronously.
func (s *State) renewLease(name string) {
	if name == "" {
		return
	}
	s.mu.Lock()
	a := s.agentByName(name)
	if a == nil {
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

// agentByName looks up an agent by display name via the nameIdx.
// Must be called with s.mu held. Returns nil if not found.
func (s *State) agentByName(name string) *Agent {
	id, ok := s.nameIdx[name]
	if !ok {
		return nil
	}
	return s.agents[id]
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
	var expiredIDs []string
	var expiredNames []string
	for id, a := range s.agents {
		if now.After(a.ExpiresAt) {
			expiredIDs = append(expiredIDs, id)
			expiredNames = append(expiredNames, a.Name)
		}
	}
	if len(expiredIDs) == 0 {
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
	for _, id := range expiredIDs {
		s.unindexAgent(id)
	}
	s.mu.Unlock()

	expiredNameSet := make(map[string]struct{}, len(expiredNames))
	for _, name := range expiredNames {
		expiredNameSet[name] = struct{}{}
	}
	for _, c := range channels {
		for name := range expiredNameSet {
			if c.PeerA == name || c.PeerB == name {
				c.releaseWaiter(name, "lease expired")
			}
		}
	}
	for _, r := range rooms {
		if r.removeMembers(expiredNameSet) {
			s.persistRoomMembers(r)
		}
	}
	for _, id := range expiredIDs {
		s.removeAgentFile(id)
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

// replayMailbox replays unread mailbox rows from the SQLite mailbox table
// into the in-memory channel and room mailboxes. Called synchronously in
// newState() before the daemon starts accepting connections, so no locking
// contest is possible.
func (s *State) replayMailbox() error {
	rows, err := s.queue.mailboxRows()
	if err != nil {
		return fmt.Errorf("replay mailbox rows: %w", err)
	}
	for _, row := range rows {
		ts, err := time.Parse(time.RFC3339, row.ts)
		if err != nil {
			ts = time.Now()
		}
		switch row.kind {
		case "peer":
			ch := s.getOrCreateChannel(row.recipient, row.target)
			ch.mu.Lock()
			ch.mailbox[row.recipient] = append(ch.mailbox[row.recipient], Message{
				Seq:  row.seq,
				From: row.fromName,
				TS:   ts,
				Body: row.body,
			})
			if row.seq > ch.seq {
				ch.seq = row.seq
			}
			ch.mu.Unlock()
		case "room":
			s.mu.Lock()
			r, ok := s.rooms[row.target]
			s.mu.Unlock()
			if !ok {
				continue
			}
			r.mu.Lock()
			r.mailbox[row.recipient] = append(r.mailbox[row.recipient], RoomMessage{
				Seq:  row.seq,
				Room: row.target,
				From: row.fromName,
				TS:   ts,
				Body: row.body,
			})
			if row.seq > r.seq {
				r.seq = row.seq
			}
			r.mu.Unlock()
		}
	}

	droppedRows, err := s.queue.mailboxDropped()
	if err != nil {
		return fmt.Errorf("replay dropped counters: %w", err)
	}
	for _, row := range droppedRows {
		if row.kind == "room" {
			s.mu.Lock()
			r, ok := s.rooms[row.target]
			s.mu.Unlock()
			if !ok {
				continue
			}
			r.mu.Lock()
			r.dropped[row.recipient] = row.dropped
			r.mu.Unlock()
		}
	}
	return nil
}

func (s *State) dispatch(req Request) Response {
	// register is unauthenticated by design — first-to-claim binds the pubkey
	// for that name via ensureKey (which reuses an existing key if present).
	// every other op that carries `from` must be signed with that pubkey.
	if req.Op != "register" && req.Op != "stop" && req.Op != "agents" && req.Op != "resolve" {
		if from, ok := req.Args["from"].(string); ok && from != "" {
			s.mu.Lock()
			a := s.agentByName(from)
			var pubHex string
			if a != nil {
				pubHex = a.Pubkey
			}
			s.mu.Unlock()
			if a == nil {
				return errorResponse(CodeUnauthorized, "not_registered", "run kopos register for this identity", "not registered: "+from, map[string]any{"from": from})
			}
			if pubHex == "" {
				return errorResponse(CodeUnauthorized, "missing_pubkey", "run kopos register to generate a keypair", "agent "+from+" has no pubkey on file; re-register to acquire one", map[string]any{"from": from})
			}
			if err := verifyRequest(pubHex, req.Args); err != nil {
				return errorResponse(CodeUnauthorized, "signature_rejected", "re-register if your key changed", "signature rejected: "+err.Error(), map[string]any{"from": from})
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
	case "resolve":
		return s.opResolve(req)
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
	case "plan_create":
		return s.opPlanCreate(req)
	case "plan_assign":
		return s.opPlanAssign(req)
	case "plan_unassign":
		return s.opPlanUnassign(req)
	case "plan_status":
		return s.opPlanStatus(req)
	case "plan_claim":
		return s.opPlanClaim(req)
	case "plan_show":
		return s.opPlanShow(req)
	case "plan_list":
		return s.opPlanList(req)
	case "plan_handoff":
		return s.opPlanHandoff(req)
	case "stop":
		return s.opStop()
	default:
		return errorResponse(CodeError, "unknown_op", "check `kopos --help` for supported commands", "unknown op: "+req.Op, map[string]any{"op": req.Op})
	}
}

func (s *State) opRegister(req Request) Response {
	name, _ := req.Args["name"].(string)
	pidF, _ := req.Args["pid"].(float64)
	pid := int(pidF)
	if name == "" {
		return errorResponse(CodeError, "missing_name", "pass --name or set KOPOS_NAME", "name required", nil)
	}
	pub, _, err := ensureKey(name)
	if err != nil {
		return errorResponse(CodeError, "keygen_failed", "check key backend permissions and retry", "keygen failed: "+err.Error(), map[string]any{"name": name})
	}
	pubHex := fmt.Sprintf("%x", pub)

	// Collect optional metadata sent by the client
	info := AgentInfo{
		Harness:  strVal(req.Args, "harness"),
		Model:    strVal(req.Args, "model"),
		Project:  strVal(req.Args, "project"),
		RepoURL:  strVal(req.Args, "repo_url"),
		Worktree: strVal(req.Args, "worktree"),
		Branch:   strVal(req.Args, "branch"),
		CWD:      strVal(req.Args, "cwd"),
	}

	now := time.Now()
	s.mu.Lock()
	// Check if this name is already registered; reuse same AgentID if so.
	a := s.agentByName(name)
	if a == nil {
		a = &Agent{
			AgentID:   newAgentID(),
			Name:      name,
			StartedAt: now,
		}
	}
	a.PID = pid
	a.Pubkey = pubHex
	a.LastSeenAt = now
	a.ExpiresAt = now.Add(leaseTTL)
	// Apply detected metadata (non-empty fields override)
	if info.Harness != "" {
		a.Harness = info.Harness
	}
	if info.Model != "" {
		a.Model = info.Model
	}
	if info.Project != "" {
		a.Project = info.Project
	}
	if info.RepoURL != "" {
		a.RepoURL = info.RepoURL
	}
	if info.Worktree != "" {
		a.Worktree = info.Worktree
	}
	if info.Branch != "" {
		a.Branch = info.Branch
	}
	if info.CWD != "" {
		a.CWD = info.CWD
	}
	if role := strVal(req.Args, "role"); role != "" {
		a.Role = role
	}
	s.indexAgent(a)
	snapshot := *a
	s.mu.Unlock()
	s.persistAgent(&snapshot)
	s.deliverKickoffs(name)
	return Response{OK: true, Data: map[string]any{
		"name":       name,
		"agent_id":   snapshot.AgentID,
		"expires_at": snapshot.ExpiresAt.Format(time.RFC3339),
		"pubkey":     pubHex,
		"role":       snapshot.Role,
	}}
}

func strVal(args map[string]any, key string) string {
	v, _ := args[key].(string)
	return v
}

func (s *State) opUnregister(req Request) Response {
	from, _ := req.Args["from"].(string)
	if from == "" {
		return errorResponse(CodeError, "missing_from", "set KOPOS_NAME or pass --as", "from required", nil)
	}
	s.mu.Lock()
	a := s.agentByName(from)
	if a == nil {
		s.mu.Unlock()
		return errorResponse(CodeNotFound, "not_registered", "run kopos register before unregister", "not registered: "+from, map[string]any{"from": from})
	}
	// Reject unregister if this agent is a supervisor with active (non-empty) plan assignments.
	for pid, plan := range s.plans {
		if plan.Supervisor != from {
			continue
		}
		for _, asgn := range plan.Assignments {
			if asgn.Status != statusMerged {
				s.mu.Unlock()
				return errorResponse(CodeSupervisorBusy, "supervisor_busy", "run kopos plan handoff <agent> first to transfer ownership", "supervisor still owns active assignments in project "+pid, map[string]any{"project": pid, "slug": asgn.Slug})
			}
		}
	}
	agentID := a.AgentID
	s.unindexAgent(agentID)
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
	s.removeAgentFile(agentID)
	_ = removeKey(from)

	return Response{OK: true, Data: map[string]any{"name": from}}
}

func (s *State) opRenew(req Request) Response {
	from, _ := req.Args["from"].(string)
	if from == "" {
		return errorResponse(CodeError, "missing_from", "set KOPOS_NAME or pass --as", "from required", nil)
	}
	s.mu.Lock()
	a := s.agentByName(from)
	if a == nil {
		s.mu.Unlock()
		return errorResponse(CodeNotFound, "not_registered", "run kopos register before renew", "not registered: "+from, map[string]any{"from": from})
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
		return errorResponse(CodeError, "missing_from", "set KOPOS_NAME or pass --as", "from required", nil)
	}
	s.mu.Lock()
	if s.agentByName(from) == nil {
		s.mu.Unlock()
		return errorResponse(CodeNotFound, "not_registered", "run kopos register before listing channels", "not registered: "+from, map[string]any{"from": from})
	}
	s.mu.Unlock()
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
		return errorResponse(CodeError, "missing_params", "provide both from and peer", "from and peer required", map[string]any{"from": from, "peer": peer})
	}
	if from == peer {
		return errorResponse(CodeError, "self_target", "choose a different peer", "cannot tell self", map[string]any{"from": from})
	}
	s.mu.Lock()
	if s.agentByName(from) == nil {
		s.mu.Unlock()
		return errorResponse(CodeNotFound, "caller_not_registered", "run kopos register before sending", "caller not registered: "+from, map[string]any{"from": from})
	}
	// Resolve peer address to a name
	peerName, err := s.resolvePeerName(peer)
	if err != nil {
		s.mu.Unlock()
		return errorResponse(CodeNotFound, "peer_not_registered", "check `kopos agents` for active peers", err.Error(), map[string]any{"peer": peer})
	}
	s.mu.Unlock()
	ch := s.getOrCreateChannel(from, peerName)
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
		return errorResponse(CodeError, "missing_from", "set KOPOS_NAME or pass --as", "from required", nil)
	}
	if peer == "" && room == "" {
		return errorResponse(CodeError, "missing_target", "specify peer or room", "peer or room required", nil)
	}
	if peer != "" && room != "" {
		return errorResponse(CodeError, "ambiguous_target", "specify exactly one of peer or room", "specify peer or room, not both", map[string]any{"peer": peer, "room": room})
	}
	if peer != "" {
		s.mu.Lock()
		peerName, err := s.resolvePeerName(peer)
		if err != nil {
			s.mu.Unlock()
			return errorResponse(CodeNotFound, "peer_not_registered", "check `kopos agents` for active peers", err.Error(), map[string]any{"peer": peer})
		}
		s.mu.Unlock()
		ch := s.getOrCreateChannel(from, peerName)
		return ch.read(s, from, time.Duration(timeout)*time.Second)
	}
	return s.roomRead(from, room, time.Duration(timeout)*time.Second)
}

// opPeek returns pending messages without consuming. Works for peers and rooms.
func (s *State) opPeek(req Request) Response {
	from, _ := req.Args["from"].(string)
	peer, _ := req.Args["peer"].(string)
	room, _ := req.Args["room"].(string)
	if from == "" {
		return errorResponse(CodeError, "missing_from", "set KOPOS_NAME or pass --as", "from required", nil)
	}
	if peer == "" && room == "" {
		return errorResponse(CodeError, "missing_target", "specify peer or room", "peer or room required", nil)
	}
	if peer != "" && room != "" {
		return errorResponse(CodeError, "ambiguous_target", "specify exactly one of peer or room", "specify peer or room, not both", map[string]any{"peer": peer, "room": room})
	}
	if peer != "" {
		s.mu.Lock()
		peerName, err := s.resolvePeerName(peer)
		if err != nil {
			s.mu.Unlock()
			return errorResponse(CodeNotFound, "peer_not_registered", "check `kopos agents` for active peers", err.Error(), map[string]any{"peer": peer})
		}
		_, hasChannel := s.channels[channelKey(from, peerName)]
		s.mu.Unlock()
		if !hasChannel {
			return Response{OK: true, Data: map[string]any{"peer": peerName, "messages": []any{}}}
		}
		ch := s.getOrCreateChannel(from, peerName)
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
		return errorResponse(CodeError, "missing_from", "set KOPOS_NAME or pass --as", "from required", nil)
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
			if s.queue != nil {
				_ = s.queue.mailboxDeleteOne(from, "peer", peer, msg.Seq)
			}
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
			if s.queue != nil {
				_ = s.queue.mailboxDeleteOne(from, "room", roomName, msg.Seq)
			}
			r.mu.Unlock()
			s.mu.Unlock()
			return anyResponse("room", roomName, toPeerMsg(msg))
		}
		r.mu.Unlock()
	}
	if _, ok := s.anyWaiter[from]; ok {
		s.mu.Unlock()
		return errorResponse(CodeError, "read_any_already_pending", "wait for the existing read-any call to finish", "another read-any is already pending for "+from, map[string]any{"from": from})
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
		return errorResponse(CodeTimeout, "timeout", "retry with a longer --timeout", "timeout waiting for any channel", map[string]any{"from": from, "timeout_seconds": timeout})
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
		return errorResponse(CodeError, "missing_from", "set KOPOS_NAME or pass --as", "from required", nil)
	}
	if peer == "" && room == "" {
		return errorResponse(CodeError, "missing_target", "specify peer or room", "peer or room required", nil)
	}
	if peer != "" && room != "" {
		return errorResponse(CodeError, "ambiguous_target", "specify exactly one of peer or room", "specify peer or room, not both", map[string]any{"peer": peer, "room": room})
	}
	if peer != "" {
		s.mu.Lock()
		c, ok := s.channels[channelKey(from, peer)]
		s.mu.Unlock()
		if !ok {
			return errorResponse(CodeNotFound, "channel_not_found", "send a message first to create a channel", "channel not found: "+peer, map[string]any{"peer": peer})
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.PeerA != from && c.PeerB != from {
			return errorResponse(CodeNotFound, "channel_not_found", "request history only for your own channels", "channel not found: "+peer, map[string]any{"from": from, "peer": peer})
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
			"agent_id":     a.AgentID,
			"name":         a.Name,
			"qualified":    qualifiedName(a),
			"harness":      a.Harness,
			"pid":          a.PID,
			"started_at":   a.StartedAt.Format(time.RFC3339),
			"last_seen_at": a.LastSeenAt.Format(time.RFC3339),
			"expires_at":   a.ExpiresAt.Format(time.RFC3339),
		})
	}
	return Response{OK: true, Data: data}
}

// opResolve resolves an address string to an agent_id. Used by the
// nickname command for stable binding.
func (s *State) opResolve(req Request) Response {
	address, _ := req.Args["address"].(string)
	if address == "" {
		return errorResponse(CodeError, "missing_address", "pass an address to resolve", "address required", nil)
	}
	nicknames, _ := loadNicknames()
	s.mu.Lock()
	agentID, err := s.ResolveAddress(address, nicknames)
	s.mu.Unlock()
	if err != nil {
		return errorResponse(CodeNotFound, "address_not_found", "check `kopos agents` or nicknames", err.Error(), map[string]any{"address": address})
	}
	return Response{OK: true, Data: map[string]any{"agent_id": agentID}}
}

// resolvePeerName resolves an address to a registered agent's display name.
// Must be called with s.mu held. Returns the name for use in channel keys.
func (s *State) resolvePeerName(addr string) (string, error) {
	nicknames, _ := loadNicknames()
	agentID, err := s.ResolveAddress(addr, nicknames)
	if err != nil {
		return "", err
	}
	a, ok := s.agents[agentID]
	if !ok {
		return "", fmt.Errorf("agent not found: %s", addr)
	}
	return a.Name, nil
}

func (s *State) opStop() Response {
	go func() {
		time.Sleep(50 * time.Millisecond)
		s.requestStop()
	}()
	return Response{OK: true}
}
