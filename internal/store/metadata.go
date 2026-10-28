package store

import (
	"context"
	"fmt"
	"time"

	"cmesh/internal/task"
)

type PeerTask struct {
	TaskID          string `json:"task_id"`
	PeerNodeID      string `json:"peer_node_id"`
	RemoteTaskID    string `json:"remote_task_id"`
	OriginSessionID string `json:"origin_session_id,omitempty"`
	// OwnerTaskID is the worker task whose task token delegated this one, or
	// empty for a host-token delegation. It is what a task token is authorized
	// against, so it is set once and never cleared by a later upsert.
	OwnerTaskID string `json:"owner_task_id,omitempty"`
	// ProfileEffective is the profile the worker actually granted this task. It
	// is mirrored here so an approval request can be checked against what the
	// task really holds, instead of always falling back to asking the human.
	ProfileEffective string     `json:"profile_effective,omitempty"`
	State            task.State `json:"state"`
	ResultRef        string     `json:"result_ref,omitempty"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
}

func (s *Store) UpsertPeerTask(ctx context.Context, peerTask PeerTask) error {
	now := time.Now().UTC()
	if peerTask.CreatedAt.IsZero() {
		peerTask.CreatedAt = now
	}
	peerTask.UpdatedAt = now
	// profile_effective is preserved when an update omits it: status refreshes
	// upsert the same row without knowing the profile, and blanking it would
	// silently disarm the mechanical approval check.
	//
	// owner_task_id is set once: status refreshes upsert without it, and a later
	// write must never move a task to a different owner.
	_, err := s.db.ExecContext(ctx, `INSERT INTO peer_tasks(task_id, peer_node_id, remote_task_id, origin_session_id, owner_task_id, profile_effective, state, result_ref, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(task_id) DO UPDATE SET
			peer_node_id = excluded.peer_node_id,
			remote_task_id = excluded.remote_task_id,
			origin_session_id = excluded.origin_session_id,
			owner_task_id = CASE WHEN peer_tasks.owner_task_id <> '' THEN peer_tasks.owner_task_id ELSE excluded.owner_task_id END,
			profile_effective = CASE WHEN excluded.profile_effective <> '' THEN excluded.profile_effective ELSE peer_tasks.profile_effective END,
			state = excluded.state,
			result_ref = excluded.result_ref,
			updated_at = excluded.updated_at`,
		peerTask.TaskID, peerTask.PeerNodeID, peerTask.RemoteTaskID, peerTask.OriginSessionID, peerTask.OwnerTaskID, peerTask.ProfileEffective, peerTask.State, peerTask.ResultRef, formatTime(peerTask.CreatedAt), formatTime(peerTask.UpdatedAt),
	)
	if err != nil {
		return fmt.Errorf("upsert peer task: %w", err)
	}
	return nil
}

func (s *Store) GetPeerTask(ctx context.Context, taskID string) (PeerTask, error) {
	var peerTask PeerTask
	var created, updated string
	err := s.db.QueryRowContext(ctx, `SELECT task_id, peer_node_id, remote_task_id, origin_session_id, owner_task_id, profile_effective, state, result_ref, created_at, updated_at FROM peer_tasks WHERE task_id = ?`, taskID).
		Scan(&peerTask.TaskID, &peerTask.PeerNodeID, &peerTask.RemoteTaskID, &peerTask.OriginSessionID, &peerTask.OwnerTaskID, &peerTask.ProfileEffective, &peerTask.State, &peerTask.ResultRef, &created, &updated)
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

func (s *Store) ListPeerTasks(ctx context.Context, limit int) ([]PeerTask, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx, `SELECT task_id, peer_node_id, remote_task_id, origin_session_id, owner_task_id, profile_effective, state, result_ref, created_at, updated_at FROM peer_tasks ORDER BY updated_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list peer tasks: %w", err)
	}
	defer rows.Close()
	var out []PeerTask
	for rows.Next() {
		var peerTask PeerTask
		var created, updated string
		if err := rows.Scan(&peerTask.TaskID, &peerTask.PeerNodeID, &peerTask.RemoteTaskID, &peerTask.OriginSessionID, &peerTask.OwnerTaskID, &peerTask.ProfileEffective, &peerTask.State, &peerTask.ResultRef, &created, &updated); err != nil {
			return nil, err
		}
		var parseErr error
		peerTask.CreatedAt, parseErr = parseTime(created)
		if parseErr != nil {
			return nil, fmt.Errorf("decode peer task created_at: %w", parseErr)
		}
		peerTask.UpdatedAt, parseErr = parseTime(updated)
		if parseErr != nil {
			return nil, fmt.Errorf("decode peer task updated_at: %w", parseErr)
		}
		out = append(out, peerTask)
	}
	return out, rows.Err()
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
