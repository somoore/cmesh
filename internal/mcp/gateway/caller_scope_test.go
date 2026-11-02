package gateway

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"cmesh/internal/task"
)

func taskTokenContext() context.Context {
	return context.WithValue(context.Background(), provenanceContextKey{}, gatewayProvenance{
		TaskID:          "tsk_a",
		OriginNodeID:    "origin",
		OriginSessionID: "gateway-default",
	})
}

// A per-task token is held by an untrusted worker runner. It may use the task
// channel, its own delegated tasks, and subdelegation -- nothing that mutates
// host config/peers/leases, registers harnesses, or probes local paths.
func TestTaskTokenCannotCallHostTools(t *testing.T) {
	g := newTestGateway(t)
	configPath := filepath.Join(t.TempDir(), "config.toml")
	body := "[node]\nid = \"origin\"\nname = \"origin\"\nmesh_port = 7778\nmcp_port = 7777\n"
	if err := os.WriteFile(configPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	g.ConfigPath = configPath

	hostOnly := []string{
		"profile_configure", "profile_use", "profile_reset", "profile_generate",
		"repair_peers", "register_harness", "validate_config", "suggest_config",
		"doctor", "init_status", "detect_runners", "list_tailnet_peers",
		"list_peers", "peer_info", "test_peer", "handoff", "task_send",
		"task_messages", "workstream_status", "task_catalog", "profile_templates",
		"profile_guide", "profile_recommend", "profile_describe", "profile_current",
	}
	for _, name := range hostOnly {
		resp, _ := g.callTool(taskTokenContext(), name, json.RawMessage(`{}`))
		if resp.OK || resp.Error == nil || resp.Error.Code != task.ErrorPeerUnauthorized {
			t.Errorf("%s with a task token = %+v, want peer_unauthorized", name, resp)
		}
	}

	after, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != body {
		t.Fatalf("a task-token tool call changed the host config:\n%s", after)
	}
}

func TestTaskTokenAllowlistKeepsWorkerFlow(t *testing.T) {
	allowed := []string{
		"task_post", "task_ask", "task_status", "task_result", "task_transcript",
		"task_events", "task_artifact", "cancel", "list_tasks", "delegate",
		"query_peer", "inspect_peer",
	}
	for _, name := range allowed {
		if !taskTokenToolAllowed(name) {
			t.Errorf("%s must stay available to a worker task token", name)
		}
	}
	for _, name := range []string{"profile_configure", "repair_peers", "register_harness", "validate_config", "profile_use", "profile_reset"} {
		if taskTokenToolAllowed(name) {
			t.Errorf("%s must never be available to a worker task token", name)
		}
	}
}

// tools/list must not advertise host-mutating tools to a task token: an
// allowlist that only denies at call time still teaches an agent to try them.
func TestToolsListIsCallerScoped(t *testing.T) {
	g := newTestGateway(t)
	params, _ := json.Marshal(map[string]any{})
	req := rpcRequest{JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "tools/list", Params: params}

	resp, _ := g.handleRPC(taskTokenContext(), req)
	result, _ := resp.Result.(map[string]any)
	tools, _ := result["tools"].([]map[string]any)
	if len(tools) == 0 {
		t.Fatalf("task-token tools/list returned no tools: %+v", resp)
	}
	for _, tool := range tools {
		name, _ := tool["name"].(string)
		if !taskTokenToolAllowed(name) {
			t.Fatalf("task-token tools/list exposed host tool %q", name)
		}
	}

	hostResp, _ := g.handleRPC(context.Background(), req)
	hostResult, _ := hostResp.Result.(map[string]any)
	hostTools, _ := hostResult["tools"].([]map[string]any)
	if len(hostTools) <= len(tools) {
		t.Fatalf("host token should see more tools than a task token (%d vs %d)", len(hostTools), len(tools))
	}
}
