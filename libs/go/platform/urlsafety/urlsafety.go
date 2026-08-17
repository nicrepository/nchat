// Package urlsafety answers one question about a URL a user named: does the
// Safe Browsing provider consider it a security threat (RF-21)?
//
// # Why it is here and not in a service
//
// The same verdict is needed in two places that must not disagree: chat-service
// refuses to publish a message carrying a malicious link, and file-service
// refuses to fetch one for a preview. Restating the provider contract, the
// verdict rule and the cache lifetime in both modules would let them drift, and
// a security control that answers differently depending on which door was used
// is not a control. So it lives here once, exactly like uploadpolicy.
//
// # Why the unit is a URL
//
// It used to be a hostname, and that was a real hole: every path on a reputable
// domain inherited one verdict, so a phishing page on a compromised
// subdirectory, or an open redirect carrying its payload in a query parameter,
// was cleared by the reputation of the domain hosting it. The unit is now the
// canonical URL — scheme, host, path and query — and CanonicalizeURL is the only
// thing allowed to decide that two spellings are the same resource.
//
// # What it is not
//
// It is not the SSRF policy. That one decides whether this deployment may open a
// connection to a destination at all, and it lives in file-service's link
// preview package where the dialer is. This package only reports reputation. Two
// different controls, neither replacing the other: a URL may be reputable and
// still live at a private address this service must never reach.
//
// # The verdict model
//
// Three states, never two. A boolean would make "the provider did not answer"
// indistinguishable from "the provider said it is fine", which for a blocking
// control is the one failure mode that matters — a Cloudflare outage would
// silently turn into a green light. So a technical failure is VerdictUnknown
// with an error, and it is the caller's policy, stated at the call site, that
// decides what an unknown means. Nothing in this package ever converts an error
// into VerdictSafe.
//
// # Why it is asynchronous
//
// Cloudflare URL Scanner is a submit-then-poll API: the result endpoint answers
// 404 while a scan runs, and the provider recommends polling every 10-30
// seconds. That cannot sit inside an interactive send, so this package exposes
// the two halves separately — Submit and Poll — and each service drives them
// from its own durable queue. The read path a request may use is Lookup, which
// touches no network at all.
package urlsafety

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/idna"
)

// Verdict is what the provider says about a canonical URL.
type Verdict string

const (
	// VerdictUnknown is the zero value, and that is deliberate: a Verdict that
	// was never assigned must not read as "safe".
	VerdictUnknown Verdict = "unknown"
	// VerdictSafe means the provider finished a scan and reported no risk.
	VerdictSafe Verdict = "safe"
	// VerdictMalicious means the provider finished a scan and reported the URL
	// as malicious.
	VerdictMalicious Verdict = "malicious"
	// VerdictInconclusive means the provider confirms the scan finished but
	// produced no verdict this package can act on.
	//
	// It is deliberately not IsFinal(): IsFinal answers "is this a usable safety
	// clearance", and inconclusive is neither an allow nor a deny — it is fail-
	// closed, exactly like an unknown answer, with one difference that matters to
	// the callers that persist it: it is terminal for the scan that produced it,
	// so a caller that already recorded it must stop asking the provider about
	// that scan id rather than retry it. Nothing in this package ever promotes it
	// to a clearance.
	VerdictInconclusive Verdict = "inconclusive"
)

// IsFinal reports whether a Verdict is one this package will act on.
//
// It exists because Verdict is a string, so a zero value, a value from a future
// version of this package, or a value a buggy provider client invented are all
// representable — and all of them must behave like "no answer" rather than like
// an answer nobody checked. Every place a verdict crosses a boundary asks this
// first, so adding a new Verdict constant cannot silently become "allowed".
func (v Verdict) IsFinal() bool {
	return v == VerdictSafe || v == VerdictMalicious
}

var (
	// ErrUnavailable marks a transient failure to obtain a verdict: the provider
	// timed out, refused, or answered with something unusable. Retrying may
	// succeed, and callers should say so.
	ErrUnavailable = errors.New("url safety: verdict unavailable")
	// ErrNotCheckable marks a URL that has no reputation to consult at all — an
	// IP literal, a scheme the provider does not scan, a URL carrying
	// credentials, or a name that is not a name. Retrying can never succeed, so
	// it must not be reported to a user as a temporary problem.
	ErrNotCheckable = errors.New("url safety: url has no consultable reputation")
)

