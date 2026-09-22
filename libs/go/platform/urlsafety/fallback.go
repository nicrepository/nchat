package urlsafety

import (
	"context"
	"errors"
	"strings"
)

// Primary with a fallback (issue #928).
//
// # The order, and why it is not negotiable
//
//	primary MALICIOUS   -> MALICIOUS. The secondary is not asked: a condemnation
//	                       is already the strictest answer available, and asking
//	                       a second source could only ever produce a weaker one.
//	primary SAFE        -> SAFE. Returned immediately. The pipeline does not wait
//	                       on a scanner to double-check a list lookup; a later
//	                       recheck may still move the URL to malicious, through
//	                       the machinery that already exists for exactly that.
//	primary UNKNOWN     -> the secondary is asked. The primary looked and had
//	                       nothing to say, which is not an answer the pipeline can
//	                       act on, so the scanner gets its turn.
//	primary unavailable -> the secondary is asked.
//	secondary anything  -> returned as it stands: SAFE, MALICIOUS, UNKNOWN.
//	both unavailable    -> unavailable, with the *primary's* reason, because the
//	                       primary is the one an operator should fix first.
//
// # What this composition may never do
//
// Turn a failure into a clearance. There is no branch below in which an error
// becomes ReputationSafe, and there is no branch in which the secondary's
// UNKNOWN — Cloudflare's "finished, hasVerdicts=false", the production case
// issue #928 exists for — displaces an answer the primary already gave. The
// primary is asked first and its SAFE and MALICIOUS both return before the
// secondary is constructed a request, so "Cloudflare downgraded a fresh Google
// SAFE" is not a rule this file enforces; it is a state it cannot represent.
//
// # Routing a resumed check
//
// URLReputationProvider.Check takes a providerRef: empty on a first attempt,
// and whatever the previous ErrCheckInProgress carried afterwards. Only an
// asynchronous provider issues one, and of the two composed here only the
// secondary is asynchronous — Web Risk answers on the first call and never hands
// back a ref. So a non-empty ref is, by construction, a scan outstanding at the
// secondary, and it goes straight there. Nothing is parsed out of the ref and no
// prefix is imposed on it: the pipeline's own recovery path adopts raw provider
// scan ids directly into the same column, and a composition that only understood
// refs it had minted itself would misroute every one of those.

// DirectProviderRef is the ref a pipeline persists for a provider that answered
// synchronously — there is no remote id to keep, but the verdict write still
// binds to an attempt through the same compare-and-set a poll uses.
//
// It is exported so the composition can recognise it: a row carrying it has no
// check outstanding anywhere, so resuming it means asking the primary again
// rather than handing a word that is not a scan id to the secondary.
const DirectProviderRef = "direct"

// PrimaryFallbackProvider asks one reputation source and falls back to another.
//
// It knows nothing about HTTP, either provider's response shape, or either
// provider's credentials. Its whole content is the precedence above, which is
// what keeps the decision reviewable in one screen and keeps the adapters from
// growing knowledge of each other.
type PrimaryFallbackProvider struct {
	primary   URLReputationProvider
	secondary URLReputationProvider
	// breaker guards the primary alone.
	//
	// The pipeline's own breaker sits in front of this whole composition and
	// opens only when the composition fails — which, during a primary outage that
	// the secondary is covering, it does not. Without a breaker here, a Web Risk
	// outage would therefore be invisible to the circuit and every single URL
	// would spend a doomed request on it before falling through. This is the
	// thing that stops that, and it is also what keeps a 429 from being answered
	// with more requests.
	breaker *Breaker
	metrics *Metrics
}

// NewPrimaryFallbackProvider composes two providers. metrics may be nil.
func NewPrimaryFallbackProvider(
	primary, secondary URLReputationProvider, metrics *Metrics,
) *PrimaryFallbackProvider {
	return &PrimaryFallbackProvider{
		primary:   primary,
		secondary: secondary,
		breaker:   NewBreaker(0, 0),
		metrics:   metrics,
	}
}

// Name satisfies URLReputationProvider. Diagnostic only: every result carries
// the name of the adapter that actually produced it.
func (p *PrimaryFallbackProvider) Name() string {
	return p.primary.Name() + "+" + p.secondary.Name()
}

