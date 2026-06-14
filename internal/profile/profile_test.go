package profile

import (
	"strings"
	"testing"
)

func TestValidateSemanticsRejectsContradictions(t *testing.T) {
	cases := []struct {
		name         string
		runner       string
		capabilities []string
		writes       string
		shell        string
		gitPush      bool
		explicit     bool
		confidence   string
	}{
		{"deny write edit", "claude-code", []string{CapEditRepo}, WritesDeny, ShellAllow, false, true, "medium"},
		{"deny shell readonly shell", "shell", []string{CapShellReadonly}, WritesDeny, ShellDeny, false, true, "medium"},
		{"probe shell", RunnerProbe, []string{CapShellReadonly}, WritesDeny, ShellDeny, false, true, "high"},
		{"desktop no explicit", "claude-code", []string{CapDesktopControl}, WritesAllow, ShellAllow, false, false, "medium"},
		{"agent high confidence", "claude-code", []string{CapReadFiles}, WritesDeny, ShellDeny, false, false, "high"},
		{"git push while denied", "claude-code", []string{CapGitPush}, WritesDeny, ShellAllow, true, true, "medium"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateSemantics(tc.name, tc.runner, tc.capabilities, tc.writes, tc.shell, false, tc.gitPush, tc.explicit, tc.confidence)
			if err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestValidateSemanticsAllowsQueryTemplate(t *testing.T) {
	tmpl, ok := TemplateByName(Query)
	if !ok {
		t.Fatal("query template missing")
	}
	if err := ValidateSemantics(tmpl.Name, tmpl.Runner, tmpl.Capabilities, tmpl.Writes, tmpl.Shell, tmpl.Network, tmpl.GitPush, tmpl.RequireExplicit, tmpl.EnforcementConfidence); err != nil {
		t.Fatalf("query template should validate: %v", err)
	}
}

func TestBuiltinsIncludeRecommendedTemplateCatalog(t *testing.T) {
	want := []string{Query, QueryNetwork, QueryShell, ReadonlyAgent, RepoDev, DesktopControl, SystemChange}
	for _, name := range want {
		tmpl, ok := TemplateByName(name)
		if !ok {
			t.Fatalf("missing template %q", name)
		}
		runner := tmpl.Runner
		if runner == "" {
			runner = "claude-code"
		}
		if err := ValidateSemantics(tmpl.Name, runner, tmpl.Capabilities, tmpl.Writes, tmpl.Shell, tmpl.Network, tmpl.GitPush, tmpl.RequireExplicit, tmpl.EnforcementConfidence); err != nil {
			t.Fatalf("template %q should validate: %v", name, err)
		}
	}
}

func TestGenerateQueryShellTemplateRendersAllowlist(t *testing.T) {
	tmpl, err := Generate(QueryShell, GenerateOptions{OS: "windows"})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if tmpl.Runner != RunnerShell || tmpl.Shell != ShellAllowlisted {
		t.Fatalf("query-shell template = %+v", tmpl)
	}
	body := RenderTOML(tmpl)
	for _, want := range []string{"[profiles.query-shell]", `runner = "shell"`, "[profiles.query-shell.shell_policy.windows]", `allowed_commands = ["Get-Service"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("rendered TOML missing %q:\n%s", want, body)
		}
	}
}
