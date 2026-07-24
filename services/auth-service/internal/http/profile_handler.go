package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/nicrepository/nchat/libs/go/platform/httputil"
	"github.com/nicrepository/nchat/services/auth-service/internal/domain"
)

// SelfProfileReader is the service surface the me-profile endpoint depends on.
type SelfProfileReader interface {
	GetProfile(ctx context.Context, userID string) (domain.SelfProfile, error)
}

// selfProfileJSON is the minimal own-profile shape (wrapped by httputil.WriteJSON
// in a top-level {"data": ...} envelope). No e-mail, status, auth source or other
// PII is exposed. avatar_url is omitted when unset.
type selfProfileJSON struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	AvatarURL   string `json:"avatar_url,omitempty"`
}

// GetMyProfile handles GET /auth/me. Identity is the session's; there is no
// lookup by any client-supplied id. Responses are marked no-store so a cached
// avatar reference can never outlive a removal.
func GetMyProfile(svc SelfProfileReader) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if svc == nil {
			httputil.WriteError(w, http.StatusServiceUnavailable, "service_unavailable", "profile endpoint disabled")
			return
		}
		userID, ok := r.Context().Value(ctxKeyUserID).(string)
		if !ok || userID == "" {
			httputil.WriteError(w, http.StatusUnauthorized, httputil.ErrCodeUnauthorized, "unauthorized")
			return
		}

		profile, err := svc.GetProfile(r.Context(), userID)
		if err != nil {
			if errors.Is(err, domain.ErrNotFound) {
				httputil.WriteError(w, http.StatusNotFound, httputil.ErrCodeNotFound, "not found")
				return
			}
			httputil.WriteError(w, http.StatusInternalServerError, httputil.ErrCodeInternal, "internal error")
			return
		}

		w.Header().Set("Cache-Control", "no-store")
		httputil.WriteJSON(w, http.StatusOK, selfProfileJSON{
			ID:          profile.ID,
			DisplayName: profile.DisplayName,
			AvatarURL:   profile.AvatarURL,
		})
	})
}
