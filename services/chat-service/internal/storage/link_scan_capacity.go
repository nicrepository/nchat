package storage

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/nicrepository/nchat/libs/go/platform/urlsafety"
)

// What this deployment is willing to spend at the provider.
//
// The message limiter counts messages. Cloudflare bills scans, and one message
// may carry ten URLs, so the two were never the same unit: a sender could spend
// ten times their apparent budget by writing ten links, and repeat it through
// create, DM, edit and forward independently because each had its own limiter.
//
// The unit here is the one that actually costs: a canonical URL nobody has a
// fresh verdict or an active job for. Everything else is free and is charged as
// free — a cached clearance, a URL another message is already waiting on, a
// duplicate inside one body, an idempotent replay. Charging for those would make
// the budget a limit on *usage* rather than on *cost*, which is both wrong and
// the kind of limit people work around by resending.

// Admission outcomes. A closed set, because they are metric label values and a
// caller must not be able to widen them.
const (
	// AdmissionAllowed means the required scans were reserved and their jobs
	// created in the same transaction.
	AdmissionAllowed = "allowed"
	// AdmissionWorkspaceBudget means this workspace has introduced as many new
	// URLs as its window allows.
	AdmissionWorkspaceBudget = "workspace_budget"
	// AdmissionBacklog means the deployment-wide queue of undecided scans is
	// full. It is not about who asked — a full backlog is a slow provider, and
	// admitting more would only make every waiting message slower.
	AdmissionBacklog = "backlog"
)

// LinkScanCapacity is the deployment's configured ceilings.
type LinkScanCapacity struct {
	// WorkspaceNewURLBudget is how many new canonical URLs one workspace may
	// introduce per window, across every path that can queue a scan.
	WorkspaceNewURLBudget int
	// BudgetWindow is the fixed window those budgets are counted in.
	BudgetWindow time.Duration
	// MaxPendingJobs caps undecided scans deployment-wide.
	MaxPendingJobs int
}

// LinkScanAdmission is what one admission decided.
type LinkScanAdmission struct {
	// NewScanCost is how many canonical URLs actually required new provider
	// work. Zero means the operation was free and was admitted without touching
	// any budget.
	NewScanCost int
	// Result is one of the admission constants above.
	Result string
}

// Allowed reports whether the operation may proceed.
func (a LinkScanAdmission) Allowed() bool { return a.Result == AdmissionAllowed }

