package server

import (
	"context"
	"encoding/json/v2"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/aeciopires/docker-hub-image-statistics/internal/collector"
	"github.com/aeciopires/docker-hub-image-statistics/internal/stats"
)

var now = time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)

type fakeProvider struct {
	images     []stats.ImageStats
	history    []stats.Snapshot
	historyErr error
	readyErr   error
	since      time.Time
}

func (f *fakeProvider) Images() []stats.ImageStats {
	return append([]stats.ImageStats(nil), f.images...)
}
func (f *fakeProvider) Image(ns, name string) (stats.ImageStats, bool) {
	for _, img := range f.images {
		if img.Namespace == ns && img.Name == name {
			return img, true
		}
	}
	return stats.ImageStats{}, false
}
func (f *fakeProvider) Summary() stats.Summary { return stats.Summarize(f.images) }
func (f *fakeProvider) History(_ context.Context, _, _ string, since time.Time) ([]stats.Snapshot, error) {
	f.since = since
	var out []stats.Snapshot
	for _, s := range f.history {
		if !s.CollectedAt.Before(since) {
			out = append(out, s)
		}
	}
	return out, f.historyErr
}
func (f *fakeProvider) Status() collector.Status {
	return collector.Status{Interval: "1h0m0s", LastSuccess: now, NextRun: now.Add(time.Hour)}
}
func (f *fakeProvider) Ready(context.Context) error { return f.readyErr }

type observer struct{ routes []string }

func (o *observer) ObserveHTTP(method, route string, code int, _ time.Duration) {
	o.routes = append(o.routes, method+" "+route)
}

func img(name string, total, d1, d7 int64, stars int64, updated time.Time) stats.ImageStats {
	return stats.ImageStats{
		LastCollection: stats.LastCollection{Pulls: d1 / 10},
		Namespace:      "acme", Name: name, PullsTotal: total, Stars: stars, LastUpdated: updated,
		CollectedAt: now, Description: "the " + name + " image",
		Pulls: map[string]stats.WindowPulls{
			"1d": {Pulls: d1, Complete: true}, "7d": {Pulls: d7, Complete: true}, "30d": {Pulls: d7},
		},
	}
}

func newTestServer(p *fakeProvider, obs *observer) http.Handler {
	static := fstest.MapFS{
		"index.html":         {Data: []byte("<html>ui</html>")},
		"favicon.svg":        {Data: []byte("<svg/>")},
		"fonts/roboto.woff2": {Data: []byte("font")},
	}
	opts := Options{
		Provider: p,
		Metrics:  http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, "metric 1\n") }),
		Static:   static,
	}
	if obs != nil {
		opts.Observer = obs
	}
	return New(opts)
}

func do(t *testing.T, h http.Handler, method, target string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, target, nil))
	return rec
}

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	return v
}

func provider() *fakeProvider {
	return &fakeProvider{images: []stats.ImageStats{
		img("web", 100, 50, 60, 9, now.Add(-time.Hour)),
		img("api", 500, 10, 70, 1, now.Add(-48*time.Hour)),
		img("cli", 300, 20, 20, 5, now.Add(-2*time.Hour)),
	}}
}

