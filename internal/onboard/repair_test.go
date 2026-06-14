package onboard

import (
	"context"
	"strings"
	"testing"

	"cmesh/internal/config"
)

// withFakeTailnet swaps the DiagnosePeers injection seams for a fake tailnet (address
// -> whois DNS label) and a fake /v1/hello (address -> name, stableID), restoring
// them after the test. This lets the diagnose->apply->diagnose round-trip run with no
// real `tailscale status` or reachable peer.
func withFakeTailnet(t *testing.T, whoisByAddr map[string]string, helloByAddr map[string][2]string) {
	t.Helper()
	origTailnet, origIdentity := diagnoseTailnetFn, diagnoseIdentityFn
	t.Cleanup(func() { diagnoseTailnetFn, diagnoseIdentityFn = origTailnet, origIdentity })
	diagnoseTailnetFn = func(_ context.Context, _ int, _ bool) ([]PeerCandidate, error) {
		var peers []PeerCandidate
		for addr, name := range whoisByAddr {
			peers = append(peers, PeerCandidate{Name: name, Address: addr})
		}
		return peers, nil
	}
	diagnoseIdentityFn = func(_ context.Context, addr string, _ int) (string, string, error) {
		if h, ok := helloByAddr[addr]; ok {
			return h[0], h[1], nil
		}
		return "", "", context.DeadlineExceeded
	}
}

