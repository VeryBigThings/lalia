package main

import (
	"fmt"
	"sort"
	"sync"
	"time"
)

// Channel is a persistent, 2-party message log keyed by the unordered pair of
// peer names. No turn FSM, no session ID — either side may tell at any time;
// read pulls the oldest unread message from the caller's mailbox. Messages
// are durable in the git-backed workspace under peers/<a>--<b>/.
type Channel struct {
	PeerA   string
	PeerB   string
	seq     int
	log     []Message
	mailbox map[string][]Message
	waiter  map[string]chan waitResult
	mu      sync.Mutex
}

type waitResult struct {
	msg  *Message
	err  string
	code int
}

// channelKey returns the canonical (sorted) key for a peer-pair channel.
func channelKey(a, b string) string {
	if a > b {
		a, b = b, a
	}
	return a + "\x00" + b
}

// channelDir returns the workspace-relative directory for a peer-pair
// channel's transcripts: "peers/<lo>--<hi>".
func channelDir(a, b string) string {
	if a > b {
		a, b = b, a
	}
	return "peers/" + safePathSegment(a) + "--" + safePathSegment(b)
}

func newChannel(a, b string) *Channel {
	if a > b {
		a, b = b, a
	}
	return &Channel{
		PeerA:   a,
		PeerB:   b,
		mailbox: make(map[string][]Message),
		waiter:  make(map[string]chan waitResult),
	}
}

func (c *Channel) peerOf(name string) (string, bool) {
	switch name {
	case c.PeerA:
		return c.PeerB, true
	case c.PeerB:
		return c.PeerA, true
	default:
		return "", false
	}
}

// tell appends a message and delivers it to the recipient's mailbox (or to a
// blocked read waiter, or to a cross-channel read-any waiter). Returns
// immediately; never blocks.
func (c *Channel) tell(s *State, from, body string) Response {
	c.mu.Lock()
	peer, ok := c.peerOf(from)
	if !ok {
		c.mu.Unlock()
		return errorResponse(CodeError, "channel_not_participant", "use a registered peer channel", "not a participant of this channel", map[string]any{
			"from": from,
		})
	}
	c.seq++
	msg := Message{
		Seq:  c.seq,
		From: from,
		TS:   time.Now(),
		Body: body,
	}
	c.log = append(c.log, msg)

	s.enqueueWrite(
		fmt.Sprintf("%s/%06d-%s.md", channelDir(c.PeerA, c.PeerB), msg.Seq, safePathSegment(from)),
		renderChannelMsg(msg),
		fmt.Sprintf("peer msg %d %s→%s", msg.Seq, from, peer),
	)

	if ch, ok := c.waiter[peer]; ok {
		ch <- waitResult{msg: &msg}
		delete(c.waiter, peer)
	} else if s.deliverAny(peer, "peer", other(c, peer), msg) {
		// delivered to cross-channel read-any
	} else {
		c.mailbox[peer] = append(c.mailbox[peer], msg)
		if s.queue != nil {
			_ = s.queue.mailboxAppend(peer, "peer", from, msg.Seq, from, msg.TS, msg.Body)
		}
	}
	c.mu.Unlock()

	return Response{OK: true, Data: map[string]any{"seq": msg.Seq, "peer": peer}}
}

// read consumes and returns the oldest message in the caller's mailbox. If
// the mailbox is empty and timeout > 0, blocks up to timeout for a new
// message. timeout == 0 returns immediately with an empty result.
func (c *Channel) read(s *State, from string, timeout time.Duration) Response {
	c.mu.Lock()
	_, ok := c.peerOf(from)
	if !ok {
		c.mu.Unlock()
		return errorResponse(CodeError, "channel_not_participant", "use a registered peer channel", "not a participant of this channel", map[string]any{
			"from": from,
		})
	}
	if q, ok := c.mailbox[from]; ok && len(q) > 0 {
		msg := q[0]
		c.mailbox[from] = q[1:]
		if s.queue != nil {
			_ = s.queue.mailboxDeleteOne(from, "peer", other(c, from), msg.Seq)
		}
		c.mu.Unlock()
		return msgResponse(&msg)
	}
	if timeout <= 0 {
		c.mu.Unlock()
		return Response{OK: true, Data: map[string]any{}}
	}
	ch := make(chan waitResult, 1)
	c.waiter[from] = ch
	c.mu.Unlock()

	select {
	case r := <-ch:
		if r.err != "" {
			reason := "read_failed"
			retry := ""
			if r.code == CodePeerClosed {
				reason = "peer_closed"
				retry = "re-register and retry read"
			}
			return errorResponse(r.code, reason, retry, r.err, map[string]any{
				"from": from,
			})
		}
		return msgResponse(r.msg)
	case <-time.After(timeout):
		c.mu.Lock()
		delete(c.waiter, from)
		c.mu.Unlock()
		return Response{OK: true, Data: map[string]any{}}
	}
}

// peek returns all pending messages without consuming.
func (c *Channel) peek(from string) Response {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.peerOf(from)
	if !ok {
		return errorResponse(CodeError, "channel_not_participant", "use a registered peer channel", "not a participant of this channel", map[string]any{
			"from": from,
		})
	}
	q := c.mailbox[from]
	out := make([]any, 0, len(q))
	for _, m := range q {
		out = append(out, map[string]any{
			"seq":  m.Seq,
			"from": m.From,
			"body": m.Body,
			"ts":   m.TS.Format(time.RFC3339),
		})
	}
	return Response{OK: true, Data: map[string]any{"peer": other(c, from), "messages": out}}
}

// releaseWaiter signals an in-flight read with peer_closed. Called when an
// agent's lease expires so their own hanging read returns immediately.
func (c *Channel) releaseWaiter(name, reason string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if ch, ok := c.waiter[name]; ok {
		ch <- waitResult{err: reason, code: CodePeerClosed}
		delete(c.waiter, name)
	}
}

func msgResponse(m *Message) Response {
	return Response{OK: true, Data: map[string]any{
		"seq":  m.Seq,
		"from": m.From,
		"body": m.Body,
		"ts":   m.TS.Format(time.RFC3339),
	}}
}

func other(c *Channel, from string) string {
	if from == c.PeerA {
		return c.PeerB
	}
	return c.PeerA
}

func renderChannelMsg(m Message) []byte {
	body := m.Body
	if len(body) == 0 || body[len(body)-1] != '\n' {
		body += "\n"
	}
	return []byte(fmt.Sprintf("---\nseq: %d\nfrom: %s\nts: %s\n---\n\n%s",
		m.Seq, m.From, m.TS.Format(time.RFC3339), body))
}

// listChannels returns channels the named agent participates in, sorted by
// key for deterministic output.
func (s *State) listChannels(name string) []*Channel {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*Channel, 0)
	for _, c := range s.channels {
		if c.PeerA == name || c.PeerB == name {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return channelKey(out[i].PeerA, out[i].PeerB) < channelKey(out[j].PeerA, out[j].PeerB)
	})
	return out
}

// getOrCreateChannel resolves or implicitly creates the peer-pair channel.
// Caller must ensure `from` and `peer` are registered agents.
func (s *State) getOrCreateChannel(from, peer string) *Channel {
	key := channelKey(from, peer)
	s.mu.Lock()
	defer s.mu.Unlock()
	if c, ok := s.channels[key]; ok {
		return c
	}
	c := newChannel(from, peer)
	s.channels[key] = c
	return c
}
