package codex

import (
	"reflect"
	"strings"
	"testing"

	"cmesh/internal/runner"
	"cmesh/internal/runner/runnertest"
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
	run := New(Config{
		Command: []string{"sh", "-c", "cat"},
		Profiles: map[string]ProfileConfig{
			"readonly": {},
		},
		DefaultProfile: "readonly",
	})
	runnertest.AssertPromptViaStdin(t, run, "task-codex-stdin", "hello from codex stdin", "readonly")
}

func TestArgvBuildsNonInteractiveExecCommand(t *testing.T) {
	run := New(Config{
		Command: []string{"codex"},
		Profiles: map[string]ProfileConfig{
			"dev": {
				Sandbox:  "workspace-write",
				Approval: "never",
				Model:    "gpt-5.1-codex",
				Config: map[string]string{
					"foo": "bar",
				},
				ExtraArgs: []string{"--json"},
			},
		},
		DefaultProfile: "dev",
	})
	got := run.argv(runner.RunRequest{CWD: "/repo", Profile: "dev"}, run.cfg.Profiles["dev"])
	want := []string{"codex", "--ask-for-approval", "never", "exec", "--sandbox", "workspace-write", "--model", "gpt-5.1-codex", "-c", "foo=bar", "-C", "/repo", "--json", "-"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("argv = %#v, want %#v", got, want)
	}
}

func TestArgvDoesNotDuplicateExecSubcommand(t *testing.T) {
	run := New(Config{
		Command: []string{"codex", "exec"},
		Profiles: map[string]ProfileConfig{
			"readonly": {Sandbox: "read-only", Approval: "never"},
		},
		DefaultProfile: "readonly",
	})
	got := run.argv(runner.RunRequest{Profile: "readonly"}, run.cfg.Profiles["readonly"])
	want := []string{"codex", "--ask-for-approval", "never", "exec", "--sandbox", "read-only", "-"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("argv = %#v, want %#v", got, want)
	}
}
