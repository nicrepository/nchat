package httpapi

import (
	"net/http"
	"time"

	"github.com/nicrepository/nchat/libs/go/platform/httputil"
	"github.com/nicrepository/nchat/libs/go/platform/observability"
)

// routeSet is the mux plus the middleware every authenticated route group
// shares, so each group registers its handlers without re-deriving them.
//
// Shared rate limiters:
//   - list: guards paginated GET list endpoints.
//   - getSingle: guards GET single-message fallback used by realtime WS.
//   - post: guards POST send-message (write endpoint).
//   - pinAction: guards pin/unpin separately from normal message writes.
//
// GC goroutines run for the process lifetime; tests that build a limiter
// explicitly use t.Cleanup(limiter.Stop).
type routeSet struct {
	mux              *http.ServeMux
	validator        *TokenValidator
	sessionValidator SessionValidator
	antiSpam         *AntiSpamGuard

	list          *UserRateLimiter
	getSingle     *UserRateLimiter
	post          *UserRateLimiter
	forward       *UserRateLimiter
	pinAction     *UserRateLimiter
	mentionSearch *UserRateLimiter
}

func newRouteSet(validator *TokenValidator, sessionValidator SessionValidator, antiSpam *AntiSpamGuard) *routeSet {
	return &routeSet{
		mux: http.NewServeMux(), validator: validator, sessionValidator: sessionValidator, antiSpam: antiSpam,
		list:          NewUserRateLimiter(msgListRateLimit, time.Minute),
		getSingle:     NewUserRateLimiter(msgGetSingleRateLimit, time.Minute),
		post:          NewUserRateLimiter(msgPostRateLimit, time.Minute),
		forward:       NewUserRateLimiter(messageForwardRateLimit, time.Minute),
		pinAction:     NewUserRateLimiter(pinActionRateLimit, time.Minute),
		mentionSearch: NewUserRateLimiter(mentionSearchRateLimit, time.Minute),
	}
}

// auth is JWT validity + active session; every route below runs behind it.
func (r *routeSet) auth(h http.Handler) http.Handler {
	return BearerAuth(r.validator)(RequireActiveSession(r.sessionValidator)(h))
}

// handle registers one authenticated route behind an optional limiter.
func (r *routeSet) handle(pattern string, limiter *UserRateLimiter, h http.HandlerFunc) {
	var handler http.Handler = h
	if limiter != nil {
		handler = limiter.Middleware(handler)
	}
	r.mux.Handle(pattern, r.auth(handler))
}

// sendLimit is RF-19 (issue #419): every route that creates a message goes
// through it, so there is exactly one place a send can be admitted from and no
// second entry point to bypass. The WebSocket is not one of them —
// Hub.handleClientMessage has no send frame, so message creation is HTTP-only.
//
// The guard also resolves each request's canonical workspace server-side and
// publishes it in the request context, so the workspace a send is counted
// against is the same one the handler writes to.
//
// When the guard is absent (Valkey unconfigured, so the shared counter does
// not exist) sends answer 503. Falling back to the in-process post limiter
// would hand every replica its own full budget — the cross-instance bypass
// RF-19 exists to close — so the routes refuse rather than degrade quietly.
func (r *routeSet) sendLimit(h http.Handler) http.Handler {
	if r.antiSpam == nil {
		return antiSpamUnavailable()
	}
	return r.antiSpam.Middleware(h)
}

