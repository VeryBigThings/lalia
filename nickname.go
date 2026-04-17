package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// Nickname represents a user-assigned alias for an agent.
type Nickname struct {
	Mode    string `json:"mode"`               // "stable" or "follow"
	AgentID string `json:"agent_id,omitempty"` // set for mode=stable
	Address string `json:"address"`            // original address string (always stored)
}

// nicknamesPath returns the path to the user's nickname file.
// Stored outside the workspace: nicknames are personal UX sugar, not audit data.
func nicknamesPath() string {
	return filepath.Join(leschDir(), "nicknames.json")
}

// loadNicknames reads the nicknames file. Returns an empty map if missing.
func loadNicknames() (map[string]Nickname, error) {
	b, err := os.ReadFile(nicknamesPath())
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]Nickname{}, nil
		}
		return nil, err
	}
	var m map[string]Nickname
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("nicknames.json parse error: %w", err)
	}
	return m, nil
}

// saveNicknames writes the nicknames map atomically.
func saveNicknames(m map[string]Nickname) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := os.MkdirAll(filepath.Dir(nicknamesPath()), 0700); err != nil {
		return err
	}
	tmp := nicknamesPath() + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, nicknamesPath())
}

// cmdNickname implements: kopos nickname [<nick> [<address>]] [-d <nick>] [--follow]
func cmdNickname(args []string) {
	if len(args) == 0 {
		// list all nicknames
		nicknames, err := loadNicknames()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		if len(nicknames) == 0 {
			return
		}
		keys := make([]string, 0, len(nicknames))
		for k := range nicknames {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			n := nicknames[k]
			if n.Mode == "stable" {
				fmt.Printf("%s\t%s\t(stable → %s)\n", k, n.Address, n.AgentID)
			} else {
				fmt.Printf("%s\t%s\t(follows)\n", k, n.Address)
			}
		}
		return
	}

	// -d <nick>: delete
	if args[0] == "-d" {
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "usage: kopos nickname -d <nick>")
			os.Exit(1)
		}
		nick := args[1]
		nicknames, err := loadNicknames()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		if _, ok := nicknames[nick]; !ok {
			fmt.Fprintln(os.Stderr, "nickname not found: "+nick)
			os.Exit(5)
		}
		delete(nicknames, nick)
		if err := saveNicknames(nicknames); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}

	nick := args[0]

	if len(args) == 1 {
		// show what nickname resolves to
		nicknames, err := loadNicknames()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		n, ok := nicknames[nick]
		if !ok {
			fmt.Fprintln(os.Stderr, "nickname not found: "+nick)
			os.Exit(5)
		}
		if n.Mode == "stable" {
			fmt.Printf("%s → agent_id %s (stable)\n  address: %s\n", nick, n.AgentID, n.Address)
		} else {
			fmt.Printf("%s → %s (follows)\n", nick, n.Address)
		}
		return
	}

	// assign: kopos nickname [--follow] <nick> <address>
	follow := false
	remaining := args
	for i, a := range remaining {
		if a == "--follow" {
			follow = true
			remaining = append(remaining[:i], remaining[i+1:]...)
			break
		}
	}
	if len(remaining) < 2 {
		fmt.Fprintln(os.Stderr, "usage: kopos nickname [--follow] <nick> <address>")
		os.Exit(1)
	}
	nick = remaining[0]
	address := remaining[1]

	nicknames, err := loadNicknames()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	if follow {
		nicknames[nick] = Nickname{Mode: "follow", Address: address}
	} else {
		// stable: resolve address to agent_id now via daemon
		resp, err := request("resolve", map[string]any{"address": address})
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		if !resp.OK {
			fmt.Fprintln(os.Stderr, resp.Error)
			os.Exit(resp.Code)
		}
		agentID, _ := resp.Data.(map[string]any)["agent_id"].(string)
		nicknames[nick] = Nickname{Mode: "stable", AgentID: agentID, Address: address}
	}

	if err := saveNicknames(nicknames); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println(nick)
}
