package store

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/aeciopires/docker-hub-image-statistics/internal/stats"
)

const diskFormatVersion = 1

// diskFile is the on-disk JSON document.
type diskFile struct {
	Version   int              `json:"version"`
	SavedAt   time.Time        `json:"saved_at"`
	Snapshots []stats.Snapshot `json:"snapshots"`
}

// Disk keeps snapshots in memory and persists them to a JSON file after every
// change, using an atomic write (temporary file + rename).
type Disk struct {
	path string
	mu   sync.Mutex // serializes mutations and file writes
	mem  *Memory
}

// OpenDisk loads (or creates) the JSON file at path.
func OpenDisk(path string) (*Disk, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, fmt.Errorf("create disk cache directory: %w", err)
	}
	d := &Disk{path: path, mem: NewMemory()}

	data, err := os.ReadFile(path) //nolint:gosec // the path is operator configuration, not user input
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return d, d.persist()
	case err != nil:
		return nil, fmt.Errorf("read disk cache: %w", err)
	}

	var doc diskFile
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("decode disk cache %q: %w", path, err)
	}
	if doc.Version != diskFormatVersion {
		return nil, fmt.Errorf("disk cache %q has unsupported version %d", path, doc.Version)
	}
	d.mem.insertLocked(doc.Snapshots)
	return d, nil
}

// Name implements Store.
func (d *Disk) Name() string { return "disk" }

// Path returns the file location.
func (d *Disk) Path() string { return d.path }

// Save implements Store.
func (d *Disk) Save(ctx context.Context, snapshots []stats.Snapshot) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := d.mem.Save(ctx, snapshots); err != nil {
		return err
	}
	return d.persist()
}

// Latest implements Store.
func (d *Disk) Latest(ctx context.Context) ([]stats.Snapshot, error) { return d.mem.Latest(ctx) }

// History implements Store.
func (d *Disk) History(ctx context.Context, key string, since time.Time) ([]stats.Snapshot, error) {
	return d.mem.History(ctx, key, since)
}

// All implements Store.
func (d *Disk) All(ctx context.Context, since time.Time) ([]stats.Snapshot, error) {
	return d.mem.All(ctx, since)
}

// Prune implements Store.
func (d *Disk) Prune(ctx context.Context, before time.Time) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := d.mem.Prune(ctx, before); err != nil {
		return err
	}
	return d.persist()
}

// Ping implements Store: the in-memory copy must be open and the directory writable.
func (d *Disk) Ping(ctx context.Context) error {
	if err := d.mem.Ping(ctx); err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Dir(d.path)); err != nil {
		return fmt.Errorf("disk cache directory: %w", err)
	}
	return nil
}

// Close implements Store.
func (d *Disk) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.mem.Close()
}

// persist writes the current state atomically. Callers hold d.mu.
func (d *Disk) persist() error {
	d.mem.mu.RLock()
	doc := diskFile{Version: diskFormatVersion, SavedAt: time.Now().UTC(), Snapshots: d.mem.snapshotAllLocked()}
	d.mem.mu.RUnlock()

	data, err := json.Marshal(doc)
	if err != nil {
		return fmt.Errorf("encode disk cache: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(d.path), ".statistics-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary disk cache: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after a successful rename

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write disk cache: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync disk cache: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close disk cache: %w", err)
	}
	if err := os.Rename(tmpName, d.path); err != nil {
		return fmt.Errorf("replace disk cache: %w", err)
	}
	return nil
}
