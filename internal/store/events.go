package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"cmesh/internal/audit"
)

// TaskEvent is one recorded step in a task's life, as read back for incremental
// polling. Seq is the cursor a caller passes back as `after` to receive only
// later events.
//
// Event carries the full stored audit record for local use (the CLI, doctor,
// this node's own tooling). It is deliberately NOT what crosses the mesh:
// schema.TaskEvent projects a narrower field set, because the audit record also
// holds error_detail and a free-form extra map that can contain worker-local
// paths and other detail a delegating peer has no need for.
type TaskEvent struct {
	Seq   int64
	Event audit.Event
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
		var item TaskEvent
		var eventID, eventType, rowTaskID, nodeID, timestamp, payload string
		if err := rows.Scan(&item.Seq, &eventID, &eventType, &rowTaskID, &nodeID, &timestamp, &payload); err != nil {
			return nil, fmt.Errorf("scan task event: %w", err)
		}
		// The payload is the authoritative record; the columns are the index.
		// Fall back to them when it cannot be decoded so a malformed row still
		// reports what happened instead of vanishing from the stream.
		if payload != "" {
			if err := json.Unmarshal([]byte(payload), &item.Event); err != nil {
				item.Event = audit.Event{}
			}
		}
		if item.Event.EventID == "" {
			item.Event.EventID = eventID
		}
		if item.Event.Type == "" {
			item.Event.Type = eventType
		}
		if item.Event.TaskID == "" {
			item.Event.TaskID = rowTaskID
		}
		if item.Event.NodeID == "" {
			item.Event.NodeID = nodeID
		}
		if item.Event.Timestamp.IsZero() {
			item.Event.Timestamp, err = parseTime(timestamp)
			if err != nil {
				return nil, fmt.Errorf("decode task event timestamp: %w", err)
			}
		}
		out = append(out, item)
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
