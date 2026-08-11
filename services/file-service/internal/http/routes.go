package httpapi

// Routes are declared without the /api/files prefix on purpose: the gateway
// owns that prefix and strips it before forwarding (strip-files-prefix in
// infra/traefik/local/dynamic.yml), so the public paths are
// /api/files/channels/{id}/attachments and so on. Nothing here invents a
// version segment or a prefix the gateway does not already route.
const (
	RouteHealthz = "/healthz"
	RouteReadyz  = "/readyz"
	RouteVersion = "/version"

	// RF-30 upload. There is one route per destination kind rather than a kind
	// field in the body, which makes "exactly one destination" impossible to
	// violate and keeps the shape consistent with chat-service's
	// /channels/{channelID}/messages and /dm/{conversationID}/messages.
	RouteChannelAttachments = "/channels/{channelID}/attachments"
	RouteDMAttachments      = "/dm/{conversationID}/attachments"

	// Metadata and content are separate resources so a client can learn an
	// attachment's scan state without starting a transfer.
	RouteAttachment        = "/attachments/{attachmentID}"
	RouteAttachmentContent = "/attachments/{attachmentID}/content"

	// RF-31 inline preview. It is a third resource rather than a mode of the
	// content route, because it is a different object with a different type,
	// different headers and a different availability: an attachment can be
	// downloadable and have no preview, and a preview is never a substitute for
	// the file. A query parameter on /content would have made both of those
	// invisible to a router, a cache and a log.
	RouteAttachmentPreview = "/attachments/{attachmentID}/preview"

	// RF-10 Open Graph preview of an external link. It is not an attachment
	// resource and shares nothing with the routes above: no attachment exists,
	// nothing is stored, and the subject is a URL the caller supplies rather
	// than an object this service owns. It is a POST because it causes an
	// outbound request and because the URL belongs in a body, not in a query
	// string that access logs and intermediary caches would keep.
	RouteLinkPreview = "/link-preview"
)
