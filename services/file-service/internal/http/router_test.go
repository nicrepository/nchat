package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nicrepository/nchat/libs/go/platform/httputil"
	platformlog "github.com/nicrepository/nchat/libs/go/platform/log"
	"github.com/nicrepository/nchat/services/file-service/internal/config"
)

func TestBaseRoutes(t *testing.T) {
	router := NewRouter(testConfig(), platformlog.New("file-service", "test"))
	for _, path := range []string{RouteHealthz, RouteReadyz, RouteVersion} {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusOK {
			t.Fatalf("expected %s to return 200, got %d", path, response.Code)
		}
		if response.Header().Get("Content-Type") != "application/json" {
			t.Fatalf("expected JSON content type, got %q", response.Header().Get("Content-Type"))
		}
	}
}

func TestHealthzResponse(t *testing.T) {
	router := NewRouter(testConfig(), platformlog.New("file-service", "test"))
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, RouteHealthz, nil))
	var body httputil.Envelope
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	data := body.Data.(map[string]any)
	if data["service"] != "file-service" || data["status"] != "ok" {
		t.Fatalf("unexpected health data: %+v", data)
	}
}

func TestRouterErrorsAndMiddleware(t *testing.T) {
	router := NewRouter(testConfig(), platformlog.New("file-service", "test"))
	missing := httptest.NewRecorder()
	router.ServeHTTP(missing, httptest.NewRequest(http.MethodGet, "/missing", nil))
	if missing.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", missing.Code)
	}
	method := httptest.NewRecorder()
	router.ServeHTTP(method, httptest.NewRequest(http.MethodPost, RouteHealthz, nil))
	if method.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", method.Code)
	}
	if method.Header().Get("X-Request-ID") == "" || method.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("expected middleware headers, got %+v", method.Header())
	}
}

func testConfig() config.Config {
	return config.Config{ServiceName: "file-service", Env: "test", Port: 8083, ReadHeaderTimeoutSeconds: 5}
}
