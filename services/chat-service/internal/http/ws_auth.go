package httpapi

import (
	"net/http"
	"strings"

	"github.com/nicrepository/nchat/libs/go/platform/httputil"
	"github.com/nicrepository/nchat/services/chat-service/internal/wsutil"
)

// WSTokenMiddleware extracts a Bearer token from the Sec-WebSocket-Protocol
// header for WebSocket upgrade requests and injects it as an Authorization
// header before the downstream BearerAuth middleware runs. The client also
// offers the fixed nchat.v1 protocol, which is the only value echoed.
//
// Browser WebSocket clients cannot set custom HTTP headers on the initial
// upgrade request. The accepted convention is to pass the access token as the
// WebSocket subprotocol:
//
//	new WebSocket(url, [accessToken, "nchat.v1"])
//
// The server validates the token server-side but does NOT echo it back in the
// response Sec-WebSocket-Protocol header, preventing JWT exposure in responses.
//
// Security notes:
//   - Only active for requests with Upgrade: websocket.
//   - Does not override an existing Authorization header.
//   - The token value is never logged by this middleware.
//   - Downstream BearerAuth validates the injected token before any WS upgrade.
//   - The JWT is never reflected in response headers.
func WSTokenMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			proto, ok := wsutil.SubprotocolHeader(r.Header)
			if ok && !wsutil.IsValidSubprotocolToken(proto) {
				httputil.WriteError(w, http.StatusBadRequest, httputil.ErrCodeBadRequest, "invalid websocket subprotocol")
				return
			}
			if ok && !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
				r2 := r.Clone(r.Context())
				r2.Header.Set("Authorization", "Bearer "+proto)
				r = r2
			}
		}
		next.ServeHTTP(w, r)
	})
}
