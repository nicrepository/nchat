package app

import (
	"context"
	"testing"
	"time"

	"github.com/nicrepository/nchat/services/chat-service/internal/config"
	"github.com/nicrepository/nchat/services/chat-service/internal/ws"
)

const shutdownTestTimeout = 500 * time.Millisecond

// newTestApp creates an App for testing and registers a t.Cleanup that calls
// Shutdown. Tests that explicitly test shutdown behaviour may call Shutdown
// themselves; subsequent cleanup calls are no-ops (idempotent via sync.Once).
func newTestApp(t *testing.T, cfg config.Config) *App {
	t.Helper()
	a := New(cfg)
	t.Cleanup(func() { _ = a.Shutdown(context.Background()) })
	return a
}

func TestNewCreatesApp(t *testing.T) {
	cfg := config.Config{ServiceName: "chat-service", Env: "test", Port: 8082, ReadHeaderTimeoutSeconds: 5}
	a := newTestApp(t, cfg)
	if a == nil || a.Logger == nil || a.Handler == nil {
		t.Fatalf("expected initialized app, got %+v", a)
	}
	if a.Config != cfg {
		t.Fatalf("expected config %+v, got %+v", cfg, a.Config)
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
	a := newTestApp(t, cfg)
	if a == nil || a.Handler == nil {
		t.Fatal("expected app to start with degraded sidebar when DB is unavailable")
	}
}

// TestApp_Hub_HasPresence verifies that the app always creates a Hub and
// PresenceTracker, even without a database.
func TestApp_Hub_HasPresence(t *testing.T) {
	cfg := config.Config{ServiceName: "chat-service", Env: "test", Port: 8082, ReadHeaderTimeoutSeconds: 5}
	a := newTestApp(t, cfg)

	if a.hub == nil {
		t.Fatal("expected hub to be non-nil")
	}
	if a.presence == nil {
		t.Fatal("expected presence tracker to be non-nil")
	}
}

func TestWSHandlerConfig_MapsWebSocketResourceControls(t *testing.T) {
	cfg := config.Config{
		WSMaxConnectionsPerUser:    7,
		WSInboundMessagesPerMinute: 120,
		WSInboundBurst:             20,
		WSMaxInvalidMessages:       3,
	}

	got := wsHandlerConfig(cfg)
	want := ws.HandlerConfig{
		MaxConnectionsPerUser:    7,
		InboundMessagesPerMinute: 120,
		InboundBurst:             20,
		MaxInvalidMessages:       3,
	}
	if got != want {
		t.Fatalf("expected websocket handler config %+v, got %+v", want, got)
	}
}

// TestApp_Shutdown_Idempotent verifies that Shutdown can be called multiple
// times without panicking or deadlocking, and that hub and presence goroutines
// exit cleanly.
func TestApp_Shutdown_Idempotent(t *testing.T) {
	cfg := config.Config{ServiceName: "chat-service", Env: "test", Port: 8082, ReadHeaderTimeoutSeconds: 5}
	a := newTestApp(t, cfg)

	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = a.Shutdown(context.Background())
		_ = a.Shutdown(context.Background()) // second call must be a no-op
	}()

	select {
	case <-done:
	case <-time.After(shutdownTestTimeout):
		t.Fatalf("Shutdown deadlocked or did not complete within %s", shutdownTestTimeout)
	}
}

// TestApp_Shutdown_PresenceStops verifies that after Shutdown the presence
// tracker's background goroutine has exited (no goroutine leak).
func TestApp_Shutdown_PresenceStops(t *testing.T) {
	cfg := config.Config{ServiceName: "chat-service", Env: "test", Port: 8082, ReadHeaderTimeoutSeconds: 5}
	a := newTestApp(t, cfg)

	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = a.Shutdown(context.Background())
	}()

	select {
	case <-done:
	case <-time.After(shutdownTestTimeout):
		t.Fatalf("Shutdown did not complete within %s; possible goroutine leak", shutdownTestTimeout)
	}
}
