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
		lease        bool
		confidence   string
	}{
		{"deny write edit", "claude-code", []string{CapEditRepo}, WritesDeny, ShellAllow, false, true, true, "medium"},
		{"deny shell readonly shell", "shell", []string{CapShellReadonly}, WritesDeny, ShellDeny, false, true, true, "medium"},
		{"probe shell", RunnerProbe, []string{CapShellReadonly}, WritesDeny, ShellDeny, false, true, true, "high"},
		{"desktop no explicit", "claude-code", []string{CapDesktopControl}, WritesAllow, ShellAllow, false, false, true, "medium"},
		{"agent high confidence", "claude-code", []string{CapReadFiles}, WritesDeny, ShellDeny, false, false, false, "high"},
		{"git push while denied", "claude-code", []string{CapGitPush}, WritesDeny, ShellAllow, true, true, true, "medium"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateSemantics(Semantics{
				Name:                  tc.name,
				Runner:                tc.runner,
				Capabilities:          tc.capabilities,
				Writes:                tc.writes,
				Shell:                 tc.shell,
				GitPush:               tc.gitPush,
				RequireExplicit:       tc.explicit,
				RequireLease:          tc.lease,
				EnforcementConfidence: tc.confidence,
			})
			if err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

// High-risk capabilities must be gated by require_explicit and require_lease
// whatever the profile is named. A name-keyed check let a custom name such as
// "my-dev" grant edit_repo with neither flag set.
func TestValidateSemanticsRequiresExplicitAndLeaseForCustomHighRiskName(t *testing.T) {
	for _, capability := range HighRiskCapabilities() {
		t.Run(capability, func(t *testing.T) {
			base := Semantics{
				Name:                  "my-custom-profile",
				Runner:                "claude-code",
				Capabilities:          []string{capability},
				Writes:                WritesAllow,
				Shell:                 ShellAllow,
				GitPush:               capability == CapGitPush,
				EnforcementConfidence: "medium",
			}

			missingBoth := base
			if err := ValidateSemantics(missingBoth); err == nil {
				t.Fatal("expected error when neither require_explicit nor require_lease is set")
			}

			explicitOnly := base
			explicitOnly.RequireExplicit = true
			err := ValidateSemantics(explicitOnly)
			if err == nil {
				t.Fatal("expected error when require_lease is missing")
			}
			if !strings.Contains(err.Error(), "require_lease") {
				t.Fatalf("error should name require_lease, got %v", err)
			}

			both := explicitOnly
			both.RequireLease = true
			if err := ValidateSemantics(both); err != nil {
				t.Fatalf("profile with both flags should validate: %v", err)
			}
		})
	}
}

func TestValidateSemanticsAllowsQueryTemplate(t *testing.T) {
	tmpl, ok := TemplateByName(Query)
	if !ok {
		t.Fatal("query template missing")
	}
	if err := ValidateSemantics(semanticsForTemplate(tmpl, tmpl.Runner)); err != nil {
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
		if err := ValidateSemantics(semanticsForTemplate(tmpl, runner)); err != nil {
			t.Fatalf("template %q should validate: %v", name, err)
		}
		if name == ReadonlyAgent && tmpl.Shell != ShellDeny {
			t.Fatalf("readonly-agent shell = %q, want deny", tmpl.Shell)
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
