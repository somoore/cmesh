package gateway

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"cmesh/internal/audit"
	"cmesh/internal/config"
	"cmesh/internal/mcp/setupinstall"
	"cmesh/internal/mesh/rpc"
	"cmesh/internal/mesh/schema"
	"cmesh/internal/onboard"
	"cmesh/internal/policy"
	profiledefs "cmesh/internal/profile"
	"cmesh/internal/store"
	"cmesh/internal/task"
)

type Gateway struct {
	Config     *config.Config
	ConfigPath string
	Store      *store.Store
	Audit      *audit.Writer
	Policy     *policy.Engine
}

type DelegateRequest struct {
	Peer             string               `json:"peer"`
	Peers            []string             `json:"peers,omitempty"`
	Prompt           string               `json:"prompt"`
	CWD              string               `json:"cwd"`
	Runner           string               `json:"runner,omitempty"`
	Profile          string               `json:"profile,omitempty"`
	TimeoutSeconds   int                  `json:"timeout_s,omitempty"`
	Wait             bool                 `json:"wait,omitempty"`
	AllowDirty       bool                 `json:"allow_dirty,omitempty"`
	OriginNodeID     string               `json:"origin_node_id,omitempty"`
	OriginSessionID  string               `json:"origin_session_id,omitempty"`
	DelegationChain  []string             `json:"delegation_chain,omitempty"`
	HopCount         int                  `json:"hop_count,omitempty"`
	IdempotencyKey   string               `json:"idempotency_key,omitempty"`
	FollowupTaskID   string               `json:"followup_task_id,omitempty"`
	TaskToken        string               `json:"task_token,omitempty"`
	TaskKind         string               `json:"task_kind,omitempty"`
	Query            *schema.QueryRequest `json:"query,omitempty"`
	ProfileSource    string               `json:"profile_source,omitempty"`
	ProfileContextID string               `json:"profile_context_id,omitempty"`
}

type QueryPeerRequest struct {
	Peer           string         `json:"peer"`
	Kind           string         `json:"kind"`
	Params         map[string]any `json:"params,omitempty"`
	CWD            string         `json:"cwd,omitempty"`
	Profile        string         `json:"profile,omitempty"`
	TimeoutSeconds int            `json:"timeout_s,omitempty"`
	Wait           bool           `json:"wait,omitempty"`
	IdempotencyKey string         `json:"idempotency_key,omitempty"`
	TaskToken      string         `json:"task_token,omitempty"`
}

type InspectPeerRequest struct {
	Peer           string `json:"peer"`
	Prompt         string `json:"prompt"`
	CWD            string `json:"cwd,omitempty"`
	Profile        string `json:"profile,omitempty"`
	TimeoutSeconds int    `json:"timeout_s,omitempty"`
	Wait           bool   `json:"wait,omitempty"`
	IdempotencyKey string `json:"idempotency_key,omitempty"`
	TaskToken      string `json:"task_token,omitempty"`
}

type ProfileUseRequest struct {
	Peer                   string                             `json:"peer"`
	Profile                string                             `json:"profile"`
	CWD                    string                             `json:"cwd,omitempty"`
	CWDScope               string                             `json:"cwd_scope,omitempty"`
	TTL                    string                             `json:"ttl,omitempty"`
	ProfileAcknowledgement profiledefs.ProfileAcknowledgement `json:"profile_acknowledgement,omitempty"`
}

type ProfileConfigureRequest struct {
	Template     string   `json:"template,omitempty"`
	Name         string   `json:"name,omitempty"`
	Runner       string   `json:"runner,omitempty"`
	Peer         string   `json:"peer,omitempty"`
	AllowedPeers []string `json:"allowed_peers,omitempty"`
	Force        bool     `json:"force,omitempty"`
	Confirmed    bool     `json:"confirmed,omitempty"`
}

type ProfileGenerateRequest struct {
	Template     string   `json:"template"`
	Name         string   `json:"name,omitempty"`
	Runner       string   `json:"runner,omitempty"`
	Capabilities []string `json:"capabilities,omitempty"`
	OS           string   `json:"os,omitempty"`
}

type ProfileValidateRequest struct {
	Name                  string   `json:"name"`
	Runner                string   `json:"runner"`
	Capabilities          []string `json:"capabilities"`
	Claims                []string `json:"claims,omitempty"`
	Writes                string   `json:"writes,omitempty"`
	Shell                 string   `json:"shell,omitempty"`
	Network               bool     `json:"network,omitempty"`
	GitPush               bool     `json:"git_push,omitempty"`
	RequireExplicit       bool     `json:"require_explicit,omitempty"`
	RequireLease          bool     `json:"require_lease,omitempty"`
	EnforcementConfidence string   `json:"enforcement_confidence,omitempty"`
}

type profileContextFile struct {
	DefaultProfile string                        `json:"default_profile"`
	Peers          map[string]profileContextPeer `json:"peers"`
	UpdatedAt      time.Time                     `json:"updated_at"`
}

