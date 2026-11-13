package storage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
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

const claimDueScansQuery = `
 SELECT a.id::text,a.workspace_id::text,a.destination_kind,
 COALESCE(a.channel_id::text,a.conversation_id::text),a.size_bytes,a.storage_object_key,
 a.envelope_version,a.wrapped_dek,COALESCE(a.kek_key_id,''),a.dek_wrap_version,
 a.scan_attempts,a.original_filename,a.declared_mime,a.format_policy_version,
 a.scan_claim_token::text,a.uploaded_at
 FROM files.claim_scans($1,$2) a`

// ClaimDueScans leases up to batchSize attachments awaiting a verdict.
func (s *PGXScanStore) ClaimDueScans(
	ctx context.Context, batchSize int, lease time.Duration,
) ([]service.ScanJob, error) {
	if s == nil || s.pool == nil {
		return nil, domain.ErrDependenciesUnavailable
	}
	if batchSize != 1 || lease < time.Second || lease > 3630*time.Second {
		// A zero lease would hand the same row to every worker at once, and a
		// zero batch would spin. Both are wiring mistakes, refused not defaulted.
		return nil, fmt.Errorf("%w: invalid scan claim parameters", domain.ErrInvalidInput)
	}

	rows, err := s.pool.Query(ctx, claimDueScansQuery,
		batchSize, lease.Seconds(),
	)
	if err != nil {
		return nil, scanClaimError(err)
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
			finalized       pgtype.Timestamptz
		)
		if err := rows.Scan(
			&job.AttachmentID, &job.WorkspaceID, &destinationKind, &destinationID,
			&job.Size, &job.StorageObjectKey, &envelopeVersion,
			&job.WrappedDEK, &job.KEKKeyID, &keyWrapVersion, &attempts,
			&job.Filename, &job.DeclaredMIME, &job.PolicyVersion,
			&job.ClaimToken, &finalized,
		); err != nil {
			return nil, fmt.Errorf("scan claimed attachment: %w", err)
		}
		job.DestinationKind = domain.DestinationKind(destinationKind.String)
		job.DestinationID = destinationID.String
		job.EnvelopeVersion = int(envelopeVersion.Int16)
		job.KeyWrapVersion = int(keyWrapVersion.Int16)
		job.Attempts = int(attempts.Int16)
		if finalized.Valid {
			job.QueuedAt = finalized.Time
		}
		claimed = append(claimed, job)
	}
	if err := rows.Err(); err != nil {
		return nil, scanClaimError(err)
	}
	return claimed, nil
}

func scanClaimError(err error) error {
	var pgerr *pgconn.PgError
	if errors.As(err, &pgerr) && pgerr.Code == "NC006" {
		return fmt.Errorf("%w: %w", service.ErrScanQueueStandby, err)
	}
	return fmt.Errorf("claim due scans: %w", err)
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
