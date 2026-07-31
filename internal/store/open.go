package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

type Store struct {
	db *sql.DB
}

func Open(path string) (*Store, error) {
	return OpenContext(context.Background(), path)
}

func OpenContext(ctx context.Context, path string) (*Store, error) {
	// The data dir holds the SQLite db (raw task prompts, results, full audit
	// trail), logs, artifacts, and task tokens. Keep it owner-only (0700) so no
	// other local user can read another node's task history. MkdirAll does not
	// chmod an existing dir, so tighten it explicitly if it already exists.
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create store directory: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, fmt.Errorf("restrict store directory permissions: %w", err)
	}
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, fmt.Errorf("open sqlite store: %w", err)
	}
	store := &Store{db: db}
	// Tighten the main db before migrations run: migrations are the first writes,
	// so leaving the default umask mode in place until afterwards leaves a window
	// where task history is world-readable. The -wal/-shm sidecars do not exist
	// until that first write, so they are chmodded below (the loop tolerates a
	// missing file, and repeats the db for the case where it was created here).
	if err := os.Chmod(path, 0o600); err != nil && !os.IsNotExist(err) {
		_ = db.Close()
		return nil, fmt.Errorf("restrict store file permissions: %w", err)
	}
	if err := store.Migrate(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	// modernc.org/sqlite creates the db (and -wal/-shm) honoring umask, which is
	// typically world-readable 0644. Tighten the db and its sidecars to 0600 so
	// the sensitive contents are owner-only regardless of umask.
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := os.Chmod(path+suffix, 0o600); err != nil && !os.IsNotExist(err) {
			_ = db.Close()
			return nil, fmt.Errorf("restrict store file permissions: %w", err)
		}
	}
	return store, nil
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}