type profileContextPeer struct {
	Profile   string    `json:"profile"`
	CWD       string    `json:"cwd,omitempty"`
	CWDScope  string    `json:"cwd_scope,omitempty"`
	Scope     string    `json:"scope,omitempty"`
	ExpiresAt time.Time `json:"expires_at,omitempty"`
	LeaseID   string    `json:"lease_id,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

type resolvedProfileContext struct {
	Profile   string
	Source    string
	ContextID string
	CWD       string
	CWDScope  string
}

type ToolResponse struct {
	OK     bool       `json:"ok"`
	Result any        `json:"result,omitempty"`
	Error  *ToolError `json:"error,omitempty"`
}

type ToolError struct {
	Code   task.ErrorCode `json:"code"`
	Detail string         `json:"detail"`
}

type TaskRecipe struct {
	ID               string   `json:"id"`
	Title            string   `json:"title"`
	DefaultProfile   string   `json:"default_profile"`
	PromptTemplate   string   `json:"prompt_template"`
	ExpectedEvidence []string `json:"expected_evidence"`
	OriginChecks     []string `json:"origin_checks"`
}

func (g *Gateway) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /", g.mcp)
	mux.HandleFunc("POST /mcp", g.mcp)
	mux.HandleFunc("GET /health", g.health)
	mux.HandleFunc("POST /tools/task_catalog", g.taskCatalog)
	mux.HandleFunc("POST /tools/list_peers", g.listPeers)
	mux.HandleFunc("POST /tools/peer_info", g.peerInfo)
	mux.HandleFunc("POST /tools/delegate", g.delegate)
	mux.HandleFunc("POST /tools/query_peer", g.queryPeer)
	mux.HandleFunc("POST /tools/inspect_peer", g.inspectPeer)
	mux.HandleFunc("POST /tools/profile_guide", g.profileGuide)
	mux.HandleFunc("POST /tools/profile_recommend", g.profileRecommend)
	mux.HandleFunc("POST /tools/profile_generate", g.profileGenerate)
	mux.HandleFunc("POST /tools/profile_validate", g.profileValidate)
	mux.HandleFunc("POST /tools/profile_describe", g.profileDescribe)
	mux.HandleFunc("POST /tools/profile_current", g.profileCurrent)
	mux.HandleFunc("POST /tools/profile_configure", g.profileConfigure)
	mux.HandleFunc("POST /tools/profile_use", g.profileUse)
	mux.HandleFunc("POST /tools/profile_reset", g.profileReset)
	mux.HandleFunc("POST /tools/list_tasks", g.listTasks)
	mux.HandleFunc("POST /tools/task_status", g.taskStatus)
	mux.HandleFunc("POST /tools/task_result", g.taskResult)
	mux.HandleFunc("POST /tools/task_transcript", g.taskTranscript)
	mux.HandleFunc("POST /tools/task_artifact", g.taskArtifact)
	mux.HandleFunc("POST /tools/cancel", g.cancel)
	return g.hardenLocal(mux)
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (g *Gateway) mcp(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	var raw json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		writeRPC(w, rpcResponse{JSONRPC: "2.0", Error: &rpcError{Code: -32700, Message: "parse error: " + err.Error()}})
		return
	}
	if len(raw) > 0 && raw[0] == '[' {
		var batch []rpcRequest
		if err := json.Unmarshal(raw, &batch); err != nil {
			writeRPC(w, rpcResponse{JSONRPC: "2.0", Error: &rpcError{Code: -32600, Message: "invalid request: " + err.Error()}})
			return
		}
		responses := make([]rpcResponse, 0, len(batch))
		for _, req := range batch {
			if resp, ok := g.handleRPC(r.Context(), req); ok {
				responses = append(responses, resp)
			}
		}
		if len(responses) == 0 {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		writeRPC(w, responses)
		return
	}
	var req rpcRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		writeRPC(w, rpcResponse{JSONRPC: "2.0", Error: &rpcError{Code: -32600, Message: "invalid request: " + err.Error()}})
		return
	}
	resp, ok := g.handleRPC(r.Context(), req)
	if !ok {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	writeRPC(w, resp)
}

func (g *Gateway) handleRPC(ctx context.Context, req rpcRequest) (rpcResponse, bool) {
	if len(req.ID) == 0 {
		if req.Method == "notifications/initialized" {
			return rpcResponse{}, false
		}
		return rpcResponse{}, false
	}
	resp := rpcResponse{JSONRPC: "2.0", ID: req.ID}
	switch req.Method {
	case "initialize":
		resp.Result = map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities": map[string]any{
				"tools": map[string]any{"listChanged": false},
			},
			"serverInfo": map[string]any{"name": "cmesh", "version": "0.1.0"},
		}
	case "tools/list":
		resp.Result = map[string]any{"tools": mcpTools()}
	case "tools/call":
		var params struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &params); err != nil {
			resp.Error = &rpcError{Code: -32602, Message: "invalid tools/call params: " + err.Error()}
			return resp, true
		}
		result, toolErr := g.callTool(ctx, params.Name, params.Arguments)
		text, _ := json.Marshal(result)
		resp.Result = map[string]any{
			"content": []map[string]string{{"type": "text", "text": string(text)}},
			"isError": toolErr != nil,
		}
	default:
		resp.Error = &rpcError{Code: -32601, Message: "method not found: " + req.Method}
	}
	return resp, true
}

func (g *Gateway) callTool(ctx context.Context, name string, args json.RawMessage) (ToolResponse, error) {
	switch name {
	case "doctor":
		status := onboard.Detect(ctx, config.DefaultPath(), false)
		return ToolResponse{OK: status.ConfigOK && status.Machine.TailscaleOK, Result: status}, nil
	case "init_status":
		return ToolResponse{OK: true, Result: onboard.Detect(ctx, config.DefaultPath(), true)}, nil
	case "detect_runners":
		return ToolResponse{OK: true, Result: onboard.DetectRunners(ctx)}, nil
	case "task_catalog":
		return ToolResponse{OK: true, Result: map[string]any{"recipes": taskRecipes()}}, nil
	case "profile_templates":
		return ToolResponse{OK: true, Result: map[string]any{"templates": profiledefs.Builtins()}}, nil
	case "profile_guide":
		return ToolResponse{OK: true, Result: profiledefs.Guide()}, nil
	case "profile_recommend":
		var req struct {
			Intent string `json:"intent"`
		}
		if err := json.Unmarshal(args, &req); err != nil {
			return toolFailure(task.ErrorWorkerFailed, err.Error())
		}
		return ToolResponse{OK: true, Result: profiledefs.Recommend(req.Intent)}, nil
	case "profile_generate":
		var req ProfileGenerateRequest
		if err := json.Unmarshal(args, &req); err != nil {
			return toolFailure(task.ErrorWorkerFailed, err.Error())
		}
		return profileGenerateCall(req)
	case "profile_validate":
		var req ProfileValidateRequest
		if err := json.Unmarshal(args, &req); err != nil {
			return toolFailure(task.ErrorWorkerFailed, err.Error())
		}
		return profileValidateCall(req)
	case "profile_describe":
		var req struct {
			Profile string `json:"profile"`
		}
		if err := json.Unmarshal(args, &req); err != nil {
			return toolFailure(task.ErrorWorkerFailed, err.Error())
		}
		return g.profileDescribeCall(req.Profile)
	case "profile_current":
		return g.profileCurrentCall(ctx, args)
	case "profile_configure":
		var req ProfileConfigureRequest
		if err := json.Unmarshal(args, &req); err != nil {
			return toolFailure(task.ErrorWorkerFailed, err.Error())
		}
		return g.profileConfigureCall(req)
	case "profile_use":
		var req ProfileUseRequest
		if err := json.Unmarshal(args, &req); err != nil {
			return toolFailure(task.ErrorWorkerFailed, err.Error())
		}
		return g.profileUseCall(ctx, req)
	case "profile_reset":
		var req struct {
			Peer string `json:"peer,omitempty"`
		}
		if err := json.Unmarshal(args, &req); err != nil {
			return toolFailure(task.ErrorWorkerFailed, err.Error())
		}
		return g.profileResetCall(ctx, req.Peer)
	case "repair_peers":
		return g.repairPeersCall(ctx, args)
	case "list_tailnet_peers":
		peers, err := onboard.TailnetPeers(ctx, g.Config.Node.MeshPort, true)
		if err != nil {
			return toolFailure(task.ErrorWorkerFailed, err.Error())
		}
		return ToolResponse{OK: true, Result: peers}, nil
	case "suggest_config":
		var req struct {
			Role         string   `json:"role,omitempty"`
			AllowedPeers []string `json:"allowed_peers,omitempty"`
			AllowedCWDs  []string `json:"allowed_cwds,omitempty"`
			PeerNames    []string `json:"peer_names,omitempty"`
			Runner       string   `json:"runner,omitempty"`
			Profiles     []string `json:"profiles,omitempty"`
		}
		if err := json.Unmarshal(args, &req); err != nil {
			return toolFailure(task.ErrorWorkerFailed, err.Error())
		}
		machine := onboard.DetectMachine(ctx)
		runners := onboard.DetectRunners(ctx)
		peers, _ := onboard.TailnetPeers(ctx, g.Config.Node.MeshPort, true)
		body, err := onboard.GenerateConfig(onboard.InitOptions{
			Role:         req.Role,
			AllowedPeers: req.AllowedPeers,
			AllowedCWDs:  req.AllowedCWDs,
			PeerNames:    req.PeerNames,
			Runner:       req.Runner,
			Profiles:     req.Profiles,
		}, machine, runners, peers)
		if err != nil {
			return toolFailure(task.ErrorWorkerFailed, err.Error())
		}
		return ToolResponse{OK: true, Result: map[string]string{"toml": body, "path": config.DefaultPath()}}, nil
	case "validate_config":
		var req struct {
			Path string `json:"path,omitempty"`
			TOML string `json:"toml,omitempty"`
		}
		if err := json.Unmarshal(args, &req); err != nil {
			return toolFailure(task.ErrorWorkerFailed, err.Error())
		}
		path := req.Path
		if req.TOML != "" {
			tmp, err := os.CreateTemp("", "cmesh-config-*.toml")
			if err != nil {
				return toolFailure(task.ErrorWorkerFailed, err.Error())
			}
			path = tmp.Name()
			if _, err := tmp.WriteString(req.TOML); err != nil {
				_ = tmp.Close()
				_ = os.Remove(path)
				return toolFailure(task.ErrorWorkerFailed, err.Error())
			}
			_ = tmp.Close()
			defer os.Remove(path)
		}
		if path == "" {
			path = config.DefaultPath()
		}
		if err := onboard.ValidateConfig(path); err != nil {
			return ToolResponse{OK: false, Error: &ToolError{Code: task.ErrorWorkerFailed, Detail: err.Error()}}, nil
		}
		return ToolResponse{OK: true, Result: map[string]string{"path": path, "status": "valid"}}, nil
	case "register_harness":
		var req struct {
			Confirmed bool   `json:"confirmed,omitempty"`
			Harness   string `json:"harness,omitempty"`
		}
		if err := json.Unmarshal(args, &req); err != nil {
			return toolFailure(task.ErrorWorkerFailed, err.Error())
		}
		gatewayURL := fmt.Sprintf("http://127.0.0.1:%d/mcp", g.Config.Node.MCPPort)
		claudeArgs := setupinstall.ClaudeHTTPAddArgs(setupinstall.ServerName, gatewayURL, g.Config.Gateway.HostToken)
		if !req.Confirmed {
			claudeCommand := append([]string{"claude"}, setupinstall.RedactClaudeHTTPAddArgs(claudeArgs)...)
			return ToolResponse{OK: true, Result: map[string]any{
				"requires_confirmation": true,
				"claude_code_command":   setupinstall.ShellCommand(claudeCommand),
				"claude_code_headers":   g.Config.Gateway.HostToken != "",
				"codex_instruction":     "add a local HTTP MCP server named cmesh at " + gatewayURL,
			}}, nil
		}
		if req.Harness == "" || req.Harness == "claude-code" {
			claude, err := exec.LookPath("claude")
			if err != nil {
				return toolFailure(task.ErrorRunnerUnavailable, "Claude Code not found on PATH")
			}
			if err := exec.Command(claude, claudeArgs...).Run(); err != nil {
				return toolFailure(task.ErrorWorkerFailed, err.Error())
			}
			return ToolResponse{OK: true, Result: map[string]string{"harness": "claude-code", "url": gatewayURL}}, nil
		}
		return ToolResponse{OK: true, Result: map[string]string{"harness": req.Harness, "manual_url": gatewayURL}}, nil
	case "list_peers":
		type peer struct {
			Name          string `json:"name"`
			NodeID        string `json:"node_id,omitempty"`
			Address       string `json:"address"`
			Port          int    `json:"port"`
			ActiveProfile string `json:"active_profile"`
			ProfileSource string `json:"profile_source"`
			Elevated      bool   `json:"elevated"`
			Risk          string `json:"risk"`
			ExpiresInS    int64  `json:"expires_in_s,omitempty"`
			CWD           string `json:"cwd_scope,omitempty"`
			CWDScopeMode  string `json:"cwd_scope_mode,omitempty"`
		}
		var peers []peer
		for pname, cfg := range g.Config.Peers {
			meta := g.activeProfileSummary(pname)
			peers = append(peers, peer{Name: firstNonEmpty(cfg.Name, pname), NodeID: cfg.NodeID, Address: cfg.Address, Port: cfg.Port, ActiveProfile: meta.Profile, ProfileSource: meta.Source, Elevated: meta.Elevated, Risk: meta.Risk, ExpiresInS: meta.ExpiresInS, CWD: meta.CWD, CWDScopeMode: meta.CWDScope})
		}
		return ToolResponse{OK: true, Result: peers}, nil
	case "peer_info":
		var req struct {
			Peer string `json:"peer"`
		}
		if err := json.Unmarshal(args, &req); err != nil {
			return toolFailure(task.ErrorWorkerFailed, err.Error())
		}
		client, err := g.client(req.Peer)
		if err != nil {
			return toolFailure(task.ErrorPeerUnreachable, err.Error())
		}
		info, err := client.Hello(ctx)
		if err != nil {
			return rpcFailure(task.ErrorPeerUnreachable, err)
		}
		return ToolResponse{OK: true, Result: info}, nil
	case "test_peer":
		var req struct {
			Peer           string `json:"peer"`
			CWD            string `json:"cwd,omitempty"`
			Profile        string `json:"profile,omitempty"`
			TimeoutSeconds int    `json:"timeout_s,omitempty"`
		}
		if err := json.Unmarshal(args, &req); err != nil {
			return toolFailure(task.ErrorWorkerFailed, err.Error())
		}
		return g.testPeerCall(ctx, req.Peer, req.CWD, req.Profile, req.TimeoutSeconds)
	case "delegate":
		var req DelegateRequest
		if err := json.Unmarshal(args, &req); err != nil {
			return toolFailure(task.ErrorWorkerFailed, err.Error())
		}
		return g.delegateCall(ctx, req)
	case "query_peer":
		var req QueryPeerRequest
		if err := json.Unmarshal(args, &req); err != nil {
			return toolFailure(task.ErrorWorkerFailed, err.Error())
		}
		return g.queryPeerCall(ctx, req)
	case "inspect_peer":
		var req InspectPeerRequest
		if err := json.Unmarshal(args, &req); err != nil {
			return toolFailure(task.ErrorWorkerFailed, err.Error())
		}
		return g.inspectPeerCall(ctx, req)
	case "list_tasks":
		var req struct {
			Limit int `json:"limit,omitempty"`
		}
		if err := json.Unmarshal(args, &req); err != nil {
			return toolFailure(task.ErrorWorkerFailed, err.Error())
		}
		return g.listTasksCall(ctx, req.Limit)
	case "task_status":
		var req struct {
			TaskID         string `json:"task_id"`
			Wait           bool   `json:"wait,omitempty"`
			TimeoutSeconds int    `json:"timeout_s,omitempty"`
		}
		if err := json.Unmarshal(args, &req); err != nil {
			return toolFailure(task.ErrorWorkerFailed, err.Error())
		}
		return g.statusCall(ctx, req.TaskID, req.Wait, req.TimeoutSeconds)
	case "task_result":
		var req struct {
			TaskID string `json:"task_id"`
		}
		if err := json.Unmarshal(args, &req); err != nil {
			return toolFailure(task.ErrorWorkerFailed, err.Error())
		}
		return g.resultCall(ctx, req.TaskID)
	case "task_transcript":
		var req struct {
			TaskID string `json:"task_id"`
		}
		if err := json.Unmarshal(args, &req); err != nil {
			return toolFailure(task.ErrorWorkerFailed, err.Error())
		}
		return g.transcriptCall(ctx, req.TaskID)
	case "task_artifact":
		var req struct {
			TaskID string `json:"task_id"`
			Kind   string `json:"kind,omitempty"`
		}
		if err := json.Unmarshal(args, &req); err != nil {
			return toolFailure(task.ErrorWorkerFailed, err.Error())
		}
		return g.artifactCall(ctx, req.TaskID, req.Kind)
	case "cancel":
		var req struct {
			TaskID string `json:"task_id"`
			Reason string `json:"reason,omitempty"`
		}
		if err := json.Unmarshal(args, &req); err != nil {
			return toolFailure(task.ErrorWorkerFailed, err.Error())
		}
		return g.cancelCall(ctx, req.TaskID, req.Reason)
	default:
		return toolFailure(task.ErrorWorkerFailed, "unknown tool "+name)
	}
}

func (g *Gateway) ListenAndServe(ctx context.Context) error {
	addr := net.JoinHostPort("127.0.0.1", fmt.Sprintf("%d", g.Config.Node.MCPPort))
	lc := net.ListenConfig{}
	ln, err := lc.Listen(ctx, "tcp", addr)
	if err != nil {
		return err
	}
	server := &http.Server{Handler: g.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	if err := server.Serve(ln); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

func (g *Gateway) health(w http.ResponseWriter, r *http.Request) {
	writeTool(w, ToolResponse{OK: true, Result: map[string]string{"status": "ok"}})
}

func (g *Gateway) taskCatalog(w http.ResponseWriter, r *http.Request) {
	writeTool(w, ToolResponse{OK: true, Result: map[string]any{"recipes": taskRecipes()}})
}

func (g *Gateway) listPeers(w http.ResponseWriter, r *http.Request) {
	type peer struct {
		Name          string `json:"name"`
		NodeID        string `json:"node_id,omitempty"`
		Address       string `json:"address"`
		Port          int    `json:"port"`
		ActiveProfile string `json:"active_profile"`
		ProfileSource string `json:"profile_source"`
		Elevated      bool   `json:"elevated"`
		Risk          string `json:"risk"`
		ExpiresInS    int64  `json:"expires_in_s,omitempty"`
		CWD           string `json:"cwd_scope,omitempty"`
		CWDScopeMode  string `json:"cwd_scope_mode,omitempty"`
	}
	var peers []peer
	for name, cfg := range g.Config.Peers {
		meta := g.activeProfileSummary(name)
		peers = append(peers, peer{Name: firstNonEmpty(cfg.Name, name), NodeID: cfg.NodeID, Address: cfg.Address, Port: cfg.Port, ActiveProfile: meta.Profile, ProfileSource: meta.Source, Elevated: meta.Elevated, Risk: meta.Risk, ExpiresInS: meta.ExpiresInS, CWD: meta.CWD, CWDScopeMode: meta.CWDScope})
	}
	writeTool(w, ToolResponse{OK: true, Result: peers})
}

func (g *Gateway) peerInfo(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Peer string `json:"peer"`
	}
	if !decodeTool(w, r, &req) {
		return
	}
	client, err := g.client(req.Peer)
	if err != nil {
		writeToolError(w, task.ErrorPeerUnreachable, err.Error())
		return
	}
	info, err := client.Hello(r.Context())
	if err != nil {
		writeToolError(w, task.ErrorPeerUnreachable, err.Error())
		return
	}
	writeTool(w, ToolResponse{OK: true, Result: info})
}

func (g *Gateway) delegate(w http.ResponseWriter, r *http.Request) {
	var req DelegateRequest
	if !decodeTool(w, r, &req) {
		return
	}
	resp, err := g.delegateCall(r.Context(), req)
	if err != nil {
		writeTool(w, resp)
		return
	}
	writeTool(w, resp)
}

func (g *Gateway) queryPeer(w http.ResponseWriter, r *http.Request) {
	var req QueryPeerRequest
	if !decodeTool(w, r, &req) {
		return
	}
	resp, err := g.queryPeerCall(r.Context(), req)
	if err != nil {
		writeTool(w, resp)
		return
	}
	writeTool(w, resp)
}

func (g *Gateway) inspectPeer(w http.ResponseWriter, r *http.Request) {
	var req InspectPeerRequest
	if !decodeTool(w, r, &req) {
		return
	}
	resp, err := g.inspectPeerCall(r.Context(), req)
	if err != nil {
		writeTool(w, resp)
		return
	}
	writeTool(w, resp)
}

func (g *Gateway) profileGuide(w http.ResponseWriter, _ *http.Request) {
	writeTool(w, ToolResponse{OK: true, Result: profiledefs.Guide()})
}

func (g *Gateway) profileRecommend(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Intent string `json:"intent"`
	}
	if !decodeTool(w, r, &req) {
		return
	}
	writeTool(w, ToolResponse{OK: true, Result: profiledefs.Recommend(req.Intent)})
}

func (g *Gateway) profileGenerate(w http.ResponseWriter, r *http.Request) {
	var req ProfileGenerateRequest
	if !decodeTool(w, r, &req) {
		return
	}
	resp, err := profileGenerateCall(req)
	if err != nil {
		writeTool(w, resp)
		return
	}
	writeTool(w, resp)
}

func (g *Gateway) profileValidate(w http.ResponseWriter, r *http.Request) {
	var req ProfileValidateRequest
	if !decodeTool(w, r, &req) {
		return
	}
	resp, err := profileValidateCall(req)
	if err != nil {
		writeTool(w, resp)
		return
	}
	writeTool(w, resp)
}

func (g *Gateway) profileDescribe(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Profile string `json:"profile"`
	}
	if !decodeTool(w, r, &req) {
		return
	}
	resp, err := g.profileDescribeCall(req.Profile)
	if err != nil {
		writeTool(w, resp)
		return
	}
	writeTool(w, resp)
}

func (g *Gateway) profileCurrent(w http.ResponseWriter, r *http.Request) {
	resp, _ := g.profileCurrentCall(r.Context(), nil)
	writeTool(w, resp)
}

func (g *Gateway) profileUse(w http.ResponseWriter, r *http.Request) {
	var req ProfileUseRequest
	if !decodeTool(w, r, &req) {
		return
	}
	resp, err := g.profileUseCall(r.Context(), req)
	if err != nil {
		writeTool(w, resp)
		return
	}
	writeTool(w, resp)
}

func (g *Gateway) profileConfigure(w http.ResponseWriter, r *http.Request) {
	var req ProfileConfigureRequest
	if !decodeTool(w, r, &req) {
		return
	}
	resp, err := g.profileConfigureCall(req)
	if err != nil {
		writeTool(w, resp)
		return
	}
	writeTool(w, resp)
}

func (g *Gateway) profileReset(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Peer string `json:"peer,omitempty"`
	}
	if !decodeTool(w, r, &req) {
		return
	}
	resp, err := g.profileResetCall(r.Context(), req.Peer)
	if err != nil {
		writeTool(w, resp)
		return
	}
	writeTool(w, resp)
}

func (g *Gateway) queryPeerCall(ctx context.Context, req QueryPeerRequest) (ToolResponse, error) {
	kind := strings.TrimSpace(req.Kind)
	if kind == "" {
		return toolFailure(task.ErrorWorkerFailed, "kind is required")
	}
	profileName := strings.TrimSpace(req.Profile)
	profileSource := "request"
	profileContextID := ""
	if profileName == "" {
		resolved := g.resolveProfileContext(req.Peer)
		profileName = firstNonEmpty(resolved.Profile, profiledefs.Query)
		profileSource = resolved.Source
		profileContextID = resolved.ContextID
		if !profileContextCWDAllowed(req.CWD, resolved.CWD, resolved.CWDScope) {
			return toolFailure(task.ErrorProfileDenied, fmt.Sprintf("active profile lease for %q is scoped to cwd %q; requested cwd %q", resolved.Profile, resolved.CWD, req.CWD))
		}
		if strings.TrimSpace(req.CWD) == "" && strings.TrimSpace(resolved.CWD) != "" {
			req.CWD = resolved.CWD
		}
	}
	if !g.profileGrantsCapability(profileName, kind) {
		if routedProfile := g.typedProfileForCapability(kind, profileName); routedProfile != "" {
			if prof, ok := g.Config.Profiles[routedProfile]; ok && (prof.RequireExplicit || prof.RequireLease) {
				return profileDeniedWithGuidance(req.Peer, profileName, routedProfile, kind, req.CWD), nil
			}
			if g.profileRiskRank(routedProfile) > g.profileRiskRank(profileName) {
				return profileDeniedWithGuidance(req.Peer, profileName, routedProfile, kind, req.CWD), nil
			}
			return g.routeQueryToTypedProfile(ctx, req, profileName, routedProfile, kind)
		}
		if suggested := g.profileForDeniedCapability(kind); suggested != "" {
			return profileDeniedWithGuidance(req.Peer, profileName, suggested, kind, req.CWD), nil
		}
		return ToolResponse{OK: false, Result: map[string]any{
			"error":            "profile_denied",
			"detail":           "kind " + strconv.Quote(kind) + " is not a typed query capability under profile " + strconv.Quote(profileName),
			"nothing_executed": true,
		}, Error: &ToolError{
			Code:   task.ErrorProfileDenied,
			Detail: "kind " + strconv.Quote(kind) + " is not a typed query capability under profile " + strconv.Quote(profileName) + "; use inspect_peer with readonly-agent for open-ended read-only inspection, or configure a typed probe profile that grants this capability",
		}}, nil
	}
	prompt := "Run cmesh deterministic query: " + kind
	key := req.IdempotencyKey
	if key == "" {
		fresh, err := task.NewID()
		if err != nil {
			return toolFailure(task.ErrorWorkerFailed, err.Error())
		}
		key = fresh
	}
	return g.delegateCall(ctx, DelegateRequest{
		Peer:             req.Peer,
		Prompt:           prompt,
		CWD:              req.CWD,
		Profile:          profileName,
		TimeoutSeconds:   req.TimeoutSeconds,
		Wait:             req.Wait,
		IdempotencyKey:   key,
		TaskToken:        req.TaskToken,
		TaskKind:         "query",
		Query:            &schema.QueryRequest{Kind: kind, Params: req.Params, CWD: req.CWD},
		ProfileSource:    profileSource,
		ProfileContextID: profileContextID,
	})
}

func (g *Gateway) profileGrantsCapability(profileName, capability string) bool {
	profileName = profiledefs.CanonicalProfile(profileName)
	prof, ok := g.Config.Profiles[profileName]
	if !ok {
		return false
	}
	return contains(profiledefs.NormalizeCapabilities(prof.Capabilities, prof.Claims), capability)
}

func (g *Gateway) typedProfileForCapability(capability, currentProfile string) string {
	candidates := map[string][]string{
		profiledefs.CapNetworkCheck:   {profiledefs.QueryNetwork},
		profiledefs.CapPortCheck:      {profiledefs.QueryNetwork},
		profiledefs.CapShellReadonly:  {profiledefs.QueryShell},
		profiledefs.CapServiceStatus:  {profiledefs.QueryShell},
		profiledefs.CapLogTail:        {profiledefs.QueryShell},
		profiledefs.CapLogTailBounded: {profiledefs.QueryShell},
	}
	for _, profileName := range candidates[capability] {
		if profileName == currentProfile {
			continue
		}
		if g.profileGrantsCapability(profileName, capability) {
			return profileName
		}
	}
	return ""
}

func (g *Gateway) profileForDeniedCapability(capability string) string {
	for _, candidate := range []string{profiledefs.Query, profiledefs.QueryNetwork, profiledefs.QueryShell, profiledefs.RepoDev, profiledefs.DesktopControl, profiledefs.SystemChange} {
		if g.profileGrantsCapability(candidate, capability) {
			return candidate
		}
	}
	switch capability {
	case profiledefs.CapNetworkCheck, profiledefs.CapPortCheck:
		return profiledefs.QueryNetwork
	case profiledefs.CapServiceStatus, profiledefs.CapLogTail, profiledefs.CapLogTailBounded, profiledefs.CapShellReadonly:
		return profiledefs.QueryShell
	case profiledefs.CapEditRepo, profiledefs.CapExecuteInRepo:
		return profiledefs.RepoDev
	default:
		return ""
	}
}

func (g *Gateway) profileRiskRank(profileName string) int {
	prof, ok := g.Config.Profiles[profileName]
	if !ok {
		return 99
	}
	risk := firstNonEmpty(prof.Risk, profiledefs.RiskForConfig(profileName, profiledefs.NormalizeCapabilities(prof.Capabilities, prof.Claims), prof.Writes, prof.Shell, prof.Network))
	switch risk {
	case profiledefs.RiskLow:
		return 0
	case profiledefs.RiskMedium:
		return 1
	case profiledefs.RiskHigh:
		return 2
	default:
		return 99
	}
}

func profileDeniedWithGuidance(peer, activeProfile, requiredProfile, kind, cwd string) ToolResponse {
	detail := fmt.Sprintf("%s requires profile %s, but active profile is %s.", kind, requiredProfile, activeProfile)
	ttl := "15m"
	if requiredProfile == profiledefs.RepoDev {
		ttl = "30m"
	}
	parts := []string{"cmesh", "profile", "use", strings.TrimSpace(peer), requiredProfile, "--ttl", ttl}
	if strings.TrimSpace(cwd) != "" && requiredProfile == profiledefs.RepoDev {
		parts = append(parts, "--cwd", strings.TrimSpace(cwd))
	}
	suggested := setupinstall.ShellCommand(parts)
	return ToolResponse{OK: false, Result: map[string]any{
		"error":             "profile_denied",
		"detail":            detail,
		"nothing_executed":  true,
		"suggested_command": suggested,
	}, Error: &ToolError{
		Code:   task.ErrorProfileDenied,
		Detail: detail + "\nEnable it temporarily with:\n  " + suggested + "\nNothing was executed.",
	}}
}

func (g *Gateway) routeQueryToTypedProfile(ctx context.Context, req QueryPeerRequest, requestedProfile, routedProfile, kind string) (ToolResponse, error) {
	req.Profile = routedProfile
	resp, err := g.queryPeerCall(ctx, req)
	if !resp.OK || resp.Error != nil {
		return resp, err
	}
	return ToolResponse{OK: true, Result: map[string]any{
		"routed_from":       "query_peer",
		"reason":            "requested profile did not grant this typed query; used the least-powerful configured typed query profile instead",
		"requested_kind":    kind,
		"requested_profile": requestedProfile,
		"used_tool":         "query_peer",
		"used_profile":      routedProfile,
		"result":            resp.Result,
	}}, err
}

func (g *Gateway) inspectPeerCall(ctx context.Context, req InspectPeerRequest) (ToolResponse, error) {
	if strings.TrimSpace(req.Prompt) == "" {
		return toolFailure(task.ErrorWorkerFailed, "prompt is required")
	}
	profileName := strings.TrimSpace(req.Profile)
	if profileName == "" {
		profileName = profiledefs.ReadonlyAgent
	}
	resp, err := g.delegateCall(ctx, DelegateRequest{
		Peer:           req.Peer,
		Prompt:         readOnlyInspectionPrompt(req.Prompt),
		CWD:            req.CWD,
		Profile:        profileName,
		TimeoutSeconds: req.TimeoutSeconds,
		Wait:           req.Wait,
		IdempotencyKey: req.IdempotencyKey,
		TaskToken:      req.TaskToken,
		TaskKind:       "inspect",
	})
	if resp.Error != nil && resp.Error.Code == task.ErrorProfileUnavailable && profileName == profiledefs.ReadonlyAgent {
		resp.Error.Detail += `; configure the read-only inspection profile with profile_configure {"template":"readonly-agent","runner":"codex","peer":"` + req.Peer + `","confirmed":true}, then retry inspect_peer`
	}
	return resp, err
}

func readOnlyInspectionPrompt(prompt string) string {
	return "Read-only inspection request. Answer the user's question using only read-only inspection. Do not write, delete, modify files, change system state, install software, start/stop services, or push network changes. If the request requires a write or system change, refuse that part and explain the read-only alternative.\n\nUser request:\n" + strings.TrimSpace(prompt)
}

func profileGenerateCall(req ProfileGenerateRequest) (ToolResponse, error) {
	templateName := strings.TrimSpace(req.Template)
	if templateName == "" {
		templateName = profiledefs.Query
	}
	tmpl, err := profiledefs.Generate(templateName, profiledefs.GenerateOptions{
		Name:         req.Name,
		Runner:       req.Runner,
		Capabilities: req.Capabilities,
		OS:           req.OS,
	})
	if err != nil {
		return toolFailure(task.ErrorWorkerFailed, err.Error())
	}
	return ToolResponse{OK: true, Result: map[string]any{
		"template": tmpl,
		"toml":     profiledefs.RenderTOML(tmpl),
	}}, nil
}

func profileValidateCall(req ProfileValidateRequest) (ToolResponse, error) {
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = "custom"
	}
	capabilities := profiledefs.NormalizeCapabilities(req.Capabilities, req.Claims)
	if err := profiledefs.ValidateSemantics(profiledefs.Semantics{
		Name:                  name,
		Runner:                strings.TrimSpace(req.Runner),
		Capabilities:          capabilities,
		Writes:                req.Writes,
		Shell:                 req.Shell,
		Network:               req.Network,
		GitPush:               req.GitPush,
		RequireExplicit:       req.RequireExplicit,
		RequireLease:          req.RequireLease,
		EnforcementConfidence: req.EnforcementConfidence,
	}); err != nil {
		return ToolResponse{OK: false, Error: &ToolError{Code: task.ErrorProfileDenied, Detail: err.Error()}}, nil
	}
	return ToolResponse{OK: true, Result: map[string]any{"status": "valid", "name": name, "capabilities": capabilities}}, nil
}

func (g *Gateway) profileDescribeCall(profileName string) (ToolResponse, error) {
	profileName = strings.TrimSpace(profileName)
	if profileName == "" {
		return toolFailure(task.ErrorProfileUnavailable, "profile is required")
	}
	prof, ok := g.Config.Profiles[profileName]
	if !ok {
		return toolFailure(task.ErrorProfileUnavailable, "profile "+profileName+" is not configured")
	}
	result := map[string]any{
		"name":                   profileName,
		"profile":                prof,
		"capabilities":           profiledefs.NormalizeCapabilities(prof.Capabilities, prof.Claims),
		"require_explicit":       prof.RequireExplicit,
		"require_lease":          prof.RequireLease,
		"writes":                 prof.Writes,
		"shell":                  prof.Shell,
		"enforcement_confidence": prof.EnforcementConfidence,
	}
	if tmpl, ok := profiledefs.TemplateByName(profileName); ok {
		result["template"] = tmpl
	}
	return ToolResponse{OK: true, Result: result}, nil
}

func (g *Gateway) profileCurrentCall(_ context.Context, _ json.RawMessage) (ToolResponse, error) {
	ctxFile, err := g.readProfileContext()
	if err != nil {
		return toolFailure(task.ErrorWorkerFailed, err.Error())
	}
	summaries := map[string]activeProfileSummary{}
	for peer := range g.Config.Peers {
		summaries[peer] = g.activeProfileSummary(peer)
	}
	return ToolResponse{OK: true, Result: map[string]any{
		"default_profile": ctxFile.DefaultProfile,
		"peers":           summaries,
		"raw_context":     ctxFile,
	}}, nil
}

type activeProfileSummary struct {
	Peer         string     `json:"peer,omitempty"`
	Profile      string     `json:"active_profile"`
	Source       string     `json:"profile_source"`
	Elevated     bool       `json:"elevated"`
	Risk         string     `json:"risk"`
	ExpiresAt    *time.Time `json:"expires_at,omitempty"`
	ExpiresInS   int64      `json:"expires_in_s,omitempty"`
	CWD          string     `json:"cwd_scope,omitempty"`
	CWDScope     string     `json:"cwd_scope_mode,omitempty"`
	LeaseID      string     `json:"profile_context_id,omitempty"`
	RequireLease bool       `json:"require_lease"`
	ExplicitOnly bool       `json:"require_explicit"`
	Capabilities []string   `json:"capabilities,omitempty"`
}

func (g *Gateway) activeProfileSummary(peer string) activeProfileSummary {
	resolved := g.resolveProfileContext(peer)
	profileName := firstNonEmpty(resolved.Profile, g.defaultProfile(), profiledefs.Query)
	prof := g.Config.Profiles[profileName]
	risk := strings.TrimSpace(prof.Risk)
	if risk == "" {
		risk = profiledefs.RiskForConfig(profileName, profiledefs.NormalizeCapabilities(prof.Capabilities, prof.Claims), prof.Writes, prof.Shell, prof.Network)
	}
	out := activeProfileSummary{
		Peer:         strings.TrimSpace(peer),
		Profile:      profileName,
		Source:       resolved.Source,
		Elevated:     risk == profiledefs.RiskMedium || risk == profiledefs.RiskHigh,
		Risk:         risk,
		CWD:          resolved.CWD,
		CWDScope:     firstNonEmpty(resolved.CWDScope, "exact"),
		LeaseID:      resolved.ContextID,
		RequireLease: prof.RequireLease,
		ExplicitOnly: prof.RequireExplicit,
		Capabilities: profiledefs.NormalizeCapabilities(prof.Capabilities, prof.Claims),
	}
	if entry, ok := g.activeProfileContextEntry(peer); ok && !entry.ExpiresAt.IsZero() {
		expiresAt := entry.ExpiresAt
		out.ExpiresAt = &expiresAt
		out.ExpiresInS = int64(time.Until(entry.ExpiresAt) / time.Second)
		if out.ExpiresInS < 0 {
			out.ExpiresInS = 0
		}
	}
	return out
}

func (g *Gateway) profileUseCall(ctx context.Context, req ProfileUseRequest) (ToolResponse, error) {
	peer := strings.TrimSpace(req.Peer)
	profileName := strings.TrimSpace(req.Profile)
	if peer == "" {
		return toolFailure(task.ErrorPeerUnreachable, "peer is required")
	}
	if profileName == "" {
		return toolFailure(task.ErrorProfileUnavailable, "profile is required")
	}
	if _, ok := g.Config.Peers[peer]; !ok {
		return toolFailure(task.ErrorPeerUnreachable, "unknown peer "+peer)
	}
	prof, ok := g.Config.Profiles[profileName]
	if !ok {
		return toolFailure(task.ErrorProfileUnavailable, "local profile "+profileName+" is not configured; call profile_configure with confirmed=true to add a built-in profile such as repo-dev")
	}
	var ttl time.Duration
	if strings.TrimSpace(req.TTL) != "" {
		parsed, err := time.ParseDuration(strings.TrimSpace(req.TTL))
		if err != nil || parsed <= 0 {
			return toolFailure(task.ErrorWorkerFailed, "ttl must be a positive duration like 30m")
		}
		ttl = parsed
	}
	if prof.RequireLease && ttl <= 0 {
		return toolFailure(task.ErrorProfileDenied, "profile "+profileName+" requires ttl to create an active lease")
	}
	cwdScope := strings.TrimSpace(req.CWDScope)
	if cwdScope == "" {
		cwdScope = "exact"
	}
	if cwdScope != "exact" && cwdScope != "recursive" {
		return toolFailure(task.ErrorProfileDenied, "cwd_scope must be exact or recursive")
	}
	if problems := profiledefs.AcknowledgementProblems([]string{profileName}, []profiledefs.ProfileAcknowledgement{req.ProfileAcknowledgement}); len(problems) > 0 {
		return toolFailure(task.ErrorProfileAckRequired, strings.Join(problems, "; "))
	}
	ctxFile, err := g.readProfileContext()
	if err != nil {
		return toolFailure(task.ErrorWorkerFailed, err.Error())
	}
	if ctxFile.Peers == nil {
		ctxFile.Peers = map[string]profileContextPeer{}
	}
	now := time.Now().UTC()
	entry := profileContextPeer{Profile: profileName, CWD: strings.TrimSpace(req.CWD), CWDScope: cwdScope, Scope: "global", UpdatedAt: now}
	if ttl > 0 && !prof.RequireLease {
		entry.Scope = "session"
		entry.ExpiresAt = now.Add(ttl)
	}
	if ttl > 0 && prof.RequireLease {
		client, err := g.client(peer)
		if err != nil {
			return toolFailure(task.ErrorPeerUnreachable, err.Error())
		}
		lease, err := client.IssueProfileLease(ctx, schema.ProfileLeaseRequest{
			Profile:         profileName,
			CWD:             strings.TrimSpace(req.CWD),
			CWDScope:        cwdScope,
			TTLSeconds:      int64(ttl / time.Second),
			Capabilities:    profiledefs.NormalizeCapabilities(prof.Capabilities, prof.Claims),
			OriginSessionID: "profile_use",
		})
		if err != nil {
			return rpcFailure(task.ErrorProfileDenied, err)
		}
		entry.Scope = "worker_lease"
		entry.CWD = strings.TrimSpace(lease.CWD)
		entry.CWDScope = strings.TrimSpace(lease.CWDScope)
		entry.ExpiresAt = lease.ExpiresAt
		entry.LeaseID = lease.LeaseID
	}
	ctxFile.Peers[peer] = entry
	ctxFile.UpdatedAt = now
	if err := g.writeProfileContext(ctxFile); err != nil {
		return toolFailure(task.ErrorWorkerFailed, err.Error())
	}
	return ToolResponse{OK: true, Result: map[string]any{"peer": peer, "profile": profileName, "cwd": strings.TrimSpace(req.CWD), "cwd_scope": entry.CWDScope, "profile_source": "active_context", "profile_context_id": entry.LeaseID, "expires_at": entry.ExpiresAt}}, nil
}

func (g *Gateway) profileConfigureCall(req ProfileConfigureRequest) (ToolResponse, error) {
	allowedPeers := append([]string(nil), req.AllowedPeers...)
	if strings.TrimSpace(req.Peer) != "" {
		allowedPeers = append(allowedPeers, strings.TrimSpace(req.Peer))
	}
	result, err := onboard.ConfigureProfile(g.configPath(), onboard.ProfileConfigureOptions{
		Template:     req.Template,
		Name:         req.Name,
		Runner:       req.Runner,
		AllowedPeers: allowedPeers,
		Force:        req.Force,
	}, req.Confirmed)
	if err != nil {
		return toolFailure(task.ErrorWorkerFailed, err.Error())
	}
	payload := map[string]any{
		"path":                  result.Path,
		"profile":               result.Profile,
		"runner":                result.Runner,
		"already_configured":    result.AlreadyConfigured,
		"profile_added":         result.ProfileAdded,
		"runner_added":          result.RunnerAdded,
		"profile_repaired":      result.ProfileRepaired,
		"runner_repaired":       result.RunnerRepaired,
		"requires_confirmation": !req.Confirmed,
		"next_step":             result.NextStep,
	}
	if len(result.RunnerCommand) > 0 {
		payload["runner_command"] = result.RunnerCommand
	}
	if result.PreviewTOML != "" {
		payload["preview_toml"] = result.PreviewTOML
	}
	if req.Confirmed {
		if err := g.reloadConfig(); err != nil {
			return toolFailure(task.ErrorWorkerFailed, err.Error())
		}
		payload["status"] = "configured"
		payload["active_immediately"] = true
	}
	return ToolResponse{OK: true, Result: payload}, nil
}

func (g *Gateway) profileResetCall(_ context.Context, peer string) (ToolResponse, error) {
	ctxFile, err := g.readProfileContext()
	if err != nil {
		return toolFailure(task.ErrorWorkerFailed, err.Error())
	}
	peer = strings.TrimSpace(peer)
	report := map[string]any{"revoked": 0, "failed": 0}
	if peer == "" {
		report = g.revokeActiveProfileLeases(ctxFile)
		ctxFile.Peers = map[string]profileContextPeer{}
	} else {
		if entry, ok := ctxFile.Peers[peer]; ok && strings.TrimSpace(entry.LeaseID) != "" {
			report = g.revokeProfileLease(peer, entry.LeaseID)
		}
		delete(ctxFile.Peers, peer)
	}
	ctxFile.UpdatedAt = time.Now().UTC()
	if err := g.writeProfileContext(ctxFile); err != nil {
		return toolFailure(task.ErrorWorkerFailed, err.Error())
	}
	g.recordProfileReset(report)
	return ToolResponse{OK: true, Result: map[string]any{"default_profile": profiledefs.Query, "context": ctxFile, "lease_revocation": report}}, nil
}

func (g *Gateway) revokeActiveProfileLeases(ctxFile profileContextFile) map[string]any {
	revoked := 0
	var errorsOut []string
	for peer, entry := range ctxFile.Peers {
		if strings.TrimSpace(entry.LeaseID) == "" {
			continue
		}
		report := g.revokeProfileLease(peer, entry.LeaseID)
		if n, _ := report["revoked"].(int); n > 0 {
			revoked += n
		}
		if errText, _ := report["error"].(string); errText != "" {
			errorsOut = append(errorsOut, peer+": "+errText)
		}
	}
	return map[string]any{"revoked": revoked, "failed": len(errorsOut), "errors": errorsOut}
}

func (g *Gateway) revokeProfileLease(peer, leaseID string) map[string]any {
	client, err := g.client(peer)
	if err != nil {
		return map[string]any{"revoked": 0, "failed": 1, "error": err.Error()}
	}
	if _, err := client.RevokeProfileLease(context.Background(), leaseID); err != nil {
		return map[string]any{"revoked": 0, "failed": 1, "error": err.Error()}
	}
	return map[string]any{"revoked": 1, "failed": 0}
}

func (g *Gateway) recordProfileReset(report map[string]any) {
	if g.Audit == nil {
		return
	}
	_, _ = g.Audit.Append(audit.Event{
		Type:           "profile.context.reset",
		PolicyDecision: "allowed",
		Source:         "mcp_gateway",
		Extra:          report,
	})
}

func (g *Gateway) delegateCall(ctx context.Context, req DelegateRequest) (ToolResponse, error) {
	if len(req.Peers) > 0 {
		return g.delegateManyCall(ctx, req)
	}
	if req.TaskKind == "query" && req.Profile == "" {
		req.Profile = profiledefs.Query
		req.ProfileSource = "query_default"
	}
	var followupPeerTask store.PeerTask
	if req.FollowupTaskID != "" {
		peerTask, err := g.Store.GetPeerTask(ctx, req.FollowupTaskID)
		if err != nil {
			return toolFailure(task.ErrorWorkerFailed, err.Error())
		}
		followupPeerTask = peerTask
		if req.Peer != "" {
			if !g.peerMatchesNode(req.Peer, peerTask.PeerNodeID) {
				return toolFailure(task.ErrorWorkerFailed, "followup_task_id belongs to peer "+peerTask.PeerNodeID+"; omit peer or use the same peer")
			}
		} else {
			name, err := g.peerNameByNode(peerTask.PeerNodeID)
			if err != nil {
				return toolFailure(task.ErrorPeerUnreachable, err.Error())
			}
			req.Peer = name
		}
		req.OriginSessionID = firstNonEmpty(req.OriginSessionID, peerTask.OriginSessionID)
	}
	if req.Profile == "" {
		resolved := g.resolveProfileContext(req.Peer)
		req.Profile = resolved.Profile
		req.ProfileSource = resolved.Source
		req.ProfileContextID = resolved.ContextID
		if !profileContextCWDAllowed(req.CWD, resolved.CWD, resolved.CWDScope) {
			return toolFailure(task.ErrorProfileDenied, fmt.Sprintf("active profile lease for %q is scoped to cwd %q; requested cwd %q", resolved.Profile, resolved.CWD, req.CWD))
		}
		if strings.TrimSpace(req.CWD) == "" && strings.TrimSpace(resolved.CWD) != "" {
			req.CWD = resolved.CWD
		}
	} else if req.ProfileSource == "" {
		if resolved := g.resolveProfileContext(req.Peer); resolved.Profile == req.Profile && resolved.ContextID != "" {
			if !profileContextCWDAllowed(req.CWD, resolved.CWD, resolved.CWDScope) {
				return toolFailure(task.ErrorProfileDenied, fmt.Sprintf("active profile lease for %q is scoped to cwd %q; requested cwd %q", resolved.Profile, resolved.CWD, req.CWD))
			}
			req.ProfileSource = resolved.Source
			req.ProfileContextID = resolved.ContextID
			if strings.TrimSpace(req.CWD) == "" && strings.TrimSpace(resolved.CWD) != "" {
				req.CWD = resolved.CWD
			}
		} else {
			req.ProfileSource = "request"
		}
	}
	peer, ok := g.Config.Peers[req.Peer]
	if !ok {
		return toolFailure(task.ErrorPeerUnreachable, "unknown peer "+req.Peer)
	}
	prov, err := g.currentProvenance(ctx, req)
	if err != nil {
		return toolFailure(task.ErrorPeerUnauthorized, err.Error())
	}
	// The origin session is the ownership key for task-token reads, so it must
	// come from the token's stored context and never from the request body: a
	// worker's runner could otherwise mint children under an arbitrary session
	// and then read that session's tasks. currentProvenance already ignores
	// body-supplied provenance when a token is present; this applies the same
	// rule to the session. Host-token callers have no provenance and keep using
	// the request value.
	sessionID := strings.TrimSpace(prov.OriginSessionID)
	if _, scoped := taskTokenSession(ctx); scoped {
		// A token whose stored context carries no session cannot be scoped, and
		// must not fall through to the shared default session — that would put
		// its children where any other unscoped token could read them.
		if sessionID == "" {
			return toolFailure(task.ErrorPeerUnauthorized, "task token context has no origin session")
		}
	} else {
		sessionID = firstNonEmpty(strings.TrimSpace(req.OriginSessionID), sessionID, "gateway-default")
	}
	idempotencyKey := req.IdempotencyKey
	if idempotencyKey == "" {
		queryJSON, _ := json.Marshal(req.Query)
		idempotencyKey = strings.Join([]string{req.FollowupTaskID, req.Peer, req.CWD, req.Runner, req.Profile, req.ProfileSource, req.ProfileContextID, req.TaskKind, string(queryJSON), strconv.Itoa(req.TimeoutSeconds), strconv.FormatBool(req.AllowDirty), task.PromptHash(req.Prompt)}, "\x00")
	}
	taskID := task.DeterministicID(g.Config.Node.ID, sessionID, idempotencyKey)
	hopCount := prov.HopCount
	subdelegation := len(prov.Chain) > 0
	if subdelegation {
		hopCount++
	}
	chain := append([]string(nil), prov.Chain...)
	if len(chain) == 0 {
		chain = []string{g.Config.Node.ID}
	} else if !contains(chain, g.Config.Node.ID) {
		chain = append(chain, g.Config.Node.ID)
	}
	originNodeID := firstNonEmpty(prov.OriginNodeID, g.Config.Node.ID)
	intent := policy.TaskIntent{
		PeerName:         firstNonEmpty(peer.Name, req.Peer),
		PeerNodeID:       peer.NodeID,
		CWD:              req.CWD,
		Runner:           req.Runner,
		Profile:          req.Profile,
		HopCount:         hopCount,
		Chain:            chain,
		OriginNodeID:     originNodeID,
		AllowDirty:       req.AllowDirty,
		Subdelegation:    subdelegation,
		TaskKind:         req.TaskKind,
		Capability:       queryKind(req.Query),
		ProfileContextID: req.ProfileContextID,
	}
	decision := g.Policy.CheckOutbound(intent)
	if !decision.Allowed {
		return toolFailure(task.ErrorCode(decision.Code), decision.Detail)
	}
	client, err := g.client(req.Peer)
	if err != nil {
		return toolFailure(task.ErrorPeerUnreachable, err.Error())
	}
	resumeSessionID := ""
	followupBaseCommit := ""
	if req.FollowupTaskID != "" {
		if result, err := client.TaskResult(ctx, followupPeerTask.RemoteTaskID); err == nil && result.Result != nil {
			resumeSessionID = stringValue(result.Result.RunnerExtra, "session_id")
			followupBaseCommit = result.Result.HeadCommit
		} else if err != nil {
			return rpcFailure(task.ErrorWorkerFailed, err)
		} else {
			return toolFailure(task.ErrorWorkerFailed, "followup task result is not available; cannot resume session")
		}
	}
	resp, err := client.CreateTask(ctx, schema.CreateTaskRequest{
		TaskID:             taskID,
		OriginNodeID:       originNodeID,
		OriginSessionID:    sessionID,
		TaskKind:           req.TaskKind,
		Query:              req.Query,
		Prompt:             req.Prompt,
		CWD:                req.CWD,
		Runner:             req.Runner,
		Profile:            req.Profile,
		ProfileSource:      req.ProfileSource,
		ProfileContextID:   req.ProfileContextID,
		TimeoutSeconds:     req.TimeoutSeconds,
		Wait:               req.Wait,
		AllowDirty:         req.AllowDirty,
		FollowupTaskID:     req.FollowupTaskID,
		FollowupBaseCommit: followupBaseCommit,
		ResumeSessionID:    resumeSessionID,
		Provenance: schema.Provenance{
			OriginNodeID:    originNodeID,
			OriginSessionID: sessionID,
			WorkerNodeID:    peer.NodeID,
			PromptHash:      task.PromptHash(req.Prompt),
			DelegationChain: chain,
			HopCount:        hopCount,
		},
	})
	if err != nil {
		return rpcFailure(task.ErrorPeerUnreachable, err)
	}
	remoteTaskID := resp.TaskID
	localResp := resp
	localResp.TaskID = taskID
	localResp.RequestedProfile = req.Profile
	localResp.EffectiveProfile = decision.EffectiveProfile
	localResp.ProfileSource = req.ProfileSource
	localResp.ProfileContextID = req.ProfileContextID
	_ = g.Store.UpsertPeerTask(ctx, store.PeerTask{
		TaskID:          taskID,
		PeerNodeID:      firstNonEmpty(peer.NodeID, peer.Name, req.Peer),
		RemoteTaskID:    remoteTaskID,
		OriginSessionID: sessionID,
		State:           resp.State,
	})
	g.record(ctx, audit.Event{
		Type:           "task.dispatch.requested",
		TaskID:         taskID,
		PeerIdentity:   firstNonEmpty(peer.NodeID, peer.Name, req.Peer),
		OriginSession:  sessionID,
		Chain:          chain,
		Runner:         req.Runner,
		Profile:        decision.EffectiveProfile,
		PolicyDecision: "allowed",
		Source:         "local_gateway",
	})
	return ToolResponse{OK: true, Result: localResp}, nil
}

func (g *Gateway) delegateManyCall(ctx context.Context, req DelegateRequest) (ToolResponse, error) {
	peers := uniqueNonEmpty(req.Peers)
	if req.Peer != "" {
		peers = uniqueNonEmpty(append([]string{req.Peer}, peers...))
	}
	if len(peers) == 0 {
		return toolFailure(task.ErrorPeerUnreachable, "no peers supplied")
	}
	results := make(map[string]ToolResponse, len(peers))
	okAll := true
	for _, peer := range peers {
		peerReq := req
		peerReq.Peer = peer
		peerReq.Peers = nil
		resp, _ := g.delegateCall(ctx, peerReq)
		results[peer] = resp
		if !resp.OK {
			okAll = false
		}
	}
	return ToolResponse{OK: okAll, Result: map[string]any{"peers": results}}, nil
}

const gatewaySmokeExpectedResponse = "cmesh smoke ok"

// repairPeersCall reconciles the config's [peers.*] entries against their
// authoritative tailscale whois identity (see onboard.DiagnosePeers). It loads the
// config fresh from disk — that file is the thing being repaired — rather than using
// the daemon's in-memory g.Config.
//
// IMPORTANT: the gateway runs INSIDE the daemon, which loaded its policy once at
// startup and has no config-reload path. So a confirmed repair rewrites the file but
// does NOT change what this live daemon enforces until it is restarted. The
// next_step says so in plain terms; we never report success as if the running mesh
// were already fixed.
func (g *Gateway) repairPeersCall(ctx context.Context, args json.RawMessage) (ToolResponse, error) {
	var req struct {
		Confirmed bool `json:"confirmed,omitempty"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &req); err != nil {
			return toolFailure(task.ErrorWorkerFailed, err.Error())
		}
	}
	cfg, err := config.Load(config.DefaultPath())
	if err != nil {
		return toolFailure(task.ErrorWorkerFailed, "load config: "+err.Error())
	}
	if len(cfg.Peers) == 0 {
		return ToolResponse{OK: true, Result: map[string]any{
			"repairs":   []onboard.PeerRepair{},
			"message":   "No [peers.*] entries are configured; nothing to repair.",
			"next_step": "If you expected a peer here, run the cmesh setup flow to discover and link one.",
		}}, nil
	}
	repairs, err := onboard.DiagnosePeers(ctx, cfg)
	if err != nil {
		return toolFailure(task.ErrorWorkerFailed, err.Error())
	}
	fixable := onboard.CountRepairs(repairs, onboard.RepairFixable)

	if !req.Confirmed {
		msg := fmt.Sprintf("Found %d configured peer(s): %d fixable, %d ok.", len(repairs), fixable, onboard.CountRepairs(repairs, onboard.RepairOK))
		next := "Nothing to fix — all reachable peer entries already match their whois identity."
		if fixable > 0 {
			next = "Call repair_peers again with confirmed=true to rewrite the fixable entries. The running daemon reloads the repaired policy immediately; the response reports whether that succeeded. Conflicts and unreachable peers are left unchanged."
		}
		return ToolResponse{OK: true, Result: map[string]any{
			"requires_confirmation": fixable > 0,
			"repairs":               repairs,
			"message":               msg,
			"next_step":             next,
		}}, nil
	}

	fixedNames, err := onboard.ApplyPeerRepairs(config.DefaultPath(), repairs)
	if err != nil {
		return toolFailure(task.ErrorWorkerFailed, err.Error())
	}
	payload := map[string]any{
		"repaired": fixedNames,
		"repairs":  repairs,
		"message":  fmt.Sprintf("Rewrote %d peer entry(ies) in the config: %s.", len(fixedNames), strings.Join(fixedNames, ", ")),
	}
	// Apply the repaired config to the shared policy engine so the running mesh
	// server enforces the corrected peer trust immediately. Report the reload
	// outcome honestly: claiming the mesh is fixed while the daemon still holds
	// the old policy is exactly the false success this tool must avoid.
	if err := g.reloadConfig(); err != nil {
		payload["active_immediately"] = false
		payload["requires_daemon_restart"] = true
		payload["reload_error"] = err.Error()
		payload["next_step"] = "The repaired config could not be reloaded into the running daemon. Restart it (e.g. `cmesh up service restart`, or stop and re-run `cmesh up`), then run a bidirectional smoke to confirm."
		return ToolResponse{OK: true, Result: payload}, nil
	}
	payload["active_immediately"] = true
	payload["requires_daemon_restart"] = false
	payload["next_step"] = "The running daemon now enforces the repaired peer trust. Run a bidirectional smoke to confirm."
	return ToolResponse{OK: true, Result: payload}, nil
}

