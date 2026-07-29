package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/nicrepository/nchat/libs/go/platform/httputil"
	"github.com/nicrepository/nchat/services/auth-service/internal/domain"
	"github.com/nicrepository/nchat/services/auth-service/internal/service"
)

const (
	errCodeConflict    = "conflict"
	errCodeUnavailable = "service_unavailable"
)

type createUserRequest struct {
	Email              string `json:"email"`
	DisplayName        string `json:"display_name"`
	FullName           string `json:"full_name"`
	InitialPassword    string `json:"initial_password"`
	MustChangePassword bool   `json:"must_change_password"`
}

type userResponse struct {
	ID              string  `json:"id"`
	Email           string  `json:"email"`
	DisplayName     string  `json:"display_name"`
	FullName        *string `json:"full_name,omitempty"`
	Status          string  `json:"status"`
	AuthSource      string  `json:"auth_source"`
	EmailVerifiedAt string  `json:"email_verified_at"`
	CreatedAt       string  `json:"created_at"`
}

// adminEndpointUnavailable is what the workspace administration routes serve
// when the service booted without a database, token manager or session store.
// It refuses rather than falling through to an unguarded handler.
func adminEndpointUnavailable() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		httputil.WriteError(w, http.StatusServiceUnavailable, errCodeUnavailable, "admin endpoint unavailable: database not configured")
	})
}

// AdminCreateUser handles POST /admin/users.
// Returns 503 if users service is nil (database not configured).
func AdminCreateUser(users service.UserCreator) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if users == nil {
			httputil.WriteError(w, http.StatusServiceUnavailable, errCodeUnavailable, "admin endpoint unavailable: database not configured")
			return
		}

		var req createUserRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httputil.WriteError(w, http.StatusBadRequest, httputil.ErrCodeBadRequest, "invalid JSON body")
			return
		}

		input := domain.CreateUserInput{
			Email:              req.Email,
			DisplayName:        req.DisplayName,
			FullName:           strings.TrimSpace(req.FullName),
			InitialPassword:    req.InitialPassword,
			MustChangePassword: req.MustChangePassword,
		}

		user, err := users.CreateUser(r.Context(), input)
		if err != nil {
			switch {
			case errors.Is(err, domain.ErrDuplicateEmail):
				httputil.WriteError(w, http.StatusConflict, errCodeConflict, "email already registered")
			case errors.Is(err, domain.ErrInvalidInput), errors.Is(err, domain.ErrPasswordPolicy):
				httputil.WriteError(w, http.StatusBadRequest, httputil.ErrCodeBadRequest, err.Error())
			default:
				httputil.WriteError(w, http.StatusInternalServerError, httputil.ErrCodeInternal, "internal error")
			}
			return
		}

		resp := userResponse{
			ID:              user.ID,
			Email:           user.Email,
			DisplayName:     user.DisplayName,
			Status:          user.Status,
			AuthSource:      user.AuthSource,
			EmailVerifiedAt: user.EmailVerifiedAt.Format(time.RFC3339),
			CreatedAt:       user.CreatedAt.Format(time.RFC3339),
		}
		if user.FullName != "" {
			resp.FullName = &user.FullName
		}

		httputil.WriteJSON(w, http.StatusCreated, resp)
	})
}

type updateUserStatusRequest struct {
	Status string `json:"status"`
}

// AdminUpdateUserStatus handles PATCH /admin/users/{id}/status.
// This endpoint is guarded by AdminBootstrapGuard and is NOT browser-callable.
// callerID is passed as "" because AdminBootstrapGuard provides no user identity;
// self-deactivation prevention requires a future JWT/RBAC admin guard.
func AdminUpdateUserStatus(users service.UserStatusManager) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if users == nil {
			httputil.WriteError(w, http.StatusServiceUnavailable, errCodeUnavailable, "admin endpoint unavailable: database not configured")
			return
		}

		id := r.PathValue("id")
		if id == "" {
			httputil.WriteError(w, http.StatusBadRequest, httputil.ErrCodeBadRequest, "missing user id")
			return
		}

		var req updateUserStatusRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httputil.WriteError(w, http.StatusBadRequest, httputil.ErrCodeBadRequest, "invalid JSON body")
			return
		}

		if req.Status != "active" && req.Status != "suspended" {
			httputil.WriteError(w, http.StatusUnprocessableEntity, "invalid_status", "status must be active or suspended")
			return
		}

		user, err := users.UpdateUserStatus(r.Context(), "", id, req.Status)
		if err != nil {
			switch {
			case errors.Is(err, domain.ErrNotFound):
				httputil.WriteError(w, http.StatusNotFound, httputil.ErrCodeNotFound, "user not found")
			case errors.Is(err, domain.ErrStatusTransitionNotAllowed):
				httputil.WriteError(w, http.StatusUnprocessableEntity, "invalid_transition", "status transition not allowed")
			case errors.Is(err, domain.ErrForbidden):
				httputil.WriteError(w, http.StatusForbidden, "forbidden", "forbidden")
			default:
				httputil.WriteError(w, http.StatusInternalServerError, httputil.ErrCodeInternal, "internal error")
			}
			return
		}

		resp := userResponse{
			ID:              user.ID,
			Email:           user.Email,
			DisplayName:     user.DisplayName,
			Status:          user.Status,
			AuthSource:      user.AuthSource,
			EmailVerifiedAt: user.EmailVerifiedAt.Format(time.RFC3339),
			CreatedAt:       user.CreatedAt.Format(time.RFC3339),
		}
		if user.FullName != "" {
			resp.FullName = &user.FullName
		}

		httputil.WriteJSON(w, http.StatusOK, resp)
	})
}
