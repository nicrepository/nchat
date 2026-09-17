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
// # Where the hardening lives
//
// The dialer, the address policy, the bounded reader and the Open Graph parser
// are libs/go/platform/linkfetch, shared with chat-service's rich-preview
// pipeline (issue #807). This package adds what is specific to the interactive
// route: the reputation gate in front of the fetch, the response cache and the
// error classes the HTTP layer maps. See that package for the threat posture;
// nothing here relaxes any part of it.
package linkpreview

import (
	"context"
	"errors"
	"net/url"
	"time"

	"github.com/nicrepository/nchat/libs/go/platform/linkfetch"
	"github.com/nicrepository/nchat/libs/go/platform/urlsafety"
	"github.com/nicrepository/nchat/services/file-service/internal/service"
)

// Error classes. They are the contract with the HTTP layer, which maps each to
// a status code and a fixed message. No error produced by this package carries
// a hostname, an address or an upstream message.
//
// The transport and policy classes are the shared fetcher's own values, not
// copies: an error the fetcher produces *is* the error the handler compares
// against, so the mapping cannot drift between the two packages.
var (
	// ErrInvalidURL marks a request that is not a usable URL at all.
	ErrInvalidURL = linkfetch.ErrInvalidURL
	// ErrURLNotAllowed marks a well-formed URL this service refuses to fetch:
	// a scheme, a port or — the case that matters — a destination that is not
	// public. It never says which.
	ErrURLNotAllowed = linkfetch.ErrURLNotAllowed
	// ErrUnsupportedContentType marks a response that is not HTML.
	ErrUnsupportedContentType = linkfetch.ErrUnsupportedContentType
	// ErrTimeout marks a remote server that did not answer within the budget.
	ErrTimeout = linkfetch.ErrTimeout
	// ErrUpstream marks any other failure of the remote server: refused
	// connection, unusable status, oversized body, unreadable stream.
	ErrUpstream = linkfetch.ErrUpstream
	// ErrNoMetadata marks a document that was fetched and parsed and carried
	// nothing worth showing. It is an expected outcome, not a failure.
	ErrNoMetadata = errors.New("link preview: no metadata")
	// ErrMaliciousURL marks a URL the Safe Browsing provider reported as a
	// security threat, or whose host carries no reputation that could be
	// consulted at all (RF-21). Both are permanent refusals: retrying the same
	// link will not change the answer.
	ErrMaliciousURL = errors.New("link preview: url is not safe")
	// ErrSafetyUnavailable marks a URL whose safety could not be established.
	// It is deliberately not ErrUpstream: the linked site may be perfectly
	// reachable, and the caller is being told to try again rather than that the
	// link is bad.
	ErrSafetyUnavailable = errors.New("link preview: safety check unavailable")
	// ErrSafetyPending marks a URL whose scan has been queued but not finished
	// (RF-21). It is distinct from ErrSafetyUnavailable because nothing is
	// broken: the provider is submit-then-poll, the submission just happened,
	// and retrying shortly succeeds. No Open Graph fetch happens in this state.
	ErrSafetyPending = errors.New("link preview: safety check pending")
	// ErrSafetyCapacity marks a URL this service declined to start a new scan
	// for: the window is spent or the queue is full.
	//
	// Its own error, kept apart from both neighbours on purpose. It is not
	// ErrMaliciousURL — a full queue says nothing about the link, and a client
	// shown a security warning for an operational condition learns the wrong
	// thing. It is not ErrSafetyUnavailable either: unavailable means the check
	// failed, this means it was deliberately not attempted. Neither is a
	// clearance, and nothing is fetched in either case.
	ErrSafetyCapacity = errors.New("link preview: safety scan capacity exceeded")
)

// MaxURLLength bounds what a client may submit. It is the shared fetcher's
// ceiling, re-exported so the HTTP layer's request bound stays in step.
const MaxURLLength = linkfetch.MaxURLLength

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
	resultMalicious       = "malicious"
	resultSafetyUnknown   = "safety_unavailable"
)