func (g *Gateway) testPeerCall(ctx context.Context, peer, cwd, profile string, timeoutSeconds int) (ToolResponse, error) {
	if strings.TrimSpace(peer) == "" {
		return toolFailure(task.ErrorPeerUnreachable, "peer is required")
	}
	// An empty cwd is forwarded as-is: this task runs on the REMOTE worker, which
	// self-selects a cwd its own policy accepts when none is given (keyed off the
	// "mcp-smoke" origin session below). A cwd from this node's policy could name a
	// path that does not exist on the worker (e.g. cross-OS). Callers can still pass
	// an explicit cwd to target a specific directory.
	if timeoutSeconds <= 0 {
		timeoutSeconds = 60
	}
	client, err := g.client(peer)
	if err != nil {
		return toolFailure(task.ErrorPeerUnreachable, err.Error())
	}
	info, err := client.Hello(ctx)
	if err != nil {
		return rpcFailure(task.ErrorPeerUnreachable, err)
	}
	if strings.TrimSpace(profile) == "" {
		if helloSupportsProfile(info, profiledefs.Query) {
			profile = profiledefs.Query
		} else {
			profile = "readonly"
		}
	}
	var delegated ToolResponse
	if profile == profiledefs.Query {
		delegated, err = g.queryPeerCall(ctx, QueryPeerRequest{
			Peer:           peer,
			Kind:           profiledefs.CapSystemInfo,
			CWD:            cwd,
			Profile:        profile,
			TimeoutSeconds: timeoutSeconds,
			Wait:           true,
			IdempotencyKey: strings.Join([]string{"mcp-smoke", peer, cwd, profile, strconv.Itoa(timeoutSeconds)}, "\x00"),
		})
	} else {
		delegated, err = g.delegateCall(ctx, DelegateRequest{
			Peer:            peer,
			Prompt:          "Reply with exactly: " + gatewaySmokeExpectedResponse,
			CWD:             cwd,
			Profile:         profile,
			TimeoutSeconds:  timeoutSeconds,
			Wait:            true,
			OriginSessionID: "mcp-smoke",
			IdempotencyKey:  strings.Join([]string{"mcp-smoke", peer, cwd, profile, strconv.Itoa(timeoutSeconds)}, "\x00"),
		})
	}
	if err != nil {
		return delegated, err
	}
	created, ok := delegated.Result.(schema.CreateTaskResponse)
	if !ok {
		return toolFailure(task.ErrorWorkerFailed, "smoke task response was not a result envelope")
	}
	result, err := g.resultCall(ctx, created.TaskID)
	if err != nil {
		return result, err
	}
	taskResult, ok := result.Result.(schema.TaskResultResponse)
	if !ok || taskResult.Result == nil {
		return toolFailure(task.ErrorTranscriptUnavailable, "smoke task result envelope is not available")
	}
	auditEventWritten, err := g.Store.HasAuditEvent(ctx, created.TaskID, "task.dispatch.requested")
	if err != nil {
		return toolFailure(task.ErrorWorkerFailed, err.Error())
	}
	verified := gatewaySmokeResultMatches(taskResult.Result, profile)
	checks := map[string]bool{
		"peer_reachable":          true,
		"policy_allowed":          delegated.OK,
		"task_created":            created.TaskID != "",
		"runner_invoked":          taskResult.Result.CommandOutput.Command != "",
		"prompt_received":         verified,
		"smoke_response_verified": verified,
		"result_envelope_parsed":  taskResult.Result.Status == task.StateDone,
		"audit_event_written":     auditEventWritten,
	}
	if !verified {
		return ToolResponse{OK: false, Result: map[string]any{
			"checks":       checks,
			"peer":         peer,
			"node_id":      info.NodeID,
			"node_name":    info.NodeName,
			"task_id":      created.TaskID,
			"result":       taskResult,
			"expected":     smokeExpectedForProfile(profile),
			"failure_hint": "runner did not return the expected smoke response in command_output",
		}}, fmt.Errorf("smoke task did not return %q", smokeExpectedForProfile(profile))
	}
	return ToolResponse{OK: true, Result: map[string]any{
		"checks":       checks,
		"peer":         peer,
		"node_id":      info.NodeID,
		"node_name":    info.NodeName,
		"runners":      info.Runners,
		"capabilities": info.Capabilities,
		"task_id":      created.TaskID,
		"result":       taskResult,
	}}, nil
}

