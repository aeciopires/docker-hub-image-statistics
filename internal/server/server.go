// Package server exposes the HTTP API, health checks, metrics and the web UI.
package server

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/trace"

	"github.com/aeciopires/docker-hub-image-statistics/internal/collector"
	"github.com/aeciopires/docker-hub-image-statistics/internal/stats"
	"github.com/aeciopires/docker-hub-image-statistics/internal/version"
)

// Provider is what the HTTP layer needs from the collector.
type Provider interface {
	Images() []stats.ImageStats
	Image(namespace, name string) (stats.ImageStats, bool)
	Summary() stats.Summary
	History(ctx context.Context, namespace, name string, since time.Time) ([]stats.Snapshot, error)
	Status() collector.Status
	Ready(ctx context.Context) error
}

// HTTPObserver records request metrics.
type HTTPObserver interface {
	ObserveHTTP(method, route string, code int, d time.Duration)
}

// Options configures the handler.
type Options struct {
	Provider       Provider
	Metrics        http.Handler
	Observer       HTTPObserver
	Static         fs.FS
	Logger         *slog.Logger
	TracerProvider trace.TracerProvider
	// MaxHistory bounds the history window a client may request.
	MaxHistory time.Duration
}

// contentSecurityPolicy only allows same-origin resources (the UI is fully embedded).
const contentSecurityPolicy = "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; " +
	"font-src 'self'; connect-src 'self'; object-src 'none'; base-uri 'self'; form-action 'none'; frame-ancestors 'none'"

// New builds the root handler.
func New(opts Options) http.Handler {
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	if opts.MaxHistory <= 0 {
		opts.MaxHistory = 90 * 24 * time.Hour
	}
	a := &api{opts: opts}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", a.healthz)
	mux.HandleFunc("GET /livez", a.healthz)
	mux.HandleFunc("GET /readyz", a.readyz)
	if opts.Metrics != nil {
		mux.Handle("GET /metrics", opts.Metrics)
	}
	mux.HandleFunc("GET /api/v1/version", a.version)
	mux.HandleFunc("GET /api/v1/status", a.status)
	mux.HandleFunc("GET /api/v1/summary", a.summary)
	mux.HandleFunc("GET /api/v1/images", a.images)
	mux.HandleFunc("GET /api/v1/images/{namespace}/{name}", a.image)
	mux.HandleFunc("GET /api/v1/images/{namespace}/{name}/history", a.history)
	mux.HandleFunc("GET /api/", func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotFound, "not found")
	})
	if opts.Static != nil {
		mux.Handle("GET /", staticHandler(opts.Static))
	}

	var h http.Handler = mux
	h = a.observe(h)
	h = securityHeaders(h)
	h = a.recoverer(h)
	otelOpts := []otelhttp.Option{
		otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string {
			return r.Method + " " + routeOf(r)
		}),
		otelhttp.WithFilter(func(r *http.Request) bool { return !isProbe(r.URL.Path) }),
	}
	if opts.TracerProvider != nil {
		otelOpts = append(otelOpts, otelhttp.WithTracerProvider(opts.TracerProvider))
	}
	return otelhttp.NewHandler(h, "http.server", otelOpts...)
}

type api struct{ opts Options }

func isProbe(path string) bool {
	switch path {
	case "/healthz", "/livez", "/readyz", "/metrics":
		return true
	}
	return false
}

func routeOf(r *http.Request) string {
	if r.Pattern != "" {
		if _, p, ok := strings.Cut(r.Pattern, " "); ok {
			return p
		}
		return r.Pattern
	}
	return "unmatched"
}

// statusRecorder captures the status code for logs and metrics.
type statusRecorder struct {
	http.ResponseWriter
	code int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.code = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

func (a *api) observe(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, code: http.StatusOK}
		next.ServeHTTP(rec, r)
		d := time.Since(start)
		route := routeOf(r)
		if a.opts.Observer != nil {
			a.opts.Observer.ObserveHTTP(r.Method, route, rec.code, d)
		}
		level := slog.LevelInfo
		switch {
		case rec.code >= 500:
			level = slog.LevelError
		case isProbe(r.URL.Path) || strings.HasPrefix(route, "/{") || route == "/":
			level = slog.LevelDebug // probes, scrapes and static assets are noisy
		}
		a.opts.Logger.Log(r.Context(), level, "http request",
			"method", r.Method, "route", route, "path", r.URL.Path, "status", rec.code,
			"duration_ms", float64(d.Microseconds())/1000, "user_agent", r.UserAgent())
	})
}

// headerTracker remembers whether the response has started.
type headerTracker struct {
	http.ResponseWriter
	started bool
}

func (h *headerTracker) WriteHeader(code int) {
	h.started = true
	h.ResponseWriter.WriteHeader(code)
}

func (h *headerTracker) Write(b []byte) (int, error) {
	h.started = true
	return h.ResponseWriter.Write(b)
}

func (h *headerTracker) Unwrap() http.ResponseWriter { return h.ResponseWriter }

