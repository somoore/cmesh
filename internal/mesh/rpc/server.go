package rpc

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"cmesh/internal/audit"
	"cmesh/internal/config"
	"cmesh/internal/gitx"
	tsid "cmesh/internal/identity/tailscale"
	"cmesh/internal/mesh/schema"
	"cmesh/internal/policy"
	profiledefs "cmesh/internal/profile"
	"cmesh/internal/runner"
	"cmesh/internal/store"
	"cmesh/internal/task"
	"cmesh/internal/version"
)

const (
	maxRequestBytes     = 2 << 20
	defaultRuntimeLimit = 2 * time.Hour
	taskContextDirName  = "task-contexts"
	taskMCPConfigDir    = "task-mcp"
	taskTokenDirName    = "task-tokens"
)

type Server struct {
	Config   *config.Config
	Store    *store.Store
	Audit    *audit.Writer
	Policy   *policy.Engine
	Leases   *policy.ProfileLeaseStore
	Identity tsid.Provider
	Runners  *runner.Registry

	runningMu sync.Mutex
	running   map[string]context.CancelFunc
	limitsMu  sync.Mutex
	limits    map[string]chan struct{}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/hello", s.handleHello)
	mux.HandleFunc("GET /v1/tasks", s.handleListTasks)
	mux.HandleFunc("POST /v1/tasks", s.handleCreateTask)
	mux.HandleFunc("GET /v1/tasks/{task_id}", s.handleTaskStatus)
	mux.HandleFunc("GET /v1/tasks/{task_id}/result", s.handleTaskResult)
	mux.HandleFunc("GET /v1/tasks/{task_id}/transcript", s.handleTaskTranscript)
	mux.HandleFunc("GET /v1/tasks/{task_id}/artifacts/{kind}", s.handleTaskArtifact)
	mux.HandleFunc("POST /v1/tasks/{task_id}/cancel", s.handleCancel)
	mux.HandleFunc("POST /v1/peer-smoke", s.handlePeerSmoke)
	mux.HandleFunc("POST /v1/profile-leases", s.handleIssueProfileLease)
	mux.HandleFunc("GET /v1/profile-leases", s.handleListProfileLeases)
	mux.HandleFunc("DELETE /v1/profile-leases/{lease_id}", s.handleRevokeProfileLease)
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
	// ReadTimeout bounds the full request read (headers + body) so a peer cannot
	// hold a connection open by dribbling the body (Slowloris). IdleTimeout reaps
	// idle keep-alive connections. WriteTimeout is intentionally left unset: the
	// long-poll endpoints (wait/timeout_s) legitimately hold the response open,
	// and each handler already bounds its own work via request context deadlines.
	server := &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
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
	stableID := ""
	if self, err := s.Identity.Self(r.Context()); err == nil {
		stableID = self.StableID
	}
	writeJSON(w, http.StatusOK, schema.HelloResponse{
		NodeID:         s.Config.Node.ID,
		NodeName:       s.Config.Node.Name,
		StableID:       stableID,
		CmeshVersion:   version.Version,
		CmeshCommit:    version.Commit,
		CmeshDate:      version.Date,
		SchemaVersions: []string{schema.Version},
		Runners:        runners,
		Capabilities:   []string{"tasks", "audit", "idempotency", "policy", "runner_manifests", "task_list", "transcripts", "git_bundle_artifacts", "long_poll_status", "peer_smoke", "profile_leases"},
		ServerTime:     time.Now().UTC(),
	})
}

func (s *Server) handleIssueProfileLease(w http.ResponseWriter, r *http.Request) {
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
	var req schema.ProfileLeaseRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, task.ErrorWorkerFailed, "invalid JSON: "+err.Error())
		return
	}
	lease, err := s.issueProfileLease(r.Context(), identity, req)
	if err != nil {
		writeError(w, http.StatusForbidden, task.ErrorProfileDenied, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, lease)
}

func (s *Server) handleListProfileLeases(w http.ResponseWriter, r *http.Request) {
	if err := requireSchema(r); err != nil {
		writeError(w, http.StatusBadRequest, task.ErrorVersionMismatch, err.Error())
		return
	}
	identity, ok := s.authenticate(w, r)
	if !ok {
		return
	}
	if s.Leases == nil {
		writeError(w, http.StatusInternalServerError, task.ErrorWorkerFailed, "profile lease store is not configured")
		return
	}
	leases, err := s.Leases.ListForPeer(policy.LeaseValidation{
		PeerName:     identity.NodeName,
		PeerNodeID:   identity.NodeID,
		PeerStableID: identity.StableID,
	}, r.URL.Query().Get("include_expired") == "true", time.Now().UTC())
	if err != nil {
		writeError(w, http.StatusInternalServerError, task.ErrorWorkerFailed, err.Error())
		return
	}
	s.record(r.Context(), audit.Event{
		Type:           "profile.lease.listed",
		PeerIdentity:   firstNonEmpty(identity.NodeName, identity.NodeID, identity.StableID),
		PolicyDecision: "allowed",
		Source:         "mesh_rpc",
		Extra:          map[string]any{"include_expired": r.URL.Query().Get("include_expired") == "true", "count": len(leases)},
	})
	writeJSON(w, http.StatusOK, schema.ProfileLeaseListResponse{Leases: leases})
}

func (s *Server) handleRevokeProfileLease(w http.ResponseWriter, r *http.Request) {
	if err := requireSchema(r); err != nil {
		writeError(w, http.StatusBadRequest, task.ErrorVersionMismatch, err.Error())
		return
	}
	identity, ok := s.authenticate(w, r)
	if !ok {
		return
	}
	if s.Leases == nil {
		writeError(w, http.StatusInternalServerError, task.ErrorWorkerFailed, "profile lease store is not configured")
		return
	}
	lease, err := s.Leases.RevokeForPeer(policy.LeaseValidation{
		PeerName:     identity.NodeName,
		PeerNodeID:   identity.NodeID,
		PeerStableID: identity.StableID,
	}, r.PathValue("lease_id"), time.Now().UTC())
	if err != nil {
		s.record(r.Context(), audit.Event{
			Type:           "profile.lease.revoked",
			PeerIdentity:   firstNonEmpty(identity.NodeName, identity.NodeID, identity.StableID),
			PolicyDecision: "denied",
			Source:         "mesh_rpc",
			ErrorCode:      string(task.ErrorProfileDenied),
			ErrorDetail:    err.Error(),
			Extra:          map[string]any{"lease_id": r.PathValue("lease_id"), "caller_node_id": identity.NodeID, "caller_stable_id": identity.StableID},
		})
		writeError(w, http.StatusNotFound, task.ErrorProfileDenied, err.Error())
		return
	}
	s.record(r.Context(), audit.Event{
		Type:           "profile.lease.revoked",
		PeerIdentity:   firstNonEmpty(identity.NodeName, identity.NodeID, identity.StableID),
		Profile:        lease.Profile,
		PolicyDecision: "allowed",
		Source:         "mesh_rpc",
		Claims:         append([]string(nil), lease.Capabilities...),
		Extra:          map[string]any{"lease_id": lease.LeaseID, "caller_node_id": identity.NodeID, "caller_stable_id": identity.StableID},
	})
	writeJSON(w, http.StatusOK, lease)
}

func (s *Server) issueProfileLease(_ context.Context, identity tsid.Identity, req schema.ProfileLeaseRequest) (schema.ProfileLease, error) {
	if s.Leases == nil {
		return schema.ProfileLease{}, errors.New("profile lease store is not configured")
	}
	profileName := strings.TrimSpace(req.Profile)
	if profileName == "" {
		return schema.ProfileLease{}, errors.New("profile is required")
	}
	prof, ok := s.Config.Profiles[profileName]
	if !ok {
		return schema.ProfileLease{}, fmt.Errorf("profile %q is not configured", profileName)
	}
	if !prof.RequireLease {
		return schema.ProfileLease{}, fmt.Errorf("profile %q does not require a worker lease", profileName)
	}
	ttl := time.Duration(req.TTLSeconds) * time.Second
	if ttl <= 0 {
		return schema.ProfileLease{}, errors.New("ttl_s must be positive")
	}
	if prof.Writes != profiledefs.WritesDeny && strings.TrimSpace(req.CWD) == "" {
		return schema.ProfileLease{}, fmt.Errorf("profile %q requires cwd for lease scope", profileName)
	}
	normalizedCWD := strings.TrimSpace(req.CWD)
	cwdScope := strings.TrimSpace(req.CWDScope)
	if cwdScope == "" {
		cwdScope = "exact"
	}
	if cwdScope != "exact" && cwdScope != "recursive" {
		return schema.ProfileLease{}, fmt.Errorf("cwd_scope must be exact or recursive")
	}
	if normalizedCWD != "" {
		normalized, err := policy.NormalizeCWD(req.CWD)
		if err != nil {
			return schema.ProfileLease{}, err
		}
		normalizedCWD = normalized
	}
	if err := s.validateLeaseRequest(identity, profileName, normalizedCWD); err != nil {
		return schema.ProfileLease{}, err
	}
	lease, err := s.Leases.Issue(policy.LeaseValidation{
		PeerName:     identity.NodeName,
		PeerNodeID:   identity.NodeID,
		PeerStableID: identity.StableID,
		Profile:      profileName,
		CWD:          normalizedCWD,
		CWDScope:     cwdScope,
		Capabilities: profiledefs.NormalizeCapabilities(prof.Capabilities, prof.Claims),
	}, ttl, time.Now().UTC())
	if err != nil {
		return schema.ProfileLease{}, err
	}
	s.record(context.Background(), audit.Event{
		Type:           "profile.lease.issued",
		PeerIdentity:   firstNonEmpty(identity.NodeName, identity.NodeID, identity.StableID),
		OriginSession:  strings.TrimSpace(req.OriginSessionID),
		Profile:        profileName,
		PolicyDecision: "allowed",
		Source:         "mesh_rpc",
		Claims:         append([]string(nil), lease.Capabilities...),
	})
	return lease, nil
}

