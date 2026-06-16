package app

import (
	"testing"

	"github.com/nicrepository/nchat/services/chat-service/internal/config"
)

func TestNewCreatesApp(t *testing.T) {
	cfg := config.Config{ServiceName: "chat-service", Env: "test", Port: 8082, ReadHeaderTimeoutSeconds: 5}
	app := New(cfg)
	if app == nil || app.Logger == nil || app.Handler == nil {
		t.Fatalf("expected initialized app, got %+v", app)
	}
	if app.Config != cfg {
		t.Fatalf("expected config %+v, got %+v", cfg, app.Config)
	}
}

// TestNew_UnavailableDB_GracefullyDegrades verifies that when DATABASE_URL is set
// but the DB is unavailable, the app still starts (sidebar disabled, no panic).
// This covers the dbErr != nil branch in app.New.
func TestNew_UnavailableDB_GracefullyDegrades(t *testing.T) {
	cfg := config.Config{
		ServiceName:              "chat-service",
		Env:                      "test",
		Port:                     8082,
		ReadHeaderTimeoutSeconds: 5,
		// Use an unreachable address with a fast 1s timeout.
		DatabaseURL:             "postgresql://localhost:19999/nonexistent_test_db",
		DBConnectTimeoutSeconds: 1,
	}
	app := New(cfg)
	if app == nil || app.Handler == nil {
		t.Fatal("expected app to start with degraded sidebar when DB is unavailable")
	}
}
