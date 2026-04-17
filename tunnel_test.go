package main

import (
	"testing"
	"time"
)

func TestTunnelTurnFSMAndTimeouts(t *testing.T) {
	s := newFixtureState()
	tun := newTunnel("sid-fsm", "alice", "bob")

	badSend := tun.send(s, "bob", "nope", 10*time.Millisecond)
	if badSend.OK || badSend.Code != CodeNotYourTurn {
		t.Fatalf("send out-of-turn should be not_your_turn: %+v", badSend)
	}

	badAwait := tun.await("alice", 10*time.Millisecond)
	if badAwait.OK || badAwait.Code != CodeNotYourTurn {
		t.Fatalf("await on your turn should be not_your_turn: %+v", badAwait)
	}

	first := tun.send(s, "alice", "hello", 5*time.Millisecond)
	if first.OK || first.Code != CodeTimeout {
		t.Fatalf("first send should timeout waiting for reply: %+v", first)
	}

	secondBad := tun.send(s, "alice", "again", 10*time.Millisecond)
	if secondBad.OK || secondBad.Code != CodeNotYourTurn {
		t.Fatalf("second consecutive send should be not_your_turn: %+v", secondBad)
	}

	got := tun.await("bob", time.Second)
	if !got.OK {
		t.Fatalf("bob await failed: %+v", got)
	}
	if body := got.Data.(map[string]any)["body"].(string); body != "hello" {
		t.Fatalf("bob received body=%q, want hello", body)
	}

	reply := tun.send(s, "bob", "pong", 5*time.Millisecond)
	if reply.OK || reply.Code != CodeTimeout {
		t.Fatalf("bob send should timeout waiting for alice reply: %+v", reply)
	}

	got2 := tun.await("alice", time.Second)
	if !got2.OK {
		t.Fatalf("alice await failed: %+v", got2)
	}
	if body := got2.Data.(map[string]any)["body"].(string); body != "pong" {
		t.Fatalf("alice received body=%q, want pong", body)
	}
}

func TestTunnelPeerClosedSemantics(t *testing.T) {
	s := newFixtureState()
	tun := newTunnel("sid-close", "alice", "bob")
	tun.close("bob hung up")

	send := tun.send(s, "alice", "still there?", 10*time.Millisecond)
	if send.OK || send.Code != CodePeerClosed {
		t.Fatalf("send after close should be peer_closed: %+v", send)
	}

	await := tun.await("alice", 10*time.Millisecond)
	if await.OK || await.Code != CodePeerClosed {
		t.Fatalf("await after close should be peer_closed: %+v", await)
	}
}

func TestTunnelSendDeliversToAwaitAny(t *testing.T) {
	s := newFixtureState()
	tun := newTunnel("sid-any", "alice", "bob")

	ch := make(chan anyMsg, 1)
	s.mu.Lock()
	s.anyWaiter["bob"] = ch
	s.mu.Unlock()

	resp := tun.send(s, "alice", "for await-any", 5*time.Millisecond)
	if resp.OK || resp.Code != CodeTimeout {
		t.Fatalf("send should timeout while still delivering message: %+v", resp)
	}

	select {
	case am := <-ch:
		if am.sid != tun.SID {
			t.Fatalf("await-any sid=%q, want %q", am.sid, tun.SID)
		}
		if am.msg.Body != "for await-any" {
			t.Fatalf("await-any body=%q, want %q", am.msg.Body, "for await-any")
		}
	default:
		t.Fatalf("await-any channel did not receive delivered message")
	}

	s.mu.Lock()
	_, stillWaiting := s.anyWaiter["bob"]
	s.mu.Unlock()
	if stillWaiting {
		t.Fatalf("await-any waiter should be cleared after delivery")
	}

	tun.mu.Lock()
	pending := len(tun.mailbox["bob"])
	tun.mu.Unlock()
	if pending != 0 {
		t.Fatalf("bob mailbox should stay empty when await-any consumed message, got %d", pending)
	}
}
