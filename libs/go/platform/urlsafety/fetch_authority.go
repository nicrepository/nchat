package urlsafety

import (
	"strconv"
	"time"
)

// The global "may this deployment fetch this URL?" answer (issue #135, CQ-002).
//
// # The invariant
//
// Once any NChat component persists a MALICIOUS verdict for a canonical URL, no
// later server-side fetch of that URL may be authorised — by any component, at
// any time, regardless of what that component's own verdict row says.
//
// Before this existed, chat.link_scans and files.link_scans were independent
// authorities with independent lifetimes, so a URL chat had just proven malicious
// could still be fetched by file-service on the strength of a SAFE row that had
// not expired yet. Eventual consistency is not an answer to that: the request the
// verdict exists to prevent is the very next one.
//
// # How it is enforced
//
// Two writes, in one statement, alongside the verdict itself:
//
//  1. an insert into files.link_fetch_denylist — the durable authority. New code
//     consults it before authorising any fetch, so even a later re-scan that
//     comes back SAFE cannot re-open the URL;
//  2. an expiry of the files.link_scans row, if there is one. That is what makes
//     the invalidation visible to an *old* file-service pod during a rolling
//     deployment: old code reads `state = 'done' AND verdict_expires_at > now()`,
//     and an expired row reads as absent, which is refusal.
//
// Both travel in the same statement as the verdict write, so there is no window
// in which the verdict is durable and the invalidation is not. If the statement
// fails, nothing is recorded and the row stays where it was — which is
// inconclusive or pending, and therefore still refuses every fetch. Fail-closed
// in every ordering.
//
// # Why the SQL text lives in this package
//
// It is a string constant and brings no driver dependency, and it is here for
// the same reason the verdict rules are: chat-service and file-service both
// execute it, `internal/` packages cannot be shared between them, and two copies
// of a security-critical statement is how the two doors start answering
// differently. One definition, executed by both.
//
// # Monotonic on purpose
//
// Insert-only. Nothing in the application removes a row, so there is no ordering
// in which a later SAFE overwrites an earlier MALICIOUS — the requirement that
// the restrictive fact dominates. Lifting a denial is a deliberate operator
// action (a DELETE, after whatever revalidation they judge sufficient), and
// making it awkward is the point.

// DenylistSources are the components allowed to record a condemnation. A closed
// set, mirrored by the CHECK constraint on the table. It is a diagnostic field
// and never a metric label.
const (
	DenylistSourceChat  = "chat"
	DenylistSourceFiles = "files"
)

// InvalidateFetchAuthoritySQL is the data-modifying CTE prelude that records a
// condemnation globally.
//
// It is a fragment rather than a function because it has to run *inside* the same
// statement as the caller's own verdict UPDATE — two statements would be two
// failure points and a window between them, and the whole requirement is that the
// invalidation is durable before the condemnation is considered recorded.
//
// The arguments are SQL *expressions*, not placeholders, because the two callers
// hold different things: chat-service knows the canonical URL directly, while
// file-service holds only a digest and reads the URL back out of the row it is
// updating. Both are values the caller controls in its own source — nothing user
// supplied is ever interpolated here.
//
// If urlExpr yields NULL the insert violates NOT NULL and the whole statement
// fails, so the verdict is not recorded either. That is deliberate: a
// condemnation that cannot be published globally must not be recorded locally,
// or the two stores would disagree in exactly the direction this exists to
// prevent. The caller reports the error and its worker retries.

// sourceRelation optionally makes the invalidation consume rows returned by the
// caller's own compare-and-set. With it, a CAS that matched nothing supplies no
// row to either write below, so a stale worker cannot create a global denial for
// a verdict it failed to persist locally. Existing callers that already gate the
// whole statement may omit it.
func InvalidateFetchAuthoritySQL(digestExpr, urlExpr, sourceExpr string, sourceRelation ...string) string {
	from, guard := "", ""
	if len(sourceRelation) == 1 {
		from = "\n\t\tFROM " + sourceRelation[0]
		guard = "\n\t\t   AND EXISTS (SELECT 1 FROM " + sourceRelation[0] + ")"
	}
	return `
	denied AS (
		INSERT INTO files.link_fetch_denylist (url_digest, canonical_url, source)
		SELECT ` + digestExpr + `, ` + urlExpr + `, ` + sourceExpr + from + `
		ON CONFLICT (url_digest) DO NOTHING
		RETURNING url_digest
	),
	-- Expiring the per-service row is what reaches an *old* pod during a rolling
	-- update: its gate is "done and not expired", so an expired row is absent, and
	-- absent is refusal. Scoped to 'done' because that is the only state that can
	-- currently authorise a fetch, and because inconclusive rows are protected by
	-- the 000009 trigger, which this must not fight.
	expired AS (
		UPDATE files.link_scans
		   SET verdict_expires_at = now(), updated_at = now()
		 WHERE url_digest = ` + digestExpr + `
		   AND state = 'done'
		   AND verdict_expires_at > now()` + guard + `
		RETURNING url_digest
	)`
}

