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

	hub          *ws.Hub
	presence     *ws.PresenceTracker
	shutdownOnce sync.Once
}

// Shutdown stops the WebSocket hub, presence tracker, and tracing exporter in
// the correct order. Safe to call multiple times — subsequent calls are no-ops.
//
// Shutdown order:
//  1. hub.Shutdown() — drains and closes all WebSocket connections.
//  2. presence.Stop() — stops the background away-check goroutine.
//  3. TracingShutdown(ctx) — flushes and closes the tracing exporter.
func (a *App) Shutdown(ctx context.Context) error {
	var err error
	a.shutdownOnce.Do(func() {
		a.hub.Shutdown()
		a.presence.Stop()
		err = a.TracingShutdown(ctx)
	})
	return err
}

func New(cfg config.Config) *App {
	logger := platformlog.New(cfg.ServiceName, cfg.Env)
	obsCfg := observability.LoadConfig(cfg.ServiceName)
	shutdown, _ := observability.SetupTracing(context.Background(), obsCfg)

	// JWT token validator — nil when secret is not configured.
	validator, err := httpapi.NewTokenValidator(cfg.AuthJWTHMACSecret, cfg.AuthJWTIssuer, cfg.AuthJWTAudience)
	if err != nil {
		logger.Warn("sidebar auth disabled", "reason", "invalid_jwt_config")
	}

	var sidebarSvc *service.SidebarService
	var messageSvc *service.MessageService
	var workspaceStore *storage.PGXWorkspaceStore
	var sessionValidator storage.SessionValidator
	var channelStore *storage.PGXChannelStore
	var memberStore *storage.PGXMemberStore
	var dmStore *storage.PGXDMStore

	if cfg.DatabaseURL != "" {
		ctx, cancel := context.WithTimeout(context.Background(), time.Duration(cfg.DBConnectTimeoutSeconds)*time.Second)
		defer cancel()
		pool, dbErr := storage.OpenDB(ctx, cfg.DatabaseURL, cfg.DBConnectTimeoutSeconds)
		if dbErr != nil {
			logger.Warn("database unavailable; endpoints disabled", "reason", "open_db_failed")
		} else if validator != nil {
			sessionValidator = storage.NewPGXSessionValidator(pool)
			workspaceStore = storage.NewPGXWorkspaceStore(pool)
			channelStore = storage.NewPGXChannelStore(pool)
			memberStore = storage.NewPGXMemberStore(pool)
			dmStore = storage.NewPGXDMStore(pool)
			messages := storage.NewPGXMessageStore(pool)
			sidebarSvc = service.NewSidebarService(workspaceStore, channelStore, memberStore, dmStore)
			messageSvc = service.NewMessageService(channelStore, dmStore, messages)
		}
	}

	sidebar := httpapi.NewSidebarHandler(sidebarSvc)
	messageHandler := httpapi.NewMessageHandler(workspaceStore, messageSvc)

	// Hub and presence are always created so their lifecycle is always managed
	// by Shutdown. When DB is unavailable, NopAuthorizer denies all subscriptions
	// and wsWorkspaces is nil so ServeWS returns 503 before any client connects.
	presence := ws.NewPresenceTracker(defaultPresenceAwayTimeout)
	var authorizer ws.SubscriptionAuthorizer = ws.NopAuthorizer{}
	var wsWorkspaces ws.WorkspaceResolver
	if workspaceStore != nil {
		permSvc := service.NewPermissionService(memberStore, channelStore)
		authorizer = ws.NewServiceAuthorizer(permSvc, dmStore)
		wsWorkspaces = &appWSWorkspaceResolver{store: workspaceStore}
	}
	hub := ws.NewHub(authorizer, logger, ws.NopBus{}, cfg.WSInstanceID, ws.WithPresence(presence))
	wsHandler := ws.ServeWSWithConfig(hub, logger, wsWorkspaces, httpapi.GetContextUserID, wsHandlerConfig(cfg))

	// Wire the hub as the broadcast publisher for message creation events.
	// SetPublisher is called after both messageSvc and hub are ready.
	if messageSvc != nil {
		messageSvc.SetPublisher(&hubBroadcaster{hub: hub})
	}

	return &App{
		Config:          cfg,
		Logger:          logger,
		Handler:         httpapi.NewRouter(cfg, logger, validator, sessionValidator, sidebar, messageHandler, wsHandler),
		TracingShutdown: shutdown,
		hub:             hub,
		presence:        presence,
	}
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

func (b *hubBroadcaster) PublishMessageCreated(ctx context.Context, workspaceID, targetType, targetID string, msg domain.Message) {
	payload := domainMessageToWSPayload(msg)
	b.hub.PublishMessageCreated(ctx, workspaceID, ws.TargetType(targetType), targetID, payload)
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
	return ws.MessagePayload{
		ID:                msg.ID,
		WorkspaceID:       msg.WorkspaceID,
		ChannelID:         msg.ChannelID,
		DMConversationID:  msg.DMConversationID,
		SenderID:          msg.SenderID,
		SenderDisplayName: msg.SenderDisplayName,
		Kind:              string(msg.Kind),
		BodyText:          msg.BodyText,
		Status:            string(msg.Status),
		IsRemoved:         deletedAt != nil,
		CreatedAt:         msg.CreatedAt,
		UpdatedAt:         msg.UpdatedAt,
		EditedAt:          editedAt,
		DeletedAt:         deletedAt,
	}
}
