package app

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
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
	a, err := New(cfg)
	if err != nil {
		t.Fatalf("unexpected bootstrap error: %v", err)
	}
	t.Cleanup(func() { _ = a.Shutdown(context.Background()) })
	return a
}

// stubPool is a non-nil storage.Pool whose methods are never called during
// bootstrap wiring (constructors only store the pool).
type stubPool struct{ storage.Pool }

// stubOpenDB swaps the bootstrap DB opener for the duration of the test.
// Tests in this package must not use t.Parallel while a stub is installed.
func stubOpenDB(t *testing.T, fn func(context.Context, string, int, *slog.Logger) (storage.Pool, error)) {
	t.Helper()
	previous := openDBWithRetry
	openDBWithRetry = fn
	t.Cleanup(func() { openDBWithRetry = previous })
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

// TestNew_UnavailableDB_FailsFast verifies that when DATABASE_URL is set but
// the DB stays unavailable through the whole bootstrap retry window, New
// refuses to build a degraded server and returns a sanitized error. The
// opener is stubbed: no network, no real sleeps.
func TestNew_UnavailableDB_FailsFast(t *testing.T) {
	const testDSN = "postgresql://nchat:sentinel-password@db.invalid:5432/nchat_test" //nolint:gosec
	attempts := 0
	stubOpenDB(t, func(context.Context, string, int, *slog.Logger) (storage.Pool, error) {
		attempts++
		return nil, storage.ErrDBBootstrapFailed
	})
	cfg := config.Config{
		ServiceName:              "chat-service",
		Env:                      "test",
		Port:                     8082,
		ReadHeaderTimeoutSeconds: 5,
		DatabaseURL:              testDSN,
		DBConnectTimeoutSeconds:  1,
	}

	start := time.Now()
	a, err := New(cfg)

	if err == nil {
		t.Fatal("expected bootstrap error when the DB is unreachable")
	}
	if !errors.Is(err, storage.ErrDBBootstrapFailed) {
		t.Fatalf("expected sanitized ErrDBBootstrapFailed, got %v", err)
	}
	for _, fragment := range []string{"sentinel-password", "db.invalid", "5432", "nchat_test"} {
		if strings.Contains(err.Error(), fragment) {
			t.Fatalf("bootstrap error must not leak DSN details (%q): %v", fragment, err)
		}
	}
	if a != nil {
		t.Fatal("expected no app when DB bootstrap fails; a degraded server must not start")
	}
	if attempts != 1 {
		t.Fatalf("expected exactly one opener call, got %d", attempts)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("fail-fast path must not sleep for real; took %s", elapsed)
	}
}

// TestNewWithStubbedDB_ReadyzReportsReady covers the success path without a
// real database: with a stubbed pool, valid JWT config, and a fake Valkey
// server for the reaction limiter, the app boots and /readyz reports 200.
func TestNewWithStubbedDB_ReadyzReportsReady(t *testing.T) {
	stubOpenDB(t, func(context.Context, string, int, *slog.Logger) (storage.Pool, error) {
		return stubPool{}, nil
	})
	cfg := config.Config{
		ServiceName:                    "chat-service",
		Env:                            "test",
		Port:                           8082,
		ReadHeaderTimeoutSeconds:       5,
		DatabaseURL:                    "postgres://stubbed",
		DBConnectTimeoutSeconds:        1,
		AuthJWTHMACSecret:              strings.Repeat("a", 32),
		AuthJWTIssuer:                  "nchat-auth",
		AuthJWTAudience:                "nchat-api",
		ValkeyURL:                      startFakeValkeyServer(t),
		ReactionRateLimitMaxActions:    5,
		ReactionRateLimitWindowSeconds: 60,
	}

	a := newTestApp(t, cfg)

	rec := httptest.NewRecorder()
	a.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected readyz 200 after successful bootstrap, got %d — %s", rec.Code, rec.Body.String())
	}
}

// TestApp_Shutdown_ClosesPoolOnce verifies that Shutdown closes the DB pool
// exactly once even when called repeatedly.
func TestApp_Shutdown_ClosesPoolOnce(t *testing.T) {
	cfg := config.Config{ServiceName: "chat-service", Env: "test", Port: 8082, ReadHeaderTimeoutSeconds: 5}
	a := newTestApp(t, cfg)

	closed := 0
	a.closeDB = func() { closed++ }

	if err := a.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	_ = a.Shutdown(context.Background())

	if closed != 1 {
		t.Fatalf("expected pool closed exactly once, got %d", closed)
	}
}

