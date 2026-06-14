package onboard

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cmesh/internal/config"
	"cmesh/internal/policy"
)

func TestDeriveOnboardingState(t *testing.T) {
	upMachine := Machine{Name: "node-a", TailscaleOK: true, TailnetIPs: []string{"100.64.0.1"}, NodeID: "stable-a"}
	downMachine := Machine{Name: "node-a", TailscaleOK: false}

	onlinePeer := PeerCandidate{Name: "node-b", Address: "100.64.0.2", Online: true}
	cmeshPeer := PeerCandidate{Name: "node-b", Address: "100.64.0.2", Online: true, CmeshCapable: true}

	healthy := func(context.Context, int) bool { return true }
	unhealthy := func(context.Context, int) bool { return false }

	cfgPresent := func(string) (*config.Config, error) {
		return &config.Config{Node: config.NodeConfig{MCPPort: config.DefaultMCPPort}}, nil
	}
	cfgLinked := func(string) (*config.Config, error) {
		return &config.Config{
			Node:    config.NodeConfig{MCPPort: config.DefaultMCPPort},
			Peers:   map[string]config.PeerConfig{"node-b": {Name: "node-b", Address: "100.64.0.2"}},
			Runners: map[string]config.RunnerConfig{"claude-code": {DefaultProfile: "dev"}},
		}, nil
	}
	// cfgOriginLinked mirrors cfgLinked but the node is origin-only (role="origin").
	// An origin-only node serves no inbound worker duty, so it must reach 'ready'
	// on the linked-and-reachable signal WITHOUT a passing reverse smoke.
	cfgOriginLinked := func(string) (*config.Config, error) {
		return &config.Config{
			Node:    config.NodeConfig{MCPPort: config.DefaultMCPPort, Role: "origin"},
			Peers:   map[string]config.PeerConfig{"node-b": {Name: "node-b", Address: "100.64.0.2"}},
			Runners: map[string]config.RunnerConfig{"claude-code": {DefaultProfile: "dev"}},
		}, nil
	}
	cfgMissing := func(string) (*config.Config, error) { return nil, os.ErrNotExist }

	smokePass := func(context.Context, PeerCandidate) SmokeResult { return SmokeResult{OK: true, Reachable: true} }
	// smokeFail models an unreachable peer (the bare "did not pass" failure).
	smokeFail := func(context.Context, PeerCandidate) SmokeResult { return SmokeResult{Detail: "peer unreachable"} }
	// smokeDenied models a peer that was REACHED but rejected the task on policy.
	smokeDenied := func(context.Context, PeerCandidate) SmokeResult {
		return SmokeResult{Reachable: true, PolicyDenied: true, Detail: "forward task failed: cwd_denied"}
	}
	// smokeReachableUntrusted models a peer reached but failing the smoke without a
	// policy signal (an auth/trust gap rather than a connectivity one).
	smokeReachableUntrusted := func(context.Context, PeerCandidate) SmokeResult {
		return SmokeResult{Reachable: true, Detail: "reverse smoke failed"}
	}

	tests := []struct {
		name    string
		machine Machine
		peers   []PeerCandidate
		health  func(context.Context, int) bool
		load    func(string) (*config.Config, error)
		smoke   SmokeFunc
		want    OnboardingState
	}{
		{"tailscale_down", downMachine, nil, healthy, cfgPresent, smokePass, StateTailscaleDown},
		{"tailscale_solo", upMachine, nil, healthy, cfgMissing, smokePass, StateTailscaleSolo},
		{"needs_config", upMachine, []PeerCandidate{onlinePeer}, healthy, cfgMissing, smokePass, StateNeedsConfig},
		{"daemon_down", upMachine, []PeerCandidate{onlinePeer}, unhealthy, cfgPresent, smokePass, StateDaemonDown},
		{"no_cmesh_peer", upMachine, []PeerCandidate{onlinePeer}, healthy, cfgPresent, smokePass, StateNoCmeshPeer},
		{"peer_untrusted_not_linked", upMachine, []PeerCandidate{cmeshPeer}, healthy, cfgPresent, smokePass, StatePeerUntrusted},
		// J3: peer is linked in config but the bidirectional smoke FAILS (unreachable)
		// -> must NOT be ready; it must report peer_untrusted with an actionable step.
		{"peer_untrusted_smoke_fails", upMachine, []PeerCandidate{cmeshPeer}, healthy, cfgLinked, smokeFail, StatePeerUntrusted},
		// Reached but failing without a policy signal -> still peer_untrusted (trust).
		{"peer_untrusted_reachable", upMachine, []PeerCandidate{cmeshPeer}, healthy, cfgLinked, smokeReachableUntrusted, StatePeerUntrusted},
		// Reached but rejected on policy -> peer_policy_denied (fix the REMOTE config).
		{"peer_policy_denied", upMachine, []PeerCandidate{cmeshPeer}, healthy, cfgLinked, smokeDenied, StatePeerPolicyDenied},
		// J3: a nil smokeFn cannot verify -> never a false ready.
		{"peer_untrusted_no_smoke_fn", upMachine, []PeerCandidate{cmeshPeer}, healthy, cfgLinked, nil, StatePeerUntrusted},
		// ready requires an actual passing smoke for worker/both (default, empty role).
		{"ready", upMachine, []PeerCandidate{cmeshPeer}, healthy, cfgLinked, smokePass, StateReady},
		// Origin-only node: no inbound worker duty, so 'ready' must NOT require the
		// reverse smoke. A linked peer reaches ready even with a FAILING smoke...
		{"ready_origin_only_smoke_fails", upMachine, []PeerCandidate{cmeshPeer}, healthy, cfgOriginLinked, smokeFail, StateReady},
		// ...and even with no smokeFn at all (the gate is skipped for origin-only).
		{"ready_origin_only_no_smoke_fn", upMachine, []PeerCandidate{cmeshPeer}, healthy, cfgOriginLinked, nil, StateReady},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			restore := swapStateFakes(tt.machine, tt.peers, tt.load)
			defer restore()
			got := DeriveOnboardingState(context.Background(), "/tmp/does-not-matter.toml", tt.health, tt.smoke)
			if got.State != tt.want {
				t.Fatalf("state = %q, want %q (detail: %s)", got.State, tt.want, got.Detail)
			}
			if got.NextStep == "" {
				t.Fatalf("state %q has empty next_step", got.State)
			}
		})
	}
}

