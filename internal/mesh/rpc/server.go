package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"cmesh/internal/audit"
	"cmesh/internal/config"
	"cmesh/internal/gitx"
	tsid "cmesh/internal/identity/tailscale"
	"cmesh/internal/mesh/schema"
	"cmesh/internal/policy"
	"cmesh/internal/runner"
	"cmesh/internal/store"
	"cmesh/internal/task"
)

const maxRequestBytes = 2 << 20

type Server struct {
	Config   *config.Config
	Store    *store.Store
	Audit    *audit.Writer
	Policy   *policy.Engine
	Identity tsid.Provider
	Runners  *runner.Registry
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/hello", s.handleHello)
	mux.HandleFunc("POST /v1/tasks", s.handleCreateTask)
	mux.HandleFunc("GET /v1/tasks/{task_id}", s.handleTaskStatus)
	mux.HandleFunc("GET /v1/tasks/{task_id}/result", s.handleTaskResult)
	mux.HandleFunc("POST /v1/tasks/{task_id}/cancel", s.handleCancel)
	return withSchemaVersion(mux)
}

func (s *Server) ListenAndServe(ctx context.Context) error {
	addr, err := s.BindAddress(ctx)
	if err != nil {
		return err
	}
	lc := net.ListenConfig{}
	ln, err := lc.Listen(ctx, "tcp", addr)
	if err != nil {
		return err
	}
	server := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	if err := server.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func (s *Server) BindAddress(ctx context.Context) (string, error) {
	bind := s.Config.Tailnet.Bind
	if bind == "" || bind == "tailnet_ip" {
		status, err := s.Identity.Status(ctx)
		if err != nil {
			return "", err
		}
		if len(status.TailscaleIPs) == 0 {
			return "", fmt.Errorf("tailscale status returned no tailnet IP")
		}
		bind = status.TailscaleIPs[0]
	}
	if bind == "0.0.0.0" || bind == "::" || bind == "" {
		return "", fmt.Errorf("mesh RPC refuses wildcard bind address %q", bind)
	}
	return net.JoinHostPort(bind, fmt.Sprintf("%d", s.Config.Node.MeshPort)), nil
}

func (s *Server) handleHello(w http.ResponseWriter, r *http.Request) {
	var runners []schema.Runner
	for _, manifest := range s.Runners.Manifests() {
		profiles := make([]string, 0, len(manifest.Profiles))
		for profile := range manifest.Profiles {
			profiles = append(profiles, profile)
		}
		runners = append(runners, schema.Runner{Name: manifest.Runner, Version: manifest.Version, Profiles: profiles})
	}
	writeJSON(w, http.StatusOK, schema.HelloResponse{
		NodeID:         s.Config.Node.ID,
		NodeName:       s.Config.Node.Name,
		SchemaVersions: []string{schema.Version},
		Runners:        runners,
		Capabilities:   []string{"tasks", "audit", "idempotency", "policy", "runner_manifests"},
		ServerTime:     time.Now().UTC(),
	})
}

func (s *Server) handleCreateTask(w http.ResponseWriter, r *http.Request) {
	if err := requireSchema(r); err != nil {
		writeError(w, http.StatusBadRequest, task.ErrorVersionMismatch, err.Error())
		return
	}
	identity, ok := s.authenticate(w, r)
	if !ok {
		return
	}
	body, err := readBody(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, task.ErrorWorkerFailed, err.Error())
		return
	}
	var req schema.CreateTaskRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, task.ErrorWorkerFailed, "invalid JSON: "+err.Error())
		return
	}
	if req.TaskID == "" {
		req.TaskID, err = task.NewID()
		if err != nil {
			writeError(w, http.StatusInternalServerError, task.ErrorWorkerFailed, err.Error())
			return
		}
	}
	if req.Provenance.OriginNodeID == "" {
		req.Provenance.OriginNodeID = req.OriginNodeID
	}
	if req.Provenance.OriginSessionID == "" {
		req.Provenance.OriginSessionID = req.OriginSessionID
	}
	req.Provenance.WorkerNodeID = s.Config.Node.ID
	req.Provenance.PromptHash = task.PromptHash(req.Prompt)

	intent := policy.TaskIntent{
		PeerName:     identity.NodeName,
		PeerNodeID:   identity.NodeID,
		CWD:          req.CWD,
		Runner:       req.Runner,
		Profile:      req.Profile,
		HopCount:     req.Provenance.HopCount,
		Chain:        req.Provenance.DelegationChain,
		OriginNodeID: req.Provenance.OriginNodeID,
		AllowDirty:   req.AllowDirty,
	}
	decision := s.Policy.CheckInbound(intent)
	rec := task.Record{
		ID:               req.TaskID,
		OriginNodeID:     firstNonEmpty(req.Provenance.OriginNodeID, req.OriginNodeID, identity.NodeID),
		OriginSessionID:  firstNonEmpty(req.Provenance.OriginSessionID, req.OriginSessionID),
		WorkerNodeID:     s.Config.Node.ID,
		State:            task.StateQueued,
		CWD:              req.CWD,
		Runner:           firstNonEmpty(req.Runner, runnerFromProfile(s.Config, req.Profile)),
		ProfileRequested: firstNonEmpty(req.Profile, "dev"),
		ProfileEffective: decision.EffectiveProfile,
		PromptHash:       req.Provenance.PromptHash,
		AllowDirty:       req.AllowDirty,
		DelegationChain:  append(req.Provenance.DelegationChain, s.Config.Node.ID),
		HopCount:         req.Provenance.HopCount,
	}
	outcome, err := s.Store.BeginDispatch(r.Context(), rec, body, task.DefaultIdempotencyDays*24*time.Hour)
	if err != nil {
		status := http.StatusInternalServerError
		code := task.ErrorWorkerFailed
		if errors.Is(err, store.ErrTaskConflict) || errors.Is(err, store.ErrTaskIDOwnedByDifferentOrigin) {
			status = http.StatusConflict
			code = task.ErrorTaskConflict
		}
		writeError(w, status, code, err.Error())
		return
	}
	if outcome.Replayed {
		writeJSON(w, http.StatusOK, schema.CreateTaskResponse{TaskID: outcome.Task.ID, State: outcome.Task.State, Replayed: true})
		return
	}
	if !decision.Allowed {
		_, _ = s.Store.UpdateTaskState(r.Context(), outcome.Task.ID, store.StateUpdate{State: task.StateDenied, TerminalErrorCode: task.ErrorCode(decision.Code)})
		s.record(r.Context(), audit.Event{
			Type:           "task.dispatch.denied",
			TaskID:         outcome.Task.ID,
			PeerIdentity:   identity.NodeName,
			OriginSession:  rec.OriginSessionID,
			Chain:          rec.DelegationChain,
			Runner:         rec.Runner,
			Profile:        rec.ProfileRequested,
			PolicyDecision: decision.Code,
			Source:         "mesh_rpc",
			ErrorCode:      decision.Code,
			ErrorDetail:    decision.Detail,
		})
		writeJSON(w, http.StatusForbidden, schema.CreateTaskResponse{
			TaskID: outcome.Task.ID,
			State:  task.StateDenied,
			Error:  &schema.Error{Code: task.ErrorCode(decision.Code), Detail: decision.Detail},
		})
		return
	}
	s.record(r.Context(), audit.Event{
		Type:           "task.dispatch.accepted",
		TaskID:         outcome.Task.ID,
		PeerIdentity:   identity.NodeName,
		OriginSession:  rec.OriginSessionID,
		Chain:          rec.DelegationChain,
		Runner:         rec.Runner,
		Profile:        decision.EffectiveProfile,
		PolicyDecision: "allowed",
		Source:         "mesh_rpc",
		Claims:         decision.Claims,
	})
	go s.runTask(context.Background(), outcome.Task, req)
	if req.Wait {
		s.waitForTask(w, r, outcome.Task.ID, time.Duration(req.TimeoutSeconds)*time.Second)
		return
	}
	writeJSON(w, http.StatusAccepted, schema.CreateTaskResponse{TaskID: outcome.Task.ID, State: task.StateQueued})
}

