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

func mustCaller(args []string) string {
	from := callerName(args)
	if from == "" {
		fmt.Fprintln(os.Stderr, "caller identity required (LESCHE_NAME or --as)")
		os.Exit(1)
	}
	return from
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

	// Auto-detect identity metadata from the caller's environment
	info := DetectAgentInfo(AgentInfo{
		Harness: parseFlag(args, "--harness"),
		Model:   parseFlag(args, "--model"),
		Project: parseFlag(args, "--project"),
	})

	reqArgs := map[string]any{
		"name": name,
		"pid":  os.Getpid(),
	}
	if info.Harness != "" {
		reqArgs["harness"] = info.Harness
	}
	if info.Model != "" {
		reqArgs["model"] = info.Model
	}
	if info.Project != "" {
		reqArgs["project"] = info.Project
	}
	if info.RepoURL != "" {
		reqArgs["repo_url"] = info.RepoURL
	}
	if info.Worktree != "" {
		reqArgs["worktree"] = info.Worktree
	}
	if info.Branch != "" {
		reqArgs["branch"] = info.Branch
	}
	if info.CWD != "" {
		reqArgs["cwd"] = info.CWD
	}

	resp, err := request("register", reqArgs)
	handle(resp, err, func(data any) {
		fmt.Println(data.(map[string]any)["name"])
	})
}

// cmdUnregister removes the caller from the registry. Signed, so it
// proves ownership of the key before releasing the name. The private
// key file on disk is deleted — a later register generates a fresh
// keypair and pubkey (full reset).
func cmdUnregister(args []string) {
	from := mustCaller(args)
	resp, err := request("unregister", map[string]any{"from": from})
	handle(resp, err, func(data any) {
		m, _ := data.(map[string]any)
		if m != nil && m["name"] != nil {
			fmt.Println(m["name"])
		}
	})
}

func cmdAgents(_ []string) {
	resp, err := request("agents", nil)
	handle(resp, err, func(data any) {
		rows, ok := data.([]any)
		if !ok {
			return
		}
		// Print header
		fmt.Printf("%-26s  %-16s  %-28s  %-12s  %s\n",
			"agent_id", "name", "qualified", "harness", "started_at")
		for _, row := range rows {
			m := row.(map[string]any)
			fmt.Printf("%-26s  %-16s  %-28s  %-12s  %v\n",
				m["agent_id"], m["name"], m["qualified"], m["harness"], m["started_at"])
		}
	})
}

func cmdRooms(args []string) {
	from := mustCaller(args)
	resp, err := request("rooms", map[string]any{"from": from})
	handle(resp, err, func(data any) {
		rows, ok := data.([]any)
		if !ok || len(rows) == 0 {
			return
		}
		for _, row := range rows {
			m := row.(map[string]any)
			fmt.Printf("%s\tmembers=%v\tmessages=%v\n", m["name"], m["members"], m["messages"])
		}
	})
}

func cmdRoom(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: lesche room create <name> [--desc <text>]")
		os.Exit(1)
	}
	switch args[0] {
	case "create":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "usage: lesche room create <name> [--desc <text>]")
			os.Exit(1)
		}
		from := mustCaller(args)
		name := args[1]
		desc := parseFlag(args, "--desc")
		resp, err := request("room_create", map[string]any{"from": from, "name": name, "desc": desc})
		handle(resp, err, func(data any) {
			fmt.Println(data.(map[string]any)["name"])
		})
	default:
		fmt.Fprintf(os.Stderr, "unknown room subcommand: %s\n", args[0])
		os.Exit(1)
	}
}

func cmdJoin(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: lesche join <room>")
		os.Exit(1)
	}
	from := mustCaller(args)
	resp, err := request("join", map[string]any{"from": from, "room": args[0]})
	handle(resp, err, nil)
}

func cmdLeave(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: lesche leave <room>")
		os.Exit(1)
	}
	from := mustCaller(args)
	resp, err := request("leave", map[string]any{"from": from, "room": args[0]})
	handle(resp, err, nil)
}

