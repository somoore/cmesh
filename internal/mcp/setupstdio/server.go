package setupstdio

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"

	"cmesh/internal/config"
	"cmesh/internal/onboard"
	profiledefs "cmesh/internal/profile"
	"cmesh/internal/version"
)

type Server struct {
	In  io.Reader
	Out io.Writer

	mu        sync.Mutex
	frameMode stdioFrameMode
}

type stdioFrameMode int

const (
	stdioFrameContentLength stdioFrameMode = iota
	stdioFrameNewline
)

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

type ToolResponse struct {
	OK     bool       `json:"ok"`
	Result any        `json:"result,omitempty"`
	Error  *ToolError `json:"error,omitempty"`
}

type ToolError struct {
	Code   string `json:"code"`
	Detail string `json:"detail"`
}

func (s *Server) Serve(ctx context.Context) error {
	if s.In == nil {
		s.In = os.Stdin
	}
	if s.Out == nil {
		s.Out = os.Stdout
	}
	reader := bufio.NewReader(s.In)
	for {
		body, mode, err := readFrame(reader)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		s.setFrameMode(mode)
		var req rpcRequest
		if err := json.Unmarshal(body, &req); err != nil {
			_ = s.write(ctx, rpcResponse{JSONRPC: "2.0", Error: &rpcError{Code: -32700, Message: "parse error: " + err.Error()}})
			continue
		}
		resp, ok := s.handle(ctx, req)
		if !ok {
			continue
		}
		if err := s.write(ctx, resp); err != nil {
			return err
		}
	}
}

func (s *Server) handle(ctx context.Context, req rpcRequest) (rpcResponse, bool) {
	if len(req.ID) == 0 {
		return rpcResponse{}, false
	}
	resp := rpcResponse{JSONRPC: "2.0", ID: req.ID}
	switch req.Method {
	case "initialize":
		resp.Result = map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities": map[string]any{
				"tools":   map[string]any{"listChanged": false},
				"prompts": map[string]any{"listChanged": false},
			},
			"serverInfo": map[string]string{"name": "cmesh-setup", "version": version.Version},
		}
	case "tools/list":
		resp.Result = map[string]any{"tools": tools()}
	case "prompts/list":
		resp.Result = map[string]any{"prompts": prompts()}
	case "prompts/get":
		var params struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(req.Params, &params); err != nil {
			resp.Error = &rpcError{Code: -32602, Message: "invalid prompts/get params: " + err.Error()}
			return resp, true
		}
		if params.Name != setupCmeshPromptName {
			resp.Error = &rpcError{Code: -32602, Message: "unknown prompt: " + params.Name}
			return resp, true
		}
		resp.Result = map[string]any{
			"description": "Set up cmesh on this machine and link it with another node.",
			"messages":    setupCmeshPromptMessages(),
		}
	case "tools/call":
		var params struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &params); err != nil {
			resp.Error = &rpcError{Code: -32602, Message: "invalid tools/call params: " + err.Error()}
			return resp, true
		}
		result := s.CallTool(ctx, params.Name, params.Arguments)
		text, _ := json.Marshal(result)
		resp.Result = map[string]any{
			"content": []map[string]string{{"type": "text", "text": string(text)}},
			"isError": !result.OK,
		}
	default:
		resp.Error = &rpcError{Code: -32601, Message: "method not found: " + req.Method}
	}
	return resp, true
}

