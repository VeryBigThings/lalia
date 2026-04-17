package main

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func roomMessages(t *testing.T, resp Response) []map[string]any {
	t.Helper()
	if !resp.OK {
		t.Fatalf("expected OK response, got: %+v", resp)
	}
	top, ok := resp.Data.(map[string]any)
	if !ok {
		t.Fatalf("room data type=%T, want map[string]any", resp.Data)
	}
	raw, ok := top["messages"].([]any)
	if !ok {
		t.Fatalf("messages type=%T, want []any", top["messages"])
	}
	out := make([]map[string]any, 0, len(raw))
	for _, row := range raw {
		m, _ := row.(map[string]any)
		if m != nil {
			out = append(out, m)
		}
	}
	return out
}

func participantNames(t *testing.T, resp Response) []string {
	t.Helper()
	if !resp.OK {
		t.Fatalf("expected OK response, got: %+v", resp)
	}
	top, ok := resp.Data.(map[string]any)
	if !ok {
		t.Fatalf("participants data type=%T, want map[string]any", resp.Data)
	}
	raw, ok := top["members"].([]any)
	if !ok {
		t.Fatalf("members type=%T, want []any", top["members"])
	}
	out := make([]string, 0, len(raw))
	for _, row := range raw {
		m, _ := row.(map[string]any)
		if m == nil {
			continue
		}
		name, _ := m["name"].(string)
		out = append(out, name)
	}
	return out
}

func TestRoomJoinLeaveMembership(t *testing.T) {
	s := newFixtureState()
	mustRegister(t, s, "alice", 1)
	mustRegister(t, s, "bob", 2)

	create := s.opRoomCreate(Request{Args: map[string]any{"from": "alice", "name": "eng"}})
	if !create.OK {
		t.Fatalf("create room failed: %+v", create)
	}

	initial := s.opParticipants(Request{Args: map[string]any{"from": "alice", "room": "eng"}})
	names := participantNames(t, initial)
	if len(names) != 1 || names[0] != "alice" {
		t.Fatalf("initial participants=%v, want [alice]", names)
	}

	join := s.opJoin(Request{Args: map[string]any{"from": "bob", "room": "eng"}})
	if !join.OK {
		t.Fatalf("join failed: %+v", join)
	}

	afterJoin := s.opParticipants(Request{Args: map[string]any{"from": "alice", "room": "eng"}})
	joinedNames := participantNames(t, afterJoin)
	if len(joinedNames) != 2 || joinedNames[0] != "alice" || joinedNames[1] != "bob" {
		t.Fatalf("participants after join=%v, want [alice bob]", joinedNames)
	}

	leave := s.opLeave(Request{Args: map[string]any{"from": "bob", "room": "eng"}})
	if !leave.OK {
		t.Fatalf("leave failed: %+v", leave)
	}

	afterLeave := s.opParticipants(Request{Args: map[string]any{"from": "alice", "room": "eng"}})
	leftNames := participantNames(t, afterLeave)
	if len(leftNames) != 1 || leftNames[0] != "alice" {
		t.Fatalf("participants after leave=%v, want [alice]", leftNames)
	}
}

