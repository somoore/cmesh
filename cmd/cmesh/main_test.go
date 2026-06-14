package main

import (
	"context"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cmesh/internal/audit"
	"cmesh/internal/config"
	"cmesh/internal/doctor"
	"cmesh/internal/gitx"
	"cmesh/internal/mcp/setupinstall"
	"cmesh/internal/runner/claudecode"
	"cmesh/internal/store"
	"cmesh/internal/task"
	"cmesh/internal/testutil"
)

func TestActiveRunnerNamesUsesProfileReferencesOnly(t *testing.T) {
	cfg := &config.Config{Profiles: map[string]config.ProfileConfig{
		"dev":      {Runner: "codex"},
		"readonly": {Runner: "codex"},
		"network":  {Runner: "claude-code"},
		"empty":    {},
	}}
	got := strings.Join(activeRunnerNames(cfg), ",")
	if got != "claude-code,codex" {
		t.Fatalf("activeRunnerNames = %q, want claude-code,codex", got)
	}
}

func TestCodexSandboxForProfileKeepsDenyWriteReadOnlyOnWindows(t *testing.T) {
	got := codexSandboxForProfile(config.ProfileConfig{Writes: "deny"}, "windows")
	if got != "read-only" {
		t.Fatalf("deny-write Windows Codex profile sandbox = %q, want read-only", got)
	}
	got = codexSandboxForProfile(config.ProfileConfig{Writes: "repo_only"}, "windows")
	if got != "danger-full-access" {
		t.Fatalf("repo-write Windows Codex profile sandbox = %q, want danger-full-access", got)
	}
}

func TestClaudeProfileForDenyWriteDisallowsWriteAndShellTools(t *testing.T) {
	cfg := &config.Config{
		Profiles: map[string]config.ProfileConfig{
			"readonly": {
				Runner:                claudecode.Name,
				Claims:                []string{"read_files"},
				Writes:                "deny",
				Shell:                 "deny",
				EnforcementConfidence: "medium",
			},
		},
		Runners: map[string]config.RunnerConfig{
			claudecode.Name: {Command: []string{"claude"}, DefaultProfile: "readonly"},
		},
	}
	reg, err := buildRunners(cfg)
	if err != nil {
		t.Fatalf("buildRunners: %v", err)
	}
	run, ok := reg.Get(claudecode.Name)
	if !ok {
		t.Fatal("claude-code runner not registered")
	}
	flags := strings.Join(run.Manifest().Profiles["readonly"].NativeFlags, " ")
	for _, want := range []string{"--permission-mode plan", "--disallowedTools", "Bash", "Write", "Edit"} {
		if !strings.Contains(flags, want) {
			t.Fatalf("native flags %q missing %q", flags, want)
		}
	}
}

func TestFailedCheckSummaryIncludesCheckDetails(t *testing.T) {
	got := failedCheckSummary([]doctor.Check{
		{Name: "ok", OK: true, Detail: "fine"},
		{Name: "runner.codex.smoke", OK: false, Err: "timeout"},
		{Name: "tailnet_ip", OK: false, Detail: "missing"},
	})
	want := "runner.codex.smoke=timeout; tailnet_ip=missing"
	if got != want {
		t.Fatalf("failedCheckSummary = %q, want %q", got, want)
	}
}

func TestCodexSandboxForProfileUsesServiceSafeWindowsMode(t *testing.T) {
	if got := codexSandboxForProfile(config.ProfileConfig{}, "windows"); got != "danger-full-access" {
		t.Fatalf("windows codex sandbox = %q, want danger-full-access", got)
	}
	if got := codexSandboxForProfile(config.ProfileConfig{}, "linux"); got != "workspace-write" {
		t.Fatalf("linux codex sandbox = %q, want workspace-write", got)
	}
}

