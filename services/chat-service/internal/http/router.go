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

// msgGetSingleRateLimit is the maximum number of single-message fetches an
// authenticated user may make per minute. WebSocket fallback uses this route;
// a separate budget prevents realtime recovery from degrading scroll/listing.
const msgGetSingleRateLimit = 120

// msgPostRateLimit is the maximum number of write requests an authenticated
// user may make per minute across all channels and DMs.
//
// Since RF-19 (issue #419) this no longer governs message *sends*: those go
// through AntiSpamGuard, whose budget is the workspace's configurable policy
// and whose counter is shared across replicas. It still guards the other writes
// below (delete, favorite, workspace settings), which are not the subject of
// RF-19. domain.DefaultMessageRateLimitPerMinute carries this same value
// forward as the send default, so nothing changed for an unconfigured
// workspace.
const msgPostRateLimit = 60

// messageForwardRateLimit isolates forwards from ordinary message writes.
const messageForwardRateLimit = 20

// pinActionRateLimit is the maximum number of pin/unpin writes per user/minute.
const pinActionRateLimit = 10

// mentionSearchRateLimit limits autocomplete enumeration independently from messages.
const mentionSearchRateLimit = 30

// linkReconcileRateLimit and linkReconcileRateWindow are the per-user budget for
// "Verificar novamente" (issue #135).
//
// A small number, because this is the only user-triggered route that reaches a
// paid third party. It is deliberately not the read budget: a client that treated
// the button as a poll would spend Cloudflare quota at 30 a minute per user, and
// the whole point of the feature is that it is a considered second look rather
// than a refresh.
//
// Enforced by the shared Valkey limiter inside the handler rather than by an
// in-process middleware, so it is six per minute for a *user* and not six per
// minute per replica — see actionRateLimiter. Without that, N pods would admit
// 6 × N.
//
// It is the outer of two limits. The inner one is a deployment-wide, durable
// cooldown of one provider search per canonical URL per minute
// (storage.ManualReconcileCooldown): this one bounds how often one person may
// ask, that one bounds how often anyone may cause a request to leave the
// building.
const (
	linkReconcileRateLimit  = 6
	linkReconcileRateWindow = time.Minute
	// linkReconcileAction names the counter. It joins the shared
	// `nchat:chat:action:` namespace, and the user id is hashed by the limiter, so
	// no identifier reaches Valkey in the clear.
	linkReconcileAction = "link_safety_reconcile"
)

const RouteMetrics = "/metrics"

func NewRouter(cfg config.Config, logger *slog.Logger, state ReadinessState, validator *TokenValidator, sessionValidator SessionValidator, sidebar *SidebarHandler, messages *MessageHandler, wsHandler http.Handler, directMessages *DMHandler, channels *ChannelHandler, channelCategories *ChannelCategoryHandler, antiSpam *AntiSpamGuard, sharedMetrics ...*observability.Metrics) http.Handler {
	_ = logger
	if wsHandler == nil {
		wsHandler = unavailableWSHandler()
		// The substituted 503 handler is never functional WebSocket wiring.
		state.WebSocket = false
	}
	state = readinessFromWiring(state, validator, sessionValidator, sidebar, messages)
	obsCfg := observability.LoadConfig(cfg.ServiceName)
	metrics := routerMetrics(obsCfg, sharedMetrics)

	r := newRouteSet(validator, sessionValidator, antiSpam)
	r.mux.Handle(RouteHealthz, httputil.MethodNotAllowed(http.MethodGet, Healthz(cfg)))
	r.mux.Handle(RouteReadyz, httputil.MethodNotAllowed(http.MethodGet, Readyz(cfg, state)))
	r.mux.Handle(RouteVersion, httputil.MethodNotAllowed(http.MethodGet, Version(cfg)))
	r.mux.Handle(RouteMetrics, metrics.Handler())
	r.registerSidebarRoutes(sidebar)
	r.registerMessageRoutes(messages, metrics)
	r.registerChannelRoutes(channels)
	r.registerChannelCategoryRoutes(channelCategories)
	r.registerDMRoutes(directMessages)
	r.registerMessageLifecycleRoutes(messages)
	r.registerLinkSafetyRoutes(messages)
	r.registerWorkspacePolicyRoutes(messages)
	r.registerFavoriteAndPinRoutes(messages)

	// WebSocket endpoint: WSTokenMiddleware extracts a Bearer token from
	// Sec-WebSocket-Protocol for browser clients that cannot set Authorization
	// headers on WebSocket upgrades. auth middleware runs before upgrade so
	// that userID is in context when ServeWS reads it.
	r.mux.Handle(RouteWS, WSTokenMiddleware(r.auth(wsHandler)))
	r.mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		httputil.WriteError(w, http.StatusNotFound, httputil.ErrCodeNotFound, "not found")
	})

	obs := observability.HTTPMiddleware(obsCfg, metrics)
	return httputil.Recover(httputil.RequestID(httputil.SecurityHeaders(obs(r.mux))))
}

// readinessFromWiring downgrades the bootstrap's readiness with what the
// router can observe directly, so an inconsistent caller cannot report a
// component as ready while its wiring is missing. Database is the app's
// exclusive signal (pool opened) and is never derived from handler wiring.
func readinessFromWiring(state ReadinessState, validator *TokenValidator, sessions SessionValidator, sidebar *SidebarHandler, messages *MessageHandler) ReadinessState {
	state.TokenValidator = state.TokenValidator && validator != nil
	state.SessionValidator = state.SessionValidator && sessions != nil
	state.Sidebar = state.Sidebar && sidebar.Ready()
	state.Messages = state.Messages && messages.Ready()
	return state
}

// routerMetrics picks the registry /metrics serves. It is normally built here,
// but RF-21 needs one counter registered before the router exists — the
// link-safety worker is wired during service construction. So the bootstrap
// may hand its own in, and this stays the only place that decides what
// /metrics serves. Variadic rather than a thirteenth positional parameter:
// every caller but the bootstrap wants the default.
func routerMetrics(obsCfg observability.Config, shared []*observability.Metrics) *observability.Metrics {
	if len(shared) > 0 && shared[0] != nil {
		return shared[0]
	}
	return observability.NewMetrics(obsCfg)
}

func unavailableWSHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		httputil.WriteError(w, http.StatusServiceUnavailable, "service_unavailable", "WebSocket not available")
	})
}
