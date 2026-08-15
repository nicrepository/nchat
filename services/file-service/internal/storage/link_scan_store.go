package storage

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/nicrepository/nchat/libs/go/platform/urlsafety"
	"github.com/nicrepository/nchat/services/file-service/internal/service"
)

// PGXLinkScanStore is file-service's durable RF-21 queue (files.link_scans).
//
// It exists because the preview path could previously only *start* a scan: it
// submitted the URL, answered "pending", and discarded the provider's scan id.
// Nothing polled it, nothing survived a restart, and every retry submitted the
// same URL again. This is the state that closes that loop.
//
// It is deliberately shaped like PGXScanStore, the antimalware queue next door:
// a claim with a lease, an attempt counter that stretches the retry, and a
// partial index that is empty once everything is decided. Two queues that look
// alike are two queues an operator only has to learn once.
type PGXLinkScanStore struct {
	pool Pool
}

func NewPGXLinkScanStore(pool Pool) *PGXLinkScanStore {
	return &PGXLinkScanStore{pool: pool}
}

var _ service.LinkScanStore = (*PGXLinkScanStore)(nil)

const (
	// linkScanLease is how long a claimed row is left alone.
	//
	// The worker takes one row at a time, immediately before processing it, so
	// the lease covers exactly one provider exchange. urlsafety's own request
	// ceiling is 10s, and the rest is margin for the database round trips around
	// it. A lease shorter than the work it protects is how a row gets reclaimed
	// and submitted twice.
	linkScanLease = 60 * time.Second

	// linkScanBackoffSteps caps the retry stretch, so a provider outage settles
	// at one attempt per five minutes instead of a tight loop. There is no
	// attempts ceiling: a URL nothing can decide is a URL that is still not
	// cleared, and abandoning it would eventually mean serving it.
	linkScanBackoffSteps = 5

	// maxLinkScanAttempts caps the stored counter so a long outage cannot
	// overflow the SMALLINT and make the claim statement itself fail.
	maxLinkScanAttempts = 32767
)

// digestOf is the primary key: SHA-256 of the canonical URL.
//
// Keying by digest keeps the path and query out of the index, and gives every
// row a fixed-width key regardless of how long the URL is. The URL itself is
// still stored — the worker has to submit it after a restart, and a digest
// cannot be reversed — but nothing else has to carry it around.
func digestOf(canonicalURL string) []byte {
	sum := sha256.Sum256([]byte(canonicalURL))
	return sum[:]
}

// LoadVerdict returns a live verdict for a canonical URL.
//
// Live is decided here: a row whose verdict_expires_at has passed is reported as
// absent, so a lapsed clearance behaves exactly like a URL nobody ever scanned.
// Reputation changes in both directions and a cache that never expires is a
// control that only gets weaker.
func (s *PGXLinkScanStore) LoadVerdict(
	ctx context.Context, canonicalURL string,
) (urlsafety.Verdict, bool, error) {
	var stored string
	err := s.pool.QueryRow(ctx, `
		SELECT verdict
		FROM files.link_scans
		WHERE url_digest = $1 AND state = 'done' AND verdict_expires_at > now()`,
		digestOf(canonicalURL),
	).Scan(&stored)
	if errors.Is(err, pgx.ErrNoRows) {
		return urlsafety.VerdictUnknown, false, nil
	}
	if err != nil {
		return urlsafety.VerdictUnknown, false, fmt.Errorf("load link verdict: %w", err)
	}
	// Only a value this package recognises becomes a verdict. A corrupted or
	// future status is treated as absent rather than as a clearance.
	verdict := urlsafety.Verdict(stored)
	if !verdict.IsFinal() {
		return urlsafety.VerdictUnknown, false, nil
	}
	return verdict, true, nil
}

