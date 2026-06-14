package main

import (
	"path/filepath"
	"testing"
)

func TestDataDirDeletable(t *testing.T) {
	home := t.TempDir()       // stand-in for $HOME
	configRoot := t.TempDir() // stand-in for the user config root
	dataDir := filepath.Join(home, ".local", "share", "cmesh")
	childOfData := filepath.Join(dataDir, "audit")

	tests := []struct {
		name              string
		resolved          string
		configuredDataDir string
		wantErr           bool
	}{
		{name: "exact data dir accepted", resolved: dataDir, configuredDataDir: dataDir},
		{name: "child of data dir accepted", resolved: childOfData, configuredDataDir: dataDir},
		{name: "empty refused", resolved: "", configuredDataDir: dataDir, wantErr: true},
		{name: "root refused", resolved: string(filepath.Separator), configuredDataDir: string(filepath.Separator), wantErr: true},
		{name: "home refused", resolved: home, configuredDataDir: home, wantErr: true},
		{name: "config root refused", resolved: configRoot, configuredDataDir: configRoot, wantErr: true},
		{name: "ancestor of home refused", resolved: filepath.Dir(home), configuredDataDir: filepath.Dir(home), wantErr: true},
		{name: "outside configured bound refused", resolved: t.TempDir(), configuredDataDir: dataDir, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := dataDirDeletable(tt.resolved, home, configRoot, tt.configuredDataDir)
			if tt.wantErr && err == nil {
				t.Fatalf("expected error for %q, got nil", tt.resolved)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error for %q: %v", tt.resolved, err)
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
