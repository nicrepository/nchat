package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
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

func AdminCreateInvite(invites service.InviteManager) http.Handler {
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

		invite, err := invites.CreateInvite(r.Context(), domain.AdminInviteInput{Email: req.Email, DisplayName: req.DisplayName, FullName: req.FullName})
		if err != nil {
			writeCreateInviteError(w, err)
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

func writeCreateInviteError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, domain.ErrEmailOutboxUnavailable):
		httputil.WriteError(w, http.StatusServiceUnavailable, errCodeUnavailable, "admin invite endpoint unavailable: email handoff disabled")
	case errors.Is(err, domain.ErrDuplicateEmail), errors.Is(err, domain.ErrInviteAlreadyPending):
		httputil.WriteError(w, http.StatusConflict, errCodeConflict, "invite conflict")
	case errors.Is(err, domain.ErrInvalidInput):
		httputil.WriteError(w, http.StatusBadRequest, httputil.ErrCodeBadRequest, err.Error())
	default:
		httputil.WriteError(w, http.StatusInternalServerError, httputil.ErrCodeInternal, "internal error")
	}
}

func writeAcceptInviteError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, domain.ErrInvalidToken):
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
