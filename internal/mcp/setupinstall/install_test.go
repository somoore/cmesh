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
	for _, scope := range claudeScopes {
		if !strings.Contains(body, "mcp remove --scope "+scope+" cmesh\n") {
			t.Fatalf("remove command for scope %q missing from log:\n%s", scope, body)
		}
	}
	if !strings.Contains(body, "mcp add --scope user cmesh -- /opt/cmesh mcp --stdio\n") {
		t.Fatalf("setup stdio add command missing from log:\n%s", body)
	}
}

func TestInstallCodexUsesCodexCLI(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake shell command uses POSIX sh")
	}
	dir := t.TempDir()
	logPath := filepath.Join(dir, "codex.log")
	markerPath := filepath.Join(dir, "registered")
	script := "#!/bin/sh\n" +
		"PATH=/bin:/usr/bin:$PATH\n" +
		"printf '%s\\n' \"$*\" >> " + shellQuote(logPath) + "\n" +
		"if [ \"$1\" = \"mcp\" ] && [ \"$2\" = \"get\" ]; then [ -e " + shellQuote(markerPath) + " ] && exit 0 || exit 1; fi\n" +
		"if [ \"$1\" = \"mcp\" ] && [ \"$2\" = \"remove\" ]; then rm -f " + shellQuote(markerPath) + "; exit 0; fi\n" +
		"if [ \"$1\" = \"mcp\" ] && [ \"$2\" = \"add\" ]; then touch " + shellQuote(markerPath) + "; exit 0; fi\n" +
		"exit 0\n"
	if err := os.WriteFile(filepath.Join(dir, "codex"), []byte(script), 0o755); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	t.Setenv("PATH", dir)

	result := installCodex(Agent{ID: "codex", Name: "Codex", Found: true}, "/opt/cmesh")
	if !result.OK {
		t.Fatalf("installCodex failed: %+v", result)
	}
	body, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	log := string(body)
	if !strings.Contains(log, "mcp remove cmesh") {
		t.Fatalf("codex mcp remove was not called:\n%s", log)
	}
	if !strings.Contains(log, "mcp add cmesh -- /opt/cmesh mcp --stdio") {
		t.Fatalf("codex mcp add was not called correctly:\n%s", log)
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

func TestRemoveTablePreservesTablesWithTrailingComment(t *testing.T) {
	// A header following the cmesh table carries a trailing comment, so it does
	// not end in "]". removeTable must still recognize it as a header and stop
	// skipping, preserving the unrelated table and its secrets.
	body := `[mcp_servers.cmesh]
command = "x"
[mcp_servers.other]   # my important server
command = "other-tool"
api_key = "SECRET123"
[some_other_section]
value = 42`

	got := removeTable(body, "mcp_servers.cmesh")

	if strings.Contains(got, "[mcp_servers.cmesh]") || strings.Contains(got, `command = "x"`) {
		t.Fatalf("cmesh table should be removed:\n%s", got)
	}
	if !strings.Contains(got, "[mcp_servers.other]") {
		t.Fatalf("[mcp_servers.other] header was destroyed:\n%s", got)
	}
	if !strings.Contains(got, `api_key = "SECRET123"`) {
		t.Fatalf("api_key secret was destroyed:\n%s", got)
	}
	if !strings.Contains(got, `command = "other-tool"`) {
		t.Fatalf("[mcp_servers.other] contents were destroyed:\n%s", got)
	}
	if !strings.Contains(got, "[some_other_section]") || !strings.Contains(got, "value = 42") {
		t.Fatalf("[some_other_section] was destroyed:\n%s", got)
	}
}

func TestRemoveTablePrefixGuardKeepsSiblingTable(t *testing.T) {
	body := `[mcp_servers.cmesh]
command = "x"
[mcp_servers.cmesh-extra]
command = "extra"`

	got := removeTable(body, "mcp_servers.cmesh")

	if strings.Contains(got, "[mcp_servers.cmesh]\n") {
		t.Fatalf("cmesh table should be removed:\n%s", got)
	}
	if !strings.Contains(got, "[mcp_servers.cmesh-extra]") || !strings.Contains(got, `command = "extra"`) {
		t.Fatalf("cmesh-extra table must survive prefix guard:\n%s", got)
	}
}

func TestRemoveTableStripsCmeshHeaderWithTrailingComment(t *testing.T) {
	body := `[mcp_servers.cmesh]   # cmesh server
command = "x"
[some_other_section]
value = 42`

	got := removeTable(body, "mcp_servers.cmesh")

	if strings.Contains(got, "mcp_servers.cmesh") || strings.Contains(got, `command = "x"`) {
		t.Fatalf("cmesh table with trailing comment should be stripped:\n%s", got)
	}
	if !strings.Contains(got, "[some_other_section]") || !strings.Contains(got, "value = 42") {
		t.Fatalf("[some_other_section] was destroyed:\n%s", got)
	}
}
