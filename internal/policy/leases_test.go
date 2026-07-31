package policy

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestProfileLeaseStoreScopesListAndRevokeToOwningPeer(t *testing.T) {
	store := NewProfileLeaseStore(t.TempDir())
	now := time.Now().UTC()
	peerA := LeaseValidation{PeerName: "peer-a", PeerNodeID: "node-a", PeerStableID: "stable-a", Profile: "repo-dev", CWD: t.TempDir()}
	peerB := LeaseValidation{PeerName: "peer-b", PeerNodeID: "node-b", PeerStableID: "stable-b", Profile: "repo-dev", CWD: t.TempDir()}
	leaseA, err := store.Issue(peerA, time.Hour, now)
	if err != nil {
		t.Fatalf("issue peer A: %v", err)
	}
	leaseB, err := store.Issue(peerB, time.Hour, now)
	if err != nil {
		t.Fatalf("issue peer B: %v", err)
	}

	listA, err := store.ListForPeer(peerA, false, now)
	if err != nil {
		t.Fatalf("list peer A: %v", err)
	}
	if len(listA) != 1 || listA[0].LeaseID != leaseA.LeaseID {
		t.Fatalf("peer A list = %+v, want only %s", listA, leaseA.LeaseID)
	}
	if _, err := store.RevokeForPeer(peerA, leaseB.LeaseID, now); err == nil {
		t.Fatal("peer A revoked peer B lease")
	}
	if _, err := store.RevokeForPeer(peerA, leaseA.LeaseID, now); err != nil {
		t.Fatalf("peer A revoke own lease: %v", err)
	}
	all, err := store.ListAllForLocalOperator(true, now)
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("local operator list count = %d, want 2", len(all))
	}
	if _, err := store.RevokeAnyForLocalOperator(leaseB.LeaseID, now); err != nil {
		t.Fatalf("local operator revoke any: %v", err)
	}
}

// The lease file was append-only: nothing ever removed an expired or revoked
// entry, so a long-lived worker grew it without bound. Issue prunes entries
// that stopped being usable longer ago than the retention window, while keeping
// recent ones readable for audit.
func TestIssuePrunesLongExpiredLeases(t *testing.T) {
	store := NewProfileLeaseStore(t.TempDir())
	now := time.Now().UTC()
	peer := LeaseValidation{PeerName: "peer-a", PeerNodeID: "node-a", Profile: "repo-dev", CWD: t.TempDir()}

	// Expired well beyond retention: issued long ago with a short TTL.
	stale, err := store.Issue(peer, time.Hour, now.Add(-leaseRetention-48*time.Hour))
	if err != nil {
		t.Fatalf("issue stale: %v", err)
	}
	// Expired, but still inside the retention window.
	recent, err := store.Issue(peer, time.Hour, now.Add(-2*time.Hour))
	if err != nil {
		t.Fatalf("issue recent: %v", err)
	}
	// Issuing now triggers the prune.
	live, err := store.Issue(peer, time.Hour, now)
	if err != nil {
		t.Fatalf("issue live: %v", err)
	}

	all, err := store.ListAllForLocalOperator(true, now)
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	kept := map[string]bool{}
	for _, lease := range all {
		kept[lease.LeaseID] = true
	}
	if kept[stale.LeaseID] {
		t.Fatal("lease expired beyond retention should be pruned")
	}
	if !kept[recent.LeaseID] {
		t.Fatal("recently expired lease should be retained for audit")
	}
	if !kept[live.LeaseID] {
		t.Fatal("live lease must never be pruned")
	}

	// Pruning must never turn a denial into an acceptance.
	if _, err := store.ValidateProfileLease(LeaseValidation{
		LeaseID: recent.LeaseID, PeerName: peer.PeerName, PeerNodeID: peer.PeerNodeID,
		Profile: peer.Profile, CWD: peer.CWD,
	}); err == nil {
		t.Fatal("expired lease must still be refused after pruning")
	}
}

func TestProfileLeaseCWDScopes(t *testing.T) {
	root := t.TempDir()
	child := filepath.Join(root, "child")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatal(err)
	}
	sibling := t.TempDir()
	escapeLink := filepath.Join(root, "escape")
	if err := os.Symlink(sibling, escapeLink); err != nil {
		t.Logf("symlink unavailable: %v", err)
		escapeLink = ""
	}
	store := NewProfileLeaseStore(t.TempDir())
	now := time.Now().UTC()
	peer := LeaseValidation{PeerName: "peer-a", Profile: "repo-dev", CWD: root}

	exact, err := store.Issue(peer, time.Hour, now)
	if err != nil {
		t.Fatalf("issue exact: %v", err)
	}
	if _, err := store.ValidateProfileLease(LeaseValidation{LeaseID: exact.LeaseID, PeerName: "peer-a", Profile: "repo-dev", CWD: child}); err == nil {
		t.Fatal("exact lease allowed child cwd")
	}

	recursiveReq := peer
	recursiveReq.CWDScope = "recursive"
	recursive, err := store.Issue(recursiveReq, time.Hour, now)
	if err != nil {
		t.Fatalf("issue recursive: %v", err)
	}
	if _, err := store.ValidateProfileLease(LeaseValidation{LeaseID: recursive.LeaseID, PeerName: "peer-a", Profile: "repo-dev", CWD: child}); err != nil {
		t.Fatalf("recursive lease denied child cwd: %v", err)
	}
	if _, err := store.ValidateProfileLease(LeaseValidation{LeaseID: recursive.LeaseID, PeerName: "peer-a", Profile: "repo-dev", CWD: sibling}); err == nil {
		t.Fatal("recursive lease allowed sibling cwd")
	}
	if escapeLink != "" {
		if _, err := store.ValidateProfileLease(LeaseValidation{LeaseID: recursive.LeaseID, PeerName: "peer-a", Profile: "repo-dev", CWD: escapeLink}); err == nil {
			t.Fatal("recursive lease allowed symlink escape")
		}
	}
}
