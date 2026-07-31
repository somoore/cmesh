package config

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	profiledefs "cmesh/internal/profile"

	"github.com/BurntSushi/toml"
)

const (
	DefaultMeshPort = 7778
	DefaultMCPPort  = 7777
)

type Config struct {
	Node     NodeConfig               `toml:"node"`
	Tailnet  TailnetConfig            `toml:"tailscale"`
	Policy   PolicyConfig             `toml:"policy"`
	Peers    map[string]PeerConfig    `toml:"peers"`
	Profiles map[string]ProfileConfig `toml:"profiles"`
	Runners  map[string]RunnerConfig  `toml:"runners"`
	Store    StoreConfig              `toml:"store"`
	Audit    AuditConfig              `toml:"audit"`
	Gateway  GatewayConfig            `toml:"gateway"`
}

type NodeConfig struct {
	ID   string `toml:"id"`
	Name string `toml:"name"`
	// Role records how this node participates in the mesh: "origin" (only sends
	// work), "worker" (only accepts work), or "both" (default). It is optional;
	// an empty value behaves like "both". Persisting it lets the stateless
	// onboarding wizard re-derive that an origin-only node reaches 'ready'
	// without requiring the reverse (worker-side) smoke it never serves.
	Role     string `toml:"role"`
	MeshPort int    `toml:"mesh_port"`
	MCPPort  int    `toml:"mcp_port"`
	DataDir  string `toml:"data_dir"`
}

type TailnetConfig struct {
	Mode string `toml:"mode"`
	Bind string `toml:"bind"`
}

type PolicyConfig struct {
	AllowedPeers    []string          `toml:"allowed_peers"`
	AllowedCWDs     []string          `toml:"allowed_cwds"`
	HopLimit        int               `toml:"hop_limit"`
	Outbound        OutboundPolicy    `toml:"outbound"`
	ProfileCeilings map[string]string `toml:"profile_ceilings"`
}

type OutboundPolicy struct {
	AllowedPeers       []string `toml:"allowed_peers"`
	DeniedPeers        []string `toml:"denied_peers"`
	DeniedProfiles     []string `toml:"denied_profiles"`
	AllowSubdelegation bool     `toml:"allow_subdelegation"`
}

type ProfileConfig struct {
	Runner                string            `toml:"runner"`
	Description           string            `toml:"description"`
	Intents               []string          `toml:"intents"`
	Capabilities          []string          `toml:"capabilities"`
	Claims                []string          `toml:"claims"`
	AllowedPeers          []string          `toml:"allowed_peers"`
	RequireExplicit       bool              `toml:"require_explicit"`
	RequireLease          bool              `toml:"require_lease"`
	Risk                  string            `toml:"risk"`
	Bare                  bool              `toml:"bare"`
	Writes                string            `toml:"writes"`
	Shell                 string            `toml:"shell"`
	Network               bool              `toml:"network"`
	GitPush               bool              `toml:"git_push"`
	MaxRuntime            Duration          `toml:"max_runtime"`
	MaxOutputBytes        int64             `toml:"max_output_bytes"`
	AuditLevel            string            `toml:"audit_level"`
	EnforcementConfidence string            `toml:"enforcement_confidence"`
	KnownGaps             []string          `toml:"known_gaps"`
	ShellPolicy           ShellPolicyConfig `toml:"shell_policy"`
}

type ShellPolicyConfig struct {
	Mode            string              `toml:"mode"`
	AllowedCommands map[string][]string `toml:"allowed_commands"`
	DeniedPatterns  []string            `toml:"denied_patterns"`
	Windows         ShellOSPolicyConfig `toml:"windows"`
	Darwin          ShellOSPolicyConfig `toml:"darwin"`
	Linux           ShellOSPolicyConfig `toml:"linux"`
}

type ShellOSPolicyConfig struct {
	AllowedCommands []string `toml:"allowed_commands"`
}

type RunnerConfig struct {
	Command               []string `toml:"command"`
	DefaultProfile        string   `toml:"default_profile"`
	ManifestPath          string   `toml:"manifest_path"`
	Concurrency           int      `toml:"concurrency"`
	AllowDangerFullAccess bool     `toml:"allow_danger_full_access"`
}