func cmdParticipants(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: lesche participants <room>")
		os.Exit(1)
	}
	from := mustCaller(args)
	resp, err := request("participants", map[string]any{"from": from, "room": args[0]})
	handle(resp, err, func(data any) {
		m := data.(map[string]any)
		rows, _ := m["members"].([]any)
		for _, row := range rows {
			p := row.(map[string]any)
			fmt.Printf("%s\tpending=%v\tdropped=%v\n", p["name"], p["pending"], p["dropped"])
		}
	})
}

func cmdPost(args []string) {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: lesche post <room> <msg>")
		os.Exit(1)
	}
	from := mustCaller(args)
	room, body := args[0], args[1]
	resp, err := request("post", map[string]any{"from": from, "room": room, "body": body})
	handle(resp, err, func(data any) {
		m := data.(map[string]any)
		fmt.Printf("room=%s seq=%v\n", m["room"], m["seq"])
	})
}

// cmdTell: lesche tell <peer> <msg>
// Fire-and-forget. Returns immediately.
func cmdTell(args []string) {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: lesche tell <peer> <msg>")
		os.Exit(1)
	}
	from := mustCaller(args)
	peer, body := args[0], args[1]
	resp, err := request("tell", map[string]any{"from": from, "peer": peer, "body": body})
	handle(resp, err, func(data any) {
		m := data.(map[string]any)
		fmt.Printf("seq=%v peer=%v\n", m["seq"], m["peer"])
	})
}

// cmdAsk: lesche ask <peer> <msg> [--timeout N]
// Client-side composition: tell + read. Sends, then blocks up to timeout
// waiting for a reply from the same peer.
func cmdAsk(args []string) {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: lesche ask <peer> <msg> [--timeout N]")
		os.Exit(1)
	}
	from := mustCaller(args)
	peer, body := args[0], args[1]
	timeout := parseIntFlag(args, "--timeout", 300)

	// Step 1: tell.
	tellResp, err := request("tell", map[string]any{"from": from, "peer": peer, "body": body})
	if err != nil || !tellResp.OK {
		handle(tellResp, err, nil)
		return
	}
	// Step 2: read with timeout.
	readResp, err := request("read", map[string]any{"from": from, "peer": peer, "timeout": timeout})
	handle(readResp, err, func(data any) {
		m, ok := data.(map[string]any)
		if !ok || m["body"] == nil {
			fmt.Fprintln(os.Stderr, "timeout: no reply from "+peer)
			os.Exit(2)
		}
		printBody(m["body"])
	})
}

// cmdRead: lesche read <target> [--room] [--timeout N]
// Consumes oldest pending message. target is a peer by default; pass --room
// to read from a room with the same name instead.
func cmdRead(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: lesche read <peer|room> [--room] [--timeout N]")
		os.Exit(1)
	}
	from := mustCaller(args)
	target := args[0]
	timeout := parseIntFlag(args, "--timeout", 300)
	asRoom := parseBoolFlag(args, "--room")

	req := map[string]any{"from": from, "timeout": timeout}
	if asRoom {
		req["room"] = target
	} else {
		req["peer"] = target
	}
	resp, err := request("read", req)
	handle(resp, err, func(data any) {
		m, _ := data.(map[string]any)
		if m == nil {
			return
		}
		if _, isRoom := m["room"]; isRoom {
			printRoomMessages(m["messages"])
			return
		}
		if m["body"] == nil {
			return
		}
		printBody(m["body"])
	})
}

// cmdPeek: lesche peek <target> [--room]
func cmdPeek(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: lesche peek <peer|room> [--room]")
		os.Exit(1)
	}
	from := mustCaller(args)
	target := args[0]
	asRoom := parseBoolFlag(args, "--room")

	req := map[string]any{"from": from}
	if asRoom {
		req["room"] = target
	} else {
		req["peer"] = target
	}
	resp, err := request("peek", req)
	handle(resp, err, func(data any) {
		m, _ := data.(map[string]any)
		if m == nil {
			return
		}
		msgs, _ := m["messages"].([]any)
		if _, isRoom := m["room"]; isRoom {
			printRoomMessages(msgs)
			return
		}
		for _, item := range msgs {
			mm, _ := item.(map[string]any)
			fmt.Printf("[%v %v %v] %v\n", mm["seq"], mm["ts"], mm["from"], mm["body"])
		}
	})
}

