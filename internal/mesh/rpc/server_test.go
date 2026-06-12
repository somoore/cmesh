package rpc

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"cmesh/internal/audit"
	"cmesh/internal/config"
	tsid "cmesh/internal/identity/tailscale"
	"cmesh/internal/policy"
	"cmesh/internal/runner"
	"cmesh/internal/store"
	"cmesh/internal/task"
)

func TestCancelSignalsRunningTask(t *testing.T) {
	root := t.TempDir()
	run := &blockingRunner{started: make(chan time.Duration, 1), cancelled: make(chan struct{})}
	server := newTestServer(t, root, run)

	createBody := `{
		"task_id":"task-1",
		"origin_node_id":"mac-studio",
		"origin_session_id":"session-1",
		"prompt":"wait until cancelled",
		"cwd":` + strconv.Quote(root) + `,
		"runner":"fake",
		"profile":"dev",
		"timeout_s":60,
		"provenance":{"origin_node_id":"mac-studio","origin_session_id":"session-1"}
	}`
	createReq := httptest.NewRequest(http.MethodPost, "/v1/tasks", strings.NewReader(createBody))
	createReq.RemoteAddr = "100.64.0.1:12345"
	createRes := httptest.NewRecorder()
	server.Handler().ServeHTTP(createRes, createReq)
	if createRes.Code != http.StatusAccepted {
		t.Fatalf("create status = %d, body = %s", createRes.Code, createRes.Body.String())
	}

	select {
	case timeout := <-run.started:
		if timeout != 10*time.Millisecond {
			t.Fatalf("timeout = %v, want profile max_runtime ceiling", timeout)
		}
	case <-time.After(time.Second):
		t.Fatal("runner did not start")
	}

	cancelReq := httptest.NewRequest(http.MethodPost, "/v1/tasks/task-1/cancel", nil)
	cancelReq.RemoteAddr = "100.64.0.1:12345"
	cancelRes := httptest.NewRecorder()
	server.Handler().ServeHTTP(cancelRes, cancelReq)
	if cancelRes.Code != http.StatusOK {
		t.Fatalf("cancel status = %d, body = %s", cancelRes.Code, cancelRes.Body.String())
	}

	select {
	case <-run.cancelled:
	case <-time.After(time.Second):
		t.Fatal("runner context was not cancelled")
	}

	rec, err := server.Store.GetTask(context.Background(), "task-1")
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if rec.State != task.StateCancelled {
		t.Fatalf("state = %q, want cancelled", rec.State)
	}
}

func TestEffectiveRuntimeLimitHasDefaultCeiling(t *testing.T) {
	server := &Server{Config: &config.Config{Profiles: map[string]config.ProfileConfig{
		"dev": {Runner: "fake"},
	}}}
	if got := server.effectiveRuntimeLimit("dev", 0); got != defaultRuntimeLimit {
		t.Fatalf("default timeout = %v, want %v", got, defaultRuntimeLimit)
	}
	if got := server.effectiveRuntimeLimit("dev", int((defaultRuntimeLimit+time.Hour)/time.Second)); got != defaultRuntimeLimit {
		t.Fatalf("oversized requested timeout = %v, want %v", got, defaultRuntimeLimit)
	}
	if got := server.effectiveRuntimeLimit("dev", 30); got != 30*time.Second {
		t.Fatalf("short requested timeout = %v, want 30s", got)
	}
}

func newTestServer(t *testing.T, root string, run runner.Runner) *Server {
	t.Helper()
	cfg := &config.Config{
		Node: config.NodeConfig{ID: "homelab-vm", Name: "homelab-vm", MeshPort: 7778, MCPPort: 7777, DataDir: t.TempDir()},
		Policy: config.PolicyConfig{
			AllowedPeers: []string{"mac-studio"},
			AllowedCWDs:  []string{filepath.Join(root, "**")},
			HopLimit:     2,
		},
		Profiles: map[string]config.ProfileConfig{
			"dev": {Runner: "fake", Claims: []string{"read_files", "edit_repo"}, MaxRuntime: config.Duration{Duration: 10 * time.Millisecond}},
		},
		Runners: map[string]config.RunnerConfig{
			"fake": {Command: []string{"fake"}, DefaultProfile: "dev"},
		},
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "cmesh.db"))
	if err != nil {
		t.Fatalf("Open store: %v", err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Fatalf("Close store: %v", err)
		}
	})
	reg := runner.NewRegistry()
	if err := reg.Register(run); err != nil {
		t.Fatalf("Register runner: %v", err)
	}
	return &Server{
		Config:   cfg,
		Store:    st,
		Audit:    audit.NewWriter(filepath.Join(t.TempDir(), "audit.jsonl"), cfg.Node.ID),
		Policy:   policy.New(cfg),
		Identity: fakeIdentity{},
		Runners:  reg,
	}
}

type blockingRunner struct {
	started   chan time.Duration
	cancelled chan struct{}
}

func (r *blockingRunner) Name() string {
	return "fake"
}

func (r *blockingRunner) Manifest() runner.CapabilityManifest {
	return runner.CapabilityManifest{
		SchemaVersion: "runner_manifest.v1",
		Runner:        "fake",
		Profiles: map[string]runner.ProfileManifest{
			"dev": {Claims: []string{"read_files", "edit_repo"}, EnforcementConfidence: "high"},
		},
	}
}

func (r *blockingRunner) Check(profile string) error {
	return nil
}

func (r *blockingRunner) Run(req runner.RunRequest) (runner.Result, error) {
	r.started <- req.Timeout
	<-req.Context.Done()
	close(r.cancelled)
	return runner.Result{WorkerSummaryUntrusted: "cancelled"}, runner.E(runner.ErrCancelled, "fake runner", req.Context.Err())
}

type fakeIdentity struct{}

func (fakeIdentity) Status(context.Context) (tsid.Status, error) {
	return tsid.Status{TailscaleIPs: []string{"100.64.0.2"}}, nil
}

func (fakeIdentity) WhoIs(context.Context, string) (tsid.Identity, error) {
	return tsid.Identity{NodeID: "node-mac", NodeName: "mac-studio"}, nil
}

func (fakeIdentity) Self(context.Context) (tsid.Identity, error) {
	return tsid.Identity{NodeID: "node-worker", NodeName: "homelab-vm"}, nil
}
