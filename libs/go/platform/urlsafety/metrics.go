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
	resultCircuitOpen  = "circuit_open"
)

// Metrics counts verdict outcomes.
//
// It lives in this package rather than in each service because the counter is
// about the shared mechanism, not about either door. One definition means one
// metric name and one label set, so a dashboard does not have to know which
// service asked.
type Metrics struct {
	total *prometheus.CounterVec
	// providers separates the composed sources (issue #928). The aggregate above
	// answers "what did the pipeline decide"; this one answers "which source
	// said so, and when it did not, why" — the question a primary with a
	// fallback creates and a single counter cannot express.
	//
	// Both labels are closed sets this package owns: Name() comes from a
	// constant per adapter, and result is a verdict or a FailureReason. Nothing
	// derived from a URL, a hostname, a scan id or a credential can reach either.
	providers *prometheus.CounterVec
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
	providers := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "nchat_url_safety_provider_checks_total",
		Help: "URL safety provider exchanges by provider and outcome.",
	}, []string{"provider", "result"})
	if !metrics.Register(providers) {
		return &Metrics{total: total}
	}
	return &Metrics{total: total, providers: providers}
}

func (m *Metrics) observe(result string) {
	if m == nil || m.total == nil {
		return
	}
	m.total.WithLabelValues(result).Inc()
}

// observeProvider counts one exchange with one named provider. Nil-safe on both
// the receiver and the vector, so a deployment that wired no observability, or
// whose registry refused the second counter, simply counts nothing.
func (m *Metrics) observeProvider(provider, result string) {
	if m == nil || m.providers == nil {
		return
	}
	m.providers.WithLabelValues(provider, result).Inc()
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
