package urlsafety

import (
	"context"
	"errors"
	"strings"
	"time"
)

// The provider-agnostic reputation contract (issue #807).
//
// The domain asks one question — "what does a reputation source say about this
// canonical URL?" — and must not know whether the answer comes from a
// submit-then-poll scanner, a synchronous lookup API or a local list. This file
// is that boundary. Everything Cloudflare-shaped stays in cloudflare.go, behind
// the adapter at the bottom.
//
// # The four answers
//
// SAFE and MALICIOUS are the only two a pipeline may act on as clearance or
// condemnation. UNKNOWN is a *terminal* non-answer: the provider looked and has
// nothing usable to say (Cloudflare's "finished, hasVerdicts=false" is the
// production case). UNAVAILABLE is not returned as a verdict at all — it is the
// error path, because "could not ask" and "asked, no answer" demand opposite
// things from a caller: one is retried, the other is not.
//
// Nothing here turns malicious=false into SAFE. A provider adapter may only
// return ReputationSafe when its contract delivers an explicit positive
// clearance; see verdictFromReport for the one adapter that exists.

// ReputationVerdict is the provider-agnostic answer about a canonical URL.
type ReputationVerdict string

const (
	// ReputationSafe is an explicit, authoritative clearance.
	ReputationSafe ReputationVerdict = "safe"
	// ReputationMalicious is an explicit condemnation.
	ReputationMalicious ReputationVerdict = "malicious"
	// ReputationUnknown is terminal: the provider answered and the answer carries
	// no usable verdict. Asking the same provider again about the same evidence
	// will not change it.
	ReputationUnknown ReputationVerdict = "unknown"
)

// ReputationResult is what a provider says, with the minimum operational
// metadata a pipeline needs to persist it.
type ReputationResult struct {
	Verdict ReputationVerdict
	// Provider names the source, from a closed set decided by each adapter. It
	// is a diagnostic field, never a metric label with user-chosen content.
	Provider string
	// ProviderRef is an opaque token an asynchronous provider hands back with
	// ErrCheckInProgress. The pipeline persists it and returns it on the next
	// Check; nothing outside the adapter interprets it.
	ProviderRef string
	// CheckedAt is when the evidence was formed, as the provider states it. Zero
	// when the provider did not say; a caller must not substitute its own clock
	// for a missing value (see reconcile.go for why).
	CheckedAt time.Time
	// ThreatCategories is provider-specific detail about a condemnation. It is
	// never shown to a user and never a label.
	ThreatCategories []string
	// Reason narrows a non-answer to one of the closed categories in reason.go,
	// when the provider said enough to tell them apart.
	//
	// It exists for one distinction issue #928 made operationally load-bearing:
	// Cloudflare answering ReputationUnknown because it scanned and found
	// nothing, versus answering it because it refused to scan the hostname at
	// all. Both are fail-closed and both are terminal, so neither changes what
	// the pipeline does — but only one of them is a reason to look at the
	// provider, and an operator who cannot separate them reads the fallback as
	// broken.
	//
	// Empty is the normal case. It is never the provider's own words: an adapter
	// normalises to a constant from reason.go before anything leaves it, which
	// is what keeps an attacker-influenceable string out of a metric label.
	Reason string
}

// ErrCheckInProgress is returned by an asynchronous provider whose answer is not
// ready. The accompanying ReputationResult.ProviderRef must be persisted and
// handed back on the next attempt; the caller schedules that attempt itself.
var ErrCheckInProgress = errors.New("url safety: reputation check in progress")

// URLReputationProvider is the one interface the safety pipeline depends on.
//
// Check may be synchronous (a lookup API answers on the first call) or
// asynchronous (a scanner answers ErrCheckInProgress until it has a report).
// The pipeline treats both identically: a result is persisted, an
// ErrCheckInProgress is scheduled again with its ref, and any other error is
// UNAVAILABLE — retryable, and never a clearance.
type URLReputationProvider interface {
	// Name identifies the provider for diagnostics. Closed set, decided by the
	// adapter.
	Name() string
	// Check asks about a canonical URL. providerRef is empty on the first
	// attempt and whatever the previous ErrCheckInProgress carried afterwards.
	Check(ctx context.Context, canonicalURL, providerRef string) (ReputationResult, error)
}

// ProviderCloudflareURLScanner is the Name() of the one adapter that exists.
const ProviderCloudflareURLScanner = "cloudflare_url_scanner"

// Name satisfies URLReputationProvider.
func (c *CloudflareScanner) Name() string { return ProviderCloudflareURLScanner }

// Check adapts the submit-then-poll scanner to the synchronous-looking
// contract.
//
// With no ref the URL is submitted and the scan id comes back as the ref under
// ErrCheckInProgress. With a ref the report is read: still running is
// ErrCheckInProgress again with the same ref; finished-without-verdict is
// ReputationUnknown; a usable report is SAFE or MALICIOUS. Every other outcome
// is ErrUnavailable, which the pipeline retries and never persists as a verdict.
//
// This is the only place Cloudflare's two-step shape is visible above the
// scanner itself; the strict verdict rules stay in verdictFromReport.
func (c *CloudflareScanner) Check(
	ctx context.Context, canonicalURL, providerRef string,
) (ReputationResult, error) {
	result := ReputationResult{Provider: c.Name()}
	if strings.TrimSpace(providerRef) == "" {
		scanID, err := c.SubmitScan(ctx, canonicalURL)
		if err != nil {
			return result, err
		}
		result.ProviderRef = scanID
		return result, ErrCheckInProgress
	}
	result.ProviderRef = providerRef
	verdict, evidence, refusal, err := c.scanReport(ctx, providerRef)
	switch {
	case errors.Is(err, ErrScanPending):
		return result, ErrCheckInProgress
	case errors.Is(err, ErrScanInconclusive):
		result.Verdict, result.CheckedAt = ReputationUnknown, evidence
		result.Reason = refusal
		return result, nil
	case err != nil:
		return result, err
	}
	result.CheckedAt = evidence
	result.Verdict, err = reputationFromVerdict(verdict)
	return result, err
}

// reputationFromVerdict maps the scanner's Verdict onto the provider contract.
// Anything that is not an explicit clearance or condemnation is refused as
// unavailable rather than translated into a weaker answer.
func reputationFromVerdict(verdict Verdict) (ReputationVerdict, error) {
	switch verdict {
	case VerdictSafe:
		return ReputationSafe, nil
	case VerdictMalicious:
		return ReputationMalicious, nil
	default:
		return "", ErrUnavailable
	}
}

// LegacyVerdict maps a reputation answer back onto the Verdict vocabulary the
// durable stores still persist: SAFE and MALICIOUS unchanged, UNKNOWN as
// inconclusive. It exists so the adoption of the provider contract does not
// require a rewrite of every row that already exists.
func (v ReputationVerdict) LegacyVerdict() Verdict {
	switch v {
	case ReputationSafe:
		return VerdictSafe
	case ReputationMalicious:
		return VerdictMalicious
	default:
		return VerdictInconclusive
	}
}
