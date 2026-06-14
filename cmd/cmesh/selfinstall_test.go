package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestCopyExecutableAndIdempotent(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	if err := os.WriteFile(src, []byte("#!/bin/sh\necho hi\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	dst := filepath.Join(dir, "bin", "cmesh")
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := copyExecutable(src, dst); err != nil {
		t.Fatalf("copyExecutable: %v", err)
	}
	info, err := os.Stat(dst)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("destination is not executable: %v", info.Mode())
	}
	// Copying again must succeed (idempotent overwrite).
	if err := copyExecutable(src, dst); err != nil {
		t.Fatalf("second copyExecutable: %v", err)
	}
}

func TestEnsurePathInRCIdempotent(t *testing.T) {
	dir := t.TempDir()
	rc := filepath.Join(dir, ".zshrc")
	target := filepath.Join(dir, ".local", "bin")

	if err := ensurePathInRC(rc, target); err != nil {
		t.Fatalf("ensurePathInRC: %v", err)
	}
	body1, err := os.ReadFile(rc)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.Contains(string(body1), selfInstallMarker) {
		t.Fatalf("marker missing:\n%s", body1)
	}
	if !strings.Contains(string(body1), target) {
		t.Fatalf("PATH export missing:\n%s", body1)
	}
	// Running again must not append a second block.
	if err := ensurePathInRC(rc, target); err != nil {
		t.Fatalf("second ensurePathInRC: %v", err)
	}
	body2, err := os.ReadFile(rc)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if strings.Count(string(body2), selfInstallMarker) != 1 {
		t.Fatalf("marker duplicated:\n%s", body2)
	}
}

func TestDirOnPath(t *testing.T) {
	dir := t.TempDir()
	other := t.TempDir()
	t.Setenv("PATH", dir+string(os.PathListSeparator)+other)
	if !dirOnPath(dir) {
		t.Fatalf("expected %s to be on PATH", dir)
	}
	if dirOnPath(t.TempDir()) {
		t.Fatalf("unexpected dir reported on PATH")
	}
}

func TestSelfInstalledBinary(t *testing.T) {
	custom := t.TempDir()
	t.Setenv("CMESH_INSTALL_DIR", custom)
	if !selfInstalledBinary(filepath.Join(custom, "cmesh")) {
		t.Fatalf("expected binary in CMESH_INSTALL_DIR to be self-installed")
	}
	if selfInstalledBinary("/opt/random/cmesh") {
		t.Fatalf("unexpected self-install match for /opt/random")
	}
}