// AdmitScan reserves capacity for a URL that needs new provider work, and
// queues it — in one transaction.
//
// It replaced an unconditional EnsureScan for the reason the security review
// gave: creating a job is spending money at a third party, and nothing counted
// how much. A client asking for previews of URLs nobody has seen before could
// introduce unbounded new scans, and the per-request limiter did not touch that
// because the unit it counts is a request, not a URL.
//
// The intent is still durable before anything is sent to the provider, which is
// the other property this call has always had: a crash between "provider
// accepted" and "id stored" leaves a row that can be recovered rather than a
// scan nobody knows about. It is also still the deduplication point — two
// previews of the same URL at the same moment both call this, the primary key
// keeps one row, and exactly one scan is submitted.
//
// A URL that is already answered or already queued costs nothing and is admitted
// regardless of how spent the budget is. That is the whole point of counting new
// canonical URLs rather than requests: repeatedly previewing the same link is
// free, because it is free for the provider too.
//
// A decided row is left alone unless its verdict has expired. Re-opening one on
// request would let anyone unblock a malicious verdict by asking for a preview.
func (s *PGXLinkScanStore) AdmitScan(
	ctx context.Context, canonicalURL string, capacity service.LinkScanCapacity,
) (service.LinkScanAdmission, error) {
	txPool, ok := TransactionPoolFrom(s.pool)
	if !ok {
		// No transaction means no atomic reservation, and a budget that cannot be
		// reserved atomically is not a budget. Refused rather than degraded: the
		// alternative silently spends at the provider under exactly the
		// concurrency an attacker would choose.
		return service.LinkScanAdmission{}, fmt.Errorf("admit link scan: %w", errNoTransactionPool)
	}
	tx, err := txPool.Begin(ctx)
	if err != nil {
		return service.LinkScanAdmission{}, fmt.Errorf("admit link scan: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	digest := digestOf(canonicalURL)
	var state string
	var expired bool
	err = tx.QueryRow(ctx, `
		SELECT state, (state = 'done' AND verdict_expires_at <= now())
		FROM files.link_scans
		WHERE url_digest = $1
		FOR UPDATE`, digest,
	).Scan(&state, &expired)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		// Brand new: this one costs a scan.
	case err != nil:
		return service.LinkScanAdmission{}, fmt.Errorf("classify link scan cost: %w", err)
	case state != "done" || !expired:
		// Already queued, already running, or answered and still fresh. Free.
		if err := tx.Commit(ctx); err != nil {
			return service.LinkScanAdmission{}, fmt.Errorf("admit link scan: %w", err)
		}
		return service.LinkScanAdmission{Result: service.AdmissionAllowed}, nil
	}

	admission, err := reserveScanCapacity(ctx, tx, capacity)
	if err != nil || !admission.Allowed() {
		return admission, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO files.link_scans (url_digest, canonical_url)
		VALUES ($1, $2)
		ON CONFLICT (url_digest) DO UPDATE
		   SET state = 'submit_pending', scan_uuid = NULL, verdict = NULL,
		       verdict_expires_at = NULL, attempts = 0, next_attempt_at = NULL,
		       lease_until = NULL, submit_attempt_started_at = NULL,
		       updated_at = now()
		 WHERE files.link_scans.state = 'done'
		   AND files.link_scans.verdict_expires_at <= now()`,
		digest, canonicalURL,
	); err != nil {
		return service.LinkScanAdmission{}, fmt.Errorf("ensure link scan: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return service.LinkScanAdmission{}, fmt.Errorf("admit link scan: %w", err)
	}
	return service.LinkScanAdmission{NewScanCost: 1, Result: service.AdmissionAllowed}, nil
}

// reserveScanCapacity applies the backlog cap and the service-wide new-URL
// budget.
//
// The budget is service-wide rather than per tenant, and that is a limitation
// stated rather than papered over: a preview request carries no workspace this
// service can trust — it is fetched for a link, not for a tenant — and inventing
// one from a header would be a tenant boundary that any client could choose.
// Per-caller fairness stays where it can actually be enforced, in the request
// limiter; this bounds what the service as a whole may spend.
func reserveScanCapacity(
	ctx context.Context, tx pgx.Tx, capacity service.LinkScanCapacity,
) (service.LinkScanAdmission, error) {
	if capacity.MaxPendingJobs > 0 {
		var pending int
		if err := tx.QueryRow(ctx,
			`SELECT count(*) FROM files.link_scans WHERE state <> 'done'`,
		).Scan(&pending); err != nil {
			return service.LinkScanAdmission{}, fmt.Errorf("read link scan backlog: %w", err)
		}
		if pending+1 > capacity.MaxPendingJobs {
			return service.LinkScanAdmission{NewScanCost: 1, Result: service.AdmissionBacklog}, nil
		}
	}
	reserved, err := reserveWindowBudget(ctx, tx, budgetScopeService, budgetKeyGlobal,
		1, capacity.NewURLBudget, capacity.BudgetWindow)
	if err != nil {
		return service.LinkScanAdmission{}, err
	}
	if !reserved {
		return service.LinkScanAdmission{NewScanCost: 1, Result: service.AdmissionServiceBudget}, nil
	}
	return service.LinkScanAdmission{NewScanCost: 1, Result: service.AdmissionAllowed}, nil
}

// errNoTransactionPool reports a store wired to something that cannot open a
// transaction. Fail closed: an admission that cannot reserve atomically is not
// an admission.
var errNoTransactionPool = errors.New("link scan capacity requires a transactional pool")

// Budget scopes. Never metric labels and never logged.
const (
	budgetScopeService        = "service"
	budgetScopeProviderSubmit = "provider_submit"
	budgetKeyGlobal           = "global"
)

// reserveWindowBudget consumes `cost` from a fixed window, atomically.
//
// One statement with the condition inside it. The read-then-write shape this
// replaces cannot be made correct with concurrent replicas: whatever the gap
// between the SELECT and the UPDATE, two callers fit through it and the window
// ends over its limit.
//
// The window start comes from the database's clock, so replicas that disagree
// about the time still count into the same row. A limit of zero disables the
// budget rather than refusing everything — a control that fails into "nothing
// works" is a control somebody switches off entirely.
func reserveWindowBudget(
	ctx context.Context, tx pgx.Tx, scopeType, scopeKey string,
	cost, limit int, window time.Duration,
) (bool, error) {
	if limit <= 0 || window <= 0 || cost <= 0 {
		return true, nil
	}
	if cost > limit {
		return false, nil
	}
	var used int
	err := tx.QueryRow(ctx, `
		INSERT INTO files.link_scan_budget_usage AS b (scope_type, scope_key, window_start, used)
		VALUES ($1, $2, to_timestamp(floor(extract(epoch FROM now()) / $5) * $5), $3)
		ON CONFLICT (scope_type, scope_key, window_start) DO UPDATE
		   SET used = b.used + $3
		 WHERE b.used + $3 <= $4
		RETURNING b.used`,
		scopeType, scopeKey, cost, limit, window.Seconds(),
	).Scan(&used)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("reserve link scan budget: %w", err)
	}
	return true, nil
}

// ReserveProviderSubmit takes one submission from the deployment-wide provider
// allowance, shared across replicas.
//
// Separate from the new-URL budget because it answers a different question: not
// who may introduce work, but how fast this service may talk to Cloudflare at
// all — including for the retries and revalidations nobody requested.
func (s *PGXLinkScanStore) ReserveProviderSubmit(
	ctx context.Context, limit int, window time.Duration,
) (bool, error) {
	if limit <= 0 || window <= 0 {
		return true, nil
	}
	txPool, ok := TransactionPoolFrom(s.pool)
	if !ok {
		return false, fmt.Errorf("reserve provider submit: %w", errNoTransactionPool)
	}
	tx, err := txPool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("reserve provider submit: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	reserved, err := reserveWindowBudget(ctx, tx, budgetScopeProviderSubmit, budgetKeyGlobal,
		1, limit, window)
	if err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("reserve provider submit: %w", err)
	}
	return reserved, nil
}

// PruneLinkScanBudget drops windows that can no longer be counted into. Bounded
// and lazy, so the table stays small without a scheduled job to operate.
func (s *PGXLinkScanStore) PruneLinkScanBudget(ctx context.Context, olderThan time.Duration) error {
	if olderThan <= 0 {
		return nil
	}
	_, err := s.pool.Exec(ctx, `
		DELETE FROM files.link_scan_budget_usage
		 WHERE (scope_type, scope_key, window_start) IN (
		     SELECT scope_type, scope_key, window_start
		     FROM files.link_scan_budget_usage
		     WHERE window_start < now() - ($1 * interval '1 second')
		     LIMIT 500
		 )`,
		olderThan.Seconds(),
	)
	if err != nil {
		return fmt.Errorf("prune link scan budget: %w", err)
	}
	return nil
}

// claimDueLinkScansQuery leases undecided rows.
//
// FOR UPDATE SKIP LOCKED lets several replicas drain the queue without
// coordinating. The lease is the concurrency control and the retry schedule at
// once: the UPDATE pushes next_attempt_at out by lease × min(attempts, steps) and
// counts the try in the same statement, so a worker that died holds nothing —
// the row is due again once its lease expires — and a provider that keeps failing
// costs geometrically fewer attempts.
const claimDueLinkScansQuery = `
	WITH due AS (
		SELECT ls.url_digest
		FROM files.link_scans ls
		WHERE ls.state <> 'done'
		  AND (ls.next_attempt_at IS NULL OR ls.next_attempt_at <= now())
		  AND (ls.lease_until IS NULL OR ls.lease_until <= now())
		ORDER BY ls.next_attempt_at NULLS FIRST, ls.created_at
		LIMIT $1
		FOR UPDATE SKIP LOCKED
	)
	UPDATE files.link_scans ls
	   SET attempts = LEAST(ls.attempts + 1, $4),
	       lease_until = now() + ($2 * interval '1 second'),
	       next_attempt_at = now() + ($2 * LEAST(ls.attempts + 1, $3) * interval '1 second'),
	       updated_at = now()
	  FROM due
	 WHERE ls.url_digest = due.url_digest
	RETURNING ls.url_digest, ls.canonical_url, ls.state, COALESCE(ls.scan_uuid, ''), ls.attempts,
	          COALESCE(ls.submit_attempt_started_at, 'epoch'::timestamptz),
	          ls.submit_generation`

// ClaimDueScans leases up to batchSize URLs awaiting a verdict.
func (s *PGXLinkScanStore) ClaimDueScans(ctx context.Context, batchSize int) ([]service.LinkScanJob, error) {
	if batchSize <= 0 {
		return nil, nil
	}
	rows, err := s.pool.Query(ctx, claimDueLinkScansQuery,
		batchSize, linkScanLease.Seconds(), linkScanBackoffSteps, maxLinkScanAttempts)
	if err != nil {
		return nil, fmt.Errorf("claim due link scans: %w", err)
	}
	defer rows.Close()

	var jobs []service.LinkScanJob
	for rows.Next() {
		var job service.LinkScanJob
		if err := rows.Scan(&job.URLDigest, &job.CanonicalURL, &job.State, &job.ScanUUID, &job.Attempts,
			&job.SubmitStartedAt, &job.SubmitGeneration); err != nil {
			return nil, fmt.Errorf("scan link scan job: %w", err)
		}
		// 'epoch' stands in for NULL so one scan target serves both; normalised
		// back to the zero time, which is what "no attempt outstanding" reads as.
		if job.SubmitStartedAt.Equal(time.Unix(0, 0).UTC()) {
			job.SubmitStartedAt = time.Time{}
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("claim due link scans: %w", err)
	}
	return jobs, nil
}

// RecordSubmission binds a provider scan id to a row and moves it to polling,
// closing the window BeginSubmit opened.
//
// Compare-and-set on the state *and* the attempt generation. The state check
// alone was not enough: a worker whose lease expired while the provider was
// answering may still be holding an id, and the row may since have been
// re-attempted — matching the generation is what stops that late write from
// overwriting the attempt that replaced it.
func (s *PGXLinkScanStore) RecordSubmission(
	ctx context.Context, urlDigest []byte, scanUUID string, generation int,
) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE files.link_scans
		   SET state = 'polling', scan_uuid = $2, submit_attempt_started_at = NULL,
		       lease_until = NULL, updated_at = now()
		 WHERE url_digest = $1 AND state = 'submitting' AND submit_generation = $3`,
		urlDigest, scanUUID, generation,
	)
	if err != nil {
		return fmt.Errorf("record link scan submission: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return service.ErrLinkScanConflict
	}
	return nil
}

// BeginSubmit records the intent to submit, before the provider is called.
//
// This is the write that makes the window recoverable. After it, a crash at any
// point leaves a row saying "an attempt is outstanding" instead of one
// indistinguishable from a URL nobody ever submitted — which is what made
// recovery resubmit and buy a second scan.
func (s *PGXLinkScanStore) BeginSubmit(
	ctx context.Context, urlDigest []byte, expectedGeneration int,
) (int, error) {
	var generation int
	err := s.pool.QueryRow(ctx, `
		UPDATE files.link_scans
		   SET state = 'submitting',
		       submit_attempt_started_at = now(),
		       submit_generation = submit_generation + 1,
		       updated_at = now()
		 WHERE url_digest = $1
		   AND state = 'submit_pending'
		   AND scan_uuid IS NULL
		   AND submit_generation = $2
		RETURNING submit_generation`,
		urlDigest, expectedGeneration,
	).Scan(&generation)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return 0, service.ErrLinkScanConflict
	case err != nil:
		return 0, fmt.Errorf("begin link scan submit: %w", err)
	}
	return generation, nil
}

// MarkSubmitUncertain moves an outstanding attempt into the state that must
// never be resubmitted from.
//
// Called when the exchange failed or its result could not be written down. Both
// look the same from here — a timeout after acceptance is indistinguishable from
// a refusal — so both become "we do not know", and the recovery path is the
// provider's own search rather than another submission.
func (s *PGXLinkScanStore) MarkSubmitUncertain(
	ctx context.Context, urlDigest []byte, generation int,
) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE files.link_scans
		   SET state = 'submit_uncertain', lease_until = NULL, updated_at = now()
		 WHERE url_digest = $1 AND state = 'submitting' AND submit_generation = $2`,
		urlDigest, generation,
	)
	if err != nil {
		return fmt.Errorf("mark submit uncertain: %w", err)
	}
	return nil
}

// AdoptScanUUID binds a scan id recovered from the provider's search to the
// outstanding attempt that produced it.
//
// The same compare-and-set the ordinary submission uses. Reconciliation recovers
// an *id*; the verdict is still read through the ordinary result path, which is
// where the strictness that decides a clearance lives.
func (s *PGXLinkScanStore) AdoptScanUUID(
	ctx context.Context, urlDigest []byte, scanUUID string, generation int,
) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE files.link_scans
		   SET state = 'polling', scan_uuid = $2, submit_attempt_started_at = NULL,
		       lease_until = NULL, updated_at = now()
		 WHERE url_digest = $1 AND state IN ('submitting', 'submit_uncertain')
		   AND scan_uuid IS NULL AND submit_generation = $3`,
		urlDigest, scanUUID, generation,
	)
	if err != nil {
		return fmt.Errorf("adopt scan uuid: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return service.ErrLinkScanConflict
	}
	return nil
}

