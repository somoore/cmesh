package store

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"cmesh/internal/audit"
	"cmesh/internal/task"
)

func TestBeginDispatchCreatesAndReplays(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	raw := []byte(`{"task_id":"tsk_taskone","prompt":"go test"}`)
	rec := newRecord("tsk_taskone")
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
	rec := newRecord("tsk_taskone")
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
	if _, err := s.BeginDispatch(ctx, newRecord("tsk_taskone"), []byte(`{"prompt":"a"}`), 30*24*time.Hour); err != nil {
		t.Fatalf("BeginDispatch create: %v", err)
	}
	rec := newRecord("tsk_taskone")
	rec.OriginNodeID = "other-node"
	_, err := s.BeginDispatch(ctx, rec, []byte(`{"prompt":"a"}`), 30*24*time.Hour)
	if !errors.Is(err, ErrTaskIDOwnedByDifferentOrigin) {
		t.Fatalf("expected ErrTaskIDOwnedByDifferentOrigin, got %v", err)
	}
}

func TestUpdateTaskStateStoresResult(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	rec := newRecord("tsk_taskone")
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
	updated, err := s.UpdateTaskState(ctx, rec.ID, StateUpdate{State: task.StateDone, Result: &result, ResultRef: "transcript:tsk_taskone"})
	if err != nil {
		t.Fatalf("done update: %v", err)
	}
	if updated.Result == nil || updated.Result.WorkerSummaryUntrusted != "tests passed" {
		t.Fatalf("result was not stored: %+v", updated.Result)
	}
	if updated.ResultRef != "transcript:tsk_taskone" {
		t.Fatalf("unexpected result ref %q", updated.ResultRef)
	}
}

