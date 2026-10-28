package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"cmesh/internal/audit"
	"cmesh/internal/config"
	"cmesh/internal/mesh/schema"
	"cmesh/internal/policy"
	profiledefs "cmesh/internal/profile"
	"cmesh/internal/store"
	"cmesh/internal/task"
)

func TestHardenLocalRequiresHostTokenWhenConfigured(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	handler := hardenLocal(next, "secret")

	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/mcp", nil)
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusForbidden {
		t.Fatalf("status without token = %d, want forbidden", res.Code)
	}

	req = httptest.NewRequest(http.MethodPost, "http://127.0.0.1/mcp", nil)
	req.Header.Set("X-Cmesh-Host-Token", "secret")
	res = httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusNoContent {
		t.Fatalf("status with token = %d, want no content", res.Code)
	}
}

// GET /health is a liveness probe and must answer unauthenticated even when a
// host token is configured. Regression guard: it was previously swept under the
// token gate, so a healthy daemon returned 403 and onboarding reported it down.
func TestHardenLocalHealthIsUnauthenticated(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	handler := hardenLocal(next, "secret")

	// Without a token: still reaches the handler.
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/health", nil)
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusNoContent {
		t.Fatalf("/health without token = %d, want no content", res.Code)
	}

	// With a token: also fine.
	req = httptest.NewRequest(http.MethodGet, "http://127.0.0.1/health", nil)
	req.Header.Set("X-Cmesh-Host-Token", "secret")
	res = httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusNoContent {
		t.Fatalf("/health with token = %d, want no content", res.Code)
	}

	// Loopback hardening still applies: a non-loopback Host is rejected.
	req = httptest.NewRequest(http.MethodGet, "http://evil.example/health", nil)
	res = httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusForbidden {
		t.Fatalf("/health with non-loopback Host = %d, want forbidden", res.Code)
	}
}

func TestHardenLocalAcceptsTaskTokenWhenHostTokenConfigured(t *testing.T) {
	want := gatewayProvenance{OriginNodeID: "origin", OriginSessionID: "session", Chain: []string{"origin", "worker"}, HopCount: 1}
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, ok := r.Context().Value(provenanceContextKey{}).(gatewayProvenance)
		if !ok {
			t.Fatalf("missing provenance context")
		}
		if got.OriginNodeID != want.OriginNodeID || got.OriginSessionID != want.OriginSessionID || got.HopCount != want.HopCount {
			t.Fatalf("provenance = %+v, want %+v", got, want)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	handler := hardenLocalWithLookup(next, "host-secret", func(token string) (gatewayProvenance, error) {
		if token != "task-token" {
			t.Fatalf("lookup token = %q", token)
		}
		return want, nil
	})

	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/mcp", nil)
	req.Header.Set("Authorization", "Bearer task-token")
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusNoContent {
		t.Fatalf("status with task token = %d, want no content", res.Code)
	}
}

func TestStatusFallsBackToMirroredPeerTask(t *testing.T) {
	ctx := context.Background()
	g := newTestGateway(t)
	if err := g.Store.UpsertPeerTask(ctx, store.PeerTask{
		TaskID:          "task-1",
		PeerNodeID:      "missing-peer",
		RemoteTaskID:    "remote-1",
		OriginSessionID: "session-1",
		State:           task.StateRunning,
	}); err != nil {
		t.Fatalf("UpsertPeerTask: %v", err)
	}
	resp, err := g.statusCall(ctx, "task-1", false, 0)
	if err != nil {
		t.Fatalf("statusCall returned error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("statusCall not ok: %+v", resp)
	}
	result, ok := resp.Result.(map[string]any)
	if !ok || result["stale"] != true || result["state"] != task.StateRunning {
		t.Fatalf("unexpected mirrored result: %#v", resp.Result)
	}
}

func TestDelegatedTaskLookupSurvivesOriginRestart(t *testing.T) {
	ctx := context.Background()
	storePath := filepath.Join(t.TempDir(), "cmesh.db")
	workerMux := http.NewServeMux()
	workerMux.HandleFunc("GET /v1/tasks/remote-1", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"task_id":"remote-1","state":"done"}`))
	})
	workerMux.HandleFunc("GET /v1/tasks/remote-1/result", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"task_id":"remote-1","result":{"status":"done","command_output":{"command":"fake","exit_code":0,"stdout":"ok\n","stderr":"","duration_s":1},"worker_summary_untrusted":"ok","usage":{},"verification_required":true}}`))
	})
	worker := httptest.NewServer(workerMux)
	defer worker.Close()
	host, port := splitTestServerAddr(t, worker.URL)

	first := newTestGatewayWithStore(t, storePath)
	first.Config.Peers = map[string]config.PeerConfig{
		"worker": {Name: "worker", NodeID: "worker-node", Address: host, Port: port},
	}
	if err := first.Store.UpsertPeerTask(ctx, store.PeerTask{
		TaskID:          "task-1",
		PeerNodeID:      "worker-node",
		RemoteTaskID:    "remote-1",
		OriginSessionID: "session-1",
		State:           task.StateRunning,
	}); err != nil {
		t.Fatalf("UpsertPeerTask: %v", err)
	}
	if err := first.Store.Close(); err != nil {
		t.Fatalf("Close first store: %v", err)
	}

	restarted := newTestGatewayWithStore(t, storePath)
	restarted.Config.Peers = first.Config.Peers
	statusResp, err := restarted.statusCall(ctx, "task-1", false, 0)
	if err != nil || !statusResp.OK {
		t.Fatalf("status after restart resp=%+v err=%v", statusResp, err)
	}
	status := statusResp.Result.(schema.TaskStatusResponse)
	if status.State != task.StateDone {
		t.Fatalf("status after restart = %s, want done", status.State)
	}
	resultResp, err := restarted.resultCall(ctx, "task-1")
	if err != nil || !resultResp.OK {
		t.Fatalf("result after restart resp=%+v err=%v", resultResp, err)
	}
	result := resultResp.Result.(schema.TaskResultResponse)
	if result.Result == nil || result.Result.CommandOutput.Command != "fake" {
		t.Fatalf("result after restart = %+v", result.Result)
	}
}

