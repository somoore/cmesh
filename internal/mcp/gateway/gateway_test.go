package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"cmesh/internal/config"
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

func TestCurrentProvenancePrefersDelegateRequest(t *testing.T) {
	t.Setenv("CMESH_ORIGIN_NODE_ID", "ambient-origin")
	t.Setenv("CMESH_ORIGIN_SESSION_ID", "ambient-session")
	t.Setenv("CMESH_DELEGATION_CHAIN", `["ambient"]`)
	t.Setenv("CMESH_HOP_COUNT", "9")

	g := newTestGateway(t)
	prov := g.currentProvenance(DelegateRequest{
		OriginNodeID:    "wire-origin",
		OriginSessionID: "wire-session",
		DelegationChain: []string{"wire-origin", "wire-worker"},
		HopCount:        1,
	})
	if prov.OriginNodeID != "wire-origin" || prov.OriginSessionID != "wire-session" {
		t.Fatalf("origin provenance = %+v", prov)
	}
	if prov.HopCount != 1 {
		t.Fatalf("hop count = %d, want 1", prov.HopCount)
	}
	if len(prov.Chain) != 2 || prov.Chain[0] != "wire-origin" || prov.Chain[1] != "wire-worker" {
		t.Fatalf("chain = %+v", prov.Chain)
	}
}

func newTestGateway(t *testing.T) *Gateway {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "cmesh.db"))
	if err != nil {
		t.Fatalf("Open store: %v", err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Fatalf("Close store: %v", err)
		}
	})
	cfg := &config.Config{
		Node: config.NodeConfig{ID: "origin", Name: "origin", DataDir: t.TempDir()},
		Policy: config.PolicyConfig{
			AllowedPeers: []string{"peer"},
			AllowedCWDs:  []string{filepath.Join(t.TempDir(), "**")},
			HopLimit:     2,
		},
		Profiles: map[string]config.ProfileConfig{
			"dev": {Runner: "fake", Claims: []string{"read_files"}},
		},
		Runners: map[string]config.RunnerConfig{
			"fake": {Command: []string{"fake"}},
		},
		Peers: map[string]config.PeerConfig{},
	}
	return &Gateway{Config: cfg, Store: st, Policy: policy.New(cfg)}
}
