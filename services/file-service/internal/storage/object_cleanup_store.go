package storage

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/nicrepository/nchat/services/file-service/internal/domain"
	"github.com/nicrepository/nchat/services/file-service/internal/service"
)

// PGXObjectCleanupStore is the durable queue of stored objects that must be
// removed (SR-002).
//
// It exists because the alternative was a log line. A preview is uploaded
// before the row that points at it can be written, so a refused publication
// leaves an object nothing references; when the delete that should follow fails,
// the key has to outlive the process that knew it. A counter and a warning do
// not do that.
type PGXObjectCleanupStore struct {
	pool Pool
}

func NewPGXObjectCleanupStore(pool Pool) *PGXObjectCleanupStore {
	return &PGXObjectCleanupStore{pool: pool}
}

// previewObjectKeyExpr derives a preview's storage key from its row, in SQL.
//
// It is a single named fragment because three statements need it — the
// reference check and the two invalidations that enqueue a key — and because it
// has to agree exactly with domain.PreviewObjectKey. A copy that drifted would
// not fail loudly: it would silently enqueue keys that match no object and
// classify live previews as unreferenced. TestPreviewObjectKeyExprMatchesDomain
// asserts the two agree against the real database.
const previewObjectKeyExpr = `'nchat/previews/' || preview_object_id::text`