func (s *Server) runTask(ctx context.Context, rec task.Record, req schema.CreateTaskRequest) {
	updated, err := s.Store.UpdateTaskState(ctx, rec.ID, store.StateUpdate{State: task.StateRunning})
	if err != nil {
		return
	}
	rec = updated
	s.record(ctx, audit.Event{Type: "task.started", TaskID: rec.ID, OriginSession: rec.OriginSessionID, Chain: rec.DelegationChain, Runner: rec.Runner, Profile: rec.ProfileEffective, Source: "worker"})
	run, ok := s.Runners.Get(rec.Runner)
	if !ok {
		s.failTask(ctx, rec, task.ErrorRunnerUnavailable, "runner is not registered")
		return
	}
	if err := run.Check(rec.ProfileEffective); err != nil {
		s.failTask(ctx, rec, task.ErrorCode(runner.CodeOf(err)), err.Error())
		return
	}
	base, branch, gitAvailable, err := prepareGit(ctx, rec, s.Config.Node.Name)
	if err != nil {
		s.failTask(ctx, rec, task.ErrorDirtyWorktree, err.Error())
		return
	}
	timeout := time.Duration(req.TimeoutSeconds) * time.Second
	logDir := filepath.Join(s.Config.Node.DataDir, "logs", rec.ID)
	result, err := run.Run(runner.RunRequest{
		TaskID:  rec.ID,
		Prompt:  req.Prompt,
		CWD:     rec.CWD,
		Profile: rec.ProfileEffective,
		Timeout: timeout,
		LogDir:  logDir,
		Metadata: runner.TaskMetadata{
			OriginNodeID:    rec.OriginNodeID,
			OriginSessionID: rec.OriginSessionID,
			WorkerNodeID:    rec.WorkerNodeID,
			Runner:          rec.Runner,
			Profile:         rec.ProfileEffective,
		},
		OutputLimit: runner.DefaultOutputLimit,
	})
	if gitAvailable {
		head, diffstat, files, commitErr := commitGit(ctx, rec, branch, base, s.Config.Node.Name)
		if commitErr != nil && err == nil {
			err = commitErr
		}
		result.Branch = branch
		result.BaseCommit = base
		result.HeadCommit = head
		result.Diffstat = diffstat
		result.FilesChanged = files
		result.RecommendedOriginChecks = append(result.RecommendedOriginChecks,
			"git fetch worker "+branch,
			"git diff --stat "+base+".."+head,
		)
	}
	if err != nil {
		code := task.ErrorWorkerFailed
		if rc := runner.CodeOf(err); rc != "" {
			code = task.ErrorCode(rc)
		}
		result.Status = string(task.StateFailed)
		ref := result.TranscriptRef
		_, _ = s.Store.UpdateTaskState(ctx, rec.ID, store.StateUpdate{State: task.StateFailed, TerminalErrorCode: code, Result: resultAsTask(result), ResultRef: ref})
		s.record(ctx, audit.Event{Type: "task.failed", TaskID: rec.ID, OriginSession: rec.OriginSessionID, Chain: rec.DelegationChain, Runner: rec.Runner, Profile: rec.ProfileEffective, Source: "worker", ErrorCode: string(code), ErrorDetail: err.Error()})
		return
	}
	result.Status = string(task.StateDone)
	ref := result.TranscriptRef
	_, _ = s.Store.UpdateTaskState(ctx, rec.ID, store.StateUpdate{State: task.StateDone, Result: resultAsTask(result), ResultRef: ref})
	s.record(ctx, audit.Event{Type: "task.completed", TaskID: rec.ID, OriginSession: rec.OriginSessionID, Chain: rec.DelegationChain, Runner: rec.Runner, Profile: rec.ProfileEffective, Source: "worker"})
}

