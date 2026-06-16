package app

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	platformlog "github.com/nicrepository/nchat/libs/go/platform/log"
	"github.com/nicrepository/nchat/libs/go/platform/observability"
	"github.com/nicrepository/nchat/services/chat-service/internal/config"
	httpapi "github.com/nicrepository/nchat/services/chat-service/internal/http"
	"github.com/nicrepository/nchat/services/chat-service/internal/service"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

type App struct {
	Config          config.Config
	Logger          *slog.Logger
	Handler         http.Handler
	TracingShutdown observability.ShutdownFunc
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
	if cfg.DatabaseURL != "" {
		ctx, cancel := context.WithTimeout(context.Background(), time.Duration(cfg.DBConnectTimeoutSeconds)*time.Second)
		defer cancel()
		pool, dbErr := storage.OpenDB(ctx, cfg.DatabaseURL, cfg.DBConnectTimeoutSeconds)
		if dbErr != nil {
			logger.Warn("database unavailable; endpoints disabled", "reason", "open_db_failed")
		} else if validator != nil {
			sessionValidator = storage.NewPGXSessionValidator(pool)
			workspaceStore = storage.NewPGXWorkspaceStore(pool)
			channels := storage.NewPGXChannelStore(pool)
			members := storage.NewPGXMemberStore(pool)
			dms := storage.NewPGXDMStore(pool)
			messages := storage.NewPGXMessageStore(pool)
			sidebarSvc = service.NewSidebarService(workspaceStore, channels, members, dms)
			messageSvc = service.NewMessageService(channels, dms, messages)
		}
	}

	sidebar := httpapi.NewSidebarHandler(sidebarSvc)
	messageHandler := httpapi.NewMessageHandler(workspaceStore, messageSvc)

	return &App{
		Config:          cfg,
		Logger:          logger,
		Handler:         httpapi.NewRouter(cfg, logger, validator, sessionValidator, sidebar, messageHandler),
		TracingShutdown: shutdown,
	}
}
