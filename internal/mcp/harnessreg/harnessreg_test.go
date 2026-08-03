package harnessreg

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cmesh/internal/mcp/setupinstall"
)

// An unconfirmed call must change nothing and must never echo the host token,
// on either harness. The token is written into harness config on confirm, but
// the preview a model sees is always redacted.
func TestPreviewNeverEchoesHostToken(t *testing.T) {
	const secret = "super-secret-host-token"
	for _, harness := range []string{"claude-code", "codex"} {
		t.Run(harness, func(t *testing.T) {
			out := Register(Request{Harness: harness}, "http://127.0.0.1:7777/mcp", secret)
			if !out.RequiresConfirmation {
				t.Fatalf("unconfirmed call must require confirmation: %+v", out)
			}
			for key, value := range out.Fields {
				text, ok := value.(string)
				if !ok {
					continue
				}
				if strings.Contains(text, secret) {
					t.Fatalf("field %q leaked the host token: %s", key, text)
				}
			}
		})
	}
}

// An unsupported harness degrades to manual instructions rather than erroring,
// so a caller on an unrecognized agent is never stranded without a next step.
func TestUnsupportedHarnessDegradesToManual(t *testing.T) {
	out := Register(Request{Harness: "some-new-agent", Confirmed: true}, "http://127.0.0.1:7777/mcp", "")
	if out.Status != "manual" {
		t.Fatalf("status = %q, want manual: %+v", out.Status, out)
	}
	if out.Fields["manual_url"] != "http://127.0.0.1:7777/mcp" {
		t.Fatalf("manual_url missing: %+v", out.Fields)
	}
}

// The Codex writer replaces its own table without disturbing neighbours, and
// writes 0600 because the file now holds the gateway host token.
func TestWriteCodexGatewayConfigReplacesOnlyItsOwnTable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	existing := "[mcp_servers.other]\ncommand = \"other\"\n\n" +
		"[mcp_servers." + setupinstall.ServerName + "]\ncommand = \"stale\"\n\n" +
		"[unrelated]\nkey = \"value\"\n"
	if err := os.WriteFile(path, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := WriteCodexGatewayConfig(path, "http://127.0.0.1:7777/mcp", "tok"); err != nil {
		t.Fatalf("WriteCodexGatewayConfig: %v", err)
	}

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(body)
	if strings.Contains(got, `command = "stale"`) {
		t.Fatalf("stale cmesh entry survived:\n%s", got)
	}
	for _, want := range []string{"[mcp_servers.other]", "[unrelated]", "url = 'http://127.0.0.1:7777/mcp'", "X-Cmesh-Host-Token"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in:\n%s", want, got)
		}
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("config permissions = %o, want 600 (file holds the host token)", perm)
	}
}

// A config with no host token must not write an empty header line.
func TestWriteCodexGatewayConfigOmitsEmptyToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := WriteCodexGatewayConfig(path, "http://127.0.0.1:7777/mcp", "  "); err != nil {
		t.Fatalf("WriteCodexGatewayConfig: %v", err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "http_headers") {
		t.Fatalf("blank token should not write a header:\n%s", body)
	}
}

func TestGatewayURLDefaultsToConfiguredPort(t *testing.T) {
	if got := GatewayURL(9999); got != "http://127.0.0.1:9999/mcp" {
		t.Fatalf("GatewayURL(9999) = %q", got)
	}
	if got := GatewayURL(0); got != "http://127.0.0.1:7777/mcp" {
		t.Fatalf("GatewayURL(0) = %q, want the default MCP port", got)
	}
}
