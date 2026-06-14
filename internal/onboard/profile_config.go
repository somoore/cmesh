package onboard

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"cmesh/internal/config"
	profiledefs "cmesh/internal/profile"
)

type ProfileConfigureOptions struct {
	Template     string
	Name         string
	Runner       string
	AllowedPeers []string
	Force        bool
}

type ProfileConfigureResult struct {
	Path              string   `json:"path"`
	Profile           string   `json:"profile"`
	Runner            string   `json:"runner"`
	RunnerCommand     []string `json:"runner_command,omitempty"`
	AlreadyConfigured bool     `json:"already_configured,omitempty"`
	ProfileAdded      bool     `json:"profile_added"`
	RunnerAdded       bool     `json:"runner_added"`
	ProfileRepaired   bool     `json:"profile_repaired,omitempty"`
	RunnerRepaired    bool     `json:"runner_repaired,omitempty"`
	PreviewTOML       string   `json:"preview_toml,omitempty"`
	NextStep          string   `json:"next_step,omitempty"`
}

func ConfigureProfile(path string, opts ProfileConfigureOptions, confirmed bool) (ProfileConfigureResult, error) {
	if path == "" {
		path = config.DefaultPath()
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return ProfileConfigureResult{}, err
	}
	text := string(body)
	tmpl, runnerCommand, err := profileTemplateForConfig(text, opts)
	if err != nil {
		return ProfileConfigureResult{}, err
	}
	profileExists := hasTOMLSection(text, "profiles."+tmpl.Name)
	runnerExists := hasTOMLSection(text, "runners."+tmpl.Runner)
	profileRepair := profileExists && (opts.Force || profileNeedsRepair(text, tmpl))
	runnerRepair := runnerExists && opts.Force
	result := ProfileConfigureResult{
		Path:              path,
		Profile:           tmpl.Name,
		Runner:            tmpl.Runner,
		RunnerCommand:     runnerCommand,
		AlreadyConfigured: profileExists,
		ProfileAdded:      !profileExists,
		RunnerAdded:       !runnerExists,
		ProfileRepaired:   profileRepair,
		RunnerRepaired:    runnerRepair,
		NextStep:          "Profile is configured. Use profile_use with the required acknowledgement/TTL, then delegate.",
	}
	result.PreviewTOML = profileConfigSnippet(tmpl, runnerCommand, profileExists && !profileRepair, runnerExists && !runnerRepair)
	if !confirmed {
		return result, nil
	}
	if result.PreviewTOML == "" {
		if _, err := config.Load(path); err != nil {
			return ProfileConfigureResult{}, err
		}
		return result, nil
	}
	nextBase := text
	if profileRepair {
		nextBase = removeTOMLSections(nextBase, "profiles."+tmpl.Name)
	}
	if runnerRepair {
		nextBase = removeTOMLSections(nextBase, "runners."+tmpl.Runner)
	}
	next := strings.TrimRight(nextBase, "\r\n") + "\n\n" + strings.TrimSpace(result.PreviewTOML) + "\n"
	if err := writeProfileConfig(path, next); err != nil {
		return ProfileConfigureResult{}, err
	}
	if _, err := config.Load(path); err != nil {
		return ProfileConfigureResult{}, fmt.Errorf("profile config written but config is invalid: %w", err)
	}
	return result, nil
}

func profileTemplateForConfig(configText string, opts ProfileConfigureOptions) (profiledefs.Template, []string, error) {
	templateName := strings.TrimSpace(opts.Template)
	if templateName == "" {
		templateName = profiledefs.RepoDev
	}
	runnerName := strings.TrimSpace(opts.Runner)
	if runnerName == "auto" {
		runnerName = ""
	}
	base, ok := profiledefs.TemplateByName(templateName)
	if !ok {
		return profiledefs.Template{}, nil, fmt.Errorf("unknown profile template %q", templateName)
	}
	var runnerCommand []string
	if base.Runner == "" {
		runnerName, runnerCommand = chooseProfileConfigureRunner(configText, runnerName)
		if runnerName == "" {
			return profiledefs.Template{}, nil, fmt.Errorf("profile %q requires an agent runner; install codex or Claude Code, or pass --runner", templateName)
		}
	}
	tmpl, err := profiledefs.Generate(templateName, profiledefs.GenerateOptions{Name: opts.Name, Runner: runnerName})
	if err != nil {
		return profiledefs.Template{}, nil, err
	}
	tmpl.AllowedPeers = uniqueStrings(opts.AllowedPeers)
	return tmpl, runnerCommand, nil
}

