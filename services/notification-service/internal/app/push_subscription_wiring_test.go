package app

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nicrepository/nchat/services/notification-service/internal/config"
	httpapi "github.com/nicrepository/nchat/services/notification-service/internal/http"
	"github.com/nicrepository/nchat/services/notification-service/internal/storage"
)

// Issue #745: the push subscription API is wired only when it can be served
// safely.
//
// Both dependencies are hard requirements. Without a database there is no
// session to check a caller against; without a usable signing secret no token
// can be verified. A build that mounted the routes anyway would be authorising
// writes it could not attribute to anybody.
//
// When either is missing the routes are not registered, so the catch-all answers
// and the status is 404 — this build does not serve them. That is the contract
// these tests pin down; the 503 an unconfigured Authenticate returns belongs to a
// partial wiring the production path cannot produce, and is covered in the http
// package instead.

func pushConfig(databaseURL string) config.Config {
	return config.Config{
		ServiceName:              "notification-service",
		Env:                      "test",
		Port:                     8084,
		ReadHeaderTimeoutSeconds: 5,
		DatabaseURL:              databaseURL,
		DBConnectTimeoutSeconds:  1,
		AuthJWTHMACSecret:        strings.Repeat("notification-service-test-key-", 2),
		AuthJWTIssuer:            "nchat-auth",
		AuthJWTAudience:          "nchat-api",
	}
}

// restorePushFactories keeps the package-level wiring hooks from leaking between
// tests.
func restorePushFactories(t *testing.T) {
	t.Helper()
	origOpenDB := openDB
	origNewTokenValidator := newTokenValidator
	t.Cleanup(func() {
		openDB = origOpenDB
		newTokenValidator = origNewTokenValidator
	})
}

// captureLogger returns a logger writing into a buffer, so what the wiring says
// about a refused configuration can be asserted rather than assumed.
func captureLogger() (*slog.Logger, *bytes.Buffer) {
	buffer := &bytes.Buffer{}
	return slog.New(slog.NewJSONHandler(buffer, nil)), buffer
}

// statusFor drives the real router the App built.
func statusFor(app *App, target string) int {
	request := httptest.NewRequest(http.MethodGet, target, nil)
	request.Header.Set("Authorization", "Bearer token")
	response := httptest.NewRecorder()
	app.Handler.ServeHTTP(response, request)
	return response.Code
}

func TestPushSubscriptionRoutesAreMountedWithADatabaseAndASecret(t *testing.T) {
	restorePushFactories(t)
	openDB = func(context.Context, string, int) (storage.Pool, error) { return fakePool{}, nil }

	app := New(pushConfig("postgres://user@127.0.0.1:1/nchat?sslmode=disable"))
	// Mounted and authenticating: an unverifiable token is refused, which is
	// only reachable once the middleware is in the chain at all.
	if status := statusFor(app, httpapi.RoutePushSubscriptions); status != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", status)
	}
}

func TestPushSubscriptionRoutesAreAbsentWithoutADatabase(t *testing.T) {
	restorePushFactories(t)

	app := New(pushConfig(""))
	if status := statusFor(app, httpapi.RoutePushSubscriptions); status != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", status)
	}
}

func TestPushSubscriptionRoutesAreAbsentWithoutAUsableSecret(t *testing.T) {
	restorePushFactories(t)
	openDB = func(context.Context, string, int) (storage.Pool, error) { return fakePool{}, nil }

	cfg := pushConfig("postgres://user@127.0.0.1:1/nchat?sslmode=disable")
	cfg.AuthJWTHMACSecret = "too-short"

	app := New(cfg)
	if status := statusFor(app, httpapi.RoutePushSubscriptions); status != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", status)
	}
}

// The reason logged for a refused validator is the category and never the
// configuration: naming the field that was short or missing would put a fact
// about the signing secret in a log line.
func TestPushSubscriptionWiringLogsNoSecretDetail(t *testing.T) {
	restorePushFactories(t)
	openDB = func(context.Context, string, int) (storage.Pool, error) { return fakePool{}, nil }
	newTokenValidator = func(config.Config) (*httpapi.TokenValidator, error) {
		return nil, errors.New("jwt hmac secret must be at least 32 bytes")
	}

	cfg := pushConfig("postgres://user@127.0.0.1:1/nchat?sslmode=disable")
	logger, buffer := captureLogger()

	if options := pushSubscriptionOptions(cfg, fakePool{}, logger); options != nil {
		t.Fatalf("options = %v, want none", options)
	}
	logged := buffer.String()
	if !strings.Contains(logged, "access_token_validation_unavailable") {
		t.Fatalf("the refusal was not logged: %s", logged)
	}
	if strings.Contains(logged, cfg.AuthJWTHMACSecret) || strings.Contains(logged, "32 bytes") {
		t.Fatalf("a secret detail reached the log: %s", logged)
	}
}
