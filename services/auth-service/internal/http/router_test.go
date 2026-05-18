package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nicrepository/nchat/libs/go/platform/httputil"
	platformlog "github.com/nicrepository/nchat/libs/go/platform/log"
	"github.com/nicrepository/nchat/services/auth-service/internal/config"
)

func TestBaseRoutes(t *testing.T) {
	router := NewRouter(testConfig(), platformlog.New("auth-service", "test"))

	tests := []struct {
		path string
		want int
	}{
		{RouteHealthz, http.StatusOK},
		{RouteReadyz, http.StatusOK},
		{RouteVersion, http.StatusOK},
		{"/missing", http.StatusNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, tt.path, nil))

			if response.Code != tt.want {
				t.Fatalf("expected status %d, got %d", tt.want, response.Code)
			}
			if response.Header().Get("Content-Type") != "application/json" {
				t.Fatalf("expected application/json content type, got %q", response.Header().Get("Content-Type"))
			}
		})
	}
}

func TestHealthzResponse(t *testing.T) {
	router := NewRouter(testConfig(), platformlog.New("auth-service", "test"))
	response := httptest.NewRecorder()

	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, RouteHealthz, nil))

	var body httputil.Envelope
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	data := body.Data.(map[string]any)
	if data["service"] != "auth-service" {
		t.Fatalf("expected auth-service, got %v", data["service"])
	}
	if data["status"] != "ok" {
		t.Fatalf("expected ok, got %v", data["status"])
	}
}

func TestRouterMiddlewareAndMethodHandling(t *testing.T) {
	router := NewRouter(testConfig(), platformlog.New("auth-service", "test"))
	response := httptest.NewRecorder()

	router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, RouteHealthz, nil))

	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected status 405, got %d", response.Code)
	}
	if response.Header().Get("X-Request-ID") == "" {
		t.Fatal("expected X-Request-ID")
	}
	if response.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("expected security headers")
	}
}

func testConfig() config.Config {
	return config.Config{ServiceName: "auth-service", Env: "test", Port: 8081, ReadHeaderTimeoutSeconds: 5}
}
