// Package stats holds the domain model and the pull-window calculations.
//
// Docker Hub only exposes the cumulative pull counter of an image. Pulls in a
// window (1d, 7d, 30d) are derived from the difference between the newest
// snapshot and the newest snapshot taken at or before the window start.
package stats

import (
	"cmp"
	"slices"
	"time"
)

// Window is a named lookback period.
type Window struct {
	Name     string
	Duration time.Duration
}

// Windows are the supported lookback periods, shortest first.
var Windows = []Window{
	{Name: "1d", Duration: 24 * time.Hour},
	{Name: "7d", Duration: 7 * 24 * time.Hour},
	{Name: "30d", Duration: 30 * 24 * time.Hour},
}

// Snapshot is one observation of an image's public counters.
type Snapshot struct {
	Namespace   string    `json:"namespace"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	PullCount   int64     `json:"pull_count"`
	StarCount   int64     `json:"star_count"`
	LastUpdated time.Time `json:"last_updated,omitzero"`
	CollectedAt time.Time `json:"collected_at"`
}

// Key returns the canonical "namespace/name" identifier.
func (s Snapshot) Key() string { return Key(s.Namespace, s.Name) }

// Key builds the canonical "namespace/name" identifier.
func Key(namespace, name string) string { return namespace + "/" + name }

// WindowPulls is the number of pulls inside one window.
type WindowPulls struct {
	Pulls int64 `json:"pulls"`
	// Complete is false while the history does not yet cover the whole window;
	// Pulls is then a lower bound computed from the oldest snapshot available.
	Complete bool `json:"complete"`
}

// ImageStats is the computed view of one image.
type ImageStats struct {
	Namespace    string                 `json:"namespace"`
	Name         string                 `json:"name"`
	Description  string                 `json:"description,omitempty"`
	PullsTotal   int64                  `json:"pulls_total"`
	Pulls        map[string]WindowPulls `json:"pulls"`
	Stars        int64                  `json:"stars"`
	LastUpdated  time.Time              `json:"last_updated,omitzero"`
	CollectedAt  time.Time              `json:"collected_at"`
	HistorySince time.Time              `json:"history_since"`
	// LastCollection is the increase between the two newest snapshots.
	LastCollection LastCollection `json:"last_collection"`
}

// LastCollection describes the pulls observed in the newest collection.
type LastCollection struct {
	// Pulls is the increase since the previous collection (0 for the first one).
	Pulls int64 `json:"pulls"`
	// PreviousAt is when the previous snapshot was taken (zero for the first one).
	PreviousAt time.Time `json:"previous_at,omitzero"`
}

// Key returns the canonical "namespace/name" identifier.
func (s ImageStats) Key() string { return Key(s.Namespace, s.Name) }

// Compute derives ImageStats from the history of one image (any order).
// It returns false when history is empty.
func Compute(history []Snapshot) (ImageStats, bool) {
	if len(history) == 0 {
		return ImageStats{}, false
	}
	h := slices.SortedFunc(slices.Values(history), func(a, b Snapshot) int {
		return a.CollectedAt.Compare(b.CollectedAt)
	})
	latest := h[len(h)-1]
	oldest := h[0]

	out := ImageStats{
		Namespace:    latest.Namespace,
		Name:         latest.Name,
		Description:  latest.Description,
		PullsTotal:   latest.PullCount,
		Pulls:        make(map[string]WindowPulls, len(Windows)),
		Stars:        latest.StarCount,
		LastUpdated:  latest.LastUpdated,
		CollectedAt:  latest.CollectedAt,
		HistorySince: oldest.CollectedAt,
	}
	for _, w := range Windows {
		out.Pulls[w.Name] = windowPulls(h, latest, w.Duration)
	}
	if len(h) > 1 {
		prev := h[len(h)-2]
		out.LastCollection = LastCollection{Pulls: max(0, latest.PullCount-prev.PullCount), PreviousAt: prev.CollectedAt}
	}
	return out, true
}

// windowPulls computes the pulls inside a window over an ascending history.
func windowPulls(asc []Snapshot, latest Snapshot, d time.Duration) WindowPulls {
	start := latest.CollectedAt.Add(-d)
	// Index of the first snapshot strictly after start.
	i, _ := slices.BinarySearchFunc(asc, start, func(s Snapshot, t time.Time) int {
		if s.CollectedAt.After(t) {
			return 1
		}
		return -1
	})
	baseline, complete := asc[0], false
	if i > 0 {
		baseline, complete = asc[i-1], true
	}
	// Counters never go down; clamp resets (e.g. a deleted and recreated repo).
	return WindowPulls{Pulls: max(0, latest.PullCount-baseline.PullCount), Complete: complete}
}

// ComputeAll groups snapshots by image and computes the stats of each,
// ordered by total pulls (descending) then key.
func ComputeAll(snapshots []Snapshot) []ImageStats {
	groups := make(map[string][]Snapshot)
	for _, s := range snapshots {
		groups[s.Key()] = append(groups[s.Key()], s)
	}
	out := make([]ImageStats, 0, len(groups))
	for _, g := range groups {
		if st, ok := Compute(g); ok {
			out = append(out, st)
		}
	}
	SortByPulls(out)
	return out
}

// SortByPulls orders images by total pulls (descending), then by key.
func SortByPulls(images []ImageStats) {
	slices.SortFunc(images, func(a, b ImageStats) int {
		return cmp.Or(cmp.Compare(b.PullsTotal, a.PullsTotal), cmp.Compare(a.Key(), b.Key()))
	})
}

// Summary aggregates every image.
type Summary struct {
	Images     int              `json:"images"`
	PullsTotal int64            `json:"pulls_total"`
	Pulls      map[string]int64 `json:"pulls"`
	// PullsLastCollection sums the pulls observed in the newest collection of each image.
	PullsLastCollection int64 `json:"pulls_last_collection"`
	Stars               int64 `json:"stars"`
	// CollectedAt is the most recent collection time across images.
	CollectedAt time.Time `json:"collected_at,omitzero"`
}

// Summarize aggregates a set of images.
func Summarize(images []ImageStats) Summary {
	s := Summary{Images: len(images), Pulls: make(map[string]int64, len(Windows))}
	for _, w := range Windows {
		s.Pulls[w.Name] = 0
	}
	for _, img := range images {
		s.PullsTotal += img.PullsTotal
		s.PullsLastCollection += img.LastCollection.Pulls
		s.Stars += img.Stars
		for name, wp := range img.Pulls {
			s.Pulls[name] += wp.Pulls
		}
		if img.CollectedAt.After(s.CollectedAt) {
			s.CollectedAt = img.CollectedAt
		}
	}
	return s
}

// Point is one entry of an image time series.
type Point struct {
	Time time.Time `json:"time"`
	// PullsTotal is the cumulative counter at Time.
	PullsTotal int64 `json:"pulls_total"`
	// Pulls is the number of pulls since the previous point (0 for the first).
	Pulls int64 `json:"pulls"`
	Stars int64 `json:"stars"`
}

// Series converts snapshots (any order) into points bucketed by step (for
// example one hour). The newest snapshot of each bucket represents it, and
// Pulls is the increase compared with the previous bucket.
func Series(history []Snapshot, step time.Duration) []Point {
	if len(history) == 0 {
		return []Point{}
	}
	if step <= 0 {
		step = time.Hour
	}
	asc := slices.SortedFunc(slices.Values(history), func(a, b Snapshot) int {
		return a.CollectedAt.Compare(b.CollectedAt)
	})
	points := make([]Point, 0, len(asc))
	for _, s := range asc {
		bucket := s.CollectedAt.UTC().Truncate(step)
		p := Point{Time: bucket, PullsTotal: s.PullCount, Stars: s.StarCount}
		if n := len(points); n > 0 && points[n-1].Time.Equal(bucket) {
			points[n-1] = p // keep the newest snapshot of the bucket
			continue
		}
		points = append(points, p)
	}
	for i := 1; i < len(points); i++ {
		points[i].Pulls = max(0, points[i].PullsTotal-points[i-1].PullsTotal)
	}
	return points
}
