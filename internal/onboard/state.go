package onboard

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"cmesh/internal/config"
)

// OnboardingState is a coarse, re-derived snapshot of how far first-run setup has
// progressed on this node. The wizard never persists wizard state; every call to
// DeriveOnboardingState re-inspects the environment so the flow is resumable and
// works identically on a fresh second node.
type OnboardingState string

const (
	// StateTailscaleDown means the Tailscale backend is not Running or has no
	// tailnet IP yet.
	StateTailscaleDown OnboardingState = "tailscale_down"
	// StateTailscaleSolo means Tailscale is up but no online peers were found.
	StateTailscaleSolo OnboardingState = "tailscale_solo"
	// StateNeedsConfig means online peers exist but no cmesh config is present here.
	StateNeedsConfig OnboardingState = "needs_config"
	// StateDaemonDown means a config exists but the local gateway /health fails.
	StateDaemonDown OnboardingState = "daemon_down"
	// StateNoCmeshPeer means the daemon is up but no cmesh-capable peer was found.
	StateNoCmeshPeer OnboardingState = "no_cmesh_peer"
	// StatePeerUntrusted means a cmesh peer is discovered but not yet linked
	// (missing [peers.X] / outbound allow), so a smoke would fail one direction.
	StatePeerUntrusted OnboardingState = "peer_untrusted"
	// StateReady means the local node is configured, the daemon is up, and at least
	// one cmesh peer is linked and reachable.
	StateReady OnboardingState = "ready"
)

// OnboardingStatus is the structured result of DeriveOnboardingState. It carries
// the verify-then-confirm narration the wizard surfaces to the user.
type OnboardingStatus struct {
	State    OnboardingState `json:"state"`
	NextStep string          `json:"next_step"`
	Detail   string          `json:"detail"`
	Machine  Machine         `json:"machine"`
	Peers    []PeerCandidate `json:"peers,omitempty"`
}

// Indirection seams for tests. In-package tests override these to inject fakes
// for machine detection, peer discovery, and config presence.
var (
	detectMachineFn = DetectMachine
	tailnetPeersFn  = TailnetPeers
	loadConfigFn    = config.Load
)

// DeriveOnboardingState re-derives onboarding progress from the live environment.
// gatewayHealthy reports whether the local HTTP gateway answers /health for the
// given mcp port; it is injected so callers (and tests) control the probe.
//
// States are evaluated in table order; the first matching state wins.
func DeriveOnboardingState(ctx context.Context, configPath string, gatewayHealthy func(ctx context.Context, mcpPort int) bool) OnboardingStatus {
	if configPath == "" {
		configPath = config.DefaultPath()
	}
	machine := detectMachineFn(ctx)
	status := OnboardingStatus{Machine: machine}

	if !machine.TailscaleOK || len(machine.TailnetIPs) == 0 {
		status.State = StateTailscaleDown
		status.NextStep = "Start Tailscale and log in (tailscale up), then call onboarding_status again."
		status.Detail = "Tailscale backend is not Running or has no tailnet IP yet."
		if machine.Detail != "" {
			status.Detail = machine.Detail
		}
		return status
	}

	peers, _ := tailnetPeersFn(ctx, config.DefaultMeshPort, true)
	status.Peers = peers
	online := onlinePeers(peers)
	cmeshPeers := cmeshCapablePeers(peers)

	if len(online) == 0 {
		status.State = StateTailscaleSolo
		status.NextStep = "Install Tailscale on a second machine and bring it onto this tailnet, then call onboarding_status again."
		status.Detail = "Tailscale is up but no online peers were found on this tailnet."
		return status
	}

	cfg, cfgErr := loadConfigFn(configPath)
	if cfgErr != nil {
		status.State = StateNeedsConfig
		status.NextStep = "Call configure_node to write this node's cmesh config."
		status.Detail = fmt.Sprintf("%d online tailnet peer(s) detected; no cmesh config at %s yet.", len(online), configPath)
		return status
	}

	mcpPort := cfg.Node.MCPPort
	if mcpPort == 0 {
		mcpPort = config.DefaultMCPPort
	}
	if gatewayHealthy == nil || !gatewayHealthy(ctx, mcpPort) {
		status.State = StateDaemonDown
		status.NextStep = "Call start_service to install and start the cmesh daemon."
		status.Detail = fmt.Sprintf("Config present but the local gateway at http://127.0.0.1:%d/health did not respond.", mcpPort)
		return status
	}

	if len(cmeshPeers) == 0 {
		status.State = StateNoCmeshPeer
		status.NextStep = "Install cmesh on the other node; there run: continue helping me configure cmesh for this new node."
		status.Detail = "Daemon is up but no cmesh-capable peer was found on the tailnet yet."
		return status
	}

	if linked := linkedCmeshPeers(cfg, cmeshPeers); len(linked) == 0 {
		status.State = StatePeerUntrusted
		status.NextStep = "Call link_peer to trust and reach the discovered cmesh peer, then verify the bidirectional smoke."
		status.Detail = fmt.Sprintf("%d cmesh peer(s) discovered but not yet linked in this config.", len(cmeshPeers))
		return status
	}

	status.State = StateReady
	status.NextStep = "Setup is complete. Try an example prompt, or switch profiles (readonly/dev/network/interactive/git_push)."
	status.Detail = "Bidirectional reachability looks good. The mesh is ready."
	return status
}