func gatewaySmokeResultMatches(result *task.ResultEnvelope, profile string) bool {
	if result == nil {
		return false
	}
	if result.CommandOutput.Command == "" || result.CommandOutput.ExitCode != 0 {
		return false
	}
	if profile == profiledefs.Query {
		return result.CommandOutput.Command == "probe:"+profiledefs.CapSystemInfo && strings.Contains(result.CommandOutput.Stdout, `"kind":"system_info"`)
	}
	return strings.Contains(result.CommandOutput.Stdout, gatewaySmokeExpectedResponse) || strings.Contains(result.CommandOutput.Stderr, gatewaySmokeExpectedResponse)
}

func smokeExpectedForProfile(profile string) string {
	if profile == profiledefs.Query {
		return "probe:" + profiledefs.CapSystemInfo
	}
	return gatewaySmokeExpectedResponse
}

func helloSupportsProfile(hello schema.HelloResponse, profile string) bool {
	for _, runner := range hello.Runners {
		for _, candidate := range runner.Profiles {
			if candidate == profile {
				return true
			}
		}
	}
	return false
}

func (g *Gateway) taskStatus(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TaskID         string `json:"task_id"`
		Wait           bool   `json:"wait,omitempty"`
		TimeoutSeconds int    `json:"timeout_s,omitempty"`
	}
	if !decodeTool(w, r, &req) {
		return
	}
	resp, _ := g.statusCall(r.Context(), req.TaskID, req.Wait, req.TimeoutSeconds)
	writeTool(w, resp)
}

