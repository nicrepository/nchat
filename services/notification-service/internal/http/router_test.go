package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/nicrepository/nchat/libs/go/platform/health"
	"github.com/nicrepository/nchat/libs/go/platform/httputil"
	platformlog "github.com/nicrepository/nchat/libs/go/platform/log"
	"github.com/nicrepository/nchat/services/notification-service/internal/config"
)

func TestHealthzContract(t *testing.T) {
	router := NewRouter(testConfig(), platformlog.New("notification-service", "test"))
	response := httptest.NewRecorder()

	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, RouteHealthz, nil))

	assertJSONResponse(t, response, http.StatusOK)
	body := decodeHealthEnvelope(t, response)
	if body.Data.Service != "notification-service" {
		t.Fatalf("expected service notification-service, got %q", body.Data.Service)
	}
	if body.Data.Probe != health.ProbeLiveness {
		t.Fatalf("expected liveness probe, got %q", body.Data.Probe)
	}
	if body.Data.Status != health.StatusOK {
		t.Fatalf("expected ok status, got %q", body.Data.Status)
	}
	if body.Data.Version != "0.0.0" {
		t.Fatalf("expected version 0.0.0, got %q", body.Data.Version)
	}
	if body.Data.Commit != "dev" {
		t.Fatalf("expected commit dev, got %q", body.Data.Commit)
	}
	assertRFC3339(t, body.Data.CheckedAt)
	if len(body.Data.Checks) != 0 {
		t.Fatalf("expected no liveness checks, got %d", len(body.Data.Checks))
	}
}

func TestReadyzContract(t *testing.T) {
	router := NewRouter(testConfig(), platformlog.New("notification-service", "test"))
	response := httptest.NewRecorder()

	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, RouteReadyz, nil))

	assertJSONResponse(t, response, http.StatusOK)
	body := decodeHealthEnvelope(t, response)
	if body.Data.Service != "notification-service" {
		t.Fatalf("expected service notification-service, got %q", body.Data.Service)
	}
	if body.Data.Probe != health.ProbeReadiness {
		t.Fatalf("expected readiness probe, got %q", body.Data.Probe)
	}
	if body.Data.Status != health.StatusReady {
		t.Fatalf("expected ready status, got %q", body.Data.Status)
	}
	if body.Data.Version != "0.0.0" {
		t.Fatalf("expected version 0.0.0, got %q", body.Data.Version)
	}
	if body.Data.Commit != "dev" {
		t.Fatalf("expected commit dev, got %q", body.Data.Commit)
	}
	assertRFC3339(t, body.Data.CheckedAt)
	assertReadinessCheck(t, body.Data.Checks, "service-bootstrap")
	assertReadinessCheck(t, body.Data.Checks, "config-loaded")
	assertReadinessCheck(t, body.Data.Checks, "smtp-worker-config")
}

func TestReadyzFailsWhenSMTPWorkerIsMisconfigured(t *testing.T) {
	cfg := testConfig()
	cfg.SMTPWorkerEnabled = true
	router := NewRouter(cfg, platformlog.New("notification-service", "test"))
	response := httptest.NewRecorder()

	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, RouteReadyz, nil))

	assertJSONResponse(t, response, http.StatusServiceUnavailable)
	body := decodeHealthEnvelope(t, response)
	if body.Data.Status != health.StatusUnready {
		t.Fatalf("expected unready status, got %q", body.Data.Status)
	}
	assertReadinessFailure(t, body.Data.Checks, "smtp-worker-config", "SMTP_HOST is required")
}

