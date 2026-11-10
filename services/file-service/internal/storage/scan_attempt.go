package storage

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/nicrepository/nchat/services/file-service/internal/domain"
	"github.com/nicrepository/nchat/services/file-service/internal/filepolicy"
	"github.com/nicrepository/nchat/services/file-service/internal/service"
)

const persistScanAttemptQuery = `SELECT result_status,result_preview,result_outcome
 FROM files.persist_scan_v2($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`

func (s *PGXAttachmentStore) persistScanAttempt(ctx context.Context, a service.ScanApproval, verdict string) (service.AttachmentLifecycleState, error) {
	if s == nil || s.pool == nil {
		return service.AttachmentLifecycleState{}, domain.ErrDependenciesUnavailable
	}
	if a.AttachmentID == "" || a.WorkspaceID == "" {
		return service.AttachmentLifecycleState{}, domain.ErrInvalidInput
	}
	run := func(q TransactionalQuerier) (service.AttachmentLifecycleState, error) {
		return writeScanAttempt(ctx, q, a, verdict)
	}
	if verdict != "rejected" {
		return run(s.pool)
	}
	if s.fence == nil {
		return service.AttachmentLifecycleState{}, domain.ErrDependenciesUnavailable
	}
	key, err := attachmentFenceKey(a.AttachmentID)
	if err != nil {
		return service.AttachmentLifecycleState{}, err
	}
	return s.fence.WithinTransaction(ctx, key, run)
}

func writeScanAttempt(ctx context.Context, q TransactionalQuerier, a service.ScanApproval, verdict string) (service.AttachmentLifecycleState, error) {
	args := []any{a.AttachmentID, a.WorkspaceID, a.ClaimToken, verdict, filepolicy.Version, nil, nil, nil, nil, nil, nil}
	if e := a.Evidence; e != nil {
		args[5] = e.Generation
		args[6] = e.Engine
		args[7] = e.DailyVersion
		args[8] = e.DatabaseTime
		args[9] = e.LoadedAt
		args[10] = e.CompletedAt
	}
	var status, preview pgtype.Text
	var outcome string
	if err := q.QueryRow(ctx, persistScanAttemptQuery, args...).Scan(&status, &preview, &outcome); err != nil {
		return service.AttachmentLifecycleState{}, signatureWriteError(err)
	}
	if err := signatureOutcome(outcome); err != nil {
		return service.AttachmentLifecycleState{}, err
	}
	return service.AttachmentLifecycleState{Status: domain.Status(status.String), PreviewStatus: previewStatusOf(preview), ScanStatusUnchanged: outcome == "rescanned"}, nil
}

func (s *PGXScanStore) RetryScan(ctx context.Context, job service.ScanJob, delay time.Duration, kind string) error {
	if s == nil || s.pool == nil {
		return domain.ErrDependenciesUnavailable
	}
	var changed bool
	err := s.pool.QueryRow(ctx, `SELECT files.retry_scan($1,$2,$3,$4,$5)`, job.AttachmentID, job.WorkspaceID, job.ClaimToken, delay.Seconds(), kind).Scan(&changed)
	if err != nil {
		return signatureWriteError(err)
	}
	if !changed {
		return service.ErrScanSuperseded
	}
	return nil
}

func (s *PGXScanStore) ScanQueueState(ctx context.Context) (int64, int64, int64, error) {
	if s == nil || s.pool == nil {
		return 0, 0, 0, domain.ErrDependenciesUnavailable
	}
	var depth, due, leased int64
	err := s.pool.QueryRow(ctx, `SELECT count(*),count(*) FILTER(WHERE wrapped_dek IS NOT NULL AND kek_key_id IS NOT NULL
 AND (scan_next_attempt_at IS NULL OR scan_next_attempt_at<=now()) AND (scan_lease_until IS NULL OR scan_lease_until<=now())),
 count(*) FILTER(WHERE scan_lease_until>now()) FROM files.attachments WHERE status='pending_scan' AND deleted_at IS NULL`).Scan(&depth, &due, &leased)
	return depth, due, leased, err
}