// Lifetimes and budgets.
//
// These are constants and not configuration, and that is the point. They are
// properties of a security verdict shared by two services: a per-service knob
// would let chat-service and file-service cache the same URL for different
// lengths of time, which is how the two doors start answering differently. The
// operator decides *whether* the check runs (one flag per service) and supplies
// the credentials; the semantics of the verdict are not a deployment choice.
const (
	// VerdictTTL is how long a finished verdict is reused, safe or malicious
	// alike. It is finite because reputation changes: a compromised URL that is
	// cleaned up, or a clean one that is compromised, must not be frozen.
	VerdictTTL = 15 * time.Minute

	// FailureTTL is how long a *failure* is remembered — as a failure. It is
	// short, and it is not a relaxation: a cached failure is still a failure and
	// still refuses. It exists so a provider outage costs one timeout per URL
	// per half-minute instead of one timeout per message.
	//
	// It is exported because the durable queues in chat-service and file-service
	// schedule their retries against the same number. Two different backoffs for
	// the same outage is how the two services start disagreeing.
	FailureTTL = 30 * time.Second

	// maxCacheEntries bounds the cache. Every entry is a canonical URL and a
	// verdict, so the count bounds the memory. The bound is what stops a client
	// feeding unique URLs from growing the map without limit.
	maxCacheEntries = 4096

	// maxHostLength is the DNS ceiling. A longer name cannot resolve anywhere,
	// so it is refused rather than sent to the provider.
	maxHostLength = 253

	// maxURLLength bounds a canonical URL, and therefore a cache key, a database
	// column and a provider submission. It is well past anything a person
	// writes and well short of anything that could be used to bloat the row it
	// is stored in.
	maxURLLength = 2048
)

// Scanner is one provider. It is an interface so the decision layer above can be
// tested without a network, and so a provider swap is one constructor.
//
// The two halves are separate because the provider's are: a submission returns
// an id, and the verdict has to be collected later. Hiding that behind a single
// blocking call is what would put a 10-30 second poll inside a user's request.
type Scanner interface {
	// SubmitScan asks for a canonical URL to be scanned and returns the scan id.
	SubmitScan(ctx context.Context, canonicalURL string) (string, error)
	// GetScanResult reads a submitted scan. It returns ErrScanPending while the
	// scan is still running, ErrScanInconclusive when the provider confirms the
	// scan is finished but produced no usable verdict, and must never return
	// VerdictSafe with an error.
	GetScanResult(ctx context.Context, scanID string) (Verdict, error)
}

// Service is the decision layer: hold the provider contract to its promises,
// remember answers for a bounded time, and count what happened.
//
// It owns no durable state. The queue that survives a restart belongs to each
// service, because each one stores it in its own schema; what lives here is the
// part that must not differ between them.
type Service struct {
	scanner Scanner
	cache   *cache
	metrics *Metrics
}

// NewService wraps a provider with the shared cache and metrics. metrics may be
// nil.
func NewService(scanner Scanner, metrics *Metrics) *Service {
	return newService(scanner, metrics, time.Now)
}

// newService is NewService with the clock supplied, so a test can drive expiry
// without sleeping.
func newService(scanner Scanner, metrics *Metrics, now func() time.Time) *Service {
	return &Service{scanner: scanner, cache: newCache(maxCacheEntries, now), metrics: metrics}
}

// Lookup answers from memory only, and never blocks.
//
// This is the call an interactive request makes. A hit with a final verdict is
// the whole fast path: a URL somebody already sent, still inside VerdictTTL,
// costs nothing and the send proceeds exactly as it did before RF-21 existed. A
// miss means the caller must arrange a scan and hold the message, which is the
// caller's business and not this package's.
//
// A cached failure is reported as a miss on purpose: it is not an answer, and
// the only thing a caller could do with it is treat it as one.
func (s *Service) Lookup(canonicalURL string) (Verdict, bool) {
	verdict, ok := s.cache.get(canonicalURL)
	if !ok || !verdict.IsFinal() {
		s.observe(resultMiss)
		return VerdictUnknown, false
	}
	s.observe(resultHit)
	return verdict, true
}

