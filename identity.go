package main

import (
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"
)

// newAgentID generates a fresh ULID for use as an agent_id.
func newAgentID() string {
	entropy := ulid.Monotonic(rand.New(rand.NewSource(time.Now().UnixNano())), 0)
	return ulid.MustNew(ulid.Timestamp(time.Now()), entropy).String()
}

// isULID returns true if s looks like a valid ULID (26 chars, Crockford base32).
func isULID(s string) bool {
	_, err := ulid.ParseStrict(s)
	return err == nil
}

// AgentInfo holds auto-detected fields for a new registration.
type AgentInfo struct {
	Harness        string
	Model          string
	Project        string
	RepoURL        string
	RepoRoot       string // absolute path of the current worktree
	MainRepoRoot   string // absolute path of the main worktree
	WorktreeKind   string // main | secondary | detached | outside
	Worktree       string
	Branch         string
	CWD            string
}

// DetectAgentInfo auto-detects registration metadata from the caller's environment.
// Explicit overrides from CLI flags take priority over auto-detection.
func DetectAgentInfo(overrides AgentInfo) AgentInfo {
	info := AgentInfo{}

	info.CWD = overrides.CWD
	if info.CWD == "" {
		if cwd, err := os.Getwd(); err == nil {
			info.CWD = canonicalPath(cwd)
		}
	}

	info.Worktree = filepath.Base(info.CWD)

	info.Branch = overrides.Branch
	if info.Branch == "" {
		info.Branch = gitOutput("rev-parse", "--abbrev-ref", "HEAD")
	}

	info.RepoURL = overrides.RepoURL
	if info.RepoURL == "" {
		info.RepoURL = gitOutput("config", "--get", "remote.origin.url")
	}

	info.RepoRoot = overrides.RepoRoot
	if info.RepoRoot == "" {
		if root := gitOutput("rev-parse", "--show-toplevel"); root != "" {
			info.RepoRoot = canonicalPath(root)
		}
	}

	// WorktreeKind and MainRepoRoot detection
	if info.RepoRoot != "" {
		commonDir := gitOutput("rev-parse", "--git-common-dir")
		if commonDir != "" {
			// --git-common-dir is relative to CWD if not absolute.
			if !filepath.IsAbs(commonDir) {
				commonDir = filepath.Join(info.CWD, commonDir)
			}
			info.MainRepoRoot = canonicalPath(filepath.Dir(commonDir))

			if info.RepoRoot == info.MainRepoRoot {
				info.WorktreeKind = "main"
			} else {
				info.WorktreeKind = "secondary"
			}
		}
		// Check for detached HEAD
		if info.Branch == "HEAD" {
			info.WorktreeKind = "detached"
		}
	} else {
		info.WorktreeKind = "outside"
	}

	info.Project = overrides.Project
	if info.Project == "" {
		if info.RepoURL != "" {
			// strip trailing .git and take last path segment
			u := strings.TrimSuffix(info.RepoURL, ".git")
			u = strings.TrimSuffix(u, "/")
			parts := strings.Split(u, "/")
			if len(parts) > 0 && parts[len(parts)-1] != "" {
				info.Project = parts[len(parts)-1]
			}
		}
		if info.Project == "" {
			// basename of master repo dir (works for worktrees without remote)
			if info.MainRepoRoot != "" {
				info.Project = filepath.Base(info.MainRepoRoot)
			}
		}
		// If still empty and not in a repo, don't fallback to CWD basename
		// unless explicitly overridden or in a repo.
		if info.Project == "" && info.WorktreeKind != "outside" {
			info.Project = filepath.Base(info.CWD)
		}
	}

	info.Harness = overrides.Harness
	if info.Harness == "" {
		info.Harness = detectHarness()
	}

	info.Model = overrides.Model

	return info
}

// canonicalPath returns the absolute, symlink-evaluated form of a path.
func canonicalPath(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	eval, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return abs
	}
	return eval
}

