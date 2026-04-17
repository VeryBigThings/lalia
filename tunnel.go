package main

import (
	"fmt"
	"sync"
	"time"
)

type Tunnel struct {
	SID    string
	PeerA  string
	PeerB  string
	turn   string
	seq    int
	closed bool
	hangup string

	// full ordered history; source of truth for reads via history.
	log []Message

	mailbox map[string][]Message
	waiter  map[string]chan waitResult

	mu sync.Mutex
}

type waitResult struct {
	msg *Message
	err string
	code int
}

func newTunnel(sid, a, b string) *Tunnel {
	return &Tunnel{
		SID:     sid,
		PeerA:   a,
		PeerB:   b,
		turn:    a,
		mailbox: make(map[string][]Message),
		waiter:  make(map[string]chan waitResult),
	}
}

func (t *Tunnel) peerOf(name string) (string, bool) {
	switch name {
	case t.PeerA:
		return t.PeerB, true
	case t.PeerB:
		return t.PeerA, true
	default:
		return "", false
	}
}

func (t *Tunnel) send(s *State, from, body string, timeout time.Duration) Response {
	t.mu.Lock()
	peer, ok := t.peerOf(from)
	if !ok {
		t.mu.Unlock()
		return Response{Error: "not a participant of this tunnel", Code: CodeError}
	}
	if t.closed {
		t.mu.Unlock()
		msg := t.hangup
		if msg == "" {
			msg = "peer hung up"
		}
		return Response{Error: msg, Code: CodePeerClosed}
	}
	if t.turn != from {
		t.mu.Unlock()
		return Response{Error: fmt.Sprintf("not your turn — waiting for %s to reply", t.turn), Code: CodeNotYourTurn}
	}
	t.seq++
	msg := Message{
		Seq:  t.seq,
		From: from,
		SID:  t.SID,
		TS:   time.Now(),
		Body: body,
	}
	t.log = append(t.log, msg)
	t.turn = peer

	// persist
	s.enqueueWrite(
		fmt.Sprintf("tunnels/%s/%03d-%s.md", t.SID, msg.Seq, from),
		renderMsg(msg),
		fmt.Sprintf("msg %d in %s from %s", msg.Seq, t.SID, from),
	)

	// deliver to peer: per-tunnel waiter wins over any-waiter; else any-waiter; else buffer.
	if ch, ok := t.waiter[peer]; ok {
		ch <- waitResult{msg: &msg}
		delete(t.waiter, peer)
	} else if s.deliverAny(peer, t.SID, msg) {
		// delivered to an await-any caller; nothing else to do.
	} else {
		t.mailbox[peer] = append(t.mailbox[peer], msg)
	}

	// now register our own waiter for the reply
	ch := make(chan waitResult, 1)
	t.waiter[from] = ch
	t.mu.Unlock()

	select {
	case r := <-ch:
		if r.err != "" {
			return Response{Error: r.err, Code: r.code}
		}
		return Response{OK: true, Data: map[string]any{
			"seq": r.msg.Seq, "from": r.msg.From, "body": r.msg.Body, "ts": r.msg.TS.Format(time.RFC3339),
		}}
	case <-time.After(timeout):
		t.mu.Lock()
		delete(t.waiter, from)
		t.mu.Unlock()
		return Response{Error: fmt.Sprintf("timeout after %s waiting for %s — call await or send again to resume", timeout, peer), Code: CodeTimeout}
	}
}

func (t *Tunnel) await(from string, timeout time.Duration) Response {
	t.mu.Lock()
	_, ok := t.peerOf(from)
	if !ok {
		t.mu.Unlock()
		return Response{Error: "not a participant of this tunnel", Code: CodeError}
	}
	// drain mailbox first
	if q, ok := t.mailbox[from]; ok && len(q) > 0 {
		msg := q[0]
		t.mailbox[from] = q[1:]
		t.mu.Unlock()
		return Response{OK: true, Data: map[string]any{
			"seq": msg.Seq, "from": msg.From, "body": msg.Body, "ts": msg.TS.Format(time.RFC3339),
		}}
	}
	if t.closed {
		t.mu.Unlock()
		msg := t.hangup
		if msg == "" {
			msg = "peer hung up"
		}
		return Response{Error: msg, Code: CodePeerClosed}
	}
	if t.turn == from {
		t.mu.Unlock()
		return Response{Error: "not your turn to await — it is your turn to send", Code: CodeNotYourTurn}
	}
	ch := make(chan waitResult, 1)
	t.waiter[from] = ch
	t.mu.Unlock()

	select {
	case r := <-ch:
		if r.err != "" {
			return Response{Error: r.err, Code: r.code}
		}
		return Response{OK: true, Data: map[string]any{
			"seq": r.msg.Seq, "from": r.msg.From, "body": r.msg.Body, "ts": r.msg.TS.Format(time.RFC3339),
		}}
	case <-time.After(timeout):
		t.mu.Lock()
		delete(t.waiter, from)
		t.mu.Unlock()
		return Response{Error: fmt.Sprintf("timeout after %s waiting for peer", timeout), Code: CodeTimeout}
	}
}

func (t *Tunnel) close(reason string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return
	}
	t.closed = true
	t.hangup = reason
	for name, ch := range t.waiter {
		ch <- waitResult{err: reason, code: CodePeerClosed}
		delete(t.waiter, name)
	}
}

// closeAll is called on daemon shutdown; no external write.
func (t *Tunnel) closeAll(reason string) {
	t.close(reason)
}

func renderMsg(m Message) []byte {
	body := m.Body
	if len(body) == 0 || body[len(body)-1] != '\n' {
		body += "\n"
	}
	return []byte(fmt.Sprintf("---\nseq: %d\nfrom: %s\nsid: %s\nts: %s\n---\n\n%s",
		m.Seq, m.From, m.SID, m.TS.Format(time.RFC3339), body))
}
