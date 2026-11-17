// Package collector discovers images, polls Docker Hub on a schedule, stores
// snapshots and keeps the computed statistics ready for readers.
package collector

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"path"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/aeciopires/docker-hub-image-statistics/internal/dockerhub"
	"github.com/aeciopires/docker-hub-image-statistics/internal/stats"
	"github.com/aeciopires/docker-hub-image-statistics/internal/store"
)

const tracerName = "github.com/aeciopires/docker-hub-image-statistics/internal/collector"

// Source is the subset of the Docker Hub client used by the collector.
type Source interface {
	ListRepositories(ctx context.Context, namespace string) ([]dockerhub.Repository, error)
	GetRepository(ctx context.Context, namespace, name string) (dockerhub.Repository, error)
}

// Recorder receives collection outcomes (implemented by the metrics package).
type Recorder interface {
	ObserveCollection(duration time.Duration, images int, err error)
}

type noopRecorder struct{}

func (noopRecorder) ObserveCollection(time.Duration, int, error) {}

// Options configures the Service.
type Options struct {
	Namespaces   []string
	Repositories []string
	Include      []string
	Exclude      []string
	Interval     time.Duration
	Retention    time.Duration
	// RunOnStart collects at startup unless the cached data is newer than
	// Interval (restarts and reloads keep the original cadence).
	RunOnStart bool
	// ForceCollect collects at startup even when the cached data is recent
	// (used when a reload changes the discovery targets).
	ForceCollect bool
	Concurrency  int
	Logger       *slog.Logger
	Recorder     Recorder
	Tracer       trace.Tracer
	// Now is injectable for tests.
	Now func() time.Time
}

// Status describes the collector state.
type Status struct {
	Collecting   bool      `json:"collecting"`
	LastAttempt  time.Time `json:"last_attempt,omitzero"`
	LastSuccess  time.Time `json:"last_success,omitzero"`
	LastError    string    `json:"last_error,omitempty"`
	Warnings     []string  `json:"warnings,omitempty"`
	LastDuration string    `json:"last_duration,omitempty"`
	Images       int       `json:"images"`
	Interval     string    `json:"interval"`
	NextRun      time.Time `json:"next_run,omitzero"`
}

// view is an immutable, precomputed snapshot of statistics.
type view struct {
	images  []stats.ImageStats
	byKey   map[string]int
	summary stats.Summary
}

// Service orchestrates discovery, collection and statistics.
//
// Analogy: a newspaper delivery person who, every hour, walks the route
// (discovery), writes down the counter shown on each house (snapshot), files
// the notes (store) and keeps an up-to-date summary on the fridge (view).
type Service struct {
	src   Source
	store store.Store
	opts  Options

	current atomic.Pointer[view]
	running atomic.Bool
	runMu   sync.Mutex // one collection at a time

	statusMu sync.RWMutex
	status   Status
}

