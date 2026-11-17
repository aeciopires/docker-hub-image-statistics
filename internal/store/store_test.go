package store

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aeciopires/docker-hub-image-statistics/internal/stats"
)

var base = time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

func sn(ns, name string, hours int, pulls int64) stats.Snapshot {
	return stats.Snapshot{
		Namespace: ns, Name: name, PullCount: pulls, StarCount: 2,
		Description: "d", LastUpdated: base.Add(-time.Hour),
		CollectedAt: base.Add(time.Duration(hours) * time.Hour),
	}
}

func discard() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// conformance exercises the Store contract.
func conformance(t *testing.T, s Store) {
	t.Helper()
	ctx := t.Context()

	if err := s.Ping(ctx); err != nil {
		t.Fatalf("ping: %v", err)
	}
	latest, err := s.Latest(ctx)
	if err != nil || len(latest) != 0 {
		t.Fatalf("empty latest = %v, %v", latest, err)
	}

	in := []stats.Snapshot{
		sn("ns", "a", 0, 10), sn("ns", "a", 2, 30), sn("ns", "a", 1, 20),
		sn("ns", "b", 1, 5),
	}
	if err := s.Save(ctx, in); err != nil {
		t.Fatalf("save: %v", err)
	}
	// Upsert of an existing timestamp.
	if err := s.Save(ctx, []stats.Snapshot{sn("ns", "a", 2, 31)}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	latest, err = s.Latest(ctx)
	if err != nil || len(latest) != 2 {
		t.Fatalf("latest = %v, %v", latest, err)
	}
	for _, l := range latest {
		switch l.Name {
		case "a":
			if l.PullCount != 31 || !l.CollectedAt.Equal(base.Add(2*time.Hour)) {
				t.Errorf("latest a = %+v", l)
			}
			if l.Description != "d" || !l.LastUpdated.Equal(base.Add(-time.Hour)) {
				t.Errorf("fields not round-tripped: %+v", l)
			}
		case "b":
			if l.PullCount != 5 {
				t.Errorf("latest b = %+v", l)
			}
		}
	}

	h, err := s.History(ctx, "ns/a", time.Time{})
	if err != nil || len(h) != 3 || h[0].PullCount != 10 || h[2].PullCount != 31 {
		t.Fatalf("history = %+v, %v", h, err)
	}
	h, err = s.History(ctx, "ns/a", base.Add(time.Hour))
	if err != nil || len(h) != 2 {
		t.Fatalf("history since = %+v, %v", h, err)
	}
	h, err = s.History(ctx, "ns/missing", time.Time{})
	if err != nil || len(h) != 0 {
		t.Fatalf("missing history = %+v, %v", h, err)
	}

	all, err := s.All(ctx, base.Add(time.Hour))
	if err != nil || len(all) != 3 {
		t.Fatalf("all = %+v, %v", all, err)
	}

	if err := s.Prune(ctx, base.Add(90*time.Minute)); err != nil {
		t.Fatalf("prune: %v", err)
	}
	all, err = s.All(ctx, time.Time{})
	if err != nil || len(all) != 1 || all[0].Name != "a" {
		t.Fatalf("after prune = %+v, %v", all, err)
	}
}

func TestMemoryConformance(t *testing.T) {
	m := NewMemory()
	conformance(t, m)
	if m.Name() != "memory" {
		t.Fatal(m.Name())
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	checks := map[string]error{
		"save":  m.Save(ctx, nil),
		"prune": m.Prune(ctx, base),
		"ping":  m.Ping(ctx),
	}
	_, checks["latest"] = m.Latest(ctx)
	_, checks["history"] = m.History(ctx, "ns/a", time.Time{})
	_, checks["all"] = m.All(ctx, time.Time{})
	for op, err := range checks {
		if !errors.Is(err, ErrClosed) {
			t.Errorf("%s after close = %v, want ErrClosed", op, err)
		}
	}
}

func TestMemoryCanceledContext(t *testing.T) {
	m := NewMemory()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	checks := map[string]error{
		"save":  m.Save(ctx, nil),
		"prune": m.Prune(ctx, base),
		"ping":  m.Ping(ctx),
	}
	_, checks["latest"] = m.Latest(ctx)
	_, checks["history"] = m.History(ctx, "ns/a", time.Time{})
	_, checks["all"] = m.All(ctx, time.Time{})
	for op, err := range checks {
		if !errors.Is(err, context.Canceled) {
			t.Errorf("%s = %v, want context.Canceled", op, err)
		}
	}
}

func TestDiskConformanceAndReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "cache.json")
	d, err := OpenDisk(path)
	if err != nil {
		t.Fatal(err)
	}
	if d.Name() != "disk" || d.Path() != path {
		t.Fatal("unexpected name/path")
	}
	conformance(t, d)
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("cache file permissions = %o, want 600", perm)
	}

	reopened, err := OpenDisk(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	all, err := reopened.All(t.Context(), time.Time{})
	if err != nil || len(all) != 1 || all[0].PullCount != 31 {
		t.Fatalf("reloaded = %+v, %v", all, err)
	}
}