// negativeTTL is how long a terminal failure is remembered.
//
// Failures are cached, and deliberately: without it a client retrying a dead
// URL pays a full timeout and a fresh socket every time, which is the one
// outcome that costs this service real resources. It is short because the
// remote side may recover and a refusal must not outlive a fixed page by long.
const negativeTTL = time.Minute

// URLSafetyChecker reports what is already known about a canonical URL (RF-21).
//
// It is declared here, at the consumer, and satisfied by *urlsafety.Service —
// this package depends on the question, not on Cloudflare. Nil is a supported
// value and means the deployment did not enable the check: the preview then
// behaves exactly as it did before RF-21.
//
// The unit is a canonical URL and not a hostname. A preview is a rendering of
// one *page*, so deciding it by the reputation of the domain hosting it was the
// hole this replaced — a phishing page on a compromised path inherited the
// clearance of its host.
//
// Neither method blocks on the provider: Cloudflare URL Scanner is
// submit-then-poll, so a URL nobody has scanned has no verdict yet. LoadVerdict
// reads what is already known and EnsureScan records that a verdict is needed —
// a background worker is what actually talks to Cloudflare. The preview answers
// "being checked" rather than waiting.
//
// AdmitScan and not Submit, and that distinction is the correction: the preview
// used to submit a scan itself and discard the provider's id, so nothing ever
// polled it and every retry of the same preview submitted again. Recording the
// *need* for a scan is idempotent, so a client refreshing a pending preview
// costs one row update and no provider quota at all.
//
// Admit and not Ensure, and that is this round's: queueing a scan is spending
// money at a third party, and nothing counted how much. A client asking for
// previews of URLs nobody has seen before could introduce unbounded new scans,
// and the per-request limiter did not touch that, because the unit it counts is
// a request and the unit the provider bills is a URL. A URL already answered or
// already queued still costs nothing and is always admitted.
type URLSafetyChecker interface {
	LoadVerdict(ctx context.Context, canonicalURL string) (urlsafety.Verdict, bool, error)
	AdmitScan(ctx context.Context, canonicalURL string, capacity service.LinkScanCapacity) (service.LinkScanAdmission, error)
}

// Service answers preview requests, in front of a cache.
type Service struct {
	fetcher  *linkfetch.Fetcher
	cache    *cache
	ttl      time.Duration
	observer Observer
	safety   URLSafetyChecker
	// scanCapacity is what this service will spend on new provider work. Zero
	// values disable the corresponding ceiling.
	scanCapacity service.LinkScanCapacity
}

// NewService builds the service. timeout bounds one whole remote exchange and
// ttl is how long a successful preview is reused.
func NewService(timeout, ttl time.Duration, observer Observer) *Service {
	return newService(linkfetch.NewFetcher(timeout), ttl, observer, time.Now)
}

// newService is NewService with the fetcher and the clock supplied, so a test
// can drive cache expiry without sleeping and reach a local server without the
// address policy being weakened for it.
func newService(f *linkfetch.Fetcher, ttl time.Duration, observer Observer, now func() time.Time) *Service {
	return &Service{
		fetcher:  f,
		cache:    newCache(maxCacheEntries, now),
		ttl:      ttl,
		observer: observer,
	}
}

// WithURLSafety enables the RF-21 reputation check in front of every fetch.
// Returns the service for chaining; when never called, no check runs.
func (s *Service) WithURLSafety(safety URLSafetyChecker) *Service {
	s.safety = safety
	return s
}

// WithScanCapacity configures what this service will spend on new provider
// work. Zero values disable the corresponding ceiling; the numbers that matter
// come from configuration, because the right rate depends on the Cloudflare
// plan this deployment is billed under.
func (s *Service) WithScanCapacity(capacity service.LinkScanCapacity) *Service {
	s.scanCapacity = capacity
	return s
}

