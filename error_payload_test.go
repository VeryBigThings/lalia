package main

import "testing"

func readErrorDetail(t *testing.T, resp Response) ErrorDetail {
	t.Helper()
	data, ok := resp.Data.(map[string]any)
	if !ok {
		t.Fatalf("response data is %T, want map", resp.Data)
	}
	raw, ok := data["error"]
	if !ok {
		t.Fatalf("response missing data.error: %+v", resp)
	}
	switch v := raw.(type) {
	case ErrorDetail:
		return v
	case map[string]any:
		d := ErrorDetail{}
		if x, ok := v["code"].(float64); ok {
			d.Code = int(x)
		}
		if x, ok := v["reason"].(string); ok {
			d.Reason = x
		}
		if x, ok := v["retry_hint"].(string); ok {
			d.RetryHint = x
		}
		if x, ok := v["context"].(map[string]any); ok {
			d.Context = x
		}
		return d
	default:
		t.Fatalf("data.error is %T, want ErrorDetail/map", raw)
	}
	return ErrorDetail{}
}

func TestErrorResponseIncludesStructuredPayload(t *testing.T) {
	resp := errorResponse(CodeNotFound, "peer_not_registered", "check agents", "peer not registered: ghost", map[string]any{"peer": "ghost"})
	if resp.OK {
		t.Fatalf("error response must not be ok: %+v", resp)
	}
	if resp.Code != CodeNotFound {
		t.Fatalf("unexpected code: %d", resp.Code)
	}
	detail := readErrorDetail(t, resp)
	if detail.Code != CodeNotFound || detail.Reason != "peer_not_registered" {
		t.Fatalf("unexpected detail: %+v", detail)
	}
	if detail.Context["peer"] != "ghost" {
		t.Fatalf("unexpected context: %+v", detail.Context)
	}
}

func TestStructuredErrorsAcrossHandlers(t *testing.T) {
	s := newFixtureState()
	mustRegister(t, s, "alice", 1)

	// state.go: unknown peer in tell
	tell := s.opTell(Request{Args: map[string]any{"from": "alice", "peer": "ghost", "body": "hi"}})
	if tell.OK || tell.Code != CodeNotFound {
		t.Fatalf("unexpected tell response: %+v", tell)
	}
	if d := readErrorDetail(t, tell); d.Reason != "peer_not_registered" {
		t.Fatalf("unexpected tell reason: %+v", d)
	}

	// channel.go: caller is not a channel participant
	ch := newChannel("alice", "bob")
	read := ch.peek("ghost")
	if read.OK || read.Code != CodeError {
		t.Fatalf("unexpected channel response: %+v", read)
	}
	if d := readErrorDetail(t, read); d.Reason != "channel_not_participant" {
		t.Fatalf("unexpected read reason: %+v", d)
	}

	// room.go: room not found
	room := s.opJoin(Request{Args: map[string]any{"from": "alice", "room": "missing"}})
	if room.OK || room.Code != CodeNotFound {
		t.Fatalf("unexpected join response: %+v", room)
	}
	if d := readErrorDetail(t, room); d.Reason != "room_not_found" {
		t.Fatalf("unexpected room reason: %+v", d)
	}
}
