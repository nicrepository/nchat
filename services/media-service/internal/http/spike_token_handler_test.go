package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nicrepository/nchat/libs/go/platform/httputil"
	"github.com/nicrepository/nchat/services/media-service/internal/config"
	"github.com/nicrepository/nchat/services/media-service/internal/service"
)

const testSpikeOrigin = "http://localhost:5173"

type fakeSpikeTokenIssuer struct {
	result service.SpikeToken
	err    error
	calls  int
}

func (f *fakeSpikeTokenIssuer) Issue(room, identity, name string) (service.SpikeToken, error) {
	f.calls++
	return f.result, f.err
}

func TestSpikeTokenHandlerReturnsEnvelopeAndRedactedLog(t *testing.T) {
	issuer := &fakeSpikeTokenIssuer{result: service.SpikeToken{
		ServerURL:        "ws://127.0.0.1:7880",
		Token:            "sensitive-participant-token",
		Room:             "spike-1to1",
		Identity:         "browser-a",
		ExpiresInSeconds: 300,
	}}
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	handler := SpikeToken(issuer, spikeHTTPConfig("development"), logger)
	response := serveSpikeRequest(handler, `{"room":"spike-1to1","identity":"browser-a","name":"Browser A"}`, testSpikeOrigin)

	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", response.Code, response.Body.String())
	}
	var body struct {
		Data service.SpikeToken `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Data.Token != issuer.result.Token || body.Data.Identity != "browser-a" {
		t.Fatalf("unexpected response: %+v", body.Data)
	}
	if issuer.calls != 1 {
		t.Fatalf("expected one issuer call, got %d", issuer.calls)
	}
	logged := logs.String()
	for _, sensitive := range []string{issuer.result.Token, "browser-a", "spike-1to1"} {
		if strings.Contains(logged, sensitive) {
			t.Fatalf("log contains sensitive value %q: %s", sensitive, logged)
		}
	}
	if !strings.Contains(logged, "room_ref") || !strings.Contains(logged, "token issuance attempted") {
		t.Fatalf("expected operational log fields, got %s", logged)
	}
}

func TestSpikeTokenHandlerRejectsInvalidBodies(t *testing.T) {
	tests := []struct {
		name string
		body string
		want int
	}{
		{name: "malformed JSON", body: `{`, want: http.StatusBadRequest},
		{name: "unknown field", body: `{"room":"spike-1to1","identity":"browser-a","extra":true}`, want: http.StatusBadRequest},
		{name: "trailing JSON", body: `{"room":"spike-1to1","identity":"browser-a"}{}`, want: http.StatusBadRequest},
		{name: "oversized", body: `{"room":"` + strings.Repeat("a", 1100) + `","identity":"browser-a"}`, want: http.StatusRequestEntityTooLarge},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			issuer := &fakeSpikeTokenIssuer{}
			response := serveSpikeRequest(SpikeToken(issuer, spikeHTTPConfig("development"), slog.Default()), tt.body, testSpikeOrigin)
			if response.Code != tt.want {
				t.Fatalf("expected %d, got %d: %s", tt.want, response.Code, response.Body.String())
			}
			if issuer.calls != 0 {
				t.Fatalf("issuer called for invalid body: %d", issuer.calls)
			}
		})
	}
}

func TestSpikeTokenHandlerRejectsInvalidInput(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{name: "room", err: service.ErrInvalidSpikeRoom},
		{name: "identity", err: service.ErrInvalidSpikeIdentity},
		{name: "name", err: service.ErrInvalidSpikeName},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			issuer := &fakeSpikeTokenIssuer{err: tt.err}
			response := serveSpikeRequest(SpikeToken(issuer, spikeHTTPConfig("development"), slog.Default()), `{"room":"spike-1to1","identity":"browser-a"}`, testSpikeOrigin)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d: %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestSpikeTokenHandlerRejectsUntrustedOrigin(t *testing.T) {
	issuer := &fakeSpikeTokenIssuer{}
	handler := SpikeToken(issuer, spikeHTTPConfig("development"), slog.Default())
	for _, origin := range []string{"", "https://evil.example"} {
		response := serveSpikeRequest(handler, `{"room":"spike-1to1","identity":"browser-a"}`, origin)
		if response.Code != http.StatusForbidden {
			t.Fatalf("expected forbidden for origin %q, got %d", origin, response.Code)
		}
	}
	if issuer.calls != 0 {
		t.Fatalf("issuer called for untrusted origin: %d", issuer.calls)
	}
}

func TestSpikeTokenHandlerReturnsSafeErrorWhenIssuerUnavailable(t *testing.T) {
	handler := SpikeToken(nil, spikeHTTPConfig("development"), slog.Default())
	response := serveSpikeRequest(handler, `{"room":"spike-1to1","identity":"browser-a"}`, testSpikeOrigin)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", response.Code)
	}
	var body httputil.Envelope
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Error == nil || strings.Contains(strings.ToLower(body.Error.Message), "secret") {
		t.Fatalf("expected safe error envelope, got %+v", body.Error)
	}
}

func TestRouterDoesNotExposeSpikeOutsideDevelopment(t *testing.T) {
	cfg := spikeHTTPConfig("staging")
	router := NewRouter(cfg, slog.Default())
	response := serveSpikeRequest(router, `{"room":"spike-1to1","identity":"browser-a"}`, testSpikeOrigin)
	if response.Code != http.StatusNotFound {
		t.Fatalf("expected 404 outside development, got %d", response.Code)
	}
}

func TestRouterDoesNotExposeDisabledSpikeInStaging(t *testing.T) {
	cfg := spikeHTTPConfig("staging")
	cfg.MediaSpikeEnabled = false
	router := NewRouter(cfg, slog.Default())
	response := serveSpikeRequest(router, `{"room":"spike-1to1","identity":"browser-a"}`, testSpikeOrigin)
	if response.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for disabled staging spike, got %d", response.Code)
	}
}

func TestRouterDoesNotExposeDisabledOrUnmarkedSpike(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*config.Config)
	}{
		{name: "disabled", mutate: func(cfg *config.Config) { cfg.MediaSpikeEnabled = false }},
		{name: "local marker absent", mutate: func(cfg *config.Config) { cfg.MediaSpikeLocalOnly = false }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := spikeHTTPConfig("development")
			tt.mutate(&cfg)
			response := serveSpikeRequest(NewRouter(cfg, slog.Default()), `{"room":"spike-1to1","identity":"browser-a"}`, testSpikeOrigin)
			if response.Code != http.StatusNotFound {
				t.Fatalf("expected 404 for %s spike, got %d", tt.name, response.Code)
			}
		})
	}
}

func TestSpikeTokenHandlerMapsInternalErrorsToGenericResponse(t *testing.T) {
	issuer := &fakeSpikeTokenIssuer{err: errors.New("do-not-leak-internal-secret")}
	response := serveSpikeRequest(SpikeToken(issuer, spikeHTTPConfig("development"), slog.Default()), `{"room":"spike-1to1","identity":"browser-a"}`, testSpikeOrigin)
	if response.Code != http.StatusInternalServerError || strings.Contains(response.Body.String(), "do-not-leak") {
		t.Fatalf("expected generic 500 response, got %d: %s", response.Code, response.Body.String())
	}
}

func spikeHTTPConfig(env string) config.Config {
	return config.Config{
		ServiceName:               "media-service",
		Env:                       env,
		MediaSpikeEnabled:         true,
		MediaSpikeLocalOnly:       true,
		MediaSpikeAllowedOrigins:  testSpikeOrigin,
		MediaSpikeRoom:            "spike-1to1",
		LiveKitURL:                "ws://127.0.0.1:7880",
		LiveKitAPIKey:             "test-key",
		LiveKitAPISecret:          "test-secret-with-sufficient-length",
		MediaSpikeTokenTTLSeconds: 300,
		ReadHeaderTimeoutSeconds:  5,
		ReadTimeoutSeconds:        10,
		WriteTimeoutSeconds:       10,
	}
}

func serveSpikeRequest(handler http.Handler, body, origin string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, RouteSpikeToken, strings.NewReader(body))
	if origin != "" {
		request.Header.Set("Origin", origin)
	}
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}
