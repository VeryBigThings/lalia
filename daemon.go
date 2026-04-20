package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/signal"
	"syscall"
)

func runDaemon() {
	if err := os.MkdirAll(leschDir(), 0700); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	// singleton: if a responsive daemon already listens, exit quietly.
	if c, err := net.Dial("unix", socketPath()); err == nil {
		c.Close()
		fmt.Fprintln(os.Stderr, "another lalia daemon is already running")
		os.Exit(0)
	}
	if err := os.WriteFile(pidPath(), []byte(fmt.Sprintf("%d\n", os.Getpid())), 0600); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	_ = os.Remove(socketPath())
	l, err := net.Listen("unix", socketPath())
	if err != nil {
		fmt.Fprintln(os.Stderr, "listen:", err)
		os.Remove(pidPath())
		os.Exit(1)
	}
	_ = os.Chmod(socketPath(), 0600)

	st, err := newState()
	if err != nil {
		fmt.Fprintln(os.Stderr, "state init:", err)
		l.Close()
		os.Remove(socketPath())
		os.Remove(pidPath())
		os.Exit(1)
	}
	go st.runWriter()
	st.startSweeper()

	// signal handler for clean shutdown
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigs
		st.requestStop()
		l.Close()
	}()

	fmt.Fprintf(os.Stderr, "lalia daemon pid=%d socket=%s workspace=%s\n", os.Getpid(), socketPath(), workspacePath())

	for {
		conn, err := l.Accept()
		if err != nil {
			break
		}
		go handleConn(st, conn)
	}

	st.waitWriterDone()
	os.Remove(socketPath())
	os.Remove(pidPath())
}

func handleConn(st *State, conn net.Conn) {
	defer conn.Close()
	r := bufio.NewReader(conn)
	line, err := r.ReadBytes('\n')
	if err != nil {
		return
	}
	var req Request
	if err := json.Unmarshal(line, &req); err != nil {
		writeResp(conn, Response{Error: "bad request: " + err.Error()})
		return
	}
	resp := st.dispatch(req)
	writeResp(conn, resp)
}

func writeResp(conn net.Conn, r Response) {
	data, _ := json.Marshal(r)
	fmt.Fprintf(conn, "%s\n", data)
}