func TestCleanupTaskArtifactsRemovesRegisteredWorktreeBeforeBranch(t *testing.T) {
	ctx := context.Background()
	testutil.EnsureGitOnPath(t)
	root := t.TempDir()
	testutil.RunGit(t, root, "init")
	testutil.RunGit(t, root, "config", "user.name", "Test User")
	testutil.RunGit(t, root, "config", "user.email", "test@example.com")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	testutil.RunGit(t, root, "add", "README.md")
	testutil.RunGit(t, root, "commit", "-m", "initial")

	dataDir := t.TempDir()
	worktreePath := filepath.Join(dataDir, "worktrees", "task-1")
	branch := "cmesh/task-1/test"
	repo, err := gitx.Open(root)
	if err != nil {
		t.Fatalf("Open repo: %v", err)
	}
	if err := repo.AddWorktree(ctx, worktreePath, branch, "HEAD"); err != nil {
		t.Fatalf("AddWorktree: %v", err)
	}

	rec := task.Record{
		ID:  "task-1",
		CWD: root,
		Result: &task.ResultEnvelope{
			Branch:      branch,
			RunnerExtra: map[string]any{"worktree_path": filepath.ToSlash(worktreePath)},
		},
	}
	if err := cleanupTaskArtifacts(ctx, dataDir, rec); err != nil {
		t.Fatalf("cleanupTaskArtifacts: %v", err)
	}

	if out := testutil.RunGitOutput(t, root, "branch", "--list", branch); strings.TrimSpace(out) != "" {
		t.Fatalf("branch still exists: %q", out)
	}
	if out := testutil.RunGitOutput(t, root, "worktree", "list", "--porcelain"); strings.Contains(filepath.ToSlash(out), filepath.ToSlash(worktreePath)) {
		t.Fatalf("worktree registration still exists: %s", out)
	}
}

func TestParseInterspersedFlagsAllowsQuickstartOrder(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	cwd := fs.String("cwd", "~", "")
	timeoutSeconds := fs.Int("timeout", 60, "")
	wait := fs.Bool("wait", true, "")

	if err := parseInterspersedFlags(fs, []string{"homelab-vm", "--cwd", "/tmp/myapp", "--timeout=20", "--wait=false"}); err != nil {
		t.Fatalf("parseInterspersedFlags: %v", err)
	}
	if fs.NArg() != 1 || fs.Arg(0) != "homelab-vm" {
		t.Fatalf("args = %q, want homelab-vm", fs.Args())
	}
	if *cwd != "/tmp/myapp" || *timeoutSeconds != 20 || *wait {
		t.Fatalf("parsed cwd=%q timeout=%d wait=%v", *cwd, *timeoutSeconds, *wait)
	}
}

func TestSelectMCPInstallAgentsAll(t *testing.T) {
	found := []setupinstall.Agent{
		{ID: "claude-code", Name: "Claude Code", Found: true},
		{ID: "codex", Name: "Codex", Found: true},
	}
	selected, err := selectMCPInstallAgents("all", found, len(found)+1)
	if err != nil {
		t.Fatalf("selectMCPInstallAgents: %v", err)
	}
	if len(selected) != len(found) {
		t.Fatalf("selected %d agents, want %d", len(selected), len(found))
	}
}

func TestSelectMCPInstallAgentsRequiresDetectedAgent(t *testing.T) {
	_, err := selectMCPInstallAgents("codex", []setupinstall.Agent{
		{ID: "claude-code", Name: "Claude Code", Found: true},
	}, 2)
	if err == nil {
		t.Fatal("selectMCPInstallAgents returned nil error")
	}
	if !strings.Contains(err.Error(), "not detected") {
		t.Fatalf("error = %v", err)
	}
}

func TestResolveWindowsServicePasswordSources(t *testing.T) {
	if _, err := resolveWindowsServicePassword(serviceInstallOptions{Password: "secret"}); err == nil {
		t.Fatal("password without user should fail")
	}
	if _, err := resolveWindowsServicePassword(serviceInstallOptions{User: `.\scott`}); err == nil {
		t.Fatal("user without password source should fail")
	}
	t.Setenv("CMESH_TEST_SERVICE_PASSWORD", "secret")
	got, err := resolveWindowsServicePassword(serviceInstallOptions{User: `.\scott`, PasswordEnv: "CMESH_TEST_SERVICE_PASSWORD"})
	if err != nil {
		t.Fatalf("resolve env password: %v", err)
	}
	if got != "secret" {
		t.Fatalf("password = %q", got)
	}
}

