package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestValidateNodeRole locks in that node.role is optional: empty and the three
// valid values load, a clearly-invalid non-empty value is rejected, and existing
// configs (no role at all) keep working.
func TestValidateNodeRole(t *testing.T) {
	cases := []struct {
		name    string
		role    string
		wantErr bool
	}{
		{"omitted", "", false},
		{"origin", "origin", false},
		{"worker", "worker", false},
		{"both", "both", false},
		{"invalid", "supervisor", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeRoleConfig(t, tc.role)
			_, err := Load(path)
			if tc.wantErr && err == nil {
				t.Fatalf("role %q should be rejected by Validate", tc.role)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("role %q should load: %v", tc.role, err)
			}
		})
	}
}

// TestValidateDefaultProfileMustBeDefined locks in the guard that catches the
// onboarding bug at the validate_config layer: a runner whose default_profile
// names a profile the config never defines must be rejected, instead of loading
// clean and only failing later at preflight with "profile_unavailable".
func TestValidateDefaultProfileMustBeDefined(t *testing.T) {
	cases := []struct {
		name           string
		profileName    string // the [profiles.<x>] table that is defined
		defaultProfile string // runners.claude-code.default_profile
		wantErr        bool
	}{
		{"default names defined profile", "readonly", "readonly", false},
		{"default names undefined profile", "readonly", "dev", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := "[node]\nid = \"node-a\"\nname = \"node-a\"\n" +
				"mesh_port = 7778\nmcp_port = 7777\n\n" +
				"[policy]\nallowed_peers = [\"node-a\"]\nallowed_cwds = [\"~/code/**\"]\nhop_limit = 2\n\n" +
				"[profiles." + tc.profileName + "]\nrunner = \"claude-code\"\nclaims = [\"read_files\"]\nmax_runtime = \"2h\"\n\n" +
				"[runners.claude-code]\ncommand = [\"/usr/local/bin/claude\", \"-p\"]\ndefault_profile = \"" + tc.defaultProfile + "\"\nconcurrency = 1\n"
			path := filepath.Join(t.TempDir(), "config.toml")
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := Load(path)
			if tc.wantErr && err == nil {
				t.Fatalf("default_profile %q (defined: %q) should be rejected", tc.defaultProfile, tc.profileName)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("default_profile %q (defined: %q) should load: %v", tc.defaultProfile, tc.profileName, err)
			}
		})
	}
}

// TestApplyDefaultsOmittedDefaultProfilePicksDefinedProfile proves a hand-written
// config that omits default_profile loads cleanly: ApplyDefaults must inject a
// profile this config actually defines (preferring dev, then readonly), not the
// bare "dev" that would trip the validation guard on a readonly-only node.
func TestApplyDefaultsOmittedDefaultProfilePicksDefinedProfile(t *testing.T) {
	cases := []struct {
		name        string
		profileName string
		want        string
	}{
		{"readonly only injects readonly", "readonly", "readonly"},
		{"dev present injects dev", "dev", "dev"},
		{"other-only injects that profile", "network", "network"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := "[node]\nid = \"node-a\"\nname = \"node-a\"\n" +
				"mesh_port = 7778\nmcp_port = 7777\n\n" +
				"[policy]\nallowed_peers = [\"node-a\"]\nallowed_cwds = [\"~/code/**\"]\nhop_limit = 2\n\n" +
				"[profiles." + tc.profileName + "]\nrunner = \"claude-code\"\nclaims = [\"read_files\"]\nmax_runtime = \"2h\"\n\n" +
				// default_profile deliberately omitted.
				"[runners.claude-code]\ncommand = [\"/usr/local/bin/claude\", \"-p\"]\nconcurrency = 1\n"
			path := filepath.Join(t.TempDir(), "config.toml")
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg, err := Load(path)
			if err != nil {
				t.Fatalf("config omitting default_profile should load: %v", err)
			}
			if got := cfg.Runners["claude-code"].DefaultProfile; got != tc.want {
				t.Fatalf("injected default_profile = %q, want %q", got, tc.want)
			}
		})
	}
}

// writeRoleConfig writes a minimal valid config with the given role (omitted when
// empty) and returns its path.
func writeRoleConfig(t *testing.T, role string) string {
	t.Helper()
	roleLine := ""
	if role != "" {
		roleLine = "role = \"" + role + "\"\n"
	}
	body := "[node]\nid = \"node-a\"\nname = \"node-a\"\n" + roleLine +
		"mesh_port = 7778\nmcp_port = 7777\n\n" +
		"[policy]\nallowed_peers = [\"node-a\"]\nallowed_cwds = [\"~/code/**\"]\nhop_limit = 2\n\n" +
		"[profiles.dev]\nrunner = \"claude-code\"\nclaims = [\"read_files\"]\nmax_runtime = \"2h\"\n\n" +
		"[runners.claude-code]\ncommand = [\"/usr/local/bin/claude\", \"-p\"]\ndefault_profile = \"dev\"\nconcurrency = 1\n"
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
