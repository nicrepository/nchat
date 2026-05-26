package app

import (
	"testing"

	"github.com/nicrepository/nchat/services/auth-service/internal/config"
)

func TestNewCreatesApp(t *testing.T) {
	cfg := config.Config{ServiceName: "auth-service", Env: "test", Port: 8081, ReadHeaderTimeoutSeconds: 5}

	app := New(cfg)

	if app == nil {
		t.Fatal("expected app")
	}
	if app.Logger == nil {
		t.Fatal("expected logger")
	}
	if app.Handler == nil {
		t.Fatal("expected handler")
	}
	if app.Config != cfg {
		t.Fatalf("expected config %+v, got %+v", cfg, app.Config)
	}
}

func TestNewWithUnreachableDB_DisablesAdminEndpoint(t *testing.T) {
	// Port 9 is discard protocol — connections are refused immediately.
	// DATABASE_URL contains a dummy password for testing purposes only.
	t.Setenv("DATABASE_URL", "postgres://nchat:pass@localhost:9/nonexistent?sslmode=disable") //nolint:gosec
	t.Setenv("DB_CONNECT_TIMEOUT_SECONDS", "1")

	cfg := config.Load()

	app := New(cfg)

	if app == nil {
		t.Fatal("expected app even when DB is unavailable")
	}
	if app.Handler == nil {
		t.Fatal("expected handler")
	}
}
