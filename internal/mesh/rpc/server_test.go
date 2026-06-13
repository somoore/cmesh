package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
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

func TestCreateTaskRejectsMismatchedOriginClaim(t *testing.T) {
	root := t.TempDir()
	server := newTestServer(t, root, immediateRunner{})

	createBody := `{
		"task_id":"task-1",
		"origin_node_id":"evil-peer",
		"origin_session_id":"session-1",
		"prompt":"hello",
		"cwd":` + strconv.Quote(root) + `,
		"runner":"fake",
		"profile":"dev",
		"provenance":{"origin_node_id":"evil-peer","origin_session_id":"session-1"}
	}`
	createReq := httptest.NewRequest(http.MethodPost, "/v1/tasks", strings.NewReader(createBody))
	createReq.RemoteAddr = "100.64.0.1:12345"
	createRes := httptest.NewRecorder()
	server.Handler().ServeHTTP(createRes, createReq)
	if createRes.Code != http.StatusForbidden {
		t.Fatalf("create status = %d, body = %s", createRes.Code, createRes.Body.String())
	}
	if _, err := server.Store.GetTask(context.Background(), "task-1"); err == nil {
		t.Fatal("forged task should not be stored")
	}
}

func TestCreateTaskDerivesHopCountFromVerifiedChain(t *testing.T) {
	root := t.TempDir()
	server := newTestServer(t, root, immediateRunner{})

	createBody := `{
		"task_id":"task-hop",
		"origin_node_id":"origin",
		"origin_session_id":"session-1",
		"prompt":"hello",
		"cwd":` + strconv.Quote(root) + `,
		"runner":"fake",
		"profile":"dev",
		"provenance":{
			"origin_node_id":"origin",
			"origin_session_id":"session-1",
			"delegation_chain":["origin","mac-studio"],
			"hop_count":0
		}
	}`
	createReq := httptest.NewRequest(http.MethodPost, "/v1/tasks", strings.NewReader(createBody))
	createReq.RemoteAddr = "100.64.0.2:12345"
	createRes := httptest.NewRecorder()
	server.Handler().ServeHTTP(createRes, createReq)
	if createRes.Code != http.StatusAccepted {
		t.Fatalf("create status = %d, body = %s", createRes.Code, createRes.Body.String())
	}
	waitForTerminalTask(t, server, "task-hop")
	rec, err := server.Store.GetTask(context.Background(), "task-hop")
	if err != nil {
		t.Fatalf("stored task: %v", err)
	}
	if rec.HopCount != 1 {
		t.Fatalf("hop count = %d, want derived chain length 1", rec.HopCount)
	}
}

func TestCreateTaskRejectsCyclicDelegationChain(t *testing.T) {
	root := t.TempDir()
	server := newTestServer(t, root, immediateRunner{})

	createBody := `{
		"task_id":"task-cycle",
		"origin_node_id":"mac-studio",
		"origin_session_id":"session-1",
		"prompt":"hello",
		"cwd":` + strconv.Quote(root) + `,
		"runner":"fake",
		"profile":"dev",
		"provenance":{
			"origin_node_id":"mac-studio",
			"origin_session_id":"session-1",
			"delegation_chain":["mac-studio","mac-studio"],
			"hop_count":1
		}
	}`
	createReq := httptest.NewRequest(http.MethodPost, "/v1/tasks", strings.NewReader(createBody))
	createReq.RemoteAddr = "100.64.0.1:12345"
	createRes := httptest.NewRecorder()
	server.Handler().ServeHTTP(createRes, createReq)
	if createRes.Code != http.StatusForbidden {
		t.Fatalf("create status = %d, body = %s", createRes.Code, createRes.Body.String())
	}
}