type PeerConfig struct {
	NodeID  string `toml:"node_id"`
	Name    string `toml:"name"`
	Address string `toml:"address"`
	Port    int    `toml:"port"`
}

type StoreConfig struct {
	Path string `toml:"path"`
}

type AuditConfig struct {
	Path string `toml:"path"`
}

type GatewayConfig struct {
	HostToken string `toml:"host_token"`
}

type Duration struct {
	time.Duration
}

func (d *Duration) UnmarshalText(text []byte) error {
	if len(text) == 0 {
		d.Duration = 0
		return nil
	}
	parsed, err := time.ParseDuration(string(text))
	if err != nil {
		return err
	}
	d.Duration = parsed
	return nil
}

func DefaultPath() string {
	if p := os.Getenv("CMESH_CONFIG"); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "cmesh.toml"
	}
	return filepath.Join(home, ".config", "cmesh", "config.toml")
}

func Load(path string) (*Config, error) {
	if path == "" {
		path = DefaultPath()
	}
	var cfg Config
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		return nil, err
	}
	if err := cfg.ApplyDefaults(filepath.Dir(path)); err != nil {
		return nil, err
	}
	return &cfg, cfg.Validate()
}

func (c *Config) ApplyDefaults(configDir string) error {
	if c.Node.Name == "" {
		host, err := os.Hostname()
		if err != nil {
			return err
		}
		c.Node.Name = host
	}
	if c.Node.ID == "" {
		c.Node.ID = c.Node.Name
	}
	if c.Node.MeshPort == 0 {
		c.Node.MeshPort = DefaultMeshPort
	}
	if c.Node.MCPPort == 0 {
		c.Node.MCPPort = DefaultMCPPort
	}
	if c.Node.DataDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		c.Node.DataDir = filepath.Join(home, ".local", "share", "cmesh")
	}
	var err error
	c.Node.DataDir, err = ExpandPath(c.Node.DataDir)
	if err != nil {
		return err
	}
	if c.Tailnet.Mode == "" {
		c.Tailnet.Mode = "host"
	}
	if c.Tailnet.Bind == "" {
		c.Tailnet.Bind = "tailnet_ip"
	}
	if c.Policy.HopLimit == 0 {
		c.Policy.HopLimit = 2
	}
	if c.Store.Path == "" {
		c.Store.Path = filepath.Join(c.Node.DataDir, "cmesh.db")
	}
	if c.Audit.Path == "" {
		c.Audit.Path = filepath.Join(c.Node.DataDir, "audit.jsonl")
	}
	c.Store.Path, err = ExpandPath(c.Store.Path)
	if err != nil {
		return err
	}
	c.Audit.Path, err = ExpandPath(c.Audit.Path)
	if err != nil {
		return err
	}
	for name, peer := range c.Peers {
		if peer.Name == "" {
			peer.Name = name
		}
		if peer.Port == 0 {
			peer.Port = DefaultMeshPort
		}
		c.Peers[name] = peer
	}
	for name, runner := range c.Runners {
		if runner.DefaultProfile == "" {
			// Default to a profile this config actually defines, or Validate (and
			// later preflight) rejects it. Prefer query, then legacy safe profiles,
			// then the lexically-first defined profile for determinism.
			// Empty only when no profiles exist, which Validate reports separately.
			runner.DefaultProfile = pickDefaultProfile(c.Profiles)
		}
		if runner.ManifestPath != "" && !filepath.IsAbs(runner.ManifestPath) {
			runner.ManifestPath = filepath.Join(configDir, runner.ManifestPath)
		}
		c.Runners[name] = runner
	}
	for name, prof := range c.Profiles {
		prof.Capabilities = profiledefs.NormalizeCapabilities(prof.Capabilities, prof.Claims)
		if len(prof.Claims) == 0 {
			prof.Claims = append([]string(nil), prof.Capabilities...)
		}
		if prof.Writes == "" {
			if containsString(prof.Capabilities, profiledefs.CapEditRepo) || containsString(prof.Capabilities, profiledefs.CapExecuteInRepo) {
				prof.Writes = profiledefs.WritesRepoOnly
			} else {
				prof.Writes = profiledefs.WritesDeny
			}
		}
		if prof.Shell == "" {
			if containsString(prof.Capabilities, profiledefs.CapExecuteInRepo) || containsString(prof.Capabilities, profiledefs.CapExecuteRepoCommands) {
				prof.Shell = profiledefs.ShellAllow
			} else {
				prof.Shell = profiledefs.ShellDeny
			}
		}
		if prof.AuditLevel == "" {
			prof.AuditLevel = "normal"
		}
		if prof.EnforcementConfidence == "" {
			if prof.Runner == profiledefs.RunnerProbe {
				prof.EnforcementConfidence = "high"
			} else {
				prof.EnforcementConfidence = "medium"
			}
		}
		if prof.Risk == "" {
			prof.Risk = profiledefs.RiskForConfig(name, prof.Capabilities, prof.Writes, prof.Shell, prof.Network)
		}
		c.Profiles[name] = prof
	}
	return nil
}

