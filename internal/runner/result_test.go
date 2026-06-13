package runner

import (
	"strings"
	"testing"
	"time"
)

func TestResultFromProcessParsesStructuredOutput(t *testing.T) {
	proc := CompletedProcess{
		ExitCode:      0,
		Stdout:        `{"result":"done cleanly","session_id":"sess-123","usage":{"input_tokens":10,"output_tokens":15,"cost_usd":0.42}}`,
		Combined:      `json wrapper should not be the summary`,
		Duration:      3 * time.Second,
		TranscriptRef: "transcript:task-1",
	}
	result := ResultFromProcess("claude-code", proc, []string{"claude", "-p", "prompt"}, "prompt")
	if result.WorkerSummaryUntrusted != "done cleanly" {
		t.Fatalf("summary = %q", result.WorkerSummaryUntrusted)
	}
	if result.CommandOutput.ExitCode != 0 || result.CommandOutput.Stdout != proc.Stdout || result.CommandOutput.Stderr != proc.Stderr {
		t.Fatalf("command output = %+v", result.CommandOutput)
	}
	if result.CommandOutput.Command != "claude -p <prompt>" {
		t.Fatalf("command = %q", result.CommandOutput.Command)
	}
	if result.Usage.Tokens != 25 || result.Usage.Cost != 0.42 || result.Usage.DurationS != 3 {
		t.Fatalf("usage = %+v", result.Usage)
	}
	if result.Tests.ClaimedPassed {
		t.Fatalf("exit code alone should not claim tests passed")
	}
	if result.RunnerExtra["session_id"] != "sess-123" {
		t.Fatalf("session id not captured: %+v", result.RunnerExtra)
	}
	if strings.Contains(result.WorkerSummaryUntrusted, "json wrapper") {
		t.Fatalf("summary used combined JSON noise: %q", result.WorkerSummaryUntrusted)
	}
}

func TestRedactedCommandOnlyRedactsPromptWhenPresent(t *testing.T) {
	if got := RedactedCommand([]string{"codex", "exec", "--sandbox", "read-only"}, "hello"); got != "codex exec --sandbox read-only" {
		t.Fatalf("command = %q", got)
	}
	if got := RedactedCommand([]string{"legacy", "hello"}, "hello"); got != "legacy <prompt>" {
		t.Fatalf("command = %q", got)
	}
}