// RecordVerdict stores a final verdict, bound to the scan that produced it.
//
// The scan_uuid in the predicate is the compare-and-set the security review
// asked for. A worker that lost its lease may still be holding an answer for a
// scan the row has since replaced; matching the id is what stops that stale
// result from overwriting the current one. Zero rows affected means this worker
// lost, and ErrLinkScanConflict says so rather than pretending the write landed.
func (s *PGXLinkScanStore) RecordVerdict(
	ctx context.Context, urlDigest []byte, scanUUID string, verdict urlsafety.Verdict, ttl time.Duration,
) error {
	if !verdict.IsFinal() {
		return fmt.Errorf("link scan: refusing to store a non-final verdict")
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE files.link_scans
		   SET state = 'done', verdict = $3,
		       verdict_expires_at = now() + ($4 * interval '1 second'),
		       lease_until = NULL, next_attempt_at = NULL, updated_at = now()
		 WHERE url_digest = $1 AND state = 'polling' AND scan_uuid = $2`,
		urlDigest, scanUUID, string(verdict), ttl.Seconds(),
	)
	if err != nil {
		return fmt.Errorf("record link verdict: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return service.ErrLinkScanConflict
	}
	return nil
}

// Backlog reports undecided rows by state and the age of the oldest, for the
// pipeline gauges. Without it a provider outage is invisible: previews simply
// stop resolving, with nothing to point at.
func (s *PGXLinkScanStore) Backlog(ctx context.Context) (map[string]int, time.Duration, error) {
	// 'submitting' and 'submit_uncertain' are reported as one, because they are
	// the same situation seen at different times: an attempt is outstanding and
	// nobody knows whether the provider accepted it. The only difference is
	// whether a lease is still held, which matters to the worker and not to the
	// person reading the gauge. Reporting them together is also what makes this
	// number comparable with chat-service, whose row cannot tell them apart at
	// all.
	rows, err := s.pool.Query(ctx, `
		SELECT CASE WHEN state IN ('submitting', 'submit_uncertain')
		            THEN 'submit_uncertain' ELSE state END AS state,
		       count(*),
		       COALESCE(EXTRACT(EPOCH FROM (now() - min(created_at))), 0)
		FROM files.link_scans
		WHERE state <> 'done'
		GROUP BY 1`)
	if err != nil {
		return nil, 0, fmt.Errorf("link scan backlog: %w", err)
	}
	defer rows.Close()

	byState := make(map[string]int, 2)
	var oldestSeconds float64
	for rows.Next() {
		var state string
		var count int
		var ageSeconds float64
		if err := rows.Scan(&state, &count, &ageSeconds); err != nil {
			return nil, 0, fmt.Errorf("scan link scan backlog: %w", err)
		}
		byState[state] = count
		if ageSeconds > oldestSeconds {
			oldestSeconds = ageSeconds
		}
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("link scan backlog: %w", err)
	}
	return byState, time.Duration(oldestSeconds * float64(time.Second)), nil
}
