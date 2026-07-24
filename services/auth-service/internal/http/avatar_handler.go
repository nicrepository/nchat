package httpapi

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"

	"github.com/nicrepository/nchat/libs/go/platform/httputil"
	"github.com/nicrepository/nchat/services/auth-service/internal/domain"
	"github.com/nicrepository/nchat/services/auth-service/internal/service"
)

// avatarMultipartOverhead is the slack added on top of the raw image cap to
// cover multipart framing (boundaries, headers). It keeps the body limit close
// to the image limit without rejecting a legitimately-sized image for its
// envelope.
const avatarMultipartOverhead = 8 << 10 // 8 KiB

// avatarFormField is the single accepted multipart field name.
const avatarFormField = "avatar"

// AvatarManager is the service surface the me-avatar endpoints depend on.
type AvatarManager interface {
	Upload(ctx context.Context, userID string, r io.Reader) (string, error)
	Remove(ctx context.Context, userID string) error
}

// AvatarReader serves stored avatar bytes by opaque object name.
type AvatarReader interface {
	Open(name string) (io.ReadCloser, error)
}

type avatarResponse struct {
	Data struct {
		AvatarURL string `json:"avatar_url"`
	} `json:"data"`
}

// UploadMyAvatar handles POST /auth/me/avatar (multipart/form-data, field
// "avatar"). Identity comes from the session context, never the request body.
func UploadMyAvatar(svc AvatarManager) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if svc == nil {
			httputil.WriteError(w, http.StatusServiceUnavailable, "service_unavailable", "avatar endpoint disabled")
			return
		}
		userID, ok := r.Context().Value(ctxKeyUserID).(string)
		if !ok || userID == "" {
			httputil.WriteError(w, http.StatusUnauthorized, httputil.ErrCodeUnauthorized, "unauthorized")
			return
		}

		r.Body = http.MaxBytesReader(w, r.Body, domain.AvatarMaxUploadBytes+avatarMultipartOverhead)
		reader, err := r.MultipartReader()
		if err != nil {
			httputil.WriteError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "expected multipart/form-data")
			return
		}

		part, err := reader.NextPart()
		if err != nil {
			httputil.WriteError(w, http.StatusBadRequest, httputil.ErrCodeBadRequest, "missing avatar file")
			return
		}
		if part.FormName() != avatarFormField || part.FileName() == "" {
			httputil.WriteError(w, http.StatusBadRequest, httputil.ErrCodeBadRequest, "expected a single 'avatar' file field")
			return
		}

		// Buffer the single part (bounded) so a second part or extra field can be
		// rejected BEFORE anything is persisted — a two-file upload must not have
		// side effects.
		buf, err := io.ReadAll(io.LimitReader(part, domain.AvatarMaxUploadBytes+1))
		if err != nil {
			if tooLarge(err) {
				httputil.WriteError(w, http.StatusRequestEntityTooLarge, "payload_too_large", "avatar too large")
				return
			}
			httputil.WriteError(w, http.StatusBadRequest, httputil.ErrCodeBadRequest, "invalid upload")
			return
		}
		if len(buf) > domain.AvatarMaxUploadBytes {
			httputil.WriteError(w, http.StatusRequestEntityTooLarge, "payload_too_large", "avatar too large")
			return
		}
		if _, err := reader.NextPart(); !errors.Is(err, io.EOF) {
			httputil.WriteError(w, http.StatusBadRequest, httputil.ErrCodeBadRequest, "exactly one file is allowed")
			return
		}

		url, err := svc.Upload(r.Context(), userID, bytes.NewReader(buf))
		if err != nil {
			writeAvatarError(w, err)
			return
		}
		var resp avatarResponse
		resp.Data.AvatarURL = url
		httputil.WriteJSON(w, http.StatusOK, resp)
	})
}

// DeleteMyAvatar handles DELETE /auth/me/avatar. It is idempotent: removing an
// absent avatar still returns 204.
func DeleteMyAvatar(svc AvatarManager) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if svc == nil {
			httputil.WriteError(w, http.StatusServiceUnavailable, "service_unavailable", "avatar endpoint disabled")
			return
		}
		userID, ok := r.Context().Value(ctxKeyUserID).(string)
		if !ok || userID == "" {
			httputil.WriteError(w, http.StatusUnauthorized, httputil.ErrCodeUnauthorized, "unauthorized")
			return
		}
		if err := svc.Remove(r.Context(), userID); err != nil {
			writeAvatarError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

// ServeAvatar handles GET /auth/avatars/{name}. The object name is an opaque,
// unguessable capability, so the file is served without requiring the viewer to
// be the owner — that is what lets an <img> tag (which cannot send a Bearer
// token) show a counterpart's avatar. Path traversal is impossible: the store
// only opens bare "<hex>.png" names inside its directory.
func ServeAvatar(reader AvatarReader) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if reader == nil {
			httputil.WriteError(w, http.StatusServiceUnavailable, "service_unavailable", "avatar serving disabled")
			return
		}
		name := r.PathValue("name")
		body, err := reader.Open(name)
		if err != nil {
			httputil.WriteError(w, http.StatusNotFound, httputil.ErrCodeNotFound, "avatar not found")
			return
		}
		defer func() { _ = body.Close() }()

		w.Header().Set("Content-Type", domain.AvatarContentType)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Disposition", "inline")
		// The name is content-addressed and never reused (a replacement gets a
		// fresh id), so the object is safe to cache aggressively.
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		w.WriteHeader(http.StatusOK)
		_, _ = io.Copy(w, body)
	})
}

func writeAvatarError(w http.ResponseWriter, err error) {
	switch {
	case service.IsAvatarTooLarge(err):
		httputil.WriteError(w, http.StatusRequestEntityTooLarge, "payload_too_large", "avatar too large")
	case service.IsAvatarUnsupported(err):
		httputil.WriteError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "unsupported image")
	case errors.Is(err, domain.ErrNotFound):
		// The session was valid but the user is no longer active/updatable.
		httputil.WriteError(w, http.StatusForbidden, httputil.ErrCodeForbidden, "forbidden")
	default:
		httputil.WriteError(w, http.StatusInternalServerError, httputil.ErrCodeInternal, "internal error")
	}
}

func tooLarge(err error) bool {
	var maxBytesErr *http.MaxBytesError
	return errors.As(err, &maxBytesErr)
}
