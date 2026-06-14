package onboard

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"cmesh/internal/config"
	tsid "cmesh/internal/identity/tailscale"
	"cmesh/internal/mesh/schema"
	"cmesh/internal/policy"
)

func TestGenerateConfigProducesValidOpinionatedDefaults(t *testing.T) {
	body, err := GenerateConfig(InitOptions{
		Role:         "worker",
		AllowedPeers: []string{"mac-studio"},
		AllowedCWDs:  []string{"~/code/**"},
		Runner:       "codex",
	}, Machine{Name: "homelab-vm", NodeID: "homelab-vm"}, []RunnerStatus{
		{Name: "codex", Command: []string{"codex"}, Found: true},
	}, nil)
	if err != nil {
		t.Fatalf("GenerateConfig: %v", err)
	}
	if !strings.Contains(body, `[profiles.readonly]`) || !strings.Contains(body, `[profiles.dev]`) {
		t.Fatalf("expected readonly and dev profiles:\n%s", body)
	}
	path := t.TempDir() + "/config.toml"
	if err := WriteConfig(path, body, false); err != nil {
		t.Fatalf("WriteConfig: %v", err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("generated config should load: %v\n%s", err, body)
	}
	if cfg.Node.Name != "homelab-vm" {
		t.Fatalf("node name = %q", cfg.Node.Name)
	}
	if cfg.Profiles["dev"].Runner != "codex" {
		t.Fatalf("dev runner = %q", cfg.Profiles["dev"].Runner)
	}
	if cfg.Profiles["readonly"].Bare {
		t.Fatal("readonly profile should use the configured runner credentials by default")
	}
	if _, ok := cfg.Profiles["interactive"]; ok {
		t.Fatal("interactive profile should not be enabled by default")
	}
}

func TestGenerateConfigInteractiveProfileIsExplicitAndPeerScoped(t *testing.T) {
	body, err := GenerateConfig(InitOptions{
		Role:         "worker",
		AllowedPeers: []string{"mac-studio"},
		AllowedCWDs:  []string{"~/code/**"},
		Runner:       "codex",
		Profiles:     []string{"readonly", "dev", "interactive"},
	}, Machine{Name: "win11-desktop", NodeID: "win11-desktop"}, []RunnerStatus{
		{Name: "codex", Command: []string{"codex"}, Found: true},
	}, nil)
	if err != nil {
		t.Fatalf("GenerateConfig: %v", err)
	}
	if !strings.Contains(body, `[profiles.interactive]`) {
		t.Fatalf("expected explicit interactive profile:\n%s", body)
	}
	if !strings.Contains(body, `denied_profiles = ["network", "interactive", "git_push"]`) {
		t.Fatalf("expected high-risk profiles to stay outbound-denied by default:\n%s", body)
	}
	path := t.TempDir() + "/config.toml"
	if err := WriteConfig(path, body, false); err != nil {
		t.Fatalf("WriteConfig: %v", err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("generated config should load: %v\n%s", err, body)
	}
	interactive := cfg.Profiles["interactive"]
	if !interactive.Network {
		t.Fatal("interactive profile should explicitly grant network")
	}
	if !interactive.RequireExplicit {
		t.Fatal("interactive profile should require an explicit request")
	}
	if got := strings.Join(interactive.AllowedPeers, ","); got != "mac-studio" {
		t.Fatalf("interactive allowed_peers = %q", got)
	}
}

func TestGenerateConfigOriginScopesInteractiveProfileToOutboundPeers(t *testing.T) {
	body, err := GenerateConfig(InitOptions{
		Role:        "origin",
		AllowedCWDs: []string{"~/code/**"},
		PeerNames:   []string{"win11-desktop"},
		Runner:      "codex",
		Profiles:    []string{"readonly", "dev", "interactive"},
	}, Machine{Name: "mac-studio", NodeID: "mac-studio"}, []RunnerStatus{
		{Name: "codex", Command: []string{"codex"}, Found: true},
	}, []PeerCandidate{
		{Name: "win11-desktop", NodeID: "win-node", Address: "100.64.10.20", CmeshCapable: true},
	})
	if err != nil {
		t.Fatalf("GenerateConfig: %v", err)
	}
	path := t.TempDir() + "/config.toml"
	if err := WriteConfig(path, body, false); err != nil {
		t.Fatalf("WriteConfig: %v", err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("generated config should load: %v\n%s", err, body)
	}
	if got := strings.Join(cfg.Profiles["interactive"].AllowedPeers, ","); got != "win11-desktop" {
		t.Fatalf("interactive allowed_peers = %q", got)
	}
}

func TestGenerateConfigSelectsCmeshCapablePeers(t *testing.T) {
	body, err := GenerateConfig(InitOptions{
		Role:        "origin",
		Runner:      "claude-code",
		AllowedCWDs: []string{"~/code/**"},
	}, Machine{Name: "mac-studio"}, []RunnerStatus{
		{Name: "claude-code", Command: []string{"claude", "-p"}, Found: true},
	}, []PeerCandidate{
		{Name: "homelab-vm", NodeID: "node-1", Address: "100.64.10.20", CmeshCapable: true},
		{Name: "printer", NodeID: "node-2", Address: "100.64.10.30"},
	})
	if err != nil {
		t.Fatalf("GenerateConfig: %v", err)
	}
	if !strings.Contains(body, `[peers.homelab-vm]`) {
		t.Fatalf("expected cmesh-capable peer in config:\n%s", body)
	}
	if strings.Contains(body, `[peers.printer]`) {
		t.Fatalf("unexpected non-cmesh peer in config:\n%s", body)
	}
}

func TestGenerateConfigSanitizesPeerTableNames(t *testing.T) {
	body, err := GenerateConfig(InitOptions{
		Role:        "origin",
		Runner:      "claude-code",
		AllowedCWDs: []string{"~/code/**"},
	}, Machine{Name: "win11-desktop"}, []RunnerStatus{
		{Name: "claude-code", Command: []string{"C:\\Users\\scott\\AppData\\Roaming\\npm\\claude.cmd", "-p"}, Found: true},
	}, []PeerCandidate{
		{Name: "Scott’s MacBook Pro", NodeID: "node-1", Address: "100.120.229.49", CmeshCapable: true},
	})
	if err != nil {
		t.Fatalf("GenerateConfig: %v", err)
	}
	if !strings.Contains(body, `[peers.scott-s-macbook-pro]`) {
		t.Fatalf("expected sanitized peer table name:\n%s", body)
	}
	path := t.TempDir() + "/config.toml"
	if err := WriteConfig(path, body, false); err != nil {
		t.Fatalf("WriteConfig: %v", err)
	}
	if _, err := config.Load(path); err != nil {
		t.Fatalf("generated config should load: %v\n%s", err, body)
	}
}

// TestGenerateConfigAllowedPeersMatchWhoisIdentity proves the value
// GenerateConfig writes into policy.allowed_peers is one that the inbound policy
// engine accepts when a peer authenticates via tailscale whois. Discovery
// supplies an ephemeral NodeID that does NOT match whois; the generated
// allowlist must key on the whois-matchable hostname (PeerCandidate.Name).
func TestGenerateConfigAllowedPeersMatchWhoisIdentity(t *testing.T) {
	const (
		hostname    = "homelab"
		ephemeralID = "n-ephemeral-12345" // tailscale status --json peer.ID; NOT whois-matchable
		whoisNodeID = "nReal67890CNTRL"   // whois Node.ID, distinct from the ephemeral id
		whoisStable = "nStableABCDEF"
		allowedCWD  = "~/code/**"
	)

	body, err := GenerateConfig(InitOptions{
		Role:        "worker",
		Runner:      "codex",
		AllowedCWDs: []string{allowedCWD},
	}, Machine{Name: "win11-desktop"}, []RunnerStatus{
		{Name: "codex", Command: []string{"codex"}, Found: true},
	}, []PeerCandidate{
		{Name: hostname, NodeID: ephemeralID, Address: "100.64.10.20", CmeshCapable: true},
	})
	if err != nil {
		t.Fatalf("GenerateConfig: %v", err)
	}

	// The matchable hostname must be written, never the ephemeral discovery id.
	if !strings.Contains(body, `allowed_peers = ["`+hostname+`"]`) {
		t.Fatalf("expected allowed_peers to key on whois-matchable hostname %q:\n%s", hostname, body)
	}
	if strings.Contains(body, ephemeralID) {
		t.Fatalf("ephemeral discovery node id %q must not appear in config:\n%s", ephemeralID, body)
	}

	path := t.TempDir() + "/config.toml"
	if err := WriteConfig(path, body, false); err != nil {
		t.Fatalf("WriteConfig: %v", err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("generated config should load: %v\n%s", err, body)
	}
	engine := policy.New(cfg)

	tests := []struct {
		name       string
		identity   tsid.Identity
		wantPeerOK bool
	}{
		{
			name:       "whois identity for the configured hostname is accepted",
			identity:   tsid.Identity{NodeName: hostname, NodeID: whoisNodeID, StableID: whoisStable},
			wantPeerOK: true,
		},
		{
			name:       "unknown peer is rejected at inbound policy",
			identity:   tsid.Identity{NodeName: "stranger", NodeID: "nStranger", StableID: "nStrangerStable"},
			wantPeerOK: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			decision := engine.CheckInbound(policy.TaskIntent{
				PeerName:     tc.identity.NodeName,
				PeerNodeID:   tc.identity.NodeID,
				PeerStableID: tc.identity.StableID,
				PeerTags:     tc.identity.Tags,
				CWD:          "~/code/example",
				Profile:      "dev",
			})
			// The peer-identity gate runs before the cwd gate. A peer match means
			// we pass past peer_unauthorized regardless of cwd existence.
			gotPeerOK := decision.Code != "peer_unauthorized"
			if gotPeerOK != tc.wantPeerOK {
				t.Fatalf("peer match = %v (code=%q), want %v", gotPeerOK, decision.Code, tc.wantPeerOK)
			}
		})
	}
}

// TestGenerateConfigRejectsGlobMetacharPeerIdentities is the B-SEC1 regression
// guard. A tailnet peer self-reports its hostname; if GenerateConfig wrote a
// glob metacharacter ("*", "[a-z]*", "a?b") into policy.allowed_peers, the real
// policy.Engine would use filepath.Match to authorize unrelated identities,
// granting inbound trust to the entire tailnet. We assert via the real engine
// that an unrelated victim identity is denied with peer_unauthorized. Asserting
// on the code (not just !Allowed) keeps the test non-vacuous: pre-fix the glob
// matches the victim, the peer check passes, and the code is NEVER
// peer_unauthorized.
func TestGenerateConfigRejectsGlobMetacharPeerIdentities(t *testing.T) {
	cases := []struct {
		name   string
		glob   string
		victim string
	}{
		{name: "star", glob: "*", victim: "evil-node"},
		{name: "char-class", glob: "[a-z]*", victim: "evil-node"},
		{name: "question", glob: "a?b", victim: "azb"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body, err := GenerateConfig(InitOptions{
				Role:        "worker",
				Runner:      "codex",
				AllowedCWDs: []string{"~/code/**"},
				PeerNames:   []string{tc.glob},
			}, Machine{Name: "homelab-vm", NodeID: "homelab-vm"}, []RunnerStatus{
				{Name: "codex", Command: []string{"codex"}, Found: true},
			}, []PeerCandidate{
				{Name: tc.glob, NodeID: tc.glob, Address: "100.64.10.20", CmeshCapable: true},
			})
			if err != nil {
				t.Fatalf("GenerateConfig: %v", err)
			}

			// The glob identity must never appear verbatim in the inbound allowlist.
			if strings.Contains(body, `allowed_peers = ["`+tc.glob+`"]`) {
				t.Fatalf("glob identity %q leaked into an allowlist:\n%s", tc.glob, body)
			}

			path := t.TempDir() + "/config.toml"
			if err := WriteConfig(path, body, false); err != nil {
				t.Fatalf("WriteConfig: %v", err)
			}
			cfg, err := config.Load(path)
			if err != nil {
				t.Fatalf("generated config should load: %v\n%s", err, body)
			}

			engine := policy.New(cfg)
			decision := engine.CheckInbound(policy.TaskIntent{
				PeerName: tc.victim,
				CWD:      "~/code",
				Runner:   "codex",
				Profile:  "dev",
			})
			if decision.Code != "peer_unauthorized" {
				t.Fatalf("victim %q should be peer_unauthorized, got allowed=%v code=%q detail=%q\nallowlist from config: %v\n%s",
					tc.victim, decision.Allowed, decision.Code, decision.Detail, cfg.Policy.AllowedPeers, body)
			}
		})
	}
}

func TestCapturePeerIdentityReadsHelloResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/hello" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(schema.HelloResponse{
			NodeID:   "n-ephemeral-999",
			NodeName: "homelab",
			StableID: "nStableABCDEF",
		})
	}))
	defer srv.Close()

	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse server URL: %v", err)
	}
	host, portStr, err := net.SplitHostPort(u.Host)
	if err != nil {
		t.Fatalf("split host port: %v", err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("parse port: %v", err)
	}

	name, stableID, err := CapturePeerIdentity(context.Background(), host, port)
	if err != nil {
		t.Fatalf("CapturePeerIdentity: %v", err)
	}
	if name != "homelab" {
		t.Fatalf("node_name = %q, want %q", name, "homelab")
	}
	if stableID != "nStableABCDEF" {
		t.Fatalf("stable_id = %q, want %q", stableID, "nStableABCDEF")
	}
}

func TestDetectRunnerResolvesCommandBinary(t *testing.T) {
	status := detectRunner(context.Background(), "go-runner", []string{"go", "version"}, "go", "version")
	if !status.Found {
		t.Skip("go not found on PATH")
	}
	if !filepath.IsAbs(status.Command[0]) {
		t.Fatalf("command[0] = %q, want absolute path", status.Command[0])
	}
}