// Submit asks the provider to start scanning a canonical URL.
//
// It returns the provider's scan id, which the caller is expected to persist:
// losing it means the scan still runs but nobody ever reads it, and the message
// waiting on it would be stuck.
func (s *Service) Submit(ctx context.Context, canonicalURL string) (string, error) {
	if s.scanner == nil {
		s.observe(resultError)
		return "", ErrUnavailable
	}
	scanID, err := s.scanner.SubmitScan(ctx, canonicalURL)
	if ctx.Err() != nil {
		// The caller going away is not a fact about the URL, so it is neither
		// cached nor counted.
		return "", ctx.Err()
	}
	if err != nil || strings.TrimSpace(scanID) == "" {
		s.cache.set(canonicalURL, VerdictUnknown, FailureTTL)
		s.observe(resultError)
		return "", ErrUnavailable
	}
	s.observe(resultSubmitted)
	return scanID, nil
}

// FindRecentScan forwards a reconciliation lookup to the provider, when the
// provider can answer one.
//
// Deliberately a pass-through and deliberately uncached. It recovers a scan
// *id* for a submission whose outcome was lost, and the verdict is then read
// through Poll, which is where the strictness lives — a search result must never
// become a second, weaker route to a clearance, so nothing here writes to the
// cache or touches the verdict counter.
//
// A provider that cannot search is a working deployment: the caller keeps the
// attempt uncertain and waits out its horizon rather than submitting again.
func (s *Service) FindRecentScan(
	ctx context.Context, canonicalURL string, since time.Time,
) (ScanRecord, int, error) {
	searcher, ok := s.scanner.(interface {
		FindRecentScan(context.Context, string, time.Time) (ScanRecord, int, error)
	})
	if !ok {
		return ScanRecord{}, 0, ErrSearchUnsupported
	}
	return searcher.FindRecentScan(ctx, canonicalURL, since)
}

// Poll reads a submitted scan and, if it finished, remembers the verdict.
//
// This is the one place a provider answer becomes a fact this deployment acts
// on, so it is where the strictness lives:
//
//   - a verdict returned together with an error is not trusted for the verdict.
//     A provider client that managed both is a provider client with a bug, and a
//     bug must not become a green light;
//   - a verdict that is not Safe or Malicious — the zero value, a value from a
//     future version, anything at all — is not an answer. It is recorded as a
//     failure, with FailureTTL, so it is retried rather than served;
//   - a pending scan is reported as pending and cached as nothing. It is not an
//     outcome yet, and giving it a TTL would make "still running" expire into
//     something;
//   - an inconclusive scan is reported as inconclusive and, like pending, cached
//     as nothing here: the cache in this package is a URL-level safety
//     clearance, and inconclusive is not one. The durable per-scan terminal
//     state that stops the polling belongs to the caller's own queue, which is
//     also what keeps this package from ever inventing a clearance TTL for an
//     answer that was never a clearance.
func (s *Service) Poll(ctx context.Context, canonicalURL, scanID string) (Verdict, error) {
	if s.scanner == nil {
		s.observe(resultError)
		return VerdictUnknown, ErrUnavailable
	}
	verdict, err := s.scanner.GetScanResult(ctx, scanID)
	if ctx.Err() != nil {
		return VerdictUnknown, ctx.Err()
	}
	if errors.Is(err, ErrScanPending) {
		s.observe(resultPending)
		return VerdictUnknown, ErrScanPending
	}
	if errors.Is(err, ErrScanInconclusive) {
		s.observe(resultInconclusive)
		return VerdictUnknown, ErrScanInconclusive
	}
	if err != nil || !verdict.IsFinal() {
		s.cache.set(canonicalURL, VerdictUnknown, FailureTTL)
		s.observe(resultError)
		return VerdictUnknown, ErrUnavailable
	}
	s.cache.set(canonicalURL, verdict, VerdictTTL)
	s.observe(resultFor(verdict))
	return verdict, nil
}

// Remember seeds the in-process cache from a verdict the caller already holds.
//
// A replica that just started has an empty cache and a database full of verdicts
// somebody else's replica obtained. Without this, every one of those URLs would
// be rescanned; with it, the durable store stays authoritative and this cache is
// only ever a shortcut in front of it.
//
// A non-final verdict is refused rather than stored, so a corrupted row cannot
// become a clearance.
func (s *Service) Remember(canonicalURL string, verdict Verdict, remaining time.Duration) {
	if !verdict.IsFinal() || remaining <= 0 {
		return
	}
	s.cache.set(canonicalURL, verdict, remaining)
}

