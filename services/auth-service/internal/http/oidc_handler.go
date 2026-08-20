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
	errCodeOIDCInvalidApp      = "invalid_oidc_app"
	errCodeOIDCAssurance       = "oidc_insufficient_assurance"
)

// oidcAppQueryParam names which NChat application a sign-in run belongs to.
//
// It is the only client input the login endpoint reads, and it is a label from
// a closed set — never a URL, never a path, never a hostname. The redirect URI
// it selects lives in server configuration, so there is no request value here
// that can be turned into a redirect target. A run that names no application is
// the chat application, which is what every pre-existing caller means.
const oidcAppQueryParam = "app"

type oidcExchangeRequest struct {
	Code string `json:"code"`
}

func OIDCLogin(oidc service.OIDCManager) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if oidc == nil {
			writeOIDCError(w, domain.ErrOIDCDisabled)
			return
		}
		app, ok := domain.ParseOIDCAppContext(r.URL.Query().Get(oidcAppQueryParam))
		if !ok {
			httputil.WriteError(w, http.StatusBadRequest, errCodeOIDCInvalidApp, "unknown application")
			return
		}
		location, err := oidc.Login(r.Context(), app)
		if err != nil {
			writeOIDCError(w, err)
			return
		}
		safeLocation, ok := safeOIDCProviderLocation(location)
		if !ok {
			writeOIDCError(w, domain.ErrOIDCMisconfigured)
			return
		}
		w.Header().Set("Location", safeLocation)
		w.WriteHeader(http.StatusFound)
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

func OIDCExchange(oidc service.OIDCManager, refreshTTL int) http.Handler {
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

// safeOIDCProviderLocation re-validates the authorization URL before the
// browser is sent to it.
//
// The URL is built by the provider from its own discovery document, whose
// endpoints are already pinned to the issuer origin, and the only client input
// on this path is a label from a closed set that never reaches a URL. This is
// therefore defence in depth rather than the primary control: it re-parses the
// value and hands the browser the reconstruction, so a header-splitting
// sequence or a non-absolute location cannot leave this handler even if a
// discovery document were ever served something strange.
func safeOIDCProviderLocation(location string) (string, bool) {
	if strings.ContainsAny(location, "\r\n") {
		return "", false
	}
	parsed, err := url.Parse(location)
	if err != nil || parsed.Host == "" || parsed.User != nil {
		return "", false
	}
	if parsed.Scheme != "https" && parsed.Scheme != "http" {
		return "", false
	}
	return parsed.String(), true
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
	case errors.Is(err, domain.ErrOIDCInsufficientAssurance):
		// Distinct from a plain refusal: retrying the same single-factor login
		// will not help, and the administrator needs to know that.
		httputil.WriteError(w, http.StatusForbidden, errCodeOIDCAssurance, "stronger authentication required")
	case errors.Is(err, domain.ErrOIDCDomainForbidden), errors.Is(err, domain.ErrOIDCProvisioningDisabled):
		httputil.WriteError(w, http.StatusForbidden, errCodeOIDCForbidden, "oidc login unavailable")
	case errors.Is(err, domain.ErrOIDCInvalidCallback), errors.Is(err, domain.ErrOIDCEmailUnverified), errors.Is(err, domain.ErrInvalidToken), errors.Is(err, domain.ErrInvalidCredentials):
		httputil.WriteError(w, http.StatusUnauthorized, errCodeOIDCInvalidCallback, "oidc login failed")
	default:
		httputil.WriteError(w, http.StatusInternalServerError, httputil.ErrCodeInternal, "internal error")
	}
}
