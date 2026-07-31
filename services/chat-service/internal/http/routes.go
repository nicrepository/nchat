package httpapi

const (
	RouteHealthz              = "/healthz"
	RouteReadyz               = "/readyz"
	RouteVersion              = "/version"
	RouteSidebar              = "/api/chat/sidebar"
	RouteDMCandidates         = "/api/chat/dm-candidates"
	RouteDMConversations      = "/api/chat/dms"
	RouteDMGroupConversations = "/api/chat/dms/group"
	RouteChannels             = "/api/chat/channels"
	// RF-17 channel categories. No workspace segment: the workspace is resolved
	// server-side from the authenticated session, exactly like every other chat
	// route, so there is no path for a client to aim at another workspace.
	// The literal /order sits under the same prefix as {categoryID} but is only
	// served for PUT, which {categoryID} never is.
	RouteChannelCategories      = "/api/chat/channel-categories"
	RouteChannelCategoriesOrder = "/api/chat/channel-categories/order"
	RouteChannelCategory        = "/api/chat/channel-categories/{categoryID}"
	// RF/issue #435 channel-details panel. Read-only projection of one channel
	// the caller can already see; no workspace segment, for the same reason the
	// category routes have none.
	RouteChannelDetails        = "/api/chat/channels/{channelID}/details"
	RouteChannelMessages       = "/api/chat/channels/{channelID}/messages"
	RouteChannelMessageForward = "/api/chat/channels/{channelID}/messages/forward"
	RouteChannelMessage        = "/api/chat/channels/{channelID}/messages/{messageID}"
	RouteChannelReferences     = "/api/chat/channels/{channelID}/message-references"
	RouteChannelMentions       = "/api/chat/channels/{channelID}/mentions"
	RouteDMMessages            = "/api/chat/dm/{conversationID}/messages"
	RouteDMMessage             = "/api/chat/dm/{conversationID}/messages/{messageID}"
	RouteDMReferences          = "/api/chat/dm/{conversationID}/message-references"
	// Issue #441 group-details panel. It lives under the DM prefix because a
	// group *is* a chat.dm_conversations row (type='group'); a /channels/ route
	// would name the wrong aggregate, and a separate /groups/ prefix would
	// invent a resource the domain does not have.
	RouteDMDetails = "/api/chat/dm/{conversationID}/details"
	// Issue #443 profile panel for a 1:1 DM. A separate route from /details on
	// purpose: what a direct conversation shows is one person's profile, not the
	// conversation's own metadata, and the two payloads share no field beyond
	// the conversation ID. Folding both into /details would make every group
	// field optional and invite a client to read `participants` out of a direct
	// conversation. There is no user ID in the path — the counterpart is
	// resolved server-side from the caller's membership.
	RouteDMProfile             = "/api/chat/dm/{conversationID}/profile"
	RouteAllowedReactionEmojis = "/api/chat/reactions/allowed-emojis"
	RouteMessageFavorite       = "/api/chat/messages/{messageID}/favorite"
	RouteFavorites             = "/api/chat/favorites"
	RouteChannelMessagePin     = "/api/chat/channels/{channelID}/messages/{messageID}/pin"
	RouteChannelPins           = "/api/chat/channels/{channelID}/pins"
	RouteDMMessagePin          = "/api/chat/dm/{conversationID}/messages/{messageID}/pin"
	RouteDMPins                = "/api/chat/dm/{conversationID}/pins"
	RouteMessage               = "/api/chat/messages/{messageID}"
	RouteMessageEditHistory    = "/api/chat/messages/{messageID}/history"
	RouteWorkspaceSettings     = "/api/v1/workspaces/{workspaceID}/settings"
	// RF-19 anti-spam policy (issue #419). It lives under /api/chat because that
	// is the only prefix the gateways forward to chat-service (Traefik local and
	// every k8s overlay route /api/chat, /api/auth, /api/admin, …, never
	// /api/v1), so the sibling settings route above is unreachable from a
	// browser. Unlike the other /api/chat routes this one names its workspace:
	// the handler never trusts that ID, it checks the caller administers exactly
	// that workspace and the UPDATE re-checks it atomically.
	RouteWorkspaceAntiSpam = "/api/chat/workspaces/{workspaceID}/anti-spam"
	RouteWS                = "/api/chat/ws"
)
