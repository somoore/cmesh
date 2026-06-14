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
	CapRepoStatus          = "repo_status"
	CapServiceStatus       = "service_status"
	CapProcessList         = "process_list"
	CapLogTail             = "log_tail"
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
)

type Template struct {
	Name                  string
	Description           string
	Runner                string
	Intents               []string
	Capabilities          []string
	Writes                string
	Shell                 string
	Network               bool
	GitPush               bool
	RequireExplicit       bool
	RequireLease          bool
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
			Capabilities:          []string{CapDiskInfo, CapSystemInfo, CapRepoStatus},
			Writes:                WritesDeny,
			Shell:                 ShellDeny,
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
			Capabilities:          []string{CapDiskInfo, CapSystemInfo, CapRepoStatus, CapNetworkCheck},
			Writes:                WritesDeny,
			Shell:                 ShellDeny,
			Network:               true,
			RequireExplicit:       true,
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
			Capabilities:          []string{CapShellReadonly, CapServiceStatus, CapProcessList, CapLogTail},
			Writes:                WritesDeny,
			Shell:                 ShellAllowlisted,
			RequireExplicit:       true,
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
			Description:           "Agent-assisted read-only inspection. Depends on harness controls.",
			Runner:                "",
			Intents:               []string{"inspect"},
			Capabilities:          []string{CapReadFiles},
			Writes:                WritesDeny,
			Shell:                 ShellDeny,
			MaxRuntime:            "30m",
			MaxOutputBytes:        1 << 20,
			AuditLevel:            "normal",
			EnforcementConfidence: "medium",
			KnownGaps:             []string{"read-only agent enforcement depends on the selected harness"},
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
		CapRepoStatus,
		CapServiceStatus,
		CapProcessList,
		CapLogTail,
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

func ValidateSemantics(name, runner string, capabilities []string, writes, shell string, network, gitPush, requireExplicit bool, confidence string) error {
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
		for _, denied := range []string{CapShellReadonly, CapReadFiles, CapEditRepo, CapExecuteInRepo, CapExecuteRepoCommands, CapGitPush, CapDesktopControl, CapDesktopInteraction, CapSystemChange} {
			if contains(capabilities, denied) {
				return fmt.Errorf("profiles.%s uses probe runner but grants %s", name, denied)
			}
		}
	}
	if (contains(capabilities, CapDesktopControl) || contains(capabilities, CapSystemChange)) && !requireExplicit {
		return fmt.Errorf("profiles.%s grants high-risk capabilities and must require explicit selection", name)
	}
	if confidence == "high" && runner != RunnerProbe {
		return fmt.Errorf("profiles.%s cannot claim high enforcement confidence with runner %q", name, runner)
	}
	_ = network
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
