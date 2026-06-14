package onboard

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"cmesh/internal/config"
	tsid "cmesh/internal/identity/tailscale"
	"cmesh/internal/mesh/schema"
	profiledefs "cmesh/internal/profile"
)

type Machine struct {
	Name        string   `json:"name"`
	NodeID      string   `json:"node_id,omitempty"`
	DNSName     string   `json:"dns_name,omitempty"`
	TailnetIPs  []string `json:"tailnet_ips,omitempty"`
	TailscaleOK bool     `json:"tailscale_ok"`
	Detail      string   `json:"detail,omitempty"`
}

type RunnerStatus struct {
	Name    string   `json:"name"`
	Command []string `json:"command"`
	Found   bool     `json:"found"`
	Detail  string   `json:"detail,omitempty"`
}

type PeerCandidate struct {
	Name         string `json:"name"`
	NodeID       string `json:"node_id,omitempty"`
	DNSName      string `json:"dns_name,omitempty"`
	Address      string `json:"address,omitempty"`
	Online       bool   `json:"online"`
	CmeshCapable bool   `json:"cmesh_capable,omitempty"`
	Detail       string `json:"detail,omitempty"`
}

type InitOptions struct {
	Role         string
	ConfigPath   string
	Yes          bool
	Connect      bool
	AllowedPeers []string
	AllowedCWDs  []string
	PeerNames    []string
	Runner       string
	Profiles     []string
	// HostToken, when set, is written verbatim into [gateway].host_token. Leave
	// empty to mint a fresh token. Callers re-running configure_node must read the
	// existing config's token and pass it here so the user is not locked out.
	HostToken string
}

type Status struct {
	ConfigPath string          `json:"config_path"`
	ConfigOK   bool            `json:"config_ok"`
	ConfigErr  string          `json:"config_error,omitempty"`
	Machine    Machine         `json:"machine"`
	Runners    []RunnerStatus  `json:"runners"`
	Peers      []PeerCandidate `json:"tailnet_peers,omitempty"`
}

func Detect(ctx context.Context, configPath string, probePeers bool) Status {
	if configPath == "" {
		configPath = config.DefaultPath()
	}
	out := Status{
		ConfigPath: configPath,
		Machine:    DetectMachine(ctx),
		Runners:    DetectRunners(ctx),
	}
	if _, err := config.Load(configPath); err != nil {
		out.ConfigErr = err.Error()
	} else {
		out.ConfigOK = true
	}
	if probePeers {
		peers, err := TailnetPeers(ctx, config.DefaultMeshPort, true)
		if err == nil {
			out.Peers = peers
		}
	}
	return out
}

func DetectMachine(ctx context.Context) Machine {
	host, _ := os.Hostname()
	machine := Machine{Name: host}
	resolver := tsid.NewResolver()
	st, err := resolver.Status(ctx)
	if err != nil {
		machine.Detail = err.Error()
		return machine
	}
	machine.TailscaleOK = strings.EqualFold(st.BackendState, "Running")
	machine.TailnetIPs = append([]string(nil), st.TailscaleIPs...)
	machine.DNSName = st.DNSName
	if st.NodeName != "" {
		machine.Name = st.NodeName
	}
	if self, err := resolver.Self(ctx); err == nil {
		machine.NodeID = firstNonEmpty(self.NodeID, self.StableID, self.NodeName)
		machine.Name = firstNonEmpty(self.NodeName, machine.Name)
		machine.DNSName = firstNonEmpty(self.DNSName, machine.DNSName)
	}
	if !machine.TailscaleOK {
		machine.Detail = "Tailscale backend state: " + st.BackendState
	}
	return machine
}

func DetectRunners(ctx context.Context) []RunnerStatus {
	runners := []RunnerStatus{
		detectRunner(ctx, "claude-code", []string{"claude", "-p"}, "claude", "--version"),
		detectRunner(ctx, "codex", []string{"codex"}, "codex", "--version"),
	}
	sort.Slice(runners, func(i, j int) bool {
		if runners[i].Found != runners[j].Found {
			return runners[i].Found
		}
		return runners[i].Name < runners[j].Name
	})
	return runners
}

