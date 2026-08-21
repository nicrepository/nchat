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

// Recovering from an inconclusive scan (issue #135).
//
// # What this is not
//
// It is not a retry of the submission. A row reaches 'inconclusive' because the
// provider confirmed a scan finished and produced nothing, and the real
// production trigger was Cloudflare refusing outright because the hostname had
// been scanned too recently. There is no idempotency token on the submit
// endpoint, so a second POST is a second billed scan — and for that particular
// refusal it is the one action guaranteed not to help.
//
// So there is no path from any function in this file to a submission. Nothing
// here clears scan_uuid, nothing writes status='pending', nothing resets
// attempts, and nothing touches submit_generation or submit_attempt_started_at.
// The only writes are 'inconclusive' -> 'safe' | 'malicious', plus the
// reconciliation bookkeeping that schedules the next read.
//
// # The one allowed transition, and why it is safe to allow it
//
// A verdict written here comes from the provider's full result report for a scan
// uuid discovered by urlsafety.Reconcile and read back through
// urlsafety.Service.GetScanReport — the same strict path an ordinary poll uses,
// applying the same identity, task.success and hasVerdicts checks. Nothing weaker may write a verdict: the provider's search
// answer, which does carry a summarised verdict field, is used only to discover a
// scan id and is structurally incapable of reaching this file, because
// urlsafety.ScanRecord has no verdict field to read.

const (
	// maxReconcileClaimBatch bounds one background reconciliation pass.
	//
	// Deliberately much smaller than the scan batch. Reconciliation is a
	// best-effort recovery for something already terminal, and every item costs
	// two provider exchanges (a search and a result read) rather than one. Nobody
	// is waiting on a pass, so the pass may be slow.
	maxReconcileClaimBatch = 4

	// maxMessageLinkSafetyRefresh is the size of one convergence batch — not a
	// ceiling on how many messages converge.
	//
	// A single popular URL can appear in a great many messages, and updating them
	// all in one statement would be an unbounded UPDATE and an unbounded fan-out of
	// realtime events. So the work is batched here, and the caller drains batches
	// until nothing is left (see LinkReconcileService.converge). The drain
	// terminates because the query only returns rows whose marker it is actually
	// changing, so every batch strictly consumes work.
	//
	// ponytail: the recompute aggregates every message naming the URL on each
	// batch, so draining N messages is O(N²/batch). At this batch size that is
	// unnoticeable for any realistic number of messages sharing one link, and the
	// alternative — a keyset cursor threaded through the service's drain loop —
	// buys nothing until a single URL appears in six figures of messages. Add the
	// cursor then, not before.
	maxMessageLinkSafetyRefresh = 500
)

// ReconcileSchedule is the backoff for background reconciliation of one
// inconclusive URL, as durations from the moment of each attempt.
//
// Four attempts and then nothing. That is the whole loop protection: the column
// counts, the count is compared against the length of this list, and a URL that
// has been asked about four times is never asked again by the background pass. A
// reader pressing "Verificar novamente" is a separate, rate-limited path and is
// not bounded by this — a person asking is evidence the answer is wanted, which a
// timer is not.
//
// The values spread deliberately wide. What is being waited for is Cloudflare's
// own per-hostname cooldown expiring or somebody else's scan of the same URL
// landing in the account's history, and neither happens on a timescale a tight
// poll would help with.
var ReconcileSchedule = []time.Duration{
	5 * time.Minute,
	30 * time.Minute,
	2 * time.Hour,
	6 * time.Hour,
}

// MessageLinkSafetyChange is one published message whose link-safety state just
// changed, and the audience that has to be told.
//
// It carries no URL, no scan id and no body. The realtime event built from it
// carries even less — see ws.MessageLinkSafetyPayload — because a subscriber
// needs to know that a message's links changed status, and nothing about which
// link or why.
type MessageLinkSafetyChange struct {
	MessageID   string
	WorkspaceID string
	// TargetType is "channel" or "dm": the audience that received the message
	// when it was published, which is exactly the audience that must converge.
	TargetType string
	TargetID   string
	State      domain.MessageLinkSafety
	UpdatedAt  time.Time
}

