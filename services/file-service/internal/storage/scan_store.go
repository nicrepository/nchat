package storage

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/nicrepository/nchat/services/file-service/internal/domain"
	"github.com/nicrepository/nchat/services/file-service/internal/service"
)

// PGXScanStore is the antimalware worker's half of files.attachments (RF-22).
//
// It is a separate type from PGXAttachmentStore for the same reason
// PGXPreviewStore is: the two answer to different callers with different
// authority. This one runs in the background, selects rows nobody asked about,
// and touches only the scheduling columns. Keeping it apart means the request
// path physically cannot reach the claim query.
//
// It deliberately does not carry the two verdict statements. Those live on
// PGXAttachmentStore, behind service.ScanResultStore, because they are the
// lifecycle transitions the whole service is built around — approving an
// attachment also decides whether its preview may be rendered — and there must
// be exactly one implementation of them, not a second one the worker owns.
type PGXScanStore struct {
	pool Pool
}

func NewPGXScanStore(pool Pool) *PGXScanStore {
	return &PGXScanStore{pool: pool}
}

var _ service.ScanQueueStore = (*PGXScanStore)(nil)

// maxScanAttemptsCounter caps the stored attempt counter.
//
// scan_attempts is a SMALLINT and claiming is not bounded by it — a row stays
// due for as long as it is unscanned — so a daemon that has been down for weeks
// would eventually overflow the column and make the claim statement itself fail,
// stalling every other row behind it. Saturating costs one LEAST() and keeps the
// queue moving; the backoff below has reached its own ceiling long before this.
const maxScanAttemptsCounter = 32000

// scanBackoffSteps caps the multiplier the retry interval grows by.
//
// The backoff is deterministic and bounded: the nth attempt schedules the next
// one lease*min(n, steps) seconds out. That is enough to turn a daemon outage
// from a request per poll into a request every few minutes per attachment,
// which is the whole requirement — no busy loop, no storm against clamd — and
// it needs no jitter, because these are rows in one queue drained by a small
// number of workers, not a herd of clients racing back to a server.
const scanBackoffSteps = 8

// claimDueScansQuery takes a batch of due rows and leases them in one statement.
//
// FOR UPDATE SKIP LOCKED is the same queue primitive the preview claim and the
// notification outbox use: concurrent workers step over each other's rows
// instead of blocking, so every replica can run this loop and no row is handed
// out twice.
//
// The lease is the whole concurrency control, and it is also the retry schedule
// — one mechanism, not two. Claiming pushes the next attempt out by
// lease * min(attempts, scanBackoffSteps) and counts the try in the same UPDATE,
// so:
//
//   - a worker still streaming to clamd holds the row, because the multiplier is
//     at least one and the base lease outlives the scan timeout by construction;
//   - a worker that died holds nothing: the row is due again once its lease
//     expires, with no 'processing' state to get stuck in and no janitor to run;
//   - a daemon that keeps failing costs geometrically fewer attempts, up to the
//     ceiling, instead of one attempt per poll forever.
//
// There is deliberately no attempts ceiling in the predicate. A row nothing can
// scan must stay in the queue: the alternative is a fourth state meaning "gave
// up", which would be indistinguishable to a client from "still being scanned"
// and would need a manual sweep to leave. The cost of an unscannable row is one
// UPDATE per (stretched) lease, and the row stays exactly where it belongs —
// not downloadable.
//
// # Which rows are due
//
// status = 'pending_scan' is the queue. It is the state MarkUploaded writes for
// a finished upload whenever the scan is required, and the only one a verdict
// can be applied from — so the queue and the state machine are the same column
// and cannot disagree.
//
// deleted_at and the key columns complete the set: a removed attachment and one
// whose key was never sealed are both unopenable, and neither is a live file.
// A NULL scan_next_attempt_at counts as due, which is what makes the migration's
// backfill fail-safe — a row that somehow lacks a schedule is scanned, never
// skipped.
const claimDueScansQuery = `
	WITH due AS (
		SELECT a.id
		FROM files.attachments AS a
		WHERE a.status = 'pending_scan'
		  AND a.deleted_at IS NULL
		  AND a.wrapped_dek IS NOT NULL
		  AND a.kek_key_id IS NOT NULL
		  AND (a.scan_next_attempt_at IS NULL OR a.scan_next_attempt_at <= now())
		ORDER BY a.scan_next_attempt_at NULLS FIRST, a.created_at
		LIMIT $1
		FOR UPDATE SKIP LOCKED
	)
	UPDATE files.attachments AS a
	   SET scan_attempts = LEAST(a.scan_attempts + 1, $4),
	       scan_next_attempt_at =
	           now() + ($2 * LEAST(a.scan_attempts + 1, $3) * interval '1 second'),
	       updated_at = now()
	  FROM due
	 WHERE a.id = due.id
	RETURNING a.id::text, a.workspace_id::text, a.destination_kind,
	          COALESCE(a.channel_id::text, a.conversation_id::text),
	          a.size_bytes, a.storage_object_key, a.envelope_version,
	          a.wrapped_dek, COALESCE(a.kek_key_id, ''), a.dek_wrap_version,
	          a.scan_attempts`

