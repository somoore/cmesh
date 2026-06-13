package policy

import (
	"os"
	"path/filepath"
	"testing"

	"cmesh/internal/config"
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
