package profile

import "strings"

const (
	StrengthNative           = "native"
	StrengthAllowlisted      = "allowlisted"
	StrengthHarnessDependent = "harness-dependent"
	StrengthHighRisk         = "high-risk"
)

type PowerSummary struct {
	Reads                string `json:"reads"`
	Writes               string `json:"writes"`
	Deletes              string `json:"deletes"`
	Shell                string `json:"shell"`
	Network              string `json:"network"`
	ProcessDesktopSystem string `json:"process_desktop_system"`
}

type GuideItem struct {
	Name                  string       `json:"name"`
	DisplayName           string       `json:"display_name"`
	Description           string       `json:"description"`
	BestFor               []string     `json:"best_for"`
	AvoidWhen             []string     `json:"avoid_when,omitempty"`
	EnforcementStrength   string       `json:"enforcement_strength"`
	Powers                PowerSummary `json:"powers"`
	Capabilities          []string     `json:"capabilities"`
	Writes                string       `json:"writes"`
	Shell                 string       `json:"shell"`
	Network               bool         `json:"network"`
	RequireExplicit       bool         `json:"require_explicit"`
	RequireLease          bool         `json:"require_lease"`
	ConsentRequired       bool         `json:"consent_required"`
	Risk                  string       `json:"risk"`
	RecommendedTTL        string       `json:"recommended_ttl,omitempty"`
	SaferAlternative      string       `json:"safer_alternative,omitempty"`
	RecommendedFirstTool  string       `json:"recommended_first_tool,omitempty"`
	KnownGaps             []string     `json:"known_gaps,omitempty"`
	PlainLanguageContract string       `json:"plain_language_contract"`
}

type GuideResult struct {
	DefaultRecommendation string      `json:"default_recommendation"`
	DecisionRule          string      `json:"decision_rule"`
	FirstTool             string      `json:"first_tool"`
	Profiles              []GuideItem `json:"profiles"`
}

type Recommendation struct {
	Intent              string    `json:"intent"`
	Profile             string    `json:"profile"`
	Why                 string    `json:"why"`
	FirstTool           string    `json:"first_tool"`
	SaferAlternative    string    `json:"safer_alternative,omitempty"`
	RequiresConsent     bool      `json:"requires_consent"`
	RequiresTTL         bool      `json:"requires_ttl"`
	RecommendedTTL      string    `json:"recommended_ttl,omitempty"`
	AcknowledgementHint string    `json:"acknowledgement_hint,omitempty"`
	ProfileGuide        GuideItem `json:"profile_guide"`
}

type ProfileAcknowledgement struct {
	Profile         string `json:"profile"`
	Writes          string `json:"writes"`
	Shell           string `json:"shell"`
	Network         bool   `json:"network"`
	TTL             string `json:"ttl,omitempty"`
	AllowedCWDScope string `json:"allowed_cwd_scope,omitempty"`
}

func Guide() GuideResult {
	return GuideResult{
		DefaultRecommendation: Query,
		DecisionRule:          "Start with query for typed native probes. Move to readonly-agent for open-ended read-only system or file questions, query-network only for bounded network checks, query-shell only for allowlisted diagnostics, and repo-dev or higher only after explicit consent with a TTL and cwd scope.",
		FirstTool:             "query_peer",
		Profiles: []GuideItem{
			guideItem(Query),
			guideItem(QueryNetwork),
			guideItem(QueryShell),
			guideItem(ReadonlyAgent),
			guideItem(RepoDev),
			guideItem(DesktopControl),
			guideItem(SystemChange),
		},
	}
}

func GuideItemByName(name string) (GuideItem, bool) {
	name = CanonicalProfile(name)
	switch name {
	case Query, QueryNetwork, QueryShell, ReadonlyAgent, RepoDev, DesktopControl, SystemChange:
		return guideItem(name), true
	default:
		return GuideItem{}, false
	}
}

