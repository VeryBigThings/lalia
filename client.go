package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"time"
)

func dial() (net.Conn, error) {
	return net.Dial("unix", socketPath())
}

func dialOrSpawn() (net.Conn, error) {
	c, err := dial()
	if err == nil {
		return c, nil
	}
	if err := spawnDaemon(); err != nil {
		return nil, fmt.Errorf("spawn daemon: %w", err)
	}
	for i := 0; i < 50; i++ {
		time.Sleep(100 * time.Millisecond)
		c, err = dial()
		if err == nil {
			return c, nil
		}
	}
	return nil, errors.New("daemon did not start after spawn")
}

func spawnDaemon() error {
	if err := os.MkdirAll(leschDir(), 0700); err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	logFile, _ := os.OpenFile(leschDir()+"/daemon.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	cmd := exec.Command(exe, "--daemon")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Stdin = nil
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	return cmd.Start()
}

func request(op string, args map[string]any) (*Response, error) {
	conn, err := dialOrSpawn()
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	// Sign the request if it carries a caller identity and we have the
	// key on disk. register/agents/stop are signature-exempt on the
	// daemon side; skip signing them here as well.
	if from, ok := args["from"].(string); ok && from != "" && op != "register" && op != "agents" && op != "stop" {
		priv, err := loadPrivateKey(from)
		if err != nil {
			return nil, fmt.Errorf("load key for %s: %w", from, err)
		}
		sig, err := signRequest(priv, args)
		if err != nil {
			return nil, fmt.Errorf("sign: %w", err)
		}
		args["sig"] = sig
	}

	req := Request{Op: op, Args: args}
	data, _ := json.Marshal(req)
	if _, err := fmt.Fprintf(conn, "%s\n", data); err != nil {
		return nil, err
	}
	r := bufio.NewReader(conn)
	// no client-side read deadline; daemon enforces timeouts
	line, err := r.ReadBytes('\n')
	if err != nil {
		return nil, err
	}
	var resp Response
	if err := json.Unmarshal(line, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func callerName(args []string) string {
	if v := parseFlag(args, "--as"); v != "" {
		return v
	}
	return os.Getenv("LESCHE_NAME")
}

func cmdRegister(args []string) {
	name := parseFlag(args, "--name")
	if name == "" {
		name = os.Getenv("LESCHE_NAME")
	}
	if name == "" {
		fmt.Fprintln(os.Stderr, "--name or LESCHE_NAME required")
		os.Exit(1)
	}
	resp, err := request("register", map[string]any{"name": name, "pid": os.Getpid()})
	handle(resp, err, func(data any) {
		fmt.Println(data.(map[string]any)["name"])
	})
}

func cmdAgents(_ []string) {
	resp, err := request("agents", nil)
	handle(resp, err, func(data any) {
		for _, row := range data.([]any) {
			m := row.(map[string]any)
			fmt.Printf("%s\t%v\t%v\n", m["name"], m["pid"], m["started_at"])
		}
	})
}

func cmdTunnel(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: lesche tunnel <peer>")
		os.Exit(1)
	}
	peer := args[0]
	from := callerName(args)
	if from == "" {
		fmt.Fprintln(os.Stderr, "caller identity required (LESCHE_NAME or --as)")
		os.Exit(1)
	}
	resp, err := request("tunnel", map[string]any{"from": from, "peer": peer})
	handle(resp, err, func(data any) {
		fmt.Printf("sid=%s\n", data.(map[string]any)["sid"])
	})
}

func cmdSend(args []string) {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: lesche send <sid> <msg> [--timeout N]")
		os.Exit(1)
	}
	sid, body := args[0], args[1]
	from := callerName(args)
	if from == "" {
		fmt.Fprintln(os.Stderr, "caller identity required (LESCHE_NAME or --as)")
		os.Exit(1)
	}
	timeout := parseIntFlag(args, "--timeout", 300)
	resp, err := request("send", map[string]any{"from": from, "sid": sid, "body": body, "timeout": timeout})
	handle(resp, err, func(data any) {
		m := data.(map[string]any)
		fmt.Print(m["body"])
		if s, ok := m["body"].(string); ok && (len(s) == 0 || s[len(s)-1] != '\n') {
			fmt.Println()
		}
	})
}

func cmdAwait(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: lesche await <sid> [--timeout N]")
		os.Exit(1)
	}
	sid := args[0]
	from := callerName(args)
	if from == "" {
		fmt.Fprintln(os.Stderr, "caller identity required (LESCHE_NAME or --as)")
		os.Exit(1)
	}
	timeout := parseIntFlag(args, "--timeout", 300)
	resp, err := request("await", map[string]any{"from": from, "sid": sid, "timeout": timeout})
	handle(resp, err, func(data any) {
		m := data.(map[string]any)
		fmt.Print(m["body"])
		if s, ok := m["body"].(string); ok && (len(s) == 0 || s[len(s)-1] != '\n') {
			fmt.Println()
		}
	})
}

