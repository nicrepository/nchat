package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/nicrepository/nchat/libs/go/platform/httputil"
	"github.com/nicrepository/nchat/services/auth-service/internal/domain"
)

// SelfProfileReader is the service surface the me-profile endpoint depends on.
type SelfProfileReader interface {
	GetProfile(ctx context.Context, userID string) (domain.SelfProfile, error)
}

// SelfProfileWriter is the service surface the me-profile PATCH endpoint
// depends on.
type SelfProfileWriter interface {
	UpdateDisplayName(ctx context.Context, userID, displayName string) (domain.SelfProfile, error)
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

// patchMyProfileRequest is the entire accepted body of PATCH /auth/me. It
// deliberately carries nothing beyond display_name: id, user_id, workspace_id,
// role, status, auth_source and email have no field to decode into, and
// DisallowUnknownFields below turns any of them showing up in the JSON into a
// rejected request instead of a silently ignored one.
type patchMyProfileRequest struct {
	DisplayName string `json:"display_name"`
}

// PatchMyProfile handles PATCH /auth/me. Identity is the session's, exactly
// like GetMyProfile — there is no user_id in the body, and none would be
// honored if present. The response echoes the profile as persisted, so the
// caller renders the confirmed value rather than the optimistic input.
func PatchMyProfile(svc SelfProfileWriter) http.Handler {
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

		r.Body = http.MaxBytesReader(w, r.Body, maxAuthRequestBodyBytes)
		var body patchMyProfileRequest
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&body); err != nil {
			writeDecodeError(w, err)
			return
		}
		// A second decode call that succeeds only on trailing whitespace means
		// the body held exactly one JSON value — never a smuggled second object.
		if err := decoder.Decode(&struct{}{}); err != io.EOF {
			writeDecodeError(w, err)
			return
		}

		profile, err := svc.UpdateDisplayName(r.Context(), userID, body.DisplayName)
		if err != nil {
			switch {
			case errors.Is(err, domain.ErrNotFound):
				// Session was valid but the user is no longer active/updatable —
				// same mapping writeAvatarError uses for its own write endpoints.
				httputil.WriteError(w, http.StatusForbidden, httputil.ErrCodeForbidden, "forbidden")
			case errors.Is(err, domain.ErrInvalidInput):
				httputil.WriteError(w, http.StatusBadRequest, httputil.ErrCodeBadRequest, err.Error())
			default:
				httputil.WriteError(w, http.StatusInternalServerError, httputil.ErrCodeInternal, "internal error")
			}
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
