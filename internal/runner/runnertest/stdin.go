package runnertest

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	"cmesh/internal/runner"
)

func AssertPromptViaStdin(t testing.TB, run runner.Runner, taskID, prompt, profile string) {
	t.Helper()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	result, err := run.Run(runner.RunRequest{
		Context: context.Background(),
		TaskID:  taskID,
		Prompt:  prompt,
		Profile: profile,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(result.WorkerSummaryUntrusted, prompt) {
		t.Fatalf("summary = %q", result.WorkerSummaryUntrusted)
	}
	for _, arg := range result.Tests.Commands {
		if strings.Contains(arg, prompt) {
			t.Fatalf("prompt leaked into argv command: %q", arg)
		}
	}
}
