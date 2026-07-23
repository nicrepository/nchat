package observability

import (
	"net/http"

	"github.com/nicrepository/nchat/libs/go/platform/buildinfo"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics holds Prometheus metrics for a service.
// nchat_service_info is always registered.
// HTTP metrics (requests_total, request_duration_seconds, in_flight_requests)
// are registered only when cfg.MetricsEnabled is true.
type Metrics struct {
	cfg             Config
	registry        *prometheus.Registry
	requestsTotal   *prometheus.CounterVec
	requestDuration *prometheus.HistogramVec
	inFlight        *prometheus.GaugeVec
	serviceInfo     *prometheus.GaugeVec
}

// NewMetrics creates and registers Prometheus metrics.
// nchat_service_info is always present so /metrics always returns content.
func NewMetrics(cfg Config) *Metrics {
	reg := prometheus.NewRegistry()
	info := buildinfo.Current()

	serviceInfo := prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "nchat_service_info",
			Help: "Service build info.",
		},
		[]string{"service", "version", "commit", "env"},
	)
	reg.MustRegister(serviceInfo)
	serviceInfo.WithLabelValues(cfg.ServiceName, info.Version, info.Commit, cfg.Environment).Set(1)

	m := &Metrics{
		cfg:         cfg,
		registry:    reg,
		serviceInfo: serviceInfo,
	}

	if !cfg.MetricsEnabled {
		return m
	}

	m.requestsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "nchat_http_requests_total",
			Help: "Total HTTP requests by service, method, path and status.",
		},
		[]string{"service", "method", "path", "status"},
	)
	m.requestDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "nchat_http_request_duration_seconds",
			Help:    "HTTP request duration in seconds.",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"service", "method", "path", "status"},
	)
	m.inFlight = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "nchat_http_in_flight_requests",
			Help: "Current number of in-flight HTTP requests.",
		},
		[]string{"service"},
	)

	reg.MustRegister(
		m.requestsTotal,
		m.requestDuration,
		m.inFlight,
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		collectors.NewGoCollector(),
	)

	return m
}

// Handler returns an HTTP handler that serves Prometheus metrics.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{})
}

// Register adds service-specific collectors to the shared registry when
// metrics are enabled. It returns false when metrics are disabled.
func (m *Metrics) Register(collectors ...prometheus.Collector) bool {
	if m == nil || !m.cfg.MetricsEnabled {
		return false
	}
	m.registry.MustRegister(collectors...)
	return true
}
