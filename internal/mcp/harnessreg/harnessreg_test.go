package harnessreg

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"cmesh/internal/mcp/setupinstall"
)

// An unconfirmed call must change nothing and must never echo the host token,
// on either harness. The token is written into harness config on confirm, but
// the preview a model sees is always redacted.
func TestPreviewNeverEchoesHostToken(t *testing.T) {
	const secret = "super-secret-host-token"
	for _, harness := range []string{"claude-code", "codex"} {
		t.Run(harness, func(t *testing.T) {
			out := Register(Request{Harness: harness}, "http://127.0.0.1:7777/mcp", secret)
			if !out.RequiresConfirmation {
				t.Fatalf("unconfirmed call must require confirmation: %+v", out)
			}
			for key, value := range out.Fields {
				text, ok := value.(string)
				if !ok {
					continue
				}
				if strings.Contains(text, secret) {
					t.Fatalf("field %q leaked the host token: %s", key, text)
				}
			}
		})
	}
}

// An unsupported harness degrades to manual instructions rather than erroring,
// so a caller on an unrecognized agent is never stranded without a next step.
func TestUnsupportedHarnessDegradesToManual(t *testing.T) {
	out := Register(Request{Harness: "some-new-agent", Confirmed: true}, "http://127.0.0.1:7777/mcp", "")
	if out.Status != "manual" {
		t.Fatalf("status = %q, want manual: %+v", out.Status, out)
	}
	if out.Fields["manual_url"] != "http://127.0.0.1:7777/mcp" {
		t.Fatalf("manual_url missing: %+v", out.Fields)
	}
}

// The Codex writer replaces its own table without disturbing neighbours, and
// writes 0600 because the file now holds the gateway host token.
func TestWriteCodexGatewayConfigReplacesOnlyItsOwnTable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	existing := "[mcp_servers.other]\ncommand = \"other\"\n\n" +
		"[mcp_servers." + setupinstall.ServerName + "]\ncommand = \"stale\"\n\n" +
		"[unrelated]\nkey = \"value\"\n"
	if err := os.WriteFile(path, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := WriteCodexGatewayConfig(path, "http://127.0.0.1:7777/mcp", "tok"); err != nil {
		t.Fatalf("WriteCodexGatewayConfig: %v", err)
	}

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(body)
	if strings.Contains(got, `command = "stale"`) {
		t.Fatalf("stale cmesh entry survived:\n%s", got)
	}
	for _, want := range []string{"[mcp_servers.other]", "[unrelated]", "url = 'http://127.0.0.1:7777/mcp'", "X-Cmesh-Host-Token"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in:\n%s", want, got)
		}
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// Windows has no POSIX permission bits (os.Chmod only toggles read-only), so
	// the 0600 guarantee is asserted where it is meaningful.
	if runtime.GOOS != "windows" {
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Fatalf("config permissions = %o, want 600 (file holds the host token)", perm)
		}
	}
}

// A config with no host token must not write an empty header line.
func TestWriteCodexGatewayConfigOmitsEmptyToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := WriteCodexGatewayConfig(path, "http://127.0.0.1:7777/mcp", "  "); err != nil {
		t.Fatalf("WriteCodexGatewayConfig: %v", err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "http_headers") {
		t.Fatalf("blank token should not write a header:\n%s", body)
	}
}

func TestGatewayURLDefaultsToConfiguredPort(t *testing.T) {
	if got := GatewayURL(9999); got != "http://127.0.0.1:9999/mcp" {
		t.Fatalf("GatewayURL(9999) = %q", got)
	}
	if got := GatewayURL(0); got != "http://127.0.0.1:7777/mcp" {
		t.Fatalf("GatewayURL(0) = %q, want the default MCP port", got)
	}
}

// Codex creates ~/.codex/config.toml itself, so a pre-existing 0644 file must be
// tightened when the host token is written into it.
func TestWriteCodexGatewayConfigTightensExistingMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX file modes")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte("[mcp_servers.other]\ncommand = \"other\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteCodexGatewayConfig(path, "http://127.0.0.1:7777/mcp", "host-token"); err != nil {
		t.Fatalf("WriteCodexGatewayConfig: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("mode = %o, want 600", got)
	}
}

// Registering Claude must not put the host token in a child's argument list: a
// local user can read ps//proc/<pid>/cmdline. The token belongs only in the
// 0600 user config the CLI would have written.
func TestRegisterClaudeKeepsTokenOutOfArgv(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake shell command uses POSIX sh")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	existing := `{"numStartups": 1, "mcpServers": {"other": {"type": "stdio", "command": "other"}}}`
	configPath := filepath.Join(home, ".claude.json")
	if err := os.WriteFile(configPath, []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(home, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(home, "claude-args.log")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$*\" >> '" + logPath + "'\n" +
		"if [ \"$1\" = \"mcp\" ] && [ \"$2\" = \"get\" ]; then\n" +
		"  printf '%s\\n' 'cmesh:' '  Scope: User' '  Type: http' '  URL: http://127.0.0.1:7777/mcp'\n" +
		"fi\n"
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	const secret = "super-secret-host-token"
	out := Register(Request{Harness: "claude-code", Confirmed: true}, "http://127.0.0.1:7777/mcp", secret)
	if out.Status != "registered" {
		t.Fatalf("status = %q fields = %+v", out.Status, out.Fields)
	}
	if logged, err := os.ReadFile(logPath); err == nil && strings.Contains(string(logged), secret) {
		t.Fatalf("host token reached the child argv:\n%s", logged)
	}
	body, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	got := string(body)
	if !strings.Contains(got, secret) || !strings.Contains(got, "X-Cmesh-Host-Token") {
		t.Fatalf("token/header missing from user config:\n%s", got)
	}
	if !strings.Contains(got, "\"other\"") || !strings.Contains(got, "numStartups") {
		t.Fatalf("unrelated Claude config was dropped:\n%s", got)
	}
	info, err := os.Stat(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("config mode = %o, want 600", perm)
	}
}
