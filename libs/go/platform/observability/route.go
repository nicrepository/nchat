package observability

import (
	"net/http"
	"strings"
)

// UnmatchedRoute is the closed-cardinality value used whenever a request has no
// route template: an unrouted path, a method the route does not serve, or any
// other case where the router did not select a pattern.
//
// A raw request path is never used as a fallback. Route labels and span names
// must stay bounded by the number of routes a service declares, not by what a
// client can put in a URL.
const UnmatchedRoute = "unmatched"

// RouteTemplate returns the low-cardinality route template for a request.
//
// The value comes from net/http.ServeMux, which records the pattern it matched
// in Request.Pattern (Go 1.22+). That is the only trustworthy source: the raw
// path contains client-controlled segments such as attachment ids, and deriving
// a template from it — by regex, by UUID detection, or by counting segments —
// would be a second, drifting copy of the routing table.
//
// The pattern is stored as "[METHOD ][HOST]/path"; only the path part is the
// template, so "POST /channels/{channelID}/attachments" becomes
// "/channels/{channelID}/attachments".
//
// Callers must read it after the router has run. Before that, and for a request
// the router never matched, it reports UnmatchedRoute.
func RouteTemplate(r *http.Request) string {
	if r == nil {
		return UnmatchedRoute
	}
	pattern := r.Pattern
	if _, rest, hasMethod := strings.Cut(pattern, " "); hasMethod {
		pattern = rest
	}
	if separator := strings.Index(pattern, "/"); separator > 0 {
		// Strip a host prefix, e.g. "example.com/path".
		pattern = pattern[separator:]
	}
	if !strings.HasPrefix(pattern, "/") {
		return UnmatchedRoute
	}
	return pattern
}