func TestSchemaSmokeRequestUsesLightweightPrompt(t *testing.T) {
	req := schemaSmokeRequest(&config.Config{Node: config.NodeConfig{ID: "origin"}}, "peer", "/tmp/repo", "readonly", true, 30)
	if req.Prompt != "Reply with exactly: cmesh smoke ok" {
		t.Fatalf("prompt = %q", req.Prompt)
	}
	if req.CWD != "/tmp/repo" || req.Profile != "readonly" || req.TimeoutSeconds != 30 || !req.Wait {
		t.Fatalf("request = %+v", req)
	}
}

func TestSmokeResultMatchesExpectedResponse(t *testing.T) {
	if !smokeResultMatches(&task.ResultEnvelope{CommandOutput: task.CommandOutput{Command: "runner", ExitCode: 0, Stdout: "cmesh smoke ok\n"}}, "readonly") {
		t.Fatal("expected raw command artifact to match")
	}
	if smokeResultMatches(&task.ResultEnvelope{CommandOutput: task.CommandOutput{Command: "runner", ExitCode: 1, Stdout: "cmesh smoke ok\n"}}, "readonly") {
		t.Fatal("non-zero command artifact must not pass smoke validation")
	}
	if smokeResultMatches(&task.ResultEnvelope{WorkerSummaryUntrusted: "cmesh smoke ok\n"}, "readonly") {
		t.Fatal("model summary without command_output must not pass smoke validation")
	}
	if smokeResultMatches(&task.ResultEnvelope{WorkerSummaryUntrusted: "What would you like me to work on?"}, "readonly") {
		t.Fatal("generic worker response must not pass smoke validation")
	}
	if smokeResultMatches(nil, "readonly") {
		t.Fatal("nil result must not pass smoke validation")
	}
}

func TestFailClosedRestartMarksQueuedAndRunningTasksFailed(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "cmesh.db"))
	if err != nil {
		t.Fatalf("Open store: %v", err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Fatalf("Close store: %v", err)
		}
	})
	for _, id := range []string{"queued-task", "running-task"} {
		if _, err := st.BeginDispatch(ctx, task.Record{
			ID:               id,
			OriginNodeID:     "origin",
			OriginSessionID:  "session",
			WorkerNodeID:     "worker",
			State:            task.StateQueued,
			CWD:              t.TempDir(),
			Runner:           "fake",
			ProfileRequested: "dev",
			ProfileEffective: "dev",
			PromptHash:       task.PromptHash(id),
		}, []byte(id), time.Hour); err != nil {
			t.Fatalf("BeginDispatch %s: %v", id, err)
		}
	}
	if _, err := st.UpdateTaskState(ctx, "running-task", store.StateUpdate{State: task.StateRunning}); err != nil {
		t.Fatalf("mark running: %v", err)
	}
	writer := audit.NewWriter(filepath.Join(t.TempDir(), "audit.jsonl"), "worker")
	if err := failClosedRestart(ctx, t.TempDir(), st, writer); err != nil {
		t.Fatalf("failClosedRestart: %v", err)
	}
	for _, id := range []string{"queued-task", "running-task"} {
		rec, err := st.GetTask(ctx, id)
		if err != nil {
			t.Fatalf("GetTask %s: %v", id, err)
		}
		if rec.State != task.StateFailed || rec.TerminalErrorCode != task.ErrorWorkerDaemonRestarted {
			t.Fatalf("%s state/code = %s/%s, want failed/%s", id, rec.State, rec.TerminalErrorCode, task.ErrorWorkerDaemonRestarted)
		}
	}
}
