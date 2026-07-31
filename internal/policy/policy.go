package policy

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"cmesh/internal/config"
	profiledefs "cmesh/internal/profile"
)

type Decision struct {
	Allowed          bool   `json:"allowed"`
	Code             string `json:"code,omitempty"`
	Detail           string `json:"detail,omitempty"`
	RequestedProfile string `json:"requested_profile,omitempty"`
	EffectiveProfile string `json:"effective_profile,omitempty"`
	// EffectiveRunner is the runner belonging to the effective profile. Callers
	// must prefer it over resolving a runner from the requested profile name: a
	// profile ceiling can remap the effective profile, and deriving the runner
	// from the requested name would run a different runner than the one whose
	// profile policy actually authorized.
	EffectiveRunner string   `json:"effective_runner,omitempty"`
	Claims          []string `json:"claims,omitempty"`
	Capabilities    []string `json:"capabilities,omitempty"`
	Intents         []string `json:"intents,omitempty"`
	Writes          string   `json:"writes,omitempty"`
	Shell           string   `json:"shell,omitempty"`
	Network         bool     `json:"network"`
	AuditLevel      string   `json:"audit_level,omitempty"`
	NormalizedCWD   string   `json:"normalized_cwd,omitempty"`
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

// Engine evaluates inbound and outbound policy. Its config may be swapped at
// runtime via Reload so that a repair which rewrites peer trust takes effect on
// every surface sharing the engine — the mesh RPC server enforces inbound peer
// policy, so a reload that only reached the local gateway would report success
// while the daemon kept rejecting the peer.
type Engine struct {
	mu             sync.RWMutex
	cfg            *config.Config
	leaseValidator LeaseValidator
}

func New(cfg *config.Config) *Engine {
	return &Engine{cfg: cfg}
}

func NewWithLeaseValidator(cfg *config.Config, validator LeaseValidator) *Engine {
	return &Engine{cfg: cfg, leaseValidator: validator}
}

// Reload swaps the engine's config in place. Callers holding this *Engine —
// including a mesh RPC server started before the reload — evaluate subsequent
// requests against the new config.
func (e *Engine) Reload(cfg *config.Config) {
	if cfg == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.cfg = cfg
}

// config returns the current config under a read lock.
func (e *Engine) config() *config.Config {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.cfg
}

func (e *Engine) CheckInbound(intent TaskIntent) Decision {
	cfg := e.config()
	if !matchesAny(peerIdentityCandidates(intent), cfg.Policy.AllowedPeers) {
		return denied("peer_unauthorized", "origin peer is not allowed by inbound policy", intent.Profile, "")
	}
	if intent.HopCount > cfg.Policy.HopLimit {
		return denied("profile_denied", fmt.Sprintf("hop count %d exceeds limit %d", intent.HopCount, cfg.Policy.HopLimit), intent.Profile, "")
	}
	// Case-insensitive to match validateDelegationChain's duplicate-hop check on
	// the mesh RPC path; a differently-cased node name must not slip past one
	// layer only to be caught by the other.
	if containsFold(intent.Chain, cfg.Node.ID) || containsFold(intent.Chain, cfg.Node.Name) {
		return denied("profile_denied", "delegation chain cycles back to this node", intent.Profile, "")
	}
	cwd, err := NormalizeCWD(intent.CWD)
	if err != nil {
		return denied("cwd_denied", err.Error(), intent.Profile, "")
	}
	if !pathAllowed(cwd, cfg.Policy.AllowedCWDs) {
		return denied("cwd_denied", fmt.Sprintf("cwd %q is outside allowed_cwds", cwd), intent.Profile, "")
	}
	intent.CWD = cwd
	return e.checkProfile(intent, true)
}

func (e *Engine) CheckOutbound(intent TaskIntent) Decision {
	cfg := e.config()
	out := cfg.Policy.Outbound
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
	if intent.HopCount > cfg.Policy.HopLimit {
		return denied("profile_denied", fmt.Sprintf("hop count %d exceeds limit %d", intent.HopCount, cfg.Policy.HopLimit), intent.Profile, "")
	}
	return e.checkProfile(intent, false)
}

func (e *Engine) checkProfile(intent TaskIntent, inbound bool) Decision {
	cfg := e.config()
	requested := intent.Profile
	explicit := strings.TrimSpace(requested) != ""
	if requested == "" {
		if intent.Runner != "" {
			for name, profile := range cfg.Profiles {
				if profile.Runner == intent.Runner && cfg.Runners[profile.Runner].DefaultProfile == name {
					requested = name
					break
				}
			}
		}
		if requested == "" {
			// Fall back to the least-authority profile. This previously named the
			// legacy "dev" profile, which is high-risk under the current ladder and
			// is the opposite of the safe default every other surface uses.
			requested = profiledefs.Query
		}
	}
	profile, ok := cfg.Profiles[requested]
	if !ok {
		return denied("profile_unavailable", fmt.Sprintf("profile %q is not configured", requested), requested, "")
	}
	if intent.Runner != "" && profile.Runner != intent.Runner {
		return denied("runner_unavailable", fmt.Sprintf("profile %q uses runner %q, not %q", requested, profile.Runner, intent.Runner), requested, "")
	}
	if _, ok := cfg.Runners[profile.Runner]; !ok {
		return denied("runner_unavailable", fmt.Sprintf("runner %q is not configured", profile.Runner), requested, "")
	}
	effective := requested
	if ceiling, ok := cfg.Policy.ProfileCeilings[requested]; ok {
		ceilingProfile, ok := cfg.Profiles[ceiling]
		if !ok {
			return denied("profile_denied", fmt.Sprintf("profile ceiling %q is not configured", ceiling), requested, "")
		}
		// A ceiling may only cap authority, never raise it. config.Validate rejects
		// an escalating mapping at load time; this is the request-time backstop for
		// a config that changed underneath a running daemon or was built in-process.
		if profiledefs.RiskRank(effectiveRisk(ceiling, ceilingProfile)) > profiledefs.RiskRank(effectiveRisk(requested, profile)) {
			return denied("profile_denied", fmt.Sprintf("profile ceiling %q raises authority above requested profile %q", ceiling, requested), requested, "")
		}
		// The runner checks above ran against the requested profile. Re-run them for
		// the ceiling so the profile that is actually enforced cannot name a runner
		// the caller did not ask for or that this node does not configure.
		if intent.Runner != "" && ceilingProfile.Runner != intent.Runner {
			return denied("runner_unavailable", fmt.Sprintf("profile ceiling %q uses runner %q, not %q", ceiling, ceilingProfile.Runner, intent.Runner), requested, ceiling)
		}
		if _, ok := cfg.Runners[ceilingProfile.Runner]; !ok {
			return denied("runner_unavailable", fmt.Sprintf("runner %q is not configured", ceilingProfile.Runner), requested, ceiling)
		}
		effective = ceiling
		profile = ceilingProfile
	}
	// Profile-level allowed_peers is an authorization gate, so it must key on the
	// authenticated peer identity only. Never include OriginNodeID here: on the
	// inbound path it comes from the request body (req.Provenance.OriginNodeID),
	// so matching on it would let an allowed-but-unprivileged peer claim a
	// privileged origin and use a profile scoped to that origin. OriginNodeID is
	// for audit/provenance display, not authorization.
	if len(profile.AllowedPeers) > 0 && !matchesAny(peerIdentityCandidates(intent), profile.AllowedPeers) {
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
		EffectiveRunner:  profile.Runner,
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
	if pathComparisonIsCaseInsensitive(runtime.GOOS) {
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
		if pathComparisonIsCaseInsensitive(runtime.GOOS) {
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
	if pathComparisonIsCaseInsensitive(runtime.GOOS) {
		real = strings.ToLower(real)
	}
	return real, nil
}

func pathComparisonIsCaseInsensitive(goos string) bool {
	return goos == "windows" || goos == "darwin"
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

// effectiveRisk returns a profile's declared risk, falling back to the
// capability-derived value when the config omits it.
func effectiveRisk(name string, prof config.ProfileConfig) string {
	if prof.Risk != "" {
		return prof.Risk
	}
	return profiledefs.RiskForConfig(name, profiledefs.NormalizeCapabilities(prof.Capabilities, prof.Claims), prof.Writes, prof.Shell, prof.Network)
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

// containsFold reports whether values contains needle, ignoring case. An empty
// needle never matches, so an unset node id or name cannot report a cycle.
func containsFold(values []string, needle string) bool {
	if strings.TrimSpace(needle) == "" {
		return false
	}
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), strings.TrimSpace(needle)) {
			return true
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
