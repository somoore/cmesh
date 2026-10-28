package profile

import "fmt"

// Authority is the part of a profile that grants power: what it may do, how far
// it may write, run commands and reach the network, and its overall risk. It is
// what a profile ceiling compares.
type Authority struct {
	Capabilities []string
	Writes       string
	Shell        string
	Network      bool
	GitPush      bool
	Risk         string
}

// CeilingExcess reports how a ceiling profile would grant authority the
// requested profile does not, or "" when the ceiling stays within it.
//
// A ceiling must be a narrowing on every axis, not only on the coarse risk
// rank: two medium-risk profiles can grant disjoint powers (readonly-agent reads
// files, query-shell runs allowlisted commands), and two high-risk profiles can
// separate repo edits from system changes. So the ceiling must not exceed the
// requested profile's writes, shell, network, git push or risk, and every
// capability it grants must be one the requested profile already holds,
// either listed outright or implied by an authority axis the requested profile
// already has at that level (see capabilityImplied). Unrecognized writes or
// shell values fail closed.
func CeilingExcess(ceiling, requested Authority) string {
	cw, ok := writesRank(ceiling.Writes)
	if !ok {
		return fmt.Sprintf("ceiling has unrecognized writes=%q", ceiling.Writes)
	}
	rw, ok := writesRank(requested.Writes)
	if !ok {
		return fmt.Sprintf("requested profile has unrecognized writes=%q", requested.Writes)
	}
	if cw > rw {
		return fmt.Sprintf("writes=%s exceeds writes=%s", ceiling.Writes, requested.Writes)
	}
	cs, ok := shellRank(ceiling.Shell)
	if !ok {
		return fmt.Sprintf("ceiling has unrecognized shell=%q", ceiling.Shell)
	}
	rs, ok := shellRank(requested.Shell)
	if !ok {
		return fmt.Sprintf("requested profile has unrecognized shell=%q", requested.Shell)
	}
	if cs > rs {
		return fmt.Sprintf("shell=%s exceeds shell=%s", ceiling.Shell, requested.Shell)
	}
	if ceiling.Network && !requested.Network {
		return "network=true exceeds network=false"
	}
	if ceiling.GitPush && !requested.GitPush {
		return "git_push=true exceeds git_push=false"
	}
	if RiskRank(ceiling.Risk) > RiskRank(requested.Risk) {
		return fmt.Sprintf("risk=%s exceeds risk=%s", ceiling.Risk, requested.Risk)
	}
	for _, capability := range ceiling.Capabilities {
		if contains(requested.Capabilities, capability) || capabilityImplied(capability, requested) {
			continue
		}
		return fmt.Sprintf("capability %q is not granted by the requested profile", capability)
	}
	return ""
}

// capabilityImplied reports whether a capability is already covered by one of
// the requested profile's authority axes. Runners enforce writes, shell and
// network directly, so a profile with writes=allow can already edit a repo even
// if it does not list edit_repo; a ceiling that names edit_repo is then not
// adding anything. This keeps the documented system-change -> repo-dev cap
// legal while still rejecting a ceiling that reaches for a disjoint power.
func capabilityImplied(capability string, requested Authority) bool {
	switch capability {
	case CapEditRepo:
		rank, ok := writesRank(requested.Writes)
		return ok && rank >= 1
	case CapExecuteInRepo, CapExecuteRepoCommands:
		return requested.Shell == ShellAllow
	case CapShellReadonly:
		rank, ok := shellRank(requested.Shell)
		return ok && rank >= 1
	case CapNetworkCheck, CapPortCheck, CapNetworkLegacy:
		return requested.Network
	case CapGitPush:
		return requested.GitPush
	case CapLogTailBounded:
		return contains(requested.Capabilities, CapLogTail)
	}
	return false
}

func writesRank(writes string) (int, bool) {
	switch writes {
	case WritesDeny:
		return 0, true
	case WritesRepoOnly:
		return 1, true
	case WritesAllow:
		return 2, true
	}
	return 0, false
}

func shellRank(shell string) (int, bool) {
	switch shell {
	case ShellDeny:
		return 0, true
	case ShellAllowlisted:
		return 1, true
	case ShellAllow:
		return 2, true
	}
	return 0, false
}