// New builds a Service.
func New(src Source, st store.Store, opts Options) (*Service, error) {
	if src == nil || st == nil {
		return nil, errors.New("collector: source and store are required")
	}
	if opts.Interval <= 0 {
		return nil, errors.New("collector: interval must be positive")
	}
	if len(opts.Namespaces) == 0 && len(opts.Repositories) == 0 {
		return nil, errors.New("collector: configure at least one namespace or repository")
	}
	opts.Concurrency = max(1, opts.Concurrency)
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	if opts.Recorder == nil {
		opts.Recorder = noopRecorder{}
	}
	if opts.Tracer == nil {
		opts.Tracer = otel.Tracer(tracerName)
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	s := &Service{src: src, store: st, opts: opts}
	s.current.Store(&view{byKey: map[string]int{}, summary: stats.Summarize(nil)})
	s.status.Interval = opts.Interval.String()
	return s, nil
}

// Run reloads cached data, optionally collects immediately and then collects
// every interval until ctx is done.
func (s *Service) Run(ctx context.Context) {
	if err := s.Reload(ctx); err != nil {
		s.opts.Logger.WarnContext(ctx, "could not load cached statistics", "error", err)
	}
	next := s.firstRun()
	for {
		s.setNextRun(next)
		if wait := next.Sub(s.opts.Now()); wait > 0 {
			timer := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		} else if ctx.Err() != nil {
			return
		}
		s.collectAndLog(ctx)
		next = s.opts.Now().Add(s.opts.Interval)
	}
}

// firstRun decides when the first collection happens. Collecting right away
// after every restart or reload would create snapshots seconds apart (wasting
// the Docker Hub rate limit and zeroing the "last collection" pulls), so a
// recent cached collection keeps the original cadence.
func (s *Service) firstRun() time.Time {
	now := s.opts.Now()
	switch {
	case s.opts.ForceCollect:
		return now
	case !s.opts.RunOnStart:
		return now.Add(s.opts.Interval)
	}
	if missing := s.missingTargets(); missing != "" {
		s.opts.Logger.Info("configured target has no cached data, collecting now", "target", missing)
		return now
	}
	s.statusMu.RLock()
	last := s.status.LastSuccess
	s.statusMu.RUnlock()
	if !last.IsZero() && now.Sub(last) < s.opts.Interval {
		s.opts.Logger.Info("recent collection found in the cache, keeping the schedule",
			"last_collection", last, "next_collection", last.Add(s.opts.Interval))
		return last.Add(s.opts.Interval)
	}
	return now
}

func (s *Service) collectAndLog(ctx context.Context) {
	if err := s.Collect(ctx); err != nil && ctx.Err() == nil {
		s.opts.Logger.ErrorContext(ctx, "collection finished with errors", "error", err)
	}
}

func (s *Service) setNextRun(t time.Time) {
	s.statusMu.Lock()
	s.status.NextRun = t.UTC()
	s.statusMu.Unlock()
}

// Collect performs one discovery + snapshot cycle. Partial failures still
// store what was collected; the returned error joins every failure.
func (s *Service) Collect(ctx context.Context) (err error) {
	s.runMu.Lock()
	defer s.runMu.Unlock()
	s.running.Store(true)
	defer s.running.Store(false)

	ctx, span := s.opts.Tracer.Start(ctx, "collector.collect")
	defer span.End()

	start := s.opts.Now()
	s.statusMu.Lock()
	s.status.LastAttempt = start.UTC()
	s.statusMu.Unlock()

	repos, discoverErr := s.discover(ctx)
	collectedAt := start.UTC().Truncate(time.Second)
	snaps := make([]stats.Snapshot, 0, len(repos))
	for _, r := range repos {
		snaps = append(snaps, r.Snapshot(collectedAt))
	}
	span.SetAttributes(attribute.Int("dockerhub.images", len(snaps)))

	var saveErr error
	if len(snaps) > 0 {
		if saveErr = s.store.Save(ctx, snaps); saveErr != nil {
			saveErr = fmt.Errorf("save snapshots: %w", saveErr)
		}
	}
	if err := s.store.Prune(ctx, collectedAt.Add(-s.opts.Retention)); err != nil {
		s.opts.Logger.WarnContext(ctx, "prune failed", "error", err)
	}
	reloadErr := s.Reload(ctx)

	err = errors.Join(discoverErr, saveErr, reloadErr)
	if len(snaps) == 0 && err == nil {
		err = errors.New("no public images discovered")
	}
	duration := s.opts.Now().Sub(start)
	s.opts.Recorder.ObserveCollection(duration, len(snaps), err)

	s.statusMu.Lock()
	s.status.LastDuration = duration.Round(time.Millisecond).String()
	s.status.Images = len(s.current.Load().images)
	if err != nil {
		s.status.LastError = err.Error()
	} else {
		s.status.LastError = ""
	}
	if saveErr == nil && len(snaps) > 0 {
		s.status.LastSuccess = collectedAt
	}
	s.statusMu.Unlock()

	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "collection failed")
	}
	s.opts.Logger.InfoContext(ctx, "collection finished",
		"images", len(snaps), "duration", duration.String(), "errors", err != nil)
	return err
}

// discover lists every namespace and fetches every explicit repository.
func (s *Service) discover(ctx context.Context) ([]dockerhub.Repository, error) {
	ctx, span := s.opts.Tracer.Start(ctx, "collector.discover")
	defer span.End()

	var (
		mu       sync.Mutex
		found    = make(map[string]dockerhub.Repository)
		errs     []error
		warnings []string
		wg       sync.WaitGroup
		sem      = make(chan struct{}, s.opts.Concurrency)
	)
	add := func(repos []dockerhub.Repository, err error) {
		mu.Lock()
		defer mu.Unlock()
		if limit, ok := errors.AsType[*dockerhub.PaginationLimitError](err); ok {
			// Not a failure: the visible (most pulled) images were collected.
			warnings = append(warnings, limit.Error())
			s.opts.Logger.WarnContext(ctx, "anonymous listing limit reached", "namespace", limit.Namespace, "images", limit.Fetched)
		} else if err != nil {
			errs = append(errs, err)
		}
		for _, r := range repos {
			found[stats.Key(r.Namespace, r.Name)] = r
		}
	}
	acquire := func() bool {
		// select picks randomly among ready cases: check cancellation first so a
		// canceled context never starts new requests.
		if err := context.Cause(ctx); err != nil {
			add(nil, err)
			return false
		}
		select {
		case sem <- struct{}{}:
			return true
		case <-ctx.Done():
			add(nil, context.Cause(ctx))
			return false
		}
	}

	for _, ns := range s.opts.Namespaces {
		wg.Go(func() {
			if !acquire() {
				return
			}
			defer func() { <-sem }()
			repos, err := s.src.ListRepositories(ctx, ns)
			s.opts.Logger.DebugContext(ctx, "namespace discovered", "namespace", ns, "images", len(repos), "error", err)
			add(repos, err)
		})
	}
	for _, ref := range s.opts.Repositories {
		ns, name := SplitReference(ref)
		wg.Go(func() {
			if !acquire() {
				return
			}
			defer func() { <-sem }()
			repo, err := s.src.GetRepository(ctx, ns, name)
			if err != nil {
				add(nil, err)
				return
			}
			add([]dockerhub.Repository{repo}, nil)
		})
	}
	wg.Wait()

	out := make([]dockerhub.Repository, 0, len(found))
	for _, key := range slices.Sorted(maps.Keys(found)) {
		if s.allowed(key) {
			out = append(out, found[key])
		}
	}
	slices.Sort(warnings)
	s.statusMu.Lock()
	s.status.Warnings = warnings
	s.statusMu.Unlock()

	err := errors.Join(errs...)
	if err != nil {
		span.RecordError(err)
	}
	return out, err
}

