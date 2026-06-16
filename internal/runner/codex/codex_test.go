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
	want := []string{"codex", "--ask-for-approval", "never", "exec", "--sandbox", "workspace-write", "--skip-git-repo-check", "--model", "gpt-5.1-codex", "-c", "foo=bar", "-C", "/repo", "--json", "-"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("argv = %#v, want %#v", got, want)
	}
}

func TestArgvNormalizesConfiguredExecOnlyFlags(t *testing.T) {
	run := New(Config{
		Command: []string{"codex", "--ignore-rules"},
		Profiles: map[string]ProfileConfig{
			"dev": {Sandbox: "workspace-write", Approval: "never"},
		},
		DefaultProfile: "dev",
	})
	got := run.argv(runner.RunRequest{Profile: "dev"}, run.cfg.Profiles["dev"])
	want := []string{"codex", "--ask-for-approval", "never", "exec", "--ignore-rules", "--sandbox", "workspace-write", "--skip-git-repo-check", "-"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("argv = %#v, want %#v", got, want)
	}
}

func TestArgvPreservesConfiguredGlobalFlagsBeforeExec(t *testing.T) {
	run := New(Config{
		Command: []string{"codex", "--ask-for-approval", "on-request", "--ignore-rules"},
		Profiles: map[string]ProfileConfig{
			"dev": {Sandbox: "workspace-write"},
		},
		DefaultProfile: "dev",
	})
	got := run.argv(runner.RunRequest{Profile: "dev"}, run.cfg.Profiles["dev"])
	want := []string{"codex", "--ask-for-approval", "on-request", "exec", "--ignore-rules", "--sandbox", "workspace-write", "--skip-git-repo-check", "-"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("argv = %#v, want %#v", got, want)
	}
}

func TestArgvPreservesExecForWrappedCodexCommand(t *testing.T) {
	run := New(Config{
		Command: []string{"env", "CODEX_HOME=/tmp/codex", "codex"},
		Profiles: map[string]ProfileConfig{
			"dev": {Sandbox: "workspace-write"},
		},
		DefaultProfile: "dev",
	})
	got := run.argv(runner.RunRequest{Profile: "dev"}, run.cfg.Profiles["dev"])
	want := []string{"env", "CODEX_HOME=/tmp/codex", "codex", "exec", "--sandbox", "workspace-write", "--skip-git-repo-check", "-"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("argv = %#v, want %#v", got, want)
	}
}

func TestArgvNormalizesWrappedCodexExecOnlyFlags(t *testing.T) {
	run := New(Config{
		Command: []string{"env", "CODEX_HOME=/tmp/codex", "codex", "--ignore-rules", "--ask-for-approval", "on-request"},
		Profiles: map[string]ProfileConfig{
			"dev": {Sandbox: "workspace-write"},
		},
		DefaultProfile: "dev",
	})
	got := run.argv(runner.RunRequest{Profile: "dev"}, run.cfg.Profiles["dev"])
	want := []string{"env", "CODEX_HOME=/tmp/codex", "codex", "--ask-for-approval", "on-request", "exec", "--ignore-rules", "--sandbox", "workspace-write", "--skip-git-repo-check", "-"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("argv = %#v, want %#v", got, want)
	}
}

func TestArgvNormalizesExecOnlyFlagsBeforeExplicitExec(t *testing.T) {
	run := New(Config{
		Command: []string{"codex", "--ignore-rules", "exec", "--json"},
		Profiles: map[string]ProfileConfig{
			"dev": {Sandbox: "workspace-write"},
		},
		DefaultProfile: "dev",
	})
	got := run.argv(runner.RunRequest{Profile: "dev"}, run.cfg.Profiles["dev"])
	want := []string{"codex", "exec", "--ignore-rules", "--json", "--sandbox", "workspace-write", "--skip-git-repo-check", "-"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("argv = %#v, want %#v", got, want)
	}
}

func TestArgvNormalizesExecOnlyFlagAliasesAndEqualsForms(t *testing.T) {
	run := New(Config{
		Command: []string{"codex", "--output-schema=/tmp/schema.json", "--experimental-json", "--full-auto", "-o", "/tmp/out", "--ask-for-approval", "never"},
		Profiles: map[string]ProfileConfig{
			"dev": {Sandbox: "workspace-write"},
		},
		DefaultProfile: "dev",
	})
	got := run.argv(runner.RunRequest{Profile: "dev"}, run.cfg.Profiles["dev"])
	want := []string{"codex", "--ask-for-approval", "never", "exec", "--output-schema=/tmp/schema.json", "--experimental-json", "--full-auto", "-o", "/tmp/out", "--sandbox", "workspace-write", "--skip-git-repo-check", "-"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("argv = %#v, want %#v", got, want)
	}
}

func TestPreviewExecHelpArgvHonorsWrappedCodexCommand(t *testing.T) {
	got := PreviewExecHelpArgv([]string{"env", "CODEX_HOME=/tmp/codex", "codex"})
	want := []string{"env", "CODEX_HOME=/tmp/codex", "codex", "exec", "--help"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("help argv = %#v, want %#v", got, want)
	}
}

func TestArgvKeepsNonCodexCommandPrefixCompatibleWithStdinHarness(t *testing.T) {
	run := New(Config{
		Command: []string{"sh", "-c", "cat"},
		Profiles: map[string]ProfileConfig{
			"dev": {Sandbox: "workspace-write"},
		},
		DefaultProfile: "dev",
	})
	got := run.argv(runner.RunRequest{Profile: "dev"}, run.cfg.Profiles["dev"])
	want := []string{"sh", "-c", "cat", "exec", "--sandbox", "workspace-write", "--skip-git-repo-check", "-"}
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
	want := []string{"codex", "--ask-for-approval", "never", "exec", "--sandbox", "read-only", "--skip-git-repo-check", "-"}
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
	want := []string{"codex", "--ask-for-approval", "never", "exec", "--sandbox", "read-only", "--skip-git-repo-check", "-"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("argv = %#v, want %#v", got, want)
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

func TestArgvDoesNotDuplicateIgnoreRules(t *testing.T) {
	run := New(Config{
		Command: []string{"codex", "exec"},
		Profiles: map[string]ProfileConfig{
			"dev": {Sandbox: "workspace-write", Approval: "never", ExtraArgs: []string{"--ignore-rules"}},
		},
		DefaultProfile: "dev",
	})
	got := run.argv(runner.RunRequest{
		Profile: "dev",
		Metadata: runner.TaskMetadata{
			MCPURL: "http://127.0.0.1:8888/mcp",
		},
	}, run.cfg.Profiles["dev"])
	if countArg(got, "--ignore-rules") != 1 {
		t.Fatalf("argv must contain exactly one --ignore-rules: %#v", got)
	}
}

func countArg(argv []string, needle string) int {
	var count int
	for _, arg := range argv {
		if arg == needle {
			count++
		}
	}
	return count
}
