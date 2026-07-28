package httpapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/nicrepository/nchat/libs/go/platform/buildinfo"
	"github.com/nicrepository/nchat/libs/go/platform/health"
	"github.com/nicrepository/nchat/libs/go/platform/httputil"
	"github.com/nicrepository/nchat/services/file-service/internal/config"
)

const (
	readinessTimeout  = 4 * time.Second
	dependencyTimeout = 3 * time.Second
)

// Pinger is a readiness dependency: PostgreSQL and the SeaweedFS filer both
// satisfy it. The check reports only pass/fail and a fixed message, never a
// DSN, a hostname or a driver error.
type Pinger interface {
	Ping(ctx context.Context) error
}

func Healthz(cfg config.Config) http.Handler {
	info := buildinfo.Current()
	return health.LivenessHandler(cfg.ServiceName, info.Version, info.Commit)
}

func Readyz(cfg config.Config, database, storage Pinger) http.Handler {
	info := buildinfo.Current()
	return health.ReadinessHandler(
		cfg.ServiceName, info.Version, info.Commit,
		readinessChecks(cfg, database, storage), readinessTimeout,
	)
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

// readinessChecks only adds the dependency checks the enabled feature set
// actually needs, so a health-only deployment stays ready without a database.
func readinessChecks(cfg config.Config, database, storage Pinger) []health.Checker {
	checks := []health.Checker{
		health.NewStaticChecker("service-bootstrap", true, health.CheckPass, ""),
		health.NewStaticChecker("config-loaded", true, health.CheckPass, ""),
	}
	if cfg.UploadsEnabled {
		checks = append(checks,
			dependencyChecker{name: "postgres", pinger: database, unavailable: "PostgreSQL unavailable"},
			dependencyChecker{name: "object-storage", pinger: storage, unavailable: "object storage unavailable"},
		)
	}
	return checks
}

type dependencyChecker struct {
	name        string
	pinger      Pinger
	unavailable string
	timeout     time.Duration
}

func (c dependencyChecker) Name() string   { return c.name }
func (c dependencyChecker) Critical() bool { return true }

func (c dependencyChecker) Check(ctx context.Context) health.CheckResult {
	result := health.CheckResult{Name: c.Name(), Critical: c.Critical(), Status: health.CheckFail}
	if errors.Is(ctx.Err(), context.Canceled) {
		result.Message = c.name + " check canceled"
		return result
	}
	if c.pinger == nil {
		result.Message = c.unavailable
		return result
	}
	timeout := c.timeout
	if timeout <= 0 {
		timeout = dependencyTimeout
	}
	checkCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	err := c.pinger.Ping(checkCtx)
	switch {
	case err == nil && checkCtx.Err() == nil:
		result.Status = health.CheckPass
	case errors.Is(ctx.Err(), context.Canceled), errors.Is(err, context.Canceled):
		result.Message = c.name + " check canceled"
	case errors.Is(err, context.DeadlineExceeded), errors.Is(checkCtx.Err(), context.DeadlineExceeded):
		result.Message = c.name + " check timeout"
	default:
		result.Message = c.unavailable
	}
	return result
}
