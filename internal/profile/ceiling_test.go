package profile

import "testing"

func templateAuthority(t *testing.T, name string) Authority {
	t.Helper()
	tmpl, ok := TemplateByName(name)
	if !ok {
		t.Fatalf("no built-in template %q", name)
	}
	return Authority{
		Capabilities: NormalizeCapabilities(tmpl.Capabilities, nil),
		Writes:       tmpl.Writes,
		Shell:        tmpl.Shell,
		Network:      tmpl.Network,
		GitPush:      tmpl.GitPush,
		Risk:         tmpl.Risk,
	}
}

// A ceiling must narrow on every axis. Risk rank alone let two profiles in the
// same tier stand in for each other even when they grant disjoint powers.
func TestCeilingExcessOnBuiltins(t *testing.T) {
	cases := []struct {
		requested, ceiling string
		allowed            bool
	}{
		// Genuine narrowing, including the documented equal-risk example.
		{SystemChange, RepoDev, true},
		{QueryNetwork, Query, true},
		{RepoDev, ReadonlyAgent, true},
		{SystemChange, ReadonlyAgent, true},
		{RepoDev, RepoDev, true},
		// Same risk tier, disjoint powers: neither direction is a cap.
		{ReadonlyAgent, QueryShell, false},
		{QueryShell, ReadonlyAgent, false},
		{QueryNetwork, QueryShell, false},
		{RepoDev, SystemChange, false},
		{RepoDev, DesktopControl, false},
		{DesktopControl, SystemChange, false},
		// Plain escalation.
		{Query, QueryNetwork, false},
		{ReadonlyAgent, RepoDev, false},
	}
	for _, tc := range cases {
		t.Run(tc.requested+"->"+tc.ceiling, func(t *testing.T) {
			excess := CeilingExcess(templateAuthority(t, tc.ceiling), templateAuthority(t, tc.requested))
			if tc.allowed && excess != "" {
				t.Fatalf("expected %s to cap %s, got %q", tc.ceiling, tc.requested, excess)
			}
			if !tc.allowed && excess == "" {
				t.Fatalf("expected %s -> %s to be rejected", tc.requested, tc.ceiling)
			}
		})
	}
}

func TestCeilingExcessAxes(t *testing.T) {
	base := Authority{Writes: WritesRepoOnly, Shell: ShellAllowlisted, Risk: RiskHigh, Capabilities: []string{CapReadFiles}}
	cases := []struct {
		name    string
		ceiling Authority
		allowed bool
	}{
		{"identical", base, true},
		{"writes raised", Authority{Writes: WritesAllow, Shell: ShellDeny, Risk: RiskLow}, false},
		{"shell raised", Authority{Writes: WritesDeny, Shell: ShellAllow, Risk: RiskLow}, false},
		{"network added", Authority{Writes: WritesDeny, Shell: ShellDeny, Network: true, Risk: RiskLow}, false},
		{"git push added", Authority{Writes: WritesDeny, Shell: ShellDeny, GitPush: true, Risk: RiskLow}, false},
		{"unknown writes", Authority{Writes: "everything", Shell: ShellDeny, Risk: RiskLow}, false},
		{"unknown shell", Authority{Writes: WritesDeny, Shell: "root", Risk: RiskLow}, false},
		{"edit_repo implied by writes", Authority{Writes: WritesRepoOnly, Shell: ShellDeny, Risk: RiskHigh, Capabilities: []string{CapEditRepo}}, true},
		{"shell_readonly implied by shell", Authority{Writes: WritesDeny, Shell: ShellAllowlisted, Risk: RiskMedium, Capabilities: []string{CapShellReadonly}}, true},
		{"execute_in_repo needs shell=allow", Authority{Writes: WritesDeny, Shell: ShellDeny, Risk: RiskHigh, Capabilities: []string{CapExecuteInRepo}}, false},
		{"unlisted capability", Authority{Writes: WritesDeny, Shell: ShellDeny, Risk: RiskLow, Capabilities: []string{CapDiskInfo}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			excess := CeilingExcess(tc.ceiling, base)
			if tc.allowed != (excess == "") {
				t.Fatalf("allowed=%v, excess=%q", tc.allowed, excess)
			}
		})
	}
}
