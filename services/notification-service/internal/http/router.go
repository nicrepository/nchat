package httpapi

import (
	"log/slog"
	"net/http"

	"github.com/nicrepository/nchat/libs/go/platform/httputil"
	"github.com/nicrepository/nchat/libs/go/platform/observability"
	"github.com/nicrepository/nchat/services/notification-service/internal/config"
)

const RouteMetrics = "/metrics"

// Option configures the router. Variadic so the existing two-argument calls,
// including every test, keep working unchanged.
type Option func(*routerOptions)

type routerOptions struct {
	// smtpWorkerProbe reports whether the SMTP worker is alive. Nil when the
	// caller has no worker to report on, in which case readiness judges the
	// configuration alone.
	smtpWorkerProbe func() bool
	// notificationWorkerProbe is the same question for the outbox worker.
	notificationWorkerProbe func() bool
	// metrics is the process registry, when the caller owns one. Nil means the
	// router builds its own, which is what every test that only wants an HTTP
	// surface does.
	metrics *observability.Metrics
	// tokenValidator and principalResolver authenticate the push subscription
	// routes. All three are set together by WithPushSubscriptions, and a process
	// that cannot serve the feature — no database, no usable signing secret —
	// simply never applies that option, so all three stay nil and Authenticate
	// answers every push route with 503.
	tokenValidator    accessTokenValidator
	principalResolver PrincipalResolver
	// pushSubscriptions serves those routes once they are authenticated. Nil is
	// what mountPushSubscriptionRoutes reads as "nothing to serve them with".
	pushSubscriptions *PushSubscriptionHandler
}

// WithSMTPWorkerProbe lets readiness observe the worker rather than only the
// configuration that was supposed to start it.
func WithSMTPWorkerProbe(probe func() bool) Option {
	return func(o *routerOptions) { o.smtpWorkerProbe = probe }
}

// WithNotificationWorkerProbe lets readiness observe the notification outbox
// worker.
func WithNotificationWorkerProbe(probe func() bool) Option {
	return func(o *routerOptions) { o.notificationWorkerProbe = probe }
}

// WithMetrics serves an already-built registry instead of a fresh one.
//
// The notification worker registers its collectors while the App is wired, which
// is before the router exists, so the two must be the same registry or /metrics
// would serve everything except the worker's own numbers.
func WithMetrics(metrics *observability.Metrics) Option {
	return func(o *routerOptions) { o.metrics = metrics }
}

// WithPushSubscriptions mounts the Web Push subscription routes (issue #745).
//
// All three dependencies are taken together because a route with only some of
// them cannot serve anyone: authentication without a resolver would accept a
// token nothing checks against the database, and a handler without either would
// run with no principal at all.
func WithPushSubscriptions(
	validator accessTokenValidator, resolver PrincipalResolver, handler *PushSubscriptionHandler,
) Option {
	return func(o *routerOptions) {
		o.tokenValidator = validator
		o.principalResolver = resolver
		o.pushSubscriptions = handler
	}
}

func NewRouter(cfg config.Config, logger *slog.Logger, opts ...Option) http.Handler {
	options := routerOptions{}
	for _, apply := range opts {
		apply(&options)
	}
	_ = logger

	obsCfg := observability.LoadConfig(cfg.ServiceName)
	metrics := options.metrics
	if metrics == nil {
		metrics = observability.NewMetrics(obsCfg)
	}

	mux := http.NewServeMux()
	mux.Handle(RouteHealthz, httputil.MethodNotAllowed(http.MethodGet, Healthz(cfg)))
	mux.Handle(RouteReadyz, httputil.MethodNotAllowed(http.MethodGet, Readyz(cfg, options)))
	mux.Handle(RouteVersion, httputil.MethodNotAllowed(http.MethodGet, Version(cfg)))
	mux.Handle(RouteMetrics, metrics.Handler())
	mountPushSubscriptionRoutes(mux, cfg, options)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		httputil.WriteError(w, http.StatusNotFound, httputil.ErrCodeNotFound, "not found")
	})

	obs := observability.HTTPMiddleware(obsCfg, metrics)
	return httputil.Recover(httputil.RequestID(httputil.SecurityHeaders(obs(mux))))
}

// mountPushSubscriptionRoutes registers the push subscription routes and the
// push configuration they depend on.
//
// Every one of them goes through Authenticate, and there is no branch here that
// mounts a handler without it: a route that could ever be reached unauthenticated
// would be one that reads or writes somebody's subscriptions with no idea whose.
//
// The routes are registered even when the process could not build what serves
// them — no database, no usable signing secret (see pushSubscriptionOptions in
// the app package). Authenticate then refuses every request with 503, so a client
// is told the service is not ready rather than that the route does not exist
// (issue #902). A handler without both authentication dependencies is treated the
// same way, so a partial wiring can never reach a handler.
func mountPushSubscriptionRoutes(mux *http.ServeMux, cfg config.Config, options routerOptions) {
	validator, resolver := options.tokenValidator, options.principalResolver
	if options.pushSubscriptions == nil {
		validator, resolver = nil, nil
	}
	authenticate := Authenticate(validator, resolver)
	mux.Handle("GET "+RoutePushConfig,
		authenticate(PushConfig(cfg, options.notificationWorkerProbe)))
	mux.Handle("POST "+RoutePushSubscriptions,
		authenticate(http.HandlerFunc(options.pushSubscriptions.Register)))
	mux.Handle("GET "+RoutePushSubscriptions,
		authenticate(http.HandlerFunc(options.pushSubscriptions.List)))
	mux.Handle("DELETE "+RoutePushSubscription,
		authenticate(http.HandlerFunc(options.pushSubscriptions.Disable)))
}
