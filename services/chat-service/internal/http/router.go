package httpapi

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/nicrepository/nchat/libs/go/platform/httputil"
	"github.com/nicrepository/nchat/libs/go/platform/observability"
	"github.com/nicrepository/nchat/services/chat-service/internal/config"
)

// msgListRateLimit is the maximum number of message-listing requests an
// authenticated user may make per minute. Pagination fetches are cheap on the
// server but an unconstrained scroll could cause excessive DB reads.
const msgListRateLimit = 30

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

	// Shared rate limiter for message-listing routes (GET only; POST/send is not limited here).
	// The GC goroutine started by NewUserRateLimiter runs for the process lifetime; in
	// production this is fine (OS cleans up on exit). Tests that call NewRouter directly
	// (e.g., integration/auth-chain tests) accept this goroutine as a known bounded leak
	// — it is stopped by the test process exit and does not affect correctness or -race.
	// Unit tests that build a limiter explicitly use t.Cleanup(limiter.Stop).
	msgListLimiter := NewUserRateLimiter(msgListRateLimit, time.Minute)

	// Channel message endpoints: GET list, POST create.
	mux.Handle("GET "+RouteChannelMessages, authMiddleware(
		msgListLimiter.Middleware(http.HandlerFunc(messages.ListChannelMessages)),
	))
	mux.Handle("POST "+RouteChannelMessages, authMiddleware(
		http.HandlerFunc(messages.CreateChannelMessage),
	))

	// DM message endpoints: GET list, POST create.
	mux.Handle("GET "+RouteDMMessages, authMiddleware(
		msgListLimiter.Middleware(http.HandlerFunc(messages.ListDMMessages)),
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
