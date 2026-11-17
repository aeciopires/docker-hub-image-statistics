// Package store persists image snapshots in cache tiers.
//
// Three implementations exist: SQLite, a JSON file on disk and memory. Tiered
// combines them, writing to every tier and reading from the first healthy one
// in the configured order (SQLite, disk, memory).
package store

import (
	"context"
	"errors"
	"time"

	"github.com/aeciopires/docker-hub-image-statistics/internal/stats"
)

// ErrClosed is returned when a store is used after Close.
var ErrClosed = errors.New("store is closed")

// Store persists snapshots.
type Store interface {
	// Name identifies the tier (sqlite, disk, memory).
	Name() string
	// Save appends snapshots.
	Save(ctx context.Context, snapshots []stats.Snapshot) error
	// Latest returns the newest snapshot of every image.
	Latest(ctx context.Context) ([]stats.Snapshot, error)
	// History returns the snapshots of one image collected at or after since,
	// in ascending order. Use a zero time for the full history.
	History(ctx context.Context, key string, since time.Time) ([]stats.Snapshot, error)
	// All returns every snapshot collected at or after since.
	All(ctx context.Context, since time.Time) ([]stats.Snapshot, error)
	// Prune deletes snapshots collected before the given time.
	Prune(ctx context.Context, before time.Time) error
	// Ping reports whether the tier is usable.
	Ping(ctx context.Context) error
	// Close releases resources.
	Close() error
}
