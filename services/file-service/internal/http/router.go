package httpapi

import (
	"log/slog"
	"net/http"

	"github.com/nicrepository/nchat/libs/go/platform/httputil"
	"github.com/nicrepository/nchat/libs/go/platform/observability"
	"github.com/nicrepository/nchat/services/file-service/internal/config"
)

const RouteMetrics = "/metrics"

// RouterDependencies carries the wiring the attachment routes need. Every field
// is absent while uploads are disabled, and the routes then answer 503.
type RouterDependencies struct {
	TokenValidator  accessTokenValidator
	Attachments     AttachmentUseCases
	RateLimiter     *UserRateLimiter
	ReadinessPinger Pinger
	StoragePinger   Pinger
	// Observability is the registry the app already built so the attachment
	// counters and the HTTP counters end up on the same /metrics endpoint.
	Observability *observability.Metrics
	Metrics       *AttachmentMetrics
}

// NewRouter builds the file-service handler. Dependencies are variadic so the
// health-only construction keeps working unchanged.
func NewRouter(cfg config.Config, logger *slog.Logger, dependencies ...RouterDependencies) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	var deps RouterDependencies
	if len(dependencies) > 0 {
		deps = dependencies[0]
	}

	obsCfg := observability.LoadConfig(cfg.ServiceName)
	metrics := deps.Observability
	if metrics == nil {
		metrics = observability.NewMetrics(obsCfg)
	}

	mux := http.NewServeMux()
	mux.Handle(RouteHealthz, httputil.MethodNotAllowed(http.MethodGet, Healthz(cfg)))
	mux.Handle(RouteReadyz, httputil.MethodNotAllowed(http.MethodGet,
		Readyz(cfg, deps.ReadinessPinger, deps.StoragePinger)))
	mux.Handle(RouteVersion, httputil.MethodNotAllowed(http.MethodGet, Version(cfg)))
	mux.Handle(RouteMetrics, metrics.Handler())

	registerAttachmentRoutes(mux, cfg, logger, deps)

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		httputil.WriteError(w, http.StatusNotFound, httputil.ErrCodeNotFound, "not found")
	})

	obs := observability.HTTPMiddleware(obsCfg, metrics)
	return httputil.Recover(httputil.RequestID(httputil.SecurityHeaders(obs(mux))))
}

func registerAttachmentRoutes(
	mux *http.ServeMux, cfg config.Config, logger *slog.Logger, deps RouterDependencies,
) {
	handler := NewAttachmentHandler(deps.Attachments, cfg.MaxUploadBytes, deps.Metrics, logger)

	// Every attachment route is registered in every configuration. A disabled or
	// half-wired feature answers 503, so a misconfiguration is never mistaken
	// for a route that does not exist.
	authenticated := func(next http.Handler) http.Handler {
		switch {
		case !cfg.UploadsEnabled:
			return Unavailable("attachment uploads are disabled")
		case deps.TokenValidator == nil || !handler.Ready():
			return Unavailable("attachment dependencies unavailable")
		default:
			return BearerAuth(deps.TokenValidator)(next)
		}
	}
	rateLimited := func(next http.Handler) http.Handler {
		if deps.RateLimiter == nil {
			return next
		}
		return deps.RateLimiter.Middleware(next)
	}

	upload := func(next http.HandlerFunc) http.Handler {
		return authenticated(rateLimited(next))
	}
	mux.Handle("POST "+RouteChannelAttachments, upload(handler.UploadToChannel))
	mux.Handle("POST "+RouteDMAttachments, upload(handler.UploadToConversation))
	mux.Handle("GET "+RouteAttachment, authenticated(http.HandlerFunc(handler.GetMetadata)))
	mux.Handle("GET "+RouteAttachmentContent, authenticated(http.HandlerFunc(handler.DownloadContent)))
}