func TestVersionRouteStillWorks(t *testing.T) {
	router := NewRouter(testConfig(), platformlog.New("notification-service", "test"))
	response := httptest.NewRecorder()

	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, RouteVersion, nil))

	assertJSONResponse(t, response, http.StatusOK)
	var body struct {
		Data map[string]string `json:"data"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode version response: %v", err)
	}
	if body.Data["service"] != "notification-service" || body.Data["version"] != "0.0.0" || body.Data["commit"] != "dev" {
		t.Fatalf("unexpected version response: %+v", body.Data)
	}
}

func TestMethodAndNotFoundBehavior(t *testing.T) {
	router := NewRouter(testConfig(), platformlog.New("notification-service", "test"))

	tests := []struct {
		name   string
		method string
		path   string
		want   int
	}{
		{name: "post healthz", method: http.MethodPost, path: RouteHealthz, want: http.StatusMethodNotAllowed},
		{name: "post readyz", method: http.MethodPost, path: RouteReadyz, want: http.StatusMethodNotAllowed},
		{name: "missing", method: http.MethodGet, path: "/missing", want: http.StatusNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(tt.method, tt.path, nil))
			assertJSONResponse(t, response, tt.want)
		})
	}
}

type healthEnvelope struct {
	Data health.Response `json:"data"`
}

func decodeHealthEnvelope(t *testing.T, response *httptest.ResponseRecorder) healthEnvelope {
	t.Helper()

	var generic httputil.Envelope
	if err := json.Unmarshal(response.Body.Bytes(), &generic); err != nil {
		t.Fatalf("decode generic envelope: %v", err)
	}
	if generic.Error != nil {
		t.Fatalf("expected data envelope, got error %+v", generic.Error)
	}

	var body healthEnvelope
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode health envelope: %v", err)
	}
	return body
}

func assertJSONResponse(t *testing.T, response *httptest.ResponseRecorder, wantStatus int) {
	t.Helper()

	if response.Code != wantStatus {
		t.Fatalf("expected status %d, got %d", wantStatus, response.Code)
	}
	if response.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("expected application/json content type, got %q", response.Header().Get("Content-Type"))
	}
	if response.Header().Get("X-Request-ID") == "" {
		t.Fatal("expected X-Request-ID")
	}
	if response.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("expected X-Content-Type-Options nosniff, got %q", response.Header().Get("X-Content-Type-Options"))
	}
}

func assertReadinessCheck(t *testing.T, checks []health.CheckResult, name string) {
	t.Helper()

	for _, check := range checks {
		if check.Name == name {
			if check.Status != health.CheckPass {
				t.Fatalf("expected %s to pass, got %q", name, check.Status)
			}
			if !check.Critical {
				t.Fatalf("expected %s to be critical", name)
			}
			if check.DurationMS < 0 {
				t.Fatalf("expected %s duration to be non-negative, got %d", name, check.DurationMS)
			}
			return
		}
	}
	t.Fatalf("expected readiness check %s in %+v", name, checks)
}

func assertReadinessFailure(t *testing.T, checks []health.CheckResult, name, message string) {
	t.Helper()

	for _, check := range checks {
		if check.Name == name {
			if check.Status != health.CheckFail {
				t.Fatalf("expected %s to fail, got %q", name, check.Status)
			}
			if check.Message != message {
				t.Fatalf("expected %s message %q, got %q", name, message, check.Message)
			}
			return
		}
	}
	t.Fatalf("expected readiness failure %s in %+v", name, checks)
}

func assertRFC3339(t *testing.T, value string) {
	t.Helper()

	if _, err := time.Parse(time.RFC3339, value); err != nil {
		t.Fatalf("expected RFC3339 timestamp, got %q: %v", value, err)
	}
}

func testConfig() config.Config {
	return config.Config{ServiceName: "notification-service", Env: "test", Port: 8084, ReadHeaderTimeoutSeconds: 5}
}

func TestMetricsRouteReturns200(t *testing.T) {
	router := NewRouter(testConfig(), platformlog.New("notification-service", "test"))
	response := httptest.NewRecorder()

	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, RouteMetrics, nil))

	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", response.Code)
	}
	body := response.Body.String()
	if body == "" {
		t.Fatal("expected non-empty metrics body")
	}
}

// errorCode reads the machine-readable code of the error envelope. The code is
// the contract; the message beside it is prose and is deliberately not asserted.
func errorCode(t *testing.T, response *httptest.ResponseRecorder) string {
	t.Helper()
	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode error envelope: %v (%s)", err, response.Body.String())
	}
	return envelope.Error.Code
}

// Issue #902: the push routes are registered whether or not the subscription
// dependencies were wired. A process that could not build them answers 503 from
// Authenticate, never the catch-all's 404, and never runs a handler.
func TestPushRoutesWithoutSubscriptionWiringAnswer503(t *testing.T) {
	router := NewRouter(config.Config{ServiceName: "notification-service"}, nil)
	for _, route := range []struct{ method, path string }{
		{http.MethodGet, RoutePushConfig},
		{http.MethodGet, RoutePushSubscriptions},
		{http.MethodPost, RoutePushSubscriptions},
		{http.MethodDelete, RoutePushSubscriptions + "/abc"},
	} {
		request := httptest.NewRequest(route.method, route.path, nil)
		request.Header.Set("Authorization", "Bearer token")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s %s: status = %d, want 503", route.method, route.path, response.Code)
		}
		if code := errorCode(t, response); code != "service_unavailable" {
			t.Fatalf("%s %s: code = %q, want service_unavailable", route.method, route.path, code)
		}
	}
}

// Issue #902: registering the routes unconditionally must not widen the surface.
// A path nobody registered is still 404, a method nobody registered is still
// answered by the catch-all (404, as before), and validator plus resolver
// without a handler never reach a handler.
func TestPushRouteSurfaceStaysClosedWhereItWasClosed(t *testing.T) {
	wired := configRouter(workingNotificationConfig(), running)
	for _, c := range []struct {
		method, path string
		want         int
	}{
		{http.MethodGet, "/api/notifications/push/unknown", http.StatusNotFound},
		{http.MethodPost, RoutePushConfig, http.StatusNotFound},
		{http.MethodDelete, RoutePushConfig, http.StatusNotFound},
	} {
		request := httptest.NewRequest(c.method, c.path, nil)
		request.Header.Set("Authorization", "Bearer token")
		response := httptest.NewRecorder()
		wired.ServeHTTP(response, request)
		if response.Code != c.want {
			t.Fatalf("%s %s: status = %d, want %d", c.method, c.path, response.Code, c.want)
		}
		if code := errorCode(t, response); code != httputil.ErrCodeNotFound {
			t.Fatalf("%s %s: code = %q, want %s", c.method, c.path, code, httputil.ErrCodeNotFound)
		}
	}

	partial := NewRouter(workingNotificationConfig(), nil, WithPushSubscriptions(
		fakeValidator{}, &fakeResolver{}, nil))
	request := httptest.NewRequest(http.MethodPost, RoutePushSubscriptions, nil)
	request.Header.Set("Authorization", "Bearer token")
	response := httptest.NewRecorder()
	partial.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("handler-less wiring: status = %d, want 503", response.Code)
	}
	if code := errorCode(t, response); code != "service_unavailable" {
		t.Fatalf("handler-less wiring: code = %q, want service_unavailable", code)
	}
}
