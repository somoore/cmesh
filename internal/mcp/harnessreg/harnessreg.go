// Package harnessreg points a coding-agent harness at the local cmesh HTTP
// delegation gateway.
//
// Both MCP surfaces expose this: the stdio setup wizard as register_gateway
// (the last step of onboarding, which swaps the agent off the stdio setup
// server) and the running gateway as register_harness. They previously had
// separate implementations that had drifted — different harness coverage, and
// only one of them removed the stdio registration it was replacing — so the
// behavior a user got depended on which surface their agent happened to be on.
// This package is the single implementation both call.
//
// The host token is written into harness config but never returned to the
// caller: previews are always redacted.
package harnessreg

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"cmesh/internal/config"
	"cmesh/internal/mcp/setupinstall"
)

// Response is a surface-neutral result. Each MCP surface maps it into its own
// response envelope.
type Response struct {
	// RequiresConfirmation reports that nothing was written and the caller must
	// call again with Confirmed set.
	RequiresConfirmation bool
	// Status is "registered" or "manual" once confirmed.
	Status string
	// Fields carries the surface-visible payload (previews, paths, next steps).
	Fields map[string]any
}

// Request is a registration request.
type Request struct {
	Harness   string
	Confirmed bool
	// SuppressStdout wraps any child process whose stdout would otherwise
	// corrupt the caller's channel. The stdio setup server speaks JSON-RPC on
	// os.Stdout, so it passes a redirector here; the HTTP gateway passes nil.
	SuppressStdout func(func())
}

func (r Request) suppress(fn func()) {
	if r.SuppressStdout != nil {
		r.SuppressStdout(fn)
		return
	}
	fn()
}

// GatewayURL is the loopback MCP endpoint for a configured MCP port.
func GatewayURL(mcpPort int) string {
	if mcpPort == 0 {
		mcpPort = config.DefaultMCPPort
	}
	return fmt.Sprintf("http://127.0.0.1:%d/mcp", mcpPort)
}

// Register points the named harness at gatewayURL. An unknown or empty harness
// defaults to claude-code, matching the historical gateway behavior.
func Register(req Request, gatewayURL, hostToken string) Response {
	switch strings.ToLower(strings.TrimSpace(req.Harness)) {
	case "", "claude-code", "claude":
		return registerClaude(req, gatewayURL, hostToken)
	case "codex":
		return registerCodex(req, gatewayURL, hostToken)
	default:
		return Response{Status: "manual", Fields: map[string]any{
			"harness":     req.Harness,
			"manual_url":  gatewayURL,
			"detail":      "unsupported harness; add a local HTTP MCP server named cmesh at manual_url",
			"gateway_url": gatewayURL,
		}}
	}
}

func registerClaude(req Request, gatewayURL, hostToken string) Response {
	addArgs := setupinstall.ClaudeHTTPAddArgs(setupinstall.ServerName, gatewayURL, hostToken)
	preview := setupinstall.ShellCommand(append([]string{"claude"}, setupinstall.RedactClaudeHTTPAddArgs(addArgs)...))
	if !req.Confirmed {
		return Response{RequiresConfirmation: true, Fields: map[string]any{
			"message":             "Call again with confirmed=true to point Claude Code at the cmesh HTTP gateway. You will restart the agent session once afterward.",
			"preview":             preview,
			"claude_code_command": preview,
			"claude_code_headers": strings.TrimSpace(hostToken) != "",
			"gateway_url":         gatewayURL,
		}}
	}
	claude, err := exec.LookPath("claude")
	if err != nil {
		return manual(gatewayURL, "Claude Code not found on PATH; register manually.", map[string]any{
			"manual_command": preview,
			"next_step":      "Run the command above, then restart your agent session.",
		})
	}
	// Replace any existing stdio registration first, then add the HTTP gateway.
	// Skipping this leaves the stdio setup server shadowing the gateway, which
	// is the failure the install-time verification exists to catch.
	req.suppress(func() {
		_ = exec.Command(claude, "mcp", "remove", "--scope", "user", setupinstall.ServerName).Run()
	})
	var out []byte
	var runErr error
	req.suppress(func() {
		out, runErr = exec.Command(claude, addArgs...).CombinedOutput()
	})
	if runErr != nil {
		detail := strings.TrimSpace(string(out))
		if detail == "" {
			detail = runErr.Error()
		}
		return manual(gatewayURL, "automatic registration failed; register manually: "+detail, map[string]any{
			"manual_command": preview,
		})
	}
	return Response{Status: "registered", Fields: map[string]any{
		"harness":     "claude-code",
		"gateway_url": gatewayURL,
		"url":         gatewayURL,
		"next_step":   "Restart your agent session once to load the cmesh HTTP gateway MCP.",
	}}
}

