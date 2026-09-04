package worker

import (
	"time"

	"github.com/nicrepository/nchat/libs/go/platform/observability"
	"github.com/prometheus/client_golang/prometheus"
)

// NotificationMetrics is what an operator needs to tell a healthy worker from a
// stalled one: how much is queued, how much is moving, and how long a delivery
// takes.
//
// # Cardinality
//
// One label, `result`, whose values are the closed set of constants below. No
// notification id, message id, workspace id, recipient, event type, provider or
// worker instance is ever a label. Every one of those is chosen by somebody
// other than this package — a user, a producer, an autoscaler — so a label
// derived from one would let ordinary traffic grow the series count without
// bound, and three of them are identifiers this service exists to keep private.
// Where an id genuinely helps an investigation it goes in a log line, which is
// sampled, retained and access-controlled differently from a metric.
type NotificationMetrics struct {
	backlog  prometheus.Gauge
	events   *prometheus.CounterVec
	duration *prometheus.HistogramVec
}

// The closed set of `result` values.
const (
	// Evaluation outcomes. Suppressed is a success, and is counted apart from
	// every failure for exactly that reason.
	resultEligible   = "eligible"
	resultSuppressed = "suppressed"

	// Claim and delivery outcomes.
	resultClaimed   = "claimed"
	resultDelivered = "delivered"
	resultRetry     = "retry"
	resultFailed    = "failed"

	// resultExhausted is an abandoned claim retired because its attempts ran
	// out. Distinct from failed: nothing reported a failure, the worker holding
	// it disappeared, and a rising count means crashes rather than a bad
	// provider.
	resultExhausted = "exhausted"

	// resultLeaseLost is a finalisation that found the row already moved on,
	// which is what a lease expiring under a slow delivery looks like. Normal in
	// small numbers, a sign the lease is too short in large ones.
	resultLeaseLost = "lease_lost"

	// resultError is a database or policy failure the worker could not act on.
	resultError = "error"
)

// NewNotificationMetrics registers the worker's collectors on the shared
// registry the service already serves at /metrics.
func NewNotificationMetrics(metrics *observability.Metrics) *NotificationMetrics {
	m := &NotificationMetrics{
		// A gauge, not a counter: a backlog goes down. No labels at all — the
		// question it answers, "is the queue draining", does not need a
		// breakdown, and a per-workspace one would grow with the product.
		backlog: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "nchat_notification_outbox_backlog",
			Help: "Notification events waiting to be delivered.",
		}),
		events: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "nchat_notification_events_total",
			Help: "Notification outbox events by processing outcome.",
		}, []string{"result"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "nchat_notification_delivery_duration_seconds",
			Help:    "Time spent in one delivery attempt, by outcome.",
			Buckets: prometheus.DefBuckets,
		}, []string{"result"}),
	}
	// Register reports false when metrics are disabled; the collectors still
	// work as no-op accumulators, so callers never need a nil check for that.
	metrics.Register(m.backlog, m.events, m.duration)
	return m
}

// ObserveBacklog records the current queue depth.
func (m *NotificationMetrics) ObserveBacklog(depth int) {
	if m == nil {
		return
	}
	m.backlog.Set(float64(depth))
}

// Count records one event outcome.
func (m *NotificationMetrics) Count(result string, quantity int) {
	if m == nil || quantity <= 0 {
		return
	}
	m.events.WithLabelValues(result).Add(float64(quantity))
}

// ObserveDelivery records one delivery attempt and how long it took.
func (m *NotificationMetrics) ObserveDelivery(result string, elapsed time.Duration) {
	if m == nil {
		return
	}
	m.events.WithLabelValues(result).Inc()
	m.duration.WithLabelValues(result).Observe(elapsed.Seconds())
}
