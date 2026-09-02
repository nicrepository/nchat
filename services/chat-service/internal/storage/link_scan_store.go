package storage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/nicrepository/nchat/libs/go/platform/urlsafety"
	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
)

// The RF-21 scan queue.
//
// Cloudflare URL Scanner is submit-then-poll: its result endpoint answers 404
// while a scan runs, and the provider recommends polling every 10-30 seconds. So
// the verdict for a URL nobody has scanned cannot be obtained inside a send, and
// this table is where the wait lives instead — durable, so a restart resumes it,
// and keyed by canonical URL, so ten people pasting one link cost one scan.
//
// It is deliberately the same shape as RF-22's antimalware queue in file-service:
// a claim with a lease, an attempt counter that stretches the retry, and a
// partial index that is empty whenever there is no backlog. Two queues that look
// alike are two queues an operator only has to learn once.

const (
	// linkScanLease is how long a claimed row is left alone.
	//
	// It has to outlive the work the claim protects, and the claim now protects
	// exactly one provider exchange: the worker takes one row at a time,
	// immediately before processing it, rather than leasing a batch and working
	// through it serially. That is what fixes the review's finding — a batch of
	// eight at a ten-second ceiling is an eighty-second worst case under a
	// sixty-second lease, so the last rows in a slow batch were reclaimable while
	// still being processed, and could be submitted twice.
	//
	// With one row per claim the relationship is stated rather than hoped for:
	// the lease is the provider's own request timeout plus a wide margin for the
	// database round trips around it. The assertion below keeps it that way.
	linkScanLease = 60 * time.Second

	// providerExchangeCeiling mirrors urlsafety's per-request timeout. It is
	// restated here because this is where the lease has to be compared against
	// it, and a lease shorter than the work it protects is the bug this constant
	// exists to make unrepresentable.
	providerExchangeCeiling = 10 * time.Second

	// linkScanBackoffSteps caps how far the retry stretches: the next attempt is
	// lease * min(attempts, steps) out, so a provider outage costs
	// geometrically fewer attempts instead of a tight loop, and a URL that
	// nobody can scan settles at one attempt per five minutes rather than being
	// abandoned. There is no attempts ceiling: a row nothing can decide is a row
	// that keeps its message withheld, which is the fail-closed answer.
	linkScanBackoffSteps = 5

	// maxPendingResolveBatch bounds one promotion pass.
	maxPendingResolveBatch = 100
)

// A lease that does not outlive one provider exchange lets a second worker
// reclaim a row that is still being processed. Checked at compile time so the
// two numbers cannot drift apart in a later edit.
const _ = uint(linkScanLease - providerExchangeCeiling)

// ErrLinkScanConflict reports that a compare-and-set lost.
//
// It is not a failure of the operation so much as a statement about who owns the
// row: another worker advanced it while this one was talking to the provider. A
// caller must not retry the provider call on it — the work has already been done
// or reassigned — and must not treat it as an error worth alarming about.
var ErrLinkScanConflict = errors.New("link scan: superseded by another worker")

// LinkScanJob is one claimed canonical URL, with everything the worker needs to
// either submit it or read its result.
type LinkScanJob struct {
	CanonicalURL string
	// ScanUUID is empty until the URL has been submitted. Empty means "submit
	// me"; set means "read my result".
	ScanUUID string
	// Attempts includes this claim. It is a diagnostic, not a budget.
	Attempts int

	// SubmitStartedAt is when the outstanding submission attempt was handed to
	// the provider, and zero when there is none. Together with ScanUUID it is
	// what separates "never submitted" from "submitted, outcome unknown" — the
	// distinction that stops a restart from resubmitting a scan the provider has
	// already accepted and billed.
	SubmitStartedAt time.Time
	// SubmitGeneration is the compare-and-set token for the current attempt.
	// Every write about that attempt carries it, so a worker whose lease expired
	// mid-submission cannot overwrite the attempt that replaced it.
	SubmitGeneration int
}

// ResolveSummary counts what one promotion pass decided. It carries no message
// content because the pass no longer publishes anything: the event it wrote to
// the outbox does.
type ResolveSummary struct {
	Published int
	Blocked   int
	// PublishedInconclusive is the subset of Published whose links could not all
	// be verified. Counted separately because it is the number that tells an
	// operator the provider has stopped producing verdicts — the messages went
	// out, so nothing else about the system looks wrong.
	PublishedInconclusive int
}

// Total reports how many withheld messages the pass decided.
func (s ResolveSummary) Total() int { return s.Published + s.Blocked }

// PublishEvent is one resolved message waiting to be announced.
type PublishEvent struct {
	MessageID   string
	WorkspaceID string
	// EventType is "message.created" for a promotion or "message.blocked" for a
	// refusal. The two go to different audiences, which is why the dispatcher
	// must not infer one from the other.
	EventType string
	// TargetType is "channel", "dm" or "sender". The first two address a
	// conversation; "sender" addresses one user, and is what keeps a blocked
	// message from being announced to a target it was never shown in.
	TargetType string
	TargetID   string
	Message    domain.Message

	// Reason is set only for a message.blocked event: one of the closed set the
	// ws package defines (malicious_link, link_check_inconclusive), read back
	// from block_reason. Empty for message.created, which carries no reason.
	Reason string

	// Cancelled marks an event whose message no longer exists, so the dispatcher
	// retires it instead of retrying something that can never succeed.
	Cancelled bool
}

