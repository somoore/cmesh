package setupstdio

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"

	"cmesh/internal/config"
	"cmesh/internal/mcp/harnessreg"
	meshrpc "cmesh/internal/mesh/rpc"
	"cmesh/internal/mesh/schema"
	"cmesh/internal/onboard"
	profiledefs "cmesh/internal/profile"
	"cmesh/internal/service"
	"cmesh/internal/task"
)

// Indirection seams so the C1 stdout test (and others) can inject fakes without a
// real launchd/systemd. Production wiring points at the service package.
var (
	installServiceFn = service.Install
	startServiceFn   = service.Start
	// healthPollBudget bounds how long start_service waits for the gateway to come
	// up before reporting. Overridable in tests to avoid a real 10s wait.
	healthPollBudget = 10 * time.Second
	// readySmokeFn is the bidirectional smoke that gates the 'ready' state in
	// onboarding_status. Overridable in tests so status polls never hit a real
	// tailnet. Production wraps bidirectionalSmoke with a short, fail-fast budget.
	readySmokeFn = readySmoke
	// readySmokeBudget bounds the ready-gate smoke. It runs on status polls (only in
	// the late-state branch). The smoke does Hello + CreateTask{Wait:true} (a real
	// runner round-trip) + PeerSmoke (a second runner round-trip), so it returns in
	// actual-completion-time on a healthy mesh (~10-20s); the budget is only a
	// CEILING for an unreachable/failing peer. It must therefore match link_peer's
	// 90s budget — a shorter cap (e.g. 8s) is less than the operation itself and
	// would make a healthy mesh always time out to peer_untrusted, never reaching
	// 'ready'. Overridable in tests (which inject an instant fake smoke).
	readySmokeBudget = 90 * time.Second
)

// readySmoke runs a short, fail-fast bidirectional smoke against a linked peer to
// gate the 'ready' state. It maps the smoke report into an onboard.SmokeResult so
// the deriver can distinguish reachable-but-policy-denied (fix the remote's policy)
// from unreachable/untrusted (fix connectivity/trust).
func readySmoke(ctx context.Context, peer onboard.PeerCandidate) onboard.SmokeResult {
	ctx, cancel := context.WithTimeout(ctx, readySmokeBudget)
	defer cancel()
	return smokeResultFromReport(bidirectionalSmoke(ctx, peer))
}

// smokeResultFromReport maps a smokeReport into the deriver's onboard.SmokeResult.
// PolicyDenied is set only when the peer was reachable AND the FAILING leg carried
// a policy-denial code. report.FailCode is populated solely from the forward leg
// (Hello + our CreateTask against the remote), so a reverse-leg denial — which is a
// LOCAL policy fix, since this node is the worker there — never sets PolicyDenied.
func smokeResultFromReport(report smokeReport) onboard.SmokeResult {
	return onboard.SmokeResult{
		OK:           report.OK,
		Reachable:    report.Reachable,
		PolicyDenied: report.Reachable && isPolicyDenialCode(report.FailCode),
		Detail:       report.Detail,
	}
}

// isPolicyDenialCode reports whether a task error code means "the peer was reached
// but rejected the task on its own policy" — a remote-config fix, not a local one.
func isPolicyDenialCode(code task.ErrorCode) bool {
	switch code {
	case task.ErrorCWDDenied, task.ErrorProfileDenied, task.ErrorProfileUnavailable:
		return true
	default:
		return false
	}
}

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

// onboardingStatus re-derives the wizard state from the live environment. The
// ready-gate smoke is injected so it only runs in the late-state branch and stays
// fail-fast.
func onboardingStatus(ctx context.Context) ToolResponse {
	status := onboard.DeriveOnboardingState(ctx, config.DefaultPath(), gatewayHealthy, readySmokeFn)
	return ToolResponse{OK: true, Result: status}
}