func (s *Server) CallTool(ctx context.Context, name string, args json.RawMessage) ToolResponse {
	switch name {
	case "doctor":
		status := onboard.Detect(ctx, config.DefaultPath(), false)
		return ToolResponse{OK: status.ConfigOK && status.Machine.TailscaleOK, Result: status}
	case "init_status":
		return ToolResponse{OK: true, Result: onboard.Detect(ctx, config.DefaultPath(), true)}
	case "detect_runners":
		return ToolResponse{OK: true, Result: onboard.DetectRunners(ctx)}
	case "profile_templates":
		return ToolResponse{OK: true, Result: map[string]any{"templates": profiledefs.Builtins()}}
	case "profile_guide":
		return ToolResponse{OK: true, Result: profiledefs.Guide()}
	case "profile_recommend":
		var req struct {
			Intent string `json:"intent"`
		}
		if err := json.Unmarshal(args, &req); err != nil {
			return failure("invalid_arguments", err.Error())
		}
		return ToolResponse{OK: true, Result: profiledefs.Recommend(req.Intent)}
	case "profile_generate":
		var req profileGenerateRequest
		if err := json.Unmarshal(args, &req); err != nil {
			return failure("invalid_arguments", err.Error())
		}
		return profileGenerate(req)
	case "profile_validate":
		var req profileValidateRequest
		if err := json.Unmarshal(args, &req); err != nil {
			return failure("invalid_arguments", err.Error())
		}
		return profileValidate(req)
	case "list_tailnet_peers":
		peers, err := onboard.TailnetPeers(ctx, config.DefaultMeshPort, true)
		if err != nil {
			return failure("tailnet_peers_failed", err.Error())
		}
		return ToolResponse{OK: true, Result: peers}
	case "suggest_config":
		opts, err := decodeInitOptions(args)
		if err != nil {
			return failure("invalid_arguments", err.Error())
		}
		body, err := suggestedConfig(ctx, opts)
		if err != nil {
			return failure("suggest_config_failed", err.Error())
		}
		return ToolResponse{OK: true, Result: map[string]string{"path": config.DefaultPath(), "toml": body}}
	case "profile_preview_config":
		opts, err := decodeInitOptions(args)
		if err != nil {
			return failure("invalid_arguments", err.Error())
		}
		body, err := suggestedConfig(ctx, opts)
		if err != nil {
			return failure("profile_preview_config_failed", err.Error())
		}
		return ToolResponse{OK: true, Result: map[string]string{"path": config.DefaultPath(), "toml": body}}
	case "validate_config":
		var req struct {
			Path string `json:"path,omitempty"`
			TOML string `json:"toml,omitempty"`
		}
		if err := json.Unmarshal(args, &req); err != nil {
			return failure("invalid_arguments", err.Error())
		}
		path, cleanup, err := pathForValidation(req.Path, req.TOML)
		if err != nil {
			return failure("validate_config_failed", err.Error())
		}
		defer cleanup()
		if err := onboard.ValidateConfig(path); err != nil {
			return failure("invalid_config", err.Error())
		}
		return ToolResponse{OK: true, Result: map[string]string{"path": path, "status": "valid"}}
	case "write_config":
		var req writeConfigRequest
		if err := json.Unmarshal(args, &req); err != nil {
			return failure("invalid_arguments", err.Error())
		}
		if !req.Confirmed {
			result := map[string]any{
				"requires_confirmation": true,
				"message":               "Call write_config again with confirmed=true to write the cmesh config file.",
				"path":                  firstNonEmpty(req.Path, config.DefaultPath()),
			}
			addProfileGuidance(result, req.options(), req.ProfileAcknowledgements)
			return ToolResponse{OK: true, Result: result}
		}
		if problems := profiledefs.AcknowledgementProblems(profilesForWriteRequest(req), req.ProfileAcknowledgements); len(problems) > 0 {
			return failure("profile_acknowledgement_required", strings.Join(problems, "; "))
		}
		body := req.TOML
		var err error
		if strings.TrimSpace(body) == "" {
			body, err = suggestedConfig(ctx, req.options())
			if err != nil {
				return failure("suggest_config_failed", err.Error())
			}
		}
		path := firstNonEmpty(req.Path, config.DefaultPath())
		if err := onboard.WriteConfig(path, body, req.Force); err != nil {
			return failure("write_config_failed", err.Error())
		}
		if err := onboard.ValidateConfig(path); err != nil {
			return failure("invalid_written_config", err.Error())
		}
		return ToolResponse{OK: true, Result: map[string]string{
			"path":        path,
			"status":      "written",
			"next_step":   "Run cmesh up, or register http://127.0.0.1:7777/mcp after the daemon is running.",
			"gateway_url": "http://127.0.0.1:7777/mcp",
		}}
	case "profile_write_config":
		var req writeConfigRequest
		if err := json.Unmarshal(args, &req); err != nil {
			return failure("invalid_arguments", err.Error())
		}
		if !req.Confirmed {
			result := map[string]any{
				"requires_confirmation": true,
				"message":               "Call profile_write_config again with confirmed=true to write the cmesh config file.",
				"path":                  firstNonEmpty(req.Path, config.DefaultPath()),
			}
			addProfileGuidance(result, req.options(), req.ProfileAcknowledgements)
			return ToolResponse{OK: true, Result: result}
		}
		if problems := profiledefs.AcknowledgementProblems(profilesForWriteRequest(req), req.ProfileAcknowledgements); len(problems) > 0 {
			return failure("profile_acknowledgement_required", strings.Join(problems, "; "))
		}
		body := req.TOML
		var err error
		if strings.TrimSpace(body) == "" {
			body, err = suggestedConfig(ctx, req.options())
			if err != nil {
				return failure("profile_preview_config_failed", err.Error())
			}
		}
		path := firstNonEmpty(req.Path, config.DefaultPath())
		if err := onboard.WriteConfig(path, body, req.Force); err != nil {
			return failure("profile_write_config_failed", err.Error())
		}
		if err := onboard.ValidateConfig(path); err != nil {
			return failure("invalid_written_config", err.Error())
		}
		return ToolResponse{OK: true, Result: map[string]string{
			"path":        path,
			"status":      "written",
			"next_step":   "Run cmesh up, or register http://127.0.0.1:7777/mcp after the daemon is running.",
			"gateway_url": "http://127.0.0.1:7777/mcp",
		}}
	case "gateway_info":
		cfg, err := config.Load(config.DefaultPath())
		if err != nil {
			return ToolResponse{OK: true, Result: map[string]any{
				"configured": false,
				"config":     config.DefaultPath(),
				"next_step":  "Use suggest_config and write_config, or run cmesh init.",
			}}
		}
		return ToolResponse{OK: true, Result: map[string]any{
			"configured":  true,
			"config":      config.DefaultPath(),
			"gateway_url": fmt.Sprintf("http://127.0.0.1:%d/mcp", cfg.Node.MCPPort),
			"run_command": "cmesh up",
		}}
	case "onboarding_status":
		return onboardingStatus(ctx)
	case "configure_node":
		return configureNode(ctx, args)
	case "start_service":
		return startService(ctx, args)
	case "link_peer":
		return linkPeer(ctx, args)
	case "repair_peers":
		return repairPeers(ctx, args)
	case "register_gateway":
		return registerGateway(ctx, args)
	default:
		return failure("unknown_tool", "unknown tool "+name)
	}
}