// TestApp_Shutdown_ConcurrentClosesPoolOnce hammers Shutdown from many
// goroutines: the pool must close exactly once (hub/presence/tracing order
// is covered by the sequential tests) and no call may panic. Run with -race.
func TestApp_Shutdown_ConcurrentClosesPoolOnce(t *testing.T) {
	cfg := config.Config{ServiceName: "chat-service", Env: "test", Port: 8082, ReadHeaderTimeoutSeconds: 5}
	a := newTestApp(t, cfg)

	closed := 0
	a.closeDB = func() { closed++ }

	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = a.Shutdown(context.Background())
		}()
	}
	wg.Wait()

	if closed != 1 {
		t.Fatalf("expected pool closed exactly once under concurrency, got %d", closed)
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

// ResolveWorkspaceID and GetDefaultWorkspaceID must stay the same answer: the
// WebSocket session bind uses one and the anti-spam guard the other, and two
// lookups that could disagree would let a session bind to one workspace while
// its sends were counted against another.
func TestAppWSWorkspaceResolverResolvesTheSameWorkspaceAsTheDefaultLookup(t *testing.T) {
	resolver := appWSWorkspaceResolver{store: workspaceResolverStore{workspace: domain.Workspace{ID: "workspace-1"}}}

	resolved, err := resolver.ResolveWorkspaceID(t.Context())
	if err != nil {
		t.Fatalf("ResolveWorkspaceID: %v", err)
	}
	canonical, err := resolver.GetDefaultWorkspaceID(t.Context())
	if err != nil {
		t.Fatalf("GetDefaultWorkspaceID: %v", err)
	}
	if resolved != canonical || resolved != "workspace-1" {
		t.Fatalf("ResolveWorkspaceID = %q, GetDefaultWorkspaceID = %q", resolved, canonical)
	}
}

// presenceReporter is what the channel- and group-details panels read presence
// through (issues #435, #441, #398). A tracker that was never wired must answer
// "nobody online" rather than panic — the panel then shows an empty preview,
// which is the honest outcome, and never a member whose presence is invented.
func TestPresenceReporterAnswersNobodyWithoutATracker(t *testing.T) {
	if online := (presenceReporter{}).OnlineUserIDs("workspace-1"); len(online) != 0 {
		t.Fatalf("an unwired tracker reported %v online", online)
	}
}

func TestPresenceReporterReportsTheTrackersOnlineUsers(t *testing.T) {
	tracker := ws.NewPresenceTracker(time.Minute)
	t.Cleanup(tracker.Stop)
	tracker.Connect("workspace-1", "user-1", "client-1")
	// Another workspace's session must not appear in this one's answer.
	tracker.Connect("workspace-2", "user-2", "client-2")

	online := presenceReporter{tracker: tracker}.OnlineUserIDs("workspace-1")

	if len(online) != 1 || online[0] != "user-1" {
		t.Fatalf("online = %v, want only user-1", online)
	}
}

func TestDomainMessageToWSPayloadMapsRemovalTimestamps(t *testing.T) {
	now := time.Now().UTC()
	got := domainMessageToWSPayload(domain.Message{
		ID: "message-1", WorkspaceID: "workspace-1", ChannelID: "channel-1", SenderID: "user-1",
		BodyText: "hello", EditedAt: now, DeletedAt: now,
	})
	if got.ID != "message-1" || got.WorkspaceID != "workspace-1" || got.ChannelID != "channel-1" || got.SenderID != "user-1" || got.BodyText != "" {
		t.Fatalf("unexpected payload: %+v", got)
	}
	if !got.IsRemoved || got.EditedAt == nil || got.DeletedAt == nil {
		t.Fatalf("removal timestamps not mapped: %+v", got)
	}

	got = domainMessageToWSPayload(domain.Message{
		ID: "message-2", WorkspaceID: "workspace-1", ChannelID: "channel-1",
		Status: domain.MessageStatusDeleted,
	})
	if !got.IsRemoved || got.DeletedAt != nil {
		t.Fatalf("deleted status without timestamp must still map as removed: %+v", got)
	}
}

func TestDomainMessageToWSUpdatedPayloadWithholdsDeletedBody(t *testing.T) {
	now := time.Now().UTC()
	got := domainMessageToWSUpdatedPayload(domain.Message{
		ID: "message-1", WorkspaceID: "workspace-1", ChannelID: "channel-1",
		BodyText: "deleted secret", BodyFormat: domain.MessageBodyFormatV3,
		Status: domain.MessageStatusDeleted, DeletedAt: now, UpdatedAt: now,
	})
	if got.Body != "" || !got.IsRemoved || got.Status != "deleted" || got.DeletedAt == nil || !got.UpdatedAt.Equal(now) {
		t.Fatalf("deleted update was not sanitized: %+v", got)
	}
}

func TestDomainMessageToWSPayloadMapsDeletedQuotePlaceholder(t *testing.T) {
	now := time.Now().UTC()
	got := domainMessageToWSPayload(domain.Message{
		ID: "message-1", WorkspaceID: "workspace-1", ChannelID: "channel-1", SenderID: "user-1",
		Quoted: &domain.QuotedMessage{
			ID: "parent-1", AuthorID: "user-2", BodyText: "quoted secret",
			BodyFormat: domain.MessageBodyFormatV1, Status: domain.MessageStatusDeleted,
			DeletedAt: now, CreatedAt: now.Add(-time.Hour),
		},
	})
	if got.Quoted == nil || got.Quoted.ID != "parent-1" || got.Quoted.AuthorID != "user-2" {
		t.Fatalf("quoted payload not mapped: %+v", got.Quoted)
	}
	if !got.Quoted.IsRemoved || got.Quoted.DeletedAt == nil || got.Quoted.Body != "" {
		t.Fatalf("deleted quoted body must be withheld: %+v", got.Quoted)
	}
}

func TestDomainMessageToWSPayloadMarksForwardedWithoutSourceMetadata(t *testing.T) {
	payload := domainMessageToWSPayload(domain.Message{
		ID: "forwarded", WorkspaceID: "workspace-1", ChannelID: "destination",
		ForwardedFromMessageID: "private-source-message",
	})
	if !payload.IsForwarded {
		t.Fatal("forwarded payload must carry is_forwarded")
	}
}

func TestDomainMessageToWSPayloadMarksOrdinaryMessageNotForwarded(t *testing.T) {
	payload := domainMessageToWSPayload(domain.Message{ID: "ordinary"})
	if payload.IsForwarded {
		t.Fatal("ordinary payload must carry is_forwarded=false")
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

type captureBroadcastBus struct{ published chan ws.Event }

func (b *captureBroadcastBus) Publish(_ context.Context, event ws.Event) error {
	b.published <- event
	return nil
}

func (*captureBroadcastBus) Subscribe(context.Context, func(ws.Event)) error { return nil }
func (*captureBroadcastBus) Close()                                          {}

func TestHubBroadcasterPublishesUpdatedMessage(t *testing.T) {
	bus := &captureBroadcastBus{published: make(chan ws.Event, 1)}
	hub := ws.NewHub(ws.NopAuthorizer{}, slog.Default(), bus, "test-updated-broadcaster")
	t.Cleanup(hub.Shutdown)
	editedAt := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)

	(&hubBroadcaster{hub: hub}).PublishMessageUpdated(t.Context(), "workspace-1", string(ws.TargetTypeChannel), "channel-1", domain.Message{
		ID: "message-1", WorkspaceID: "workspace-1", ChannelID: "channel-1",
		BodyText: "edited", BodyFormat: domain.MessageBodyFormatV3, EditedAt: editedAt, EditCount: 2,
	})

	select {
	case event := <-bus.published:
		if event.Type != ws.EventTypeMessageUpdated || event.WorkspaceID != "workspace-1" || event.TargetType != ws.TargetTypeChannel || event.TargetID != "channel-1" || event.MessageID != "message-1" {
			t.Fatalf("unexpected published event route: %+v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("message.updated was not published")
	}
}

func TestHubBroadcasterPublishesPinUpdated(t *testing.T) {
	hub := ws.NewHub(ws.NopAuthorizer{}, slog.Default(), ws.NopBus{}, "test-pin-broadcaster")
	t.Cleanup(hub.Shutdown)
	broadcaster := hubBroadcaster{hub: hub}

	broadcaster.PublishPinUpdated(t.Context(), "workspace-1", string(ws.TargetTypeDM), "dm-1", "message-1", "user-1", true)
}

// ── Add-members broadcasters (issue #398) ───────────────────────────────────
//
// These cover the adapter only: it is the seam where the HTTP layer's plain
// strings become ws types, and getting that translation wrong would route a
// correct event to the wrong room. Delivery, authorization and canonicalization
// belong to internal/ws and are not re-tested here — the bus is read purely as
// the observable proof that the adapter handed the right envelope over.

// publishedEvents drains up to want events, failing rather than hanging when the
// adapter publishes fewer than expected.
func publishedEvents(t *testing.T, bus *captureBroadcastBus, want int) []ws.Event {
	t.Helper()
	events := make([]ws.Event, 0, want)
	for range want {
		select {
		case event := <-bus.published:
			events = append(events, event)
		case <-time.After(time.Second):
			t.Fatalf("published %d event(s), want %d", len(events), want)
		}
	}
	return events
}

func TestHubBroadcasterPublishesMembersAdded(t *testing.T) {
	bus := &captureBroadcastBus{published: make(chan ws.Event, 1)}
	hub := ws.NewHub(ws.NopAuthorizer{}, slog.Default(), bus, "test-members-broadcaster")
	t.Cleanup(hub.Shutdown)

	// "dm" as a plain string, exactly as the HTTP layer passes it: the adapter
	// exists so that layer keeps no ws import.
	(&hubBroadcaster{hub: hub}).PublishMembersAdded(
		t.Context(), "workspace-1", "dm", "dm-1", "actor-1", 2, 7,
	)

	event := publishedEvents(t, bus, 1)[0]
	if event.Type != ws.EventTypeMembersAdded {
		t.Fatalf("Type = %q, want %q", event.Type, ws.EventTypeMembersAdded)
	}
	if event.WorkspaceID != "workspace-1" {
		t.Fatalf("WorkspaceID = %q, want workspace-1", event.WorkspaceID)
	}
	// The string became the typed target, and the ID travelled untouched.
	if event.TargetType != ws.TargetTypeDM || event.TargetID != "dm-1" {
		t.Fatalf("target = %s/%s, want dm/dm-1", event.TargetType, event.TargetID)
	}
	if event.Members == nil {
		t.Fatal("members payload was dropped by the adapter")
	}
	if event.Members.ActorUserID != "actor-1" ||
		event.Members.AddedCount != 2 || event.Members.MemberCount != 7 {
		t.Fatalf("members payload = %+v", event.Members)
	}
	// It is target-scoped: a recipient is what makes delivery bypass
	// subscriptions, and this event must never carry one.
	if event.RecipientUserID != "" {
		t.Fatalf("RecipientUserID = %q, want empty", event.RecipientUserID)
	}
}

// The event says how many people were added, never who. The adapter takes no
// member IDs at all, and this is the assertion that keeps it that way.
func TestHubBroadcasterMembersAddedCarriesNoMemberIdentities(t *testing.T) {
	bus := &captureBroadcastBus{published: make(chan ws.Event, 1)}
	hub := ws.NewHub(ws.NopAuthorizer{}, slog.Default(), bus, "test-members-pii")
	t.Cleanup(hub.Shutdown)

	(&hubBroadcaster{hub: hub}).PublishMembersAdded(
		t.Context(), "workspace-1", "channel", "channel-1", "actor-1", 3, 9,
	)

	encoded, err := json.Marshal(publishedEvents(t, bus, 1)[0])
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, leak := range []string{
		"added_user_ids", "user_ids", "participants", "display_name", "email", "body_text",
	} {
		if strings.Contains(string(encoded), leak) {
			t.Fatalf("members.added carried %q: %s", leak, encoded)
		}
	}
}

func TestHubBroadcasterPublishesConversationAvailablePerRecipient(t *testing.T) {
	// One event per recipient, so the buffer must hold both or the adapter would
	// block on the second.
	bus := &captureBroadcastBus{published: make(chan ws.Event, 2)}
	hub := ws.NewHub(ws.NopAuthorizer{}, slog.Default(), bus, "test-available-broadcaster")
	t.Cleanup(hub.Shutdown)

	(&hubBroadcaster{hub: hub}).PublishConversationAvailable(
		t.Context(), "workspace-1", "channel", "channel-1", []string{"added-1", "added-2"},
	)

	recipients := map[string]int{}
	for _, event := range publishedEvents(t, bus, 2) {
		if event.Type != ws.EventTypeConversationAvailable {
			t.Fatalf("Type = %q, want %q", event.Type, ws.EventTypeConversationAvailable)
		}
		if event.WorkspaceID != "workspace-1" {
			t.Fatalf("WorkspaceID = %q, want workspace-1", event.WorkspaceID)
		}
		if event.TargetType != ws.TargetTypeChannel || event.TargetID != "channel-1" {
			t.Fatalf("target = %s/%s, want channel/channel-1", event.TargetType, event.TargetID)
		}
		recipients[event.RecipientUserID]++
	}
	// Each addressee named exactly once, and nobody else: one event per
	// recipient is what stops a recipient from learning who else was added.
	if len(recipients) != 2 || recipients["added-1"] != 1 || recipients["added-2"] != 1 {
		t.Fatalf("recipients = %v, want one event each for added-1 and added-2", recipients)
	}
}

// A committed add that inserted nobody has no one to address, so the adapter
// must publish nothing rather than an event with an empty recipient.
func TestHubBroadcasterConversationAvailableWithNoRecipientsPublishesNothing(t *testing.T) {
	bus := &captureBroadcastBus{published: make(chan ws.Event, 1)}
	hub := ws.NewHub(ws.NopAuthorizer{}, slog.Default(), bus, "test-available-empty")
	t.Cleanup(hub.Shutdown)

	broadcaster := &hubBroadcaster{hub: hub}
	broadcaster.PublishConversationAvailable(t.Context(), "workspace-1", "dm", "dm-1", nil)
	broadcaster.PublishConversationAvailable(t.Context(), "workspace-1", "dm", "dm-1", []string{})

	select {
	case event := <-bus.published:
		t.Fatalf("published %+v for an add that named nobody", event)
	case <-time.After(50 * time.Millisecond):
	}
}

// Both adapters are wired in deployments with no distributed bus configured
// (NopBus). The defined behaviour there is local delivery only: the call
// completes normally and publishes nowhere.
func TestHubBroadcasterAddMembersSignalsSurviveAMissingBus(t *testing.T) {
	hub := ws.NewHub(ws.NopAuthorizer{}, slog.Default(), ws.NopBus{}, "test-members-nopbus")
	t.Cleanup(hub.Shutdown)
	broadcaster := &hubBroadcaster{hub: hub}

	broadcaster.PublishMembersAdded(t.Context(), "workspace-1", "channel", "channel-1", "actor-1", 1, 4)
	broadcaster.PublishConversationAvailable(
		t.Context(), "workspace-1", "channel", "channel-1", []string{"added-1"},
	)
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

// RF-32: a subscriber must be able to render a message that carries a file
// without a follow-up GET, and a removed one must describe nothing.
func TestDomainMessageToWSPayloadCarriesAttachmentMetadata(t *testing.T) {
	attachment := domain.MessageAttachment{
		ID: "attachment-1", Filename: "relatorio.pdf", ContentType: "application/pdf",
		SizeBytes: 2048, Status: "pending_scan", PreviewStatus: "pending",
	}
	payload := domainMessageToWSPayload(domain.Message{
		ID: "message-1", WorkspaceID: "workspace-1", ChannelID: "channel-1",
		SenderID: "user-1", Attachments: []domain.MessageAttachment{attachment},
	})
	if len(payload.Attachments) != 1 {
		t.Fatalf("attachment payload not mapped: %+v", payload.Attachments)
	}
	got := payload.Attachments[0]
	if got.ID != attachment.ID || got.Filename != attachment.Filename ||
		got.ContentType != attachment.ContentType || got.Size != attachment.SizeBytes ||
		got.Status != attachment.Status || got.PreviewStatus != attachment.PreviewStatus {
		t.Fatalf("attachment payload mismatch: %+v", got)
	}
}

func TestDomainMessageToWSPayloadWithholdsAttachmentsOnRemovedMessage(t *testing.T) {
	payload := domainMessageToWSPayload(domain.Message{
		ID: "message-1", Status: domain.MessageStatusDeleted, DeletedAt: time.Now().UTC(),
		Attachments: []domain.MessageAttachment{{ID: "attachment-1", Filename: "secret.pdf"}},
	})
	if payload.Attachments != nil {
		t.Fatalf("removed message must not describe its attachments: %+v", payload.Attachments)
	}
}