// A per-task token authenticates a worker's runner reaching back in during
// subdelegation. Its output is untrusted by design, so it must not be able to
// read or cancel tasks outside its own delegation — only the host token, which
// belongs to this machine's own agent, may reach every delegated task.
//
// Ownership is the delegating task, not the session: every host-token
// delegation that omits origin_session_id shares gateway-default, so tokens for
// unrelated tasks routinely carry the same session.
func TestTaskTokenReachesOnlyTasksItDelegated(t *testing.T) {
	ctx := context.Background()
	g := newTestGateway(t)
	const shared = "gateway-default"
	for _, seed := range []store.PeerTask{
		{TaskID: "mine", PeerNodeID: "missing-peer", RemoteTaskID: "remote-mine", OriginSessionID: shared, OwnerTaskID: "tsk_a", State: task.StateRunning},
		{TaskID: "sibling", PeerNodeID: "missing-peer", RemoteTaskID: "remote-sibling", OriginSessionID: shared, OwnerTaskID: "tsk_b", State: task.StateRunning},
		{TaskID: "host", PeerNodeID: "missing-peer", RemoteTaskID: "remote-host", OriginSessionID: shared, State: task.StateRunning},
		{TaskID: "theirs", PeerNodeID: "missing-peer", RemoteTaskID: "remote-theirs", OriginSessionID: "session-b", OwnerTaskID: "tsk_c", State: task.StateRunning},
		{TaskID: "legacy", PeerNodeID: "missing-peer", RemoteTaskID: "remote-legacy", State: task.StateRunning},
	} {
		if err := g.Store.UpsertPeerTask(ctx, seed); err != nil {
			t.Fatalf("UpsertPeerTask %s: %v", seed.TaskID, err)
		}
	}

	// A task token minted for worker task tsk_a, in the shared default session.
	scoped := context.WithValue(ctx, provenanceContextKey{}, gatewayProvenance{
		TaskID:          "tsk_a",
		OriginNodeID:    "worker-node",
		OriginSessionID: shared,
	})

	cases := []struct {
		name string
		call func(context.Context, string) (ToolResponse, error)
	}{
		{"result", func(c context.Context, id string) (ToolResponse, error) { return g.resultCall(c, id) }},
		{"transcript", func(c context.Context, id string) (ToolResponse, error) { return g.transcriptCall(c, id) }},
		{"artifact", func(c context.Context, id string) (ToolResponse, error) { return g.artifactCall(c, id, "git-bundle") }},
		{"cancel", func(c context.Context, id string) (ToolResponse, error) { return g.cancelCall(c, id, "stop") }},
		{"status", func(c context.Context, id string) (ToolResponse, error) { return g.statusCall(c, id, false, 0) }},
		{"events", func(c context.Context, id string) (ToolResponse, error) { return g.eventsCall(c, id, 0, 0, false, 0) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// A sibling task's child in the same session, a host-token task in the
			// same session, another session's task, and a task predating
			// ownership are all refused. toolFailure returns the denial as both a
			// response and an error, so only the response is asserted here.
			for _, id := range []string{"sibling", "host", "theirs", "legacy"} {
				resp, _ := tc.call(scoped, id)
				if resp.OK || resp.Error == nil || resp.Error.Code != task.ErrorPeerUnauthorized {
					t.Fatalf("%s on %q should be peer_unauthorized, got %+v", tc.name, id, resp)
				}
			}

			// A task this token delegated gets past authorization; the peer is
			// unreachable in this fixture, so anything other than
			// peer_unauthorized proves the gate allowed it through.
			resp, _ := tc.call(scoped, "mine")
			if resp.Error != nil && resp.Error.Code == task.ErrorPeerUnauthorized {
				t.Fatalf("own %s must not be denied: %+v", tc.name, resp)
			}

			// A host-token caller (no provenance in context) reaches every task.
			resp, _ = tc.call(ctx, "sibling")
			if resp.Error != nil && resp.Error.Code == task.ErrorPeerUnauthorized {
				t.Fatalf("host-token %s must not be denied: %+v", tc.name, resp)
			}
		})
	}
}

// A status refresh upserts the mirror without knowing the owner. It must not
// strip ownership, or the delegating task would lose access to its own child.
func TestPeerTaskOwnerSurvivesUpsertWithoutOwner(t *testing.T) {
	ctx := context.Background()
	g := newTestGateway(t)
	if err := g.Store.UpsertPeerTask(ctx, store.PeerTask{TaskID: "child", PeerNodeID: "p", RemoteTaskID: "r", OriginSessionID: "s", OwnerTaskID: "tsk_a", State: task.StateRunning}); err != nil {
		t.Fatal(err)
	}
	if err := g.Store.UpsertPeerTask(ctx, store.PeerTask{TaskID: "child", PeerNodeID: "p", RemoteTaskID: "r", OriginSessionID: "s", State: task.StateDone}); err != nil {
		t.Fatal(err)
	}
	// Nor may a later write move it to a different owner.
	if err := g.Store.UpsertPeerTask(ctx, store.PeerTask{TaskID: "child", PeerNodeID: "p", RemoteTaskID: "r", OriginSessionID: "s", OwnerTaskID: "tsk_b", State: task.StateDone}); err != nil {
		t.Fatal(err)
	}
	got, err := g.Store.GetPeerTask(ctx, "child")
	if err != nil {
		t.Fatal(err)
	}
	if got.OwnerTaskID != "tsk_a" {
		t.Fatalf("owner = %q, want tsk_a", got.OwnerTaskID)
	}
}

// list_tasks must not let a task token enumerate other tasks' delegations --
// including those in its own session -- or this node's own local task history.
func TestListTasksScopedForTaskToken(t *testing.T) {
	ctx := context.Background()
	g := newTestGateway(t)
	for _, seed := range []store.PeerTask{
		{TaskID: "mine", PeerNodeID: "peer", RemoteTaskID: "remote-mine", OriginSessionID: "gateway-default", OwnerTaskID: "tsk_a", State: task.StateRunning},
		{TaskID: "sibling", PeerNodeID: "peer", RemoteTaskID: "remote-sibling", OriginSessionID: "gateway-default", OwnerTaskID: "tsk_b", State: task.StateRunning},
		{TaskID: "theirs", PeerNodeID: "peer", RemoteTaskID: "remote-theirs", OriginSessionID: "session-b", State: task.StateRunning},
	} {
		if err := g.Store.UpsertPeerTask(ctx, seed); err != nil {
			t.Fatalf("UpsertPeerTask %s: %v", seed.TaskID, err)
		}
	}

	scoped := context.WithValue(ctx, provenanceContextKey{}, gatewayProvenance{TaskID: "tsk_a", OriginSessionID: "gateway-default"})
	resp, err := g.listTasksCall(scoped, 20, "")
	if err != nil || !resp.OK {
		t.Fatalf("listTasksCall resp=%+v err=%v", resp, err)
	}
	result := resp.Result.(map[string]any)
	delegated := result["delegated"].([]store.PeerTask)
	if len(delegated) != 1 || delegated[0].TaskID != "mine" {
		t.Fatalf("task token should see only tasks it delegated, got %+v", delegated)
	}
	if local := result["local"].([]task.Record); len(local) != 0 {
		t.Fatalf("task token must not see local tasks, got %+v", local)
	}

	hostResp, err := g.listTasksCall(ctx, 20, "")
	if err != nil || !hostResp.OK {
		t.Fatalf("host listTasksCall resp=%+v err=%v", hostResp, err)
	}
	if got := len(hostResp.Result.(map[string]any)["delegated"].([]store.PeerTask)); got != 3 {
		t.Fatalf("host token should see all delegated tasks, got %d", got)
	}
}

// A follow-up resumes the earlier task's runner session and reads its result,
// so a task token must not be able to follow up a task it did not delegate.
func TestTaskTokenCannotFollowUpAnotherTasksChild(t *testing.T) {
	ctx := context.Background()
	g := newTestGateway(t)
	if err := g.Store.UpsertPeerTask(ctx, store.PeerTask{TaskID: "sibling", PeerNodeID: "peer", RemoteTaskID: "remote-sibling", OriginSessionID: "gateway-default", OwnerTaskID: "tsk_b", State: task.StateDone}); err != nil {
		t.Fatal(err)
	}
	scoped := context.WithValue(ctx, provenanceContextKey{}, gatewayProvenance{TaskID: "tsk_a", OriginSessionID: "gateway-default"})
	resp, _ := g.delegateCall(scoped, DelegateRequest{Prompt: "continue", FollowupTaskID: "sibling"})
	if resp.OK || resp.Error == nil || resp.Error.Code != task.ErrorPeerUnauthorized {
		t.Fatalf("follow-up of another task's child should be peer_unauthorized, got %+v", resp)
	}
}

func TestResultTranscriptAndCancelReportUnreachablePeer(t *testing.T) {
	ctx := context.Background()
	g := newTestGateway(t)
	if err := g.Store.UpsertPeerTask(ctx, store.PeerTask{
		TaskID:          "task-1",
		PeerNodeID:      "missing-peer",
		RemoteTaskID:    "remote-1",
		OriginSessionID: "session-1",
		State:           task.StateRunning,
	}); err != nil {
		t.Fatalf("UpsertPeerTask: %v", err)
	}
	for name, call := range map[string]func() (ToolResponse, error){
		"result":     func() (ToolResponse, error) { return g.resultCall(ctx, "task-1") },
		"transcript": func() (ToolResponse, error) { return g.transcriptCall(ctx, "task-1") },
		"cancel":     func() (ToolResponse, error) { return g.cancelCall(ctx, "task-1", "network test") },
	} {
		resp, err := call()
		if err == nil || resp.OK {
			t.Fatalf("%s expected unreachable error, resp=%+v err=%v", name, resp, err)
		}
		if resp.Error == nil || resp.Error.Code != task.ErrorPeerUnreachable {
			t.Fatalf("%s error = %+v, want peer_unreachable", name, resp.Error)
		}
	}
}

