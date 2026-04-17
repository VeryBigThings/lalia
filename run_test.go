package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInitWorkerMatchesEmbeddedPrompt(t *testing.T) {
	got, err := promptForRole("worker")
	if err != nil {
		t.Fatalf("promptForRole(worker): %v", err)
	}
	wantBytes, err := os.ReadFile(filepath.Join("prompts", "worker.md"))
	if err != nil {
		t.Fatalf("read prompts/worker.md: %v", err)
	}
	if got != string(wantBytes) {
		t.Fatalf("init worker prompt mismatch")
	}
}

func TestPromptWritesKOPOSAndRespectsOverwriteMarker(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	prompt, err := promptForRole("worker")
	if err != nil {
		t.Fatalf("promptForRole: %v", err)
	}

	path := filepath.Join(dir, "KOPOS.md")
	if err := writeManagedPromptFile(path, prompt, false); err != nil {
		t.Fatalf("writeManagedPromptFile create: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read KOPOS.md: %v", err)
	}
	if !strings.HasPrefix(string(data), managedPromptMarker+"\n") {
		t.Fatalf("KOPOS.md missing managed marker prefix")
	}

	if err := os.WriteFile(path, []byte("custom content\n"), 0644); err != nil {
		t.Fatalf("seed custom KOPOS.md: %v", err)
	}
	if err := writeManagedPromptFile(path, prompt, false); err == nil {
		t.Fatalf("expected overwrite refusal for unmarked file")
	}
	if err := writeManagedPromptFile(path, prompt, true); err != nil {
		t.Fatalf("force overwrite should succeed: %v", err)
	}
}

func TestRunHarnessClaudeWritesPromptAndExecsHarness(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	stubBin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(stubBin, 0755); err != nil {
		t.Fatalf("mkdir stub bin: %v", err)
	}
	logPath := filepath.Join(dir, "claude.log")
	writeStub(t, filepath.Join(stubBin, "claude"), logPath)
	t.Setenv("PATH", stubBin+string(os.PathListSeparator)+os.Getenv("PATH"))

	if err := runHarness("worker", "--claude-code", false, []string{"--dry-run"}); err != nil {
		t.Fatalf("runHarness claude: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "KOPOS.md")); err != nil {
		t.Fatalf("KOPOS.md not written: %v", err)
	}
	args := readStubArgs(t, logPath)
	want := []string{"--append-system-prompt-file", "KOPOS.md", "--dry-run"}
	if strings.Join(args, "\n") != strings.Join(want, "\n") {
		t.Fatalf("claude argv=%v want=%v", args, want)
	}
}

func TestRunHarnessCodexWritesPromptAndExecsWithConfigOverride(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	stubBin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(stubBin, 0755); err != nil {
		t.Fatalf("mkdir stub bin: %v", err)
	}
	logPath := filepath.Join(dir, "codex.log")
	writeStub(t, filepath.Join(stubBin, "codex"), logPath)
	t.Setenv("PATH", stubBin+string(os.PathListSeparator)+os.Getenv("PATH"))

	if err := runHarness("worker", "--codex", false, []string{"--continue"}); err != nil {
		t.Fatalf("runHarness codex: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "KOPOS.md")); err != nil {
		t.Fatalf("KOPOS.md not written: %v", err)
	}
	args := readStubArgs(t, logPath)
	if len(args) < 3 {
		t.Fatalf("codex argv too short: %v", args)
	}
	if args[0] != "-c" {
		t.Fatalf("codex arg0=%q want -c", args[0])
	}
	if !strings.HasPrefix(args[1], "experimental_instructions_file=") {
		t.Fatalf("codex config override missing: %q", args[1])
	}
	if !strings.Contains(args[1], filepath.Join(dir, "KOPOS.md")) {
		t.Fatalf("codex config override path missing: %q", args[1])
	}
	if args[2] != "--continue" {
		t.Fatalf("codex passthrough arg missing: %v", args)
	}
}

func TestRunHarnessCopilotRefusesUnmarkedFileWithoutForce(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	path := filepath.Join(dir, ".github", "copilot-instructions.md")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir .github: %v", err)
	}
	if err := os.WriteFile(path, []byte("manual instructions\n"), 0644); err != nil {
		t.Fatalf("seed copilot file: %v", err)
	}

	err := runHarness("worker", "--copilot", false, nil)
	if err == nil {
		t.Fatalf("expected refusal for unmarked copilot instructions")
	}
	if !strings.Contains(err.Error(), "refusing to overwrite unmarked") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestColdPathsInitPromptRunWithoutDaemonOrRegister(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("KOPOS_HOME", filepath.Join(dir, "kopos-home"))
	t.Setenv("KOPOS_WORKSPACE", filepath.Join(dir, "workspace"))

	if _, err := promptForRole("worker"); err != nil {
		t.Fatalf("promptForRole worker: %v", err)
	}
	if err := writeManagedPromptFile(filepath.Join(dir, "KOPOS.md"), workerPrompt, false); err != nil {
		t.Fatalf("writeManagedPromptFile: %v", err)
	}

	stubBin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(stubBin, 0755); err != nil {
		t.Fatalf("mkdir stub bin: %v", err)
	}
	writeStub(t, filepath.Join(stubBin, "claude"), filepath.Join(dir, "claude.log"))
	writeStub(t, filepath.Join(stubBin, "codex"), filepath.Join(dir, "codex.log"))
	writeStub(t, filepath.Join(stubBin, "copilot"), filepath.Join(dir, "copilot.log"))
	t.Setenv("PATH", stubBin+string(os.PathListSeparator)+os.Getenv("PATH"))

	if err := runHarness("worker", "--claude-code", false, nil); err != nil {
		t.Fatalf("cold run claude: %v", err)
	}
	if err := runHarness("worker", "--codex", false, nil); err != nil {
		t.Fatalf("cold run codex: %v", err)
	}
	if err := runHarness("worker", "--copilot", false, nil); err != nil {
		t.Fatalf("cold run copilot: %v", err)
	}
}

func writeStub(t *testing.T, path, logPath string) {
	t.Helper()
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"" + logPath + "\"\n"
	if err := os.WriteFile(path, []byte(script), 0755); err != nil {
		t.Fatalf("write stub %s: %v", path, err)
	}
}

func readStubArgs(t *testing.T, logPath string) []string {
	t.Helper()
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read stub log %s: %v", logPath, err)
	}
	raw := strings.TrimSpace(string(data))
	if raw == "" {
		return nil
	}
	return strings.Split(raw, "\n")
}
