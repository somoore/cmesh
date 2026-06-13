package rpc

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"cmesh/internal/mesh/schema"
	"cmesh/internal/task"
)

func TestClientPreservesStructuredErrorCode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"code":"cwd_denied","detail":"outside allowed_cwds"}`))
	}))
	defer srv.Close()

	client := NewClient(srv.URL)
	_, err := client.CreateTask(context.Background(), schema.CreateTaskRequest{TaskID: "task-1"})
	if err == nil {
		t.Fatal("CreateTask error = nil")
	}
	if CodeOf(err) != task.ErrorCWDDenied {
		t.Fatalf("CodeOf(err) = %q, want %q (err=%v)", CodeOf(err), task.ErrorCWDDenied, err)
	}
}
