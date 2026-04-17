package main

import (
	"fmt"
	"sync"
	"time"
)

type Agent struct {
	Name      string    `json:"name"`
	PID       int       `json:"pid"`
	StartedAt time.Time `json:"started_at"`
}

type State struct {
	mu      sync.Mutex
	agents  map[string]*Agent
	tunnels map[string]*Tunnel

	writes chan writeOp
	wg     sync.WaitGroup
	stop   chan struct{}
}

func newState() (*State, error) {
	s := &State{
		agents:  make(map[string]*Agent),
		tunnels: make(map[string]*Tunnel),
		writes:  make(chan writeOp, 128),
		stop:    make(chan struct{}),
	}
	if err := ensureWorkspace(); err != nil {
		return nil, err
	}
	return s, nil
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
	switch req.Op {
	case "register":
		return s.opRegister(req)
	case "agents":
		return s.opAgents()
	case "tunnel":
		return s.opTunnel(req)
	case "send":
		return s.opSend(req)
	case "await":
		return s.opAwait(req)
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
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.agents[name]; ok {
		if existing.PID == pid {
			return Response{OK: true, Data: map[string]any{"name": name}}
		}
		existing.PID = pid
		existing.StartedAt = time.Now()
		return Response{OK: true, Data: map[string]any{"name": name}}
	}
	s.agents[name] = &Agent{Name: name, PID: pid, StartedAt: time.Now()}
	return Response{OK: true, Data: map[string]any{"name": name}}
}

func (s *State) opAgents() Response {
	s.mu.Lock()
	defer s.mu.Unlock()
	data := make([]any, 0, len(s.agents))
	for _, a := range s.agents {
		data = append(data, map[string]any{
			"name":       a.Name,
			"pid":        a.PID,
			"started_at": a.StartedAt.Format(time.RFC3339),
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
