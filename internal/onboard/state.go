package onboard

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"cmesh/internal/config"
	profiledefs "cmesh/internal/profile"
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
	// StatePeerPolicyDenied means a linked cmesh peer was REACHED but rejected the
	// smoke task on policy (e.g. the remote's allowed_cwds or denied_profiles). The
	// mesh path works; the fix is on the OTHER node's config, not local trust.
	StatePeerPolicyDenied OnboardingState = "peer_policy_denied"
	// StateReady means the local node is configured, the daemon is up, and at least
	// one cmesh peer is linked and reachable.
	StateReady OnboardingState = "ready"
)

// SmokeResult is the rich outcome of a bidirectional smoke against a linked peer.
// It carries enough to distinguish failure modes that need DIFFERENT fixes:
// unreachable (peer down / not on tailnet), policy-denied (reached but the remote
// rejected the task on policy), and untrusted (reached but auth/trust failed).
// The category is a plain string so this package stays free of a task dependency;
// callers map their structured error codes onto Reason.
type SmokeResult struct {
	// OK is true only when the smoke passed in both directions.
	OK bool
	// Reachable is true once the peer answered /hello, even if a later step failed.
	Reachable bool
	// PolicyDenied is true when the peer was reached but rejected the task on policy
	// (cwd_denied, profile_denied, and similar remote-config rejections).
	PolicyDenied bool
	// Detail is a human-readable explanation of the outcome.
	Detail string
}

// SmokeFunc runs a bidirectional smoke against a linked peer and reports the rich
// outcome. It is injected into DeriveOnboardingState so the round-trip stays
// testable; a nil SmokeFunc means "cannot verify".
type SmokeFunc func(ctx context.Context, peer PeerCandidate) SmokeResult

