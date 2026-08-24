package service

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/nicrepository/nchat/services/admin-service/internal/domain"
)

// Prometheus instrumentation for the Health Center (issue #581).
//
// The cardinality rule this file exists to keep: every label value comes from a
// closed set declared in code. `service` is a health registry id, `state` is
// one of the five health states. There is no user id, no e-mail, no URL, no
// request id, no channel id and no file id anywhere in a label, so the series
// count is bounded by the size of two literals and cannot grow with traffic.
//
// The collectors are built here and registered by the router into the shared
// registry, so metrics stay opt-in exactly like the rest of the platform's:
// with PROMETHEUS_METRICS_ENABLED unset, Register is a no-op and these
// collectors are simply never scraped.

// HealthMetrics holds the collectors the health surface exports.
//
// Every method tolerates a nil receiver, so a deployment that does not wire
// metrics runs the same code path as one that does — instrumentation is never
// the reason a health check behaves differently.
type HealthMetrics struct {
	checkDuration *prometheus.HistogramVec
	checkResults  *prometheus.CounterVec
	cacheEvents   *prometheus.CounterVec
	buildDuration *prometheus.HistogramVec
}

// NewHealthMetrics builds the collectors.
func NewHealthMetrics() *HealthMetrics {
	return &HealthMetrics{
		checkDuration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "nchat_admin_health_check_duration_seconds",
				Help:    "Duration of one Admin Console health check, by service.",
				Buckets: []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2, 5},
			},
			[]string{"service"},
		),
		checkResults: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "nchat_admin_health_check_results_total",
				Help: "Admin Console health check results by service and resulting state.",
			},
			[]string{"service", "state"},
		),
		cacheEvents: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "nchat_admin_health_cache_events_total",
				Help: "Whether an Admin Console health request was served from the cached snapshot.",
			},
			[]string{"result"},
		),
		buildDuration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "nchat_admin_dashboard_build_duration_seconds",
				Help:    "Duration of assembling one Admin Console dashboard summary.",
				Buckets: []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2, 5},
			},
			[]string{"outcome"},
		),
	}
}

// Collectors exposes the collectors for registration into the shared registry.
func (m *HealthMetrics) Collectors() []prometheus.Collector {
	if m == nil {
		return nil
	}
	return []prometheus.Collector{m.checkDuration, m.checkResults, m.cacheEvents, m.buildDuration}
}

// recordCheck observes one probe.
//
// The state is validated against the domain's closed set before it becomes a
// label. An unrecognised value is recorded as unknown rather than as itself,
// so a future state added without touching this file cannot silently widen the
// label space.
func (m *HealthMetrics) recordCheck(id domain.HealthServiceID, state domain.HealthState, elapsed time.Duration) {
	if m == nil {
		return
	}
	if !domain.ValidHealthState(state) {
		state = domain.HealthUnknown
	}
	m.checkDuration.WithLabelValues(string(id)).Observe(elapsed.Seconds())
	m.checkResults.WithLabelValues(string(id), string(state)).Inc()
}

func (m *HealthMetrics) recordCache(hit bool) {
	if m == nil {
		return
	}
	result := "miss"
	if hit {
		result = "hit"
	}
	m.cacheEvents.WithLabelValues(result).Inc()
}

// recordDashboard observes one summary assembly. The outcome label separates a
// full answer from one whose counters were unavailable, which is the failure
// mode worth alerting on: the page still renders, so nothing else would show
// it.
func (m *HealthMetrics) recordDashboard(complete bool, elapsed time.Duration) {
	if m == nil {
		return
	}
	outcome := "partial"
	if complete {
		outcome = "complete"
	}
	m.buildDuration.WithLabelValues(outcome).Observe(elapsed.Seconds())
}