// TestDiagnoseApplyConverges is the real convergence invariant: a stale peer entry
// must be diagnosed fixable, applied, and then diagnose-clean (zero fixable) on the
// next pass — never a perpetual-fixable loop. Run against a fake tailnet so the whole
// round-trip (not just the helpers) is exercised.
func TestDiagnoseApplyConverges(t *testing.T) {
	const addr = "100.120.229.49"
	whois := map[string]string{addr: "scotts-macbook-pro"}
	hello := map[string][2]string{addr: {"scotts-macbook-pro", "nduXyDZMbU11CNTRL"}}
	withFakeTailnet(t, whois, hello)

	// Seed a stale display-name entry for that address (the win11 shape).
	path := writeBaseConfig(t)
	stale := PeerCandidate{Name: "Scott's MacBook Pro", NodeID: "Scott's MacBook Pro", Address: addr}
	if _, err := AddPeerLink(path, stale, "", true); err != nil {
		t.Fatalf("seed: %v", err)
	}

	ctx := context.Background()
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	repairs, err := DiagnosePeers(ctx, cfg)
	if err != nil {
		t.Fatalf("diagnose: %v", err)
	}
	if CountRepairs(repairs, RepairFixable) == 0 {
		t.Fatalf("stale entry should be fixable on first pass: %+v", repairs)
	}
	if _, err := ApplyPeerRepairs(path, repairs); err != nil {
		t.Fatalf("apply: %v", err)
	}

	// Second pass must converge: zero fixable.
	cfg2, err := config.Load(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	repairs2, err := DiagnosePeers(ctx, cfg2)
	if err != nil {
		t.Fatalf("diagnose 2: %v", err)
	}
	if n := CountRepairs(repairs2, RepairFixable); n != 0 {
		t.Fatalf("did not converge: %d still fixable after apply: %+v", n, repairs2)
	}
}

// TestDiagnoseConflictLeftUnchanged: when the peer's /v1/hello name disagrees with
// its whois identity, the entry is a conflict and is never auto-fixed.
func TestDiagnoseConflictLeftUnchanged(t *testing.T) {
	const addr = "100.64.0.2"
	withFakeTailnet(t,
		map[string]string{addr: "real-whois-name"},
		map[string][2]string{addr: {"impostor-self-report", "stable-x"}},
	)
	cfg := repairTestConfig("both", []string{"node-a"}, nil)
	cfg.Peers = map[string]config.PeerConfig{
		"some-peer": {Name: "some-peer", NodeID: "some-peer", Address: addr, Port: 7778},
	}
	repairs, err := DiagnosePeers(context.Background(), cfg)
	if err != nil {
		t.Fatalf("diagnose: %v", err)
	}
	if len(repairs) != 1 || repairs[0].Status != RepairConflict {
		t.Fatalf("expected one conflict, got %+v", repairs)
	}
}

// repairTestConfig builds a minimal config with one peer entry and configurable
// node role + allowlist membership, for exercising the pure repair decision logic
// (peerEntryConsistent / peerHasInbound / ApplyPeerRepairs) without a live tailnet.
func repairTestConfig(role string, inbound, outbound []string) *config.Config {
	return &config.Config{
		Node: config.NodeConfig{ID: "node-a", Name: "node-a", Role: role, MeshPort: 7778, MCPPort: 7777},
		Policy: config.PolicyConfig{
			AllowedPeers: inbound,
			AllowedCWDs:  []string{"~/code/**"},
			HopLimit:     2,
			Outbound:     config.OutboundPolicy{AllowedPeers: outbound},
		},
		Profiles: map[string]config.ProfileConfig{"dev": {Runner: "claude-code"}},
		Runners:  map[string]config.RunnerConfig{"claude-code": {Command: []string{"claude"}, DefaultProfile: "dev"}},
	}
}

// TestPeerEntryConsistentOutboundOnly covers the decision helper underpinning the
// convergence fix: on a worker node, a peer that is outbound-only (in
// outbound.allowed_peers but NOT inbound) with a matchable identity must be judged
// consistent (WantInbound=false), not fixable. The full diagnose->apply->diagnose
// convergence is asserted by TestDiagnoseApplyConverges.
func TestPeerEntryConsistentOutboundOnly(t *testing.T) {
	whois, stable := "node-b", "stable-b"
	// Worker node; peer is outbound-only (no inbound grant) but already matchable.
	cfg := repairTestConfig("both", []string{"node-a"}, []string{whois, stable})

	wantInbound := peerHasInbound(cfg, whois, whois, stable) // false: not in inbound list
	if wantInbound {
		t.Fatal("precondition: outbound-only peer must not report inbound")
	}
	// With WantInbound=false, a matchable identity is consistent — NOT fixable.
	if !peerEntryConsistent(cfg, whois, whois, stable, wantInbound) {
		t.Fatal("outbound-only matchable peer should be consistent, not fixable (convergence bug)")
	}
}

// TestPeerEntryConsistentRequiresInboundWhenGranted: a peer that DOES hold inbound
// trust must have the matchable identity present in the inbound allowlist to be
// considered consistent.
func TestPeerEntryConsistentRequiresInboundWhenGranted(t *testing.T) {
	whois, stable := "node-b", "stable-b"
	// Inbound holds only a STALE identity, not the whois one → inconsistent.
	cfg := repairTestConfig("both", []string{"node-a", "Stale Name"}, []string{whois})
	wantInbound := peerHasInbound(cfg, "Stale Name", whois, stable)
	if !wantInbound {
		t.Fatal("peer with a stale inbound entry should report inbound posture")
	}
	if peerEntryConsistent(cfg, "Stale Name", whois, stable, wantInbound) {
		t.Fatal("a peer granted inbound but missing its whois identity inbound must be fixable")
	}
	// Once the whois identity is present inbound, it is consistent.
	cfg2 := repairTestConfig("both", []string{"node-a", whois}, []string{whois})
	if !peerEntryConsistent(cfg2, whois, whois, stable, true) {
		t.Fatal("whois identity present inbound should be consistent")
	}
}

// TestApplyPeerRepairsPreservesOriginPosture: an origin-only node must never gain an
// inbound grant from a repair, even for a fixable entry.
func TestApplyPeerRepairsPreservesOriginPosture(t *testing.T) {
	path := writeBaseConfig(t)
	// Pretend the diagnosis marked one peer fixable, WantInbound=false (origin-only).
	repairs := []PeerRepair{{
		Section:       "node-b",
		Address:       "100.64.0.2",
		WhoisIdentity: "node-b",
		StableID:      "stable-b",
		Status:        RepairFixable,
		WantInbound:   false,
	}}
	if _, err := ApplyPeerRepairs(path, repairs); err != nil {
		t.Fatalf("ApplyPeerRepairs: %v", err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if !contains(cfg.Policy.Outbound.AllowedPeers, "node-b") {
		t.Fatalf("outbound grant expected: %v", cfg.Policy.Outbound.AllowedPeers)
	}
	if contains(cfg.Policy.AllowedPeers, "node-b") {
		t.Fatalf("origin posture violated: inbound grant added during repair: %v", cfg.Policy.AllowedPeers)
	}
}

// TestApplyPeerRepairsWritesMatchableIdentity: a fixable entry with WantInbound=true
// gets the whois identity + stable id into both allowlists and a correct section.
func TestApplyPeerRepairsWritesMatchableIdentity(t *testing.T) {
	path := writeBaseConfig(t)
	repairs := []PeerRepair{{
		Section:       "node-b",
		Address:       "100.64.0.2",
		WhoisIdentity: "node-b",
		StableID:      "stable-b",
		Status:        RepairFixable,
		WantInbound:   true,
	}}
	fixed, err := ApplyPeerRepairs(path, repairs)
	if err != nil {
		t.Fatalf("ApplyPeerRepairs: %v", err)
	}
	if len(fixed) != 1 || fixed[0] != "node-b" {
		t.Fatalf("fixed = %v, want [node-b]", fixed)
	}
	body := readFile(t, path)
	if !strings.Contains(body, "[peers.node-b]") {
		t.Fatalf("missing peer section:\n%s", body)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	for _, want := range []string{"node-b", "stable-b"} {
		if !contains(cfg.Policy.AllowedPeers, want) {
			t.Fatalf("inbound allowlist missing %q: %v", want, cfg.Policy.AllowedPeers)
		}
		if !contains(cfg.Policy.Outbound.AllowedPeers, want) {
			t.Fatalf("outbound allowlist missing %q: %v", want, cfg.Policy.Outbound.AllowedPeers)
		}
	}
}

// TestApplyPeerRepairsSkipsNonFixable: conflict and unreachable repairs are not acted
// on.
func TestApplyPeerRepairsSkipsNonFixable(t *testing.T) {
	path := writeBaseConfig(t)
	repairs := []PeerRepair{
		{Section: "node-b", Address: "100.64.0.2", WhoisIdentity: "node-b", Status: RepairConflict, WantInbound: true},
		{Section: "node-c", Address: "100.64.0.3", Status: RepairUnreachable},
		{Section: "node-d", Address: "100.64.0.4", WhoisIdentity: "node-d", Status: RepairOK},
	}
	fixed, err := ApplyPeerRepairs(path, repairs)
	if err != nil {
		t.Fatalf("ApplyPeerRepairs: %v", err)
	}
	if len(fixed) != 0 {
		t.Fatalf("expected no writes for non-fixable repairs, got %v", fixed)
	}
	body := readFile(t, path)
	if strings.Contains(body, "[peers.node-b]") || strings.Contains(body, "[peers.node-c]") {
		t.Fatalf("non-fixable peer was written:\n%s", body)
	}
}