// OnboardingStatus is the structured result of DeriveOnboardingState. It carries
// the verify-then-confirm narration the wizard surfaces to the user.
type OnboardingStatus struct {
	State               OnboardingState              `json:"state"`
	NextStep            string                       `json:"next_step"`
	Detail              string                       `json:"detail"`
	Machine             Machine                      `json:"machine"`
	Peers               []PeerCandidate              `json:"peers,omitempty"`
	ProfileMenu         []profiledefs.GuideItem      `json:"profile_menu"`
	RecommendedProfiles []profiledefs.Recommendation `json:"recommended_profiles"`
	ProfileDecisionRule string                       `json:"profile_decision_rule"`
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
// smokeFn, when non-nil, runs a real bidirectional CreateTask round-trip against a
// linked cmesh peer and reports whether trust is verified in BOTH directions. It is
// injected (like gatewayHealthy) so the round-trip stays testable and cheap, and is
// only invoked in the LATE-state branch (config present + daemon up + a cmesh peer
// linked) so early onboarding_status polls never pay its cost. A nil smokeFn means
// "cannot verify", which yields peer_untrusted rather than a false ready — 'ready'
// is never returned without an actual passing smoke.
//
// States are evaluated in table order; the first matching state wins.
func DeriveOnboardingState(ctx context.Context, configPath string, gatewayHealthy func(ctx context.Context, mcpPort int) bool, smokeFn SmokeFunc) OnboardingStatus {
	if configPath == "" {
		configPath = config.DefaultPath()
	}
	machine := detectMachineFn(ctx)
	guide := profiledefs.Guide()
	status := OnboardingStatus{
		Machine:             machine,
		ProfileMenu:         guide.Profiles,
		RecommendedProfiles: onboardingRecommendations(),
		ProfileDecisionRule: guide.DecisionRule,
	}

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

	linked := linkedCmeshPeers(cfg, cmeshPeers)
	if len(linked) == 0 {
		status.State = StatePeerUntrusted
		status.NextStep = "On THIS origin node, call link_peer to trust and reach the discovered cmesh peer (this writes the [peers.X] block + allowlist and runs the smoke). The OTHER node must already have run configure_node + start_service so it is reachable."
		status.Detail = fmt.Sprintf("%d cmesh peer(s) discovered but not yet linked in this config.", len(cmeshPeers))
		return status
	}

	// Role-aware ready gate. An origin-only node (role == "origin") has no inbound
	// worker duty — it only sends work — so it never serves the reverse (peer ->
	// this node) leg of the bidirectional smoke and must not be held at
	// peer_untrusted waiting for it. For an origin-only node, reaching the linked
	// state (peer linked + outbound reachable, already established above) IS ready.
	//
	// For worker/both (the default, and an empty role), 'ready' must reflect a
	// REAL, re-derived bidirectional smoke — not just the presence of a [peers.X]
	// block (which link_peer writes BEFORE running its smoke). Only here, in the
	// late-state branch, do we pay for the CreateTask round-trip; early polls
	// returned above without it. A nil smokeFn or a failing smoke yields
	// peer_untrusted, never a false ready. (J3: do NOT relax this for worker/both.)
	originOnly := strings.EqualFold(strings.TrimSpace(cfg.Node.Role), "origin")
	if !originOnly {
		smoke := bestPeerSmoke(ctx, smokeFn, linked)
		if !smoke.OK {
			// Three-way split: a reachable-but-policy-denied peer needs a fix on the
			// OTHER node's config (allowed_cwds / denied_profiles), not local trust,
			// so it gets its own state and next step. Reachable-but-not-OK without a
			// policy signal is an auth/trust gap; unreachable is a connectivity gap.
			switch {
			case smoke.PolicyDenied:
				status.State = StatePeerPolicyDenied
				status.NextStep = "The peer was REACHED but rejected the task on its own policy. Fix it on the OTHER node: add the worker cwd to its policy.allowed_cwds (and confirm the requested profile is not in denied_profiles), then restart its service. Re-run link_peer from here to confirm."
				status.Detail = fmt.Sprintf("%d cmesh peer(s) linked and reachable, but the smoke task was policy-denied by the remote: %s", len(linked), smoke.Detail)
			case smoke.Reachable:
				// Reached but the smoke did not complete both ways, with no forward
				// policy signal. Cause is genuinely ambiguous: it can be a trust gap on
				// the OTHER node (it does not list this node in allowed_peers), OR a
				// LOCAL limit on this node's reverse smoke (the reverse leg runs this
				// node as the worker against cwd "~", which this node's allowed_cwds may
				// not permit). Name both so the user checks the right machine.
				status.State = StatePeerUntrusted
				status.NextStep = "The peer was reached but the bidirectional smoke did not complete both ways. This can be a trust gap on the OTHER node (it must list THIS node in policy.allowed_peers and have run configure_node + start_service), OR a LOCAL policy limit (this node's reverse smoke runs in cwd \"~\" — ensure that is within THIS node's policy.allowed_cwds). Check both, then call link_peer again from here."
				status.Detail = fmt.Sprintf("%d cmesh peer(s) linked and reachable, but the bidirectional smoke did not complete: %s", len(linked), smoke.Detail)
			default:
				status.State = StatePeerUntrusted
				status.NextStep = "On THIS origin node, call link_peer again to re-establish trust with the linked cmesh peer (the bidirectional smoke did not pass). Confirm the OTHER node has run configure_node + start_service and is reachable on the tailnet."
				status.Detail = fmt.Sprintf("%d cmesh peer(s) linked in config, but the linked peer could not be reached: %s", len(linked), smoke.Detail)
			}
			return status
		}
	}

	status.State = StateReady
	status.NextStep = "Setup is verified. Call register_gateway to switch this agent onto the cmesh HTTP delegation gateway (one session restart); then try an example prompt below."
	status.Detail = ReadyPayoff(cfg, originOnly, linked)
	return status
}

// bestPeerSmoke runs the smoke against each linked peer and returns the most
// informative result. A passing smoke short-circuits immediately (linked peers are
// typically a single consented peer, so this is one round-trip in the common case).
// When none pass, it keeps the result that gives the user the most actionable next
// step, ranked: policy-denied (fix the remote's policy) > reachable (fix trust) >
// unreachable (fix connectivity). A nil smokeFn means "cannot verify" and yields a
// not-OK, unreachable result so the caller falls back to peer_untrusted.
func bestPeerSmoke(ctx context.Context, smokeFn SmokeFunc, linked []PeerCandidate) SmokeResult {
	if smokeFn == nil {
		return SmokeResult{Detail: "smoke not available (cannot verify trust)"}
	}
	var best SmokeResult
	for _, peer := range linked {
		res := smokeFn(ctx, peer)
		if res.OK {
			return res
		}
		if smokeRank(res) > smokeRank(best) {
			best = res
		}
	}
	return best
}

// smokeRank orders non-passing smoke results by how actionable they are, so
// bestPeerSmoke surfaces the result that points at the real fix.
func smokeRank(r SmokeResult) int {
	switch {
	case r.PolicyDenied:
		return 2
	case r.Reachable:
		return 1
	default:
		return 0
	}
}

// ReadyPayoff builds the Part-5 post-onboarding narration shown when the mesh is
// ready: the per-peer default profile (read from config), how to switch profiles,
// and example prompts templated with the real linked peer name(s). It is exported
// so the wizard can surface it directly and so it is unit-testable.
//
// originOnly selects the accurate readiness sentence: an origin-only node reaches
// ready on the linked-and-reachable signal (it never serves the reverse smoke),
// whereas worker/both reach ready only after a passing bidirectional smoke.
func ReadyPayoff(cfg *config.Config, originOnly bool, linked []PeerCandidate) string {
	defaultProfile := defaultRunnerProfile(cfg)
	var names []string
	for _, p := range linked {
		if n := cleanName(p.Name); n != "" {
			names = append(names, n)
		}
	}
	if len(names) == 0 {
		names = []string{"the peer"}
	}
	primary := names[0]

	var b strings.Builder
	if originOnly {
		b.WriteString("Origin node linked and reachable — the mesh is ready. ")
	} else {
		b.WriteString("Bidirectional smoke passed — the mesh is verified ready. ")
	}
	b.WriteString(fmt.Sprintf("Linked peer(s): %s.\n\n", strings.Join(names, ", ")))
	b.WriteString(fmt.Sprintf("Per-peer default profile: %q. Query is deterministic probes with no shell and no writes; use query_peer first for machine facts. Switch profiles only after reading the profile menu and, for repo-dev or higher, creating an acknowledged TTL lease.\n\n", defaultProfile))
	b.WriteString("Example prompts:\n")
	for _, ex := range readyExamplePrompts(primary) {
		b.WriteString("  - ")
		b.WriteString(ex)
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}

// defaultRunnerProfile reads the default profile configured for a runner
// (runners.<r>.default_profile, typically query). Falls back to "query". Runner names
// are visited in sorted order so the result is deterministic when more than one
// runner is configured (the common case is a single runner).
func defaultRunnerProfile(cfg *config.Config) string {
	names := make([]string, 0, len(cfg.Runners))
	for name := range cfg.Runners {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if p := strings.TrimSpace(cfg.Runners[name].DefaultProfile); p != "" {
			return p
		}
	}
	return "query"
}

// readyExamplePrompts returns example delegation prompts templated with the real
// linked peer name. These mirror the gateway's taskRecipes() (disk check, repo
// status, native build, service logs, test runner, branch handoff); the recipe set
// itself lives in the gateway package (unexported), so the user-facing example
// prompts are kept in sync here.
func readyExamplePrompts(peer string) []string {
	return []string{
		fmt.Sprintf("Use cmesh to ask %s to report disk usage for / (query).", peer),
		fmt.Sprintf("Use cmesh to ask %s for repository status in ~/code/myapp (query).", peer),
		fmt.Sprintf("Use cmesh to ask %s to summarize recent logs for a service (query-shell).", peer),
		fmt.Sprintf("Use cmesh to ask %s to run the native build in ~/code/myapp (repo-dev).", peer),
		fmt.Sprintf("Use cmesh to ask %s to run the test suite in ~/code/myapp (repo-dev).", peer),
		fmt.Sprintf("Use cmesh to ask %s to continue work on a branch in ~/code/myapp (repo-dev).", peer),
	}
}

func onboardingRecommendations() []profiledefs.Recommendation {
	return []profiledefs.Recommendation{
		profiledefs.Recommend("safe first look at a machine"),
		profiledefs.Recommend("read files"),
		profiledefs.Recommend("edit code"),
		profiledefs.Recommend("restart service"),
	}
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
// address/port) and grants the single consented peer trust. The peer's
// whois-matchable identity (Name/StableID, never the ephemeral discovery NodeID) is
// added to policy.outbound.allowed_peers (this node may send work to the peer) and,
// when grantInbound is true, ALSO to policy.allowed_peers (this node may accept work
// from the peer). It is idempotent.
//
// Per the amended trust model, linking a peer is an explicit, consented, single-peer
// trust grant in BOTH directions. The inbound grant is bounded: only the one
// consented peer's glob-escaped identity is added (never a wildcard, never any other
// node), and the peer remains constrained by this node's allowed_cwds and profile
// ceilings — inbound trust is "may ask", not "may do anything". grantInbound is
// role-aware: pass false for an origin-only node, which never takes the inbound
// grant (and never requires the reverse smoke for 'ready').
//
// stableID, when non-empty, is the peer's authoritative whois stable_id captured
// from its /v1/hello (see CapturePeerIdentity). It is added to the allowlists
// alongside the hostname so trust survives a hostname change. Pass "" to rely on the
// hostname label alone.
//
// Because the config file does not round-trip through the TOML encoder (Duration
// has no MarshalText), this performs targeted text edits that mirror the format
// GenerateConfig writes.
func AddPeerLink(configPath string, peer PeerCandidate, stableID string, grantInbound bool) (peerName string, err error) {
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
	// policy.outbound.allowed_peers (idempotent) — outbound: this node may send work
	// to the peer.
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

	// 2. When this node serves as a worker (role worker|both, the default), grant the
	// single consented peer INBOUND trust too (policy.allowed_peers) so the reverse
	// smoke leg (peer -> this node) can pass and the handshake closes. Bounded: only
	// the one peer's already-glob-escaped identity (and stable_id) is added, never a
	// wildcard or any other node.
	if grantInbound {
		body, err = ensureInboundAllow(body, identity)
		if err != nil {
			return "", err
		}
		if stableID != "" && stableID != identity {
			body, err = ensureInboundAllow(body, stableID)
			if err != nil {
				return "", err
			}
		}
	}

	// 3. Ensure a [peers.<name>] block exists (idempotent).
	body = ensurePeerSection(body, name, peer, identity)

	// 4. Self-heal: prune any OTHER [peers.*] section for the same physical peer
	// (matched by tailnet address) keyed on a superseded identity — e.g. a stale
	// entry an earlier buggy discovery wrote under a display name. The address is
	// the stable machine identifier; keep only the section we just wrote. Drop the
	// pruned sections' now-dead identities from the allowlists too, so re-linking a
	// peer whose name changed leaves no orphaned trust strings behind.
	if peer.Address != "" {
		var prunedIdentities []string
		body, prunedIdentities = prunePeerSectionsByAddress(body, name, peer.Address)
		for _, dead := range prunedIdentities {
			if dead == identity || dead == stableID {
				continue // never remove the identity/stable_id we just granted.
			}
			body = removeFromAllowlists(body, dead)
		}
	}

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

// ensureInboundAllow inserts identity into the [policy] allowed_peers array if
// absent, preserving the single-line array format. It targets the top-level
// [policy] section only (not [policy.outbound]). If the section/key is missing it is
// appended. This is the inbound grant: this node may accept work from the peer.
func ensureInboundAllow(body, identity string) (string, error) {
	lines := strings.Split(body, "\n")
	inPolicy := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			section := strings.Trim(trimmed, "[]")
			inPolicy = section == "policy"
			continue
		}
		if !inPolicy {
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
		// Preserve original indentation of the key line.
		indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
		lines[i] = indent + "allowed_peers = " + encodeStringArray(existing)
		return strings.Join(lines, "\n"), nil
	}
	// No [policy] allowed_peers found; append a section.
	appended := strings.TrimRight(body, "\n")
	appended += "\n\n[policy]\nallowed_peers = " + encodeStringArray([]string{identity}) + "\n"
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

// prunePeerSectionsByAddress removes every [peers.X] block whose `address` equals
// addr, EXCEPT the block named keepName. It returns the rewritten body and the
// node_id identities of the removed blocks (so their dead allowlist entries can be
// pruned too). A section runs from its "[peers.X]" header to the next table header
// or EOF. Sections without a matching address — and all non-peer tables — are left
// untouched.
func prunePeerSectionsByAddress(body, keepName, addr string) (string, []string) {
	lines := strings.Split(body, "\n")
	var out []string
	var removed []string
	i := 0
	for i < len(lines) {
		line := lines[i]
		trimmed := strings.TrimSpace(line)
		peerName, isPeerHeader := peerSectionName(trimmed)
		if !isPeerHeader {
			out = append(out, line)
			i++
			continue
		}
		// Collect the whole section [header .. next header / EOF).
		start := i
		j := i + 1
		for j < len(lines) {
			t := strings.TrimSpace(lines[j])
			if strings.HasPrefix(t, "[") && strings.HasSuffix(t, "]") {
				break
			}
			j++
		}
		section := lines[start:j]
		if peerName != keepName && sectionAddress(section) == addr {
			if id := sectionValue(section, "node_id"); id != "" {
				removed = append(removed, id)
			}
			// Drop the section (and a single trailing blank line, to avoid piling up
			// blank lines where the block used to be).
			if j < len(lines) && strings.TrimSpace(lines[j]) == "" {
				j++
			}
		} else {
			out = append(out, section...)
		}
		i = j
	}
	return strings.Join(out, "\n"), removed
}

// peerSectionName returns the X in a "[peers.X]" header line and true, or "", false.
func peerSectionName(trimmed string) (string, bool) {
	if !strings.HasPrefix(trimmed, "[") || !strings.HasSuffix(trimmed, "]") {
		return "", false
	}
	inner := strings.Trim(trimmed, "[]")
	name, ok := strings.CutPrefix(inner, "peers.")
	if !ok || name == "" {
		return "", false
	}
	return name, true
}

func sectionAddress(section []string) string {
	return sectionValue(section, "address")
}

// sectionValue returns the decoded string value of key within a section's lines,
// or "" if absent.
func sectionValue(section []string, key string) string {
	for _, line := range section {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok || strings.TrimSpace(k) != key {
			continue
		}
		var s string
		if err := json.Unmarshal([]byte(strings.TrimSpace(v)), &s); err == nil {
			return s
		}
		return strings.Trim(strings.TrimSpace(v), `"`)
	}
	return ""
}

// removeFromAllowlists strips identity from both the [policy] and [policy.outbound]
// allowed_peers arrays. Used to clear dead trust strings left by a superseded peer
// identity. The current (correct) identities are never passed here.
func removeFromAllowlists(body, identity string) string {
	lines := strings.Split(body, "\n")
	section := ""
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			section = strings.Trim(trimmed, "[]")
			continue
		}
		if section != "policy" && section != "policy.outbound" {
			continue
		}
		key, _, ok := strings.Cut(trimmed, "=")
		if !ok || strings.TrimSpace(key) != "allowed_peers" {
			continue
		}
		existing := parseStringArray(trimmed)
		var kept []string
		changed := false
		for _, e := range existing {
			if e == identity {
				changed = true
				continue
			}
			kept = append(kept, e)
		}
		if !changed {
			continue
		}
		indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
		lines[i] = indent + "allowed_peers = " + encodeStringArray(kept)
	}
	return strings.Join(lines, "\n")
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
