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
	RouteChannelMessages        = "/api/chat/channels/{channelID}/messages"
	RouteChannelMessageForward  = "/api/chat/channels/{channelID}/messages/forward"
	RouteChannelMessage         = "/api/chat/channels/{channelID}/messages/{messageID}"
	RouteChannelReferences      = "/api/chat/channels/{channelID}/message-references"
	RouteChannelMentions        = "/api/chat/channels/{channelID}/mentions"
	RouteDMMessages             = "/api/chat/dm/{conversationID}/messages"
	RouteDMMessage              = "/api/chat/dm/{conversationID}/messages/{messageID}"
	RouteDMReferences           = "/api/chat/dm/{conversationID}/message-references"
	RouteAllowedReactionEmojis  = "/api/chat/reactions/allowed-emojis"
	RouteMessageFavorite        = "/api/chat/messages/{messageID}/favorite"
	RouteFavorites              = "/api/chat/favorites"
	RouteChannelMessagePin      = "/api/chat/channels/{channelID}/messages/{messageID}/pin"
	RouteChannelPins            = "/api/chat/channels/{channelID}/pins"
	RouteDMMessagePin           = "/api/chat/dm/{conversationID}/messages/{messageID}/pin"
	RouteDMPins                 = "/api/chat/dm/{conversationID}/pins"
	RouteMessage                = "/api/chat/messages/{messageID}"
	RouteMessageEditHistory     = "/api/chat/messages/{messageID}/history"
	RouteWorkspaceSettings      = "/api/v1/workspaces/{workspaceID}/settings"
	RouteWS                     = "/api/chat/ws"
)
