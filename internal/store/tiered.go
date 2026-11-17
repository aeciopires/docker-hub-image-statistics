package store

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/aeciopires/docker-hub-image-statistics/internal/stats"
)

// Observer is notified about every tier operation (for metrics).
type Observer func(tier, op string, err error)

// Tiered fans writes out to every tier and serves reads from the first tier,
// in order, that answers without error and with data.
//
// Analogy: a library that keeps the original book (SQLite), a photocopy in a
// drawer (disk) and notes on a whiteboard (memory). New pages are written to
// all three; to read, you try the original first and only fall back to the
// copies when it is unavailable.
type Tiered struct {
	tiers    []Store
	logger   *slog.Logger
	observer Observer
}

// NewTiered combines tiers. At least one tier is required.
func NewTiered(logger *slog.Logger, observer Observer, tiers ...Store) (*Tiered, error) {
	if len(tiers) == 0 {
		return nil, errors.New("at least one store tier is required")
	}
	if observer == nil {
		observer = func(string, string, error) {}
	}
	return &Tiered{tiers: tiers, logger: logger, observer: observer}, nil
}

// Tiers returns the tier names in read order.
func (t *Tiered) Tiers() []string {
	names := make([]string, len(t.tiers))
	for i, s := range t.tiers {
		names[i] = s.Name()
	}
	return names
}

// Name implements Store.
func (t *Tiered) Name() string { return "tiered(" + strings.Join(t.Tiers(), ",") + ")" }

// writeAll runs fn on every tier; it fails only when every tier fails.
func (t *Tiered) writeAll(ctx context.Context, op string, fn func(Store) error) error {
	var errs []error
	for _, s := range t.tiers {
		err := fn(s)
		t.observer(s.Name(), op, err)
		if err != nil {
			t.logger.WarnContext(ctx, "cache tier write failed", "tier", s.Name(), "op", op, "error", err)
			errs = append(errs, fmt.Errorf("%s: %w", s.Name(), err))
		}
	}
	if len(errs) == len(t.tiers) {
		return errors.Join(errs...)
	}
	return nil
}

// readFirst returns the first successful, non-empty result.
func readFirst[T any](ctx context.Context, t *Tiered, op string, fn func(Store) ([]T, error)) ([]T, error) {
	var errs []error
	for _, s := range t.tiers {
		res, err := fn(s)
		t.observer(s.Name(), op, err)
		if err != nil {
			t.logger.WarnContext(ctx, "cache tier read failed, trying next tier", "tier", s.Name(), "op", op, "error", err)
			errs = append(errs, fmt.Errorf("%s: %w", s.Name(), err))
			continue
		}
		if len(res) > 0 {
			return res, nil
		}
	}
	if len(errs) == len(t.tiers) {
		return nil, errors.Join(errs...)
	}
	return nil, nil
}

// Save implements Store.
func (t *Tiered) Save(ctx context.Context, snapshots []stats.Snapshot) error {
	return t.writeAll(ctx, "save", func(s Store) error { return s.Save(ctx, snapshots) })
}

// Latest implements Store.
func (t *Tiered) Latest(ctx context.Context) ([]stats.Snapshot, error) {
	return readFirst(ctx, t, "latest", func(s Store) ([]stats.Snapshot, error) { return s.Latest(ctx) })
}

// History implements Store.
func (t *Tiered) History(ctx context.Context, key string, since time.Time) ([]stats.Snapshot, error) {
	return readFirst(ctx, t, "history", func(s Store) ([]stats.Snapshot, error) { return s.History(ctx, key, since) })
}

// All implements Store.
func (t *Tiered) All(ctx context.Context, since time.Time) ([]stats.Snapshot, error) {
	return readFirst(ctx, t, "all", func(s Store) ([]stats.Snapshot, error) { return s.All(ctx, since) })
}

// Prune implements Store.
func (t *Tiered) Prune(ctx context.Context, before time.Time) error {
	return t.writeAll(ctx, "prune", func(s Store) error { return s.Prune(ctx, before) })
}

// Ping implements Store: healthy while at least one tier is healthy.
func (t *Tiered) Ping(ctx context.Context) error {
	var errs []error
	for _, s := range t.tiers {
		if err := s.Ping(ctx); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", s.Name(), err))
		}
	}
	if len(errs) == len(t.tiers) {
		return errors.Join(errs...)
	}
	return nil
}

// Close implements Store.
func (t *Tiered) Close() error {
	var errs []error
	for _, s := range t.tiers {
		if err := s.Close(); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", s.Name(), err))
		}
	}
	return errors.Join(errs...)
}

// Warm copies the history of the first non-empty tier into the tiers that
// are empty (for example memory after a restart), so fallbacks are useful.
func (t *Tiered) Warm(ctx context.Context, since time.Time) error {
	var source []stats.Snapshot
	empty := make([]Store, 0, len(t.tiers))
	for _, s := range t.tiers {
		all, err := s.All(ctx, since)
		if err != nil {
			continue
		}
		if len(all) == 0 {
			empty = append(empty, s)
			continue
		}
		if source == nil {
			source = all
		}
	}
	if len(source) == 0 {
		return nil
	}
	var errs []error
	for _, s := range empty {
		if err := s.Save(ctx, source); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", s.Name(), err))
			continue
		}
		t.logger.InfoContext(ctx, "cache tier warmed", "tier", s.Name(), "snapshots", len(source))
	}
	return errors.Join(errs...)
}

func splitKey(key string) (namespace, name string, ok bool) {
	namespace, name, ok = strings.Cut(key, "/")
	return namespace, name, ok && namespace != "" && name != "" && !strings.Contains(name, "/")
}
