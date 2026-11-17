package collector

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aeciopires/docker-hub-image-statistics/internal/dockerhub"
	"github.com/aeciopires/docker-hub-image-statistics/internal/stats"
	"github.com/aeciopires/docker-hub-image-statistics/internal/store"
)

type fakeSource struct {
	mu      sync.Mutex
	pulls   int64
	limited bool
	listErr error
	getErr  error
	calls   atomic.Int32
}

func (f *fakeSource) ListRepositories(_ context.Context, ns string) ([]dockerhub.Repository, error) {
	f.calls.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listErr != nil {
		return nil, f.listErr
	}
	repos := []dockerhub.Repository{
		{Namespace: ns, Name: "api", PullCount: f.pulls, StarCount: 1},
		{Namespace: ns, Name: "api-dev", PullCount: 1},
		{Namespace: ns, Name: "web", PullCount: f.pulls / 2},
	}
	if f.limited {
		return repos, &dockerhub.PaginationLimitError{Namespace: ns, Fetched: len(repos)}
	}
	return repos, nil
}

func (f *fakeSource) GetRepository(_ context.Context, ns, name string) (dockerhub.Repository, error) {
	f.calls.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.getErr != nil {
		return dockerhub.Repository{}, f.getErr
	}
	return dockerhub.Repository{Namespace: ns, Name: name, PullCount: f.pulls * 10}, nil
}

type recorder struct {
	mu     sync.Mutex
	images []int
	errs   []error
}