func profileConfigSnippet(tmpl profiledefs.Template, runnerCommand []string, skipProfile, skipRunner bool) string {
	var parts []string
	if !skipProfile {
		parts = append(parts, strings.TrimSpace(profiledefs.RenderProfileTOML(tmpl)))
	}
	if !skipRunner {
		parts = append(parts, strings.TrimSpace(profiledefs.RenderRunnerTOMLWithCommand(tmpl, runnerCommand)))
	}
	return strings.Join(parts, "\n\n")
}

func chooseProfileConfigureRunner(configText, requested string) (string, []string) {
	if requested != "" {
		return runnerWithCommand(requested)
	}
	for _, preferred := range []string{"codex", "claude-code"} {
		if hasTOMLSection(configText, "runners."+preferred) {
			return preferred, nil
		}
	}
	for _, preferred := range []string{"codex", "claude-code"} {
		name, command := runnerWithCommand(preferred)
		if command != nil {
			return name, command
		}
	}
	return "", nil
}

func runnerWithCommand(name string) (string, []string) {
	for _, runner := range DetectRunners(context.Background()) {
		if runner.Name == name && runner.Found {
			return name, runner.Command
		}
	}
	switch name {
	case "codex":
		return name, []string{"codex"}
	case "claude-code":
		return name, []string{"claude", "-p"}
	default:
		return name, []string{name}
	}
}

func hasTOMLSection(text, section string) bool {
	for _, line := range strings.Split(text, "\n") {
		if tomlSectionName(line) == section {
			return true
		}
	}
	return false
}

func profileNeedsRepair(text string, tmpl profiledefs.Template) bool {
	section := tomlSectionText(text, "profiles."+tmpl.Name)
	if section == "" {
		return false
	}
	if !sectionHasStringKV(section, "runner", tmpl.Runner) {
		return true
	}
	for _, peer := range tmpl.AllowedPeers {
		if !sectionArrayContains(section, "allowed_peers", peer) {
			return true
		}
	}
	if tmpl.RequireExplicit && !sectionHasBoolKV(section, "require_explicit", true) {
		return true
	}
	if tmpl.RequireLease && !sectionHasBoolKV(section, "require_lease", true) {
		return true
	}
	if !sectionHasStringKV(section, "writes", tmpl.Writes) {
		return true
	}
	if !sectionHasStringKV(section, "shell", tmpl.Shell) {
		return true
	}
	return false
}

func tomlSectionText(text, section string) string {
	var b strings.Builder
	inSection := false
	for _, line := range strings.Split(text, "\n") {
		if current := tomlSectionName(line); current != "" {
			if inSection {
				break
			}
			inSection = current == section
		}
		if inSection {
			b.WriteString(line)
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func removeTOMLSections(text, section string) string {
	var out []string
	skip := false
	for _, line := range strings.Split(text, "\n") {
		if current := tomlSectionName(line); current != "" {
			skip = current == section || strings.HasPrefix(current, section+".")
		}
		if !skip {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}

func tomlSectionName(line string) string {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "[") {
		return ""
	}
	end := strings.Index(line, "]")
	if end < 0 {
		return ""
	}
	return strings.TrimSpace(line[1:end])
}

func sectionHasStringKV(section, key, value string) bool {
	for _, line := range strings.Split(section, "\n") {
		parts := strings.SplitN(strings.TrimSpace(line), "=", 2)
		if len(parts) != 2 || strings.TrimSpace(parts[0]) != key {
			continue
		}
		return strings.Trim(strings.TrimSpace(parts[1]), `"`) == value
	}
	return false
}

func sectionHasBoolKV(section, key string, value bool) bool {
	want := "false"
	if value {
		want = "true"
	}
	for _, line := range strings.Split(section, "\n") {
		parts := strings.SplitN(strings.TrimSpace(line), "=", 2)
		if len(parts) != 2 || strings.TrimSpace(parts[0]) != key {
			continue
		}
		return strings.TrimSpace(parts[1]) == want
	}
	return false
}

func sectionArrayContains(section, key, value string) bool {
	for _, line := range strings.Split(section, "\n") {
		parts := strings.SplitN(strings.TrimSpace(line), "=", 2)
		if len(parts) != 2 || strings.TrimSpace(parts[0]) != key {
			continue
		}
		return strings.Contains(parts[1], `"`+value+`"`)
	}
	return false
}

func writeProfileConfig(path, body string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "cmesh-profile-*.toml")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()
	if _, err := tmp.WriteString(body); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpPath, 0o600); err != nil {
		return err
	}
	if _, err := config.Load(tmpPath); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	cleanup = false
	return nil
}

func uniqueStrings(values []string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}
