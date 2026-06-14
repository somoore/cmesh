// Package service manages the cmesh OS service (launchd on macOS, user systemd
// on Linux, Windows SC) for both the CLI and the MCP start_service tool.
//
// The package is intentionally interaction-free: it never prompts and never
// reads passwords. Callers own all consent and password resolution (env/prompt)
// and pass a final, resolved password via Options.Windows.Password. Output for
// install/status/logs is written to os.Stdout/os.Stderr (suitable for the CLI);
// callers that need to capture output (e.g. an MCP server using stdout as a
// JSON-RPC channel) should not rely on this package writing structured data.
package service

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"cmesh/internal/config"
)

// Options describes a service installation request.
type Options struct {
	// ConfigPath is the cmesh config file the service should load. Empty falls
	// back to config.DefaultPath().
	ConfigPath string
	// ExePath is the absolute path to the cmesh binary. Callers resolve this
	// (typically via os.Executable()) and pass it in.
	ExePath string
	// Windows holds Windows-only service account options.
	Windows WindowsOptions
}

// WindowsOptions carries the Windows service-account configuration. Password
// resolution (env lookup / interactive prompt) is the caller's responsibility;
// this package receives an already-resolved Password.
type WindowsOptions struct {
	User     string
	Password string
	// PasswordEnv and PromptPassword are retained so the CLI can describe how the
	// password was sourced, but this package never reads the env var or prompts.
	PasswordEnv    string
	PromptPassword bool
}

// Install writes the platform service definition and registers it.
func Install(opts Options) error {
	configPath := firstNonEmpty(opts.ConfigPath, config.DefaultPath())
	exe := opts.ExePath
	if exe == "" {
		return errors.New("service install requires an executable path")
	}
	win := opts.Windows
	if runtime.GOOS != "windows" && (win.User != "" || win.Password != "" || win.PasswordEnv != "" || win.PromptPassword) {
		return fmt.Errorf("Windows service account options are only supported on Windows")
	}
	switch runtime.GOOS {
	case "linux":
		unitDir := filepath.Join(userConfigDir(), "systemd", "user")
		if err := os.MkdirAll(unitDir, 0o755); err != nil {
			return err
		}
		unitPath := filepath.Join(unitDir, "cmesh.service")
		body := SystemdUnitBody(exe, configPath)
		if err := os.WriteFile(unitPath, []byte(body), 0o644); err != nil {
			return err
		}
		_ = runServiceCommand("systemctl", "--user", "daemon-reload")
		if err := runServiceCommand("systemctl", "--user", "enable", "cmesh.service"); err != nil {
			return err
		}
		fmt.Printf("Installed user systemd service: %s\n", unitPath)
	case "darwin":
		launchDir := filepath.Join(homeDir(), "Library", "LaunchAgents")
		if err := os.MkdirAll(launchDir, 0o755); err != nil {
			return err
		}
		plistPath := launchdPlistPath()
		body := LaunchdPlistBody(exe, configPath, LaunchdPATH())
		if err := os.WriteFile(plistPath, []byte(body), 0o644); err != nil {
			return err
		}
		fmt.Printf("Installed launchd agent: %s\n", plistPath)
	case "windows":
		cmdLine := ServiceCommandLine(exe, configPath)
		action := "create"
		if windowsServiceExists() {
			action = "config"
		}
		args := WindowsServiceSCArgs(action, cmdLine, win.User, win.Password)
		if err := runServiceCommand("sc.exe", args...); err != nil {
			return err
		}
		if win.User == "" {
			fmt.Println("Installed Windows service: cmesh")
			fmt.Println("Warning: service will run as LocalSystem. Runner auth from your interactive user session will not be available.")
			fmt.Println(`For Claude Code/Codex auth, reinstall with: cmesh up service install --user ".\YOURUSER" --prompt-password`)
		} else if action == "config" {
			fmt.Printf("Updated Windows service cmesh to run as %s\n", win.User)
		} else {
			fmt.Printf("Installed Windows service cmesh to run as %s\n", win.User)
		}
	default:
		return fmt.Errorf("service install is not supported on %s", runtime.GOOS)
	}
	return nil
}

// Start starts the cmesh service.
func Start() error {
	switch runtime.GOOS {
	case "linux":
		return runServiceCommand("systemctl", "--user", "start", "cmesh.service")
	case "darwin":
		domain := launchdDomain()
		target := domain + "/dev.cmesh"
		if err := exec.Command("launchctl", "print", target).Run(); err != nil {
			if err := runServiceCommand("launchctl", "bootstrap", domain, launchdPlistPath()); err != nil {
				return err
			}
		}
		return runServiceCommand("launchctl", "kickstart", "-k", target)
	case "windows":
		return runServiceCommand("sc.exe", "start", "cmesh")
	default:
		return fmt.Errorf("service start is not supported on %s", runtime.GOOS)
	}
}