// MessageInconclusiveURLs returns the canonical URLs of one message whose scans
// finished without a usable verdict, for a caller authorised to read it.
//
// # This is the whole of the manual endpoint's input validation
//
// The client sends a message id and nothing else. It does not send a URL and it
// does not send a scan uuid, because neither is a parameter here: the URLs are
// read out of chat.message_link_scans, bound to the fingerprint the message
// currently carries, so the only URLs that can ever be reconciled are ones this
// server itself recorded for this very message. Without that, the endpoint would
// be a way to make NChat's Cloudflare credentials search for an arbitrary URL —
// a free provider proxy paid for by this account.
//
// # Authorization
//
// The same read authorization every other message read applies, expressed with
// the same shared predicates: active workspace, active membership, and either
// channel visibility or DM participation. A withheld message is excluded, since
// there is nothing published to reconcile. A caller who cannot read the message
// gets ErrNotFound and not a refusal, so the endpoint cannot be used to discover
// which message ids exist.
//
// A message the caller may read but which has no inconclusive link is also
// ErrNotFound. There is nothing to do, and answering "nothing to do" distinctly
// would let a reader probe the link-safety state of messages one at a time.
func (s *PGXMessageStore) MessageInconclusiveURLs(
	ctx context.Context, workspaceID, viewerID, messageID string,
) ([]string, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT mls.canonical_url
		FROM chat.messages m`+messageAccessJoins("$2")+`
		JOIN chat.message_link_scans mls
		  ON mls.message_id = m.id
		 AND mls.fingerprint = COALESCE(m.link_safety_fingerprint, '')
		JOIN chat.link_scans ls
		  ON ls.canonical_url = mls.canonical_url
		 AND ls.status = 'inconclusive'
		WHERE m.workspace_id = $1::uuid
		  AND m.id = $3::uuid
		  AND m.status = 'active'
		  AND `+messageAccessPredicate("$2"),
		workspaceID, viewerID, messageID,
	)
	if err != nil {
		return nil, fmt.Errorf("read inconclusive message links: %w", err)
	}
	defer rows.Close()

	var urls []string
	for rows.Next() {
		var url string
		if err := rows.Scan(&url); err != nil {
			return nil, fmt.Errorf("scan inconclusive message link: %w", err)
		}
		urls = append(urls, url)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read inconclusive message links: %w", err)
	}
	if len(urls) == 0 {
		return nil, domain.ErrNotFound
	}
	return urls, nil
}

// ReconcileLinkVerdict records a verdict obtained for a scan that had already
// finished without one.
//
// It is the *only* write that may leave the inconclusive state, and every clause
// in its predicate is load-bearing:
//
//   - status = 'inconclusive' makes this a one-way door. It cannot promote a
//     pending row (that is RecordLinkVerdict's job, with its own generation
//     checks) and it cannot overwrite a verdict already recorded;
//   - scan_uuid = the id the verdict was read from. A verdict is a statement
//     about one scan, and this row is only allowed to adopt it if it is the row
//     that owns that scan. Two concurrent reconciliations of the same URL cannot
//     write each other's answers, and a reconciliation whose row was superseded
//     writes nothing;
//   - scan_uuid IS NOT NULL, so a row that somehow reached this state without an
//     id cannot be decided by an answer that cannot belong to it.
//
// decided_at is not the same for both outcomes, and the split is in the two
// helpers below. A clearance is dated from the provider's own observation time,
// so adopting an hours-old safe report does not buy a fresh VerdictTTL — it buys
// whatever that report has left, which is usually nothing, in which case the row
// simply stays inconclusive. A condemnation is dated from adoption, because
// retaining a restriction longer than its evidence's age grants no one anything.
//
// Non-final verdicts are refused outright. Inconclusive in particular: this
// function exists to leave that state, and being able to write it here would make
// "reconcile" a way to reset the attempt bookkeeping.
func (s *PGXMessageStore) ReconcileLinkVerdict(
	ctx context.Context, canonicalURL, scanUUID string, evidence urlsafety.ScanEvidence,
) error {
	if !evidence.Verdict.IsFinal() {
		return fmt.Errorf("%w: reconciliation may only record a final verdict", domain.ErrInvalidInput)
	}
	if evidence.ObservedAt.IsZero() {
		// A verdict with no evidence time has no honest lifetime, and inventing one
		// is the finding this rule exists for. Refused rather than dated locally.
		return fmt.Errorf("%w: reconciliation may only record dated evidence", domain.ErrInvalidInput)
	}

	if evidence.Verdict == urlsafety.VerdictMalicious {
		return s.reconcileMalicious(ctx, canonicalURL, scanUUID)
	}
	return s.reconcileSafe(ctx, canonicalURL, scanUUID, evidence)
}

// reconcileSafe records a clearance, dated by the provider.
//
// decided_at is the evidence time, so the freshness rule every reader already
// applies — `decided_at > now() - VerdictTTL` — gives this clearance exactly the
// lifetime it has left and not a fresh one. The `> now()` guard repeats the check
// urlsafety.Reconcile already made, at the point of the write, so the store
// cannot be talked into rejuvenating stale evidence by a future caller.
func (s *PGXMessageStore) reconcileSafe(
	ctx context.Context, canonicalURL, scanUUID string, evidence urlsafety.ScanEvidence,
) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE chat.link_scans
		   SET status = $2, decided_at = $4, next_attempt_at = NULL,
		       next_reconcile_at = NULL, updated_at = now()
		 WHERE canonical_url = $1
		   AND status = 'inconclusive'
		   AND scan_uuid IS NOT NULL
		   AND scan_uuid = $3
		   AND $5 > now()`,
		canonicalURL, string(evidence.Verdict), scanUUID,
		evidence.ObservedAt, evidence.ExpiresAt(),
	)
	if err != nil {
		return fmt.Errorf("reconcile link verdict: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrLinkScanConflict
	}
	return nil
}

