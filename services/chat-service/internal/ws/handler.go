package ws

import (
	"log/slog"
	"net/http"

	"github.com/nicrepository/nchat/libs/go/platform/httputil"
)

// ServeWS is the WebSocket upgrade handler stub.
//
// # Implementation gate
//
// chat-service has no authenticated request context yet. A middleware that
// extracts and verifies userID from a signed token and stores it in
// http.Request context must be implemented before this handler can safely
// upgrade connections.
//
// Until that middleware exists, ServeWS returns 501 Not Implemented for all
// requests. No WebSocket upgrade occurs in this version.
//
// # Integration checklist (when auth middleware is ready)
//
//  1. Extract userID from r.Context() (set by auth middleware).
//  2. Generate an opaque client ID (uuid).
//  3. Validate the Origin header against the configured allowed-origins list.
//  4. Call websocket.Accept with per-message deflate disabled for server-push path.
//  5. Create a Client with newClient(id, userID, workspaceID, wsSender{conn}).
//  6. Call hub.Register(client).
//  7. Defer hub.Unregister(client) and conn.Close.
//  8. Start read pump goroutine: reads ClientMessage frames, calls
//     hub.handleClientMessage for each (records presence activity and dispatches
//     subscribe/unsubscribe/ping).
//
// # Security invariants (enforced now; must remain enforced after upgrade)
//
//   - Access tokens must never appear in query strings.
//   - Refresh tokens must never be used for WebSocket auth.
//   - Origin must be validated against a server-side allowlist before upgrade.
//   - Unauthenticated connections must never be upgraded.
func ServeWS(_ *Hub, _ *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Reject tokens passed in query string before any other processing.
		// This must remain enforced even after the handler is fully implemented.
		q := r.URL.Query()
		if q.Get("token") != "" || q.Get("access_token") != "" || q.Get("authorization") != "" {
			httputil.WriteError(w, http.StatusBadRequest, httputil.ErrCodeBadRequest,
				"passing credentials in URL query string is not permitted")
			return
		}

		// Auth middleware integration is deferred.
		// Return 501 until the authenticated context pattern is available.
		httputil.WriteError(w, http.StatusNotImplemented, "not_implemented",
			"WebSocket endpoint not yet implemented: auth middleware integration deferred")
	})
}