type writeConfigRequest struct {
	initOptionsRequest
	Path                    string                               `json:"path,omitempty"`
	TOML                    string                               `json:"toml,omitempty"`
	Force                   bool                                 `json:"force,omitempty"`
	Confirmed               bool                                 `json:"confirmed,omitempty"`
	ProfileAcknowledgements []profiledefs.ProfileAcknowledgement `json:"profile_acknowledgements,omitempty"`
}

type profileGenerateRequest struct {
	Template     string   `json:"template"`
	Name         string   `json:"name,omitempty"`
	Runner       string   `json:"runner,omitempty"`
	Capabilities []string `json:"capabilities,omitempty"`
	OS           string   `json:"os,omitempty"`
}

type profileValidateRequest struct {
	Name                  string   `json:"name"`
	Runner                string   `json:"runner"`
	Capabilities          []string `json:"capabilities"`
	Claims                []string `json:"claims,omitempty"`
	Writes                string   `json:"writes,omitempty"`
	Shell                 string   `json:"shell,omitempty"`
	Network               bool     `json:"network,omitempty"`
	GitPush               bool     `json:"git_push,omitempty"`
	RequireExplicit       bool     `json:"require_explicit,omitempty"`
	EnforcementConfidence string   `json:"enforcement_confidence,omitempty"`
}

