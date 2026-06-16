//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

func removeCurrentExecutable(exe string) (string, error) {
	script, err := writeSelfDeleteScript(exe)
	if err != nil {
		return "", err
	}
	cmd := exec.Command("powershell.exe", "-NoProfile", "-ExecutionPolicy", "Bypass", "-WindowStyle", "Hidden", "-File", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if err := cmd.Start(); err != nil {
		_ = os.Remove(script)
		return "", fmt.Errorf("start binary cleanup helper: %w", err)
	}
	return "scheduled removal after cmesh exits: " + exe, nil
}

func writeSelfDeleteScript(target string) (string, error) {
	if strings.TrimSpace(target) == "" {
		return "", errors.New("empty executable path")
	}
	f, err := os.CreateTemp("", "cmesh-uninstall-*.ps1")
	if err != nil {
		return "", err
	}
	scriptPath := f.Name()
	body := fmt.Sprintf(`$target = '%s'
$self = '%s'
for ($i = 0; $i -lt 100; $i++) {
  try {
    if (Test-Path -LiteralPath $target) {
      Remove-Item -LiteralPath $target -Force -ErrorAction Stop
    }
    break
  } catch {
    Start-Sleep -Milliseconds 200
  }
}
Remove-Item -LiteralPath $self -Force -ErrorAction SilentlyContinue
`, powerShellSingleQuoted(target), powerShellSingleQuoted(filepath.Clean(scriptPath)))
	if _, err := f.WriteString(body); err != nil {
		_ = f.Close()
		_ = os.Remove(scriptPath)
		return "", err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(scriptPath)
		return "", err
	}
	return scriptPath, nil
}

func powerShellSingleQuoted(value string) string {
	return strings.ReplaceAll(value, `'`, `''`)
}
