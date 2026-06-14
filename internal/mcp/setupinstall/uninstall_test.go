package setupinstall

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestCodexHasCmeshTable(t *testing.T) {
	tests := []struct {
		name string
		body string
		want bool
	}{
		{name: "present", body: "model = \"x\"\n\n[mcp_servers.cmesh]\ncommand = 'cmesh'\n", want: true},
		{name: "absent", body: "model = \"x\"\n\n[mcp_servers.other]\n", want: false},
		{name: "empty", body: "", want: false},
		{name: "inline-not-matched", body: "foo = '[mcp_servers.cmesh]'\n", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := codexHasCmeshTable(tt.body); got != tt.want {
				t.Fatalf("codexHasCmeshTable = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestUninstallCodexStripsCmeshOnly(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	// CodexConfigPath uses ~/.codex/config.toml; point Uninstall at our temp via
	// the same HOME so the detector and remover agree.
	codexDir := filepath.Join(dir, ".codex")
	if err := os.MkdirAll(codexDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	path := filepath.Join(codexDir, "config.toml")
	existing := `model = "gpt-5"

[mcp_servers.cmesh]
command = 'cmesh'
args = ['mcp', '--stdio']

[mcp_servers.other]
command = 'other'
`
	if err := os.WriteFile(path, []byte(existing), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	res := Uninstall(Agent{ID: "codex", Name: "Codex"})
	if !res.OK {
		t.Fatalf("Uninstall failed: %+v", res)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if strings.Contains(string(body), "[mcp_servers.cmesh]") {
		t.Fatalf("cmesh table still present:\n%s", body)
	}
	if !strings.Contains(string(body), "[mcp_servers.other]") {
		t.Fatalf("other table removed:\n%s", body)
	}
}

func TestUninstallCodexIdempotentWhenAbsent(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	codexDir := filepath.Join(dir, ".codex")
	if err := os.MkdirAll(codexDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	path := filepath.Join(codexDir, "config.toml")
	if err := os.WriteFile(path, []byte("model = \"x\"\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	res := Uninstall(Agent{ID: "codex", Name: "Codex"})
	if !res.OK || res.Detail != "not registered" {
		t.Fatalf("expected OK/not registered, got %+v", res)
	}
}

func TestUninstallCodexIdempotentWhenNoConfig(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	res := Uninstall(Agent{ID: "codex", Name: "Codex"})
	if !res.OK || res.Detail != "not registered" {
		t.Fatalf("expected OK/not registered, got %+v", res)
	}
}

func TestUninstallClaudeIdempotentWhenAbsent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake shell command uses POSIX sh")
	}
	dir := t.TempDir()
	claudePath := filepath.Join(dir, "claude")
	// A claude that exits 1 for `mcp get` (cmesh not registered).
	script := "#!/bin/sh\nif [ \"$1\" = \"mcp\" ] && [ \"$2\" = \"get\" ]; then exit 1; fi\nexit 0\n"
	if err := os.WriteFile(claudePath, []byte(script), 0o755); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	t.Setenv("PATH", dir)
	res := uninstallClaude(Agent{ID: "claude-code", Name: "Claude Code"})
	if !res.OK || res.Detail != "not registered" {
		t.Fatalf("expected OK/not registered, got %+v", res)
	}
}

func TestUninstallClaudeRemovesWhenPresent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake shell command uses POSIX sh")
	}
	dir := t.TempDir()
	logPath := filepath.Join(dir, "claude.log")
	claudePath := filepath.Join(dir, "claude")
	// `mcp get` exits 0 (present); everything else logs and exits 0.
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = \"mcp\" ] && [ \"$2\" = \"get\" ]; then exit 0; fi\n" +
		"printf '%s\\n' \"$*\" >> " + shellQuote(logPath) + "\n" +
		"exit 0\n"
	if err := os.WriteFile(claudePath, []byte(script), 0o755); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	t.Setenv("PATH", dir)
	res := uninstallClaude(Agent{ID: "claude-code", Name: "Claude Code"})
	if !res.OK {
		t.Fatalf("uninstallClaude failed: %+v", res)
	}
	body, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.Contains(string(body), "mcp remove --scope user cmesh") {
		t.Fatalf("remove command missing from log:\n%s", body)
	}
}

func TestDetectInstalledAgentsCodexOnly(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	// Empty PATH so no real claude/codex binaries are detected.
	t.Setenv("PATH", "")
	codexDir := filepath.Join(dir, ".codex")
	if err := os.MkdirAll(codexDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(codexDir, "config.toml"), []byte("[mcp_servers.cmesh]\ncommand='c'\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	agents := DetectInstalledAgents()
	if len(agents) != 1 || agents[0].ID != "codex" {
		t.Fatalf("DetectInstalledAgents = %+v, want single codex", agents)
	}
}
