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
}

// WithSMTPWorkerProbe lets readiness observe the worker rather than only the
// configuration that was supposed to start it.
func WithSMTPWorkerProbe(probe func() bool) Option {
	return func(o *routerOptions) { o.smtpWorkerProbe = probe }
}

func NewRouter(cfg config.Config, logger *slog.Logger, opts ...Option) http.Handler {
	options := routerOptions{}
	for _, apply := range opts {
		apply(&options)
	}
	_ = logger

	obsCfg := observability.LoadConfig(cfg.ServiceName)
	metrics := observability.NewMetrics(obsCfg)

	mux := http.NewServeMux()
	mux.Handle(RouteHealthz, httputil.MethodNotAllowed(http.MethodGet, Healthz(cfg)))
	mux.Handle(RouteReadyz, httputil.MethodNotAllowed(http.MethodGet, Readyz(cfg, options)))
	mux.Handle(RouteVersion, httputil.MethodNotAllowed(http.MethodGet, Version(cfg)))
	mux.Handle(RouteMetrics, metrics.Handler())
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		httputil.WriteError(w, http.StatusNotFound, httputil.ErrCodeNotFound, "not found")
	})

	obs := observability.HTTPMiddleware(obsCfg, metrics)
	return httputil.Recover(httputil.RequestID(httputil.SecurityHeaders(obs(mux))))
}