func (s *Server) validateLeaseRequest(identity tsid.Identity, profileName, cwd string) error {
	cfgCopy := *s.Config
	cfgCopy.Profiles = make(map[string]config.ProfileConfig, len(s.Config.Profiles))
	for name, prof := range s.Config.Profiles {
		cfgCopy.Profiles[name] = prof
	}
	prof := cfgCopy.Profiles[profileName]
	prof.RequireLease = false
	cfgCopy.Profiles[profileName] = prof
	decision := policy.New(&cfgCopy).CheckInbound(policy.TaskIntent{
		PeerName:     identity.NodeName,
		PeerNodeID:   identity.NodeID,
		PeerStableID: identity.StableID,
		PeerTags:     append([]string(nil), identity.Tags...),
		CWD:          cwd,
		Profile:      profileName,
		OriginNodeID: identity.NodeID,
	})
	if !decision.Allowed {
		return errors.New(decision.Detail)
	}
	return nil
}

func (s *Server) handlePeerSmoke(w http.ResponseWriter, r *http.Request) {
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
	var req schema.PeerSmokeRequest
	if len(strings.TrimSpace(string(body))) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			writeError(w, http.StatusBadRequest, task.ErrorWorkerFailed, "invalid JSON: "+err.Error())
			return
		}
	}
	resp, err := s.runPeerSmokeToCaller(r.Context(), identity, req)
	if err != nil {
		status := http.StatusInternalServerError
		code := task.ErrorWorkerFailed
		if rpcErr, ok := err.(*Error); ok {
			status = rpcErr.StatusCode
			code = rpcErr.Code
		} else if resp.Error != nil {
			code = resp.Error.Code
			switch code {
			case task.ErrorPeerUnauthorized:
				status = http.StatusForbidden
			case task.ErrorPeerUnreachable:
				status = http.StatusBadGateway
			}
		}
		if resp.Error == nil {
			resp.Error = &schema.Error{Code: code, Detail: err.Error()}
		}
		writeJSON(w, status, resp)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) runPeerSmokeToCaller(ctx context.Context, identity tsid.Identity, req schema.PeerSmokeRequest) (schema.PeerSmokeResponse, error) {
	peerName, peer, ok := s.peerForIdentity(identity)
	if !ok {
		err := fmt.Errorf("authenticated caller is not a configured peer")
		return schema.PeerSmokeResponse{Error: &schema.Error{Code: task.ErrorPeerUnauthorized, Detail: err.Error()}}, err
	}
	// Leave cwd empty when the caller did not specify one: this smoke delegates the
	// task BACK to the caller (the caller is the worker here), so the caller's own
	// receive path self-selects a cwd its policy accepts. cwd is not checked by
	// CheckOutbound, only by the worker's CheckInbound, so an empty cwd is correct
	// to forward. A hardcoded "~" — or this node's allowed_cwds — would be a path
	// that may not exist on or be allowed by the caller.
	cwd := strings.TrimSpace(req.CWD)
	profile := strings.TrimSpace(req.Profile)
	if profile == "" {
		profile = "readonly"
	}
	timeoutSeconds := req.TimeoutSeconds
	if timeoutSeconds <= 0 {
		timeoutSeconds = 60
	}
	checks := map[string]bool{}
	resp := schema.PeerSmokeResponse{
		Peer:     peerName,
		NodeID:   firstNonEmpty(peer.NodeID, identity.NodeID),
		NodeName: firstNonEmpty(peer.Name, identity.NodeName),
		Checks:   checks,
	}
	port := firstNonZero(peer.Port, config.DefaultMeshPort)
	client := NewClient("http://" + net.JoinHostPort(peer.Address, fmt.Sprintf("%d", port)))
	hello, err := client.Hello(ctx)
	if err != nil {
		resp.Error = &schema.Error{Code: task.ErrorPeerUnreachable, Detail: err.Error()}
		return resp, err
	}
	resp.NodeID = firstNonEmpty(hello.NodeID, resp.NodeID)
	resp.NodeName = firstNonEmpty(hello.NodeName, resp.NodeName)
	checks["peer_reachable"] = true
	chain := []string{s.Config.Node.ID}
	decision := s.Policy.CheckOutbound(policy.TaskIntent{
		PeerName:     firstNonEmpty(peer.Name, peerName),
		PeerNodeID:   peer.NodeID,
		CWD:          cwd,
		Profile:      profile,
		Chain:        chain,
		OriginNodeID: s.Config.Node.ID,
		TaskKind:     taskKindForSmoke(profile),
		Capability:   capabilityForSmoke(profile),
	})
	checks["policy_allowed"] = decision.Allowed
	if !decision.Allowed {
		err := fmt.Errorf("%s: %s", decision.Code, decision.Detail)
		resp.Error = &schema.Error{Code: task.ErrorCode(decision.Code), Detail: decision.Detail}
		return resp, err
	}
	taskReq := smokeCreateTaskRequest(s.Config, cwd, decision.EffectiveProfile, timeoutSeconds)
	started := time.Now()
	created, err := client.CreateTask(ctx, taskReq)
	if err != nil {
		resp.Error = &schema.Error{Code: task.ErrorPeerUnreachable, Detail: err.Error()}
		return resp, err
	}
	resp.TaskID = created.TaskID
	resp.State = created.State
	resp.ElapsedSeconds = int64(time.Since(started).Round(time.Second).Seconds())
	checks["task_created"] = created.TaskID != ""
	if created.State != task.StateDone {
		if created.Error != nil {
			resp.Error = created.Error
			return resp, fmt.Errorf("%s: %s", created.Error.Code, created.Error.Detail)
		}
		err := fmt.Errorf("smoke task finished with state %s", created.State)
		resp.Error = &schema.Error{Code: task.ErrorWorkerFailed, Detail: err.Error()}
		return resp, err
	}
	result, err := client.TaskResult(ctx, created.TaskID)
	if err != nil {
		resp.Error = &schema.Error{Code: task.ErrorWorkerFailed, Detail: err.Error()}
		return resp, err
	}
	resp.Result = result.Result
	checks["result_envelope_parsed"] = result.Result != nil && task.State(result.Result.Status) == task.StateDone
	checks["runner_invoked"] = result.Result != nil && result.Result.CommandOutput.Command != ""
	verified := rpcSmokeResultMatches(result.Result, decision.EffectiveProfile)
	checks["prompt_received"] = verified
	checks["smoke_response_verified"] = verified
	if !verified {
		err := fmt.Errorf("smoke task did not return %q", rpcSmokeExpectedResponse)
		resp.Error = &schema.Error{Code: task.ErrorWorkerFailed, Detail: err.Error()}
		return resp, err
	}
	return resp, nil
}

func (s *Server) peerForIdentity(identity tsid.Identity) (string, config.PeerConfig, bool) {
	for name, peer := range s.Config.Peers {
		if peer.NodeID != "" && matchesIdentityClaim(peer.NodeID, identity) {
			return name, peer, true
		}
		if peer.Name != "" && matchesIdentityClaim(peer.Name, identity) {
			return name, peer, true
		}
		if name != "" && matchesIdentityClaim(name, identity) {
			return name, peer, true
		}
	}
	return "", config.PeerConfig{}, false
}

const rpcSmokeExpectedResponse = "cmesh smoke ok"

