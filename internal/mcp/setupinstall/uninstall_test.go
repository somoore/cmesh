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
		{name: "present with trailing comment", body: "model = \"x\"\n\n[mcp_servers.cmesh] # installed by cmesh\ncommand = 'cmesh'\n", want: true},
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
	t.Setenv("USERPROFILE", dir)
	t.Setenv("PATH", "")
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
	t.Setenv("USERPROFILE", dir)
	t.Setenv("PATH", "")
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
	t.Setenv("USERPROFILE", dir)
	t.Setenv("PATH", "")
	res := Uninstall(Agent{ID: "codex", Name: "Codex"})
	if !res.OK || res.Detail != "not registered" {
		t.Fatalf("expected OK/not registered, got %+v", res)
	}
}

func TestUninstallCodexUsesCodexCLI(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake shell command uses POSIX sh")
	}
	dir := t.TempDir()
	logPath := filepath.Join(dir, "codex.log")
	markerPath := filepath.Join(dir, "registered")
	if err := os.WriteFile(markerPath, []byte("x"), 0o644); err != nil {
		t.Fatalf("seed marker: %v", err)
	}
	script := "#!/bin/sh\n" +
		"PATH=/bin:/usr/bin:$PATH\n" +
		"printf '%s\\n' \"$*\" >> " + shellQuote(logPath) + "\n" +
		"if [ \"$1\" = \"mcp\" ] && [ \"$2\" = \"get\" ]; then [ -e " + shellQuote(markerPath) + " ] && exit 0 || exit 1; fi\n" +
		"if [ \"$1\" = \"mcp\" ] && [ \"$2\" = \"remove\" ]; then rm -f " + shellQuote(markerPath) + "; exit 0; fi\n" +
		"exit 0\n"
	if err := os.WriteFile(filepath.Join(dir, "codex"), []byte(script), 0o755); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	t.Setenv("PATH", dir)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	res := uninstallCodex(Agent{ID: "codex", Name: "Codex"})
	if !res.OK {
		t.Fatalf("uninstallCodex failed: %+v", res)
	}
	body, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.Contains(string(body), "mcp remove cmesh") {
		t.Fatalf("codex mcp remove was not called:\n%s", body)
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

// writeStatefulClaudeStub installs a fake `claude` that models per-scope
// registration. cmesh is "present" (mcp get exits 0) as long as ANY scope marker
// file exists; `mcp remove --scope <s>` deletes that scope's marker (and exits
// non-zero when the marker is already gone, mirroring the real CLI). This lets a
// test prove the sweep removes EVERY scope and that the post-sweep `get` arbiter
// is honored. presentScopes are the scopes that start registered.
func writeStatefulClaudeStub(t *testing.T, dir string, presentScopes ...string) string {
	t.Helper()
	logPath := filepath.Join(dir, "claude.log")
	for _, s := range presentScopes {
		if err := os.WriteFile(filepath.Join(dir, "scope-"+s), []byte("x"), 0o644); err != nil {
			t.Fatalf("seed scope %s: %v", s, err)
		}
	}
	// $0's dir holds the scope-<name> markers; resolve it portably. Set a real
	// PATH first: tests run with PATH=<dir> (so LookPath finds this stub), but the
	// script itself needs rm/printf/etc., which are not all shell builtins.
	script := "#!/bin/sh\n" +
		"PATH=/bin:/usr/bin:$PATH\n" +
		"d=" + shellQuote(dir) + "\n" +
		"printf '%s\\n' \"$*\" >> " + shellQuote(logPath) + "\n" +
		"if [ \"$1\" = \"mcp\" ] && [ \"$2\" = \"get\" ]; then\n" +
		"  for m in \"$d\"/scope-*; do [ -e \"$m\" ] && exit 0; done; exit 1\n" +
		"fi\n" +
		"if [ \"$1\" = \"mcp\" ] && [ \"$2\" = \"remove\" ]; then\n" +
		// args: mcp remove --scope <scope> cmesh
		"  s=\"$4\"\n" +
		"  if [ -e \"$d/scope-$s\" ]; then rm -f \"$d/scope-$s\"; exit 0; fi\n" +
		"  echo \"No $s-scoped MCP server found with name: cmesh\" >&2; exit 1\n" +
		"fi\n" +
		"exit 0\n"
	claudePath := filepath.Join(dir, "claude")
	if err := os.WriteFile(claudePath, []byte(script), 0o755); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return logPath
}

func TestUninstallClaudeRemovesAllScopes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake shell command uses POSIX sh")
	}
	dir := t.TempDir()
	// cmesh registered in BOTH user and local — the real-world case the old
	// user-only removal missed.
	logPath := writeStatefulClaudeStub(t, dir, "user", "local")
	t.Setenv("PATH", dir)

	res := uninstallClaude(Agent{ID: "claude-code", Name: "Claude Code"})
	if !res.OK {
		t.Fatalf("uninstallClaude should succeed once every scope is cleared: %+v", res)
	}
	body, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	log := string(body)
	// Every scope must have been swept (user AND local at minimum).
	for _, scope := range []string{"user", "local", "project"} {
		if !strings.Contains(log, "mcp remove --scope "+scope+" cmesh") {
			t.Fatalf("scope %q was not swept:\n%s", scope, log)
		}
	}
	// Markers gone => the post-sweep arbiter `get` saw nothing.
	for _, scope := range []string{"user", "local"} {
		if _, err := os.Stat(filepath.Join(dir, "scope-"+scope)); !os.IsNotExist(err) {
			t.Fatalf("scope %q marker should be removed", scope)
		}
	}
}

func TestUninstallClaudeReportsFailureWhenScopeRemains(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake shell command uses POSIX sh")
	}
	dir := t.TempDir()
	logPath := filepath.Join(dir, "claude.log")
	// A stub where remove always claims success but never actually clears
	// registration (mcp get stays 0). The post-sweep arbiter must catch this and
	// report failure rather than a false success.
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$*\" >> " + shellQuote(logPath) + "\n" +
		"if [ \"$1\" = \"mcp\" ] && [ \"$2\" = \"get\" ]; then exit 0; fi\n" +
		"exit 0\n"
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte(script), 0o755); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	t.Setenv("PATH", dir)

	res := uninstallClaude(Agent{ID: "claude-code", Name: "Claude Code"})
	if res.OK {
		t.Fatalf("uninstallClaude must NOT report success while cmesh is still registered: %+v", res)
	}
	if !strings.Contains(res.Detail, "still registered") {
		t.Fatalf("failure detail should explain cmesh is still registered, got %q", res.Detail)
	}
}

func TestDetectInstalledAgentsCodexOnly(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
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
