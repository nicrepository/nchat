package storage

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/nicrepository/nchat/services/file-service/internal/domain"
	"github.com/nicrepository/nchat/services/file-service/internal/filepolicy"
	"github.com/nicrepository/nchat/services/file-service/internal/service"
)

// Lock the target before classifying its state; all outcomes share one statement
// and the database policy lock. No policy cache or second per-scan query.
const markProvenScanCleanQuery = `SELECT result_status,result_preview,result_outcome
 FROM files.persist_proven_scan($1,$2,$3,$4,$5,$6,$7,$8,$9)`

func markProvenClean(ctx context.Context, q TransactionalQuerier, a service.ScanApproval) (service.AttachmentLifecycleState, error) {
	e := a.Evidence
	var status, preview pgtype.Text
	var outcome string
	err := q.QueryRow(ctx, markProvenScanCleanQuery, a.AttachmentID, a.WorkspaceID,
		e.Generation, e.Engine, e.DailyVersion, e.DatabaseTime, e.LoadedAt, e.CompletedAt, filepolicy.Version).Scan(&status, &preview, &outcome)
	if err != nil {
		return service.AttachmentLifecycleState{}, signatureWriteError(err)
	}
	if err = signatureOutcome(outcome); err != nil {
		return service.AttachmentLifecycleState{}, err
	}
	state := service.AttachmentLifecycleState{Status: domain.Status(status.String), PreviewStatus: previewStatusOf(preview)}
	state.ScanStatusUnchanged = outcome == "rescanned"
	if !state.Status.Valid() {
		return service.AttachmentLifecycleState{}, service.ErrScanConflict
	}
	return state, nil
}

func signatureOutcome(outcome string) error {
	switch outcome {
	case "applied", "rescanned":
		return nil
	case "missing":
		return domain.ErrNotFound
	case "superseded":
		return service.ErrScanSuperseded
	case "already_recorded":
		return service.ErrScanAlreadyRecorded
	default:
		return service.ErrScanConflict
	}
}

func signatureWriteError(err error) error {
	var pgerr *pgconn.PgError
	if errors.As(err, &pgerr) {
		switch pgerr.Code {
		case "NC001":
			return fmt.Errorf("%w: %w", service.ErrSignaturePolicyInactive, err)
		case "NC002":
			return fmt.Errorf("%w: %w", service.ErrSignaturePolicyInvalid, err)
		case "NC005":
			return fmt.Errorf("%w: %w", service.ErrScanSuperseded, err)
		case "NC004":
			return fmt.Errorf("%w: %w", service.ErrScanFencingInactive, err)
		case "NC003", "40001", "40P01":
			return fmt.Errorf("%w: %w", service.ErrScanConflict, err)
		}
	}
	return fmt.Errorf("persist signature approval: %w", err)
}
