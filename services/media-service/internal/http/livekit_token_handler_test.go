package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nicrepository/nchat/libs/go/platform/httputil"
	"github.com/nicrepository/nchat/services/media-service/internal/domain"
	"github.com/nicrepository/nchat/services/media-service/internal/service"
	"go.opentelemetry.io/otel/trace"
)

const (
	handlerTestUserID    = "11111111-1111-4111-8111-111111111111"
	handlerTestSessionID = "22222222-2222-4222-8222-222222222222"
	handlerTestResource  = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
)

type fakeTokenIssuer struct {
	result service.IssuedToken
	err    error
	input  service.IssueTokenInput
	calls  int
}

func (f *fakeTokenIssuer) Issue(ctx context.Context, input service.IssueTokenInput) (service.IssuedToken, error) {
	f.calls++
	f.input = input
	if err := ctx.Err(); err != nil {
		return service.IssuedToken{}, err
	}
	return f.result, f.err
}

func TestLiveKitTokenHandlerIssuesChannelAndDMTokenFromPrincipal(t *testing.T) {
	for _, kind := range []domain.ResourceKind{domain.ResourceKindChannel, domain.ResourceKindDM} {
		t.Run(string(kind), func(t *testing.T) {
			expiresAt := time.Date(2026, 7, 21, 15, 4, 5, 0, time.UTC)
			issuer := &fakeTokenIssuer{result: service.IssuedToken{
				Token: "signed-livekit-token", ExpiresAt: expiresAt,
			}}
			response := serveLiveKitHandler(
				LiveKitToken(issuer, slog.Default()),
				`{"kind":"`+string(kind)+`","id":"`+handlerTestResource+`"}`,
				authenticatedRequestContext(),
			)

			if response.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d: %s", response.Code, response.Body.String())
			}
			var body struct {
				Data struct {
					Token     string `json:"token"`
					ExpiresAt string `json:"expiresAt"`
				} `json:"data"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if body.Data.Token != issuer.result.Token || body.Data.ExpiresAt != expiresAt.Format(time.RFC3339) {
				t.Fatalf("unexpected response: %+v", body.Data)
			}
			if issuer.calls != 1 {
				t.Fatalf("expected one issuer call, got %d", issuer.calls)
			}
			if issuer.input.Kind != kind || issuer.input.ResourceID != handlerTestResource ||
				issuer.input.UserID != handlerTestUserID || issuer.input.SessionID != handlerTestSessionID {
				t.Fatalf("issuer received untrusted or incorrect input: %+v", issuer.input)
			}
			if !issuer.input.AccessExpiresAt.Equal(authenticatedRequestContext().Value(principalContextKey{}).(Principal).AccessExpiresAt) {
				t.Fatalf("unexpected access expiry: %s", issuer.input.AccessExpiresAt)
			}
		})
	}
}

func TestLiveKitTokenHandlerRejectsInvalidBodies(t *testing.T) {
	tests := []struct {
		name string
		body string
		want int
	}{
		{name: "empty", body: "", want: http.StatusBadRequest},
		{name: "malformed", body: "{", want: http.StatusBadRequest},
		{name: "invalid kind", body: `{"kind":"group","id":"` + handlerTestResource + `"}`, want: http.StatusBadRequest},
		{name: "invalid uuid", body: `{"kind":"channel","id":"not-a-uuid"}`, want: http.StatusBadRequest},
		{name: "trailing json", body: `{"kind":"channel","id":"` + handlerTestResource + `"}{}`, want: http.StatusBadRequest},
		{name: "identity", body: `{"kind":"channel","id":"` + handlerTestResource + `","identity":"other"}`, want: http.StatusBadRequest},
		{name: "room", body: `{"kind":"channel","id":"` + handlerTestResource + `","room":"admin"}`, want: http.StatusBadRequest},
		{name: "grants", body: `{"kind":"channel","id":"` + handlerTestResource + `","grants":{}}`, want: http.StatusBadRequest},
		{name: "ttl", body: `{"kind":"channel","id":"` + handlerTestResource + `","ttl":3600}`, want: http.StatusBadRequest},
		{name: "oversized", body: `{"kind":"channel","id":"` + strings.Repeat("a", liveKitTokenBodyLimit) + `"}`, want: http.StatusRequestEntityTooLarge},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			issuer := &fakeTokenIssuer{}
			response := serveLiveKitHandler(LiveKitToken(issuer, slog.Default()), tt.body, authenticatedRequestContext())
			if response.Code != tt.want {
				t.Fatalf("expected %d, got %d: %s", tt.want, response.Code, response.Body.String())
			}
			if issuer.calls != 0 {
				t.Fatalf("issuer called for invalid input: %d", issuer.calls)
			}
		})
	}
}

func TestLiveKitTokenHandlerRequiresPrincipalAndJSON(t *testing.T) {
	issuer := &fakeTokenIssuer{}
	response := serveLiveKitHandler(LiveKitToken(issuer, slog.Default()),
		`{"kind":"channel","id":"`+handlerTestResource+`"}`, context.Background())
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", response.Code)
	}

	request := httptest.NewRequest(http.MethodPost, RouteLiveKitToken,
		strings.NewReader(`{"kind":"channel","id":"`+handlerTestResource+`"}`))
	request = request.WithContext(authenticatedRequestContext())
	response = httptest.NewRecorder()
	LiveKitToken(issuer, slog.Default()).ServeHTTP(response, request)
	if response.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("expected 415, got %d", response.Code)
	}
	if issuer.calls != 0 {
		t.Fatalf("issuer called without authenticated JSON request: %d", issuer.calls)
	}
}

func TestLiveKitTokenHandlerMapsServiceErrors(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
		code string
	}{
		{name: "invalid", err: domain.ErrInvalidInput, want: http.StatusBadRequest, code: httputil.ErrCodeBadRequest},
		{name: "session", err: domain.ErrUnauthorized, want: http.StatusUnauthorized, code: httputil.ErrCodeUnauthorized},
		{name: "resource", err: domain.ErrNotFound, want: http.StatusNotFound, code: httputil.ErrCodeNotFound},
		{name: "disabled", err: domain.ErrUnavailable, want: http.StatusServiceUnavailable, code: "service_unavailable"},
		{name: "database", err: errors.New("database secret failure"), want: http.StatusInternalServerError, code: httputil.ErrCodeInternal},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			issuer := &fakeTokenIssuer{err: tt.err}
			response := serveLiveKitHandler(LiveKitToken(issuer, slog.Default()),
				`{"kind":"dm","id":"`+handlerTestResource+`"}`, authenticatedRequestContext())
			if response.Code != tt.want {
				t.Fatalf("expected %d, got %d: %s", tt.want, response.Code, response.Body.String())
			}
			var body httputil.Envelope
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode error envelope: %v", err)
			}
			if body.Error == nil || body.Error.Code != tt.code {
				t.Fatalf("unexpected error envelope: %+v", body)
			}
			if strings.Contains(response.Body.String(), "database secret") {
				t.Fatalf("internal error leaked: %s", response.Body.String())
			}
		})
	}
}

func TestLiveKitTokenHandlerDoesNotLogTokensSecretsOrIdentifiers(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	issuer := &fakeTokenIssuer{result: service.IssuedToken{
		Token:     "sensitive-livekit-token",
		ExpiresAt: time.Now().UTC().Add(time.Minute),
	}}
	handler := httputil.RequestID(LiveKitToken(issuer, logger))
	request := httptest.NewRequest(http.MethodPost, RouteLiveKitToken,
		strings.NewReader(`{"kind":"channel","id":"`+handlerTestResource+`"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Request-ID", "req-success")
	request = request.WithContext(authenticatedRequestContext())
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", response.Code)
	}
	for _, sensitive := range []string{
		issuer.result.Token, handlerTestResource, handlerTestUserID, handlerTestSessionID, "api-secret",
	} {
		if strings.Contains(logs.String(), sensitive) {
			t.Fatalf("logs contain sensitive value %q: %s", sensitive, logs.String())
		}
	}
	for _, field := range []string{"resource_kind", "result", "status", "duration_ms", "request_id", "req-success", "\"level\":\"INFO\""} {
		if !strings.Contains(logs.String(), field) {
			t.Fatalf("expected bounded log field %q: %s", field, logs.String())
		}
	}
}

func TestLiveKitTokenHandlerClassifiesLogsAndCorrelatesInternalFailures(t *testing.T) {
	traceID, err := trace.TraceIDFromHex("0102030405060708090a0b0c0d0e0f10")
	if err != nil {
		t.Fatalf("trace id: %v", err)
	}
	spanID, err := trace.SpanIDFromHex("0102030405060708")
	if err != nil {
		t.Fatalf("span id: %v", err)
	}
	spanContext := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: traceID, SpanID: spanID, TraceFlags: trace.FlagsSampled,
	})

	tests := []struct {
		name       string
		issuerErr  error
		wantLevel  string
		wantCause  string
		wantStatus int
	}{
		{name: "client error", issuerErr: domain.ErrInvalidInput, wantLevel: "DEBUG", wantStatus: http.StatusBadRequest},
		{name: "operational unavailable", issuerErr: errors.Join(domain.ErrUnavailable, errors.New("postgres://user:secret@internal-db/nchat")), wantLevel: "WARN", wantCause: "service_unavailable", wantStatus: http.StatusServiceUnavailable},
		{name: "storage failure", issuerErr: errors.Join(service.ErrAuthorizationDependency, errors.New("storage-secret")), wantLevel: "ERROR", wantCause: "storage", wantStatus: http.StatusInternalServerError},
		{name: "signer failure", issuerErr: errors.Join(service.ErrTokenSigning, errors.New("signer-secret")), wantLevel: "ERROR", wantCause: "signer", wantStatus: http.StatusInternalServerError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var logs bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
			handler := httputil.RequestID(LiveKitToken(&fakeTokenIssuer{err: tt.issuerErr}, logger))
			request := httptest.NewRequest(http.MethodPost, RouteLiveKitToken,
				strings.NewReader(`{"kind":"dm","id":"`+handlerTestResource+`"}`))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("X-Request-ID", "req-correlated")
			ctx := trace.ContextWithSpanContext(authenticatedRequestContext(), spanContext)
			request = request.WithContext(ctx)
			response := httptest.NewRecorder()

			handler.ServeHTTP(response, request)

			if response.Code != tt.wantStatus {
				t.Fatalf("expected %d, got %d: %s", tt.wantStatus, response.Code, response.Body.String())
			}
			for _, expected := range []string{
				"\"level\":\"" + tt.wantLevel + "\"",
				"\"request_id\":\"req-correlated\"",
				"\"trace_id\":\"" + traceID.String() + "\"",
			} {
				if !strings.Contains(logs.String(), expected) {
					t.Fatalf("expected log field %q: %s", expected, logs.String())
				}
			}
			if tt.wantCause != "" && !strings.Contains(logs.String(), "\"cause\":\""+tt.wantCause+"\"") {
				t.Fatalf("expected categorized cause %q: %s", tt.wantCause, logs.String())
			}
			for _, forbidden := range []string{"storage-secret", "signer-secret", "postgres://user:secret@internal-db/nchat", "Authorization", "livekit-token"} {
				if strings.Contains(logs.String(), forbidden) || strings.Contains(response.Body.String(), forbidden) {
					t.Fatalf("sensitive internal value %q leaked: log=%s body=%s", forbidden, logs.String(), response.Body.String())
				}
			}
			if tt.wantStatus >= 500 && strings.Contains(logs.String(), "\"level\":\"INFO\"") {
				t.Fatalf("server error was logged as info: %s", logs.String())
			}
			if tt.wantStatus < 500 && strings.Contains(logs.String(), "\"level\":\"ERROR\"") {
				t.Fatalf("client error was logged as internal error: %s", logs.String())
			}
		})
	}
}

