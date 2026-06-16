package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"cmesh/internal/audit"
	"cmesh/internal/config"
	"cmesh/internal/doctor"
	"cmesh/internal/gitx"
	"cmesh/internal/mcp/setupinstall"
	"cmesh/internal/onboard"
	profiledefs "cmesh/internal/profile"
	"cmesh/internal/runner"
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
	got := codexSandboxForProfile(config.ProfileConfig{Writes: "deny"}, "windows", true)
	if got != "read-only" {
		t.Fatalf("deny-write Windows Codex profile sandbox = %q, want read-only", got)
	}
	got = codexSandboxForProfile(config.ProfileConfig{Writes: "repo_only"}, "windows", true)
	if got != "danger-full-access" {
		t.Fatalf("repo-write Windows Codex profile sandbox with opt-in = %q, want danger-full-access", got)
	}
	got = codexSandboxForProfile(config.ProfileConfig{Writes: "repo_only"}, "windows", false)
	if got != "workspace-write" {
		t.Fatalf("repo-write Windows Codex profile sandbox without opt-in = %q, want workspace-write", got)
	}
}

func TestCodexTaskScopedDoctorChecksRequireIgnoreRulesAfterExec(t *testing.T) {
	t.Setenv("CMESH_TEST_CODEX_HELPER", "1")
	cfg := &config.Config{
		Profiles: map[string]config.ProfileConfig{
			profiledefs.RepoDev: {
				Runner: "codex",
				Writes: profiledefs.WritesRepoOnly,
			},
		},
	}
	rcfg := config.RunnerConfig{
		Command:        []string{os.Args[0], "-test.run=TestCodexHelpHelper", "--", "--ignore-rules"},
		DefaultProfile: profiledefs.RepoDev,
	}

	checks := codexTaskScopedDoctorChecks(context.Background(), cfg, rcfg)
	got := map[string]doctor.Check{}
	for _, check := range checks {
		got[check.Name] = check
	}
	for _, name := range []string{
		"runner.codex.task_scoped_ignore_rules",
		"runner.codex.task_scoped_sandbox",
		"runner.codex.task_scoped_sandbox_mode",
		"runner.codex.exec_ignore_rules_supported",
	} {
		check, ok := got[name]
		if !ok {
			t.Fatalf("missing check %q in %+v", name, checks)
		}
		if !check.OK {
			t.Fatalf("%s failed: %+v", name, check)
		}
	}
}

func TestDoctorRunnerChecksAllowPathCommandOutsideServiceMode(t *testing.T) {
	root := t.TempDir()
	helper := filepath.Join(root, "codex-helper")
	body := "#!/bin/sh\nprintf 'Options:\\n      --ignore-rules\\n'\n"
	if err := os.WriteFile(helper, []byte(body), 0o700); err != nil {
		t.Fatalf("write helper: %v", err)
	}
	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))

	cfg := &config.Config{
		Profiles: map[string]config.ProfileConfig{
			profiledefs.RepoDev: {
				Runner: "codex",
				Writes: profiledefs.WritesRepoOnly,
			},
		},
		Runners: map[string]config.RunnerConfig{
			"codex": {
				Command:        []string{"codex-helper"},
				DefaultProfile: profiledefs.RepoDev,
			},
		},
	}

	checks := doctorRunnerChecks(context.Background(), cfg, "codex", false, time.Second, false)
	byName := map[string]doctor.Check{}
	for _, check := range checks {
		byName[check.Name] = check
	}
	pathCheck, ok := byName["runner executable absolute"]
	if !ok {
		t.Fatalf("missing runner executable absolute check: %+v", checks)
	}
	if !pathCheck.OK {
		t.Fatalf("top-level doctor path command should not fail absolute check: %+v", pathCheck)
	}
	if !strings.Contains(pathCheck.Detail, "resolved via PATH") {
		t.Fatalf("absolute check detail = %q, want PATH resolution", pathCheck.Detail)
	}

	serviceChecks := doctorRunnerChecks(context.Background(), cfg, "codex", false, time.Second, true)
	for _, check := range serviceChecks {
		if check.Name == "runner executable absolute" && check.OK {
			t.Fatalf("service-mode path command unexpectedly passed absolute check: %+v", check)
		}
	}
}

