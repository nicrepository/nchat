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
	errCodeInvalidCredentials        = "invalid_credentials" //nolint:gosec // G101: error code string, not a credential value
	errCodePasswordExpired           = "password_expired"    //nolint:gosec // G101: error code string, not a credential value
	errCodeRequestTooLarge           = "request_too_large"
	maxAuthRequestBodyBytes    int64 = 4 * 1024

	refreshCookieName = "nchat_rt"
	refreshCookiePath = "/api/auth"
)

// refreshCookie builds an HttpOnly, Secure, SameSite=Strict cookie for the refresh token.
func refreshCookie(token string, maxAge int) *http.Cookie {
	return &http.Cookie{
		Name:     refreshCookieName,
		Value:    token,
		Path:     refreshCookiePath,
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
	}
}

// clearRefreshCookie returns a cookie directive that instructs the browser to delete nchat_rt.
func clearRefreshCookie() *http.Cookie {
	return &http.Cookie{
		Name:     refreshCookieName,
		Value:    "",
		Path:     refreshCookiePath,
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
	}
}

// readRefreshCookie extracts the refresh token value from the request cookie.
func readRefreshCookie(r *http.Request) (string, bool) {
	c, err := r.Cookie(refreshCookieName)
	if err != nil {
		return "", false
	}
	return c.Value, true
}

// tokenResponse is the JSON body returned by /auth/refresh.
// The refresh token is no longer included in the body; it is set as an HttpOnly cookie.
type tokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int    `json:"expires_in"`
}

func AuthRefresh(auth service.AuthSessionManager, refreshTTL int) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if auth == nil {
			httputil.WriteError(w, http.StatusServiceUnavailable, errCodeUnavailable, "auth token endpoints disabled")
			return
		}

		refreshToken, ok := readRefreshCookie(r)
		if !ok {
			httputil.WriteError(w, http.StatusUnauthorized, errCodeInvalidRefreshToken, "invalid refresh token")
			return
		}

		pair, err := auth.Refresh(r.Context(), refreshToken)
		if err != nil {
			writeAuthError(w, err)
			return
		}

		http.SetCookie(w, refreshCookie(pair.RefreshToken, refreshTTL))
		writeTokenResponse(w, http.StatusOK, tokenResponse{
			AccessToken: pair.AccessToken,
			TokenType:   pair.TokenType,
			ExpiresIn:   pair.ExpiresIn,
		})
	})
}

func AuthLogout(auth service.AuthSessionManager) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if auth == nil {
			httputil.WriteError(w, http.StatusServiceUnavailable, errCodeUnavailable, "auth token endpoints disabled")
			return
		}

		refreshToken, ok := readRefreshCookie(r)
		if !ok {
			http.SetCookie(w, clearRefreshCookie())
			w.WriteHeader(http.StatusNoContent)
			return
		}

		if err := auth.Logout(r.Context(), refreshToken); err != nil {
			if !errors.Is(err, domain.ErrInvalidRefreshToken) {
				writeAuthError(w, err)
				return
			}
		}
		http.SetCookie(w, clearRefreshCookie())
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
	//nolint:gosec // This endpoint must serialize the access token to the authenticated caller.
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

// loginResponse is the JSON body returned on a successful login or OIDC exchange.
// The refresh token is no longer included in the body; it is set as an HttpOnly cookie.
type loginResponse struct {
	AccessToken string            `json:"access_token"`
	TokenType   string            `json:"token_type"`
	ExpiresIn   int               `json:"expires_in"`
	User        loginUserResponse `json:"user"`
}

// AuthLogin returns an http.Handler for POST /auth/login.
func AuthLogin(login service.LoginManager, trustedProxyCIDRs []*net.IPNet, refreshTTL int) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if login == nil {
			httputil.WriteError(w, http.StatusServiceUnavailable, errCodeUnavailable, "auth login endpoint disabled")
			return
		}

		var req loginRequest
		if !decodeLoginRequest(w, r, &req) {
			return
		}

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

		//nolint:gosec // Access token returned to authenticated caller; refresh token set as HttpOnly cookie only.
		body, marshalErr := json.Marshal(loginResponse{
			AccessToken: result.AccessToken,
			TokenType:   result.TokenType,
			ExpiresIn:   result.ExpiresIn,
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
		http.SetCookie(w, refreshCookie(result.RefreshToken, refreshTTL))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(append(body, '\n')) // nosemgrep
	})
}

// decodeLoginRequest decodes the login JSON body with a 4 KiB cap and trailing-garbage rejection.
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

func writeDecodeError(w http.ResponseWriter, err error) {
	var maxBytesErr *http.MaxBytesError
	if errors.As(err, &maxBytesErr) {
		httputil.WriteError(w, http.StatusRequestEntityTooLarge, errCodeRequestTooLarge, "request body too large")
		return
	}
	httputil.WriteError(w, http.StatusBadRequest, httputil.ErrCodeBadRequest, "invalid JSON body")
}

// writeLoginError maps login service errors to HTTP responses.
//
// All credential-related errors (including lockout) map to a generic 401 to
// prevent enumeration. An expired password is the one 401 that says more, and
// it can: it is only reachable once the password has already been verified, so
// nobody learns it without holding the password. Withholding it would leave the
// owner retrying a correct password against a wall.
func writeLoginError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, domain.ErrPasswordExpired):
		httputil.WriteError(w, http.StatusUnauthorized, errCodePasswordExpired, "password expired")
	case errors.Is(err, domain.ErrInvalidCredentials):
		httputil.WriteError(w, http.StatusUnauthorized, errCodeInvalidCredentials, "invalid credentials")
	case errors.Is(err, domain.ErrInvalidInput):
		httputil.WriteError(w, http.StatusBadRequest, httputil.ErrCodeBadRequest, err.Error())
	default:
		httputil.WriteError(w, http.StatusInternalServerError, httputil.ErrCodeInternal, "internal error")
	}
}
