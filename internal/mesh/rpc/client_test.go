package rpc

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"cmesh/internal/mesh/schema"
	"cmesh/internal/task"
)

func TestClientPreservesTopLevelStructuredErrorCode(t *testing.T) {
	client := NewClient(errorServer(t, `{"code":"cwd_denied","detail":"outside allowed_cwds"}`))

	_, err := client.CreateTask(context.Background(), schema.CreateTaskRequest{TaskID: "tsk_taskone"})
	if err == nil {
		t.Fatal("CreateTask error = nil")
	}
	if CodeOf(err) != task.ErrorCWDDenied {
		t.Fatalf("CodeOf(err) = %q, want %q (err=%v)", CodeOf(err), task.ErrorCWDDenied, err)
	}
}

func TestClientPreservesWrappedStructuredErrorCode(t *testing.T) {
	client := NewClient(errorServer(t, `{"task_id":"tsk_taskone","state":"denied","error":{"code":"profile_unavailable","detail":"profile is not configured"}}`))

	_, err := client.CreateTask(context.Background(), schema.CreateTaskRequest{TaskID: "tsk_taskone"})
	if err == nil {
		t.Fatal("CreateTask error = nil")
	}
	if CodeOf(err) != task.ErrorProfileUnavailable {
		t.Fatalf("CodeOf(err) = %q, want %q (err=%v)", CodeOf(err), task.ErrorProfileUnavailable, err)
	}
}

func errorServer(t *testing.T, body string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}
