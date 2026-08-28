package httpapi

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/nicrepository/nchat/libs/go/platform/httputil"
	"github.com/nicrepository/nchat/services/auth-service/internal/domain"
)

// LoginAttemptsManager is the service interface for fetching login attempts.
type LoginAttemptsManager interface {
	GetMyAttempts(ctx context.Context, userID string, limit int, cursorStr string) ([]domain.LoginAttempt, string, error)
}

type loginAttemptResponse struct {
	ID            string `json:"id"`
	Email         string `json:"email"`
	IPAddress     string `json:"ip_address,omitempty"`
	UserAgent     string `json:"user_agent,omitempty"`
	FailureReason string `json:"failure_reason"`
	CreatedAt     string `json:"created_at"`
}

type loginAttemptsPagination struct {
	Limit      int     `json:"limit"`
	NextCursor *string `json:"next_cursor"`
}

type loginAttemptsResponse struct {
	Data       []loginAttemptResponse  `json:"data"`
	Pagination loginAttemptsPagination `json:"pagination"`
}

// GetMyLoginAttempts returns the authenticated user's failed login attempts.
func GetMyLoginAttempts(svc LoginAttemptsManager) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if svc == nil {
			httputil.WriteError(w, http.StatusServiceUnavailable, "service_unavailable", "login attempts endpoint disabled")
			return
		}

		userID, ok := r.Context().Value(ctxKeyUserID).(string)
		if !ok || userID == "" {
			httputil.WriteError(w, http.StatusUnauthorized, httputil.ErrCodeUnauthorized, "unauthorized")
			return
		}

		limit := 50
		if limitStr := r.URL.Query().Get("limit"); limitStr != "" {
			if n, err := strconv.Atoi(limitStr); err == nil && n > 0 {
				limit = n
			}
		}
		if limit > 100 {
			limit = 100
		}
		cursorStr := r.URL.Query().Get("cursor")

		attempts, nextCursor, err := svc.GetMyAttempts(r.Context(), userID, limit, cursorStr)
		if err != nil {
			switch {
			case errors.Is(err, domain.ErrInvalidInput):
				httputil.WriteError(w, http.StatusBadRequest, httputil.ErrCodeBadRequest, err.Error())
			default:
				httputil.WriteError(w, http.StatusInternalServerError, httputil.ErrCodeInternal, "internal error")
			}
			return
		}

		data := make([]loginAttemptResponse, 0, len(attempts))
		for _, a := range attempts {
			data = append(data, loginAttemptResponse{
				ID:            strconv.FormatInt(a.ID, 10),
				Email:         a.Email,
				IPAddress:     maskIPAddress(a.IPAddress),
				UserAgent:     sanitizeUserAgent(a.UserAgent),
				FailureReason: a.FailureReason,
				CreatedAt:     a.CreatedAt.Format(time.RFC3339),
			})
		}

		var nextCursorPtr *string
		if nextCursor != "" {
			nextCursorPtr = &nextCursor
		}

		httputil.WriteJSON(w, http.StatusOK, loginAttemptsResponse{
			Data: data,
			Pagination: loginAttemptsPagination{
				Limit:      limit,
				NextCursor: nextCursorPtr,
			},
		})
	})
}

// maskIPAddress masks IP addresses for privacy.
// IPv4: replace last two octets with *.*
// IPv6: replace all but first group with *
// Unparseable: return ""
func maskIPAddress(raw string) string {
	if raw == "" {
		return ""
	}
	ip := net.ParseIP(raw)
	if ip == nil {
		return ""
	}
	if ip.To4() != nil {
		parts := strings.Split(ip.String(), ".")
		if len(parts) == 4 {
			return parts[0] + "." + parts[1] + ".*.*"
		}
		return ""
	}
	parts := strings.Split(ip.String(), ":")
	if len(parts) > 0 && parts[0] == "" {
		return "::*"
	}
	for _, part := range parts {
		if part != "" {
			return part + ":*"
		}
	}
	return ""
}

// sanitizeUserAgent truncates to 200 chars and strips non-printable characters.
func sanitizeUserAgent(ua string) string {
	var sb strings.Builder
	count := 0
	for _, r := range ua {
		if !unicode.IsPrint(r) {
			continue
		}
		if count == 200 {
			break
		}
		sb.WriteRune(r)
		count++
	}
	return sb.String()
}