// TestReadyPayoffRoutesToRegisterGatewayAndShowsExamples covers J1: the verified
// 'ready' state must route to register_gateway and surface the Part-5 payoff
// (example prompts templated with the real peer name + the per-peer default
// profile).
func TestReadyPayoffRoutesToRegisterGatewayAndShowsExamples(t *testing.T) {
	machine := Machine{Name: "node-a", TailscaleOK: true, TailnetIPs: []string{"100.64.0.1"}, NodeID: "stable-a"}
	cmeshPeer := PeerCandidate{Name: "node-b", Address: "100.64.0.2", Online: true, CmeshCapable: true}
	load := func(string) (*config.Config, error) {
		return &config.Config{
			Node:    config.NodeConfig{MCPPort: config.DefaultMCPPort},
			Peers:   map[string]config.PeerConfig{"node-b": {Name: "node-b", Address: "100.64.0.2"}},
			Runners: map[string]config.RunnerConfig{"claude-code": {DefaultProfile: "dev"}},
		}, nil
	}
	restore := swapStateFakes(machine, []PeerCandidate{cmeshPeer}, load)
	defer restore()

	got := DeriveOnboardingState(context.Background(), "/tmp/x.toml",
		func(context.Context, int) bool { return true },
		func(context.Context, PeerCandidate) SmokeResult { return SmokeResult{OK: true, Reachable: true} })

	if got.State != StateReady {
		t.Fatalf("state = %q, want ready (detail: %s)", got.State, got.Detail)
	}
	if !strings.Contains(got.NextStep, "register_gateway") {
		t.Fatalf("ready next_step must route to register_gateway: %q", got.NextStep)
	}
	// Payoff: example prompts templated with the real peer name.
	if !strings.Contains(got.Detail, "node-b") {
		t.Fatalf("payoff must template the real peer name node-b: %s", got.Detail)
	}
	if !strings.Contains(strings.ToLower(got.Detail), "disk usage") {
		t.Fatalf("payoff must include example prompts from the recipe set: %s", got.Detail)
	}
	// Payoff: the per-peer default profile (read from runners.<r>.default_profile).
	if !strings.Contains(got.Detail, "dev") {
		t.Fatalf("payoff must include the default profile dev: %s", got.Detail)
	}
	if !strings.Contains(strings.ToLower(got.Detail), "readonly") {
		t.Fatalf("payoff must mention how to switch profiles (readonly): %s", got.Detail)
	}
}