func TestDiskErrors(t *testing.T) {
	dir := t.TempDir()

	bad := filepath.Join(dir, "bad.json")
	os.WriteFile(bad, []byte("{not json"), 0o600)
	if _, err := OpenDisk(bad); err == nil || !strings.Contains(err.Error(), "decode") {
		t.Errorf("corrupt file err = %v", err)
	}

	future := filepath.Join(dir, "future.json")
	os.WriteFile(future, []byte(`{"version":99,"snapshots":[]}`), 0o600)
	if _, err := OpenDisk(future); err == nil || !strings.Contains(err.Error(), "version") {
		t.Errorf("version err = %v", err)
	}

	asDir := filepath.Join(dir, "isdir")
	os.Mkdir(asDir, 0o750)
	if _, err := OpenDisk(asDir); err == nil {
		t.Error("expected error reading a directory")
	}

	blocker := filepath.Join(dir, "file")
	os.WriteFile(blocker, nil, 0o600)
	if _, err := OpenDisk(filepath.Join(blocker, "x", "cache.json")); err == nil {
		t.Error("expected mkdir error")
	}

	// Operations after the directory disappears.
	gone := filepath.Join(dir, "gone")
	d, err := OpenDisk(filepath.Join(gone, "c.json"))
	if err != nil {
		t.Fatal(err)
	}
	os.RemoveAll(gone)
	if err := d.Ping(t.Context()); err == nil {
		t.Error("ping must fail without directory")
	}
	if err := d.Save(t.Context(), []stats.Snapshot{sn("ns", "a", 0, 1)}); err == nil {
		t.Error("save must fail without directory")
	}
	if err := d.Prune(t.Context(), base); err == nil {
		t.Error("prune must fail without directory")
	}
	d.Close()
	if err := d.Save(t.Context(), nil); !errors.Is(err, ErrClosed) {
		t.Errorf("save after close = %v", err)
	}
	if err := d.Prune(t.Context(), base); !errors.Is(err, ErrClosed) {
		t.Errorf("prune after close = %v", err)
	}
	if err := d.Ping(t.Context()); !errors.Is(err, ErrClosed) {
		t.Errorf("ping after close = %v", err)
	}
}

