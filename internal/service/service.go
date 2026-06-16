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
	"syscall"
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
	// UserTask installs a per-user scheduled task instead of an SCM service.
	// This is the right Windows worker mode when the daemon must inherit the
	// interactive user's Tailscale and runner auth and survive SSH logout.
	UserTask bool
	// Firewall opens the mesh RPC port for the installed cmesh executable.
	Firewall bool
	// PasswordEnv and PromptPassword are retained so the CLI can describe how the
	// password was sourced, but this package never reads the env var or prompts.
	PasswordEnv    string
	PromptPassword bool
}

// ErrWindowsElevationRequired marks Windows service operations that need
// Administrator approval.
var ErrWindowsElevationRequired = errors.New("windows service operation requires elevation")

// IsWindowsElevationRequired reports whether err means Windows blocked a
// service operation because the process is not elevated.
func IsWindowsElevationRequired(err error) bool {
	return errors.Is(err, ErrWindowsElevationRequired)
}

// Install writes the platform service definition and registers it.
func Install(opts Options) error {
	configPath := firstNonEmpty(opts.ConfigPath, config.DefaultPath())
	exe := opts.ExePath
	if exe == "" {
		return errors.New("service install requires an executable path")
	}
	win := opts.Windows
	if runtime.GOOS != "windows" && (win.User != "" || win.Password != "" || win.UserTask || win.PasswordEnv != "" || win.PromptPassword) {
		return fmt.Errorf("Windows service account options are only supported on Windows")
	}
	switch runtime.GOOS {
	case "linux":
		unitDir := filepath.Join(userConfigDir(), "systemd", "user")
		if err := os.MkdirAll(unitDir, 0o755); err != nil {
			return linuxInstallError("create user systemd directory", unitDir, err)
		}
		unitPath := filepath.Join(unitDir, "cmesh.service")
		body := SystemdUnitBody(exe, configPath)
		if err := os.WriteFile(unitPath, []byte(body), 0o644); err != nil {
			return linuxInstallError("write user systemd unit", unitPath, err)
		}
		// If a prior failed/manual install left cmesh.service masked, enable/start
		// will fail even though the freshly written user unit is valid. Unmasking
		// this exact unit is idempotent and scoped to cmesh's own user service.
		_ = runServiceCommandCapture("systemctl", "--user", "unmask", "cmesh.service")
		_ = runServiceCommand("systemctl", "--user", "daemon-reload")
		if err := runServiceCommandCapture("systemctl", "--user", "enable", "cmesh.service"); err != nil {
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
		if win.UserTask {
			if err := installWindowsUserTask(exe, configPath, win.Firewall); err != nil {
				return err
			}
			return nil
		}
		cmdLine := ServiceCommandLine(exe, configPath)
		action := "create"
		if windowsServiceExists() {
			action = "config"
		}
		args := WindowsServiceSCArgs(action, cmdLine, win.User, win.Password)
		if err := runServiceCommand("sc.exe", args...); err != nil {
			if isWindowsAccessDeniedError(err) {
				return fmt.Errorf("installing the Windows service requires Administrator rights (sc.exe returned access denied). Re-run from an elevated prompt — right-click PowerShell and \"Run as administrator\", then run the install again. Or skip the service and run the daemon directly with `cmesh up`: %w", err)
			}
			return err
		}
		if win.User == "" {
			fmt.Println("Installed Windows service: cmesh")
			fmt.Println("Warning: service will run as LocalSystem. Runner auth from your interactive user session will not be available.")
			fmt.Println("For Claude Code/Codex auth, reinstall with: cmesh up service install --user-task")
		} else if action == "config" {
			fmt.Printf("Updated Windows service cmesh to run as %s\n", win.User)
		} else {
			fmt.Printf("Installed Windows service cmesh to run as %s\n", win.User)
		}
		if win.Firewall {
			if err := EnsureWindowsFirewallRule(exe, config.DefaultMeshPort); err != nil {
				return err
			}
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
		if windowsUserTaskExists() {
			return runServiceCommand("schtasks.exe", "/Run", "/TN", WindowsUserTaskName)
		}
		if windowsServiceExists() {
			return runServiceCommand("sc.exe", "start", "cmesh")
		}
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
		if windowsUserTaskExists() {
			return runServiceCommand("schtasks.exe", "/End", "/TN", WindowsUserTaskName)
		}
		if windowsServiceExists() {
			return runServiceCommand("sc.exe", "stop", "cmesh")
		}
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
		if windowsUserTaskExists() {
			_ = runServiceCommand("schtasks.exe", "/End", "/TN", WindowsUserTaskName)
			time.Sleep(2 * time.Second)
			return runServiceCommand("schtasks.exe", "/Run", "/TN", WindowsUserTaskName)
		}
		if windowsServiceExists() {
			if err := Stop(); err != nil {
				return err
			}
			time.Sleep(5 * time.Second)
			return Start()
		}
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
		if windowsUserTaskExists() {
			return runServiceCommand("schtasks.exe", "/Query", "/TN", WindowsUserTaskName, "/FO", "LIST")
		}
		if windowsServiceExists() {
			return runServiceCommand("sc.exe", "query", "cmesh")
		}
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
		if windowsUserTaskExists() {
			_ = runServiceCommandCapture("schtasks.exe", "/End", "/TN", WindowsUserTaskName)
			if err := runServiceCommandCapture("schtasks.exe", "/Delete", "/TN", WindowsUserTaskName, "/F"); err != nil {
				return err
			}
		}
		exists, err := windowsServiceExistsStatus()
		if err != nil {
			if isWindowsAccessDeniedError(err) {
				return windowsServiceAdminRequiredError("removing")
			}
			return err
		}
		if !exists {
			return nil
		}
		elevated, err := currentProcessElevated()
		if err != nil {
			return fmt.Errorf("checking Windows elevation before service removal: %w", err)
		}
		if !elevated {
			return windowsServiceAdminRequiredError("removing")
		}
		if err := runServiceCommandCapture("sc.exe", "stop", "cmesh"); err != nil {
			if isWindowsAccessDeniedError(err) {
				return windowsServiceAdminRequiredError("removing")
			}
			// The service may already be stopped; removal can still proceed.
			if !isWindowsServiceNotRunningError(err) && !isWindowsServiceNotInstalledError(err) {
				return err
			}
		}
		if err := runServiceCommandCapture("sc.exe", "delete", "cmesh"); err != nil {
			if isWindowsServiceNotInstalledError(err) {
				return nil
			}
			if isWindowsAccessDeniedError(err) {
				return windowsServiceAdminRequiredError("removing")
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

// WindowsUserTaskName is the per-user scheduled task used for non-admin
// Windows workers. It runs under the current user so runner auth and PATH match
// the account that ran cmesh setup.
const WindowsUserTaskName = "cmesh-daemon"

// WindowsUserTaskCommandLine builds the command persisted in Task Scheduler.
func WindowsUserTaskCommandLine(exe, configPath string) string {
	return fmt.Sprintf(`"%s" up --config "%s"`, exe, configPath)
}

// WindowsUserTaskCreateArgs builds schtasks.exe arguments for a current-user
// daemon task. The task is also runnable on demand with `schtasks /Run`.
func WindowsUserTaskCreateArgs(exe, configPath string) []string {
	return []string{"/Create", "/TN", WindowsUserTaskName, "/TR", WindowsUserTaskCommandLine(exe, configPath), "/SC", "ONLOGON", "/RL", "LIMITED", "/F"}
}

// WindowsFirewallRuleName returns the stable rule name for the mesh RPC port.
func WindowsFirewallRuleName(port int) string {
	return fmt.Sprintf("cmesh mesh %d", port)
}

// WindowsFirewallRuleArgs builds a narrow inbound firewall rule for cmesh mesh
// RPC. The local MCP gateway remains loopback-only and is not opened.
func WindowsFirewallRuleArgs(exe string, port int) []string {
	return []string{"advfirewall", "firewall", "add", "rule", "name=" + WindowsFirewallRuleName(port), "dir=in", "action=allow", "protocol=TCP", fmt.Sprintf("localport=%d", port), "program=" + exe}
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

func installWindowsUserTask(exe, configPath string, firewall bool) error {
	if err := runServiceCommand("schtasks.exe", WindowsUserTaskCreateArgs(exe, configPath)...); err != nil {
		return err
	}
	fmt.Printf("Installed Windows user scheduled task: %s\n", WindowsUserTaskName)
	fmt.Println("This mode runs as the current user, survives SSH logout, and preserves Claude Code/Codex auth.")
	if firewall {
		if err := EnsureWindowsFirewallRule(exe, config.DefaultMeshPort); err != nil {
			return err
		}
	}
	return nil
}

// EnsureWindowsFirewallRule opens the mesh RPC port for the cmesh executable.
func EnsureWindowsFirewallRule(exe string, port int) error {
	if err := runServiceCommandCapture("netsh.exe", "advfirewall", "firewall", "show", "rule", "name="+WindowsFirewallRuleName(port)); err == nil {
		return nil
	}
	if err := runServiceCommand("netsh.exe", WindowsFirewallRuleArgs(exe, port)...); err != nil {
		if isWindowsAccessDeniedError(err) {
			return fmt.Errorf("opening Windows firewall for cmesh mesh RPC requires Administrator rights. Re-run from an elevated PowerShell or add an inbound allow rule for TCP %d to %s: %w", port, exe, err)
		}
		return err
	}
	fmt.Printf("Installed Windows firewall rule: %s\n", WindowsFirewallRuleName(port))
	return nil
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

func isWindowsServiceNotRunningError(err error) bool {
	if err == nil {
		return false
	}
	text := err.Error()
	return strings.Contains(text, "FAILED 1062") || strings.Contains(strings.ToLower(text), "service has not been started")
}

// isWindowsAccessDeniedError reports whether err is sc.exe failing with access
// denied (Windows error 5) — the symptom of running service install without
// Administrator rights. sc.exe writes "Access is denied." to stdout/stderr,
// which runServiceCommand does not capture, so the resulting *exec.ExitError
// carries only exit code 5. Key on the exit code first, with a text fallback for
// callers that do capture output.
func isWindowsAccessDeniedError(err error) bool {
	if err == nil {
		return false
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 5 {
		return true
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "access is denied") || strings.Contains(text, "exit status 5")
}

func windowsServiceAdminRequiredError(action string) error {
	return fmt.Errorf("%s the Windows service requires Administrator approval. Accept the Windows UAC prompt, or run this command from an elevated PowerShell. If you decline, the user-level MCP/config/data cleanup can still be removed separately: %w", action, ErrWindowsElevationRequired)
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
	exists, _ := windowsServiceExistsStatus()
	return exists
}

func windowsUserTaskExists() bool {
	return runServiceCommandCapture("schtasks.exe", "/Query", "/TN", WindowsUserTaskName) == nil
}

func windowsServiceExistsStatus() (bool, error) {
	err := runServiceCommandCapture("sc.exe", "query", "cmesh")
	if err == nil {
		return true, nil
	}
	if isWindowsServiceNotInstalledError(err) {
		return false, nil
	}
	return false, err
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

func runServiceCommandCapture(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	if err == nil {
		return nil
	}
	text := strings.TrimSpace(string(out))
	if text == "" {
		return err
	}
	return fmt.Errorf("%s %s: %s: %w", name, strings.Join(args, " "), text, err)
}

func linuxInstallError(action, path string, err error) error {
	if errors.Is(err, syscall.ENOSPC) {
		return fmt.Errorf("%s at %s: no space left on device. Free space on the filesystem containing %s, then rerun `cmesh up service install`: %w", action, path, path, err)
	}
	return fmt.Errorf("%s at %s: %w", action, path, err)
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
