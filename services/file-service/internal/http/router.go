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
	TokenValidator accessTokenValidator
	Attachments    AttachmentUseCases
	// Admission is the cluster-wide concurrency control. The per-minute
	// RateLimiter below counts request starts in one process; this one counts
	// transfers in flight across every replica, which is what an attacker
	// holding several slow uploads open actually consumes.
	Admission       UploadAdmission
	RateLimiter     *UserRateLimiter
	ReadinessPinger Pinger
	StoragePinger   Pinger
	// LinkPreviews serves RF-10. It has its own limiter rather than sharing the
	// upload budget: a client previewing the links in a busy channel would
	// otherwise spend the allowance that exists to protect uploads, and one
	// feature would silently deny the other.
	LinkPreviews           LinkPreviewUseCase
	LinkPreviewRateLimiter *UserRateLimiter
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
	registerLinkPreviewRoute(mux, cfg, deps)

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		httputil.WriteError(w, http.StatusNotFound, httputil.ErrCodeNotFound, "not found")
	})

	obs := observability.HTTPMiddleware(obsCfg, metrics)
	return httputil.Recover(httputil.RequestID(httputil.SecurityHeaders(obs(mux))))
}

func registerAttachmentRoutes(
	mux *http.ServeMux, cfg config.Config, logger *slog.Logger, deps RouterDependencies,
) {
	handler := NewAttachmentHandler(
		deps.Attachments, cfg.MaxUploadBytes,
		deps.Admission, cfg.UploadRetryAfterSeconds,
		deps.Metrics, logger,
	)

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
	// Listing a destination's recent attachments (issues #435 and #441) is a
	// read, so it is authenticated but not counted against the upload budget.
	mux.Handle("GET "+RouteChannelAttachments, authenticated(http.HandlerFunc(handler.ListChannelAttachments)))
	mux.Handle("GET "+RouteDMAttachments, authenticated(http.HandlerFunc(handler.ListConversationAttachments)))
	mux.Handle("GET "+RouteAttachment, authenticated(http.HandlerFunc(handler.GetMetadata)))
	mux.Handle("GET "+RouteAttachmentContent, authenticated(http.HandlerFunc(handler.DownloadContent)))
	// The preview is authenticated exactly like the content it is derived from,
	// and is not rate limited for the same reason the listing is not: it is a
	// read, and the upload budget exists to protect writes.
	mux.Handle("GET "+RouteAttachmentPreview, authenticated(http.HandlerFunc(handler.GetPreview)))
}

// registerLinkPreviewRoute wires RF-10.
//
// The route is registered in every configuration, exactly like the attachment
// routes: disabled or half-wired it answers 503, so a missing configuration is
// never mistaken for a route that does not exist.
//
// It is authenticated for a reason worth stating: an anonymous version of this
// endpoint would be an open fetcher anyone on the internet could point at
// arbitrary hosts, using this deployment's address and this deployment's
// budget. The rate limit is the second half of that — the route is the only one
// in the service where a caller decides how much outbound work happens.
func registerLinkPreviewRoute(mux *http.ServeMux, cfg config.Config, deps RouterDependencies) {
	guarded := func(next http.Handler) http.Handler {
		switch {
		case !cfg.LinkPreviewEnabled:
			return Unavailable("link previews are disabled")
		case deps.TokenValidator == nil || deps.LinkPreviews == nil:
			return Unavailable("link preview dependencies unavailable")
		default:
			return BearerAuth(deps.TokenValidator)(next)
		}
	}
	var handler http.Handler = http.HandlerFunc(NewLinkPreviewHandler(deps.LinkPreviews).Create)
	if deps.LinkPreviewRateLimiter != nil {
		handler = deps.LinkPreviewRateLimiter.Middleware(handler)
	}
	mux.Handle("POST "+RouteLinkPreview, guarded(handler))
}
