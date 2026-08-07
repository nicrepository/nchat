package storage

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/nicrepository/nchat/services/file-service/internal/domain"
	"github.com/nicrepository/nchat/services/file-service/internal/service"
)

// PGXPreviewStore is the preview worker's half of files.attachments.
//
// It is a separate type from PGXAttachmentStore because the two answer to
// different callers with different authority: the attachment store serves
// requests on behalf of an authenticated session, while this one runs in the
// background and touches only the preview columns of rows the service itself
// finalised. Keeping them apart means the request path physically cannot reach
// the claim query, and the worker physically cannot read an attachment through
// a path that skipped authorization.
type PGXPreviewStore struct {
	pool Pool
}

func NewPGXPreviewStore(pool Pool) *PGXPreviewStore {
	return &PGXPreviewStore{pool: pool}
}

// maxPreviewAttemptsCounter caps the stored attempt counter.
//
// preview_attempts is a SMALLINT and claiming is not bounded by it, so a row
// that nothing can finalise — a database that refuses writes for weeks — would
// eventually overflow the column and make the claim statement itself fail,
// which would stall every other row behind it. Saturating the counter costs one
// LEAST() and keeps the queue moving; the number it saturates at is far beyond
// any budget the service actually reads.
const maxPreviewAttemptsCounter = 32000

// claimDuePreviewsQuery takes a batch of due rows and leases them in one
// statement.
//
// FOR UPDATE SKIP LOCKED is the same queue primitive auth's e-mail outbox uses
// (services/notification-service/internal/storage/outbox_store.go): concurrent
// workers step over each other's rows instead of blocking, so every replica can
// run this loop and no row is handed out twice.
//
// The lease is the whole concurrency control. Claiming pushes the next attempt
// into the future and increments the attempt count in the same UPDATE, so:
//
//   - a worker that is still rendering holds the row, because the lease is
//     derived from previewJobTimeout and outlives it by construction;
//   - a worker that died holds nothing: the lease expires and the row is due
//     again, with no 'processing' state to get stuck in and no janitor to run.
//
// There is deliberately no attempts ceiling in this predicate. The renderer's
// budget is enforced by the service, which stops decrypting past it; making the
// *claim* expire too would strand any row whose terminal state could not be
// written, since the state that says "give up" is itself a database write.
//
// # The scan gate
//
// a.status = 'clean' is a containment control, not a UI rule, and it is here —
// inside the atomic claim — rather than in a check before it, because anything
// checked separately can go stale between the check and the decryption.
//
// The worker decrypts the attachment and feeds it to a parser. Doing that to a
// file the antimalware scan has not approved would mean the one component that
// touches hostile bytes is also the one that ignores the verdict on them.
// 'clean' is the only status that has a verdict; pending_scan does not have one
// yet, rejected has the opposite one, and pending_upload and failed are not
// finished uploads at all. Where the deployment has no scanner and has
// explicitly declared itself development, uploads finalise as clean already
// (config.validateScanPolicy), so this predicate needs no environment switch
// and offers no bypass to one that has not made that declaration.
//
// deleted_at and the key columns complete the set: a removed attachment and one
// whose key was never sealed are both unopenable, and neither is a live file.
const claimDuePreviewsQuery = `
	WITH due AS (
		SELECT a.id
		FROM files.attachments AS a
		WHERE a.preview_status = 'pending'
		  AND a.status = 'clean'
		  AND a.deleted_at IS NULL
		  AND a.wrapped_dek IS NOT NULL
		  AND a.kek_key_id IS NOT NULL
		  AND (a.preview_next_attempt_at IS NULL OR a.preview_next_attempt_at <= now())
		ORDER BY a.preview_next_attempt_at NULLS FIRST, a.created_at
		LIMIT $1
		FOR UPDATE SKIP LOCKED
	)
	UPDATE files.attachments AS a
	   SET preview_attempts = LEAST(a.preview_attempts + 1, $3),
	       preview_next_attempt_at = now() + ($2 * interval '1 second'),
	       updated_at = now()
	  FROM due
	 WHERE a.id = due.id
	RETURNING a.id::text, a.workspace_id::text, COALESCE(a.detected_mime, ''),
	          a.size_bytes, a.storage_object_key, a.envelope_version,
	          a.wrapped_dek, COALESCE(a.kek_key_id, ''), a.dek_wrap_version,
	          a.preview_attempts`