func TestLiveKitUnavailableLogsBoundedCauseWithCorrelation(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	traceID, err := trace.TraceIDFromHex("11111111111111111111111111111111")
	if err != nil {
		t.Fatalf("trace id: %v", err)
	}
	spanID, err := trace.SpanIDFromHex("2222222222222222")
	if err != nil {
		t.Fatalf("span id: %v", err)
	}
	ctx := trace.ContextWithSpanContext(context.Background(), trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: traceID, SpanID: spanID, TraceFlags: trace.FlagsSampled,
	}))
	handler := httputil.RequestID(LiveKitUnavailable(logger))
	request := httptest.NewRequest(http.MethodPost, RouteLiveKitToken, nil).WithContext(ctx)
	request.Header.Set("X-Request-ID", "req-disabled")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d: %s", response.Code, response.Body.String())
	}
	for _, expected := range []string{
		"\"level\":\"WARN\"",
		"\"cause\":\"integration_disabled\"",
		"\"request_id\":\"req-disabled\"",
		"\"trace_id\":\"" + traceID.String() + "\"",
	} {
		if !strings.Contains(logs.String(), expected) {
			t.Fatalf("expected log field %q: %s", expected, logs.String())
		}
	}
	for _, forbidden := range []string{"Authorization", "secret", "postgres://"} {
		if strings.Contains(logs.String(), forbidden) {
			t.Fatalf("log leaked forbidden value %q: %s", forbidden, logs.String())
		}
	}
}