func (s *Server) handleTaskStatus(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.authenticate(w, r); !ok {
		return
	}
	rec, err := s.Store.GetTask(r.Context(), r.PathValue("task_id"))
	if err != nil {
		writeError(w, http.StatusNotFound, task.ErrorWorkerFailed, err.Error())
		return
	}
	elapsed := time.Since(rec.CreatedAt)
	if !rec.CompletedAt.IsZero() {
		elapsed = rec.CompletedAt.Sub(rec.CreatedAt)
	}
	writeJSON(w, http.StatusOK, schema.TaskStatusResponse{
		TaskID:            rec.ID,
		State:             rec.State,
		CreatedAt:         rec.CreatedAt,
		UpdatedAt:         rec.UpdatedAt,
		ElapsedSeconds:    int64(elapsed / time.Second),
		LastActivity:      string(rec.State),
		TerminalErrorCode: rec.TerminalErrorCode,
	})
}

func (s *Server) handleTaskResult(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.authenticate(w, r); !ok {
		return
	}
	rec, err := s.Store.GetTask(r.Context(), r.PathValue("task_id"))
	if err != nil {
		writeError(w, http.StatusNotFound, task.ErrorWorkerFailed, err.Error())
		return
	}
	if rec.Result == nil {
		writeError(w, http.StatusNotFound, task.ErrorTranscriptUnavailable, "result is not available")
		return
	}
	writeJSON(w, http.StatusOK, schema.TaskResultResponse{TaskID: rec.ID, Result: rec.Result})
}