// ClaimDuePreviews leases up to batchSize attachments awaiting a preview.
func (s *PGXPreviewStore) ClaimDuePreviews(
	ctx context.Context, batchSize int, lease time.Duration,
) ([]service.PreviewJob, error) {
	if s == nil || s.pool == nil {
		return nil, domain.ErrDependenciesUnavailable
	}
	if batchSize <= 0 || lease <= 0 {
		// A zero lease would hand the same row to every worker at once, and a
		// zero batch would spin. Both are wiring mistakes, refused rather than
		// defaulted.
		return nil, fmt.Errorf("%w: invalid preview claim parameters", domain.ErrInvalidInput)
	}

	rows, err := s.pool.Query(ctx, claimDuePreviewsQuery,
		batchSize, int64(lease.Seconds()), maxPreviewAttemptsCounter,
	)
	if err != nil {
		return nil, fmt.Errorf("claim due previews: %w", err)
	}
	defer rows.Close()

	claimed := make([]service.PreviewJob, 0, batchSize)
	for rows.Next() {
		var (
			job             service.PreviewJob
			envelopeVersion pgtype.Int2
			keyWrapVersion  pgtype.Int2
			attempts        pgtype.Int2
		)
		if err := rows.Scan(
			&job.AttachmentID, &job.WorkspaceID, &job.DetectedMIME,
			&job.Size, &job.StorageObjectKey, &envelopeVersion,
			&job.WrappedDEK, &job.KEKKeyID, &keyWrapVersion, &attempts,
		); err != nil {
			return nil, fmt.Errorf("scan claimed preview: %w", err)
		}
		job.EnvelopeVersion = int(envelopeVersion.Int16)
		job.KeyWrapVersion = int(keyWrapVersion.Int16)
		job.Attempts = int(attempts.Int16)
		claimed = append(claimed, job)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate claimed previews: %w", err)
	}
	return claimed, nil
}

// revalidateClaimQuery re-reads, inside the fence, everything the claim
// asserted when it took the row.
//
// It is not a substitute for the fence and it is not redundant with it: the
// fence guarantees that no invalidation *runs* while this is held, and this
// query answers whether one already ran before the fence was taken. Together
// they leave no ordering in which a rejected, removed or superseded attachment
// reaches a decoder — the check cannot go stale, because nothing can change the
// row until the fence is given back.
const revalidateClaimQuery = `
	SELECT EXISTS (
		SELECT 1
		FROM files.attachments
		WHERE id = $1
		  AND status = 'clean'
		  AND deleted_at IS NULL
		  AND preview_status = 'pending'
		  AND preview_attempts = $2
	)`

// RevalidateClaim reports whether this claim may still open the attachment.
//
// False means one of four things happened between the claim and the fence: the
// scan condemned it, it was removed, another attempt superseded this one, or
// the preview already reached a terminal state. All four mean the same to the
// caller — stop, and do not decrypt.
func (s *PGXPreviewStore) RevalidateClaim(
	ctx context.Context, attachmentID string, claimAttempt int,
) (bool, error) {
	if s == nil || s.pool == nil {
		return false, domain.ErrDependenciesUnavailable
	}
	if attachmentID == "" || claimAttempt <= 0 {
		return false, fmt.Errorf("%w: revalidation needs a claim", domain.ErrInvalidInput)
	}
	var valid bool
	if err := s.pool.QueryRow(ctx, revalidateClaimQuery, attachmentID, claimAttempt).Scan(&valid); err != nil {
		return false, fmt.Errorf("revalidate preview claim: %w", err)
	}
	return valid, nil
}

