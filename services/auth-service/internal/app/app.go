package app

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	platformlog "github.com/nicrepository/nchat/libs/go/platform/log"
	"github.com/nicrepository/nchat/libs/go/platform/observability"
	"github.com/nicrepository/nchat/services/auth-service/internal/config"
	httpapi "github.com/nicrepository/nchat/services/auth-service/internal/http"
	"github.com/nicrepository/nchat/services/auth-service/internal/service"
	"github.com/nicrepository/nchat/services/auth-service/internal/storage"
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

	var users service.UserCreator
	var auth service.AuthSessionManager
	var login service.LoginManager
	var password service.PasswordRecoveryManager
	var invites service.InviteManager
	var pool storage.Pool
	if cfg.DatabaseURL != "" {
		ctx, cancel := context.WithTimeout(context.Background(), time.Duration(cfg.DBConnectTimeoutSeconds)*time.Second)
		defer cancel()
		openedPool, err := storage.OpenDB(ctx, cfg.DatabaseURL, cfg.DBConnectTimeoutSeconds)
		if err != nil {
			logger.Warn("database unavailable; auth database endpoints disabled", "reason", "open_db_failed")
		} else {
			pool = openedPool
			users = service.NewUserService(storage.NewPGXUserStore(pool))
		}
	}

	tokens, err := service.NewTokenManager(service.TokenConfig{
		HMACSecret: cfg.AuthJWTHMACSecret,
		Issuer:     cfg.AuthJWTIssuer,
		Audience:   cfg.AuthJWTAudience,
		AccessTTL:  time.Duration(cfg.AuthAccessTokenTTLSeconds) * time.Second,
		RefreshTTL: time.Duration(cfg.AuthRefreshTokenTTLSeconds) * time.Second,
	})
	switch {
	case err != nil:
		logger.Warn("auth token endpoints disabled", "reason", "invalid_jwt_config")
	case pool == nil:
		logger.Warn("auth token endpoints disabled", "reason", "database_not_configured")
	default:
		auth = service.NewAuthService(tokens, storage.NewPGXSessionStore(pool))
		login = service.NewLoginService(tokens, storage.NewPGXLoginStore(pool, service.VerifyPassword, service.RunDummyPasswordVerification))
		password = service.NewPasswordResetService(tokens, storage.NewPGXPasswordResetStore(pool))
		invites = service.NewInviteService(tokens, storage.NewPGXInviteStore(pool))
	}

	return &App{
		Config:          cfg,
		Logger:          logger,
		Handler:         httpapi.NewRouter(cfg, logger, users, auth, login, password, invites),
		TracingShutdown: shutdown,
	}
}
