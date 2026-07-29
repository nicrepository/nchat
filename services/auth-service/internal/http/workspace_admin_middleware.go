package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/nicrepository/nchat/libs/go/platform/httputil"
	"github.com/nicrepository/nchat/services/auth-service/internal/domain"
)

// WorkspaceAdminResolver yields the workspace an authenticated caller
// administers. Satisfied by service.UserAdmin.
type WorkspaceAdminResolver interface {
	GetAdminWorkspaceID(ctx context.Context, userID string) (string, error)
}

// RequireWorkspaceAdmin is the browser-safe replacement for
// AdminBootstrapGuard on the workspace administration endpoints (issue #425).
//
// AdminBootstrapGuard authenticates a shared static token, which a browser
// cannot be given without handing every user operator rights. This guard
// instead derives everything from the session: the actor is the JWT subject
// that BearerAuth already validated, and the workspace is whatever the
// resolver says that actor administers.
//
// Nothing the client sends participates. There is no workspace path segment,
// query parameter, body field or header consulted here, so there is no value a
// caller could change to be judged against a workspace other than their own —
// the request carries no such value in the first place.
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
//   - caller administers no active workspace → 403. This is the answer for a
//     member, a guest, a suspended admin and an admin of an archived workspace
//     alike; the body never distinguishes them.
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

			workspaceID, err := resolver.GetAdminWorkspaceID(r.Context(), userID)
			if err != nil {
				if errors.Is(err, domain.ErrForbidden) || errors.Is(err, domain.ErrNotFound) {
					httputil.WriteError(w, http.StatusForbidden, httputil.ErrCodeForbidden, "forbidden")
					return
				}
				httputil.WriteError(w, http.StatusInternalServerError, httputil.ErrCodeInternal, "internal error")
				return
			}

			ctx := context.WithValue(r.Context(), ctxKeyWorkspaceID, workspaceID)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// getContextWorkspaceID returns the workspace RequireWorkspaceAdmin resolved,
// or "" when the guard did not run. Handlers treat "" as a refusal, never as
// "any workspace".
func getContextWorkspaceID(r *http.Request) string {
	id, _ := r.Context().Value(ctxKeyWorkspaceID).(string)
	return id
}