func (g *Gateway) listTasks(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Limit int `json:"limit,omitempty"`
	}
	if !decodeTool(w, r, &req) {
		return
	}
	resp, _ := g.listTasksCall(r.Context(), req.Limit)
	writeTool(w, resp)
}

func (g *Gateway) taskResult(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TaskID string `json:"task_id"`
	}
	if !decodeTool(w, r, &req) {
		return
	}
	resp, _ := g.resultCall(r.Context(), req.TaskID)
	writeTool(w, resp)
}

func (g *Gateway) taskTranscript(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TaskID string `json:"task_id"`
	}
	if !decodeTool(w, r, &req) {
		return
	}
	resp, _ := g.transcriptCall(r.Context(), req.TaskID)
	writeTool(w, resp)
}

func (g *Gateway) taskArtifact(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TaskID string `json:"task_id"`
		Kind   string `json:"kind,omitempty"`
	}
	if !decodeTool(w, r, &req) {
		return
	}
	resp, _ := g.artifactCall(r.Context(), req.TaskID, req.Kind)
	writeTool(w, resp)
}

func (g *Gateway) cancel(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TaskID string `json:"task_id"`
		Reason string `json:"reason,omitempty"`
	}
	if !decodeTool(w, r, &req) {
		return
	}
	resp, _ := g.cancelCall(r.Context(), req.TaskID, req.Reason)
	writeTool(w, resp)
}