// MarkPreviewReady records a produced preview against its attachment.
//
// The whole binding lands in one statement, for the same reason MarkUploaded
// writes the attachment's: the wrapped key authenticates the length and the
// identity, so a row that carried some of them would be a preview that cannot
// be opened. WHERE preview_status = 'pending' is the idempotency fence — a
// second attempt that arrives late affects no row and is told so, instead of
// overwriting a preview a client may already be reading.
//
// # The fence
//
// preview_attempts is this claim's fencing token, and requiring it is what a
// lease alone cannot do. A lease says "your time is up"; it does not stop a
// worker that is still running from writing. An attempt whose lease expired
// while it was rendering would otherwise publish over — or conclude — the newer
// attempt that took the row from it, discarding a preview that actually
// succeeded. Requiring the token means the older attempt updates no row and is
// told it has lost ownership.
//
// The scan gate is repeated here, and it is not redundant with the claim's.
// Rendering takes time, and a scan verdict can land inside that window: an
// attachment that was clean when it was claimed may be rejected by the time its
// preview is ready. Re-asserting status = 'clean' in the publishing statement
// is what makes that window closed rather than merely narrow — the row is not
// updated, the caller is told it was not recorded, and the object it produced
// is discarded instead of becoming a thumbnail of a file the scanner has since
// condemned. deleted_at is re-asserted for the same reason.
func (s *PGXPreviewStore) MarkPreviewReady(
	ctx context.Context, result service.PreviewResult,
) (bool, error) {
	if s == nil || s.pool == nil {
		return false, domain.ErrDependenciesUnavailable
	}
	if result.PreviewObjectID == "" || result.Size <= 0 ||
		len(result.WrappedDEK) == 0 || result.KEKKeyID == "" {
		return false, fmt.Errorf(
			"%w: preview cannot be recorded without its key binding", domain.ErrInvalidInput)
	}
	if result.ClaimAttempt <= 0 {
		return false, fmt.Errorf("%w: a preview needs its claim", domain.ErrInvalidInput)
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE files.attachments
		   SET preview_status = 'ready',
		       preview_object_id = $2,
		       preview_size_bytes = $3,
		       preview_wrapped_dek = $4,
		       preview_kek_key_id = $5,
		       preview_envelope_version = $6,
		       preview_dek_wrap_version = $7,
		       preview_next_attempt_at = NULL,
		       updated_at = now()
		 WHERE id = $1
		   AND preview_status = 'pending'
		   AND status = 'clean'
		   AND deleted_at IS NULL
		   AND preview_attempts = $8`,
		result.AttachmentID, result.PreviewObjectID, result.Size,
		result.WrappedDEK, result.KEKKeyID,
		result.EnvelopeVersion, result.KeyWrapVersion,
		result.ClaimAttempt,
	)
	if err != nil {
		return false, fmt.Errorf("mark preview ready: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// MarkPreviewTerminal records an outcome that produced no object.
//
// Only the two terminal states with no material are accepted here: 'ready' has
// its own statement because it must write a binding, and 'pending' is not an
// outcome — a retry is expressed by writing nothing at all and letting the
// lease expire.
func (s *PGXPreviewStore) MarkPreviewTerminal(
	ctx context.Context, attachmentID string, claimAttempt int, status domain.PreviewStatus,
) (bool, error) {
	if s == nil || s.pool == nil {
		return false, domain.ErrDependenciesUnavailable
	}
	if status != domain.PreviewStatusUnsupported && status != domain.PreviewStatusFailed {
		return false, fmt.Errorf("%w: invalid terminal preview status", domain.ErrInvalidInput)
	}
	if claimAttempt <= 0 {
		// A conclusion with no claim behind it is a wiring mistake, and the
		// fence is what stops it from ending somebody else's job.
		return false, fmt.Errorf("%w: a terminal state needs its claim", domain.ErrInvalidInput)
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE files.attachments
		   SET preview_status = $2,
		       preview_next_attempt_at = NULL,
		       updated_at = now()
		 WHERE id = $1
		   AND preview_status = 'pending'
		   AND preview_attempts = $3`,
		attachmentID, string(status), claimAttempt,
	)
	if err != nil {
		return false, fmt.Errorf("mark preview terminal: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}
