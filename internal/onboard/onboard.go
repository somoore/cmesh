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
	var raw struct {
		Self tailnetPeer            `json:"Self"`
		Peer map[string]tailnetPeer `json:"Peer"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("parse tailscale status --json: %w", err)
	}
	selfName := firstNonEmpty(raw.Self.HostName, firstLabel(raw.Self.DNSName), firstLabel(raw.Self.Name))
	var peers []PeerCandidate
	for _, peer := range raw.Peer {
		name := firstNonEmpty(peer.HostName, firstLabel(peer.DNSName), firstLabel(peer.Name))
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
		if probeCmesh && candidate.Address != "" && candidate.Online {
			candidate.CmeshCapable, candidate.Detail = probeMesh(ctx, candidate.Address, meshPort)
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
	runnerName, runnerCommand := chooseRunner(opts.Runner, runners)
	if runnerName == "" {
		return "", errors.New("no supported runner found; install Claude Code or Codex, or pass --runner")
	}
	cwds := append([]string(nil), opts.AllowedCWDs...)
	if len(cwds) == 0 {
		cwds = SuggestedCWDs()
	}
	allowedPeers := append([]string(nil), opts.AllowedPeers...)
	selectedPeers := selectPeers(opts.PeerNames, peers)
	if len(allowedPeers) == 0 {
		for _, peer := range selectedPeers {
			allowedPeers = append(allowedPeers, matchableIdentity(peer))
		}
	}
	if len(allowedPeers) == 0 {
		allowedPeers = []string{firstNonEmpty(machine.NodeID, machine.Name, "self")}
	}
	profileAllowedPeers := profilePeerScope(role, allowedPeers, peerNames(selectedPeers))
	profiles := opts.Profiles
	if len(profiles) == 0 {
		profiles = []string{"readonly", "dev"}
	}
	nodeName := cleanName(firstNonEmpty(machine.Name, "cmesh-node"))
	nodeID := firstNonEmpty(machine.NodeID, nodeName)
	hostToken, err := randomToken()
	if err != nil {
		return "", err
	}
	var b strings.Builder
	writeSection(&b, "node")
	writeKV(&b, "id", nodeID)
	writeKV(&b, "name", nodeName)
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
		writeArray(&b, "allowed_peers", peerNames(selectedPeers))
	}
	writeArray(&b, "denied_profiles", []string{"network", "interactive", "git_push"})
	writeBool(&b, "allow_subdelegation", false)
	for _, profile := range profiles {
		switch profile {
		case "readonly":
			writeProfile(&b, "readonly", runnerName, []string{"read_files"}, nil, false, false, false, false, "30m")
		case "dev":
			writeProfile(&b, "dev", runnerName, []string{"read_files", "edit_repo", "execute_repo_commands"}, nil, false, false, false, false, "2h")
		case "network":
			writeProfile(&b, "network", runnerName, []string{"read_files", "edit_repo", "execute_repo_commands", "network"}, profileAllowedPeers, true, false, true, false, "2h")
		case "interactive":
			writeProfile(&b, "interactive", runnerName, []string{"read_files", "edit_repo", "execute_repo_commands", "network", "launch_processes", "desktop_interaction"}, profileAllowedPeers, true, false, true, false, "30m")
		case "git_push":
			writeProfile(&b, "git_push", runnerName, []string{"read_files", "edit_repo", "execute_repo_commands", "git_push"}, profileAllowedPeers, true, false, false, true, "2h")
		}
	}
	writeSection(&b, "runners."+runnerName)
	writeArray(&b, "command", runnerCommand)
	writeKV(&b, "default_profile", "dev")
	writeInt(&b, "concurrency", 1)
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

func writeProfile(b *strings.Builder, name, runner string, claims, allowedPeers []string, requireExplicit, bare, network, gitPush bool, maxRuntime string) {
	writeSection(b, "profiles."+name)
	writeKV(b, "runner", runner)
	writeArray(b, "claims", claims)
	if len(allowedPeers) > 0 {
		writeArray(b, "allowed_peers", allowedPeers)
	}
	if requireExplicit {
		writeBool(b, "require_explicit", true)
	}
	writeBool(b, "bare", bare)
	writeBool(b, "network", network)
	writeBool(b, "git_push", gitPush)
	writeKV(b, "max_runtime", maxRuntime)
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
