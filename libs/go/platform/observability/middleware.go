package observability

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
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
func HTTPMiddleware(cfg Config, m *Metrics) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			path := r.URL.Path
			start := time.Now()

			if cfg.MetricsEnabled && m != nil && m.inFlight != nil {
				m.inFlight.WithLabelValues(cfg.ServiceName).Inc()
				defer m.inFlight.WithLabelValues(cfg.ServiceName).Dec()
			}

			ctx := r.Context()
			var span trace.Span
			if cfg.TracingEnabled {
				ctx, span = otel.Tracer(cfg.ServiceName).Start(ctx, fmt.Sprintf("%s %s", r.Method, path))
				span.SetAttributes(
					attribute.String("http.method", r.Method),
					attribute.String("http.route", path),
					attribute.String("service.name", cfg.ServiceName),
				)
				defer span.End()
				r = r.WithContext(ctx)
			}

			rw := &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}
			next.ServeHTTP(rw, r)

			status := fmt.Sprintf("%d", rw.statusCode)
			elapsed := time.Since(start).Seconds()

			if cfg.MetricsEnabled && m != nil && m.requestsTotal != nil {
				m.requestsTotal.WithLabelValues(cfg.ServiceName, r.Method, path, status).Inc()
				m.requestDuration.WithLabelValues(cfg.ServiceName, r.Method, path, status).Observe(elapsed)
			}

			if cfg.TracingEnabled && span != nil {
				span.SetAttributes(attribute.String("http.status_code", status))
			}
		})
	}
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
