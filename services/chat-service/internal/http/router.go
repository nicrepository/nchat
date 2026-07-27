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

// msgPostRateLimit is the maximum number of message-send requests an authenticated
// user may make per minute across all channels and DMs.
const msgPostRateLimit = 60

// messageForwardRateLimit isolates forwards from ordinary message writes.
const messageForwardRateLimit = 20

// pinActionRateLimit is the maximum number of pin/unpin writes per user/minute.
const pinActionRateLimit = 10

// mentionSearchRateLimit limits autocomplete enumeration independently from messages.
const mentionSearchRateLimit = 30

const RouteMetrics = "/metrics"

func NewRouter(cfg config.Config, logger *slog.Logger, state ReadinessState, validator *TokenValidator, sessionValidator SessionValidator, sidebar *SidebarHandler, messages *MessageHandler, wsHandler http.Handler, directMessages *DMHandler, channels *ChannelHandler, channelCategories *ChannelCategoryHandler) http.Handler {
	_ = logger
	if wsHandler == nil {
		wsHandler = unavailableWSHandler()
		// The substituted 503 handler is never functional WebSocket wiring.
		state.WebSocket = false
	}

	// state comes from the app bootstrap; Database is the app's exclusive
	// signal (pool opened) and is never derived from handler wiring. The
	// router only downgrades the other fields with what it can observe
	// directly, so an inconsistent caller cannot report a component as ready
	// while its wiring is missing.
	state.TokenValidator = state.TokenValidator && validator != nil
	state.SessionValidator = state.SessionValidator && sessionValidator != nil
	state.Sidebar = state.Sidebar && sidebar.Ready()
	state.Messages = state.Messages && messages.Ready()

	obsCfg := observability.LoadConfig(cfg.ServiceName)
	metrics := observability.NewMetrics(obsCfg)

	mux := http.NewServeMux()
	mux.Handle(RouteHealthz, httputil.MethodNotAllowed(http.MethodGet, Healthz(cfg)))
	mux.Handle(RouteReadyz, httputil.MethodNotAllowed(http.MethodGet, Readyz(cfg, state)))
	mux.Handle(RouteVersion, httputil.MethodNotAllowed(http.MethodGet, Version(cfg)))
	mux.Handle(RouteMetrics, metrics.Handler())

	// Authenticated sidebar endpoint: JWT validity + active session + active workspace member.
	mux.Handle(RouteSidebar, httputil.MethodNotAllowed(http.MethodGet,
		BearerAuth(validator)(RequireActiveSession(sessionValidator)(sidebar)),
	))

	authMiddleware := func(h http.Handler) http.Handler {
		return BearerAuth(validator)(RequireActiveSession(sessionValidator)(h))
	}

	// Shared rate limiters.
	// msgListLimiter: guards paginated GET list endpoints.
	// msgGetSingleLimiter: guards GET single-message fallback used by realtime WS.
	// msgPostLimiter: guards POST send-message (write endpoint).
	// pinActionLimiter: guards pin/unpin separately from normal message writes.
	// GC goroutines run for the process lifetime; tests that build a limiter
	// explicitly use t.Cleanup(limiter.Stop).
	msgListLimiter := NewUserRateLimiter(msgListRateLimit, time.Minute)
	msgGetSingleLimiter := NewUserRateLimiter(msgGetSingleRateLimit, time.Minute)
	msgPostLimiter := NewUserRateLimiter(msgPostRateLimit, time.Minute)
	messageForwardLimiter := NewUserRateLimiter(messageForwardRateLimit, time.Minute)
	pinActionLimiter := NewUserRateLimiter(pinActionRateLimit, time.Minute)
	mentionSearchLimiter := NewUserRateLimiter(mentionSearchRateLimit, time.Minute)

	// Static, non-sensitive configuration; authentication still prevents adding
	// a new public API surface.
	mux.Handle("GET "+RouteAllowedReactionEmojis, authMiddleware(
		msgListLimiter.Middleware(http.HandlerFunc(messages.ListAllowedReactionEmojis)),
	))

	// Channel message endpoints: GET list, POST create, GET single.
	mux.Handle("GET "+RouteChannelMessages, authMiddleware(
		msgListLimiter.Middleware(http.HandlerFunc(messages.ListChannelMessages)),
	))
	mux.Handle("POST "+RouteChannelMessages, authMiddleware(
		msgPostLimiter.Middleware(http.HandlerFunc(messages.CreateChannelMessage)),
	))
	mux.Handle("POST "+RouteChannelMessageForward, authMiddleware(
		newForwardMetrics(metrics).Middleware(
			messageForwardLimiter.Middleware(http.HandlerFunc(messages.ForwardChannelMessage)),
		),
	))
	mux.Handle("GET "+RouteChannelMessage, authMiddleware(
		msgGetSingleLimiter.Middleware(http.HandlerFunc(messages.GetChannelMessage)),
	))
	mux.Handle("POST "+RouteChannelReferences, authMiddleware(
		msgListLimiter.Middleware(http.HandlerFunc(messages.ResolveChannelMessageReferences)),
	))
	mux.Handle("GET "+RouteChannelMentions, authMiddleware(
		mentionSearchLimiter.Middleware(http.HandlerFunc(messages.SearchMentions)),
	))
	// Channel creation (RF-01). Registered only when wired, exactly like the DM
	// routes, so a build without the handler answers 404 rather than a misleading
	// 503 on a route that does not exist.
	if channels != nil {
		mux.Handle("POST "+RouteChannels, authMiddleware(http.HandlerFunc(channels.Create)))
	}
	// RF-17 channel categories. Registered only when wired, like the channel and
	// DM routes, so a build without the handler answers 404 on a route that does
	// not exist rather than a misleading 503. The listing shares the read budget;
	// the four mutations carry their own budget inside the handler.
	if channelCategories != nil {
		mux.Handle("GET "+RouteChannelCategories, authMiddleware(
			msgListLimiter.Middleware(http.HandlerFunc(channelCategories.List)),
		))
		mux.Handle("POST "+RouteChannelCategories, authMiddleware(http.HandlerFunc(channelCategories.Create)))
		mux.Handle("PUT "+RouteChannelCategoriesOrder, authMiddleware(http.HandlerFunc(channelCategories.Reorder)))
		mux.Handle("PATCH "+RouteChannelCategory, authMiddleware(http.HandlerFunc(channelCategories.Rename)))
		mux.Handle("DELETE "+RouteChannelCategory, authMiddleware(http.HandlerFunc(channelCategories.Delete)))
	}
	if directMessages != nil {
		mux.Handle("GET "+RouteDMCandidates, authMiddleware(http.HandlerFunc(directMessages.SearchCandidates)))
		mux.Handle("POST "+RouteDMConversations, authMiddleware(http.HandlerFunc(directMessages.GetOrCreateDirect)))
		mux.Handle("POST "+RouteDMGroupConversations, authMiddleware(http.HandlerFunc(directMessages.CreateGroup)))
	}

	// DM message endpoints: GET list, POST create, GET single.
	mux.Handle("GET "+RouteDMMessages, authMiddleware(
		msgListLimiter.Middleware(http.HandlerFunc(messages.ListDMMessages)),
	))
	mux.Handle("POST "+RouteDMMessages, authMiddleware(
		msgPostLimiter.Middleware(http.HandlerFunc(messages.CreateDMMessage)),
	))
	mux.Handle("GET "+RouteDMMessage, authMiddleware(
		msgGetSingleLimiter.Middleware(http.HandlerFunc(messages.GetDMMessage)),
	))
	mux.Handle("POST "+RouteDMReferences, authMiddleware(
		msgListLimiter.Middleware(http.HandlerFunc(messages.ResolveDMMessageReferences)),
	))

	// RF-13/RF-14 message editing, history, soft deletion, and workspace edit-window configuration.
	// The edit handler applies the shared Valkey Lua limiter before touching DB.
	mux.Handle("PATCH "+RouteMessage, authMiddleware(http.HandlerFunc(messages.EditMessage)))
	mux.Handle("DELETE "+RouteMessage, authMiddleware(
		msgPostLimiter.Middleware(http.HandlerFunc(messages.DeleteMessage)),
	))
	mux.Handle("GET "+RouteMessageEditHistory, authMiddleware(
		msgListLimiter.Middleware(http.HandlerFunc(messages.GetMessageEditHistory)),
	))
	mux.Handle("PATCH "+RouteWorkspaceSettings, authMiddleware(
		msgPostLimiter.Middleware(http.HandlerFunc(messages.UpdateWorkspaceEditWindow)),
	))

	// Favorite endpoints (RF-06): per-user private bookmarks. The list endpoint
	// only ever returns the authenticated caller's own favorites. Writes share
	// msgPostLimiter so favoriting cannot exceed the general write quota.
	mux.Handle("POST "+RouteMessageFavorite, authMiddleware(
		msgPostLimiter.Middleware(http.HandlerFunc(messages.FavoriteMessage)),
	))
	mux.Handle("DELETE "+RouteMessageFavorite, authMiddleware(
		msgPostLimiter.Middleware(http.HandlerFunc(messages.UnfavoriteMessage)),
	))
	mux.Handle("GET "+RouteFavorites, authMiddleware(
		msgListLimiter.Middleware(http.HandlerFunc(messages.ListFavorites)),
	))

	// Pin endpoints (RF-05): channel/DM pinned messages. Writes use current read
	// access and a dedicated pin-action budget; lists use the read budget.
	mux.Handle("POST "+RouteChannelMessagePin, authMiddleware(
		pinActionLimiter.Middleware(http.HandlerFunc(messages.PinMessage)),
	))
	mux.Handle("DELETE "+RouteChannelMessagePin, authMiddleware(
		pinActionLimiter.Middleware(http.HandlerFunc(messages.UnpinMessage)),
	))
	mux.Handle("GET "+RouteChannelPins, authMiddleware(
		msgListLimiter.Middleware(http.HandlerFunc(messages.ListPins)),
	))
	mux.Handle("POST "+RouteDMMessagePin, authMiddleware(
		pinActionLimiter.Middleware(http.HandlerFunc(messages.PinDMMessage)),
	))
	mux.Handle("DELETE "+RouteDMMessagePin, authMiddleware(
		pinActionLimiter.Middleware(http.HandlerFunc(messages.UnpinDMMessage)),
	))
	mux.Handle("GET "+RouteDMPins, authMiddleware(
		msgListLimiter.Middleware(http.HandlerFunc(messages.ListDMPins)),
	))

	// WebSocket endpoint: WSTokenMiddleware extracts a Bearer token from
	// Sec-WebSocket-Protocol for browser clients that cannot set Authorization
	// headers on WebSocket upgrades. auth middleware runs before upgrade so
	// that userID is in context when ServeWS reads it.
	mux.Handle(RouteWS, WSTokenMiddleware(authMiddleware(wsHandler)))

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
