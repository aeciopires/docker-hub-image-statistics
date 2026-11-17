package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/aeciopires/docker-hub-image-statistics/internal/stats"

	_ "modernc.org/sqlite" // pure-Go driver: no CGO, works in distroless static images
)

const sqliteSchema = `
CREATE TABLE IF NOT EXISTS snapshots (
	namespace    TEXT    NOT NULL,
	name         TEXT    NOT NULL,
	collected_at INTEGER NOT NULL, -- Unix milliseconds (UTC)
	pull_count   INTEGER NOT NULL,
	star_count   INTEGER NOT NULL,
	last_updated INTEGER NOT NULL DEFAULT 0, -- Unix milliseconds, 0 when unknown
	description  TEXT    NOT NULL DEFAULT '',
	PRIMARY KEY (namespace, name, collected_at)
) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS snapshots_collected_at ON snapshots (collected_at);
`

// SQLite is the durable tier and the primary source of history.
type SQLite struct {
	db   *sql.DB
	path string
}

// OpenSQLite opens (or creates) the database file and applies the schema.
func OpenSQLite(ctx context.Context, path string) (*SQLite, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, fmt.Errorf("create sqlite directory: %w", err)
	}
	dsn := "file:" + filepath.ToSlash(path) +
		"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(ON)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	// SQLite allows a single writer; one connection avoids SQLITE_BUSY.
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, sqliteSchema); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply sqlite schema: %w", err)
	}
	return &SQLite{db: db, path: path}, nil
}

// Name implements Store.
func (s *SQLite) Name() string { return "sqlite" }

// Path returns the database file location.
func (s *SQLite) Path() string { return s.path }

func toMillis(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

func fromMillis(ms int64) time.Time {
	if ms == 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms).UTC()
}

// Save implements Store.
func (s *SQLite) Save(ctx context.Context, snapshots []stats.Snapshot) (err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO snapshots
		(namespace, name, collected_at, pull_count, star_count, last_updated, description)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (namespace, name, collected_at) DO UPDATE SET
			pull_count = excluded.pull_count, star_count = excluded.star_count,
			last_updated = excluded.last_updated, description = excluded.description`)
	if err != nil {
		return fmt.Errorf("prepare insert: %w", err)
	}
	defer stmt.Close()
	for _, sn := range snapshots {
		if _, err = stmt.ExecContext(ctx, sn.Namespace, sn.Name, toMillis(sn.CollectedAt),
			sn.PullCount, sn.StarCount, toMillis(sn.LastUpdated), sn.Description); err != nil {
			return fmt.Errorf("insert %s: %w", sn.Key(), err)
		}
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

const selectColumns = `namespace, name, collected_at, pull_count, star_count, last_updated, description`

func (s *SQLite) query(ctx context.Context, q string, args ...any) ([]stats.Snapshot, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []stats.Snapshot
	for rows.Next() {
		var (
			sn                 stats.Snapshot
			collected, lastUpd int64
		)
		if err := rows.Scan(&sn.Namespace, &sn.Name, &collected, &sn.PullCount, &sn.StarCount, &lastUpd, &sn.Description); err != nil {
			return nil, err
		}
		sn.CollectedAt, sn.LastUpdated = fromMillis(collected), fromMillis(lastUpd)
		out = append(out, sn)
	}
	return out, rows.Err()
}

// Latest implements Store.
func (s *SQLite) Latest(ctx context.Context) ([]stats.Snapshot, error) {
	return s.query(ctx, `SELECT `+selectColumns+` FROM snapshots AS s
		WHERE collected_at = (SELECT MAX(collected_at) FROM snapshots
			WHERE namespace = s.namespace AND name = s.name)`)
}

// History implements Store.
func (s *SQLite) History(ctx context.Context, key string, since time.Time) ([]stats.Snapshot, error) {
	ns, name, ok := splitKey(key)
	if !ok {
		return nil, fmt.Errorf("invalid image key %q", key)
	}
	return s.query(ctx, `SELECT `+selectColumns+` FROM snapshots
		WHERE namespace = ? AND name = ? AND collected_at >= ? ORDER BY collected_at`,
		ns, name, toMillis(since))
}

// All implements Store.
func (s *SQLite) All(ctx context.Context, since time.Time) ([]stats.Snapshot, error) {
	return s.query(ctx, `SELECT `+selectColumns+` FROM snapshots
		WHERE collected_at >= ? ORDER BY collected_at, namespace, name`, toMillis(since))
}

// Prune implements Store.
func (s *SQLite) Prune(ctx context.Context, before time.Time) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM snapshots WHERE collected_at < ?`, toMillis(before))
	return err
}

// Ping implements Store.
func (s *SQLite) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

// Close implements Store.
func (s *SQLite) Close() error { return s.db.Close() }