func TailnetPeers(ctx context.Context, meshPort int, probeCmesh bool) ([]PeerCandidate, error) {
	out, err := exec.CommandContext(ctx, "tailscale", "status", "--json").Output()
	if err != nil {
		return nil, fmt.Errorf("tailscale status --json: %w", err)
	}
	peers, err := parseTailnetPeers(out)
	if err != nil {
		return nil, err
	}
	if probeCmesh {
		for i := range peers {
			if peers[i].Address != "" && peers[i].Online {
				peers[i].CmeshCapable, peers[i].Detail = probeMesh(ctx, peers[i].Address, meshPort)
			}
		}
	}
	return peers, nil
}

// parseTailnetPeers turns `tailscale status --json` output into PeerCandidates.
// Pure (no network): cmesh probing is layered on by the caller. Split out so the
// whois-matchable name derivation is unit-testable.
func parseTailnetPeers(statusJSON []byte) ([]PeerCandidate, error) {
	var raw struct {
		Self tailnetPeer            `json:"Self"`
		Peer map[string]tailnetPeer `json:"Peer"`
	}
	if err := json.Unmarshal(statusJSON, &raw); err != nil {
		return nil, fmt.Errorf("parse tailscale status --json: %w", err)
	}
	// PeerCandidate.Name feeds the trust allowlist (matchableIdentity), so it MUST
	// be the whois-matchable identity. tailscale whois reports the DNS hostname
	// label (e.g. "scotts-macbook-pro"), NOT HostName, which is the human display
	// name and can contain spaces/apostrophes (e.g. "Scott's MacBook Pro"). Keying
	// on HostName writes an allowlist entry that never matches the peer's
	// authenticated identity. Prefer the DNS first label; fall back to HostName
	// only when no DNS name is available.
	selfName := firstNonEmpty(firstLabel(raw.Self.DNSName), firstLabel(raw.Self.Name), raw.Self.HostName)
	var peers []PeerCandidate
	for _, peer := range raw.Peer {
		name := firstNonEmpty(firstLabel(peer.DNSName), firstLabel(peer.Name), peer.HostName)
		if name == "" || name == selfName {
			continue
		}
		candidate := PeerCandidate{
			Name:    name,
			NodeID:  firstNonEmpty(peer.ID, peer.NodeID, peer.StableID, name),
			DNSName: strings.TrimSuffix(firstNonEmpty(peer.DNSName, peer.Name), "."),
			Online:  peer.Online,
		}
		if len(peer.TailscaleIPs) > 0 {
			candidate.Address = peer.TailscaleIPs[0]
		}
		peers = append(peers, candidate)
	}
	sort.Slice(peers, func(i, j int) bool { return peers[i].Name < peers[j].Name })
	return peers, nil
}

func SuggestedCWDs() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return []string{"~/code/**"}
	}
	candidates := []string{"~/code/**", "~/work/**"}
	var existing []string
	for _, candidate := range candidates {
		dir := strings.TrimSuffix(candidate, "/**")
		expanded := filepath.Join(home, strings.TrimPrefix(dir, "~/"))
		if info, err := os.Stat(expanded); err == nil && info.IsDir() {
			existing = append(existing, candidate)
		}
	}
	if len(existing) > 0 {
		return existing
	}
	return candidates[:1]
}