func smokeCreateTaskRequest(cfg *config.Config, cwd, profile string, timeoutSeconds int) schema.CreateTaskRequest {
	req := schema.CreateTaskRequest{
		TaskID:          task.DeterministicID(cfg.Node.ID, "cmesh-rpc-smoke", cwd+"\x00"+profile+"\x00"+strconv.Itoa(timeoutSeconds)+"\x00"+time.Now().UTC().Format(time.RFC3339Nano)),
		OriginNodeID:    cfg.Node.ID,
		OriginSessionID: "cmesh-rpc-smoke",
		Prompt:          "Reply with exactly: " + rpcSmokeExpectedResponse,
		CWD:             cwd,
		Profile:         profile,
		TimeoutSeconds:  timeoutSeconds,
		Wait:            true,
		Provenance: schema.Provenance{
			OriginNodeID:    cfg.Node.ID,
			OriginSessionID: "cmesh-rpc-smoke",
			DelegationChain: []string{cfg.Node.ID},
			HopCount:        0,
		},
	}
	if profile == profiledefs.Query {
		req.TaskKind = "query"
		req.Query = &schema.QueryRequest{Kind: profiledefs.CapSystemInfo, CWD: cwd}
		req.Prompt = ""
	}
	return req
}

func taskKindForSmoke(profile string) string {
	if profile == profiledefs.Query {
		return "query"
	}
	return ""
}

func capabilityForSmoke(profile string) string {
	if profile == profiledefs.Query {
		return profiledefs.CapSystemInfo
	}
	return ""
}

// isSmokeOriginSession reports whether any of the candidate origin-session IDs
// names a cmesh smoke task. All smoke entrypoints (wizard, rpc, mcp) tag their
// origin session with a "-smoke" suffix; this is the worker-side signal to
// self-select a cwd when none was supplied. Matching is suffix-based so it does
// not fire on a user session that merely contains "smoke".
func isSmokeOriginSession(candidates ...string) bool {
	for _, c := range candidates {
		c = strings.TrimSpace(c)
		if c == "cmesh-rpc-smoke" || c == "cmesh-wizard-smoke" || c == "mcp-smoke" || strings.HasSuffix(c, "-smoke") {
			return true
		}
	}
	return false
}

func rpcSmokeResultMatches(result *task.ResultEnvelope, profile string) bool {
	if result == nil {
		return false
	}
	if result.CommandOutput.Command == "" || result.CommandOutput.ExitCode != 0 {
		return false
	}
	if profile == profiledefs.Query {
		return result.CommandOutput.Command == "probe:"+profiledefs.CapSystemInfo && strings.Contains(result.CommandOutput.Stdout, `"kind":"system_info"`)
	}
	return strings.Contains(result.CommandOutput.Stdout, rpcSmokeExpectedResponse) || strings.Contains(result.CommandOutput.Stderr, rpcSmokeExpectedResponse)
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
	} else if !task.ValidID(req.TaskID) {
		// A caller-supplied task_id flows into filepath.Join for logs, artifacts,
		// worktrees, and task-token files. Reject anything that is not a
		// system-minted id before it can become a path-traversal primitive.
		writeError(w, http.StatusBadRequest, task.ErrorWorkerFailed, "malformed task_id")
		return
	}
	if req.FollowupTaskID != "" && !task.ValidID(req.FollowupTaskID) {
		writeError(w, http.StatusBadRequest, task.ErrorWorkerFailed, "malformed followup_task_id")
		return
	}
	if req.FollowupBaseCommit != "" && !isGitObjectish(req.FollowupBaseCommit) {
		// followup_base_commit is passed to git as a revision argument. Constrain
		// it to a hex object id so it cannot be interpreted as a git option
		// (e.g. a leading-dash value) or otherwise steer the git invocation.
		writeError(w, http.StatusBadRequest, task.ErrorWorkerFailed, "malformed followup_base_commit")
		return
	}
	if err := validateOriginClaims(&req, identity); err != nil {
		s.record(r.Context(), audit.Event{
			Type:           "task.dispatch.denied",
			TaskID:         req.TaskID,
			PeerIdentity:   firstNonEmpty(identity.NodeName, identity.NodeID, identity.StableID),
			OriginSession:  firstNonEmpty(req.Provenance.OriginSessionID, req.OriginSessionID),
			Chain:          append([]string(nil), req.Provenance.DelegationChain...),
			Runner:         req.Runner,
			Profile:        req.Profile,
			PolicyDecision: "provenance_mismatch",
			Source:         "mesh_rpc",
			ErrorCode:      string(task.ErrorPeerUnauthorized),
			ErrorDetail:    err.Error(),
		})
		writeError(w, http.StatusForbidden, task.ErrorPeerUnauthorized, err.Error())
		return
	}
	req.Provenance.WorkerNodeID = s.Config.Node.ID
	req.Provenance.PromptHash = task.PromptHash(req.Prompt)
	if req.TaskKind == "query" {
		if req.Query == nil || strings.TrimSpace(req.Query.Kind) == "" {
			writeError(w, http.StatusBadRequest, task.ErrorWorkerFailed, "query.kind is required")
			return
		}
		if req.Profile == "" {
			req.Profile = profiledefs.Query
		}
		if req.Query.CWD != "" {
			req.CWD = req.Query.CWD
		}
		if strings.TrimSpace(req.Prompt) == "" {
			req.Prompt = "Run cmesh deterministic query: " + req.Query.Kind
			req.Provenance.PromptHash = task.PromptHash(req.Prompt)
		}
	}

	// A smoke, typed query, or read-only inspection task with no cwd self-selects
	// one this worker's policy accepts. The delegator often cannot know a cwd
	// that exists on this worker (especially cross-OS), so the worker — the node
	// that runs the task and enforces allowed_cwds — picks it.
	if strings.TrimSpace(req.CWD) == "" && shouldSelfSelectCWD(s.Config, req) {
		if cwd := policy.SmokeCWD(s.Config); cwd != "" {
			req.CWD = cwd
		}
	}

	intent := policy.TaskIntent{
		PeerName:         identity.NodeName,
		PeerNodeID:       identity.NodeID,
		PeerStableID:     identity.StableID,
		PeerTags:         append([]string(nil), identity.Tags...),
		CWD:              req.CWD,
		Runner:           req.Runner,
		Profile:          req.Profile,
		HopCount:         req.Provenance.HopCount,
		Chain:            req.Provenance.DelegationChain,
		OriginNodeID:     req.Provenance.OriginNodeID,
		AllowDirty:       req.AllowDirty,
		TaskKind:         req.TaskKind,
		Capability:       queryCapability(req),
		ProfileContextID: req.ProfileContextID,
	}
	decision := s.Policy.CheckInbound(intent)
	effectiveCWD := firstNonEmpty(decision.NormalizedCWD, req.CWD)
	rec := task.Record{
		ID:              req.TaskID,
		OriginNodeID:    firstNonEmpty(req.Provenance.OriginNodeID, req.OriginNodeID, identity.NodeID),
		OriginSessionID: firstNonEmpty(req.Provenance.OriginSessionID, req.OriginSessionID),
		WorkerNodeID:    s.Config.Node.ID,
		State:           task.StateQueued,
		CWD:             effectiveCWD,
		// Prefer the runner of the profile policy actually granted. A profile
		// ceiling can remap the effective profile, so resolving from req.Profile
		// would run a runner that belongs to a profile the worker did not enforce.
		Runner:           firstNonEmpty(req.Runner, decision.EffectiveRunner, runnerFromProfile(s.Config, req.Profile)),
		ProfileRequested: firstNonEmpty(req.Profile, defaultProfileForTask(req.TaskKind)),
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
		writeJSON(w, http.StatusOK, schema.CreateTaskResponse{
			TaskID:           outcome.Task.ID,
			State:            outcome.Task.State,
			Replayed:         true,
			RequestedProfile: outcome.Task.ProfileRequested,
			EffectiveProfile: outcome.Task.ProfileEffective,
			ProfileSource:    req.ProfileSource,
			ProfileContextID: req.ProfileContextID,
		})
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
			TaskID:           outcome.Task.ID,
			State:            task.StateDenied,
			RequestedProfile: rec.ProfileRequested,
			EffectiveProfile: decision.EffectiveProfile,
			ProfileSource:    req.ProfileSource,
			ProfileContextID: req.ProfileContextID,
			Error:            &schema.Error{Code: task.ErrorCode(decision.Code), Detail: decision.Detail},
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
	runCtx, cancel := context.WithCancel(context.Background())
	s.registerRunningTask(outcome.Task.ID, cancel)
	go func() {
		defer cancel()
		defer s.unregisterRunningTask(outcome.Task.ID)
		// net/http recovers panics inside handlers, but this task runs in a
		// detached goroutine. Without recovery, a panic anywhere in runTask
		// (runner, git prep, result marshaling) would crash the whole daemon and
		// kill every in-flight task. Recover, fail this one task, and stay up.
		defer func() {
			if rec := recover(); rec != nil {
				s.failTask(context.Background(), outcome.Task, task.ErrorWorkerFailed, fmt.Sprintf("task panicked: %v", rec))
			}
		}()
		s.runTask(runCtx, outcome.Task, req)
	}()
	if req.Wait {
		s.waitForTask(w, r, outcome.Task.ID, time.Duration(req.TimeoutSeconds)*time.Second, req)
		return
	}
	writeJSON(w, http.StatusAccepted, schema.CreateTaskResponse{
		TaskID:           outcome.Task.ID,
		State:            task.StateQueued,
		RequestedProfile: rec.ProfileRequested,
		EffectiveProfile: decision.EffectiveProfile,
		ProfileSource:    req.ProfileSource,
		ProfileContextID: req.ProfileContextID,
	})
}

