package httpapi

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/nicrepository/nchat/libs/go/platform/httputil"
)

const errCodeCSRF = "csrf_failed"

// CSRFValidator checks a submitted double-submit token against the session it
// claims to belong to. Satisfied by service.AdminSessionService.
type CSRFValidator interface {
	ValidateCSRF(sessionID string, provided string) bool
}

// RequireCSRF guards every state-changing request the session cookie can
// authorize.
//
// Three independent checks, all of which must pass:
//
//  1. The cookie itself is SameSite=Strict, so a cross-site request does not
//     carry it at all. That is the defence; the two below exist because
//     SameSite is a browser behaviour and this service does not get to assume
//     which browser is talking to it.
//  2. Origin (or Referer, when a browser omits Origin) must be one this
//     deployment recognises. A request with neither header is refused rather
//     than trusted: browsers send Origin on exactly the cross-origin requests
//     that matter here.
//  3. A token derived from the session must be echoed in a custom header. A
//     cross-site form or image cannot set a custom header, and a cross-site
//     script cannot read the token to copy it.
//
// Safe methods are not guarded — they change nothing — but they are also not
// exempt from authorization, which happens before this middleware runs.
func RequireCSRF(validator CSRFValidator, allowedOrigins []string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if isSafeMethod(r.Method) {
				next.ServeHTTP(w, r)
				return
			}
			if validator == nil {
				writeUnavailable(w)
				return
			}
			if !originAllowed(r, allowedOrigins) {
				httputil.WriteError(w, http.StatusForbidden, errCodeCSRF, "request origin rejected")
				return
			}
			admin, ok := AdminFromContext(r)
			if !ok {
				httputil.WriteError(w, http.StatusUnauthorized, httputil.ErrCodeUnauthorized, "unauthorized")
				return
			}
			if !validator.ValidateCSRF(admin.Session.ID, r.Header.Get(csrfHeaderName)) {
				httputil.WriteError(w, http.StatusForbidden, errCodeCSRF, "csrf token invalid")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func isSafeMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	default:
		return false
	}
}

// originAllowed compares the request's origin against the deployment's own.
//
// When no allowlist is configured the console and the API share a host, so the
// request's own Host header is the only acceptable origin. That keeps the
// same-origin deployment safe without asking an operator to restate its own
// hostname in configuration — and it still refuses a request that names a
// different origin.
func originAllowed(r *http.Request, allowedOrigins []string) bool {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		origin = originOf(r.Header.Get("Referer"))
	}
	if origin == "" {
		return false
	}
	for _, allowed := range allowedOrigins {
		if strings.EqualFold(origin, allowed) {
			return true
		}
	}
	return sameOriginAsRequest(r, origin)
}

func sameOriginAsRequest(r *http.Request, origin string) bool {
	if r.Host == "" {
		return false
	}
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Host == "" {
		return false
	}
	return strings.EqualFold(parsed.Host, r.Host)
}

func originOf(referer string) string {
	parsed, err := url.Parse(strings.TrimSpace(referer))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return ""
	}
	return parsed.Scheme + "://" + parsed.Host
}
