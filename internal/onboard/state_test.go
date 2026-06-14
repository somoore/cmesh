package onboard

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cmesh/internal/config"
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
			Node:  config.NodeConfig{MCPPort: config.DefaultMCPPort},
			Peers: map[string]config.PeerConfig{"node-b": {Name: "node-b", Address: "100.64.0.2"}},
		}, nil
	}
	cfgMissing := func(string) (*config.Config, error) { return nil, os.ErrNotExist }

	tests := []struct {
		name    string
		machine Machine
		peers   []PeerCandidate
		health  func(context.Context, int) bool
		load    func(string) (*config.Config, error)
		want    OnboardingState
	}{
		{"tailscale_down", downMachine, nil, healthy, cfgPresent, StateTailscaleDown},
		{"tailscale_solo", upMachine, nil, healthy, cfgMissing, StateTailscaleSolo},
		{"needs_config", upMachine, []PeerCandidate{onlinePeer}, healthy, cfgMissing, StateNeedsConfig},
		{"daemon_down", upMachine, []PeerCandidate{onlinePeer}, unhealthy, cfgPresent, StateDaemonDown},
		{"no_cmesh_peer", upMachine, []PeerCandidate{onlinePeer}, healthy, cfgPresent, StateNoCmeshPeer},
		{"peer_untrusted", upMachine, []PeerCandidate{cmeshPeer}, healthy, cfgPresent, StatePeerUntrusted},
		{"ready", upMachine, []PeerCandidate{cmeshPeer}, healthy, cfgLinked, StateReady},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			restore := swapStateFakes(tt.machine, tt.peers, tt.load)
			defer restore()
			got := DeriveOnboardingState(context.Background(), "/tmp/does-not-matter.toml", tt.health)
			if got.State != tt.want {
				t.Fatalf("state = %q, want %q (detail: %s)", got.State, tt.want, got.Detail)
			}
			if got.NextStep == "" {
				t.Fatalf("state %q has empty next_step", got.State)
			}
		})
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

	name, err := AddPeerLink(path, peer, "nodeB-stable-id")
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
	// outbound allow must contain the matchable identity AND the captured stable id;
	// inbound must be untouched.
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
	if contains(cfg.Policy.AllowedPeers, "Node-B") {
		t.Fatalf("inbound allowed_peers was widened: %v", cfg.Policy.AllowedPeers)
	}
}

func TestAddPeerLinkRejectsGlobMetachars(t *testing.T) {
	path := writeBaseConfig(t)
	for _, bad := range []string{"node-*", "node-?", "node-[a]"} {
		peer := PeerCandidate{Name: bad, Address: "100.64.0.2"}
		if _, err := AddPeerLink(path, peer, ""); err == nil {
			t.Fatalf("AddPeerLink(%q) should reject glob metachars", bad)
		}
	}
}

func TestAddPeerLinkIdempotent(t *testing.T) {
	path := writeBaseConfig(t)
	peer := PeerCandidate{Name: "node-b", Address: "100.64.0.2", Online: true, CmeshCapable: true}

	if _, err := AddPeerLink(path, peer, ""); err != nil {
		t.Fatalf("AddPeerLink first: %v", err)
	}
	first := readFile(t, path)
	if _, err := AddPeerLink(path, peer, ""); err != nil {
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
