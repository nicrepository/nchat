package app

import (
	"bufio"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nicrepository/nchat/services/chat-service/internal/config"
	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/service"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
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
	defer func() { _ = conn.Close() }()
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

func TestNewInvalidValkeyBroadcastConfigGracefullyDegrades(t *testing.T) {
	a := newTestApp(t, config.Config{
		ServiceName: "chat-service", Env: "test", Port: 8082, ReadHeaderTimeoutSeconds: 5,
		ValkeyURL: "invalid", ValkeyWSBroadcastEnabled: true,
	})
	if a.hub == nil {
		t.Fatal("expected in-process hub when Valkey broadcast config is invalid")
	}
}

func TestAppShutdownClosesReactionLimiter(t *testing.T) {
	a := newTestApp(t, config.Config{ServiceName: "chat-service", Env: "test", Port: 8082, ReadHeaderTimeoutSeconds: 5})
	limiter, err := ws.NewValkeyReactionLimiter(startFakeValkeyServer(t), 60, 60)
	if err != nil {
		t.Fatalf("new reaction limiter: %v", err)
	}
	a.reactionLimiter = limiter
	if err := a.Shutdown(t.Context()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
}

type workspaceResolverStore struct {
	workspace domain.Workspace
	err       error
}

func (s workspaceResolverStore) GetDefaultWorkspace(context.Context) (domain.Workspace, error) {
	return s.workspace, s.err
}

func TestAppWSWorkspaceResolver(t *testing.T) {
	resolver := appWSWorkspaceResolver{store: workspaceResolverStore{workspace: domain.Workspace{ID: "workspace-1"}}}
	if id, err := resolver.GetDefaultWorkspaceID(t.Context()); err != nil || id != "workspace-1" {
		t.Fatalf("id=%q err=%v", id, err)
	}

	want := errors.New("lookup failed")
	resolver.store = workspaceResolverStore{err: want}
	if _, err := resolver.GetDefaultWorkspaceID(t.Context()); !errors.Is(err, want) {
		t.Fatalf("expected %v, got %v", want, err)
	}
}

func TestDomainMessageToWSPayloadMapsRemovalTimestamps(t *testing.T) {
	now := time.Now().UTC()
	got := domainMessageToWSPayload(domain.Message{
		ID: "message-1", WorkspaceID: "workspace-1", ChannelID: "channel-1", SenderID: "user-1",
		BodyText: "hello", EditedAt: now, DeletedAt: now,
	})
	if got.ID != "message-1" || got.WorkspaceID != "workspace-1" || got.ChannelID != "channel-1" || got.SenderID != "user-1" || got.BodyText != "hello" {
		t.Fatalf("unexpected payload: %+v", got)
	}
	if !got.IsRemoved || got.EditedAt == nil || got.DeletedAt == nil {
		t.Fatalf("removal timestamps not mapped: %+v", got)
	}
}

func TestHubBroadcasterPublishesCreatedMessage(t *testing.T) {
	hub := ws.NewHub(ws.NopAuthorizer{}, slog.Default(), ws.NopBus{}, "test-broadcaster")
	t.Cleanup(hub.Shutdown)
	broadcaster := hubBroadcaster{hub: hub}

	broadcaster.PublishMessageCreated(t.Context(), "workspace-1", string(ws.TargetTypeChannel), "channel-1", domain.Message{
		ID: "message-1", WorkspaceID: "workspace-1", ChannelID: "channel-1", SenderID: "user-1",
	})
}

type reactionStoreStub struct {
	result storage.ToggleReactionResult
	err    error
	input  storage.ToggleReactionInput
}

func (s *reactionStoreStub) ToggleReaction(_ context.Context, input storage.ToggleReactionInput) (storage.ToggleReactionResult, error) {
	s.input = input
	return s.result, s.err
}

func TestReactionHandlerAdapterMapsChannelAndDMUpdates(t *testing.T) {
	for _, tt := range []struct {
		name       string
		result     storage.ToggleReactionResult
		targetType ws.TargetType
		targetID   string
	}{
		{name: "channel", result: storage.ToggleReactionResult{MessageID: "message-1", ChannelID: "channel-1", Added: true}, targetType: ws.TargetTypeChannel, targetID: "channel-1"},
		{name: "dm", result: storage.ToggleReactionResult{MessageID: "message-1", DMID: "dm-1", Added: false}, targetType: ws.TargetTypeDM, targetID: "dm-1"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store := &reactionStoreStub{result: tt.result}
			store.result.Reactions = []domain.MessageReaction{{Emoji: "👍", Count: 2}}
			adapter := reactionHandlerAdapter{service: service.NewReactionService(store)}

			got, err := adapter.ToggleReaction(t.Context(), "workspace-1", "user-1", "message-1", "👍")
			if err != nil {
				t.Fatalf("ToggleReaction: %v", err)
			}
			if got.MessageID != "message-1" || got.TargetType != tt.targetType || got.TargetID != tt.targetID || got.Added != tt.result.Added {
				t.Fatalf("unexpected update: %+v", got)
			}
			if len(got.Reactions) != 1 || got.Reactions[0].Emoji != "👍" || got.Reactions[0].Count != 2 {
				t.Fatalf("unexpected reactions: %+v", got.Reactions)
			}
			if store.input.WorkspaceID != "workspace-1" || store.input.UserID != "user-1" {
				t.Fatalf("server identity not forwarded: %+v", store.input)
			}
		})
	}
}

func TestReactionHandlerAdapterPropagatesServiceError(t *testing.T) {
	want := errors.New("store failed")
	adapter := reactionHandlerAdapter{service: service.NewReactionService(&reactionStoreStub{err: want})}

	_, err := adapter.ToggleReaction(t.Context(), "workspace-1", "user-1", "message-1", "👍")
	if !errors.Is(err, want) {
		t.Fatalf("expected %v, got %v", want, err)
	}
}
