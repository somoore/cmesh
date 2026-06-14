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