// Check runs the precedence documented above.
func (p *PrimaryFallbackProvider) Check(
	ctx context.Context, canonicalURL, providerRef string,
) (ReputationResult, error) {
	if resumable(providerRef) {
		// A scan is outstanding at the secondary. The primary has nothing to
		// resume and asking it again would start a second, parallel line of
		// evidence for a row that is already committed to one.
		return p.checkSecondary(ctx, canonicalURL, providerRef)
	}
	result, err := p.checkPrimary(ctx, canonicalURL)
	if decisive(result, err) {
		return result, err
	}
	if ctx.Err() != nil {
		// The caller went away between the two providers. Not a fact about the
		// URL, and not a reason to spend a second exchange.
		return ReputationResult{}, ctx.Err()
	}
	secondaryResult, secondaryErr := p.checkSecondary(ctx, canonicalURL, "")
	if secondaryErr != nil && err != nil {
		// Both failed. The primary's reason is the one reported: it is the source
		// that is supposed to answer, so it is the one an operator fixes first.
		// Either way the pipeline retries, and neither is ever a clearance.
		return ReputationResult{}, err
	}
	return secondaryResult, secondaryErr
}

// resumable reports whether a ref names a check outstanding at the secondary.
func resumable(providerRef string) bool {
	trimmed := strings.TrimSpace(providerRef)
	return trimmed != "" && trimmed != DirectProviderRef
}

// decisive reports whether the primary's answer ends the exchange.
//
// An in-progress check is decisive too, and deliberately: the pipeline must
// persist that ref and come back to it, not have a second provider consulted
// behind its back for the same URL.
func decisive(result ReputationResult, err error) bool {
	if errors.Is(err, ErrCheckInProgress) {
		return true
	}
	if err != nil {
		return false
	}
	return result.Verdict == ReputationSafe || result.Verdict == ReputationMalicious
}

// checkPrimary asks the primary through its own circuit breaker.
func (p *PrimaryFallbackProvider) checkPrimary(
	ctx context.Context, canonicalURL string,
) (ReputationResult, error) {
	if !p.breaker.Allow() {
		p.observe(p.primary.Name(), ReasonCircuitOpen)
		return ReputationResult{}, ErrCircuitOpen
	}
	result, err := p.primary.Check(ctx, canonicalURL, "")
	p.breaker.Complete(breakerOutcome(ctx, err))
	p.observeExchange(p.primary.Name(), result, err)
	return result, err
}

// checkSecondary asks the fallback. It has no breaker here because the
// pipeline's own one already covers it: every path that reaches the secondary
// either returns its answer or fails the whole composition, which is exactly
// what that breaker counts.
func (p *PrimaryFallbackProvider) checkSecondary(
	ctx context.Context, canonicalURL, providerRef string,
) (ReputationResult, error) {
	result, err := p.secondary.Check(ctx, canonicalURL, providerRef)
	p.observeExchange(p.secondary.Name(), result, err)
	return result, err
}

// observeExchange counts one provider exchange under a closed outcome label.
func (p *PrimaryFallbackProvider) observeExchange(
	provider string, result ReputationResult, err error,
) {
	switch {
	case errors.Is(err, ErrCheckInProgress):
		p.observe(provider, resultPending)
	case errors.Is(err, context.Canceled):
		// The caller going away says nothing about the provider, so it is not
		// counted against it. A deadline elapsing is not in this branch and is
		// counted, for the reason breakerOutcome states: the provider was given
		// a budget and did not answer inside it.
	case err != nil:
		p.observe(provider, FailureReason(err))
	case result.Reason != "":
		// A terminal non-answer the adapter could narrow — today only
		// Cloudflare's hostname refusal. Counted under that reason instead of a
		// flat "unknown", because the two need different responses even though
		// the pipeline treats them identically.
		p.observe(provider, result.Reason)
	default:
		p.observe(provider, string(result.Verdict))
	}
}

func (p *PrimaryFallbackProvider) observe(provider, result string) {
	p.metrics.observeProvider(provider, result)
}
