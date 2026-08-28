package httpapi

import (
	"net/http"
	"time"

	"github.com/nicrepository/nchat/libs/go/platform/buildinfo"
	"github.com/nicrepository/nchat/libs/go/platform/health"
	"github.com/nicrepository/nchat/libs/go/platform/httputil"
	"github.com/nicrepository/nchat/services/chat-service/internal/config"
)

const readinessTimeout = time.Second

func Healthz(cfg config.Config) http.Handler {
	info := buildinfo.Current()
	return health.LivenessHandler(cfg.ServiceName, info.Version, info.Commit)
}

// ReadinessState captures which mandatory dependencies finished bootstrap.
// It is computed once by app.New, downgraded by NewRouter with what the
// router can observe directly, and never mutated afterwards — concurrent
// probe reads are race-free by construction.
//
// Each check answers "did this component's bootstrap complete?", NOT "is it
// healthy right now": this is not continuous monitoring. In particular,
// Database reports only that the PostgreSQL pool opened during bootstrap;
// pgxpool manages reconnections at runtime, and continuous DB health
// monitoring is a possible follow-up. Optional components (tracing, mention
// label cache, distributed WS broadcast) intentionally do not gate readiness.
type ReadinessState struct {
	Database         bool
	TokenValidator   bool
	SessionValidator bool
	Sidebar          bool
	Messages         bool
	WebSocket        bool
}

func Readyz(cfg config.Config, state ReadinessState) http.Handler {
	info := buildinfo.Current()
	return health.ReadinessHandler(cfg.ServiceName, info.Version, info.Commit, readinessChecks(cfg, state), readinessTimeout)
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

func readinessChecks(cfg config.Config, state ReadinessState) []health.Checker {
	reactionLimiterStatus := health.CheckPass
	if cfg.DatabaseURL != "" && cfg.ValkeyURL == "" {
		reactionLimiterStatus = health.CheckFail
	}
	return []health.Checker{
		health.NewStaticChecker("service-bootstrap", true, health.CheckPass, ""),
		health.NewStaticChecker("config-loaded", true, health.CheckPass, ""),
		health.NewStaticChecker("reaction-rate-limiter-configured", true, reactionLimiterStatus, ""),
		boolChecker("database", state.Database),
		boolChecker("jwt-validator", state.TokenValidator),
		boolChecker("session-validator", state.SessionValidator),
		boolChecker("sidebar-service", state.Sidebar),
		boolChecker("message-service", state.Messages),
		boolChecker("websocket", state.WebSocket),
	}
}

func boolChecker(name string, ready bool) health.Checker {
	status := health.CheckFail
	if ready {
		status = health.CheckPass
	}
	return health.NewStaticChecker(name, true, status, "")
}