func (s *Server) runTask(ctx context.Context, rec task.Record, req schema.CreateTaskRequest) {
	timeout := s.effectiveRuntimeLimit(rec.ProfileEffective, req.TimeoutSeconds)
	if err := ctx.Err(); err != nil {
		s.cancelTaskFromWorker(context.Background(), rec, runner.Result{}, err.Error())
		return
	}
	run, ok := s.Runners.Get(rec.Runner)
	if !ok {
		s.failTask(ctx, rec, task.ErrorRunnerUnavailable, "runner is not registered")
		return
	}
	if err := run.Check(rec.ProfileEffective); err != nil {
		s.failTask(ctx, rec, task.ErrorCode(runner.CodeOf(err)), err.Error())
		return
	}
	if err := ctx.Err(); err != nil {
		s.cancelTaskFromWorker(context.Background(), rec, runner.Result{}, err.Error())
		return
	}
	workDirOverride, err := s.followupWorkDir(ctx, req.FollowupTaskID)
	if err != nil {
		s.failTask(ctx, rec, task.ErrorWorkerFailed, err.Error())
		return
	}
	gitPrep, err := prepareGit(ctx, rec, s.Config.Node.Name, s.Config.Node.DataDir, decisionClaims(s.Config, rec.ProfileEffective), gitPreparationOptions{
		BaseCommit:      req.FollowupBaseCommit,
		WorkDirOverride: workDirOverride,
	})
	if err != nil {
		if ctx.Err() != nil {
			s.cancelTaskFromWorker(ctx, rec, runner.Result{}, ctx.Err().Error())
			return
		}
		s.failTask(ctx, rec, gitErrorCode(err), err.Error())
		return
	}
	logDir := filepath.Join(s.Config.Node.DataDir, "logs", rec.ID)
	metadata := runner.TaskMetadata{
		TaskID:          rec.ID,
		OriginNodeID:    rec.OriginNodeID,
		OriginSessionID: rec.OriginSessionID,
		WorkerNodeID:    rec.WorkerNodeID,
		Runner:          rec.Runner,
		Profile:         rec.ProfileEffective,
		DelegationChain: append([]string(nil), rec.DelegationChain...),
		HopCount:        rec.HopCount,
		ResumeSessionID: req.ResumeSessionID,
	}
	metadata.MCPURL = taskMCPURL(s.Config.Node.MCPPort)
	token, err := newTaskToken()
	if err != nil {
		s.failTask(ctx, rec, task.ErrorWorkerFailed, err.Error())
		return
	}
	metadata.TaskToken = token
	contextPath, err := writeTaskContextFile(s.Config.Node.DataDir, metadata)
	if err != nil {
		s.failTask(ctx, rec, task.ErrorWorkerFailed, err.Error())
		return
	}
	metadata.ContextFile = contextPath
	mcpConfigPath, err := writeTaskMCPConfigFile(s.Config.Node.DataDir, s.Config.Node.MCPPort, metadata)
	if err != nil {
		s.failTask(ctx, rec, task.ErrorWorkerFailed, err.Error())
		return
	}
	metadata.MCPConfigFile = mcpConfigPath
	acquireCtx := ctx
	cancelAcquire := func() {}
	if req.TimeoutSeconds > 0 {
		acquireCtx, cancelAcquire = context.WithTimeout(ctx, time.Duration(req.TimeoutSeconds)*time.Second)
	}
	defer cancelAcquire()
	release, err := s.acquireRunner(acquireCtx, rec.Runner)
	if err != nil {
		s.cancelTaskFromWorker(context.Background(), rec, runner.Result{}, err.Error())
		return
	}
	defer release()
	if err := ctx.Err(); err != nil {
		s.cancelTaskFromWorker(context.Background(), rec, runner.Result{}, err.Error())
		return
	}
	updated, err := s.Store.UpdateTaskState(context.Background(), rec.ID, store.StateUpdate{State: task.StateRunning})
	if err != nil {
		// The task token and context/MCP-config files were already written above.
		// Don't leak them: drive a terminal state and remove the secrets even
		// though this state write failed (failTask also calls removeTaskSecrets).
		s.failTask(context.Background(), rec, task.ErrorWorkerFailed, err.Error())
		return
	}
	rec = updated
	s.record(context.Background(), audit.Event{Type: "task.started", TaskID: rec.ID, OriginSession: rec.OriginSessionID, Chain: rec.DelegationChain, Runner: rec.Runner, Profile: rec.ProfileEffective, Source: "worker"})
	if err := ctx.Err(); err != nil {
		s.cancelTaskFromWorker(context.Background(), rec, runner.Result{}, err.Error())
		return
	}
	result, err := run.Run(runner.RunRequest{
		Context:     ctx,
		TaskID:      rec.ID,
		Prompt:      promptWithTaskContext(req.Prompt, metadata),
		CWD:         firstNonEmpty(gitPrep.WorkDir, rec.CWD),
		Profile:     rec.ProfileEffective,
		Env:         taskEnv(metadata),
		Timeout:     timeout,
		LogDir:      logDir,
		Metadata:    metadata,
		OutputLimit: runner.DefaultOutputLimit,
		QueryKind:   queryCapability(req),
		QueryParams: queryParams(req),
	})
	if ctx.Err() != nil && err == nil {
		err = runner.E(runner.ErrCancelled, "run command", ctx.Err())
	}
	if gitPrep.Available && runner.CodeOf(err) != runner.ErrCancelled {
		gitPush := false
		if profile, ok := s.Config.Profiles[rec.ProfileEffective]; ok {
			gitPush = profile.GitPush
		}
		head, diffstat, files, bundlePath, pushed, commitErr := commitGit(ctx, rec, gitPrep, s.Config.Node.Name, s.Config.Node.DataDir, gitPush)
		if commitErr != nil && err == nil {
			err = commitErr
		}
		result.Branch = gitPrep.Branch
		result.BaseCommit = gitPrep.Base
		result.HeadCommit = head
		result.Pushed = pushed
		result.Diffstat = diffstat
		result.FilesChanged = files
		if bundlePath != "" {
			if result.RunnerExtra == nil {
				result.RunnerExtra = map[string]any{}
			}
			result.RunnerExtra["git_bundle_path"] = bundlePath
			result.RecommendedOriginChecks = append(result.RecommendedOriginChecks,
				"cmesh task_artifact "+rec.ID+" git-bundle",
				"git fetch <bundle-path> "+gitPrep.Branch,
				"git diff --stat "+gitPrep.Base+".."+head,
			)
		}
		if gitPrep.WorkDir != "" {
			if result.RunnerExtra == nil {
				result.RunnerExtra = map[string]any{}
			}
			result.RunnerExtra["worktree_path"] = filepath.ToSlash(gitPrep.WorkDir)
		}
	}
	if err != nil {
		code := task.ErrorWorkerFailed
		if rc := runner.CodeOf(err); rc != "" {
			code = task.ErrorCode(rc)
		}
		if code == task.ErrorCancelled {
			s.cancelTaskFromWorker(context.Background(), rec, result, err.Error())
			return
		}
		result.Status = string(task.StateFailed)
		ref := result.TranscriptRef
		_, _ = s.Store.UpdateTaskState(context.Background(), rec.ID, store.StateUpdate{State: task.StateFailed, TerminalErrorCode: code, Result: resultAsTask(result), ResultRef: ref})
		_ = removeTaskSecrets(s.Config.Node.DataDir, rec.ID)
		s.record(context.Background(), audit.Event{Type: "task.failed", TaskID: rec.ID, OriginSession: rec.OriginSessionID, Chain: rec.DelegationChain, Runner: rec.Runner, Profile: rec.ProfileEffective, Source: "worker", ErrorCode: string(code), ErrorDetail: err.Error()})
		return
	}
	result.Status = string(task.StateDone)
	ref := result.TranscriptRef
	_, _ = s.Store.UpdateTaskState(context.Background(), rec.ID, store.StateUpdate{State: task.StateDone, Result: resultAsTask(result), ResultRef: ref})
	_ = removeTaskSecrets(s.Config.Node.DataDir, rec.ID)
	s.record(context.Background(), audit.Event{Type: "task.completed", TaskID: rec.ID, OriginSession: rec.OriginSessionID, Chain: rec.DelegationChain, Runner: rec.Runner, Profile: rec.ProfileEffective, Source: "worker"})
}