// registerSidebarRoutes: the authenticated sidebar read plus its private
// preferences. Pins, mutes and read markers are private preferences, but still
// writes; they reuse the established pin-action budget so they cannot become
// an unbounded write API.
func (r *routeSet) registerSidebarRoutes(sidebar *SidebarHandler) {
	r.mux.Handle(RouteSidebar, httputil.MethodNotAllowed(http.MethodGet, r.auth(sidebar)))
	r.handle("POST "+RouteChannelSidebarPin, r.pinAction, sidebar.PinChannel)
	r.handle("DELETE "+RouteChannelSidebarPin, r.pinAction, sidebar.UnpinChannel)
	r.handle("POST "+RouteDMSidebarPin, r.pinAction, sidebar.PinDM)
	r.handle("DELETE "+RouteDMSidebarPin, r.pinAction, sidebar.UnpinDM)
	r.handle("POST "+RouteChannelMute, r.pinAction, sidebar.MuteChannel)
	r.handle("DELETE "+RouteChannelMute, r.pinAction, sidebar.UnmuteChannel)
	r.handle("POST "+RouteDMMute, r.pinAction, sidebar.MuteDM)
	r.handle("DELETE "+RouteDMMute, r.pinAction, sidebar.UnmuteDM)
	r.handle("POST "+RouteChannelRead, r.pinAction, sidebar.MarkChannelRead)
	r.handle("POST "+RouteDMRead, r.pinAction, sidebar.MarkDMRead)
	// The canonical whole-preference write (issue #136) shares the pin-action
	// budget with the mute shortcut above, deliberately: they change the same
	// row, so giving the newer route its own budget would only mean a caller
	// could spend twice as much by alternating between them.
	r.handle("PUT "+RouteChannelNotificationPreference, r.pinAction, sidebar.SetChannelNotificationPreference)
	r.handle("PUT "+RouteDMNotificationPreference, r.pinAction, sidebar.SetDMNotificationPreference)
}

// registerMessageRoutes: channel and DM message listing, creation, single
// fetch, references, security snapshots and mention search.
func (r *routeSet) registerMessageRoutes(messages *MessageHandler, metrics *observability.Metrics) {
	// Static, non-sensitive configuration; authentication still prevents adding
	// a new public API surface.
	r.handle("GET "+RouteAllowedReactionEmojis, r.list, messages.ListAllowedReactionEmojis)

	r.handle("GET "+RouteChannelMessages, r.list, messages.ListChannelMessages)
	r.mux.Handle("POST "+RouteChannelMessages, r.auth(r.sendLimit(http.HandlerFunc(messages.CreateChannelMessage))))
	// A forward creates a message, so it spends the anti-spam budget like any
	// other send. Its own tighter budget stays on top: RF-19 makes the general
	// limit configurable, it does not raise the dedicated forward cap.
	r.mux.Handle("POST "+RouteChannelMessageForward, r.auth(
		newForwardMetrics(metrics).Middleware(
			r.forward.Middleware(r.sendLimit(http.HandlerFunc(messages.ForwardChannelMessage))),
		),
	))
	r.handle("GET "+RouteChannelMessage, r.getSingle, messages.GetChannelMessage)
	r.handle("POST "+RouteChannelReferences, r.list, messages.ResolveChannelMessageReferences)
	r.handle("POST "+RouteChannelSecuritySnapshots, r.list, messages.GetChannelMessageSecuritySnapshots)
	r.handle("GET "+RouteChannelMentions, r.mentionSearch, messages.SearchMentions)
	r.handle("GET "+RouteDMMentions, r.mentionSearch, messages.SearchDMMentions)

	r.handle("GET "+RouteDMMessages, r.list, messages.ListDMMessages)
	r.mux.Handle("POST "+RouteDMMessages, r.auth(r.sendLimit(http.HandlerFunc(messages.CreateDMMessage))))
	r.handle("GET "+RouteDMMessage, r.getSingle, messages.GetDMMessage)
	r.handle("POST "+RouteDMReferences, r.list, messages.ResolveDMMessageReferences)
	r.handle("POST "+RouteDMSecuritySnapshots, r.list, messages.GetDMMessageSecuritySnapshots)
}

// registerChannelRoutes: channel creation (RF-01) and its mutations.
// Registered only when wired, exactly like the DM routes, so a build without
// the handler answers 404 rather than a misleading 503 on a route that does not
// exist. Authorization is enforced inside ChannelService — registration grants
// nothing on its own — and the write budget is applied in the handlers so it
// holds per user rather than per replica.
func (r *routeSet) registerChannelRoutes(channels *ChannelHandler) {
	if channels == nil {
		return
	}
	r.handle("POST "+RouteChannels, nil, channels.Create)
	// Rename (issue #527). PATCH only, and only under the {channelID} segment:
	// the literal /details, /members and /call-participants sit under the same
	// prefix and Go's mux prefers them.
	r.handle("PATCH "+RouteChannel, nil, channels.Rename)
	// Self-leave (issue #527). DELETE on the caller's own membership.
	r.handle("DELETE "+RouteChannelMembership, nil, channels.Leave)
	// Channel details (issue #435) is a read, so it shares the listing budget
	// rather than the write one: the panel refetches on every channel switch.
	r.handle("GET "+RouteChannelDetails, r.list, channels.Details)
	// Call-participant identity resolution (issue #612) carries its own budget
	// inside the handler, like add-members.
	r.handle("POST "+RouteChannelCallParticipants, nil, channels.CallParticipants)
	// Add members (issue #398), candidate search and admin removal (issue #685)
	// carry their own budget inside the handler. Registered only when the member
	// service is wired so a partially built service answers 404.
	if channels.HasMembers() {
		r.handle("POST "+RouteChannelMembers, nil, channels.AddMembers)
		r.handle("GET "+RouteChannelMemberCandidates, nil, channels.MemberCandidates)
		r.handle("DELETE "+RouteChannelMember, nil, channels.RemoveMember)
	}
}

