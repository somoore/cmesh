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
	"cmesh/internal/version"
)

type Server struct {
	In  io.Reader
	Out io.Writer

	mu sync.Mutex
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
		body, err := readFrame(reader)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
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
				"tools": map[string]any{"listChanged": false},
			},
			"serverInfo": map[string]string{"name": "cmesh-setup", "version": version.Version},
		}
	case "tools/list":
		resp.Result = map[string]any{"tools": tools()}
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
			return ToolResponse{OK: true, Result: map[string]any{
				"requires_confirmation": true,
				"message":               "Call write_config again with confirmed=true to write the cmesh config file.",
				"path":                  firstNonEmpty(req.Path, config.DefaultPath()),
			}}
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
	default:
		return failure("unknown_tool", "unknown tool "+name)
	}
}

type writeConfigRequest struct {
	initOptionsRequest
	Path      string `json:"path,omitempty"`
	TOML      string `json:"toml,omitempty"`
	Force     bool   `json:"force,omitempty"`
	Confirmed bool   `json:"confirmed,omitempty"`
}

type initOptionsRequest struct {
	Role         string   `json:"role,omitempty"`
	AllowedPeers []string `json:"allowed_peers,omitempty"`
	AllowedCWDs  []string `json:"allowed_cwds,omitempty"`
	PeerNames    []string `json:"peer_names,omitempty"`
	Runner       string   `json:"runner,omitempty"`
	Profiles     []string `json:"profiles,omitempty"`
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

func readFrame(reader *bufio.Reader) ([]byte, error) {
	contentLength := -1
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
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
				return nil, fmt.Errorf("invalid Content-Length: %w", err)
			}
			contentLength = parsed
		}
	}
	if contentLength < 0 {
		return nil, errors.New("missing Content-Length")
	}
	body := make([]byte, contentLength)
	_, err := io.ReadFull(reader, body)
	return body, err
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
	if _, err := fmt.Fprintf(s.Out, "Content-Length: %d\r\n\r\n", len(body)); err != nil {
		return err
	}
	_, err = s.Out.Write(body)
	return err
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
				"path":          stringSchema("Optional config file path. Defaults to the cmesh config path."),
				"toml":          stringSchema("Optional TOML content to write. If omitted, setup intent is used to generate TOML."),
				"force":         map[string]any{"type": "boolean"},
				"confirmed":     map[string]any{"type": "boolean"},
				"role":          stringSchema("Machine role: origin, worker, or both."),
				"allowed_peers": arraySchema(stringSchema("Peer node/name allowed to delegate to this worker.")),
				"allowed_cwds":  arraySchema(stringSchema("Directory glob workers may use on this machine.")),
				"peer_names":    arraySchema(stringSchema("Peer names to configure for outbound delegation.")),
				"runner":        stringSchema("Runner to use: auto, claude-code, or codex."),
				"profiles":      arraySchema(stringSchema("Profiles to expose: readonly, dev, network, git_push.")),
			}, nil),
		},
		{
			"name":        "gateway_info",
			"description": "Show the post-init local HTTP MCP gateway URL and daemon run command.",
			"inputSchema": objectSchema(nil, nil),
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
		"profiles":      arraySchema(stringSchema("Profiles to expose: readonly, dev, network, git_push.")),
	}, nil)
}

func objectSchema(properties map[string]any, required []string) map[string]any {
	if properties == nil {
		properties = map[string]any{}
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