func (a *api) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tw := &headerTracker{ResponseWriter: w}
		defer func() {
			if v := recover(); v != nil {
				if err, ok := v.(error); ok && errors.Is(err, http.ErrAbortHandler) {
					panic(v)
				}
				a.opts.Logger.ErrorContext(r.Context(), "panic serving request",
					"panic", fmt.Sprint(v), "stack", string(debug.Stack()))
				// Only answer when nothing was sent yet; otherwise the body would be corrupted.
				if !tw.started {
					writeError(tw, http.StatusInternalServerError, "internal server error")
				}
			}
		}()
		next.ServeHTTP(tw, r)
	})
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", contentSecurityPolicy)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), interest-cohort=()")
		next.ServeHTTP(w, r)
	})
}

func staticHandler(root fs.FS) http.Handler {
	files := http.FileServerFS(root)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/fonts/") || r.URL.Path == "/favicon.svg" {
			w.Header().Set("Cache-Control", "public, max-age=604800, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		files.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.MarshalWrite(w, v)
}

type errorBody struct {
	Error string `json:"error"`
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, errorBody{Error: msg})
}

func (a *api) healthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (a *api) readyz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := a.opts.Provider.Ready(ctx); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable", "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func (a *api) version(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, version.Get())
}

func (a *api) status(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, a.opts.Provider.Status())
}

type summaryResponse struct {
	Summary stats.Summary    `json:"summary"`
	Status  collector.Status `json:"status"`
	Version version.Info     `json:"version"`
}

func (a *api) summary(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, summaryResponse{
		Summary: a.opts.Provider.Summary(),
		Status:  a.opts.Provider.Status(),
		Version: version.Get(),
	})
}

type imagesResponse struct {
	Images []stats.ImageStats `json:"images"`
	Count  int                `json:"count"`
}

// images supports ?q=<substring> and ?sort=pulls|last|1d|7d|30d|stars|name|updated.
func (a *api) images(w http.ResponseWriter, r *http.Request) {
	images := a.opts.Provider.Images()
	if q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q"))); q != "" {
		filtered := make([]stats.ImageStats, 0, len(images))
		for _, img := range images {
			if strings.Contains(img.Key(), q) || strings.Contains(strings.ToLower(img.Description), q) {
				filtered = append(filtered, img)
			}
		}
		images = filtered
	}
	if err := sortImages(images, r.URL.Query().Get("sort")); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, imagesResponse{Images: images, Count: len(images)})
}

func (a *api) image(w http.ResponseWriter, r *http.Request) {
	img, ok := a.opts.Provider.Image(r.PathValue("namespace"), r.PathValue("name"))
	if !ok {
		writeError(w, http.StatusNotFound, "image not found")
		return
	}
	writeJSON(w, http.StatusOK, img)
}

type historyResponse struct {
	Image  stats.ImageStats `json:"image"`
	Step   string           `json:"step"`
	Since  time.Time        `json:"since"`
	Points []stats.Point    `json:"points"`
}

// history supports ?range=24h|7d|30d|... (default 24h) and ?step=1h (default).
func (a *api) history(w http.ResponseWriter, r *http.Request) {
	ns, name := r.PathValue("namespace"), r.PathValue("name")
	img, ok := a.opts.Provider.Image(ns, name)
	if !ok {
		writeError(w, http.StatusNotFound, "image not found")
		return
	}
	window, err := parseDuration(r.URL.Query().Get("range"), 24*time.Hour)
	if err != nil || window <= 0 || window > a.opts.MaxHistory {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("range must be a positive duration up to %s", a.opts.MaxHistory))
		return
	}
	step, err := parseDuration(r.URL.Query().Get("step"), time.Hour)
	if err != nil || step < time.Minute || step > window {
		writeError(w, http.StatusBadRequest, "step must be between 1m and the range")
		return
	}
	// One extra step so the first bucket has a baseline for its pull delta.
	since := img.CollectedAt.Add(-window - step)
	snaps, err := a.opts.Provider.History(r.Context(), ns, name, since)
	if err != nil {
		a.opts.Logger.ErrorContext(r.Context(), "history query failed", "image", img.Key(), "error", err)
		writeError(w, http.StatusInternalServerError, "could not load history")
		return
	}
	points := stats.Series(snaps, step)
	if len(points) > 1 && points[0].Time.Before(img.CollectedAt.Add(-window).Truncate(step)) {
		points = points[1:] // drop the baseline-only bucket
	}
	writeJSON(w, http.StatusOK, historyResponse{
		Image: img, Step: step.String(), Since: img.CollectedAt.Add(-window), Points: points,
	})
}

// parseDuration accepts Go durations plus a "d" (day) suffix.
func parseDuration(v string, def time.Duration) (time.Duration, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return def, nil
	}
	if days, ok := strings.CutSuffix(v, "d"); ok {
		n, err := strconv.Atoi(days)
		if err != nil {
			return 0, err
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	return time.ParseDuration(v)
}
