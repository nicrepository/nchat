package urlsafety

import (
	"github.com/nicrepository/nchat/libs/go/platform/observability"
	"github.com/prometheus/client_golang/prometheus"
)

// Outcome labels. The set is closed and decided here: no hostname, URL, user or
// provider response is ever a label. The caller of a check picks the value it
// asks about, so a label derived from it would let one hostile client create an
// unbounded number of series.
const (
	resultHit          = "hit"
	resultMiss         = "miss"
	resultSubmitted    = "submitted"
	resultPending      = "pending"
	resultSafe         = "safe"
	resultMalicious    = "malicious"
	resultError        = "error"
	resultInconclusive = "inconclusive"
)

// Metrics counts verdict outcomes.
//
// It lives in this package rather than in each service because the counter is
// about the shared mechanism, not about either door. One definition means one
// metric name and one label set, so a dashboard does not have to know which
// service asked.
type Metrics struct {
	total *prometheus.CounterVec
}

// NewMetrics registers the counter. A nil *Metrics is usable and does nothing,
// which is what lets a caller wire observability optionally.
func NewMetrics(metrics *observability.Metrics) *Metrics {
	if metrics == nil {
		return nil
	}
	total := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "nchat_url_safety_checks_total",
		Help: "URL safety verdicts by result.",
	}, []string{"result"})
	if !metrics.Register(total) {
		return nil
	}
	return &Metrics{total: total}
}

func (m *Metrics) observe(result string) {
	if m == nil || m.total == nil {
		return
	}
	m.total.WithLabelValues(result).Inc()
}

func resultFor(verdict Verdict) string {
	switch verdict {
	case VerdictSafe:
		return resultSafe
	case VerdictMalicious:
		return resultMalicious
	default:
		return resultError
	}
}
