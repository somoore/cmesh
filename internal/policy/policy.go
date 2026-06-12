package policy

import (
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"

	"cmesh/internal/config"
)

type Decision struct {
	Allowed          bool     `json:"allowed"`
	Code             string   `json:"code,omitempty"`
	Detail           string   `json:"detail,omitempty"`
	RequestedProfile string   `json:"requested_profile,omitempty"`
	EffectiveProfile string   `json:"effective_profile,omitempty"`
	Claims           []string `json:"claims,omitempty"`
}

type TaskIntent struct {
	PeerName      string
	PeerNodeID    string
	PeerStableID  string
	PeerTags      []string
	CWD           string
	Runner        string
	Profile       string
	HopCount      int
	Chain         []string
	OriginNodeID  string
	AllowDirty    bool
	Subdelegation bool
}

type Engine struct {
	cfg *config.Config
}

func New(cfg *config.Config) *Engine {
	return &Engine{cfg: cfg}
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
	return e.checkProfile(intent)
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
	return e.checkProfile(intent)
}

func (e *Engine) checkProfile(intent TaskIntent) Decision {
	requested := intent.Profile
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
	return Decision{
		Allowed:          true,
		RequestedProfile: requested,
		EffectiveProfile: effective,
		Claims:           append([]string(nil), profile.Claims...),
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