func GenerateConfig(opts InitOptions, machine Machine, runners []RunnerStatus, peers []PeerCandidate) (string, error) {
	role := firstNonEmpty(opts.Role, "both")
	if role != "origin" && role != "worker" && role != "both" {
		return "", fmt.Errorf("role must be origin, worker, or both")
	}
	cwds := append([]string(nil), opts.AllowedCWDs...)
	if len(cwds) == 0 {
		cwds = SuggestedCWDs()
	}
	profiles := opts.Profiles
	if len(profiles) == 0 {
		profiles = []string{profiledefs.Query}
	}
	runnerName, runnerCommand := chooseRunner(opts.Runner, runners)
	needsAgent := profilesNeedAgent(profiles)
	if needsAgent && runnerName == "" {
		return "", errors.New("agent-backed profiles require Claude Code or Codex; install one or choose query-only")
	}
	allowedPeers := append([]string(nil), opts.AllowedPeers...)
	selectedPeers := selectPeers(opts.PeerNames, peers)
	if len(allowedPeers) == 0 {
		for _, peer := range selectedPeers {
			allowedPeers = append(allowedPeers, matchableIdentity(peer))
		}
	}
	// B-SEC1: peer identities are self-reported (tailscale HostName/DNSName).
	// A peer advertising a glob metachar (e.g. "*" or "[a-z]*") would become a
	// wildcard once policy.matchesAny feeds it to filepath.Match, granting
	// inbound/outbound trust to unrelated identities across the tailnet. Drop
	// any identity carrying glob metacharacters from every allowlist before it
	// can reach the generated config. Done before the empty-check fallback so a
	// fully-rejected list safely degrades to self-only trust.
	allowedPeers = dropGlobMetachars(allowedPeers)
	if len(allowedPeers) == 0 {
		allowedPeers = []string{firstNonEmpty(machine.NodeID, machine.Name, "self")}
	}
	outboundPeers := dropGlobMetachars(peerNames(selectedPeers))
	profileAllowedPeers := profilePeerScope(role, allowedPeers, outboundPeers)
	nodeName := cleanName(firstNonEmpty(machine.Name, "cmesh-node"))
	nodeID := firstNonEmpty(machine.NodeID, nodeName)
	hostToken := strings.TrimSpace(opts.HostToken)
	if hostToken == "" {
		var err error
		hostToken, err = randomToken()
		if err != nil {
			return "", err
		}
	}
	var b strings.Builder
	writeSection(&b, "node")
	writeKV(&b, "id", nodeID)
	writeKV(&b, "name", nodeName)
	// Persist the role so DeriveOnboardingState can re-derive that an origin-only
	// node reaches 'ready' without the reverse worker-side smoke it never serves.
	writeKV(&b, "role", role)
	writeInt(&b, "mesh_port", config.DefaultMeshPort)
	writeInt(&b, "mcp_port", config.DefaultMCPPort)
	writeKV(&b, "data_dir", "~/.local/share/cmesh")
	writeSection(&b, "tailscale")
	writeKV(&b, "mode", "host")
	writeKV(&b, "bind", "tailnet_ip")
	writeSection(&b, "policy")
	writeArray(&b, "allowed_peers", allowedPeers)
	writeArray(&b, "allowed_cwds", cwds)
	writeInt(&b, "hop_limit", 2)
	writeSection(&b, "policy.outbound")
	if role == "worker" {
		writeArray(&b, "allowed_peers", nil)
	} else {
		writeArray(&b, "allowed_peers", outboundPeers)
	}
	writeArray(&b, "denied_profiles", []string{"network", "interactive", "git_push"})
	writeBool(&b, "allow_subdelegation", false)
	runnersToWrite := map[string][]string{}
	for _, profileName := range profiles {
		switch profileName {
		case profiledefs.Query:
			writeProfile(&b, profiledefs.Query, profiledefs.RunnerProbe, "Deterministic read-only machine queries. No shell, no writes.", []string{"query"}, []string{profiledefs.CapDiskInfo, profiledefs.CapSystemInfo, profiledefs.CapRepoStatus}, nil, false, false, false, "deny", "deny", false, false, "30s", 65536, "normal", "high")
			runnersToWrite[profiledefs.RunnerProbe] = []string{"builtin"}
		case profiledefs.QueryNetwork:
			writeProfile(&b, profiledefs.QueryNetwork, profiledefs.RunnerProbe, "Deterministic read-only queries plus bounded network checks.", []string{"query"}, []string{profiledefs.CapDiskInfo, profiledefs.CapSystemInfo, profiledefs.CapRepoStatus, profiledefs.CapNetworkCheck}, nil, true, false, false, "deny", "deny", true, false, "30s", 65536, "loud", "high")
			runnersToWrite[profiledefs.RunnerProbe] = []string{"builtin"}
		case profiledefs.QueryShell:
			writeProfile(&b, profiledefs.QueryShell, profiledefs.RunnerShell, "Allowlisted read-oriented diagnostics. No writes.", []string{"query"}, []string{profiledefs.CapShellReadonly, profiledefs.CapServiceStatus, profiledefs.CapProcessList, profiledefs.CapLogTail}, nil, true, false, false, "deny", "allowlisted", false, false, "30s", 65536, "loud", "medium")
			writeShellPolicy(&b, profiledefs.QueryShell)
			runnersToWrite[profiledefs.RunnerShell] = []string{"builtin"}
		case profiledefs.ReadonlyAgent:
			writeProfile(&b, profiledefs.ReadonlyAgent, runnerName, "Agent-assisted read-only inspection. Prefer query when deterministic probes are enough.", []string{"inspect"}, []string{profiledefs.CapReadFiles}, nil, false, false, false, "deny", "deny", false, false, "30m", 1<<20, "normal", "medium")
			runnersToWrite[runnerName] = runnerCommand
		case profiledefs.LegacyReadonly:
			writeProfile(&b, profiledefs.LegacyReadonly, runnerName, "Agent-assisted read-only inspection. Prefer query when deterministic probes are enough.", []string{"inspect"}, []string{profiledefs.CapReadFiles}, nil, false, false, false, "deny", "deny", false, false, "30m", 1<<20, "normal", "medium")
			runnersToWrite[runnerName] = runnerCommand
		case profiledefs.RepoDev, profiledefs.LegacyDev:
			writeProfile(&b, profiledefs.RepoDev, runnerName, "Agent-assisted repo-local edits and test commands inside allowed cwd.", []string{"build", "modify"}, []string{profiledefs.CapReadFiles, profiledefs.CapEditRepo, profiledefs.CapExecuteInRepo}, profileAllowedPeers, true, true, false, "repo_only", "allow", false, false, "2h", 1<<20, "loud", "medium")
			runnersToWrite[runnerName] = runnerCommand
		case profiledefs.DesktopControl:
			writeProfile(&b, profiledefs.DesktopControl, runnerName, "High-risk desktop/session control profile.", []string{"desktop"}, []string{profiledefs.CapReadFiles, profiledefs.CapDesktopControl, profiledefs.CapDesktopInteraction}, profileAllowedPeers, true, true, false, "allow", "allow", true, false, "30m", 1<<20, "loud", "medium")
			runnersToWrite[runnerName] = runnerCommand
		case profiledefs.SystemChange:
			writeProfile(&b, profiledefs.SystemChange, runnerName, "High-risk system/service/package changes profile.", []string{"system"}, []string{profiledefs.CapReadFiles, profiledefs.CapSystemChange, profiledefs.CapLaunchProcesses}, profileAllowedPeers, true, true, false, "allow", "allow", true, false, "30m", 1<<20, "loud", "medium")
			runnersToWrite[runnerName] = runnerCommand
		case "network":
			writeProfile(&b, "network", runnerName, "Agent-assisted repo work with network access.", []string{"build", "modify"}, []string{profiledefs.CapReadFiles, profiledefs.CapEditRepo, profiledefs.CapExecuteInRepo, profiledefs.CapNetworkCheck}, profileAllowedPeers, true, true, false, "repo_only", "allow", true, false, "2h", 1<<20, "loud", "medium")
			runnersToWrite[runnerName] = runnerCommand
		case "interactive":
			writeProfile(&b, "interactive", runnerName, "High-risk desktop/session control profile.", []string{"desktop"}, []string{profiledefs.CapReadFiles, profiledefs.CapEditRepo, profiledefs.CapExecuteInRepo, profiledefs.CapNetworkCheck, profiledefs.CapDesktopControl}, profileAllowedPeers, true, true, false, "allow", "allow", true, false, "30m", 1<<20, "loud", "medium")
			runnersToWrite[runnerName] = runnerCommand
		case "git_push":
			writeProfile(&b, "git_push", runnerName, "High-risk repo profile that can push git branches.", []string{"modify"}, []string{profiledefs.CapReadFiles, profiledefs.CapEditRepo, profiledefs.CapExecuteInRepo, profiledefs.CapGitPush}, profileAllowedPeers, true, true, false, "repo_only", "allow", false, true, "2h", 1<<20, "loud", "medium")
			runnersToWrite[runnerName] = runnerCommand
		}
	}
	for runName, command := range runnersToWrite {
		if runName == "" {
			continue
		}
		writeSection(&b, "runners."+runName)
		writeArray(&b, "command", command)
		writeKV(&b, "default_profile", defaultProfileForRunner(runName, profiles))
		writeInt(&b, "concurrency", 1)
	}
	writeSection(&b, "gateway")
	writeKV(&b, "host_token", hostToken)
	for _, peer := range selectedPeers {
		if peer.Address == "" {
			continue
		}
		writeSection(&b, "peers."+cleanName(peer.Name))
		writeKV(&b, "name", peer.Name)
		writeKV(&b, "node_id", matchableIdentity(peer))
		writeKV(&b, "address", peer.Address)
		writeInt(&b, "port", config.DefaultMeshPort)
	}
	return b.String(), nil
}

