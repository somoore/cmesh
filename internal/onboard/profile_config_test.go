package onboard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cmesh/internal/config"
)

func TestConfigureProfileRepairsBrokenRepoDevProfile(t *testing.T) {
	configPath := writeProfileConfigFixture(t, `[node]
id = "origin"
name = "origin"
role = "origin"
mesh_port = 7778
mcp_port = 7777
data_dir = "data"

[policy]
allowed_peers = ["worker"]
allowed_cwds = ["/tmp"]

[profiles.query]
runner = "probe"
capabilities = ["disk_info"]
writes = "deny"
shell = "deny"

[profiles.repo-dev]
runner = ""
allowed_peers = ["other"]
writes = "deny"
shell = "deny"

[runners.probe]
command = ["builtin"]
default_profile = "query"

[peers.worker]
name = "worker"
node_id = "worker-node"
address = "127.0.0.1"
port = 7778
`)

	result, err := ConfigureProfile(configPath, ProfileConfigureOptions{
		Template:     "repo-dev",
		Runner:       "codex",
		AllowedPeers: []string{"worker"},
	}, true)
	if err != nil {
		t.Fatalf("ConfigureProfile: %v", err)
	}
	if !result.ProfileRepaired || !result.RunnerAdded {
		t.Fatalf("repair result = %+v", result)
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("load repaired config: %v", err)
	}
	prof := cfg.Profiles["repo-dev"]
	if prof.Runner != "codex" || !containsTestString(prof.AllowedPeers, "worker") || !prof.RequireLease || prof.Writes != "repo_only" {
		t.Fatalf("repaired repo-dev = %+v", prof)
	}
	body, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if strings.Count(string(body), "[profiles.repo-dev]") != 1 {
		t.Fatalf("duplicate repo-dev section after repair:\n%s", body)
	}
}

func TestConfigureProfileForceReplacesWithoutDuplicateSections(t *testing.T) {
	configPath := writeProfileConfigFixture(t, validQueryConfig(t))
	if _, err := ConfigureProfile(configPath, ProfileConfigureOptions{Template: "repo-dev", Runner: "codex", AllowedPeers: []string{"worker"}}, true); err != nil {
		t.Fatalf("initial ConfigureProfile: %v", err)
	}
	if _, err := ConfigureProfile(configPath, ProfileConfigureOptions{Template: "repo-dev", Runner: "codex", AllowedPeers: []string{"worker"}, Force: true}, true); err != nil {
		t.Fatalf("force ConfigureProfile: %v", err)
	}
	if _, err := config.Load(configPath); err != nil {
		t.Fatalf("load force config: %v", err)
	}
	body, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	for _, section := range []string{"[profiles.repo-dev]", "[runners.codex]"} {
		if got := strings.Count(string(body), section); got != 1 {
			t.Fatalf("%s count = %d in:\n%s", section, got, body)
		}
	}
}

func TestConfigureProfileDefaultsToExistingCodexRunner(t *testing.T) {
	configPath := writeProfileConfigFixture(t, validQueryConfig(t)+`
[runners.codex]
command = ["/opt/codex/bin/codex"]
`)
	result, err := ConfigureProfile(configPath, ProfileConfigureOptions{Template: "repo-dev", AllowedPeers: []string{"worker"}}, false)
	if err != nil {
		t.Fatalf("ConfigureProfile preview: %v", err)
	}
	if result.Runner != "codex" {
		t.Fatalf("runner = %q, want codex", result.Runner)
	}
	if strings.Contains(result.PreviewTOML, "[runners.codex]") {
		t.Fatalf("preview should not duplicate existing codex runner:\n%s", result.PreviewTOML)
	}
}

func writeProfileConfigFixture(t *testing.T, body string) string {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, "config.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func validQueryConfig(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	return `[node]
id = "origin"
name = "origin"
role = "origin"
mesh_port = 7778
mcp_port = 7777
data_dir = "` + filepath.ToSlash(filepath.Join(root, "data")) + `"

[policy]
allowed_peers = ["worker"]
allowed_cwds = ["` + filepath.ToSlash(root) + `"]

[profiles.query]
runner = "probe"
capabilities = ["disk_info"]
writes = "deny"
shell = "deny"

[runners.probe]
command = ["builtin"]
default_profile = "query"

[peers.worker]
name = "worker"
node_id = "worker-node"
address = "127.0.0.1"
port = 7778
`
}

func containsTestString(values []string, needle string) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
}
