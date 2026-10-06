package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nicrepository/nchat/libs/go/platform/observability"
)

func TestPresenceMetricsUseOnlyTheResult(t *testing.T) {
	metrics := observability.NewMetrics(observability.Config{
		ServiceName: "chat-service", Environment: "test", MetricsEnabled: true,
	})
	presenceMetrics := newPresenceUpdateMetrics(metrics)
	for _, status := range []int{http.StatusOK, http.StatusBadRequest, http.StatusForbidden, http.StatusTooManyRequests, http.StatusInternalServerError} {
		handler := presenceMetrics.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
		}))
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPut, RoutePresenceMe, nil))
	}

	recorder := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := recorder.Body.String()
	for _, result := range []string{"success", "invalid", "denied", "rate_limited", "error"} {
		if !strings.Contains(body, `chat_presence_manual_update_total{result="`+result+`"} 1`) {
			t.Fatalf("missing result %q in metrics", result)
		}
	}
	for _, sensitive := range []string{"user_id", "workspace_id", "state=", "custom_status"} {
		if strings.Contains(body, sensitive) {
			t.Fatalf("metrics expose %q", sensitive)
		}
	}
}

func TestPresenceMetricsDisabledStillServes(t *testing.T) {
	presenceMetrics := newPresenceUpdateMetrics(nil)
	recorder := httptest.NewRecorder()
	presenceMetrics.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})).ServeHTTP(recorder, httptest.NewRequest(http.MethodDelete, RoutePresenceMe, nil))
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d", recorder.Code)
	}
}
