package urlsafety

import (
	"context"
	"errors"
	"time"
)

// Reconciling a URL whose scan finished without a usable verdict.
//
// # Why this is not a resubmission
//
// A scan that reached VerdictInconclusive is terminal: the provider confirmed it
// finished and confirmed it produced nothing. Cloudflare has no idempotency
// token, so a second POST for the same URL is a second billed scan — and the
// real production case this exists for is a refusal that says the hostname was
// *already* scanned too recently, which means another POST is the one thing
// guaranteed not to help.
//
// What is available is the account-scoped search over scans this deployment's own
// credentials created. So reconciliation is read-only at the provider: find a
// scan for exactly this URL, then read that scan through the ordinary result
// endpoint. Nothing here submits, and there is no parameter that could make it.
//
// # Search is not a verdict authority
//
// The search answer carries a summarised verdict field. It is deliberately never
// read: ScanRecord has no verdict, so there is no value here that could be
// mistaken for one. A candidate only ever yields a scan *id*, and the id is then
// passed to GetScanReport, which reads the full report through the same strict
// path a first-hand poll uses — the single place in this package where a provider
// answer becomes a fact this deployment acts on, and therefore the single place
// the strict checks live (identity, task.success, hasVerdicts, malicious
// presence).
// A weaker second route to a clearance is exactly what this split prevents.

// reconcileLookback bounds how far back a reconciliation will adopt one of this
// deployment's own scans.
//
// It has to be much wider than VerdictTTL to be useful at all: the URL being
// reconciled is one the provider already refused to scan again, so the only
// candidate that can exist is an older one.
//
// Widening it does not widen trust, because the lookback is not the freshness
// rule. A verdict adopted here is dated from the provider's own observation time
// (ScanEvidence.ObservedAt), not from the moment of adoption, so its clearance
// expires VerdictTTL after the scan ran rather than VerdictTTL from now. A safe
// report already older than VerdictTTL yields no clearance at all — Reconcile
// returns ErrEvidenceExpired. This bound therefore only decides which scans are
// worth *looking* at.
//
// ponytail: a full day is a stated ceiling, not a measured one. A report read
// today from a scan Cloudflare ran yesterday describes yesterday's page. That is
// accepted because the alternative is leaving the URL permanently unresolvable,
// and because the direction of the error is bounded in both senses — a stale
// malicious report still blocks, permanently and across services, while a stale
// safe report buys only whatever is left of VerdictTTL measured from the scan
// itself, which for most of this window is nothing. Narrow it if a case shows up
// where a day is too long.
const reconcileLookback = 24 * time.Hour

// ScanEvidence is a verdict together with when the provider produced it.
//
// The second field is the whole point of this type. A verdict is evidence, and
// evidence has an age: reconciliation may adopt a scan that ran hours ago, and
// dating the resulting clearance from the moment it was *adopted* would hand a
// full fresh lifetime to stale evidence. So the age travels with the verdict all
// the way into the durable stores, which compute expiry from it.
type ScanEvidence struct {
	Verdict Verdict
	// ObservedAt is when the provider says the scan concluded — never a local
	// clock reading, and never the moment of adoption. It is always set on a
	// successful Reconcile: the callers below refuse to return a verdict they
	// cannot date.
	ObservedAt time.Time
	// CandidateFound reports that the account-scoped search produced a scan whose
	// canonical URL matched exactly, before its report was read.
	//
	// Set on the error returns too, which is the point: it separates "there was
	// nothing to read" from "there was, and reading it did not yield a clearance",
	// and those need different responses from an operator. It is the only thing in
	// this struct that is meaningful on a failure.
	CandidateFound bool
}

// Age reports how old the evidence is relative to now.
func (e ScanEvidence) Age(now time.Time) time.Duration { return now.Sub(e.ObservedAt) }

