package store

import (
	"cmp"
	"context"
	"slices"
	"sync"
	"time"

	"github.com/aeciopires/docker-hub-image-statistics/internal/stats"
)

// Memory keeps snapshots in process memory. It is the fastest tier and the
// last-resort fallback, but it is lost on restart.
type Memory struct {
	mu     sync.RWMutex
	byKey  map[string][]stats.Snapshot // ascending by CollectedAt
	closed bool
}

// NewMemory returns an empty in-memory store.
func NewMemory() *Memory {
	return &Memory{byKey: make(map[string][]stats.Snapshot)}
}

// Name implements Store.
func (m *Memory) Name() string { return "memory" }

// Save implements Store. A snapshot with the same key and timestamp replaces the old one.
func (m *Memory) Save(ctx context.Context, snapshots []stats.Snapshot) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return ErrClosed
	}
	m.insertLocked(snapshots)
	return nil
}

func (m *Memory) insertLocked(snapshots []stats.Snapshot) {
	for _, s := range snapshots {
		s.CollectedAt = s.CollectedAt.UTC()
		h := m.byKey[s.Key()]
		i, found := slices.BinarySearchFunc(h, s.CollectedAt, func(e stats.Snapshot, t time.Time) int {
			return e.CollectedAt.Compare(t)
		})
		if found {
			h[i] = s
		} else {
			h = slices.Insert(h, i, s)
		}
		m.byKey[s.Key()] = h
	}
}

// Latest implements Store.
func (m *Memory) Latest(ctx context.Context) ([]stats.Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.closed {
		return nil, ErrClosed
	}
	out := make([]stats.Snapshot, 0, len(m.byKey))
	for _, h := range m.byKey {
		out = append(out, h[len(h)-1])
	}
	return out, nil
}

// History implements Store.
func (m *Memory) History(ctx context.Context, key string, since time.Time) ([]stats.Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.closed {
		return nil, ErrClosed
	}
	return slices.Clone(sinceIndex(m.byKey[key], since)), nil
}

// All implements Store.
func (m *Memory) All(ctx context.Context, since time.Time) ([]stats.Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.closed {
		return nil, ErrClosed
	}
	var out []stats.Snapshot
	for _, h := range m.byKey {
		out = append(out, sinceIndex(h, since)...)
	}
	return out, nil
}

func sinceIndex(asc []stats.Snapshot, since time.Time) []stats.Snapshot {
	i, _ := slices.BinarySearchFunc(asc, since, func(e stats.Snapshot, t time.Time) int {
		return e.CollectedAt.Compare(t)
	})
	return asc[i:]
}

// Prune implements Store.
func (m *Memory) Prune(ctx context.Context, before time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return ErrClosed
	}
	m.pruneLocked(before)
	return nil
}

func (m *Memory) pruneLocked(before time.Time) {
	for k, h := range m.byKey {
		kept := sinceIndex(h, before)
		if len(kept) == 0 {
			delete(m.byKey, k)
			continue
		}
		m.byKey[k] = slices.Clone(kept)
	}
}

// Ping implements Store.
func (m *Memory) Ping(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.closed {
		return ErrClosed
	}
	return nil
}

// Close implements Store.
func (m *Memory) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	clear(m.byKey)
	return nil
}

// snapshotAllLocked returns every snapshot ordered by time then key (used by Disk).
func (m *Memory) snapshotAllLocked() []stats.Snapshot {
	var out []stats.Snapshot
	for _, h := range m.byKey {
		out = append(out, h...)
	}
	slices.SortFunc(out, func(a, b stats.Snapshot) int {
		return cmp.Or(a.CollectedAt.Compare(b.CollectedAt), cmp.Compare(a.Key(), b.Key()))
	})
	return out
}
