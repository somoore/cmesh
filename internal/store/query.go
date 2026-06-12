package store

import (
	"context"
	"fmt"
	"time"

	"cmesh/internal/task"
)

func (s *Store) ListTasksByState(ctx context.Context, states ...task.State) ([]task.Record, error) {
	if len(states) == 0 {
		return nil, nil
	}
	query := `SELECT ` + taskColumns() + ` FROM tasks WHERE state IN (`
	args := make([]interface{}, 0, len(states))
	for i, state := range states {
		if i > 0 {
			query += `,`
		}
		query += `?`
		args = append(args, string(state))
	}
	query += `) ORDER BY updated_at DESC`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list tasks by state: %w", err)
	}
	defer rows.Close()
	var out []task.Record
	for rows.Next() {
		rec, err := decodeTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) RecentTasks(ctx context.Context, limit int) ([]task.Record, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+taskColumns()+` FROM tasks ORDER BY updated_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("recent tasks: %w", err)
	}
	defer rows.Close()
	var out []task.Record
	for rows.Next() {
		rec, err := decodeTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

func (s *Store) TerminalTasksCompletedBefore(ctx context.Context, cutoff time.Time, limit int) ([]task.Record, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+taskColumns()+` FROM tasks
		WHERE state IN (?, ?, ?, ?) AND completed_at != '' AND completed_at < ?
		ORDER BY completed_at ASC LIMIT ?`,
		task.StateDone, task.StateFailed, task.StateDenied, task.StateCancelled, formatTime(cutoff.UTC()), limit)
	if err != nil {
		return nil, fmt.Errorf("terminal tasks completed before: %w", err)
	}
	defer rows.Close()
	var out []task.Record
	for rows.Next() {
		rec, err := decodeTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}
