package app

import (
	"github.com/nicrepository/nchat/libs/go/platform/observability"
	"github.com/prometheus/client_golang/prometheus"
)

type ownershipOutboxMetrics struct {
	pending   prometheus.Gauge
	oldestAge prometheus.Gauge
	failures  prometheus.Counter
	retries   prometheus.Counter
}

func newOwnershipOutboxMetrics(registry *observability.Metrics) *ownershipOutboxMetrics {
	if registry == nil {
		return nil
	}
	m := &ownershipOutboxMetrics{
		pending:   prometheus.NewGauge(prometheus.GaugeOpts{Name: "chat_ownership_outbox_pending", Help: "Committed unpublished ownership invalidations in the shared database; do not sum replicas."}),
		oldestAge: prometheus.NewGauge(prometheus.GaugeOpts{Name: "chat_ownership_outbox_oldest_pending_age_seconds", Help: "Age of the oldest unpublished ownership invalidation, including delayed retries."}),
		failures:  prometheus.NewCounter(prometheus.CounterOpts{Name: "chat_ownership_outbox_publish_failures_total", Help: "Failed ownership invalidation publish attempts on this replica."}),
		retries:   prometheus.NewCounter(prometheus.CounterOpts{Name: "chat_ownership_outbox_retries_total", Help: "Ownership invalidation publish attempts with a previously persisted attempt on this replica."}),
	}
	if !registry.Register(m.pending, m.oldestAge, m.failures, m.retries) {
		return nil
	}
	return m
}

func (m *ownershipOutboxMetrics) attempt(retry bool, err error) {
	if m == nil {
		return
	}
	if retry {
		m.retries.Inc()
	}
	if err != nil {
		m.failures.Inc()
	}
}

func (m *ownershipOutboxMetrics) backlog(pending int64, oldestAge float64) {
	m.pending.Set(float64(pending))
	m.oldestAge.Set(oldestAge)
}
