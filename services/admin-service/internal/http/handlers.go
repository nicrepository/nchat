package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/nicrepository/nchat/libs/go/platform/buildinfo"
	"github.com/nicrepository/nchat/libs/go/platform/health"
	"github.com/nicrepository/nchat/libs/go/platform/httputil"
	"github.com/nicrepository/nchat/services/admin-service/internal/config"
)

const readinessTimeout = time.Second

// PostgresPinger is the readiness signal for the Admin API's only backing
// store.
type PostgresPinger interface {
	Ping(ctx context.Context) error
}

func Healthz(cfg config.Config) http.Handler {
	info := buildinfo.Current()
	return health.LivenessHandler(cfg.ServiceName, info.Version, info.Commit)
}

// Readyz reports the Admin API's real dependencies.
//
// The database check is derived from the pool instance, never from a
// configuration value: a nil pool means the Admin API was not wired, and a pod
// in that state must not be sent traffic that it would answer with 503.
func Readyz(cfg config.Config, pinger PostgresPinger) http.Handler {
	info := buildinfo.Current()
	return health.ReadinessHandler(cfg.ServiceName, info.Version, info.Commit, readinessChecks(cfg, pinger), readinessTimeout)
}

func Version(cfg config.Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		info := buildinfo.Current()
		httputil.WriteJSON(w, http.StatusOK, map[string]string{
			"service": cfg.ServiceName,
			"version": info.Version,
			"commit":  info.Commit,
		})
	})
}

// readinessChecks adds the database check only when the Admin API is
// configured, so a deployment that runs this service for health and version
// alone stays ready — the same rule file-service applies to its upload
// dependencies. When the Admin API *is* configured, the check is derived from
// the pool instance rather than from configuration: a nil pool is a pod that
// cannot serve a privileged request and must not receive one.
func readinessChecks(cfg config.Config, pinger PostgresPinger) []health.Checker {
	checks := []health.Checker{
		health.NewStaticChecker("service-bootstrap", true, health.CheckPass, ""),
		health.NewStaticChecker("config-loaded", true, health.CheckPass, ""),
	}
	if !cfg.AdminAPIEnabled() {
		return checks
	}
	return append(checks, databaseChecker{pinger: pinger})
}

type databaseChecker struct {
	pinger PostgresPinger
}

func (c databaseChecker) Name() string   { return "postgres" }
func (c databaseChecker) Critical() bool { return true }

func (c databaseChecker) Check(ctx context.Context) health.CheckResult {
	result := health.CheckResult{Name: c.Name(), Critical: true, Status: health.CheckFail}
	if c.pinger == nil {
		result.Message = "PostgreSQL unavailable"
		return result
	}
	if err := c.pinger.Ping(ctx); err != nil {
		result.Message = "PostgreSQL unavailable"
		return result
	}
	result.Status = health.CheckPass
	return result
}