func Recommend(intent string) Recommendation {
	raw := strings.TrimSpace(intent)
	text := strings.ToLower(raw)
	profile := Query
	why := "Deterministic probe queries are the least-privilege starting point: no shell, no writes, no arbitrary agent."
	firstTool := "query_peer"

	switch {
	case anyContains(text, "restart", "install package", "uninstall", "service install", "system service", "admin", "registry", "firewall"):
		profile = SystemChange
		why = "System/service/package changes need the explicit high-risk system-change profile."
		firstTool = "profile_use then delegate"
	case anyContains(text, "desktop", "browser", "click", "type into", "screen", "ui automation"):
		profile = DesktopControl
		why = "Desktop or browser session control needs the explicit high-risk desktop-control profile."
		firstTool = "profile_use then delegate"
	case anyContains(text, "edit", "fix", "implement", "write code", "modify", "patch", "commit", "build", "test suite", "run tests", "run command"):
		profile = RepoDev
		why = "Repo edits, builds, and test commands require repo-dev because it allows repo-scoped writes and shell execution."
		firstTool = "profile_use then delegate"
	case anyContains(text, "read file", "read files", "inspect files", "search files", "open file", "open ", "arbitrary file", "summarize code", "review code", "battery", "app inventory", "installed app", "install date", "last reboot", "rebooted", "uptime", "anything"):
		profile = ReadonlyAgent
		why = "Open-ended read-only inspection needs an agent-backed read-only profile; it denies writes/deletes while allowing the worker to inspect system facts and files through the runner sandbox."
		firstTool = "inspect_peer"
	case anyContains(text, "logs", "service status", "process", "processes", "event log", "psdrive"):
		profile = QueryShell
		why = "Read-oriented OS diagnostics need query-shell because it allows only specific allowlisted commands and still denies writes."
		firstTool = "query_peer"
	case anyContains(text, "network", "http", "tcp", "port", "ping", "connect"):
		profile = QueryNetwork
		why = "Bounded connectivity checks need query-network; it stays on the builtin probe runner with writes and shell denied."
		firstTool = "query_peer"
	}

	item := guideItem(profile)
	out := Recommendation{
		Intent:           raw,
		Profile:          profile,
		Why:              why,
		FirstTool:        firstTool,
		SaferAlternative: item.SaferAlternative,
		RequiresConsent:  item.ConsentRequired,
		RequiresTTL:      item.RequireLease,
		RecommendedTTL:   item.RecommendedTTL,
		ProfileGuide:     item,
	}
	if item.ConsentRequired {
		out.AcknowledgementHint = "Before enabling this profile, acknowledge profile, writes, shell, network, TTL, and allowed cwd scope."
	}
	return out
}

func CanonicalProfile(name string) string {
	switch strings.TrimSpace(name) {
	case LegacyReadonly:
		return ReadonlyAgent
	case LegacyDev:
		return RepoDev
	case "interactive":
		return DesktopControl
	case "network":
		return QueryNetwork
	case "git_push":
		return RepoDev
	default:
		return strings.TrimSpace(name)
	}
}

func ProfilesRequiringAcknowledgement(profiles []string) []GuideItem {
	var out []GuideItem
	seen := map[string]struct{}{}
	for _, name := range profiles {
		item, ok := GuideItemByName(name)
		if !ok || !item.ConsentRequired {
			continue
		}
		if _, exists := seen[item.Name]; exists {
			continue
		}
		seen[item.Name] = struct{}{}
		out = append(out, item)
	}
	return out
}

func AcknowledgementProblems(profiles []string, acknowledgements []ProfileAcknowledgement) []string {
	required := ProfilesRequiringAcknowledgement(profiles)
	if len(required) == 0 {
		return nil
	}
	byProfile := map[string]ProfileAcknowledgement{}
	for _, ack := range acknowledgements {
		ack.Profile = CanonicalProfile(ack.Profile)
		byProfile[ack.Profile] = ack
	}
	var problems []string
	for _, item := range required {
		ack, ok := byProfile[item.Name]
		if !ok {
			problems = append(problems, "missing acknowledgement for "+item.Name)
			continue
		}
		if ack.Writes != item.Writes {
			problems = append(problems, item.Name+" acknowledgement writes must be "+item.Writes)
		}
		if ack.Shell != item.Shell {
			problems = append(problems, item.Name+" acknowledgement shell must be "+item.Shell)
		}
		if ack.Network != item.Network {
			problems = append(problems, item.Name+" acknowledgement network must match profile")
		}
		if item.RequireLease && strings.TrimSpace(ack.TTL) == "" {
			problems = append(problems, item.Name+" acknowledgement must include ttl")
		}
		if strings.TrimSpace(ack.AllowedCWDScope) == "" {
			problems = append(problems, item.Name+" acknowledgement must include allowed_cwd_scope")
		}
	}
	return problems
}

