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
	openDB          func(context.Context, string, int, int) (storage.Pool, error)
	openAdmission   func(storage.Pool, storage.UploadAdmissionLimits, *slog.Logger) (httpapi.UploadAdmission, error)
	tracingShutdown observability.ShutdownFunc
}

// New validates the configuration and wires the service. It fails rather than
// starting with a half-built attachment feature: a missing master key, an
// unreachable database or an invalid storage endpoint is a start-up error, not
// a runtime surprise on the first upload.
func New(cfg config.Config) (*App, error) {
	return newApp(cfg, appDependencies{openDB: storage.OpenDBWithMaxConns, openAdmission: newUploadAdmission})
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
	if deps.openDB == nil || deps.openAdmission == nil {
		return errDependenciesUnavailable
	}
	pool, err := deps.openDB(
		context.Background(), cfg.DatabaseURL, cfg.DBConnectTimeoutSeconds, cfg.DBMaxConnections,
	)
	if err != nil {
		// The DSN and the driver message never reach the caller or the log.
		return errDependenciesUnavailable
	}
	// Cluster-wide upload admission. It needs connections it can reserve for the
	// duration of a transfer, which the per-statement Pool interface cannot
	// express, so it is wired from the concrete pool. A pool that cannot supply
	// them leaves admission unwired, and the routes then answer 503 rather than
	// accepting uncounted uploads.
	admission, err := deps.openAdmission(pool, storage.UploadAdmissionLimits{
		Global:  cfg.UploadMaxConcurrent,
		PerUser: cfg.UploadMaxConcurrentPerUser,
	}, logger)
	if err != nil {
		pool.Close()
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
	routerDeps.Admission = admission
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

// newUploadAdmission builds the PostgreSQL-backed admission control.
//
// It needs the concrete pool: a slot is a session advisory lock held on a
// connection reserved for the whole upload, and the per-statement storage.Pool
// interface has no way to lend one out. A pool that is not the pgx
// implementation therefore cannot support admission, and saying so here keeps
// the failure at start-up rather than on the first upload.
func newUploadAdmission(
	pool storage.Pool, limits storage.UploadAdmissionLimits, logger *slog.Logger,
) (httpapi.UploadAdmission, error) {
	lockPool, ok := storage.LockConnPoolFrom(pool)
	if !ok {
		return nil, errDependenciesUnavailable
	}
	return storage.NewPGXUploadAdmission(lockPool, limits, logger), nil
}