func TestListTasksIncludesDelegatedMirror(t *testing.T) {
	ctx := context.Background()
	g := newTestGateway(t)
	if err := g.Store.UpsertPeerTask(ctx, store.PeerTask{
		TaskID:          "task-1",
		PeerNodeID:      "peer-1",
		RemoteTaskID:    "remote-1",
		OriginSessionID: "session-1",
		State:           task.StateDone,
	}); err != nil {
		t.Fatalf("UpsertPeerTask: %v", err)
	}
	resp, err := g.listTasksCall(ctx, 10, "")
	if err != nil {
		t.Fatalf("listTasksCall returned error: %v", err)
	}
	result := resp.Result.(map[string]any)
	delegated := result["delegated"].([]store.PeerTask)
	if len(delegated) != 1 || delegated[0].TaskID != "task-1" {
		t.Fatalf("delegated tasks = %+v", delegated)
	}
}

func TestFollowupRejectsExplicitDifferentPeer(t *testing.T) {
	ctx := context.Background()
	g := newTestGateway(t)
	g.Config.Peers = map[string]config.PeerConfig{
		"old": {Name: "old", NodeID: "old-node", Address: "127.0.0.1", Port: 1},
		"new": {Name: "new", NodeID: "new-node", Address: "127.0.0.1", Port: 1},
	}
	if err := g.Store.UpsertPeerTask(ctx, store.PeerTask{
		TaskID:       "task-1",
		PeerNodeID:   "old-node",
		RemoteTaskID: "task-1",
		State:        task.StateDone,
	}); err != nil {
		t.Fatalf("UpsertPeerTask: %v", err)
	}
	resp, err := g.delegateCall(ctx, DelegateRequest{FollowupTaskID: "task-1", Peer: "new", Prompt: "again", CWD: t.TempDir()})
	if err == nil || resp.OK {
		t.Fatalf("expected cross-peer followup rejection, resp=%+v err=%v", resp, err)
	}
}

func TestDelegateRejectsUnknownPeer(t *testing.T) {
	g := newTestGateway(t)
	resp, err := g.delegateCall(context.Background(), DelegateRequest{
		Peer:   "missing-peer",
		Prompt: "hello",
		CWD:    t.TempDir(),
	})
	if err == nil || resp.OK {
		t.Fatalf("expected unknown peer denial, resp=%+v err=%v", resp, err)
	}
	if resp.Error == nil || resp.Error.Code != task.ErrorPeerUnreachable {
		t.Fatalf("error = %+v, want peer_unreachable", resp.Error)
	}
}

func TestProfileUseRequiresTTLForLeasedProfileAndDelegateInheritsContext(t *testing.T) {
	ctx := context.Background()
	g := newTestGateway(t)
	cwd := t.TempDir()
	g.Config.Profiles["repo-dev"] = config.ProfileConfig{
		Runner:          "fake",
		Capabilities:    []string{profiledefs.CapReadFiles, profiledefs.CapEditRepo, profiledefs.CapExecuteInRepo},
		Claims:          []string{profiledefs.CapReadFiles, profiledefs.CapEditRepo, profiledefs.CapExecuteInRepo},
		Writes:          profiledefs.WritesRepoOnly,
		Shell:           profiledefs.ShellAllow,
		RequireExplicit: true,
		RequireLease:    true,
	}
	g.Config.Runners["fake"] = config.RunnerConfig{Command: []string{"fake"}, DefaultProfile: "readonly"}
	g.Config.Peers = map[string]config.PeerConfig{"worker": {Name: "worker", NodeID: "worker-node", Address: "127.0.0.1", Port: 1}}

	denied, _ := g.profileUseCall(ctx, ProfileUseRequest{Peer: "worker", Profile: "repo-dev", CWD: cwd})
	if denied.OK || denied.Error == nil || denied.Error.Code != task.ErrorProfileDenied {
		t.Fatalf("profile_use without ttl should be denied, got %+v", denied)
	}

	var got schema.CreateTaskRequest
	workerMux := http.NewServeMux()
	workerMux.HandleFunc("POST /v1/profile-leases", func(w http.ResponseWriter, r *http.Request) {
		var leaseReq schema.ProfileLeaseRequest
		if err := json.NewDecoder(r.Body).Decode(&leaseReq); err != nil {
			t.Fatalf("decode lease: %v", err)
		}
		if leaseReq.Profile != "repo-dev" || leaseReq.CWD != cwd || leaseReq.TTLSeconds <= 0 {
			t.Fatalf("lease request = %+v", leaseReq)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(schema.ProfileLease{
			LeaseID:   "lease_worker_1",
			Profile:   "repo-dev",
			CWD:       cwd,
			ExpiresAt: time.Now().Add(30 * time.Minute),
		})
	})
	workerMux.HandleFunc("POST /v1/tasks", func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode task: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(schema.CreateTaskResponse{TaskID: "remote-1", State: task.StateQueued})
	})
	worker := httptest.NewServer(workerMux)
	defer worker.Close()
	host, port := splitTestServerAddr(t, worker.URL)
	g.Config.Peers = map[string]config.PeerConfig{"worker": {Name: "worker", NodeID: "worker-node", Address: host, Port: port}}

	ack := profiledefs.ProfileAcknowledgement{Profile: "repo-dev", Writes: profiledefs.WritesRepoOnly, Shell: profiledefs.ShellAllow, Network: false, TTL: "30m", AllowedCWDScope: cwd}
	stillDenied, _ := g.profileUseCall(ctx, ProfileUseRequest{Peer: "worker", Profile: "repo-dev", CWD: cwd, TTL: "30m"})
	if stillDenied.OK || stillDenied.Error == nil || stillDenied.Error.Code != task.ErrorProfileAckRequired {
		t.Fatalf("profile_use without explicit acknowledgement should be denied, got %+v", stillDenied)
	}

	used, _ := g.profileUseCall(ctx, ProfileUseRequest{Peer: "worker", Profile: "repo-dev", CWD: cwd, TTL: "30m", ProfileAcknowledgement: ack})
	if !used.OK {
		t.Fatalf("profile_use with ttl failed: %+v", used)
	}
	resp, err := g.delegateCall(ctx, DelegateRequest{Peer: "worker", Prompt: "run tests"})
	if err != nil || !resp.OK {
		t.Fatalf("delegate with active context resp=%+v err=%v", resp, err)
	}
	if got.Profile != "repo-dev" || got.ProfileSource != "active_context" || got.ProfileContextID != "lease_worker_1" || got.CWD != cwd {
		t.Fatalf("delegated request did not inherit context: %+v", got)
	}
	local := resp.Result.(schema.CreateTaskResponse)
	if local.RequestedProfile != "repo-dev" || local.ProfileSource != "active_context" || local.ProfileContextID != "lease_worker_1" {
		t.Fatalf("local response missing profile metadata: %+v", local)
	}

	got = schema.CreateTaskRequest{}
	resp, err = g.delegateCall(ctx, DelegateRequest{Peer: "worker", Profile: "repo-dev", Prompt: "run tests explicitly"})
	if err != nil || !resp.OK {
		t.Fatalf("delegate with explicit active profile resp=%+v err=%v", resp, err)
	}
	if got.Profile != "repo-dev" || got.ProfileSource != "active_context" || got.ProfileContextID != "lease_worker_1" || got.CWD != cwd {
		t.Fatalf("explicit delegated request did not attach active lease: %+v", got)
	}

	otherCWD := t.TempDir()
	deniedDelegate, _ := g.delegateCall(ctx, DelegateRequest{Peer: "worker", Profile: "repo-dev", CWD: otherCWD, Prompt: "run tests elsewhere"})
	if deniedDelegate.OK || deniedDelegate.Error == nil || deniedDelegate.Error.Code != task.ErrorProfileDenied {
		t.Fatalf("explicit delegate with mismatched lease cwd = %+v", deniedDelegate)
	}
	deniedDelegate, _ = g.delegateCall(ctx, DelegateRequest{Peer: "worker", CWD: otherCWD, Prompt: "run tests elsewhere"})
	if deniedDelegate.OK || deniedDelegate.Error == nil || deniedDelegate.Error.Code != task.ErrorProfileDenied {
		t.Fatalf("implicit delegate with mismatched lease cwd = %+v", deniedDelegate)
	}
}

