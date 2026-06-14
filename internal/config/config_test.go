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
