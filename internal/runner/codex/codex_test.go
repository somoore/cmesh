package codex

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	"cmesh/internal/runner"
)

func TestRunRejectsResumeSessionID(t *testing.T) {
	run := New(Config{Command: []string{"codex"}, Profiles: map[string]ProfileConfig{
		"dev": {Claims: []string{"read_files"}},
	}})
	_, err := run.Run(runner.RunRequest{
		Prompt:  "continue",
		Profile: "dev",
		Metadata: runner.TaskMetadata{
			ResumeSessionID: "sess-123",
		},
	})
	if err == nil {
		t.Fatal("Run returned nil error")
	}
	if runner.CodeOf(err) != runner.ErrWorkerFailed {
		t.Fatalf("error code = %q, want %q", runner.CodeOf(err), runner.ErrWorkerFailed)
	}
	if !strings.Contains(err.Error(), "does not support resume_session_id") {
		t.Fatalf("error = %v", err)
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
		Prompt:  "hello from codex stdin",
		Profile: "readonly",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(result.WorkerSummaryUntrusted, "hello from codex stdin") {
		t.Fatalf("summary = %q", result.WorkerSummaryUntrusted)
	}
	for _, arg := range result.Tests.Commands {
		if strings.Contains(arg, "hello from codex stdin") {
			t.Fatalf("prompt leaked into argv command: %q", arg)
		}
	}
}