// Enqueue records that an object must be removed.
//
// ON CONFLICT DO NOTHING is what makes it idempotent, and idempotence is what
// makes it safe to call from a failure path: the same delete can fail on every
// retry and every restart, and the queue still holds exactly one job. It
// deliberately does not reset the schedule of a job already waiting — a repeat
// is not new information, and letting it push the next attempt around would let
// a fast-failing caller starve the backoff.
func (s *PGXObjectCleanupStore) Enqueue(ctx context.Context, objectKey string) error {
	if s == nil || s.pool == nil {
		return domain.ErrDependenciesUnavailable
	}
	if objectKey == "" {
		return fmt.Errorf("%w: a cleanup job needs an object key", domain.ErrInvalidInput)
	}
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO files.object_cleanup_jobs (object_key)
		VALUES ($1)
		ON CONFLICT (object_key) DO NOTHING`, objectKey,
	); err != nil {
		return fmt.Errorf("enqueue object cleanup: %w", err)
	}
	return nil
}

const expireDuePreviewsQuery = `
	WITH due AS (
		SELECT id, preview_object_id
		FROM files.attachments
		WHERE preview_lifecycle_status = 'available'
		  AND preview_expires_at <= now()
		  AND deleted_at IS NULL
		ORDER BY preview_expires_at
		LIMIT $1
		FOR UPDATE SKIP LOCKED
	), objects AS (
		SELECT 'nchat/previews/' || preview_object_id::text AS object_key
		FROM due WHERE preview_object_id IS NOT NULL
		UNION
		SELECT 'nchat/previews/' || pages.object_id::text
		FROM files.attachment_preview_pages AS pages
		JOIN due ON due.id = pages.attachment_id
	), queued AS (
		INSERT INTO files.object_cleanup_jobs (object_key)
		SELECT object_key FROM objects
		ON CONFLICT (object_key) DO NOTHING
	), cleared AS (
		DELETE FROM files.attachment_preview_pages
		WHERE attachment_id IN (SELECT id FROM due)
	)
	UPDATE files.attachments
	SET preview_lifecycle_status = 'expired',
	    preview_failure_reason = 'expired',
	    preview_object_id = NULL,
	    preview_size_bytes = NULL,
	    preview_wrapped_dek = NULL,
	    preview_kek_key_id = NULL,
	    preview_envelope_version = NULL,
	    preview_dek_wrap_version = NULL,
	    preview_page_count = 1,
	    preview_content_type = 'image/jpeg',
	    preview_expires_at = NULL,
	    updated_at = now()
	WHERE id IN (SELECT id FROM due)`

func (s *PGXObjectCleanupStore) ExpireDuePreviews(ctx context.Context, limit int) (int, error) {
	if s == nil || s.pool == nil {
		return 0, domain.ErrDependenciesUnavailable
	}
	if limit <= 0 {
		return 0, fmt.Errorf("%w: expiry limit must be positive", domain.ErrInvalidInput)
	}
	tag, err := s.pool.Exec(ctx, expireDuePreviewsQuery, limit)
	if err != nil {
		return 0, fmt.Errorf("expire document previews: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// claimDueCleanupsQuery leases due jobs, exactly as the preview claim does.
//
// The attempt count is the fencing token here too: completing a job requires
// it, so a worker whose lease expired cannot delete the queue entry a newer
// attempt is working on. Saturating the counter keeps a permanently failing job
// from overflowing its column.
const claimDueCleanupsQuery = `
	WITH due AS (
		SELECT id
		FROM files.object_cleanup_jobs
		WHERE next_attempt_at <= now()
		ORDER BY next_attempt_at
		LIMIT $1
		FOR UPDATE SKIP LOCKED
	)
	UPDATE files.object_cleanup_jobs AS j
	   SET attempts = LEAST(j.attempts + 1, $3),
	       next_attempt_at = now() + ($2 * interval '1 second'),
	       updated_at = now()
	  FROM due
	 WHERE j.id = due.id
	RETURNING j.id::text, j.object_key, j.attempts`

// ClaimDueCleanups leases up to batchSize outstanding cleanups.
func (s *PGXObjectCleanupStore) ClaimDueCleanups(
	ctx context.Context, batchSize int, lease time.Duration,
) ([]service.ObjectCleanupJob, error) {
	if s == nil || s.pool == nil {
		return nil, domain.ErrDependenciesUnavailable
	}
	if batchSize <= 0 || lease <= 0 {
		return nil, fmt.Errorf("%w: invalid cleanup claim parameters", domain.ErrInvalidInput)
	}
	rows, err := s.pool.Query(ctx, claimDueCleanupsQuery,
		batchSize, int64(lease.Seconds()), maxPreviewAttemptsCounter,
	)
	if err != nil {
		return nil, fmt.Errorf("claim due cleanups: %w", err)
	}
	defer rows.Close()

	claimed := make([]service.ObjectCleanupJob, 0, batchSize)
	for rows.Next() {
		var (
			job      service.ObjectCleanupJob
			attempts pgtype.Int2
		)
		if err := rows.Scan(&job.ID, &job.ObjectKey, &attempts); err != nil {
			return nil, fmt.Errorf("scan claimed cleanup: %w", err)
		}
		job.Attempts = int(attempts.Int16)
		claimed = append(claimed, job)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate claimed cleanups: %w", err)
	}
	return claimed, nil
}

// Complete removes a finished job.
//
// The attempt count fences it: a worker that lost its lease cannot delete the
// entry a newer attempt is still working on, so a job is only ever forgotten by
// the attempt that actually finished it. It reports whether the row was still
// the caller's.
func (s *PGXObjectCleanupStore) Complete(
	ctx context.Context, jobID string, claimAttempt int,
) (bool, error) {
	if s == nil || s.pool == nil {
		return false, domain.ErrDependenciesUnavailable
	}
	if jobID == "" || claimAttempt <= 0 {
		return false, fmt.Errorf("%w: completing a cleanup needs its claim", domain.ErrInvalidInput)
	}
	tag, err := s.pool.Exec(ctx, `
		DELETE FROM files.object_cleanup_jobs
		 WHERE id = $1
		   AND attempts = $2`, jobID, claimAttempt,
	)
	if err != nil {
		return false, fmt.Errorf("complete object cleanup: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// IsObjectReferenced reports whether a *servable* preview still points at this
// key.
//
// The worker asks before deleting, and the answer is what stops a stale job
// from removing an object that has since become a live preview: a key is
// derived from a preview's own random id, so a referenced one belongs to a row
// that was published after the job was enqueued.
//
// # Why "servable" and not merely "pointed at"
//
// preview_status = 'ready' alone is not a reference, and treating it as one is
// what let preview objects accumulate forever. A ready preview under a rejected
// or removed attachment is unreachable by construction — every read path gates
// on the attachment's own status and visibility, so no request can ever be
// answered with those bytes — but the row still pointed at the key, so the
// cleanup worker classified the job as "referenced" and discarded it without
// deleting anything. The object then had no owner and no job: a permanent leak.
//
// So the predicate is the delivery gate, expressed once: an object is
// referenced exactly while something could still be served from it. The two
// transitions that end that — rejection and removal — enqueue the key in the
// same statement that ends it (see markScanRejectedQuery and
// markAttachmentDeletedQuery), so the job and the state change become visible
// together and there is no window in which one is committed without the other.
const isObjectReferencedQuery = `
	SELECT EXISTS (
		SELECT 1 FROM files.attachments
		WHERE preview_object_id IS NOT NULL
		  AND preview_status = 'ready'
		  AND status = 'clean'
		  AND deleted_at IS NULL
		  AND ` + previewObjectKeyExpr + ` = $1
	)`

func (s *PGXObjectCleanupStore) IsObjectReferenced(
	ctx context.Context, objectKey string,
) (bool, error) {
	if s == nil || s.pool == nil {
		return false, domain.ErrDependenciesUnavailable
	}
	var referenced bool
	if err := s.pool.QueryRow(ctx, isObjectReferencedQuery, objectKey).Scan(&referenced); err != nil {
		return false, fmt.Errorf("check object reference: %w", err)
	}
	return referenced, nil
}