// reconcileMalicious records a condemnation and publishes it to the shared fetch
// authority in the same statement (issue #135, CQ-002).
//
// Two asymmetries with the clearance path, both deliberate:
//
//   - decided_at is now, not the evidence time. A restriction is not a
//     permission: retaining it longer than the evidence's own age grants nothing
//     and only keeps a known-bad URL refused for longer. Back-dating it would let
//     the row age out of every reader's freshness window immediately, which would
//     *discard* the finding — the opposite of conservative;
//   - it writes files.link_fetch_denylist and expires any live files.link_scans
//     clearance, both inside this statement. Until that lands, file-service could
//     still be holding a fresh SAFE row for this URL and would fetch it. Doing it
//     here rather than afterwards is what makes "no fetch after any component
//     knows" true rather than eventually true. If the invalidation fails, the
//     whole statement fails and the condemnation is not recorded either — the row
//     stays inconclusive, which still refuses every fetch.
func (s *PGXMessageStore) reconcileMalicious(
	ctx context.Context, canonicalURL, scanUUID string,
) error {
	return s.recordMaliciousLinkVerdict(ctx, canonicalURL, scanUUID, "inconclusive")
}

// refreshMessageLinkSafetyQuery recomputes the per-message link-safety marker for
// the published messages that name one URL.
//
// # Why the aggregate is recomputed rather than assigned
//
// A message may carry several links. Learning that one of them is malicious does
// not tell you the message's state — but learning that one of them is now safe
// certainly does not either, because another may still be inconclusive. So the
// whole set is folded again, by the same precedence the resolver uses:
// malicious > inconclusive > safe.
//
// # Why an undecided link leaves the marker alone
//
// EnsureLinkScans legitimately moves a lapsed safe or malicious row back to
// 'pending' when somebody sends the same URL again. During that window the row
// carries no opinion, and folding it in as "not malicious" would quietly drop a
// block from a message that had one. So the aggregate returns NULL unless every
// link is decided, and a NULL updates nothing: the message keeps the marker it
// already had until there is a complete answer to replace it with.
//
// That is also why this cannot be a read-time expression. A derived value has no
// "keep what you had".
const refreshMessageLinkSafetyQuery = `
	WITH candidate AS (
		-- Every published message that names this URL. Not limited here: the batch
		-- has to be taken *after* the recompute, because limiting first would keep
		-- handing back the same already-converged rows and the drain would stall
		-- with work still outstanding — the bug the scale test exists to catch.
		SELECT DISTINCT m.id, m.link_safety_state,
		       COALESCE(m.link_safety_fingerprint, '') AS fingerprint
		FROM chat.message_link_scans mls
		JOIN chat.messages m
		  ON m.id = mls.message_id
		 AND mls.fingerprint = COALESCE(m.link_safety_fingerprint, '')
		WHERE mls.canonical_url = $1
		  AND m.status = 'active'
	),
	recomputed AS MATERIALIZED (
		SELECT candidate.id, candidate.fingerprint,
		       CASE
		         WHEN bool_or(ls.status = 'malicious') THEN 'malicious'
		         WHEN bool_and(ls.status IN ('safe', 'malicious', 'inconclusive'))
		          AND bool_or(ls.status = 'inconclusive') THEN 'inconclusive'
		         WHEN bool_and(ls.status = 'safe') THEN 'safe'
		         ELSE NULL
		       END AS state
		FROM candidate
		JOIN chat.messages m ON m.id = candidate.id
		JOIN chat.message_link_scans mls
		  ON mls.message_id = m.id
		 AND mls.fingerprint = COALESCE(m.link_safety_fingerprint, '')
		JOIN chat.link_scans ls ON ls.canonical_url = mls.canonical_url
		GROUP BY candidate.id, candidate.link_safety_state, candidate.fingerprint
		-- Only rows that will actually change, so every batch strictly consumes
		-- work and the caller's drain is guaranteed to make progress.
		HAVING CASE
		         WHEN bool_or(ls.status = 'malicious') THEN 'malicious'
		         WHEN bool_and(ls.status IN ('safe', 'malicious', 'inconclusive'))
		          AND bool_or(ls.status = 'inconclusive') THEN 'inconclusive'
		         WHEN bool_and(ls.status = 'safe') THEN 'safe'
		         ELSE NULL
		       END IS DISTINCT FROM candidate.link_safety_state
		   AND CASE
		         WHEN bool_or(ls.status = 'malicious') THEN 'malicious'
		         WHEN bool_and(ls.status IN ('safe', 'malicious', 'inconclusive'))
		          AND bool_or(ls.status = 'inconclusive') THEN 'inconclusive'
		         WHEN bool_and(ls.status = 'safe') THEN 'safe'
		         ELSE NULL
		       END IS NOT NULL
		ORDER BY candidate.id
		LIMIT $2
	)
	UPDATE chat.messages m
	   SET link_safety_state = recomputed.state, updated_at = now()
	  FROM recomputed
	 WHERE m.id = recomputed.id
	   AND m.status = 'active'
	   -- The candidate CTE can wait on a concurrent edit after snapshotting its
	   -- old associations. Apply that fold only if the body identity and the URL
	   -- association which selected it are still current after the wait.
	   AND COALESCE(m.link_safety_fingerprint, '') = recomputed.fingerprint
	   AND EXISTS (
	       SELECT 1
	       FROM chat.message_link_scans current_mls
	       WHERE current_mls.message_id = m.id
	         AND current_mls.canonical_url = $1
	         AND current_mls.fingerprint = recomputed.fingerprint
	   )
	   -- Re-checked at write time as well as in the HAVING: between the two, another
	   -- writer may have converged this row already, and re-announcing a state every
	   -- client already has is exactly what the drain must not do.
	   AND m.link_safety_state <> recomputed.state
	RETURNING m.id::text, m.workspace_id::text, m.link_safety_state, m.updated_at,
	          COALESCE(m.channel_id::text, ''), COALESCE(m.dm_conversation_id::text, '')`