func TestProfileConfigureAddsProfileActiveAfterRestart(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	configPath := filepath.Join(root, "config.toml")
	body := `[node]
id = "origin"
name = "origin"
role = "origin"
mesh_port = 7778
mcp_port = 7777
data_dir = "` + filepath.ToSlash(filepath.Join(root, "data")) + `"

[policy]
allowed_peers = ["worker"]
allowed_cwds = ["` + filepath.ToSlash(root) + `"]

[profiles.query]
runner = "probe"
capabilities = ["disk_info", "system_info", "repo_status"]
writes = "deny"
shell = "deny"

[runners.probe]
command = ["builtin"]
default_profile = "query"

[peers.worker]
name = "worker"
node_id = "worker-node"
address = "127.0.0.1"
port = 1
`
	if err := os.WriteFile(configPath, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	st, err := store.OpenContext(ctx, filepath.Join(root, "store.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()
	g := &Gateway{
		Config:     cfg,
		ConfigPath: configPath,
		Store:      st,
		Audit:      audit.NewWriter(filepath.Join(root, "audit.jsonl"), cfg.Node.ID),
		Policy:     policy.New(cfg),
	}

	denied, _ := g.profileUseCall(ctx, ProfileUseRequest{Peer: "worker", Profile: "repo-dev", TTL: "30m"})
	if denied.OK || denied.Error == nil || denied.Error.Code != task.ErrorProfileUnavailable {
		t.Fatalf("profile_use before configure = %+v", denied)
	}

	preview, err := g.profileConfigureCall(ProfileConfigureRequest{Template: "repo-dev", Runner: "codex", Peer: "worker"})
	if err != nil || !preview.OK {
		t.Fatalf("profile_configure preview resp=%+v err=%v", preview, err)
	}
	previewResult := preview.Result.(map[string]any)
	previewTOML, _ := previewResult["preview_toml"].(string)
	if !strings.Contains(previewTOML, "[profiles.repo-dev]") || !strings.Contains(previewTOML, `allowed_peers = ["worker"]`) {
		t.Fatalf("preview missing repo-dev peer-scoped TOML: %s", previewTOML)
	}

	configured, err := g.profileConfigureCall(ProfileConfigureRequest{Template: "repo-dev", Runner: "codex", Peer: "worker", Confirmed: true})
	if err != nil || !configured.OK {
		t.Fatalf("profile_configure confirmed resp=%+v err=%v", configured, err)
	}
	// A new profile is written but not hot-loaded: the runner registry is built
	// at startup, so profiles only change on restart, and the response says so.
	configuredResult := configured.Result.(map[string]any)
	if configuredResult["active_immediately"] != false || configuredResult["requires_daemon_restart"] != true {
		t.Fatalf("profile_configure must report that a restart is required: %+v", configuredResult)
	}
	if held, _ := configuredResult["restart_required_for"].([]string); !containsString(held, "profiles") {
		t.Fatalf("restart_required_for should name profiles, got %+v", configuredResult["restart_required_for"])
	}
	if _, ok := g.Config.Profiles["repo-dev"]; ok {
		t.Fatal("a reload must not hot-load a new profile")
	}
	if stillUnavailable, _ := g.profileUseCall(ctx, ProfileUseRequest{Peer: "worker", Profile: "repo-dev", TTL: "30m"}); stillUnavailable.OK {
		t.Fatalf("profile_use before restart should still fail: %+v", stillUnavailable)
	}

	// After a restart the daemon loads the file afresh and the profile is usable.
	restarted, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("reload after restart: %v", err)
	}
	g = &Gateway{Config: restarted, ConfigPath: configPath, Store: st, Audit: g.Audit, Policy: policy.New(restarted)}
	if _, ok := g.Config.Profiles["repo-dev"]; !ok {
		t.Fatalf("restarted gateway is missing repo-dev: %#v", g.Config.Profiles)
	}
	workerMux := http.NewServeMux()
	workerMux.HandleFunc("POST /v1/profile-leases", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(schema.ProfileLease{
			LeaseID:   "lease_after_configure",
			Profile:   "repo-dev",
			CWD:       root,
			ExpiresAt: time.Now().Add(30 * time.Minute),
		})
	})
	worker := httptest.NewServer(workerMux)
	defer worker.Close()
	host, port := splitTestServerAddr(t, worker.URL)
	g.Config.Peers["worker"] = config.PeerConfig{Name: "worker", NodeID: "worker-node", Address: host, Port: port}
	ack := profiledefs.ProfileAcknowledgement{Profile: "repo-dev", Writes: profiledefs.WritesRepoOnly, Shell: profiledefs.ShellAllow, Network: false, TTL: "30m", AllowedCWDScope: root}
	used, _ := g.profileUseCall(ctx, ProfileUseRequest{Peer: "worker", Profile: "repo-dev", CWD: root, TTL: "30m", ProfileAcknowledgement: ack})
	if !used.OK {
		t.Fatalf("profile_use after configure failed: %+v", used)
	}
}

func TestProfileConfigureMCPToolIsCallable(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "config.toml")
	body := `[node]
id = "origin"
name = "origin"
role = "origin"
mesh_port = 7778
mcp_port = 7777
data_dir = "` + filepath.ToSlash(filepath.Join(root, "data")) + `"

[policy]
allowed_peers = ["worker"]
allowed_cwds = ["` + filepath.ToSlash(root) + `"]

[profiles.query]
runner = "probe"
capabilities = ["disk_info", "system_info", "repo_status"]
writes = "deny"
shell = "deny"

[runners.probe]
command = ["builtin"]
default_profile = "query"

[peers.worker]
name = "worker"
node_id = "worker-node"
address = "127.0.0.1"
port = 1
`
	if err := os.WriteFile(configPath, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	g := newTestGateway(t)
	g.Config = cfg
	g.ConfigPath = configPath
	g.Policy = policy.New(cfg)

	resp, err := g.callTool(context.Background(), "profile_configure", []byte(`{"template":"repo-dev","runner":"codex","peer":"worker"}`))
	if err != nil || !resp.OK {
		t.Fatalf("profile_configure callTool resp=%+v err=%v", resp, err)
	}
	bodyJSON, _ := json.Marshal(resp.Result)
	if !strings.Contains(string(bodyJSON), "preview_toml") || !strings.Contains(string(bodyJSON), "repo-dev") {
		t.Fatalf("profile_configure result missing preview: %s", bodyJSON)
	}
}

func TestGatewayProfileGuideAndRecommendTools(t *testing.T) {
	g := newTestGateway(t)
	guide, err := g.callTool(context.Background(), "profile_guide", nil)
	if err != nil || !guide.OK {
		t.Fatalf("profile_guide resp=%+v err=%v", guide, err)
	}
	body, _ := json.Marshal(guide.Result)
	for _, want := range []string{"query", "readonly-agent", "repo-dev", "no shell", "no writes"} {
		if !strings.Contains(strings.ToLower(string(body)), want) {
			t.Fatalf("profile_guide missing %q: %s", want, body)
		}
	}

	rec, err := g.callTool(context.Background(), "profile_recommend", []byte(`{"intent":"read files"}`))
	if err != nil || !rec.OK {
		t.Fatalf("profile_recommend resp=%+v err=%v", rec, err)
	}
	body, _ = json.Marshal(rec.Result)
	if !strings.Contains(string(body), `"profile":"readonly-agent"`) || !strings.Contains(string(body), `"safer_alternative":"query"`) {
		t.Fatalf("read files recommendation did not pick readonly-agent with query alternative: %s", body)
	}

	rec, err = g.callTool(context.Background(), "profile_recommend", []byte(`{"intent":"query battery remaining"}`))
	if err != nil || !rec.OK {
		t.Fatalf("profile_recommend battery resp=%+v err=%v", rec, err)
	}
	body, _ = json.Marshal(rec.Result)
	if !strings.Contains(string(body), `"profile":"readonly-agent"`) || !strings.Contains(string(body), `"first_tool":"inspect_peer"`) {
		t.Fatalf("battery recommendation did not pick inspect_peer readonly-agent: %s", body)
	}
}

func TestInspectPeerUsesReadonlyAgent(t *testing.T) {
	ctx := context.Background()
	g := newTestGateway(t)
	g.Config.Profiles[profiledefs.ReadonlyAgent] = config.ProfileConfig{
		Runner:       "fake",
		Capabilities: []string{profiledefs.CapReadFiles},
		Claims:       []string{profiledefs.CapReadFiles},
		Writes:       profiledefs.WritesDeny,
		Shell:        profiledefs.ShellAllow,
	}
	g.Config.Runners["fake"] = config.RunnerConfig{Command: []string{"fake"}, DefaultProfile: profiledefs.ReadonlyAgent}

	var got schema.CreateTaskRequest
	workerMux := http.NewServeMux()
	workerMux.HandleFunc("POST /v1/tasks", func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode task: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(schema.CreateTaskResponse{TaskID: "remote-inspect", State: task.StateQueued})
	})
	worker := httptest.NewServer(workerMux)
	defer worker.Close()
	host, port := splitTestServerAddr(t, worker.URL)
	g.Config.Peers = map[string]config.PeerConfig{"worker": {Name: "worker", NodeID: "worker-node", Address: host, Port: port}}

	resp, err := g.inspectPeerCall(ctx, InspectPeerRequest{Peer: "worker", Prompt: "how much battery is left?", Wait: true})
	if err != nil || !resp.OK {
		t.Fatalf("inspectPeerCall resp=%+v err=%v", resp, err)
	}
	if got.Profile != profiledefs.ReadonlyAgent || got.TaskKind != "inspect" {
		t.Fatalf("inspect request profile/task kind = %q/%q", got.Profile, got.TaskKind)
	}
	if !strings.Contains(got.Prompt, "Read-only inspection request") || !strings.Contains(got.Prompt, "how much battery is left?") {
		t.Fatalf("inspect prompt did not include read-only wrapper and user ask: %q", got.Prompt)
	}
}