func TestRoomPostBroadcastAndReadAccess(t *testing.T) {
	s := newFixtureState()
	mustRegister(t, s, "alice", 1)
	mustRegister(t, s, "bob", 2)
	mustRegister(t, s, "carol", 3)
	mustRegister(t, s, "dave", 4)

	if !s.opRoomCreate(Request{Args: map[string]any{"from": "alice", "name": "ops"}}).OK {
		t.Fatalf("create room failed")
	}
	if !s.opJoin(Request{Args: map[string]any{"from": "bob", "room": "ops"}}).OK {
		t.Fatalf("bob join failed")
	}
	if !s.opJoin(Request{Args: map[string]any{"from": "carol", "room": "ops"}}).OK {
		t.Fatalf("carol join failed")
	}

	post := s.opPost(Request{Args: map[string]any{"from": "alice", "room": "ops", "body": "deploy at 10"}})
	if !post.OK {
		t.Fatalf("post failed: %+v", post)
	}

	bobPeek := s.opPeek(Request{Args: map[string]any{"from": "bob", "room": "ops"}})
	bobMsgs := roomMessages(t, bobPeek)
	if len(bobMsgs) != 1 || bobMsgs[0]["body"] != "deploy at 10" {
		t.Fatalf("bob messages=%v, want one broadcast", bobMsgs)
	}

	carolPeek := s.opPeek(Request{Args: map[string]any{"from": "carol", "room": "ops"}})
	carolMsgs := roomMessages(t, carolPeek)
	if len(carolMsgs) != 1 || carolMsgs[0]["body"] != "deploy at 10" {
		t.Fatalf("carol messages=%v, want one broadcast", carolMsgs)
	}

	alicePeek := s.opPeek(Request{Args: map[string]any{"from": "alice", "room": "ops"}})
	aliceMsgs := roomMessages(t, alicePeek)
	if len(aliceMsgs) != 0 {
		t.Fatalf("sender should not receive own post, got %v", aliceMsgs)
	}

	nonMemberPeek := s.opPeek(Request{Args: map[string]any{"from": "dave", "room": "ops"}})
	if nonMemberPeek.OK || nonMemberPeek.Code != CodeNotFound {
		t.Fatalf("non-member peek should be not_found: %+v", nonMemberPeek)
	}
	nonMemberInbox := s.opInbox(Request{Args: map[string]any{"from": "dave", "room": "ops"}})
	if nonMemberInbox.OK || nonMemberInbox.Code != CodeNotFound {
		t.Fatalf("non-member inbox should be not_found: %+v", nonMemberInbox)
	}
	nonMemberPost := s.opPost(Request{Args: map[string]any{"from": "dave", "room": "ops", "body": "x"}})
	if nonMemberPost.OK || nonMemberPost.Code != CodeNotFound {
		t.Fatalf("non-member post should be not_found: %+v", nonMemberPost)
	}
}

func TestRoomMailboxOverflowNoticeAndNoSenderBlocking(t *testing.T) {
	s := newFixtureState()
	mustRegister(t, s, "alice", 1)
	mustRegister(t, s, "bob", 2)

	create := s.opRoomCreate(Request{Args: map[string]any{"from": "alice", "name": "alerts"}})
	if !create.OK {
		t.Fatalf("create room failed: %+v", create)
	}
	if !s.opJoin(Request{Args: map[string]any{"from": "bob", "room": "alerts"}}).OK {
		t.Fatalf("bob join failed")
	}

	total := roomMailboxLimit + 6
	start := time.Now()
	for i := 1; i <= total; i++ {
		resp := s.opPost(Request{Args: map[string]any{
			"from": "alice",
			"room": "alerts",
			"body": fmt.Sprintf("m%02d", i),
		}})
		if !resp.OK {
			t.Fatalf("post %d failed: %+v", i, resp)
		}
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("posting took too long (%s), sender should not block on slow subscriber", elapsed)
	}

	peek := s.opPeek(Request{Args: map[string]any{"from": "bob", "room": "alerts"}})
	peekMsgs := roomMessages(t, peek)
	if len(peekMsgs) != roomMailboxLimit {
		t.Fatalf("peek count=%d, want %d", len(peekMsgs), roomMailboxLimit)
	}
	if body, _ := peekMsgs[0]["body"].(string); body != "m07" {
		t.Fatalf("oldest retained body=%q, want m07", body)
	}

	inbox := s.opInbox(Request{Args: map[string]any{"from": "bob", "room": "alerts"}})
	inboxMsgs := roomMessages(t, inbox)
	if len(inboxMsgs) != roomMailboxLimit+1 {
		t.Fatalf("inbox count=%d, want %d (notice + queue)", len(inboxMsgs), roomMailboxLimit+1)
	}
	if notice, _ := inboxMsgs[0]["notice"].(bool); !notice {
		t.Fatalf("first inbox entry should be overflow notice: %v", inboxMsgs[0])
	}
	noticeBody, _ := inboxMsgs[0]["body"].(string)
	if !strings.Contains(noticeBody, "6 dropped") {
		t.Fatalf("notice body=%q, want to mention 6 dropped", noticeBody)
	}
	if body, _ := inboxMsgs[1]["body"].(string); body != "m07" {
		t.Fatalf("first delivered message body=%q, want m07", body)
	}

	nextInbox := s.opInbox(Request{Args: map[string]any{"from": "bob", "room": "alerts"}})
	nextMsgs := roomMessages(t, nextInbox)
	if len(nextMsgs) != 0 {
		t.Fatalf("next inbox should be empty after drain, got %d entries", len(nextMsgs))
	}
}

