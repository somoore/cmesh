package setupstdio

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"cmesh/internal/config"
	"cmesh/internal/mcp/setupinstall"
	meshrpc "cmesh/internal/mesh/rpc"
	"cmesh/internal/mesh/schema"
	"cmesh/internal/onboard"
	"cmesh/internal/service"
)

// Indirection seams so the C1 stdout test (and others) can inject fakes without a
// real launchd/systemd. Production wiring points at the service package.
var (
	installServiceFn = service.Install
	startServiceFn   = service.Start
	// healthPollBudget bounds how long start_service waits for the gateway to come
	// up before reporting. Overridable in tests to avoid a real 10s wait.
	healthPollBudget = 10 * time.Second
)

// gatewayHealthy reports whether the local cmesh gateway answers GET /health.
func gatewayHealthy(ctx context.Context, mcpPort int) bool {
	if mcpPort == 0 {
		mcpPort = config.DefaultMCPPort
	}
	ctx, cancel := context.WithTimeout(ctx, 800*time.Millisecond)
	defer cancel()
	url := fmt.Sprintf("http://127.0.0.1:%d/health", mcpPort)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode >= 200 && resp.StatusCode <= 299
}

// onboardingStatus re-derives the wizard state from the live environment.
func onboardingStatus(ctx context.Context) ToolResponse {
	status := onboard.DeriveOnboardingState(ctx, config.DefaultPath(), gatewayHealthy)
	return ToolResponse{OK: true, Result: status}
}

type configureNodeRequest struct {
	initOptionsRequest
	Confirmed bool `json:"confirmed,omitempty"`
}

// configureNode wraps GenerateConfig+WriteConfig+ValidateConfig. It auto-fills
// role/cwds/peers from detection, resolves an absolute runner command[0], and
// preserves an existing gateway.host_token on re-run.
func configureNode(ctx context.Context, args json.RawMessage) ToolResponse {
	var req configureNodeRequest
	if len(args) > 0 {
		if err := json.Unmarshal(args, &req); err != nil {
			return failure("invalid_arguments", err.Error())
		}
	}
	path := config.DefaultPath()
	opts := req.options()

	// Preserve an existing gateway.host_token: re-running configure_node must not
	// mint a fresh token, or it locks the user out of an already-registered
	// gateway. Detection of an existing config also implies force is required.
	if existing, err := config.Load(path); err == nil {
		opts.HostToken = existing.Gateway.HostToken
	}

	machine := onboard.DetectMachine(ctx)
	runners := onboard.DetectRunners(ctx)
	peers, _ := onboard.TailnetPeers(ctx, config.DefaultMeshPort, true)
	body, genErr := onboard.GenerateConfig(opts, machine, runners, peers)

	if !req.Confirmed {
		result := map[string]any{
			"requires_confirmation": true,
			"message":               "Call configure_node again with confirmed=true to write this node's cmesh config.",
			"path":                  path,
		}
		if genErr != nil {
			result["preview_error"] = genErr.Error()
		} else {
			result["preview_toml"] = body
		}
		return ToolResponse{OK: true, Result: result}
	}
	if genErr != nil {
		return failure("suggest_config_failed", genErr.Error())
	}
	if err := onboard.WriteConfig(path, body, true); err != nil {
		return failure("write_config_failed", err.Error())
	}
	if err := onboard.ValidateConfig(path); err != nil {
		return failure("invalid_written_config", err.Error())
	}
	return ToolResponse{OK: true, Result: map[string]any{
		"path":      path,
		"status":    "written",
		"next_step": "Call start_service to install and start the cmesh daemon.",
	}}
}

type startServiceRequest struct {
	Confirmed bool `json:"confirmed,omitempty"`
}

