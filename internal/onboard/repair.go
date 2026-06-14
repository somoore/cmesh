package onboard

import (
	"context"
	"fmt"
	"strings"

	"cmesh/internal/config"
)

// PeerRepair is the per-peer diagnosis the report returns and the confirm path
// acts on. It is computed by DiagnosePeers and consumed by ApplyPeerRepairs, so the
// classification and the action share one inbound-posture decision (WantInbound)
// and can never disagree.
type PeerRepair struct {
	Section        string `json:"section"`                  // [peers.<Section>]
	Address        string `json:"address"`                  // tailnet address: the stable join key
	StoredIdentity string `json:"stored_identity"`          // current node_id/name in config
	WhoisIdentity  string `json:"whois_identity,omitempty"` // tailscale DNS label: the trust anchor
	HelloName      string `json:"hello_name,omitempty"`     // peer's self-reported /v1/hello node_name
	StableID       string `json:"stable_id,omitempty"`      // captured from /v1/hello
	Reachable      bool   `json:"reachable"`                // /v1/hello answered
	WantInbound    bool   `json:"want_inbound"`             // whether the repaired entry keeps an inbound grant
	Status         string `json:"status"`                   // ok | fixable | unreachable | conflict
	Detail         string `json:"detail,omitempty"`
}

// Repair status values.
const (
	RepairOK          = "ok"
	RepairFixable     = "fixable"
	RepairUnreachable = "unreachable"
	RepairConflict    = "conflict"
)

// Injection seams so DiagnosePeers can be tested against a fake tailnet/identity
// without a live `tailscale status` or a reachable peer. Production points at the
// real discovery + /v1/hello capture.
var (
	diagnoseTailnetFn  = TailnetPeers
	diagnoseIdentityFn = CapturePeerIdentity
)

// DiagnosePeers reconciles every [peers.*] entry in cfg against the authoritative
// tailnet identity. The trust anchor is tailscale whois (the DNS label the inbound
// policy matches a connection against), matched to each entry by address. The peer's
// self-reported /v1/hello name is used ONLY to confirm liveness and capture
// stable_id — never as an allowlist identity, since it is attacker-influenceable. A
// whois-vs-hello disagreement is reported as a conflict and left for review.
//
// It is read-only. ApplyPeerRepairs performs the writes for the entries it marks
// fixable, using the same WantInbound decision so classify and act never diverge.
func DiagnosePeers(ctx context.Context, cfg *config.Config) ([]PeerRepair, error) {
	tailnet, err := diagnoseTailnetFn(ctx, cfg.Node.MeshPort, false)
	if err != nil {
		return nil, err
	}
	whoisByAddr := map[string]string{}
	for _, p := range tailnet {
		if p.Address != "" {
			whoisByAddr[p.Address] = p.Name
		}
	}
	// An origin-only node never holds inbound grants, so a repair must never add one.
	nodeTakesInbound := !strings.EqualFold(strings.TrimSpace(cfg.Node.Role), "origin")

	var repairs []PeerRepair
	for section, peer := range cfg.Peers {
		stored := firstNonEmpty(peer.NodeID, peer.Name, section)
		r := PeerRepair{Section: section, Address: peer.Address, StoredIdentity: stored}
		if peer.Address == "" {
			r.Status = RepairUnreachable
			r.Detail = "entry has no tailnet address to verify against"
			repairs = append(repairs, r)
			continue
		}
		whois := whoisByAddr[peer.Address]
		r.WhoisIdentity = whois
		helloName, stableID, helloErr := diagnoseIdentityFn(ctx, peer.Address, cfg.Node.MeshPort)
		r.HelloName = helloName
		r.StableID = stableID
		r.Reachable = helloErr == nil

		// Inbound posture is decided ONCE here and reused on apply: preserve whatever
		// trust this peer's existing entry already had (under any of its identities),
		// and never grant inbound on an origin-only node. Repair migrates trust to the
		// correct identity; it does not widen it.
		r.WantInbound = nodeTakesInbound && peerHasInbound(cfg, r.StoredIdentity, whois, stableID)

		switch {
		case whois == "":
			r.Status = RepairUnreachable
			r.Detail = "no live tailnet peer found at this address; cannot verify identity (left unchanged)"
		case helloName != "" && !strings.EqualFold(helloName, whois):
			r.Status = RepairConflict
			r.Detail = fmt.Sprintf("tailscale whois reports %q but the peer self-reports %q; left unchanged for review", whois, helloName)
		case peerEntryConsistent(cfg, stored, whois, stableID, r.WantInbound):
			r.Status = RepairOK
			r.Detail = "entry already matches the peer's whois identity"
		default:
			r.Status = RepairFixable
			r.Detail = fmt.Sprintf("rewrite identity to the whois-matchable %q (+ stable_id) and reconcile allowlists", whois)
		}
		repairs = append(repairs, r)
	}
	return repairs, nil
}

