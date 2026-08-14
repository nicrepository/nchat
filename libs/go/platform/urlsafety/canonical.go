package urlsafety

import (
	"net/url"
	"strings"
)

// CanonicalizeURL turns a URL a user wrote into the exact string that is both
// submitted to the provider and used as the verdict cache key.
//
// # Why the whole URL and not the host
//
// RF-21 used to decide by hostname. That made every path on a reputable domain
// inherit one verdict, so a phishing page parked on a compromised subdirectory —
// or an open redirect carrying the payload in a query parameter — was cleared by
// the reputation of the domain hosting it. Path and query are therefore part of
// the identity here, and the transformations below are chosen so that two
// spellings of *the same resource* collapse into one key while two different
// resources never can.
//
// # What is normalised, and why each one is safe
//
//   - the scheme is lower-cased. Schemes are case-insensitive by RFC 3986 §3.1;
//   - the host is lower-cased and punycoded by NormalizeHost, because that is
//     the name that exists in DNS. Skipping it would let an IDN homograph be
//     submitted under a spelling the provider has never seen;
//   - a default port is dropped (:80 for http, :443 for https). RFC 3986 §6.2.3
//     makes those equivalent to the empty port;
//   - an empty path becomes "/", also §6.2.3: "https://example.com" and
//     "https://example.com/" are the same resource;
//   - the fragment is removed. A fragment is never sent to the origin server, so
//     it cannot change what the provider fetches — keeping it would only split
//     one resource into unbounded cache keys, which is a quota drain rather than
//     a security property.
//
// # What is deliberately left alone
//
// The path is taken as written (EscapedPath), and the query is taken as written
// (RawQuery): not sorted, not decoded, not re-encoded, not collapsed. Every one
// of those is a way for two URLs that the origin server treats differently to
// end up sharing a verdict, and the whole point of this function is that they
// must not. "/a/../b" is left as "/a/../b" for the same reason — resolving it
// assumes the server resolves it the same way, and some do not.
//
// Userinfo is refused outright rather than stripped. A URL carrying credentials
// must not be handed to a third-party scanner, and silently removing them would
// submit a different URL from the one that was written.
func CanonicalizeURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > maxURLLength {
		return "", ErrNotCheckable
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", ErrNotCheckable
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", ErrNotCheckable
	}
	if parsed.User != nil {
		return "", ErrNotCheckable
	}
	host, err := NormalizeHost(parsed.Hostname())
	if err != nil {
		return "", err
	}
	if port := parsed.Port(); port != "" && !isDefaultPort(scheme, port) {
		host += ":" + port
	}

	canonical := scheme + "://" + host
	path := parsed.EscapedPath()
	if path == "" {
		path = "/"
	}
	canonical += path
	if parsed.RawQuery != "" {
		canonical += "?" + parsed.RawQuery
	}
	if len(canonical) > maxURLLength {
		return "", ErrNotCheckable
	}
	return canonical, nil
}

// isDefaultPort reports the port that carries no information for a scheme.
func isDefaultPort(scheme, port string) bool {
	return (scheme == "http" && port == "80") || (scheme == "https" && port == "443")
}
