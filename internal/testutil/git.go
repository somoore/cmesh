package testutil

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func EnsureGitOnPath(t testing.TB) {
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

func RunGit(t testing.TB, dir string, args ...string) {
	t.Helper()
	_ = RunGitOutput(t, dir, args...)
}

func RunGitOutput(t testing.TB, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}
