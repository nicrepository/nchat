package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/nicrepository/nchat/libs/go/platform/httputil"
	"github.com/nicrepository/nchat/services/auth-service/internal/domain"
	"github.com/nicrepository/nchat/services/auth-service/internal/service"
)

const errCodeInvalidRefreshToken = "invalid_refresh_token"

type refreshRequest struct {
	RefreshToken string `json:"refresh_token"`
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
}

func AuthRefresh(auth service.AuthSessionManager) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if auth == nil {
			httputil.WriteError(w, http.StatusServiceUnavailable, errCodeUnavailable, "auth token endpoints disabled")
			return
		}

		var req refreshRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httputil.WriteError(w, http.StatusBadRequest, httputil.ErrCodeBadRequest, "invalid JSON body")
			return
		}

		pair, err := auth.Refresh(r.Context(), req.RefreshToken)
		if err != nil {
			writeAuthError(w, err)
			return
		}

		writeTokenResponse(w, http.StatusOK, tokenResponse{
			AccessToken:  pair.AccessToken,
			RefreshToken: pair.RefreshToken,
			TokenType:    pair.TokenType,
			ExpiresIn:    pair.ExpiresIn,
		})
	})
}

func AuthLogout(auth service.AuthSessionManager) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if auth == nil {
			httputil.WriteError(w, http.StatusServiceUnavailable, errCodeUnavailable, "auth token endpoints disabled")
			return
		}

		var req refreshRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httputil.WriteError(w, http.StatusBadRequest, httputil.ErrCodeBadRequest, "invalid JSON body")
			return
		}

		if err := auth.Logout(r.Context(), req.RefreshToken); err != nil {
			writeAuthError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

func writeAuthError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, domain.ErrInvalidInput):
		httputil.WriteError(w, http.StatusBadRequest, httputil.ErrCodeBadRequest, err.Error())
	case errors.Is(err, domain.ErrInvalidRefreshToken):
		httputil.WriteError(w, http.StatusUnauthorized, errCodeInvalidRefreshToken, "invalid refresh token")
	default:
		httputil.WriteError(w, http.StatusInternalServerError, httputil.ErrCodeInternal, "internal error")
	}
}

func writeTokenResponse(w http.ResponseWriter, status int, payload tokenResponse) {
	//nolint:gosec // This endpoint must serialize tokens to the authenticated caller; token hashes are never included.
	body, err := json.Marshal(payload)
	if err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, httputil.ErrCodeInternal, "internal error")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(append(body, '\n'))
}