// AdmitLinkScans reserves capacity for the URLs that need new provider work and
// creates their jobs, atomically.
//
// # Why it is one transaction and not three calls
//
// The obvious shape — check the budget, release, then queue the scans — is a
// race with a name: two concurrent sends both read "9 of 10 used", both decide
// they fit, and the window ends at 12. Under an attacker choosing the
// concurrency that is not a rare interleaving, it is the normal case. So the
// classification, the backlog check, the reservation and the job creation are
// one transaction, and the reservation is a single conditional statement rather
// than a read followed by a write.
//
// No provider call happens inside it. The transaction only ever touches this
// database, and the work it authorizes is done later by the worker.
//
// # All or nothing
//
// A message needing four new scans with one left in the budget queues *nothing*.
// Admitting the three that fit would publish a message whose fourth link was
// never checked — the fail-closed rule says a URL with no verdict withholds the
// message, and a URL with no *job* would withhold it forever.
//
// # The race that is deliberately left open
//
// Rows that already exist are locked, so two admissions cannot both reactivate
// the same expired verdict and both charge for it. A URL that exists in neither
// transaction cannot be locked — there is no row yet — so two admissions naming
// the same brand-new URL may both charge one. The insert is idempotent, so only
// one job is created; the cost is that the budget was charged twice for one
// scan. That errs toward refusing too much rather than spending too much, which
// is the direction a quota control should err in.
func (s *PGXMessageStore) AdmitLinkScans(
	ctx context.Context, workspaceID string, canonicalURLs []string, capacity LinkScanCapacity,
) (LinkScanAdmission, error) {
	if len(canonicalURLs) == 0 {
		return LinkScanAdmission{Result: AdmissionAllowed}, nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return LinkScanAdmission{}, fmt.Errorf("admit link scans: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	needed, err := chargeableURLs(ctx, tx, canonicalURLs)
	if err != nil {
		return LinkScanAdmission{}, err
	}
	if len(needed) == 0 {
		// Every URL is already answered or already queued. Nothing to reserve and
		// nothing to create — the operation costs the provider nothing, so it is
		// admitted regardless of how spent the budget is.
		if err := tx.Commit(ctx); err != nil {
			return LinkScanAdmission{}, fmt.Errorf("admit link scans: %w", err)
		}
		return LinkScanAdmission{Result: AdmissionAllowed}, nil
	}

	admission, err := reserveCapacity(ctx, tx, workspaceID, len(needed), capacity)
	if err != nil || !admission.Allowed() {
		return admission, err
	}
	if err := ensureLinkScanJobs(ctx, tx, needed); err != nil {
		return LinkScanAdmission{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return LinkScanAdmission{}, fmt.Errorf("admit link scans: %w", err)
	}
	return LinkScanAdmission{NewScanCost: len(needed), Result: AdmissionAllowed}, nil
}

// chargeableURLs returns the URLs that will actually cost a provider scan.
//
// Three ways a URL is free, and each is a real deduplication rather than a
// discount: a fresh final verdict answers it without asking anyone; a row still
// pending is a scan somebody already paid for and this message simply waits on
// the same one; and a repeated URL inside one body is one URL.
//
// Existing rows are locked in a stable order. The lock is what makes the
// classification and the reservation consistent with each other; the ordering is
// what stops two admissions naming overlapping URL sets from deadlocking.
func chargeableURLs(ctx context.Context, tx pgx.Tx, canonicalURLs []string) ([]string, error) {
	wanted := uniqueSortedURLs(canonicalURLs)
	rows, err := tx.Query(ctx, `
		SELECT canonical_url, status,
		       (status IN ('safe', 'malicious')
		        AND decided_at > now() - ($2 * interval '1 second')) AS fresh
		FROM chat.link_scans
		WHERE canonical_url = ANY($1::text[])
		ORDER BY canonical_url
		FOR UPDATE`,
		wanted, urlsafety.VerdictTTL.Seconds(),
	)
	if err != nil {
		return nil, fmt.Errorf("classify link scan cost: %w", err)
	}
	defer rows.Close()

	free := make(map[string]struct{}, len(wanted))
	for rows.Next() {
		var url, status string
		var fresh bool
		if err := rows.Scan(&url, &status, &fresh); err != nil {
			return nil, fmt.Errorf("classify link scan cost: %w", err)
		}
		// Pending joins existing work; inconclusive is terminal for its scan; a
		// fresh safe/malicious verdict is a reusable answer.
		if status == "pending" || status == "inconclusive" || fresh {
			free[url] = struct{}{}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("classify link scan cost: %w", err)
	}

	needed := make([]string, 0, len(wanted))
	for _, url := range wanted {
		if _, isFree := free[url]; !isFree {
			needed = append(needed, url)
		}
	}
	return needed, nil
}

// reserveCapacity applies the backlog cap and then the workspace budget.
//
// The backlog is checked first because it is the deployment-wide health signal:
// when the queue is full, every workspace's messages are already waiting longer,
// and admitting more work on the grounds that one tenant still has budget would
// make that worse for everyone.
func reserveCapacity(
	ctx context.Context, tx pgx.Tx, workspaceID string, cost int, capacity LinkScanCapacity,
) (LinkScanAdmission, error) {
	if capacity.MaxPendingJobs > 0 {
		var pending int
		if err := tx.QueryRow(ctx,
			`SELECT count(*) FROM chat.link_scans WHERE status = 'pending'`,
		).Scan(&pending); err != nil {
			return LinkScanAdmission{}, fmt.Errorf("read link scan backlog: %w", err)
		}
		if pending+cost > capacity.MaxPendingJobs {
			return LinkScanAdmission{NewScanCost: cost, Result: AdmissionBacklog}, nil
		}
	}
	reserved, err := reserveWindowBudget(ctx, tx, budgetScopeWorkspace, workspaceID,
		cost, capacity.WorkspaceNewURLBudget, capacity.BudgetWindow)
	if err != nil {
		return LinkScanAdmission{}, err
	}
	if !reserved {
		return LinkScanAdmission{NewScanCost: cost, Result: AdmissionWorkspaceBudget}, nil
	}
	return LinkScanAdmission{NewScanCost: cost, Result: AdmissionAllowed}, nil
}

// ensureLinkScanJobs creates or reactivates exactly the jobs that were paid for.
//
// The insert is idempotent and the reactivation can only ever move a row from
// "stale clearance" to "must be re-scanned" — never the other way, which would
// be a way to unblock a malicious verdict by naming it.
func ensureLinkScanJobs(ctx context.Context, tx pgx.Tx, canonicalURLs []string) error {
	if _, err := tx.Exec(ctx, `
		INSERT INTO chat.link_scans (canonical_url)
		SELECT DISTINCT url FROM unnest($1::text[]) AS urls(url)
		ON CONFLICT (canonical_url) DO NOTHING`,
		canonicalURLs,
	); err != nil {
		return fmt.Errorf("create link scan jobs: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE chat.link_scans
		   SET status = 'pending', scan_uuid = NULL, decided_at = NULL,
		       attempts = 0, next_attempt_at = NULL,
		       submit_attempt_started_at = NULL, updated_at = now()
		 WHERE canonical_url = ANY($1::text[])
		   AND status IN ('safe', 'malicious')`,
		canonicalURLs,
	); err != nil {
		return fmt.Errorf("reactivate link scan jobs: %w", err)
	}
	return nil
}

// Budget scopes. Never metric labels, never logged: a workspace id identifies a
// tenant, and 'global' is the only other key that exists.
const (
	budgetScopeWorkspace      = "workspace"
	budgetScopeProviderSubmit = "provider_submit"
	budgetKeyGlobal           = "global"
)

// reserveWindowBudget consumes `cost` from a fixed window, atomically.
//
// One statement, and the condition is inside it. The read-then-write shape this
// replaces cannot be made correct with concurrent replicas: whatever the gap
// between the SELECT and the UPDATE, two callers fit through it and the window
// ends over its limit.
//
// The window start is computed by the database from its own clock, so replicas
// with drifting clocks still agree on which window they are in — a counter is
// only a counter if everyone is counting into the same row.
//
// A limit of zero disables the budget rather than refusing everything. That is
// stated here because the opposite reading would make a missing configuration
// silently block every message with a link, and a security control that fails
// into "nothing works" gets switched off entirely.
func reserveWindowBudget(
	ctx context.Context, tx pgx.Tx, scopeType, scopeKey string,
	cost, limit int, window time.Duration,
) (bool, error) {
	if limit <= 0 || window <= 0 || cost <= 0 {
		return true, nil
	}
	// A single operation larger than the whole window budget can never fit, and
	// the conditional insert below would admit it (there is no conflict to test
	// the condition against on a fresh window). Refused here instead.
	if cost > limit {
		return false, nil
	}
	var used int
	err := tx.QueryRow(ctx, `
		INSERT INTO chat.link_scan_budget_usage AS b (scope_type, scope_key, window_start, used)
		VALUES ($1, $2, to_timestamp(floor(extract(epoch FROM now()) / $5) * $5), $3)
		ON CONFLICT (scope_type, scope_key, window_start) DO UPDATE
		   SET used = b.used + $3
		 WHERE b.used + $3 <= $4
		RETURNING b.used`,
		scopeType, scopeKey, cost, limit, window.Seconds(),
	).Scan(&used)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		// The conditional update matched nothing: the window is spent.
		return false, nil
	case err != nil:
		return false, fmt.Errorf("reserve link scan budget: %w", err)
	}
	return true, nil
}

// ReserveProviderSubmit takes one submission from the deployment-wide provider
// allowance.
//
// Separate from the workspace budget and enforced at a different moment, because
// they answer different questions. The workspace budget is about *who* may
// introduce new work; this is about how fast this deployment may talk to
// Cloudflare at all, and it applies to every submission including the retries
// and revalidations no user asked for.
//
// It lives in the database rather than in each process for the reason a
// per-process limiter always fails: three replicas each allowing ten per minute
// is thirty per minute at the provider, and the number that matters is the one
// the provider counts.
func (s *PGXMessageStore) ReserveProviderSubmit(
	ctx context.Context, limit int, window time.Duration,
) (bool, error) {
	if limit <= 0 || window <= 0 {
		return true, nil
	}
	tx, err := s.pool.Begin(ctx)
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

// PruneLinkScanBudget drops windows that can no longer be counted into.
//
// Bounded and lazy: the worker calls it once a pass and deletes at most a page,
// so the table stays small without a scheduled job to operate and without one
// long delete blocking the reservations that share the table.
func (s *PGXMessageStore) PruneLinkScanBudget(ctx context.Context, olderThan time.Duration) error {
	if olderThan <= 0 {
		return nil
	}
	_, err := s.pool.Exec(ctx, `
		DELETE FROM chat.link_scan_budget_usage
		 WHERE (scope_type, scope_key, window_start) IN (
		     SELECT scope_type, scope_key, window_start
		     FROM chat.link_scan_budget_usage
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

// ── The submission window ─────────────────────────────────────────────────────

// Submission states, derived from the row rather than stored as a fourth column.
//
// The distinction that did not exist before: a URL with no scan id had either
// never been submitted or been submitted with the answer lost, and those two
// demand opposite things. The first must be submitted. The second must not.
const (
	// SubmitPending means no external attempt has been made.
	SubmitPending = "submit_pending"
	// SubmitUncertain means an attempt was handed to the provider and this
	// deployment does not know whether it was accepted. Never a reason to submit.
	SubmitUncertain = "submit_uncertain"
	// SubmitPolling means a scan id is confirmed and owned locally.
	SubmitPolling = "polling"
)

// SubmitState reports which of the three situations this job is in.
func (j LinkScanJob) SubmitState() string {
	switch {
	case j.ScanUUID != "":
		return SubmitPolling
	case !j.SubmitStartedAt.IsZero():
		return SubmitUncertain
	default:
		return SubmitPending
	}
}

// BeginLinkScanSubmit records the intent to submit, before the provider is
// called.
//
// This is the write that makes the gap recoverable. After it, a crash at any
// point leaves a row that says "an attempt is outstanding" instead of one
// indistinguishable from a URL nobody ever submitted — which is what made
// recovery resubmit.
//
// It returns the generation the attempt owns. Every later write about this
// attempt carries it, so a worker whose lease expired mid-submission cannot
// overwrite the attempt that replaced it: the row moved on, the generation
// changed, and the stale write finds nothing.
func (s *PGXMessageStore) BeginLinkScanSubmit(
	ctx context.Context, canonicalURL string, expectedGeneration int,
) (int, error) {
	var generation int
	err := s.pool.QueryRow(ctx, `
		UPDATE chat.link_scans
		   SET submit_attempt_started_at = now(),
		       submit_generation = submit_generation + 1,
		       updated_at = now()
		 WHERE canonical_url = $1
		   AND status = 'pending'
		   AND scan_uuid IS NULL
		   AND submit_generation = $2
		RETURNING submit_generation`,
		canonicalURL, expectedGeneration,
	).Scan(&generation)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		// Decided, submitted, or re-attempted by somebody else while this worker
		// held the claim. Not this worker's to submit.
		return 0, ErrLinkScanConflict
	case err != nil:
		return 0, fmt.Errorf("begin link scan submit: %w", err)
	}
	return generation, nil
}

// AdoptScanUUID binds a scan id recovered from the provider's search to the
// uncertain attempt that produced it.
//
// The same compare-and-set the ordinary submission uses, and deliberately the
// same predicate: the row must still be pending, must still have no scan id, and
// must still be on the generation the reconciliation was about. A verdict is
// then read through the ordinary result endpoint, which applies its own checks —
// reconciliation recovers an *id*, never a verdict.
func (s *PGXMessageStore) AdoptScanUUID(
	ctx context.Context, canonicalURL, scanUUID string, generation int,
) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE chat.link_scans
		   SET scan_uuid = $2, submit_attempt_started_at = NULL, updated_at = now()
		 WHERE canonical_url = $1
		   AND status = 'pending'
		   AND scan_uuid IS NULL
		   AND submit_generation = $3`,
		canonicalURL, scanUUID, generation,
	)
	if err != nil {
		return fmt.Errorf("adopt scan uuid: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrLinkScanConflict
	}
	return nil
}

// uniqueSortedURLs deduplicates and orders a URL set.
//
// Sorted because the rows are locked in this order, and two admissions naming
// overlapping sets in different orders is the textbook deadlock.
func uniqueSortedURLs(urls []string) []string {
	seen := make(map[string]struct{}, len(urls))
	unique := make([]string, 0, len(urls))
	for _, url := range urls {
		if _, exists := seen[url]; exists {
			continue
		}
		seen[url] = struct{}{}
		unique = append(unique, url)
	}
	sort.Strings(unique)
	return unique
}
