package app

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nicrepository/nchat/libs/go/platform/observability"
)

func TestHubDeliveryMetrics(t *testing.T) {
	if newHubDeliveryMetrics(nil) != nil {
		t.Fatal("disabled metrics produced a collector")
	}
	registry := observability.NewMetrics(observability.Config{ServiceName: "chat-service", MetricsEnabled: true})
	metrics := newHubDeliveryMetrics(registry)
	metrics.AuthRecheck("dm", "timeout")
	metrics.SyncRecovery("signalled")
	metrics.Disconnect("heartbeat")

	recorder := httptest.NewRecorder()
	registry.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := recorder.Body.String()
	for _, want := range []string{
		`chat_ws_auth_recheck_total{outcome="timeout",target_type="dm"} 1`,
		`chat_ws_sync_recovery_total{outcome="signalled"} 1`,
		`chat_ws_disconnect_total{reason="heartbeat"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("metrics missing %q", want)
		}
	}
}
