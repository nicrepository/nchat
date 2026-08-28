package urlsafety

import (
	"crypto/sha256"
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
	parsed, err := parseScannableURL(raw)
	if err != nil {
		return "", err
	}
	authority, err := canonicalAuthority(parsed)
	if err != nil {
		return "", err
	}
	canonical := authority + canonicalPathAndQuery(parsed)
	if len(canonical) > maxURLLength {
		return "", ErrNotCheckable
	}
	return canonical, nil
}

// parseScannableURL parses raw and refuses anything the provider cannot scan.
//
// Three refusals, each a security decision rather than a parsing one:
//
//   - a scheme other than http/https. The provider fetches pages; a javascript:
//     or data: URL is not one, and a mailto: has no page to fetch;
//   - userinfo. A URL carrying credentials must not be handed to a third-party
//     scanner, and silently stripping them would submit a different URL from the
//     one that was written;
//   - anything past the length bound, which also bounds the cache key, the
//     database column and the submission body.
func parseScannableURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > maxURLLength {
		return nil, ErrNotCheckable
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return nil, ErrNotCheckable
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, ErrNotCheckable
	}
	if parsed.User != nil {
		return nil, ErrNotCheckable
	}
	return parsed, nil
}

// canonicalAuthority builds "scheme://host[:port]".
//
// The host is lower-cased and punycoded by NormalizeHost, because that is the
// name that exists in DNS — submitting the Unicode spelling would scan a name
// the provider resolves differently, and an IDN homograph would come back "no
// risk found" for free. A default port is dropped because RFC 3986 §6.2.3 makes
// it equivalent to the empty one.
func canonicalAuthority(parsed *url.URL) (string, error) {
	host, err := NormalizeHost(parsed.Hostname())
	if err != nil {
		return "", err
	}
	if port := parsed.Port(); port != "" && !isDefaultPort(parsed.Scheme, port) {
		host += ":" + port
	}
	return parsed.Scheme + "://" + host, nil
}

// canonicalPathAndQuery returns the part of the URL the origin server sees.
//
// Taken as written — EscapedPath and RawQuery — and that is the whole point:
// sorting, decoding, re-encoding or collapsing any of it is a way for two URLs
// the origin server treats differently to end up sharing a verdict. An empty
// path becomes "/" because RFC 3986 §6.2.3 makes them the same resource, and the
// fragment is dropped because it never reaches the server at all.
func canonicalPathAndQuery(parsed *url.URL) string {
	path := parsed.EscapedPath()
	if path == "" {
		path = "/"
	}
	if parsed.RawQuery == "" {
		return path
	}
	return path + "?" + parsed.RawQuery
}

// isDefaultPort reports the port that carries no information for a scheme.
func isDefaultPort(scheme, port string) bool {
	return (scheme == "http" && port == "80") || (scheme == "https" && port == "443")
}

// URLDigest is the storage key for a canonical URL: SHA-256 over its exact bytes.
//
// It lives here, next to CanonicalizeURL, because it is the second half of the
// same contract. chat-service and file-service both key durable rows by it — and
// since issue #135 they share one table, files.link_fetch_denylist, whose whole
// job is to let one service veto the other's clearance. Two definitions of "the
// key for this URL" would be a veto that silently never matches: the row would be
// written under one digest and looked up under another, and the failure would
// look exactly like "no such URL" rather than like a bug.
//
// It hashes the canonical form and nothing else. Feeding it a raw user URL would
// key two spellings of one resource differently, which is the same failure by
// another route.
func URLDigest(canonicalURL string) []byte {
	sum := sha256.Sum256([]byte(canonicalURL))
	return sum[:]
}