type configureNodeRequest struct {
	initOptionsRequest
	Confirmed               bool                                 `json:"confirmed,omitempty"`
	ProfileAcknowledgements []profiledefs.ProfileAcknowledgement `json:"profile_acknowledgements,omitempty"`
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
		addProfileGuidance(result, opts, req.ProfileAcknowledgements)
		if genErr != nil {
			result["preview_error"] = genErr.Error()
		} else {
			result["preview_toml"] = body
		}
		return ToolResponse{OK: true, Result: result}
	}
	if problems := profiledefs.AcknowledgementProblems(selectedProfiles(opts), req.ProfileAcknowledgements); len(problems) > 0 {
		return failure("profile_acknowledgement_required", strings.Join(problems, "; "))
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
	if runtime.GOOS == "windows" {
		opts.Windows.UserTask = true
		opts.Windows.Firewall = true
	}

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
	Peer string `json:"peer"`
	// Role lets an origin-only node opt out of the inbound trust grant. Empty or
	// worker|both (the default) takes the bidirectional grant; only "origin" skips
	// the inbound side. Role is not persisted in config, so it cannot be re-derived
	// here — the wizard threads it explicitly.
	Role      string `json:"role,omitempty"`
	Confirmed bool   `json:"confirmed,omitempty"`
}

// linkPeerPreview builds the confirmation preview. For informed consent it MUST
// disclose BOTH directions of the trust grant when inbound is granted.
func linkPeerPreview(peer string, grantInbound bool) string {
	if grantInbound {
		return fmt.Sprintf("%s will be able to send work to this machine AND this machine can send work to %s.", peer, peer)
	}
	return fmt.Sprintf("This machine can send work to %s (origin-only: no inbound grant; %s cannot send work here).", peer, peer)
}

// grantsInbound reports whether this node should take the inbound trust grant when
// linking a peer. Per the amended trust model the grant is role-aware: only an
// explicit origin-only node skips it; worker|both (the default) takes it so the
// reverse smoke leg can pass and the handshake closes.
func (r linkPeerRequest) grantsInbound() bool {
	return strings.ToLower(strings.TrimSpace(r.Role)) != "origin"
}

// linkPeer adds the single consented peer (reciprocal [peers.X] + outbound allow,
// and — when this node serves as a worker — inbound allow, all keyed on the
// whois-matchable identity) and runs a bidirectional smoke:
// Hello -> CreateTask (A->B) -> PeerSmoke (B->A). The inbound grant is bounded to
// the one consented peer (never a wildcard, never another node).
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

	grantInbound := req.grantsInbound()
	if !req.Confirmed {
		return ToolResponse{OK: true, Result: map[string]any{
			"requires_confirmation": true,
			"message":               fmt.Sprintf("Call link_peer again with confirmed=true to trust and reach %q, then run the bidirectional smoke.", candidate.Name),
			"preview":               linkPeerPreview(candidate.Name, grantInbound),
			"grants_inbound":        grantInbound,
			"note":                  "Bounded grant: the peer is still constrained by this node's allowed_cwds and profile ceilings. This is \"may ask\", not \"may do anything\".",
			"peer":                  candidate.Name,
			"address":               candidate.Address,
		}}
	}

	// Resolve the peer's identity from its own /v1/hello — the authoritative source
	// for the whois-matchable node_name and stable_id (both from MagicDNS, which is
	// also what the inbound policy matches a connection against). When capture
	// succeeds, prefer the reported node_name over the discovery-derived candidate
	// name: discovery reads `tailscale status`, whose HostName can be a human
	// display name ("Scott's MacBook Pro") that never matches the peer's whois
	// identity. Capture is best-effort — on failure we keep the discovery name as a
	// fallback rather than blanking it (AddPeerLink requires a non-empty name).
	capturedName, stableID, _ := onboard.CapturePeerIdentity(ctx, candidate.Address, config.DefaultMeshPort)
	if n := strings.TrimSpace(capturedName); n != "" {
		candidate.Name = n
	}
	peerName, err := onboard.AddPeerLink(config.DefaultPath(), candidate, stableID, grantInbound)
	if err != nil {
		return failure("link_peer_failed", err.Error())
	}
	smoke := bidirectionalSmoke(ctx, candidate)
	next := "Bidirectional smoke did not pass yet; call onboarding_status to see what to fix."
	if smoke.OK {
		next = "Trust verified. Call onboarding_status (it will show 'ready'), then call register_gateway to switch onto the cmesh HTTP delegation gateway."
	}
	return ToolResponse{OK: smoke.OK, Result: map[string]any{
		"peer":           peerName,
		"linked":         true,
		"grants_inbound": grantInbound,
		"smoke":          smoke,
		"next_step":      next,
	}}
}

type repairPeersRequest struct {
	Confirmed bool `json:"confirmed,omitempty"`
}

