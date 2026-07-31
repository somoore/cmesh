package profile

import (
	"fmt"
	"sort"
	"strings"
)

const (
	Query          = "query"
	QueryNetwork   = "query-network"
	QueryShell     = "query-shell"
	ReadonlyAgent  = "readonly-agent"
	LegacyReadonly = "readonly"
	RepoDev        = "repo-dev"
	LegacyDev      = "dev"
	DesktopControl = "desktop-control"
	SystemChange   = "system-change"

	RunnerProbe = "probe"
	RunnerShell = "shell"

	WritesDeny     = "deny"
	WritesRepoOnly = "repo_only"
	WritesAllow    = "allow"

	ShellDeny        = "deny"
	ShellAllowlisted = "allowlisted"
	ShellAllow       = "allow"

	CapDiskInfo            = "disk_info"
	CapSystemInfo          = "system_info"
	CapMemoryInfo          = "memory_info"
	CapUptime              = "uptime"
	CapRepoStatus          = "repo_status"
	CapServiceStatus       = "service_status"
	CapProcessList         = "process_list"
	CapLogTail             = "log_tail"
	CapLogTailBounded      = "log_tail_bounded"
	CapPortCheck           = "port_check"
	CapNetworkCheck        = "network_check"
	CapShellReadonly       = "shell_readonly"
	CapReadFiles           = "read_files"
	CapEditRepo            = "edit_repo"
	CapExecuteInRepo       = "execute_in_repo"
	CapExecuteRepoCommands = "execute_repo_commands"
	CapNetworkLegacy       = "network"
	CapLaunchProcesses     = "launch_processes"
	CapGitPush             = "git_push"
	CapDesktopControl      = "desktop_control"
	CapDesktopInteraction  = "desktop_interaction"
	CapSystemChange        = "system_change"

	RiskLow    = "low"
	RiskMedium = "medium"
	RiskHigh   = "high"
)

type Template struct {
	Name                  string
	Description           string
	Runner                string
	Intents               []string
	Capabilities          []string
	AllowedPeers          []string
	Writes                string
	Shell                 string
	Network               bool
	GitPush               bool
	RequireExplicit       bool
	RequireLease          bool
	Risk                  string
	MaxRuntime            string
	MaxOutputBytes        int64
	AuditLevel            string
	EnforcementConfidence string
	KnownGaps             []string
	ShellPolicy           ShellPolicy
}

type ShellPolicy struct {
	Mode            string
	AllowedCommands map[string][]string
	DeniedPatterns  []string
}

