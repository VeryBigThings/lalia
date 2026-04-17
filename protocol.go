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

type Message struct {
	Seq  int       `json:"seq"`
	From string    `json:"from"`
	SID  string    `json:"sid"`
	TS   time.Time `json:"ts"`
	Body string    `json:"body"`
}

const (
	CodeOK                = 0
	CodeError             = 1
	CodeTimeout           = 2
	CodePeerClosed        = 3
	CodeNotYourTurn       = 4
	CodeNotFound          = 5
)
