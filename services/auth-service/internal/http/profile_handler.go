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
	UpdateProfileFields(ctx context.Context, userID string, jobTitle, bio, timezone, customStatus *string) (domain.SelfProfile, error)
}

// selfProfileJSON is the minimal own-profile shape (wrapped by httputil.WriteJSON
// in a top-level {"data": ...} envelope). No e-mail, status, auth source or other
// PII is exposed. avatar_url, job_title, bio, timezone and custom_status are
// omitted when unset.
type selfProfileJSON struct {
	ID           string `json:"id"`
	DisplayName  string `json:"display_name"`
	AvatarURL    string `json:"avatar_url,omitempty"`
	JobTitle     string `json:"job_title,omitempty"`
	Bio          string `json:"bio,omitempty"`
	Timezone     string `json:"timezone,omitempty"`
	CustomStatus string `json:"custom_status,omitempty"`
}

// selfProfileJSONFrom builds the wire shape from a domain.SelfProfile so every
// handler below returns the identical field set.
func selfProfileJSONFrom(p domain.SelfProfile) selfProfileJSON {
	return selfProfileJSON{
		ID:           p.ID,
		DisplayName:  p.DisplayName,
		AvatarURL:    p.AvatarURL,
		JobTitle:     p.JobTitle,
		Bio:          p.Bio,
		Timezone:     p.Timezone,
		CustomStatus: p.CustomStatus,
	}
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
		httputil.WriteJSON(w, http.StatusOK, selfProfileJSONFrom(profile))
	})
}

// patchMyProfileRequest is the entire accepted body of PATCH /auth/me. It
// deliberately carries nothing beyond these fields: id, user_id, workspace_id,
// role, status, auth_source and email have no field to decode into, and
// DisallowUnknownFields below turns any of them showing up in the JSON into a
// rejected request instead of a silently ignored one.
//
// Every field is a pointer, and that is load-bearing, not a style choice: it
// is what lets the handler tell "the caller did not mention this field" (nil)
// apart from "the caller wants this field cleared" (a pointer to ""). Two
// independent screens share this one endpoint — one edits only display_name,
// the other only job_title/bio/timezone/custom_status — and
// neither sends the other's fields. A plain (non-pointer) string field would
// decode a JSON body that omits it to Go's zero value "", indistinguishable
// from the caller explicitly asking to clear it; that indistinguishability is
// exactly what used to make a display_name-only PATCH silently wipe the other
// fields. custom_status is the sole custom-status field for this MVP.
type patchMyProfileRequest struct {
	DisplayName  *string `json:"display_name"`
	JobTitle     *string `json:"job_title"`
	Bio          *string `json:"bio"`
	Timezone     *string `json:"timezone"`
	CustomStatus *string `json:"custom_status"`
}

// PatchMyProfile handles PATCH /auth/me. Identity is the session's, exactly
// like GetMyProfile — there is no user_id in the body, and none would be
// honored if present. The response echoes the profile as persisted, so the
// caller renders the confirmed value rather than the optimistic input.
//
// A request may provide display_name, any of job_title/bio/timezone/
// custom_status, or both groups at once — each group that is present is
// persisted by its own call (UpdateDisplayName / UpdateProfileFields), so a
// request touching only one group never reaches, let alone alters, the other.
// A request providing neither group is rejected rather than silently
// no-op-succeeding, since it could not have been a deliberate save from
// either screen.
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

		profileFieldsProvided := body.JobTitle != nil || body.Bio != nil || body.Timezone != nil ||
			body.CustomStatus != nil
		if body.DisplayName == nil && !profileFieldsProvided {
			httputil.WriteError(w, http.StatusBadRequest, httputil.ErrCodeBadRequest, "at least one field must be provided")
			return
		}

		var profile domain.SelfProfile
		var err error
		if body.DisplayName != nil {
			profile, err = svc.UpdateDisplayName(r.Context(), userID, *body.DisplayName)
			if err != nil {
				writePatchMyProfileError(w, err)
				return
			}
		}
		if profileFieldsProvided {
			profile, err = svc.UpdateProfileFields(r.Context(), userID, body.JobTitle, body.Bio, body.Timezone, body.CustomStatus)
			if err != nil {
				writePatchMyProfileError(w, err)
				return
			}
		}

		w.Header().Set("Cache-Control", "no-store")
		httputil.WriteJSON(w, http.StatusOK, selfProfileJSONFrom(profile))
	})
}

// writePatchMyProfileError maps a PATCH /auth/me service error to a response,
// shared by both the display_name and the job_title/bio/timezone/
// custom_status writes so the two calls in PatchMyProfile fail identically.
func writePatchMyProfileError(w http.ResponseWriter, err error) {
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
}
