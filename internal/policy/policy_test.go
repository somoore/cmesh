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
