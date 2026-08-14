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
	// linkScanLease is how long a claimed row is left alone. It has to outlive
	// one provider exchange with room to spare, so a worker that is merely slow
	// does not have its row stolen and submitted twice.
	linkScanLease = 60 * time.Second

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

// LinkScanJob is one claimed canonical URL, with everything the worker needs to
// either submit it or read its result.
type LinkScanJob struct {
	CanonicalURL string
	// ScanUUID is empty until the URL has been submitted. Empty means "submit
	// me"; set means "read my result".
	ScanUUID string
	// Attempts includes this claim. It is a diagnostic, not a budget.
	Attempts int
}

// ResolvedMessage is a withheld message whose links have all been decided.
type ResolvedMessage struct {
	Message domain.Message
	// Published is true when the message was promoted to active and must now be
	// broadcast exactly once; false when it was blocked and must never be.
	Published bool
	// TargetType is "channel" or "dm", so the caller can publish to the right
	// topic without re-deriving it.
	TargetType string
	TargetID   string
}

// LoadLinkVerdicts returns the fresh verdicts known for canonicalURLs.
//
// Freshness is decided here and not by the caller: a row older than the shared
// VerdictTTL is not returned at all, so an expired clearance behaves exactly
// like a URL nobody ever scanned — the message waits for a new scan rather than
// riding a verdict from last week. Reputation changes in both directions, and a
// cache that never expires is a control that only ever gets weaker.
//
// A URL absent from the result has no usable verdict. There is no third return
// value saying why, because every reason has the same consequence.
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
		  AND status <> 'pending'
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
		if verdict := urlsafety.Verdict(status); verdict.IsFinal() {
			verdicts[url] = verdict
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load link verdicts: %w", err)
	}
	return verdicts, nil
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
	_, err = s.pool.Exec(ctx, `
		UPDATE chat.link_scans
		   SET status = 'pending', scan_uuid = NULL, decided_at = NULL,
		       attempts = 0, next_attempt_at = NULL, updated_at = now()
		 WHERE canonical_url = ANY($1::text[])
		   AND status <> 'pending'
		   AND decided_at <= now() - ($2 * interval '1 second')`,
		canonicalURLs, urlsafety.VerdictTTL.Seconds(),
	)
	if err != nil {
		return fmt.Errorf("reopen expired link scans: %w", err)
	}
	return nil
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
	RETURNING ls.canonical_url, COALESCE(ls.scan_uuid, ''), ls.attempts`

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
		if err := rows.Scan(&job.CanonicalURL, &job.ScanUUID, &job.Attempts); err != nil {
			return nil, fmt.Errorf("scan link scan job: %w", err)
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("claim due link scans: %w", err)
	}
	return jobs, nil
}

// RecordLinkScanSubmission stores the provider's scan id.
//
// Only a row still pending is updated. A URL that was decided while this
// submission was in flight keeps its verdict: the answer already obtained is not
// replaced by a scan that has not finished.
func (s *PGXMessageStore) RecordLinkScanSubmission(ctx context.Context, canonicalURL, scanUUID string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE chat.link_scans
		   SET scan_uuid = $2, updated_at = now()
		 WHERE canonical_url = $1 AND status = 'pending'`,
		canonicalURL, scanUUID,
	)
	if err != nil {
		return fmt.Errorf("record link scan submission: %w", err)
	}
	return nil
}

// RecordLinkVerdict stores a final verdict.
//
// It refuses anything that is not an explicit clearance or condemnation, which
// is the same rule the shared package applies: a zero value, a value from a
// future version, or a provider client bug must not be able to write a row that
// later reads as safe.
func (s *PGXMessageStore) RecordLinkVerdict(
	ctx context.Context, canonicalURL string, verdict urlsafety.Verdict,
) error {
	if !verdict.IsFinal() {
		return fmt.Errorf("%w: refusing to store a non-final verdict", domain.ErrInvalidInput)
	}
	_, err := s.pool.Exec(ctx, `
		UPDATE chat.link_scans
		   SET status = $2, decided_at = now(), next_attempt_at = NULL, updated_at = now()
		 WHERE canonical_url = $1 AND status = 'pending'`,
		canonicalURL, string(verdict),
	)
	if err != nil {
		return fmt.Errorf("record link verdict: %w", err)
	}
	return nil
}

