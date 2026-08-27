package app

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	platformlog "github.com/nicrepository/nchat/libs/go/platform/log"
	"github.com/nicrepository/nchat/libs/go/platform/observability"
	"github.com/nicrepository/nchat/services/media-service/internal/config"
	"github.com/nicrepository/nchat/services/media-service/internal/domain"
	httpapi "github.com/nicrepository/nchat/services/media-service/internal/http"
	"github.com/nicrepository/nchat/services/media-service/internal/service"
	"github.com/nicrepository/nchat/services/media-service/internal/storage"
)

const liveKitTokenRateLimitPerMinute = 30

type App struct {
	Config          config.Config
	Logger          *slog.Logger
	Handler         http.Handler
	TracingShutdown observability.ShutdownFunc
	pool            storage.Pool
	rateLimiter     *httpapi.UserRateLimiter
	shutdownOnce    sync.Once
	shutdownErr     error
}

type appDependencies struct {
	openDB          func(context.Context, string, int) (storage.Pool, error)
	tracingShutdown observability.ShutdownFunc
}

func New(cfg config.Config) (*App, error) {
	return newApp(cfg, appDependencies{openDB: storage.OpenDB})
}

func newApp(cfg config.Config, deps appDependencies) (*App, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	logger := platformlog.New(cfg.ServiceName, cfg.Env)
	shutdown := deps.tracingShutdown
	if shutdown == nil {
		obsCfg := observability.LoadConfig(cfg.ServiceName)
		shutdown, _ = observability.SetupTracing(context.Background(), obsCfg)
	}
	application := &App{
		Config: cfg, Logger: logger, TracingShutdown: shutdown,
	}

	var routerDeps httpapi.RouterDependencies
	if cfg.LiveKitEnabled {
		validator, err := httpapi.NewTokenValidator(
			cfg.AuthJWTHMACSecret, cfg.AuthJWTIssuer, cfg.AuthJWTAudience,
		)
		if err != nil {
			_ = shutdownTracing(shutdown)
			return nil, err
		}
		clock := time.Now
		environment, err := domain.ParseEnvironment(cfg.Env)
		if err != nil {
			_ = shutdownTracing(shutdown)
			return nil, err
		}
		signer, err := service.NewLiveKitTokenSigner(environment, cfg.LiveKitAPIKey, cfg.LiveKitAPISecret, clock)
		if err != nil {
			_ = shutdownTracing(shutdown)
			return nil, err
		}
		if deps.openDB == nil {
			_ = shutdownTracing(shutdown)
			return nil, domainUnavailableError()
		}
		pool, err := deps.openDB(context.Background(), cfg.DatabaseURL, cfg.DBConnectTimeoutSeconds)
		if err != nil {
			_ = shutdownTracing(shutdown)
			return nil, domainUnavailableError()
		}
		limiter := httpapi.NewUserRateLimiter(liveKitTokenRateLimitPerMinute, time.Minute)
		application.pool = pool
		application.rateLimiter = limiter
		routerDeps = httpapi.RouterDependencies{
			TokenValidator: validator,
			TokenIssuer: service.NewTokenService(
				storage.NewPGXResourceAuthorizer(pool),
				signer,
				environment,
				time.Duration(cfg.LiveKitTokenTTLSeconds)*time.Second,
				clock,
			),
			RateLimiter:     limiter,
			ReadinessPinger: pool,
		}
	}
	application.Handler = httpapi.NewRouter(cfg, logger, routerDeps)
	return application, nil
}

func (a *App) Shutdown(ctx context.Context) error {
	if a == nil {
		return nil
	}
	a.shutdownOnce.Do(func() {
		if a.rateLimiter != nil {
			a.rateLimiter.Stop()
		}
		if a.pool != nil {
			a.pool.Close()
		}
		if a.TracingShutdown != nil {
			a.shutdownErr = a.TracingShutdown(ctx)
		}
	})
	return a.shutdownErr
}

func shutdownTracing(shutdown observability.ShutdownFunc) error {
	if shutdown == nil {
		return nil
	}
	return shutdown(context.Background())
}

func domainUnavailableError() error {
	return errors.New("media integration dependencies unavailable")
}