func WriteConfig(path, body string, overwrite bool) error {
	if path == "" {
		path = config.DefaultPath()
	}
	if !overwrite {
		if _, err := os.Stat(path); err == nil {
			return fmt.Errorf("%s already exists; pass --force to replace it", path)
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(body), 0o600)
}

func ValidateConfig(path string) error {
	_, err := config.Load(path)
	return err
}

type tailnetPeer struct {
	ID           string   `json:"ID"`
	NodeID       string   `json:"NodeID"`
	StableID     string   `json:"StableID"`
	Name         string   `json:"Name"`
	HostName     string   `json:"HostName"`
	DNSName      string   `json:"DNSName"`
	TailscaleIPs []string `json:"TailscaleIPs"`
	Online       bool     `json:"Online"`
}

func detectRunner(ctx context.Context, name string, command []string, binary string, versionArgs ...string) RunnerStatus {
	status := RunnerStatus{Name: name, Command: append([]string(nil), command...)}
	path, err := exec.LookPath(binary)
	if err != nil {
		status.Detail = "not found on PATH"
		return status
	}
	status.Found = true
	status.Detail = path
	if len(status.Command) > 0 {
		status.Command[0] = path
	}
	if len(versionArgs) > 0 {
		ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, binary, versionArgs...)
		out, err := cmd.CombinedOutput()
		if err == nil && len(bytes.TrimSpace(out)) > 0 {
			status.Detail = strings.TrimSpace(string(out))
		}
	}
	return status
}