// TestOriginOnlyReachesReadyWithoutReverseSmoke covers the role-persistence fix:
// an origin-only node (role="origin") serves no inbound worker duty, so reaching
// the linked-and-reachable state IS 'ready' — it must NOT be held at
// peer_untrusted waiting for a reverse (peer -> this node) smoke it never serves.
// Asserted with a FAILING smoke (the strongest form) so the test proves the gate
// is skipped, not merely satisfied. The ready next_step + payoff must still fire.
func TestOriginOnlyReachesReadyWithoutReverseSmoke(t *testing.T) {
	machine := Machine{Name: "node-a", TailscaleOK: true, TailnetIPs: []string{"100.64.0.1"}, NodeID: "stable-a"}
	cmeshPeer := PeerCandidate{Name: "node-b", Address: "100.64.0.2", Online: true, CmeshCapable: true}
	load := func(string) (*config.Config, error) {
		return &config.Config{
			Node:    config.NodeConfig{MCPPort: config.DefaultMCPPort, Role: "origin"},
			Peers:   map[string]config.PeerConfig{"node-b": {Name: "node-b", Address: "100.64.0.2"}},
			Runners: map[string]config.RunnerConfig{"claude-code": {DefaultProfile: "dev"}},
		}, nil
	}
	restore := swapStateFakes(machine, []PeerCandidate{cmeshPeer}, load)
	defer restore()

	got := DeriveOnboardingState(context.Background(), "/tmp/x.toml",
		func(context.Context, int) bool { return true },
		func(context.Context, PeerCandidate) SmokeResult { return SmokeResult{Detail: "reverse smoke FAILS"} }) // reverse smoke FAILS

	if got.State != StateReady {
		t.Fatalf("origin-only node must reach ready without the reverse smoke; got %q (detail: %s)", got.State, got.Detail)
	}
	if !strings.Contains(got.NextStep, "register_gateway") {
		t.Fatalf("origin-only ready next_step must route to register_gateway: %q", got.NextStep)
	}
	// The payoff still fires (example prompts templated with the real peer name).
	if !strings.Contains(got.Detail, "node-b") || !strings.Contains(strings.ToLower(got.Detail), "disk usage") {
		t.Fatalf("origin-only ready must surface the payoff: %s", got.Detail)
	}
	// The readiness sentence must be accurate: origin-only did not run a smoke, so
	// it must not claim "bidirectional smoke passed".
	if strings.Contains(strings.ToLower(got.Detail), "bidirectional smoke passed") {
		t.Fatalf("origin-only payoff must not claim a passing smoke: %s", got.Detail)
	}
}

func swapStateFakes(machine Machine, peers []PeerCandidate, load func(string) (*config.Config, error)) func() {
	origMachine := detectMachineFn
	origPeers := tailnetPeersFn
	origLoad := loadConfigFn
	detectMachineFn = func(context.Context) Machine { return machine }
	tailnetPeersFn = func(context.Context, int, bool) ([]PeerCandidate, error) { return peers, nil }
	loadConfigFn = load
	return func() {
		detectMachineFn = origMachine
		tailnetPeersFn = origPeers
		loadConfigFn = origLoad
	}
}

