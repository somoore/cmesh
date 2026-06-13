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
