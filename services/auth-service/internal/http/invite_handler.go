package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/nicrepository/nchat/libs/go/platform/httputil"
	"github.com/nicrepository/nchat/services/auth-service/internal/domain"
	"github.com/nicrepository/nchat/services/auth-service/internal/service"
)

const errCodeInvalidInviteToken = "invalid_invite_token"

type createInviteRequest struct {
	Email       string `json:"email"`
	DisplayName string `json:"display_name"`
	FullName    string `json:"full_name"`
}

type inviteResponse struct {
	ID        string `json:"id"`
	Email     string `json:"email"`
	CreatedAt string `json:"created_at"`
}

type acceptInviteRequest struct {
	Token       string `json:"token"`
	DisplayName string `json:"display_name"`
	FullName    string `json:"full_name"`
	Password    string `json:"password"`
}

type acceptInviteResponse struct {
	ID          string  `json:"id"`
	Email       string  `json:"email"`
	DisplayName string  `json:"display_name"`
	FullName    *string `json:"full_name,omitempty"`
	CreatedAt   string  `json:"created_at"`
}

// AdminCreateInvite handles POST /auth/admin/invites.
//
// The workspace and the actor are read from the request context, where
// BearerAuth and RequireWorkspaceAdmin put them after deriving both from the
// session. createInviteRequest has no field for either, so a workspace_id or
// actor sent in the body is discarded at decode time and cannot reach the
// service — see TestAdminInvites_PayloadWorkspaceIsIgnored.
func AdminCreateInvite(invites service.InviteManager, retryAfterSeconds int) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if invites == nil {
			httputil.WriteError(w, http.StatusServiceUnavailable, errCodeUnavailable, "admin invite endpoint unavailable: database not configured")
			return
		}
		if !emailHandoffAvailable(invites) {
			httputil.WriteError(w, http.StatusServiceUnavailable, errCodeUnavailable, "admin invite endpoint unavailable: email handoff disabled")
			return
		}

		workspaceID := getContextWorkspaceID(r)
		actorID, _ := r.Context().Value(ctxKeyUserID).(string)
		if workspaceID == "" || actorID == "" {
			// Only reachable if the guard chain was removed. An invite with no
			// verified issuer would create a membership nobody authorized.
			httputil.WriteError(w, http.StatusForbidden, httputil.ErrCodeForbidden, "forbidden")
			return
		}

		var req createInviteRequest
		if !decodePasswordRequest(w, r, &req) {
			return
		}

		invite, err := invites.CreateInvite(r.Context(), domain.AdminInviteInput{
			WorkspaceID: workspaceID,
			ActorID:     actorID,
			Email:       req.Email,
			DisplayName: req.DisplayName,
			FullName:    req.FullName,
		})
		if err != nil {
			writeInviteFailure(w, err, retryAfterSeconds)
			return
		}
		writeInviteResponse(w, http.StatusCreated, inviteResponse{ID: invite.ID, Email: invite.Email, CreatedAt: invite.CreatedAt.Format(time.RFC3339)})
	})
}

func AuthAcceptInvite(invites service.InviteManager, limiters ...*targetAwareRateLimiter) http.Handler {
	targetLimiter := firstTargetLimiter(limiters)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if invites == nil {
			httputil.WriteError(w, http.StatusServiceUnavailable, errCodeUnavailable, "invite endpoint disabled")
			return
		}

		var req acceptInviteRequest
		if !decodePasswordRequest(w, r, &req) {
			return
		}
		if !targetLimiter.allowToken(req.Token) {
			writeRateLimited(w)
			return
		}

		result, err := invites.AcceptInvite(r.Context(), domain.AcceptInviteInput{Token: req.Token, DisplayName: req.DisplayName, FullName: req.FullName, Password: req.Password})
		if err != nil {
			writeAcceptInviteError(w, err)
			return
		}
		resp := acceptInviteResponse{ID: result.UserID, Email: result.Email, DisplayName: result.DisplayName, CreatedAt: result.CreatedAt.Format(time.RFC3339)}
		if result.FullName != "" {
			resp.FullName = &result.FullName
		}
		writeAcceptInviteResponse(w, http.StatusCreated, resp)
	})
}

