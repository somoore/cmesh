package setupstdio

import (
	"bytes"
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
)

func TestServeInitializeOverStdioFrame(t *testing.T) {
	req := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`
	in := strings.NewReader(frame(req))
	var out bytes.Buffer
	srv := &Server{In: in, Out: &out}
	if err := srv.Serve(context.Background()); err != nil {
		t.Fatalf("Serve: %v", err)
	}
	body := responseBody(t, out.String())
	var resp rpcResponse
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("response JSON: %v\n%s", err, body)
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	if !strings.Contains(body, `"name":"cmesh-setup"`) {
		t.Fatalf("missing server info: %s", body)
	}
}

func TestServeInitializeOverNewlineFrame(t *testing.T) {
	req := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`
	in := strings.NewReader(req + "\n")
	var out bytes.Buffer
	srv := &Server{In: in, Out: &out}
	if err := srv.Serve(context.Background()); err != nil {
		t.Fatalf("Serve: %v", err)
	}
	got := out.String()
	if strings.Contains(got, "Content-Length:") {
		t.Fatalf("newline request got content-length response: %q", got)
	}
	var resp rpcResponse
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &resp); err != nil {
		t.Fatalf("response JSON: %v\n%s", err, got)
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	if !strings.Contains(got, `"name":"cmesh-setup"`) {
		t.Fatalf("missing server info: %s", got)
	}
}

func TestWriteConfigRequiresConfirmation(t *testing.T) {
	resp := (&Server{}).CallTool(context.Background(), "write_config", []byte(`{"role":"worker"}`))
	if !resp.OK {
		t.Fatalf("write_config preflight should be ok: %+v", resp)
	}
	encoded, _ := json.Marshal(resp.Result)
	if !strings.Contains(string(encoded), "requires_confirmation") {
		t.Fatalf("expected confirmation preflight, got %s", encoded)
	}
}

func TestProfilePreviewAndWriteConfigTools(t *testing.T) {
	preview := (&Server{}).CallTool(context.Background(), "profile_preview_config", []byte(`{"role":"worker","allowed_peers":["mac"],"allowed_cwds":["~/code/**"],"profiles":["query"]}`))
	if !preview.OK {
		t.Fatalf("profile_preview_config failed: %+v", preview)
	}
	encoded, _ := json.Marshal(preview.Result)
	if !strings.Contains(string(encoded), "[profiles.query]") {
		t.Fatalf("preview missing query profile: %s", encoded)
	}

	write := (&Server{}).CallTool(context.Background(), "profile_write_config", []byte(`{"role":"worker"}`))
	if !write.OK {
		t.Fatalf("profile_write_config preflight should be ok: %+v", write)
	}
	encoded, _ = json.Marshal(write.Result)
	if !strings.Contains(string(encoded), "requires_confirmation") {
		t.Fatalf("expected confirmation preflight, got %s", encoded)
	}
}

func TestProfileGuideAndRecommendTools(t *testing.T) {
	srv := &Server{}
	guide := srv.CallTool(context.Background(), "profile_guide", nil)
	if !guide.OK {
		t.Fatalf("profile_guide failed: %+v", guide)
	}
	body, _ := json.Marshal(guide.Result)
	for _, want := range []string{"query", "readonly-agent", "repo-dev", "no shell", "no writes"} {
		if !strings.Contains(strings.ToLower(string(body)), want) {
			t.Fatalf("profile_guide missing %q: %s", want, body)
		}
	}

	rec := srv.CallTool(context.Background(), "profile_recommend", []byte(`{"intent":"edit code and run tests"}`))
	if !rec.OK {
		t.Fatalf("profile_recommend failed: %+v", rec)
	}
	body, _ = json.Marshal(rec.Result)
	if !strings.Contains(string(body), `"profile":"repo-dev"`) || !strings.Contains(string(body), `"requires_ttl":true`) {
		t.Fatalf("repo edit recommendation missing repo-dev ttl: %s", body)
	}
}

func TestProfileWriteConfigRequiresElevatedAcknowledgement(t *testing.T) {
	srv := &Server{}
	preview := srv.CallTool(context.Background(), "profile_write_config", []byte(`{"role":"worker","profiles":["query","repo-dev"],"runner":"codex"}`))
	if !preview.OK {
		t.Fatalf("profile_write_config preview failed: %+v", preview)
	}
	body, _ := json.Marshal(preview.Result)
	if !strings.Contains(string(body), "profile_acknowledgement_required") || !strings.Contains(string(body), "repo-dev") {
		t.Fatalf("preview did not guide acknowledgement: %s", body)
	}

	denied := srv.CallTool(context.Background(), "profile_write_config", []byte(`{"role":"worker","profiles":["query","repo-dev"],"runner":"codex","confirmed":true}`))
	if denied.OK || denied.Error == nil || denied.Error.Code != "profile_acknowledgement_required" {
		t.Fatalf("confirmed repo-dev without acknowledgement should be denied: %+v", denied)
	}
}

func TestProfileWriteConfigScansRawTOMLForElevatedProfiles(t *testing.T) {
	body := "[profiles.repo-dev]\nrunner = \"codex\"\nwrites = \"repo_only\"\nshell = \"allow\"\nrequire_lease = true\n\n"
	encoded, _ := json.Marshal(map[string]any{"toml": body, "confirmed": true})
	resp := (&Server{}).CallTool(context.Background(), "profile_write_config", encoded)
	if resp.OK || resp.Error == nil || resp.Error.Code != "profile_acknowledgement_required" {
		t.Fatalf("raw TOML repo-dev without acknowledgement should be denied: %+v", resp)
	}
}

func TestToolSchemasUseArrayRequired(t *testing.T) {
	for _, tool := range tools() {
		schema, ok := tool["inputSchema"].(map[string]any)
		if !ok {
			t.Fatalf("tool %q inputSchema = %#v", tool["name"], tool["inputSchema"])
		}
		if _, ok := schema["required"].([]string); !ok {
			t.Fatalf("tool %q required = %#v, want []string", tool["name"], schema["required"])
		}
	}
	body, err := json.Marshal(tools())
	if err != nil {
		t.Fatalf("marshal tools: %v", err)
	}
	if strings.Contains(string(body), `"required":null`) {
		t.Fatalf("tool schemas contain null required: %s", body)
	}
}

func frame(body string) string {
	return "Content-Length: " + strconv.Itoa(len(body)) + "\r\n\r\n" + body
}

func responseBody(t *testing.T, framed string) string {
	t.Helper()
	_, body, ok := strings.Cut(framed, "\r\n\r\n")
	if !ok {
		t.Fatalf("missing frame separator: %q", framed)
	}
	return body
}
