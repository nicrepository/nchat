package httpapi

import (
	"net/http"
	"time"

	"github.com/nicrepository/nchat/libs/go/platform/buildinfo"
	"github.com/nicrepository/nchat/libs/go/platform/health"
	"github.com/nicrepository/nchat/libs/go/platform/httputil"
	"github.com/nicrepository/nchat/services/file-service/internal/config"
)

const readinessTimeout = time.Second

func Healthz(cfg config.Config) http.Handler {
	info := buildinfo.Current()
	return health.LivenessHandler(cfg.ServiceName, info.Version, info.Commit)
}

func Readyz(cfg config.Config) http.Handler {
	info := buildinfo.Current()
	return health.ReadinessHandler(cfg.ServiceName, info.Version, info.Commit, readinessChecks(), readinessTimeout)
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

func readinessChecks() []health.Checker {
	return []health.Checker{
		health.NewStaticChecker("service-bootstrap", true, health.CheckPass, ""),
		health.NewStaticChecker("config-loaded", true, health.CheckPass, ""),
	}
}
