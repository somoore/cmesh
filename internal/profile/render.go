package profile

import (
	"fmt"
	"runtime"
	"sort"
	"strings"
)

type GenerateOptions struct {
	Name         string
	Runner       string
	Capabilities []string
	OS           string
}

func Generate(name string, opts GenerateOptions) (Template, error) {
	tmpl, ok := TemplateByName(name)
	if !ok {
		return Template{}, fmt.Errorf("unknown profile template %q", name)
	}
	if strings.TrimSpace(opts.Name) != "" {
		tmpl.Name = strings.TrimSpace(opts.Name)
	}
	if strings.TrimSpace(opts.Runner) != "" {
		tmpl.Runner = strings.TrimSpace(opts.Runner)
	} else if tmpl.Runner == "" {
		tmpl.Runner = "claude-code"
	}
	if len(opts.Capabilities) > 0 {
		tmpl.Capabilities = NormalizeCapabilities(opts.Capabilities, nil)
	}
	if strings.TrimSpace(opts.OS) != "" && len(tmpl.ShellPolicy.AllowedCommands) > 0 {
		osName := strings.ToLower(strings.TrimSpace(opts.OS))
		if _, ok := tmpl.ShellPolicy.AllowedCommands[osName]; !ok {
			return Template{}, fmt.Errorf("unsupported shell policy os %q", opts.OS)
		}
		tmpl.ShellPolicy.AllowedCommands = map[string][]string{osName: append([]string(nil), tmpl.ShellPolicy.AllowedCommands[osName]...)}
	}
	if err := ValidateSemantics(semanticsForTemplate(tmpl, tmpl.Runner)); err != nil {
		return Template{}, err
	}
	return tmpl, nil
}

func RenderTOML(tmpl Template) string {
	var b strings.Builder
	b.WriteString(RenderProfileTOML(tmpl))
	b.WriteString("\n")
	b.WriteString(RenderRunnerTOML(tmpl))
	return b.String()
}

func RenderProfileTOML(tmpl Template) string {
	var b strings.Builder
	writeSection(&b, "profiles."+tmpl.Name)
	writeKV(&b, "description", tmpl.Description)
	writeKV(&b, "runner", tmpl.Runner)
	writeArray(&b, "intents", tmpl.Intents)
	writeArray(&b, "capabilities", tmpl.Capabilities)
	writeArray(&b, "claims", tmpl.Capabilities)
	writeArray(&b, "allowed_peers", tmpl.AllowedPeers)
	writeKV(&b, "writes", defaultString(tmpl.Writes, WritesDeny))
	writeKV(&b, "shell", defaultString(tmpl.Shell, ShellDeny))
	writeBool(&b, "network", tmpl.Network)
	writeBool(&b, "git_push", tmpl.GitPush)
	writeBool(&b, "require_explicit", tmpl.RequireExplicit)
	writeBool(&b, "require_lease", tmpl.RequireLease)
	writeKV(&b, "risk", defaultString(tmpl.Risk, RiskForProfile(tmpl.Name)))
	writeKV(&b, "max_runtime", defaultString(tmpl.MaxRuntime, "30s"))
	if tmpl.MaxOutputBytes > 0 {
		fmt.Fprintf(&b, "max_output_bytes = %d\n", tmpl.MaxOutputBytes)
	}
	writeKV(&b, "audit_level", defaultString(tmpl.AuditLevel, "normal"))
	writeKV(&b, "enforcement_confidence", defaultString(tmpl.EnforcementConfidence, "medium"))
	writeArray(&b, "known_gaps", tmpl.KnownGaps)
	if tmpl.ShellPolicy.Mode != "" || len(tmpl.ShellPolicy.AllowedCommands) > 0 || len(tmpl.ShellPolicy.DeniedPatterns) > 0 {
		b.WriteString("\n")
		writeSection(&b, "profiles."+tmpl.Name+".shell_policy")
		writeKV(&b, "mode", defaultString(tmpl.ShellPolicy.Mode, "allowlist"))
		writeArray(&b, "denied_patterns", tmpl.ShellPolicy.DeniedPatterns)
		keys := make([]string, 0, len(tmpl.ShellPolicy.AllowedCommands))
		for osName := range tmpl.ShellPolicy.AllowedCommands {
			keys = append(keys, osName)
		}
		sort.Strings(keys)
		for _, osName := range keys {
			b.WriteString("\n")
			writeSection(&b, "profiles."+tmpl.Name+".shell_policy."+osName)
			writeArray(&b, "allowed_commands", tmpl.ShellPolicy.AllowedCommands[osName])
		}
	}
	return b.String()
}

func RenderRunnerTOML(tmpl Template) string {
	return RenderRunnerTOMLWithCommand(tmpl, nil)
}

func RenderRunnerTOMLWithCommand(tmpl Template, command []string) string {
	var b strings.Builder
	b.WriteString("\n")
	writeSection(&b, "runners."+tmpl.Runner)
	if len(command) > 0 {
		writeArray(&b, "command", command)
	} else if tmpl.Runner == RunnerProbe || tmpl.Runner == RunnerShell {
		writeArray(&b, "command", []string{"builtin"})
	} else {
		writeArray(&b, "command", []string{tmpl.Runner})
	}
	writeKV(&b, "default_profile", tmpl.Name)
	if tmpl.Runner != RunnerProbe && tmpl.Runner != RunnerShell {
		fmt.Fprintf(&b, "concurrency = %d\n", 1)
	}
	return b.String()
}

func CurrentOS() string {
	return runtime.GOOS
}

func writeSection(b *strings.Builder, name string) {
	fmt.Fprintf(b, "[%s]\n", name)
}

func writeKV(b *strings.Builder, key, value string) {
	if value == "" {
		return
	}
	fmt.Fprintf(b, "%s = %q\n", key, value)
}

func writeBool(b *strings.Builder, key string, value bool) {
	if value {
		fmt.Fprintf(b, "%s = true\n", key)
	}
}

func writeArray(b *strings.Builder, key string, values []string) {
	if len(values) == 0 {
		return
	}
	fmt.Fprintf(b, "%s = [", key)
	for i, value := range values {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(b, "%q", value)
	}
	b.WriteString("]\n")
}

func defaultString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