// SplitReference turns "ns/name" or "name" (official image) into parts.
func SplitReference(ref string) (namespace, name string) {
	if ns, n, ok := strings.Cut(ref, "/"); ok {
		return ns, n
	}
	return "library", ref
}

// allowed applies the include/exclude globs.
func (s *Service) allowed(key string) bool {
	match := func(patterns []string) bool {
		return slices.ContainsFunc(patterns, func(p string) bool {
			ok, _ := path.Match(p, key)
			return ok
		})
	}
	if len(s.opts.Include) > 0 && !match(s.opts.Include) {
		return false
	}
	return !match(s.opts.Exclude)
}

// missingTargets returns the first configured namespace or repository without
// cached statistics (for example one added by a reload or a Helm upgrade), or "".
func (s *Service) missingTargets() string {
	v := s.current.Load()
	for _, ref := range s.opts.Repositories {
		ns, name := SplitReference(ref)
		if _, ok := v.byKey[stats.Key(ns, name)]; !ok && s.allowed(stats.Key(ns, name)) {
			return ref
		}
	}
	for _, ns := range s.opts.Namespaces {
		if !slices.ContainsFunc(v.images, func(img stats.ImageStats) bool { return img.Namespace == ns }) {
			return ns
		}
	}
	return ""
}

// configured reports whether an image belongs to the current configuration
// (so images removed from the config disappear from the views).
func (s *Service) configured(namespace, name string) bool {
	key := stats.Key(namespace, name)
	if !s.allowed(key) {
		return false
	}
	if slices.Contains(s.opts.Namespaces, namespace) {
		return true
	}
	return slices.ContainsFunc(s.opts.Repositories, func(ref string) bool {
		ns, n := SplitReference(ref)
		return ns == namespace && n == name
	})
}

// Reload recomputes the statistics from the store.
func (s *Service) Reload(ctx context.Context) error {
	ctx, span := s.opts.Tracer.Start(ctx, "collector.reload")
	defer span.End()

	// Enough history to find the 30d baseline even with a missed collection.
	lookback := stats.Windows[len(stats.Windows)-1].Duration + max(2*s.opts.Interval, 24*time.Hour)
	all, err := s.store.All(ctx, s.opts.Now().Add(-lookback))
	if err != nil {
		span.RecordError(err)
		return fmt.Errorf("load snapshots: %w", err)
	}
	all = slices.DeleteFunc(all, func(sn stats.Snapshot) bool { return !s.configured(sn.Namespace, sn.Name) })

	images := stats.ComputeAll(all)
	v := &view{images: images, byKey: make(map[string]int, len(images)), summary: stats.Summarize(images)}
	for i, img := range images {
		v.byKey[img.Key()] = i
	}
	s.current.Store(v)

	// After a restart, the newest cached snapshot tells when data was last collected.
	s.statusMu.Lock()
	if v.summary.CollectedAt.After(s.status.LastSuccess) {
		s.status.LastSuccess = v.summary.CollectedAt
	}
	s.status.Images = len(images)
	s.statusMu.Unlock()
	return nil
}

// Images returns the statistics of every image (sorted by total pulls).
func (s *Service) Images() []stats.ImageStats { return slices.Clone(s.current.Load().images) }

// Image returns the statistics of one image.
func (s *Service) Image(namespace, name string) (stats.ImageStats, bool) {
	v := s.current.Load()
	i, ok := v.byKey[stats.Key(namespace, name)]
	if !ok {
		return stats.ImageStats{}, false
	}
	return v.images[i], true
}

// Summary returns aggregated statistics.
func (s *Service) Summary() stats.Summary { return s.current.Load().summary }

// History returns the stored snapshots of one image since a time.
func (s *Service) History(ctx context.Context, namespace, name string, since time.Time) ([]stats.Snapshot, error) {
	if !s.configured(namespace, name) {
		return nil, nil
	}
	return s.store.History(ctx, stats.Key(namespace, name), since)
}

// Status returns the collector state.
func (s *Service) Status() Status {
	s.statusMu.RLock()
	defer s.statusMu.RUnlock()
	st := s.status
	st.Warnings = slices.Clone(st.Warnings)
	st.Collecting = s.running.Load()
	return st
}

// Ready reports whether the storage can serve requests.
func (s *Service) Ready(ctx context.Context) error { return s.store.Ping(ctx) }
