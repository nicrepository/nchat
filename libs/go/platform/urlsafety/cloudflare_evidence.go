package urlsafety

import (
	"context"
	"errors"
	"strings"
	"time"
)

// Reusing a scan the provider already has, for the exact URL (issue #928).
//
// # Why this is a separate operation from FindRecentScan
//
// FindRecentScan answers one question: "did the submission whose outcome I lost
// actually reach the provider?" Its filters are about *our own attempt* — the
// scan must not predate it, and the newest survivor is taken because that is
// the one the attempt most likely produced. Widening it into a general lookup
// would silently change what reconciliation means, and reconciliation is what
// stands between a lost response and a second billed scan.
//
// This answers a different question: "does the provider already hold evidence
// about this exact URL that is fresh enough to act on?" Different filters, a
// different age rule, and — crucially — it reads a full report and applies the
// verdict rules, because it may produce a clearance and reconciliation may not.
//
// # Why it runs before a submission
//
// The refusal that motivated issue #928 is a hostname budget: Cloudflare
// declines to scan a hostname somebody scanned recently. A POST into that
// refusal is spent for nothing, and repeating it is the resubmission storm this
// package has always refused to have. Asking first turns the most common case —
// "the hostname was scanned recently because *this* URL was scanned recently" —
// from a dead end into an answer.
//
// # What a search result is never allowed to be
//
// A clearance. The search answer carries a summarised verdict field; ScanRecord
// has no place to put it and nothing here reads it. A candidate yields a scan
// *id*, and the id is then read through the ordinary result endpoint, whose
// strict checks — identity, task.success, hasVerdicts, malicious present — are
// the single place a provider answer becomes a fact this deployment acts on.
// malicious=false on its own is not a clearance here any more than it is there.

// ErrNoReusableEvidence reports that the provider holds nothing about this exact
// URL that may be acted on: no candidate, or a candidate whose report is stale,
// still running, unusable or about a different URL.
//
// Its own error, and deliberately not ErrUnavailable: nothing failed. The
// caller's next step is to submit a scan, which is exactly what it would do if
// this path did not exist, so treating it as a failure would turn the ordinary
// case into a retry.
var ErrNoReusableEvidence = errors.New("url safety: no reusable scan evidence for this url")

// ReusableEvidence is a verdict recovered from a scan the provider already had,
// together with when the provider says it was formed.
type ReusableEvidence struct {
	// Verdict is Safe or Malicious and never anything else: the constructors
	// below return ErrNoReusableEvidence rather than an unusable verdict.
	Verdict Verdict
	// ObservedAt is the provider's own statement of when the scan concluded.
	// Never a local clock reading — see reconcile.go for what inventing one
	// costs — and never zero on a successful return.
	ObservedAt time.Time
	// UUID is the scan the verdict was read from, for the caller to persist as
	// the ref the verdict is bound to.
	UUID string
}

// FindReusableEvidence looks for a usable verdict the provider already holds for
// exactly canonicalURL, no older than maxAge.
//
// Every filter, and what each one closes:
//
//   - the search is for the exact canonical URL, never a hostname. A hostname
//     match would let any page on a domain clear any other, which is the
//     granularity RF-21 exists to refuse;
//   - a candidate must carry a uuid, must have been submitted unlisted (this
//     client never submits public, so a public scan is somebody else's), and
//     its reported url must canonicalize to the same URL;
//   - the report is then read in full. Its task.uuid must be the one requested
//     *and* its task.url must canonicalize to the same URL — the second check
//     exists only on this path, because here the id came from a search rather
//     than from a submission this deployment made, so the report has to prove
//     its subject rather than have it assumed;
//   - the verdict comes from verdictFromReport, shared with polling. Finished
//     without verdicts is not a clearance, malicious=false alone is not a
//     clearance, and a scan still running is not evidence yet;
//   - the evidence must be no older than maxAge, measured from the provider's
//     own time. Stale evidence authorises nothing.
//
// Anything that fails is ErrNoReusableEvidence, except a genuine transport or
// provider failure, which is reported as itself so the caller can tell "there
// is nothing to reuse" from "I could not ask".
func (c *CloudflareScanner) FindReusableEvidence(
	ctx context.Context, canonicalURL string, maxAge time.Duration,
) (ReusableEvidence, error) {
	return c.findReusableEvidenceAt(ctx, canonicalURL, maxAge, time.Now())
}

