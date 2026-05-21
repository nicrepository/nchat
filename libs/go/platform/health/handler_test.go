package health

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/nicrepository/nchat/libs/go/platform/httputil"
)

func TestLivenessHandlerReturnsJSONEnvelope(t *testing.T) {
	handler := LivenessHandler("auth-service", "1.2.3", "abc123")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", response.Code)
	}
	if response.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("expected application/json content type, got %q", response.Header().Get("Content-Type"))
	}

	body := decodeHealthEnvelope(t, response)
	if body.Data.Probe != ProbeLiveness {
		t.Fatalf("expected liveness probe, got %q", body.Data.Probe)
	}
	if body.Data.Status != StatusOK {
		t.Fatalf("expected ok status, got %q", body.Data.Status)
	}
}

func TestReadinessHandlerReturns503ForCriticalFailure(t *testing.T) {
	handler := ReadinessHandler("auth-service", "1.2.3", "abc123", []Checker{
		NewStaticChecker("service-bootstrap", true, CheckFail, ""),
	}, time.Second)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", response.Code)
	}
	body := decodeHealthEnvelope(t, response)
	if body.Data.Status != StatusUnready {
		t.Fatalf("expected unready, got %q", body.Data.Status)
	}
}

type healthEnvelope struct {
	Data Response `json:"data"`
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
