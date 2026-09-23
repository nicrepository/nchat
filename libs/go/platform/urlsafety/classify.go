package urlsafety

import (
	"net/url"
	"strings"
)

// URL classification (issue #807, §22-23): what may be done with a URL *before*
// any external party or any outbound connection is involved.
//
// Reputation and SSRF policy are both decided later and by other code. This is
// the earlier, cheaper question: is this a URL whose full text may be handed to
// a public provider and fetched by the public preview fetcher at all? Two
// classes say no —
//
//   - SENSITIVE: the URL plausibly carries a secret. A magic login link, a
//     password reset, an OAuth callback, a signed download URL, an invitation.
//     Sending it to a third party or fetching it server-side would spend or leak
//     the secret. The pipeline terminalises it as unknown without asking anyone,
//     and never previews it;
//   - INTERNAL: the hostname names a private network by convention. A public
//     provider has nothing to say about it and the public fetcher must never be
//     a bridge into it. Same treatment.
//
// Both are heuristics, and they are deliberately conservative in one direction
// only: a false positive costs a link its automatic clearance (the reader still
// gets an interstitial), a false negative would cost a secret. Adding a marker
// here is cheap; removing one needs a reason.
//
// Nothing here logs, resolves or connects. A hostname that *resolves* to a
// private address is a different check, made by the worker with the fetcher's
// address policy, because it needs DNS.

// URLClass is the closed set of pre-provider classifications.
type URLClass string

const (
	// URLClassPublic is an ordinary URL: eligible for the provider and, once
	// cleared, for the preview fetcher.
	URLClassPublic URLClass = "public"
	// URLClassSensitive plausibly carries a secret. Never sent to a public
	// provider by default, never previewed.
	URLClassSensitive URLClass = "sensitive"
	// URLClassInternal names a private network by hostname convention. Never
	// sent to a public provider, never previewed.
	URLClassInternal URLClass = "internal"
)

// internalHostSuffixes are hostname endings that name private networks by
// convention or by RFC. ".local" is mDNS, ".internal" and ".home.arpa" are the
// two RFC-blessed private zones, and the rest are the spellings corporate DNS
// actually uses. The reserved documentation zones (.test, .example, .invalid)
// are deliberately absent: nothing private lives there either, and the shared
// test corpus names hosts in them.
var internalHostSuffixes = []string{
	".internal", ".local", ".localhost", ".lan", ".intranet", ".corp", ".home",
	".home.arpa", ".private", ".localdomain",
}

// sensitiveQueryKeys are parameter names that carry a credential or a one-time
// grant when they appear at all. Lower-case; compared case-insensitively.
var sensitiveQueryKeys = map[string]struct{}{
	"token": {}, "access_token": {}, "id_token": {}, "refresh_token": {}, "auth_token": {},
	"login_token": {}, "reset_token": {}, "session_token": {}, "jwt": {}, "otp": {},
	"api_key": {}, "apikey": {}, "secret": {}, "client_secret": {}, "password": {}, "passwd": {},
	"signature": {}, "sig": {}, "x-amz-signature": {}, "x-amz-credential": {},
	"x-goog-signature": {}, "x-goog-credential": {},
	"session": {}, "sessionid": {}, "sid": {}, "invite": {}, "invite_code": {}, "invitation": {},
	"magic": {}, "verification_code": {}, "confirmation_token": {}, "nonce": {},
}

// sensitivePathMarkers are path segments that belong to credential flows.
// Matched against whole, lower-cased segments — "/invite/abc" is sensitive,
// "/blog/why-we-invite-feedback" is not.
var sensitivePathMarkers = map[string]struct{}{
	"reset-password": {}, "reset_password": {}, "password-reset": {}, "password_reset": {},
	"magic-link": {}, "magiclink": {}, "magic_link": {}, "verify-email": {}, "verify_email": {},
	"email-verification": {}, "confirm-email": {}, "confirm_email": {}, "invite": {},
	"invitation": {}, "invitations": {}, "unsubscribe": {}, "oauth": {}, "oauth2": {}, "callback": {},
	"sso": {}, "saml": {},
}

// ClassifyURL classifies a canonical URL. A URL that does not parse is reported
// public: the callers that matter canonicalised it first, and a value that fails
// here is refused there.
func ClassifyURL(canonicalURL string) URLClass {
	parsed, err := url.Parse(canonicalURL)
	if err != nil {
		return URLClassPublic
	}
	if IsInternalHost(parsed.Hostname()) {
		return URLClassInternal
	}
	if hasSensitiveQuery(parsed.RawQuery) || hasSensitivePath(parsed.EscapedPath()) {
		return URLClassSensitive
	}
	return URLClassPublic
}

// IsInternalHost reports whether a hostname names a private network by
// convention. It is a spelling test, not a resolution.
func IsInternalHost(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	if host == "" || host == "localhost" || !strings.Contains(host, ".") {
		return true
	}
	for _, suffix := range internalHostSuffixes {
		if strings.HasSuffix(host, suffix) {
			return true
		}
	}
	return false
}

// hasSensitiveQuery reports whether any parameter *name* is a credential
// carrier. Names only: values are never inspected, so nothing here has to
// handle a secret.
func hasSensitiveQuery(rawQuery string) bool {
	if rawQuery == "" {
		return false
	}
	names := make(map[string]struct{}, 4)
	for _, pair := range strings.Split(rawQuery, "&") {
		name, _, _ := strings.Cut(pair, "=")
		if unescaped, err := url.QueryUnescape(name); err == nil {
			name = unescaped
		}
		name = strings.ToLower(name)
		if _, sensitive := sensitiveQueryKeys[name]; sensitive {
			return true
		}
		names[name] = struct{}{}
	}
	// An OAuth authorization response is `code` *and* `state` together. `code`
	// alone is too common a name (coupons, country codes) to condemn a URL by.
	_, hasCode := names["code"]
	_, hasState := names["state"]
	return hasCode && hasState
}

// hasSensitivePath reports whether any whole path segment is a credential-flow
// marker.
func hasSensitivePath(escapedPath string) bool {
	for _, segment := range strings.Split(escapedPath, "/") {
		if _, sensitive := sensitivePathMarkers[strings.ToLower(segment)]; sensitive {
			return true
		}
	}
	return false
}
