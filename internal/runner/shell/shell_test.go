package shell

import (
	"context"
	"os/exec"
	"runtime"
	"testing"

	"cmesh/internal/profile"
	"cmesh/internal/runner"
)

func TestShellRunnerAllowsAllowlistedCommand(t *testing.T) {
	goCmd, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go command not on PATH")
	}
	run := New(map[string]ProfileConfig{
		"profile": {
			Capabilities: []string{profile.CapShellReadonly},
			ShellPolicy: ShellPolicy{
				Mode:            "allowlist",
				AllowedCommands: map[string][]string{runtime.GOOS: {goCmd}},
			},
		},
	})
	result, err := run.Run(runner.RunRequest{
		Context:   context.Background(),
		TaskID:    "task-1",
		Profile:   "profile",
		QueryKind: profile.CapShellReadonly,
		QueryParams: map[string]any{
			"command": goCmd,
			"args":    []any{"version"},
		},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.CommandOutput.ExitCode != 0 || result.RunnerExtra["no_writes"] != true {
		t.Fatalf("result = %+v", result)
	}
}

func TestShellRunnerUsesOnlyProfileAsDefault(t *testing.T) {
	goCmd, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go command not on PATH")
	}
	run := New(map[string]ProfileConfig{
		profile.QueryShell: {
			Capabilities: []string{profile.CapShellReadonly},
			ShellPolicy: ShellPolicy{
				Mode:            "allowlist",
				AllowedCommands: map[string][]string{runtime.GOOS: {goCmd}},
			},
		},
	})
	result, err := run.Run(runner.RunRequest{
		Context:   context.Background(),
		TaskID:    "task-1",
		QueryKind: profile.CapShellReadonly,
		QueryParams: map[string]any{
			"command": goCmd,
			"args":    []any{"version"},
		},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.CommandOutput.ExitCode != 0 {
		t.Fatalf("result = %+v", result)
	}
}

func TestShellRunnerExecutesWindowsCmdletThroughPowerShell(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("PowerShell cmdlet smoke is Windows-only")
	}
	run := New(map[string]ProfileConfig{
		profile.QueryShell: {
			Capabilities: []string{profile.CapShellReadonly},
			ShellPolicy: ShellPolicy{
				Mode:            "allowlist",
				AllowedCommands: map[string][]string{runtime.GOOS: {"Get-PSDrive"}},
			},
		},
	})
	result, err := run.Run(runner.RunRequest{
		Context:   context.Background(),
		TaskID:    "task-1",
		QueryKind: profile.CapShellReadonly,
		QueryParams: map[string]any{
			"command": "Get-PSDrive",
		},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.CommandOutput.ExitCode != 0 {
		t.Fatalf("result = %+v", result)
	}
}

func TestShellRunnerDeniesCommandNotInAllowlist(t *testing.T) {
	run := New(map[string]ProfileConfig{
		"profile": {
			Capabilities: []string{profile.CapShellReadonly},
			ShellPolicy: ShellPolicy{
				Mode:            "allowlist",
				AllowedCommands: map[string][]string{runtime.GOOS: {"definitely-not-this"}},
			},
		},
	})
	_, err := run.Run(runner.RunRequest{
		Context:   context.Background(),
		TaskID:    "task-1",
		Profile:   "profile",
		QueryKind: profile.CapShellReadonly,
		QueryParams: map[string]any{
			"command": "go",
			"args":    []any{"version"},
		},
	})
	if runner.CodeOf(err) != runner.ErrProfileDenied {
		t.Fatalf("err = %v, want profile_denied", err)
	}
}

func TestShellRunnerDeniesPatternBeforeExecution(t *testing.T) {
	goCmd, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go command not on PATH")
	}
	run := New(map[string]ProfileConfig{
		"profile": {
			Capabilities: []string{profile.CapShellReadonly},
			ShellPolicy: ShellPolicy{
				Mode:            "allowlist",
				AllowedCommands: map[string][]string{runtime.GOOS: {goCmd}},
				DeniedPatterns:  []string{";"},
			},
		},
	})
	_, err = run.Run(runner.RunRequest{
		Context:   context.Background(),
		TaskID:    "task-1",
		Profile:   "profile",
		QueryKind: profile.CapShellReadonly,
		QueryParams: map[string]any{
			"command": goCmd,
			"args":    []any{"version;"},
		},
	})
	if runner.CodeOf(err) != runner.ErrProfileDenied {
		t.Fatalf("err = %v, want profile_denied", err)
	}
}

func TestShellRunnerDeniesUnsafeAllowlistedCommandArgs(t *testing.T) {
	tests := []struct {
		goos string
		argv []string
	}{
		{"windows", []string{"Get-EventLog", "-LogName", "System"}},
		{"windows", []string{"Get-Service", "-ComputerName", "remote"}},
		{"linux", []string{"journalctl", "-u", "cmesh"}},
		{"linux", []string{"systemctl", "restart", "cmesh"}},
		{"darwin", []string{"log", "stream"}},
	}
	for _, tc := range tests {
		if err := validateCommandArgs(tc.goos, tc.argv); runner.CodeOf(err) != runner.ErrProfileDenied {
			t.Fatalf("validateCommandArgs(%s, %#v) err=%v, want profile_denied", tc.goos, tc.argv, err)
		}
	}
}

func TestShellRunnerAllowsBoundedDiagnosticArgs(t *testing.T) {
	tests := []struct {
		goos string
		argv []string
	}{
		{"windows", []string{"Get-EventLog", "-LogName", "System", "-Newest", "50"}},
		{"windows", []string{"Get-Service", "-Name", "cmesh"}},
		{"linux", []string{"journalctl", "-u", "cmesh", "-n", "50", "--no-pager"}},
		{"linux", []string{"systemctl", "status", "cmesh", "--no-pager"}},
		{"darwin", []string{"log", "show", "--last", "5m", "--style", "compact"}},
	}
	for _, tc := range tests {
		if err := validateCommandArgs(tc.goos, tc.argv); err != nil {
			t.Fatalf("validateCommandArgs(%s, %#v) err=%v, want nil", tc.goos, tc.argv, err)
		}
	}
}
