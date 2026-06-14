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