func (s *Server) handleCancel(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.authenticate(w, r); !ok {
		return
	}
	taskID := r.PathValue("task_id")
	rec, err := s.Store.UpdateTaskState(r.Context(), taskID, store.StateUpdate{State: task.StateCancelled, TerminalErrorCode: task.ErrorCancelled})
	if err != nil {
		writeError(w, http.StatusBadRequest, task.ErrorCancelled, err.Error())
		return
	}
	s.record(r.Context(), audit.Event{Type: "task.cancelled", TaskID: taskID, Source: "mesh_rpc", ErrorCode: string(task.ErrorCancelled)})
	writeJSON(w, http.StatusOK, schema.CancelTaskResponse{TaskID: taskID, State: rec.State})
}

func (s *Server) waitForTask(w http.ResponseWriter, r *http.Request, taskID string, timeout time.Duration) {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-deadline.C:
			writeJSON(w, http.StatusAccepted, schema.CreateTaskResponse{TaskID: taskID, State: task.StateRunning})
			return
		case <-ticker.C:
			rec, err := s.Store.GetTask(r.Context(), taskID)
			if err != nil {
				writeError(w, http.StatusInternalServerError, task.ErrorWorkerFailed, err.Error())
				return
			}
			if rec.State.Terminal() {
				writeJSON(w, http.StatusOK, schema.CreateTaskResponse{TaskID: taskID, State: rec.State})
				return
			}
		case <-r.Context().Done():
			return
		}
	}
}

func (s *Server) authenticate(w http.ResponseWriter, r *http.Request) (tsid.Identity, bool) {
	if !tsid.IsTailnetIP(r.RemoteAddr) {
		writeError(w, http.StatusForbidden, task.ErrorPeerUnauthorized, "remote address is not a tailnet IP")
		return tsid.Identity{}, false
	}
	id, err := s.Identity.WhoIs(r.Context(), r.RemoteAddr)
	if err != nil {
		writeError(w, http.StatusForbidden, task.ErrorPeerUnauthorized, err.Error())
		return tsid.Identity{}, false
	}
	if id.NodeID == "" && id.NodeName == "" {
		writeError(w, http.StatusForbidden, task.ErrorPeerUnauthorized, "tailscale whois returned no node identity")
		return tsid.Identity{}, false
	}
	return id, true
}

func prepareGit(ctx context.Context, rec task.Record, nodeName string) (base, branch string, available bool, err error) {
	repo, err := gitx.Open(rec.CWD)
	if errors.Is(err, gitx.ErrGitUnavailable) {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, err
	}
	status, err := repo.Status(ctx)
	if err != nil {
		return "", "", true, err
	}
	if !status.IsRepo {
		return "", "", false, nil
	}
	if status.Dirty && !rec.AllowDirty {
		return "", "", true, fmt.Errorf("%w: %s", gitx.ErrGitUnavailable, "dirty worktree refused")
	}
	base = status.HeadCommit
	branch, err = gitx.DeterministicBranch(rec.ID, rec.ProfileRequested)
	if err != nil {
		return "", "", true, err
	}
	return base, branch, true, repo.CheckoutBranch(ctx, branch)
}

