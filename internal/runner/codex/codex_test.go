package codex

import (
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