// repairPeers lets a user say "fix my peer entries" to the agent instead of editing
// TOML or knowing to re-run link_peer. It delegates to onboard.DiagnosePeers /
// ApplyPeerRepairs (the trust anchor is tailscale whois, not the peer's
// self-reported /v1/hello name; see those for the security rationale). Without
// confirmed=true it returns a per-peer diff and changes nothing; with confirmed=true
// it rewrites the fixable entries, preserving this node's inbound trust posture.
//
// This runs in the onboarding stdio server, a SEPARATE process from the daemon, so
// the written config takes effect on the next daemon (re)start as usual.
func repairPeers(ctx context.Context, args json.RawMessage) ToolResponse {
	var req repairPeersRequest
	if len(args) > 0 {
		if err := json.Unmarshal(args, &req); err != nil {
			return failure("invalid_arguments", err.Error())
		}
	}
	cfg, err := config.Load(config.DefaultPath())
	if err != nil {
		return failure("config_load_failed", err.Error())
	}
	if len(cfg.Peers) == 0 {
		return ToolResponse{OK: true, Result: map[string]any{
			"repairs":   []onboard.PeerRepair{},
			"message":   "No [peers.*] entries are configured; nothing to repair.",
			"next_step": "If you expected a peer here, run the setup flow to discover and link one.",
		}}
	}
	repairs, err := onboard.DiagnosePeers(ctx, cfg)
	if err != nil {
		return failure("tailnet_peers_failed", err.Error())
	}
	fixable := onboard.CountRepairs(repairs, onboard.RepairFixable)

	if !req.Confirmed {
		msg := fmt.Sprintf("Found %d configured peer(s): %d fixable, %d ok.", len(repairs), fixable, onboard.CountRepairs(repairs, onboard.RepairOK))
		next := "Nothing to fix — all reachable peer entries already match their whois identity."
		if fixable > 0 {
			next = "Call repair_peers again with confirmed=true to rewrite the fixable entries to their whois-matchable identity. Conflicts and unreachable peers are left unchanged."
		}
		return ToolResponse{OK: true, Result: map[string]any{
			"requires_confirmation": fixable > 0,
			"repairs":               repairs,
			"message":               msg,
			"next_step":             next,
		}}
	}

	fixedNames, err := onboard.ApplyPeerRepairs(config.DefaultPath(), repairs)
	if err != nil {
		return failure("repair_failed", err.Error())
	}
	return ToolResponse{OK: true, Result: map[string]any{
		"repaired":  fixedNames,
		"repairs":   repairs,
		"message":   fmt.Sprintf("Repaired %d peer entry(ies): %s.", len(fixedNames), strings.Join(fixedNames, ", ")),
		"next_step": "Run a bidirectional smoke (or onboarding_status) to confirm the mesh is healthy.",
	}}
}