func TestHasAuditEvent(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	event := audit.Event{EventID: "evt-1", Type: "task.dispatch.requested", TaskID: "tsk_taskone", NodeID: "node-1", Timestamp: time.Now().UTC()}
	if err := s.RecordAuditEvent(ctx, event); err != nil {
		t.Fatalf("RecordAuditEvent: %v", err)
	}
	found, err := s.HasAuditEvent(ctx, "tsk_taskone", "task.dispatch.requested")
	if err != nil {
		t.Fatalf("HasAuditEvent: %v", err)
	}
	if !found {
		t.Fatal("expected audit event to be found")
	}
	found, err = s.HasAuditEvent(ctx, "tsk_taskone", "task.completed")
	if err != nil {
		t.Fatalf("HasAuditEvent missing type: %v", err)
	}
	if found {
		t.Fatal("unexpected audit event match")
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

// Events must come back in a stable, monotonic order with a usable cursor:
// a poller resuming from the highest seq it has seen should receive exactly
// what is new, with no replays and no gaps. Timestamps cannot serve this --
// several events can share one instant.
func TestListTaskEventsPagesByStableCursor(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	// Same timestamp for every event, so ordering cannot lean on it.
	stamp := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		if err := s.RecordAuditEvent(ctx, audit.Event{
			EventID:   "evt_" + string(rune('a'+i)),
			Type:      "task.progress",
			TaskID:    "tsk_events",
			NodeID:    "worker",
			Timestamp: stamp,
		}); err != nil {
			t.Fatalf("RecordAuditEvent %d: %v", i, err)
		}
	}
	// An event on a different task must never appear in the first task's page.
	if err := s.RecordAuditEvent(ctx, audit.Event{
		EventID: "evt_other", Type: "task.progress", TaskID: "tsk_other", NodeID: "worker", Timestamp: stamp,
	}); err != nil {
		t.Fatalf("RecordAuditEvent other: %v", err)
	}

	first, err := s.ListTaskEvents(ctx, "tsk_events", 0, 3)
	if err != nil {
		t.Fatalf("ListTaskEvents: %v", err)
	}
	if len(first) != 3 {
		t.Fatalf("first page = %d events, want 3", len(first))
	}
	for i := 1; i < len(first); i++ {
		if first[i].Seq <= first[i-1].Seq {
			t.Fatalf("events not strictly increasing: %d then %d", first[i-1].Seq, first[i].Seq)
		}
	}
	if first[0].Event.EventID != "evt_a" {
		t.Fatalf("first event = %q, want evt_a (oldest first)", first[0].Event.EventID)
	}

	rest, err := s.ListTaskEvents(ctx, "tsk_events", first[len(first)-1].Seq, 10)
	if err != nil {
		t.Fatalf("ListTaskEvents resume: %v", err)
	}
	if len(rest) != 2 {
		t.Fatalf("resumed page = %d events, want the remaining 2", len(rest))
	}
	for _, event := range rest {
		if event.Event.EventID == "evt_other" {
			t.Fatal("events from another task leaked into the page")
		}
	}

	latest, err := s.LatestTaskEventSeq(ctx, "tsk_events")
	if err != nil {
		t.Fatalf("LatestTaskEventSeq: %v", err)
	}
	if latest != rest[len(rest)-1].Seq {
		t.Fatalf("latest seq = %d, want %d", latest, rest[len(rest)-1].Seq)
	}
	// Polling from the latest cursor yields nothing until new activity.
	tail, err := s.ListTaskEvents(ctx, "tsk_events", latest, 10)
	if err != nil {
		t.Fatalf("ListTaskEvents tail: %v", err)
	}
	if len(tail) != 0 {
		t.Fatalf("polling from the latest cursor returned %d events, want 0", len(tail))
	}

	if seq, err := s.LatestTaskEventSeq(ctx, "tsk_missing"); err != nil || seq != 0 {
		t.Fatalf("LatestTaskEventSeq(unknown) = %d, %v; want 0, nil", seq, err)
	}
}

// Migration v3 adds seq to a table that may already hold rows. Those rows must
// be backfilled in the order they were written, and the counter must continue
// past them so a new event never reuses an existing cursor value.
func TestMigrationBackfillsExistingTaskEventSeq(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "cmesh.db")
	s := openStoreAt(t, path)

	// Seed rows the way a pre-v3 database would have: no seq column value.
	for i, id := range []string{"evt_old_a", "evt_old_b"} {
		if _, err := s.db.ExecContext(ctx,
			`INSERT INTO task_events(event_id, event_type, task_id, node_id, timestamp, payload_json, seq)
			 VALUES (?, 'task.legacy', 'tsk_legacy', 'worker', ?, '{}', 0)`,
			id, formatTime(time.Date(2026, 7, 30, 0, i, 0, 0, time.UTC))); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
	// Re-run v3's backfill and counter reset against the seeded rows, selecting
	// the statements by content so this does not break when the migration gains
	// or reorders statements.
	var replayed int
	for _, stmt := range migrationByVersion(t, 3).statements {
		if !strings.Contains(stmt, "UPDATE task_events SET seq") && !strings.Contains(stmt, "task_event_seq(id, next_seq)") {
			continue
		}
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("rerun migration statement: %v", err)
		}
		replayed++
	}
	if replayed != 2 {
		t.Fatalf("replayed %d migration statements, want the backfill and the counter reset", replayed)
	}

	if err := s.RecordAuditEvent(ctx, audit.Event{
		EventID: "evt_new", Type: "task.progress", TaskID: "tsk_legacy", NodeID: "worker",
		Timestamp: time.Date(2026, 7, 31, 0, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("RecordAuditEvent: %v", err)
	}

	events, err := s.ListTaskEvents(ctx, "tsk_legacy", 0, 10)
	if err != nil {
		t.Fatalf("ListTaskEvents: %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("got %d events, want 3", len(events))
	}
	var ids []string
	seen := map[int64]bool{}
	for _, event := range events {
		if seen[event.Seq] {
			t.Fatalf("duplicate seq %d after backfill", event.Seq)
		}
		seen[event.Seq] = true
		ids = append(ids, event.Event.EventID)
	}
	// Backfilled rows keep their written order, and the new event sorts last.
	if ids[0] != "evt_old_a" || ids[1] != "evt_old_b" || ids[2] != "evt_new" {
		t.Fatalf("event order = %v, want [evt_old_a evt_old_b evt_new]", ids)
	}
}

// openStoreAt opens a store at an explicit path so a test can seed rows and
// re-run migration statements against the same database.
func openStoreAt(t *testing.T, path string) *Store {
	t.Helper()
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// migrationByVersion returns the migration with the given version so tests can
// replay specific statements without depending on slice positions.
func migrationByVersion(t *testing.T, version int) migration {
	t.Helper()
	for _, m := range migrations {
		if m.version == version {
			return m
		}
	}
	t.Fatalf("migration version %d not found", version)
	return migration{}
}

// RecordAuditEvent allocates a cursor and inserts in one transaction so two
// concurrent recorders cannot be handed the same seq. The daemon writes audit
// events from per-task goroutines, so this path is genuinely concurrent.
func TestRecordAuditEventAllocatesDistinctSeqsConcurrently(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	const writers = 20

	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := s.RecordAuditEvent(ctx, audit.Event{
				EventID:   fmt.Sprintf("evt_%02d", i),
				Type:      "task.progress",
				TaskID:    "tsk_concurrent",
				NodeID:    "worker",
				Timestamp: time.Now().UTC(),
			}); err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent RecordAuditEvent: %v", err)
	}

	events, err := s.ListTaskEvents(ctx, "tsk_concurrent", 0, writers+10)
	if err != nil {
		t.Fatalf("ListTaskEvents: %v", err)
	}
	if len(events) != writers {
		t.Fatalf("recorded %d events, want %d", len(events), writers)
	}
	seen := map[int64]string{}
	for _, event := range events {
		if other, dup := seen[event.Seq]; dup {
			t.Fatalf("seq %d handed to both %s and %s", event.Seq, other, event.Event.EventID)
		}
		seen[event.Seq] = event.Event.EventID
	}
}
