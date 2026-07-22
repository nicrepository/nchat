package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nicrepository/nchat/libs/go/platform/health"
	platformlog "github.com/nicrepository/nchat/libs/go/platform/log"
)

func TestRouterReadyzFailsWhenConfiguredDatabaseBootstrapUnavailable(t *testing.T) {
	cfg := testConfig()
	cfg.DatabaseURL = "postgres://configured"
	router := NewRouter(cfg, platformlog.New("auth-service", "test"), nil, nil, nil, nil, nil, nil, nil, nil)
	response := httptest.NewRecorder()

	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, RouteReadyz, nil))

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected status 503, got %d", response.Code)
	}
	body := decodeHealthEnvelope(t, response)
	if body.Data.Status != health.StatusUnready {
		t.Fatalf("expected unready status, got %q", body.Data.Status)
	}
	for _, check := range body.Data.Checks {
		if check.Name == "service-bootstrap" {
			if check.Status != health.CheckFail || !check.Critical {
				t.Fatalf("unexpected bootstrap check: %+v", check)
			}
			return
		}
	}
	t.Fatal("service-bootstrap readiness check not found")
}