func TestUnsupportedQueryPeerDeniesInsteadOfInspectFallback(t *testing.T) {
	ctx := context.Background()
	g := newTestGateway(t)
	g.Config.Profiles[profiledefs.Query] = config.ProfileConfig{
		Runner:       profiledefs.RunnerProbe,
		Capabilities: []string{profiledefs.CapDiskInfo},
		Writes:       profiledefs.WritesDeny,
		Shell:        profiledefs.ShellDeny,
	}
	g.Config.Profiles[profiledefs.ReadonlyAgent] = config.ProfileConfig{
		Runner:       "fake",
		Capabilities: []string{profiledefs.CapReadFiles},
		Claims:       []string{profiledefs.CapReadFiles},
		Writes:       profiledefs.WritesDeny,
		Shell:        profiledefs.ShellAllow,
	}
	g.Config.Runners[profiledefs.RunnerProbe] = config.RunnerConfig{Command: []string{"builtin"}, DefaultProfile: profiledefs.Query}
	g.Config.Runners["fake"] = config.RunnerConfig{Command: []string{"fake"}, DefaultProfile: profiledefs.ReadonlyAgent}

	resp, err := g.queryPeerCall(ctx, QueryPeerRequest{Peer: "worker", Kind: "battery_info", Wait: true})
	if err != nil {
		t.Fatalf("queryPeerCall err=%v", err)
	}
	if resp.OK || resp.Error == nil || resp.Error.Code != task.ErrorProfileDenied {
		t.Fatalf("queryPeerCall should deny unsupported kind, got %+v", resp)
	}
	if !strings.Contains(resp.Error.Detail, "inspect_peer") || !strings.Contains(resp.Error.Detail, "battery_info") {
		t.Fatalf("denial should point to inspect_peer and requested kind: %+v", resp.Error)
	}
}

func TestQueryPeerUsesActiveProfileContext(t *testing.T) {
	ctx := context.Background()
	g := newTestGateway(t)
	root := t.TempDir()
	g.Config.Profiles[profiledefs.QueryShell] = config.ProfileConfig{
		Runner:       profiledefs.RunnerShell,
		Capabilities: []string{profiledefs.CapShellReadonly, profiledefs.CapServiceStatus},
		Writes:       profiledefs.WritesDeny,
		Shell:        profiledefs.ShellAllowlisted,
	}
	g.Config.Runners[profiledefs.RunnerShell] = config.RunnerConfig{Command: []string{"builtin"}, DefaultProfile: profiledefs.QueryShell}

	var got schema.CreateTaskRequest
	workerMux := http.NewServeMux()
	workerMux.HandleFunc("POST /v1/tasks", func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode task: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(schema.CreateTaskResponse{TaskID: "remote-service", State: task.StateQueued})
	})
	worker := httptest.NewServer(workerMux)
	defer worker.Close()
	host, port := splitTestServerAddr(t, worker.URL)
	g.Config.Peers = map[string]config.PeerConfig{"worker": {Name: "worker", NodeID: "worker-node", Address: host, Port: port}}

	if err := g.writeProfileContext(profileContextFile{
		Peers: map[string]profileContextPeer{
			"worker": {Profile: profiledefs.QueryShell, CWD: root, Scope: "session", UpdatedAt: time.Now().UTC(), ExpiresAt: time.Now().Add(time.Hour)},
		},
	}); err != nil {
		t.Fatalf("writeProfileContext: %v", err)
	}

	resp, err := g.queryPeerCall(ctx, QueryPeerRequest{Peer: "worker", Kind: profiledefs.CapServiceStatus})
	if err != nil || !resp.OK {
		t.Fatalf("queryPeerCall resp=%+v err=%v", resp, err)
	}
	if got.Profile != profiledefs.QueryShell || got.ProfileSource != "active_context" || got.CWD != root || got.TaskKind != "query" {
		t.Fatalf("query_peer did not inherit active context: %+v", got)
	}
}

func TestQueryPeerDoesNotAutoRouteIntoExplicitProfile(t *testing.T) {
	ctx := context.Background()
	g := newTestGateway(t)
	g.Config.Profiles[profiledefs.Query] = config.ProfileConfig{
		Runner:       profiledefs.RunnerProbe,
		Capabilities: []string{profiledefs.CapDiskInfo},
		Writes:       profiledefs.WritesDeny,
		Shell:        profiledefs.ShellDeny,
		Risk:         profiledefs.RiskLow,
	}
	g.Config.Profiles[profiledefs.QueryNetwork] = config.ProfileConfig{
		Runner:          profiledefs.RunnerProbe,
		Capabilities:    []string{profiledefs.CapNetworkCheck},
		Writes:          profiledefs.WritesDeny,
		Shell:           profiledefs.ShellDeny,
		Network:         true,
		RequireExplicit: true,
		Risk:            profiledefs.RiskMedium,
	}
	g.Config.Runners[profiledefs.RunnerProbe] = config.RunnerConfig{Command: []string{"builtin"}, DefaultProfile: profiledefs.Query}
	called := false
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		t.Fatalf("query_peer silently routed into query-network")
	}))
	defer worker.Close()
	host, port := splitTestServerAddr(t, worker.URL)
	g.Config.Peers = map[string]config.PeerConfig{"worker": {Name: "worker", NodeID: "worker-node", Address: host, Port: port}}

	resp, err := g.queryPeerCall(ctx, QueryPeerRequest{Peer: "worker", Kind: profiledefs.CapNetworkCheck})
	if err != nil {
		t.Fatalf("queryPeerCall err=%v", err)
	}
	if resp.OK || resp.Error == nil || resp.Error.Code != task.ErrorProfileDenied {
		t.Fatalf("query_peer should deny silent escalation, got %+v", resp)
	}
	if called {
		t.Fatal("worker was called despite denial")
	}
	payload, ok := resp.Result.(map[string]any)
	if !ok || payload["nothing_executed"] != true || !strings.Contains(fmt.Sprint(payload["suggested_command"]), "query-network") {
		t.Fatalf("denial payload = %#v", resp.Result)
	}
}

