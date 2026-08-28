package observability

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// HTTPMiddleware returns a middleware that records Prometheus metrics and
// OpenTelemetry spans for each HTTP request.
//
// Metrics are recorded only when cfg.MetricsEnabled is true.
// Spans are created only when cfg.TracingEnabled is true.
// Authorization, Cookie and request bodies are never recorded.
//
// Requests are attributed to their route template, never to the raw path.
// Routes carry client-controlled segments (channel, conversation and
// attachment ids), and this middleware runs before authentication, so labelling
// by path would let an unauthenticated caller mint an unbounded number of
// Prometheus series and span names simply by varying an id. The template is
// only known after the router has matched, so recording happens on the way out.
func HTTPMiddleware(cfg Config, m *Metrics) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()

			if cfg.MetricsEnabled && m != nil && m.inFlight != nil {
				m.inFlight.WithLabelValues(cfg.ServiceName).Inc()
				defer m.inFlight.WithLabelValues(cfg.ServiceName).Dec()
			}

			var span trace.Span
			if cfg.TracingEnabled {
				var ctx = r.Context()
				// The span opens before the route is known, so it starts under a
				// name that carries no path at all and is renamed on the way out.
				ctx, span = otel.Tracer(cfg.ServiceName).Start(ctx, r.Method)
				r = r.WithContext(ctx)
			}

			rw := &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}
			// Deferred so an early return or a panic further down still records
			// the request under a bounded route rather than being lost.
			defer func() {
				route := RouteTemplate(r)
				status := strconv.Itoa(rw.statusCode)
				recordRequestMetrics(cfg, m, r.Method, route, status, time.Since(start))
				finishRequestSpan(cfg, span, r.Method, route, status)
			}()

			next.ServeHTTP(rw, r)
		})
	}
}

func recordRequestMetrics(cfg Config, m *Metrics, method, route, status string, elapsed time.Duration) {
	if !cfg.MetricsEnabled || m == nil || m.requestsTotal == nil {
		return
	}
	// The counter and the histogram share the same normalised template so the
	// two always describe the same set of series.
	m.requestsTotal.WithLabelValues(cfg.ServiceName, method, route, status).Inc()
	m.requestDuration.WithLabelValues(cfg.ServiceName, method, route, status).Observe(elapsed.Seconds())
}

func finishRequestSpan(cfg Config, span trace.Span, method, route, status string) {
	if !cfg.TracingEnabled || span == nil {
		return
	}
	span.SetName(method + " " + route)
	span.SetAttributes(
		attribute.String("http.method", method),
		attribute.String("http.route", route),
		attribute.String("http.status_code", status),
		attribute.String("service.name", cfg.ServiceName),
	)
	span.End()
}

// responseWriter wraps http.ResponseWriter to capture the response status code.
type responseWriter struct {
	http.ResponseWriter
	statusCode int
	written    bool
}

func (rw *responseWriter) WriteHeader(code int) {
	if !rw.written {
		rw.statusCode = code
		rw.written = true
	}
	rw.ResponseWriter.WriteHeader(code)
}

func (rw *responseWriter) Write(b []byte) (int, error) {
	if !rw.written {
		rw.statusCode = http.StatusOK
		rw.written = true
	}
	return rw.ResponseWriter.Write(b)
}

func (rw *responseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := rw.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("response writer does not support hijacking")
	}
	return hijacker.Hijack()
}

func (rw *responseWriter) Flush() {
	flusher, ok := rw.ResponseWriter.(http.Flusher)
	if !ok {
		return
	}
	if !rw.written {
		rw.statusCode = http.StatusOK
		rw.written = true
	}
	flusher.Flush()
}

func (rw *responseWriter) Unwrap() http.ResponseWriter {
	return rw.ResponseWriter
}
