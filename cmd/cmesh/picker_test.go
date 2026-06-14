package main

import (
	"testing"

	"cmesh/internal/mcp/setupinstall"
)

func TestAgentsToItemsDefaultsChecked(t *testing.T) {
	agents := []setupinstall.Agent{
		{Name: "Claude Code", Detail: "/usr/bin/claude"},
		{Name: "Codex", Detail: "/usr/bin/codex"},
	}
	items := agentsToItems(agents)
	if len(items) != 2 {
		t.Fatalf("got %d items, want 2", len(items))
	}
	for i, it := range items {
		if !it.Checked {
			t.Fatalf("item %d not checked by default", i)
		}
		if it.Label != agents[i].Name {
			t.Fatalf("item %d label = %q, want %q", i, it.Label, agents[i].Name)
		}
		if it.Detail != agents[i].Detail {
			t.Fatalf("item %d detail = %q, want %q", i, it.Detail, agents[i].Detail)
		}
	}
}

func TestAgentNames(t *testing.T) {
	agents := []setupinstall.Agent{{Name: "Claude Code"}, {Name: "Codex"}}
	got := agentNames(agents)
	want := []string{"Claude Code", "Codex"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}
