package runner

import "testing"

// A manifest claim is only worth anything if it is honest. mid_task_input says
// how a runner receives messages sent to a task while it runs, and the only
// honest values today are "checkpoint" for agent-backed runners -- which pick
// up queued messages when the agent calls task_ask through its task-scoped MCP
// gateway -- and "none" for runners with no agent to talk to.
//
// "stream" means the harness accepts input on an open stdin mid-run. No adapter
// declares it, because exec.go has one batch spawn path. This test exists so
// that claim cannot be added without the code to back it.
func TestMidTaskInputValues(t *testing.T) {
	for _, value := range []string{"none", "checkpoint", "stream", ""} {
		if !ValidMidTaskInput(value) {
			t.Fatalf("%q should be a recognized mid_task_input value", value)
		}
	}
	for _, value := range []string{"streaming", "yes", "live"} {
		if ValidMidTaskInput(value) {
			t.Fatalf("%q should not be a recognized mid_task_input value", value)
		}
	}
}
