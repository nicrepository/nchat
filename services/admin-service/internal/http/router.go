package httpapi

import (
	"log/slog"
	"net/http"
	"sort"
	"strings"

	"github.com/nicrepository/nchat/libs/go/platform/httputil"
	"github.com/nicrepository/nchat/libs/go/platform/observability"
	"github.com/nicrepository/nchat/services/admin-service/internal/config"
	"github.com/nicrepository/nchat/services/admin-service/internal/domain"
)

const RouteMetrics = "/metrics"

// RouterDependencies are the privileged components the Admin API needs. They
// arrive already constructed so the router never decides whether a dependency
// is safe to build — it only decides whether it has one.
type RouterDependencies struct {
	TokenValidator  accessTokenValidator
	Sessions        AdminSessionManager
	Authenticator   AdminAuthenticator
	CSRF            CSRFValidator
	Audit           *AuditPorts
	RateLimiter     *IPRateLimiter
	ReadinessPinger PostgresPinger
}

// AuditPorts groups the two directions of the audit trail. They are one
// component; splitting the interfaces keeps each consumer honest about which
// direction it uses — the capability guard only records, the audit endpoint
// only reads.
type AuditPorts struct {
	Recorder AuthorizationRecorder
	Reader   AuditReader
}

// NewAuditPorts wires the audit service into the router.
func NewAuditPorts(recorder AuthorizationRecorder, reader AuditReader) *AuditPorts {
	return &AuditPorts{Recorder: recorder, Reader: reader}
}

func NewRouter(cfg config.Config, logger *slog.Logger, dependencies ...RouterDependencies) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	var deps RouterDependencies
	if len(dependencies) > 0 {
		deps = dependencies[0]
	}

	obsCfg := observability.LoadConfig(cfg.ServiceName)
	metrics := observability.NewMetrics(obsCfg)

	mux := http.NewServeMux()
	mux.Handle(RouteHealthz, httputil.MethodNotAllowed(http.MethodGet, Healthz(cfg)))
	mux.Handle(RouteReadyz, httputil.MethodNotAllowed(http.MethodGet, Readyz(cfg, deps.ReadinessPinger)))
	mux.Handle(RouteVersion, httputil.MethodNotAllowed(http.MethodGet, Version(cfg)))
	mux.Handle(RouteMetrics, metrics.Handler())

	registerAdminAPI(mux, cfg, logger, deps)

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		httputil.WriteError(w, http.StatusNotFound, httputil.ErrCodeNotFound, "not found")
	})

	obs := observability.HTTPMiddleware(obsCfg, metrics)
	// GeneratedRequestID rather than RequestID: on this service the request ID
	// becomes the correlation_id of an audit row, so it must be minted here and
	// not accepted from the caller. The rest of the platform keeps the
	// trace-propagating variant.
	return httputil.Recover(httputil.GeneratedRequestID(httputil.SecurityHeaders(CORS(cfg.AllowedOrigins)(obs(mux)))))
}

// registerAdminAPI assembles the privileged routes, and only when every part
// of the guard chain exists.
//
// The all-or-nothing shape is the point. A partially wired pod — no database,
// no JWT secret, a half-built session service — serves a refusal on these
// paths instead of an unguarded handler, so a configuration mistake cannot
// leave the Admin API reachable without its guards. This mirrors the same
// decision in auth-service's router.
func registerAdminAPI(mux *http.ServeMux, cfg config.Config, logger *slog.Logger, deps RouterDependencies) {
	createSession := adminUnavailable()
	destroySession := adminUnavailable()
	bootstrap := adminUnavailable()
	auditEvents := adminUnavailable()

	ready := deps.TokenValidator != nil && deps.Sessions != nil && deps.Authenticator != nil &&
		deps.CSRF != nil && deps.Audit != nil && deps.RateLimiter != nil
	if ready {
		requireSession := RequireAdminSession(deps.Authenticator, sessionCookieName)
		requireCSRF := RequireCSRF(deps.CSRF, cfg.AllowedOrigins)

		// The handshake is the one route authenticated by a bearer token
		// rather than by the administrative cookie, so it is also the one
		// route that carries its own rate limit.
		createSession = deps.RateLimiter.Middleware(
			BearerAuth(deps.TokenValidator)(
				CreateAdminSession(deps.Sessions, deps.Audit.Recorder, cfg, httputil.ParseCIDRs(cfg.TrustedProxyCIDRs)),
			),
		)
		destroySession = requireSession(requireCSRF(DestroyAdminSession(deps.Sessions, deps.Audit.Recorder, cfg, logger)))
		bootstrap = requireSession(Bootstrap(cfg, deps.Sessions))
		auditEvents = requireSession(
			RequireCapability(domain.CapabilityAuditRead, deps.Audit.Recorder)(
				ListAuditEvents(deps.Audit.Reader),
			),
		)
	}

	mux.Handle(RouteAdminSession, methodRouter(map[string]http.Handler{
		http.MethodPost:   createSession,
		http.MethodDelete: destroySession,
	}))
	mux.Handle(RouteAdminBootstrap, httputil.MethodNotAllowed(http.MethodGet, bootstrap))
	mux.Handle(RouteAdminAudit, httputil.MethodNotAllowed(http.MethodGet, auditEvents))
}

func adminUnavailable() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeUnavailable(w)
	})
}

// methodRouter dispatches one path across methods, answering 405 with an
// Allow header for the rest. Same shape as auth-service's.
func methodRouter(handlers map[string]http.Handler) http.Handler {
	allowed := make([]string, 0, len(handlers)+1)
	for method := range handlers {
		allowed = append(allowed, method)
	}
	allowed = append(allowed, http.MethodOptions)
	allowHeader := joinSorted(allowed)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if handler, ok := handlers[r.Method]; ok {
			handler.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Allow", allowHeader)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		httputil.WriteError(w, http.StatusMethodNotAllowed, httputil.ErrCodeBadRequest, "method not allowed")
	})
}

func joinSorted(values []string) string {
	sorted := make([]string, len(values))
	copy(sorted, values)
	sort.Strings(sorted)
	return strings.Join(sorted, ", ")
}
