package gitx

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cmesh/internal/testutil"
)

// initRepo creates a repository with one commit and returns its path and head.
// content distinguishes otherwise-identical repositories: git derives a commit
// hash from its content, author, and timestamp, so two repos seeded the same way
// in the same second produce the *same* commit -- which would make an
// "unrelated" repository accidentally hold the prerequisite under test.
func initRepo(t *testing.T, content string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	testutil.RunGit(t, dir, "init", "-q", "-b", "main")
	testutil.RunGit(t, dir, "config", "user.email", "test@example.com")
	testutil.RunGit(t, dir, "config", "user.name", "test")
	if err := os.WriteFile(filepath.Join(dir, "base.txt"), []byte(content+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	testutil.RunGit(t, dir, "add", "-A")
	testutil.RunGit(t, dir, "commit", "-q", "-m", "base")
	head := strings.TrimSpace(testutil.RunGitOutput(t, dir, "rev-parse", "HEAD"))
	return dir, head
}

// A handoff only works if the receiving checkout can actually apply the source
// branch. cmesh bundles are thin, so this exercises the real round trip: bundle
// on one repository, verify and fetch on another that shares the base.
func TestBundleRoundTripAppliesOnPeerSharingBase(t *testing.T) {
	testutil.EnsureGitOnPath(t)
	ctx := context.Background()

	source, base := initRepo(t, "shared-history")
	// The receiving peer is an independent clone that already holds the base.
	dest := t.TempDir()
	testutil.RunGit(t, filepath.Dir(dest), "clone", "-q", source, filepath.Base(dest))

	// The source task does its work on a cmesh branch.
	testutil.RunGit(t, source, "checkout", "-q", "-b", "cmesh/tsk_one/fix")
	if err := os.WriteFile(filepath.Join(source, "fix.txt"), []byte("fixed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	testutil.RunGit(t, source, "add", "-A")
	testutil.RunGit(t, source, "commit", "-q", "-m", "fix")

	sourceRepo, err := Open(source)
	if err != nil {
		t.Fatalf("Open source: %v", err)
	}
	bundlePath := filepath.Join(t.TempDir(), "seed.bundle")
	if err := sourceRepo.Bundle(ctx, bundlePath, "cmesh/tsk_one/fix", base); err != nil {
		t.Fatalf("Bundle: %v", err)
	}

	destRepo, err := Open(dest)
	if err != nil {
		t.Fatalf("Open dest: %v", err)
	}
	if err := destRepo.VerifyBundle(ctx, bundlePath); err != nil {
		t.Fatalf("peer sharing the base should verify the bundle: %v", err)
	}
	if err := destRepo.FetchBundle(ctx, bundlePath, "cmesh/tsk_one/fix", "refs/cmesh/handoff/tsk_one"); err != nil {
		t.Fatalf("FetchBundle: %v", err)
	}

	// The handed-off work is now reachable in the receiving repository.
	got := strings.TrimSpace(testutil.RunGitOutput(t, dest, "show", "refs/cmesh/handoff/tsk_one:fix.txt"))
	if got != "fixed" {
		t.Fatalf("handed-off content = %q, want %q", got, "fixed")
	}
}

// A peer whose checkout does not share history cannot apply a thin bundle.
// Verify must catch that before the fetch, so the failure is the actionable
// handoff_base_missing rather than an opaque git error mid-apply.
func TestVerifyBundleRejectsPeerMissingBase(t *testing.T) {
	testutil.EnsureGitOnPath(t)
	ctx := context.Background()

	source, base := initRepo(t, "shared-history")
	testutil.RunGit(t, source, "checkout", "-q", "-b", "cmesh/tsk_two/fix")
	if err := os.WriteFile(filepath.Join(source, "fix.txt"), []byte("fixed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	testutil.RunGit(t, source, "add", "-A")
	testutil.RunGit(t, source, "commit", "-q", "-m", "fix")

	sourceRepo, err := Open(source)
	if err != nil {
		t.Fatalf("Open source: %v", err)
	}
	bundlePath := filepath.Join(t.TempDir(), "seed.bundle")
	if err := sourceRepo.Bundle(ctx, bundlePath, "cmesh/tsk_two/fix", base); err != nil {
		t.Fatalf("Bundle: %v", err)
	}

	// An unrelated repository: same tool, no shared history.
	stranger, _ := initRepo(t, "entirely-different-history")
	strangerRepo, err := Open(stranger)
	if err != nil {
		t.Fatalf("Open stranger: %v", err)
	}
	if err := strangerRepo.VerifyBundle(ctx, bundlePath); err == nil {
		t.Fatal("a peer without the base must not verify a thin bundle")
	}
}

// Handoff lineage is recorded in the commit object, so provenance survives
// without cmesh's database.
func TestCommitRecordsHandoffTrailer(t *testing.T) {
	testutil.EnsureGitOnPath(t)
	ctx := context.Background()

	dir, _ := initRepo(t, "trailer-test")
	if err := os.WriteFile(filepath.Join(dir, "more.txt"), []byte("more\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	repo, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := repo.AddAll(ctx); err != nil {
		t.Fatalf("AddAll: %v", err)
	}
	if _, err := repo.Commit(ctx, CommitOptions{
		AuthorName: "cmesh", AuthorEmail: "cmesh@test",
		Subject: "cmesh task tsk_dst", TaskID: "tsk_dst",
		OriginNode: "mac", WorkerNode: "windows", Profile: "repo-dev",
		HandoffFrom: "tsk_src",
	}); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	message := testutil.RunGitOutput(t, dir, "log", "-1", "--format=%B")
	for _, want := range []string{"cmesh-task: tsk_dst", "cmesh-handoff-from: tsk_src", "cmesh-profile: repo-dev"} {
		if !strings.Contains(message, want) {
			t.Fatalf("commit message missing %q:\n%s", want, message)
		}
	}
}