// registerChannelCategoryRoutes: RF-17 channel categories. Registered only
// when wired, like the channel and DM routes. The listing shares the read
// budget; the four mutations carry their own budget inside the handler.
func (r *routeSet) registerChannelCategoryRoutes(categories *ChannelCategoryHandler) {
	if categories == nil {
		return
	}
	r.handle("GET "+RouteChannelCategories, r.list, categories.List)
	r.handle("POST "+RouteChannelCategories, nil, categories.Create)
	r.handle("PUT "+RouteChannelCategoriesOrder, nil, categories.Reorder)
	r.handle("PATCH "+RouteChannelCategory, nil, categories.Rename)
	r.handle("DELETE "+RouteChannelCategory, nil, categories.Delete)
}

// registerDMRoutes: direct and group conversations. Group rename and
// self-leave (issue #527) are group-only: the statements behind them require
// type = 'group', so a 1:1 conversation ID reaches nothing. Registration grants
// nothing on its own — participation is re-derived inside each write.
func (r *routeSet) registerDMRoutes(dms *DMHandler) {
	if dms == nil {
		return
	}
	r.handle("GET "+RouteDMCandidates, nil, dms.SearchCandidates)
	r.handle("POST "+RouteDMConversations, nil, dms.GetOrCreateDirect)
	r.handle("POST "+RouteDMGroupConversations, nil, dms.CreateGroup)
	// Adding participants (issue #398): same shared add-members budget as the
	// channel route, applied inside the handler.
	r.handle("POST "+RouteDMMembers, nil, dms.AddParticipants)
	r.handle("PATCH "+RouteDMConversation, nil, dms.RenameGroup)
	r.handle("DELETE "+RouteDMMembership, nil, dms.LeaveGroup)
	// Admin removal (issue #685), the counterpart to self-leave.
	r.handle("DELETE "+RouteDMParticipant, nil, dms.RemoveParticipant)
	r.handle("GET "+RouteDMMemberCandidates, nil, dms.ParticipantCandidates)
	// Group details (issue #441) and the 1:1 profile panel (issue #443) are
	// reads on the same resource, so they share the listing budget.
	r.handle("GET "+RouteDMDetails, r.list, dms.GroupDetails)
	r.handle("GET "+RouteDMProfile, r.list, dms.DirectProfile)
	// Call-participant identity resolution (issue #612), group-DM side.
	r.handle("POST "+RouteDMCallParticipants, nil, dms.GroupCallParticipants)
}

// registerMessageLifecycleRoutes: RF-13/RF-14 editing, history and soft
// deletion, plus issue #824 acknowledgements.
//
// Editing spends the same write budget as sending, and for a reason beyond
// symmetry: since RF-21, an edit whose body carries an unscanned link queues a
// Cloudflare submission. Without a limiter here, one authenticated client could
// drain the account's scan quota by editing one message in a loop. The limiter
// runs before the handler, so a request over the cap never reaches the
// classification and never queues a scan.
func (r *routeSet) registerMessageLifecycleRoutes(messages *MessageHandler) {
	r.handle("PATCH "+RouteMessage, r.post, messages.EditMessage)
	r.handle("DELETE "+RouteMessage, r.post, messages.DeleteMessage)
	// Issue #824. The POST spends the ordinary write budget: confirming receipt
	// is something a person legitimately does once per urgent message, and it
	// reaches no third party. The GET spends the single-fetch budget for the
	// same reason the single-message route does — a client reconciling after a
	// reconnect must not compete with scroll. The page-load batch spends the
	// list budget: it is one request for the screen.
	r.handle("POST "+RouteMessageAcknowledgement, r.post, messages.AcknowledgeMessage)
	r.handle("POST "+RouteMessageAcknowledgements, r.list, messages.GetMessageAcknowledgements)
	r.handle("GET "+RouteMessageAcknowledgement, r.getSingle, messages.GetMessageAcknowledgement)
	// Issue #825: the author stops their own reminders. A write, on the
	// ordinary write budget like the acknowledgement it undoes.
	r.handle("DELETE "+RouteMessagePersistentNotifications, r.post, messages.CancelPersistentNotifications)
	r.handle("GET "+RouteMessageEditHistory, r.list, messages.GetMessageEditHistory)
}

