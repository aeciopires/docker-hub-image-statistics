package stats

import (
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)

func snap(name string, at time.Duration, pulls int64) Snapshot {
	return Snapshot{Namespace: "ns", Name: name, PullCount: pulls, StarCount: 3, CollectedAt: t0.Add(at)}
}

func TestComputeEmpty(t *testing.T) {
	if _, ok := Compute(nil); ok {
		t.Fatal("expected ok=false for empty history")
	}
}

func TestComputeWindows(t *testing.T) {
	day := 24 * time.Hour
	// Unordered on purpose: Compute must sort.
	history := []Snapshot{
		snap("a", 40*day, 10_000),
		snap("a", 0, 1_000),
		snap("a", 9*day, 2_000),  // before 30d start (day 10)
		snap("a", 33*day, 5_000), // exactly 7d before latest
		snap("a", 39*day, 9_000), // exactly 1d before latest
		snap("a", 39*day+time.Hour, 9_100),
	}
	st, ok := Compute(history)
	if !ok {
		t.Fatal("expected stats")
	}
	if st.PullsTotal != 10_000 || st.Stars != 3 || st.Key() != "ns/a" {
		t.Fatalf("unexpected stats: %+v", st)
	}
	want := map[string]WindowPulls{
		"1d":  {Pulls: 1_000, Complete: true},
		"7d":  {Pulls: 5_000, Complete: true},
		"30d": {Pulls: 8_000, Complete: true},
	}
	for k, w := range want {
		if st.Pulls[k] != w {
			t.Errorf("%s = %+v, want %+v", k, st.Pulls[k], w)
		}
	}
	if st.LastCollection.Pulls != 900 || !st.LastCollection.PreviousAt.Equal(t0.Add(39*day+time.Hour)) {
		t.Errorf("last collection = %+v", st.LastCollection)
	}
	if !st.HistorySince.Equal(t0) || !st.CollectedAt.Equal(t0.Add(40*day)) {
		t.Errorf("history since %v collected %v", st.HistorySince, st.CollectedAt)
	}
}

func TestComputePartialHistoryAndReset(t *testing.T) {
	st, _ := Compute([]Snapshot{snap("a", 0, 500), snap("a", 2*time.Hour, 600)})
	for _, w := range Windows {
		if got := st.Pulls[w.Name]; got.Pulls != 100 || got.Complete {
			t.Errorf("%s = %+v, want partial 100", w.Name, got)
		}
	}
	// Counter reset: never negative.
	st, _ = Compute([]Snapshot{snap("a", 0, 500), snap("a", 25*time.Hour, 10)})
	if st.Pulls["1d"].Pulls != 0 {
		t.Errorf("reset must clamp to 0, got %d", st.Pulls["1d"].Pulls)
	}
	// Single snapshot.
	st, _ = Compute([]Snapshot{snap("a", 0, 7)})
	if st.Pulls["30d"] != (WindowPulls{Pulls: 0, Complete: false}) {
		t.Errorf("single snapshot = %+v", st.Pulls["30d"])
	}
	if st.LastCollection != (LastCollection{}) {
		t.Errorf("first collection must be zero, got %+v", st.LastCollection)
	}
}

func TestComputeAllAndSummarize(t *testing.T) {
	all := ComputeAll([]Snapshot{
		snap("small", 0, 10), snap("small", 25*time.Hour, 20),
		snap("big", 0, 100), snap("big", 25*time.Hour, 300),
		snap("tie", 25*time.Hour, 20),
	})
	if len(all) != 3 || all[0].Name != "big" || all[1].Name != "small" || all[2].Name != "tie" {
		t.Fatalf("unexpected order: %+v", all)
	}
	sum := Summarize(all)
	if sum.Images != 3 || sum.PullsTotal != 340 || sum.Stars != 9 {
		t.Fatalf("summary = %+v", sum)
	}
	if sum.PullsLastCollection != 210 {
		t.Fatalf("summary last collection = %d", sum.PullsLastCollection)
	}
	if sum.Pulls["1d"] != 210 || sum.Pulls["30d"] != 210 {
		t.Fatalf("summary windows = %+v", sum.Pulls)
	}
	if !sum.CollectedAt.Equal(t0.Add(25 * time.Hour)) {
		t.Fatalf("collected at = %v", sum.CollectedAt)
	}
	empty := Summarize(nil)
	if empty.Images != 0 || len(empty.Pulls) != len(Windows) {
		t.Fatalf("empty summary = %+v", empty)
	}
}

func TestSeries(t *testing.T) {
	if got := Series(nil, time.Hour); len(got) != 0 || got == nil {
		t.Fatalf("empty series = %#v", got)
	}
	pts := Series([]Snapshot{
		snap("a", 2*time.Hour+10*time.Minute, 130),
		snap("a", 0, 100),
		snap("a", time.Hour+5*time.Minute, 110),
		snap("a", time.Hour+50*time.Minute, 120), // same bucket, newer wins
		snap("a", 3*time.Hour, 90),               // reset clamps to 0
	}, 0)
	if len(pts) != 4 {
		t.Fatalf("points = %+v", pts)
	}
	want := []int64{0, 20, 10, 0}
	for i, p := range pts {
		if p.Pulls != want[i] {
			t.Errorf("point %d pulls = %d, want %d", i, p.Pulls, want[i])
		}
	}
	if !pts[1].Time.Equal(t0.Add(time.Hour)) || pts[1].PullsTotal != 120 {
		t.Errorf("bucket = %+v", pts[1])
	}
}
