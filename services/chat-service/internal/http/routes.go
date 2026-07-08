package httpapi

const (
	RouteHealthz               = "/healthz"
	RouteReadyz                = "/readyz"
	RouteVersion               = "/version"
	RouteSidebar               = "/api/chat/sidebar"
	RouteChannelMessages       = "/api/chat/channels/{channelID}/messages"
	RouteChannelMessage        = "/api/chat/channels/{channelID}/messages/{messageID}"
	RouteChannelMentions       = "/api/chat/channels/{channelID}/mentions"
	RouteDMMessages            = "/api/chat/dm/{conversationID}/messages"
	RouteDMMessage             = "/api/chat/dm/{conversationID}/messages/{messageID}"
	RouteAllowedReactionEmojis = "/api/chat/reactions/allowed-emojis"
	RouteMessageFavorite       = "/api/chat/messages/{messageID}/favorite"
	RouteFavorites             = "/api/chat/favorites"
	RouteChannelMessagePin     = "/api/chat/channels/{channelID}/messages/{messageID}/pin"
	RouteChannelPins           = "/api/chat/channels/{channelID}/pins"
	RouteDMMessagePin          = "/api/chat/dm/{conversationID}/messages/{messageID}/pin"
	RouteDMPins                = "/api/chat/dm/{conversationID}/pins"
	RouteWS                    = "/api/chat/ws"
)