// authorizeTaskAccess gates the task read/cancel surface for a caller holding a
// per-task token rather than the gateway host token.
//
// A host-token caller is the machine's own agent and may read any task this node
// delegated; peer_tasks is precisely that mirror, and origin-restart recovery
// depends on those lookups staying unscoped.
//
// A task-token caller is a *worker's* runner reaching back in during
// subdelegation. Its output is untrusted by design, so it must not be able to
// enumerate or act on tasks outside its own delegation. Both the task context
// file backing the token and peer_tasks carry origin_session_id, so the token's
// session is the ownership key: it covers the token's own task and anything that
// task delegated onward (children inherit the session), and nothing else.
//
// Tasks predating this scoping have an empty mirrored session; those stay
// readable only by a host-token caller, never by a task token.
func (g *Gateway) authorizeTaskAccess(ctx context.Context, peerTask store.PeerTask) error {
	prov, ok := ctx.Value(provenanceContextKey{}).(gatewayProvenance)
	if !ok {
		return nil
	}
	session := strings.TrimSpace(prov.OriginSessionID)
	if session != "" && session == strings.TrimSpace(peerTask.OriginSessionID) {
		return nil
	}
	return errors.New("task token is not authorized for this task")
}

// taskTokenSession returns the origin session of a task-token caller, and false
// for a host-token caller (which is not session-scoped).
func taskTokenSession(ctx context.Context) (string, bool) {
	prov, ok := ctx.Value(provenanceContextKey{}).(gatewayProvenance)
	if !ok {
		return "", false
	}
	return strings.TrimSpace(prov.OriginSessionID), true
}

func (g *Gateway) listTasksCall(ctx context.Context, limit int) (ToolResponse, error) {
	peerTasks, err := g.Store.ListPeerTasks(ctx, limit)
	if err != nil {
		return toolFailure(task.ErrorWorkerFailed, err.Error())
	}
	localTasks, err := g.Store.RecentTasks(ctx, limit)
	if err != nil {
		return toolFailure(task.ErrorWorkerFailed, err.Error())
	}
	// A task-token caller may only see its own delegation, so filter the mirror
	// to its session and withhold this node's local task list entirely.
	if session, scoped := taskTokenSession(ctx); scoped {
		filtered := peerTasks[:0]
		for _, peerTask := range peerTasks {
			if session != "" && strings.TrimSpace(peerTask.OriginSessionID) == session {
				filtered = append(filtered, peerTask)
			}
		}
		return ToolResponse{OK: true, Result: map[string]any{"delegated": filtered, "local": []task.Record{}}}, nil
	}
	return ToolResponse{OK: true, Result: map[string]any{"delegated": peerTasks, "local": localTasks}}, nil
}

func (g *Gateway) statusCall(ctx context.Context, taskID string, wait bool, timeoutSeconds int) (ToolResponse, error) {
	peerTask, err := g.Store.GetPeerTask(ctx, taskID)
	if err != nil {
		return toolFailure(task.ErrorWorkerFailed, err.Error())
	}
	if err := g.authorizeTaskAccess(ctx, peerTask); err != nil {
		return toolFailure(task.ErrorPeerUnauthorized, err.Error())
	}
	client, err := g.clientByNode(peerTask.PeerNodeID)
	if err != nil {
		return ToolResponse{OK: true, Result: mirroredStatus(peerTask, err)}, nil
	}
	resp, err := client.TaskStatus(ctx, peerTask.RemoteTaskID, wait, timeoutSeconds)
	if err != nil {
		return ToolResponse{OK: true, Result: mirroredStatus(peerTask, err)}, nil
	}
	_ = g.Store.UpsertPeerTask(ctx, store.PeerTask{TaskID: taskID, PeerNodeID: peerTask.PeerNodeID, RemoteTaskID: peerTask.RemoteTaskID, OriginSessionID: peerTask.OriginSessionID, State: resp.State})
	return ToolResponse{OK: true, Result: resp}, nil
}

// fetchOwnedTask resolves a mirrored task, authorizes the caller against
// it, and hands the peer client to fetch. Every task-scoped read shares this
// prologue so the ownership check cannot be omitted from a new one.
func (g *Gateway) fetchOwnedTask(
	ctx context.Context,
	taskID string,
	unreachableCode task.ErrorCode,
	fetch func(rpcClient *rpc.Client, remoteTaskID string) (any, error),
) (ToolResponse, error) {
	peerTask, err := g.Store.GetPeerTask(ctx, taskID)
	if err != nil {
		return toolFailure(task.ErrorWorkerFailed, err.Error())
	}
	if err := g.authorizeTaskAccess(ctx, peerTask); err != nil {
		return toolFailure(task.ErrorPeerUnauthorized, err.Error())
	}
	client, err := g.clientByNode(peerTask.PeerNodeID)
	if err != nil {
		return toolFailure(task.ErrorPeerUnreachable, err.Error())
	}
	resp, err := fetch(client, peerTask.RemoteTaskID)
	if err != nil {
		return rpcFailure(unreachableCode, err)
	}
	return ToolResponse{OK: true, Result: resp}, nil
}

func (g *Gateway) resultCall(ctx context.Context, taskID string) (ToolResponse, error) {
	return g.fetchOwnedTask(ctx, taskID, task.ErrorPeerUnreachable,
		func(client *rpc.Client, remoteTaskID string) (any, error) {
			return client.TaskResult(ctx, remoteTaskID)
		})
}

func (g *Gateway) transcriptCall(ctx context.Context, taskID string) (ToolResponse, error) {
	return g.fetchOwnedTask(ctx, taskID, task.ErrorTranscriptUnavailable,
		func(client *rpc.Client, remoteTaskID string) (any, error) {
			return client.TaskTranscript(ctx, remoteTaskID)
		})
}

func (g *Gateway) artifactCall(ctx context.Context, taskID, kind string) (ToolResponse, error) {
	if kind == "" {
		kind = "git-bundle"
	}
	if kind != "git-bundle" {
		return toolFailure(task.ErrorWorkerFailed, "unsupported artifact kind "+kind)
	}
	peerTask, err := g.Store.GetPeerTask(ctx, taskID)
	if err != nil {
		return toolFailure(task.ErrorWorkerFailed, err.Error())
	}
	if err := g.authorizeTaskAccess(ctx, peerTask); err != nil {
		return toolFailure(task.ErrorPeerUnauthorized, err.Error())
	}
	client, err := g.clientByNode(peerTask.PeerNodeID)
	if err != nil {
		return toolFailure(task.ErrorPeerUnreachable, err.Error())
	}
	body, err := client.TaskArtifact(ctx, peerTask.RemoteTaskID, kind)
	if err != nil {
		return rpcFailure(task.ErrorWorkerFailed, err)
	}
	dir := filepath.Join(g.Config.Node.DataDir, "artifacts", "from-peers")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return toolFailure(task.ErrorWorkerFailed, err.Error())
	}
	path := filepath.Join(dir, taskID+"."+kind)
	if err := os.WriteFile(path, body, 0o600); err != nil {
		return toolFailure(task.ErrorWorkerFailed, err.Error())
	}
	return ToolResponse{OK: true, Result: map[string]any{"task_id": taskID, "kind": kind, "path": path, "bytes": len(body)}}, nil
}

func (g *Gateway) cancelCall(ctx context.Context, taskID, reason string) (ToolResponse, error) {
	peerTask, err := g.Store.GetPeerTask(ctx, taskID)
	if err != nil {
		return toolFailure(task.ErrorWorkerFailed, err.Error())
	}
	if err := g.authorizeTaskAccess(ctx, peerTask); err != nil {
		return toolFailure(task.ErrorPeerUnauthorized, err.Error())
	}
	client, err := g.clientByNode(peerTask.PeerNodeID)
	if err != nil {
		return toolFailure(task.ErrorPeerUnreachable, err.Error())
	}
	resp, err := client.Cancel(ctx, peerTask.RemoteTaskID, reason)
	if err != nil {
		return rpcFailure(task.ErrorPeerUnreachable, err)
	}
	return ToolResponse{OK: true, Result: resp}, nil
}

func (g *Gateway) client(peerName string) (*rpc.Client, error) {
	peer, ok := g.Config.Peers[peerName]
	if !ok {
		return nil, fmt.Errorf("unknown peer %q", peerName)
	}
	if peer.Address == "" {
		return nil, fmt.Errorf("peer %q has no address", peerName)
	}
	port := peer.Port
	if port == 0 {
		port = config.DefaultMeshPort
	}
	return rpc.NewClient("http://" + net.JoinHostPort(peer.Address, fmt.Sprintf("%d", port))), nil
}

func (g *Gateway) clientByNode(nodeID string) (*rpc.Client, error) {
	name, err := g.peerNameByNode(nodeID)
	if err != nil {
		return nil, err
	}
	return g.client(name)
}

func (g *Gateway) peerNameByNode(nodeID string) (string, error) {
	for name, peer := range g.Config.Peers {
		if peer.NodeID == nodeID || peer.Name == nodeID || name == nodeID {
			return name, nil
		}
	}
	return "", fmt.Errorf("no configured peer for node %q", nodeID)
}

func (g *Gateway) peerMatchesNode(peerName, nodeID string) bool {
	peer, ok := g.Config.Peers[peerName]
	if !ok {
		return false
	}
	return peer.NodeID == nodeID || peer.Name == nodeID || peerName == nodeID
}

type gatewayProvenance struct {
	OriginNodeID    string
	OriginSessionID string
	Chain           []string
	HopCount        int
}

type provenanceContextKey struct{}

func (g *Gateway) currentProvenance(ctx context.Context, req DelegateRequest) (gatewayProvenance, error) {
	if prov, ok := ctx.Value(provenanceContextKey{}).(gatewayProvenance); ok {
		return prov, nil
	}
	if req.TaskToken != "" {
		return g.provenanceFromTaskToken(req.TaskToken)
	}
	return gatewayProvenance{}, nil
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

func (g *Gateway) provenanceFromTaskToken(token string) (gatewayProvenance, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return gatewayProvenance{}, errors.New("empty task token")
	}
	tokenHash := task.SHA256Hex([]byte(token))
	path := filepath.Join(g.Config.Node.DataDir, "task-tokens", tokenHash+".json")
	body, err := os.ReadFile(path)
	if err != nil {
		return gatewayProvenance{}, fmt.Errorf("invalid task token")
	}
	var ctxFile taskContextFile
	if err := json.Unmarshal(body, &ctxFile); err != nil {
		return gatewayProvenance{}, fmt.Errorf("invalid task token context: %w", err)
	}
	if ctxFile.TaskTokenHash != tokenHash {
		return gatewayProvenance{}, fmt.Errorf("task token context hash mismatch")
	}
	return gatewayProvenance{
		OriginNodeID:    ctxFile.OriginNodeID,
		OriginSessionID: ctxFile.OriginSessionID,
		Chain:           append([]string(nil), ctxFile.DelegationChain...),
		HopCount:        ctxFile.HopCount,
	}, nil
}

func mirroredStatus(peerTask store.PeerTask, cause error) map[string]any {
	out := map[string]any{
		"task_id":        peerTask.TaskID,
		"remote_task_id": peerTask.RemoteTaskID,
		"peer_node_id":   peerTask.PeerNodeID,
		"state":          peerTask.State,
		"stale":          true,
		"updated_at":     peerTask.UpdatedAt,
	}
	if cause != nil {
		out["peer_error"] = cause.Error()
	}
	return out
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

func queryKind(query *schema.QueryRequest) string {
	if query == nil {
		return ""
	}
	return strings.TrimSpace(query.Kind)
}

func (g *Gateway) resolveProfileContext(peer string) resolvedProfileContext {
	ctxFile, err := g.readProfileContext()
	if err == nil {
		if entry, ok := ctxFile.Peers[strings.TrimSpace(peer)]; ok && strings.TrimSpace(entry.Profile) != "" {
			if entry.ExpiresAt.IsZero() || time.Now().UTC().Before(entry.ExpiresAt) {
				return resolvedProfileContext{
					Profile:   strings.TrimSpace(entry.Profile),
					Source:    "active_context",
					ContextID: strings.TrimSpace(entry.LeaseID),
					CWD:       strings.TrimSpace(entry.CWD),
					CWDScope:  strings.TrimSpace(entry.CWDScope),
				}
			}
		}
		if strings.TrimSpace(ctxFile.DefaultProfile) != "" {
			return resolvedProfileContext{Profile: strings.TrimSpace(ctxFile.DefaultProfile), Source: "default_context"}
		}
	}
	return resolvedProfileContext{Profile: g.defaultProfile(), Source: "default_profile"}
}

func (g *Gateway) activeProfileContextEntry(peer string) (profileContextPeer, bool) {
	ctxFile, err := g.readProfileContext()
	if err != nil {
		return profileContextPeer{}, false
	}
	entry, ok := ctxFile.Peers[strings.TrimSpace(peer)]
	if !ok || strings.TrimSpace(entry.Profile) == "" {
		return profileContextPeer{}, false
	}
	if !entry.ExpiresAt.IsZero() && !time.Now().UTC().Before(entry.ExpiresAt) {
		return profileContextPeer{}, false
	}
	return entry, true
}

func profileContextCWDAllowed(requested, leased, scope string) bool {
	requested = strings.TrimSpace(requested)
	leased = strings.TrimSpace(leased)
	if requested == "" || leased == "" {
		return true
	}
	requested = filepath.Clean(requested)
	leased = filepath.Clean(leased)
	if strings.EqualFold(strings.TrimSpace(scope), "recursive") {
		cmpReq, cmpLease := requested, leased
		if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
			cmpReq, cmpLease = strings.ToLower(cmpReq), strings.ToLower(cmpLease)
		}
		return cmpReq == cmpLease || strings.HasPrefix(cmpReq, cmpLease+string(filepath.Separator))
	}
	return strings.EqualFold(requested, leased)
}