func Builtins() []Template {
	return []Template{
		{
			Name:                  Query,
			Description:           "Deterministic read-only machine queries. No shell, no writes, no arbitrary agent.",
			Runner:                RunnerProbe,
			Intents:               []string{"query"},
			Capabilities:          []string{CapDiskInfo, CapSystemInfo, CapMemoryInfo, CapUptime, CapRepoStatus, CapProcessList},
			Writes:                WritesDeny,
			Shell:                 ShellDeny,
			Risk:                  RiskLow,
			MaxRuntime:            "30s",
			MaxOutputBytes:        65536,
			AuditLevel:            "normal",
			EnforcementConfidence: "high",
		},
		{
			Name:                  QueryNetwork,
			Description:           "Deterministic read-only queries plus bounded network checks.",
			Runner:                RunnerProbe,
			Intents:               []string{"query"},
			Capabilities:          []string{CapDiskInfo, CapSystemInfo, CapMemoryInfo, CapUptime, CapRepoStatus, CapProcessList, CapNetworkCheck, CapPortCheck},
			Writes:                WritesDeny,
			Shell:                 ShellDeny,
			Network:               true,
			RequireExplicit:       true,
			Risk:                  RiskMedium,
			MaxRuntime:            "30s",
			MaxOutputBytes:        65536,
			AuditLevel:            "loud",
			EnforcementConfidence: "high",
		},
		{
			Name:                  QueryShell,
			Description:           "Allowlisted read-oriented diagnostics. No writes.",
			Runner:                "shell",
			Intents:               []string{"query"},
			Capabilities:          []string{CapShellReadonly, CapServiceStatus, CapProcessList, CapLogTailBounded, CapLogTail},
			Writes:                WritesDeny,
			Shell:                 ShellAllowlisted,
			RequireExplicit:       true,
			Risk:                  RiskMedium,
			MaxRuntime:            "30s",
			MaxOutputBytes:        65536,
			AuditLevel:            "loud",
			EnforcementConfidence: "medium",
			ShellPolicy: ShellPolicy{
				Mode: "allowlist",
				AllowedCommands: map[string][]string{
					"windows": {"Get-Service", "Get-Process", "Get-EventLog", "Get-PSDrive"},
					"darwin":  {"df", "uname", "sysctl", "ps", "log"},
					"linux":   {"df", "uname", "systemctl", "ps", "journalctl"},
				},
				DeniedPatterns: []string{">", ">>", "|", "&&", "||", ";", "`", "$(", " rm ", " del ", " remove-item", "set-content", "out-file"},
			},
		},
		{
			Name:                  ReadonlyAgent,
			Description:           "Agent-assisted open-ended read-only inspection without shell. Depends on harness read controls.",
			Runner:                "",
			Intents:               []string{"inspect"},
			Capabilities:          []string{CapReadFiles},
			Writes:                WritesDeny,
			Shell:                 ShellDeny,
			Risk:                  RiskMedium,
			MaxRuntime:            "30m",
			MaxOutputBytes:        1 << 20,
			AuditLevel:            "normal",
			EnforcementConfidence: "medium",
			KnownGaps:             []string{"open-ended file read boundaries depend on the selected harness and OS sandbox"},
		},
		{
			Name:                  RepoDev,
			Description:           "Agent-assisted repo-local edits and test commands inside allowed cwd.",
			Runner:                "",
			Intents:               []string{"build", "modify"},
			Capabilities:          []string{CapReadFiles, CapEditRepo, CapExecuteInRepo},
			Writes:                WritesRepoOnly,
			Shell:                 ShellAllow,
			RequireExplicit:       true,
			RequireLease:          true,
			Risk:                  RiskHigh,
			MaxRuntime:            "2h",
			MaxOutputBytes:        1 << 20,
			AuditLevel:            "loud",
			EnforcementConfidence: "medium",
			KnownGaps:             []string{"repo-dev authority depends on runner and OS account permissions"},
		},
		{
			Name:                  DesktopControl,
			Description:           "High-risk desktop/session control. Requires explicit selection.",
			Runner:                "",
			Intents:               []string{"desktop"},
			Capabilities:          []string{CapReadFiles, CapDesktopControl, CapDesktopInteraction},
			Writes:                WritesAllow,
			Shell:                 ShellAllow,
			Network:               true,
			RequireExplicit:       true,
			RequireLease:          true,
			Risk:                  RiskHigh,
			MaxRuntime:            "30m",
			MaxOutputBytes:        1 << 20,
			AuditLevel:            "loud",
			EnforcementConfidence: "medium",
			KnownGaps:             []string{"desktop control depends on OS session permissions and runner controls"},
		},
		{
			Name:                  SystemChange,
			Description:           "High-risk system/service/package changes. Requires explicit selection.",
			Runner:                "",
			Intents:               []string{"system"},
			Capabilities:          []string{CapReadFiles, CapSystemChange, CapLaunchProcesses},
			Writes:                WritesAllow,
			Shell:                 ShellAllow,
			Network:               true,
			RequireExplicit:       true,
			RequireLease:          true,
			Risk:                  RiskHigh,
			MaxRuntime:            "30m",
			MaxOutputBytes:        1 << 20,
			AuditLevel:            "loud",
			EnforcementConfidence: "medium",
			KnownGaps:             []string{"system changes depend on OS account permissions and may need human approval"},
		},
	}
}

func TemplateByName(name string) (Template, bool) {
	name = strings.TrimSpace(name)
	for _, tmpl := range Builtins() {
		if tmpl.Name == name {
			return tmpl, true
		}
	}
	return Template{}, false
}