// Preview returns the metadata for rawURL.
func (s *Service) Preview(ctx context.Context, rawURL string) (Preview, error) {
	target, err := linkfetch.ParseURL(rawURL)
	if err != nil {
		// A URL that never became canonical has no cache key, so this one case
		// is answered before the cache rather than through it.
		s.observe(err)
		return Preview{}, err
	}
	// Reputation is judged ahead of the preview cache, not behind it.
	//
	// Behind it, the two lifetimes would compose the wrong way round: a preview
	// may be reused for up to a day while a verdict is only trusted for minutes,
	// so a cache hit would keep serving a card for a URL whose reputation had
	// already turned. Asking first makes the shorter lifetime the authority —
	// once the verdict expires the next request re-consults, and a URL that has
	// become malicious is refused even though its Open Graph entry is still
	// perfectly valid.
	//
	// It costs nothing per request: the checker has its own cache, so a hit here
	// is a map lookup and not a call to the provider.
	if err := s.checkSafety(ctx, target); err != nil {
		if ctx.Err() != nil {
			return Preview{}, ctx.Err()
		}
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

// load fetches and parses one document. Reputation has already been judged by
// the caller, before the cache and therefore before this — a preview of a
// phishing page is a phishing page rendered by this service.
//
// That check is not a replacement for the address policy below: that one decides
// whether this deployment may connect to the destination at all, and it still
// runs, at every hop, for every URL that gets this far.
func (s *Service) load(ctx context.Context, key string, target *url.URL) (Preview, error) {
	// No hop policy beyond the address rules: this route answers one
	// interactive request about one URL and has no per-hop verdict to consult.
	// The chat-service pipeline, which does, passes one.
	final, body, err := s.fetcher.FetchDocument(ctx, target, nil)
	if err != nil {
		return Preview{}, err
	}
	metadata := linkfetch.Extract(final, body)
	if !metadata.HasMetadata() {
		return Preview{}, ErrNoMetadata
	}
	return Preview{
		URL: key, Title: metadata.Title, Description: metadata.Description,
		ImageURL: metadata.ImageURL, SiteName: metadata.SiteName,
	}, nil
}

// checkSafety refuses a URL the provider condemned, and refuses one it could
// not answer about.
//
// Fail-closed is the deliberate choice, and it is cheap here: an unavailable
// verdict costs the user a card they can retry, never a message they cannot
// send. The alternative — previewing a link of unknown reputation — would make
// a provider outage the moment the control silently stops existing.
func (s *Service) checkSafety(ctx context.Context, target *url.URL) error {
	if s.safety == nil {
		return nil
	}
	canonical, err := urlsafety.CanonicalizeURL(target.String())
	if err != nil {
		// A URL with no consultable reputation — an IP literal, credentials in
		// the URL — cannot be cleared, and a permanent condition must not be
		// reported as a temporary one.
		return ErrMaliciousURL
	}
	verdict, ok, err := s.safety.LoadVerdict(ctx, canonical)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// The store is the authority; not being able to read it is not a
		// clearance.
		return ErrSafetyUnavailable
	}
	if !ok {
		// No verdict yet. The need for one is recorded and the caller is told to
		// come back; what must not happen is the fetch, because fetching first
		// and asking afterwards is rendering the phishing page.
		//
		// Recording is idempotent, so a client polling this endpoint produces one
		// durable job and not one scan per request. A failure to record is still
		// an unavailable verdict, never a clearance.
		admission, err := s.safety.AdmitScan(ctx, canonical, s.scanCapacity)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return ErrSafetyUnavailable
		}
		if !admission.Allowed() {
			// This service declined to start a new scan right now. Its own error:
			// a spent window or a full queue says nothing about the link, and
			// reporting it as malicious would show a security warning for an
			// operational condition. Still not a clearance — nothing is fetched.
			return ErrSafetyCapacity
		}
		return ErrSafetyPending
	}
	switch {
	case verdict == urlsafety.VerdictMalicious:
		return ErrMaliciousURL
	case verdict != urlsafety.VerdictSafe:
		// Any state that is not an explicit clearance is refused. This case
		// exists so adding a verdict to the shared package cannot quietly
		// become "allowed" here.
		return ErrSafetyUnavailable
	}
	return nil
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
	case errors.Is(err, ErrMaliciousURL):
		return resultMalicious
	case errors.Is(err, ErrSafetyUnavailable):
		return resultSafetyUnknown
	default:
		return resultUpstreamError
	}
}
