// Package linkfetch is the one place NChat opens an outbound HTTP connection to
// a destination a user named (RF-10 preview, issue #807 rich previews).
//
// # Why a shared package
//
// It used to live inside file-service's link preview. Issue #807 moves the
// preview pipeline to chat-service, where the messages are, and the SSRF policy
// cannot exist twice: two dialers with two address lists is how one of them
// silently falls behind the other. So the dialer, the URL rules, the bounded
// reader and the Open Graph parser live here once, exactly like urlsafety, and
// both services import them.
//
// # Threat posture
//
// The URL is attacker-controlled by definition, so the controls below are the
// feature rather than hardening around it:
//
//   - the destination is judged by the IP address the connection will use, not
//     by its hostname. The dialer resolves, checks every answer, and connects
//     to an address it has already accepted, so there is no window in which a
//     name could resolve to something else — DNS rebinding has nowhere to
//     happen. A name that answers with several addresses is refused if any one
//     of them is private;
//   - every redirect is a new connection through that same dialer, so the whole
//     policy applies again at every hop, the hop count is bounded, and the
//     caller may veto a hop with its own policy (reputation, in #807) before it
//     is followed;
//   - the environment's proxy settings are ignored: a proxy would resolve and
//     connect on this service's behalf, which is precisely the decision the
//     dialer exists to make;
//   - TLS is verified normally, against the original hostname. Nothing here
//     relaxes certificate validation;
//   - the response must declare an allowed media type, the body is read through
//     a limit applied to the *decompressed* bytes, and HTML parsing stops at
//     <body>. A slow server, an endless body and a compression bomb all end at
//     a bound;
//   - an image is sniffed, its declared dimensions are bounded before a single
//     pixel is decoded, and what leaves is a re-encoded thumbnail — never the
//     remote bytes;
//   - the extracted strings are data, never markup. They are validity-checked,
//     whitespace-normalised and truncated, and nothing here turns them into
//     HTML.
//
// Errors are classified rather than described: a caller learns that a
// destination was refused, never which one or why, so nothing built on this
// package can be used to map a network it is not supposed to reach.
package linkfetch

import "errors"

// Error classes. They are the contract with every caller, which maps each to a
// status code, a preview state or a metric label. No error produced by this
// package carries a hostname, an address or an upstream message.
var (
	// ErrInvalidURL marks a request that is not a usable URL at all.
	ErrInvalidURL = errors.New("link fetch: invalid url")
	// ErrURLNotAllowed marks a well-formed URL this package refuses to fetch: a
	// scheme, a port or — the case that matters — a destination that is not
	// public. It never says which.
	ErrURLNotAllowed = errors.New("link fetch: url not allowed")
	// ErrRedirectRefused marks a redirect the caller's hop policy vetoed. It is
	// distinct from ErrURLNotAllowed because the destination may be perfectly
	// public; it is its *reputation* the caller would not vouch for.
	ErrRedirectRefused = errors.New("link fetch: redirect refused by policy")
	// ErrUnsupportedContentType marks a response whose media type is not one the
	// fetch asked for.
	ErrUnsupportedContentType = errors.New("link fetch: unsupported content type")
	// ErrTimeout marks a remote server that did not answer within the budget.
	ErrTimeout = errors.New("link fetch: upstream timed out")
	// ErrUpstream marks any other failure of the remote server: refused
	// connection, unusable status, oversized body, unreadable stream.
	ErrUpstream = errors.New("link fetch: upstream failed")
	// ErrImageRejected marks image bytes that were fetched but may not be
	// rendered: not an image, an unsupported format, or dimensions past the
	// ceiling.
	ErrImageRejected = errors.New("link fetch: image rejected")
)