func TestRoomPeekDoesNotConsumeInboxDoes(t *testing.T) {
	s := newFixtureState()
	mustRegister(t, s, "alice", 1)
	mustRegister(t, s, "bob", 2)

	if !s.opRoomCreate(Request{Args: map[string]any{"from": "alice", "name": "ops"}}).OK {
		t.Fatalf("create room failed")
	}
	if !s.opJoin(Request{Args: map[string]any{"from": "bob", "room": "ops"}}).OK {
		t.Fatalf("join failed")
	}
	if !s.opPost(Request{Args: map[string]any{"from": "alice", "room": "ops", "body": "m1"}}).OK {
		t.Fatalf("post failed")
	}

	peek1 := roomMessages(t, s.opPeek(Request{Args: map[string]any{"from": "bob", "room": "ops"}}))
	if len(peek1) != 1 || peek1[0]["body"] != "m1" {
		t.Fatalf("first peek=%v, want single m1", peek1)
	}
	peek2 := roomMessages(t, s.opPeek(Request{Args: map[string]any{"from": "bob", "room": "ops"}}))
	if len(peek2) != 1 || peek2[0]["body"] != "m1" {
		t.Fatalf("second peek should still see m1, got %v", peek2)
	}

	inbox := roomMessages(t, s.opInbox(Request{Args: map[string]any{"from": "bob", "room": "ops"}}))
	if len(inbox) != 1 || inbox[0]["body"] != "m1" {
		t.Fatalf("inbox=%v, want single m1", inbox)
	}
	inbox2 := roomMessages(t, s.opInbox(Request{Args: map[string]any{"from": "bob", "room": "ops"}}))
	if len(inbox2) != 0 {
		t.Fatalf("second inbox should be empty, got %v", inbox2)
	}
}

func TestSafePathSegment(t *testing.T) {
	in := "agent/name with\\tabs\nline"
	got := safePathSegment(in)
	if strings.Contains(got, "/") || strings.Contains(got, "\\") || strings.Contains(got, " ") || strings.Contains(got, "\t") || strings.Contains(got, "\n") {
		t.Fatalf("safePathSegment(%q)=%q still contains unsafe chars", in, got)
	}
	if got == in || got == "" {
		t.Fatalf("safePathSegment(%q)=%q, expected transformed non-empty value", in, got)
	}
}

func TestRoomPerSenderFIFOWithInterleavedPosts(t *testing.T) {
	s := newFixtureState()
	mustRegister(t, s, "alice", 1)
	mustRegister(t, s, "carol", 2)
	mustRegister(t, s, "bob", 3)

	if !s.opRoomCreate(Request{Args: map[string]any{"from": "alice", "name": "triage"}}).OK {
		t.Fatalf("create room failed")
	}
	if !s.opJoin(Request{Args: map[string]any{"from": "carol", "room": "triage"}}).OK {
		t.Fatalf("carol join failed")
	}
	if !s.opJoin(Request{Args: map[string]any{"from": "bob", "room": "triage"}}).OK {
		t.Fatalf("bob join failed")
	}

	posts := []struct {
		from string
		body string
	}{
		{from: "alice", body: "a1"},
		{from: "carol", body: "c1"},
		{from: "alice", body: "a2"},
		{from: "carol", body: "c2"},
	}
	for _, p := range posts {
		resp := s.opPost(Request{Args: map[string]any{"from": p.from, "room": "triage", "body": p.body}})
		if !resp.OK {
			t.Fatalf("post %+v failed: %+v", p, resp)
		}
	}

	msgs := roomMessages(t, s.opInbox(Request{Args: map[string]any{"from": "bob", "room": "triage"}}))
	if len(msgs) != 4 {
		t.Fatalf("bob inbox len=%d, want 4", len(msgs))
	}

	var aliceBodies, carolBodies []string
	for _, m := range msgs {
		from, _ := m["from"].(string)
		body, _ := m["body"].(string)
		switch from {
		case "alice":
			aliceBodies = append(aliceBodies, body)
		case "carol":
			carolBodies = append(carolBodies, body)
		}
	}
	if strings.Join(aliceBodies, ",") != "a1,a2" {
		t.Fatalf("alice FIFO violated: got %v, want [a1 a2]", aliceBodies)
	}
	if strings.Join(carolBodies, ",") != "c1,c2" {
		t.Fatalf("carol FIFO violated: got %v, want [c1 c2]", carolBodies)
	}
}