func (g *Gateway) profileContextPath() string {
	return filepath.Join(g.Config.Node.DataDir, "profile-context.json")
}

func (g *Gateway) configPath() string {
	return firstNonEmpty(g.ConfigPath, config.DefaultPath())
}

// reloadConfig re-reads config from disk and applies it to this gateway and to
// the policy engine.
//
// The engine is reloaded in place rather than replaced: the daemon builds one
// engine and shares it with the mesh RPC server, which is what enforces inbound
// peer policy. Assigning a fresh engine to g.Policy would leave the mesh server
// evaluating the old config, so a peer repair would report success while the
// daemon kept rejecting the peer.
func (g *Gateway) reloadConfig() error {
	cfg, err := config.Load(g.configPath())
	if err != nil {
		return err
	}
	g.Config = cfg
	if g.Policy != nil {
		g.Policy.Reload(cfg)
	} else {
		g.Policy = policy.New(cfg)
	}
	return nil
}

func (g *Gateway) readProfileContext() (profileContextFile, error) {
	out := profileContextFile{DefaultProfile: g.defaultProfile(), Peers: map[string]profileContextPeer{}}
	body, err := os.ReadFile(g.profileContextPath())
	if errors.Is(err, os.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return profileContextFile{}, err
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return profileContextFile{}, err
	}
	if out.DefaultProfile == "" {
		out.DefaultProfile = g.defaultProfile()
	}
	if out.Peers == nil {
		out.Peers = map[string]profileContextPeer{}
	}
	return out, nil
}

func (g *Gateway) writeProfileContext(ctxFile profileContextFile) error {
	if ctxFile.DefaultProfile == "" {
		ctxFile.DefaultProfile = g.defaultProfile()
	}
	if ctxFile.Peers == nil {
		ctxFile.Peers = map[string]profileContextPeer{}
	}
	if err := os.MkdirAll(g.Config.Node.DataDir, 0o700); err != nil {
		return err
	}
	body, err := json.MarshalIndent(ctxFile, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(g.profileContextPath(), body, 0o600)
}

func (g *Gateway) defaultProfile() string {
	if _, ok := g.Config.Profiles[profiledefs.Query]; ok {
		return profiledefs.Query
	}
	var names []string
	for name := range g.Config.Runners {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if profile := strings.TrimSpace(g.Config.Runners[name].DefaultProfile); profile != "" {
			return profile
		}
	}
	return "dev"
}

func contains(values []string, needle string) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
}

func uniqueNonEmpty(values []string) []string {
	var out []string
	seen := map[string]struct{}{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func (g *Gateway) record(ctx context.Context, event audit.Event) {
	written, err := g.Audit.Append(event)
	if err == nil {
		_ = g.Store.RecordAuditEvent(ctx, written)
	}
}

func (g *Gateway) hardenLocal(next http.Handler) http.Handler {
	return hardenLocalWithLookup(next, g.Config.Gateway.HostToken, g.provenanceFromTaskToken)
}

func hardenLocal(next http.Handler, hostToken string) http.Handler {
	return hardenLocalWithLookup(next, hostToken, nil)
}

func hardenLocalWithLookup(next http.Handler, hostToken string, lookup func(string) (gatewayProvenance, error)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Forwarded-For") != "" || r.Header.Get("X-Forwarded-Host") != "" || r.Header.Get("X-Real-IP") != "" {
			writeToolErrorStatus(w, http.StatusForbidden, task.ErrorPeerUnauthorized, "forwarded identity headers are not accepted")
			return
		}
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		host = strings.Trim(host, "[]")
		if host != "localhost" && host != "127.0.0.1" && host != "::1" {
			writeToolErrorStatus(w, http.StatusForbidden, task.ErrorPeerUnauthorized, "gateway accepts loopback Host only")
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" {
			parsed, err := url.Parse(origin)
			if err != nil {
				writeToolErrorStatus(w, http.StatusForbidden, task.ErrorPeerUnauthorized, "invalid Origin header")
				return
			}
			originHost := parsed.Hostname()
			if originHost != "localhost" && originHost != "127.0.0.1" && originHost != "::1" {
				writeToolErrorStatus(w, http.StatusForbidden, task.ErrorPeerUnauthorized, "gateway accepts loopback Origin only")
				return
			}
		}
		// Liveness probe: GET /health exposes nothing sensitive (just {status:ok})
		// and must answer unauthenticated so onboarding/monitoring can confirm the
		// daemon is up without holding the gateway host token. It stays behind the
		// loopback/forwarded-header hardening above.
		if r.Method == http.MethodGet && r.URL.Path == "/health" {
			next.ServeHTTP(w, r)
			return
		}
		token := requestHostToken(r)
		if hostToken != "" {
			if validHostToken(r, hostToken) {
				next.ServeHTTP(w, r)
				return
			}
			if lookup != nil && token != "" {
				if prov, err := lookup(token); err == nil {
					next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), provenanceContextKey{}, prov)))
					return
				}
			}
			writeToolErrorStatus(w, http.StatusForbidden, task.ErrorPeerUnauthorized, "missing or invalid gateway host token")
			return
		}
		if lookup != nil && token != "" {
			prov, err := lookup(token)
			if err != nil {
				writeToolErrorStatus(w, http.StatusForbidden, task.ErrorPeerUnauthorized, "invalid task token")
				return
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), provenanceContextKey{}, prov)))
			return
		}
		// Fail closed: with no configured host token and no valid task token, the
		// gateway has no way to authenticate the caller. Refuse rather than serve
		// tool calls unauthenticated to any local process. A correctly onboarded
		// node always has a host token (the wizard mints one); reaching here means
		// a hand-edited or pre-host-token config, which must not run open.
		writeToolErrorStatus(w, http.StatusForbidden, task.ErrorPeerUnauthorized, "gateway host token is not configured; refusing unauthenticated request")
		return
	})
}

func validHostToken(r *http.Request, hostToken string) bool {
	return subtle.ConstantTimeCompare([]byte(requestHostToken(r)), []byte(hostToken)) == 1
}

func requestHostToken(r *http.Request) string {
	if token := strings.TrimSpace(r.Header.Get("X-Cmesh-Host-Token")); token != "" {
		return token
	}
	auth := r.Header.Get("Authorization")
	if strings.HasPrefix(auth, "Bearer ") {
		return strings.TrimSpace(strings.TrimPrefix(auth, "Bearer "))
	}
	return ""
}

func decodeTool(w http.ResponseWriter, r *http.Request, out any) bool {
	defer r.Body.Close()
	if err := json.NewDecoder(r.Body).Decode(out); err != nil {
		writeToolErrorStatus(w, http.StatusBadRequest, task.ErrorWorkerFailed, "invalid JSON: "+err.Error())
		return false
	}
	return true
}

func writeTool(w http.ResponseWriter, resp ToolResponse) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func writeToolError(w http.ResponseWriter, code task.ErrorCode, detail string) {
	writeToolErrorStatus(w, http.StatusOK, code, detail)
}

func writeToolErrorStatus(w http.ResponseWriter, status int, code task.ErrorCode, detail string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(ToolResponse{OK: false, Error: &ToolError{Code: code, Detail: detail}})
}

