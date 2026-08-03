package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

type migration struct {
	version    int
	statements []string
}

var migrations = []migration{
	{
		version: 1,
		statements: []string{
			`CREATE TABLE IF NOT EXISTS tasks (
				task_id TEXT PRIMARY KEY,
				origin_node_id TEXT NOT NULL,
				origin_session_id TEXT NOT NULL,
				worker_node_id TEXT NOT NULL DEFAULT '',
				state TEXT NOT NULL,
				cwd TEXT NOT NULL,
				runner TEXT NOT NULL,
				profile_requested TEXT NOT NULL,
				profile_effective TEXT NOT NULL DEFAULT '',
				prompt_hash TEXT NOT NULL,
				payload_hash TEXT NOT NULL,
				allow_dirty INTEGER NOT NULL DEFAULT 0,
				delegation_chain_json TEXT NOT NULL DEFAULT '[]',
				hop_count INTEGER NOT NULL DEFAULT 0,
				created_at TEXT NOT NULL,
				updated_at TEXT NOT NULL,
				started_at TEXT NOT NULL DEFAULT '',
				completed_at TEXT NOT NULL DEFAULT '',
				terminal_error_code TEXT NOT NULL DEFAULT '',
				result_ref TEXT NOT NULL DEFAULT '',
				result_json TEXT NOT NULL DEFAULT ''
			)`,
			`CREATE INDEX IF NOT EXISTS idx_tasks_origin ON tasks(origin_node_id, origin_session_id)`,
			`CREATE INDEX IF NOT EXISTS idx_tasks_state_updated ON tasks(state, updated_at)`,
			`CREATE TABLE IF NOT EXISTS idempotency_keys (
				key TEXT PRIMARY KEY,
				task_id TEXT NOT NULL,
				origin_node_id TEXT NOT NULL,
				origin_session_id TEXT NOT NULL,
				payload_hash TEXT NOT NULL,
				request_json TEXT NOT NULL,
				created_at TEXT NOT NULL,
				last_seen_at TEXT NOT NULL,
				retain_until TEXT NOT NULL,
				replay_count INTEGER NOT NULL DEFAULT 0,
				FOREIGN KEY(task_id) REFERENCES tasks(task_id) ON DELETE CASCADE
			)`,
			`CREATE INDEX IF NOT EXISTS idx_idempotency_retain_until ON idempotency_keys(retain_until)`,
			`CREATE TABLE IF NOT EXISTS task_events (
				event_id TEXT PRIMARY KEY,
				event_type TEXT NOT NULL,
				task_id TEXT NOT NULL DEFAULT '',
				node_id TEXT NOT NULL,
				timestamp TEXT NOT NULL,
				payload_json TEXT NOT NULL
			)`,
			`CREATE INDEX IF NOT EXISTS idx_task_events_task_time ON task_events(task_id, timestamp)`,
			`CREATE TABLE IF NOT EXISTS peer_tasks (
				task_id TEXT PRIMARY KEY,
				peer_node_id TEXT NOT NULL,
				remote_task_id TEXT NOT NULL,
				state TEXT NOT NULL,
				result_ref TEXT NOT NULL DEFAULT '',
				created_at TEXT NOT NULL,
				updated_at TEXT NOT NULL
			)`,
			`CREATE TABLE IF NOT EXISTS runner_manifests (
				runner TEXT PRIMARY KEY,
				version TEXT NOT NULL DEFAULT '',
				manifest_json TEXT NOT NULL,
				updated_at TEXT NOT NULL
			)`,
		},
	},
	{
		version: 2,
		statements: []string{
			`ALTER TABLE peer_tasks ADD COLUMN origin_session_id TEXT NOT NULL DEFAULT ''`,
		},
	},
	{
		version: 3,
		statements: []string{
			// seq gives task_events a stable, monotonic cursor for incremental
			// reads. The existing (task_id, timestamp) index cannot serve that:
			// timestamps are not unique, so a poller resuming from one could
			// replay or skip events recorded in the same instant.
			//
			// SQLite cannot add an AUTOINCREMENT column to an existing table, so
			// seq is assigned explicitly on insert from a counter table.
			//
			// Existing rows are backfilled from rowid, which is already their
			// write order because this table is insert-only. Ordering by
			// (timestamp, rowid) via a correlated subquery would give the same
			// result but is quadratic: measured at 61s for 20k rows versus 10ms
			// for this, and task_events grows with every state transition on
			// every task. Migrations hold the write lock and run before the
			// daemon binds a listener, so a slow backfill reads as a hang.
			`ALTER TABLE task_events ADD COLUMN seq INTEGER NOT NULL DEFAULT 0`,
			`CREATE TABLE IF NOT EXISTS task_event_seq (
				id INTEGER PRIMARY KEY CHECK (id = 0),
				next_seq INTEGER NOT NULL
			)`,
			`UPDATE task_events SET seq = rowid`,
			`INSERT OR REPLACE INTO task_event_seq(id, next_seq)
				VALUES (0, (SELECT COALESCE(MAX(seq), 0) + 1 FROM task_events))`,
			`CREATE INDEX IF NOT EXISTS idx_task_events_task_seq ON task_events(task_id, seq)`,
		},
	},
	{
		version: 4,
		statements: []string{
			// The task channel: a durable, ordered conversation scoped to one task.
			// body_untrusted is named for what it is -- worker-authored text on the
			// inbound direction -- so no reader mistakes it for evidence.
			`CREATE TABLE IF NOT EXISTS task_messages (
				message_id TEXT PRIMARY KEY,
				task_id TEXT NOT NULL,
				seq INTEGER NOT NULL,
				direction TEXT NOT NULL,
				kind TEXT NOT NULL,
				body_untrusted TEXT NOT NULL DEFAULT '',
				metadata_json TEXT NOT NULL DEFAULT '',
				answered_seq INTEGER NOT NULL DEFAULT 0,
				client_message_id TEXT NOT NULL DEFAULT '',
				created_at TEXT NOT NULL,
				-- A message only means something attached to a task. The FK keeps
				-- an orphan out of the store the way idempotency_keys does, and
				-- drops a task's channel with the task.
				FOREIGN KEY(task_id) REFERENCES tasks(task_id) ON DELETE CASCADE
			)`,
			`CREATE INDEX IF NOT EXISTS idx_task_messages_task_seq ON task_messages(task_id, seq)`,
			// Idempotency for a retried post: the same client id on the same task
			// must not append a second message.
			`CREATE UNIQUE INDEX IF NOT EXISTS idx_task_messages_client_id
				ON task_messages(task_id, client_message_id) WHERE client_message_id <> ''`,
			// Same counter shape as task_event_seq rather than generalizing that
			// table: its CHECK (id = 0) makes a retrofit a migration risk for no
			// gain, and two tables with one obvious pattern beats one clever one.
			`CREATE TABLE IF NOT EXISTS task_message_seq (
				id INTEGER PRIMARY KEY CHECK (id = 0),
				next_seq INTEGER NOT NULL
			)`,
			`INSERT OR REPLACE INTO task_message_seq(id, next_seq) VALUES (0, 1)`,
			// The origin-side mirror records the profile the worker actually got,
			// so an approval request can be checked mechanically against what that
			// task was granted instead of always falling back to asking the human.
			`ALTER TABLE peer_tasks ADD COLUMN profile_effective TEXT NOT NULL DEFAULT ''`,
		},
	},
}

func (s *Store) Migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY,
		applied_at TEXT NOT NULL
	)`); err != nil {
		return fmt.Errorf("create schema migrations table: %w", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration: %w", err)
	}
	defer rollback(tx)
	applied := map[int]bool{}
	rows, err := tx.QueryContext(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return fmt.Errorf("read schema migrations: %w", err)
	}
	for rows.Next() {
		var version int
		if err := rows.Scan(&version); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan schema migration: %w", err)
		}
		applied[version] = true
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close schema migration rows: %w", err)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate schema migrations: %w", err)
	}
	for _, migration := range migrations {
		if applied[migration.version] {
			continue
		}
		for _, statement := range migration.statements {
			if _, err := tx.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("apply migration %d: %w", migration.version, err)
			}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations(version, applied_at) VALUES(?, ?)`, migration.version, formatTime(time.Now().UTC())); err != nil {
			return fmt.Errorf("record migration %d: %w", migration.version, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration: %w", err)
	}
	return nil
}

func rollback(tx *sql.Tx) {
	if tx != nil {
		_ = tx.Rollback()
	}
}
