package policy

import (
	"os"
	"path/filepath"
	"testing"

	"cmesh/internal/config"
	"cmesh/internal/profile"
)

func TestInboundAllowsConfiguredPeerAndCWD(t *testing.T) {
	root := t.TempDir()
	cfg := testConfig(root)
	decision := New(cfg).CheckInbound(TaskIntent{
		PeerName:     "mac-studio",
		CWD:          root,
		Profile:      "dev",
		OriginNodeID: "mac-studio",
	})
	if !decision.Allowed {
		t.Fatalf("expected allowed decision: %+v", decision)
	}
}

func TestInboundRejectsSpoofedOriginNodeID(t *testing.T) {
	root := t.TempDir()
	cfg := testConfig(root)
	decision := New(cfg).CheckInbound(TaskIntent{
		PeerName:     "untrusted-laptop",
		CWD:          root,
		Profile:      "dev",
		OriginNodeID: "mac-studio",
	})
	if decision.Allowed || decision.Code != "peer_unauthorized" {
		t.Fatalf("expected peer_unauthorized, got %+v", decision)
	}
}

// Regression: a profile scoped via profile-level allowed_peers must not be
// usable by a peer that merely claims the privileged origin via OriginNodeID
// (a request-body field). Authorization must key on the authenticated peer.
func TestProfileAllowedPeersIgnoresSpoofedOriginNodeID(t *testing.T) {
	root := t.TempDir()
	cfg := testConfig(root)
	// Both peers are allowed at the top level; the scoped profile is restricted
	// to mac-studio only.
	cfg.Policy.AllowedPeers = []string{"mac-studio", "untrusted-laptop"}
	cfg.Profiles["scoped"] = config.ProfileConfig{
		Runner:       "claude-code",
		Claims:       []string{"read_files"},
		AllowedPeers: []string{"mac-studio"},
	}
	decision := New(cfg).CheckInbound(TaskIntent{
		PeerName:     "untrusted-laptop", // authenticated identity
		PeerNodeID:   "untrusted-laptop",
		CWD:          root,
		Profile:      "scoped",
		OriginNodeID: "mac-studio", // attacker-supplied claim — must be ignored
	})
	if decision.Allowed {
		t.Fatalf("profile-level allowed_peers was bypassed via spoofed OriginNodeID: %+v", decision)
	}
	if decision.Code != "profile_denied" {
		t.Fatalf("expected profile_denied, got %+v", decision)
	}
	// Sanity: the genuinely-allowed peer still passes.
	ok := New(cfg).CheckInbound(TaskIntent{
		PeerName:   "mac-studio",
		PeerNodeID: "mac-studio",
		CWD:        root,
		Profile:    "scoped",
	})
	if !ok.Allowed {
		t.Fatalf("expected the scoped peer to be allowed: %+v", ok)
	}
}

func TestInboundAllowsTrustedPeerTag(t *testing.T) {
	root := t.TempDir()
	cfg := testConfig(root)
	cfg.Policy.AllowedPeers = []string{"tag:cmesh"}
	decision := New(cfg).CheckInbound(TaskIntent{
		PeerName:     "tagged-worker",
		PeerNodeID:   "node-1",
		PeerTags:     []string{"tag:cmesh"},
		CWD:          root,
		Profile:      "dev",
		OriginNodeID: "spoofed-origin",
	})
	if !decision.Allowed {
		t.Fatalf("expected tag-based allow decision: %+v", decision)
	}
}