func onlinePeers(peers []PeerCandidate) []PeerCandidate {
	var out []PeerCandidate
	for _, p := range peers {
		if p.Online {
			out = append(out, p)
		}
	}
	return out
}

func cmeshCapablePeers(peers []PeerCandidate) []PeerCandidate {
	var out []PeerCandidate
	for _, p := range peers {
		if p.CmeshCapable {
			out = append(out, p)
		}
	}
	return out
}

// linkedCmeshPeers returns the discovered cmesh peers that already have a matching
// [peers.X] block in the config (reachability is configured). The bidirectional
// smoke remains link_peer's job; this is the cheap "is it wired" signal.
func linkedCmeshPeers(cfg *config.Config, cmeshPeers []PeerCandidate) []PeerCandidate {
	var out []PeerCandidate
	for _, peer := range cmeshPeers {
		for _, cp := range cfg.Peers {
			if strings.EqualFold(cleanName(peer.Name), cleanName(firstNonEmpty(cp.Name, cp.NodeID))) ||
				(cp.Address != "" && cp.Address == peer.Address) {
				out = append(out, peer)
				break
			}
		}
	}
	return out
}

// AddPeerLink writes the reciprocal reachability ([peers.<name>] with the peer's
// address/port) and adds the peer's whois-matchable identity (Name/StableID,
// never the ephemeral discovery NodeID) to policy.outbound.allowed_peers. It is
// idempotent and never widens inbound policy.allowed_peers.
//
// stableID, when non-empty, is the peer's authoritative whois stable_id captured
// from its /v1/hello (see CapturePeerIdentity). It is added to the outbound
// allowlist alongside the hostname so trust survives a hostname change. Pass "" to
// rely on the hostname label alone.
//
// Because the config file does not round-trip through the TOML encoder (Duration
// has no MarshalText), this performs targeted text edits that mirror the format
// GenerateConfig writes.
func AddPeerLink(configPath string, peer PeerCandidate, stableID string) (peerName string, err error) {
	if configPath == "" {
		configPath = config.DefaultPath()
	}
	identity := matchableIdentity(peer)
	if identity == "" {
		return "", fmt.Errorf("peer has no whois-matchable identity (name/stable id)")
	}
	if err := rejectGlobMetachars(identity); err != nil {
		return "", err
	}
	name := cleanName(peer.Name)
	if name == "" {
		return "", fmt.Errorf("peer has no usable name")
	}
	if err := rejectGlobMetachars(name); err != nil {
		return "", err
	}
	if peer.Address == "" {
		return "", fmt.Errorf("peer %q has no tailnet address to reach", peer.Name)
	}
	stableID = strings.TrimSpace(stableID)
	if stableID != "" {
		if err := rejectGlobMetachars(stableID); err != nil {
			return "", err
		}
	}

	raw, err := os.ReadFile(configPath)
	if err != nil {
		return "", err
	}
	body := string(raw)

	// 1. Ensure identity (and stable_id if present) are in
	// policy.outbound.allowed_peers (idempotent).
	body, err = ensureOutboundAllow(body, identity)
	if err != nil {
		return "", err
	}
	if stableID != "" && stableID != identity {
		body, err = ensureOutboundAllow(body, stableID)
		if err != nil {
			return "", err
		}
	}

	// 2. Ensure a [peers.<name>] block exists (idempotent).
	body = ensurePeerSection(body, name, peer, identity)

	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(configPath, []byte(body), 0o600); err != nil {
		return "", err
	}
	return name, nil
}