// pickDefaultProfile chooses a runner's default profile from the defined profiles
// when the config omits default_profile. It prefers query, then legacy safe
// defaults, then falls back to the lexically-first defined profile so
// the choice is deterministic regardless of map iteration order. Returns "" only
// when no profiles are defined, which Validate reports as its own error.
func pickDefaultProfile(profiles map[string]ProfileConfig) string {
	for _, preferred := range []string{"query", "readonly", "dev"} {
		if _, ok := profiles[preferred]; ok {
			return preferred
		}
	}
	first := ""
	for name := range profiles {
		if first == "" || name < first {
			first = name
		}
	}
	return first
}

func (c *Config) Validate() error {
	if c.Node.Name == "" {
		return errors.New("node.name is required")
	}
	// Role is optional (empty behaves like "both"); only reject a clearly-invalid
	// non-empty value so existing configs without a role keep loading.
	switch c.Node.Role {
	case "", "origin", "worker", "both":
	default:
		return fmt.Errorf("node.role must be origin, worker, or both, got %q", c.Node.Role)
	}
	if c.Node.MeshPort < 1 || c.Node.MeshPort > 65535 {
		return fmt.Errorf("node.mesh_port out of range: %d", c.Node.MeshPort)
	}
	if c.Node.MCPPort < 1 || c.Node.MCPPort > 65535 {
		return fmt.Errorf("node.mcp_port out of range: %d", c.Node.MCPPort)
	}
	if c.Tailnet.Mode != "host" && c.Tailnet.Mode != "tsnet" {
		return fmt.Errorf("tailscale.mode must be host or tsnet, got %q", c.Tailnet.Mode)
	}
	if c.Tailnet.Bind != "tailnet_ip" && net.ParseIP(c.Tailnet.Bind) == nil {
		return fmt.Errorf("tailscale.bind must be tailnet_ip or an explicit IP, got %q", c.Tailnet.Bind)
	}
	if len(c.Policy.AllowedPeers) == 0 {
		return errors.New("policy.allowed_peers must name at least one peer")
	}
	if len(c.Policy.AllowedCWDs) == 0 {
		return errors.New("policy.allowed_cwds must name at least one path glob")
	}
	if len(c.Profiles) == 0 {
		return errors.New("at least one profile is required")
	}
	if len(c.Runners) == 0 {
		return errors.New("at least one runner is required")
	}
	for name, prof := range c.Profiles {
		if prof.Runner == "" {
			return fmt.Errorf("profiles.%s.runner is required", name)
		}
		if _, ok := c.Runners[prof.Runner]; !ok {
			return fmt.Errorf("profiles.%s references unknown runner %q", name, prof.Runner)
		}
		// Validate the normalized capability set, not the raw Capabilities slice:
		// a profile using only the legacy claims field would otherwise skip every
		// capability-keyed constraint, including the high-risk lease requirement.
		if err := profiledefs.ValidateSemantics(profiledefs.Semantics{
			Name:                  name,
			Runner:                prof.Runner,
			Capabilities:          profiledefs.NormalizeCapabilities(prof.Capabilities, prof.Claims),
			Writes:                prof.Writes,
			Shell:                 prof.Shell,
			Network:               prof.Network,
			GitPush:               prof.GitPush,
			RequireExplicit:       prof.RequireExplicit,
			RequireLease:          prof.RequireLease,
			EnforcementConfidence: prof.EnforcementConfidence,
		}); err != nil {
			return err
		}
		switch prof.Risk {
		case "", profiledefs.RiskLow, profiledefs.RiskMedium, profiledefs.RiskHigh:
		default:
			return fmt.Errorf("profiles.%s.risk must be low, medium, or high", name)
		}
		// The high-risk require_explicit/require_lease invariant is enforced by
		// ValidateSemantics above, keyed on capabilities so it holds for custom
		// profile names too.
		if prof.Risk == profiledefs.RiskMedium && (name == profiledefs.QueryNetwork || name == profiledefs.QueryShell) && !prof.RequireExplicit {
			return fmt.Errorf("profiles.%s risk=medium must require explicit selection", name)
		}
	}
	if err := c.validateProfileCeilings(); err != nil {
		return err
	}
	for name, runner := range c.Runners {
		if len(runner.Command) == 0 {
			return fmt.Errorf("runners.%s.command is required", name)
		}
		// default_profile must name a profile this config actually defines, or the
		// runner fails at task time with "profile_unavailable". Catch it here so a
		// bad config is rejected by validate_config instead of crashing preflight.
		if runner.DefaultProfile != "" {
			if _, ok := c.Profiles[runner.DefaultProfile]; !ok {
				return fmt.Errorf("runners.%s.default_profile references undefined profile %q", name, runner.DefaultProfile)
			}
		}
	}
	return nil
}