func TestInboundAllowsSymlinkedAllowedRoot(t *testing.T) {
	root := t.TempDir()
	link := filepath.Join(t.TempDir(), "allowed")
	if err := os.Symlink(root, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	cfg := testConfig(root)
	cfg.Policy.AllowedCWDs = []string{filepath.Join(link, "**")}
	decision := New(cfg).CheckInbound(TaskIntent{
		PeerName:     "mac-studio",
		CWD:          root,
		Profile:      "dev",
		OriginNodeID: "mac-studio",
	})
	if !decision.Allowed {
		t.Fatalf("expected allowed decision: %+v", decision)
	}
}

func TestInboundRejectsSymlinkEscape(t *testing.T) {
	allowed := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(allowed, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	cfg := testConfig(allowed)
	decision := New(cfg).CheckInbound(TaskIntent{
		PeerName:     "mac-studio",
		CWD:          link,
		Profile:      "dev",
		OriginNodeID: "mac-studio",
	})
	if decision.Allowed || decision.Code != "cwd_denied" {
		t.Fatalf("expected cwd_denied, got %+v", decision)
	}
}

func TestInboundRejectsDisallowedCWD(t *testing.T) {
	allowed := t.TempDir()
	outside := t.TempDir()
	cfg := testConfig(allowed)
	decision := New(cfg).CheckInbound(TaskIntent{
		PeerName:     "mac-studio",
		CWD:          outside,
		Profile:      "dev",
		OriginNodeID: "mac-studio",
	})
	if decision.Allowed || decision.Code != "cwd_denied" {
		t.Fatalf("expected cwd_denied, got %+v", decision)
	}
}

func TestPathComparisonCaseInsensitivePlatforms(t *testing.T) {
	for _, goos := range []string{"windows", "darwin"} {
		if !pathComparisonIsCaseInsensitive(goos) {
			t.Fatalf("%s should use case-insensitive path comparison", goos)
		}
	}
	if pathComparisonIsCaseInsensitive("linux") {
		t.Fatal("linux should keep case-sensitive path comparison")
	}
}

func TestQueryTaskRequiresSafeRunnerDenyWriteProfileAndCapability(t *testing.T) {
	root := t.TempDir()
	cfg := testConfig(root)
	cfg.Profiles["query"] = config.ProfileConfig{
		Runner:       profile.RunnerProbe,
		Capabilities: []string{profile.CapDiskInfo},
		Writes:       profile.WritesDeny,
		Shell:        profile.ShellDeny,
	}
	cfg.Profiles["query-shell"] = config.ProfileConfig{
		Runner:       profile.RunnerShell,
		Capabilities: []string{profile.CapShellReadonly, profile.CapServiceStatus},
		Writes:       profile.WritesDeny,
		Shell:        profile.ShellAllowlisted,
	}
	cfg.Profiles["bad-shell-query"] = config.ProfileConfig{
		Runner:       profile.RunnerShell,
		Capabilities: []string{profile.CapShellReadonly},
		Writes:       profile.WritesDeny,
		Shell:        profile.ShellAllow,
	}
	cfg.Profiles["bad-query"] = config.ProfileConfig{
		Runner:       "claude-code",
		Capabilities: []string{profile.CapDiskInfo},
		Writes:       profile.WritesDeny,
		Shell:        profile.ShellDeny,
	}
	cfg.Runners[profile.RunnerProbe] = config.RunnerConfig{Command: []string{"builtin"}, DefaultProfile: "query"}
	cfg.Runners[profile.RunnerShell] = config.RunnerConfig{Command: []string{"builtin"}, DefaultProfile: "query-shell"}

	allowed := New(cfg).CheckInbound(TaskIntent{
		PeerName:   "mac-studio",
		CWD:        root,
		Profile:    "query",
		TaskKind:   "query",
		Capability: profile.CapDiskInfo,
	})
	if !allowed.Allowed {
		t.Fatalf("query disk_info should be allowed: %+v", allowed)
	}

	deniedCapability := New(cfg).CheckInbound(TaskIntent{
		PeerName:   "mac-studio",
		CWD:        root,
		Profile:    "query",
		TaskKind:   "query",
		Capability: profile.CapShellReadonly,
	})
	if deniedCapability.Allowed || deniedCapability.Code != "profile_denied" {
		t.Fatalf("query shell_readonly should be denied, got %+v", deniedCapability)
	}

	allowedShell := New(cfg).CheckInbound(TaskIntent{
		PeerName:   "mac-studio",
		CWD:        root,
		Profile:    "query-shell",
		TaskKind:   "query",
		Capability: profile.CapServiceStatus,
	})
	if !allowedShell.Allowed {
		t.Fatalf("query-shell service_status should be allowed: %+v", allowedShell)
	}

	deniedShellMode := New(cfg).CheckInbound(TaskIntent{
		PeerName:   "mac-studio",
		CWD:        root,
		Profile:    "bad-shell-query",
		TaskKind:   "query",
		Capability: profile.CapShellReadonly,
	})
	if deniedShellMode.Allowed || deniedShellMode.Code != "profile_denied" {
		t.Fatalf("query-shell with shell=allow should be denied, got %+v", deniedShellMode)
	}

	deniedRunner := New(cfg).CheckInbound(TaskIntent{
		PeerName:   "mac-studio",
		CWD:        root,
		Profile:    "bad-query",
		TaskKind:   "query",
		Capability: profile.CapDiskInfo,
	})
	if deniedRunner.Allowed || deniedRunner.Code != "profile_denied" {
		t.Fatalf("query on agent runner should be denied, got %+v", deniedRunner)
	}
}

func TestRequireLeaseProfileNeedsActiveContextID(t *testing.T) {
	root := t.TempDir()
	cfg := testConfig(root)
	cfg.Profiles["repo-dev"] = config.ProfileConfig{
		Runner:          "fake",
		Capabilities:    []string{profile.CapReadFiles, profile.CapEditRepo, profile.CapExecuteInRepo},
		Writes:          profile.WritesRepoOnly,
		Shell:           profile.ShellAllow,
		RequireExplicit: true,
		RequireLease:    true,
	}
	cfg.Runners["fake"] = config.RunnerConfig{Command: []string{"fake"}, DefaultProfile: "dev"}

	denied := New(cfg).CheckInbound(TaskIntent{
		PeerName: "mac-studio",
		CWD:      root,
		Profile:  "repo-dev",
	})
	if denied.Allowed || denied.Code != "profile_denied" {
		t.Fatalf("repo-dev without lease should be denied, got %+v", denied)
	}

	fakeID := NewWithLeaseValidator(cfg, fakeLeaseValidator{err: os.ErrNotExist}).CheckInbound(TaskIntent{
		PeerName:         "mac-studio",
		CWD:              root,
		Profile:          "repo-dev",
		ProfileContextID: "lease_fake",
	})
	if fakeID.Allowed || fakeID.Code != "profile_denied" {
		t.Fatalf("repo-dev with fake lease should be denied, got %+v", fakeID)
	}

	allowed := NewWithLeaseValidator(cfg, fakeLeaseValidator{}).CheckInbound(TaskIntent{
		PeerName:         "mac-studio",
		CWD:              root,
		Profile:          "repo-dev",
		ProfileContextID: "lease_test",
	})
	if !allowed.Allowed {
		t.Fatalf("repo-dev with lease should be allowed, got %+v", allowed)
	}
}

type fakeLeaseValidator struct {
	err error
}

func (f fakeLeaseValidator) ValidateProfileLease(req LeaseValidation) (LeaseValidation, error) {
	return req, f.err
}

// TestSmokeCWDResolvesToAnAllowedExistingDir proves SmokeCWD returns a concrete
// cwd that CheckInbound accepts, for both the recursive "/**" allowlist form
// (root is allowed) and the single-segment "/*" form (a child must be chosen,
// since the parent does not match "*"). This is the invariant the onboarding
// smoke depends on: a hardcoded "~" would fail a conservatively-scoped allowlist.
func TestSmokeCWDResolvesToAnAllowedExistingDir(t *testing.T) {
	t.Run("recursive glob returns the allowed root", func(t *testing.T) {
		root := t.TempDir()
		cfg := testConfig(root) // allowed_cwds = [root/**]
		cwd := SmokeCWD(cfg)
		if cwd == "" {
			t.Fatal("SmokeCWD returned empty for a recursive glob over an existing dir")
		}
		assertCWDAllowed(t, cfg, cwd)
	})

	t.Run("single-segment glob descends to an existing child", func(t *testing.T) {
		// EvalSymlinks the root so the "/*" pattern (which pathAllowed matches via
		// filepath.Match without resolving symlinks) lines up with the normalized
		// cwd; otherwise a symlinked temp root (/var -> /private/var on macOS) makes
		// even a valid child unmatchable. This mirrors how a real, non-symlinked
		// allowlist path (e.g. /Users/me/code/*) behaves.
		base := t.TempDir()
		root, err := filepath.EvalSymlinks(base)
		if err != nil {
			t.Fatal(err)
		}
		child := filepath.Join(root, "repo")
		if err := os.Mkdir(child, 0o755); err != nil {
			t.Fatal(err)
		}
		cfg := testConfig(root)
		cfg.Policy.AllowedCWDs = []string{filepath.Join(root, "*")}
		cwd := SmokeCWD(cfg)
		if cwd == "" {
			t.Fatal("SmokeCWD returned empty despite an existing child dir")
		}
		assertCWDAllowed(t, cfg, cwd)
	})

	t.Run("single-segment glob with no child returns empty", func(t *testing.T) {
		base := t.TempDir() // empty: no subdirectories to satisfy "*"
		root, err := filepath.EvalSymlinks(base)
		if err != nil {
			t.Fatal(err)
		}
		cfg := testConfig(root)
		cfg.Policy.AllowedCWDs = []string{filepath.Join(root, "*")}
		if cwd := SmokeCWD(cfg); cwd != "" {
			t.Fatalf("expected empty cwd (no child to match \"*\"), got %q", cwd)
		}
	})
}

// assertCWDAllowed fails the test unless cwd passes CheckInbound — the real
// contract SmokeCWD must uphold.
func assertCWDAllowed(t *testing.T, cfg *config.Config, cwd string) {
	t.Helper()
	decision := New(cfg).CheckInbound(TaskIntent{
		PeerName:     "mac-studio",
		CWD:          cwd,
		Profile:      "dev",
		OriginNodeID: "mac-studio",
	})
	if !decision.Allowed {
		t.Fatalf("SmokeCWD %q is not accepted by CheckInbound: %+v", cwd, decision)
	}
}

func TestInboundRejectsUnavailableProfile(t *testing.T) {
	root := t.TempDir()
	cfg := testConfig(root)
	decision := New(cfg).CheckInbound(TaskIntent{
		PeerName:     "mac-studio",
		CWD:          root,
		Profile:      "network",
		OriginNodeID: "mac-studio",
	})
	if decision.Allowed || decision.Code != "profile_unavailable" {
		t.Fatalf("expected profile_unavailable, got %+v", decision)
	}
}

func TestInboundProfileAllowedPeersRestrictsInteractiveProfile(t *testing.T) {
	root := t.TempDir()
	cfg := testConfig(root)
	cfg.Policy.AllowedPeers = []string{"mac-studio", "other-laptop"}
	cfg.Profiles["interactive"] = config.ProfileConfig{
		Runner:       "claude-code",
		Claims:       []string{"read_files", "execute_repo_commands", "network", "desktop_interaction"},
		AllowedPeers: []string{"mac-studio"},
		Network:      true,
	}
	allowed := New(cfg).CheckInbound(TaskIntent{
		PeerName:     "mac-studio",
		CWD:          root,
		Profile:      "interactive",
		OriginNodeID: "mac-studio",
	})
	if !allowed.Allowed {
		t.Fatalf("expected trusted peer to use interactive profile: %+v", allowed)
	}
	denied := New(cfg).CheckInbound(TaskIntent{
		PeerName:     "other-laptop",
		CWD:          root,
		Profile:      "interactive",
		OriginNodeID: "other-laptop",
	})
	if denied.Allowed || denied.Code != "profile_denied" {
		t.Fatalf("expected profile_denied for non-profile peer, got %+v", denied)
	}
}

func TestInboundProfileRequireExplicitRejectsImplicitDefault(t *testing.T) {
	root := t.TempDir()
	cfg := testConfig(root)
	cfg.Runners["claude-code"] = config.RunnerConfig{Command: []string{"claude", "-p"}, DefaultProfile: "interactive"}
	cfg.Profiles["interactive"] = config.ProfileConfig{
		Runner:          "claude-code",
		Claims:          []string{"read_files", "execute_repo_commands", "network", "desktop_interaction"},
		AllowedPeers:    []string{"mac-studio"},
		RequireExplicit: true,
		Network:         true,
	}
	denied := New(cfg).CheckInbound(TaskIntent{
		PeerName:     "mac-studio",
		CWD:          root,
		Runner:       "claude-code",
		OriginNodeID: "mac-studio",
	})
	if denied.Allowed || denied.Code != "profile_denied" {
		t.Fatalf("expected profile_denied for implicit interactive default, got %+v", denied)
	}
	allowed := New(cfg).CheckInbound(TaskIntent{
		PeerName:     "mac-studio",
		CWD:          root,
		Runner:       "claude-code",
		Profile:      "interactive",
		OriginNodeID: "mac-studio",
	})
	if !allowed.Allowed {
		t.Fatalf("expected explicit interactive request to pass: %+v", allowed)
	}
}

func TestOutboundProfileAllowedPeersRestrictsInteractiveProfile(t *testing.T) {
	root := t.TempDir()
	cfg := testConfig(root)
	cfg.Policy.Outbound.AllowedPeers = []string{"win11-desktop", "linux-worker"}
	cfg.Profiles["interactive"] = config.ProfileConfig{
		Runner:       "claude-code",
		Claims:       []string{"read_files", "execute_repo_commands", "network", "desktop_interaction"},
		AllowedPeers: []string{"win11-desktop"},
		Network:      true,
	}
	allowed := New(cfg).CheckOutbound(TaskIntent{
		PeerName: "win11-desktop",
		CWD:      root,
		Profile:  "interactive",
	})
	if !allowed.Allowed {
		t.Fatalf("expected trusted peer to use interactive profile outbound: %+v", allowed)
	}
	denied := New(cfg).CheckOutbound(TaskIntent{
		PeerName: "linux-worker",
		CWD:      root,
		Profile:  "interactive",
	})
	if denied.Allowed || denied.Code != "profile_denied" {
		t.Fatalf("expected profile_denied for non-profile outbound peer, got %+v", denied)
	}
}

func TestInboundRejectsCycle(t *testing.T) {
	root := t.TempDir()
	cfg := testConfig(root)
	decision := New(cfg).CheckInbound(TaskIntent{
		PeerName:     "mac-studio",
		CWD:          root,
		Profile:      "dev",
		OriginNodeID: "mac-studio",
		Chain:        []string{"homelab-vm"},
	})
	if decision.Allowed || decision.Code != "profile_denied" {
		t.Fatalf("expected cycle denial, got %+v", decision)
	}
}

func TestOutboundRejectsUnknownPeer(t *testing.T) {
	root := t.TempDir()
	cfg := testConfig(root)
	cfg.Policy.Outbound.AllowedPeers = []string{"homelab-vm"}
	decision := New(cfg).CheckOutbound(TaskIntent{
		PeerName:     "unknown-peer",
		CWD:          root,
		Profile:      "dev",
		OriginNodeID: "mac-studio",
	})
	if decision.Allowed || decision.Code != "peer_unauthorized" {
		t.Fatalf("expected peer_unauthorized, got %+v", decision)
	}
}

// A profile ceiling exists to cap authority. A mapping that points at a
// stronger profile must be refused rather than silently applied, because the
// remap is unconditional: applying it would escalate every request naming the
// keyed profile.
func TestCeilingCannotRaiseAuthority(t *testing.T) {
	root := t.TempDir()
	cfg := testConfig(root)
	cfg.Profiles["look"] = config.ProfileConfig{
		Runner:       "claude-code",
		Capabilities: []string{profile.CapReadFiles},
		Writes:       profile.WritesDeny,
		Shell:        profile.ShellDeny,
		Risk:         profile.RiskMedium,
	}
	cfg.Profiles["power"] = config.ProfileConfig{
		Runner:          "claude-code",
		Capabilities:    []string{profile.CapReadFiles, profile.CapEditRepo},
		Writes:          profile.WritesRepoOnly,
		Shell:           profile.ShellAllow,
		Risk:            profile.RiskHigh,
		RequireExplicit: true,
		RequireLease:    true,
	}
	cfg.Policy.ProfileCeilings = map[string]string{"look": "power"}

	decision := New(cfg).CheckInbound(TaskIntent{
		PeerName: "mac-studio",
		CWD:      root,
		Profile:  "look",
	})
	if decision.Allowed {
		t.Fatalf("ceiling must not raise authority: %+v", decision)
	}
	if decision.Code != "profile_denied" {
		t.Fatalf("code = %q, want profile_denied: %+v", decision.Code, decision)
	}
	if decision.EffectiveProfile == "power" {
		t.Fatal("denied decision must not report the escalated profile as effective")
	}
}

// Capping to an equal-risk profile is legitimate narrowing (the documented
// example maps system-change to repo-dev, both high risk), so rank equality
// must stay allowed.
func TestCeilingAllowsEqualRiskCap(t *testing.T) {
	root := t.TempDir()
	cfg := testConfig(root)
	cfg.Profiles["broad"] = config.ProfileConfig{
		Runner:          "claude-code",
		Capabilities:    []string{profile.CapReadFiles, profile.CapSystemChange},
		Writes:          profile.WritesAllow,
		Shell:           profile.ShellAllow,
		Risk:            profile.RiskHigh,
		RequireExplicit: true,
		RequireLease:    true,
	}
	cfg.Profiles["narrow"] = config.ProfileConfig{
		Runner:       "claude-code",
		Capabilities: []string{profile.CapReadFiles, profile.CapEditRepo},
		Writes:       profile.WritesRepoOnly,
		Shell:        profile.ShellAllow,
		Risk:         profile.RiskHigh,
	}
	cfg.Policy.ProfileCeilings = map[string]string{"broad": "narrow"}

	decision := New(cfg).CheckInbound(TaskIntent{
		PeerName: "mac-studio",
		CWD:      root,
		Profile:  "broad",
	})
	if !decision.Allowed {
		t.Fatalf("equal-risk cap should be allowed: %+v", decision)
	}
	if decision.EffectiveProfile != "narrow" {
		t.Fatalf("effective profile = %q, want narrow", decision.EffectiveProfile)
	}
}

// The runner that executes must belong to the profile that was enforced. When a
// ceiling remaps the effective profile, the decision must report the ceiling's
// runner so callers do not resolve one from the requested profile name.
func TestCeilingReportsEffectiveRunner(t *testing.T) {
	root := t.TempDir()
	cfg := testConfig(root)
	cfg.Runners["probe"] = config.RunnerConfig{Command: []string{"builtin"}, DefaultProfile: "capped"}
	cfg.Profiles["asked"] = config.ProfileConfig{
		Runner:       "claude-code",
		Capabilities: []string{profile.CapReadFiles},
		Writes:       profile.WritesDeny,
		Shell:        profile.ShellDeny,
		Risk:         profile.RiskMedium,
	}
	cfg.Profiles["capped"] = config.ProfileConfig{
		Runner:       profile.RunnerProbe,
		Capabilities: []string{profile.CapDiskInfo},
		Writes:       profile.WritesDeny,
		Shell:        profile.ShellDeny,
		Risk:         profile.RiskLow,
	}
	cfg.Policy.ProfileCeilings = map[string]string{"asked": "capped"}

	decision := New(cfg).CheckInbound(TaskIntent{
		PeerName: "mac-studio",
		CWD:      root,
		Profile:  "asked",
	})
	if !decision.Allowed {
		t.Fatalf("expected allowed decision: %+v", decision)
	}
	if decision.EffectiveProfile != "capped" {
		t.Fatalf("effective profile = %q, want capped", decision.EffectiveProfile)
	}
	if decision.EffectiveRunner != profile.RunnerProbe {
		t.Fatalf("effective runner = %q, want %q", decision.EffectiveRunner, profile.RunnerProbe)
	}
}

// A ceiling whose runner is not configured on this node must be refused rather
// than dispatched: the runner checks earlier in checkProfile ran against the
// requested profile, not the ceiling.
func TestCeilingRejectsUnconfiguredRunner(t *testing.T) {
	root := t.TempDir()
	cfg := testConfig(root)
	cfg.Profiles["asked"] = config.ProfileConfig{
		Runner:       "claude-code",
		Capabilities: []string{profile.CapReadFiles},
		Writes:       profile.WritesDeny,
		Shell:        profile.ShellDeny,
		Risk:         profile.RiskMedium,
	}
	cfg.Profiles["capped"] = config.ProfileConfig{
		Runner:       "not-configured",
		Capabilities: []string{profile.CapReadFiles},
		Writes:       profile.WritesDeny,
		Shell:        profile.ShellDeny,
		Risk:         profile.RiskLow,
	}
	cfg.Policy.ProfileCeilings = map[string]string{"asked": "capped"}

	decision := New(cfg).CheckInbound(TaskIntent{
		PeerName: "mac-studio",
		CWD:      root,
		Profile:  "asked",
	})
	if decision.Allowed || decision.Code != "runner_unavailable" {
		t.Fatalf("expected runner_unavailable, got %+v", decision)
	}
}

func testConfig(root string) *config.Config {
	return &config.Config{
		Node:    config.NodeConfig{ID: "homelab-vm", Name: "homelab-vm", MeshPort: 7778, MCPPort: 7777},
		Tailnet: config.TailnetConfig{Mode: "host", Bind: "tailnet_ip"},
		Policy: config.PolicyConfig{
			AllowedPeers: []string{"mac-studio"},
			AllowedCWDs:  []string{filepath.Join(root, "**")},
			HopLimit:     2,
		},
		Profiles: map[string]config.ProfileConfig{
			"dev": {Runner: "claude-code", Claims: []string{"read_files", "edit_repo"}},
		},
		Runners: map[string]config.RunnerConfig{
			"claude-code": {Command: []string{"claude", "-p"}, DefaultProfile: "dev"},
		},
	}
}
