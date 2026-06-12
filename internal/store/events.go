package store

import (
	"context"
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
