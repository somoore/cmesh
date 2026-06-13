package gateway

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"cmesh/internal/audit"
	"cmesh/internal/config"
	"cmesh/internal/mesh/schema"
	"cmesh/internal/policy"
	"cmesh/internal/store"
	"cmesh/internal/task"
)

func TestHardenLocalRequiresHostTokenWhenConfigured(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	handler := hardenLocal(next, "secret")

	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/health", nil)
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusForbidden {
		t.Fatalf("status without token = %d, want forbidden", res.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "http://127.0.0.1/health", nil)
	req.Header.Set("X-Cmesh-Host-Token", "secret")
	res = httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusNoContent {
		t.Fatalf("status with token = %d, want no content", res.Code)
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
	resp, err := g.listTasksCall(ctx, 10)
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
	}) {
		t.Fatal("raw command artifact should pass smoke verification")
	}
	if gatewaySmokeResultMatches(&task.ResultEnvelope{
		WorkerSummaryUntrusted: gatewaySmokeExpectedResponse,
	}) {
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
			"dev":      {Runner: "fake", Claims: []string{"read_files", "edit_repo"}},
			"readonly": {Runner: "fake", Claims: []string{"read_files"}},
		},
		Runners: map[string]config.RunnerConfig{
			"fake": {Command: []string{"fake"}},
		},
		Peers: map[string]config.PeerConfig{},
	}
	return &Gateway{Config: cfg, Store: st, Audit: audit.NewWriter(filepath.Join(t.TempDir(), "audit.jsonl"), cfg.Node.ID), Policy: policy.New(cfg)}
}