// RefreshMessageLinkSafety recomputes and persists the link-safety marker of the
// published messages that name canonicalURL, and reports the ones that changed.
//
// The returned list is what the realtime announcement is built from, and it is
// deliberately the *changed* set rather than the affected set: a subscriber that
// already holds the right state must not be sent an event, or every background
// pass would be a broadcast.
func (s *PGXMessageStore) RefreshMessageLinkSafety(
	ctx context.Context, canonicalURL string,
) ([]MessageLinkSafetyChange, error) {
	rows, err := s.pool.Query(ctx, refreshMessageLinkSafetyQuery,
		canonicalURL, maxMessageLinkSafetyRefresh)
	if err != nil {
		return nil, fmt.Errorf("refresh message link safety: %w", err)
	}
	defer rows.Close()

	var changes []MessageLinkSafetyChange
	for rows.Next() {
		var change MessageLinkSafetyChange
		var state, channelID, conversationID string
		if err := rows.Scan(&change.MessageID, &change.WorkspaceID,
			&state, &change.UpdatedAt, &channelID, &conversationID); err != nil {
			return nil, fmt.Errorf("scan refreshed message link safety: %w", err)
		}
		change.State = domain.MessageLinkSafety(state)
		// Exactly one of the two is set by the schema's own CHECK, so this cannot
		// pick the wrong audience: a channel message is announced to its channel and
		// a DM to its conversation, which is the same routing the original
		// message.created used.
		if channelID != "" {
			change.TargetType, change.TargetID = TargetChannel, channelID
		} else {
			change.TargetType, change.TargetID = TargetDM, conversationID
		}
		changes = append(changes, change)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("refresh message link safety: %w", err)
	}
	return changes, nil
}