// findReusableEvidenceAt is FindReusableEvidence with the clock supplied, so a
// test can age evidence without waiting.
func (c *CloudflareScanner) findReusableEvidenceAt(
	ctx context.Context, canonicalURL string, maxAge time.Duration, now time.Time,
) (ReusableEvidence, error) {
	if maxAge <= 0 {
		return ReusableEvidence{}, ErrNoReusableEvidence
	}
	decoded, err := c.searchScans(ctx, canonicalURL)
	if err != nil {
		return ReusableEvidence{}, err
	}
	candidate, ok := selectReusableCandidate(decoded, canonicalURL, now.Add(-maxAge))
	if !ok {
		return ReusableEvidence{}, ErrNoReusableEvidence
	}
	report, err := c.fetchScanReport(ctx, candidate.UUID)
	switch {
	case errors.Is(err, ErrScanPending):
		// Found, but not finished. Not evidence yet and not a failure either:
		// the caller submits, and a scan already running for this URL is exactly
		// what the hostname budget will refuse anyway — which converges to
		// UNKNOWN rather than to anything permissive.
		return ReusableEvidence{}, ErrNoReusableEvidence
	case err != nil:
		return ReusableEvidence{}, err
	}
	return usableEvidence(report, candidate, canonicalURL, now, maxAge)
}

// usableEvidence applies the report-level checks to a candidate's full report.
//
// Split out so the transport above stays a sequence of steps and every reason a
// report may not be adopted is visible in one place.
func usableEvidence(
	report resultResponse, candidate ScanRecord, canonicalURL string,
	now time.Time, maxAge time.Duration,
) (ReusableEvidence, error) {
	// The report must describe the URL being asked about, proved from the report
	// itself. The search said so too, but a search summary is not what this
	// deployment acts on, and a cached, misrouted or substituted response
	// describing a different page is precisely what this closes.
	reported, err := CanonicalizeURL(strings.TrimSpace(report.Task.URL))
	if err != nil || reported != canonicalURL {
		return ReusableEvidence{}, ErrNoReusableEvidence
	}
	// Shared with the polling path: identity by uuid, task.success present and
	// true, hasVerdicts present and true, malicious present. Nothing weaker.
	verdict, err := verdictFromReport(report, candidate.UUID)
	if err != nil || !verdict.IsFinal() {
		return ReusableEvidence{}, ErrNoReusableEvidence
	}
	observedAt := reportEvidenceTime(report)
	if observedAt.IsZero() {
		// The report did not date itself. The search's submission time is the
		// conservative stand-in: a scan cannot have concluded before it started,
		// so it can only make the evidence look older than it is.
		observedAt = candidate.SubmittedAt
	}
	if observedAt.IsZero() || observedAt.Before(now.Add(-maxAge)) {
		return ReusableEvidence{}, ErrNoReusableEvidence
	}
	if observedAt.After(now) {
		// A provider clock ahead of ours, or a nonsense response. Capped rather
		// than trusted: a future timestamp would mint a lifetime longer than the
		// one this deployment grants.
		observedAt = now
	}
	return ReusableEvidence{Verdict: verdict, ObservedAt: observedAt, UUID: candidate.UUID}, nil
}

// selectReusableCandidate picks the newest scan of exactly this URL that is
// inside the freshness window.
//
// It reuses eligibleScan, so identity, visibility and uuid presence are decided
// by the same code reconciliation uses — the rules that say what "one of our
// scans of this URL" means do not get a second implementation. What differs is
// only the age bound handed in: an evidence window rather than the start of an
// outstanding attempt.
func selectReusableCandidate(
	response searchResponse, canonicalURL string, earliest time.Time,
) (ScanRecord, bool) {
	var best ScanRecord
	for _, result := range response.Results {
		task := result.Task
		record, ok := eligibleScan(
			task.UUID, task.URL, task.Time, task.Visibility, canonicalURL, earliest)
		if !ok {
			continue
		}
		if best.UUID == "" || record.SubmittedAt.After(best.SubmittedAt) {
			best = record
		}
	}
	return best, best.UUID != ""
}
