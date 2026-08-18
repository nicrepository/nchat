package server

import (
	"context"
	"net/http"
	"time"

	"github.com/nicrepository/nchat/libs/go/platform/buildinfo"
	"github.com/nicrepository/nchat/libs/go/platform/health"
	"github.com/nicrepository/nchat/libs/go/platform/httputil"
	"github.com/nicrepository/nchat/libs/go/platform/observability"
)

const readinessTimeout = time.Second

func NewHandler(serviceName string) http.Handler {
	return NewHandlerWithDependencies(serviceName, Dependencies{})
}

type Dependencies struct {
	Search          SearchProvider
	Tokens          AccessTokenValidator
	Sessions        SessionValidator
	ReadinessPinger interface{ Ping(context.Context) error }
}

func NewHandlerWithDependencies(serviceName string, deps Dependencies) http.Handler {
	info := buildinfo.Current()

	obsCfg := observability.LoadConfig(serviceName)
	metrics := observability.NewMetrics(obsCfg)

	mux := http.NewServeMux()
	mux.Handle("/healthz", httputil.MethodNotAllowed(http.MethodGet, health.LivenessHandler(serviceName, info.Version, info.Commit)))
	mux.Handle("/readyz", httputil.MethodNotAllowed(http.MethodGet, health.ReadinessHandler(serviceName, info.Version, info.Commit, readinessChecks(deps.ReadinessPinger), readinessTimeout)))
	mux.Handle("/version", httputil.MethodNotAllowed(http.MethodGet, versionHandler(serviceName)))
	mux.Handle("/metrics", metrics.Handler())
	if deps.Search != nil {
		search := NewSearchHandler(deps.Search)
		protect := func(h http.Handler) http.Handler {
			return BearerAuth(deps.Tokens)(RequireActiveSession(deps.Sessions)(h))
		}
		register := func(publicPath, strippedPath string, handler http.HandlerFunc) {
			protected := httputil.MethodNotAllowed(http.MethodGet, protect(handler))
			mux.Handle(publicPath, protected)
			mux.Handle(strippedPath, protected)
		}
		register("/api/search/messages", "/messages", search.Messages)
		register("/api/search/users", "/users", search.Users)
		register("/api/search/channels", "/channels", search.Channels)
	}
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		httputil.WriteError(w, http.StatusNotFound, httputil.ErrCodeNotFound, "not found")
	})

	obs := observability.HTTPMiddleware(obsCfg, metrics)
	return httputil.Recover(httputil.RequestID(httputil.SecurityHeaders(obs(mux))))
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

func readinessChecks(pinger interface{ Ping(context.Context) error }) []health.Checker {
	checks := []health.Checker{
		health.NewStaticChecker("service-bootstrap", true, health.CheckPass, ""),
		health.NewStaticChecker("config-loaded", true, health.CheckPass, ""),
	}
	if pinger != nil {
		checks = append(checks, databaseChecker{pinger: pinger})
	}
	return checks
}

type databaseChecker struct {
	pinger interface{ Ping(context.Context) error }
}

func (databaseChecker) Name() string   { return "postgres" }
func (databaseChecker) Critical() bool { return true }
func (c databaseChecker) Check(ctx context.Context) health.CheckResult {
	r := health.CheckResult{Name: c.Name(), Critical: true, Status: health.CheckPass}
	if err := c.pinger.Ping(ctx); err != nil {
		r.Status = health.CheckFail
		r.Message = "database unavailable"
	}
	return r
}