// detectHarness probes well-known env vars set by agent harnesses.
func detectHarness() string {
	switch {
	case os.Getenv("CLAUDECODE") != "" || os.Getenv("CLAUDE_CODE") != "":
		return "claude-code"
	case os.Getenv("CODEX") != "" || os.Getenv("OPENAI_CODEX") != "":
		return "codex"
	case os.Getenv("CURSOR_TRACE_ID") != "" || os.Getenv("CURSOR_SESSION_ID") != "":
		return "cursor"
	case os.Getenv("AIDER_ENV") != "":
		return "aider"
	case os.Getenv("GITHUB_COPILOT_CLI") != "" || os.Getenv("COPILOT_AGENT") != "":
		return "copilot"
	default:
		return "unknown"
	}
}

// gitOutput runs a git command in the current directory and returns trimmed stdout.
// Returns "" on error or missing git.
func gitOutput(args ...string) string {
	out, err := exec.Command("git", args...).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// qualifiedName returns the fully-qualified form: name@project:branch
// Falls back gracefully if fields are empty.
func qualifiedName(a *Agent) string {
	if a.Project == "" && a.Branch == "" {
		return a.Name
	}
	if a.Branch == "" {
		return a.Name + "@" + a.Project
	}
	return a.Name + "@" + a.Project + ":" + a.Branch
}

// ResolveAddress resolves an agent address string to an agent_id.
// Resolution order (per IDENTITY.md):
//  1. Nickname (checked via nicknames map passed in)
//  2. Bare ULID
//  3. Fully-qualified: name@project, name@project:branch, name@project:branch:worktree
//  4. Bare name (unique → resolve; multiple → error listing candidates)
//
// Returns (agent_id, error). error is non-nil and descriptive on failure.
func (s *State) ResolveAddress(addr string, nicknames map[string]Nickname) (string, error) {
	// 1. Nickname
	if nick, ok := nicknames[addr]; ok {
		if nick.Mode == "follow" {
			// re-resolve the stored address string
			id, err := s.resolveAddressInner(nick.Address, nil)
			if err != nil {
				return "", err
			}
			return id, nil
		}
		// stable: return agent_id directly
		if _, exists := s.agents[nick.AgentID]; !exists {
			return "", fmt.Errorf("nickname %q points to agent_id %s which is no longer registered; reassign with: lalia nickname %s <new-address>", addr, nick.AgentID, addr)
		}
		return nick.AgentID, nil
	}
	return s.resolveAddressInner(addr, nicknames)
}

func (s *State) resolveAddressInner(addr string, _ map[string]Nickname) (string, error) {
	// 2. Bare ULID
	if isULID(addr) {
		if _, ok := s.agents[addr]; ok {
			return addr, nil
		}
		return "", fmt.Errorf("agent_id %s not found", addr)
	}

	// 3. Fully-qualified: name@project[:branch[:worktree]]
	if strings.Contains(addr, "@") {
		parts := strings.SplitN(addr, "@", 2)
		name := parts[0]
		rest := parts[1] // project[:branch[:worktree]]
		rparts := strings.SplitN(rest, ":", 3)
		project := rparts[0]
		branch := ""
		worktree := ""
		if len(rparts) >= 2 {
			branch = rparts[1]
		}
		if len(rparts) >= 3 {
			worktree = rparts[2]
		}
		var matches []*Agent
		for _, a := range s.agents {
			if a.Name != name {
				continue
			}
			if project != "" && a.Project != project {
				continue
			}
			if branch != "" && a.Branch != branch {
				continue
			}
			if worktree != "" && a.Worktree != worktree {
				continue
			}
			matches = append(matches, a)
		}
		switch len(matches) {
		case 0:
			return "", fmt.Errorf("agent %q not found", addr)
		case 1:
			return matches[0].AgentID, nil
		default:
			return "", ambiguousError(addr, matches)
		}
	}

	// 4. Bare name
	var matches []*Agent
	for _, a := range s.agents {
		if a.Name == addr {
			matches = append(matches, a)
		}
	}
	switch len(matches) {
	case 0:
		return "", fmt.Errorf("agent %q not found", addr)
	case 1:
		return matches[0].AgentID, nil
	default:
		return "", ambiguousError(addr, matches)
	}
}

func ambiguousError(addr string, matches []*Agent) error {
	forms := make([]string, len(matches))
	for i, a := range matches {
		forms[i] = qualifiedName(a)
	}
	return fmt.Errorf("ambiguous address %q: matches %s", addr, strings.Join(forms, ", "))
}
