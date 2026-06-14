package setupinstall

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestClaudeHTTPAddArgsUsesHTTPTransport(t *testing.T) {
	got := ClaudeHTTPAddArgs(ServerName, "http://127.0.0.1:7777/mcp", "")
	want := []string{"mcp", "add", "--transport", "http", "cmesh", "http://127.0.0.1:7777/mcp"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ClaudeHTTPAddArgs = %#v, want %#v", got, want)
	}
}

func TestClaudeHTTPAddArgsIncludesHostTokenHeader(t *testing.T) {
	got := ClaudeHTTPAddArgs(ServerName, "http://127.0.0.1:7777/mcp", " secret ")
	want := []string{"mcp", "add", "--transport", "http", "cmesh", "http://127.0.0.1:7777/mcp", "--header", "X-Cmesh-Host-Token: secret"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ClaudeHTTPAddArgs = %#v, want %#v", got, want)
	}
	redacted := RedactClaudeHTTPAddArgs(got)
	if strings.Contains(strings.Join(redacted, " "), "secret") {
		t.Fatalf("redacted args leaked token: %#v", redacted)
	}
}

func TestShellCommandQuotesHeaderArgs(t *testing.T) {
	got := ShellCommand([]string{"claude", "mcp", "add", "--header", "X-Cmesh-Host-Token: <redacted>", "cmesh"})
	want := "claude mcp add --header 'X-Cmesh-Host-Token: <redacted>' cmesh"
	if got != want {
		t.Fatalf("ShellCommand = %q, want %q", got, want)
	}
}

func TestInstallClaudeRegistersSetupStdioServer(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake shell command uses POSIX sh")
	}
	dir := t.TempDir()
	logPath := filepath.Join(dir, "claude-args.log")
	claudePath := filepath.Join(dir, "claude")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + shellQuote(logPath) + "\n"
	if err := os.WriteFile(claudePath, []byte(script), 0o755); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	result := installClaude(Agent{ID: "claude-code", Name: "Claude Code", Found: true}, "/opt/cmesh")
	if !result.OK {
		t.Fatalf("installClaude failed: %+v", result)
	}
	bodyBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	body := string(bodyBytes)
	if !strings.Contains(body, "mcp remove --scope user cmesh\n") {
		t.Fatalf("remove command missing from log:\n%s", body)
	}
	if !strings.Contains(body, "mcp add --scope user cmesh -- /opt/cmesh mcp --stdio\n") {
		t.Fatalf("setup stdio add command missing from log:\n%s", body)
	}
}

func TestUpsertCodexConfigReplacesExistingCmeshOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	existing := `model = "gpt-5"

[mcp_servers.cmesh]
command = 'old'
args = ['old']

[mcp_servers.other]
command = 'other'
args = ['serve']
`
	if err := os.WriteFile(path, []byte(existing), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := UpsertCodexConfig(path, `C:\tools\cmesh.exe`); err != nil {
		t.Fatalf("UpsertCodexConfig: %v", err)
	}
	bodyBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	body := string(bodyBytes)
	if strings.Contains(body, "old") {
		t.Fatalf("old cmesh block still present:\n%s", body)
	}
	if !strings.Contains(body, "[mcp_servers.other]") {
		t.Fatalf("other MCP server was removed:\n%s", body)
	}
	if !strings.Contains(body, "[mcp_servers.cmesh]") || !strings.Contains(body, "args = [\"mcp\", \"--stdio\"]") {
		t.Fatalf("new cmesh block missing:\n%s", body)
	}
}
