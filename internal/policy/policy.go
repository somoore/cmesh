package policy

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"cmesh/internal/config"
	profiledefs "cmesh/internal/profile"
)

type Decision struct {
	Allowed          bool     `json:"allowed"`
	Code             string   `json:"code,omitempty"`
	Detail           string   `json:"detail,omitempty"`
	RequestedProfile string   `json:"requested_profile,omitempty"`
	EffectiveProfile string   `json:"effective_profile,omitempty"`
	Claims           []string `json:"claims,omitempty"`
	Capabilities     []string `json:"capabilities,omitempty"`
	Intents          []string `json:"intents,omitempty"`
	Writes           string   `json:"writes,omitempty"`
	Shell            string   `json:"shell,omitempty"`
	Network          bool     `json:"network"`
	AuditLevel       string   `json:"audit_level,omitempty"`
	NormalizedCWD    string   `json:"normalized_cwd,omitempty"`
}

type TaskIntent struct {
	PeerName         string
	PeerNodeID       string
	PeerStableID     string
	PeerTags         []string
	CWD              string
	Runner           string
	Profile          string
	HopCount         int
	Chain            []string
	OriginNodeID     string
	AllowDirty       bool
	Subdelegation    bool
	TaskKind         string
	Capability       string
	ProfileContextID string
}

type Engine struct {
	cfg            *config.Config
	leaseValidator LeaseValidator
}

func New(cfg *config.Config) *Engine {
	return &Engine{cfg: cfg}
}

func NewWithLeaseValidator(cfg *config.Config, validator LeaseValidator) *Engine {
	return &Engine{cfg: cfg, leaseValidator: validator}
}

func (e *Engine) CheckInbound(intent TaskIntent) Decision {
	if !matchesAny(peerIdentityCandidates(intent), e.cfg.Policy.AllowedPeers) {
		return denied("peer_unauthorized", "origin peer is not allowed by inbound policy", intent.Profile, "")
	}
	if intent.HopCount > e.cfg.Policy.HopLimit {
		return denied("profile_denied", fmt.Sprintf("hop count %d exceeds limit %d", intent.HopCount, e.cfg.Policy.HopLimit), intent.Profile, "")
	}
	if contains(intent.Chain, e.cfg.Node.ID) || contains(intent.Chain, e.cfg.Node.Name) {
		return denied("profile_denied", "delegation chain cycles back to this node", intent.Profile, "")
	}
	cwd, err := NormalizeCWD(intent.CWD)
	if err != nil {
		return denied("cwd_denied", err.Error(), intent.Profile, "")
	}
	if !pathAllowed(cwd, e.cfg.Policy.AllowedCWDs) {
		return denied("cwd_denied", fmt.Sprintf("cwd %q is outside allowed_cwds", cwd), intent.Profile, "")
	}
	intent.CWD = cwd
	return e.checkProfile(intent, true)
}

func (e *Engine) CheckOutbound(intent TaskIntent) Decision {
	out := e.cfg.Policy.Outbound
	if len(out.AllowedPeers) > 0 && !matchesAny(identityCandidates(intent), out.AllowedPeers) {
		return denied("peer_unauthorized", "peer is not allowed by outbound policy", intent.Profile, "")
	}
	if matchesAny(identityCandidates(intent), out.DeniedPeers) {
		return denied("peer_unauthorized", "peer is denied by outbound policy", intent.Profile, "")
	}
	if contains(out.DeniedProfiles, intent.Profile) {
		return denied("profile_denied", "profile is denied by outbound policy", intent.Profile, "")
	}
	if intent.Subdelegation && !out.AllowSubdelegation {
		return denied("profile_denied", "subdelegation is disabled by outbound policy", intent.Profile, "")
	}
	if intent.HopCount > e.cfg.Policy.HopLimit {
		return denied("profile_denied", fmt.Sprintf("hop count %d exceeds limit %d", intent.HopCount, e.cfg.Policy.HopLimit), intent.Profile, "")
	}
	return e.checkProfile(intent, false)
}