type smokeReport struct {
	Forward map[string]any `json:"forward"`
	Reverse map[string]any `json:"reverse"`
	OK      bool           `json:"ok"`
	Detail  string         `json:"detail,omitempty"`
	// Reachable is true once the peer answered Hello, even if a later leg failed.
	Reachable bool `json:"-"`
	// FailCode is the structured task error code of the first failing leg (e.g.
	// cwd_denied, profile_denied, peer_unauthorized), when the peer surfaced one.
	// Empty when the failure carried no structured code (e.g. a transport error).
	FailCode task.ErrorCode `json:"-"`
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
		report.FailCode = meshrpc.CodeOf(err)
		return report
	}
	report.Reachable = true
	report.Forward["reachable"] = true
	report.Forward["node_name"] = firstNonEmpty(hello.NodeName, hello.NodeID)
	smokeProfile := "readonly"
	if helloSupportsProfile(hello, profiledefs.Query) {
		smokeProfile = profiledefs.Query
	}
	report.Forward["profile"] = smokeProfile

	const smokeExpected = "cmesh smoke ok"
	// Leave CWD empty: the forward task runs on the REMOTE worker, which self-
	// selects a cwd its own allowed_cwds accepts (keyed off the "cmesh-wizard-smoke"
	// origin session). We can't know a path that exists on the remote — especially
	// cross-OS — so the worker, which enforces the policy, picks it.
	createReq := schema.CreateTaskRequest{
		OriginNodeID:    cfg.Node.ID,
		OriginSessionID: "cmesh-wizard-smoke",
		Prompt:          "Reply with exactly: " + smokeExpected,
		CWD:             "",
		Profile:         smokeProfile,
		TimeoutSeconds:  60,
		Wait:            true,
		Provenance: schema.Provenance{
			OriginNodeID:    cfg.Node.ID,
			OriginSessionID: "cmesh-wizard-smoke",
			DelegationChain: []string{cfg.Node.ID},
			HopCount:        0,
		},
	}
	if smokeProfile == profiledefs.Query {
		createReq.TaskKind = "query"
		createReq.Query = &schema.QueryRequest{Kind: profiledefs.CapSystemInfo}
		createReq.Prompt = ""
	}
	createResp, err := client.CreateTask(ctx, createReq)
	if err != nil {
		report.Forward["task_ok"] = false
		report.Detail = "forward task failed: " + err.Error()
		report.FailCode = meshrpc.CodeOf(err)
		return report
	}
	report.Forward["task_id"] = createResp.TaskID
	report.Forward["state"] = string(createResp.State)
	forwardOK := createResp.State == "done"
	report.Forward["ok"] = forwardOK

	// Leave CWD empty: the reverse smoke runs THIS node as the worker (the remote
	// asks us to run it), and runPeerSmokeToCaller defaults an empty cwd to a path
	// the worker's own policy accepts. Sending "~" here would force a cwd our own
	// allowed_cwds may reject.
	reverse, err := client.PeerSmoke(ctx, schema.PeerSmokeRequest{Profile: smokeProfile, TimeoutSeconds: 60})
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
		// Deliberately NOT mapped to FailCode/PolicyDenied: the reverse leg runs with
		// THIS node as the worker (the remote asks us to run the smoke against it), so
		// a reverse cwd_denied/profile_denied is a LOCAL policy fix. Only the forward
		// leg — the remote rejecting OUR task — is a remote-policy denial. Surfacing a
		// reverse denial as peer_policy_denied would point the user at the wrong node.
	}

	report.OK = forwardOK && reverseOK
	if report.OK {
		report.Detail = "bidirectional smoke passed"
	} else if report.Detail == "" {
		report.Detail = "bidirectional smoke did not pass in both directions"
	}
	return report
}

func helloSupportsProfile(hello schema.HelloResponse, profile string) bool {
	for _, runner := range hello.Runners {
		for _, candidate := range runner.Profiles {
			if candidate == profile {
				return true
			}
		}
	}
	return false
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
// local HTTP gateway. The registration itself lives in harnessreg, shared with
// the gateway's register_harness tool so both surfaces behave identically. The
// host_token is never echoed: previews redact it.
//
// Unlike the gateway surface, this one requires an explicit harness: it runs
// during onboarding, where guessing wrong would point the wrong agent at the
// gateway and strand the user mid-swap.
func registerGateway(_ context.Context, args json.RawMessage) ToolResponse {
	var req registerGatewayRequest
	if err := json.Unmarshal(args, &req); err != nil {
		return failure("invalid_arguments", err.Error())
	}
	harness := strings.ToLower(strings.TrimSpace(req.Harness))
	if harness == "" {
		return failure("invalid_arguments", "harness is required (claude-code or codex)")
	}
	switch harness {
	case "claude-code", "claude", "codex":
	default:
		return failure("unsupported_harness", "harness must be claude-code or codex")
	}
	cfg, err := config.Load(config.DefaultPath())
	if err != nil {
		return failure("config_required", "write a config with configure_node first: "+err.Error())
	}
	// The setup server speaks JSON-RPC on os.Stdout, so any child process output
	// must be redirected or it corrupts the protocol channel.
	out := harnessreg.Register(harnessreg.Request{
		Harness:        harness,
		Confirmed:      req.Confirmed,
		SuppressStdout: withSuppressedStdout,
	}, harnessreg.GatewayURL(cfg.Node.MCPPort), cfg.Gateway.HostToken)
	return toolResponseFromRegistration(out)
}

// toolResponseFromRegistration maps a harnessreg result onto the stdio surface's
// response envelope.
func toolResponseFromRegistration(out harnessreg.Response) ToolResponse {
	result := map[string]any{}
	for k, v := range out.Fields {
		result[k] = v
	}
	if out.RequiresConfirmation {
		result["requires_confirmation"] = true
	} else if out.Status != "" {
		result["status"] = out.Status
	}
	return ToolResponse{OK: true, Result: result}
}
