package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"

	"github.com/nicrepository/nchat/libs/go/platform/httputil"
	platformlog "github.com/nicrepository/nchat/libs/go/platform/log"
	"github.com/nicrepository/nchat/libs/go/platform/observability"
	"github.com/nicrepository/nchat/services/admin-service/internal/config"
	httpapi "github.com/nicrepository/nchat/services/admin-service/internal/http"
	"github.com/nicrepository/nchat/services/admin-service/internal/service"
	"github.com/nicrepository/nchat/services/admin-service/internal/storage"
)

type App struct {
	Config          config.Config
	Logger          *slog.Logger
	Handler         http.Handler
	TracingShutdown observability.ShutdownFunc
	pool            storage.Pool
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

// newApp builds the service, and refuses to build a broken one.
//
// There are two outcomes, and the difference between them is deliberate:
//
//   - the Admin API is *not configured* (no DATABASE_URL, no JWT secret). That
//     is a supported mode — a deployment that runs this service only for
//     /healthz and /version — so startup succeeds and every privileged route
//     answers 503.
//   - the Admin API *is* configured and a dependency it needs cannot be built.
//     That is an error, and it is returned.
//
// The second case used to degrade like the first, and that was wrong in a way
// Kubernetes cannot fix: nothing here reopens the pool, so the process would
// serve /healthz forever with /readyz stuck at 503, and readiness alone never
// restarts a container. Returning the error lets the process exit non-zero and
// be restarted, which is the only recovery this design has.
func newApp(cfg config.Config, deps appDependencies) (*App, error) {
	logger := platformlog.New(cfg.ServiceName, cfg.Env)
	shutdown := deps.tracingShutdown
	if shutdown == nil {
		obsCfg := observability.LoadConfig(cfg.ServiceName)
		shutdown, _ = observability.SetupTracing(context.Background(), obsCfg)
	}
	application := &App{Config: cfg, Logger: logger, TracingShutdown: shutdown}

	routerDeps, pool, err := buildAdminAPI(cfg, logger, deps)
	if err != nil {
		// The tracer was started above and must not outlive a failed boot.
		_ = shutdownTracing(shutdown)
		return nil, err
	}
	application.pool = pool
	application.Handler = httpapi.NewRouter(cfg, logger, routerDeps)
	return application, nil
}

func shutdownTracing(shutdown observability.ShutdownFunc) error {
	if shutdown == nil {
		return nil
	}
	return shutdown(context.Background())
}

// buildAdminAPI assembles the privileged dependencies.
//
// A nil error with empty dependencies means "not configured", which the router
// turns into 503 on the privileged paths. A non-nil error means the deployment
// asked for the Admin API and it cannot be provided.
func buildAdminAPI(cfg config.Config, logger *slog.Logger, deps appDependencies) (httpapi.RouterDependencies, storage.Pool, error) {
	if err := cfg.ValidateAdminAPI(); err != nil {
		// Not an error: this is the health-only deployment mode.
		logger.Warn("admin api disabled", "reason", err.Error())
		return httpapi.RouterDependencies{}, nil, nil
	}
	if deps.openDB == nil {
		return httpapi.RouterDependencies{}, nil, errors.New("admin api enabled but no database driver is wired")
	}
	pool, err := deps.openDB(context.Background(), cfg.DatabaseURL, cfg.DBConnectTimeoutSeconds)
	if err != nil {
		// The DSN is never logged or wrapped: it carries the database password.
		logger.Error("admin api unavailable", "reason", "database unreachable")
		return httpapi.RouterDependencies{}, nil, errors.New("admin api enabled but the database is unreachable")
	}
	validator, err := httpapi.NewTokenValidator(cfg.AuthJWTHMACSecret, cfg.AuthJWTIssuer, cfg.AuthJWTAudience)
	if err != nil {
		pool.Close()
		return httpapi.RouterDependencies{}, nil, fmt.Errorf("admin api jwt configuration: %w", err)
	}
	store := storage.NewPGXAdminStore(pool)
	sessions, err := service.NewAdminSessionService(store, cfg.AuthJWTHMACSecret, cfg.SessionIdleTTL, cfg.SessionAbsoluteTTL)
	if err != nil {
		pool.Close()
		return httpapi.RouterDependencies{}, nil, fmt.Errorf("admin session policy: %w", err)
	}
	audit := service.NewAuditService(store, logger)
	// The management stores share the pool, not the store: each owns its own
	// queries, so a change to the channel directory cannot reach the session
	// authorization path by being in the same file.
	users := service.NewUserAdminService(storage.NewPGXUserDirectoryStore(pool), audit)
	channels := service.NewChannelAdminService(storage.NewPGXChannelDirectoryStore(pool), audit)
	policies := service.NewPolicyService(storage.NewPGXPolicyStore(pool), audit)
	return httpapi.RouterDependencies{
		TokenValidator:  validator,
		Sessions:        sessions,
		Authenticator:   sessions,
		CSRF:            sessions,
		Audit:           httpapi.NewAuditPorts(audit, audit),
		RateLimiter:     httpapi.NewIPRateLimiter(cfg.SessionRateLimitPerMinute, cfg.SessionRateLimitBurst, httputil.ParseCIDRs(cfg.TrustedProxyCIDRs)),
		ReadinessPinger: store,
		Management:      httpapi.NewManagementPorts(users, channels, policies),
	}, pool, nil
}

func (a *App) Shutdown(ctx context.Context) error {
	if a == nil {
		return nil
	}
	a.shutdownOnce.Do(func() {
		if a.pool != nil {
			a.pool.Close()
		}
		if a.TracingShutdown != nil {
			a.shutdownErr = a.TracingShutdown(ctx)
		}
	})
	return a.shutdownErr
}
