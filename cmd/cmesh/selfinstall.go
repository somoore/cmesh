package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// selfInstallMarker is written into the shell rc file alongside the PATH export
// so re-running self-install is idempotent (we grep for it before appending).
const selfInstallMarker = "# added by cmesh self-install"

// cmdSelfInstall copies the running binary to a PATH directory and ensures that
// directory is on PATH (Part 1). It is idempotent: copying over an identical
// destination and re-ensuring PATH are both safe to repeat.
func cmdSelfInstall(args []string) error {
	_ = args
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return err
	}

	dir := preferredInstallDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	dst := filepath.Join(dir, binaryName())

	if same, _ := sameFile(exe, dst); same {
		fmt.Printf("cmesh is already installed at %s\n", dst)
	} else {
		if err := copyExecutable(exe, dst); err != nil {
			return fmt.Errorf("copy binary to %s: %w", dst, err)
		}
		fmt.Printf("Installed cmesh to %s\n", dst)
	}

	if dirOnPath(dir) {
		fmt.Printf("%s is already on PATH.\n", dir)
		return nil
	}

	if rc := userShellRC(); rc != "" {
		if err := ensurePathInRC(rc, dir); err != nil {
			printManualPathHint(dir)
			return nil
		}
		fmt.Printf("Added %s to PATH in %s\n", dir, rc)
		fmt.Println("Open a new shell (or source the file) for it to take effect.")
		return nil
	}

	printManualPathHint(dir)
	return nil
}

func binaryName() string {
	if runtime.GOOS == "windows" {
		return "cmesh.exe"
	}
	return "cmesh"
}

// preferredInstallDir picks a user-writable PATH directory. Root prefers
// /usr/local/bin; everyone else uses ~/.local/bin.
func preferredInstallDir() string {
	if dir := strings.TrimSpace(os.Getenv("CMESH_INSTALL_DIR")); dir != "" {
		return dir
	}
	if runtime.GOOS != "windows" && os.Geteuid() == 0 {
		return "/usr/local/bin"
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".", "bin")
	}
	return filepath.Join(home, ".local", "bin")
}

func copyExecutable(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Chmod(tmp, 0o755); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}

func sameFile(a, b string) (bool, error) {
	ai, err := os.Stat(a)
	if err != nil {
		return false, err
	}
	bi, err := os.Stat(b)
	if err != nil {
		return false, err
	}
	return os.SameFile(ai, bi), nil
}

func dirOnPath(dir string) bool {
	dir = filepath.Clean(dir)
	for _, p := range filepath.SplitList(os.Getenv("PATH")) {
		if filepath.Clean(p) == dir {
			return true
		}
	}
	return false
}

// userShellRC returns the rc file for the user's login shell, or "" if it can't
// be determined (e.g. Windows, where PATH is managed differently).
func userShellRC() string {
	if runtime.GOOS == "windows" {
		return ""
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	shell := os.Getenv("SHELL")
	switch {
	case strings.Contains(shell, "zsh"):
		return filepath.Join(home, ".zshrc")
	case strings.Contains(shell, "bash"):
		return filepath.Join(home, ".bashrc")
	default:
		// Reasonable default that most POSIX shells read.
		return filepath.Join(home, ".profile")
	}
}

// ensurePathInRC appends a PATH export to the rc file, guarded by a marker so
// repeated self-installs don't duplicate the line.
func ensurePathInRC(rc, dir string) error {
	if body, err := os.ReadFile(rc); err == nil {
		if strings.Contains(string(body), selfInstallMarker) || strings.Contains(string(body), dir) {
			return nil
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(rc), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(rc, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	line := fmt.Sprintf("\n%s\nexport PATH=\"%s:$PATH\"\n", selfInstallMarker, dir)
	_, err = f.WriteString(line)
	return err
}

func printManualPathHint(dir string) {
	fmt.Printf("Add %s to your PATH to run cmesh from any shell, e.g.:\n", dir)
	if runtime.GOOS == "windows" {
		fmt.Printf("  setx PATH \"%%PATH%%;%s\"\n", dir)
		return
	}
	fmt.Printf("  export PATH=\"%s:$PATH\"\n", dir)
}