func TestHealthAndReadiness(t *testing.T) {
	p := provider()
	h := newTestServer(p, nil)
	for _, path := range []string{"/healthz", "/livez"} {
		if rec := do(t, h, "GET", path); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"ok"`) {
			t.Errorf("%s = %d %s", path, rec.Code, rec.Body)
		}
	}
	if rec := do(t, h, "GET", "/readyz"); rec.Code != 200 {
		t.Errorf("readyz = %d", rec.Code)
	}
	p.readyErr = errors.New("db down")
	rec := do(t, h, "GET", "/readyz")
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "db down") {
		t.Errorf("readyz unavailable = %d %s", rec.Code, rec.Body)
	}
	if rec := do(t, h, "POST", "/healthz"); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST /healthz = %d", rec.Code)
	}
}

func TestSecurityHeaders(t *testing.T) {
	rec := do(t, newTestServer(provider(), nil), "GET", "/")
	for header, want := range map[string]string{
		"Content-Security-Policy": "default-src 'self'",
		"X-Content-Type-Options":  "nosniff",
		"X-Frame-Options":         "DENY",
		"Referrer-Policy":         "no-referrer",
	} {
		if got := rec.Header().Get(header); !strings.Contains(got, want) {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}
}

func TestStaticAndMetrics(t *testing.T) {
	h := newTestServer(provider(), nil)
	rec := do(t, h, "GET", "/")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "ui") || rec.Header().Get("Cache-Control") != "no-cache" {
		t.Fatalf("index = %d %q %q", rec.Code, rec.Body, rec.Header().Get("Cache-Control"))
	}
	rec = do(t, h, "GET", "/fonts/roboto.woff2")
	if !strings.Contains(rec.Header().Get("Cache-Control"), "immutable") {
		t.Errorf("font cache = %q", rec.Header().Get("Cache-Control"))
	}
	if rec := do(t, h, "GET", "/metrics"); rec.Body.String() != "metric 1\n" {
		t.Errorf("metrics = %q", rec.Body)
	}
	if rec := do(t, h, "GET", "/api/v1/unknown"); rec.Code != 404 || !strings.Contains(rec.Body.String(), "not found") {
		t.Errorf("unknown api = %d %s", rec.Code, rec.Body)
	}
}

func TestVersionStatusSummary(t *testing.T) {
	h := newTestServer(provider(), nil)
	if rec := do(t, h, "GET", "/api/v1/version"); rec.Code != 200 || !strings.Contains(rec.Body.String(), "go_version") {
		t.Errorf("version = %s", rec.Body)
	}
	st := decode[collector.Status](t, do(t, h, "GET", "/api/v1/status"))
	if st.Interval != "1h0m0s" || !st.NextRun.Equal(now.Add(time.Hour)) {
		t.Errorf("status = %+v", st)
	}
	sum := decode[summaryResponse](t, do(t, h, "GET", "/api/v1/summary"))
	if sum.Summary.Images != 3 || sum.Summary.PullsTotal != 900 || !sum.Status.LastSuccess.Equal(now) {
		t.Errorf("summary = %+v", sum)
	}
}

func TestImagesSortingAndSearch(t *testing.T) {
	h := newTestServer(provider(), nil)
	order := func(target string) string {
		resp := decode[imagesResponse](t, do(t, h, "GET", target))
		names := make([]string, len(resp.Images))
		for i, im := range resp.Images {
			names[i] = im.Name
		}
		return strings.Join(names, ",")
	}
	cases := map[string]string{
		"/api/v1/images":              "api,cli,web",
		"/api/v1/images?sort=1d":      "web,cli,api",
		"/api/v1/images?sort=7d":      "api,web,cli",
		"/api/v1/images?sort=last":    "web,cli,api",
		"/api/v1/images?sort=30d":     "api,web,cli",
		"/api/v1/images?sort=stars":   "web,cli,api",
		"/api/v1/images?sort=updated": "web,cli,api",
		"/api/v1/images?sort=name":    "api,cli,web",
		"/api/v1/images?q=WE":         "web",
		"/api/v1/images?q=the+cli":    "cli",
		"/api/v1/images?q=nothing":    "",
	}
	for target, want := range cases {
		if got := order(target); got != want {
			t.Errorf("%s = %q, want %q", target, got, want)
		}
	}
	if rec := do(t, h, "GET", "/api/v1/images?sort=bogus"); rec.Code != 400 {
		t.Errorf("bad sort = %d", rec.Code)
	}
}

func TestImageDetail(t *testing.T) {
	h := newTestServer(provider(), nil)
	got := decode[stats.ImageStats](t, do(t, h, "GET", "/api/v1/images/acme/api"))
	if got.PullsTotal != 500 {
		t.Errorf("image = %+v", got)
	}
	if rec := do(t, h, "GET", "/api/v1/images/acme/missing"); rec.Code != 404 {
		t.Errorf("missing = %d", rec.Code)
	}
	if rec := do(t, h, "GET", "/api/v1/images/acme/missing/history"); rec.Code != 404 {
		t.Errorf("missing history = %d", rec.Code)
	}
}

func TestHistoryHourly(t *testing.T) {
	p := provider()
	for i := range 27 { // 26h of hourly snapshots + one at "now"
		p.history = append(p.history, stats.Snapshot{
			Namespace: "acme", Name: "api", PullCount: int64(1000 + 10*i),
			CollectedAt: now.Add(time.Duration(i-26) * time.Hour),
		})
	}
	obs := &observer{}
	h := newTestServer(p, obs)
	resp := decode[historyResponse](t, do(t, h, "GET", "/api/v1/images/acme/api/history"))
	if resp.Step != "1h0m0s" || !resp.Since.Equal(now.Add(-24*time.Hour)) {
		t.Fatalf("resp = %+v", resp)
	}
	if !p.since.Equal(now.Add(-25 * time.Hour)) {
		t.Errorf("queried since %v", p.since)
	}
	if len(resp.Points) != 25 || resp.Points[len(resp.Points)-1].Pulls != 10 {
		t.Fatalf("points = %d %+v", len(resp.Points), resp.Points)
	}
	if len(obs.routes) != 1 || obs.routes[0] != "GET /api/v1/images/{namespace}/{name}/history" {
		t.Errorf("observed routes = %v", obs.routes)
	}

	resp = decode[historyResponse](t, do(t, h, "GET", "/api/v1/images/acme/api/history?range=7d&step=6h"))
	if resp.Step != "6h0m0s" {
		t.Errorf("step = %s", resp.Step)
	}

	for _, bad := range []string{"?range=-1h", "?range=1000d", "?range=xd", "?step=1s", "?step=48h", "?range=soon"} {
		if rec := do(t, h, "GET", "/api/v1/images/acme/api/history"+bad); rec.Code != 400 {
			t.Errorf("%s = %d", bad, rec.Code)
		}
	}
	p.historyErr = errors.New("boom")
	if rec := do(t, h, "GET", "/api/v1/images/acme/api/history"); rec.Code != 500 {
		t.Errorf("history error = %d", rec.Code)
	}
}

func TestRecoverer(t *testing.T) {
	a := &api{opts: Options{Logger: slog.New(slog.DiscardHandler)}}
	h := a.recoverer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("kaboom") }))
	rec := do(t, h, "GET", "/")
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "internal server error") {
		t.Fatalf("recover = %d %s", rec.Code, rec.Body)
	}

	late := a.recoverer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, "partial")
		panic("late")
	}))
	if rec := do(t, late, "GET", "/"); rec.Body.String() != "partial" {
		t.Fatalf("late panic must not append an error body: %q", rec.Body)
	}

	abort := a.recoverer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic(http.ErrAbortHandler) }))
	defer func() {
		if v, _ := recover().(error); !errors.Is(v, http.ErrAbortHandler) {
			t.Fatalf("ErrAbortHandler must be re-panicked, got %v", v)
		}
	}()
	do(t, abort, "GET", "/")
}

func TestServerErrorsLoggedAndNoStatic(t *testing.T) {
	h := New(Options{Provider: provider()}) // no static, no metrics, default logger
	if rec := do(t, h, "GET", "/"); rec.Code != 404 {
		t.Errorf("no static = %d", rec.Code)
	}
	if rec := do(t, h, "GET", "/metrics"); rec.Code != 404 {
		t.Errorf("no metrics = %d", rec.Code)
	}
}

func TestRouteOf(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/x", nil)
	if routeOf(r) != "unmatched" {
		t.Error(routeOf(r))
	}
	r.Pattern = "/plain"
	if routeOf(r) != "/plain" {
		t.Error(routeOf(r))
	}
}
