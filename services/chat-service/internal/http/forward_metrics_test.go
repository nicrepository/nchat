package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nicrepository/nchat/libs/go/platform/observability"
)

func TestForwardMetricsUsesOnlyFixedLowCardinalityResults(t *testing.T) {
	metrics := observability.NewMetrics(observability.Config{
		ServiceName: "chat-service", Environment: "test", MetricsEnabled: true,
	})
	forwardMetrics := newForwardMetrics(metrics)
	for _, test := range []struct {
		status int
		result string
	}{
		{status: http.StatusCreated, result: "success"},
		{status: http.StatusOK, result: "replay"},
		{status: http.StatusBadRequest, result: "invalid"},
		{status: http.StatusNotFound, result: "denied"},
		{status: http.StatusConflict, result: "conflict"},
		{status: http.StatusTooManyRequests, result: "rate_limited"},
		{status: http.StatusInternalServerError, result: "error"},
	} {
		handler := forwardMetrics.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(test.status)
		}))
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/forward", nil))
	}

	recorder := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := recorder.Body.String()
	for _, result := range []string{"success", "replay", "invalid", "denied", "conflict", "rate_limited", "error"} {
		if !strings.Contains(body, `chat_message_forward_total{result="`+result+`"} 1`) {
			t.Fatalf("missing fixed result %q in metrics", result)
		}
	}
	for _, sensitive := range []string{"source_message_id", "destination_channel_id", "actor_id", "workspace_id", "idempotency_key"} {
		if strings.Contains(strings.ToLower(body), sensitive) {
			t.Fatalf("metrics expose forbidden dimension %q", sensitive)
		}
	}
}
