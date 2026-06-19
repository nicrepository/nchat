package httpapi

import (
	"log/slog"
	"net/http"

	"github.com/nicrepository/nchat/libs/go/platform/httputil"
	"github.com/nicrepository/nchat/libs/go/platform/observability"
	"github.com/nicrepository/nchat/services/chat-service/internal/config"
)

const RouteMetrics = "/metrics"

func NewRouter(cfg config.Config, logger *slog.Logger, validator *TokenValidator, sessionValidator SessionValidator, sidebar *SidebarHandler, messages *MessageHandler, wsHandler http.Handler) http.Handler {
	_ = logger
	if wsHandler == nil {
		wsHandler = unavailableWSHandler()
	}

	obsCfg := observability.LoadConfig(cfg.ServiceName)
	metrics := observability.NewMetrics(obsCfg)

	mux := http.NewServeMux()
	mux.Handle(RouteHealthz, httputil.MethodNotAllowed(http.MethodGet, Healthz(cfg)))
	mux.Handle(RouteReadyz, httputil.MethodNotAllowed(http.MethodGet, Readyz(cfg)))
	mux.Handle(RouteVersion, httputil.MethodNotAllowed(http.MethodGet, Version(cfg)))
	mux.Handle(RouteMetrics, metrics.Handler())

	// Authenticated sidebar endpoint: JWT validity + active session + active workspace member.
	mux.Handle(RouteSidebar, httputil.MethodNotAllowed(http.MethodGet,
		BearerAuth(validator)(RequireActiveSession(sessionValidator)(sidebar)),
	))

	authMiddleware := func(h http.Handler) http.Handler {
		return BearerAuth(validator)(RequireActiveSession(sessionValidator)(h))
	}

	// Channel message endpoints: GET list, POST create.
	mux.Handle("GET "+RouteChannelMessages, authMiddleware(
		http.HandlerFunc(messages.ListChannelMessages),
	))
	mux.Handle("POST "+RouteChannelMessages, authMiddleware(
		http.HandlerFunc(messages.CreateChannelMessage),
	))

	// DM message endpoints: GET list, POST create.
	mux.Handle("GET "+RouteDMMessages, authMiddleware(
		http.HandlerFunc(messages.ListDMMessages),
	))
	mux.Handle("POST "+RouteDMMessages, authMiddleware(
		http.HandlerFunc(messages.CreateDMMessage),
	))

	// WebSocket endpoint: auth middleware runs before upgrade so that
	// userID is in context when ServeWS reads it.
	mux.Handle(RouteWS, authMiddleware(wsHandler))

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		httputil.WriteError(w, http.StatusNotFound, httputil.ErrCodeNotFound, "not found")
	})

	obs := observability.HTTPMiddleware(obsCfg, metrics)
	return httputil.Recover(httputil.RequestID(httputil.SecurityHeaders(obs(mux))))
}

func unavailableWSHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		httputil.WriteError(w, http.StatusServiceUnavailable, "service_unavailable", "WebSocket not available")
	})
}