// rejectGlobMetachars refuses identities containing glob metacharacters so a
// crafted peer name cannot widen an allowlist match.
func rejectGlobMetachars(value string) error {
	if strings.ContainsAny(value, "*?[]") {
		return fmt.Errorf("identity %q contains glob metacharacters (* ? [ ]) and cannot be used in an allowlist", value)
	}
	return nil
}

// ensureOutboundAllow inserts identity into the [policy.outbound] allowed_peers
// array if absent, preserving the single-line array format. If the section is
// missing it is appended.
func ensureOutboundAllow(body, identity string) (string, error) {
	lines := strings.Split(body, "\n")
	inOutbound := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			section := strings.Trim(trimmed, "[]")
			inOutbound = section == "policy.outbound"
			continue
		}
		if !inOutbound {
			continue
		}
		key, _, ok := strings.Cut(trimmed, "=")
		if !ok || strings.TrimSpace(key) != "allowed_peers" {
			continue
		}
		existing := parseStringArray(trimmed)
		for _, e := range existing {
			if e == identity {
				return body, nil // already present; idempotent.
			}
		}
		existing = append(existing, identity)
		lines[i] = "allowed_peers = " + encodeStringArray(existing)
		return strings.Join(lines, "\n"), nil
	}
	// No [policy.outbound] allowed_peers found; append a section.
	appended := strings.TrimRight(body, "\n")
	appended += "\n\n[policy.outbound]\nallowed_peers = " + encodeStringArray([]string{identity}) + "\n"
	return appended, nil
}

// ensurePeerSection appends a [peers.<name>] block if one does not already exist.
func ensurePeerSection(body, name string, peer PeerCandidate, identity string) string {
	header := "[peers." + name + "]"
	for _, line := range strings.Split(body, "\n") {
		if strings.TrimSpace(line) == header {
			return body // already present; idempotent.
		}
	}
	port := config.DefaultMeshPort
	var b strings.Builder
	b.WriteString(strings.TrimRight(body, "\n"))
	b.WriteString("\n\n")
	b.WriteString(header)
	b.WriteByte('\n')
	b.WriteString("name = " + encodeString(firstNonEmpty(peer.Name, name)) + "\n")
	b.WriteString("node_id = " + encodeString(identity) + "\n")
	b.WriteString("address = " + encodeString(peer.Address) + "\n")
	b.WriteString(fmt.Sprintf("port = %d\n", port))
	return b.String()
}

func parseStringArray(line string) []string {
	open := strings.IndexByte(line, '[')
	close := strings.LastIndexByte(line, ']')
	if open < 0 || close < 0 || close <= open {
		return nil
	}
	inner := line[open+1 : close]
	var out []string
	for _, part := range strings.Split(inner, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		var s string
		if err := json.Unmarshal([]byte(part), &s); err == nil {
			out = append(out, s)
		}
	}
	return out
}

func encodeStringArray(values []string) string {
	var b strings.Builder
	b.WriteByte('[')
	for i, v := range values {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(encodeString(v))
	}
	b.WriteByte(']')
	return b.String()
}

func encodeString(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}