// startService installs and starts the OS service, then polls the gateway for
// health. Per constraint C1 it must emit NOTHING to the real os.Stdout (the MCP
// JSON-RPC channel): internal/service writes to os.Stdout, so we redirect
// os.Stdout to os.DevNull for the duration and restore it after.
func startService(ctx context.Context, args json.RawMessage) ToolResponse {
	var req startServiceRequest
	if len(args) > 0 {
		if err := json.Unmarshal(args, &req); err != nil {
			return failure("invalid_arguments", err.Error())
		}
	}
	cfg, err := config.Load(config.DefaultPath())
	if err != nil {
		return failure("config_required", "write a config with configure_node first: "+err.Error())
	}
	exe, err := os.Executable()
	if err != nil {
		return failure("executable_unresolved", err.Error())
	}
	if !req.Confirmed {
		return ToolResponse{OK: true, Result: map[string]any{
			"requires_confirmation": true,
			"message":               "Call start_service again with confirmed=true to install and start the cmesh background service.",
			"config":                config.DefaultPath(),
		}}
	}

	mcpPort := cfg.Node.MCPPort
	if mcpPort == 0 {
		mcpPort = config.DefaultMCPPort
	}
	opts := service.Options{ConfigPath: config.DefaultPath(), ExePath: exe}

	var installErr, startErr error
	withSuppressedStdout(func() {
		installErr = installServiceFn(opts)
		if installErr == nil {
			startErr = startServiceFn()
		}
	})
	if installErr != nil {
		return failure("service_install_failed", installErr.Error())
	}
	if startErr != nil {
		return failure("service_start_failed", startErr.Error())
	}

	healthy := pollHealth(ctx, mcpPort, healthPollBudget)
	next := "Call onboarding_status to see the next step."
	if !healthy {
		next = "Service started but the gateway has not answered /health yet; call onboarding_status again shortly."
	}
	return ToolResponse{OK: healthy, Result: map[string]any{
		"status":          "started",
		"gateway_healthy": healthy,
		"gateway_url":     fmt.Sprintf("http://127.0.0.1:%d/mcp", mcpPort),
		"next_step":       next,
	}}
}

