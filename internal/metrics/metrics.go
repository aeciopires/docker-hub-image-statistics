// Package metrics exposes Prometheus metrics for images and the application.
package metrics

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/aeciopires/docker-hub-image-statistics/internal/collector"
	"github.com/aeciopires/docker-hub-image-statistics/internal/stats"
	"github.com/aeciopires/docker-hub-image-statistics/internal/version"
)

// Provider supplies the data exported as image metrics.
type Provider interface {
	Images() []stats.ImageStats
	Status() collector.Status
}

// Metrics owns a dedicated registry.
type Metrics struct {
	registry *prometheus.Registry

	collectionDuration prometheus.Histogram
	collections        *prometheus.CounterVec
	lastDuration       prometheus.Gauge
	lastImages         prometheus.Gauge
	cacheOps           *prometheus.CounterVec
	httpRequests       *prometheus.CounterVec
	httpDuration       *prometheus.HistogramVec
}

// New registers every metric. provider may be nil until the collector exists
// (call SetProvider later).
func New() *Metrics {
	reg := prometheus.NewRegistry()
	m := &Metrics{
		registry: reg,
		collectionDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "dhis_collection_duration_seconds",
			Help:    "Duration of a Docker Hub collection cycle.",
			Buckets: []float64{0.5, 1, 2, 5, 10, 30, 60, 120, 300, 600},
		}),
		collections: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "dhis_collections_total",
			Help: "Collection cycles by result (success or error).",
		}, []string{"result"}),
		lastDuration: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "dhis_last_collection_duration_seconds",
			Help: "Duration of the last collection cycle.",
		}),
		lastImages: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "dhis_last_collection_images",
			Help: "Images collected in the last cycle.",
		}),
		cacheOps: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "dhis_cache_operations_total",
			Help: "Cache tier operations by tier, operation and result.",
		}, []string{"tier", "op", "result"}),
		httpRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "dhis_http_requests_total",
			Help: "HTTP requests by method, route and status code.",
		}, []string{"method", "route", "code"}),
		httpDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "dhis_http_request_duration_seconds",
			Help:    "HTTP request latency by method and route.",
			Buckets: prometheus.DefBuckets,
		}, []string{"method", "route"}),
	}
	info := version.Get()
	buildInfo := prometheus.NewGauge(prometheus.GaugeOpts{
		Name:        "dhis_build_info",
		Help:        "Build information (always 1).",
		ConstLabels: prometheus.Labels{"version": info.Version, "commit": info.Commit, "go_version": info.GoVersion},
	})
	buildInfo.Set(1)
	// Pre-create both results so the first collection is visible to increase().
	m.collections.WithLabelValues("success")
	m.collections.WithLabelValues("error")

	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		m.collectionDuration, m.collections, m.lastDuration, m.lastImages,
		m.cacheOps, m.httpRequests, m.httpDuration, buildInfo,
	)
	return m
}

// Registry returns the underlying registry (useful for tests).
func (m *Metrics) Registry() *prometheus.Registry { return m.registry }

// RegisterProvider exposes image metrics from provider.
func (m *Metrics) RegisterProvider(p Provider) error {
	return m.registry.Register(&imageCollector{provider: p})
}

// Handler serves the metrics in the Prometheus/OpenMetrics format.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{EnableOpenMetrics: true})
}

// ObserveCollection implements collector.Recorder.
func (m *Metrics) ObserveCollection(d time.Duration, images int, err error) {
	m.collectionDuration.Observe(d.Seconds())
	m.lastDuration.Set(d.Seconds())
	m.lastImages.Set(float64(images))
	if err != nil {
		m.collections.WithLabelValues("error").Inc()
		return
	}
	m.collections.WithLabelValues("success").Inc()
}

// ObserveCache implements store.Observer.
func (m *Metrics) ObserveCache(tier, op string, err error) {
	result := "success"
	if err != nil {
		result = "error"
	}
	m.cacheOps.WithLabelValues(tier, op, result).Inc()
}

// ObserveHTTP records one HTTP request.
func (m *Metrics) ObserveHTTP(method, route string, code int, d time.Duration) {
	m.httpRequests.WithLabelValues(method, route, strconv.Itoa(code)).Inc()
	m.httpDuration.WithLabelValues(method, route).Observe(d.Seconds())
}

// imageCollector reads the provider on every scrape, so the metrics always
// reflect the cached statistics (no duplicated state).
type imageCollector struct{ provider Provider }