func TestAddPeerLinkWritesMatchableIdentity(t *testing.T) {
	path := writeBaseConfig(t)
	peer := PeerCandidate{Name: "Node-B", NodeID: "n12345ephemeral", Address: "100.64.0.2", Online: true, CmeshCapable: true}

	// grantInbound=true (worker|both, the default): bidirectional trust grant.
	name, err := AddPeerLink(path, peer, "nodeB-stable-id", true)
	if err != nil {
		t.Fatalf("AddPeerLink: %v", err)
	}
	if name != "node-b" {
		t.Fatalf("peer name = %q, want node-b", name)
	}
	body := readFile(t, path)
	if !strings.Contains(body, "[peers.node-b]") {
		t.Fatalf("missing peers block:\n%s", body)
	}
	// matchable identity (hostname label) must be present, ephemeral node id must not.
	if !strings.Contains(body, `node_id = "Node-B"`) {
		t.Fatalf("expected matchable identity Node-B in node_id:\n%s", body)
	}
	if strings.Contains(body, "n12345ephemeral") {
		t.Fatalf("ephemeral discovery node id leaked into config:\n%s", body)
	}
	// J2: outbound AND inbound allow must contain the matchable identity AND the
	// captured stable id; the original inbound entry ([self]) must be preserved.
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if !contains(cfg.Policy.Outbound.AllowedPeers, "Node-B") {
		t.Fatalf("outbound allowed_peers missing Node-B: %v", cfg.Policy.Outbound.AllowedPeers)
	}
	if !contains(cfg.Policy.Outbound.AllowedPeers, "nodeB-stable-id") {
		t.Fatalf("outbound allowed_peers missing captured stable id: %v", cfg.Policy.Outbound.AllowedPeers)
	}
	if !contains(cfg.Policy.AllowedPeers, "Node-B") {
		t.Fatalf("inbound allowed_peers missing the consented peer Node-B: %v", cfg.Policy.AllowedPeers)
	}
	if !contains(cfg.Policy.AllowedPeers, "nodeB-stable-id") {
		t.Fatalf("inbound allowed_peers missing captured stable id: %v", cfg.Policy.AllowedPeers)
	}
	if !contains(cfg.Policy.AllowedPeers, "node-a") {
		t.Fatalf("inbound grant clobbered the existing self entry: %v", cfg.Policy.AllowedPeers)
	}
}

