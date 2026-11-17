package metrics

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/aeciopires/docker-hub-image-statistics/internal/collector"
	"github.com/aeciopires/docker-hub-image-statistics/internal/stats"
)

type fakeProvider struct {
	images []stats.ImageStats
	status collector.Status
}

func (f fakeProvider) Images() []stats.ImageStats { return f.images }
func (f fakeProvider) Status() collector.Status   { return f.status }

func TestImageMetrics(t *testing.T) {
	m := New()
	now := time.Unix(1_800_000_000, 0)
	p := fakeProvider{
		images: []stats.ImageStats{{
			Namespace: "acme", Name: "api", PullsTotal: 1234, Stars: 5,
			LastCollection: stats.LastCollection{Pulls: 42},
			LastUpdated:    now, CollectedAt: now,
			Pulls: map[string]stats.WindowPulls{
				"1d": {Pulls: 10, Complete: true}, "7d": {Pulls: 70, Complete: true}, "30d": {Pulls: 300},
			},
		}, {Namespace: "acme", Name: "new", CollectedAt: now, Pulls: map[string]stats.WindowPulls{}}},
		status: collector.Status{NextRun: now.Add(time.Hour), LastSuccess: now, Collecting: true},
	}
	if err := m.RegisterProvider(p); err != nil {
		t.Fatal(err)
	}

	expected := `
# HELP dockerhub_image_pulls Pulls inside the window (1d, 7d, 30d) derived from stored snapshots.
# TYPE dockerhub_image_pulls gauge
dockerhub_image_pulls{namespace="acme",repository="api",window="1d"} 10
dockerhub_image_pulls{namespace="acme",repository="api",window="30d"} 300
dockerhub_image_pulls{namespace="acme",repository="api",window="7d"} 70
dockerhub_image_pulls{namespace="acme",repository="new",window="1d"} 0
dockerhub_image_pulls{namespace="acme",repository="new",window="30d"} 0
dockerhub_image_pulls{namespace="acme",repository="new",window="7d"} 0
# HELP dockerhub_image_pulls_total Total pulls reported by Docker Hub.
# TYPE dockerhub_image_pulls_total counter
dockerhub_image_pulls_total{namespace="acme",repository="api"} 1234
dockerhub_image_pulls_total{namespace="acme",repository="new"} 0
# HELP dockerhub_image_pulls_last_collection Pulls observed between the two newest collections of the image.
# TYPE dockerhub_image_pulls_last_collection gauge
dockerhub_image_pulls_last_collection{namespace="acme",repository="api"} 42
dockerhub_image_pulls_last_collection{namespace="acme",repository="new"} 0
# HELP dockerhub_images Number of images being tracked.
# TYPE dockerhub_images gauge
dockerhub_images 2
# HELP dhis_collection_in_progress 1 while a collection is running.
# TYPE dhis_collection_in_progress gauge
dhis_collection_in_progress 1
`
	if err := testutil.GatherAndCompare(m.Registry(), strings.NewReader(expected),
		"dockerhub_image_pulls", "dockerhub_image_pulls_total", "dockerhub_image_pulls_last_collection",
		"dockerhub_images", "dhis_collection_in_progress"); err != nil {
		t.Fatal(err)
	}
	if n, _ := testutil.GatherAndCount(m.Registry(), "dockerhub_image_last_updated_timestamp_seconds"); n != 1 {
		t.Fatalf("last_updated series = %d, want 1 (zero time skipped)", n)
	}
	for _, name := range []string{"dhis_next_collection_timestamp_seconds", "dhis_last_collection_success_timestamp_seconds"} {
		if n, _ := testutil.GatherAndCount(m.Registry(), name); n != 1 {
			t.Fatalf("%s series = %d", name, n)
		}
	}
	if n, _ := testutil.GatherAndCount(m.Registry(), "dockerhub_image_pulls_window_complete"); n != 6 {
		t.Fatalf("window complete series = %d", n)
	}
}

func TestAppMetricsAndHandler(t *testing.T) {
	m := New()
	m.ObserveCollection(2*time.Second, 3, nil)
	m.ObserveCollection(time.Second, 0, errors.New("x"))
	m.ObserveCache("sqlite", "save", nil)
	m.ObserveCache("disk", "save", errors.New("x"))
	m.ObserveHTTP("GET", "/api/v1/images", 200, 10*time.Millisecond)
	if err := m.RegisterProvider(fakeProvider{}); err != nil {
		t.Fatal(err)
	}
	if n, _ := testutil.GatherAndCount(m.Registry(), "dhis_last_collection_success_timestamp_seconds"); n != 0 {
		t.Fatal("unknown last success must not be exported as 0")
	}

	if got := testutil.ToFloat64(m.collections.WithLabelValues("success")); got != 1 {
		t.Errorf("success = %v", got)
	}
	if got := testutil.ToFloat64(m.collections.WithLabelValues("error")); got != 1 {
		t.Errorf("error = %v", got)
	}
	if got := testutil.ToFloat64(m.cacheOps.WithLabelValues("disk", "save", "error")); got != 1 {
		t.Errorf("cache error = %v", got)
	}
	if got := testutil.ToFloat64(m.lastDuration); got != 1 {
		t.Errorf("last duration = %v", got)
	}

	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body, _ := io.ReadAll(rec.Body)
	for _, want := range []string{"dhis_build_info{", "dhis_http_requests_total", "go_goroutines", "dhis_collection_duration_seconds_bucket"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("metrics output missing %q", want)
		}
	}
}