func KnownCapabilities() map[string]struct{} {
	values := []string{
		CapDiskInfo,
		CapSystemInfo,
		CapMemoryInfo,
		CapUptime,
		CapRepoStatus,
		CapServiceStatus,
		CapProcessList,
		CapLogTail,
		CapLogTailBounded,
		CapPortCheck,
		CapNetworkCheck,
		CapShellReadonly,
		CapReadFiles,
		CapEditRepo,
		CapExecuteInRepo,
		CapExecuteRepoCommands,
		CapNetworkLegacy,
		CapLaunchProcesses,
		CapGitPush,
		CapDesktopControl,
		CapDesktopInteraction,
		CapSystemChange,
	}
	out := make(map[string]struct{}, len(values))
	for _, value := range values {
		out[value] = struct{}{}
	}
	return out
}

func NormalizeCapabilities(capabilities, claims []string) []string {
	if len(capabilities) == 0 {
		capabilities = claims
	}
	seen := map[string]struct{}{}
	var out []string
	for _, value := range capabilities {
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
	sort.Strings(out)
	return out
}

// RiskRank orders risk levels so they can be compared. Unknown or empty values
// rank lowest, which is safe for ceiling comparison: an unrankable ceiling can
// never be treated as outranking a known profile.
func RiskRank(risk string) int {
	switch risk {
	case RiskLow:
		return 1
	case RiskMedium:
		return 2
	case RiskHigh:
		return 3
	default:
		return 0
	}
}

func RiskForProfile(name string) string {
	switch CanonicalProfile(name) {
	case Query:
		return RiskLow
	case QueryNetwork, QueryShell, ReadonlyAgent:
		return RiskMedium
	case RepoDev, DesktopControl, SystemChange:
		return RiskHigh
	default:
		return ""
	}
}

func RiskForConfig(name string, capabilities []string, writes, shell string, network bool) string {
	if risk := RiskForProfile(name); risk != "" {
		return risk
	}
	if writes == WritesAllow || contains(capabilities, CapDesktopControl) || contains(capabilities, CapSystemChange) || contains(capabilities, CapLaunchProcesses) {
		return RiskHigh
	}
	if writes == WritesRepoOnly || shell == ShellAllow || contains(capabilities, CapEditRepo) || contains(capabilities, CapExecuteInRepo) {
		return RiskHigh
	}
	if network || shell == ShellAllowlisted || contains(capabilities, CapNetworkCheck) || contains(capabilities, CapPortCheck) || contains(capabilities, CapShellReadonly) || contains(capabilities, CapReadFiles) {
		return RiskMedium
	}
	return RiskLow
}

// Semantics describes a profile's cross-field constraints for validation.
// It is a struct rather than a positional argument list because the boolean
// fields are interchangeable at a call site: adding one more bool to a
// positional signature makes silent argument-order mistakes easy, and these
// fields gate authority.
type Semantics struct {
	Name                  string
	Runner                string
	Capabilities          []string
	Writes                string
	Shell                 string
	Network               bool
	GitPush               bool
	RequireExplicit       bool
	RequireLease          bool
	EnforcementConfidence string
}

// HighRiskCapabilities returns the capabilities that make a profile high-risk
// regardless of what it is named. These are the capabilities that let a task
// modify the worker beyond reading: repo edits, command execution, pushes,
// desktop control, system change, and process launching.
//
// The list is keyed on capabilities rather than on the profile name or the
// computed risk string on purpose. Name-keyed checks miss a high-risk profile
// under a custom name, and RiskForConfig reports high for shell=allow alone,
// which ApplyDefaults sets for any profile granting execute_in_repo — that
// would sweep in profiles this guard is not meant to cover.
func HighRiskCapabilities() []string {
	return []string{
		CapEditRepo,
		CapExecuteInRepo,
		CapExecuteRepoCommands,
		CapGitPush,
		CapDesktopControl,
		CapDesktopInteraction,
		CapSystemChange,
		CapLaunchProcesses,
	}
}

// HighRiskCapability reports whether capabilities grant any high-risk capability,
// returning the first one found for use in error messages.
func HighRiskCapability(capabilities []string) (string, bool) {
	for _, candidate := range HighRiskCapabilities() {
		if contains(capabilities, candidate) {
			return candidate, true
		}
	}
	return "", false
}

// semanticsForTemplate builds validation input from a template, letting the
// caller override the runner for templates that leave it agent-selected.
func semanticsForTemplate(tmpl Template, runner string) Semantics {
	return Semantics{
		Name:                  tmpl.Name,
		Runner:                runner,
		Capabilities:          tmpl.Capabilities,
		Writes:                tmpl.Writes,
		Shell:                 tmpl.Shell,
		Network:               tmpl.Network,
		GitPush:               tmpl.GitPush,
		RequireExplicit:       tmpl.RequireExplicit,
		RequireLease:          tmpl.RequireLease,
		EnforcementConfidence: tmpl.EnforcementConfidence,
	}
}

func ValidateSemantics(s Semantics) error {
	name := s.Name
	runner := s.Runner
	capabilities := s.Capabilities
	writes := s.Writes
	shell := s.Shell
	gitPush := s.GitPush
	requireExplicit := s.RequireExplicit
	confidence := s.EnforcementConfidence
	known := KnownCapabilities()
	for _, capability := range capabilities {
		if _, ok := known[capability]; !ok {
			return fmt.Errorf("profiles.%s has unknown capability %q", name, capability)
		}
	}
	if writes == "" {
		writes = WritesRepoOnly
	}
	switch writes {
	case WritesDeny, WritesRepoOnly, WritesAllow:
	default:
		return fmt.Errorf("profiles.%s.writes must be deny, repo_only, or allow", name)
	}
	if shell == "" {
		shell = ShellAllow
	}
	switch shell {
	case ShellDeny, ShellAllowlisted, ShellAllow:
	default:
		return fmt.Errorf("profiles.%s.shell must be deny, allowlisted, or allow", name)
	}
	if contains(capabilities, CapEditRepo) && writes == WritesDeny {
		return fmt.Errorf("profiles.%s cannot combine writes=deny with edit_repo", name)
	}
	if contains(capabilities, CapGitPush) && (writes == WritesDeny || !gitPush) {
		return fmt.Errorf("profiles.%s cannot grant git_push while writes are denied or git_push=false", name)
	}
	if contains(capabilities, CapShellReadonly) && shell == ShellDeny {
		return fmt.Errorf("profiles.%s cannot combine shell=deny with shell_readonly", name)
	}
	if runner == RunnerProbe {
		for _, denied := range []string{CapServiceStatus, CapLogTail, CapLogTailBounded, CapShellReadonly, CapReadFiles, CapEditRepo, CapExecuteInRepo, CapExecuteRepoCommands, CapGitPush, CapDesktopControl, CapDesktopInteraction, CapSystemChange} {
			if contains(capabilities, denied) {
				return fmt.Errorf("profiles.%s uses probe runner but grants %s", name, denied)
			}
		}
	}
	// A profile granting any high-risk capability must be requested by name and
	// backed by a worker-issued lease, whatever it is called. Keying this on
	// capabilities rather than the profile name closes the gap where a custom
	// name (e.g. "my-dev" granting edit_repo) escaped the invariant that the
	// built-in repo-dev/desktop-control/system-change templates satisfy.
	if capability, ok := HighRiskCapability(capabilities); ok {
		if !requireExplicit {
			return fmt.Errorf("profiles.%s grants high-risk capability %q and must set require_explicit", name, capability)
		}
		if !s.RequireLease {
			return fmt.Errorf("profiles.%s grants high-risk capability %q and must set require_lease", name, capability)
		}
	}
	if confidence == "high" && runner != RunnerProbe {
		return fmt.Errorf("profiles.%s cannot claim high enforcement confidence with runner %q", name, runner)
	}
	_ = s.Network
	return nil
}

func contains(values []string, needle string) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
}
