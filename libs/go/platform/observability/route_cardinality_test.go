package observability

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// The ids below are the shape a client controls: if any of them reaches a
// Prometheus label or a span name, an unauthenticated caller can mint an
// unbounded number of series by varying them.
const (
	idA = "11111111-1111-4111-8111-111111111111"
	idB = "22222222-2222-4222-8222-222222222222"
	idC = "33333333-3333-4333-8333-333333333333"
)

// newInstrumentedMux builds a router shaped like the file-service one: routes
// with client-controlled segments, an authentication gate that answers 401
// before the handler, and a catch-all. Each call gets its own Metrics, so every
// test owns an isolated Prometheus registry.
func newInstrumentedMux(t *testing.T, cfg Config) (http.Handler, *Metrics) {
	t.Helper()
	metrics := NewMetrics(cfg)

	unauthorized := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	notFound := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	mux := http.NewServeMux()
	mux.Handle("POST /channels/{channelID}/attachments", unauthorized)
	mux.Handle("POST /dm/{conversationID}/attachments", unauthorized)
	mux.Handle("GET /attachments/{attachmentID}", unauthorized)
	mux.Handle("GET /attachments/{attachmentID}/content", unauthorized)
	mux.Handle("GET /healthz", ok)
	mux.Handle("/", notFound)

	return HTTPMiddleware(cfg, metrics)(mux), metrics
}

func exposition(t *testing.T, metrics *Metrics) string {
	t.Helper()
	recorder := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body, err := io.ReadAll(recorder.Body)
	if err != nil {
		t.Fatalf("read exposition: %v", err)
	}
	return string(body)
}

// seriesFor returns the exported sample lines of a metric family.
func seriesFor(exported, metric string) []string {
	var matched []string
	for _, line := range strings.Split(exported, "\n") {
		if strings.HasPrefix(line, metric+"{") {
			matched = append(matched, line)
		}
	}
	return matched
}

func TestMetricsUseTheRouteTemplateNotTheRequestPath(t *testing.T) {
	tests := []struct {
		name     string
		method   string
		paths    []string
		template string
		status   string
	}{
		{
			name:   "channel upload",
			method: http.MethodPost,
			paths: []string{
				"/channels/" + idA + "/attachments",
				"/channels/" + idB + "/attachments",
			},
			template: "/channels/{channelID}/attachments",
			status:   "401",
		},
		{
			name:   "dm upload",
			method: http.MethodPost,
			paths: []string{
				"/dm/" + idA + "/attachments",
				"/dm/" + idB + "/attachments",
			},
			template: "/dm/{conversationID}/attachments",
			status:   "401",
		},
		{
			name:   "attachment metadata",
			method: http.MethodGet,
			paths: []string{
				"/attachments/" + idA,
				"/attachments/" + idB,
			},
			template: "/attachments/{attachmentID}",
			status:   "401",
		},
		{
			name:   "attachment content",
			method: http.MethodGet,
			paths: []string{
				"/attachments/" + idA + "/content",
				"/attachments/" + idB + "/content",
			},
			template: "/attachments/{attachmentID}/content",
			status:   "401",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Config{ServiceName: "file-service", MetricsEnabled: true}
			handler, metrics := newInstrumentedMux(t, cfg)

			for _, path := range tt.paths {
				recorder := httptest.NewRecorder()
				handler.ServeHTTP(recorder, httptest.NewRequest(tt.method, path, nil))
			}

			exported := exposition(t, metrics)
			for _, id := range []string{idA, idB} {
				if strings.Contains(exported, id) {
					t.Fatalf("exposition leaked the id %s:\n%s", id, exported)
				}
			}

			counters := seriesFor(exported, "nchat_http_requests_total")
			if len(counters) != 1 {
				t.Fatalf("expected exactly one counter series, got %d:\n%s", len(counters), strings.Join(counters, "\n"))
			}
			series := counters[0]
			for _, want := range []string{
				`route="` + tt.template + `"`,
				`method="` + tt.method + `"`,
				`status="` + tt.status + `"`,
				`service="file-service"`,
			} {
				if !strings.Contains(series, want) {
					t.Fatalf("expected %s in %q", want, series)
				}
			}
			// Both requests landed on the same series.
			if !strings.HasSuffix(strings.TrimSpace(series), " 2") {
				t.Fatalf("expected the series to count 2 requests, got %q", series)
			}

			// The histogram must describe the same series as the counter.
			histogram := seriesFor(exported, "nchat_http_request_duration_seconds_count")
			if len(histogram) != 1 {
				t.Fatalf("expected exactly one histogram series, got %d", len(histogram))
			}
			if !strings.Contains(histogram[0], `route="`+tt.template+`"`) {
				t.Fatalf("histogram uses a different route: %q", histogram[0])
			}
		})
	}
}

