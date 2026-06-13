package main

import (
	"context"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"cmesh/internal/gitx"
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
