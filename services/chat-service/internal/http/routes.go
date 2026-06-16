package httpapi

const (
	RouteHealthz         = "/healthz"
	RouteReadyz          = "/readyz"
	RouteVersion         = "/version"
	RouteSidebar         = "/api/chat/sidebar"
	RouteChannelMessages = "/api/chat/channels/{channelID}/messages"
	RouteDMMessages      = "/api/chat/dm/{conversationID}/messages"
)