func TestCodexRuntimeSandboxCheckMatchesBanner(t *testing.T) {
	check := codexRuntimeSandboxCheck("workspace-write", runner.Result{
		CommandOutput: runner.CommandOutput{
			Stderr: "OpenAI Codex\n--------\nsandbox: workspace-write [workdir]\n",
		},
	})
	if !check.OK {
		t.Fatalf("sandbox check failed: %+v", check)
	}
}

func TestCodexRuntimeSandboxCheckRejectsReadOnlyBanner(t *testing.T) {
	check := codexRuntimeSandboxCheck("workspace-write", runner.Result{
		CommandOutput: runner.CommandOutput{
			Stderr: "OpenAI Codex\n--------\nsandbox: read-only\n",
		},
	})
	if check.OK {
		t.Fatalf("sandbox check unexpectedly passed: %+v", check)
	}
	if !strings.Contains(check.Detail, "sandbox: read-only") || !strings.Contains(check.Err, "workspace-write") {
		t.Fatalf("sandbox check did not report mismatch: %+v", check)
	}
}

func TestDoctorCommandJSONIncludesCodexTaskScopedChecks(t *testing.T) {
	t.Setenv("CMESH_TEST_CODEX_HELPER", "1")
	root := t.TempDir()
	configPath := filepath.Join(root, "config.toml")
	body := `[node]
id = "test"
name = "test"
role = "both"
mesh_port = 7778
mcp_port = 7777
data_dir = ` + strconv.Quote(filepath.ToSlash(filepath.Join(root, "data"))) + `

[policy]
allowed_peers = ["worker"]
allowed_cwds = [` + strconv.Quote(filepath.ToSlash(root)) + `]

[profiles.repo-dev]
runner = "codex"
writes = "repo_only"
shell = "allow"
claims = ["read_files", "edit_repo", "execute_in_repo"]
risk = "high"
require_explicit = true
require_lease = true

[runners.codex]
command = [` + strconv.Quote(os.Args[0]) + `, "-test.run=TestCodexHelpHelper", "--", "--ignore-rules"]
default_profile = "repo-dev"

[peers.worker]
name = "worker"
node_id = "worker"
address = "127.0.0.1"
port = 7778
`
	if err := os.WriteFile(configPath, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	output, err := captureStdout(t, func() error {
		return cmdDoctor([]string{"--config", configPath, "--skip-runner-smoke", "--json"})
	})
	if err != nil {
		t.Fatalf("cmdDoctor: %v\n%s", err, output)
	}
	var payload struct {
		Runners []doctor.Check `json:"runners"`
	}
	if err := json.Unmarshal([]byte(output), &payload); err != nil {
		t.Fatalf("unmarshal doctor output: %v\n%s", err, output)
	}
	byName := map[string]doctor.Check{}
	for _, check := range payload.Runners {
		byName[check.Name] = check
	}
	for _, name := range []string{
		"runner.codex.task_scoped_ignore_rules",
		"runner.codex.task_scoped_sandbox",
		"runner.codex.task_scoped_sandbox_mode",
		"runner.codex.exec_ignore_rules_supported",
	} {
		check, ok := byName[name]
		if !ok {
			t.Fatalf("doctor output missing %q: %+v", name, payload.Runners)
		}
		if !check.OK {
			t.Fatalf("doctor check %q failed: %+v", name, check)
		}
	}
}

func captureStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	defer func() {
		os.Stdout = old
		_ = r.Close()
	}()
	err = fn()
	if closeErr := w.Close(); closeErr != nil && err == nil {
		err = closeErr
	}
	out, readErr := io.ReadAll(r)
	if readErr != nil && err == nil {
		err = readErr
	}
	return string(out), err
}