func (e *Engine) checkProfile(intent TaskIntent, inbound bool) Decision {
	requested := intent.Profile
	explicit := strings.TrimSpace(requested) != ""
	if requested == "" {
		if intent.Runner != "" {
			for name, profile := range e.cfg.Profiles {
				if profile.Runner == intent.Runner && e.cfg.Runners[profile.Runner].DefaultProfile == name {
					requested = name
					break
				}
			}
		}
		if requested == "" {
			requested = "dev"
		}
	}
	profile, ok := e.cfg.Profiles[requested]
	if !ok {
		return denied("profile_unavailable", fmt.Sprintf("profile %q is not configured", requested), requested, "")
	}
	if intent.Runner != "" && profile.Runner != intent.Runner {
		return denied("runner_unavailable", fmt.Sprintf("profile %q uses runner %q, not %q", requested, profile.Runner, intent.Runner), requested, "")
	}
	if _, ok := e.cfg.Runners[profile.Runner]; !ok {
		return denied("runner_unavailable", fmt.Sprintf("runner %q is not configured", profile.Runner), requested, "")
	}
	effective := requested
	if ceiling, ok := e.cfg.Policy.ProfileCeilings[requested]; ok {
		if _, ok := e.cfg.Profiles[ceiling]; !ok {
			return denied("profile_denied", fmt.Sprintf("profile ceiling %q is not configured", ceiling), requested, "")
		}
		effective = ceiling
		profile = e.cfg.Profiles[effective]
	}
	if len(profile.AllowedPeers) > 0 && !matchesAny(identityCandidates(intent), profile.AllowedPeers) {
		return denied("profile_denied", fmt.Sprintf("profile %q is not allowed for this peer", effective), requested, effective)
	}
	if profile.RequireExplicit && !explicit {
		return denied("profile_denied", fmt.Sprintf("profile %q requires an explicit profile request", effective), requested, effective)
	}
	if profile.RequireLease {
		if strings.TrimSpace(intent.ProfileContextID) == "" {
			return denied("profile_denied", fmt.Sprintf("profile %q requires an active lease", effective), requested, effective)
		}
		if inbound {
			if e.leaseValidator == nil {
				return denied("profile_denied", fmt.Sprintf("profile %q requires a worker-issued lease", effective), requested, effective)
			}
			validated, err := e.leaseValidator.ValidateProfileLease(LeaseValidation{
				LeaseID:      intent.ProfileContextID,
				PeerName:     intent.PeerName,
				PeerNodeID:   intent.PeerNodeID,
				PeerStableID: intent.PeerStableID,
				Profile:      effective,
				CWD:          intent.CWD,
				Capability:   intent.Capability,
				Capabilities: profiledefs.NormalizeCapabilities(profile.Capabilities, profile.Claims),
			})
			if err != nil {
				return denied("profile_denied", err.Error(), requested, effective)
			}
			if strings.TrimSpace(validated.EffectiveCWD) != "" {
				intent.CWD = validated.EffectiveCWD
			}
		}
	}
	capabilities := profiledefs.NormalizeCapabilities(profile.Capabilities, profile.Claims)
	if strings.TrimSpace(intent.Capability) != "" && !contains(capabilities, intent.Capability) {
		return denied("profile_denied", fmt.Sprintf("profile %q does not grant capability %q", effective, intent.Capability), requested, effective)
	}
	if intent.TaskKind == "query" {
		if profile.Writes != profiledefs.WritesDeny {
			return denied("profile_denied", fmt.Sprintf("query task requires writes=deny on profile %q", effective), requested, effective)
		}
		switch profile.Runner {
		case profiledefs.RunnerProbe:
			if profile.Shell != profiledefs.ShellDeny {
				return denied("profile_denied", fmt.Sprintf("probe query task requires shell=deny on profile %q", effective), requested, effective)
			}
		case profiledefs.RunnerShell:
			if profile.Shell != profiledefs.ShellAllowlisted {
				return denied("profile_denied", fmt.Sprintf("shell query task requires shell=allowlisted on profile %q", effective), requested, effective)
			}
			if !contains(capabilities, profiledefs.CapShellReadonly) {
				return denied("profile_denied", fmt.Sprintf("shell query profile %q must grant shell_readonly", effective), requested, effective)
			}
		default:
			return denied("profile_denied", fmt.Sprintf("query task requires probe or shell runner, got %q", profile.Runner), requested, effective)
		}
	}
	return Decision{
		Allowed:          true,
		RequestedProfile: requested,
		EffectiveProfile: effective,
		Claims:           append([]string(nil), capabilities...),
		Capabilities:     append([]string(nil), capabilities...),
		Intents:          append([]string(nil), profile.Intents...),
		Writes:           profile.Writes,
		Shell:            profile.Shell,
		Network:          profile.Network,
		AuditLevel:       profile.AuditLevel,
		NormalizedCWD:    intent.CWD,
	}
}

func NormalizeCWD(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("cwd is required")
	}
	expanded, err := config.ExpandPath(path)
	if err != nil {
		return "", err
	}
	abs, err := filepath.Abs(expanded)
	if err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	real = filepath.Clean(real)
	if runtime.GOOS == "windows" {
		real = strings.ToLower(real)
	}
	return real, nil
}

