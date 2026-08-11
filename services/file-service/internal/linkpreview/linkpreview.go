// Package linkpreview turns a URL a user pasted into the handful of strings a
// link card shows (RF-10).
//
// # What it is
//
// One controlled GET, server-side, for one HTML document, from which a few
// Open Graph values are read. That is the whole feature. It is not a crawler,
// not a proxy, and not a renderer: no JavaScript, CSS, image, font, iframe or
// any other subresource is ever requested, and no browser is involved. The only
// thing that reaches the network is the document itself.
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
//     policy applies again at every hop, and the hop count is bounded;
//   - the environment's proxy settings are ignored: a proxy would resolve and
//     connect on this service's behalf, which is precisely the decision the
//     dialer exists to make;
//   - TLS is verified normally, against the original hostname. Nothing here
//     relaxes certificate validation;
//   - the response must declare text/html, the body is read through a limit,
//     and parsing stops at <body>. A slow server, an endless body and a
//     compression bomb all end at a bound;
//   - the extracted strings are data, never markup. They are validity-checked,
//     whitespace-normalised and truncated, and nothing in this service turns
//     them into HTML.
//
// Errors are classified rather than described: a caller learns that a
// destination was refused, never which one or why, so the endpoint cannot be
// used to map a network it is not supposed to reach.
package linkpreview

import (
	"context"
	"errors"
	"net/url"
	"time"
)

// Error classes. They are the contract with the HTTP layer, which maps each to
// a status code and a fixed message. No error produced by this package carries
// a hostname, an address or an upstream message.
var (
	// ErrInvalidURL marks a request that is not a usable URL at all.
	ErrInvalidURL = errors.New("link preview: invalid url")
	// ErrURLNotAllowed marks a well-formed URL this service refuses to fetch:
	// a scheme, a port or — the case that matters — a destination that is not
	// public. It never says which.
	ErrURLNotAllowed = errors.New("link preview: url not allowed")
	// ErrUnsupportedContentType marks a response that is not HTML.
	ErrUnsupportedContentType = errors.New("link preview: unsupported content type")
	// ErrTimeout marks a remote server that did not answer within the budget.
	ErrTimeout = errors.New("link preview: upstream timed out")
	// ErrUpstream marks any other failure of the remote server: refused
	// connection, unusable status, oversized body, unreadable stream.
	ErrUpstream = errors.New("link preview: upstream failed")
	// ErrNoMetadata marks a document that was fetched and parsed and carried
	// nothing worth showing. It is an expected outcome, not a failure.
	ErrNoMetadata = errors.New("link preview: no metadata")
)

// Preview is what the client receives. Every field is plain text or a plain
// URL; none of it is markup and none of it may be rendered as such.
type Preview struct {
	// URL is the canonical form of what was requested — not where a redirect
	// ended. A card that claimed a different address than the link the user
	// sees would be a phishing primitive.
	URL         string `json:"url"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	ImageURL    string `json:"imageUrl,omitempty"`
	SiteName    string `json:"siteName,omitempty"`
}

// hasMetadata reports whether there is anything worth showing.
//
// URL is excluded deliberately: it is what the caller already sent, so a
// preview carrying only that is an empty card, not a card. Every other field is
// counted, and they are counted here rather than at the call site so the rule
// cannot drift from the struct — adding a field to Preview without adding it
// below is the mistake this method exists to make hard.
func (p Preview) hasMetadata() bool {
	return p.Title != "" || p.Description != "" || p.ImageURL != "" || p.SiteName != ""
}

// Observer counts outcomes. Values come from a closed set decided in this
// package: no URL, hostname or address is ever a label.
type Observer interface {
	ObserveLinkPreview(result string)
}

// Outcome labels.
const (
	resultHit             = "hit"
	resultSuccess         = "success"
	resultInvalidURL      = "invalid_url"
	resultBlocked         = "blocked"
	resultUnsupportedType = "unsupported_content_type"
	resultTimeout         = "timeout"
	resultUpstreamError   = "upstream_error"
	resultNoMetadata      = "no_metadata"
)

// negativeTTL is how long a terminal failure is remembered.
//
// Failures are cached, and deliberately: without it a client retrying a dead
// URL pays a full timeout and a fresh socket every time, which is the one
// outcome that costs this service real resources. It is short because the
// remote side may recover and a refusal must not outlive a fixed page by long.
const negativeTTL = time.Minute

// Service answers preview requests, in front of a cache.
type Service struct {
	fetcher  *fetcher
	cache    *cache
	ttl      time.Duration
	observer Observer
}

// NewService builds the service. timeout bounds one whole remote exchange and
// ttl is how long a successful preview is reused.
func NewService(timeout, ttl time.Duration, observer Observer) *Service {
	return newService(newFetcher(timeout, lookupAddrs), ttl, observer, time.Now)
}

// newService is NewService with the fetcher and the clock supplied, so a test
// can drive cache expiry without sleeping and reach a local server without the
// address policy being weakened for it.
func newService(f *fetcher, ttl time.Duration, observer Observer, now func() time.Time) *Service {
	return &Service{
		fetcher:  f,
		cache:    newCache(maxCacheEntries, now),
		ttl:      ttl,
		observer: observer,
	}
}

// Preview returns the metadata for rawURL.
func (s *Service) Preview(ctx context.Context, rawURL string) (Preview, error) {
	target, err := canonicalURL(rawURL)
	if err != nil {
		// A URL that never became canonical has no cache key, so this one case
		// is answered before the cache rather than through it.
		s.observe(err)
		return Preview{}, err
	}
	key := target.String()
	if entry, ok := s.cache.get(key); ok {
		if entry.err != nil {
			s.observe(entry.err)
			return Preview{}, entry.err
		}
		s.observeResult(resultHit)
		return entry.preview, nil
	}

	preview, err := s.load(ctx, key, target)
	// A cancelled request is the caller going away, not an answer about the
	// URL, so it is neither cached nor counted.
	if ctx.Err() != nil {
		return Preview{}, ctx.Err()
	}
	s.cache.set(key, preview, err, s.ttlFor(err))
	s.observe(err)
	return preview, err
}

func (s *Service) load(ctx context.Context, key string, target *url.URL) (Preview, error) {
	final, body, err := s.fetcher.fetch(ctx, target)
	if err != nil {
		return Preview{}, err
	}
	preview := extract(final, body)
	if !preview.hasMetadata() {
		return Preview{}, ErrNoMetadata
	}
	preview.URL = key
	return preview, nil
}

func (s *Service) ttlFor(err error) time.Duration {
	if err != nil {
		return negativeTTL
	}
	return s.ttl
}

func (s *Service) observe(err error) {
	s.observeResult(resultFor(err))
}

func (s *Service) observeResult(result string) {
	if s.observer == nil {
		return
	}
	s.observer.ObserveLinkPreview(result)
}

// resultFor maps an error to its metric label. The set is closed, so an
// unclassified error is counted as an upstream failure rather than creating a
// new series.
func resultFor(err error) string {
	switch {
	case err == nil:
		return resultSuccess
	case errors.Is(err, ErrInvalidURL):
		return resultInvalidURL
	case errors.Is(err, ErrURLNotAllowed):
		return resultBlocked
	case errors.Is(err, ErrUnsupportedContentType):
		return resultUnsupportedType
	case errors.Is(err, ErrTimeout):
		return resultTimeout
	case errors.Is(err, ErrNoMetadata):
		return resultNoMetadata
	default:
		return resultUpstreamError
	}
}