// ApplyPeerRepairs rewrites every fixable entry to its whois-matchable identity via
// AddPeerLink (correct section + stale-section pruning + allowlist reconciliation),
// using each repair's precomputed WantInbound so the action matches the diagnosis.
// Returns the names written. Conflicts and unreachable peers are skipped.
func ApplyPeerRepairs(configPath string, repairs []PeerRepair) ([]string, error) {
	if configPath == "" {
		configPath = config.DefaultPath()
	}
	var fixed []string
	for _, r := range repairs {
		if r.Status != RepairFixable {
			continue
		}
		candidate := PeerCandidate{Name: r.WhoisIdentity, NodeID: r.WhoisIdentity, Address: r.Address}
		name, err := AddPeerLink(configPath, candidate, r.StableID, r.WantInbound)
		if err != nil {
			return fixed, fmt.Errorf("repairing %q: %w", r.Section, err)
		}
		fixed = append(fixed, name)
	}
	return fixed, nil
}

// CountRepairs returns how many repairs have the given status.
func CountRepairs(repairs []PeerRepair, status string) int {
	n := 0
	for _, r := range repairs {
		if r.Status == status {
			n++
		}
	}
	return n
}

// peerEntryConsistent reports whether the stored entry already matches the peer's
// whois identity AND the inbound allowlist reflects the intended posture, so the
// entry needs no rewrite.
func peerEntryConsistent(cfg *config.Config, stored, whois, stableID string, wantInbound bool) bool {
	if !identityIsMatchable(stored, whois, stableID) {
		return false
	}
	if !wantInbound {
		return true // origin-only / no-inbound posture: nothing to assert inbound.
	}
	return containsFold(cfg.Policy.AllowedPeers, whois) || (stableID != "" && containsFold(cfg.Policy.AllowedPeers, stableID))
}

// identityIsMatchable reports whether stored already equals a whois-matchable
// identity (the DNS label or stable id), case-insensitively.
func identityIsMatchable(stored, whois, stableID string) bool {
	stored = strings.TrimSpace(stored)
	return (whois != "" && strings.EqualFold(stored, whois)) ||
		(stableID != "" && strings.EqualFold(stored, stableID))
}

// peerHasInbound reports whether this peer currently holds an inbound grant under
// any of its identities, so repair preserves rather than widens trust.
func peerHasInbound(cfg *config.Config, stored, whois, stableID string) bool {
	return containsFold(cfg.Policy.AllowedPeers, stored) ||
		containsFold(cfg.Policy.AllowedPeers, whois) ||
		(stableID != "" && containsFold(cfg.Policy.AllowedPeers, stableID))
}

func containsFold(values []string, target string) bool {
	if strings.TrimSpace(target) == "" {
		return false
	}
	for _, v := range values {
		if strings.EqualFold(strings.TrimSpace(v), strings.TrimSpace(target)) {
			return true
		}
	}
	return false
}