func TestMetricsNeverExposeARawPathLabel(t *testing.T) {
	cfg := Config{ServiceName: "file-service", MetricsEnabled: true}
	handler, metrics := newInstrumentedMux(t, cfg)

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/attachments/"+idA, nil))

	exported := exposition(t, metrics)
	if strings.Contains(exported, `path="`) {
		t.Fatalf("the raw-path label must be gone:\n%s", exported)
	}
	if !strings.Contains(exported, `route="`) {
		t.Fatalf("expected a route label:\n%s", exported)
	}
}

// Unrouted paths are the easiest way to mint series: they are unbounded by
// definition, so they must all collapse onto one closed value.
func TestUnmatchedRoutesCollapseOntoAClosedFallback(t *testing.T) {
	cfg := Config{ServiceName: "file-service", MetricsEnabled: true}
	handler, metrics := newInstrumentedMux(t, cfg)

	// The catch-all pattern "/" is itself a template, so it is a legitimate
	// bounded label. What must never appear is the arbitrary path.
	arbitrary := []string{
		"/" + idA,
		"/wp-admin/" + idB,
		"/a/b/c/d/" + idC,
		"/attachments/" + idA + "/content/extra",
	}
	for _, path := range arbitrary {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
	}

	exported := exposition(t, metrics)
	for _, id := range []string{idA, idB, idC} {
		if strings.Contains(exported, id) {
			t.Fatalf("exposition leaked %s:\n%s", id, exported)
		}
	}
	for _, path := range arbitrary {
		if strings.Contains(exported, `route="`+path+`"`) {
			t.Fatalf("exposition used the raw path %q as a label", path)
		}
	}
	counters := seriesFor(exported, "nchat_http_requests_total")
	if len(counters) != 1 {
		t.Fatalf("expected one series for all unrouted paths, got %d:\n%s",
			len(counters), strings.Join(counters, "\n"))
	}
	if !strings.HasSuffix(strings.TrimSpace(counters[0]), " 4") {
		t.Fatalf("expected 4 requests on one series, got %q", counters[0])
	}
}

// A method the route does not serve is answered by ServeMux itself, which
// leaves no pattern behind. That is the case the closed fallback exists for.
func TestMethodMismatchUsesTheClosedFallback(t *testing.T) {
	cfg := Config{ServiceName: "file-service", MetricsEnabled: true}
	metrics := NewMetrics(cfg)
	mux := http.NewServeMux()
	mux.Handle("POST /channels/{channelID}/attachments", http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusCreated) }))
	handler := HTTPMiddleware(cfg, metrics)(mux)

	for _, path := range []string{"/channels/" + idA + "/attachments", "/channels/" + idB + "/attachments"} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodDelete, path, nil))
		if recorder.Code != http.StatusMethodNotAllowed {
			t.Fatalf("expected 405, got %d", recorder.Code)
		}
	}

	exported := exposition(t, metrics)
	for _, id := range []string{idA, idB} {
		if strings.Contains(exported, id) {
			t.Fatalf("exposition leaked %s:\n%s", id, exported)
		}
	}
	counters := seriesFor(exported, "nchat_http_requests_total")
	if len(counters) != 1 {
		t.Fatalf("expected one series, got %d:\n%s", len(counters), strings.Join(counters, "\n"))
	}
	if !strings.Contains(counters[0], `route="`+UnmatchedRoute+`"`) {
		t.Fatalf("expected the closed fallback, got %q", counters[0])
	}
}