// ExpiresAt is when a clearance based on this evidence stops being usable.
//
// Computed from the evidence, not from adoption, which is what makes a scan from
// 23 hours ago worth nothing under a 15-minute TTL instead of worth another full
// 15 minutes.
func (e ScanEvidence) ExpiresAt() time.Time { return e.ObservedAt.Add(VerdictTTL) }

// scanReporter is the provider client that can date its own answers.
//
// Separate from Scanner because dating a report is only needed where evidence
// may be old — reconciliation — and a provider client that cannot do it is still
// a working deployment for ordinary polling.
type scanReporter interface {
	GetScanReport(ctx context.Context, scanID string) (Verdict, time.Time, error)
}

// ErrEvidenceExpired reports a candidate whose verdict is real but too old to
// clear anything.
//
// Its own error because the remedy differs from every neighbour: the scan was
// found, the report was read, the checks passed, and the answer is still not a
// clearance — because it describes a page as it was longer ago than a clearance
// is allowed to last. A caller keeps the row inconclusive, exactly as it would
// for a report with no verdict at all.
var ErrEvidenceExpired = errors.New("url safety: scan verdict is older than its usable lifetime")

// Reconcile reads the verdict of a scan this deployment already created for
// canonicalURL, without submitting anything.
//
// The outcomes, and what each one means to a caller that is holding an
// inconclusive row:
//
//   - VerdictSafe or VerdictMalicious with a nil error: a candidate scan existed
//     and its full report passed every check a first-hand poll applies. This is the only
//     result that may change the stored state, and it may change it in either
//     direction. ScanEvidence.ObservedAt carries the provider's own time, and the
//     caller must date the stored verdict from it;
//   - ErrEvidenceExpired: a real clearance, too old to be one. Stay inconclusive;
//   - ErrNotCheckable: no candidate. Nothing eligible was found for exactly this
//     URL, which is the ordinary answer and the expected one for a hostname the
//     provider refused because somebody else's account scanned it. Stay
//     inconclusive;
//   - ErrScanInconclusive: a candidate existed and its report still carries no
//     usable verdict. Stay inconclusive;
//   - ErrScanPending: a candidate existed and is still running. Stay
//     inconclusive; asking again later is free;
//   - ErrSearchUnsupported: this deployment's provider client cannot search, or
//     cannot date a report, so reconciliation is not available at all;
//   - ErrUnavailable or a context error: the question could not be asked.
//
// Every one of those except the first leaves the caller exactly where it was, and
// none of them is ever a reason to submit. Fail-closed for server-side fetch is
// preserved by construction: only an explicit VerdictSafe with live evidence can
// ever become a clearance.
func (s *Service) Reconcile(ctx context.Context, canonicalURL string) (ScanEvidence, error) {
	return s.reconcileAt(ctx, canonicalURL, time.Now())
}