// claimDueReconciliationsQuery leases inconclusive URLs the background pass may
// re-read, and schedules the next attempt in the same statement.
//
// The claim *is* the schedule and the attempt counter, exactly like the scan
// queue's: SKIP LOCKED so replicas do not coordinate, and a bookkeeping write
// that happens whether or not the provider is then reachable. That is what makes
// this terminate — a failing provider still consumes attempts, so an unreachable
// Cloudflare cannot turn into an endless search loop.
//
// Two filters bound the work to what is worth doing:
//
//   - reconcile_attempts < the length of the schedule. This is the hard stop.
//     There is no branch anywhere that resets this counter;
//   - the URL must currently be the reason a *published* message is showing a
//     warning. A URL nothing is waiting on is not reconciled at all: the answer
//     would be correct and useless, and it would be paid for at the provider.
var claimDueReconciliationsQuery = `
	WITH due AS (
		SELECT ls.canonical_url
		FROM chat.link_scans ls
		WHERE ls.status = 'inconclusive'
		  AND ls.scan_uuid IS NOT NULL
		  AND ls.reconcile_attempts < $2
		  AND (ls.next_reconcile_at IS NULL OR ls.next_reconcile_at <= now())
		  AND EXISTS (
		      SELECT 1
		      FROM chat.message_link_scans mls
		      JOIN chat.messages m ON m.id = mls.message_id
		      WHERE mls.canonical_url = ls.canonical_url
		        AND mls.fingerprint = COALESCE(m.link_safety_fingerprint, '')
		        AND m.status = 'active'
		        AND m.link_safety_state = 'inconclusive'
		  )
		  -- Skip what file-service is already reading, so a contended URL does not
		  -- occupy a slot in this LIMIT and starve the pass. The upsert below is
		  -- what actually decides the race.
		  AND ` + urlsafety.ReconcileLeaseAvailablePredicate("sha256(ls.canonical_url::bytea)") + `
		ORDER BY ls.next_reconcile_at NULLS FIRST, ls.created_at
		LIMIT $1
		FOR UPDATE SKIP LOCKED
	),` + urlsafety.AcquireReconcileLeaseSQL(
	"due", "sha256(due.canonical_url::bytea)", "due.canonical_url",
	"'"+urlsafety.DenylistSourceChat+"'",
) + `,
	-- Only the URLs this service won the lease for. A row the other service is
	-- holding never reaches the UPDATE, so it never spends a reconcile attempt.
	won AS (
		SELECT due.canonical_url
		FROM due
		JOIN leased ON leased.url_digest = sha256(due.canonical_url::bytea)
	)
	UPDATE chat.link_scans ls
	   SET reconcile_attempts = ls.reconcile_attempts + 1,
	       next_reconcile_at = now() + (
	           ($3::double precision[])[
	               LEAST(ls.reconcile_attempts + 1, array_length($3::double precision[], 1))
	           ] * interval '1 second'
	       ),
	       updated_at = now()
	  FROM won
	 WHERE ls.canonical_url = won.canonical_url
	RETURNING ls.canonical_url, COALESCE(ls.scan_uuid, '')`

