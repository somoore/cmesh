package shell

import (
	"testing"

	"cmesh/internal/runner"
)

// On Windows the allowlisted cmdlets run through `powershell -Command`, so every
// argv token is script text. These cases are the injection primitives a single
// `&`, a newline, or a quote character gives an attacker; the old validator
// checked only tokens beginning with "-".
func TestWindowsArgValuesCannotBecomePowerShellStatements(t *testing.T) {
	bad := [][]string{
		{"Get-Service", "-Name", "spooler\nStart-Process calc"},
		{"Get-Service", "-Name", "spooler\r\nStart-Process calc"},
		{"Get-Service", "-Name=x&calc"},
		{"Get-Service", "-Name", "x&calc"},
		{"Get-Process", "-Id", "1;Start-Process calc"},
		{"Get-Service", "-Name", "a", "Start-Process", "calc"},
		{"Get-PSDrive", "-PSProvider", "FileSystem\");calc;(\""},
		{"Get-EventLog", "-LogName", "System", "-Newest", "5", "-Source", "x\nStart-Process calc"},
		{"Get-Service", "-Name", "x|calc"},
		{"Get-Service", "-Name", "x`calc"},
		{"Get-Service", "-Name", "$(calc)"},
		{"Get-Service", "-Name", "x > out.txt"},
	}
	for _, argv := range bad {
		if err := validateCommandArgs("windows", argv); runner.CodeOf(err) != runner.ErrProfileDenied {
			t.Errorf("validateCommandArgs(windows, %#v) = %v, want profile_denied", argv, err)
		}
	}

	good := [][]string{
		{"Get-Service", "-Name", "cmesh"},
		{"Get-Service", "-Name", "SQL*"},
		{"Get-Service", "-DisplayName", "Print Spooler"},
		{"Get-EventLog", "-LogName", "System", "-Newest", "50"},
		{"Get-PSDrive", "-PSProvider", "FileSystem"},
	}
	for _, argv := range good {
		if err := validateCommandArgs("windows", argv); err != nil {
			t.Errorf("validateCommandArgs(windows, %#v) = %v, want nil", argv, err)
		}
	}
}

// A custom shell_policy may omit denied_patterns entirely; token validation is
// not allowed to depend on that list.
func TestWindowsShellPolicyRejectsNewlineWithEmptyDeniedPatterns(t *testing.T) {
	p := ShellPolicy{Mode: "allowlist", AllowedCommands: map[string][]string{"windows": {"Get-Service"}}}
	if err := validatePolicy(p, []string{"Get-Service", "-Name", "x\ncalc"}); err == nil {
		t.Fatal("a profile without denied_patterns must still reject a newline")
	}
}

// A later duplicate flag overrides the bound set by an earlier one, so the
// bounded log tail must reject the duplicate rather than use the last value.
func TestShellPolicyRejectsDuplicateBoundFlags(t *testing.T) {
	if err := validateCommandArgs("windows", []string{"Get-EventLog", "-LogName", "System", "-Newest", "1", "-Newest", "1000000"}); runner.CodeOf(err) != runner.ErrProfileDenied {
		t.Fatalf("duplicate -Newest err = %v, want profile_denied", err)
	}
	if err := validateCommandArgs("linux", []string{"journalctl", "-u", "cmesh", "-n", "1", "-n", "999999"}); runner.CodeOf(err) != runner.ErrProfileDenied {
		t.Fatalf("duplicate -n err = %v, want profile_denied", err)
	}
}

// The exec-boundary helper must refuse the same tokens even if a caller reaches
// it without running validatePolicy first.
func TestWindowsExecArgvRefusesUnsafeTokens(t *testing.T) {
	if err := validateWindowsArgs("get-service", []string{"-Name", "x&calc"}); runner.CodeOf(err) != runner.ErrProfileDenied {
		t.Fatalf("validateWindowsArgs err = %v, want profile_denied", err)
	}
}
