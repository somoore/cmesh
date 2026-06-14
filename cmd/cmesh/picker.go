package main

import (
	"os"

	"cmesh/internal/mcp/setupinstall"
	"cmesh/internal/tui"
)

// agentsToItems maps detected agents to tui.Item rows. All rows default to
// checked so the common "install/remove everything detected" case is one Enter.
func agentsToItems(agents []setupinstall.Agent) []tui.Item {
	items := make([]tui.Item, 0, len(agents))
	for _, a := range agents {
		items = append(items, tui.Item{
			Label:   a.Name,
			Detail:  a.Detail,
			Checked: true,
		})
	}
	return items
}

// pickAgents runs the multi-select picker over the given agents and returns the
// chosen subset (in original order). It propagates tui.ErrCancelled unchanged.
func pickAgents(title string, agents []setupinstall.Agent) ([]setupinstall.Agent, error) {
	indices, err := tui.MultiSelect(os.Stdin, os.Stdout, title, agentsToItems(agents))
	if err != nil {
		return nil, err
	}
	chosen := make([]setupinstall.Agent, 0, len(indices))
	for _, i := range indices {
		if i >= 0 && i < len(agents) {
			chosen = append(chosen, agents[i])
		}
	}
	return chosen, nil
}