// reconcileAt is Reconcile with the clock supplied, so a test can age evidence
// without waiting.
func (s *Service) reconcileAt(
	ctx context.Context, canonicalURL string, now time.Time,
) (ScanEvidence, error) {
	if s.scanner == nil {
		return ScanEvidence{}, ErrUnavailable
	}
	// A client that cannot date its answers cannot be reconciled from. Refusing is
	// the only safe reading: the alternative is adopting a verdict of unknown age,
	// and an undated clearance is indistinguishable from an infinitely fresh one.
	reporter, canDate := s.scanner.(scanReporter)
	if !canDate {
		return ScanEvidence{}, ErrSearchUnsupported
	}

	record, _, err := s.FindRecentScan(ctx, canonicalURL, now.Add(-reconcileLookback))
	if err != nil {
		// Passed through unchanged, including ErrNotCheckable. Each one has a
		// different remedy at the call site and collapsing them here would make
		// "there is no such scan" indistinguishable from "the search was
		// throttled" — the confusion that, in the submission path, is how a
		// duplicate scan gets bought.
		return ScanEvidence{}, err
	}
	if record.UUID == "" {
		// A match with no id is not a candidate. Defensive: selectReconcilableScan
		// already refuses one, and this refuses it again rather than handing an
		// empty id to the result endpoint.
		return ScanEvidence{}, ErrNotCheckable
	}
	// From here on a candidate exists: the search matched, and the reported URL
	// canonicalised to exactly the one asked about. Every return below carries the
	// flag, so an operator can tell a search that found nothing from a candidate
	// whose report did not clear anything.
	found := ScanEvidence{CandidateFound: true}

	// The verdict comes from the full report and from nowhere else. The identity
	// check against this very uuid, task.success and hasVerdicts all live in
	// verdictFromReport, which this shares with the ordinary polling path.
	verdict, observedAt, err := reporter.GetScanReport(ctx, record.UUID)
	if ctx.Err() != nil {
		return found, ctx.Err()
	}
	if err != nil {
		return found, err
	}
	if !verdict.IsFinal() {
		// Unreachable through verdictFromReport, which already refuses this. Kept
		// so a future change cannot let a non-clearance leak out of here.
		return found, ErrUnavailable
	}

	evidence := ScanEvidence{Verdict: verdict, ObservedAt: observedAt, CandidateFound: true}
	if observedAt.IsZero() {
		// The report carried no usable time. The scan's submission time is the
		// conservative stand-in: a scan cannot have concluded before it started, so
		// using it can only ever make the evidence look *older* than it is, never
		// younger. That is the safe direction.
		evidence.ObservedAt = record.SubmittedAt
	}
	if evidence.ObservedAt.IsZero() {
		// Neither the report nor the search could date it. There is no honest
		// freshness to assign, and inventing one is the finding.
		return found, ErrEvidenceExpired
	}
	if evidence.ObservedAt.After(now) {
		// A provider clock ahead of ours, or a nonsense response. Capped rather than
		// trusted: a future timestamp would mint a clearance lasting longer than
		// VerdictTTL, which is a lifetime nothing in this system is allowed to grant.
		evidence.ObservedAt = now
	}

	// A clearance is only a clearance while it is live, and its life is measured
	// from the provider's own scan time — never from this moment.
	//
	// Malicious is deliberately exempt. It is a restriction rather than a
	// permission, so its retention is the storage layer's policy: both stores keep
	// a condemnation for a full lifetime measured from adoption, precisely so that
	// old evidence of harm is not discarded by ageing out of a freshness window.
	// Retaining a refusal longer grants nothing.
	if verdict == VerdictSafe && !evidence.ExpiresAt().After(now) {
		return found, ErrEvidenceExpired
	}

	// Seeded into the in-process cache with the lifetime it has *left*, not a
	// fresh one. Remember refuses a non-positive remainder, so expired evidence
	// cannot enter the cache at all.
	s.Remember(canonicalURL, verdict, evidence.ExpiresAt().Sub(now))
	s.observe(resultFor(verdict))
	return evidence, nil
}

// ReconcileOutcome classifies one reconciliation for a metric, without the
// caller having to re-derive the mapping from errors.
//
// The values are the closed label set — see the Reconcile* constants in
// pipeline_metrics.go. Nothing derived from the URL, the scan id or the provider's
// message can reach a label through here: the only input is an error this package
// produced.
func ReconcileOutcome(verdict Verdict, err error) string {
	switch {
	case err == nil && verdict == VerdictSafe:
		return ReconcileVerdictSafe
	case err == nil && verdict == VerdictMalicious:
		return ReconcileVerdictMalicious
	case errors.Is(err, ErrNotCheckable):
		return ReconcileNoCandidate
	case errors.Is(err, ErrScanInconclusive),
		errors.Is(err, ErrScanPending),
		errors.Is(err, ErrEvidenceExpired):
		return ReconcileStillInconclusive
	case errors.Is(err, ErrSearchUnsupported):
		return ReconcileUnsupported
	default:
		return ReconcileProviderError
	}
}
