package claudecode

import (
	"context"
	"os/exec"
	"slices"
	"strings"
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

func TestRunSendsPromptOnStdin(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	run := New(Config{
		Command: []string{"sh", "-c", "cat"},
		Profiles: map[string]ProfileConfig{
			"readonly": {},
		},
		DefaultProfile: "readonly",
	})
	result, err := run.Run(runner.RunRequest{
		Context: context.Background(),
		TaskID:  "task-stdin",
		Prompt:  "hello from stdin",
		Profile: "readonly",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(result.WorkerSummaryUntrusted, "hello from stdin") {
		t.Fatalf("summary = %q", result.WorkerSummaryUntrusted)
	}
	for _, arg := range result.Tests.Commands {
		if strings.Contains(arg, "hello from stdin") {
			t.Fatalf("prompt leaked into argv command: %q", arg)
		}
	}
}
