package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// TestSelfTestStdioAgainstRealBinary builds the cmesh binary and runs the
// installer's stdio self-test against it, confirming the initialize +
// tools/list handshake completes and reports tools. This is the honest "MCP
// wired" check from Part 2.
func TestSelfTestStdioAgainstRealBinary(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping binary build in -short mode")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available")
	}
	dir := t.TempDir()
	exe := filepath.Join(dir, "cmesh")
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	build := exec.Command("go", "build", "-o", exe, ".")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		t.Fatalf("build cmesh: %v", err)
	}
	if err := selfTestStdio(exe); err != nil {
		t.Fatalf("selfTestStdio failed: %v", err)
	}
}