// Stop stops the cmesh service.
func Stop() error {
	switch runtime.GOOS {
	case "linux":
		return runServiceCommand("systemctl", "--user", "stop", "cmesh.service")
	case "darwin":
		return runServiceCommand("launchctl", "bootout", launchdDomain()+"/dev.cmesh")
	case "windows":
		return runServiceCommand("sc.exe", "stop", "cmesh")
	default:
		return fmt.Errorf("service stop is not supported on %s", runtime.GOOS)
	}
}

// Restart restarts the cmesh service.
func Restart() error {
	switch runtime.GOOS {
	case "linux":
		return runServiceCommand("systemctl", "--user", "restart", "cmesh.service")
	case "darwin":
		// Delegate to Start, which bootstraps the job if it is not loaded and then
		// runs `launchctl kickstart -k` to atomically kill+relaunch the running
		// instance (re-exec'ing a rebuilt binary from the plist). Do NOT bootout
		// first: unloading the job races the old process's socket teardown, so the
		// relaunch can hit "address already in use", and a failed re-bootstrap left
		// the job unloaded entirely. kickstart -k makes the bootout unnecessary.
		return Start()
	case "windows":
		if err := Stop(); err != nil {
			return err
		}
		time.Sleep(5 * time.Second)
		return Start()
	default:
		return fmt.Errorf("service restart is not supported on %s", runtime.GOOS)
	}
}

// Status reports the cmesh service status.
func Status() error {
	switch runtime.GOOS {
	case "linux":
		return runServiceCommand("systemctl", "--user", "status", "cmesh.service", "--no-pager")
	case "darwin":
		return runServiceCommand("launchctl", "print", "gui/"+fmt.Sprint(os.Getuid())+"/dev.cmesh")
	case "windows":
		return runServiceCommand("sc.exe", "query", "cmesh")
	default:
		return fmt.Errorf("service status is not supported on %s", runtime.GOOS)
	}
}

// Logs streams recent cmesh service logs.
func Logs() error {
	switch runtime.GOOS {
	case "linux":
		return runServiceCommand("journalctl", "--user", "-u", "cmesh.service", "-n", "100", "--no-pager")
	case "darwin":
		return runServiceCommand("log", "show", "--predicate", `process == "cmesh"`, "--last", "1h")
	case "windows":
		return runServiceCommand("powershell", "-NoProfile", "-Command", `Get-EventLog -LogName Application -Source cmesh -Newest 100`)
	default:
		return fmt.Errorf("service logs are not supported on %s", runtime.GOOS)
	}
}

// Remove stops and uninstalls the cmesh service. It is idempotent: a service
// that is not installed is treated as success. Best-effort stop/disable errors
// (the service may not be loaded) are ignored; only failures to remove the
// on-disk definition are reported.
func Remove() error {
	switch runtime.GOOS {
	case "linux":
		_ = runServiceCommand("systemctl", "--user", "stop", "cmesh.service")
		_ = runServiceCommand("systemctl", "--user", "disable", "cmesh.service")
		unitPath := filepath.Join(userConfigDir(), "systemd", "user", "cmesh.service")
		if err := os.Remove(unitPath); err != nil && !os.IsNotExist(err) {
			return err
		}
		_ = runServiceCommand("systemctl", "--user", "daemon-reload")
		return nil
	case "darwin":
		_ = runServiceCommand("launchctl", "bootout", launchdDomain()+"/dev.cmesh")
		if err := os.Remove(launchdPlistPath()); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	case "windows":
		if !windowsServiceExists() {
			return nil
		}
		_ = runServiceCommand("sc.exe", "stop", "cmesh")
		if err := runServiceCommand("sc.exe", "delete", "cmesh"); err != nil {
			if isWindowsServiceNotInstalledError(err) {
				return nil
			}
			return err
		}
		return nil
	default:
		return fmt.Errorf("service remove is not supported on %s", runtime.GOOS)
	}
}

// ServiceCommandLine builds the Windows service binPath= command. The Windows
// service runs the dedicated `_service` entrypoint (not the foreground `up`).
func ServiceCommandLine(exe, configPath string) string {
	return fmt.Sprintf(`"%s" _service --config "%s"`, exe, configPath)
}

