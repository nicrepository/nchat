package server

import (
	"net/http"
	"time"

	"github.com/nicrepository/nchat/libs/go/platform/buildinfo"
	"github.com/nicrepository/nchat/libs/go/platform/health"
	"github.com/nicrepository/nchat/libs/go/platform/httputil"
)

const readinessTimeout = time.Second

func NewHandler(serviceName string) http.Handler {
	info := buildinfo.Current()

	mux := http.NewServeMux()
	mux.Handle("/healthz", httputil.MethodNotAllowed(http.MethodGet, health.LivenessHandler(serviceName, info.Version, info.Commit)))
	mux.Handle("/readyz", httputil.MethodNotAllowed(http.MethodGet, health.ReadinessHandler(serviceName, info.Version, info.Commit, readinessChecks(), readinessTimeout)))
	mux.Handle("/version", httputil.MethodNotAllowed(http.MethodGet, versionHandler(serviceName)))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		httputil.WriteError(w, http.StatusNotFound, httputil.ErrCodeNotFound, "not found")
	})

	return httputil.Recover(httputil.RequestID(httputil.SecurityHeaders(mux)))
}

func versionHandler(serviceName string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		info := buildinfo.Current()
		httputil.WriteJSON(w, http.StatusOK, map[string]string{
			"service": serviceName,
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
