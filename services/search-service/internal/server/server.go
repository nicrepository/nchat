package server

import (
	"context"
	"net/http"
	"slices"
	"strings"
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
		register := func(publicPath, strippedPath string, handler http.HandlerFunc, methods ...string) {
			protected := allowMethods(protect(handler), methods...)
			mux.Handle(publicPath, protected)
			mux.Handle(strippedPath, protected)
		}
		// Deprecated, retained for rollout compatibility: pre-#900 web builds.
		register("/api/search/messages", "/messages", search.LegacyMessages, http.MethodGet)
		// GET is the #900 contract, kept for older web builds; POST carries the
		// same search in the body, so the query never reaches a URL (#1081).
		register("/api/search/v2/messages", "/v2/messages", search.Messages, http.MethodGet, http.MethodPost)
		register("/api/search/users", "/users", search.Users, http.MethodGet, http.MethodPost)
		register("/api/search/channels", "/channels", search.Channels, http.MethodGet, http.MethodPost)
		register("/api/search/groups", "/groups", search.Groups, http.MethodGet, http.MethodPost)
		register("/api/search/files", "/files", search.Files, http.MethodGet, http.MethodPost)
		// Links has no GET: no client ever needs one (#1081).
		register("/api/search/links", "/links", search.Links, http.MethodPost)
	}
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		httputil.WriteError(w, http.StatusNotFound, httputil.ErrCodeNotFound, "not found")
	})

	obs := observability.HTTPMiddleware(obsCfg, metrics)
	return httputil.Recover(httputil.RequestID(httputil.SecurityHeaders(obs(mux))))
}

// allowMethods is httputil.MethodNotAllowed for a route that answers more than
// one method.
func allowMethods(next http.Handler, methods ...string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !slices.Contains(methods, r.Method) {
			w.Header().Set("Allow", strings.Join(methods, ", "))
			httputil.WriteError(w, http.StatusMethodNotAllowed, httputil.ErrCodeBadRequest, "method not allowed")
			return
		}
		next.ServeHTTP(w, r)
	})
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
