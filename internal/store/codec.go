package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"cmesh/internal/task"
)

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func parseTime(value string) (time.Time, error) {
	if value == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, err
	}
	return t.UTC(), nil
}

func encodeJSON(value interface{}) (string, error) {
	b, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func decodeTask(rows scanner) (task.Record, error) {
	var rec task.Record
	var allowDirty int
	var chainJSON string
	var created, updated, started, completed, terminalCode, resultJSON string
	err := rows.Scan(
		&rec.ID,
		&rec.OriginNodeID,
		&rec.OriginSessionID,
		&rec.WorkerNodeID,
		&rec.State,
		&rec.CWD,
		&rec.Runner,
		&rec.ProfileRequested,
		&rec.ProfileEffective,
		&rec.PromptHash,
		&rec.PayloadHash,
		&allowDirty,
		&chainJSON,
		&rec.HopCount,
		&created,
		&updated,
		&started,
		&completed,
		&terminalCode,
		&rec.ResultRef,
		&resultJSON,
	)
	if err != nil {
		return task.Record{}, err
	}
	rec.AllowDirty = allowDirty != 0
	if chainJSON != "" {
		if err := json.Unmarshal([]byte(chainJSON), &rec.DelegationChain); err != nil {
			return task.Record{}, fmt.Errorf("decode delegation chain: %w", err)
		}
	}
	createdAt, err := parseTime(created)
	if err != nil {
		return task.Record{}, fmt.Errorf("decode created_at: %w", err)
	}
	rec.CreatedAt = createdAt
	updatedAt, err := parseTime(updated)
	if err != nil {
		return task.Record{}, fmt.Errorf("decode updated_at: %w", err)
	}
	rec.UpdatedAt = updatedAt
	startedAt, err := parseTime(started)
	if err != nil {
		return task.Record{}, fmt.Errorf("decode started_at: %w", err)
	}
	rec.StartedAt = startedAt
	completedAt, err := parseTime(completed)
	if err != nil {
		return task.Record{}, fmt.Errorf("decode completed_at: %w", err)
	}
	rec.CompletedAt = completedAt
	rec.TerminalErrorCode = task.ErrorCode(terminalCode)
	if resultJSON != "" {
		var result task.ResultEnvelope
		if err := json.Unmarshal([]byte(resultJSON), &result); err != nil {
			return task.Record{}, fmt.Errorf("decode result: %w", err)
		}
		rec.Result = &result
	}
	return rec, nil
}

type scanner interface {
	Scan(dest ...interface{}) error
}

func taskColumns() string {
	return `task_id, origin_node_id, origin_session_id, worker_node_id, state, cwd, runner,
		profile_requested, profile_effective, prompt_hash, payload_hash, allow_dirty,
		delegation_chain_json, hop_count, created_at, updated_at, started_at, completed_at,
		terminal_error_code, result_ref, result_json`
}

func notFound(err error) error {
	if err == sql.ErrNoRows {
		return ErrNotFound
	}
	return err
}