// TestAddPeerLinkOriginOnlySkipsInbound covers the role-aware guard: an origin-only
// node (grantInbound=false) gets the outbound grant but NOT the inbound grant.
func TestAddPeerLinkOriginOnlySkipsInbound(t *testing.T) {
	path := writeBaseConfig(t)
	peer := PeerCandidate{Name: "Node-B", Address: "100.64.0.2", Online: true, CmeshCapable: true}

	if _, err := AddPeerLink(path, peer, "nodeB-stable-id", false); err != nil {
		t.Fatalf("AddPeerLink: %v", err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if !contains(cfg.Policy.Outbound.AllowedPeers, "Node-B") {
		t.Fatalf("origin-only still grants outbound: %v", cfg.Policy.Outbound.AllowedPeers)
	}
	if contains(cfg.Policy.AllowedPeers, "Node-B") {
		t.Fatalf("origin-only must NOT take the inbound grant: %v", cfg.Policy.AllowedPeers)
	}
}

// TestAddPeerLinkPrunesStaleSectionForSameAddress is the self-heal regression: a
// config already carries a [peers.*] block written under a superseded (display-name)
// identity for a machine, plus its dead allowlist strings. Re-linking that machine
// under the correct whois-matchable identity must (a) write the correct section,
// (b) remove the stale section for the same tailnet address, and (c) drop the dead
// identity strings from both allowlists — leaving no orphaned trust behind and no
// hand-editing required.
func TestAddPeerLinkPrunesStaleSectionForSameAddress(t *testing.T) {
	path := writeBaseConfig(t)
	const addr = "100.120.229.49"

	// Seed the exact stale shape an earlier buggy discovery wrote: a display-name
	// section + display-name allowlist entries, all for the same address.
	stale := PeerCandidate{Name: "Scott's MacBook Pro", NodeID: "Scott's MacBook Pro", Address: addr, CmeshCapable: true}
	if _, err := AddPeerLink(path, stale, "", true); err != nil {
		t.Fatalf("seed stale link: %v", err)
	}
	seeded := readFile(t, path)
	if !strings.Contains(seeded, "Scott's MacBook Pro") {
		t.Fatalf("expected the seeded stale entry to be present:\n%s", seeded)
	}

	// Re-link the SAME machine (same address) under the corrected whois identity.
	good := PeerCandidate{Name: "scotts-macbook-pro", NodeID: "scotts-macbook-pro", Address: addr, CmeshCapable: true}
	name, err := AddPeerLink(path, good, "nduXyDZMbU11CNTRL", true)
	if err != nil {
		t.Fatalf("re-link: %v", err)
	}
	if name != "scotts-macbook-pro" {
		t.Fatalf("name = %q, want scotts-macbook-pro", name)
	}

	body := readFile(t, path)
	// (a) correct section present.
	if !strings.Contains(body, "[peers.scotts-macbook-pro]") {
		t.Fatalf("missing corrected peer section:\n%s", body)
	}
	// (b) stale section pruned (matched by address).
	if strings.Contains(body, "[peers.scott-s-macbook-pro]") || strings.Contains(body, `[peers.Scott`) {
		t.Fatalf("stale display-name peer section was not pruned:\n%s", body)
	}
	// (c) dead display-name identity gone from the allowlists; correct identities kept.
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if contains(cfg.Policy.AllowedPeers, "Scott's MacBook Pro") || contains(cfg.Policy.Outbound.AllowedPeers, "Scott's MacBook Pro") {
		t.Fatalf("dead display-name identity still in allowlists: inbound=%v outbound=%v", cfg.Policy.AllowedPeers, cfg.Policy.Outbound.AllowedPeers)
	}
	if !contains(cfg.Policy.AllowedPeers, "scotts-macbook-pro") {
		t.Fatalf("corrected identity missing from inbound allowlist: %v", cfg.Policy.AllowedPeers)
	}
	if !contains(cfg.Policy.AllowedPeers, "nduXyDZMbU11CNTRL") {
		t.Fatalf("captured stable id missing from inbound allowlist: %v", cfg.Policy.AllowedPeers)
	}
	// Exactly one peer section for this machine remains.
	if n := strings.Count(body, "address = "+encodeString(addr)); n != 1 {
		t.Fatalf("expected exactly 1 peer section for %s, found %d:\n%s", addr, n, body)
	}
}

// TestAddPeerLinkKeepsOtherPeers ensures pruning is scoped to the same address and
// never removes an unrelated peer.
func TestAddPeerLinkKeepsOtherPeers(t *testing.T) {
	path := writeBaseConfig(t)
	other := PeerCandidate{Name: "node-c", NodeID: "node-c", Address: "100.64.0.9", CmeshCapable: true}
	if _, err := AddPeerLink(path, other, "stable-c", true); err != nil {
		t.Fatalf("link other: %v", err)
	}
	relinked := PeerCandidate{Name: "node-b", NodeID: "node-b", Address: "100.64.0.2", CmeshCapable: true}
	if _, err := AddPeerLink(path, relinked, "stable-b", true); err != nil {
		t.Fatalf("link b: %v", err)
	}
	body := readFile(t, path)
	if !strings.Contains(body, "[peers.node-c]") {
		t.Fatalf("unrelated peer node-c was removed:\n%s", body)
	}
	if !strings.Contains(body, "[peers.node-b]") {
		t.Fatalf("peer node-b missing:\n%s", body)
	}
}

func TestAddPeerLinkRejectsGlobMetachars(t *testing.T) {
	path := writeBaseConfig(t)
	for _, bad := range []string{"node-*", "node-?", "node-[a]"} {
		peer := PeerCandidate{Name: bad, Address: "100.64.0.2"}
		if _, err := AddPeerLink(path, peer, "", true); err == nil {
			t.Fatalf("AddPeerLink(%q) should reject glob metachars", bad)
		}
	}
}

func TestAddPeerLinkIdempotent(t *testing.T) {
	path := writeBaseConfig(t)
	peer := PeerCandidate{Name: "node-b", Address: "100.64.0.2", Online: true, CmeshCapable: true}

	if _, err := AddPeerLink(path, peer, "", true); err != nil {
		t.Fatalf("AddPeerLink first: %v", err)
	}
	first := readFile(t, path)
	if _, err := AddPeerLink(path, peer, "", true); err != nil {
		t.Fatalf("AddPeerLink second: %v", err)
	}
	second := readFile(t, path)
	if first != second {
		t.Fatalf("AddPeerLink not idempotent:\nfirst:\n%s\nsecond:\n%s", first, second)
	}
	if c := strings.Count(second, "[peers.node-b]"); c != 1 {
		t.Fatalf("peers block written %d times, want 1", c)
	}
	cfg, _ := config.Load(path)
	if c := countOccurrences(cfg.Policy.Outbound.AllowedPeers, "node-b"); c != 1 {
		t.Fatalf("outbound allow has node-b %d times, want 1", c)
	}
}

// TestTwoSidedTrustPolicyAccepts is THE BAR: a policy-level, single-process,
// no-tailnet regression guard for J2 (and the missing link_peer/B-QA1 coverage).
// It builds Node A's and Node B's configs exactly the way the wizard does — A links
// B via AddPeerLink (bidirectional grant), B is configured with A as a peer (role
// both on each side) — then asserts that policy.CheckInbound AND CheckOutbound
// ACCEPT the peer in BOTH directions given each side's whois identity, and that a
// third node C is REJECTED both ways on both engines.
func TestTwoSidedTrustPolicyAccepts(t *testing.T) {
	// A shared, real, existing dir for cwd: CheckInbound -> NormalizeCWD ->
	// EvalSymlinks requires the path to exist, and the "/**" form runs through
	// normalizeAllowedRoot so it matches symlinked temp roots (e.g. macOS
	// /var -> /private/var). Use it as both allowed_cwds and intent.CWD.
	cwd := t.TempDir()
	allowedCWDs := []string{cwd + "/**"}

	machineA := Machine{Name: "node-a", NodeID: "stable-a", TailscaleOK: true, TailnetIPs: []string{"100.64.0.1"}}
	machineB := Machine{Name: "node-b", NodeID: "stable-b", TailscaleOK: true, TailnetIPs: []string{"100.64.0.2"}}
	peerA := PeerCandidate{Name: "node-a", Address: "100.64.0.1", Online: true, CmeshCapable: true}
	peerB := PeerCandidate{Name: "node-b", Address: "100.64.0.2", Online: true, CmeshCapable: true}

	// --- Node A: configure (no peer yet), then link B (bidirectional grant). ---
	pathA := filepath.Join(t.TempDir(), "a.toml")
	bodyA, err := GenerateConfig(InitOptions{Role: "both", Runner: "claude-code", AllowedCWDs: allowedCWDs}, machineA, nil, nil)
	if err != nil {
		t.Fatalf("GenerateConfig A: %v", err)
	}
	if err := WriteConfig(pathA, bodyA, true); err != nil {
		t.Fatalf("WriteConfig A: %v", err)
	}
	if _, err := AddPeerLink(pathA, peerB, "stable-b", true); err != nil {
		t.Fatalf("AddPeerLink B onto A: %v", err)
	}

	// --- Node B: configure with A as a peer (worker|both takes A inbound+outbound). ---
	pathB := filepath.Join(t.TempDir(), "b.toml")
	bodyB, err := GenerateConfig(InitOptions{Role: "both", Runner: "claude-code", AllowedCWDs: allowedCWDs, PeerNames: []string{"node-a"}}, machineB, nil, []PeerCandidate{peerA})
	if err != nil {
		t.Fatalf("GenerateConfig B: %v", err)
	}
	if err := WriteConfig(pathB, bodyB, true); err != nil {
		t.Fatalf("WriteConfig B: %v", err)
	}

	cfgA, err := config.Load(pathA)
	if err != nil {
		t.Fatalf("load A: %v", err)
	}
	cfgB, err := config.Load(pathB)
	if err != nil {
		t.Fatalf("load B: %v", err)
	}
	engineA := policy.New(cfgA)
	engineB := policy.New(cfgB)

	// Intent describing "the other node B" as A would see it (whois identity node-b).
	fromB := policy.TaskIntent{PeerName: "node-b", PeerStableID: "stable-b", CWD: cwd, Profile: "dev"}
	// Intent describing "the other node A" as B would see it (whois identity node-a).
	fromA := policy.TaskIntent{PeerName: "node-a", PeerStableID: "stable-a", CWD: cwd, Profile: "dev"}
	// A third, unrelated node C — must be rejected everywhere.
	fromC := policy.TaskIntent{PeerName: "node-c", PeerStableID: "stable-c", CWD: cwd, Profile: "dev"}

	mustAllow := func(label string, d policy.Decision) {
		t.Helper()
		if !d.Allowed {
			t.Fatalf("%s should be ALLOWED, got denied: %s %s", label, d.Code, d.Detail)
		}
	}
	mustDeny := func(label string, d policy.Decision) {
		t.Helper()
		if d.Allowed {
			t.Fatalf("%s should be DENIED, but was allowed", label)
		}
	}

	// Both directions accepted on both engines.
	mustAllow("A inbound from B", engineA.CheckInbound(fromB)) // B -> A (closes the handshake)
	mustAllow("A outbound to B", engineA.CheckOutbound(fromB)) // A -> B
	mustAllow("B inbound from A", engineB.CheckInbound(fromA)) // A -> B
	mustAllow("B outbound to A", engineB.CheckOutbound(fromA)) // B -> A

	// Third node C rejected both ways on both engines.
	mustDeny("A inbound from C", engineA.CheckInbound(fromC))
	mustDeny("A outbound to C", engineA.CheckOutbound(fromC))
	mustDeny("B inbound from C", engineB.CheckInbound(fromC))
	mustDeny("B outbound to C", engineB.CheckOutbound(fromC))
}

func writeBaseConfig(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	body := "[node]\nid = \"node-a\"\nname = \"node-a\"\nmesh_port = 7778\nmcp_port = 7777\n\n" +
		"[policy]\nallowed_peers = [\"node-a\"]\nallowed_cwds = [\"~/code/**\"]\nhop_limit = 2\n\n" +
		"[policy.outbound]\nallowed_peers = []\ndenied_profiles = [\"network\"]\nallow_subdelegation = false\n\n" +
		"[profiles.dev]\nrunner = \"claude-code\"\nclaims = [\"read_files\", \"edit_repo\", \"execute_repo_commands\"]\nbare = false\nnetwork = false\ngit_push = false\nmax_runtime = \"2h\"\n\n" +
		"[runners.claude-code]\ncommand = [\"/usr/local/bin/claude\", \"-p\"]\ndefault_profile = \"dev\"\nconcurrency = 1\n\n" +
		"[gateway]\nhost_token = \"existing-token\"\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func contains(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}

func countOccurrences(values []string, want string) int {
	n := 0
	for _, v := range values {
		if v == want {
			n++
		}
	}
	return n
}