func shouldSelfSelectCWD(cfg *config.Config, req schema.CreateTaskRequest) bool {
	if isSmokeOriginSession(req.Provenance.OriginSessionID, req.OriginSessionID) || req.TaskKind == "query" {
		return true
	}
	if req.TaskKind != "inspect" {
		return false
	}
	profileName := profiledefs.CanonicalProfile(req.Profile)
	if profileName != profiledefs.ReadonlyAgent {
		return false
	}
	prof, ok := cfg.Profiles[req.Profile]
	if !ok {
		prof, ok = cfg.Profiles[profileName]
	}
	if !ok {
		return false
	}
	caps := profiledefs.NormalizeCapabilities(prof.Capabilities, prof.Claims)
	return prof.Writes == profiledefs.WritesDeny && contains(caps, profiledefs.CapReadFiles)
}

func (s *Server) handleListTasks(w http.ResponseWriter, r *http.Request) {
	identity, ok := s.authenticate(w, r)
	if !ok {
		return
	}
	const maxListLimit = 200
	limit := 20
	if value := r.URL.Query().Get("limit"); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil && parsed > 0 {
			limit = parsed
		}
	}
	if limit > maxListLimit {
		// Cap caller-supplied limits so a peer cannot force an oversized query/alloc.
		limit = maxListLimit
	}
	tasks, err := s.Store.RecentTasks(r.Context(), limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, task.ErrorWorkerFailed, err.Error())
		return
	}
	visible := tasks[:0]
	for _, rec := range tasks {
		if taskAccessibleTo(rec, identity) {
			visible = append(visible, rec)
		}
	}
	writeJSON(w, http.StatusOK, schema.TaskListResponse{Tasks: visible})
}

func (s *Server) handleTaskStatus(w http.ResponseWriter, r *http.Request) {
	identity, ok := s.authenticate(w, r)
	if !ok {
		return
	}
	if _, ok := s.authorizeTaskAccess(w, r, identity, r.PathValue("task_id")); !ok {
		return
	}
	if r.URL.Query().Get("wait") == "true" {
		timeout := 30 * time.Second
		if value := r.URL.Query().Get("timeout_s"); value != "" {
			if parsed, err := strconv.Atoi(value); err == nil && parsed > 0 {
				timeout = time.Duration(parsed) * time.Second
			}
		}
		s.waitForTaskStatus(w, r, r.PathValue("task_id"), timeout)
		return
	}
	rec, _ := s.Store.GetTask(r.Context(), r.PathValue("task_id"))
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

func (s *Server) handleTaskTranscript(w http.ResponseWriter, r *http.Request) {
	identity, ok := s.authenticate(w, r)
	if !ok {
		return
	}
	taskID := r.PathValue("task_id")
	if _, ok := s.authorizeTaskAccess(w, r, identity, taskID); !ok {
		return
	}
	logDir := filepath.Join(s.Config.Node.DataDir, "logs", taskID)
	stdout, _ := readLogLimit(filepath.Join(logDir, taskID+".stdout.log"), 256*1024)
	stderr, _ := readLogLimit(filepath.Join(logDir, taskID+".stderr.log"), 256*1024)
	if stdout == "" && stderr == "" {
		matches, _ := filepath.Glob(filepath.Join(logDir, "*.log"))
		for _, path := range matches {
			body, _ := readLogLimit(path, 256*1024)
			if strings.HasSuffix(path, ".stderr.log") {
				stderr += body
			} else {
				stdout += body
			}
		}
	}
	if stdout == "" && stderr == "" {
		writeError(w, http.StatusNotFound, task.ErrorTranscriptUnavailable, "transcript logs are not available")
		return
	}
	writeJSON(w, http.StatusOK, schema.TaskTranscriptResponse{TaskID: taskID, Stdout: stdout, Stderr: stderr})
}

func (s *Server) handleTaskArtifact(w http.ResponseWriter, r *http.Request) {
	identity, ok := s.authenticate(w, r)
	if !ok {
		return
	}
	taskID := r.PathValue("task_id")
	if r.PathValue("kind") != "git-bundle" {
		writeError(w, http.StatusNotFound, task.ErrorWorkerFailed, "unsupported artifact kind")
		return
	}
	if _, ok := s.authorizeTaskAccess(w, r, identity, taskID); !ok {
		return
	}
	path := filepath.Join(s.Config.Node.DataDir, "artifacts", taskID+".bundle")
	f, err := os.Open(path)
	if err != nil {
		writeError(w, http.StatusNotFound, task.ErrorWorkerFailed, "artifact is not available")
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "application/vnd.git.bundle")
	w.Header().Set("Content-Disposition", `attachment; filename="`+taskID+`.bundle"`)
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, f)
}

func (s *Server) handleTaskResult(w http.ResponseWriter, r *http.Request) {
	identity, ok := s.authenticate(w, r)
	if !ok {
		return
	}
	rec, ok := s.authorizeTaskAccess(w, r, identity, r.PathValue("task_id"))
	if !ok {
		return
	}
	if rec.Result == nil {
		writeError(w, http.StatusNotFound, task.ErrorTranscriptUnavailable, "result is not available")
		return
	}
	writeJSON(w, http.StatusOK, schema.TaskResultResponse{TaskID: rec.ID, Result: rec.Result})
}

func (s *Server) handleCancel(w http.ResponseWriter, r *http.Request) {
	identity, ok := s.authenticate(w, r)
	if !ok {
		return
	}
	taskID := r.PathValue("task_id")
	if _, ok := s.authorizeTaskAccess(w, r, identity, taskID); !ok {
		return
	}
	rec, err := s.Store.UpdateTaskState(r.Context(), taskID, store.StateUpdate{State: task.StateCancelled, TerminalErrorCode: task.ErrorCancelled})
	if err != nil {
		writeError(w, http.StatusBadRequest, task.ErrorCancelled, err.Error())
		return
	}
	if cancel := s.cancelRunningTask(taskID); cancel != nil {
		cancel()
	}
	_ = removeTaskSecrets(s.Config.Node.DataDir, taskID)
	s.record(r.Context(), audit.Event{Type: "task.cancelled", TaskID: taskID, Source: "mesh_rpc", ErrorCode: string(task.ErrorCancelled)})
	writeJSON(w, http.StatusOK, schema.CancelTaskResponse{TaskID: taskID, State: rec.State})
}

func (s *Server) effectiveRuntimeLimit(profileName string, requestedSeconds int) time.Duration {
	limit := defaultRuntimeLimit
	if requestedSeconds > 0 {
		requested := time.Duration(requestedSeconds) * time.Second
		if requested < limit {
			limit = requested
		}
	}
	if profile, ok := s.Config.Profiles[profileName]; ok && profile.MaxRuntime.Duration > 0 {
		maxRuntime := profile.MaxRuntime.Duration
		if limit == 0 || maxRuntime < limit {
			limit = maxRuntime
		}
	}
	return limit
}

func (s *Server) followupWorkDir(ctx context.Context, followupTaskID string) (string, error) {
	if strings.TrimSpace(followupTaskID) == "" {
		return "", nil
	}
	prior, err := s.Store.GetTask(ctx, followupTaskID)
	if err != nil {
		return "", fmt.Errorf("load followup task %s: %w", followupTaskID, err)
	}
	if prior.Result == nil {
		return "", fmt.Errorf("followup task %s has no result envelope", followupTaskID)
	}
	if workDir := stringValue(prior.Result.RunnerExtra, "worktree_path"); workDir != "" {
		return filepath.FromSlash(workDir), nil
	}
	return filepath.Join(s.Config.Node.DataDir, "worktrees", followupTaskID), nil
}

func (s *Server) registerRunningTask(taskID string, cancel context.CancelFunc) {
	s.runningMu.Lock()
	defer s.runningMu.Unlock()
	if s.running == nil {
		s.running = map[string]context.CancelFunc{}
	}
	s.running[taskID] = cancel
}

func (s *Server) unregisterRunningTask(taskID string) {
	s.runningMu.Lock()
	defer s.runningMu.Unlock()
	delete(s.running, taskID)
}

func (s *Server) cancelRunningTask(taskID string) context.CancelFunc {
	s.runningMu.Lock()
	defer s.runningMu.Unlock()
	return s.running[taskID]
}

