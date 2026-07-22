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

// Readyz preserves the standalone readiness contract used by unit tests and
// local tooling. Runtime routers should use ReadyzWithBootstrap so Kubernetes
// does not route traffic to a process whose required dependencies failed to
// initialize.
func Readyz(cfg config.Config) http.Handler {
	return ReadyzWithBootstrap(cfg, true)
}

func ReadyzWithBootstrap(cfg config.Config, bootstrapReady bool) http.Handler {
	info := buildinfo.Current()
	return health.ReadinessHandler(cfg.ServiceName, info.Version, info.Commit, readinessChecks(cfg, bootstrapReady), readinessTimeout)
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

func readinessChecks(cfg config.Config, bootstrapReady bool) []health.Checker {
	bootstrapStatus := health.CheckPass
	bootstrapMessage := ""
	if !bootstrapReady {
		bootstrapStatus = health.CheckFail
		bootstrapMessage = "database or token bootstrap unavailable"
	}

	reactionLimiterStatus := health.CheckPass
	if cfg.DatabaseURL != "" && cfg.ValkeyURL == "" {
		reactionLimiterStatus = health.CheckFail
	}
	return []health.Checker{
		health.NewStaticChecker("service-bootstrap", true, bootstrapStatus, bootstrapMessage),
		health.NewStaticChecker("config-loaded", true, health.CheckPass, ""),
		health.NewStaticChecker("reaction-rate-limiter-configured", true, reactionLimiterStatus, ""),
	}
}
