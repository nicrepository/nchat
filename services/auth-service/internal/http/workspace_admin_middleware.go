package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/nicrepository/nchat/libs/go/platform/httputil"
	"github.com/nicrepository/nchat/services/auth-service/internal/domain"
)

// headerWorkspaceSelector is how a caller who administers several workspaces
// says which one a request is for.
const headerWorkspaceSelector = "X-NChat-Workspace-Id"

// WorkspaceAdminResolver yields the workspace an authenticated caller
// administers. Satisfied by service.UserAdmin.
type WorkspaceAdminResolver interface {
	ResolveAdminWorkspaceID(ctx context.Context, userID, selector string) (string, error)
}

// RequireWorkspaceAdmin authorizes a session against a workspace membership and
// puts the resolved workspace in the request context.
//
// The actor is the JWT subject that BearerAuth already validated. The workspace
// is resolved from that actor's own memberships — never taken from the request
// as authority. A caller may send X-NChat-Workspace-Id to *narrow* the answer
// when they administer several workspaces, but the header is only ever a
// selector: the resolver checks it against the caller's memberships and refuses
// it exactly like a missing one when it does not match. A caller who
// administers a single workspace gets the same result whatever they send.
//
// Ordering requirement: it must sit after BearerAuth and RequireActiveSession.
// BearerAuth is what puts a validated user ID in the context, and
// RequireActiveSession is what rejects a token whose session was revoked; a
// guard placed before either would authorize against an identity that has not
// been proven.
//
// Status mapping:
//
//   - resolver not wired → 503. The endpoint is disabled rather than open.
//   - no user ID in context → 401. Reachable only if the middleware order above
//     is broken, and it fails closed when it is.
//   - malformed selector → 403, without a lookup. Same answer as a selector
//     naming a workspace the caller does not administer, so the two are
//     indistinguishable.
//   - caller administers no matching workspace → 403. This is the answer for a
//     member, a guest, a suspended admin, an admin of an archived workspace and
//     a selector for somebody else's workspace alike; the body never
//     distinguishes them.
//   - caller administers several and selected none → 409. The body names no
//     workspace and does not say how many there are.
//   - resolver error → 500 with no detail.
func RequireWorkspaceAdmin(resolver WorkspaceAdminResolver) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if resolver == nil {
				httputil.WriteError(w, http.StatusServiceUnavailable, errCodeUnavailable, "admin endpoint unavailable: database not configured")
				return
			}

			userID, ok := r.Context().Value(ctxKeyUserID).(string)
			if !ok || userID == "" || !isValidUUID(userID) {
				httputil.WriteError(w, http.StatusUnauthorized, httputil.ErrCodeUnauthorized, "unauthorized")
				return
			}

			selector := strings.TrimSpace(r.Header.Get(headerWorkspaceSelector))
			if selector != "" && !isValidUUID(selector) {
				httputil.WriteError(w, http.StatusForbidden, httputil.ErrCodeForbidden, "forbidden")
				return
			}

			workspaceID, err := resolver.ResolveAdminWorkspaceID(r.Context(), userID, selector)
			if err != nil {
				writeWorkspaceResolutionError(w, err)
				return
			}

			ctx := context.WithValue(r.Context(), ctxKeyWorkspaceID, workspaceID)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// writeWorkspaceResolutionError maps a resolution failure without ever naming a
// workspace: the selection-required body says only that a selection is needed,
// so it cannot be used to enumerate which workspaces the caller administers.
func writeWorkspaceResolutionError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, domain.ErrWorkspaceSelectionRequired):
		httputil.WriteError(w, http.StatusConflict, errCodeWorkspaceSelectionRequired, "workspace selection required")
	case errors.Is(err, domain.ErrForbidden), errors.Is(err, domain.ErrNotFound):
		httputil.WriteError(w, http.StatusForbidden, httputil.ErrCodeForbidden, "forbidden")
	default:
		httputil.WriteError(w, http.StatusInternalServerError, httputil.ErrCodeInternal, "internal error")
	}
}

// getContextWorkspaceID returns the workspace RequireWorkspaceAdmin resolved,
// or "" when the guard did not run. Handlers treat "" as a refusal, never as
// "any workspace".
func getContextWorkspaceID(r *http.Request) string {
	id, _ := r.Context().Value(ctxKeyWorkspaceID).(string)
	return id
}