// BootstrapCreateInvite handles POST /admin/invites, the initialization-only
// route behind AdminBootstrapGuard.
//
// It is a separate handler from AdminCreateInvite rather than the same one with
// a branch, because the two have incompatible notions of authority: this route
// has no session, so there is no workspace and no actor in the context to read.
// Sharing the handler is exactly what made this route answer 403 unconditionally
// — it demanded context values the bootstrap guard never injects.
//
// Everything that decides *what* the invite does is server-side: the workspace
// comes from configuration and the issuer is a system identity. The request
// body names only the invitee, and any other field is discarded at decode time.
func BootstrapCreateInvite(invites service.InviteManager, retryAfterSeconds int) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if invites == nil {
			httputil.WriteError(w, http.StatusServiceUnavailable, errCodeUnavailable, "admin invite endpoint unavailable: database not configured")
			return
		}
		if !emailHandoffAvailable(invites) {
			httputil.WriteError(w, http.StatusServiceUnavailable, errCodeUnavailable, "admin invite endpoint unavailable: email handoff disabled")
			return
		}

		var req createInviteRequest
		if !decodePasswordRequest(w, r, &req) {
			return
		}

		invite, err := invites.CreateBootstrapInvite(r.Context(), domain.BootstrapInviteInput{
			Email:       req.Email,
			DisplayName: req.DisplayName,
			FullName:    req.FullName,
		})
		if err != nil {
			writeInviteFailure(w, err, retryAfterSeconds)
			return
		}
		writeInviteResponse(w, http.StatusCreated, inviteResponse{ID: invite.ID, Email: invite.Email, CreatedAt: invite.CreatedAt.Format(time.RFC3339)})
	})
}

// writeInviteFailure maps an issuance error to a response, setting Retry-After
// only for the rate-limited case. Shared by both invite routes so the two
// cannot drift into reporting the same condition differently.
func writeInviteFailure(w http.ResponseWriter, err error, retryAfterSeconds int) {
	if errors.Is(err, domain.ErrInviteRateLimited) && retryAfterSeconds > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(retryAfterSeconds))
	}
	writeCreateInviteError(w, err)
}

func writeCreateInviteError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, domain.ErrEmailOutboxUnavailable):
		httputil.WriteError(w, http.StatusServiceUnavailable, errCodeUnavailable, "admin invite endpoint unavailable: email handoff disabled")
	case errors.Is(err, domain.ErrBootstrapUnavailable):
		// Not configured, or the workspace already has an administrator. Both
		// mean the same thing to the caller — use the authenticated endpoint —
		// and neither reveals which workspace or who administers it.
		httputil.WriteError(w, http.StatusServiceUnavailable, errCodeUnavailable, "bootstrap invite endpoint unavailable")
	case errors.Is(err, domain.ErrInviteRateLimited):
		// Retry-After is the window, matching what writeRateLimited does for
		// the token endpoints. The body says nothing about the budget or who
		// spent it.
		writeRateLimited(w)
	case errors.Is(err, domain.ErrDuplicateEmail), errors.Is(err, domain.ErrInviteAlreadyPending), errors.Is(err, domain.ErrAlreadyMember):
		// One code for all three. Distinguishing "already a member here" from
		// "invite already pending" would tell an admin whether an address is
		// present in a workspace they may not administer.
		httputil.WriteError(w, http.StatusConflict, errCodeConflict, "invite conflict")
	case errors.Is(err, domain.ErrInvalidInput):
		httputil.WriteError(w, http.StatusBadRequest, httputil.ErrCodeBadRequest, err.Error())
	default:
		httputil.WriteError(w, http.StatusInternalServerError, httputil.ErrCodeInternal, "internal error")
	}
}

func writeAcceptInviteError(w http.ResponseWriter, err error) {
	switch {
	// ErrInviteWorkspaceMissing is reported identically to ErrInvalidToken: an
	// invite predating the workspace binding is simply not acceptable, and
	// saying why would describe the database's migration state to an
	// unauthenticated caller.
	case errors.Is(err, domain.ErrInvalidToken), errors.Is(err, domain.ErrInviteWorkspaceMissing):
		httputil.WriteError(w, http.StatusUnauthorized, errCodeInvalidInviteToken, "invalid or expired invite")
	case errors.Is(err, domain.ErrDuplicateEmail):
		httputil.WriteError(w, http.StatusConflict, errCodeConflict, "email already registered")
	case errors.Is(err, domain.ErrInvalidInput), errors.Is(err, domain.ErrPasswordPolicy):
		httputil.WriteError(w, http.StatusBadRequest, httputil.ErrCodeBadRequest, err.Error())
	default:
		httputil.WriteError(w, http.StatusInternalServerError, httputil.ErrCodeInternal, "internal error")
	}
}

func writeInviteResponse(w http.ResponseWriter, status int, payload inviteResponse) {
	body, err := json.Marshal(payload)
	if err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, httputil.ErrCodeInternal, "internal error")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(append(body, '\n')) // nosemgrep
}

func writeAcceptInviteResponse(w http.ResponseWriter, status int, payload acceptInviteResponse) {
	body, err := json.Marshal(payload)
	if err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, httputil.ErrCodeInternal, "internal error")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(append(body, '\n')) // nosemgrep
}
