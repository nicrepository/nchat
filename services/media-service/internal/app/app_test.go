package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/livekit/protocol/auth"
	"github.com/nicrepository/nchat/services/media-service/internal/config"
	httpapi "github.com/nicrepository/nchat/services/media-service/internal/http"
	"github.com/nicrepository/nchat/services/media-service/internal/storage"
	"github.com/pashagolub/pgxmock/v2"
)

const (
	appTestUserID    = "11111111-1111-4111-8111-111111111111"
	appTestSessionID = "22222222-2222-4222-8222-222222222222"
	appTestResource  = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
)

func repeatedAppTestValue(value byte, size int) string {
	result := make([]byte, size)
	for index := range result {
		result[index] = value
	}
	return string(result)
}

func TestNewCreatesDisabledAppWithoutDatabase(t *testing.T) {
	cfg := config.Config{ServiceName: "media-service", Env: "test", Port: 8087, ReadHeaderTimeoutSeconds: 5}
	application, err := newApp(cfg, appDependencies{
		openDB: func(context.Context, string, int) (storage.Pool, error) {
			t.Fatal("disabled integration must not open the database")
			return nil, nil
		},
		tracingShutdown: noTracing,
	})
	if err != nil {
		t.Fatalf("new app: %v", err)
	}
	if application == nil || application.Logger == nil || application.Handler == nil {
		t.Fatalf("expected initialized app, got %+v", application)
	}
	if application.Config != cfg {
		t.Fatalf("expected config %+v, got %+v", cfg, application.Config)
	}
	if err := application.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
}

func TestNewEnabledAppIssuesOfficialLiveKitToken(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("new pgx mock: %v", err)
	}
	cfg := validAppConfig()
	sessionExpiry := time.Now().UTC().Add(10 * time.Minute)
	mock.ExpectQuery(`(?s)WITH active_session AS.*authorized_resource AS.*chat\.calls`).
		WithArgs(appTestSessionID, appTestUserID, appTestResource, "").
		WillReturnRows(pgxmock.NewRows([]string{"session_expires_at", "resource_id", "display_name"}).
			AddRow(sessionExpiry, appTestResource, "App Test User"))

	application, err := newApp(cfg, appDependencies{
		openDB:          func(context.Context, string, int) (storage.Pool, error) { return mock, nil },
		tracingShutdown: noTracing,
	})
	if err != nil {
		t.Fatalf("new app: %v", err)
	}
	t.Cleanup(func() { _ = application.Shutdown(context.Background()) })

	request := httptest.NewRequest(http.MethodPost, httpapi.RouteLiveKitToken,
		strings.NewReader(`{"call_id":"`+appTestResource+`"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+signedAppAccessToken(t))
	response := httptest.NewRecorder()
	application.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", response.Code, response.Body.String())
	}
	var body struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	verifier, err := auth.ParseAPIToken(body.Data.Token)
	if err != nil {
		t.Fatalf("parse LiveKit token: %v", err)
	}
	_, grants, err := verifier.Verify(repeatedAppTestValue('l', 32))
	if err != nil {
		t.Fatalf("verify LiveKit token: %v", err)
	}
	if grants.Identity != "production:"+appTestUserID || grants.Video == nil ||
		!grants.Video.RoomJoin || grants.Video.Room != "production:call:"+appTestResource {
		t.Fatalf("unexpected LiveKit grants: %+v", grants)
	}
	if grants.Name != "App Test User" {
		t.Fatalf("expected server-resolved display name in token, got %q", grants.Name)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("storage expectations: %v", err)
	}
}

func TestNewEnabledAppFailsClosedWithoutLeakingDatabaseDetails(t *testing.T) {
	cfg := validAppConfig()
	application, err := newApp(cfg, appDependencies{
		openDB: func(context.Context, string, int) (storage.Pool, error) {
			return nil, errors.New("postgres://user:secret@database/nchat")
		},
		tracingShutdown: noTracing,
	})
	if err == nil || application != nil {
		t.Fatalf("expected startup failure, app=%v err=%v", application, err)
	}
	if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "postgres://") {
		t.Fatalf("database details leaked: %v", err)
	}
}

func TestPublicNewCreatesDisabledApp(t *testing.T) {
	application, err := New(config.Config{ServiceName: "media-service", Env: "test"})
	if err != nil {
		t.Fatalf("public new: %v", err)
	}
	if err := application.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
}

func TestNewEnabledAppFailsClosedWithoutDatabaseFactory(t *testing.T) {
	application, err := newApp(validAppConfig(), appDependencies{tracingShutdown: noTracing})
	if err == nil || application != nil {
		t.Fatalf("expected missing database factory to fail closed, app=%v err=%v", application, err)
	}
}

func TestAppShutdownIsNilSafeIdempotentAndPropagatesTracingError(t *testing.T) {
	var application *App
	if err := application.Shutdown(context.Background()); err != nil {
		t.Fatalf("nil shutdown: %v", err)
	}
	want := errors.New("tracing shutdown failed")
	application = &App{TracingShutdown: func(context.Context) error { return want }}
	if err := application.Shutdown(context.Background()); !errors.Is(err, want) {
		t.Fatalf("expected tracing error, got %v", err)
	}
	if err := application.Shutdown(context.Background()); !errors.Is(err, want) {
		t.Fatalf("expected idempotent tracing error, got %v", err)
	}
}

func validAppConfig() config.Config {
	return config.Config{
		ServiceName: "media-service", Env: "production", Port: 8087,
		ReadHeaderTimeoutSeconds: 5, ReadTimeoutSeconds: 10, WriteTimeoutSeconds: 10,
		DatabaseURL: "postgres://database/nchat", DBConnectTimeoutSeconds: 1,
		AuthJWTHMACSecret: repeatedAppTestValue('j', 32), AuthJWTIssuer: "nchat-auth", AuthJWTAudience: "nchat-api",
		LiveKitEnabled: true, LiveKitAPIURL: "http://livekit:7880",
		LiveKitAPIKey: repeatedAppTestValue('k', 16), LiveKitAPISecret: repeatedAppTestValue('l', 32), LiveKitTokenTTLSeconds: 300,
	}
}

func signedAppAccessToken(t *testing.T) string {
	t.Helper()
	now := time.Now().UTC().Add(-time.Second)
	claims := jwt.MapClaims{
		"sub": appTestUserID, "sid": appTestSessionID, "jti": "app-test",
		"iss": "nchat-auth", "aud": "nchat-api",
		"iat": now.Unix(), "nbf": now.Unix(), "exp": now.Add(10 * time.Minute).Unix(),
	}
	raw, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(repeatedAppTestValue('j', 32)))
	if err != nil {
		t.Fatalf("sign access token: %v", err)
	}
	return raw
}

func noTracing(context.Context) error { return nil }