var (
	imageLabels  = []string{"namespace", "repository"}
	windowLabels = []string{"namespace", "repository", "window"}

	descPullsTotal = prometheus.NewDesc("dockerhub_image_pulls_total",
		"Total pulls reported by Docker Hub.", imageLabels, nil)
	descPullsWindow = prometheus.NewDesc("dockerhub_image_pulls",
		"Pulls inside the window (1d, 7d, 30d) derived from stored snapshots.", windowLabels, nil)
	descWindowComplete = prometheus.NewDesc("dockerhub_image_pulls_window_complete",
		"1 when the stored history covers the whole window, 0 when the value is a lower bound.", windowLabels, nil)
	descLastCollection = prometheus.NewDesc("dockerhub_image_pulls_last_collection",
		"Pulls observed between the two newest collections of the image.", imageLabels, nil)
	descStars = prometheus.NewDesc("dockerhub_image_stars",
		"Stars reported by Docker Hub.", imageLabels, nil)
	descLastUpdated = prometheus.NewDesc("dockerhub_image_last_updated_timestamp_seconds",
		"Unix time of the last push to the repository.", imageLabels, nil)
	descCollectedAt = prometheus.NewDesc("dockerhub_image_collected_timestamp_seconds",
		"Unix time of the newest snapshot of the image.", imageLabels, nil)
	descImages = prometheus.NewDesc("dockerhub_images",
		"Number of images being tracked.", nil, nil)
	descNextRun = prometheus.NewDesc("dhis_next_collection_timestamp_seconds",
		"Unix time of the next scheduled collection.", nil, nil)
	// Derived from the collector status (restored from the cache after a
	// restart), so it is never reported as the Unix epoch.
	descLastSuccess = prometheus.NewDesc("dhis_last_collection_success_timestamp_seconds",
		"Unix time of the newest successfully stored collection.", nil, nil)
	descCollecting = prometheus.NewDesc("dhis_collection_in_progress",
		"1 while a collection is running.", nil, nil)
)

func (c *imageCollector) Describe(ch chan<- *prometheus.Desc) {
	for _, d := range []*prometheus.Desc{
		descPullsTotal, descPullsWindow, descWindowComplete, descLastCollection, descStars,
		descLastUpdated, descCollectedAt, descImages, descNextRun, descLastSuccess, descCollecting,
	} {
		ch <- d
	}
}

func (c *imageCollector) Collect(ch chan<- prometheus.Metric) {
	images := c.provider.Images()
	ch <- prometheus.MustNewConstMetric(descImages, prometheus.GaugeValue, float64(len(images)))
	for _, img := range images {
		ns, repo := img.Namespace, img.Name
		ch <- prometheus.MustNewConstMetric(descPullsTotal, prometheus.CounterValue, float64(img.PullsTotal), ns, repo)
		ch <- prometheus.MustNewConstMetric(descStars, prometheus.GaugeValue, float64(img.Stars), ns, repo)
		ch <- prometheus.MustNewConstMetric(descLastCollection, prometheus.GaugeValue, float64(img.LastCollection.Pulls), ns, repo)
		ch <- prometheus.MustNewConstMetric(descCollectedAt, prometheus.GaugeValue, unix(img.CollectedAt), ns, repo)
		if !img.LastUpdated.IsZero() {
			ch <- prometheus.MustNewConstMetric(descLastUpdated, prometheus.GaugeValue, unix(img.LastUpdated), ns, repo)
		}
		for _, w := range stats.Windows {
			wp := img.Pulls[w.Name]
			ch <- prometheus.MustNewConstMetric(descPullsWindow, prometheus.GaugeValue, float64(wp.Pulls), ns, repo, w.Name)
			ch <- prometheus.MustNewConstMetric(descWindowComplete, prometheus.GaugeValue, boolFloat(wp.Complete), ns, repo, w.Name)
		}
	}
	st := c.provider.Status()
	if !st.NextRun.IsZero() {
		ch <- prometheus.MustNewConstMetric(descNextRun, prometheus.GaugeValue, unix(st.NextRun))
	}
	if !st.LastSuccess.IsZero() {
		ch <- prometheus.MustNewConstMetric(descLastSuccess, prometheus.GaugeValue, unix(st.LastSuccess))
	}
	ch <- prometheus.MustNewConstMetric(descCollecting, prometheus.GaugeValue, boolFloat(st.Collecting))
}

func unix(t time.Time) float64 { return float64(t.UnixMilli()) / 1000 }

func boolFloat(b bool) float64 {
	if b {
		return 1
	}
	return 0
}