func writeRPC(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

func toolFailure(code task.ErrorCode, detail string) (ToolResponse, error) {
	resp := ToolResponse{OK: false, Error: &ToolError{Code: code, Detail: detail}}
	return resp, fmt.Errorf("%s: %s", code, detail)
}

func rpcFailure(fallback task.ErrorCode, err error) (ToolResponse, error) {
	code := rpc.CodeOf(err)
	if code == "" {
		code = fallback
	}
	return toolFailure(code, err.Error())
}

func taskRecipes() []TaskRecipe {
	return []TaskRecipe{
		{
			ID:             "disk_check",
			Title:          "Disk check",
			DefaultProfile: "readonly",
			PromptTemplate: "On this machine, run local read-only commands to report disk usage for {path_or_drive}. Return total, used, free, and percent free. Include the exact command names used and do not edit files.",
			ExpectedEvidence: []string{
				"command_output.exit_code is 0",
				"command_output stdout/stderr contains the runner transcript for the disk check",
				"worker summary cites total, used, free, and percent free",
			},
			OriginChecks: []string{
				"parse numeric disk values from command_output or cited command output",
				"compare against a follow-up only when the numbers look inconsistent",
			},
		},
		{
			ID:             "repo_status",
			Title:          "Repository status",
			DefaultProfile: "readonly",
			PromptTemplate: "In {repo_path}, inspect repository status. Report current branch, HEAD commit, dirty files, untracked files, and whether the branch is ahead or behind its upstream. Do not edit files.",
			ExpectedEvidence: []string{
				"command_output.exit_code is 0",
				"worker summary names branch and HEAD commit",
				"worker summary lists dirty and untracked file counts",
			},
			OriginChecks: []string{
				"check command_output before trusting the summary",
				"ask a follow-up only for surprising dirty or divergent state",
			},
		},
		{
			ID:             "native_build",
			Title:          "Native build",
			DefaultProfile: "dev",
			PromptTemplate: "In {repo_path}, build the project using the native command for this machine: {build_command}. Fix only small, directly related build issues if requested by the origin. Return commands run, result, changed files, and verification steps.",
			ExpectedEvidence: []string{
				"command_output.exit_code reflects the runner result",
				"result envelope includes files_changed and diffstat when edits were made",
				"worker summary lists build commands and status",
			},
			OriginChecks: []string{
				"review files_changed before applying or merging work",
				"rerun the relevant build locally or on the target peer when risk is high",
			},
		},
		{
			ID:             "service_log_summary",
			Title:          "Service log summary",
			DefaultProfile: "readonly",
			PromptTemplate: "On this machine, inspect recent logs for service {service_name}. Summarize failures, timestamps, likely cause, and the exact log commands used. Do not restart services or edit files.",
			ExpectedEvidence: []string{
				"command_output.exit_code is 0 unless log access failed",
				"worker summary cites concrete timestamps or log lines",
				"worker summary names the log source inspected",
			},
			OriginChecks: []string{
				"treat summarized causes as hypotheses until verified",
				"fetch transcript or ask for narrower logs before remediation",
			},
		},
		{
			ID:             "test_runner",
			Title:          "Test runner",
			DefaultProfile: "dev",
			PromptTemplate: "In {repo_path}, run the target test command: {test_command}. If it fails, diagnose and fix only directly related issues when allowed. Return commands run, failing output, changed files, and verification status.",
			ExpectedEvidence: []string{
				"command_output.exit_code reflects the runner result",
				"worker summary lists test command and status",
				"result envelope includes files_changed and diffstat when edits were made",
			},
			OriginChecks: []string{
				"rerun the exact test command before trusting a fix",
				"inspect command_output and files_changed before follow-up delegation",
			},
		},
		{
			ID:             "branch_handoff",
			Title:          "Branch handoff",
			DefaultProfile: "dev",
			PromptTemplate: "In {repo_path}, continue work on the requested branch or task. Keep edits scoped to {scope}. Commit only if requested. Return branch, base commit, head commit, diffstat, files changed, tests run, and any artifact refs.",
			ExpectedEvidence: []string{
				"result envelope includes branch, base_commit, and head_commit when git work was available",
				"result envelope includes diffstat and files_changed for edits",
				"worker summary lists tests or explains why none ran",
			},
			OriginChecks: []string{
				"fetch git-bundle artifacts or inspect the remote branch before merging",
				"verify diffstat and tests against the requested scope",
			},
		},
	}
}

func mcpTools() []map[string]any {
	return []map[string]any{
		{
			"name":        "doctor",
			"description": "Check local cmesh setup readiness: config, Tailscale identity/reachability, runners, and MCP gateway details.",
			"inputSchema": objectSchema(nil, nil),
		},
		{
			"name":        "init_status",
			"description": "Inspect first-run setup status, detected runners, local Tailscale identity, and cmesh-capable tailnet peers.",
			"inputSchema": objectSchema(nil, nil),
		},
		{
			"name":        "detect_runners",
			"description": "Detect supported local coding-agent runners such as Claude Code and Codex.",
			"inputSchema": objectSchema(nil, nil),
		},
		{
			"name":        "task_catalog",
			"description": "List bounded delegation recipes such as disk checks, repo status, native builds, service log summaries, test runs, and branch handoff.",
			"inputSchema": objectSchema(nil, nil),
		},
		{
			"name":        "profile_templates",
			"description": "List built-in cmesh profile templates. The default query template is enforced by the builtin probe runner: no shell, no writes, no arbitrary agent execution.",
			"inputSchema": objectSchema(nil, nil),
		},
		{
			"name":        "profile_guide",
			"description": "Explain every cmesh profile in plain language, including exact powers, enforcement strength, safer alternatives, and why query is the default paved road.",
			"inputSchema": objectSchema(nil, nil),
		},
		{
			"name":        "profile_recommend",
			"description": "Recommend the least-powerful cmesh profile for a user intent and explain consent, TTL, safer alternative, and first tool to use.",
			"inputSchema": objectSchema(map[string]any{
				"intent": stringSchema("User intent, such as check disk, query battery, inventory installed apps, open a file, edit code, restart service, or inspect repo."),
			}, []string{"intent"}),
		},
		{
			"name":        "profile_generate",
			"description": "Generate a built-in profile template preview and TOML snippet without writing config.",
			"inputSchema": objectSchema(map[string]any{
				"template":     stringSchema("Built-in template name such as query, query-shell, readonly-agent, or repo-dev."),
				"name":         stringSchema("Optional generated profile name override."),
				"runner":       stringSchema("Optional runner override. Required only when previewing agent-backed templates without a default runner."),
				"capabilities": arraySchema(stringSchema("Optional capability override list.")),
				"os":           stringSchema("Optional shell policy OS filter: windows, darwin, or linux."),
			}, nil),
		},
		{
			"name":        "profile_validate",
			"description": "Validate one profile capability contract and reject unsafe contradictions such as writes=deny with edit_repo.",
			"inputSchema": objectSchema(map[string]any{
				"name":                   stringSchema("Profile name used in validation messages."),
				"runner":                 stringSchema("Runner name."),
				"capabilities":           arraySchema(stringSchema("Capability names.")),
				"claims":                 arraySchema(stringSchema("Compatibility alias for capabilities.")),
				"writes":                 stringSchema("Write mode: deny, repo_only, or allow."),
				"shell":                  stringSchema("Shell mode: deny, allowlisted, or allow."),
				"network":                map[string]any{"type": "boolean"},
				"git_push":               map[string]any{"type": "boolean"},
				"require_explicit":       map[string]any{"type": "boolean"},
				"require_lease":          map[string]any{"type": "boolean"},
				"enforcement_confidence": stringSchema("Enforcement confidence, such as high or medium."),
			}, []string{"runner"}),
		},
		{
			"name":        "profile_describe",
			"description": "Describe one configured profile, including its effective capabilities and lease/explicit requirements.",
			"inputSchema": objectSchema(map[string]any{
				"profile": stringSchema("Configured profile name."),
			}, []string{"profile"}),
		},
		{
			"name":        "profile_current",
			"description": "Show the local gateway's active profile context. Empty delegate and query_peer profile requests resolve from this context and default to query; use inspect_peer for open-ended read-only questions instead of inventing query_peer kinds.",
			"inputSchema": objectSchema(nil, nil),
		},
		{
			"name":        "profile_configure",
			"description": "Preview or add a built-in profile to the local gateway config, such as repo-dev bound to codex for a peer. Preserves existing config and hot-reloads the gateway after confirmed=true.",
			"inputSchema": objectSchema(map[string]any{
				"template":      stringSchema("Built-in template name such as repo-dev, readonly-agent, query-shell, desktop-control, or system-change. Defaults to repo-dev."),
				"name":          stringSchema("Optional generated profile name override."),
				"runner":        stringSchema("Runner to bind agent-backed profiles to, such as codex or claude-code."),
				"peer":          stringSchema("Optional peer name to allow on this profile."),
				"allowed_peers": arraySchema(stringSchema("Optional peer names allowed to use this profile.")),
				"force":         map[string]any{"type": "boolean"},
				"confirmed":     map[string]any{"type": "boolean", "description": "Set true after user consent to write the local config and hot-reload this gateway."},
			}, nil),
		},
		{
			"name":        "profile_use",
			"description": "Set the active profile for a configured peer. The worker still enforces its own policy and may deny the requested profile.",
			"inputSchema": objectSchema(map[string]any{
				"peer":    stringSchema("Configured peer name."),
				"profile": stringSchema("Local profile to request by default for this peer."),
				"cwd":     stringSchema("Optional cwd scope note for the active profile context."),
				"ttl":     stringSchema("Optional lease TTL such as 30m. Required for profiles with require_lease=true."),
				"profile_acknowledgement": objectSchema(map[string]any{
					"profile":           stringSchema("Elevated profile being acknowledged, such as repo-dev, desktop-control, or system-change."),
					"writes":            stringSchema("Acknowledged write mode, such as repo_only or allow."),
					"shell":             stringSchema("Acknowledged shell mode, such as allow."),
					"network":           map[string]any{"type": "boolean"},
					"ttl":               stringSchema("Time-limited lease TTL, such as 30m."),
					"allowed_cwd_scope": stringSchema("Human-readable allowed cwd scope the user approved."),
				}, []string{"profile", "writes", "shell", "network", "ttl", "allowed_cwd_scope"}),
			}, []string{"peer", "profile"}),
		},
		{
			"name":        "profile_reset",
			"description": "Clear active profile context for one peer, or all peers when peer is omitted.",
			"inputSchema": objectSchema(map[string]any{
				"peer": stringSchema("Optional configured peer name to reset."),
			}, nil),
		},
		{
			"name":        "repair_peers",
			"description": "Diagnose and fix wrong, stale, or broken [peers.*] entries in this node's cmesh config — use when the user says a peer entry looks wrong, a peer won't authenticate (peer_unauthorized), or asks to fix/reconcile/repair their peers. Checks every configured peer against its authoritative tailscale whois identity, reports a per-peer diff, and (with confirmed=true) rewrites mismatched entries, prunes stale duplicates, and reconciles allowlists — no manual config editing. Trust posture is preserved; conflicts and unreachable peers are left unchanged. The daemon must be restarted afterward to load the repaired config.",
			"inputSchema": objectSchema(map[string]any{
				"confirmed": map[string]any{"type": "boolean", "description": "Omit (or false) for a per-peer report that changes nothing; set true to apply the repairs after the user consents."},
			}, nil),
		},
		{
			"name":        "list_tailnet_peers",
			"description": "List Tailscale peer candidates and probe which ones appear to expose cmesh.",
			"inputSchema": objectSchema(nil, nil),
		},
		{
			"name":        "suggest_config",
			"description": "Generate an opinionated cmesh config TOML from human setup intent. Does not write files.",
			"inputSchema": objectSchema(map[string]any{
				"role":          stringSchema("Machine role: origin, worker, or both."),
				"allowed_peers": arraySchema(stringSchema("Peer node/name allowed to delegate to this worker.")),
				"allowed_cwds":  arraySchema(stringSchema("Directory glob workers may use on this machine.")),
				"peer_names":    arraySchema(stringSchema("Peer names to configure for outbound delegation.")),
				"runner":        stringSchema("Runner to use: auto, claude-code, or codex."),
				"profiles":      arraySchema(stringSchema("Profiles to expose: query, query-network, query-shell, readonly-agent, repo-dev, desktop-control, system-change, or legacy aliases.")),
			}, nil),
		},
		{
			"name":        "validate_config",
			"description": "Validate a cmesh config file or provided TOML string without starting the daemon.",
			"inputSchema": objectSchema(map[string]any{
				"path": stringSchema("Optional config file path. Defaults to the cmesh config path."),
				"toml": stringSchema("Optional TOML content to validate from a temporary file."),
			}, nil),
		},
		{
			"name":        "register_harness",
			"description": "Register the local cmesh MCP gateway with a local harness. Requires confirmed=true before changing anything.",
			"inputSchema": objectSchema(map[string]any{
				"confirmed": map[string]any{"type": "boolean"},
				"harness":   stringSchema("Harness to register, currently claude-code or manual."),
			}, nil),
		},
		{
			"name":        "list_peers",
			"description": "List configured cmesh peers and their static mesh addresses.",
			"inputSchema": objectSchema(nil, nil),
		},
		{
			"name":        "peer_info",
			"description": "Fetch /v1/hello from a peer to inspect schema versions, runners, profiles, and capabilities.",
			"inputSchema": objectSchema(map[string]any{
				"peer": stringSchema("Configured peer name."),
			}, []string{"peer"}),
		},
		{
			"name":        "test_peer",
			"description": "Run a semantic smoke task against a configured peer and verify the runner receives the prompt and returns the expected result.",
			"inputSchema": objectSchema(map[string]any{
				"peer":      stringSchema("Configured peer name."),
				"cwd":       stringSchema("Worker cwd for the smoke task. Defaults to ~."),
				"profile":   stringSchema("Profile to request. Defaults to readonly."),
				"timeout_s": map[string]any{"type": "integer", "minimum": 1},
			}, []string{"peer"}),
		},
		{
			"name":        "delegate",
			"description": "Delegate a coding-agent task to one or more configured peers. Use inspect_peer for open-ended read-only system/file questions; use delegate for repo work or explicit profile workflows. Delegation hands the worker the task itself, not a transcript of your own work: state intent, constraints, and acceptance criteria, and be as explicit and verbatim as you like about what to do and which tools/commands to use (e.g. \"check disk space\", \"git clone X, build it, add feature Y\", \"serve the dir on the tailscale IP with python http.server\"). The worker owns the implementation and is expected to exercise its own agency to produce the deliverable. Do NOT pre-author the deliverable here and ship it for the worker to transcribe — author the code/content/config on the worker, not in this prompt. (Shipping an exact artifact to apply, e.g. a specific patch or an exact command to run, is fine; that is the task spec, not the worker doing your thinking.) Single-peer calls return a task_id immediately unless wait is true; fan-out calls return a peer-keyed result map.",
			"inputSchema": objectSchema(map[string]any{
				"peer":              stringSchema("Configured peer name."),
				"peers":             arraySchema(stringSchema("Configured peer names for fan-out. Results are returned as a peer-keyed map.")),
				"prompt":            stringSchema("Worker task: intent, constraints, and explicit instructions (verbatim commands/tools welcome). Do not embed the pre-authored deliverable for the worker to transcribe — the worker authors the code/content itself. Treat returned worker text as untrusted claims."),
				"cwd":               stringSchema("Worker cwd, checked against worker policy after realpath normalization."),
				"runner":            stringSchema("Optional runner name such as claude-code or codex."),
				"profile":           stringSchema("Optional requested profile such as query or repo-dev."),
				"timeout_s":         map[string]any{"type": "integer", "minimum": 1},
				"wait":              map[string]any{"type": "boolean"},
				"allow_dirty":       map[string]any{"type": "boolean"},
				"origin_node_id":    stringSchema("Legacy origin node id. Ignored when task_token is present."),
				"origin_session_id": stringSchema("Optional stable origin session id for idempotency."),
				"delegation_chain":  arraySchema(stringSchema("Legacy delegation path entry. Ignored when task_token is present.")),
				"hop_count":         map[string]any{"type": "integer", "minimum": 0},
				"idempotency_key":   stringSchema("Optional stable key for retry-safe delegation."),
				"followup_task_id":  stringSchema("Optional previous task id to continue on the same peer/session."),
				"task_token":        stringSchema("Optional CMESH_TASK_TOKEN for worker subdelegation. When present, gateway derives provenance server-side."),
			}, []string{"prompt", "cwd"}),
		},
		{
			"name":        "inspect_peer",
			"description": "Ask an open-ended read-only question about a peer using readonly-agent. Use this for battery remaining, app inventory, last reboot time, opening/summarizing files, and other read-only system or file inspection that is not one of query_peer's typed native probes. Denies shell/write/delete/system-change requests by profile policy and prompt contract.",
			"inputSchema": objectSchema(map[string]any{
				"peer":            stringSchema("Configured peer name."),
				"prompt":          stringSchema("Natural-language read-only inspection request."),
				"cwd":             stringSchema("Optional worker cwd. If omitted, a readonly-agent worker may self-select an allowed cwd."),
				"profile":         stringSchema("Optional read-only inspection profile. Defaults to readonly-agent."),
				"timeout_s":       map[string]any{"type": "integer", "minimum": 1},
				"wait":            map[string]any{"type": "boolean"},
				"idempotency_key": stringSchema("Optional stable key for retry-safe inspection dispatch."),
				"task_token":      stringSchema("Optional CMESH_TASK_TOKEN for worker subdelegation."),
			}, []string{"peer", "prompt"}),
		},
		{
			"name":        "query_peer",
			"description": "Run a deterministic typed cmesh query on a peer through a typed query profile. Supported native kinds are disk_info, system_info, repo_status, and any explicitly configured probe capability such as network_check or allowlisted query-shell capability. Empty profile resolves from the active profile context, then defaults to query. For battery remaining, app inventory, last reboot time, opening files, or other open-ended read-only questions, use inspect_peer instead. Write requests under query-only must be denied.",
			"inputSchema": objectSchema(map[string]any{
				"peer":            stringSchema("Configured peer name."),
				"kind":            stringSchema("Typed native query kind such as disk_info, system_info, repo_status, or network_check. Do not invent arbitrary kinds; use inspect_peer for open-ended read-only questions."),
				"params":          map[string]any{"type": "object", "additionalProperties": true},
				"cwd":             stringSchema("Optional worker cwd. Required for repo_status; otherwise the worker selects an allowed cwd when omitted."),
				"profile":         stringSchema("Optional profile. Defaults to the active profile context for the peer, then query."),
				"timeout_s":       map[string]any{"type": "integer", "minimum": 1},
				"wait":            map[string]any{"type": "boolean"},
				"idempotency_key": stringSchema("Optional stable key for retry-safe query dispatch."),
				"task_token":      stringSchema("Optional CMESH_TASK_TOKEN for worker subdelegation."),
			}, []string{"peer", "kind"}),
		},
		{
			"name":        "list_tasks",
			"description": "List recent local and delegated tasks so a fresh session can rediscover task ids.",
			"inputSchema": objectSchema(map[string]any{
				"limit": map[string]any{"type": "integer", "minimum": 1},
			}, nil),
		},
		{
			"name":        "task_status",
			"description": "Poll delegated task state, optionally long-polling until terminal or timeout.",
			"inputSchema": objectSchema(map[string]any{
				"task_id":   stringSchema("Origin-local task id."),
				"wait":      map[string]any{"type": "boolean"},
				"timeout_s": map[string]any{"type": "integer", "minimum": 1},
			}, []string{"task_id"}),
		},
		{
			"name":        "task_result",
			"description": "Fetch the bounded result envelope for a terminal delegated task.",
			"inputSchema": objectSchema(map[string]any{"task_id": stringSchema("Origin-local task id.")}, []string{"task_id"}),
		},
		{
			"name":        "task_transcript",
			"description": "Fetch bounded stdout/stderr transcript logs from the worker for a delegated task.",
			"inputSchema": objectSchema(map[string]any{"task_id": stringSchema("Origin-local task id.")}, []string{"task_id"}),
		},
		{
			"name":        "task_artifact",
			"description": "Fetch a worker artifact such as a git bundle and save it under the local cmesh data directory.",
			"inputSchema": objectSchema(map[string]any{
				"task_id": stringSchema("Origin-local task id."),
				"kind":    stringSchema("Artifact kind. Currently git-bundle."),
			}, []string{"task_id"}),
		},
		{
			"name":        "cancel",
			"description": "Cancel a delegated task.",
			"inputSchema": objectSchema(map[string]any{
				"task_id": stringSchema("Origin-local task id."),
				"reason":  stringSchema("Optional cancellation reason."),
			}, []string{"task_id"}),
		},
	}
}

func objectSchema(properties map[string]any, required []string) map[string]any {
	if properties == nil {
		properties = map[string]any{}
	}
	if required == nil {
		required = []string{}
	}
	return map[string]any{
		"type":                 "object",
		"properties":           properties,
		"required":             required,
		"additionalProperties": false,
	}
}

func arraySchema(items map[string]any) map[string]any {
	return map[string]any{"type": "array", "items": items}
}

func stringSchema(description string) map[string]any {
	return map[string]any{"type": "string", "description": description}
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
