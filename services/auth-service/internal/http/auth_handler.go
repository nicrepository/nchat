package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"

	"github.com/nicrepository/nchat/libs/go/platform/httputil"
	"github.com/nicrepository/nchat/services/auth-service/internal/domain"
	"github.com/nicrepository/nchat/services/auth-service/internal/service"
)

const (
	errCodeInvalidRefreshToken       = "invalid_refresh_token"
	errCodeInvalidCredentials        = "invalid_credentials" //nolint:gosec // G101: this is an error code string, not a credential value
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
	_, _ = w.Write(append(body, '\n')) // nosemgrep
}

// loginRequest is the JSON body for POST /auth/login.
type loginRequest struct {
	Email             string `json:"email"`
	Password          string `json:"password"`
	DeviceFingerprint string `json:"device_fingerprint"`
	DeviceName        string `json:"device_name"`
	Platform          string `json:"platform"`
}

// loginUserResponse is the safe user representation in the login response.
type loginUserResponse struct {
	ID                 string `json:"id"`
	Email              string `json:"email"`
	DisplayName        string `json:"display_name"`
	MustChangePassword bool   `json:"must_change_password"`
}

// loginResponse is the JSON body returned on a successful login.
type loginResponse struct {
	AccessToken  string            `json:"access_token"`
	RefreshToken string            `json:"refresh_token"`
	TokenType    string            `json:"token_type"`
	ExpiresIn    int               `json:"expires_in"`
	User         loginUserResponse `json:"user"`
}

// AuthLogin returns an http.Handler for POST /auth/login.
// It returns 503 when login service is nil (database not configured),
// 401 for invalid credentials (including lockout), and 200 with tokens on success.
func AuthLogin(login service.LoginManager, trustedProxyCIDRs []*net.IPNet) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if login == nil {
			httputil.WriteError(w, http.StatusServiceUnavailable, errCodeUnavailable, "auth login endpoint disabled")
			return
		}

		var req loginRequest
		if !decodeLoginRequest(w, r, &req) {
			return
		}

		// Extract real client IP using trusted-proxy-aware logic.
		ipAddress := httputil.ClientIP(r, trustedProxyCIDRs)

		result, err := login.Login(r.Context(), domain.LoginInput{
			Email:             req.Email,
			Password:          req.Password,
			DeviceFingerprint: req.DeviceFingerprint,
			DeviceName:        req.DeviceName,
			Platform:          req.Platform,
			IPAddress:         ipAddress,
			UserAgent:         r.UserAgent(),
		})
		if err != nil {
			writeLoginError(w, err)
			return
		}

		//nolint:gosec // JSON-serialised tokens returned only to the authenticated caller; no hashes or raw secrets included.
		body, marshalErr := json.Marshal(loginResponse{
			AccessToken:  result.AccessToken,
			RefreshToken: result.RefreshToken,
			TokenType:    result.TokenType,
			ExpiresIn:    result.ExpiresIn,
			User: loginUserResponse{
				ID:                 result.User.ID,
				Email:              result.User.Email,
				DisplayName:        result.User.DisplayName,
				MustChangePassword: result.User.MustChangePassword,
			},
		})
		if marshalErr != nil {
			httputil.WriteError(w, http.StatusInternalServerError, httputil.ErrCodeInternal, "internal error")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(append(body, '\n')) // nosemgrep
	})
}

// decodeLoginRequest decodes the login JSON body with the same 4 KiB cap and
// trailing-garbage rejection used by decodeRefreshRequest.
func decodeLoginRequest(w http.ResponseWriter, r *http.Request, req *loginRequest) bool {
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

// writeLoginError maps login service errors to HTTP responses.
// All credential-related errors (including lockout) map to a generic 401 to prevent enumeration.
func writeLoginError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, domain.ErrInvalidCredentials):
		httputil.WriteError(w, http.StatusUnauthorized, errCodeInvalidCredentials, "invalid credentials")
	case errors.Is(err, domain.ErrInvalidInput):
		httputil.WriteError(w, http.StatusBadRequest, httputil.ErrCodeBadRequest, err.Error())
	default:
		httputil.WriteError(w, http.StatusInternalServerError, httputil.ErrCodeInternal, "internal error")
	}
}