// Outbox event types and audiences. Closed sets, mirrored by the CHECK
// constraints on the table.
const (
	EventMessageCreated = "message.created"
	EventMessageBlocked = "message.blocked"

	TargetSender = "sender"
	// TargetChannel and TargetDM name the two conversation audiences. They were
	// SQL literals until the link-safety change event needed to address the same
	// audiences from Go; naming them keeps the two routings provably identical.
	TargetChannel = "channel"
	TargetDM      = "dm"
)

// Blocked reports whether this event announces a refusal rather than a
// publication.
func (e PublishEvent) Blocked() bool { return e.EventType == EventMessageBlocked }

const (
	// publishRetryBase paces redelivery. Shorter than the scan lease because the
	// work is local — one hub publish — and a message already active but not yet
	// announced is a message somebody is waiting to see.
	publishRetryBase = 5 * time.Second

	// publishBackoffSteps caps the stretch, so a hub refusing traffic settles at
	// one attempt per half-minute instead of a storm.
	publishBackoffSteps = 6
)

// LoadLinkVerdicts returns the fresh verdicts known for canonicalURLs.
//
// Freshness is decided here and not by the caller: a row older than the shared
// VerdictTTL is not returned at all, so an expired clearance behaves exactly
// like a URL nobody ever scanned — the message waits for a new scan rather than
// riding a verdict from last week. Reputation changes in both directions, and a
// cache that never expires is a control that only ever gets weaker.
//
// Three states are returned, not two: `safe`, `malicious` and `inconclusive`.
// The first two are subject to the freshness window; `inconclusive` is not,
// because it is terminal rather than a clearance with a lifetime.
//
// Returning inconclusive here is **not** returning a verdict. The caller's policy
// (aggregateLinkDecision) is what decides what each state means, and it has never
// treated inconclusive as an answer — it is reported so that the one caller which
// genuinely needs to tell "decided to say nothing" from "not decided yet", the
// edit path, can do so without a second query and a second policy.
//
// A URL absent from the result has no usable state at all: never scanned, or
// scanned and expired.
func (s *PGXMessageStore) LoadLinkVerdicts(
	ctx context.Context, canonicalURLs []string,
) (map[string]urlsafety.Verdict, error) {
	if len(canonicalURLs) == 0 {
		return map[string]urlsafety.Verdict{}, nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT canonical_url, status
		FROM chat.link_scans
		WHERE canonical_url = ANY($1::text[])
		  AND status IN ('safe', 'malicious')
		  AND decided_at > now() - ($2 * interval '1 second')`,
		canonicalURLs, urlsafety.VerdictTTL.Seconds(),
	)
	if err != nil {
		return nil, fmt.Errorf("load link verdicts: %w", err)
	}
	defer rows.Close()

	verdicts := make(map[string]urlsafety.Verdict, len(canonicalURLs))
	for rows.Next() {
		var url, status string
		if err := rows.Scan(&url, &status); err != nil {
			return nil, fmt.Errorf("scan link verdict: %w", err)
		}
		// Only a value this package recognises becomes a verdict. A row carrying
		// anything else is treated as absent, so a corrupted or future status
		// cannot clear a message.
		if verdictIsLoadable(status) {
			verdicts[url] = urlsafety.Verdict(status)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load link verdicts: %w", err)
	}
	return verdicts, nil
}

// verdictIsLoadable reports whether a stored status is one this package
// recognises and may report to the policy layer.
//
// It is *not* "may be treated as a clearance" — inconclusive passes here and is
// never a clearance anywhere. One place, so a corrupted or future status cannot
// reach the policy layer at all.
func verdictIsLoadable(status string) bool {
	verdict := urlsafety.Verdict(status)
	return verdict.IsFinal() || verdict == urlsafety.VerdictInconclusive
}

// EnsureLinkScans records that these canonical URLs need a verdict.
//
// It is the deduplication point, and it is a plain upsert rather than a lock:
// two replicas inserting the same URL at the same moment both succeed, the
// primary key keeps one row, and the worker submits it once. That is the whole
// concurrency control — a distributed lock to save one duplicate submission in a
// rare race would cost far more than the race does.
//
// A row that already carries a fresh verdict is left completely alone: DO
// NOTHING, not DO UPDATE. Resetting it to pending would let anyone re-open a
// decided URL by naming it, which for a malicious verdict means unblocking it.
func (s *PGXMessageStore) EnsureLinkScans(ctx context.Context, canonicalURLs []string) error {
	if len(canonicalURLs) == 0 {
		return nil
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO chat.link_scans (canonical_url)
		SELECT DISTINCT url FROM unnest($1::text[]) AS urls(url)
		ON CONFLICT (canonical_url) DO NOTHING`,
		canonicalURLs,
	)
	if err != nil {
		return fmt.Errorf("ensure link scans: %w", err)
	}
	// A decided row whose verdict has expired is due again. This is the one
	// update that may touch a decided row, and it can only ever move it from
	// "stale clearance" to "must be re-scanned" — never the other way.
	//
	// Scoped to safe/malicious only: inconclusive is deliberately excluded. It
	// has no clearance to go stale, and reopening it here would be exactly the
	// automatic resubmit loop the fix for that terminal state refuses to
	// introduce — an inconclusive row stays inconclusive until something
	// deliberate changes it, not until VerdictTTL happens to elapse.
	_, err = s.pool.Exec(ctx, `
		UPDATE chat.link_scans
		   SET status = 'pending', scan_uuid = NULL, decided_at = NULL,
		       attempts = 0, next_attempt_at = NULL, updated_at = now()
		 WHERE canonical_url = ANY($1::text[])
		   AND status IN ('safe', 'malicious')
		   AND decided_at <= now() - ($2 * interval '1 second')`,
		canonicalURLs, urlsafety.VerdictTTL.Seconds(),
	)
	if err != nil {
		return fmt.Errorf("reopen expired link scans: %w", err)
	}
	return nil
}

