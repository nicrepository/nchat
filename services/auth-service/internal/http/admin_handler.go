package httpapi

import (
	"net/http"

	"github.com/nicrepository/nchat/libs/go/platform/httputil"
)

const (
	errCodeConflict    = "conflict"
	errCodeUnavailable = "service_unavailable"
	// Distinct from errCodeConflict so a client can tell "resend with a
	// workspace selected" from "this request will never succeed".
	errCodeWorkspaceSelectionRequired = "workspace_selection_required"
)

// adminEndpointUnavailable is what the workspace administration routes serve
// when the service booted without a database, token manager or session store.
// It refuses rather than falling through to an unguarded handler.
func adminEndpointUnavailable() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		httputil.WriteError(w, http.StatusServiceUnavailable, errCodeUnavailable, "admin endpoint unavailable: database not configured")
	})
}
