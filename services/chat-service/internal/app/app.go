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

	// Database pool — nil when DATABASE_URL is not configured.
	var sidebarSvc *service.SidebarService
	var sessionValidator storage.SessionValidator
	if cfg.DatabaseURL != "" {
		ctx, cancel := context.WithTimeout(context.Background(), time.Duration(cfg.DBConnectTimeoutSeconds)*time.Second)
		defer cancel()
		pool, dbErr := storage.OpenDB(ctx, cfg.DatabaseURL, cfg.DBConnectTimeoutSeconds)
		if dbErr != nil {
			logger.Warn("database unavailable; sidebar endpoint disabled", "reason", "open_db_failed")
		} else if validator != nil {
			sessionValidator = storage.NewPGXSessionValidator(pool)
			workspaces := storage.NewPGXWorkspaceStore(pool)
			channels := storage.NewPGXChannelStore(pool)
			members := storage.NewPGXMemberStore(pool)
			dms := storage.NewPGXDMStore(pool)
			sidebarSvc = service.NewSidebarService(workspaces, channels, members, dms)
		}
	}

	sidebar := httpapi.NewSidebarHandler(sidebarSvc)

	return &App{
		Config:          cfg,
		Logger:          logger,
		Handler:         httpapi.NewRouter(cfg, logger, validator, sessionValidator, sidebar),
		TracingShutdown: shutdown,
	}
}