func TestTaskAccessRequiresOriginOrDirectRequester(t *testing.T) {
	root := t.TempDir()
	server := newTestServer(t, root, immediateRunner{})

	createBody := `{
		"task_id":"task-1",
		"origin_node_id":"mac-studio",
		"origin_session_id":"session-1",
		"prompt":"hello",
		"cwd":` + strconv.Quote(root) + `,
		"runner":"fake",
		"profile":"dev",
		"provenance":{"origin_node_id":"mac-studio","origin_session_id":"session-1"}
	}`
	createReq := httptest.NewRequest(http.MethodPost, "/v1/tasks", strings.NewReader(createBody))
	createReq.RemoteAddr = "100.64.0.1:12345"
	createRes := httptest.NewRecorder()
	server.Handler().ServeHTTP(createRes, createReq)
	if createRes.Code != http.StatusAccepted {
		t.Fatalf("create status = %d, body = %s", createRes.Code, createRes.Body.String())
	}
	waitForTerminalTask(t, server, "task-1")

	server.Identity = fakeIdentity{nodeID: "node-other", nodeName: "windows-vm"}

	statusReq := httptest.NewRequest(http.MethodGet, "/v1/tasks/task-1", nil)
	statusReq.RemoteAddr = "100.64.0.3:12345"
	statusRes := httptest.NewRecorder()
	server.Handler().ServeHTTP(statusRes, statusReq)
	if statusRes.Code != http.StatusForbidden {
		t.Fatalf("status code = %d, body = %s", statusRes.Code, statusRes.Body.String())
	}

	cancelReq := httptest.NewRequest(http.MethodPost, "/v1/tasks/task-1/cancel", nil)
	cancelReq.RemoteAddr = "100.64.0.3:12345"
	cancelRes := httptest.NewRecorder()
	server.Handler().ServeHTTP(cancelRes, cancelReq)
	if cancelRes.Code != http.StatusForbidden {
		t.Fatalf("cancel code = %d, body = %s", cancelRes.Code, cancelRes.Body.String())
	}

	listReq := httptest.NewRequest(http.MethodGet, "/v1/tasks", nil)
	listReq.RemoteAddr = "100.64.0.3:12345"
	listRes := httptest.NewRecorder()
	server.Handler().ServeHTTP(listRes, listReq)
	if listRes.Code != http.StatusOK {
		t.Fatalf("list code = %d, body = %s", listRes.Code, listRes.Body.String())
	}
	var listed struct {
		Tasks []task.Record `json:"tasks"`
	}
	if err := json.Unmarshal(listRes.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(listed.Tasks) != 0 {
		t.Fatalf("unauthorized peer saw tasks: %+v", listed.Tasks)
	}
}

func waitForTerminalTask(t *testing.T, server *Server, taskID string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		rec, err := server.Store.GetTask(context.Background(), taskID)
		if err == nil && rec.State.Terminal() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	rec, err := server.Store.GetTask(context.Background(), taskID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	t.Fatalf("task %s did not reach terminal state, got %s", taskID, rec.State)
}

func TestSubdelegationDirectRequesterCanAccessDownstreamTask(t *testing.T) {
	rec := task.Record{
		ID:              "task-1",
		OriginNodeID:    "mac-studio",
		DelegationChain: []string{"mac-studio", "windows-vm", "homelab-vm"},
	}
	if !taskAccessibleTo(rec, tsid.Identity{NodeID: "node-win", NodeName: "windows-vm"}) {
		t.Fatal("direct subdelegation requester should access downstream task")
	}
	if taskAccessibleTo(rec, tsid.Identity{NodeID: "node-other", NodeName: "linux-vm"}) {
		t.Fatal("unrelated peer should not access downstream task")
	}
}

func TestCreateTaskRecordsAuditEvents(t *testing.T) {
	root := t.TempDir()
	server := newTestServer(t, root, immediateRunner{})
	auditPath := filepath.Join(t.TempDir(), "audit.jsonl")
	server.Audit = audit.NewWriter(auditPath, server.Config.Node.ID)

	createBody := `{
		"task_id":"task-audit",
		"origin_node_id":"mac-studio",
		"origin_session_id":"session-1",
		"prompt":"hello",
		"cwd":` + strconv.Quote(root) + `,
		"runner":"fake",
		"profile":"dev",
		"provenance":{"origin_node_id":"mac-studio","origin_session_id":"session-1"}
	}`
	createReq := httptest.NewRequest(http.MethodPost, "/v1/tasks", strings.NewReader(createBody))
	createReq.RemoteAddr = "100.64.0.1:12345"
	createRes := httptest.NewRecorder()
	server.Handler().ServeHTTP(createRes, createReq)
	if createRes.Code != http.StatusAccepted {
		t.Fatalf("create status = %d, body = %s", createRes.Code, createRes.Body.String())
	}
	waitForTerminalTask(t, server, "task-audit")
	body, err := os.ReadFile(auditPath)
	if err != nil {
		t.Fatalf("ReadFile audit: %v", err)
	}
	text := string(body)
	for _, want := range []string{`"type":"task.dispatch.accepted"`, `"type":"task.completed"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("audit log missing %s:\n%s", want, text)
		}
	}
}

func TestPrepareGitRejectsDirtyWorktree(t *testing.T) {
	ctx := context.Background()
	ensureGitOnPath(t)
	root := t.TempDir()
	runGit(t, root, "init")
	runGit(t, root, "config", "user.name", "Test User")
	runGit(t, root, "config", "user.email", "test@example.com")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	runGit(t, root, "add", "README.md")
	runGit(t, root, "commit", "-m", "initial")
	if err := os.WriteFile(filepath.Join(root, "dirty.txt"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatalf("WriteFile dirty: %v", err)
	}
	rec := task.Record{ID: "task-dirty", CWD: root, ProfileRequested: "dev"}
	_, err := prepareGit(ctx, rec, "worker", t.TempDir(), []string{"edit_repo"}, gitPreparationOptions{})
	if !errors.Is(err, errDirtyWorktree) {
		t.Fatalf("prepareGit error = %v, want errDirtyWorktree", err)
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

func TestTaskMCPConfigCarriesTaskTokenAndSecretsAreRemoved(t *testing.T) {
	dataDir := t.TempDir()
	metadata := runner.TaskMetadata{
		TaskID:          "task-1",
		OriginNodeID:    "origin",
		OriginSessionID: "session",
		WorkerNodeID:    "worker",
		Runner:          "fake",
		Profile:         "dev",
		DelegationChain: []string{"origin", "worker"},
		HopCount:        1,
		TaskToken:       "task-token",
	}
	if _, err := writeTaskContextFile(dataDir, metadata); err != nil {
		t.Fatalf("writeTaskContextFile: %v", err)
	}
	mcpPath, err := writeTaskMCPConfigFile(dataDir, 7777, metadata)
	if err != nil {
		t.Fatalf("writeTaskMCPConfigFile: %v", err)
	}
	body, err := os.ReadFile(mcpPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var raw struct {
		MCPServers map[string]struct {
			Type    string            `json:"type"`
			URL     string            `json:"url"`
			Headers map[string]string `json:"headers"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	cmesh := raw.MCPServers["cmesh"]
	if cmesh.Type != "http" || cmesh.URL != "http://127.0.0.1:7777/mcp" {
		t.Fatalf("cmesh MCP server = %+v", cmesh)
	}
	if got := cmesh.Headers["Authorization"]; got != "Bearer task-token" {
		t.Fatalf("Authorization header = %q", got)
	}

	hash := task.SHA256Hex([]byte("task-token"))
	tokenPath := filepath.Join(dataDir, taskTokenDirName, hash+".json")
	if _, err := os.Stat(tokenPath); err != nil {
		t.Fatalf("token context missing before removal: %v", err)
	}
	if err := removeTaskSecrets(dataDir, "task-1"); err != nil {
		t.Fatalf("removeTaskSecrets: %v", err)
	}
	if _, err := os.Stat(tokenPath); !os.IsNotExist(err) {
		t.Fatalf("token context still exists or unexpected error: %v", err)
	}
	if _, err := os.Stat(mcpPath); !os.IsNotExist(err) {
		t.Fatalf("MCP config still exists or unexpected error: %v", err)
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

type immediateRunner struct{}

func (immediateRunner) Name() string {
	return "fake"
}

func (immediateRunner) Manifest() runner.CapabilityManifest {
	return runner.CapabilityManifest{
		SchemaVersion: "runner_manifest.v1",
		Runner:        "fake",
		Profiles: map[string]runner.ProfileManifest{
			"dev": {Claims: []string{"read_files", "edit_repo"}, EnforcementConfidence: "high"},
		},
	}
}

func (immediateRunner) Check(profile string) error {
	return nil
}

func (immediateRunner) Run(req runner.RunRequest) (runner.Result, error) {
	return runner.Result{WorkerSummaryUntrusted: "done"}, nil
}

type fakeIdentity struct {
	nodeID   string
	nodeName string
}

func (fakeIdentity) Status(context.Context) (tsid.Status, error) {
	return tsid.Status{TailscaleIPs: []string{"100.64.0.2"}}, nil
}

func (f fakeIdentity) WhoIs(context.Context, string) (tsid.Identity, error) {
	nodeID := f.nodeID
	if nodeID == "" {
		nodeID = "node-mac"
	}
	nodeName := f.nodeName
	if nodeName == "" {
		nodeName = "mac-studio"
	}
	return tsid.Identity{NodeID: nodeID, NodeName: nodeName}, nil
}

func (fakeIdentity) Self(context.Context) (tsid.Identity, error) {
	return tsid.Identity{NodeID: "node-worker", NodeName: "homelab-vm"}, nil
}

func ensureGitOnPath(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err == nil {
		return
	}
	for _, dir := range []string{`C:\Program Files\Git\cmd`, `/usr/bin`} {
		if _, err := os.Stat(filepath.Join(dir, "git.exe")); err == nil {
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			return
		}
		if _, err := os.Stat(filepath.Join(dir, "git")); err == nil {
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			return
		}
	}
	t.Skip("git not found on PATH")
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}