type initOptionsRequest struct {
	Role         string   `json:"role,omitempty"`
	AllowedPeers []string `json:"allowed_peers,omitempty"`
	AllowedCWDs  []string `json:"allowed_cwds,omitempty"`
	PeerNames    []string `json:"peer_names,omitempty"`
	Runner       string   `json:"runner,omitempty"`
	Profiles     []string `json:"profiles,omitempty"`
}

func profileGenerate(req profileGenerateRequest) ToolResponse {
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
		return failure("profile_generate_failed", err.Error())
	}
	return ToolResponse{OK: true, Result: map[string]any{
		"template": tmpl,
		"toml":     profiledefs.RenderTOML(tmpl),
	}}
}

func profileValidate(req profileValidateRequest) ToolResponse {
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = "custom"
	}
	capabilities := profiledefs.NormalizeCapabilities(req.Capabilities, req.Claims)
	if err := profiledefs.ValidateSemantics(name, strings.TrimSpace(req.Runner), capabilities, req.Writes, req.Shell, req.Network, req.GitPush, req.RequireExplicit, req.EnforcementConfidence); err != nil {
		return failure("profile_denied", err.Error())
	}
	return ToolResponse{OK: true, Result: map[string]any{"status": "valid", "name": name, "capabilities": capabilities}}
}

func (r initOptionsRequest) options() onboard.InitOptions {
	return onboard.InitOptions{
		Role:         r.Role,
		AllowedPeers: r.AllowedPeers,
		AllowedCWDs:  r.AllowedCWDs,
		PeerNames:    r.PeerNames,
		Runner:       r.Runner,
		Profiles:     r.Profiles,
	}
}

func decodeInitOptions(args json.RawMessage) (onboard.InitOptions, error) {
	var req initOptionsRequest
	if len(args) == 0 {
		return onboard.InitOptions{}, nil
	}
	if err := json.Unmarshal(args, &req); err != nil {
		return onboard.InitOptions{}, err
	}
	return req.options(), nil
}

func selectedProfiles(opts onboard.InitOptions) []string {
	if len(opts.Profiles) == 0 {
		return []string{profiledefs.Query}
	}
	return opts.Profiles
}

func profilesForWriteRequest(req writeConfigRequest) []string {
	if profiles := profilesFromTOML(req.TOML); len(profiles) > 0 {
		return profiles
	}
	return selectedProfiles(req.options())
}

func profilesFromTOML(body string) []string {
	var out []string
	seen := map[string]struct{}{}
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "[profiles.") || !strings.HasSuffix(line, "]") {
			continue
		}
		name := strings.TrimSuffix(strings.TrimPrefix(line, "[profiles."), "]")
		if strings.Contains(name, ".") {
			name = strings.SplitN(name, ".", 2)[0]
		}
		name = strings.Trim(name, `"`)
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	return out
}

func addProfileGuidance(result map[string]any, opts onboard.InitOptions, acknowledgements []profiledefs.ProfileAcknowledgement) {
	profiles := selectedProfiles(opts)
	result["profile_guide"] = profiledefs.Guide()
	result["recommended_profiles"] = []profiledefs.Recommendation{
		profiledefs.Recommend("safe first look at a machine"),
		profiledefs.Recommend("read files"),
		profiledefs.Recommend("edit code"),
	}
	result["profile_menu"] = profiledefs.Guide().Profiles
	if needed := profiledefs.ProfilesRequiringAcknowledgement(profiles); len(needed) > 0 {
		result["profile_acknowledgement_required"] = true
		result["profile_acknowledgement_profiles"] = needed
		result["profile_acknowledgement_message"] = "Before enabling repo-dev, desktop-control, or system-change, explain and acknowledge profile, writes, shell, network, TTL, and allowed cwd scope."
		if problems := profiledefs.AcknowledgementProblems(profiles, acknowledgements); len(problems) > 0 {
			result["profile_acknowledgement_missing"] = problems
		}
	}
}

func suggestedConfig(ctx context.Context, opts onboard.InitOptions) (string, error) {
	machine := onboard.DetectMachine(ctx)
	runners := onboard.DetectRunners(ctx)
	peers, _ := onboard.TailnetPeers(ctx, config.DefaultMeshPort, true)
	return onboard.GenerateConfig(opts, machine, runners, peers)
}