// SystemdUnitBody returns the user systemd unit file contents.
func SystemdUnitBody(exe, configPath string) string {
	return fmt.Sprintf("[Unit]\nDescription=cmesh agent mesh daemon\nAfter=network-online.target\n\n[Service]\nExecStart=%s up --config %s\nRestart=on-failure\nRestartSec=3\n\n[Install]\nWantedBy=default.target\n", exe, configPath)
}

// LaunchdPlistBody returns the launchd agent plist contents.
func LaunchdPlistBody(exe, configPath, pathValue string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict><key>Label</key><string>dev.cmesh</string><key>ProgramArguments</key><array><string>%s</string><string>up</string><string>--config</string><string>%s</string></array><key>EnvironmentVariables</key><dict><key>PATH</key><string>%s</string></dict><key>RunAtLoad</key><true/><key>KeepAlive</key><true/></dict></plist>
`, xmlPlistString(exe), xmlPlistString(configPath), xmlPlistString(pathValue))
}

// LaunchdPATH returns the PATH the launchd agent runs with: Homebrew and the
// standard launchd defaults first, followed by any extra entries from the
// current process PATH (so runner binaries remain discoverable).
func LaunchdPATH() string {
	parts := []string{"/opt/homebrew/bin", "/opt/homebrew/sbin", "/usr/local/bin", "/usr/bin", "/bin", "/usr/sbin", "/sbin"}
	for _, part := range strings.Split(os.Getenv("PATH"), ":") {
		part = strings.TrimSpace(part)
		if part != "" && !containsString(parts, part) {
			parts = append(parts, part)
		}
	}
	return strings.Join(parts, ":")
}

// WindowsServiceSCArgs builds sc.exe arguments for create/config actions.
func WindowsServiceSCArgs(action, cmdLine, user, password string) []string {
	args := []string{action, "cmesh", "binPath=", cmdLine, "start=", "auto"}
	if user != "" {
		args = append(args, "obj=", user, "password=", password)
	}
	return args
}

// WindowsServiceStartName queries the configured Windows service account.
func WindowsServiceStartName() (string, error) {
	out, err := exec.Command("sc.exe", "qc", "cmesh").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("query Windows service cmesh: %s", strings.TrimSpace(string(out)))
	}
	return parseWindowsServiceStartName(string(out))
}

// IsWindowsServiceNotInstalledError reports whether err indicates the cmesh
// Windows service does not exist.
func IsWindowsServiceNotInstalledError(err error) bool {
	return isWindowsServiceNotInstalledError(err)
}

// SameWindowsUser reports whether two Windows account names refer to the same
// user after normalizing domain and `.\` prefixes.
func SameWindowsUser(a, b string) bool {
	a = NormalizeWindowsUserName(a)
	b = NormalizeWindowsUserName(b)
	return a != "" && b != "" && a == b
}

// NormalizeWindowsUserName lowercases and strips domain / `.\` prefixes from a
// Windows account name.
func NormalizeWindowsUserName(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	value = strings.TrimPrefix(value, `.\`)
	if i := strings.LastIndex(value, `\`); i >= 0 {
		value = value[i+1:]
	}
	return value
}

func isWindowsServiceNotInstalledError(err error) bool {
	if err == nil {
		return false
	}
	text := err.Error()
	return strings.Contains(text, "FAILED 1060") || strings.Contains(text, "does not exist as an installed service")
}

func parseWindowsServiceStartName(out string) (string, error) {
	for _, line := range strings.Split(out, "\n") {
		if before, after, ok := strings.Cut(line, ":"); ok && strings.TrimSpace(before) == "SERVICE_START_NAME" {
			value := strings.TrimSpace(after)
			if value == "" {
				return "", errors.New("SERVICE_START_NAME is empty")
			}
			return value, nil
		}
	}
	return "", errors.New("SERVICE_START_NAME not found")
}

func windowsServiceExists() bool {
	return exec.Command("sc.exe", "query", "cmesh").Run() == nil
}

func launchdDomain() string {
	return "gui/" + fmt.Sprint(os.Getuid())
}

func launchdPlistPath() string {
	return filepath.Join(homeDir(), "Library", "LaunchAgents", "dev.cmesh.plist")
}

func runServiceCommand(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func xmlPlistString(value string) string {
	replacer := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;")
	return replacer.Replace(value)
}

func containsString(values []string, needle string) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func homeDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return home
}

func userConfigDir() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return filepath.Join(homeDir(), ".config")
	}
	return dir
}