// The template must survive every exit path, not only the successful one.
func TestRouteIsRecordedForEveryOutcome(t *testing.T) {
	tests := []struct {
		name     string
		handler  http.Handler
		method   string
		path     string
		template string
		status   string
	}{
		{
			name: "unauthorized before the handler",
			handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusUnauthorized)
			}),
			method: http.MethodGet, path: "/attachments/" + idA,
			template: "/attachments/{attachmentID}", status: "401",
		},
		{
			name: "handler not found",
			handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNotFound)
			}),
			method: http.MethodGet, path: "/attachments/" + idA,
			template: "/attachments/{attachmentID}", status: "404",
		},
		{
			name: "handler error",
			handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
			}),
			method: http.MethodGet, path: "/attachments/" + idA,
			template: "/attachments/{attachmentID}", status: "500",
		},
		{
			name: "success",
			handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
			}),
			method: http.MethodGet, path: "/attachments/" + idA,
			template: "/attachments/{attachmentID}", status: "200",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Config{ServiceName: "file-service", MetricsEnabled: true}
			metrics := NewMetrics(cfg)
			mux := http.NewServeMux()
			mux.Handle("GET /attachments/{attachmentID}", tt.handler)
			handler := HTTPMiddleware(cfg, metrics)(mux)

			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, httptest.NewRequest(tt.method, tt.path, nil))

			exported := exposition(t, metrics)
			if strings.Contains(exported, idA) {
				t.Fatalf("exposition leaked %s:\n%s", idA, exported)
			}
			counters := seriesFor(exported, "nchat_http_requests_total")
			if len(counters) != 1 {
				t.Fatalf("expected one series, got %d", len(counters))
			}
			for _, want := range []string{`route="` + tt.template + `"`, `status="` + tt.status + `"`} {
				if !strings.Contains(counters[0], want) {
					t.Fatalf("expected %s in %q", want, counters[0])
				}
			}
		})
	}
}

// A panic below the middleware must still be recorded under a bounded route
// rather than being dropped, which is why recording is deferred.
func TestRouteIsRecordedWhenTheHandlerPanics(t *testing.T) {
	cfg := Config{ServiceName: "file-service", MetricsEnabled: true}
	metrics := NewMetrics(cfg)
	mux := http.NewServeMux()
	mux.Handle("GET /attachments/{attachmentID}", http.HandlerFunc(
		func(http.ResponseWriter, *http.Request) { panic("boom") }))
	handler := HTTPMiddleware(cfg, metrics)(mux)

	func() {
		defer func() { _ = recover() }()
		handler.ServeHTTP(httptest.NewRecorder(),
			httptest.NewRequest(http.MethodGet, "/attachments/"+idA, nil))
	}()

	exported := exposition(t, metrics)
	if strings.Contains(exported, idA) {
		t.Fatalf("exposition leaked %s:\n%s", idA, exported)
	}
	counters := seriesFor(exported, "nchat_http_requests_total")
	if len(counters) != 1 {
		t.Fatalf("expected the panicking request to be recorded once, got %d", len(counters))
	}
	if !strings.Contains(counters[0], `route="/attachments/{attachmentID}"`) {
		t.Fatalf("expected the route template, got %q", counters[0])
	}
}

// --- tracing ------------------------------------------------------------

// withSpanRecorder installs an isolated tracer provider and restores the
// previous one, so tracing assertions never depend on test ordering.
func withSpanRecorder(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(previous)
		_ = provider.Shutdown(context.Background())
	})
	return recorder
}

func spanAttribute(t *testing.T, span sdktrace.ReadOnlySpan, key string) string {
	t.Helper()
	for _, attribute := range span.Attributes() {
		if string(attribute.Key) == key {
			return attribute.Value.AsString()
		}
	}
	t.Fatalf("span %q has no attribute %q", span.Name(), key)
	return ""
}