func pollHealth(ctx context.Context, mcpPort int, budget time.Duration) bool {
	deadline := time.Now().Add(budget)
	for {
		if gatewayHealthy(ctx, mcpPort) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// withSuppressedStdout redirects os.Stdout to os.DevNull while fn runs, then
// restores it. internal/service reads os.Stdout at call time (fmt.Printf and
// cmd.Stdout=os.Stdout), so the swap captures all of its writes. Discarding to
// DevNull avoids any pipe-drain goroutine or full-pipe deadlock.
func withSuppressedStdout(fn func()) {
	saved := os.Stdout
	devNull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		// If DevNull cannot be opened we still must not corrupt the protocol;
		// run without redirection only as an impossible-in-practice fallback.
		fn()
		return
	}
	os.Stdout = devNull
	defer func() {
		os.Stdout = saved
		_ = devNull.Close()
	}()
	fn()
}

type linkPeerRequest struct {
	Peer      string `json:"peer"`
	Confirmed bool   `json:"confirmed,omitempty"`
}

// linkPeer adds the single consented peer (reciprocal [peers.X] + outbound allow
// keyed on the whois-matchable identity) and runs a bidirectional smoke:
// Hello -> CreateTask (A->B) -> PeerSmoke (B->A). It never widens inbound policy.
func linkPeer(ctx context.Context, args json.RawMessage) ToolResponse {
	var req linkPeerRequest
	if err := json.Unmarshal(args, &req); err != nil {
		return failure("invalid_arguments", err.Error())
	}
	if strings.TrimSpace(req.Peer) == "" {
		return failure("invalid_arguments", "peer is required")
	}
	peers, err := onboard.TailnetPeers(ctx, config.DefaultMeshPort, true)
	if err != nil {
		return failure("tailnet_peers_failed", err.Error())
	}
	candidate, ok := selectPeerCandidate(req.Peer, peers)
	if !ok {
		return failure("peer_not_found", fmt.Sprintf("no tailnet peer named %q was discovered", req.Peer))
	}
	if !candidate.CmeshCapable {
		return failure("peer_not_cmesh", fmt.Sprintf("peer %q does not appear to expose cmesh yet; install + start cmesh there first", req.Peer))
	}

	if !req.Confirmed {
		return ToolResponse{OK: true, Result: map[string]any{
			"requires_confirmation": true,
			"message":               fmt.Sprintf("Call link_peer again with confirmed=true to trust and reach %q, then run the bidirectional smoke.", candidate.Name),
			"peer":                  candidate.Name,
			"address":               candidate.Address,
		}}
	}

	// Capture the peer's authoritative whois identity (name + stable_id) from its
	// /v1/hello so the outbound allowlist survives a hostname change. Best-effort:
	// the hostname label already matches whois NodeName, so a failed capture is not
	// fatal.
	_, stableID, _ := onboard.CapturePeerIdentity(ctx, candidate.Address, config.DefaultMeshPort)
	peerName, err := onboard.AddPeerLink(config.DefaultPath(), candidate, stableID)
	if err != nil {
		return failure("link_peer_failed", err.Error())
	}
	smoke := bidirectionalSmoke(ctx, candidate)
	return ToolResponse{OK: smoke.OK, Result: map[string]any{
		"peer":      peerName,
		"linked":    true,
		"smoke":     smoke,
		"next_step": "Call onboarding_status; if ready, try an example prompt.",
	}}
}

type smokeReport struct {
	Forward map[string]any `json:"forward"`
	Reverse map[string]any `json:"reverse"`
	OK      bool           `json:"ok"`
	Detail  string         `json:"detail,omitempty"`
}

func bidirectionalSmoke(ctx context.Context, peer onboard.PeerCandidate) smokeReport {
	report := smokeReport{Forward: map[string]any{}, Reverse: map[string]any{}}
	cfg, err := config.Load(config.DefaultPath())
	if err != nil {
		report.Detail = "config load failed: " + err.Error()
		return report
	}
	port := config.DefaultMeshPort
	client := meshrpc.NewClient("http://" + net.JoinHostPort(peer.Address, fmt.Sprintf("%d", port)))

	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()

	hello, err := client.Hello(ctx)
	if err != nil {
		report.Forward["reachable"] = false
		report.Detail = "peer unreachable: " + err.Error()
		return report
	}
	report.Forward["reachable"] = true
	report.Forward["node_name"] = firstNonEmpty(hello.NodeName, hello.NodeID)

	const smokeExpected = "cmesh smoke ok"
	createResp, err := client.CreateTask(ctx, schema.CreateTaskRequest{
		OriginNodeID:    cfg.Node.ID,
		OriginSessionID: "cmesh-wizard-smoke",
		Prompt:          "Reply with exactly: " + smokeExpected,
		CWD:             "~",
		Profile:         "readonly",
		TimeoutSeconds:  60,
		Wait:            true,
		Provenance: schema.Provenance{
			OriginNodeID:    cfg.Node.ID,
			OriginSessionID: "cmesh-wizard-smoke",
			DelegationChain: []string{cfg.Node.ID},
			HopCount:        0,
		},
	})
	if err != nil {
		report.Forward["task_ok"] = false
		report.Detail = "forward task failed: " + err.Error()
		return report
	}
	report.Forward["task_id"] = createResp.TaskID
	report.Forward["state"] = string(createResp.State)
	forwardOK := createResp.State == "done"
	report.Forward["ok"] = forwardOK

	reverse, err := client.PeerSmoke(ctx, schema.PeerSmokeRequest{CWD: "~", Profile: "readonly", TimeoutSeconds: 60})
	if err != nil {
		report.Reverse["ok"] = false
		report.Detail = "reverse smoke failed: " + err.Error()
		return report
	}
	reverseOK := reverse.Checks["prompt_received"] && reverse.Checks["smoke_response_verified"]
	report.Reverse["ok"] = reverseOK
	report.Reverse["node_name"] = firstNonEmpty(reverse.NodeName, reverse.NodeID, reverse.Peer)
	if reverse.Error != nil {
		report.Reverse["error"] = reverse.Error.Detail
	}

	report.OK = forwardOK && reverseOK
	if report.OK {
		report.Detail = "bidirectional smoke passed"
	} else if report.Detail == "" {
		report.Detail = "bidirectional smoke did not pass in both directions"
	}
	return report
}

func selectPeerCandidate(name string, peers []onboard.PeerCandidate) (onboard.PeerCandidate, bool) {
	want := strings.ToLower(strings.TrimSpace(name))
	for _, p := range peers {
		if strings.ToLower(p.Name) == want || strings.ToLower(p.NodeID) == want || strings.ToLower(p.DNSName) == want {
			return p, true
		}
	}
	return onboard.PeerCandidate{}, false
}

type registerGatewayRequest struct {
	Harness   string `json:"harness"`
	Confirmed bool   `json:"confirmed,omitempty"`
}

// registerGateway swaps the agent's cmesh MCP from the stdio setup server to the
// local HTTP gateway. For Claude Code it reuses ClaudeHTTPAddArgs; for Codex it
// writes the [mcp_servers.cmesh] HTTP entry itself (0600). The host_token is
// never echoed: previews redact it.
func registerGateway(ctx context.Context, args json.RawMessage) ToolResponse {
	var req registerGatewayRequest
	if err := json.Unmarshal(args, &req); err != nil {
		return failure("invalid_arguments", err.Error())
	}
	harness := strings.ToLower(strings.TrimSpace(req.Harness))
	if harness == "" {
		return failure("invalid_arguments", "harness is required (claude-code or codex)")
	}
	cfg, err := config.Load(config.DefaultPath())
	if err != nil {
		return failure("config_required", "write a config with configure_node first: "+err.Error())
	}
	mcpPort := cfg.Node.MCPPort
	if mcpPort == 0 {
		mcpPort = config.DefaultMCPPort
	}
	gatewayURL := fmt.Sprintf("http://127.0.0.1:%d/mcp", mcpPort)

	switch harness {
	case "claude-code", "claude":
		return registerClaudeGateway(req, gatewayURL, cfg.Gateway.HostToken)
	case "codex":
		return registerCodexGateway(req, gatewayURL, cfg.Gateway.HostToken)
	default:
		return failure("unsupported_harness", "harness must be claude-code or codex")
	}
}

func registerClaudeGateway(req registerGatewayRequest, gatewayURL, hostToken string) ToolResponse {
	addArgs := setupinstall.ClaudeHTTPAddArgs(setupinstall.ServerName, gatewayURL, hostToken)
	redacted := append([]string{"claude"}, setupinstall.RedactClaudeHTTPAddArgs(addArgs)...)
	preview := setupinstall.ShellCommand(redacted)
	if !req.Confirmed {
		return ToolResponse{OK: true, Result: map[string]any{
			"requires_confirmation": true,
			"message":               "Call register_gateway again with confirmed=true to point Claude Code at the cmesh HTTP gateway. You will restart the agent session once afterward.",
			"preview":               preview,
			"gateway_url":           gatewayURL,
		}}
	}
	claude, err := exec.LookPath("claude")
	if err != nil {
		return ToolResponse{OK: true, Result: map[string]any{
			"status":         "manual",
			"detail":         "Claude Code not found on PATH; register manually.",
			"manual_command": preview,
			"gateway_url":    gatewayURL,
			"next_step":      "Run the command above, then restart your agent session.",
		}}
	}
	// Replace any existing stdio registration first, then add the HTTP gateway.
	withSuppressedStdout(func() {
		_ = exec.Command(claude, "mcp", "remove", "--scope", "user", setupinstall.ServerName).Run()
	})
	cmd := exec.Command(claude, addArgs...)
	out, runErr := cmd.CombinedOutput()
	if runErr != nil {
		detail := strings.TrimSpace(string(out))
		if detail == "" {
			detail = runErr.Error()
		}
		return ToolResponse{OK: true, Result: map[string]any{
			"status":         "manual",
			"detail":         "automatic registration failed; register manually: " + detail,
			"manual_command": preview,
			"gateway_url":    gatewayURL,
		}}
	}
	return ToolResponse{OK: true, Result: map[string]any{
		"status":      "registered",
		"harness":     "claude-code",
		"gateway_url": gatewayURL,
		"next_step":   "Restart your agent session once to load the cmesh HTTP gateway MCP.",
	}}
}

func registerCodexGateway(req registerGatewayRequest, gatewayURL, hostToken string) ToolResponse {
	path := setupinstall.CodexConfigPath()
	// Preview mirrors the real config that writeCodexGatewayConfig produces, with
	// the host token redacted. It never echoes the token.
	preview := fmt.Sprintf("[mcp_servers.cmesh]\nurl = %s\nhttp_headers = { \"X-Cmesh-Host-Token\" = '<redacted; written to %s>' }\nstartup_timeout_sec = 120", tomlString(gatewayURL), path)
	if !req.Confirmed {
		return ToolResponse{OK: true, Result: map[string]any{
			"requires_confirmation": true,
			"message":               "Codex HTTP MCP registration varies by CLI version. Call register_gateway again with confirmed=true to write the Codex config entry; the host token is written to the file but never shown.",
			"preview":               preview,
			"config_path":           path,
			"gateway_url":           gatewayURL,
		}}
	}
	if err := writeCodexGatewayConfig(path, gatewayURL, hostToken); err != nil {
		return ToolResponse{OK: true, Result: map[string]any{
			"status":      "manual",
			"detail":      "could not write Codex config automatically: " + err.Error(),
			"gateway_url": gatewayURL,
			"next_step":   "Add a local HTTP MCP server named cmesh at the gateway_url manually (Codex registration varies by CLI version).",
		}}
	}
	return ToolResponse{OK: true, Result: map[string]any{
		"status":      "registered",
		"harness":     "codex",
		"config_path": path,
		"gateway_url": gatewayURL,
		"next_step":   "Restart your agent session once to load the cmesh HTTP gateway MCP.",
	}}
}

// writeCodexGatewayConfig writes/replaces the [mcp_servers.cmesh] HTTP table in
// the Codex config (0600), embedding the host_token as an Authorization header.
// The file is written directly; the token is never returned to the caller.
func writeCodexGatewayConfig(path, gatewayURL, hostToken string) error {
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
