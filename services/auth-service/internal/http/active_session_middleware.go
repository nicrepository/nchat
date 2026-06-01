package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/nicrepository/nchat/libs/go/platform/httputil"
	"github.com/nicrepository/nchat/services/auth-service/internal/domain"
)

// ActiveSessionValidator validates the current JWT sid against the backing session store.
type ActiveSessionValidator interface {
	ValidateActiveSession(ctx context.Context, userID, sessionID string) error
}

// RequireActiveSession rejects revoked, expired, cross-user, or missing current sessions.
func RequireActiveSession(validator ActiveSessionValidator) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if validator == nil {
				httputil.WriteError(w, http.StatusServiceUnavailable, "service_unavailable", "active session validation disabled")
				return
			}

			userID, ok := r.Context().Value(ctxKeyUserID).(string)
			if !ok || userID == "" {
				httputil.WriteError(w, http.StatusUnauthorized, httputil.ErrCodeUnauthorized, "unauthorized")
				return
			}
			sessionID := GetContextSessionID(r)
			if sessionID == "" || !isValidUUID(sessionID) {
				httputil.WriteError(w, http.StatusUnauthorized, httputil.ErrCodeUnauthorized, "unauthorized")
				return
			}

			if err := validator.ValidateActiveSession(r.Context(), userID, sessionID); err != nil {
				if errors.Is(err, domain.ErrInvalidToken) || errors.Is(err, domain.ErrNotFound) {
					httputil.WriteError(w, http.StatusUnauthorized, httputil.ErrCodeUnauthorized, "unauthorized")
					return
				}
				httputil.WriteError(w, http.StatusInternalServerError, httputil.ErrCodeInternal, "internal error")
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