func (r *recorder) ObserveCollection(_ time.Duration, images int, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.images = append(r.images, images)
	r.errs = append(r.errs, err)
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time      { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) Add(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

func newService(t *testing.T, src Source, st store.Store, mutate func(*Options)) (*Service, *clock, *recorder) {
	t.Helper()
	clk := &clock{t: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)}
	rec := &recorder{}
	opts := Options{
		Namespaces:   []string{"acme"},
		Repositories: []string{"nginx"},
		Exclude:      []string{"*/*-dev"},
		Interval:     time.Hour,
		Retention:    90 * 24 * time.Hour,
		Concurrency:  2,
		Recorder:     rec,
		Now:          clk.Now,
	}
	if mutate != nil {
		mutate(&opts)
	}
	s, err := New(src, st, opts)
	if err != nil {
		t.Fatal(err)
	}
	return s, clk, rec
}

func TestNewValidation(t *testing.T) {
	src, st := &fakeSource{}, store.NewMemory()
	cases := map[string]struct {
		src  Source
		st   store.Store
		opts Options
	}{
		"nil source":  {nil, st, Options{Interval: time.Hour, Namespaces: []string{"a"}}},
		"nil store":   {src, nil, Options{Interval: time.Hour, Namespaces: []string{"a"}}},
		"no interval": {src, st, Options{Namespaces: []string{"a"}}},
		"no targets":  {src, st, Options{Interval: time.Hour}},
	}
	for name, c := range cases {
		if _, err := New(c.src, c.st, c.opts); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
	s, err := New(src, st, Options{Interval: time.Hour, Namespaces: []string{"a"}})
	if err != nil {
		t.Fatal(err)
	}
	if s.opts.Logger == nil || s.opts.Recorder == nil || s.opts.Tracer == nil || s.opts.Now == nil {
		t.Fatal("defaults not applied")
	}
	s.opts.Recorder.ObserveCollection(0, 0, nil) // noop recorder
}

func TestCollectComputesWindows(t *testing.T) {
	src := &fakeSource{pulls: 1000}
	st := store.NewMemory()
	s, clk, rec := newService(t, src, st, nil)
	ctx := t.Context()

	if err := s.Collect(ctx); err != nil {
		t.Fatal(err)
	}
	clk.Add(25 * time.Hour)
	src.mu.Lock()
	src.pulls = 1600
	src.mu.Unlock()
	if err := s.Collect(ctx); err != nil {
		t.Fatal(err)
	}

	images := s.Images()
	if len(images) != 3 {
		t.Fatalf("images = %+v (api-dev must be excluded)", images)
	}
	if images[0].Key() != "library/nginx" || images[0].PullsTotal != 16000 {
		t.Fatalf("first image = %+v", images[0])
	}
	api, ok := s.Image("acme", "api")
	if !ok || api.Pulls["1d"] != (stats.WindowPulls{Pulls: 600, Complete: true}) {
		t.Fatalf("api = %+v", api)
	}
	if _, ok := s.Image("acme", "missing"); ok {
		t.Fatal("missing image must not be found")
	}
	sum := s.Summary()
	if sum.Images != 3 || sum.PullsTotal != 16000+1600+800 {
		t.Fatalf("summary = %+v", sum)
	}

	hist, err := s.History(ctx, "acme", "api", time.Time{})
	if err != nil || len(hist) != 2 {
		t.Fatalf("history = %+v, %v", hist, err)
	}
	if hist, _ := s.History(ctx, "other", "x", time.Time{}); hist != nil {
		t.Fatal("unconfigured images must not expose history")
	}

	status := s.Status()
	if status.Images != 3 || status.LastError != "" || status.LastSuccess.IsZero() || status.Interval != "1h0m0s" {
		t.Fatalf("status = %+v", status)
	}
	if len(rec.images) != 2 || rec.images[1] != 3 || rec.errs[1] != nil {
		t.Fatalf("recorder = %+v", rec)
	}
	if err := s.Ready(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestCollectPartialFailure(t *testing.T) {
	src := &fakeSource{pulls: 10, getErr: errors.New("nginx exploded")}
	s, _, rec := newService(t, src, store.NewMemory(), nil)
	err := s.Collect(t.Context())
	if err == nil || !strings.Contains(err.Error(), "nginx exploded") {
		t.Fatalf("err = %v", err)
	}
	if len(s.Images()) != 2 {
		t.Fatalf("partial results must be stored, got %d", len(s.Images()))
	}
	if st := s.Status(); st.LastError == "" || st.LastSuccess.IsZero() {
		t.Fatalf("status = %+v", st)
	}
	if rec.errs[0] == nil {
		t.Fatal("recorder must see the error")
	}
}

func TestCollectNothingDiscovered(t *testing.T) {
	src := &fakeSource{listErr: errors.New("down"), getErr: errors.New("down")}
	s, _, _ := newService(t, src, store.NewMemory(), nil)
	if err := s.Collect(t.Context()); err == nil {
		t.Fatal("expected error")
	}
	if !s.Status().LastSuccess.IsZero() {
		t.Fatal("no success expected")
	}

	ok := &fakeSource{}
	s2, _, _ := newService(t, ok, store.NewMemory(), func(o *Options) {
		o.Include = []string{"nothing/*"}
	})
	if err := s2.Collect(t.Context()); err == nil || !strings.Contains(err.Error(), "no public images") {
		t.Fatalf("err = %v", err)
	}
}

func TestCollectStoreFailure(t *testing.T) {
	st := store.NewMemory()
	st.Close()
	s, _, _ := newService(t, &fakeSource{pulls: 1}, st, nil)
	err := s.Collect(t.Context())
	if err == nil || !strings.Contains(err.Error(), "save snapshots") {
		t.Fatalf("err = %v", err)
	}
	if err := s.Reload(t.Context()); err == nil {
		t.Fatal("reload must fail on closed store")
	}
	if err := s.Ready(t.Context()); err == nil {
		t.Fatal("ready must fail on closed store")
	}
}

func TestDiscoverCanceled(t *testing.T) {
	src := &fakeSource{pulls: 1}
	s, _, _ := newService(t, src, store.NewMemory(), func(o *Options) {
		o.Concurrency = 1
		o.Namespaces = []string{"a", "b", "c"}
	})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := s.discover(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

func TestRunCollectsOnStartAndOnTick(t *testing.T) {
	src := &fakeSource{pulls: 5}
	st := store.NewMemory()
	s, _, _ := newService(t, src, st, func(o *Options) {
		o.RunOnStart = true
		o.Interval = 20 * time.Millisecond
		o.Now = time.Now
	})
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()

	deadline := time.After(5 * time.Second)
	for src.calls.Load() < 4 { // >= 2 collections (2 calls each)
		select {
		case <-deadline:
			t.Fatal("collector did not tick")
		case <-time.After(5 * time.Millisecond):
		}
	}
	cancel()
	<-done
	if s.Status().NextRun.IsZero() {
		t.Fatal("next run must be set")
	}
}

func TestRunReloadFailureAndErrorLogging(t *testing.T) {
	st := store.NewMemory()
	st.Close()
	s, _, _ := newService(t, &fakeSource{listErr: errors.New("x"), getErr: errors.New("y")}, st, func(o *Options) {
		o.RunOnStart = true
	})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	s.Run(ctx) // must return promptly
}

func TestSplitReference(t *testing.T) {
	if ns, n := SplitReference("nginx"); ns != "library" || n != "nginx" {
		t.Fatal(ns, n)
	}
	if ns, n := SplitReference("acme/api"); ns != "acme" || n != "api" {
		t.Fatal(ns, n)
	}
}

func TestConfiguredHonorsConfigChanges(t *testing.T) {
	st := store.NewMemory()
	st.Save(t.Context(), []stats.Snapshot{
		{Namespace: "old", Name: "gone", PullCount: 1, CollectedAt: time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC)},
	})
	s, _, _ := newService(t, &fakeSource{}, st, nil)
	if err := s.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(s.Images()) != 0 {
		t.Fatal("images outside the configuration must be hidden")
	}
}

func TestReloadRestoresLastSuccess(t *testing.T) {
	st := store.NewMemory()
	at := time.Date(2026, 10, 31, 23, 0, 0, 0, time.UTC)
	st.Save(t.Context(), []stats.Snapshot{{Namespace: "acme", Name: "api", PullCount: 1, CollectedAt: at}})
	s, _, _ := newService(t, &fakeSource{}, st, nil)
	if err := s.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := s.Status(); !got.LastSuccess.Equal(at) || got.Images != 1 {
		t.Fatalf("status after reload = %+v", got)
	}
}

func TestPaginationLimitIsAWarning(t *testing.T) {
	src := &fakeSource{pulls: 10, limited: true}
	s, _, _ := newService(t, src, store.NewMemory(), nil)
	if err := s.Collect(t.Context()); err != nil {
		t.Fatalf("pagination limit must not fail the collection: %v", err)
	}
	st := s.Status()
	if len(st.Warnings) != 1 || !strings.Contains(st.Warnings[0], "anonymous") || st.LastError != "" {
		t.Fatalf("status = %+v", st)
	}
	if len(s.Images()) != 3 {
		t.Fatalf("images = %d", len(s.Images()))
	}
}

func TestFirstRunKeepsCadenceAfterRestart(t *testing.T) {
	st := store.NewMemory()
	last := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	st.Save(t.Context(), []stats.Snapshot{
		{Namespace: "acme", Name: "api", PullCount: 1, CollectedAt: last},
		{Namespace: "library", Name: "nginx", PullCount: 1, CollectedAt: last},
	})

	cases := []struct {
		name  string
		opts  func(*Options)
		after time.Duration // clock offset from the cached collection
		want  time.Duration // expected first run, relative to the cached collection
	}{
		{"recent cache keeps cadence", func(o *Options) { o.RunOnStart = true }, 10 * time.Minute, time.Hour},
		{"old cache collects now", func(o *Options) { o.RunOnStart = true }, 2 * time.Hour, 2 * time.Hour},
		{"forced collects now", func(o *Options) { o.ForceCollect = true }, 10 * time.Minute, 10 * time.Minute},
		{"no run on start waits an interval", func(*Options) {}, 10 * time.Minute, 70 * time.Minute},
		{"new repository collects now", func(o *Options) {
			o.RunOnStart = true
			o.Repositories = []string{"nginx", "alpine"}
		}, 10 * time.Minute, 10 * time.Minute},
		{"new namespace collects now", func(o *Options) {
			o.RunOnStart = true
			o.Namespaces = []string{"acme", "newcorp"}
		}, 10 * time.Minute, 10 * time.Minute},
		{"excluded repository is not missing", func(o *Options) {
			o.RunOnStart = true
			o.Repositories = []string{"nginx", "acme/api-dev"}
		}, 10 * time.Minute, time.Hour},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, clk, _ := newService(t, &fakeSource{}, st, tc.opts)
			clk.mu.Lock()
			clk.t = last.Add(tc.after)
			clk.mu.Unlock()
			if err := s.Reload(t.Context()); err != nil {
				t.Fatal(err)
			}
			if got := s.firstRun(); !got.Equal(last.Add(tc.want)) {
				t.Fatalf("first run = %v, want %v", got, last.Add(tc.want))
			}
		})
	}
}

func TestRunWaitsForScheduledCollection(t *testing.T) {
	src := &fakeSource{pulls: 1}
	st := store.NewMemory()
	st.Save(t.Context(), []stats.Snapshot{
		{Namespace: "acme", Name: "api", PullCount: 1, CollectedAt: time.Now()},
		{Namespace: "library", Name: "nginx", PullCount: 1, CollectedAt: time.Now()},
	})
	s, _, _ := newService(t, src, st, func(o *Options) {
		o.RunOnStart = true
		o.Interval = time.Hour
		o.Now = time.Now
	})
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	deadline := time.Now().Add(2 * time.Second)
	for s.Status().NextRun.IsZero() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done
	if src.calls.Load() != 0 {
		t.Fatalf("a recent cached collection must not trigger a new one (calls=%d)", src.calls.Load())
	}
	if next := s.Status().NextRun; time.Until(next) < 50*time.Minute {
		t.Fatalf("next run = %v, want about one hour from the cached collection", next)
	}
}
