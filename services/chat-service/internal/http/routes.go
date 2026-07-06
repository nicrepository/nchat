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
	RouteWS                    = "/api/chat/ws"
)
