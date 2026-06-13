package main

import (
	"context"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cmesh/internal/audit"
	"cmesh/internal/config"
	"cmesh/internal/gitx"
	"cmesh/internal/store"
	"cmesh/internal/task"
)

func TestCleanupTaskArtifactsRemovesRegisteredWorktreeBeforeBranch(t *testing.T) {
	ctx := context.Background()
	ensureGitOnPath(t)
	root := t.TempDir()
	runGit(t, root, "init")
	runGit(t, root, "config", "user.name", "Test User")
	runGit(t, root, "config", "user.email", "test@example.com")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	runGit(t, root, "add", "README.md")
	runGit(t, root, "commit", "-m", "initial")

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

	if out := runGitOutput(t, root, "branch", "--list", branch); strings.TrimSpace(out) != "" {
		t.Fatalf("branch still exists: %q", out)
	}
	if out := runGitOutput(t, root, "worktree", "list", "--porcelain"); strings.Contains(filepath.ToSlash(out), filepath.ToSlash(worktreePath)) {
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

func TestServiceCommandLineUsesServiceEntrypoint(t *testing.T) {
	got := serviceCommandLine(`C:\Tools\cmesh.exe`, `C:\Users\scott\.config\cmesh\config.toml`)
	if !strings.Contains(got, ` _service --config `) {
		t.Fatalf("service command line = %q, want _service entrypoint", got)
	}
	if strings.Contains(got, ` up --config `) {
		t.Fatalf("service command line = %q, must not use foreground up command", got)
	}
}

func TestWindowsServiceSCArgsWithUser(t *testing.T) {
	got := strings.Join(windowsServiceSCArgs("config", `C:\Tools\cmesh.exe _service`, `.\scott`, "secret"), "\x00")
	for _, want := range []string{"config", "cmesh", "binPath=", `C:\Tools\cmesh.exe _service`, "start=", "auto", "obj=", `.\scott`, "password=", "secret"} {
		if !strings.Contains(got, want) {
			t.Fatalf("windowsServiceSCArgs missing %q in %#v", want, got)
		}
	}
}

func TestParseWindowsServiceStartName(t *testing.T) {
	out := `
SERVICE_NAME: cmesh
        TYPE               : 10  WIN32_OWN_PROCESS
        START_TYPE         : 2   AUTO_START
        SERVICE_START_NAME : .\scott
`
	got, err := parseWindowsServiceStartName(out)
	if err != nil {
		t.Fatalf("parseWindowsServiceStartName: %v", err)
	}
	if got != `.\scott` {
		t.Fatalf("start name = %q", got)
	}
}

func TestSameWindowsUserNormalizesDomainAndDotPrefix(t *testing.T) {
	if !sameWindowsUser(`.\scott`, `DESKTOP\scott`) {
		t.Fatal("expected dot-prefix and machine-domain names to match")
	}
	if sameWindowsUser(`.\scott`, `DESKTOP\other`) {
		t.Fatal("different users should not match")
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
	if !smokeResultMatches(&task.ResultEnvelope{CommandOutput: task.CommandOutput{Command: "runner", ExitCode: 0, Stdout: "cmesh smoke ok\n"}}) {
		t.Fatal("expected raw command artifact to match")
	}
	if smokeResultMatches(&task.ResultEnvelope{CommandOutput: task.CommandOutput{Command: "runner", ExitCode: 1, Stdout: "cmesh smoke ok\n"}}) {
		t.Fatal("non-zero command artifact must not pass smoke validation")
	}
	if !smokeResultMatches(&task.ResultEnvelope{WorkerSummaryUntrusted: "cmesh smoke ok\n"}) {
		t.Fatal("expected smoke result to match")
	}
	if smokeResultMatches(&task.ResultEnvelope{WorkerSummaryUntrusted: "What would you like me to work on?"}) {
		t.Fatal("generic worker response must not pass smoke validation")
	}
	if smokeResultMatches(nil) {
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

func ensureGitOnPath(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err == nil {
		return
	}
	for _, dir := range []string{`C:\Program Files\Git\cmd`, `/usr/bin`} {
		if _, err := os.Stat(filepath.Join(dir, "git.exe")); err == nil {
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			return
		}
		if _, err := os.Stat(filepath.Join(dir, "git")); err == nil {
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			return
		}
	}
	t.Skip("git not found on PATH")
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	_ = runGitOutput(t, dir, args...)
}

func runGitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}