// ClaimDueInconclusiveScans leases up to batchSize inconclusive URLs for the
// background reconciliation pass, and returns each with the scan id its verdict
// must be read from.
//
// The scan id comes from the row and never from a caller. It is what binds the
// eventual write back to this exact scan, and it is also why the claim refuses a
// row without one.
func (s *PGXMessageStore) ClaimDueInconclusiveScans(
	ctx context.Context, batchSize int,
) ([]InconclusiveScan, error) {
	if batchSize <= 0 {
		return nil, nil
	}
	if batchSize > maxReconcileClaimBatch {
		batchSize = maxReconcileClaimBatch
	}
	seconds := make([]float64, len(ReconcileSchedule))
	for i, delay := range ReconcileSchedule {
		seconds[i] = delay.Seconds()
	}
	rows, err := s.pool.Query(ctx, claimDueReconciliationsQuery,
		batchSize, len(ReconcileSchedule), seconds)
	if err != nil {
		return nil, fmt.Errorf("claim due reconciliations: %w", err)
	}
	defer rows.Close()

	var scans []InconclusiveScan
	for rows.Next() {
		var scan InconclusiveScan
		if err := rows.Scan(&scan.CanonicalURL, &scan.ScanUUID); err != nil {
			return nil, fmt.Errorf("scan due reconciliation: %w", err)
		}
		scans = append(scans, scan)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("claim due reconciliations: %w", err)
	}
	return scans, nil
}

// InconclusiveScan is one terminal-without-verdict scan a reconciliation may
// re-read: the URL to search for and the scan id to read the answer from.
type InconclusiveScan struct {
	CanonicalURL string
	ScanUUID     string
}

// ManualReconcileCooldown is the minimum interval between two provider
// reconciliations of one URL driven by readers pressing "Verificar novamente".
//
// It is the rate limit that actually protects the Cloudflare account, and it is
// deliberately in the database rather than in a process: the per-user HTTP limiter
// bounds how often one person may ask, but ten people in a channel looking at the
// same warning are ten different users on however many replicas, and the number
// the provider counts is per URL. One minute per URL, deployment-wide.
//
// A click inside the cooldown is not an error. The caller answers with the state
// the message currently has and a retry hint, which is both honest — nothing new
// was learned — and what stops a client from treating the button as a poll.
const ManualReconcileCooldown = time.Minute

// ClaimManualReconcile takes the reader-driven reconciliation slot for each of
// canonicalURLs, and returns only the ones this caller may now ask the provider
// about.
//
// The cooldown is applied by the same next_reconcile_at the background schedule
// uses, which is what makes the two paths share one budget instead of two: a URL
// is asked about at most once a minute in total, no matter which path asks.
//
// It deliberately does *not* consume reconcile_attempts. That counter is the
// background pass's hard stop, and a person clicking a button is evidence the
// answer is wanted in a way a timer is not — spending the automatic budget on a
// manual request would let a few clicks silence the schedule for good. The manual
// path's own bound is this cooldown plus the per-user HTTP limiter.
//
// A URL absent from the result is one whose slot was taken, or one that is no
// longer inconclusive. Both mean the same thing to the caller: do not ask the
// provider, report the state that is stored.
func (s *PGXMessageStore) ClaimManualReconcile(
	ctx context.Context, canonicalURLs []string,
) ([]InconclusiveScan, error) {
	if len(canonicalURLs) == 0 {
		return nil, nil
	}
	rows, err := s.pool.Query(ctx, `
		WITH due AS (
			SELECT ls.canonical_url
			FROM chat.link_scans ls
			WHERE ls.canonical_url = ANY($1::text[])
			  AND ls.status = 'inconclusive'
			  AND ls.scan_uuid IS NOT NULL
			  AND (ls.next_reconcile_at IS NULL OR ls.next_reconcile_at <= now())
			  AND `+urlsafety.ReconcileLeaseAvailablePredicate("sha256(ls.canonical_url::bytea)")+`
			FOR UPDATE SKIP LOCKED
		),`+urlsafety.AcquireReconcileLeaseSQL(
		"due", "sha256(due.canonical_url::bytea)", "due.canonical_url",
		"'"+urlsafety.DenylistSourceChat+"'",
	)+`,
		-- Same cross-service gate as the background pass. A reader pressing
		-- "Verificar novamente" while file-service is mid-read gets the state
		-- that read produces, without a second search on the same account.
		won AS (
			SELECT due.canonical_url
			FROM due
			JOIN leased ON leased.url_digest = sha256(due.canonical_url::bytea)
		)
		UPDATE chat.link_scans ls
		   SET next_reconcile_at = now() + ($2 * interval '1 second'),
		       updated_at = now()
		  FROM won
		 WHERE ls.canonical_url = won.canonical_url
		RETURNING ls.canonical_url, COALESCE(ls.scan_uuid, '')`,
		canonicalURLs, ManualReconcileCooldown.Seconds(),
	)
	if err != nil {
		return nil, fmt.Errorf("claim manual reconcile: %w", err)
	}
	defer rows.Close()

	var scans []InconclusiveScan
	for rows.Next() {
		var scan InconclusiveScan
		if err := rows.Scan(&scan.CanonicalURL, &scan.ScanUUID); err != nil {
			return nil, fmt.Errorf("scan manual reconcile claim: %w", err)
		}
		scans = append(scans, scan)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("claim manual reconcile: %w", err)
	}
	return scans, nil
}

