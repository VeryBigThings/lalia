package main

import "time"

type Request struct {
	Op   string         `json:"op"`
	Args map[string]any `json:"args,omitempty"`
}

type Response struct {
	OK    bool   `json:"ok"`
	Data  any    `json:"data,omitempty"`
	Error string `json:"error,omitempty"`
	Code  int    `json:"code,omitempty"`
}

type ErrorDetail struct {
	Code      int            `json:"code,omitempty"`
	Reason    string         `json:"reason,omitempty"`
	RetryHint string         `json:"retry_hint,omitempty"`
	Context   map[string]any `json:"context,omitempty"`
}

type Message struct {
	Seq  int       `json:"seq"`
	From string    `json:"from"`
	TS   time.Time `json:"ts"`
	Body string    `json:"body"`
}

const (
	CodeOK         = 0
	CodeError      = 1
	CodeTimeout    = 2
	CodePeerClosed = 3
	// CodeNotYourTurn is reserved; no longer produced now that channels have
	// no turn FSM. Keep the slot so external tooling that still parses 4
	// doesn't silently reuse the code for a new meaning.
	CodeNotYourTurn  = 4
	CodeNotFound     = 5
	CodeUnauthorized    = 6 // signature rejected or caller not registered
	CodeSupervisorBusy          = 7  // supervisor still owns a non-empty task list; handoff first
	CodeProjectIdentityMismatch = 8  // publish payload project does not match caller's registered project
	CodePIDConflict             = 9  // PID already registered as a different live agent
	CodeSessionConflict         = 10 // re-registration conflicts with a live session's harness or CWD
)

func errorResponse(code int, reason, retryHint, message string, context map[string]any) Response {
	if code == 0 {
		code = CodeError
	}
	return Response{
		Error: message,
		Code:  code,
		Data: map[string]any{
			"error": ErrorDetail{
				Code:      code,
				Reason:    reason,
				RetryHint: retryHint,
				Context:   context,
			},
		},
	}
}