func guideItem(name string) GuideItem {
	switch name {
	case QueryNetwork:
		return GuideItem{
			Name:                  QueryNetwork,
			DisplayName:           "Query with bounded network checks",
			Description:           "Deterministic read-only probes plus explicit network_check.",
			BestFor:               []string{"TCP/HTTP reachability", "port checks", "network facts that do not need shell"},
			AvoidWhen:             []string{"you need arbitrary shell commands", "you need file reads beyond probe output"},
			EnforcementStrength:   StrengthNative,
			Powers:                PowerSummary{Reads: "bounded machine/repo/network facts", Writes: "none", Deletes: "none", Shell: "none", Network: "bounded network_check only", ProcessDesktopSystem: "none"},
			Capabilities:          []string{CapDiskInfo, CapSystemInfo, CapMemoryInfo, CapUptime, CapRepoStatus, CapProcessList, CapNetworkCheck, CapPortCheck},
			Writes:                WritesDeny,
			Shell:                 ShellDeny,
			Network:               true,
			RequireExplicit:       true,
			Risk:                  RiskMedium,
			RecommendedFirstTool:  "query_peer",
			SaferAlternative:      Query,
			PlainLanguageContract: "Can run deterministic probes including bounded network checks; cannot run shell, write, delete, or invoke an arbitrary agent.",
		}
	case QueryShell:
		return GuideItem{
			Name:                  QueryShell,
			DisplayName:           "Allowlisted read diagnostics",
			Description:           "Read-oriented shell diagnostics with a strict command allowlist.",
			BestFor:               []string{"service status", "process list", "event/log summaries"},
			AvoidWhen:             []string{"a builtin query can answer it", "the command is not explicitly allowlisted"},
			EnforcementStrength:   StrengthAllowlisted,
			Powers:                PowerSummary{Reads: "allowlisted diagnostic command output", Writes: "none", Deletes: "none", Shell: "allowlisted read-only argv only", Network: "none by default", ProcessDesktopSystem: "read-only diagnostics only"},
			Capabilities:          []string{CapShellReadonly, CapServiceStatus, CapProcessList, CapLogTailBounded, CapLogTail},
			Writes:                WritesDeny,
			Shell:                 ShellAllowlisted,
			RequireExplicit:       true,
			Risk:                  RiskMedium,
			RecommendedFirstTool:  "query_peer",
			SaferAlternative:      Query,
			PlainLanguageContract: "Can run only allowlisted read diagnostics; write/delete commands and shell metacharacters are denied.",
		}
	case ReadonlyAgent:
		return GuideItem{
			Name:                  ReadonlyAgent,
			DisplayName:           "Readonly agent",
			Description:           "Agent-assisted open-ended read-only inspection. Reads are allowed; writes, deletes, and shell are denied by default.",
			BestFor:               []string{"battery and uptime questions", "installed app inventory", "read/open files", "inspect source", "summarize code"},
			AvoidWhen:             []string{"deterministic query output is enough", "you need shell diagnostics, edits, builds, or tests"},
			EnforcementStrength:   StrengthHarnessDependent,
			Powers:                PowerSummary{Reads: "system facts and files permitted by runner and OS account", Writes: "denied", Deletes: "denied", Shell: "denied", Network: "runner-dependent, not granted by cmesh profile", ProcessDesktopSystem: "read-only inspection only"},
			Capabilities:          []string{CapReadFiles},
			Writes:                WritesDeny,
			Shell:                 ShellDeny,
			Risk:                  RiskMedium,
			SaferAlternative:      Query,
			RecommendedFirstTool:  "inspect_peer",
			KnownGaps:             []string{"open-ended file read boundaries depend on the selected harness honoring its sandbox"},
			PlainLanguageContract: "Can answer open-ended read-only system and file questions through an agent; cannot run shell, write, or delete files. Prefer query_peer for deterministic no-agent probes.",
		}
	case RepoDev:
		return GuideItem{
			Name:                  RepoDev,
			DisplayName:           "Repo development",
			Description:           "Agent-assisted repo-local edits and test/build commands inside allowed cwd.",
			BestFor:               []string{"edit code", "run tests", "build", "prepare branch handoff"},
			AvoidWhen:             []string{"you only need facts or file reads", "the cwd scope is broad or unclear"},
			EnforcementStrength:   StrengthHarnessDependent,
			Powers:                PowerSummary{Reads: "repo files in allowed cwd", Writes: "repo-scoped writes", Deletes: "repo-scoped deletes possible", Shell: "allowed for repo work", Network: "not granted by default", ProcessDesktopSystem: "none"},
			Capabilities:          []string{CapReadFiles, CapEditRepo, CapExecuteInRepo},
			Writes:                WritesRepoOnly,
			Shell:                 ShellAllow,
			RequireExplicit:       true,
			RequireLease:          true,
			ConsentRequired:       true,
			Risk:                  RiskHigh,
			RecommendedTTL:        "30m",
			SaferAlternative:      ReadonlyAgent,
			KnownGaps:             []string{"repo-dev authority depends on runner and OS account permissions"},
			PlainLanguageContract: "Can edit/delete within the allowed repo scope and run repo commands; requires explicit consent, cwd scope, and a time-limited lease.",
		}
	case DesktopControl:
		return GuideItem{
			Name:                  DesktopControl,
			DisplayName:           "Desktop control",
			Description:           "High-risk desktop/session control.",
			BestFor:               []string{"interact with a GUI session", "browser or desktop automation"},
			AvoidWhen:             []string{"CLI/query/readonly can answer it", "the session contains sensitive apps"},
			EnforcementStrength:   StrengthHighRisk,
			Powers:                PowerSummary{Reads: "screen/session state", Writes: "allowed through desktop actions", Deletes: "possible through desktop actions", Shell: "allowed", Network: "allowed", ProcessDesktopSystem: "desktop interaction allowed"},
			Capabilities:          []string{CapReadFiles, CapDesktopControl, CapDesktopInteraction},
			Writes:                WritesAllow,
			Shell:                 ShellAllow,
			Network:               true,
			RequireExplicit:       true,
			RequireLease:          true,
			ConsentRequired:       true,
			Risk:                  RiskHigh,
			RecommendedTTL:        "15m",
			SaferAlternative:      RepoDev,
			KnownGaps:             []string{"desktop control depends on OS session permissions and runner controls"},
			PlainLanguageContract: "Can control a desktop session; use only with explicit consent, short TTL, and a narrow scope.",
		}
	case SystemChange:
		return GuideItem{
			Name:                  SystemChange,
			DisplayName:           "System change",
			Description:           "High-risk system/service/package changes.",
			BestFor:               []string{"install/start services", "change packages", "admin-level machine changes"},
			AvoidWhen:             []string{"a diagnostic query is enough", "the requested change is not reversible"},
			EnforcementStrength:   StrengthHighRisk,
			Powers:                PowerSummary{Reads: "system state", Writes: "system writes allowed", Deletes: "system deletes possible", Shell: "allowed", Network: "allowed", ProcessDesktopSystem: "process/service/system changes allowed"},
			Capabilities:          []string{CapReadFiles, CapSystemChange, CapLaunchProcesses},
			Writes:                WritesAllow,
			Shell:                 ShellAllow,
			Network:               true,
			RequireExplicit:       true,
			RequireLease:          true,
			ConsentRequired:       true,
			Risk:                  RiskHigh,
			RecommendedTTL:        "15m",
			SaferAlternative:      QueryShell,
			KnownGaps:             []string{"system changes depend on OS account permissions and may need human approval"},
			PlainLanguageContract: "Can change services/packages/system state; requires explicit consent, short TTL, and a narrow scope.",
		}
	default:
		return GuideItem{
			Name:                  Query,
			DisplayName:           "Query only",
			Description:           "Deterministic read-only machine queries. No shell, no writes, no arbitrary agent.",
			BestFor:               []string{"disk info", "system info", "repo status", "safe first look at a machine"},
			AvoidWhen:             []string{"you need open-ended system or file inspection", "you need edits, builds, tests, or shell commands"},
			EnforcementStrength:   StrengthNative,
			Powers:                PowerSummary{Reads: "bounded machine/repo facts", Writes: "none", Deletes: "none", Shell: "none", Network: "none", ProcessDesktopSystem: "none"},
			Capabilities:          []string{CapDiskInfo, CapSystemInfo, CapMemoryInfo, CapUptime, CapRepoStatus, CapProcessList},
			Writes:                WritesDeny,
			Shell:                 ShellDeny,
			Risk:                  RiskLow,
			RecommendedFirstTool:  "query_peer",
			PlainLanguageContract: "Can run deterministic builtin probes; cannot run shell, write, delete, or invoke an arbitrary agent.",
		}
	}
}

func anyContains(text string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(text, needle) {
			return true
		}
	}
	return false
}
