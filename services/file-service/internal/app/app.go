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
	"github.com/nicrepository/nchat/services/file-service/internal/config"
	"github.com/nicrepository/nchat/services/file-service/internal/crypto"
	httpapi "github.com/nicrepository/nchat/services/file-service/internal/http"
	"github.com/nicrepository/nchat/services/file-service/internal/service"
	"github.com/nicrepository/nchat/services/file-service/internal/storage"
)

const (
	uploadRateLimitPerMinute = 20
	uploadRateLimitWindow    = time.Minute
)

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

// New validates the configuration and wires the service. It fails rather than
// starting with a half-built attachment feature: a missing master key, an
// unreachable database or an invalid storage endpoint is a start-up error, not
// a runtime surprise on the first upload.
func New(cfg config.Config) (*App, error) {
	return newApp(cfg, appDependencies{openDB: storage.OpenDB})
}

func newApp(cfg config.Config, deps appDependencies) (*App, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	logger := platformlog.New(cfg.ServiceName, cfg.Env)
	obsCfg := observability.LoadConfig(cfg.ServiceName)
	shutdown := deps.tracingShutdown
	if shutdown == nil {
		shutdown, _ = observability.SetupTracing(context.Background(), obsCfg)
	}
	application := &App{Config: cfg, Logger: logger, TracingShutdown: shutdown}

	metrics := observability.NewMetrics(obsCfg)
	routerDeps := httpapi.RouterDependencies{Observability: metrics}
	if cfg.UploadsEnabled {
		if err := application.wireAttachments(cfg, logger, metrics, deps, &routerDeps); err != nil {
			_ = shutdownTracing(shutdown)
			return nil, err
		}
	}
	application.Handler = httpapi.NewRouter(cfg, logger, routerDeps)
	return application, nil
}

func (a *App) wireAttachments(
	cfg config.Config,
	logger *slog.Logger,
	metrics *observability.Metrics,
	deps appDependencies,
	routerDeps *httpapi.RouterDependencies,
) error {
	validator, err := httpapi.NewTokenValidator(cfg.AuthJWTHMACSecret, cfg.AuthJWTIssuer, cfg.AuthJWTAudience)
	if err != nil {
		return err
	}
	kek, err := crypto.NewKeyEncryptionKey(cfg.EncryptionMasterKey)
	if err != nil {
		return err
	}
	objects, err := storage.NewSeaweedFSStore(
		cfg.SeaweedFSFilerURL, time.Duration(cfg.SeaweedFSTimeoutSeconds)*time.Second,
	)
	if err != nil {
		return err
	}
	if deps.openDB == nil {
		return errDependenciesUnavailable
	}
	pool, err := deps.openDB(context.Background(), cfg.DatabaseURL, cfg.DBConnectTimeoutSeconds)
	if err != nil {
		// The DSN and the driver message never reach the caller or the log.
		return errDependenciesUnavailable
	}

	attachmentMetrics := httpapi.NewAttachmentMetrics(metrics)
	limiter := httpapi.NewUserRateLimiter(uploadRateLimitPerMinute, uploadRateLimitWindow)
	a.pool = pool
	a.rateLimiter = limiter

	routerDeps.TokenValidator = validator
	routerDeps.Attachments = service.NewAttachmentService(
		storage.NewPGXDestinationAuthorizer(pool),
		storage.NewPGXAttachmentStore(pool),
		objects,
		kek,
		cfg.MaxUploadBytes,
		cfg.MalwareScanRequired,
		attachmentMetrics,
		logger,
	)
	routerDeps.RateLimiter = limiter
	routerDeps.ReadinessPinger = pool
	routerDeps.StoragePinger = objects
	routerDeps.Metrics = attachmentMetrics
	return nil
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

var errDependenciesUnavailable = errors.New("attachment dependencies unavailable")

func shutdownTracing(shutdown observability.ShutdownFunc) error {
	if shutdown == nil {
		return nil
	}
	return shutdown(context.Background())
}
