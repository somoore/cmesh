package task

import "testing"

func TestValidIDRejectsPathTraversalAndMalformed(t *testing.T) {
	// A freshly minted id must validate.
	id, err := NewID()
	if err != nil {
		t.Fatalf("NewID: %v", err)
	}
	if !ValidID(id) {
		t.Fatalf("minted id %q failed ValidID", id)
	}
	if !ValidID(DeterministicID("a", "b")) {
		t.Fatalf("deterministic id failed ValidID")
	}
	// Anything that could escape the data dir or otherwise isn't a system id
	// must be rejected — this guards the filepath.Join sinks (logs, artifacts,
	// worktrees, task tokens) against path traversal.
	for _, bad := range []string{
		"",
		"task-1",
		"../../etc/passwd",
		"tsk_../escape",
		"tsk_UPPER",
		"tsk_has space",
		"tsk_with/slash",
		"tsk_0189", // 0,1,8,9 are not in the base32 alphabet
		"notprefixed",
	} {
		if ValidID(bad) {
			t.Errorf("ValidID(%q) = true, want false", bad)
		}
	}
}

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

func TestDeterministicIDIsStableAndScoped(t *testing.T) {
	a := DeterministicID("node-a", "session-1", "prompt")
	b := DeterministicID("node-a", "session-1", "prompt")
	c := DeterministicID("node-a", "session-2", "prompt")
	if a != b {
		t.Fatalf("deterministic ids differed: %q %q", a, b)
	}
	if a == c {
		t.Fatalf("deterministic ids collided across session scope: %q", a)
	}
	if len(a) <= len("tsk_") || a[:4] != "tsk_" {
		t.Fatalf("unexpected deterministic id format: %q", a)
	}
}
