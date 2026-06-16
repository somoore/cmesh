package codex

import (
	"reflect"
	"slices"
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
	want := []string{"codex", "--ask-for-approval", "never", "--sandbox", "workspace-write", "exec", "--skip-git-repo-check", "--model", "gpt-5.1-codex", "-c", "foo=bar", "-c", "sandbox_mode=\"workspace-write\"", "-C", "/repo", "--json", "-"}
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
	want := []string{"codex", "--ask-for-approval", "never", "--sandbox", "read-only", "exec", "--skip-git-repo-check", "-c", "sandbox_mode=\"read-only\"", "-"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("argv = %#v, want %#v", got, want)
	}
}

func TestArgvDoesNotDuplicateSkipGitRepoCheck(t *testing.T) {
	run := New(Config{
		Command: []string{"codex"},
		Profiles: map[string]ProfileConfig{
			"readonly": {Sandbox: "read-only", Approval: "never", ExtraArgs: []string{"--skip-git-repo-check"}},
		},
		DefaultProfile: "readonly",
	})
	got := run.argv(runner.RunRequest{Profile: "readonly"}, run.cfg.Profiles["readonly"])
	want := []string{"codex", "--ask-for-approval", "never", "--sandbox", "read-only", "exec", "-c", "sandbox_mode=\"read-only\"", "--skip-git-repo-check", "-"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("argv = %#v, want %#v", got, want)
	}
}

func TestArgvProfileControlsReplaceConfiguredSandboxAndApproval(t *testing.T) {
	run := New(Config{
		Command: []string{"codex", "--sandbox", "read-only", "exec", "--ask-for-approval", "on-request"},
		Profiles: map[string]ProfileConfig{
			"dev": {Sandbox: "workspace-write", Approval: "never"},
		},
		DefaultProfile: "dev",
	})

	got := run.argv(runner.RunRequest{Profile: "dev"}, run.cfg.Profiles["dev"])
	wantPrefix := []string{"codex", "--ask-for-approval", "never", "--sandbox", "workspace-write", "exec"}
	if len(got) < len(wantPrefix) || !reflect.DeepEqual(got[:len(wantPrefix)], wantPrefix) {
		t.Fatalf("argv prefix = %#v, want prefix %#v (full argv %#v)", got[:min(len(got), len(wantPrefix))], wantPrefix, got)
	}
	joined := strings.Join(got, "\x00")
	for _, stale := range []string{"read-only", "on-request"} {
		if strings.Contains(joined, stale) {
			t.Fatalf("argv retained stale configured control %q: %#v", stale, got)
		}
	}
	if strings.Count(joined, "--sandbox") != 1 || strings.Count(joined, "--ask-for-approval") != 1 {
		t.Fatalf("argv duplicated profile controls: %#v", got)
	}
}

func TestArgvProfileSandboxReplacesConfiguredSandboxMode(t *testing.T) {
	run := New(Config{
		Command: []string{"codex"},
		Profiles: map[string]ProfileConfig{
			"dev": {
				Sandbox:  "workspace-write",
				Approval: "never",
				Config: map[string]string{
					"sandbox_mode": `"read-only"`,
				},
			},
		},
		DefaultProfile: "dev",
	})

	got := run.argv(runner.RunRequest{Profile: "dev"}, run.cfg.Profiles["dev"])
	joined := strings.Join(got, "\x00")
	if strings.Contains(joined, "sandbox_mode=\"read-only\"") {
		t.Fatalf("argv retained stale sandbox_mode: %#v", got)
	}
	if strings.Count(joined, "sandbox_mode=\"workspace-write\"") != 1 {
		t.Fatalf("argv must include one authoritative profile sandbox_mode: %#v", got)
	}
}

func TestArgvProfileControlsRemoveStaleExtraArgs(t *testing.T) {
	run := New(Config{
		Command: []string{"codex"},
		Profiles: map[string]ProfileConfig{
			"dev": {
				Sandbox:  "workspace-write",
				Approval: "never",
				ExtraArgs: []string{
					"--sandbox", "read-only",
					"--ask-for-approval", "on-request",
					"-c", "sandbox_mode=\"read-only\"",
					"--config=sandbox_mode=\"read-only\"",
					"--json",
				},
			},
		},
		DefaultProfile: "dev",
	})

	got := run.argv(runner.RunRequest{Profile: "dev"}, run.cfg.Profiles["dev"])
	joined := strings.Join(got, "\x00")
	for _, stale := range []string{"read-only", "on-request"} {
		if strings.Contains(joined, stale) {
			t.Fatalf("argv retained stale extra arg control %q: %#v", stale, got)
		}
	}
	if strings.Count(joined, "sandbox_mode=\"workspace-write\"") != 1 {
		t.Fatalf("argv must include one authoritative profile sandbox_mode: %#v", got)
	}
	if !slices.Contains(got, "--json") {
		t.Fatalf("argv removed unrelated extra arg: %#v", got)
	}
}

func TestArgvInjectsTaskScopedMCPWithoutLeakingToken(t *testing.T) {
	run := New(Config{
		Command: []string{"codex"},
		Profiles: map[string]ProfileConfig{
			"dev": {Sandbox: "workspace-write", Approval: "never"},
		},
		DefaultProfile: "dev",
	})

	got := run.argv(runner.RunRequest{
		Profile: "dev",
		Metadata: runner.TaskMetadata{
			MCPURL:    "http://127.0.0.1:8888/mcp",
			TaskToken: "secret-task-token",
		},
	}, run.cfg.Profiles["dev"])

	for _, want := range []string{
		"--ignore-user-config",
		"--ignore-rules",
		"mcp_servers.cmesh.url=\"http://127.0.0.1:8888/mcp\"",
		"mcp_servers.cmesh.bearer_token_env_var=\"CMESH_TASK_TOKEN\"",
		"mcp_servers.cmesh.startup_timeout_sec=120",
	} {
		if !slices.Contains(got, want) {
			t.Fatalf("argv missing %q: %#v", want, got)
		}
	}
	if strings.Contains(strings.Join(got, " "), "secret-task-token") {
		t.Fatalf("argv leaked task token: %#v", got)
	}
}

func TestArgvMovesConfiguredExecOnlyFlagsAfterExec(t *testing.T) {
	run := New(Config{
		Command: []string{"codex", "--ignore-rules"},
		Profiles: map[string]ProfileConfig{
			"dev": {Sandbox: "workspace-write", Approval: "never"},
		},
		DefaultProfile: "dev",
	})

	got := run.argv(runner.RunRequest{
		Profile: "dev",
		Metadata: runner.TaskMetadata{
			MCPURL: "http://127.0.0.1:8888/mcp",
		},
	}, run.cfg.Profiles["dev"])

	execAt := slices.Index(got, "exec")
	rulesAt := slices.Index(got, "--ignore-rules")
	if execAt < 0 || rulesAt < 0 {
		t.Fatalf("argv missing exec or --ignore-rules: %#v", got)
	}
	if rulesAt <= execAt {
		t.Fatalf("--ignore-rules must be an exec flag after exec: %#v", got)
	}
	if strings.Count(strings.Join(got, "\x00"), "--ignore-rules") != 1 {
		t.Fatalf("argv duplicated --ignore-rules: %#v", got)
	}
}
