package httpapi

import (
	"net/http"

	"github.com/nicrepository/nchat/libs/go/platform/health"
	"github.com/nicrepository/nchat/libs/go/platform/httputil"
	"github.com/nicrepository/nchat/services/media-service/internal/config"
)

const (
	version = "0.0.0"
	commit  = "dev"
)

func Healthz(cfg config.Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httputil.WriteJSON(w, http.StatusOK, health.New(cfg.ServiceName))
	})
}

func Readyz(cfg config.Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httputil.WriteJSON(w, http.StatusOK, health.Response{Service: cfg.ServiceName, Status: "ready"})
	})
}

func Version(cfg config.Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httputil.WriteJSON(w, http.StatusOK, map[string]string{"service": cfg.ServiceName, "version": version, "commit": commit})
	})
}