func commitGit(ctx context.Context, rec task.Record, branch, base, nodeName string) (head, diffstat string, files []string, err error) {
	repo, err := gitx.Open(rec.CWD)
	if err != nil {
		return "", "", nil, err
	}
	if err := repo.AddAll(ctx); err != nil {
		return "", "", nil, err
	}
	head, err = repo.Commit(ctx, gitx.CommitOptions{
		AuthorName:  "cmesh",
		AuthorEmail: "cmesh@" + nodeName,
		Subject:     "cmesh task " + rec.ID,
		TaskID:      rec.ID,
		OriginNode:  rec.OriginNodeID,
		WorkerNode:  rec.WorkerNodeID,
		Profile:     rec.ProfileEffective,
		AllowEmpty:  true,
	})
	if err != nil {
		return "", "", nil, err
	}
	diffstat, _ = repo.DiffStat(ctx, base, head)
	files, _ = repo.ChangedFiles(ctx, base, head)
	return head, diffstat, files, nil
}

func resultAsTask(result runner.Result) *task.ResultEnvelope {
	status := task.State(result.Status)
	if status == "" {
		status = task.StateDone
	}
	out := task.ResultEnvelope{
		Status:                  status,
		Branch:                  result.Branch,
		BaseCommit:              result.BaseCommit,
		HeadCommit:              result.HeadCommit,
		Pushed:                  result.Pushed,
		Diffstat:                result.Diffstat,
		FilesChanged:            result.FilesChanged,
		Tests:                   task.TestClaims{ClaimedPassed: result.Tests.ClaimedPassed, Commands: result.Tests.Commands, LogsRef: result.Tests.LogsRef},
		WorkerSummaryUntrusted:  result.WorkerSummaryUntrusted,
		Usage:                   task.Usage{Tokens: int64(result.Usage.Tokens), Cost: result.Usage.Cost, DurationS: result.Usage.DurationS},
		VerificationRequired:    result.VerificationRequired,
		RecommendedOriginChecks: result.RecommendedOriginChecks,
		RunnerExtra:             result.RunnerExtra,
	}
	return &out
}

func (s *Server) failTask(ctx context.Context, rec task.Record, code task.ErrorCode, detail string) {
	if code == "" {
		code = task.ErrorWorkerFailed
	}
	_, _ = s.Store.UpdateTaskState(ctx, rec.ID, store.StateUpdate{State: task.StateFailed, TerminalErrorCode: code})
	s.record(ctx, audit.Event{Type: "task.failed", TaskID: rec.ID, OriginSession: rec.OriginSessionID, Chain: rec.DelegationChain, Runner: rec.Runner, Profile: rec.ProfileEffective, Source: "worker", ErrorCode: string(code), ErrorDetail: detail})
}

func (s *Server) record(ctx context.Context, event audit.Event) {
	written, err := s.Audit.Append(event)
	if err == nil {
		_ = s.Store.RecordAuditEvent(ctx, written)
	}
}

func withSchemaVersion(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(schema.HeaderSchemaVersion, schema.Version)
		next.ServeHTTP(w, r)
	})
}

func requireSchema(r *http.Request) error {
	got := r.Header.Get(schema.HeaderSchemaVersion)
	if got == "" || got == schema.Version {
		return nil
	}
	return fmt.Errorf("unsupported schema version %q", got)
}

func readBody(r *http.Request) ([]byte, error) {
	defer r.Body.Close()
	return io.ReadAll(io.LimitReader(r.Body, maxRequestBytes))
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, code task.ErrorCode, detail string) {
	writeJSON(w, status, schema.Error{Code: code, Detail: detail})
}

func runnerFromProfile(cfg *config.Config, profileName string) string {
	if profileName != "" {
		if profile, ok := cfg.Profiles[profileName]; ok {
			return profile.Runner
		}
	}
	if profile, ok := cfg.Profiles["dev"]; ok {
		return profile.Runner
	}
	for _, profile := range cfg.Profiles {
		return profile.Runner
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}