func TestSQLiteConformance(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db", "stats.db")
	s, err := OpenSQLite(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	if s.Name() != "sqlite" || s.Path() != path {
		t.Fatal("unexpected name/path")
	}
	conformance(t, s)

	if _, err := s.History(t.Context(), "invalid", time.Time{}); err == nil {
		t.Error("expected invalid key error")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	if err := s.Save(ctx, []stats.Snapshot{sn("ns", "a", 0, 1)}); err == nil {
		t.Error("save after close must fail")
	}
	if _, err := s.Latest(ctx); err == nil {
		t.Error("latest after close must fail")
	}
	if err := s.Ping(ctx); err == nil {
		t.Error("ping after close must fail")
	}

	reopened, err := OpenSQLite(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	all, err := reopened.All(ctx, time.Time{})
	if err != nil || len(all) != 1 {
		t.Fatalf("persisted = %+v, %v", all, err)
	}
}

func TestSQLiteOpenErrors(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "file")
	os.WriteFile(blocker, nil, 0o600)
	if _, err := OpenSQLite(t.Context(), filepath.Join(blocker, "x", "s.db")); err == nil {
		t.Error("expected mkdir error")
	}
	notDB := filepath.Join(dir, "garbage.db")
	os.WriteFile(notDB, []byte(strings.Repeat("garbage!", 1024)), 0o600)
	if _, err := OpenSQLite(t.Context(), notDB); err == nil {
		t.Error("expected schema error on non-database file")
	}
}

func TestSQLiteSaveCanceled(t *testing.T) {
	s, err := OpenSQLite(t.Context(), filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := s.Save(ctx, []stats.Snapshot{sn("ns", "a", 0, 1)}); err == nil {
		t.Error("expected error with canceled context")
	}
}

func TestSplitKey(t *testing.T) {
	for key, ok := range map[string]bool{"a/b": true, "a": false, "/b": false, "a/": false, "a/b/c": false} {
		if _, _, got := splitKey(key); got != ok {
			t.Errorf("splitKey(%q) ok = %v", key, got)
		}
	}
}

// failing is a Store whose every operation fails.
type failing struct{ name string }

var errBoom = errors.New("boom")

func (f failing) Name() string                                   { return f.name }
func (failing) Save(context.Context, []stats.Snapshot) error     { return errBoom }
func (failing) Latest(context.Context) ([]stats.Snapshot, error) { return nil, errBoom }
func (failing) History(context.Context, string, time.Time) ([]stats.Snapshot, error) {
	return nil, errBoom
}
func (failing) All(context.Context, time.Time) ([]stats.Snapshot, error) { return nil, errBoom }
func (failing) Prune(context.Context, time.Time) error                   { return errBoom }
func (failing) Ping(context.Context) error                               { return errBoom }
func (failing) Close() error                                             { return errBoom }

func TestTieredConformance(t *testing.T) {
	tr, err := NewTiered(discard(), nil, NewMemory(), NewMemory())
	if err != nil {
		t.Fatal(err)
	}
	conformance(t, tr)
	if tr.Name() != "tiered(memory,memory)" {
		t.Fatal(tr.Name())
	}
}

func TestTieredFallbackAndObserver(t *testing.T) {
	if _, err := NewTiered(discard(), nil); err == nil {
		t.Fatal("expected error without tiers")
	}

	type event struct {
		tier, op string
		failed   bool
	}
	var events []event
	obs := func(tier, op string, err error) { events = append(events, event{tier, op, err != nil}) }

	mem := NewMemory()
	tr, _ := NewTiered(discard(), obs, failing{"sqlite"}, mem)
	ctx := t.Context()

	if err := tr.Save(ctx, []stats.Snapshot{sn("ns", "a", 0, 1)}); err != nil {
		t.Fatalf("save must succeed when one tier works: %v", err)
	}
	latest, err := tr.Latest(ctx)
	if err != nil || len(latest) != 1 {
		t.Fatalf("latest fallback = %v, %v", latest, err)
	}
	if h, err := tr.History(ctx, "ns/a", time.Time{}); err != nil || len(h) != 1 {
		t.Fatalf("history fallback = %v, %v", h, err)
	}
	if err := tr.Ping(ctx); err != nil {
		t.Fatalf("ping must succeed: %v", err)
	}
	if err := tr.Prune(ctx, base.Add(-time.Hour)); err != nil {
		t.Fatalf("prune must succeed: %v", err)
	}
	if len(events) == 0 || events[0] != (event{"sqlite", "save", true}) || events[1] != (event{"memory", "save", false}) {
		t.Fatalf("events = %+v", events)
	}
	if err := tr.Close(); !errors.Is(err, errBoom) {
		t.Fatalf("close = %v", err)
	}

	allBad, _ := NewTiered(discard(), nil, failing{"a"}, failing{"b"})
	if err := allBad.Save(ctx, nil); !errors.Is(err, errBoom) {
		t.Errorf("save all failing = %v", err)
	}
	if _, err := allBad.Latest(ctx); !errors.Is(err, errBoom) {
		t.Errorf("latest all failing = %v", err)
	}
	if err := allBad.Ping(ctx); !errors.Is(err, errBoom) {
		t.Errorf("ping all failing = %v", err)
	}

	// Empty everywhere: no error, no data.
	empty, _ := NewTiered(discard(), nil, NewMemory(), NewMemory())
	if res, err := empty.Latest(ctx); err != nil || res != nil {
		t.Errorf("empty = %v, %v", res, err)
	}
}

func TestTieredWarm(t *testing.T) {
	ctx := t.Context()
	full, empty := NewMemory(), NewMemory()
	full.Save(ctx, []stats.Snapshot{sn("ns", "a", 0, 1), sn("ns", "a", 1, 2)})

	tr, _ := NewTiered(discard(), nil, failing{"broken"}, empty, full)
	if err := tr.Warm(ctx, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if all, _ := empty.All(ctx, time.Time{}); len(all) != 2 {
		t.Fatalf("warmed tier has %d snapshots", len(all))
	}

	// Nothing to warm from.
	nothing, _ := NewTiered(discard(), nil, NewMemory())
	if err := nothing.Warm(ctx, time.Time{}); err != nil {
		t.Fatal(err)
	}

	// Warm into a closed tier reports the error.
	closed := NewMemory()
	closed.Close()
	src := NewMemory()
	src.Save(ctx, []stats.Snapshot{sn("ns", "a", 0, 1)})
	bad, _ := NewTiered(discard(), nil, src, emptyButFailingSave{closed})
	if err := bad.Warm(ctx, time.Time{}); err == nil {
		t.Fatal("expected warm error")
	}
}

// emptyButFailingSave reports no data but cannot be written.
type emptyButFailingSave struct{ *Memory }

func (emptyButFailingSave) All(context.Context, time.Time) ([]stats.Snapshot, error) { return nil, nil }