// registerLinkSafetyRoutes: RF-21 and issue #807.
func (r *routeSet) registerLinkSafetyRoutes(messages *MessageHandler) {
	// Issue #807: the derived link-preview thumbnail. A read the timeline makes
	// once per card, so the single-message budget is the right one.
	r.handle("GET "+RouteLinkPreviewImage, r.getSingle, messages.GetLinkPreviewImage)
	// RF-21 reconnect reconciliation. POST because it carries a batch of ids in
	// the body, but it is a read: the list budget is the right one, the same
	// budget the reference batch spends for the same reason.
	r.handle("POST "+RouteMessageLinkSafetyStatus, r.list, messages.GetMessageLinkSafetyStatus)
	// RF-21 "Verificar novamente" (issue #135). No middleware limiter here on
	// purpose: this route's budget must hold across replicas, so it is applied
	// inside the handler against the shared Valkey counter. An in-process
	// middleware would hand every pod its own full allowance at the one route
	// that reaches a paid third party. The durable per-URL cooldown in storage
	// is the second layer. Message-scoped because the message is the only thing
	// a client is allowed to name — there is deliberately no route that takes a
	// URL.
	r.handle("POST "+RouteMessageLinkSafetyReconcile, nil, messages.ReconcileMessageLinkSafety)
}

// registerWorkspacePolicyRoutes: administrative reads and writes carry the
// ordinary read/write budgets so the endpoints cannot be hammered, and
// authorization is enforced inside the handlers — registration grants nothing
// on its own. Edit window (RF-14), anti-spam (RF-19, issue #419) and the
// attachment size policy (RF-32, issue #458, enforced atomically in the UPDATE).
func (r *routeSet) registerWorkspacePolicyRoutes(messages *MessageHandler) {
	r.handle("PATCH "+RouteWorkspaceSettings, r.post, messages.UpdateWorkspaceEditWindow)
	r.handle("GET "+RouteWorkspaceAntiSpam, r.list, messages.GetWorkspaceAntiSpam)
	r.handle("PATCH "+RouteWorkspaceAntiSpam, r.post, messages.UpdateWorkspaceAntiSpam)
	r.handle("GET "+RouteWorkspaceUploadLimit, r.list, messages.GetWorkspaceUploadLimit)
	r.handle("PATCH "+RouteWorkspaceUploadLimit, r.post, messages.UpdateWorkspaceUploadLimit)
}

// registerFavoriteAndPinRoutes: RF-06 favorites are per-user private
// bookmarks whose list only ever returns the caller's own; writes share the
// post budget so favoriting cannot exceed the general write quota. RF-05 pins
// use current read access and a dedicated pin-action budget; lists use the
// read budget.
func (r *routeSet) registerFavoriteAndPinRoutes(messages *MessageHandler) {
	r.handle("POST "+RouteMessageFavorite, r.post, messages.FavoriteMessage)
	r.handle("DELETE "+RouteMessageFavorite, r.post, messages.UnfavoriteMessage)
	r.handle("GET "+RouteFavorites, r.list, messages.ListFavorites)
	r.handle("POST "+RouteChannelMessagePin, r.pinAction, messages.PinMessage)
	r.handle("DELETE "+RouteChannelMessagePin, r.pinAction, messages.UnpinMessage)
	r.handle("GET "+RouteChannelPins, r.list, messages.ListPins)
	r.handle("POST "+RouteDMMessagePin, r.pinAction, messages.PinDMMessage)
	r.handle("DELETE "+RouteDMMessagePin, r.pinAction, messages.UnpinDMMessage)
	r.handle("GET "+RouteDMPins, r.list, messages.ListDMPins)
}
