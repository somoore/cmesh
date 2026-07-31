package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"cmesh/internal/audit"
)

// TaskEvent is one recorded step in a task's life, as read back for incremental
// polling. Payload is the marshaled audit event; Seq is the cursor a caller
// passes back as `after` to receive only later events.
type TaskEvent struct {
	Seq       int64     `json:"seq"`
	EventID   string    `json:"event_id"`
	EventType string    `json:"event_type"`
	TaskID    string    `json:"task_id,omitempty"`
	NodeID    string    `json:"node_id,omitempty"`
	Timestamp time.Time `json:"timestamp"`
	Payload   any       `json:"payload,omitempty"`
}

// DefaultTaskEventLimit bounds a single events read. Callers poll with a cursor
// rather than fetching an unbounded history in one response.
const DefaultTaskEventLimit = 100

// MaxTaskEventLimit is the largest page a caller may request.
const MaxTaskEventLimit = 500

func (s *Store) RecordAuditEvent(ctx context.Context, event audit.Event) error {
	payload, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("encode audit event: %w", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin record audit event: %w", err)
	}
	defer rollback(tx)
	// Allocate the sequence inside the same transaction as the insert so two
	// concurrent recorders cannot be handed the same cursor value.
	var seq int64
	if err := tx.QueryRowContext(ctx, `UPDATE task_event_seq SET next_seq = next_seq + 1 WHERE id = 0 RETURNING next_seq - 1`).Scan(&seq); err != nil {
		return fmt.Errorf("allocate task event seq: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO task_events(event_id, event_type, task_id, node_id, timestamp, payload_json, seq)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		event.EventID, event.Type, event.TaskID, event.NodeID, formatTime(event.Timestamp), string(payload), seq,
	); err != nil {
		return fmt.Errorf("record audit event: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit audit event: %w", err)
	}
	return nil
}

// ListTaskEvents returns events for one task with seq greater than after, oldest
// first. It is the read side of progress polling: a caller passes the highest
// seq it has seen and receives only what is new.
func (s *Store) ListTaskEvents(ctx context.Context, taskID string, after int64, limit int) ([]TaskEvent, error) {
	if limit <= 0 {
		limit = DefaultTaskEventLimit
	}
	if limit > MaxTaskEventLimit {
		limit = MaxTaskEventLimit
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT seq, event_id, event_type, task_id, node_id, timestamp, payload_json
			FROM task_events WHERE task_id = ? AND seq > ? ORDER BY seq ASC LIMIT ?`,
		taskID, after, limit)
	if err != nil {
		return nil, fmt.Errorf("query task events: %w", err)
	}
	defer rows.Close()
	var out []TaskEvent
	for rows.Next() {
		var event TaskEvent
		var timestamp, payload string
		if err := rows.Scan(&event.Seq, &event.EventID, &event.EventType, &event.TaskID, &event.NodeID, &timestamp, &payload); err != nil {
			return nil, fmt.Errorf("scan task event: %w", err)
		}
		event.Timestamp, err = parseTime(timestamp)
		if err != nil {
			return nil, fmt.Errorf("decode task event timestamp: %w", err)
		}
		if payload != "" {
			var decoded any
			if err := json.Unmarshal([]byte(payload), &decoded); err == nil {
				event.Payload = decoded
			}
		}
		out = append(out, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate task events: %w", err)
	}
	return out, nil
}

// LatestTaskEventSeq returns the highest seq recorded for a task, or 0 when it
// has no events. Long-polling uses it to detect new activity cheaply.
func (s *Store) LatestTaskEventSeq(ctx context.Context, taskID string) (int64, error) {
	var seq sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `SELECT MAX(seq) FROM task_events WHERE task_id = ?`, taskID).Scan(&seq); err != nil {
		return 0, fmt.Errorf("query latest task event seq: %w", err)
	}
	if !seq.Valid {
		return 0, nil
	}
	return seq.Int64, nil
}

func (s *Store) HasAuditEvent(ctx context.Context, taskID, eventType string) (bool, error) {
	var value int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM task_events WHERE task_id = ? AND event_type = ? LIMIT 1`, taskID, eventType).Scan(&value)
	if err == nil {
		return true, nil
	}
	if err == sql.ErrNoRows {
		return false, nil
	}
	return false, fmt.Errorf("query audit event: %w", err)
}
