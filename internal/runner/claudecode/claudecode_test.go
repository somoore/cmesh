package claudecode

import (
	"slices"
	"testing"

	"cmesh/internal/runner"
	"cmesh/internal/runner/runnertest"
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

func TestArgvIncludesPermissionModeAndAllowedTools(t *testing.T) {
	run := New(Config{Command: []string{"claude", "-p"}})
	argv := run.argv(runner.RunRequest{}, ProfileConfig{
		PermissionMode: "acceptEdits",
		AllowedTools:   []string{"Bash", "PowerShell"},
	})

	for _, want := range []string{"--permission-mode", "acceptEdits", "--allowedTools", "Bash,PowerShell"} {
		if !slices.Contains(argv, want) {
			t.Fatalf("argv missing %q: %#v", want, argv)
		}
	}
	if slices.Contains(argv, "--disallowedTools") {
		t.Fatalf("argv unexpectedly disallows tools: %#v", argv)
	}
}

func TestRunSendsPromptOnStdin(t *testing.T) {
	run := New(Config{
		Command: []string{"sh", "-c", "cat"},
		Profiles: map[string]ProfileConfig{
			"readonly": {},
		},
		DefaultProfile: "readonly",
	})
	runnertest.AssertPromptViaStdin(t, run, "task-stdin", "hello from stdin", "readonly")
}
