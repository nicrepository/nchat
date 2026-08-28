package httpapi

import (
	"log/slog"
	"net/http"

	"github.com/nicrepository/nchat/libs/go/platform/httputil"
	"github.com/nicrepository/nchat/libs/go/platform/observability"
	"github.com/nicrepository/nchat/services/media-service/internal/config"
)

const RouteMetrics = "/metrics"

type RouterDependencies struct {
	TokenValidator  accessTokenValidator
	TokenIssuer     LiveKitTokenIssuer
	RateLimiter     *UserRateLimiter
	ReadinessPinger PostgresPinger
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
	var tokenHandler http.Handler
	switch {
	case !cfg.LiveKitEnabled:
		tokenHandler = LiveKitUnavailable(logger)
	case deps.TokenValidator == nil || deps.TokenIssuer == nil || deps.RateLimiter == nil:
		tokenHandler = LiveKitDependenciesUnavailable(logger)
	default:
		tokenHandler = BearerAuth(deps.TokenValidator)(
			deps.RateLimiter.Middleware(LiveKitToken(deps.TokenIssuer, cfg.LiveKitAPIURL, logger)),
		)
	}
	mux.Handle(RouteLiveKitToken, httputil.MethodNotAllowed(http.MethodPost, tokenHandler))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		httputil.WriteError(w, http.StatusNotFound, httputil.ErrCodeNotFound, "not found")
	})

	obs := observability.HTTPMiddleware(obsCfg, metrics)
	return httputil.Recover(httputil.RequestID(httputil.SecurityHeaders(obs(mux))))
}
