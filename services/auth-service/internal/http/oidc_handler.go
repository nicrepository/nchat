package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/nicrepository/nchat/libs/go/platform/httputil"
	"github.com/nicrepository/nchat/services/auth-service/internal/domain"
	"github.com/nicrepository/nchat/services/auth-service/internal/service"
)

const (
	errCodeOIDCDisabled        = "oidc_disabled"
	errCodeOIDCUnavailable     = "oidc_unavailable"
	errCodeOIDCInvalidCallback = "invalid_oidc_callback"
	errCodeOIDCForbidden       = "oidc_forbidden"
	errCodeOIDCLinkRequired    = "account_link_required"
)

type oidcExchangeRequest struct {
	Code string `json:"code"`
}

func OIDCLogin(oidc service.OIDCManager) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if oidc == nil {
			writeOIDCError(w, domain.ErrOIDCDisabled)
			return
		}
		location, err := oidc.Login(r.Context())
		if err != nil {
			writeOIDCError(w, err)
			return
		}
		http.Redirect(w, r, location, http.StatusFound)
	})
}

func OIDCCallback(oidc service.OIDCManager, trustedProxyCIDRs []*net.IPNet) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if oidc == nil {
			writeOIDCError(w, domain.ErrOIDCDisabled)
			return
		}
		location, err := oidc.Callback(r.Context(), service.OIDCCallbackInput{
			Code:       r.URL.Query().Get("code"),
			State:      r.URL.Query().Get("state"),
			DeviceName: "NIC Chat Web SSO",
			Platform:   "web",
			IPAddress:  httputil.ClientIP(r, trustedProxyCIDRs),
			UserAgent:  r.UserAgent(),
		})
		if err != nil {
			writeOIDCError(w, err)
			return
		}
		if !redirectOIDCCallbackLocation(w, location) {
			writeOIDCError(w, domain.ErrOIDCMisconfigured)
			return
		}
	})
}

func OIDCExchange(oidc service.OIDCManager) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if oidc == nil {
			writeOIDCError(w, domain.ErrOIDCDisabled)
			return
		}
		var req oidcExchangeRequest
		if !decodeOIDCExchangeRequest(w, r, &req) {
			return
		}
		result, err := oidc.Exchange(r.Context(), req.Code)
		if err != nil {
			writeOIDCError(w, err)
			return
		}
		//nolint:gosec // This endpoint serializes NChat tokens only after consuming a valid one-time OIDC exchange code.
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

func redirectOIDCCallbackLocation(w http.ResponseWriter, location string) bool {
	safeLocation, ok := safeOIDCCallbackLocation(location)
	if !ok {
		return false
	}
	w.Header().Set("Location", safeLocation)
	w.WriteHeader(http.StatusFound)
	return true
}

func safeOIDCCallbackLocation(location string) (string, bool) {
	if strings.ContainsAny(location, "\r\n") {
		return "", false
	}
	if !strings.HasPrefix(location, "/") || strings.HasPrefix(location, "//") {
		return "", false
	}
	parsed, err := url.Parse(location)
	if err != nil {
		return "", false
	}
	if parsed.Scheme != "" || parsed.Host != "" || parsed.User != nil || parsed.Fragment != "" || parsed.Path != domain.OIDCFrontendCallbackPath {
		return "", false
	}
	return parsed.String(), true
}

func decodeOIDCExchangeRequest(w http.ResponseWriter, r *http.Request, req *oidcExchangeRequest) bool {
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

func writeOIDCError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, domain.ErrOIDCDisabled):
		httputil.WriteError(w, http.StatusNotFound, errCodeOIDCDisabled, "oidc disabled")
	case errors.Is(err, domain.ErrOIDCMisconfigured):
		httputil.WriteError(w, http.StatusServiceUnavailable, errCodeOIDCUnavailable, "oidc unavailable")
	case errors.Is(err, domain.ErrOIDCAccountConflict):
		httputil.WriteError(w, http.StatusConflict, errCodeOIDCLinkRequired, "account linking required")
	case errors.Is(err, domain.ErrOIDCDomainForbidden), errors.Is(err, domain.ErrOIDCProvisioningDisabled):
		httputil.WriteError(w, http.StatusForbidden, errCodeOIDCForbidden, "oidc login unavailable")
	case errors.Is(err, domain.ErrOIDCInvalidCallback), errors.Is(err, domain.ErrOIDCEmailUnverified), errors.Is(err, domain.ErrInvalidToken), errors.Is(err, domain.ErrInvalidCredentials):
		httputil.WriteError(w, http.StatusUnauthorized, errCodeOIDCInvalidCallback, "oidc login failed")
	default:
		httputil.WriteError(w, http.StatusInternalServerError, httputil.ErrCodeInternal, "internal error")
	}
}
