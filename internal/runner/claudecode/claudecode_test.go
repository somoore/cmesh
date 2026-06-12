package claudecode

import (
	"slices"
	"testing"

	"cmesh/internal/runner"
)

func TestArgvIncludesTaskMCPConfigStrictly(t *testing.T) {
	run := New(Config{Command: []string{"claude", "-p"}})
	argv := run.argv(runner.RunRequest{
		Metadata: runner.TaskMetadata{MCPConfigFile: "/tmp/cmesh-task-mcp.json"},
	}, ProfileConfig{OutputFormat: "json"})

	if !slices.Contains(argv, "--mcp-config") {
		t.Fatalf("argv missing --mcp-config: %#v", argv)
	}
	if !slices.Contains(argv, "/tmp/cmesh-task-mcp.json") {
		t.Fatalf("argv missing task MCP config path: %#v", argv)
	}
	if !slices.Contains(argv, "--strict-mcp-config") {
		t.Fatalf("argv missing --strict-mcp-config: %#v", argv)
	}
}