func pathForValidation(path, tomlBody string) (string, func(), error) {
	if strings.TrimSpace(tomlBody) == "" {
		if path == "" {
			path = config.DefaultPath()
		}
		return path, func() {}, nil
	}
	tmp, err := os.CreateTemp("", "cmesh-config-*.toml")
	if err != nil {
		return "", func() {}, err
	}
	if _, err := tmp.WriteString(tomlBody); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return "", func() {}, err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return "", func() {}, err
	}
	return tmp.Name(), func() { _ = os.Remove(tmp.Name()) }, nil
}

func readFrame(reader *bufio.Reader) ([]byte, stdioFrameMode, error) {
	contentLength := -1
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return nil, stdioFrameContentLength, err
		}
		line = strings.TrimRight(line, "\r\n")
		if strings.HasPrefix(strings.TrimSpace(line), "{") {
			return []byte(strings.TrimSpace(line)), stdioFrameNewline, nil
		}
		if line == "" {
			break
		}
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(name), "Content-Length") {
			parsed, err := strconv.Atoi(strings.TrimSpace(value))
			if err != nil {
				return nil, stdioFrameContentLength, fmt.Errorf("invalid Content-Length: %w", err)
			}
			contentLength = parsed
		}
	}
	if contentLength < 0 {
		return nil, stdioFrameContentLength, errors.New("missing Content-Length")
	}
	body := make([]byte, contentLength)
	_, err := io.ReadFull(reader, body)
	return body, stdioFrameContentLength, err
}

func (s *Server) write(ctx context.Context, value any) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	body, err := json.Marshal(value)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.frameMode == stdioFrameNewline {
		_, err = s.Out.Write(append(body, '\n'))
		return err
	}
	if _, err := fmt.Fprintf(s.Out, "Content-Length: %d\r\n\r\n", len(body)); err != nil {
		return err
	}
	_, err = s.Out.Write(body)
	return err
}

func (s *Server) setFrameMode(mode stdioFrameMode) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.frameMode = mode
}

func failure(code, detail string) ToolResponse {
	return ToolResponse{OK: false, Error: &ToolError{Code: code, Detail: detail}}
}

