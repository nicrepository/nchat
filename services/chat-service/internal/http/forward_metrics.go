package httpapi

import (
	"net/http"
	"time"

	"github.com/nicrepository/nchat/libs/go/platform/observability"
	"github.com/prometheus/client_golang/prometheus"
)

type forwardMetrics struct {
	total    *prometheus.CounterVec
	duration *prometheus.HistogramVec
}

func newForwardMetrics(metrics *observability.Metrics) *forwardMetrics {
	total := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "chat_message_forward_total",
		Help: "Forward-message HTTP operations by result.",
	}, []string{"result"})
	duration := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "chat_message_forward_duration_seconds",
		Help:    "Forward-message HTTP operation duration by result.",
		Buckets: prometheus.DefBuckets,
	}, []string{"result"})
	if !metrics.Register(total, duration) {
		return &forwardMetrics{}
	}
	return &forwardMetrics{total: total, duration: duration}
}

func (m *forwardMetrics) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		recorder := &forwardStatusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(recorder, r)
		if m.total == nil {
			return
		}
		result := forwardMetricResult(recorder.status)
		m.total.WithLabelValues(result).Inc()
		m.duration.WithLabelValues(result).Observe(time.Since(started).Seconds())
	})
}

type forwardStatusRecorder struct {
	http.ResponseWriter
	status int
}

func (w *forwardStatusRecorder) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func forwardMetricResult(status int) string {
	if status == http.StatusOK {
		return "replay"
	}
	switch {
	case status >= http.StatusOK && status < http.StatusMultipleChoices:
		return "success"
	case status == http.StatusBadRequest:
		return "invalid"
	case status == http.StatusNotFound || status == http.StatusForbidden:
		return "denied"
	case status == http.StatusConflict:
		return "conflict"
	case status == http.StatusTooManyRequests:
		return "rate_limited"
	default:
		return "error"
	}
}
