package main

import (
	_ "embed"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const (
	managedPromptMarker = "<!-- lalia:managed -->"
	copilotBeginMarker  = "<!-- lalia-begin -->"
	copilotEndMarker    = "<!-- lalia-end -->"
)

//go:embed prompts/worker.md
var workerPrompt string

//go:embed prompts/supervisor.md
var supervisorPrompt string

//go:embed prompts/peer.md
var peerPrompt string

func parseRunArgs(args []string) (harness string, force bool, harnessArgs []string, err error) {
	for _, a := range args {
		switch a {
		case "--claude-code", "--codex", "--copilot":
			if harness != "" {
				return "", false, nil, fmt.Errorf("multiple harness flags provided")
			}
			harness = a
		case "--force":
			force = true
		default:
			harnessArgs = append(harnessArgs, a)
		}
	}
	if harness == "" {
		return "", false, nil, fmt.Errorf("missing harness flag: use --claude-code, --codex, or --copilot")
	}
	return harness, force, harnessArgs, nil
}

func runHarness(role, harness string, force bool, harnessArgs []string) error {
	prompt, err := promptForRole(role)
	if err != nil {
		return err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}

	laliaPath := filepath.Join(cwd, "LALIA.md")
	switch harness {
	case "--claude-code":
		if err := writeManagedPromptFile(laliaPath, prompt, force); err != nil {
			return err
		}
		return runExternal("claude", append([]string{"--append-system-prompt-file", "LALIA.md"}, harnessArgs...))
	case "--codex":
		if err := writeManagedPromptFile(laliaPath, prompt, force); err != nil {
			return err
		}
		configArg := fmt.Sprintf("experimental_instructions_file=%q", laliaPath)
		errText, err := runExternalCaptureStderr("codex", append([]string{"-c", configArg}, harnessArgs...))
		if err == nil {
			return nil
		}
		if !isCodexConfigKeyError(errText) {
			return err
		}

		agentsPath := filepath.Join(cwd, "AGENTS.md")
		if err := writeManagedPromptFile(agentsPath, prompt, force); err != nil {
			return err
		}
		return runExternal("codex", harnessArgs)
	case "--copilot":
		path := filepath.Join(cwd, ".github", "copilot-instructions.md")
		if err := writeCopilotPromptFile(path, prompt, force); err != nil {
			return err
		}
		return runExternal("copilot", harnessArgs)
	default:
		return fmt.Errorf("unknown harness flag: %s", harness)
	}
}

func promptForRole(role string) (string, error) {
	switch role {
	case "peer":
		return peerPrompt, nil
	case "worker":
		return workerPrompt, nil
	case "supervisor":
		return supervisorPrompt, nil
	default:
		return "", fmt.Errorf("unknown role: %s (expected peer, worker or supervisor)", role)
	}
}

func writeManagedPromptFile(path, prompt string, force bool) error {
	existing, hasExisting, err := readExisting(path)
	if err != nil {
		return err
	}
	if hasExisting && !force && !hasManagedFirstLine(existing) {
		return fmt.Errorf("refusing to overwrite unmarked %s; rerun with --force", path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(renderManagedPrompt(prompt)), 0644)
}

func writeCopilotPromptFile(path, prompt string, force bool) error {
	section := renderCopilotSection(prompt)
	existing, hasExisting, err := readExisting(path)
	if err != nil {
		return err
	}

	switch {
	case !hasExisting:
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			return err
		}
		return os.WriteFile(path, []byte(section), 0644)
	case strings.Contains(existing, copilotBeginMarker):
		updated := upsertCopilotSection(existing, section)
		return os.WriteFile(path, []byte(updated), 0644)
	case force:
		return os.WriteFile(path, []byte(section), 0644)
	default:
		return fmt.Errorf("refusing to overwrite unmarked %s; rerun with --force", path)
	}
}

func renderManagedPrompt(prompt string) string {
	body := strings.TrimLeft(prompt, "\n")
	return managedPromptMarker + "\n\n" + ensureTrailingNewline(body)
}

func renderCopilotSection(prompt string) string {
	return copilotBeginMarker + "\n" + ensureTrailingNewline(prompt) + copilotEndMarker + "\n"
}

func ensureTrailingNewline(s string) string {
	if strings.HasSuffix(s, "\n") {
		return s
	}
	return s + "\n"
}

func hasManagedFirstLine(content string) bool {
	first := content
	if i := strings.IndexByte(first, '\n'); i >= 0 {
		first = first[:i]
	}
	return strings.TrimSpace(first) == managedPromptMarker
}

func readExisting(path string) (string, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", false, nil
		}
		return "", false, err
	}
	return string(data), true, nil
}

func upsertCopilotSection(existing, section string) string {
	start := strings.Index(existing, copilotBeginMarker)
	if start < 0 {
		if existing == "" {
			return section
		}
		return ensureTrailingNewline(existing) + "\n" + section
	}
	afterBegin := existing[start+len(copilotBeginMarker):]
	endRel := strings.Index(afterBegin, copilotEndMarker)
	if endRel < 0 {
		return strings.TrimRight(existing[:start], "\n") + "\n" + section
	}
	end := start + len(copilotBeginMarker) + endRel + len(copilotEndMarker)
	tail := existing[end:]
	tail = strings.TrimLeft(tail, "\n")
	if tail == "" {
		return strings.TrimRight(existing[:start], "\n") + "\n" + section
	}
	return strings.TrimRight(existing[:start], "\n") + "\n" + section + "\n" + tail
}

func runExternal(name string, args []string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return err
	}
	return nil
}

func runExternalCaptureStderr(name string, args []string) (string, error) {
	var stderr strings.Builder
	cmd := exec.Command(name, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = io.MultiWriter(os.Stderr, &stderr)
	err := cmd.Run()
	return stderr.String(), err
}

func isCodexConfigKeyError(stderr string) bool {
	lower := strings.ToLower(stderr)
	if !strings.Contains(lower, "experimental_instructions_file") {
		return false
	}
	markers := []string{"unknown", "unrecognized", "invalid", "not found", "no such", "unsupported"}
	for _, m := range markers {
		if strings.Contains(lower, m) {
			return true
		}
	}
	return false
}
