package httpapi

import (
	"context"
	"net/http"
	"strings"

	"github.com/nicrepository/nchat/libs/go/platform/httputil"
	"github.com/nicrepository/nchat/services/auth-service/internal/service"
)

// ctxKeyUserID is the context key for the authenticated userID.
type ctxKey int

const (
	ctxKeyUserID    ctxKey = iota
	ctxKeySessionID        // carries AccessClaims.SessionID ("sid"); "" if absent
)

// GetContextSessionID returns the session ID injected by BearerAuth, or "" if absent.
func GetContextSessionID(r *http.Request) string {
	sid, _ := r.Context().Value(ctxKeySessionID).(string)
	return sid
}

// BearerAuth extracts and validates a Bearer JWT access token.
// On success it injects the userID into the request context and calls next.
// On failure it returns a generic auth error without leaking token details.
func BearerAuth(tokens *service.TokenManager) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if tokens == nil {
				httputil.WriteError(w, http.StatusServiceUnavailable, "service_unavailable", "auth disabled")
				return
			}

			hdr := r.Header.Get("Authorization")
			if !strings.HasPrefix(hdr, "Bearer ") {
				httputil.WriteError(w, http.StatusUnauthorized, httputil.ErrCodeUnauthorized, "unauthorized")
				return
			}

			raw := strings.TrimPrefix(hdr, "Bearer ")
			if raw == "" {
				httputil.WriteError(w, http.StatusUnauthorized, httputil.ErrCodeUnauthorized, "unauthorized")
				return
			}

			claims, err := tokens.ValidateAccessToken(raw)
			if err != nil {
				httputil.WriteError(w, http.StatusUnauthorized, httputil.ErrCodeUnauthorized, "unauthorized")
				return
			}

			ctx := context.WithValue(r.Context(), ctxKeyUserID, claims.Subject)
			ctx = context.WithValue(ctx, ctxKeySessionID, claims.SessionID)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
