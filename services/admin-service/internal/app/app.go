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

// The budget one administrator has for the active diagnostics of issue #582.
//
// Per administrator and per integration, not per IP: two operators debugging
// the same outage must not throttle each other, and one operator holding down a
// button must not turn the console into a scanner. The test message is one per
// minute with no burst at all — it leaves the platform, lands in a mailbox and
// costs the relay's reputation, so being able to press it twice in a row buys
// nothing an operator needs.
const (
	diagnosticsPerMinute = 6
	diagnosticsBurst     = 3
	testEmailsPerMinute  = 1
	testEmailsBurst      = 1
)

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
	// The configuration surface reads this pod's own environment to report the
	// deployment settings and credential status it does not own. That is the
	// only place it looks outside the database, it is read-only, and it is the
	// same environment every other service receives from the shared ConfigMap
	// and Secret.
	configuration := service.NewConfigService(storage.NewPGXConfigStore(pool), audit)
	// The observability surface (issue #581) reads the same environment for the
	// same reason, and adds one thing the rest of this service does not do:
	// outbound connections. Every destination comes from a compile-time
	// registry resolved against this pod's own environment — no request
	// supplies one — and the health check for PostgreSQL reuses the pool built
	// above rather than opening a second connection to it.
	healthMetrics := service.NewHealthMetrics()
	health := service.NewHealthService(store, healthMetrics)
	dashboard := service.NewDashboardService(health, storage.NewPGXMetricsStore(pool), healthMetrics)
	// The integration surface (issue #582) composes the two above and adds the
	// only outbound connection an operator can cause deliberately: the active
	// diagnostic. It gets its own limiters rather than sharing the session one,
	// because the thing worth bounding is not how often an address signs in but
	// how often one administrator can make this pod dial a dependency — and the
	// test message, which spends a relay's reputation, is bounded harder still.
	// The store is handed in as the authorizer: an operation whose effect
	// leaves the platform re-proves the administrator's authority against the
	// database at the last safe point, rather than trusting the snapshot the
	// middleware produced when the request arrived.
	integrations := service.NewIntegrationService(
		health, configuration, store, audit,
		httpapi.NewIPRateLimiter(diagnosticsPerMinute, diagnosticsBurst, nil),
		httpapi.NewIPRateLimiter(testEmailsPerMinute, testEmailsBurst, nil),
	)
	return httpapi.RouterDependencies{
		TokenValidator:   validator,
		Sessions:         sessions,
		Authenticator:    sessions,
		CSRF:             sessions,
		Audit:            httpapi.NewAuditPorts(audit, audit),
		RateLimiter:      httpapi.NewIPRateLimiter(cfg.SessionRateLimitPerMinute, cfg.SessionRateLimitBurst, httputil.ParseCIDRs(cfg.TrustedProxyCIDRs)),
		ReadinessPinger:  store,
		Management:       httpapi.NewManagementPorts(users, channels, policies),
		Configuration:    configuration,
		Observability:    httpapi.NewObservabilityPorts(dashboard, health),
		Integrations:     integrations,
		HealthCollectors: healthMetrics.Collectors(),
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