func (s *Server) acquireRunner(ctx context.Context, runnerName string) (func(), error) {
	limit := 0
	if cfg, ok := s.Config.Runners[runnerName]; ok {
		limit = cfg.Concurrency
	}
	if limit <= 0 {
		return func() {}, nil
	}
	s.limitsMu.Lock()
	if s.limits == nil {
		s.limits = map[string]chan struct{}{}
	}
	ch := s.limits[runnerName]
	if ch == nil || cap(ch) != limit {
		ch = make(chan struct{}, limit)
		s.limits[runnerName] = ch
	}
	s.limitsMu.Unlock()
	select {
	case ch <- struct{}{}:
		return func() { <-ch }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

type taskContextFile struct {
	TaskID          string   `json:"task_id"`
	OriginNodeID    string   `json:"origin_node_id"`
	OriginSessionID string   `json:"origin_session_id"`
	WorkerNodeID    string   `json:"worker_node_id"`
	Runner          string   `json:"runner"`
	Profile         string   `json:"profile"`
	DelegationChain []string `json:"delegation_chain"`
	HopCount        int      `json:"hop_count"`
	TaskTokenHash   string   `json:"task_token_hash"`
}

func writeTaskContextFile(dataDir string, metadata runner.TaskMetadata) (string, error) {
	dir := filepath.Join(dataDir, taskContextDirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create task context dir: %w", err)
	}
	tokenHash := task.SHA256Hex([]byte(metadata.TaskToken))
	path := filepath.Join(dir, metadata.TaskID+".json")
	body, err := json.MarshalIndent(taskContextFile{
		TaskID:          metadata.TaskID,
		OriginNodeID:    metadata.OriginNodeID,
		OriginSessionID: metadata.OriginSessionID,
		WorkerNodeID:    metadata.WorkerNodeID,
		Runner:          metadata.Runner,
		Profile:         metadata.Profile,
		DelegationChain: append([]string(nil), metadata.DelegationChain...),
		HopCount:        metadata.HopCount,
		TaskTokenHash:   tokenHash,
	}, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encode task context: %w", err)
	}
	if err := os.WriteFile(path, append(body, '\n'), 0o600); err != nil {
		return "", fmt.Errorf("write task context: %w", err)
	}
	tokenDir := filepath.Join(dataDir, taskTokenDirName)
	if err := os.MkdirAll(tokenDir, 0o700); err != nil {
		return "", fmt.Errorf("create task token dir: %w", err)
	}
	tokenPath := filepath.Join(tokenDir, tokenHash+".json")
	if err := os.WriteFile(tokenPath, append(body, '\n'), 0o600); err != nil {
		return "", fmt.Errorf("write task token context: %w", err)
	}
	return path, nil
}

func taskMCPURL(mcpPort int) string {
	return fmt.Sprintf("http://127.0.0.1:%d/mcp", mcpPort)
}

func writeTaskMCPConfigFile(dataDir string, mcpPort int, metadata runner.TaskMetadata) (string, error) {
	if strings.TrimSpace(metadata.TaskToken) == "" {
		return "", errors.New("task token is required")
	}
	dir := filepath.Join(dataDir, taskMCPConfigDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create task mcp config dir: %w", err)
	}
	path := filepath.Join(dir, metadata.TaskID+".json")
	body, err := json.MarshalIndent(map[string]any{
		"mcpServers": map[string]any{
			"cmesh": map[string]any{
				"type": "http",
				"url":  taskMCPURL(mcpPort),
				"headers": map[string]string{
					"Authorization": "Bearer " + metadata.TaskToken,
				},
			},
		},
	}, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encode task mcp config: %w", err)
	}
	if err := os.WriteFile(path, append(body, '\n'), 0o600); err != nil {
		return "", fmt.Errorf("write task mcp config: %w", err)
	}
	return path, nil
}

func removeTaskSecrets(dataDir, taskID string) error {
	contextPath := filepath.Join(dataDir, taskContextDirName, taskID+".json")
	if body, err := os.ReadFile(contextPath); err == nil {
		var raw struct {
			TaskTokenHash string `json:"task_token_hash"`
		}
		if json.Unmarshal(body, &raw) == nil && raw.TaskTokenHash != "" {
			if err := os.Remove(filepath.Join(dataDir, taskTokenDirName, raw.TaskTokenHash+".json")); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
	}
	if err := os.Remove(contextPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Remove(filepath.Join(dataDir, taskMCPConfigDir, taskID+".json")); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func newTaskToken() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate task token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

func (s *Server) waitForTask(w http.ResponseWriter, r *http.Request, taskID string, timeout time.Duration, req schema.CreateTaskRequest) {
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
			rec, err := s.Store.GetTask(r.Context(), taskID)
			if err != nil {
				writeError(w, http.StatusInternalServerError, task.ErrorWorkerFailed, err.Error())
				return
			}
			writeJSON(w, http.StatusAccepted, schema.CreateTaskResponse{
				TaskID:           taskID,
				State:            task.StateRunning,
				RequestedProfile: rec.ProfileRequested,
				EffectiveProfile: rec.ProfileEffective,
				ProfileSource:    req.ProfileSource,
				ProfileContextID: req.ProfileContextID,
			})
			return
		case <-ticker.C:
			rec, err := s.Store.GetTask(r.Context(), taskID)
			if err != nil {
				writeError(w, http.StatusInternalServerError, task.ErrorWorkerFailed, err.Error())
				return
			}
			if rec.State.Terminal() {
				writeJSON(w, http.StatusOK, schema.CreateTaskResponse{
					TaskID:           taskID,
					State:            rec.State,
					RequestedProfile: rec.ProfileRequested,
					EffectiveProfile: rec.ProfileEffective,
					ProfileSource:    req.ProfileSource,
					ProfileContextID: req.ProfileContextID,
				})
				return
			}
		case <-r.Context().Done():
			return
		}
	}
}

func (s *Server) waitForTaskStatus(w http.ResponseWriter, r *http.Request, taskID string, timeout time.Duration) {
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
			s.writeTaskStatus(w, r, taskID, http.StatusOK)
			return
		case <-ticker.C:
			rec, err := s.Store.GetTask(r.Context(), taskID)
			if err != nil {
				writeError(w, http.StatusInternalServerError, task.ErrorWorkerFailed, err.Error())
				return
			}
			if rec.State.Terminal() {
				s.writeTaskStatus(w, r, taskID, http.StatusOK)
				return
			}
		case <-r.Context().Done():
			return
		}
	}
}

func (s *Server) writeTaskStatus(w http.ResponseWriter, r *http.Request, taskID string, status int) {
	rec, err := s.Store.GetTask(r.Context(), taskID)
	if err != nil {
		writeError(w, http.StatusNotFound, task.ErrorWorkerFailed, err.Error())
		return
	}
	elapsed := time.Since(rec.CreatedAt)
	if !rec.CompletedAt.IsZero() {
		elapsed = rec.CompletedAt.Sub(rec.CreatedAt)
	}
	writeJSON(w, status, schema.TaskStatusResponse{
		TaskID:            rec.ID,
		State:             rec.State,
		CreatedAt:         rec.CreatedAt,
		UpdatedAt:         rec.UpdatedAt,
		ElapsedSeconds:    int64(elapsed / time.Second),
		LastActivity:      string(rec.State),
		TerminalErrorCode: rec.TerminalErrorCode,
	})
}

func readLogLimit(path string, limit int64) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	body, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return "", err
	}
	if int64(len(body)) > limit {
		body = append(body[:limit], []byte("\n[truncated]\n")...)
	}
	return string(body), nil
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

// gitObjectishRE matches a full or abbreviated git object id (hex, 7-64 chars).
// followup_base_commit is restricted to this so it can never be read by git as
// an option (e.g. "--upload-pack=...") rather than a revision.
var gitObjectishRE = regexp.MustCompile(`^[0-9a-fA-F]{7,64}$`)

func isGitObjectish(s string) bool {
	return gitObjectishRE.MatchString(s)
}

