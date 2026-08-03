package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"cmesh/internal/task"
)

type DispatchOutcome struct {
	Task     task.Record
	Created  bool
	Replayed bool
}

type StateUpdate struct {
	State             task.State
	TerminalErrorCode task.ErrorCode
	ResultRef         string
	Result            *task.ResultEnvelope
	At                time.Time
}

func (s *Store) BeginDispatch(ctx context.Context, rec task.Record, rawRequestBody []byte, retention time.Duration) (DispatchOutcome, error) {
	if retention <= 0 {
		return DispatchOutcome{}, ErrExpiredIdempotencyRetention
	}
	now := time.Now().UTC()
	rec.State = task.StateQueued
	rec.PayloadHash = task.SHA256Hex(rawRequestBody)
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = now
	}
	rec.UpdatedAt = now
	if err := rec.ValidateNew(); err != nil {
		return DispatchOutcome{}, err
	}
	key := task.IdempotencyKey(rec.OriginNodeID, rec.OriginSessionID, rec.ID)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return DispatchOutcome{}, fmt.Errorf("begin dispatch: %w", err)
	}
	defer rollback(tx)
	existing, exists, err := getIdempotencyEntry(ctx, tx, key)
	if err != nil {
		return DispatchOutcome{}, err
	}
	if exists {
		if existing.payloadHash != rec.PayloadHash {
			return DispatchOutcome{}, ErrTaskConflict
		}
		if _, err := tx.ExecContext(ctx, `UPDATE idempotency_keys SET last_seen_at = ?, replay_count = replay_count + 1 WHERE key = ?`, formatTime(now), key); err != nil {
			return DispatchOutcome{}, fmt.Errorf("update idempotency replay: %w", err)
		}
		stored, err := getTaskTx(ctx, tx, existing.taskID)
		if err != nil {
			return DispatchOutcome{}, err
		}
		if err := tx.Commit(); err != nil {
			return DispatchOutcome{}, fmt.Errorf("commit dispatch replay: %w", err)
		}
		return DispatchOutcome{Task: stored, Created: false, Replayed: true}, nil
	}
	stored, err := getTaskTx(ctx, tx, rec.ID)
	if err == nil {
		if stored.OriginNodeID != rec.OriginNodeID || stored.OriginSessionID != rec.OriginSessionID {
			return DispatchOutcome{}, ErrTaskIDOwnedByDifferentOrigin
		}
		return DispatchOutcome{}, ErrTaskConflict
	}
	if err != ErrNotFound {
		return DispatchOutcome{}, err
	}
	if err := insertTask(ctx, tx, rec); err != nil {
		return DispatchOutcome{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO idempotency_keys
		(key, task_id, origin_node_id, origin_session_id, payload_hash, request_json, created_at, last_seen_at, retain_until, replay_count)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 0)`,
		key, rec.ID, rec.OriginNodeID, rec.OriginSessionID, rec.PayloadHash, string(rawRequestBody), formatTime(now), formatTime(now), formatTime(now.Add(retention)),
	); err != nil {
		return DispatchOutcome{}, fmt.Errorf("insert idempotency key: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return DispatchOutcome{}, fmt.Errorf("commit dispatch: %w", err)
	}
	return DispatchOutcome{Task: rec, Created: true, Replayed: false}, nil
}

func (s *Store) GetTask(ctx context.Context, taskID string) (task.Record, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+taskColumns()+` FROM tasks WHERE task_id = ?`, taskID)
	rec, err := decodeTask(row)
	return rec, notFound(err)
}

func (s *Store) UpdateTaskState(ctx context.Context, taskID string, update StateUpdate) (task.Record, error) {
	if !update.State.Valid() {
		return task.Record{}, fmt.Errorf("invalid task state %q", update.State)
	}
	if update.At.IsZero() {
		update.At = time.Now().UTC()
	}
	if update.Result != nil {
		if err := task.ValidateResult(*update.Result); err != nil {
			return task.Record{}, err
		}
		if update.Result.Status != update.State {
			return task.Record{}, fmt.Errorf("result status %q does not match update state %q", update.Result.Status, update.State)
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return task.Record{}, fmt.Errorf("begin state update: %w", err)
	}
	defer rollback(tx)
	rec, err := getTaskTx(ctx, tx, taskID)
	if err != nil {
		return task.Record{}, err
	}
	if !task.ValidTransition(rec.State, update.State) {
		return task.Record{}, ErrInvalidStateTransition
	}
	resultJSON := ""
	if update.Result != nil {
		encoded, err := encodeJSON(update.Result)
		if err != nil {
			return task.Record{}, fmt.Errorf("encode result: %w", err)
		}
		resultJSON = encoded
	}
	startedAt := formatTime(rec.StartedAt)
	if update.State == task.StateRunning && rec.StartedAt.IsZero() {
		startedAt = formatTime(update.At)
	}
	completedAt := formatTime(rec.CompletedAt)
	if update.State.Terminal() {
		completedAt = formatTime(update.At)
	}
	resultRef := rec.ResultRef
	if update.ResultRef != "" {
		resultRef = update.ResultRef
	}
	terminalCode := string(update.TerminalErrorCode)
	if update.State == task.StateDone {
		terminalCode = ""
	}
	if resultJSON == "" && rec.Result != nil {
		encoded, err := encodeJSON(rec.Result)
		if err != nil {
			return task.Record{}, fmt.Errorf("encode existing result: %w", err)
		}
		resultJSON = encoded
	}
	if _, err := tx.ExecContext(ctx, `UPDATE tasks
		SET state = ?, updated_at = ?, started_at = ?, completed_at = ?, terminal_error_code = ?, result_ref = ?, result_json = ?
		WHERE task_id = ?`,
		update.State, formatTime(update.At), startedAt, completedAt, terminalCode, resultRef, resultJSON, taskID,
	); err != nil {
		return task.Record{}, fmt.Errorf("update task state: %w", err)
	}
	updated, err := getTaskTx(ctx, tx, taskID)
	if err != nil {
		return task.Record{}, err
	}
	if err := tx.Commit(); err != nil {
		return task.Record{}, fmt.Errorf("commit state update: %w", err)
	}
	return updated, nil
}

func (s *Store) DeleteExpiredIdempotencyRecords(ctx context.Context, now time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM idempotency_keys WHERE retain_until < ?`, formatTime(now.UTC()))
	if err != nil {
		return 0, fmt.Errorf("delete expired idempotency records: %w", err)
	}
	return res.RowsAffected()
}

