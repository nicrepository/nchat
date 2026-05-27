package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/nicrepository/nchat/libs/go/platform/httputil"
	"github.com/nicrepository/nchat/services/auth-service/internal/domain"
	"github.com/nicrepository/nchat/services/auth-service/internal/service"
)

const (
	errCodeInvalidRefreshToken       = "invalid_refresh_token"
	errCodeRequestTooLarge           = "request_too_large"
	maxAuthRequestBodyBytes    int64 = 4 * 1024
)

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
		if !decodeRefreshRequest(w, r, &req) {
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
		if !decodeRefreshRequest(w, r, &req) {
			return
		}

		if err := auth.Logout(r.Context(), req.RefreshToken); err != nil {
			if errors.Is(err, domain.ErrInvalidRefreshToken) {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			writeAuthError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

func decodeRefreshRequest(w http.ResponseWriter, r *http.Request, req *refreshRequest) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxAuthRequestBodyBytes)
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(req); err != nil {
		writeDecodeError(w, err)
		return false
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeDecodeError(w, err)
		return false
	}
	return true
}

func writeDecodeError(w http.ResponseWriter, err error) {
	var maxBytesErr *http.MaxBytesError
	if errors.As(err, &maxBytesErr) {
		httputil.WriteError(w, http.StatusRequestEntityTooLarge, errCodeRequestTooLarge, "request body too large")
		return
	}
	httputil.WriteError(w, http.StatusBadRequest, httputil.ErrCodeBadRequest, "invalid JSON body")
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
