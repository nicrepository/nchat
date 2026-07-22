package app

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"

	platformlog "github.com/nicrepository/nchat/libs/go/platform/log"
	"github.com/nicrepository/nchat/libs/go/platform/observability"
	"github.com/nicrepository/nchat/services/chat-service/internal/config"
	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	httpapi "github.com/nicrepository/nchat/services/chat-service/internal/http"
	"github.com/nicrepository/nchat/services/chat-service/internal/service"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
	"github.com/nicrepository/nchat/services/chat-service/internal/ws"
)

// defaultPresenceAwayTimeout is the duration of connection inactivity after
// which a user is considered away. Not configurable yet; adjust here if needed.
const defaultPresenceAwayTimeout = 5 * time.Minute

// dbBootstrapTimeout bounds the total retry window for the initial database
// connection. Keep it below the Kubernetes startupProbe budget (60s) so a
// failed bootstrap exits and the container is restarted before the kubelet
// intervenes.
const dbBootstrapTimeout = 30 * time.Second

// openDBWithRetry is swappable in tests so app bootstrap failure paths run
// without real network access or real sleeps.
var openDBWithRetry = storage.OpenDBWithRetry

// App is the fully assembled chat-service application.
//
// Lifecycle ownership:
//   - hub:     owned by App; shut down via Shutdown.
//   - presence: owned by App; stopped via Shutdown after hub exits.
//   - tracing: shut down via Shutdown after presence stops.
//
// Call Shutdown to release all resources cleanly.
type App struct {
	Config          config.Config
	Logger          *slog.Logger
	Handler         http.Handler
	TracingShutdown observability.ShutdownFunc

	hub             *ws.Hub
	presence        *ws.PresenceTracker
	mentionCache    *storage.ValkeyMentionLabelCache
	reactionLimiter *ws.ValkeyReactionLimiter
	closeDB         func()
	shutdownOnce    sync.Once
}

// Shutdown stops the WebSocket hub, presence tracker, and tracing exporter in
// the correct order. Safe to call multiple times — subsequent calls are no-ops.
//
// Shutdown order:
//  1. hub.Shutdown() — drains and closes all WebSocket connections.
//  2. presence.Stop() — stops the background away-check goroutine.
//  3. closeDB() — closes the PostgreSQL pool after in-flight queries drain.
//  4. TracingShutdown(ctx) — flushes and closes the tracing exporter.
func (a *App) Shutdown(ctx context.Context) error {
	var err error
	a.shutdownOnce.Do(func() {
		a.hub.Shutdown()
		a.presence.Stop()
		if a.mentionCache != nil {
			a.mentionCache.Close()
		}
		if a.reactionLimiter != nil {
			a.reactionLimiter.Close()
		}
		// Close the DB pool only after the hub has drained connections that
		// may still be issuing queries.
		if a.closeDB != nil {
			a.closeDB()
		}
		err = a.TracingShutdown(ctx)
	})
	return err
}