// ReconcileLeaseTTL is how long one service's exclusive right to re-read a URL
// at the provider lasts (issue #135, CQ-003).
//
// A minute, matching the per-URL manual cooldown already in chat-service, and
// chosen against what it is protecting rather than against latency: a
// reconciliation reads a scan that has already *finished*, so a second read
// inside the same minute cannot return anything the first one did not. The lease
// is not a mutex over a long operation — a search and a report read take well
// under a second — it is a floor on how often the deployment as a whole is
// willing to spend a provider attempt on one address.
//
// Expiry is the only release. There is no unlock path, deliberately: a worker
// that crashes mid-reconciliation must not leave the URL locked out, and one
// that finishes early gains nothing by releasing, because the next attempt is
// not due for minutes anyway (ReconcileSchedule).
const ReconcileLeaseTTL = time.Minute

// AcquireReconcileLeaseSQL is the data-modifying CTE that takes the cross-service
// reconciliation lease for a set of URLs, and yields only the ones it won.
//
// # Why a fragment, and why it gates the claim rather than the provider call
//
// The requirement is that a service blocked by the other one does not *consume a
// provider attempt* — and in both services the attempt budget is spent by the
// claim statement, which increments the row's reconcile_attempts as it selects
// it. So checking the lease afterwards, in Go, would be too late: the budget
// would already be gone and the URL would be pushed out to its next backoff step
// having learned nothing. Composing the acquisition into the claim makes winning
// the lease a precondition of being claimed at all, in one statement, and a URL
// the other service is already reading is simply not returned.
//
// # Why an upsert with a WHERE, and not a check followed by an insert
//
// `ON CONFLICT ... DO UPDATE ... WHERE leased_until <= now()` re-reads the
// conflicting row's latest version under the row lock the conflict already took,
// which is what makes it a compare-and-set rather than a read-then-write. Two
// services attempting the same digest in the same instant produce one winner and
// one row that is simply absent from the RETURNING set. A prior SELECT would have
// the classic gap between the check and the write, which under two independent
// deployments is not a theoretical race — it is the exact minute this exists to
// close.
//
// An expired row is updated in place rather than deleted and reinserted, so there
// is no window in which the address has no row at all.
//
// digestExpr, urlExpr and holderExpr are code literals or column references from
// the surrounding query, never user input; sourceRelation is the CTE or table the
// candidate rows come from.
func AcquireReconcileLeaseSQL(sourceRelation, digestExpr, urlExpr, holderExpr string) string {
	return `
	leased AS (
		INSERT INTO files.link_reconcile_leases (url_digest, canonical_url, leased_until, leased_by)
		SELECT ` + digestExpr + `, ` + urlExpr + `,
		       now() + interval '` + strconv.Itoa(int(ReconcileLeaseTTL.Seconds())) + ` seconds',
		       ` + holderExpr + `
		FROM ` + sourceRelation + `
		ORDER BY ` + digestExpr + `
		ON CONFLICT (url_digest) DO UPDATE
		   SET leased_until  = EXCLUDED.leased_until,
		       canonical_url = EXCLUDED.canonical_url,
		       leased_by     = EXCLUDED.leased_by,
		       updated_at    = now()
		 WHERE files.link_reconcile_leases.leased_until <= now()
		RETURNING url_digest
	)`
}

// ReconcileLeaseAvailablePredicate is the cheap pre-filter for the candidate
// selection, so a URL the other service is currently reading is not picked into
// a batch it would then be dropped from — which, with a LIMIT on the batch, would
// let one contended address starve the pass.
//
// It is an optimisation and never the guarantee. Between this predicate and the
// upsert above another service may take the lease; the upsert's WHERE is what
// makes that lose.
func ReconcileLeaseAvailablePredicate(digestExpr string) string {
	return `NOT EXISTS (
		SELECT 1 FROM files.link_reconcile_leases l
		WHERE l.url_digest = ` + digestExpr + `
		  AND l.leased_until > now()
	)`
}