// LinkScanBacklog summarises the queue for the metrics gauges.
//
// Two numbers an operator cannot get any other way: how many URLs are waiting,
// split by whether they still need submitting or are being polled, and how long
// the oldest one has been waiting. Without them a provider outage is invisible —
// messages simply stop appearing, with nothing to point at.
//
// One aggregate query per worker pass, against the same partial index the claim
// uses, so it costs nothing when the queue is empty.
func (s *PGXMessageStore) LinkScanBacklog(ctx context.Context) (map[string]int, time.Duration, error) {
	// Three states, not two. submit_uncertain is reported on its own because it
	// is the one a restart will not clear and must not clear — a growing
	// uncertain backlog means submissions whose outcome nobody can recover, and
	// folding it into submit_pending would hide exactly that.
	rows, err := s.pool.Query(ctx, `
		SELECT CASE
		         WHEN scan_uuid IS NOT NULL THEN 'polling'
		         WHEN submit_attempt_started_at IS NOT NULL THEN 'submit_uncertain'
		         ELSE 'submit_pending'
		       END AS state,
		       count(*),
		       COALESCE(EXTRACT(EPOCH FROM (now() - min(created_at))), 0)
		FROM chat.link_scans
		WHERE status = 'pending'
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

// ReopenExpiredVerdicts puts lapsed verdicts back in the queue.
//
// A verdict is only usable while it is fresh, and the promotion now enforces
// that — but enforcing it alone would strand a message forever: its URL is
// decided, so nothing re-scans it, and it is stale, so nothing promotes it. This
// is the other half. A decided row that a withheld message still depends on, and
// whose verdict has lapsed, goes back to pending and is scanned again.
//
// Scoped to URLs a withheld message is actually waiting on. Reopening every
// expired verdict in the table would re-scan the entire history on a schedule,
// which is a quota bill for answers nobody is waiting for; a verdict that
// nothing is blocked on is simply re-scanned on demand, by the send that next
// names it.
//
// Idempotent by construction: a row already pending is not matched, so repeated
// passes and concurrent workers cannot produce a second scan for one URL.
func (s *PGXMessageStore) ReopenExpiredVerdicts(ctx context.Context) (int, error) {
	// Scoped to safe/malicious for the same reason EnsureLinkScans is: an
	// inconclusive row has no clearance to expire, and reopening it on a timer
	// would be the automatic resubmit loop this terminal state must not have.
	tag, err := s.pool.Exec(ctx, `
		UPDATE chat.link_scans ls
		   SET status = 'pending', scan_uuid = NULL, decided_at = NULL,
		       attempts = 0, next_attempt_at = NULL, updated_at = now()
		 WHERE ls.status IN ('safe', 'malicious')
		   AND ls.decided_at <= now() - ($1 * interval '1 second')
		   AND EXISTS (
		       SELECT 1
		       FROM chat.message_link_scans mls
		       JOIN chat.messages m ON m.id = mls.message_id
		       WHERE mls.canonical_url = ls.canonical_url
		         AND mls.fingerprint = COALESCE(m.link_safety_fingerprint, '')
		         AND m.status = 'pending_link_scan'
		   )`,
		urlsafety.VerdictTTL.Seconds(),
	)
	if err != nil {
		return 0, fmt.Errorf("reopen expired verdicts: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// linkSafetyStatesQuery answers, for messages the caller wrote, what became of
// them.
//
// # Where "blocked" comes from
//
// A refused message is stored as `deleted`, which is what it is to every other
// reader: something that was never published. That alone cannot distinguish it
// from a message its author deleted, so the reason is derived from the
// link-safety rows themselves — the association recorded at creation, joined to
// the verdict for the URL it named. Those are durable state about the decision.
//
// Deliberately not derived from chat.message_publish_outbox. That table is a
// delivery mechanism: a row there means "an announcement is owed", not "this is
// what happened", and it is retired once delivered. Reading business state out
// of it would make the answer depend on whether a dispatcher had run.
//
// The association is matched on the fingerprint the message currently carries,
// the same binding the promotion uses, so a verdict recorded for other content
// cannot explain this one.
//
// # The scoping is the authorization
//
// workspace + active membership + `sender_id = the caller`. A message is only
// ever described to the person who wrote it, so there is no ordering in which
// this endpoint answers about somebody else's message. Rows that do not match
// produce no output at all rather than a "not allowed" answer, which is what
// keeps missing, foreign and inaccessible indistinguishable.
//
// # Known ceiling
//
// A malicious verdict older than VerdictTTL is requeued for re-scanning as soon
// as anybody sends the same URL again, and while it is pending it no longer
// explains the block: the answer degrades from `blocked` to `deleted`. That is
// the conservative direction — the author is told the message is gone rather
// than told something we can no longer evidence — and it is out of reach of the
// flow this serves, which reconciles within one session of the block.
const linkSafetyStatesQuery = `
	SELECT m.id::text,
	       m.status,
	       EXISTS (
	           SELECT 1
	           FROM chat.message_link_scans mls
	           JOIN chat.link_scans ls ON ls.canonical_url = mls.canonical_url
	           WHERE mls.message_id = m.id
	             AND mls.fingerprint = COALESCE(m.link_safety_fingerprint, '')
	             AND ls.status = 'malicious'
	       ) AS malicious,
	       EXISTS (
	           SELECT 1
	           FROM chat.message_link_scans mls
	           JOIN chat.link_scans ls ON ls.canonical_url = mls.canonical_url
	           WHERE mls.message_id = m.id
	             AND mls.fingerprint = COALESCE(m.link_safety_fingerprint, '')
	             AND ls.status = 'inconclusive'
	       ) AS inconclusive
	FROM chat.messages m
	JOIN chat.workspace_members wm
	  ON wm.workspace_id = m.workspace_id
	 AND wm.user_id = $2::uuid
	 AND wm.status = 'active'
	WHERE m.workspace_id = $1::uuid
	  AND m.sender_id = $2::uuid
	  AND m.id = ANY($3::uuid[])`

// LinkSafetyStates returns the current state of the caller's own messages.
//
// Only ids that resolve to a message this caller wrote appear in the result. An
// id that names nothing, somebody else's message, or another workspace's is
// absent, and absence carries no information beyond "this service will not talk
// about that id".
func (s *PGXMessageStore) LinkSafetyStates(
	ctx context.Context, workspaceID, senderID string, messageIDs []string,
) ([]domain.MessageLinkSafetyState, error) {
	if len(messageIDs) == 0 {
		return nil, nil
	}
	rows, err := s.pool.Query(ctx, linkSafetyStatesQuery, workspaceID, senderID, messageIDs)
	if err != nil {
		return nil, fmt.Errorf("read link safety states: %w", err)
	}
	defer rows.Close()

	states := make([]domain.MessageLinkSafetyState, 0, len(messageIDs))
	for rows.Next() {
		var id, status string
		var malicious, inconclusive bool
		if err := rows.Scan(&id, &status, &malicious, &inconclusive); err != nil {
			return nil, fmt.Errorf("scan link safety state: %w", err)
		}
		state, reason := linkSafetyState(status, malicious, inconclusive)
		states = append(states, domain.MessageLinkSafetyState{
			MessageID: id, State: state, Reason: reason,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read link safety states: %w", err)
	}
	return states, nil
}

// linkSafetyState maps a stored row to the state, and for a blocked one the
// reason, its author is told about.
//
// A separate function so the mapping is unit-testable without a database, and
// so the one branch that matters — a deleted message is only *blocked* when a
// malicious or inconclusive verdict explains it — is stated in one place.
// Malicious wins the tie: a message whose links included both a malicious URL
// and an inconclusive one was refused for the stronger reason.
func linkSafetyState(status string, malicious, inconclusive bool) (domain.LinkSafetyState, string) {
	switch {
	case status == string(domain.MessageStatusPendingLinkScan):
		return domain.LinkSafetyStatePending, ""
	case status == string(domain.MessageStatusActive):
		return domain.LinkSafetyStateActive, ""
	case malicious:
		return domain.LinkSafetyStateBlocked, domain.LinkSafetyReasonMaliciousLink
	case inconclusive:
		return domain.LinkSafetyStateBlocked, domain.LinkSafetyReasonLinkCheckInconclusive
	default:
		return domain.LinkSafetyStateDeleted, ""
	}
}

// claimDueLinkScansQuery takes a batch of due rows and leases them in one
// statement.
//
// FOR UPDATE SKIP LOCKED is what lets several replicas drain the queue without
// coordinating: each takes rows nobody else holds. The lease is the whole
// concurrency control and also the retry schedule — the UPDATE pushes
// next_attempt_at out by lease * min(attempts, steps) and counts the try in the
// same statement, so:
//
//   - two workers cannot hold one row, because the claim is the update;
//   - a worker that died holds nothing: the row is due again once its lease
//     expires, which is what makes a restart resume the queue rather than lose
//     it;
//   - a provider that keeps failing costs geometrically fewer attempts.
//
// A NULL next_attempt_at counts as due, which is what makes a freshly inserted
// row claimable on the next pass without a second write.
const claimDueLinkScansQuery = `
	WITH due AS (
		SELECT ls.canonical_url
		FROM chat.link_scans ls
		WHERE ls.status = 'pending'
		  AND (ls.next_attempt_at IS NULL OR ls.next_attempt_at <= now())
		ORDER BY ls.next_attempt_at NULLS FIRST, ls.created_at
		LIMIT $1
		FOR UPDATE SKIP LOCKED
	)
	UPDATE chat.link_scans ls
	   SET attempts = LEAST(ls.attempts + 1, 32767),
	       next_attempt_at = now() + ($2 * LEAST(ls.attempts + 1, $3) * interval '1 second'),
	       updated_at = now()
	  FROM due
	 WHERE ls.canonical_url = due.canonical_url
	RETURNING ls.canonical_url, COALESCE(ls.scan_uuid, ''), ls.attempts,
	          COALESCE(ls.submit_attempt_started_at, 'epoch'::timestamptz),
	          ls.submit_generation`

// ClaimDueLinkScans leases up to batchSize URLs awaiting a verdict.
func (s *PGXMessageStore) ClaimDueLinkScans(ctx context.Context, batchSize int) ([]LinkScanJob, error) {
	if batchSize <= 0 {
		return nil, nil
	}
	rows, err := s.pool.Query(ctx, claimDueLinkScansQuery,
		batchSize, linkScanLease.Seconds(), linkScanBackoffSteps)
	if err != nil {
		return nil, fmt.Errorf("claim due link scans: %w", err)
	}
	defer rows.Close()

	var jobs []LinkScanJob
	for rows.Next() {
		var job LinkScanJob
		if err := rows.Scan(&job.CanonicalURL, &job.ScanUUID, &job.Attempts,
			&job.SubmitStartedAt, &job.SubmitGeneration); err != nil {
			return nil, fmt.Errorf("scan link scan job: %w", err)
		}
		// 'epoch' stands in for NULL so one scan target serves both, and it is
		// normalised back here: a zero time means "no attempt outstanding", which
		// is what SubmitState reads.
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

// RecordLinkScanSubmission stores the provider's scan id, closing the window the
// intent opened.
//
// Only a row still pending, still without a scan id, and still on the generation
// this attempt owns is updated. A URL decided while the submission was in flight
// keeps its verdict; an attempt superseded by another worker's does not get to
// write over it. Clearing submit_attempt_started_at in the same statement is what
// moves the row out of the uncertain state — the id is now known locally, so
// there is nothing left to reconcile.
func (s *PGXMessageStore) RecordLinkScanSubmission(
	ctx context.Context, canonicalURL, scanUUID string, generation int,
) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE chat.link_scans
		   SET scan_uuid = $2, submit_attempt_started_at = NULL, updated_at = now()
		 WHERE canonical_url = $1 AND status = 'pending' AND scan_uuid IS NULL
		   AND submit_generation = $3`,
		canonicalURL, scanUUID, generation,
	)
	if err != nil {
		return fmt.Errorf("record link scan submission: %w", err)
	}
	if tag.RowsAffected() == 0 {
		// Somebody else already bound a scan to this URL, or it was decided while
		// this submission was in flight. Either way this worker's scan id is not
		// the one that counts, and saying so lets the caller record it as a lost
		// race rather than retrying into a resubmission loop.
		return ErrLinkScanConflict
	}
	return nil
}