// New assembles the application. Bootstrap outcomes by state:
//
//   - DATABASE_URL configured but unreachable: retry with backoff, then
//     fail fast — New returns an error, the process exits non-zero and
//     Kubernetes restarts the container.
//   - DATABASE_URL absent: configuration choice, not a transient failure —
//     the process stays alive and /readyz reports 503.
//   - Invalid JWT config: the process stays alive and /readyz reports 503.
//
// In every degraded state the pod never becomes Ready, so the Service sends
// it no traffic.
func New(cfg config.Config) (*App, error) {
	logger := platformlog.New(cfg.ServiceName, cfg.Env)
	obsCfg := observability.LoadConfig(cfg.ServiceName)
	shutdown, _ := observability.SetupTracing(context.Background(), obsCfg)

	// JWT token validator — nil when secret is not configured.
	validator, err := httpapi.NewTokenValidator(cfg.AuthJWTHMACSecret, cfg.AuthJWTIssuer, cfg.AuthJWTAudience)
	if err != nil {
		logger.Warn("sidebar auth disabled", "reason", "invalid_jwt_config")
	}

	var sidebarSvc *service.SidebarService
	var dmSvc *service.DMService
	var messageSvc *service.MessageService
	var mentionSvc *service.MentionService
	var workspaceStore *storage.PGXWorkspaceStore
	var sessionValidator storage.SessionValidator
	var channelStore *storage.PGXChannelStore
	var memberStore *storage.PGXMemberStore
	var dmStore *storage.PGXDMStore
	var mentionCache *storage.ValkeyMentionLabelCache
	var reactionSvc *service.ReactionService
	var favoriteSvc *service.FavoriteService
	var pinSvc *service.PinService
	var permissionSvc *service.PermissionService

	var closeDB func()
	databaseReady := false
	if cfg.DatabaseURL != "" {
		ctx, cancel := context.WithTimeout(context.Background(), dbBootstrapTimeout)
		pool, dbErr := openDBWithRetry(ctx, cfg.DatabaseURL, cfg.DBConnectTimeoutSeconds, logger)
		cancel()
		if dbErr != nil {
			// Fail fast: a half-wired server must never start serving.
			// Kubernetes restarts the container and the retry window resets.
			logger.Error("database bootstrap failed; refusing degraded start", "reason", "open_db_failed")
			_ = shutdown(context.Background())
			return nil, dbErr
		}
		databaseReady = true
		if closer, ok := pool.(interface{ Close() }); ok {
			closeDB = closer.Close
		}
		if validator != nil {
			sessionValidator = storage.NewPGXSessionValidator(pool)
			workspaceStore = storage.NewPGXWorkspaceStore(pool)
			channelStore = storage.NewPGXChannelStore(pool)
			memberStore = storage.NewPGXMemberStore(pool)
			dmStore = storage.NewPGXDMStore(pool)
			dmSvc = service.NewDMService(workspaceStore, dmStore, memberStore)
			messages := storage.NewPGXMessageStore(pool)
			reactionSvc = service.NewReactionService(storage.NewPGXReactionStore(pool))
			favoriteSvc = service.NewFavoriteService(storage.NewPGXFavoriteStore(pool))
			pinSvc = service.NewPinService(storage.NewPGXPinStore(pool))
			permissionSvc = service.NewPermissionService(memberStore, channelStore)
			sidebarSvc = service.NewSidebarService(workspaceStore, channelStore, memberStore, dmStore)
			messageSvc = service.NewMessageService(channelStore, dmStore, messages)
			mentionCache = wireMentionLabelCache(cfg.ValkeyURL, cfg.MentionLabelCacheTTLSeconds, messageSvc, logger)
			mentionSvc = service.NewMentionService(
				service.NewMemberService(memberStore, channelStore, workspaceStore),
				permissionSvc,
			)
		}
	}

	sidebar := httpapi.NewSidebarHandler(sidebarSvc)
	messageHandler := httpapi.NewMessageHandler(workspaceStore, messageSvc, nil)
	if mentionSvc != nil {
		messageHandler = httpapi.NewMessageHandler(workspaceStore, messageSvc, mentionSvc)
	}
	if favoriteSvc != nil {
		messageHandler = messageHandler.WithFavorites(favoriteSvc)
	}

	// Hub and presence are always created so their lifecycle is always managed
	// by Shutdown. When DB is unavailable, NopAuthorizer denies all subscriptions
	// and wsWorkspaces is nil so ServeWS returns 503 before any client connects.
	presence := ws.NewPresenceTracker(defaultPresenceAwayTimeout)
	var authorizer ws.SubscriptionAuthorizer = ws.NopAuthorizer{}
	var wsWorkspaces ws.WorkspaceResolver
	if workspaceStore != nil {
		authorizer = ws.NewServiceAuthorizer(permissionSvc, dmStore)
		wsWorkspaces = &appWSWorkspaceResolver{store: workspaceStore}
	}
	var bus ws.BroadcastBus = ws.NopBus{}
	if cfg.ValkeyWSBroadcastEnabled {
		if valkeyBus, busErr := ws.NewValkeyBus(cfg.ValkeyURL, cfg.WSInstanceID, logger); busErr != nil {
			logger.Warn("distributed ws broadcast disabled", "reason", "invalid_valkey_config")
		} else {
			bus = valkeyBus
		}
	}
	options := []ws.HubOption{ws.WithPresence(presence)}
	var reactionLimiter *ws.ValkeyReactionLimiter
	if reactionSvc != nil {
		if limiter, limiterErr := ws.NewValkeyReactionLimiter(
			cfg.ValkeyURL, cfg.ReactionRateLimitMaxActions, cfg.ReactionRateLimitWindowSeconds,
		); limiterErr != nil {
			logger.Warn("message reactions disabled", "reason", "invalid_valkey_config")
		} else {
			reactionLimiter = limiter
			options = append(options, ws.WithReactionHandler(&reactionHandlerAdapter{service: reactionSvc}), ws.WithReactionLimiter(limiter))
		}
	}
	if workspaceStore != nil {
		messageHandler = messageHandler.WithEditing(workspaceStore, permissionSvc, reactionLimiter)
	}
	var directMessages *httpapi.DMHandler
	if dmSvc != nil {
		directMessages = httpapi.NewDMHandler(workspaceStore, dmSvc, reactionLimiter)
	}
	hub := ws.NewHub(authorizer, logger, bus, cfg.WSInstanceID, options...)
	wsHandler := ws.ServeWSWithConfig(hub, logger, wsWorkspaces, httpapi.GetContextUserID, wsHandlerConfig(cfg))

	// Wire the hub as the broadcast publisher for message creation events.
	// SetPublisher is called after both messageSvc and hub are ready.
	if messageSvc != nil {
		messageSvc.SetPublisher(&hubBroadcaster{hub: hub})
	}

	// Pins broadcast over the same hub; wired after the hub exists (RF-05).
	if pinSvc != nil {
		messageHandler = messageHandler.WithPins(pinSvc, &hubBroadcaster{hub: hub})
	}

	// Database is the pool's own bootstrap outcome, independent of JWT or
	// service wiring; the remaining fields reflect each component's wiring.
	readiness := httpapi.ReadinessState{
		Database:         databaseReady,
		TokenValidator:   validator != nil,
		SessionValidator: sessionValidator != nil,
		Sidebar:          sidebarSvc != nil,
		Messages:         messageSvc != nil,
		WebSocket:        wsWorkspaces != nil && validator != nil && sessionValidator != nil,
	}

	return &App{
		Config:          cfg,
		Logger:          logger,
		Handler:         httpapi.NewRouter(cfg, logger, readiness, validator, sessionValidator, sidebar, messageHandler, wsHandler, directMessages),
		TracingShutdown: shutdown,
		hub:             hub,
		presence:        presence,
		mentionCache:    mentionCache,
		reactionLimiter: reactionLimiter,
		closeDB:         closeDB,
	}, nil
}

