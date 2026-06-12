package store

import (
	"context"
	"fmt"
	"time"

	"cmesh/internal/task"
)

type PeerTask struct {
	TaskID       string     `json:"task_id"`
	PeerNodeID   string     `json:"peer_node_id"`
	RemoteTaskID string     `json:"remote_task_id"`
	State        task.State `json:"state"`
	ResultRef    string     `json:"result_ref,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

func (s *Store) UpsertPeerTask(ctx context.Context, peerTask PeerTask) error {
	now := time.Now().UTC()
	if peerTask.CreatedAt.IsZero() {
		peerTask.CreatedAt = now
	}
	peerTask.UpdatedAt = now
	_, err := s.db.ExecContext(ctx, `INSERT INTO peer_tasks(task_id, peer_node_id, remote_task_id, state, result_ref, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(task_id) DO UPDATE SET
			peer_node_id = excluded.peer_node_id,
			remote_task_id = excluded.remote_task_id,
			state = excluded.state,
			result_ref = excluded.result_ref,
			updated_at = excluded.updated_at`,
		peerTask.TaskID, peerTask.PeerNodeID, peerTask.RemoteTaskID, peerTask.State, peerTask.ResultRef, formatTime(peerTask.CreatedAt), formatTime(peerTask.UpdatedAt),
	)
	if err != nil {
		return fmt.Errorf("upsert peer task: %w", err)
	}
	return nil
}

func (s *Store) GetPeerTask(ctx context.Context, taskID string) (PeerTask, error) {
	var peerTask PeerTask
	var created, updated string
	err := s.db.QueryRowContext(ctx, `SELECT task_id, peer_node_id, remote_task_id, state, result_ref, created_at, updated_at FROM peer_tasks WHERE task_id = ?`, taskID).
		Scan(&peerTask.TaskID, &peerTask.PeerNodeID, &peerTask.RemoteTaskID, &peerTask.State, &peerTask.ResultRef, &created, &updated)
	if err != nil {
		return PeerTask{}, notFound(err)
	}
	var parseErr error
	peerTask.CreatedAt, parseErr = parseTime(created)
	if parseErr != nil {
		return PeerTask{}, fmt.Errorf("decode peer task created_at: %w", parseErr)
	}
	peerTask.UpdatedAt, parseErr = parseTime(updated)
	if parseErr != nil {
		return PeerTask{}, fmt.Errorf("decode peer task updated_at: %w", parseErr)
	}
	return peerTask, nil
}

func (s *Store) UpsertRunnerManifest(ctx context.Context, runner, version, manifestJSON string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO runner_manifests(runner, version, manifest_json, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(runner) DO UPDATE SET
			version = excluded.version,
			manifest_json = excluded.manifest_json,
			updated_at = excluded.updated_at`,
		runner, version, manifestJSON, formatTime(time.Now().UTC()),
	)
	if err != nil {
		return fmt.Errorf("upsert runner manifest: %w", err)
	}
	return nil
}

func (s *Store) GetRunnerManifest(ctx context.Context, runner string) (version string, manifestJSON string, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT version, manifest_json FROM runner_manifests WHERE runner = ?`, runner).Scan(&version, &manifestJSON)
	if err != nil {
		return "", "", notFound(err)
	}
	return version, manifestJSON, nil
}
