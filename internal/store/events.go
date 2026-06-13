package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"cmesh/internal/audit"
)

func (s *Store) RecordAuditEvent(ctx context.Context, event audit.Event) error {
	payload, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("encode audit event: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO task_events(event_id, event_type, task_id, node_id, timestamp, payload_json)
		VALUES (?, ?, ?, ?, ?, ?)`,
		event.EventID, event.Type, event.TaskID, event.NodeID, formatTime(event.Timestamp), string(payload),
	)
	if err != nil {
		return fmt.Errorf("record audit event: %w", err)
	}
	return nil
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