func TestLiveKitTokenHandlerLogsUnavailableDependencyCause(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	handler := httputil.RequestID(LiveKitToken(nil, logger))
	request := httptest.NewRequest(http.MethodPost, RouteLiveKitToken, nil)
	request.Header.Set("X-Request-ID", "req-dependency")
	request = request.WithContext(authenticatedRequestContext())
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d: %s", response.Code, response.Body.String())
	}
	for _, expected := range []string{
		"\"level\":\"WARN\"",
		"\"cause\":\"dependencies_unavailable\"",
		"\"request_id\":\"req-dependency\"",
	} {
		if !strings.Contains(logs.String(), expected) {
			t.Fatalf("expected log field %q: %s", expected, logs.String())
		}
	}
	for _, forbidden := range []string{"database-secret", "sensitive-livekit-token", "api-secret", "postgres://"} {
		if strings.Contains(logs.String(), forbidden) || strings.Contains(response.Body.String(), forbidden) {
			t.Fatalf("unavailable dependency leaked %q: log=%s body=%s", forbidden, logs.String(), response.Body.String())
		}
	}
}

func TestLiveKitTokenHandlerPropagatesCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(authenticatedRequestContext())
	cancel()
	issuer := &fakeTokenIssuer{}
	response := serveLiveKitHandler(LiveKitToken(issuer, slog.Default()),
		`{"kind":"channel","id":"`+handlerTestResource+`"}`, ctx)
	if issuer.calls != 1 {
		t.Fatalf("expected canceled context to reach issuer, got %d calls", issuer.calls)
	}
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("expected generic 500, got %d", response.Code)
	}
}

func authenticatedRequestContext() context.Context {
	return context.WithValue(context.Background(), principalContextKey{}, Principal{
		UserID: handlerTestUserID, SessionID: handlerTestSessionID,
		AccessExpiresAt: time.Date(2026, 7, 21, 15, 10, 0, 0, time.UTC),
	})
}

func serveLiveKitHandler(handler http.Handler, body string, ctx context.Context) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, RouteLiveKitToken, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request = request.WithContext(ctx)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}
