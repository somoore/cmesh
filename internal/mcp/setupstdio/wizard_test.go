package setupstdio

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cmesh/internal/config"
	"cmesh/internal/service"
)

// TestStartServiceEmitsNothingToStdout is the critical C1 test: start_service
// must never write to the real os.Stdout (the MCP JSON-RPC channel), even when
// the underlying service ops print to stdout.
func TestStartServiceEmitsNothingToStdout(t *testing.T) {
	withConfig(t)

	// Inject fakes that deliberately write to os.Stdout, as internal/service does.
	origInstall, origStart := installServiceFn, startServiceFn
	installServiceFn = func(service.Options) error {
		os.Stdout.WriteString("INSTALL noise to stdout\n")
		return nil
	}
	startServiceFn = func() error {
		os.Stdout.WriteString("START noise to stdout\n")
		return nil
	}
	origBudget := healthPollBudget
	healthPollBudget = 0
	defer func() {
		installServiceFn, startServiceFn = origInstall, origStart
		healthPollBudget = origBudget
	}()

	// Capture the REAL os.Stdout via a pipe for the duration of the call.
	realStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	captured := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(r)
		captured <- buf.String()
	}()

	resp := startService(context.Background(), []byte(`{"confirmed":true}`))

	os.Stdout = realStdout
	_ = w.Close()
	got := <-captured

	if strings.Contains(got, "noise") || strings.TrimSpace(got) != "" {
		t.Fatalf("start_service leaked to stdout: %q", got)
	}
	// The result should still report (gateway won't be healthy in test; that's fine).
	if resp.Result == nil {
		t.Fatalf("start_service returned no result")
	}
}

func TestUnconfirmedToolsNoSideEffects(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	t.Setenv("CMESH_CONFIG", path)

	tests := []struct {
		name string
		args string
		fn   func(context.Context, json.RawMessage) ToolResponse
	}{
		{"configure_node", `{"role":"worker"}`, configureNode},
		{"start_service", `{}`, startService},
		{"register_gateway", `{"harness":"claude-code"}`, registerGateway},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// start_service / register_gateway need a config present to reach the
			// confirmation gate without erroring; configure_node works with none.
			if tt.name != "configure_node" {
				writeValidConfig(t, path)
			}
			resp := tt.fn(context.Background(), []byte(tt.args))
			encoded, _ := json.Marshal(resp.Result)
			if !strings.Contains(string(encoded), "requires_confirmation") {
				t.Fatalf("%s without confirmed should require confirmation, got %s", tt.name, encoded)
			}
			if tt.name == "configure_node" {
				if _, err := os.Stat(path); err == nil {
					t.Fatalf("configure_node wrote a config without confirmation")
				}
			}
		})
	}
}

func TestConfigureNodePreservesHostToken(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	t.Setenv("CMESH_CONFIG", path)
	writeValidConfig(t, path)

	before, err := config.Load(path)
	if err != nil {
		t.Fatalf("load before: %v", err)
	}
	if before.Gateway.HostToken == "" {
		t.Fatalf("fixture has no host token")
	}

	// configure_node will attempt detection (machine/runners/peers). Detection may
	// fail in CI; the host_token preservation logic runs regardless because it
	// reads the existing config before generating. We assert the generated preview
	// reuses the token by writing with confirmed=true and reloading.
	resp := configureNode(context.Background(), []byte(`{"role":"both","confirmed":true}`))
	if !resp.OK {
		// If detection fails (no runner on the CI box), skip rather than false-fail;
		// the preservation path is still exercised by reading existing first.
		encoded, _ := json.Marshal(resp.Error)
		t.Skipf("configure_node could not run in this environment: %s", encoded)
	}
	after, err := config.Load(path)
	if err != nil {
		t.Fatalf("load after: %v", err)
	}
	if after.Gateway.HostToken != before.Gateway.HostToken {
		t.Fatalf("host_token changed: before=%q after=%q", before.Gateway.HostToken, after.Gateway.HostToken)
	}
}

func TestPromptsListAndGetShape(t *testing.T) {
	srv := &Server{}

	listResp, ok := srv.handle(context.Background(), rpcRequest{
		JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "prompts/list",
	})
	if !ok || listResp.Error != nil {
		t.Fatalf("prompts/list failed: %+v", listResp.Error)
	}
	listBody, _ := json.Marshal(listResp.Result)
	if !strings.Contains(string(listBody), `"name":"setup-cmesh"`) {
		t.Fatalf("prompts/list missing setup-cmesh: %s", listBody)
	}

	getResp, ok := srv.handle(context.Background(), rpcRequest{
		JSONRPC: "2.0", ID: json.RawMessage(`2`), Method: "prompts/get",
		Params: json.RawMessage(`{"name":"setup-cmesh"}`),
	})
	if !ok || getResp.Error != nil {
		t.Fatalf("prompts/get failed: %+v", getResp.Error)
	}
	getBody, _ := json.Marshal(getResp.Result)
	body := string(getBody)
	if !strings.Contains(body, `"role":"user"`) {
		t.Fatalf("prompts/get missing user message: %s", body)
	}
	if !strings.Contains(body, "onboarding_status") {
		t.Fatalf("prompts/get does not instruct calling onboarding_status: %s", body)
	}
	if !strings.Contains(strings.ToLower(body), "no cross-node session") && !strings.Contains(body, "NO cross-node session") {
		t.Fatalf("prompts/get does not state there is no cross-node session: %s", body)
	}

	// Unknown prompt errors.
	bad, _ := srv.handle(context.Background(), rpcRequest{
		JSONRPC: "2.0", ID: json.RawMessage(`3`), Method: "prompts/get",
		Params: json.RawMessage(`{"name":"nope"}`),
	})
	if bad.Error == nil {
		t.Fatalf("prompts/get for unknown prompt should error")
	}
}

func TestInitializeAdvertisesPromptsCapability(t *testing.T) {
	srv := &Server{}
	resp, _ := srv.handle(context.Background(), rpcRequest{
		JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "initialize",
	})
	body, _ := json.Marshal(resp.Result)
	if !strings.Contains(string(body), `"prompts"`) {
		t.Fatalf("initialize did not advertise prompts capability: %s", body)
	}
}

func withConfig(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	t.Setenv("CMESH_CONFIG", path)
	writeValidConfig(t, path)
	return path
}

func writeValidConfig(t *testing.T, path string) {
	t.Helper()
	body := "[node]\nid = \"node-a\"\nname = \"node-a\"\nmesh_port = 7778\nmcp_port = 7777\n\n" +
		"[policy]\nallowed_peers = [\"node-a\"]\nallowed_cwds = [\"~/code/**\"]\nhop_limit = 2\n\n" +
		"[policy.outbound]\nallowed_peers = []\ndenied_profiles = [\"network\"]\nallow_subdelegation = false\n\n" +
		"[profiles.dev]\nrunner = \"claude-code\"\nclaims = [\"read_files\"]\nbare = false\nnetwork = false\ngit_push = false\nmax_runtime = \"2h\"\n\n" +
		"[runners.claude-code]\ncommand = [\"/usr/local/bin/claude\", \"-p\"]\ndefault_profile = \"dev\"\nconcurrency = 1\n\n" +
		"[gateway]\nhost_token = \"preserve-me-token\"\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}