// RecordLinkVerdict stores a terminal poll outcome: safe, malicious, or
// inconclusive.
//
// It refuses anything that is not one of those three, which is the same rule
// the shared package applies: a zero value, a value from a future version, or a
// provider client bug must not be able to write a row that later reads as safe.
// Inconclusive is accepted here but is never IsFinal() and is never loadable as
// a verdict by LoadLinkVerdicts — it is a terminal lifecycle state for the scan,
// not a clearance.
//
// The write is bound to the scan it came from. A worker whose lease expired
// while it was waiting on the provider may still be holding a verdict for a scan
// that has since been superseded — the row was reclaimed, resubmitted, and now
// carries a different scan id. Matching scan_uuid in the predicate is what stops
// that stale answer from overwriting the current one; zero rows affected means
// this worker lost the race, and ErrLinkScanConflict says so rather than
// pretending the write succeeded.
func (s *PGXMessageStore) RecordLinkVerdict(
	ctx context.Context, canonicalURL, scanUUID string, verdict urlsafety.Verdict,
) error {
	if !verdict.IsFinal() && verdict != urlsafety.VerdictInconclusive {
		return fmt.Errorf("%w: refusing to store a non-final verdict", domain.ErrInvalidInput)
	}
	if verdict == urlsafety.VerdictMalicious {
		return s.recordMaliciousLinkVerdict(ctx, canonicalURL, scanUUID, "pending")
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE chat.link_scans
		   SET status = $2, decided_at = now(), next_attempt_at = NULL, updated_at = now()
		 WHERE canonical_url = $1 AND status = 'pending' AND scan_uuid = $3`,
		canonicalURL, string(verdict), scanUUID,
	)
	if err != nil {
		return fmt.Errorf("record link verdict: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrLinkScanConflict
	}
	return nil
}

// recordMaliciousLinkVerdict is the one compare-and-set for every chat
// condemnation, whether it came from the initial poll or reconciliation.
// Publishing the global denial and expiring a file-service SAFE row are CTEs of
// the same statement, so success can never expose chat=malicious while either
// current or rolling-deploy file readers still have fetch authority.
const recordMaliciousLinkVerdictQuery = `
	WITH updated AS (
		UPDATE chat.link_scans
		   SET status = 'malicious', decided_at = now(), next_attempt_at = NULL,
		       next_reconcile_at = NULL, updated_at = now()
		 WHERE canonical_url = $1
		   AND status = $2
		   AND scan_uuid IS NOT NULL
		   AND scan_uuid = $3
		 RETURNING canonical_url
	),
	denied AS (
		INSERT INTO files.link_fetch_denylist (url_digest, canonical_url, source)
		SELECT $4, updated.canonical_url, 'chat'
		FROM updated
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
		 WHERE url_digest = $4
		   AND state = 'done'
		   AND verdict_expires_at > now()
		   AND EXISTS (SELECT 1 FROM updated)
		 RETURNING url_digest
	)
	SELECT EXISTS (SELECT 1 FROM updated)`

func (s *PGXMessageStore) recordMaliciousLinkVerdict(
	ctx context.Context, canonicalURL, scanUUID, expectedStatus string,
) error {
	var updated bool
	err := s.pool.QueryRow(ctx, recordMaliciousLinkVerdictQuery,
		canonicalURL, expectedStatus, scanUUID, urlsafety.URLDigest(canonicalURL),
	).Scan(&updated)
	if err != nil {
		return fmt.Errorf("record malicious link verdict: %w", err)
	}
	if !updated {
		return ErrLinkScanConflict
	}
	return nil
}

// resolvePendingMessagesQuery decides every withheld message whose links are all
// decided, in one statement.
//
// The rule is stated once, in SQL, because it has to be atomic with the state
// change. Computing it in Go and then writing the result would leave a window in
// which two replicas both decided to publish the same message and both broadcast
// it.
//
// # The aggregation, and the two things that changed for issue #135
//
// Per URL, a row is *terminal* when it is a fresh clearance, a fresh
// condemnation, or inconclusive. It is not terminal while it is pending, and a
// safe or malicious verdict older than VerdictTTL is not terminal either — the
// send path already refuses an expired clearance and this applies the same rule,
// which is what an earlier review found missing.
//
// Over a message's URLs:
//
//	any terminal-malicious          -> blocked, immediately
//	otherwise, all terminal:
//	    any inconclusive            -> published, link_safety_state=inconclusive
//	    otherwise                   -> published, link_safety_state=safe
//	otherwise                       -> left alone; work is still outstanding
//
// The first change is `all_terminal` replacing `all_fresh_safe`, and it is a
// correctness requirement created by the second: while inconclusive meant
// "refuse", publishing early on it was impossible, so it was safe to decide a
// message the moment *any* URL came back inconclusive. Now that inconclusive
// publishes, deciding early would deliver a message whose second URL could still
// turn out to be malicious. A message is not published while any of its links may
// still be condemned.
//
// Malicious is still allowed to decide the message on its own, before the other
// URLs are terminal. That is not the same risk in reverse: the outcome is a
// refusal, and no later verdict can make a refusal wrong.
//
// The second change is that inconclusive no longer produces status='deleted' and a
// sender-only message.blocked. It publishes, to the ordinary audience, carrying
// link_safety_state='inconclusive' — a marker the client draws a notice from and
// that nothing in this codebase may read as permission to fetch the URL. See
// domain.MessageLinkSafety.
//
// The UPDATE's own predicate — status = 'pending_link_scan' — is what makes the
// promotion exactly-once. Two replicas may both evaluate the rule; only one
// UPDATE finds the row still pending, and only that one gets a RETURNING row to
// publish from. The other publishes nothing.
const resolvePendingMessagesQuery = `
	WITH candidate AS (
		SELECT m.id,
		       m.link_safety_fingerprint AS fingerprint,
		       bool_or(ls.status = 'malicious'
		               AND ls.decided_at > now() - ($2 * interval '1 second')) AS blocked,
		       -- Inconclusive has no freshness window — see ReopenExpiredVerdicts —
		       -- so it is checked without one: a scan that finished without a
		       -- usable verdict stays that way until reconciliation deliberately
		       -- changes it, and a message waiting on it must stop waiting rather
		       -- than poll a terminal row forever.
		       bool_or(ls.status = 'inconclusive') AS has_inconclusive,
		       -- "No link of this message can still change to malicious." This is
		       -- the gate that keeps a message with one inconclusive URL and one
		       -- still-pending URL withheld.
		       bool_and(
		           (ls.status IN ('safe', 'malicious')
		            AND ls.decided_at > now() - ($2 * interval '1 second'))
		           OR ls.status = 'inconclusive'
		       ) AS all_terminal
		FROM chat.messages m
		JOIN chat.message_link_scans mls
		  ON mls.message_id = m.id
		 -- The binding: only associations recorded for the content this message
		 -- currently carries may decide it, so a verdict obtained for other
		 -- content cannot publish this one.
		 AND mls.fingerprint = COALESCE(m.link_safety_fingerprint, '')
		JOIN chat.link_scans ls ON ls.canonical_url = mls.canonical_url
		WHERE m.status = 'pending_link_scan'
		GROUP BY m.id, m.link_safety_fingerprint
		LIMIT $1
	),
	promoted AS (
		UPDATE chat.messages m
		   SET status = CASE WHEN candidate.blocked THEN 'deleted' ELSE 'active' END,
		       deleted_at = CASE WHEN candidate.blocked THEN now() ELSE m.deleted_at END,
		       -- Written in the same statement as the status, so a published
		       -- message can never exist without the marker that says what may be
		       -- done with its links. A blocked one is marked too: its author's
		       -- client is told nothing about the body, but the row itself carries
		       -- the reason it is a tombstone.
		       link_safety_state = CASE
		                             WHEN candidate.blocked THEN 'malicious'
		                             WHEN candidate.has_inconclusive THEN 'inconclusive'
		                             ELSE 'safe'
		                           END,
		       updated_at = now()
		  FROM candidate
		 WHERE m.id = candidate.id
		   -- Compare-and-set, not read-then-write: the row must still be withheld
		   -- and must still carry the fingerprint the verdicts were read for.
		   -- Zero rows affected means it changed underneath, and nothing is
		   -- published.
		   AND m.status = 'pending_link_scan'
		   AND COALESCE(m.link_safety_fingerprint, '') = COALESCE(candidate.fingerprint, '')
		   AND (candidate.blocked OR candidate.all_terminal)
		RETURNING m.id, m.workspace_id, m.sender_id,
		          NOT candidate.blocked AS published,
		          candidate.has_inconclusive,
		          COALESCE(m.channel_id::text, '') AS channel_id,
		          COALESCE(m.dm_conversation_id::text, '') AS conversation_id
	),
	queued AS (
		-- Two terminal outcomes now, not three. A promotion — safe or
		-- inconclusive — goes to the target, because the message was published and
		-- its recipients must receive it exactly as they would any other. A
		-- refusal goes only to its author.
		--
		-- block_reason is always 'malicious_link' here: a refusal can no longer be
		-- caused by an inconclusive scan. The column keeps its other permitted
		-- value for the rows written by the previous version, which are still in
		-- the table and still deliverable.
		INSERT INTO chat.message_publish_outbox
			(message_id, workspace_id, event_type, target_type, target_id, block_reason)
		SELECT promoted.id, promoted.workspace_id,
		       CASE WHEN promoted.published THEN 'message.created' ELSE 'message.blocked' END,
		       CASE
		         WHEN NOT promoted.published THEN 'sender'
		         WHEN promoted.channel_id <> '' THEN 'channel'
		         ELSE 'dm'
		       END,
		       (CASE
		          WHEN NOT promoted.published THEN promoted.sender_id::text
		          WHEN promoted.channel_id <> '' THEN promoted.channel_id
		          ELSE promoted.conversation_id
		        END)::uuid,
		       CASE WHEN promoted.published THEN NULL ELSE 'malicious_link' END
		FROM promoted
		ON CONFLICT (message_id) DO NOTHING
		RETURNING message_id
	),
	notified AS (
		-- RF-21 delays mention notifications rather than dropping them: a withheld
		-- message must produce no side effect aimed at anyone else, so its
		-- mentions were parked at creation and are released here, in the same
		-- transaction that makes the message publishable. A blocked message never
		-- reaches this step.
		--
		-- An inconclusive message does, and that is deliberate: it was published,
		-- so its mentions are as real as any other message's. A notification names
		-- a message, it does not fetch a URL, so nothing downstream of here gains
		-- an authority the message itself does not have. Recipient membership is
		-- revalidated here because it may have changed while the scan was pending.
		INSERT INTO chat.notification_outbox
			(workspace_id, message_id, recipient_user_id, kind, status)
		SELECT promoted.workspace_id, promoted.id, mention.user_id, 'mention', 'pending'
		FROM promoted
		JOIN chat.message_pending_mentions mention ON mention.message_id = promoted.id
		JOIN chat.workspaces mention_workspace
		  ON mention_workspace.id = promoted.workspace_id AND mention_workspace.status = 'active'
		JOIN chat.workspace_members mention_member
		  ON mention_member.workspace_id = promoted.workspace_id
		 AND mention_member.user_id = mention.user_id
		 AND mention_member.status = 'active'
		JOIN auth.users mention_recipient
		  ON mention_recipient.id = mention.user_id
		 AND mention_recipient.status = 'active'
		 AND mention_recipient.deleted_at IS NULL
		WHERE promoted.published
		  AND (
		    (promoted.channel_id <> '' AND EXISTS (
		      SELECT 1
		      FROM chat.channels mention_channel
		      JOIN chat.channel_members mention_channel_member
		        ON mention_channel_member.channel_id = mention_channel.id
		       AND mention_channel_member.user_id = mention.user_id
		      WHERE mention_channel.id = promoted.channel_id::uuid
		        AND mention_channel.workspace_id = promoted.workspace_id
		        AND mention_channel.status = 'active'
		    ))
		    OR
		    (promoted.conversation_id <> '' AND EXISTS (
		      SELECT 1
		      FROM chat.dm_conversations mention_dm
		      JOIN chat.dm_members mention_dm_member
		        ON mention_dm_member.conversation_id = mention_dm.id
		       AND mention_dm_member.user_id = mention.user_id
		       AND mention_dm_member.status = 'active'
		      WHERE mention_dm.id = promoted.conversation_id::uuid
		        AND mention_dm.workspace_id = promoted.workspace_id
		        AND mention_dm.type = 'group'
		        AND mention_dm.status = 'active'
		    ))
		  )
		ON CONFLICT (message_id, recipient_user_id, kind) DO NOTHING
		RETURNING message_id
	)
	SELECT id::text, published, has_inconclusive FROM promoted`

// ResolveDecidedMessages promotes or blocks withheld messages whose links have
// all been decided.
//
// It no longer returns anything to broadcast, and that is the point of this
// round's change: promotion and the event announcing it are now written in one
// statement, so the caller has nothing to do afterwards that a crash could skip.
// What comes back is a summary — how many were released, how many were blocked —
// for logging and metrics only.
//
// The UPDATE's own predicate (status = 'pending_link_scan') is what makes the
// promotion exactly-once: two replicas may both evaluate the rule, only one
// finds the row still pending, and the outbox row is keyed by message id so even
// a double promotion could only ever produce one event.
func (s *PGXMessageStore) ResolveDecidedMessages(ctx context.Context) (ResolveSummary, error) {
	rows, err := s.pool.Query(ctx, resolvePendingMessagesQuery,
		maxPendingResolveBatch, urlsafety.VerdictTTL.Seconds())
	if err != nil {
		return ResolveSummary{}, fmt.Errorf("resolve decided messages: %w", err)
	}
	defer rows.Close()

	var summary ResolveSummary
	for rows.Next() {
		var id string
		var published, inconclusive bool
		if err := rows.Scan(&id, &published, &inconclusive); err != nil {
			return ResolveSummary{}, fmt.Errorf("scan resolved message: %w", err)
		}
		if !published {
			summary.Blocked++
			continue
		}
		summary.Published++
		if inconclusive {
			summary.PublishedInconclusive++
		}
	}
	if err := rows.Err(); err != nil {
		return ResolveSummary{}, fmt.Errorf("resolve decided messages: %w", err)
	}
	return summary, nil
}

// claimPublishEventsQuery leases due publish events.
//
// The same claim shape as the scan queue's, for the same reasons: SKIP LOCKED so
// several replicas can drain it without coordinating, and a backoff that grows
// with the attempt count so a hub that is refusing traffic does not become a
// retry storm.
const claimPublishEventsQuery = `
	WITH due AS (
		SELECT o.message_id
		FROM chat.message_publish_outbox o
		WHERE o.status = 'pending'
		  AND (o.next_attempt_at IS NULL OR o.next_attempt_at <= now())
		ORDER BY o.next_attempt_at NULLS FIRST, o.created_at
		LIMIT $1
		FOR UPDATE SKIP LOCKED
	)
	UPDATE chat.message_publish_outbox o
	   SET attempts = LEAST(o.attempts + 1, 32767),
	       next_attempt_at = now() + ($2 * LEAST(o.attempts + 1, $3) * interval '1 second'),
	       updated_at = now()
	  FROM due
	 WHERE o.message_id = due.message_id
	RETURNING o.message_id::text, o.workspace_id::text, o.event_type,
	          o.target_type, o.target_id::text, COALESCE(o.block_reason, '')`

// ClaimPublishEvents leases up to batchSize undelivered promotion events and
// returns them with the message payload the broadcast needs.
//
// The message is read back here rather than stored in the outbox row: a
// broadcast payload carries sender display info and a quote preview, and
// duplicating those into the event would mean maintaining two copies of the
// message shape. The row is already committed as active, so what is read is what
// every other reader sees.
func (s *PGXMessageStore) ClaimPublishEvents(ctx context.Context, batchSize int) ([]PublishEvent, error) {
	if batchSize <= 0 {
		return nil, nil
	}
	rows, err := s.pool.Query(ctx, claimPublishEventsQuery,
		batchSize, publishRetryBase.Seconds(), publishBackoffSteps)
	if err != nil {
		return nil, fmt.Errorf("claim publish events: %w", err)
	}
	var events []PublishEvent
	for rows.Next() {
		var event PublishEvent
		if err := rows.Scan(&event.MessageID, &event.WorkspaceID, &event.EventType,
			&event.TargetType, &event.TargetID, &event.Reason); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan publish event: %w", err)
		}
		events = append(events, event)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("claim publish events: %w", err)
	}

	ready := make([]PublishEvent, 0, len(events))
	for _, event := range events {
		// A refusal carries no body: the author is told their message was blocked,
		// and nothing about the content or the provider travels with it.
		if event.Blocked() {
			ready = append(ready, event)
			continue
		}
		message, err := s.readPublishedMessage(ctx, event.MessageID)
		switch {
		case errors.Is(err, domain.ErrNotFound):
			// The message was deleted between promotion and delivery, so there is
			// nothing left to announce — ever. Previously this was skipped and the
			// row stayed pending, retried forever and kept the backlog gauge
			// climbing over work that could never succeed. Cancelling is the
			// terminal state that says so.
			event.Cancelled = true
			ready = append(ready, event)
		case err != nil:
			// A transient database failure is not a reason to cancel: the row stays
			// pending and the next pass tries again.
			continue
		default:
			event.Message = message
			ready = append(ready, event)
		}
	}
	return ready, nil
}

// CancelPublishEvent retires an event that can never be delivered.
//
// Distinct from MarkPublished so the two are distinguishable in the table and in
// the metric: "delivered" and "there was nothing left to deliver" are different
// operational facts, and collapsing them would hide a message being deleted out
// from under its own announcement.
func (s *PGXMessageStore) CancelPublishEvent(ctx context.Context, messageID string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE chat.message_publish_outbox
		   SET status = 'cancelled', next_attempt_at = NULL, updated_at = now()
		 WHERE message_id = $1::uuid AND status = 'pending'`,
		messageID,
	)
	if err != nil {
		return fmt.Errorf("cancel publish event: %w", err)
	}
	return nil
}

