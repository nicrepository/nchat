package httpapi

import (
	"net/http"
	"time"
)

const (
	// sessionCookieName is prefixed with __Host- so a compliant browser
	// enforces the rest of the policy for us: the cookie is rejected unless it
	// is Secure, has Path=/ and carries no Domain attribute. That last one is
	// the point — a __Host- cookie cannot be set by, or shared with, a sibling
	// subdomain, so nothing served from the chat host can plant or read the
	// administrative session.
	//
	// Path=/ is a consequence of the prefix, not a widening: this origin serves
	// the console and its API and nothing else.
	sessionCookieName = "__Host-nchat_admin_session"

	csrfHeaderName = "X-NChat-Admin-CSRF"
)

// newSessionCookie builds the administrative session cookie.
//
// HttpOnly: the value is never readable from JavaScript, so an XSS foothold on
// the console cannot exfiltrate the administrative credential the way it could
// a token in Web Storage.
//
// SameSite=Strict: the browser withholds the cookie on every cross-site
// request, including top-level navigations. This is the primary CSRF defence;
// the double-submit token in csrf.go is the second layer, not the first.
//
// Secure is unconditional, exactly as it is for auth-service's refresh cookie.
// Every environment this console is reachable from terminates TLS, and a knob
// to turn it off would be a knob to ship an administrative credential over
// plaintext.
//
// MaxAge tracks the idle window, so a tab left open past it stops presenting a
// credential the server would refuse anyway.
func newSessionCookie(value string, idleTTL time.Duration) *http.Cookie {
	return &http.Cookie{
		Name:     sessionCookieName,
		Value:    value,
		Path:     "/",
		MaxAge:   int(idleTTL.Seconds()),
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
	}
}

// clearSessionCookie expires the cookie in the browser. It is written on every
// logout, so a browser holding a dead credential stops sending it.
func clearSessionCookie() *http.Cookie {
	return &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
	}
}