// ClaimDueScans leases up to batchSize attachments awaiting a verdict.
func (s *PGXScanStore) ClaimDueScans(
	ctx context.Context, batchSize int, lease time.Duration,
) ([]service.ScanJob, error) {
	if s == nil || s.pool == nil {
		return nil, domain.ErrDependenciesUnavailable
	}
	if batchSize <= 0 || lease <= 0 {
		// A zero lease would hand the same row to every worker at once, and a
		// zero batch would spin. Both are wiring mistakes, refused not defaulted.
		return nil, fmt.Errorf("%w: invalid scan claim parameters", domain.ErrInvalidInput)
	}

	rows, err := s.pool.Query(ctx, claimDueScansQuery,
		batchSize, int64(lease.Seconds()), scanBackoffSteps, maxScanAttemptsCounter,
	)
	if err != nil {
		return nil, fmt.Errorf("claim due scans: %w", err)
	}
	defer rows.Close()

	claimed := make([]service.ScanJob, 0, batchSize)
	for rows.Next() {
		var (
			job             service.ScanJob
			destinationKind pgtype.Text
			destinationID   pgtype.Text
			envelopeVersion pgtype.Int2
			keyWrapVersion  pgtype.Int2
			attempts        pgtype.Int2
		)
		if err := rows.Scan(
			&job.AttachmentID, &job.WorkspaceID, &destinationKind, &destinationID,
			&job.Size, &job.StorageObjectKey, &envelopeVersion,
			&job.WrappedDEK, &job.KEKKeyID, &keyWrapVersion, &attempts,
		); err != nil {
			return nil, fmt.Errorf("scan claimed attachment: %w", err)
		}
		job.DestinationKind = domain.DestinationKind(destinationKind.String)
		job.DestinationID = destinationID.String
		job.EnvelopeVersion = int(envelopeVersion.Int16)
		job.KeyWrapVersion = int(keyWrapVersion.Int16)
		job.Attempts = int(attempts.Int16)
		claimed = append(claimed, job)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate claimed scans: %w", err)
	}
	return claimed, nil
}

// countPendingScansQuery is the queue depth: everything still awaiting a
// verdict, whether or not its next attempt is due yet.
//
// It counts the backlog rather than the due subset on purpose. A gauge of
// "rows due right now" reads zero whenever every attachment has just been
// claimed, which is exactly when the queue is busiest; what an operator needs to
// see is how many files are waiting, and whether that number is going down.
//
// It reads the same partial index the claim does.
const countPendingScansQuery = `
	SELECT count(*)
	  FROM files.attachments
	 WHERE status = 'pending_scan'
	   AND deleted_at IS NULL`

// CountPendingScans reports how many attachments are still awaiting a verdict.
func (s *PGXScanStore) CountPendingScans(ctx context.Context) (int64, error) {
	if s == nil || s.pool == nil {
		return 0, domain.ErrDependenciesUnavailable
	}
	var depth int64
	if err := s.pool.QueryRow(ctx, countPendingScansQuery).Scan(&depth); err != nil {
		return 0, fmt.Errorf("count pending scans: %w", err)
	}
	return depth, nil
}