// resolvePendingMessagesQuery decides every withheld message whose links are all
// decided, in one statement.
//
// The rule is stated once, in SQL, because it has to be atomic with the state
// change: a message is blocked if *any* of its URLs is malicious, published if
// *all* of them are safe, and left alone otherwise. Computing it in Go and then
// writing the result would leave a window in which two replicas both decided to
// publish the same message and both broadcast it.
//
// The UPDATE's own predicate — status = 'pending_link_scan' — is what makes the
// promotion exactly-once. Two replicas may both evaluate the rule; only one
// UPDATE finds the row still pending, and only that one gets a RETURNING row to
// publish from. The other publishes nothing.
const resolvePendingMessagesQuery = `
	WITH decided AS (
		SELECT m.id,
		       bool_or(ls.status = 'malicious') AS blocked,
		       bool_and(ls.status <> 'pending') AS complete
		FROM chat.messages m
		JOIN chat.message_link_scans mls ON mls.message_id = m.id
		JOIN chat.link_scans ls ON ls.canonical_url = mls.canonical_url
		WHERE m.status = 'pending_link_scan'
		GROUP BY m.id
		LIMIT $1
	)
	UPDATE chat.messages m
	   SET status = CASE WHEN decided.blocked THEN 'deleted' ELSE 'active' END,
	       deleted_at = CASE WHEN decided.blocked THEN now() ELSE m.deleted_at END,
	       updated_at = now()
	  FROM decided
	 WHERE m.id = decided.id
	   AND m.status = 'pending_link_scan'
	   AND (decided.blocked OR decided.complete)
	RETURNING m.id, NOT decided.blocked AS published,
	          COALESCE(m.channel_id::text, ''), COALESCE(m.dm_conversation_id::text, '')`

// ResolveDecidedMessages promotes or blocks withheld messages whose links have
// all been decided, and returns what happened so the caller can broadcast.
//
// The returned messages are re-read afterwards rather than assembled from the
// UPDATE, because a broadcast payload needs sender display info and the quote
// preview, which the UPDATE does not have. Re-reading is safe here in a way it
// is not elsewhere: the row is already committed as active, so what is read is
// what every other reader will see.
func (s *PGXMessageStore) ResolveDecidedMessages(ctx context.Context) ([]ResolvedMessage, error) {
	rows, err := s.pool.Query(ctx, resolvePendingMessagesQuery, maxPendingResolveBatch)
	if err != nil {
		return nil, fmt.Errorf("resolve decided messages: %w", err)
	}
	type outcome struct {
		id             string
		published      bool
		channelID      string
		conversationID string
	}
	var outcomes []outcome
	for rows.Next() {
		var got outcome
		if err := rows.Scan(&got.id, &got.published, &got.channelID, &got.conversationID); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan resolved message: %w", err)
		}
		outcomes = append(outcomes, got)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("resolve decided messages: %w", err)
	}

	resolved := make([]ResolvedMessage, 0, len(outcomes))
	for _, got := range outcomes {
		entry := ResolvedMessage{Published: got.published}
		entry.TargetType, entry.TargetID = "channel", got.channelID
		if got.channelID == "" {
			entry.TargetType, entry.TargetID = "dm", got.conversationID
		}
		if !got.published {
			// A blocked message is never broadcast, so nothing about it needs to
			// be read back. Only the fact that it was blocked matters.
			entry.Message = domain.Message{ID: got.id}
			resolved = append(resolved, entry)
			continue
		}
		message, err := s.readPublishedMessage(ctx, got.id)
		if err != nil {
			// The promotion is committed; failing to build its payload must not
			// undo it. Skipping the broadcast leaves the message visible on the
			// next read, which is the same outcome a dropped websocket frame has.
			continue
		}
		entry.Message = message
		resolved = append(resolved, entry)
	}
	return resolved, nil
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