func (s *Service) observe(result string) {
	s.metrics.observe(result)
}

// NormalizeHost turns a hostname from a URL into the exact string used inside a
// canonical URL.
//
// Everything it does is about making two spellings of the same destination one
// cache entry and one submission, without making two different destinations
// share either:
//
//   - case is folded, because DNS is case-insensitive;
//   - a trailing root dot is dropped, because "example.com." is "example.com";
//   - a Unicode name is converted to punycode, because that is the name that
//     actually exists in DNS. Submitting the Unicode spelling would scan a name
//     the provider resolves differently — an IDN homograph would answer "no risk
//     found" for free.
//
// An IP literal is refused rather than scanned. It is not a relaxation: the
// caller treats ErrNotCheckable as a permanent refusal, so "host the phishing
// page on a bare address" is blocked rather than cleared.
func NormalizeHost(host string) (string, error) {
	host = strings.TrimSpace(host)
	host = strings.TrimSuffix(host, ".")
	if host == "" || len(host) > maxHostLength {
		return "", ErrNotCheckable
	}
	if _, err := netip.ParseAddr(strings.Trim(host, "[]")); err == nil {
		return "", ErrNotCheckable
	}
	// idna.Lookup is the strict profile: it is the one meant for turning a name
	// into the query you are about to make, and it rejects the malformed and
	// disallowed forms rather than guessing at them.
	ascii, err := idna.Lookup.ToASCII(host)
	if err != nil || ascii == "" || !strings.Contains(ascii, ".") {
		return "", ErrNotCheckable
	}
	return strings.ToLower(ascii), nil
}

// cache remembers verdicts for a bounded time.
//
// In-process, which is a known and accepted ceiling: N replicas keep N caches,
// so a URL may be looked up once per replica. Nothing here is authoritative —
// the durable queue in each service is — and nothing here is sensitive beyond
// the URL the caller already handed in.
type cache struct {
	mu         sync.Mutex
	entries    map[string]cacheEntry
	maxEntries int
	now        func() time.Time
}

type cacheEntry struct {
	verdict   Verdict
	expiresAt time.Time
}

func newCache(maxEntries int, now func() time.Time) *cache {
	if maxEntries <= 0 {
		maxEntries = maxCacheEntries
	}
	if now == nil {
		now = time.Now
	}
	return &cache{entries: make(map[string]cacheEntry), maxEntries: maxEntries, now: now}
}

// get returns a live entry. An expired one is reported as a miss and removed, so
// a stale verdict is never served and a URL nobody asks about again does not
// need a sweep to disappear.
func (c *cache) get(key string) (Verdict, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok {
		return VerdictUnknown, false
	}
	if !c.now().Before(entry.expiresAt) {
		delete(c.entries, key)
		return VerdictUnknown, false
	}
	return entry.verdict, true
}

// set stores a verdict under key for ttl.
//
// The key is the whole canonical URL: no truncation, no hashing, no prefix
// matching, no falling back to the host. That is what makes it impossible for
// one URL to inherit another's verdict — a poisoning primitive that a "close
// enough" key would hand over for free.
func (c *cache) set(key string, verdict Verdict, ttl time.Duration) {
	if ttl <= 0 {
		return
	}
	now := c.now()
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.entries) >= c.maxEntries {
		c.evict(now)
	}
	c.entries[key] = cacheEntry{verdict: verdict, expiresAt: now.Add(ttl)}
}

// evict makes room: expired entries first, and if that freed nothing, the entry
// closest to expiring.
//
// ponytail: O(n) over a map bounded at maxEntries, on writes only. An LRU with
// its own list is the upgrade if the scan ever shows up in a profile.
func (c *cache) evict(now time.Time) {
	var (
		oldestKey string
		oldest    time.Time
		found     bool
	)
	for key, entry := range c.entries {
		if !now.Before(entry.expiresAt) {
			delete(c.entries, key)
			found = true
			continue
		}
		if oldestKey == "" || entry.expiresAt.Before(oldest) {
			oldestKey, oldest = key, entry.expiresAt
		}
	}
	if !found && oldestKey != "" {
		delete(c.entries, oldestKey)
	}
}