func TestQueryPeerWithoutIdempotencyKeyCreatesFreshTasks(t *testing.T) {
	ctx := context.Background()
	g := newTestGateway(t)
	g.Config.Profiles[profiledefs.Query] = config.ProfileConfig{
		Runner:       profiledefs.RunnerProbe,
		Capabilities: []string{profiledefs.CapSystemInfo},
		Writes:       profiledefs.WritesDeny,
		Shell:        profiledefs.ShellDeny,
	}
	g.Config.Runners[profiledefs.RunnerProbe] = config.RunnerConfig{Command: []string{"builtin"}, DefaultProfile: profiledefs.Query}

	var ids []string
	workerMux := http.NewServeMux()
	workerMux.HandleFunc("POST /v1/tasks", func(w http.ResponseWriter, r *http.Request) {
		var got schema.CreateTaskRequest
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode task: %v", err)
		}
		ids = append(ids, got.TaskID)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(schema.CreateTaskResponse{TaskID: "remote-" + strconv.Itoa(len(ids)), State: task.StateQueued})
	})
	worker := httptest.NewServer(workerMux)
	defer worker.Close()
	host, port := splitTestServerAddr(t, worker.URL)
	g.Config.Peers = map[string]config.PeerConfig{"worker": {Name: "worker", NodeID: "worker-node", Address: host, Port: port}}

	for i := 0; i < 2; i++ {
		resp, err := g.queryPeerCall(ctx, QueryPeerRequest{Peer: "worker", Kind: profiledefs.CapSystemInfo})
		if err != nil || !resp.OK {
			t.Fatalf("queryPeerCall %d resp=%+v err=%v", i, resp, err)
		}
	}
	if len(ids) != 2 || ids[0] == ids[1] {
		t.Fatalf("query ids should be fresh, got %#v", ids)
	}
}

func TestProfileDescribeReturnsConfiguredContract(t *testing.T) {
	g := newTestGateway(t)
	g.Config.Profiles[profiledefs.Query] = config.ProfileConfig{
		Runner:                profiledefs.RunnerProbe,
		Capabilities:          []string{profiledefs.CapDiskInfo},
		Writes:                profiledefs.WritesDeny,
		Shell:                 profiledefs.ShellDeny,
		EnforcementConfidence: "high",
	}
	resp, err := g.profileDescribeCall(profiledefs.Query)
	if err != nil || !resp.OK {
		t.Fatalf("profileDescribeCall resp=%+v err=%v", resp, err)
	}
	result := resp.Result.(map[string]any)
	caps := result["capabilities"].([]string)
	if len(caps) != 1 || caps[0] != profiledefs.CapDiskInfo || result["writes"] != profiledefs.WritesDeny {
		t.Fatalf("profile description = %#v", result)
	}
}

func TestDelegateFanoutReturnsPeerKeyedResults(t *testing.T) {
	ctx := context.Background()
	g := newTestGateway(t)
	var okPrompt string
	okMux := http.NewServeMux()
	okMux.HandleFunc("POST /v1/tasks", func(w http.ResponseWriter, r *http.Request) {
		var raw struct {
			Prompt string `json:"prompt"`
		}
		if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
			t.Fatalf("decode ok task request: %v", err)
		}
		okPrompt = raw.Prompt
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"task_id":"remote-ok","state":"queued"}`))
	})
	okSrv := httptest.NewServer(okMux)
	defer okSrv.Close()
	failMux := http.NewServeMux()
	failMux.HandleFunc("POST /v1/tasks", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"task_id":"remote-denied","state":"denied","error":{"code":"peer_unauthorized","detail":"blocked by worker"}}`))
	})
	failSrv := httptest.NewServer(failMux)
	defer failSrv.Close()

	okHost, okPort := splitTestServerAddr(t, okSrv.URL)
	failHost, failPort := splitTestServerAddr(t, failSrv.URL)
	g.Config.Peers = map[string]config.PeerConfig{
		"ok":   {Name: "ok", NodeID: "ok-node", Address: okHost, Port: okPort},
		"fail": {Name: "fail", NodeID: "fail-node", Address: failHost, Port: failPort},
	}

	resp, err := g.delegateCall(ctx, DelegateRequest{
		Peers:  []string{"ok", "fail"},
		Prompt: "check disk space",
		CWD:    t.TempDir(),
	})
	if err != nil {
		t.Fatalf("delegateCall returned error: %v", err)
	}
	if resp.OK {
		t.Fatalf("fan-out should report partial failure: %+v", resp)
	}
	result, ok := resp.Result.(map[string]any)
	if !ok {
		t.Fatalf("result = %#v", resp.Result)
	}
	peers, ok := result["peers"].(map[string]ToolResponse)
	if !ok {
		t.Fatalf("peers result = %#v", result["peers"])
	}
	if !peers["ok"].OK {
		t.Fatalf("ok peer response = %+v", peers["ok"])
	}
	if peers["fail"].OK || peers["fail"].Error == nil || peers["fail"].Error.Code != task.ErrorPeerUnauthorized {
		t.Fatalf("fail peer response = %+v", peers["fail"])
	}
	if okPrompt != "check disk space" {
		t.Fatalf("ok peer prompt = %q", okPrompt)
	}
	mirror, err := g.Store.GetPeerTask(ctx, peers["ok"].Result.(schema.CreateTaskResponse).TaskID)
	if err != nil {
		t.Fatalf("GetPeerTask for ok peer: %v", err)
	}
	if mirror.PeerNodeID != "ok-node" || mirror.RemoteTaskID != "remote-ok" {
		t.Fatalf("ok peer mirror = %+v", mirror)
	}
}

func TestPeerSmokeRunsTaskAndVerifiesResult(t *testing.T) {
	ctx := context.Background()
	g := newTestGateway(t)
	var sawSmokePrompt bool
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/hello", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"node_id":"worker-node","node_name":"worker","schema_versions":["v1"],"runners":[{"name":"fake","profiles":["readonly"]}],"capabilities":["tasks"]}`))
	})
	mux.HandleFunc("POST /v1/tasks", func(w http.ResponseWriter, r *http.Request) {
		var raw struct {
			Prompt string `json:"prompt"`
		}
		if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
			t.Fatalf("decode task request: %v", err)
		}
		sawSmokePrompt = strings.Contains(raw.Prompt, gatewaySmokeExpectedResponse)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"task_id":"remote-smoke","state":"done"}`))
	})
	mux.HandleFunc("GET /v1/tasks/remote-smoke/result", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"task_id":"remote-smoke","result":{"status":"done","command_output":{"command":"fake","exit_code":0,"stdout":"cmesh smoke ok\n","stderr":"","duration_s":1},"worker_summary_untrusted":"cmesh smoke ok","usage":{},"verification_required":true}}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	host, portText, err := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatalf("SplitHostPort: %v", err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatalf("Atoi: %v", err)
	}
	g.Config.Peers = map[string]config.PeerConfig{
		"worker": {Name: "worker", NodeID: "worker-node", Address: host, Port: port},
	}
	resp, err := g.testPeerCall(ctx, "worker", t.TempDir(), "readonly", 5)
	if err != nil || !resp.OK {
		t.Fatalf("testPeerCall resp=%+v err=%v", resp, err)
	}
	if !sawSmokePrompt {
		t.Fatal("peer did not receive smoke prompt")
	}
	result := resp.Result.(map[string]any)
	checks := result["checks"].(map[string]bool)
	for _, name := range []string{"peer_reachable", "policy_allowed", "task_created", "runner_invoked", "prompt_received", "smoke_response_verified", "result_envelope_parsed", "audit_event_written"} {
		if !checks[name] {
			t.Fatalf("check %s = false in %+v", name, checks)
		}
	}
}

func splitTestServerAddr(t *testing.T, rawURL string) (string, int) {
	t.Helper()
	host, portText, err := net.SplitHostPort(strings.TrimPrefix(rawURL, "http://"))
	if err != nil {
		t.Fatalf("SplitHostPort: %v", err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatalf("Atoi: %v", err)
	}
	return host, port
}

func TestGatewaySmokeRequiresCommandOutput(t *testing.T) {
	if !gatewaySmokeResultMatches(&task.ResultEnvelope{
		CommandOutput: task.CommandOutput{Command: "runner", ExitCode: 0, Stdout: gatewaySmokeExpectedResponse + "\n"},
	}, "readonly") {
		t.Fatal("raw command artifact should pass smoke verification")
	}
	if gatewaySmokeResultMatches(&task.ResultEnvelope{
		WorkerSummaryUntrusted: gatewaySmokeExpectedResponse,
	}, "readonly") {
		t.Fatal("summary-only smoke response must not pass verification")
	}
}

func TestReadOnlyFollowupDoesNotRequireHeadCommit(t *testing.T) {
	ctx := context.Background()
	g := newTestGateway(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/tasks/task-1/result", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"task_id":"task-1","result":{"status":"done","runner_extra":{"session_id":"sess-1"}}}`))
	})
	mux.HandleFunc("POST /v1/tasks", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"task_id":"task-2","state":"queued"}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	host, portText, err := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatalf("SplitHostPort: %v", err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatalf("Atoi: %v", err)
	}
	g.Config.Peers = map[string]config.PeerConfig{
		"worker": {Name: "worker", NodeID: "worker-node", Address: host, Port: port},
	}
	if err := g.Store.UpsertPeerTask(ctx, store.PeerTask{
		TaskID:          "task-1",
		PeerNodeID:      "worker-node",
		RemoteTaskID:    "task-1",
		OriginSessionID: "session-1",
		State:           task.StateDone,
	}); err != nil {
		t.Fatalf("UpsertPeerTask: %v", err)
	}
	resp, err := g.delegateCall(ctx, DelegateRequest{FollowupTaskID: "task-1", Prompt: "continue inspection", CWD: t.TempDir(), Profile: "readonly"})
	if err != nil || !resp.OK {
		t.Fatalf("read-only followup should not require head_commit, resp=%+v err=%v", resp, err)
	}
}

