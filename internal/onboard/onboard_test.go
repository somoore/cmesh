package onboard

import (
	"strings"
	"testing"

	"cmesh/internal/config"
)

func TestGenerateConfigProducesValidOpinionatedDefaults(t *testing.T) {
	body, err := GenerateConfig(InitOptions{
		Role:         "worker",
		AllowedPeers: []string{"mac-studio"},
		AllowedCWDs:  []string{"~/code/**"},
		Runner:       "codex",
	}, Machine{Name: "homelab-vm", NodeID: "homelab-vm"}, []RunnerStatus{
		{Name: "codex", Command: []string{"codex"}, Found: true},
	}, nil)
	if err != nil {
		t.Fatalf("GenerateConfig: %v", err)
	}
	if !strings.Contains(body, `[profiles.readonly]`) || !strings.Contains(body, `[profiles.dev]`) {
		t.Fatalf("expected readonly and dev profiles:\n%s", body)
	}
	path := t.TempDir() + "/config.toml"
	if err := WriteConfig(path, body, false); err != nil {
		t.Fatalf("WriteConfig: %v", err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("generated config should load: %v\n%s", err, body)
	}
	if cfg.Node.Name != "homelab-vm" {
		t.Fatalf("node name = %q", cfg.Node.Name)
	}
	if cfg.Profiles["dev"].Runner != "codex" {
		t.Fatalf("dev runner = %q", cfg.Profiles["dev"].Runner)
	}
}

func TestGenerateConfigSelectsCmeshCapablePeers(t *testing.T) {
	body, err := GenerateConfig(InitOptions{
		Role:        "origin",
		Runner:      "claude-code",
		AllowedCWDs: []string{"~/code/**"},
	}, Machine{Name: "mac-studio"}, []RunnerStatus{
		{Name: "claude-code", Command: []string{"claude", "-p"}, Found: true},
	}, []PeerCandidate{
		{Name: "homelab-vm", NodeID: "node-1", Address: "100.64.10.20", CmeshCapable: true},
		{Name: "printer", NodeID: "node-2", Address: "100.64.10.30"},
	})
	if err != nil {
		t.Fatalf("GenerateConfig: %v", err)
	}
	if !strings.Contains(body, `[peers.homelab-vm]`) {
		t.Fatalf("expected cmesh-capable peer in config:\n%s", body)
	}
	if strings.Contains(body, `[peers.printer]`) {
		t.Fatalf("unexpected non-cmesh peer in config:\n%s", body)
	}
}
