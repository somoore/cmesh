package gateway

import (
	"context"
	"strings"
	"testing"

	"cmesh/internal/store"
	"cmesh/internal/task"
)

// A workstream name becomes an origin session id, which is the ownership key
// for task-token reads. Only a host-token caller may choose one: a worker that
// could name a workstream would name its way into another team's tasks.
func TestWorkstreamCannotBeChosenByTaskToken(t *testing.T) {
	g := newTestGateway(t)
	rec := seedWorkerTask(t, g, "tsk_ws")

	// workstream_status is owner-only outright.
	resp, _ := g.workstreamStatusCall(workerCtx(rec.ID), "auth-refactor", 0)
	if resp.OK || resp.Error == nil || resp.Error.Code != task.ErrorPeerUnauthorized {
		t.Fatalf("a worker token must not read workstream status, got %+v", resp)
	}

	// And a task token's session comes from its stored context, so naming a
	// workstream cannot move its children into another thread. The session is
	// decided before dispatch, so the mirrored child records what was chosen.
	workerSession := "session-worker"
	scoped := context.WithValue(context.Background(), provenanceContextKey{}, gatewayProvenance{
		TaskID:          rec.ID,
		OriginNodeID:    "mac-studio",
		OriginSessionID: workerSession,
	})
	// The peer is unreachable, so dispatch fails -- but the session decision has
	// already happened, and a scoped caller must never land in "ws:auth-refactor".
	_, _ = g.delegateCall(scoped, DelegateRequest{
		Peer:       "nowhere",
		Prompt:     "do a thing",
		CWD:        t.TempDir(),
		Workstream: "auth-refactor",
	})
	mirrored, err := g.Store.ListPeerTasks(context.Background(), 50)
	if err != nil {
		t.Fatalf("ListPeerTasks: %v", err)
	}
	stolen, _ := workstreamSession("auth-refactor")
	for _, peerTask := range mirrored {
		if peerTask.OriginSessionID == stolen {
			t.Fatalf("a task token placed a task into a named workstream: %+v", peerTask)
		}
	}
}

// Names that would be ambiguous or unreadable where a session id surfaces --
// task records, idempotency keys, audit events -- are refused up front.
func TestWorkstreamNameValidation(t *testing.T) {
	cases := []struct {
		name  string
		valid bool
	}{
		{"auth-refactor", true},
		{"release_2026.08", true},
		{"a", true},
		{"", true}, // empty means "no workstream"
		{"-leading-dash", false},
		{"has space", false},
		{"has/slash", false},
		{"ws:injected", false},
		{strings.Repeat("x", 65), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			session, err := workstreamSession(tc.name)
			if tc.valid && err != nil {
				t.Fatalf("%q should be accepted: %v", tc.name, err)
			}
			if !tc.valid && err == nil {
				t.Fatalf("%q should be refused", tc.name)
			}
			if tc.valid && tc.name != "" {
				// The session is namespaced so it can never collide with a
				// harness-supplied session id.
				if !strings.HasPrefix(session, workstreamSessionPrefix) {
					t.Fatalf("session %q is not namespaced", session)
				}
				if got := workstreamName(session); got != tc.name {
					t.Fatalf("round trip = %q, want %q", got, tc.name)
				}
			}
		})
	}
}

// workstream_status aggregates one thread across peers and leaves other
// threads out.
func TestWorkstreamStatusAggregatesAcrossPeers(t *testing.T) {
	ctx := context.Background()
	g := newTestGateway(t)
	session, err := workstreamSession("auth-refactor")
	if err != nil {
		t.Fatal(err)
	}
	other, err := workstreamSession("unrelated")
	if err != nil {
		t.Fatal(err)
	}
	for _, seed := range []store.PeerTask{
		{TaskID: "t1", PeerNodeID: "linux", RemoteTaskID: "r1", OriginSessionID: session, State: task.StateDone, ProfileEffective: "repo-dev"},
		{TaskID: "t2", PeerNodeID: "windows", RemoteTaskID: "r2", OriginSessionID: session, State: task.StateRunning},
		{TaskID: "t3", PeerNodeID: "linux", RemoteTaskID: "r3", OriginSessionID: other, State: task.StateDone},
	} {
		if err := g.Store.UpsertPeerTask(ctx, seed); err != nil {
			t.Fatalf("UpsertPeerTask %s: %v", seed.TaskID, err)
		}
	}

	resp, err := g.workstreamStatusCall(ctx, "auth-refactor", 0)
	if err != nil || !resp.OK {
		t.Fatalf("workstreamStatusCall resp=%+v err=%v", resp, err)
	}
	result := resp.Result.(map[string]any)
	if result["tasks"] != 2 {
		t.Fatalf("tasks = %v, want 2 (the unrelated workstream must not appear)", result["tasks"])
	}
	peers := result["peers"].([]string)
	if len(peers) != 2 || peers[0] != "linux" || peers[1] != "windows" {
		t.Fatalf("peers = %v, want sorted [linux windows]", peers)
	}
	counts := result["by_state"].(map[string]int)
	if counts[string(task.StateDone)] != 1 || counts[string(task.StateRunning)] != 1 {
		t.Fatalf("by_state = %+v", counts)
	}
}

// list_tasks narrows to one workstream when asked, and is unchanged otherwise.
func TestListTasksWorkstreamFilter(t *testing.T) {
	ctx := context.Background()
	g := newTestGateway(t)
	session, _ := workstreamSession("mine")
	for _, seed := range []store.PeerTask{
		{TaskID: "t1", PeerNodeID: "linux", RemoteTaskID: "r1", OriginSessionID: session, State: task.StateDone},
		{TaskID: "t2", PeerNodeID: "linux", RemoteTaskID: "r2", OriginSessionID: "someone-else", State: task.StateDone},
	} {
		if err := g.Store.UpsertPeerTask(ctx, seed); err != nil {
			t.Fatalf("UpsertPeerTask: %v", err)
		}
	}

	filtered, err := g.listTasksCall(ctx, 20, "mine")
	if err != nil || !filtered.OK {
		t.Fatalf("listTasksCall resp=%+v err=%v", filtered, err)
	}
	if got := len(filtered.Result.(map[string]any)["delegated"].([]store.PeerTask)); got != 1 {
		t.Fatalf("filtered list = %d tasks, want 1", got)
	}

	all, err := g.listTasksCall(ctx, 20, "")
	if err != nil || !all.OK {
		t.Fatalf("unfiltered listTasksCall resp=%+v err=%v", all, err)
	}
	if got := len(all.Result.(map[string]any)["delegated"].([]store.PeerTask)); got != 2 {
		t.Fatalf("unfiltered list = %d tasks, want 2", got)
	}

	if bad, _ := g.listTasksCall(ctx, 20, "not a slug"); bad.OK {
		t.Fatal("an invalid workstream name should be refused, not silently ignored")
	}
}