func validateOriginClaims(req *schema.CreateTaskRequest, identity tsid.Identity) error {
	origin := firstNonEmpty(req.Provenance.OriginNodeID, req.OriginNodeID)
	if req.OriginNodeID != "" && req.Provenance.OriginNodeID != "" && req.OriginNodeID != req.Provenance.OriginNodeID {
		return fmt.Errorf("origin_node_id does not match provenance origin_node_id")
	}
	if req.Provenance.OriginSessionID == "" {
		req.Provenance.OriginSessionID = req.OriginSessionID
	}
	if req.OriginSessionID == "" {
		req.OriginSessionID = req.Provenance.OriginSessionID
	}
	if len(req.Provenance.DelegationChain) == 0 {
		// origin becomes the task's ownership key, so it must equal a unique node
		// identity of the authenticated caller — never a shared tag (which would
		// let any tag-sharing peer claim ownership of this task later).
		if origin != "" && !matchesNodeIdentity(origin, identity) {
			return fmt.Errorf("origin_node_id %q does not match authenticated peer", origin)
		}
		origin = firstNonEmpty(origin, identity.NodeID, identity.NodeName, identity.StableID)
		req.Provenance.OriginNodeID = origin
		req.OriginNodeID = origin
		req.Provenance.DelegationChain = []string{origin}
		req.Provenance.HopCount = 0
		return nil
	}
	if err := validateDelegationChain(req.Provenance.DelegationChain); err != nil {
		return err
	}
	lastHop := req.Provenance.DelegationChain[len(req.Provenance.DelegationChain)-1]
	if !matchesNodeIdentity(lastHop, identity) {
		return fmt.Errorf("delegation_chain last hop %q does not match authenticated peer", lastHop)
	}
	if origin == "" {
		origin = req.Provenance.DelegationChain[0]
	}
	if req.Provenance.DelegationChain[0] != "" && origin != req.Provenance.DelegationChain[0] {
		return fmt.Errorf("origin_node_id %q does not match delegation_chain origin %q", origin, req.Provenance.DelegationChain[0])
	}
	req.Provenance.OriginNodeID = origin
	req.OriginNodeID = origin
	req.Provenance.HopCount = len(req.Provenance.DelegationChain) - 1
	return nil
}

func validateDelegationChain(chain []string) error {
	seen := map[string]struct{}{}
	for i, hop := range chain {
		hop = strings.TrimSpace(hop)
		if hop == "" {
			return fmt.Errorf("delegation_chain contains empty hop at index %d", i)
		}
		key := strings.ToLower(hop)
		if _, exists := seen[key]; exists {
			return fmt.Errorf("delegation_chain contains duplicate hop %q", hop)
		}
		seen[key] = struct{}{}
	}
	return nil
}

func (s *Server) authorizeTaskAccess(w http.ResponseWriter, r *http.Request, identity tsid.Identity, taskID string) (task.Record, bool) {
	rec, err := s.Store.GetTask(r.Context(), taskID)
	if err != nil {
		writeError(w, http.StatusNotFound, task.ErrorWorkerFailed, err.Error())
		return task.Record{}, false
	}
	if !taskAccessibleTo(rec, identity) {
		writeError(w, http.StatusForbidden, task.ErrorPeerUnauthorized, "authenticated peer does not own this task")
		return task.Record{}, false
	}
	return rec, true
}

func taskAccessibleTo(rec task.Record, identity tsid.Identity) bool {
	// Ownership must resolve to a unique node identity. Tags are shared across
	// machines, so tag matching here would let any peer carrying a shared tag
	// read or cancel another peer's task — use the tag-free matcher.
	if matchesNodeIdentity(rec.OriginNodeID, identity) {
		return true
	}
	if len(rec.DelegationChain) >= 2 {
		requester := rec.DelegationChain[len(rec.DelegationChain)-2]
		if matchesNodeIdentity(requester, identity) {
			return true
		}
	}
	return false
}

// matchesNodeIdentity matches a claimed identity value against the authenticated
// peer's UNIQUE identifiers only (node id / stable id / DNS name / DNS label).
// It deliberately excludes Tailscale tags, which are shared across machines and
// therefore unsafe for ownership or origin authorization.
func matchesNodeIdentity(value string, identity tsid.Identity) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	for _, candidate := range []string{identity.NodeID, identity.NodeName, identity.StableID, identity.DNSName} {
		if candidate != "" && strings.EqualFold(value, strings.TrimSpace(candidate)) {
			return true
		}
	}
	return false
}

// matchesIdentityClaim matches against unique identifiers AND shared tags. Use
// it only for matching against operator-configured, trusted values (e.g. a
// [peers.*] entry), never for authorization decisions keyed on request-supplied
// claims — for those use matchesNodeIdentity.
func matchesIdentityClaim(value string, identity tsid.Identity) bool {
	if matchesNodeIdentity(value, identity) {
		return true
	}
	value = strings.TrimSpace(value)
	for _, tag := range identity.Tags {
		if tag != "" && strings.EqualFold(value, strings.TrimSpace(tag)) {
			return true
		}
	}
	return false
}

type gitPreparation struct {
	Base      string
	Branch    string
	WorkDir   string
	Available bool
}

var errDirtyWorktree = errors.New("dirty worktree refused")

type gitPreparationOptions struct {
	BaseCommit      string
	WorkDirOverride string
}

func prepareGit(ctx context.Context, rec task.Record, nodeName, dataDir string, claims []string, opts gitPreparationOptions) (gitPreparation, error) {
	if !contains(claims, "edit_repo") {
		return gitPreparation{}, nil
	}
	repo, err := gitx.Open(rec.CWD)
	if errors.Is(err, gitx.ErrGitUnavailable) {
		return gitPreparation{}, nil
	}
	if err != nil {
		return gitPreparation{}, err
	}
	status, err := repo.Status(ctx)
	if err != nil {
		return gitPreparation{Available: true}, err
	}
	if !status.IsRepo {
		return gitPreparation{}, nil
	}
	if status.Dirty && !rec.AllowDirty {
		return gitPreparation{Available: true}, fmt.Errorf("%w: %s", errDirtyWorktree, status.Porcelain)
	}
	base := firstNonEmpty(opts.BaseCommit, status.HeadCommit)
	branch, err := gitx.DeterministicBranch(rec.ID, rec.ProfileRequested)
	if err != nil {
		return gitPreparation{Available: true}, err
	}
	workDir := firstNonEmpty(opts.WorkDirOverride, filepath.Join(dataDir, "worktrees", rec.ID))
	if opts.WorkDirOverride != "" {
		if _, err := os.Stat(workDir); errors.Is(err, os.ErrNotExist) {
			if err := os.MkdirAll(filepath.Dir(workDir), 0o755); err != nil {
				return gitPreparation{Available: true}, err
			}
			if err := repo.AddWorktree(ctx, workDir, branch, base); err != nil {
				return gitPreparation{Available: true}, err
			}
			return gitPreparation{
				Base:      base,
				Branch:    branch,
				WorkDir:   workDir,
				Available: true,
			}, nil
		} else if err != nil {
			return gitPreparation{Available: true}, err
		}
		workRepo, err := gitx.Open(workDir)
		if err != nil {
			return gitPreparation{Available: true}, err
		}
		workStatus, err := workRepo.Status(ctx)
		if err != nil {
			return gitPreparation{Available: true}, err
		}
		if workStatus.Dirty && !rec.AllowDirty {
			return gitPreparation{Available: true}, fmt.Errorf("%w: %s", errDirtyWorktree, workStatus.Porcelain)
		}
		if err := workRepo.CheckoutBranchAt(ctx, branch, base); err != nil {
			return gitPreparation{Available: true}, err
		}
		return gitPreparation{
			Base:      base,
			Branch:    branch,
			WorkDir:   workDir,
			Available: true,
		}, nil
	}
	_ = repo.RemoveWorktree(ctx, workDir)
	if err := os.RemoveAll(workDir); err != nil {
		return gitPreparation{Available: true}, err
	}
	if err := os.MkdirAll(filepath.Dir(workDir), 0o755); err != nil {
		return gitPreparation{Available: true}, err
	}
	if err := repo.AddWorktree(ctx, workDir, branch, base); err != nil {
		return gitPreparation{Available: true}, err
	}
	return gitPreparation{
		Base:      base,
		Branch:    branch,
		WorkDir:   workDir,
		Available: true,
	}, nil
}

func commitGit(ctx context.Context, rec task.Record, prep gitPreparation, nodeName, dataDir string, gitPush bool) (head, diffstat string, files []string, bundlePath string, pushed bool, err error) {
	repo, err := gitx.Open(firstNonEmpty(prep.WorkDir, rec.CWD))
	if err != nil {
		return "", "", nil, "", false, err
	}
	if err := repo.AddAll(ctx); err != nil {
		return "", "", nil, "", false, err
	}
	head, err = repo.Commit(ctx, gitx.CommitOptions{
		AuthorName:  "cmesh",
		AuthorEmail: "cmesh@" + nodeName,
		Subject:     "cmesh task " + rec.ID,
		TaskID:      rec.ID,
		OriginNode:  rec.OriginNodeID,
		WorkerNode:  rec.WorkerNodeID,
		Profile:     rec.ProfileEffective,
		AllowEmpty:  false,
	})
	if err != nil {
		if strings.Contains(err.Error(), "nothing to commit") || strings.Contains(err.Error(), "no changes added to commit") {
			head, err = repo.Head(ctx)
			if err != nil {
				return "", "", nil, "", false, err
			}
		} else {
			return "", "", nil, "", false, err
		}
	}
	diffstat, _ = repo.DiffStat(ctx, prep.Base, head)
	files, _ = repo.ChangedFiles(ctx, prep.Base, head)
	if len(files) > 0 {
		artifactDir := filepath.Join(dataDir, "artifacts")
		if err := os.MkdirAll(artifactDir, 0o755); err != nil {
			return "", "", nil, "", false, err
		}
		bundlePath = filepath.Join(artifactDir, rec.ID+".bundle")
		if err := repo.Bundle(ctx, bundlePath, prep.Branch, prep.Base); err != nil {
			return "", "", nil, "", false, err
		}
		if gitPush {
			if err := repo.Push(ctx, "origin", prep.Branch); err != nil {
				return "", "", nil, "", false, err
			}
			pushed = true
		}
	}
	return head, diffstat, files, bundlePath, pushed, nil
}

