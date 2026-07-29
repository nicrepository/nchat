package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
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

// workspaceUserResponse is one row of GET /auth/admin/users.
//
// It mirrors domain.WorkspaceUser minus the sort key, and carries no
// email_verified_at, avatar, external subject or session data: the admin table
// renders none of them, and a response type that cannot express them is what
// keeps a later query change from leaking one.
type workspaceUserResponse struct {
	ID          string  `json:"id"`
	Email       string  `json:"email"`
	DisplayName string  `json:"display_name"`
	FullName    *string `json:"full_name,omitempty"`
	Status      string  `json:"status"`
	AuthSource  string  `json:"auth_source"`
	CreatedAt   string  `json:"created_at"`
}

// workspaceUsersPagination mirrors the shape the login-attempts listing
// already publishes, so the API has one pagination contract rather than two.
// has_more is redundant with next_cursor and is sent anyway: it lets a client
// branch on "is there more" without encoding the rule that an empty cursor
// means the end.
type workspaceUsersPagination struct {
	Limit      int     `json:"limit"`
	NextCursor *string `json:"next_cursor"`
	HasMore    bool    `json:"has_more"`
}

type workspaceUsersResponse struct {
	Data       []workspaceUserResponse  `json:"data"`
	Pagination workspaceUsersPagination `json:"pagination"`
}

// parseWorkspaceUsersLimit reads the page size.
//
// Out-of-range and non-numeric values are rejected rather than silently
// corrected, because a client asking for 5000 users has a bug that a quiet
// clamp would hide. The one exception is a value above the maximum, which is
// clamped to it — that matches the existing login-attempts listing, and a
// client asking for "as many as possible" is a reasonable thing to mean.
func parseWorkspaceUsersLimit(raw string) (int, error) {
	if raw == "" {
		return domain.WorkspaceUserPageDefaultLimit, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, errInvalidLimit
	}
	if n < domain.WorkspaceUserPageMinLimit {
		return 0, errInvalidLimit
	}
	if n > domain.WorkspaceUserPageMaxLimit {
		return domain.WorkspaceUserPageMaxLimit, nil
	}
	return n, nil
}

var errInvalidLimit = errors.New("invalid limit")

// AdminListWorkspaceUsers handles GET /auth/admin/users (issues #425, #433).
//
// It lists one page of the caller's own workspace. The workspace is read from
// the context, where RequireWorkspaceAdmin put it after deriving it from the
// session — this handler never parses one and there is none to parse.
//
// The listing is paginated because an unbounded one is a resource-exhaustion
// lever: a workspace with a large membership would otherwise let any admin
// force the service to materialise and serialise every row (CWE-400). At most
// limit+1 rows are ever read.
//
// An empty page is 200 with an empty array, which is what lets the client tell
// "no users" from "the request failed": every failure below has its own status,
// and none of them produces a body a client could mistake for a list.
func AdminListWorkspaceUsers(users service.WorkspaceUserLister) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if users == nil {
			httputil.WriteError(w, http.StatusServiceUnavailable, errCodeUnavailable, "admin endpoint unavailable: database not configured")
			return
		}

		workspaceID := getContextWorkspaceID(r)
		if workspaceID == "" {
			// Only reachable if the guard was removed from the chain. Refusing
			// is the safe answer: an unscoped listing would span workspaces.
			httputil.WriteError(w, http.StatusForbidden, httputil.ErrCodeForbidden, "forbidden")
			return
		}

		limit, err := parseWorkspaceUsersLimit(r.URL.Query().Get("limit"))
		if err != nil {
			httputil.WriteError(w, http.StatusBadRequest, httputil.ErrCodeBadRequest, "limit must be an integer between 1 and 100")
			return
		}

		found, nextCursor, err := users.ListWorkspaceUsers(r.Context(), workspaceID, limit, r.URL.Query().Get("cursor"))
		if err != nil {
			// A bad cursor is the client's error, but the message stays generic:
			// saying why would tell the caller whether a cursor belongs to some
			// other workspace.
			if errors.Is(err, domain.ErrInvalidInput) {
				httputil.WriteError(w, http.StatusBadRequest, httputil.ErrCodeBadRequest, "invalid cursor")
				return
			}
			httputil.WriteError(w, http.StatusInternalServerError, httputil.ErrCodeInternal, "internal error")
			return
		}

		data := make([]workspaceUserResponse, 0, len(found))
		for _, u := range found {
			item := workspaceUserResponse{
				ID:          u.ID,
				Email:       u.Email,
				DisplayName: u.DisplayName,
				Status:      u.Status,
				AuthSource:  u.AuthSource,
				CreatedAt:   u.CreatedAt.Format(time.RFC3339),
			}
			if u.FullName != "" {
				fullName := u.FullName
				item.FullName = &fullName
			}
			data = append(data, item)
		}

		var nextCursorPtr *string
		if nextCursor != "" {
			nextCursorPtr = &nextCursor
		}
		httputil.WriteJSON(w, http.StatusOK, workspaceUsersResponse{
			Data: data,
			Pagination: workspaceUsersPagination{
				Limit:      limit,
				NextCursor: nextCursorPtr,
				HasMore:    nextCursor != "",
			},
		})
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
