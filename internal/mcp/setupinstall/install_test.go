package setupinstall

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestClaudeHTTPAddArgsUsesHTTPTransport(t *testing.T) {
	got := ClaudeHTTPAddArgs(ServerName, "http://127.0.0.1:7777/mcp")
	want := []string{"mcp", "add", "--transport", "http", "cmesh", "http://127.0.0.1:7777/mcp"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ClaudeHTTPAddArgs = %#v, want %#v", got, want)
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
