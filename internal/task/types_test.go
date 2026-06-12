package task

import "testing"

func TestValidTransition(t *testing.T) {
	valid := [][2]State{
		{StateQueued, StateRunning},
		{StateQueued, StateDenied},
		{StateQueued, StateFailed},
		{StateQueued, StateCancelled},
		{StateRunning, StateDone},
		{StateRunning, StateFailed},
		{StateRunning, StateCancelled},
	}
	for _, tc := range valid {
		if !ValidTransition(tc[0], tc[1]) {
			t.Fatalf("expected %s -> %s to be valid", tc[0], tc[1])
		}
	}
	if ValidTransition(StateDone, StateRunning) {
		t.Fatalf("terminal states must not transition back to running")
	}
}

func TestIdempotencyKeyIncludesAllParts(t *testing.T) {
	a := IdempotencyKey("node-a", "session-a", "task-a")
	b := IdempotencyKey("node-a", "session-b", "task-a")
	c := IdempotencyKey("node-a", "session-a", "task-b")
	if a == b || a == c || b == c {
		t.Fatalf("idempotency keys collided: %q %q %q", a, b, c)
	}
}