func TestRegisterHarnessAdviceUsesHTTPTransport(t *testing.T) {
	g := newTestGateway(t)
	g.Config.Node.MCPPort = 7777
	resp, err := g.callTool(context.Background(), "register_harness", []byte(`{}`))
	if err != nil || !resp.OK {
		t.Fatalf("register_harness returned resp=%+v err=%v", resp, err)
	}
	result, ok := resp.Result.(map[string]any)
	if !ok {
		t.Fatalf("result = %#v", resp.Result)
	}
	got, ok := result["claude_code_command"].(string)
	if !ok {
		t.Fatalf("claude_code_command missing from %#v", result)
	}
	want := "claude mcp add --transport http cmesh http://127.0.0.1:7777/mcp"
	if got != want {
		t.Fatalf("claude_code_command = %q, want %q", got, want)
	}
}

func TestRegisterHarnessAdviceIncludesRedactedHostTokenHeader(t *testing.T) {
	g := newTestGateway(t)
	g.Config.Node.MCPPort = 7777
	g.Config.Gateway.HostToken = "secret"
	resp, err := g.callTool(context.Background(), "register_harness", []byte(`{}`))
	if err != nil || !resp.OK {
		t.Fatalf("register_harness returned resp=%+v err=%v", resp, err)
	}
	result, ok := resp.Result.(map[string]any)
	if !ok {
		t.Fatalf("result = %#v", resp.Result)
	}
	got, ok := result["claude_code_command"].(string)
	if !ok {
		t.Fatalf("claude_code_command missing from %#v", result)
	}
	want := "claude mcp add --transport http cmesh http://127.0.0.1:7777/mcp --header 'X-Cmesh-Host-Token: <redacted>'"
	if got != want {
		t.Fatalf("claude_code_command = %q, want %q", got, want)
	}
	if result["claude_code_headers"] != true {
		t.Fatalf("claude_code_headers = %#v, want true", result["claude_code_headers"])
	}
	if strings.Contains(got, "secret") {
		t.Fatalf("claude_code_command leaked host token: %q", got)
	}
}

func TestTaskCatalogIncludesFeedbackRecipes(t *testing.T) {
	g := newTestGateway(t)
	resp, err := g.callTool(context.Background(), "task_catalog", []byte(`{}`))
	if err != nil || !resp.OK {
		t.Fatalf("task_catalog returned resp=%+v err=%v", resp, err)
	}
	result, ok := resp.Result.(map[string]any)
	if !ok {
		t.Fatalf("result = %#v", resp.Result)
	}
	recipes, ok := result["recipes"].([]TaskRecipe)
	if !ok {
		t.Fatalf("recipes = %#v", result["recipes"])
	}
	byID := map[string]TaskRecipe{}
	for _, recipe := range recipes {
		byID[recipe.ID] = recipe
	}
	for _, id := range []string{"disk_check", "repo_status", "native_build", "service_log_summary", "test_runner", "branch_handoff"} {
		recipe, ok := byID[id]
		if !ok {
			t.Fatalf("missing recipe %q in %+v", id, recipes)
		}
		if recipe.DefaultProfile == "" || recipe.PromptTemplate == "" || len(recipe.ExpectedEvidence) == 0 || len(recipe.OriginChecks) == 0 {
			t.Fatalf("recipe %q is incomplete: %+v", id, recipe)
		}
	}
	tools := mcpTools()
	foundTool := false
	for _, tool := range tools {
		if tool["name"] == "task_catalog" {
			foundTool = true
			break
		}
	}
	if !foundTool {
		t.Fatal("task_catalog is not advertised in MCP tools")
	}
}

func TestMCPToolSchemasUseArrayRequired(t *testing.T) {
	for _, tool := range mcpTools() {
		schema, ok := tool["inputSchema"].(map[string]any)
		if !ok {
			t.Fatalf("tool %q inputSchema = %#v", tool["name"], tool["inputSchema"])
		}
		if _, ok := schema["required"].([]string); !ok {
			t.Fatalf("tool %q required = %#v, want []string", tool["name"], schema["required"])
		}
	}
	body, err := json.Marshal(mcpTools())
	if err != nil {
		t.Fatalf("marshal tools: %v", err)
	}
	if strings.Contains(string(body), `"required":null`) {
		t.Fatalf("tool schemas contain null required: %s", body)
	}
}

func TestCurrentProvenanceIgnoresDelegateRequestWithoutTaskToken(t *testing.T) {
	t.Setenv("CMESH_ORIGIN_NODE_ID", "ambient-origin")
	t.Setenv("CMESH_ORIGIN_SESSION_ID", "ambient-session")
	t.Setenv("CMESH_DELEGATION_CHAIN", `["ambient"]`)
	t.Setenv("CMESH_HOP_COUNT", "9")

	g := newTestGateway(t)
	prov, err := g.currentProvenance(context.Background(), DelegateRequest{
		OriginNodeID:    "wire-origin",
		OriginSessionID: "wire-session",
		DelegationChain: []string{"wire-origin", "wire-worker"},
		HopCount:        1,
	})
	if err != nil {
		t.Fatalf("currentProvenance: %v", err)
	}
	if prov.OriginNodeID != "" || prov.OriginSessionID != "" || prov.HopCount != 0 || len(prov.Chain) != 0 {
		t.Fatalf("request/env provenance should not be authoritative without task token: %+v", prov)
	}
}