// validateProfileCeilings rejects a profile_ceilings mapping that could raise
// authority instead of capping it.
//
// A ceiling remaps a requested profile down to a weaker one at policy time. The
// remap is unconditional, so a mapping pointing at a *stronger* profile silently
// escalates every request naming the keyed profile — the opposite of what the
// field is for. Both endpoints must exist, and the ceiling's risk rank must not
// exceed the requested profile's.
//
// The comparison is rank <=, not <, so an equal-risk remap stays legal: the
// documented example (system-change -> repo-dev) is high -> high, and capping
// within a risk tier is a legitimate narrowing of capabilities. Risk comes from
// RiskForConfig rather than RiskForProfile because the latter returns "" for
// any custom profile name, which would make an unknown name compare as weakest.
func (c *Config) validateProfileCeilings() error {
	names := make([]string, 0, len(c.Policy.ProfileCeilings))
	for requested := range c.Policy.ProfileCeilings {
		names = append(names, requested)
	}
	sort.Strings(names)
	for _, requested := range names {
		ceiling := c.Policy.ProfileCeilings[requested]
		requestedProfile, ok := c.Profiles[requested]
		if !ok {
			return fmt.Errorf("policy.profile_ceilings references undefined profile %q", requested)
		}
		ceilingProfile, ok := c.Profiles[ceiling]
		if !ok {
			return fmt.Errorf("policy.profile_ceilings[%q] references undefined ceiling profile %q", requested, ceiling)
		}
		if _, ok := c.Runners[ceilingProfile.Runner]; !ok {
			return fmt.Errorf("policy.profile_ceilings[%q] ceiling profile %q references unknown runner %q", requested, ceiling, ceilingProfile.Runner)
		}
		if profiledefs.RiskRank(profileRisk(ceiling, ceilingProfile)) > profiledefs.RiskRank(profileRisk(requested, requestedProfile)) {
			return fmt.Errorf(
				"policy.profile_ceilings[%q] raises authority: ceiling %q is risk=%s but %q is risk=%s; a ceiling must not exceed the profile it caps",
				requested, ceiling, profileRisk(ceiling, ceilingProfile), requested, profileRisk(requested, requestedProfile),
			)
		}
	}
	return nil
}

// profileRisk returns a profile's effective risk, preferring the explicit risk
// field and falling back to the capability-derived value.
func profileRisk(name string, prof ProfileConfig) string {
	if prof.Risk != "" {
		return prof.Risk
	}
	return profiledefs.RiskForConfig(name, profiledefs.NormalizeCapabilities(prof.Capabilities, prof.Claims), prof.Writes, prof.Shell, prof.Network)
}

func containsString(values []string, needle string) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
}

func ExpandPath(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	if strings.HasPrefix(path, "~") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		if path == "~" {
			path = home
		} else if strings.HasPrefix(path, "~/") || strings.HasPrefix(path, `~\`) {
			path = filepath.Join(home, path[2:])
		}
	}
	if strings.Contains(path, "$") {
		path = os.ExpandEnv(path)
	}
	return filepath.Clean(path), nil
}