// wireMentionLabelCache creates the Valkey-backed mention label cache and
// attaches it to messageSvc, using the configured TTL. Returns the cache
// (nil when disabled or the connection failed) so the caller can track it
// for Shutdown. A connection failure is logged and non-fatal: the mention
// label cache is simply skipped and messageSvc falls back to resolving
// labels directly from storage on every read.
func wireMentionLabelCache(valkeyURL string, ttlSeconds int, messageSvc *service.MessageService, logger *slog.Logger) *storage.ValkeyMentionLabelCache {
	if valkeyURL == "" {
		return nil
	}
	cache, err := storage.NewValkeyMentionLabelCache(valkeyURL)
	if err != nil {
		logger.Warn("mention label cache disabled", "reason", "invalid_valkey_config")
		return nil
	}
	messageSvc.SetMentionLabelCache(cache)
	messageSvc.SetMentionLabelCacheTTL(time.Duration(ttlSeconds) * time.Second)
	return cache
}

func wsHandlerConfig(cfg config.Config) ws.HandlerConfig {
	return ws.HandlerConfig{
		MaxConnectionsPerUser:    cfg.WSMaxConnectionsPerUser,
		InboundMessagesPerMinute: cfg.WSInboundMessagesPerMinute,
		InboundBurst:             cfg.WSInboundBurst,
		MaxInvalidMessages:       cfg.WSMaxInvalidMessages,
	}
}

// appWSWorkspaceResolver adapts storage.PGXWorkspaceStore to ws.WorkspaceResolver.
// The workspace ID is always resolved server-side; client-provided IDs are never accepted.
type appWSWorkspaceResolver struct {
	store interface {
		GetDefaultWorkspace(ctx context.Context) (domain.Workspace, error)
	}
}

func (r *appWSWorkspaceResolver) GetDefaultWorkspaceID(ctx context.Context) (string, error) {
	workspace, err := r.store.GetDefaultWorkspace(ctx)
	if err != nil {
		return "", err
	}
	return workspace.ID, nil
}

// hubBroadcaster adapts ws.Hub to service.MessageEventPublisher.
// It converts the string targetType to ws.TargetType and domain.Message to
// ws.MessagePayload, keeping the service package free of a direct ws import.
type hubBroadcaster struct{ hub *ws.Hub }

type reactionHandlerAdapter struct{ service *service.ReactionService }