func tools() []map[string]any {
	return []map[string]any{
		{
			"name":        "doctor",
			"description": "Check local setup readiness before the daemon is running.",
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
				"intent": stringSchema("User intent, such as check disk, read files, edit code, restart service, or inspect repo."),
			}, []string{"intent"}),
		},
		{
			"name":        "profile_generate",
			"description": "Generate a built-in profile template preview and TOML snippet without writing config.",
			"inputSchema": objectSchema(map[string]any{
				"template":     stringSchema("Built-in template name such as query, query-shell, readonly-agent, or repo-dev."),
				"name":         stringSchema("Optional generated profile name override."),
				"runner":       stringSchema("Optional runner override."),
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
				"enforcement_confidence": stringSchema("Enforcement confidence, such as high or medium."),
			}, []string{"runner"}),
		},
		{
			"name":        "list_tailnet_peers",
			"description": "List Tailscale peer candidates and probe which ones appear to expose cmesh.",
			"inputSchema": objectSchema(nil, nil),
		},
		{
			"name":        "suggest_config",
			"description": "Generate an opinionated cmesh config TOML from human setup intent. Does not write files.",
			"inputSchema": initOptionsSchema(),
		},
		{
			"name":        "profile_preview_config",
			"description": "Preview cmesh config TOML for selected profile templates. Does not write files.",
			"inputSchema": initOptionsSchema(),
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
			"name":        "write_config",
			"description": "Write a cmesh config from provided TOML or generated setup intent. Requires confirmed=true.",
			"inputSchema": objectSchema(map[string]any{
				"path":      stringSchema("Optional config file path. Defaults to the cmesh config path."),
				"toml":      stringSchema("Optional TOML content to write. If omitted, setup intent is used to generate TOML."),
				"force":     map[string]any{"type": "boolean"},
				"confirmed": map[string]any{"type": "boolean"},
				"profile_acknowledgements": arraySchema(objectSchema(map[string]any{
					"profile":           stringSchema("Elevated profile being acknowledged, such as repo-dev, desktop-control, or system-change."),
					"writes":            stringSchema("Acknowledged write mode, such as repo_only or allow."),
					"shell":             stringSchema("Acknowledged shell mode, such as allow."),
					"network":           map[string]any{"type": "boolean"},
					"ttl":               stringSchema("Time-limited lease TTL, such as 30m."),
					"allowed_cwd_scope": stringSchema("Human-readable allowed cwd scope the user approved."),
				}, []string{"profile", "writes", "shell", "network", "ttl", "allowed_cwd_scope"})),
				"role":          stringSchema("Machine role: origin, worker, or both."),
				"allowed_peers": arraySchema(stringSchema("Peer node/name allowed to delegate to this worker.")),
				"allowed_cwds":  arraySchema(stringSchema("Directory glob workers may use on this machine.")),
				"peer_names":    arraySchema(stringSchema("Peer names to configure for outbound delegation.")),
				"runner":        stringSchema("Runner to use: auto, claude-code, or codex."),
				"profiles":      arraySchema(stringSchema("Profiles to expose: query, query-network, query-shell, readonly-agent, repo-dev, desktop-control, system-change, or legacy aliases.")),
			}, nil),
		},
		{
			"name":        "profile_write_config",
			"description": "Write a cmesh config from provided TOML or selected profile templates. Requires confirmed=true.",
			"inputSchema": objectSchema(map[string]any{
				"path":      stringSchema("Optional config file path. Defaults to the cmesh config path."),
				"toml":      stringSchema("Optional TOML content to write. If omitted, setup intent is used to generate TOML."),
				"force":     map[string]any{"type": "boolean"},
				"confirmed": map[string]any{"type": "boolean"},
				"profile_acknowledgements": arraySchema(objectSchema(map[string]any{
					"profile":           stringSchema("Elevated profile being acknowledged, such as repo-dev, desktop-control, or system-change."),
					"writes":            stringSchema("Acknowledged write mode, such as repo_only or allow."),
					"shell":             stringSchema("Acknowledged shell mode, such as allow."),
					"network":           map[string]any{"type": "boolean"},
					"ttl":               stringSchema("Time-limited lease TTL, such as 30m."),
					"allowed_cwd_scope": stringSchema("Human-readable allowed cwd scope the user approved."),
				}, []string{"profile", "writes", "shell", "network", "ttl", "allowed_cwd_scope"})),
				"role":          stringSchema("Machine role: origin, worker, or both."),
				"allowed_peers": arraySchema(stringSchema("Peer node/name allowed to delegate to this worker.")),
				"allowed_cwds":  arraySchema(stringSchema("Directory glob workers may use on this machine.")),
				"peer_names":    arraySchema(stringSchema("Peer names to configure for outbound delegation.")),
				"runner":        stringSchema("Runner to use: auto, claude-code, or codex."),
				"profiles":      arraySchema(stringSchema("Profiles to expose: query, query-network, query-shell, readonly-agent, repo-dev, desktop-control, system-change, or legacy aliases.")),
			}, nil),
		},
		{
			"name":        "gateway_info",
			"description": "Show the post-init local HTTP MCP gateway URL and daemon run command.",
			"inputSchema": objectSchema(nil, nil),
		},
		{
			"name":        "onboarding_status",
			"description": "Drive cmesh onboarding. Call this FIRST and follow next_step. Re-derives setup progress from the environment on every call; safe to call repeatedly and on a second node to continue.",
			"inputSchema": objectSchema(nil, nil),
		},
		{
			"name":        "configure_node",
			"description": "Write this node's cmesh config (auto-filled from detection). Preserves an existing gateway token on re-run. Requires confirmed=true to write.",
			"inputSchema": objectSchema(map[string]any{
				"role":         stringSchema("Machine role: origin, worker, or both."),
				"allowed_cwds": arraySchema(stringSchema("Directory glob workers may use on this machine.")),
				"peer_names":   arraySchema(stringSchema("Peer names to configure for outbound delegation.")),
				"runner":       stringSchema("Runner to use: auto, claude-code, or codex."),
				"profiles":     arraySchema(stringSchema("Profiles to expose: query, query-network, query-shell, readonly-agent, repo-dev, desktop-control, system-change, or legacy aliases.")),
				"profile_acknowledgements": arraySchema(objectSchema(map[string]any{
					"profile":           stringSchema("Elevated profile being acknowledged, such as repo-dev, desktop-control, or system-change."),
					"writes":            stringSchema("Acknowledged write mode, such as repo_only or allow."),
					"shell":             stringSchema("Acknowledged shell mode, such as allow."),
					"network":           map[string]any{"type": "boolean"},
					"ttl":               stringSchema("Time-limited lease TTL, such as 30m."),
					"allowed_cwd_scope": stringSchema("Human-readable allowed cwd scope the user approved."),
				}, []string{"profile", "writes", "shell", "network", "ttl", "allowed_cwd_scope"})),
				"confirmed": map[string]any{"type": "boolean", "description": "Set true to write the config after the user consents (always overwrites an existing config, preserving only the gateway host token)."},
			}, nil),
		},
		{
			"name":        "start_service",
			"description": "Install and start the cmesh background service, then verify the local gateway is healthy. Requires confirmed=true.",
			"inputSchema": objectSchema(map[string]any{
				"confirmed": map[string]any{"type": "boolean", "description": "Set true to install and start the service after the user consents."},
			}, nil),
		},
		{
			"name":        "link_peer",
			"description": "Trust and reach a discovered cmesh peer (reciprocal reachability + outbound allow keyed on the whois-matchable name), then run a bidirectional smoke. Requires confirmed=true.",
			"inputSchema": objectSchema(map[string]any{
				"peer":      stringSchema("Name of the discovered cmesh peer to link."),
				"confirmed": map[string]any{"type": "boolean", "description": "Set true to link the peer after the user consents."},
			}, []string{"peer"}),
		},
		{
			"name":        "repair_peers",
			"description": "Diagnose and fix wrong, stale, or broken [peers.*] entries in this node's cmesh config — use when the user says a peer entry looks wrong, a peer won't authenticate, or asks to fix/reconcile/repair their peers. Checks every configured peer against its authoritative tailscale whois identity, reports a per-peer diff, and (with confirmed=true) rewrites mismatched entries to the whois-matchable identity, prunes stale duplicates, and reconciles allowlists — without the user editing any config. Trust posture is preserved; conflicts and unreachable peers are left unchanged.",
			"inputSchema": objectSchema(map[string]any{
				"confirmed": map[string]any{"type": "boolean", "description": "Omit (or false) to get a per-peer report and change nothing; set true to apply the repairs after the user consents."},
			}, nil),
		},
		{
			"name":        "register_gateway",
			"description": "Point the agent's cmesh MCP at the local HTTP gateway (Claude Code or Codex). Triggers a single end-of-flow session restart. The host token is never echoed. Requires confirmed=true.",
			"inputSchema": objectSchema(map[string]any{
				"harness":   stringSchema("Target agent: claude-code or codex."),
				"confirmed": map[string]any{"type": "boolean", "description": "Set true to register the gateway after the user consents."},
			}, []string{"harness"}),
		},
	}
}

func initOptionsSchema() map[string]any {
	return objectSchema(map[string]any{
		"role":          stringSchema("Machine role: origin, worker, or both."),
		"allowed_peers": arraySchema(stringSchema("Peer node/name allowed to delegate to this worker.")),
		"allowed_cwds":  arraySchema(stringSchema("Directory glob workers may use on this machine.")),
		"peer_names":    arraySchema(stringSchema("Peer names to configure for outbound delegation.")),
		"runner":        stringSchema("Runner to use: auto, claude-code, or codex."),
		"profiles":      arraySchema(stringSchema("Profiles to expose: query, query-network, query-shell, readonly-agent, repo-dev, desktop-control, system-change, or legacy aliases.")),
	}, nil)
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