func gitErrorCode(err error) task.ErrorCode {
	if errors.Is(err, errDirtyWorktree) {
		return task.ErrorDirtyWorktree
	}
	return task.ErrorWorkerFailed
}

func resultAsTask(result runner.Result) *task.ResultEnvelope {
	status := task.State(result.Status)
	if status == "" {
		status = task.StateDone
	}
	out := task.ResultEnvelope{
		Status:       status,
		Branch:       result.Branch,
		BaseCommit:   result.BaseCommit,
		HeadCommit:   result.HeadCommit,
		Pushed:       result.Pushed,
		Diffstat:     result.Diffstat,
		FilesChanged: result.FilesChanged,
		Tests:        task.TestClaims{ClaimedPassed: result.Tests.ClaimedPassed, Commands: result.Tests.Commands, LogsRef: result.Tests.LogsRef},
		CommandOutput: task.CommandOutput{
			Command:   result.CommandOutput.Command,
			ExitCode:  result.CommandOutput.ExitCode,
			Stdout:    result.CommandOutput.Stdout,
			Stderr:    result.CommandOutput.Stderr,
			StdoutRef: result.CommandOutput.StdoutRef,
			StderrRef: result.CommandOutput.StderrRef,
			DurationS: result.CommandOutput.DurationS,
		},
		WorkerSummaryUntrusted:  result.WorkerSummaryUntrusted,
		Usage:                   task.Usage{Tokens: int64(result.Usage.Tokens), Cost: result.Usage.Cost, DurationS: result.Usage.DurationS},
		VerificationRequired:    result.VerificationRequired,
		RecommendedOriginChecks: result.RecommendedOriginChecks,
		RunnerExtra:             result.RunnerExtra,
	}
	if result.QueryResult != nil {
		out.QueryResult = &task.QueryResult{Kind: result.QueryResult.Kind, Data: result.QueryResult.Data}
	}
	return &out
}

func taskEnv(metadata runner.TaskMetadata) map[string]string {
	chain, _ := json.Marshal(metadata.DelegationChain)
	env := map[string]string{
		"CMESH_TASK_ID":               metadata.TaskID,
		"CMESH_ORIGIN_NODE_ID":        metadata.OriginNodeID,
		"CMESH_ORIGIN_SESSION_ID":     metadata.OriginSessionID,
		"CMESH_WORKER_NODE_ID":        metadata.WorkerNodeID,
		"CMESH_RUNNER":                metadata.Runner,
		"CMESH_PROFILE":               metadata.Profile,
		"CMESH_DELEGATION_CHAIN":      string(chain),
		"CMESH_DELEGATION_CHAIN_TEXT": strings.Join(metadata.DelegationChain, ","),
		"CMESH_HOP_COUNT":             strconv.Itoa(metadata.HopCount),
	}
	if metadata.ContextFile != "" {
		env["CMESH_TASK_CONTEXT_FILE"] = metadata.ContextFile
	}
	if metadata.MCPConfigFile != "" {
		env["CMESH_TASK_MCP_CONFIG_FILE"] = metadata.MCPConfigFile
	}
	if metadata.TaskToken != "" {
		env["CMESH_TASK_TOKEN"] = metadata.TaskToken
	}
	return env
}

func promptWithTaskContext(prompt string, metadata runner.TaskMetadata) string {
	var b strings.Builder
	b.WriteString("cmesh task context:\n")
	b.WriteString("- task_id: ")
	b.WriteString(metadata.TaskID)
	b.WriteString("\n- origin_node_id: ")
	b.WriteString(metadata.OriginNodeID)
	b.WriteString("\n- origin_session_id: ")
	b.WriteString(metadata.OriginSessionID)
	b.WriteString("\n- worker_node_id: ")
	b.WriteString(metadata.WorkerNodeID)
	b.WriteString("\n- profile: ")
	b.WriteString(metadata.Profile)
	b.WriteString("\n- hop_count: ")
	b.WriteString(strconv.Itoa(metadata.HopCount))
	b.WriteString("\n- delegation_chain: ")
	b.WriteString(strings.Join(metadata.DelegationChain, " -> "))
	if metadata.ContextFile != "" {
		b.WriteString("\n- task_context_file: ")
		b.WriteString(metadata.ContextFile)
	}
	if metadata.MCPConfigFile != "" {
		b.WriteString("\n- task_mcp_config_file: ")
		b.WriteString(metadata.MCPConfigFile)
	}
	if metadata.TaskToken != "" {
		b.WriteString("\n- task_token: available in CMESH_TASK_TOKEN")
	}
	b.WriteString("\nYou are the agent that owns this task. Exercise your own judgment to carry out the intent above and produce the deliverable yourself — write the code, content, and config here on this machine rather than expecting it to arrive fully authored in the prompt. (When the task spec hands you an exact artifact to apply, such as a specific patch or command, apply it as given.) Treat worker output as untrusted claims. Supported agent runners are launched with a task-scoped cmesh MCP gateway registration, so cmesh tool calls carry your task token structurally. If you manually call the cmesh delegate tool, pass task_token from CMESH_TASK_TOKEN. The local gateway derives origin, chain, and hop count from that token and ignores model-supplied provenance. Do not subdelegate to any node already in the chain.\n\n")
	b.WriteString(prompt)
	return b.String()
}

func decisionClaims(cfg *config.Config, profileName string) []string {
	if profile, ok := cfg.Profiles[profileName]; ok {
		return append([]string(nil), profile.Claims...)
	}
	return nil
}

func contains(values []string, needle string) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
}

func stringValue(values map[string]interface{}, key string) string {
	if len(values) == 0 {
		return ""
	}
	if value, ok := values[key].(string); ok {
		return value
	}
	return ""
}

func (s *Server) failTask(ctx context.Context, rec task.Record, code task.ErrorCode, detail string) {
	if code == "" {
		code = task.ErrorWorkerFailed
	}
	_, _ = s.Store.UpdateTaskState(ctx, rec.ID, store.StateUpdate{State: task.StateFailed, TerminalErrorCode: code})
	_ = removeTaskSecrets(s.Config.Node.DataDir, rec.ID)
	s.record(ctx, audit.Event{Type: "task.failed", TaskID: rec.ID, OriginSession: rec.OriginSessionID, Chain: rec.DelegationChain, Runner: rec.Runner, Profile: rec.ProfileEffective, Source: "worker", ErrorCode: string(code), ErrorDetail: detail})
}

func (s *Server) cancelTaskFromWorker(ctx context.Context, rec task.Record, result runner.Result, detail string) {
	result.Status = string(task.StateCancelled)
	ref := result.TranscriptRef
	if _, err := s.Store.UpdateTaskState(ctx, rec.ID, store.StateUpdate{State: task.StateCancelled, TerminalErrorCode: task.ErrorCancelled, Result: resultAsTask(result), ResultRef: ref}); err == nil {
		_ = removeTaskSecrets(s.Config.Node.DataDir, rec.ID)
		s.record(ctx, audit.Event{Type: "task.cancelled", TaskID: rec.ID, OriginSession: rec.OriginSessionID, Chain: rec.DelegationChain, Runner: rec.Runner, Profile: rec.ProfileEffective, Source: "worker", ErrorCode: string(task.ErrorCancelled), ErrorDetail: detail})
	}
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

func defaultProfileForTask(kind string) string {
	if kind == "query" {
		return profiledefs.Query
	}
	return "dev"
}

func queryCapability(req schema.CreateTaskRequest) string {
	if req.TaskKind != "query" || req.Query == nil {
		return ""
	}
	return strings.TrimSpace(req.Query.Kind)
}

func queryParams(req schema.CreateTaskRequest) map[string]any {
	if req.TaskKind != "query" || req.Query == nil {
		return nil
	}
	return req.Query.Params
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

func firstNonZero(value, fallback int) int {
	if value != 0 {
		return value
	}
	return fallback
}