func TestCurrentProvenanceUsesTaskTokenContext(t *testing.T) {
	g := newTestGateway(t)
	token := "task-token"
	hash := task.SHA256Hex([]byte(token))
	dir := filepath.Join(g.Config.Node.DataDir, "task-tokens")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	body := `{"task_id":"task-1","origin_node_id":"origin","origin_session_id":"session","worker_node_id":"worker","runner":"fake","profile":"dev","delegation_chain":["origin","worker"],"hop_count":1,"task_token_hash":"` + hash + `"}`
	if err := os.WriteFile(filepath.Join(dir, hash+".json"), []byte(body), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	prov, err := g.currentProvenance(context.Background(), DelegateRequest{TaskToken: token})
	if err != nil {
		t.Fatalf("currentProvenance: %v", err)
	}
	if prov.OriginNodeID != "origin" || prov.OriginSessionID != "session" || prov.HopCount != 1 {
		t.Fatalf("provenance = %+v", prov)
	}
	if len(prov.Chain) != 2 || prov.Chain[1] != "worker" {
		t.Fatalf("chain = %+v", prov.Chain)
	}
}

func newTestGateway(t *testing.T) *Gateway {
	t.Helper()
	return newTestGatewayWithStore(t, filepath.Join(t.TempDir(), "cmesh.db"))
}

func newTestGatewayWithStore(t *testing.T, storePath string) *Gateway {
	t.Helper()
	st, err := store.Open(storePath)
	if err != nil {
		t.Fatalf("Open store: %v", err)
	}
	t.Cleanup(func() {
		_ = st.Close()
	})
	cfg := &config.Config{
		Node: config.NodeConfig{ID: "origin", Name: "origin", DataDir: t.TempDir()},
		Policy: config.PolicyConfig{
			AllowedPeers: []string{"peer"},
			AllowedCWDs:  []string{filepath.Join(t.TempDir(), "**")},
			HopLimit:     2,
		},
		Profiles: map[string]config.ProfileConfig{
			"dev":      {Runner: "fake", Claims: []string{"read_files", "edit_repo"}, RequireExplicit: true, RequireLease: true},
			"readonly": {Runner: "fake", Claims: []string{"read_files"}},
		},
		Runners: map[string]config.RunnerConfig{
			// dev grants edit_repo, so it is high-risk and requires an explicit
			// request plus a lease. Point the implicit default at readonly so
			// profile-less delegation in these tests exercises the normal path.
			"fake": {Command: []string{"fake"}, DefaultProfile: "readonly"},
		},
		Peers: map[string]config.PeerConfig{},
	}
	return &Gateway{Config: cfg, Store: st, Audit: audit.NewWriter(filepath.Join(t.TempDir(), "audit.jsonl"), cfg.Node.ID), Policy: policy.New(cfg)}
}

// repair_peers must diagnose, rewrite and reload the same file: the one the
// daemon was started with. Reading config.DefaultPath() while the reload reads
// g.ConfigPath let a daemon started with --config repair one file and report
// another, untouched one as active.
func TestRepairPeersUsesDaemonConfigPath(t *testing.T) {
	dir := t.TempDir()
	// The default location holds nothing; only the daemon's --config file exists.
	t.Setenv("CMESH_CONFIG", filepath.Join(dir, "default-missing.toml"))
	custom := filepath.Join(dir, "custom.toml")
	body := "[node]\nid = \"node-a\"\nname = \"node-a\"\n" +
		"mesh_port = 7778\nmcp_port = 7777\n\n" +
		"[policy]\nallowed_peers = [\"node-a\"]\nallowed_cwds = [\"~/code/**\"]\nhop_limit = 2\n\n" +
		"[profiles.look]\nrunner = \"claude-code\"\ncapabilities = [\"read_files\"]\n" +
		"writes = \"deny\"\nshell = \"deny\"\nrisk = \"medium\"\n\n" +
		"[runners.claude-code]\ncommand = [\"/usr/local/bin/claude\", \"-p\"]\ndefault_profile = \"look\"\n"
	if err := os.WriteFile(custom, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	g := newTestGateway(t)
	g.ConfigPath = custom

	resp, err := g.repairPeersCall(context.Background(), nil)
	if err != nil || !resp.OK {
		t.Fatalf("repair_peers should load the daemon's config file, resp=%+v err=%v", resp, err)
	}
	result := resp.Result.(map[string]any)
	if msg, _ := result["message"].(string); !strings.Contains(msg, "No [peers.*] entries") {
		t.Fatalf("repair_peers diagnosed the wrong file: %+v", result)
	}
}

// Two worker tasks that share a session and send the same delegation must get
// separate child tasks, each owned by the task whose token sent it. If they
// collapsed onto one deterministic id, the second would be handed the first
// one's task and result.
func TestTaskTokenDelegationsAreOwnedPerTask(t *testing.T) {
	ctx := context.Background()
	g := newTestGateway(t)
	var created []schema.CreateTaskRequest
	workerMux := http.NewServeMux()
	workerMux.HandleFunc("POST /v1/tasks", func(w http.ResponseWriter, r *http.Request) {
		var req schema.CreateTaskRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode task: %v", err)
		}
		created = append(created, req)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(schema.CreateTaskResponse{TaskID: req.TaskID, State: task.StateQueued})
	})
	worker := httptest.NewServer(workerMux)
	defer worker.Close()
	host, port := splitTestServerAddr(t, worker.URL)
	g.Config.Peers = map[string]config.PeerConfig{"peer": {Name: "peer", NodeID: "peer", Address: host, Port: port}}

	cwd := t.TempDir()
	ids := map[string]string{}
	for _, owner := range []string{"tsk_a", "tsk_b"} {
		scoped := context.WithValue(ctx, provenanceContextKey{}, gatewayProvenance{
			TaskID:          owner,
			OriginNodeID:    "origin",
			OriginSessionID: "gateway-default",
		})
		resp, err := g.delegateCall(scoped, DelegateRequest{Peer: "peer", Prompt: "same request", CWD: cwd, IdempotencyKey: "same-key"})
		if err != nil || !resp.OK {
			t.Fatalf("delegate as %s resp=%+v err=%v", owner, resp, err)
		}
		ids[owner] = resp.Result.(schema.CreateTaskResponse).TaskID
	}
	if ids["tsk_a"] == ids["tsk_b"] {
		t.Fatalf("delegations from different tasks collapsed onto one id %q", ids["tsk_a"])
	}
	for owner, id := range ids {
		mirrored, err := g.Store.GetPeerTask(ctx, id)
		if err != nil {
			t.Fatalf("GetPeerTask %s: %v", id, err)
		}
		if mirrored.OwnerTaskID != owner {
			t.Fatalf("task %s owner = %q, want %q", id, mirrored.OwnerTaskID, owner)
		}
	}
	if len(created) != 2 {
		t.Fatalf("worker saw %d creates, want 2", len(created))
	}
}

func containsString(values []string, needle string) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
}

// A reload (repair_peers, profile_configure) applies peers and policy only. A
// stray profile edit on disk must not ride along with a peer fix; it is
// reported as needing a restart instead.
func TestReloadConfigHoldsProfileChanges(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	write := func(writes, peerAddr string) {
		body := "[node]\nid = \"node-a\"\nname = \"node-a\"\n" +
			"mesh_port = 7778\nmcp_port = 7777\ndata_dir = \"" + filepath.ToSlash(filepath.Join(dir, "data")) + "\"\n\n" +
			"[policy]\nallowed_peers = [\"worker\"]\nallowed_cwds = [\"~/code/**\"]\nhop_limit = 2\n\n" +
			"[profiles.look]\nrunner = \"claude-code\"\ncapabilities = [\"read_files\"]\n" +
			"writes = \"" + writes + "\"\nshell = \"deny\"\nrisk = \"medium\"\n\n" +
			"[runners.claude-code]\ncommand = [\"/usr/local/bin/claude\", \"-p\"]\ndefault_profile = \"look\"\n\n" +
			"[peers.worker]\nname = \"worker\"\nnode_id = \"worker-node\"\naddress = \"" + peerAddr + "\"\n"
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("deny", "100.64.0.1")
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	g := newTestGateway(t)
	g.Config, g.ConfigPath, g.Policy = cfg, path, policy.New(cfg)

	// A peer fix lands on disk alongside an unrelated profile edit.
	write("repo_only", "100.64.0.9")
	held, err := g.reloadConfig()
	if err != nil {
		t.Fatalf("reloadConfig: %v", err)
	}
	if len(held) != 1 || held[0] != "profiles" {
		t.Fatalf("held = %v, want [profiles]", held)
	}
	if got := g.Config.Peers["worker"].Address; got != "100.64.0.9" {
		t.Fatalf("peer repair should be live, address = %q", got)
	}
	if got := g.Config.Profiles["look"].Writes; got != "deny" {
		t.Fatalf("profile edit must wait for restart, writes = %q", got)
	}
	if got := g.Policy.Config().Profiles["look"].Writes; got != "deny" {
		t.Fatalf("policy's view of the profile changed on reload, writes = %q", got)
	}
	if note := restartRequiredNote(held); !strings.Contains(note, "profiles") || !strings.Contains(note, "restart") {
		t.Fatalf("restart note = %q", note)
	}
}