func TestCodexHelpHelper(t *testing.T) {
	if os.Getenv("CMESH_TEST_CODEX_HELPER") != "1" {
		return
	}
	args := os.Args
	for i, arg := range args {
		if arg == "--" {
			args = args[i+1:]
			break
		}
	}
	if !testContains(args, "exec") || !testContains(args, "--ignore-rules") || !testContains(args, "--help") {
		os.Exit(2)
	}
	fmt.Fprintln(os.Stdout, "Run Codex non-interactively\n\nOptions:\n      --ignore-rules")
	os.Exit(0)
}

func TestProfileConfigureCommandAddsRepoDev(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "config.toml")
	body := `[node]
id = "origin"
name = "origin"
role = "origin"
mesh_port = 7778
mcp_port = 7777
data_dir = "` + filepath.ToSlash(filepath.Join(root, "data")) + `"

[policy]
allowed_peers = ["win11-desktop"]
allowed_cwds = ["` + filepath.ToSlash(root) + `"]

[profiles.query]
runner = "probe"
capabilities = ["disk_info", "system_info", "repo_status"]
writes = "deny"
shell = "deny"

[runners.probe]
command = ["builtin"]
default_profile = "query"

[peers.win11-desktop]
name = "win11-desktop"
node_id = "win11-desktop"
address = "127.0.0.1"
port = 7778
`
	if err := os.WriteFile(configPath, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	err := cmdProfile([]string{"configure", "repo-dev", "--config", configPath, "--runner", "codex", "--peer", "win11-desktop", "--confirmed"})
	if err != nil {
		t.Fatalf("profile configure: %v", err)
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	prof, ok := cfg.Profiles["repo-dev"]
	if !ok {
		t.Fatalf("repo-dev profile missing: %#v", cfg.Profiles)
	}
	if prof.Runner != "codex" || !testContains(prof.AllowedPeers, "win11-desktop") || !prof.RequireLease {
		t.Fatalf("repo-dev profile = %#v", prof)
	}
	if _, ok := cfg.Runners["codex"]; !ok {
		t.Fatalf("codex runner missing: %#v", cfg.Runners)
	}
}

func TestDiscoveredPeerLineShowsOnlineCmeshProbeFailure(t *testing.T) {
	got := discoveredPeerLine(onboard.PeerCandidate{
		Name:    "win11-desktop",
		Address: "100.107.159.54",
		Online:  true,
		Detail:  `Get "http://100.107.159.54:7778/v1/hello": context deadline exceeded`,
	})
	want := `  - win11-desktop  100.107.159.54  online  no cmesh (Get "http://100.107.159.54:7778/v1/hello": context deadline exceeded)`
	if got != want {
		t.Fatalf("discoveredPeerLine() = %q, want %q", got, want)
	}
}

func testContains(values []string, needle string) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
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
	for _, want := range []string{"--permission-mode plan", "--disallowedTools", "Bash", "PowerShell", "Write", "Edit"} {
		if !strings.Contains(flags, want) {
			t.Fatalf("native flags %q missing %q", flags, want)
		}
	}
}