func registerCodex(req Request, gatewayURL, hostToken string) Response {
	path := setupinstall.CodexConfigPath()
	// Preview mirrors the config WriteCodexGatewayConfig produces, with the host
	// token redacted. It never echoes the token.
	preview := fmt.Sprintf(
		"[mcp_servers.%s]\nurl = %s\nhttp_headers = { \"X-Cmesh-Host-Token\" = '<redacted; written to %s>' }\nstartup_timeout_sec = 120",
		setupinstall.ServerName, tomlString(gatewayURL), path)
	if !req.Confirmed {
		return Response{RequiresConfirmation: true, Fields: map[string]any{
			"message":     "Codex HTTP MCP registration varies by CLI version. Call again with confirmed=true to write the Codex config entry; the host token is written to the file but never shown.",
			"preview":     preview,
			"config_path": path,
			"gateway_url": gatewayURL,
		}}
	}
	if err := WriteCodexGatewayConfig(path, gatewayURL, hostToken); err != nil {
		return manual(gatewayURL, "could not write Codex config automatically: "+err.Error(), map[string]any{
			"next_step": "Add a local HTTP MCP server named cmesh at the gateway_url manually (Codex registration varies by CLI version).",
		})
	}
	return Response{Status: "registered", Fields: map[string]any{
		"harness":     "codex",
		"config_path": path,
		"gateway_url": gatewayURL,
		"url":         gatewayURL,
		"next_step":   "Restart your agent session once to load the cmesh HTTP gateway MCP.",
	}}
}

// manual builds a degraded result. Registration failures report "manual" with a
// runnable command rather than erroring: the user can always finish by hand,
// and failing the tool would strand onboarding at its last step.
func manual(gatewayURL, detail string, extra map[string]any) Response {
	fields := map[string]any{"detail": detail, "gateway_url": gatewayURL}
	for k, v := range extra {
		fields[k] = v
	}
	return Response{Status: "manual", Fields: fields}
}

// WriteCodexGatewayConfig writes or replaces the [mcp_servers.cmesh] HTTP table
// in the Codex config (0600), embedding the host token as a request header. The
// file is written directly; the token is never returned to the caller.
func WriteCodexGatewayConfig(path, gatewayURL, hostToken string) error {
	if path == "" {
		path = setupinstall.CodexConfigPath()
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	body := ""
	if existing, err := os.ReadFile(path); err == nil {
		body = string(existing)
	} else if !os.IsNotExist(err) {
		return err
	}
	body = strings.TrimRight(stripCodexTable(body, "mcp_servers."+setupinstall.ServerName), "\r\n")
	if body != "" {
		body += "\n\n"
	}
	body += "[mcp_servers." + setupinstall.ServerName + "]\n"
	body += "url = " + tomlString(gatewayURL) + "\n"
	if strings.TrimSpace(hostToken) != "" {
		body += "http_headers = { \"X-Cmesh-Host-Token\" = " + tomlString(strings.TrimSpace(hostToken)) + " }\n"
	}
	body += "startup_timeout_sec = 120\n"
	return os.WriteFile(path, []byte(body), 0o600)
}

// stripCodexTable removes a top-level TOML table (and its subtables) from body.
func stripCodexTable(body, table string) string {
	var kept []string
	skip := false
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			name := strings.Trim(trimmed, "[]")
			skip = name == table || strings.HasPrefix(name, table+".")
		}
		if !skip {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n")
}

func tomlString(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}
