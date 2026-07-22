package httpapi

import (
	"net/http"
	"time"

	"github.com/nicrepository/nchat/libs/go/platform/buildinfo"
	"github.com/nicrepository/nchat/libs/go/platform/health"
	"github.com/nicrepository/nchat/libs/go/platform/httputil"
	"github.com/nicrepository/nchat/services/auth-service/internal/config"
)

const readinessTimeout = time.Second

func Healthz(cfg config.Config) http.Handler {
	info := buildinfo.Current()
	return health.LivenessHandler(cfg.ServiceName, info.Version, info.Commit)
}

// ReadinessState captures which mandatory dependencies finished bootstrap.
// Values are computed once during router construction and never mutated, so
// concurrent probe reads are race-free by construction.
//
// Each check answers "did this component's bootstrap complete?", NOT "is it
// healthy right now": this is not continuous monitoring. In particular,
// Database reports only that the PostgreSQL pool opened during bootstrap
// (the user service is wired immediately after the pool opens, independent
// of JWT config); pgxpool manages reconnections at runtime, and continuous
// DB health monitoring is a possible follow-up. Optional components (OIDC,
// email outbox, tracing) intentionally do not gate readiness.
type ReadinessState struct {
	Database       bool
	TokenManager   bool
	LoginManager   bool
	SessionManager bool
}

func Readyz(cfg config.Config, state ReadinessState) http.Handler {
	info := buildinfo.Current()
	return health.ReadinessHandler(cfg.ServiceName, info.Version, info.Commit, readinessChecks(state), readinessTimeout)
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

func readinessChecks(state ReadinessState) []health.Checker {
	return []health.Checker{
		health.NewStaticChecker("service-bootstrap", true, health.CheckPass, ""),
		health.NewStaticChecker("config-loaded", true, health.CheckPass, ""),
		boolChecker("database", state.Database),
		boolChecker("jwt-token-manager", state.TokenManager),
		boolChecker("login-manager", state.LoginManager),
		boolChecker("session-manager", state.SessionManager),
	}
}

func boolChecker(name string, ready bool) health.Checker {
	status := health.CheckFail
	if ready {
		status = health.CheckPass
	}
	return health.NewStaticChecker(name, true, status, "")
}