func chooseRunner(wanted string, runners []RunnerStatus) (string, []string) {
	wanted = strings.TrimSpace(wanted)
	for _, runner := range runners {
		if wanted != "" && wanted != "auto" && runner.Name != wanted {
			continue
		}
		if runner.Found || wanted == runner.Name {
			return runner.Name, runner.Command
		}
	}
	if wanted == "claude-code" {
		return "claude-code", []string{"claude", "-p"}
	}
	if wanted == "codex" {
		return "codex", []string{"codex"}
	}
	return "", nil
}

func profilesNeedAgent(profiles []string) bool {
	for _, name := range profiles {
		switch name {
		case profiledefs.Query, profiledefs.QueryNetwork, profiledefs.QueryShell:
			continue
		default:
			return true
		}
	}
	return false
}

func defaultProfileForRunner(runnerName string, profiles []string) string {
	if runnerName == profiledefs.RunnerProbe {
		for _, preferred := range []string{profiledefs.Query, profiledefs.QueryNetwork} {
			for _, name := range profiles {
				if name == preferred {
					return preferred
				}
			}
		}
		return profiledefs.Query
	}
	if runnerName == profiledefs.RunnerShell {
		return profiledefs.QueryShell
	}
	for _, preferred := range []string{profiledefs.RepoDev, profiledefs.LegacyReadonly, "network", "interactive", "git_push"} {
		for _, name := range profiles {
			if name == preferred || (preferred == profiledefs.RepoDev && name == profiledefs.LegacyDev) {
				return preferred
			}
		}
	}
	for _, name := range profiles {
		if name != profiledefs.Query {
			return name
		}
	}
	return profiledefs.Query
}