func TestClaudeProfileForRepoDevScopesEditsToCwdAndAllowsShellForWindowsCommand(t *testing.T) {
	cfg := &config.Config{
		Profiles: map[string]config.ProfileConfig{
			profiledefs.RepoDev: {
				Runner:                claudecode.Name,
				Claims:                []string{profiledefs.CapReadFiles, profiledefs.CapEditRepo, profiledefs.CapExecuteInRepo},
				Writes:                profiledefs.WritesRepoOnly,
				Shell:                 profiledefs.ShellAllow,
				RequireExplicit:       true,
				RequireLease:          true,
				EnforcementConfidence: "medium",
			},
		},
		Runners: map[string]config.RunnerConfig{
			claudecode.Name: {Command: []string{`C:\Users\scott\AppData\Roaming\npm\claude.cmd`, "-p"}, DefaultProfile: profiledefs.RepoDev},
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
	flags := strings.Join(run.Manifest().Profiles[profiledefs.RepoDev].NativeFlags, " ")
	for _, want := range []string{"--allowedTools", "Bash", "PowerShell", "Write(**)", "Edit(**)", "MultiEdit(**)", "NotebookEdit(**)"} {
		if !strings.Contains(flags, want) {
			t.Fatalf("native flags %q missing %q", flags, want)
		}
	}
	// Edits must be cwd-scoped, not a filesystem-wide blanket allow or acceptEdits.
	if strings.Contains(flags, "acceptEdits") {
		t.Fatalf("native flags %q must not use acceptEdits (it bypasses cwd scope)", flags)
	}
	for _, unwanted := range []string{"--disallowedTools", "--permission-mode plan"} {
		if strings.Contains(flags, unwanted) {
			t.Fatalf("native flags %q unexpectedly contain %q", flags, unwanted)
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

func TestCodexSandboxForProfileUsesWindowsDangerOnlyForWriteProfiles(t *testing.T) {
	if got := codexSandboxForProfile(config.ProfileConfig{}, "windows", false); got != "workspace-write" {
		t.Fatalf("windows codex sandbox without opt-in = %q, want workspace-write", got)
	}
	if got := codexSandboxForProfile(config.ProfileConfig{}, "windows", true); got != "danger-full-access" {
		t.Fatalf("windows codex sandbox with opt-in = %q, want danger-full-access", got)
	}
	if got := codexSandboxForProfile(config.ProfileConfig{}, "linux", false); got != "workspace-write" {
		t.Fatalf("linux codex sandbox = %q, want workspace-write", got)
	}
	if got := codexSandboxForProfile(config.ProfileConfig{}, "darwin", false); got != "workspace-write" {
		t.Fatalf("darwin codex sandbox = %q, want workspace-write", got)
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

type fakeShellSmokeRunner struct {
	req runner.RunRequest
}

func (f *fakeShellSmokeRunner) Name() string { return "shell" }

func (f *fakeShellSmokeRunner) Manifest() runner.CapabilityManifest {
	return runner.CapabilityManifest{}
}

func (f *fakeShellSmokeRunner) Check(string) error { return nil }

func (f *fakeShellSmokeRunner) Run(req runner.RunRequest) (runner.Result, error) {
	f.req = req
	return runner.Result{CommandOutput: runner.CommandOutput{Command: "diagnostic", ExitCode: 0, Stdout: "ok"}}, nil
}

func TestShellRunnerSmokeUsesAllowlistedQuery(t *testing.T) {
	run := &fakeShellSmokeRunner{}
	check := shellRunnerSmokeCheck(context.Background(), run, time.Second, "")
	if !check.OK {
		t.Fatalf("check = %+v", check)
	}
	if run.req.Prompt != "" {
		t.Fatalf("shell smoke should not use agent prompt, got %q", run.req.Prompt)
	}
	if run.req.QueryKind != profiledefs.CapShellReadonly {
		t.Fatalf("query kind = %q, want %q", run.req.QueryKind, profiledefs.CapShellReadonly)
	}
	if run.req.QueryParams["command"] == "" {
		t.Fatalf("shell smoke missing command: %+v", run.req.QueryParams)
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
	for _, id := range []string{"tsk_queuedtask", "tsk_runningtask"} {
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
	if _, err := st.UpdateTaskState(ctx, "tsk_runningtask", store.StateUpdate{State: task.StateRunning}); err != nil {
		t.Fatalf("mark running: %v", err)
	}
	writer := audit.NewWriter(filepath.Join(t.TempDir(), "audit.jsonl"), "worker")
	if err := failClosedRestart(ctx, t.TempDir(), st, writer); err != nil {
		t.Fatalf("failClosedRestart: %v", err)
	}
	for _, id := range []string{"tsk_queuedtask", "tsk_runningtask"} {
		rec, err := st.GetTask(ctx, id)
		if err != nil {
			t.Fatalf("GetTask %s: %v", id, err)
		}
		if rec.State != task.StateFailed || rec.TerminalErrorCode != task.ErrorWorkerDaemonRestarted {
			t.Fatalf("%s state/code = %s/%s, want failed/%s", id, rec.State, rec.TerminalErrorCode, task.ErrorWorkerDaemonRestarted)
		}
	}
}
