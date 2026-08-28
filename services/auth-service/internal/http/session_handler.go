package httpapi

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/nicrepository/nchat/libs/go/platform/httputil"
	"github.com/nicrepository/nchat/services/auth-service/internal/domain"
)

var uuidRE = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func isValidUUID(s string) bool { return uuidRE.MatchString(s) }

// SessionManager is the service interface for session management endpoints.
type SessionManager interface {
	ListSessions(ctx context.Context, userID string, includeRevoked bool, limit int) ([]domain.SessionInfo, error)
	RevokeSession(ctx context.Context, sessionID, userID string) error
	RevokeAllSessionsExcept(ctx context.Context, userID, exceptSessionID string) error
	ValidateActiveSession(ctx context.Context, userID, sessionID string) error
}

type sessionResponse struct {
	ID                string  `json:"id"`
	DeviceID          *string `json:"device_id"`
	CreatedAt         string  `json:"created_at"`
	LastSeenAt        string  `json:"last_seen_at"`
	IdleExpiresAt     string  `json:"idle_expires_at"`
	AbsoluteExpiresAt *string `json:"absolute_expires_at"`
	RevokedAt         *string `json:"revoked_at"`
	IPAddress         string  `json:"ip_address,omitempty"`
	UserAgent         string  `json:"user_agent,omitempty"`
	Current           bool    `json:"current"`
}

type sessionsListResponse struct {
	Data       []sessionResponse       `json:"data"`
	Pagination loginAttemptsPagination `json:"pagination"`
}

// GetMySessions returns the authenticated user's sessions, newest first.
func GetMySessions(svc SessionManager) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if svc == nil {
			httputil.WriteError(w, http.StatusServiceUnavailable, "service_unavailable", "sessions endpoint disabled")
			return
		}

		userID, ok := r.Context().Value(ctxKeyUserID).(string)
		if !ok || userID == "" {
			httputil.WriteError(w, http.StatusUnauthorized, httputil.ErrCodeUnauthorized, "unauthorized")
			return
		}
		currentSessionID := GetContextSessionID(r)

		limit := 50
		if s := r.URL.Query().Get("limit"); s != "" {
			if n, err := strconv.Atoi(s); err == nil && n > 0 {
				limit = n
			}
		}
		if limit > 100 {
			limit = 100
		}

		includeRevoked := r.URL.Query().Get("include_revoked") == "true"

		sessions, err := svc.ListSessions(r.Context(), userID, includeRevoked, limit)
		if err != nil {
			httputil.WriteError(w, http.StatusInternalServerError, httputil.ErrCodeInternal, "internal error")
			return
		}

		data := make([]sessionResponse, 0, len(sessions))
		for _, s := range sessions {
			resp := sessionResponse{
				ID:            s.ID,
				DeviceID:      s.DeviceID,
				CreatedAt:     s.CreatedAt.Format(time.RFC3339),
				LastSeenAt:    s.LastSeenAt.Format(time.RFC3339),
				IdleExpiresAt: s.IdleExpiresAt.Format(time.RFC3339),
				IPAddress:     maskIPAddress(s.IPAddress),
				UserAgent:     sanitizeUserAgent(s.UserAgent),
				Current:       currentSessionID != "" && s.ID == currentSessionID,
			}
			if s.AbsoluteExpiresAt != nil {
				t := s.AbsoluteExpiresAt.Format(time.RFC3339)
				resp.AbsoluteExpiresAt = &t
			}
			if s.RevokedAt != nil {
				t := s.RevokedAt.Format(time.RFC3339)
				resp.RevokedAt = &t
			}
			data = append(data, resp)
		}

		httputil.WriteJSON(w, http.StatusOK, sessionsListResponse{
			Data:       data,
			Pagination: loginAttemptsPagination{Limit: limit},
		})
	})
}

// DeleteMySession revokes a single session owned by the authenticated user.
// Returns 204 on success, 404 for unknown or cross-user session, 400 for invalid session_id.
func DeleteMySession(svc SessionManager) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if svc == nil {
			httputil.WriteError(w, http.StatusServiceUnavailable, "service_unavailable", "sessions endpoint disabled")
			return
		}

		userID, ok := r.Context().Value(ctxKeyUserID).(string)
		if !ok || userID == "" {
			httputil.WriteError(w, http.StatusUnauthorized, httputil.ErrCodeUnauthorized, "unauthorized")
			return
		}

		sessionID := r.PathValue("session_id")
		if !isValidUUID(sessionID) {
			httputil.WriteError(w, http.StatusBadRequest, httputil.ErrCodeBadRequest, "invalid session_id")
			return
		}

		if err := svc.RevokeSession(r.Context(), sessionID, userID); err != nil {
			if errors.Is(err, domain.ErrNotFound) {
				httputil.WriteError(w, http.StatusNotFound, httputil.ErrCodeNotFound, "session not found")
				return
			}
			httputil.WriteError(w, http.StatusInternalServerError, httputil.ErrCodeInternal, "internal error")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

// DeleteAllMySessions revokes all sessions for the authenticated user except the current one.
// Returns 401 if the request context does not carry a current session ID.
func DeleteAllMySessions(svc SessionManager) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if svc == nil {
			httputil.WriteError(w, http.StatusServiceUnavailable, "service_unavailable", "sessions endpoint disabled")
			return
		}

		userID, ok := r.Context().Value(ctxKeyUserID).(string)
		if !ok || userID == "" {
			httputil.WriteError(w, http.StatusUnauthorized, httputil.ErrCodeUnauthorized, "unauthorized")
			return
		}

		currentSessionID := GetContextSessionID(r)
		if currentSessionID == "" {
			httputil.WriteError(w, http.StatusUnauthorized, httputil.ErrCodeUnauthorized, "unauthorized")
			return
		}

		if err := svc.RevokeAllSessionsExcept(r.Context(), userID, currentSessionID); err != nil {
			httputil.WriteError(w, http.StatusInternalServerError, httputil.ErrCodeInternal, "internal error")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}