// LookupInconclusiveScans resolves canonical URLs to the scans that must be
// re-read for them, keeping only the ones still inconclusive.
//
// Used by the manual path, which starts from a message rather than from the
// queue. A URL that is no longer inconclusive — reconciled by the background pass
// a moment ago, or by another reader's click — is simply absent, and the caller
// reports the state it now has instead of asking the provider again.
func (s *PGXMessageStore) LookupInconclusiveScans(
	ctx context.Context, canonicalURLs []string,
) ([]InconclusiveScan, error) {
	if len(canonicalURLs) == 0 {
		return nil, nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT canonical_url, scan_uuid
		FROM chat.link_scans
		WHERE canonical_url = ANY($1::text[])
		  AND status = 'inconclusive'
		  AND scan_uuid IS NOT NULL`,
		canonicalURLs,
	)
	if err != nil {
		return nil, fmt.Errorf("lookup inconclusive scans: %w", err)
	}
	defer rows.Close()

	var scans []InconclusiveScan
	for rows.Next() {
		var scan InconclusiveScan
		if err := rows.Scan(&scan.CanonicalURL, &scan.ScanUUID); err != nil {
			return nil, fmt.Errorf("scan inconclusive scan: %w", err)
		}
		scans = append(scans, scan)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("lookup inconclusive scans: %w", err)
	}
	return scans, nil
}

// MessageLinkSafety reads back the marker one message currently carries, for a
// caller authorised to read it.
//
// It is what the manual endpoint answers with, so a client that clicked and got
// nothing new still learns the authoritative state rather than being told to
// assume. Scoped by the same read authorization as everything else; an
// unauthorised or missing id is ErrNotFound.
func (s *PGXMessageStore) MessageLinkSafety(
	ctx context.Context, workspaceID, viewerID, messageID string,
) (domain.MessageLinkSafety, time.Time, error) {
	var state string
	var updatedAt time.Time
	err := s.pool.QueryRow(ctx, `
		SELECT m.link_safety_state, m.updated_at
		FROM chat.messages m`+messageAccessJoins("$2")+`
		WHERE m.workspace_id = $1::uuid
		  AND m.id = $3::uuid
		  AND `+messageVisibilityPredicate("m", "$2")+`
		  AND `+messageAccessPredicate("$2"),
		workspaceID, viewerID, messageID,
	).Scan(&state, &updatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.MessageLinkSafetyNone, time.Time{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.MessageLinkSafetyNone, time.Time{}, fmt.Errorf("read message link safety: %w", err)
	}
	return domain.MessageLinkSafety(state), updatedAt, nil
}
