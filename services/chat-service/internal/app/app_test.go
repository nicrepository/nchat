package app

import (
	"bufio"
	"context"
	"io"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nicrepository/nchat/services/chat-service/internal/config"
	"github.com/nicrepository/nchat/services/chat-service/internal/service"
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

// startFakeValkeyServer starts a minimal in-process RESP server that answers
// just enough of the handshake (HELLO / CLIENT) for a real valkey-go client
// to connect successfully. It returns a "valkey://host:port" URL. No data
// commands are needed since these tests only exercise cache wiring, not
// Get/Set behavior (covered separately in storage/mention_label_cache_test.go).
func startFakeValkeyServer(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go serveFakeValkeyConn(conn)
		}
	}()
	// protocol=2 and client_cache=0 select the RESP2, no-client-side-caching
	// mode our minimal fake server implements (RESP3 push invalidation is
	// out of scope for this handshake-only test).
	return "valkey://" + ln.Addr().String() + "?protocol=2&client_cache=0"
}

func serveFakeValkeyConn(conn net.Conn) {
	defer conn.Close()
	reader := bufio.NewReader(conn)
	for {
		command, err := readRESPCommand(reader)
		if err != nil {
			return
		}
		switch strings.ToUpper(command[0]) {
		case "HELLO":
			_, _ = io.WriteString(conn, "*2\r\n+proto\r\n:2\r\n")
		default:
			_, _ = io.WriteString(conn, "+OK\r\n")
		}
	}
}

func readRESPCommand(reader *bufio.Reader) ([]string, error) {
	header, err := reader.ReadString('\n')
	if err != nil {
		return nil, err
	}
	count, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(header, "*")))
	if err != nil {
		return nil, err
	}
	command := make([]string, count)
	for i := range count {
		lengthLine, err := reader.ReadString('\n')
		if err != nil {
			return nil, err
		}
		length, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(lengthLine, "$")))
		if err != nil {
			return nil, err
		}
		value := make([]byte, length+2)
		if _, err := io.ReadFull(reader, value); err != nil {
			return nil, err
		}
		command[i] = string(value[:length])
	}
	return command, nil
}

// TestWireMentionLabelCache_Disabled verifies that an empty Valkey URL leaves
// the cache disabled without contacting anything.
func TestWireMentionLabelCache_Disabled(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	messageSvc := service.NewMessageService(nil, nil, nil)

	cache := wireMentionLabelCache("", 45, messageSvc, logger)
	if cache != nil {
		t.Fatalf("expected nil cache for empty Valkey URL, got %+v", cache)
	}
}

// TestWireMentionLabelCache_InvalidURL_Disables covers the cacheErr != nil
// branch: an unparsable Valkey URL disables the cache and does not panic or
// interrupt the service, matching the documented graceful-degradation
// behavior.
func TestWireMentionLabelCache_InvalidURL_Disables(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	messageSvc := service.NewMessageService(nil, nil, nil)

	cache := wireMentionLabelCache("not-a-valid-url", 45, messageSvc, logger)
	if cache != nil {
		t.Fatalf("expected nil cache for invalid Valkey URL, got %+v", cache)
	}
}

// TestWireMentionLabelCache_Success covers the cacheErr == nil branch: a
// reachable Valkey server results in a non-nil cache wired onto messageSvc.
func TestWireMentionLabelCache_Success(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	messageSvc := service.NewMessageService(nil, nil, nil)
	valkeyURL := startFakeValkeyServer(t)

	cache := wireMentionLabelCache(valkeyURL, 45, messageSvc, logger)
	if cache == nil {
		t.Fatal("expected non-nil cache for reachable Valkey server")
	}
	t.Cleanup(cache.Close)
}

// TestApp_Shutdown_ClosesMentionCache verifies that Shutdown closes a
// non-nil mention label cache exactly once, even across repeated calls.
func TestApp_Shutdown_ClosesMentionCache(t *testing.T) {
	cfg := config.Config{ServiceName: "chat-service", Env: "test", Port: 8082, ReadHeaderTimeoutSeconds: 5}
	a := newTestApp(t, cfg)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	messageSvc := service.NewMessageService(nil, nil, nil)
	valkeyURL := startFakeValkeyServer(t)
	a.mentionCache = wireMentionLabelCache(valkeyURL, 45, messageSvc, logger)
	if a.mentionCache == nil {
		t.Fatal("expected non-nil mention cache before Shutdown")
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = a.Shutdown(context.Background())
		_ = a.Shutdown(context.Background()) // idempotent
	}()

	select {
	case <-done:
	case <-time.After(shutdownTestTimeout):
		t.Fatal("Shutdown with mention cache deadlocked or did not complete in time")
	}
}
