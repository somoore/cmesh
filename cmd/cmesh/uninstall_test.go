package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDataDirDeletable(t *testing.T) {
	home := t.TempDir()       // stand-in for $HOME
	configRoot := t.TempDir() // stand-in for the user config root
	dataDir := filepath.Join(home, ".local", "share", "cmesh")
	childOfData := filepath.Join(dataDir, "audit")

	tests := []struct {
		name     string
		resolved string
		wantErr  bool
	}{
		// The path guard only refuses catastrophic paths; it deliberately does
		// NOT bound the path against the configured data dir (that was the
		// tautology). Proof a dir is cmesh's is the artifact check below.
		{name: "data dir accepted", resolved: dataDir},
		{name: "child of data dir accepted", resolved: childOfData},
		{name: "arbitrary outside dir accepted by path guard", resolved: t.TempDir()},
		{name: "empty refused", resolved: "", wantErr: true},
		{name: "root refused", resolved: string(filepath.Separator), wantErr: true},
		{name: "home refused", resolved: home, wantErr: true},
		{name: "config root refused", resolved: configRoot, wantErr: true},
		{name: "ancestor of home refused", resolved: filepath.Dir(home), wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := dataDirDeletable(tt.resolved, home, configRoot)
			if tt.wantErr && err == nil {
				t.Fatalf("expected error for %q, got nil", tt.resolved)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error for %q: %v", tt.resolved, err)
			}
		})
	}
}

// TestDataDirHasCmeshArtifacts proves the strengthened B-SEC2 guard is
// non-vacuous: a dir containing a cmesh artifact is recognized (deletable),
// while a dir with no artifact is refused even when it sits outside $HOME and
// even when it looks /etc-like. This is what stops data_dir=/etc from being
// deleted after a single Y.
func TestDataDirHasCmeshArtifacts(t *testing.T) {
	mkdir := func(t *testing.T, files ...string) string {
		t.Helper()
		dir := t.TempDir()
		for _, f := range files {
			if err := os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o600); err != nil {
				t.Fatalf("write %s: %v", f, err)
			}
		}
		return dir
	}

	tests := []struct {
		name string
		dir  string
		want bool
	}{
		{name: "dir with cmesh.db is cmesh artifact", dir: mkdir(t, "cmesh.db"), want: true},
		{name: "dir with audit.jsonl is cmesh artifact", dir: mkdir(t, "audit.jsonl"), want: true},
		{name: "dir with both artifacts", dir: mkdir(t, "cmesh.db", "audit.jsonl"), want: true},
		{name: "empty dir has no artifacts", dir: mkdir(t), want: false},
		{name: "dir with only unrelated files refused", dir: mkdir(t, "passwd", "hosts"), want: false},
		{name: "empty path string", dir: "", want: false},
		{name: "etc-like dir without artifacts refused", dir: mkdir(t, "fstab", "passwd"), want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := dataDirHasCmeshArtifacts(tt.dir); got != tt.want {
				t.Fatalf("dataDirHasCmeshArtifacts(%q) = %v, want %v", tt.dir, got, tt.want)
			}
		})
	}
}

// TestDataDirDeleteCombinedGuard exercises both guards together the way
// maybeRemoveDataDir does: a dir must pass the path guard AND contain a cmesh
// artifact to be deletable. This proves the /etc-style case is refused by the
// combination even though the path guard alone would allow it.
func TestDataDirDeleteCombinedGuard(t *testing.T) {
	home := t.TempDir()
	configRoot := t.TempDir()

	cmeshDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(cmeshDir, "cmesh.db"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	etcLike := t.TempDir() // outside $HOME, no cmesh artifacts -> the danger case
	if err := os.WriteFile(filepath.Join(etcLike, "fstab"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	deletable := func(dir string) bool {
		return dataDirDeletable(dir, home, configRoot) == nil && dataDirHasCmeshArtifacts(dir)
	}

	tests := []struct {
		name string
		dir  string
		want bool
	}{
		{name: "real cmesh data dir deletable", dir: cmeshDir, want: true},
		{name: "etc-like dir without artifacts refused", dir: etcLike, want: false},
		{name: "home refused", dir: home, want: false},
		{name: "config root refused", dir: configRoot, want: false},
		{name: "root refused", dir: string(filepath.Separator), want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := deletable(tt.dir); got != tt.want {
				t.Fatalf("deletable(%q) = %v, want %v", tt.dir, got, tt.want)
			}
		})
	}
}

func TestIsAncestor(t *testing.T) {
	tests := []struct {
		ancestor string
		child    string
		want     bool
	}{
		{ancestor: "/a", child: "/a/b", want: true},
		{ancestor: "/a", child: "/a/b/c", want: true},
		{ancestor: "/a", child: "/a", want: false},
		{ancestor: "/a/b", child: "/a", want: false},
		{ancestor: "/a", child: "/b", want: false},
	}
	for _, tt := range tests {
		if got := isAncestor(tt.ancestor, tt.child); got != tt.want {
			t.Fatalf("isAncestor(%q,%q) = %v, want %v", tt.ancestor, tt.child, got, tt.want)
		}
	}
}