func (a *reactionHandlerAdapter) ToggleReaction(ctx context.Context, workspaceID, userID, messageID, emoji string) (ws.ReactionUpdate, error) {
	result, err := a.service.ToggleReaction(ctx, service.ToggleReactionInput{
		WorkspaceID: workspaceID, UserID: userID, MessageID: messageID, Emoji: emoji,
	})
	if err != nil {
		return ws.ReactionUpdate{}, err
	}
	targetType, targetID := ws.TargetTypeChannel, result.ChannelID
	if result.DMID != "" {
		targetType, targetID = ws.TargetTypeDM, result.DMID
	}
	reactions := make([]ws.ReactionPayload, len(result.Reactions))
	for i, reaction := range result.Reactions {
		reactions[i] = ws.ReactionPayload{Emoji: reaction.Emoji, Count: reaction.Count}
	}
	return ws.ReactionUpdate{
		MessageID: result.MessageID, TargetType: targetType, TargetID: targetID,
		Added: result.Added, Reactions: reactions,
	}, nil
}

func (b *hubBroadcaster) PublishMessageCreated(ctx context.Context, workspaceID, targetType, targetID string, msg domain.Message) {
	payload := domainMessageToWSPayload(msg)
	b.hub.PublishMessageCreated(ctx, workspaceID, ws.TargetType(targetType), targetID, payload)
}

func (b *hubBroadcaster) PublishMessageUpdated(ctx context.Context, workspaceID, targetType, targetID string, msg domain.Message) {
	b.hub.PublishMessageUpdated(ctx, workspaceID, ws.TargetType(targetType), targetID, domainMessageToWSUpdatedPayload(msg))
}

func domainMessageToWSUpdatedPayload(msg domain.Message) ws.MessageUpdatedPayload {
	removed := msg.Status == domain.MessageStatusDeleted || !msg.DeletedAt.IsZero()
	var deletedAt *time.Time
	if !msg.DeletedAt.IsZero() {
		t := msg.DeletedAt
		deletedAt = &t
	}
	body := msg.BodyText
	if removed {
		body = ""
	}
	return ws.MessageUpdatedPayload{
		MessageID: msg.ID, ChannelID: msg.ChannelID, DMID: msg.DMConversationID,
		Body: body, BodyFormat: string(msg.BodyFormat), EditedAt: msg.EditedAt,
		EditCount: msg.EditCount, IsEdited: msg.EditCount > 0,
		Status: string(msg.Status), IsRemoved: removed, DeletedAt: deletedAt, UpdatedAt: msg.UpdatedAt,
	}
}

// PublishPinUpdated adapts the hub for the RF-05 pin broadcaster interface.
func (b *hubBroadcaster) PublishPinUpdated(ctx context.Context, workspaceID, targetType, targetID, messageID, actorUserID string, pinned bool) {
	b.hub.PublishPinUpdated(ctx, workspaceID, ws.TargetType(targetType), targetID, messageID, actorUserID, pinned)
}

func domainMessageToWSPayload(msg domain.Message) ws.MessagePayload {
	var editedAt, deletedAt *time.Time
	if !msg.EditedAt.IsZero() {
		t := msg.EditedAt
		editedAt = &t
	}
	if !msg.DeletedAt.IsZero() {
		t := msg.DeletedAt
		deletedAt = &t
	}
	removed := msg.Status == domain.MessageStatusDeleted || deletedAt != nil
	body := msg.BodyText
	quoted := domainQuoteToWSPayload(msg.Quoted)
	if removed {
		body, quoted = "", nil
	}
	return ws.MessagePayload{
		ID:                msg.ID,
		WorkspaceID:       msg.WorkspaceID,
		ChannelID:         msg.ChannelID,
		DMConversationID:  msg.DMConversationID,
		SenderID:          msg.SenderID,
		SenderDisplayName: msg.SenderDisplayName,
		Kind:              string(msg.Kind),
		BodyText:          body,
		BodyFormat:        string(msg.BodyFormat),
		Status:            string(msg.Status),
		IsRemoved:         removed,
		CreatedAt:         msg.CreatedAt,
		UpdatedAt:         msg.UpdatedAt,
		EditedAt:          editedAt,
		DeletedAt:         deletedAt,
		Quoted:            quoted,
		HasReference:      msg.ReferencedMessageID != "",
	}
}

func domainQuoteToWSPayload(q *domain.QuotedMessage) *ws.QuotePayload {
	if q == nil {
		return nil
	}
	var deletedAt *time.Time
	if !q.DeletedAt.IsZero() {
		t := q.DeletedAt
		deletedAt = &t
	}
	payload := &ws.QuotePayload{
		ID:         q.ID,
		AuthorID:   q.AuthorID,
		BodyFormat: string(q.BodyFormat),
		IsRemoved:  q.Status == domain.MessageStatusDeleted || deletedAt != nil,
		DeletedAt:  deletedAt,
		CreatedAt:  q.CreatedAt,
	}
	if !payload.IsRemoved {
		payload.Body = q.BodyText
	}
	return payload
}