func TestSpansUseTheRouteTemplateNotTheRequestPath(t *testing.T) {
	recorder := withSpanRecorder(t)
	cfg := Config{ServiceName: "file-service", TracingEnabled: true}
	handler, _ := newInstrumentedMux(t, cfg)

	for _, path := range []string{"/attachments/" + idA, "/attachments/" + idB} {
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, path, nil))
	}

	spans := recorder.Ended()
	if len(spans) != 2 {
		t.Fatalf("expected 2 spans, got %d", len(spans))
	}
	for _, span := range spans {
		if span.Name() != "GET /attachments/{attachmentID}" {
			t.Fatalf("unexpected span name %q", span.Name())
		}
		for _, id := range []string{idA, idB} {
			if strings.Contains(span.Name(), id) {
				t.Fatalf("span name leaked %s: %q", id, span.Name())
			}
		}
		if route := spanAttribute(t, span, "http.route"); route != "/attachments/{attachmentID}" {
			t.Fatalf("unexpected http.route %q", route)
		}
		if status := spanAttribute(t, span, "http.status_code"); status != "401" {
			t.Fatalf("unexpected http.status_code %q", status)
		}
		if method := spanAttribute(t, span, "http.method"); method != http.MethodGet {
			t.Fatalf("unexpected http.method %q", method)
		}
	}
	// Two different ids produced the same span name: cardinality is closed.
	if spans[0].Name() != spans[1].Name() {
		t.Fatal("requests to the same route must share one span name")
	}
	if !spans[0].EndTime().After(spans[0].StartTime()) {
		t.Fatal("span duration must still be recorded")
	}
}

func TestSpansUseTheClosedFallbackForUnroutedPaths(t *testing.T) {
	recorder := withSpanRecorder(t)
	cfg := Config{ServiceName: "file-service", TracingEnabled: true}
	metrics := NewMetrics(cfg)
	mux := http.NewServeMux()
	mux.Handle("GET /attachments/{attachmentID}", http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	handler := HTTPMiddleware(cfg, metrics)(mux)

	handler.ServeHTTP(httptest.NewRecorder(),
		httptest.NewRequest(http.MethodDelete, "/attachments/"+idA, nil))

	spans := recorder.Ended()
	if len(spans) != 1 {
		t.Fatalf("expected 1 span, got %d", len(spans))
	}
	if spans[0].Name() != "DELETE "+UnmatchedRoute {
		t.Fatalf("unexpected span name %q", spans[0].Name())
	}
	if route := spanAttribute(t, spans[0], "http.route"); route != UnmatchedRoute {
		t.Fatalf("unexpected http.route %q", route)
	}
	if strings.Contains(spans[0].Name(), idA) {
		t.Fatalf("span name leaked %s", idA)
	}
}

func TestSpansAreNotCreatedWhenTracingIsDisabled(t *testing.T) {
	recorder := withSpanRecorder(t)
	cfg := Config{ServiceName: "file-service", TracingEnabled: false}
	handler, _ := newInstrumentedMux(t, cfg)

	handler.ServeHTTP(httptest.NewRecorder(),
		httptest.NewRequest(http.MethodGet, "/attachments/"+idA, nil))

	if len(recorder.Ended()) != 0 {
		t.Fatalf("expected no spans, got %d", len(recorder.Ended()))
	}
}

// --- RouteTemplate ------------------------------------------------------

func TestRouteTemplateNormalisesPatterns(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
		want    string
	}{
		{name: "method and path", pattern: "POST /channels/{channelID}/attachments", want: "/channels/{channelID}/attachments"},
		{name: "path only", pattern: "/healthz", want: "/healthz"},
		{name: "catch all", pattern: "/", want: "/"},
		{name: "host and path", pattern: "example.com/path", want: "/path"},
		{name: "method host and path", pattern: "GET example.com/path", want: "/path"},
		{name: "empty", pattern: "", want: UnmatchedRoute},
		{name: "relative", pattern: "not-a-route", want: UnmatchedRoute},
		{name: "method without path", pattern: "GET ", want: UnmatchedRoute},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/anything", nil)
			request.Pattern = tt.pattern
			if got := RouteTemplate(request); got != tt.want {
				t.Fatalf("expected %q, got %q", tt.want, got)
			}
		})
	}
}

func TestRouteTemplateHandlesANilRequest(t *testing.T) {
	if got := RouteTemplate(nil); got != UnmatchedRoute {
		t.Fatalf("expected %q, got %q", UnmatchedRoute, got)
	}
}