// cmdReadAny: lesche read-any [--timeout N]
// Blocks until any channel or room delivers a message to the caller.
// Prints the source kind+target on the first line, body on subsequent lines.
func cmdReadAny(args []string) {
	from := mustCaller(args)
	timeout := parseIntFlag(args, "--timeout", 300)
	resp, err := request("read-any", map[string]any{"from": from, "timeout": timeout})
	handle(resp, err, func(data any) {
		m := data.(map[string]any)
		fmt.Printf("%s=%s\n", m["kind"], m["target"])
		printBody(m["body"])
	})
}

// cmdChannels: lesche channels — list caller's peer-pair channels.
func cmdChannels(args []string) {
	from := mustCaller(args)
	resp, err := request("channels", map[string]any{"from": from})
	handle(resp, err, func(data any) {
		rows, _ := data.([]any)
		for _, row := range rows {
			m := row.(map[string]any)
			fmt.Printf("peer=%s\tpending=%v\tmsgs=%v\n", m["peer"], m["pending_for_me"], m["msg_count"])
		}
	})
}

// cmdHistory: lesche history <target> [--room] [--since N] [--limit N]
func cmdHistory(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: lesche history <peer|room> [--room] [--since N] [--limit N]")
		os.Exit(1)
	}
	from := mustCaller(args)
	target := args[0]
	since := parseIntFlag(args, "--since", 0)
	limit := parseIntFlag(args, "--limit", 0)
	asRoom := parseBoolFlag(args, "--room")

	req := map[string]any{"from": from, "since": since, "limit": limit}
	if asRoom {
		req["room"] = target
	} else {
		req["peer"] = target
	}
	resp, err := request("history", req)
	handle(resp, err, func(data any) {
		m := data.(map[string]any)
		label := fmt.Sprintf("peer=%s", m["peer"])
		if _, ok := m["room"]; ok {
			label = fmt.Sprintf("room=%s", m["room"])
		}
		fmt.Println(label)
		msgs, _ := m["messages"].([]any)
		for _, mm := range msgs {
			row := mm.(map[string]any)
			fmt.Printf("[%v %v %v] %v\n", row["seq"], row["ts"], row["from"], row["body"])
		}
	})
}

func cmdRenew(args []string) {
	from := mustCaller(args)
	resp, err := request("renew", map[string]any{"from": from})
	handle(resp, err, func(data any) {
		m := data.(map[string]any)
		fmt.Printf("expires_at=%s\n", m["expires_at"])
	})
}

func cmdStop(_ []string) {
	resp, err := request("stop", nil)
	handle(resp, err, nil)
}

func printBody(v any) {
	s, ok := v.(string)
	if !ok {
		return
	}
	fmt.Print(s)
	if len(s) == 0 || s[len(s)-1] != '\n' {
		fmt.Println()
	}
}

func printRoomMessages(v any) {
	msgs, _ := v.([]any)
	for _, item := range msgs {
		m, _ := item.(map[string]any)
		if m == nil {
			continue
		}
		if typ, _ := m["type"].(string); typ == "notice" {
			fmt.Printf("[notice] %v\n", m["body"])
			continue
		}
		if notice, _ := m["notice"].(bool); notice {
			fmt.Printf("[notice] %v\n", m["body"])
			continue
		}
		fmt.Printf("[%v %v %v] %v\n", m["seq"], m["ts"], m["from"], m["body"])
	}
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

func parseBoolFlag(args []string, name string) bool {
	for _, a := range args {
		if a == name {
			return true
		}
	}
	return false
}

func parseIntFlag(args []string, name string, def int) int {
	v := parseFlag(args, name)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return def
	}
	return n
}