func selectPeers(names []string, peers []PeerCandidate) []PeerCandidate {
	if len(names) == 0 {
		var capable []PeerCandidate
		for _, peer := range peers {
			if peer.CmeshCapable {
				capable = append(capable, peer)
			}
		}
		return capable
	}
	wanted := map[string]bool{}
	for _, name := range names {
		wanted[strings.ToLower(strings.TrimSpace(name))] = true
	}
	var selected []PeerCandidate
	for _, peer := range peers {
		if wanted[strings.ToLower(peer.Name)] || wanted[strings.ToLower(peer.NodeID)] {
			selected = append(selected, peer)
			delete(wanted, strings.ToLower(peer.Name))
			delete(wanted, strings.ToLower(peer.NodeID))
		}
	}
	for name := range wanted {
		selected = append(selected, PeerCandidate{Name: name, NodeID: name})
	}
	return selected
}

func peerNames(peers []PeerCandidate) []string {
	names := make([]string, 0, len(peers))
	for _, peer := range peers {
		names = append(names, peer.Name)
	}
	return names
}

func profilePeerScope(role string, allowedPeers, outboundPeers []string) []string {
	switch role {
	case "origin":
		return uniqueNonEmpty(outboundPeers)
	case "worker":
		return uniqueNonEmpty(allowedPeers)
	default:
		return uniqueNonEmpty(append(append([]string(nil), allowedPeers...), outboundPeers...))
	}
}

// dropGlobMetachars drops any value containing a filepath.Match metacharacter
// (* ? [ ]). Such values must never reach an allowlist that policy.matchesAny
// evaluates with filepath.Match, since they would match unrelated peer
// identities. Rejecting (rather than escaping) keeps setup output predictable:
// an operator can see the entry was dropped, and policy still does an exact
// `contains` check for legitimate literal identities.
func dropGlobMetachars(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if strings.ContainsAny(value, "*?[]") {
			continue
		}
		out = append(out, value)
	}
	return out
}

func uniqueNonEmpty(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

// matchableIdentity returns the whois-matchable identity for a discovered peer.
// Trust allowlists must key on the stable, whois-matchable hostname label
// (PeerCandidate.Name), not the ephemeral discovery node id (peer.ID from
// tailscale status --json), which does not match a peer's whois identity.
func matchableIdentity(peer PeerCandidate) string {
	return firstNonEmpty(peer.Name, peer.NodeID)
}

// CapturePeerIdentity fetches a peer's authoritative, whois-matchable identity
// from its /v1/hello endpoint, returning the node_name and stable_id the peer
// reports for itself. Reachability is discovered separately; this records the
// identity used for inbound allowlist matching.
func CapturePeerIdentity(ctx context.Context, address string, meshPort int) (name, stableID string, err error) {
	if meshPort == 0 {
		meshPort = config.DefaultMeshPort
	}
	ctx, cancel := context.WithTimeout(ctx, 700*time.Millisecond)
	defer cancel()
	url := "http://" + net.JoinHostPort(address, fmt.Sprintf("%d", meshPort)) + "/v1/hello"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return "", "", fmt.Errorf("/v1/hello returned %s", resp.Status)
	}
	var hello schema.HelloResponse
	if err := json.NewDecoder(resp.Body).Decode(&hello); err != nil {
		return "", "", fmt.Errorf("decode /v1/hello: %w", err)
	}
	return hello.NodeName, hello.StableID, nil
}

func probeMesh(ctx context.Context, address string, port int) (bool, string) {
	if port == 0 {
		port = config.DefaultMeshPort
	}
	ctx, cancel := context.WithTimeout(ctx, 700*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+net.JoinHostPort(address, fmt.Sprintf("%d", port))+"/v1/hello", nil)
	if err != nil {
		return false, err.Error()
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, err.Error()
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return false, resp.Status
	}
	return true, "cmesh /v1/hello responded"
}

