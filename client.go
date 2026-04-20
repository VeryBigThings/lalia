package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
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
	return os.Getenv("LALIA_NAME")
}

func mustCaller(args []string) string {
	from := callerName(args)
	if from == "" {
		fmt.Fprintln(os.Stderr, "caller identity required (LALIA_NAME or --as)")
		os.Exit(1)
	}
	return from
}

func cmdRegister(args []string) {
	name := parseFlag(args, "--name")
	if name == "" {
		name = os.Getenv("LALIA_NAME")
	}
	if name == "" {
		fmt.Fprintln(os.Stderr, "--name or LALIA_NAME required")
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
	if role := parseFlag(args, "--role"); role != "" {
		reqArgs["role"] = role
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
	if info.RepoRoot != "" {
		reqArgs["repo_root"] = info.RepoRoot
	}
	if info.MainRepoRoot != "" {
		reqArgs["main_repo_root"] = info.MainRepoRoot
	}
	if info.WorktreeKind != "" {
		reqArgs["worktree_kind"] = info.WorktreeKind
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

func cmdAgents(args []string) {
	resp, err := request("agents", nil)
	handle(resp, err, func(data any) {
		rows, ok := data.([]any)
		if !ok {
			return
		}

		if parseBoolFlag(args, "--json") {
			raw, _ := json.MarshalIndent(rows, "", "  ")
			fmt.Println(string(raw))
			return
		}

		flat := parseBoolFlag(args, "--flat")
		wide := parseBoolFlag(args, "--wide")

		if flat {
			// Print header
			if wide {
				fmt.Printf("%-26s  %-16s  %-28s  %-12s  %-12s  %-10s  %-12s  %-12s  %-7s  %s\n",
					"agent_id", "name", "project", "harness", "branch", "role", "started", "last_seen", "lease", "cwd")
			} else {
				fmt.Printf("%-16s  %-20s  %-12s  %-12s  %-12s  %-12s  %s\n",
					"name", "project", "harness", "branch", "role", "last_seen", "lease")
			}
			for _, row := range rows {
				m := row.(map[string]any)
				lastSeenTs, _ := time.Parse(time.RFC3339, m["last_seen_at"].(string))
				startedTs, _ := time.Parse(time.RFC3339, m["started_at"].(string))
				lease, _ := m["lease_status"].(string)

				if wide {
					fmt.Printf("%-26s  %-16s  %-28s  %-12s  %-12s  %-10s  %-12s  %-12s  %-7s  %s\n",
						m["agent_id"], m["name"], m["project"], m["harness"], m["branch"], m["role"],
						startedTs.Format("2006-01-02"), humanizeDuration(lastSeenTs), lease, m["cwd"])
				} else {
					fmt.Printf("%-16s  %-20s  %-12s  %-12s  %-12s  %-12s  %s\n",
						m["name"], m["project"], m["harness"], m["branch"], m["role"],
						humanizeDuration(lastSeenTs), lease)
				}
			}
			return
		}

		// Grouped view (default)
		type RepoGroup struct {
			Path   string
			Name   string
			Agents []map[string]any
		}
		groups := make(map[string]*RepoGroup)
		var outside []map[string]any

		for _, row := range rows {
			m := row.(map[string]any)
			mainRoot, _ := m["main_repo_root"].(string)
			if mainRoot == "" {
				outside = append(outside, m)
				continue
			}
			g, ok := groups[mainRoot]
			if !ok {
				g = &RepoGroup{Path: mainRoot, Name: m["project"].(string)}
				groups[mainRoot] = g
			}
			g.Agents = append(g.Agents, m)
		}

		// Sort groups by agent count desc
		sortedGroups := make([]*RepoGroup, 0, len(groups))
		for _, g := range groups {
			sortedGroups = append(sortedGroups, g)
		}
		sort.Slice(sortedGroups, func(i, j int) bool {
			return len(sortedGroups[i].Agents) > len(sortedGroups[j].Agents)
		})

		for _, g := range sortedGroups {
			fmt.Printf("repo: %s (%s)\n", g.Path, g.Name)
			// Sort agents: main first, then secondary alphabetical by worktree
			sort.Slice(g.Agents, func(i, j int) bool {
				ki, _ := g.Agents[i]["worktree_kind"].(string)
				kj, _ := g.Agents[j]["worktree_kind"].(string)
				if ki == "main" && kj != "main" {
					return true
				}
				if ki != "main" && kj == "main" {
					return false
				}
				wi, _ := g.Agents[i]["worktree"].(string)
				wj, _ := g.Agents[j]["worktree"].(string)
				return wi < wj
			})

			for _, m := range g.Agents {
				kind, _ := m["worktree_kind"].(string)
				prefix := "  worktree: "
				if kind == "main" {
					prefix = "  main:     "
				}
				lastSeenTs, _ := time.Parse(time.RFC3339, m["last_seen_at"].(string))
				lease, _ := m["lease_status"].(string)

				detail := ""
				if kind == "secondary" {
					detail = fmt.Sprintf(" (%s)", m["worktree"])
				}

				fmt.Printf("%s%-16s  %-14s  %-7s  %-12s  %-10s%s\n",
					prefix, m["name"], m["branch"], lease, m["harness"], humanizeDuration(lastSeenTs), detail)

				if wide {
					fmt.Printf("            id:%s  cwd:%s\n", m["agent_id"], m["cwd"])
				}
			}
		}

		if len(outside) > 0 {
			fmt.Println("outside:")
			for _, m := range outside {
				lastSeenTs, _ := time.Parse(time.RFC3339, m["last_seen_at"].(string))
				lease, _ := m["lease_status"].(string)
				project, _ := m["project"].(string)
				if project != "" {
					project = " (--project=" + project + ")"
				}
				fmt.Printf("  %-16s  (cwd: %-20s%s)  %-7s  %-12s  %s\n",
					m["name"], m["cwd"], project, lease, m["harness"], humanizeDuration(lastSeenTs))
				if wide {
					fmt.Printf("            id:%s\n", m["agent_id"])
				}
			}
		}
	})
}

func cmdRooms(args []string) {
	if len(args) >= 1 && args[0] == "gc" {
		cmdRoomsGC(args[1:])
		return
	}
	from := mustCaller(args)
	resp, err := request("rooms", map[string]any{"from": from})
	handle(resp, err, func(data any) {
		rows, ok := data.([]any)
		if !ok || len(rows) == 0 {
			return
		}
		for _, row := range rows {
			m := row.(map[string]any)
			archived := ""
			if v, _ := m["archived"].(bool); v {
				archived = "\tarchived"
			}
			fmt.Printf("%s\tmembers=%v\tmessages=%v%s\n", m["name"], m["members"], m["messages"], archived)
		}
	})
}

func cmdRoomsGC(args []string) {
	from := mustCaller(args)
	resp, err := request("rooms_gc", map[string]any{"from": from})
	handle(resp, err, func(data any) {
		m, _ := data.(map[string]any)
		rows, _ := m["archived"].([]any)
		if len(rows) == 0 {
			fmt.Println("no merged rooms to archive")
			return
		}
		for _, row := range rows {
			rm := row.(map[string]any)
			fmt.Printf("archived slug=%s project=%s\n", rm["slug"], rm["project"])
		}
	})
}

func cmdRoom(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: lalia room create <name> [--desc <text>]")
		os.Exit(1)
	}
	switch args[0] {
	case "create":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "usage: lalia room create <name> [--desc <text>]")
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
		fmt.Fprintln(os.Stderr, "usage: lalia join <room>")
		os.Exit(1)
	}
	from := mustCaller(args)
	resp, err := request("join", map[string]any{"from": from, "room": args[0]})
	handle(resp, err, nil)
}

func cmdLeave(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: lalia leave <room>")
		os.Exit(1)
	}
	from := mustCaller(args)
	resp, err := request("leave", map[string]any{"from": from, "room": args[0]})
	handle(resp, err, nil)
}

func cmdParticipants(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: lalia participants <room>")
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
		fmt.Fprintln(os.Stderr, "usage: lalia post <room> <msg>")
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

// cmdTell: lalia tell <peer> <msg>
// Fire-and-forget. Returns immediately.
func cmdTell(args []string) {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: lalia tell <peer> <msg>")
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

// cmdAsk: lalia ask <peer> <msg> [--timeout N]
// Client-side composition: tell + read. Sends, then blocks up to timeout
// waiting for a reply from the same peer.
func cmdAsk(args []string) {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: lalia ask <peer> <msg> [--timeout N]")
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

// cmdRead: lalia read <target> [--room] [--timeout N]
// Consumes oldest pending message. target is a peer by default; pass --room
// to read from a room with the same name instead.
func cmdRead(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: lalia read <peer|room> [--room] [--timeout N]")
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

// cmdPeek: lalia peek <target> [--room]
func cmdPeek(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: lalia peek <peer|room> [--room]")
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

// cmdReadAny: lalia read-any [--timeout N]
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

// cmdChannels: lalia channels — list caller's peer-pair channels.
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

// cmdHistory: lalia history <target> [--room] [--since N] [--limit N]
func cmdHistory(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: lalia history <peer|room> [--room] [--since N] [--limit N]")
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

// cmdTask routes task subcommands. The project is auto-detected from the
// caller's git environment unless --project is specified explicitly.
func cmdTask(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: lalia task <publish|bulletin|claim|status|unassign|reassign|unpublish|show|list|handoff>")
		os.Exit(1)
	}
	sub := args[0]
	rest := args[1:]

	info := DetectAgentInfo(AgentInfo{})
	detectedProject := projectID(info.RepoURL, info.Project)

	proj := parseFlag(rest, "--project")
	if proj == "" {
		proj = detectedProject
	}
	from := mustCaller(rest)

	switch sub {
	case "publish":
		payloadPath := parseFlag(rest, "--file")
		var raw []byte
		var err error
		if payloadPath == "" || payloadPath == "-" {
			raw, err = io.ReadAll(os.Stdin)
		} else {
			raw, err = os.ReadFile(payloadPath)
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "read publish payload: %v\n", err)
			os.Exit(1)
		}
		var payload map[string]any
		if err := json.Unmarshal(raw, &payload); err != nil {
			fmt.Fprintf(os.Stderr, "parse publish payload: %v\n", err)
			os.Exit(1)
		}
		if payload["project"] == nil || payload["project"] == "" {
			payload["project"] = proj
		}
		if payload["repo_root"] == nil || payload["repo_root"] == "" {
			payload["repo_root"] = info.RepoRoot
		}
		payload["from"] = from
		resp, err := request("task_publish", payload)
		handle(resp, err, func(data any) {
			m := data.(map[string]any)
			fmt.Printf("project=%s repo_root=%s\n", m["project"], m["repo_root"])
			if okRows, _ := m["ok"].([]any); len(okRows) > 0 {
				fmt.Println("ok:")
				for _, o := range okRows {
					om := o.(map[string]any)
					fmt.Printf("  slug=%s worktree=%v noop=%v created=%v\n", om["slug"], om["worktree"], om["noop"], om["created_worktree"])
				}
			}
			if failedRows, _ := m["failed"].([]any); len(failedRows) > 0 {
				fmt.Println("failed:")
				for _, f := range failedRows {
					fm := f.(map[string]any)
					fmt.Printf("  slug=%s error=%s\n", fm["slug"], fm["error"])
				}
			}
		})

	case "bulletin":
		resp, err := request("task_bulletin", map[string]any{
			"from": from, "project": proj,
		})
		handle(resp, err, func(data any) {
			m := data.(map[string]any)
			rows, _ := m["tasks"].([]any)
			if len(rows) == 0 {
				fmt.Printf("no open tasks in project=%s\n", m["project"])
				return
			}
			for _, row := range rows {
				a := row.(map[string]any)
				fmt.Printf("slug=%-24s branch=%-24s status=%s has_context=%v\n",
					a["slug"], a["branch"], a["status"], a["has_context"])
				if summary, _ := a["brief_summary"].(string); summary != "" {
					fmt.Printf("  %s\n", summary)
				}
				if paths, _ := a["owned_paths"].([]any); len(paths) > 0 {
					strs := make([]string, len(paths))
					for i, p := range paths {
						strs[i], _ = p.(string)
					}
					fmt.Printf("  owned: %s\n", strings.Join(strs, ", "))
				}
			}
		})

	case "claim":
		if len(rest) < 1 || rest[0] == "" || rest[0][0] == '-' {
			fmt.Fprintln(os.Stderr, "usage: lalia task claim <slug> [--project <id>]")
			os.Exit(1)
		}
		slug := rest[0]
		resp, err := request("task_claim", map[string]any{
			"from": from, "project": proj, "slug": slug,
		})
		handle(resp, err, func(data any) {
			m := data.(map[string]any)
			fmt.Printf("claimed slug=%s owner=%s status=%s worktree=%v\n", m["slug"], m["owner"], m["status"], m["worktree"])
			if bundle, _ := m["bundle"].(map[string]any); bundle != nil {
				fmt.Printf("---\n[%v %v] %v\n%v\n---\n", bundle["seq"], bundle["ts"], bundle["from"], bundle["body"])
			}
		})

	case "status":
		if len(rest) < 2 {
			fmt.Fprintln(os.Stderr, "usage: lalia task status <slug> <in-progress|ready|blocked|merged> [--project <id>]")
			os.Exit(1)
		}
		slug, status := rest[0], rest[1]
		resp, err := request("task_status", map[string]any{
			"from": from, "project": proj, "slug": slug, "status": status,
		})
		handle(resp, err, func(data any) {
			m := data.(map[string]any)
			fmt.Printf("slug=%s status=%s\n", m["slug"], m["status"])
		})

	case "unassign":
		if len(rest) < 1 || rest[0] == "" || rest[0][0] == '-' {
			fmt.Fprintln(os.Stderr, "usage: lalia task unassign <slug> [--project <id>]")
			os.Exit(1)
		}
		slug := rest[0]
		resp, err := request("task_unassign", map[string]any{
			"from": from, "project": proj, "slug": slug,
		})
		handle(resp, err, func(data any) {
			m := data.(map[string]any)
			fmt.Printf("unassigned slug=%s status=%s\n", m["slug"], m["status"])
		})

	case "reassign":
		if len(rest) < 2 {
			fmt.Fprintln(os.Stderr, "usage: lalia task reassign <slug> <agent> [--project <id>]")
			os.Exit(1)
		}
		slug, owner := rest[0], rest[1]
		resp, err := request("task_reassign", map[string]any{
			"from": from, "project": proj, "slug": slug, "owner": owner,
		})
		handle(resp, err, func(data any) {
			m := data.(map[string]any)
			fmt.Printf("reassigned slug=%s owner=%s status=%s\n", m["slug"], m["owner"], m["status"])
		})

	case "unpublish":
		if len(rest) < 1 || rest[0] == "" || rest[0][0] == '-' {
			fmt.Fprintln(os.Stderr, "usage: lalia task unpublish <slug> [--force] [--wipe-worktree] [--evict-owner] [--project <id>]")
			os.Exit(1)
		}
		slug := rest[0]
		resp, err := request("task_unpublish", map[string]any{
			"from":           from,
			"project":        proj,
			"slug":           slug,
			"force":          parseBoolFlag(rest, "--force"),
			"wipe_worktree":  parseBoolFlag(rest, "--wipe-worktree"),
			"evict_owner":    parseBoolFlag(rest, "--evict-owner"),
		})
		handle(resp, err, func(data any) {
			m := data.(map[string]any)
			fmt.Printf("unpublished slug=%s worktree_removed=%v room_archived=%v",
				m["slug"], m["worktree_removed"], m["room_archived"])
			if preserved, ok := m["worktree_preserved"].(string); ok && preserved != "" {
				fmt.Printf(" worktree_preserved=%s", preserved)
			}
			if remErr, ok := m["worktree_remove_error"].(string); ok && remErr != "" {
				fmt.Printf(" remove_error=%q", remErr)
			}
			fmt.Println()
		})

	case "show":
		slug := ""
		if len(rest) >= 1 && rest[0] != "" && rest[0][0] != '-' {
			slug = rest[0]
		}
		resp, err := request("task_show", map[string]any{
			"from": from, "project": proj, "slug": slug,
		})
		handle(resp, err, func(data any) {
			m := data.(map[string]any)
			if slug != "" {
				fmt.Printf("slug=%s owner=%s status=%s branch=%v worktree=%v\n",
					m["slug"], m["owner"], m["status"], m["branch"], m["worktree"])
				if brief, _ := m["brief"].(string); brief != "" {
					fmt.Printf("---\n%s\n---\n", brief)
				}
				return
			}
			fmt.Printf("project=%s supervisor=%s\n", m["project_id"], m["supervisor"])
			rows, _ := m["tasks"].([]any)
			for _, row := range rows {
				a := row.(map[string]any)
				fmt.Printf("  slug=%-24s owner=%-16s status=%s\n", a["slug"], a["owner"], a["status"])
			}
		})

	case "list":
		resp, err := request("task_list", map[string]any{"from": from})
		handle(resp, err, func(data any) {
			lists, _ := data.([]any)
			for _, p := range lists {
				m := p.(map[string]any)
				fmt.Printf("project=%s supervisor=%s\n", m["project_id"], m["supervisor"])
				rows, _ := m["tasks"].([]any)
				for _, row := range rows {
					a := row.(map[string]any)
					fmt.Printf("  slug=%-24s owner=%-16s status=%s\n", a["slug"], a["owner"], a["status"])
				}
			}
		})

	case "handoff":
		if len(rest) < 1 || rest[0] == "" || rest[0][0] == '-' {
			fmt.Fprintln(os.Stderr, "usage: lalia task handoff <new-supervisor> [--project <id>]")
			os.Exit(1)
		}
		newSup := rest[0]
		resp, err := request("task_handoff", map[string]any{
			"from": from, "project": proj, "to": newSup,
		})
		handle(resp, err, func(data any) {
			m := data.(map[string]any)
			fmt.Printf("project=%s new supervisor=%s\n", m["project"], m["supervisor"])
		})

	default:
		fmt.Fprintf(os.Stderr, "unknown task subcommand: %s\n", sub)
		os.Exit(1)
	}
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

func cmdInit(args []string) {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: lalia init <peer|worker|supervisor>")
		os.Exit(1)
	}
	prompt, err := promptForRole(args[0])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Print(prompt)
}

func cmdPrompt(args []string) {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: lalia prompt <peer|worker|supervisor>")
		os.Exit(1)
	}
	prompt, err := promptForRole(args[0])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Print(prompt)
}

func cmdRun(args []string) {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: lalia run <peer|worker|supervisor> --claude-code|--codex|--copilot [--force] [args...]")
		os.Exit(1)
	}

	role := args[0]
	harness, force, harnessArgs, err := parseRunArgs(args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	if err := runHarness(role, harness, force, harnessArgs); err != nil {
		fmt.Fprintln(os.Stderr, err)
		if ee := new(exec.ExitError); errors.As(err, &ee) {
			os.Exit(ee.ExitCode())
		}
		os.Exit(1)
	}
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
		detail := decodeErrorDetail(resp.Data)
		if resp.Error != "" {
			fmt.Fprintln(os.Stderr, resp.Error)
		} else if detail != nil && detail.Reason != "" {
			fmt.Fprintln(os.Stderr, detail.Reason)
		}
		if detail != nil {
			if detail.Reason != "" && detail.Reason != resp.Error {
				fmt.Fprintf(os.Stderr, "reason: %s\n", detail.Reason)
			}
			if detail.RetryHint != "" {
				fmt.Fprintf(os.Stderr, "hint: %s\n", detail.RetryHint)
			}
			if len(detail.Context) > 0 {
				raw, _ := json.Marshal(detail.Context)
				fmt.Fprintf(os.Stderr, "context: %s\n", raw)
			}
		}
		code := resp.Code
		if code == 0 && detail != nil {
			code = detail.Code
		}
		if code == 0 {
			code = 1
		}
		os.Exit(code)
	}
	if ok != nil && resp.Data != nil {
		ok(resp.Data)
	}
}

func decodeErrorDetail(data any) *ErrorDetail {
	m, ok := data.(map[string]any)
	if !ok {
		return nil
	}
	raw, ok := m["error"].(map[string]any)
	if !ok {
		return nil
	}
	detail := &ErrorDetail{}
	if v, ok := raw["reason"].(string); ok {
		detail.Reason = v
	}
	if v, ok := raw["retry_hint"].(string); ok {
		detail.RetryHint = v
	}
	if v, ok := raw["code"].(float64); ok {
		detail.Code = int(v)
	}
	if v, ok := raw["context"].(map[string]any); ok {
		detail.Context = v
	}
	return detail
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