func insertTask(ctx context.Context, tx *sql.Tx, rec task.Record) error {
	chainJSON, err := json.Marshal(rec.DelegationChain)
	if err != nil {
		return fmt.Errorf("encode delegation chain: %w", err)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO tasks
		(task_id, origin_node_id, origin_session_id, worker_node_id, state, cwd, runner,
		profile_requested, profile_effective, prompt_hash, payload_hash, allow_dirty,
		delegation_chain_json, hop_count, created_at, updated_at, started_at, completed_at,
		terminal_error_code, result_ref, result_json, handoff_from)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		rec.ID, rec.OriginNodeID, rec.OriginSessionID, rec.WorkerNodeID, rec.State, rec.CWD, rec.Runner,
		rec.ProfileRequested, rec.ProfileEffective, rec.PromptHash, rec.PayloadHash, boolInt(rec.AllowDirty),
		string(chainJSON), rec.HopCount, formatTime(rec.CreatedAt), formatTime(rec.UpdatedAt), formatTime(rec.StartedAt),
		formatTime(rec.CompletedAt), string(rec.TerminalErrorCode), rec.ResultRef, "", rec.HandoffFrom,
	)
	if err != nil {
		return fmt.Errorf("insert task: %w", err)
	}
	return nil
}

type rowQuerier interface {
	QueryRowContext(context.Context, string, ...interface{}) *sql.Row
}

func getTaskTx(ctx context.Context, tx rowQuerier, taskID string) (task.Record, error) {
	row := tx.QueryRowContext(ctx, `SELECT `+taskColumns()+` FROM tasks WHERE task_id = ?`, taskID)
	rec, err := decodeTask(row)
	return rec, notFound(err)
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

type idempotencyEntry struct {
	taskID      string
	payloadHash string
}

func getIdempotencyEntry(ctx context.Context, tx *sql.Tx, key string) (idempotencyEntry, bool, error) {
	var entry idempotencyEntry
	err := tx.QueryRowContext(ctx, `SELECT task_id, payload_hash FROM idempotency_keys WHERE key = ?`, key).Scan(&entry.taskID, &entry.payloadHash)
	if err == sql.ErrNoRows {
		return idempotencyEntry{}, false, nil
	}
	if err != nil {
		return idempotencyEntry{}, false, fmt.Errorf("read idempotency key: %w", err)
	}
	return entry, true, nil
}