// MarkPublished retires a delivered event.
//
// Delivery is at-least-once: a dispatcher can publish and then fail before this
// lands, so the same message.created may go out twice. The client deduplicates
// by message id, which is what makes that safe — and it is a far better failure
// than the alternative this replaced, where a crash after the commit meant the
// event was never sent at all.
func (s *PGXMessageStore) MarkPublished(ctx context.Context, messageID string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE chat.message_publish_outbox
		   SET status = 'delivered', next_attempt_at = NULL, updated_at = now()
		 WHERE message_id = $1::uuid AND status = 'pending'`,
		messageID,
	)
	if err != nil {
		return fmt.Errorf("mark message published: %w", err)
	}
	return nil
}

// PublishOutboxBacklog reports undelivered events and the age of the oldest, for
// the dispatcher's gauges.
func (s *PGXMessageStore) PublishOutboxBacklog(ctx context.Context) (int, time.Duration, error) {
	var pending int
	var ageSeconds float64
	err := s.pool.QueryRow(ctx, `
		SELECT count(*), COALESCE(EXTRACT(EPOCH FROM (now() - min(created_at))), 0)
		FROM chat.message_publish_outbox
		WHERE status = 'pending'`).Scan(&pending, &ageSeconds)
	if err != nil {
		return 0, 0, fmt.Errorf("publish outbox backlog: %w", err)
	}
	return pending, time.Duration(ageSeconds * float64(time.Second)), nil
}

// readPublishedMessage re-reads a just-promoted message with the columns a
// broadcast needs. It is scoped by the sender's own id because a broadcast
// payload is built once and the per-recipient favourite flag is not part of it.
func (s *PGXMessageStore) readPublishedMessage(ctx context.Context, messageID string) (domain.Message, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT `+listMessageWithQuoteColumns("m", "m.sender_id::text", "q")+`
		FROM chat.messages m
		LEFT JOIN auth.users u ON u.id = m.sender_id`+quotedMessageJoin("m", "q")+`
		WHERE m.id = $1::uuid AND m.status = 'active'`,
		messageID,
	)
	message, err := scanMessageWithSenderAndQuote(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Message{}, domain.ErrNotFound
		}
		return domain.Message{}, fmt.Errorf("read published message: %w", err)
	}
	return message, nil
}
