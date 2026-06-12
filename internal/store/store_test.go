package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"cmesh/internal/task"
)

func TestBeginDispatchCreatesAndReplays(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	raw := []byte(`{"task_id":"task-1","prompt":"go test"}`)
	rec := newRecord("task-1")
	first, err := s.BeginDispatch(ctx, rec, raw, 30*24*time.Hour)
	if err != nil {
		t.Fatalf("BeginDispatch create: %v", err)
	}
	if !first.Created || first.Replayed {
		t.Fatalf("expected created outcome, got %+v", first)
	}
	second, err := s.BeginDispatch(ctx, rec, raw, 30*24*time.Hour)
	if err != nil {
		t.Fatalf("BeginDispatch replay: %v", err)
	}
	if second.Created || !second.Replayed {
		t.Fatalf("expected replay outcome, got %+v", second)
	}
	if second.Task.PayloadHash != task.SHA256Hex(raw) {
		t.Fatalf("unexpected payload hash %q", second.Task.PayloadHash)
	}
}

func TestBeginDispatchRejectsMutatedReplay(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	rec := newRecord("task-1")
	if _, err := s.BeginDispatch(ctx, rec, []byte(`{"prompt":"a"}`), 30*24*time.Hour); err != nil {
		t.Fatalf("BeginDispatch create: %v", err)
	}
	_, err := s.BeginDispatch(ctx, rec, []byte(`{"prompt":"b"}`), 30*24*time.Hour)
	if !errors.Is(err, ErrTaskConflict) {
		t.Fatalf("expected ErrTaskConflict, got %v", err)
	}
}

func TestBeginDispatchRejectsDifferentOriginForSameTaskID(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	if _, err := s.BeginDispatch(ctx, newRecord("task-1"), []byte(`{"prompt":"a"}`), 30*24*time.Hour); err != nil {
		t.Fatalf("BeginDispatch create: %v", err)
	}
	rec := newRecord("task-1")
	rec.OriginNodeID = "other-node"
	_, err := s.BeginDispatch(ctx, rec, []byte(`{"prompt":"a"}`), 30*24*time.Hour)
	if !errors.Is(err, ErrTaskIDOwnedByDifferentOrigin) {
		t.Fatalf("expected ErrTaskIDOwnedByDifferentOrigin, got %v", err)
	}
}

func TestUpdateTaskStateStoresResult(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	rec := newRecord("task-1")
	if _, err := s.BeginDispatch(ctx, rec, []byte(`{"prompt":"a"}`), 30*24*time.Hour); err != nil {
		t.Fatalf("BeginDispatch create: %v", err)
	}
	if _, err := s.UpdateTaskState(ctx, rec.ID, StateUpdate{State: task.StateRunning}); err != nil {
		t.Fatalf("running update: %v", err)
	}
	result := task.ResultEnvelope{
		Status:                 task.StateDone,
		WorkerSummaryUntrusted: "tests passed",
		VerificationRequired:   true,
	}
	updated, err := s.UpdateTaskState(ctx, rec.ID, StateUpdate{State: task.StateDone, Result: &result, ResultRef: "transcript:task-1"})
	if err != nil {
		t.Fatalf("done update: %v", err)
	}
	if updated.Result == nil || updated.Result.WorkerSummaryUntrusted != "tests passed" {
		t.Fatalf("result was not stored: %+v", updated.Result)
	}
	if updated.ResultRef != "transcript:task-1" {
		t.Fatalf("unexpected result ref %q", updated.ResultRef)
	}
}

func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "cmesh.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	})
	return s
}

func newRecord(id string) task.Record {
	return task.Record{
		ID:               id,
		OriginNodeID:     "origin-node",
		OriginSessionID:  "session-1",
		WorkerNodeID:     "worker-node",
		State:            task.StateQueued,
		CWD:              "/repo",
		Runner:           "claude-code",
		ProfileRequested: "dev",
		ProfileEffective: "dev",
		PromptHash:       task.PromptHash("go test"),
		PayloadHash:      task.SHA256Hex([]byte(`{"prompt":"a"}`)),
	}
}