func pathAllowed(cwd string, patterns []string) bool {
	for _, pattern := range patterns {
		expanded, err := config.ExpandPath(pattern)
		if err != nil {
			continue
		}
		abs, err := filepath.Abs(expanded)
		if err != nil {
			continue
		}
		abs = filepath.Clean(abs)
		if runtime.GOOS == "windows" {
			abs = strings.ToLower(abs)
		}
		if strings.HasSuffix(abs, string(filepath.Separator)+"**") {
			root, err := normalizeAllowedRoot(strings.TrimSuffix(abs, string(filepath.Separator)+"**"))
			if err != nil {
				continue
			}
			if cwd == root || strings.HasPrefix(cwd, root+string(filepath.Separator)) {
				return true
			}
			continue
		}
		ok, err := filepath.Match(abs, cwd)
		if err == nil && ok {
			return true
		}
		if cwd == abs {
			return true
		}
	}
	return false
}

func normalizeAllowedRoot(path string) (string, error) {
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	real = filepath.Clean(real)
	if runtime.GOOS == "windows" {
		real = strings.ToLower(real)
	}
	return real, nil
}

// SmokeCWD returns a concrete, existing directory that this node's own inbound
// policy accepts, for use as the default cwd of a smoke task. The smoke runs a
// real runner round-trip in the worker's cwd, so that cwd must pass CheckInbound
// (NormalizeCWD requires it to exist; pathAllowed requires it to match
// allowed_cwds). A hardcoded "~" can never satisfy a conservatively-scoped
// allowlist (e.g. ["~/code/*"]), which would make a smoke — and the onboarding
// "ready" gate that depends on it — impossible to pass without loosening policy.
//
// It walks allowed_cwds in order and returns the first pattern that resolves to
// an existing directory the policy allows: the root for a recursive "/**" glob,
// an existing child for a single-segment "/*" glob, or the literal path itself.
// It returns "" when nothing resolves, so callers fall back to their prior
// default ("~") and the smoke fails loudly rather than silently mis-targeting.
func SmokeCWD(cfg *config.Config) string {
	if cfg == nil {
		return ""
	}
	for _, pattern := range cfg.Policy.AllowedCWDs {
		if cwd := smokeCWDForPattern(pattern, cfg.Policy.AllowedCWDs); cwd != "" {
			return cwd
		}
	}
	return ""
}

func smokeCWDForPattern(pattern string, patterns []string) string {
	expanded, err := config.ExpandPath(pattern)
	if err != nil {
		return ""
	}
	// Recursive "/**" glob: the root dir itself is allowed and is concrete.
	if root := strings.TrimSuffix(expanded, string(filepath.Separator)+"**"); root != expanded {
		return existingAllowedDir(root, patterns)
	}
	// Single-segment "/*" glob matches direct children only (not the parent), so
	// descend to the first existing subdirectory.
	if parent := strings.TrimSuffix(expanded, string(filepath.Separator)+"*"); parent != expanded {
		entries, err := os.ReadDir(parent)
		if err != nil {
			return ""
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			if cwd := existingAllowedDir(filepath.Join(parent, entry.Name()), patterns); cwd != "" {
				return cwd
			}
		}
		return ""
	}
	// Literal path with no glob.
	return existingAllowedDir(expanded, patterns)
}

// existingAllowedDir returns candidate normalized when it is an existing
// directory that pathAllowed accepts, else "".
func existingAllowedDir(candidate string, patterns []string) string {
	info, err := os.Stat(candidate)
	if err != nil || !info.IsDir() {
		return ""
	}
	normalized, err := NormalizeCWD(candidate)
	if err != nil {
		return ""
	}
	if !pathAllowed(normalized, patterns) {
		return ""
	}
	return candidate
}

func peerIdentityCandidates(intent TaskIntent) []string {
	values := []string{intent.PeerName, intent.PeerNodeID, intent.PeerStableID}
	values = append(values, intent.PeerTags...)
	out := values[:0]
	for _, v := range values {
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}

func identityCandidates(intent TaskIntent) []string {
	values := []string{intent.PeerName, intent.PeerNodeID, intent.PeerStableID, intent.OriginNodeID}
	values = append(values, intent.PeerTags...)
	out := values[:0]
	for _, v := range values {
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}

func matchesAny(values, patterns []string) bool {
	for _, value := range values {
		if contains(patterns, value) {
			return true
		}
		for _, pattern := range patterns {
			ok, err := filepath.Match(pattern, value)
			if err == nil && ok {
				return true
			}
		}
	}
	return false
}

func contains(values []string, needle string) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
}

func denied(code, detail, requested, effective string) Decision {
	return Decision{
		Allowed:          false,
		Code:             code,
		Detail:           detail,
		RequestedProfile: requested,
		EffectiveProfile: effective,
	}
}