func randomToken() (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

func writeProfile(b *strings.Builder, name, runner, description string, intents, capabilities, allowedPeers []string, requireExplicit, requireLease, bare bool, writes, shell string, network, gitPush bool, maxRuntime string, maxOutputBytes int64, auditLevel, confidence string) {
	writeSection(b, "profiles."+name)
	writeKV(b, "description", description)
	writeKV(b, "runner", runner)
	writeArray(b, "intents", intents)
	writeArray(b, "capabilities", capabilities)
	writeArray(b, "claims", capabilities)
	if len(allowedPeers) > 0 {
		writeArray(b, "allowed_peers", allowedPeers)
	}
	if requireExplicit {
		writeBool(b, "require_explicit", true)
	}
	if requireLease {
		writeBool(b, "require_lease", true)
	}
	writeBool(b, "bare", bare)
	writeKV(b, "writes", writes)
	writeKV(b, "shell", shell)
	writeBool(b, "network", network)
	writeBool(b, "git_push", gitPush)
	writeKV(b, "max_runtime", maxRuntime)
	if maxOutputBytes > 0 {
		writeInt64(b, "max_output_bytes", maxOutputBytes)
	}
	writeKV(b, "audit_level", auditLevel)
	writeKV(b, "enforcement_confidence", confidence)
}

func writeShellPolicy(b *strings.Builder, profileName string) {
	tmpl, ok := profiledefs.TemplateByName(profileName)
	if !ok || tmpl.ShellPolicy.Mode == "" {
		return
	}
	writeSection(b, "profiles."+profileName+".shell_policy")
	writeKV(b, "mode", tmpl.ShellPolicy.Mode)
	writeArray(b, "denied_patterns", tmpl.ShellPolicy.DeniedPatterns)
	keys := make([]string, 0, len(tmpl.ShellPolicy.AllowedCommands))
	for osName := range tmpl.ShellPolicy.AllowedCommands {
		keys = append(keys, osName)
	}
	sort.Strings(keys)
	for _, osName := range keys {
		writeSection(b, "profiles."+profileName+".shell_policy."+osName)
		writeArray(b, "allowed_commands", tmpl.ShellPolicy.AllowedCommands[osName])
	}
}

func writeSection(b *strings.Builder, section string) {
	if b.Len() > 0 {
		b.WriteByte('\n')
	}
	b.WriteString("[")
	b.WriteString(section)
	b.WriteString("]\n")
}

func writeKV(b *strings.Builder, key, value string) {
	encoded, _ := json.Marshal(value)
	b.WriteString(key)
	b.WriteString(" = ")
	b.Write(encoded)
	b.WriteByte('\n')
}

func writeInt(b *strings.Builder, key string, value int) {
	b.WriteString(key)
	b.WriteString(" = ")
	b.WriteString(fmt.Sprintf("%d", value))
	b.WriteByte('\n')
}

func writeInt64(b *strings.Builder, key string, value int64) {
	b.WriteString(key)
	b.WriteString(" = ")
	b.WriteString(fmt.Sprintf("%d", value))
	b.WriteByte('\n')
}

func writeBool(b *strings.Builder, key string, value bool) {
	b.WriteString(key)
	b.WriteString(" = ")
	if value {
		b.WriteString("true\n")
	} else {
		b.WriteString("false\n")
	}
}

func writeArray(b *strings.Builder, key string, values []string) {
	b.WriteString(key)
	b.WriteString(" = [")
	for i, value := range values {
		if i > 0 {
			b.WriteString(", ")
		}
		encoded, _ := json.Marshal(value)
		b.Write(encoded)
	}
	b.WriteString("]\n")
}

func cleanName(name string) string {
	name = strings.TrimSpace(strings.ToLower(name))
	if name == "" {
		return "cmesh-node"
	}
	var b strings.Builder
	lastDash := false
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	cleaned := strings.Trim(b.String(), "-")
	if cleaned == "" {
		return "cmesh-node"
	}
	return cleaned
}

func firstLabel(name string) string {
	name = strings.TrimSuffix(name, ".")
	if i := strings.IndexByte(name, '.'); i >= 0 {
		return name[:i]
	}
	return name
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}