func cmdClose(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: lesche close <sid>")
		os.Exit(1)
	}
	sid := args[0]
	from := callerName(args)
	if from == "" {
		fmt.Fprintln(os.Stderr, "caller identity required (LESCHE_NAME or --as)")
		os.Exit(1)
	}
	resp, err := request("close", map[string]any{"from": from, "sid": sid})
	handle(resp, err, nil)
}

func cmdStop(_ []string) {
	resp, err := request("stop", nil)
	handle(resp, err, nil)
}

func cmdSessions(args []string) {
	from := callerName(args)
	if from == "" {
		fmt.Fprintln(os.Stderr, "caller identity required (LESCHE_NAME or --as)")
		os.Exit(1)
	}
	resp, err := request("sessions", map[string]any{"from": from})
	handle(resp, err, func(data any) {
		rows, ok := data.([]any)
		if !ok || len(rows) == 0 {
			return
		}
		for _, row := range rows {
			m := row.(map[string]any)
			turn := "your turn"
			if !m["your_turn"].(bool) {
				turn = fmt.Sprintf("%s's turn", m["turn"])
			}
			status := "open"
			if m["closed"].(bool) {
				status = "closed"
			}
			fmt.Printf("%s\tpeer=%s\t%s\t%s\tpending=%v\tmsgs=%v\n",
				m["sid"], m["peer"], status, turn, m["pending_for_me"], m["msg_count"])
		}
	})
}

func cmdAwaitAny(args []string) {
	from := callerName(args)
	if from == "" {
		fmt.Fprintln(os.Stderr, "caller identity required (LESCHE_NAME or --as)")
		os.Exit(1)
	}
	timeout := parseIntFlag(args, "--timeout", 300)
	resp, err := request("await-any", map[string]any{"from": from, "timeout": timeout})
	handle(resp, err, func(data any) {
		m := data.(map[string]any)
		fmt.Printf("sid=%s\n", m["sid"])
		body := m["body"].(string)
		fmt.Print(body)
		if len(body) == 0 || body[len(body)-1] != '\n' {
			fmt.Println()
		}
	})
}

func cmdHistory(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: lesche history <sid> [--since SEQ] [--limit N]")
		os.Exit(1)
	}
	sid := args[0]
	from := callerName(args)
	if from == "" {
		fmt.Fprintln(os.Stderr, "caller identity required (LESCHE_NAME or --as)")
		os.Exit(1)
	}
	since := parseIntFlag(args, "--since", 0)
	limit := parseIntFlag(args, "--limit", 0)
	resp, err := request("history", map[string]any{"from": from, "sid": sid, "since": since, "limit": limit})
	handle(resp, err, func(data any) {
		m := data.(map[string]any)
		fmt.Printf("sid=%s peers=%s,%s closed=%v\n", m["sid"], m["peer_a"], m["peer_b"], m["closed"])
		msgs, _ := m["messages"].([]any)
		for _, mm := range msgs {
			row := mm.(map[string]any)
			fmt.Printf("[%v %v %v] %v\n", row["seq"], row["ts"], row["from"], row["body"])
		}
	})
}

func cmdRenew(args []string) {
	from := callerName(args)
	if from == "" {
		fmt.Fprintln(os.Stderr, "caller identity required (LESCHE_NAME or --as)")
		os.Exit(1)
	}
	resp, err := request("renew", map[string]any{"from": from})
	handle(resp, err, func(data any) {
		m := data.(map[string]any)
		fmt.Printf("expires_at=%s\n", m["expires_at"])
	})
}

func handle(resp *Response, err error, ok func(any)) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if !resp.OK {
		fmt.Fprintln(os.Stderr, resp.Error)
		code := resp.Code
		if code == 0 {
			code = 1
		}
		os.Exit(code)
	}
	if ok != nil && resp.Data != nil {
		ok(resp.Data)
	}
}

func parseFlag(args []string, name string) string {
	for i, a := range args {
		if a == name && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func parseIntFlag(args []string, name string, def int) int {
	v := parseFlag(args, name)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return def
	}
	return n
}
