package storage_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/nicrepository/nchat/services/file-service/internal/domain"
	"github.com/nicrepository/nchat/services/file-service/internal/service"
	"github.com/nicrepository/nchat/services/file-service/internal/signatures"
	"github.com/nicrepository/nchat/services/file-service/internal/storage"
)

func TestProvenApprovalOutcomesAreDistinct(t *testing.T) {
	for outcome, want := range map[string]error{"missing": domain.ErrNotFound, "superseded": service.ErrScanSuperseded, "already_recorded": service.ErrScanAlreadyRecorded, "conflict": service.ErrScanConflict, "unexpected": service.ErrScanConflict} {
		t.Run(outcome, func(t *testing.T) {
			calls := 0
			pool := &fakePool{queryRow: func(string, ...any) pgx.Row {
				calls++
				return valueRow{values: append(lifecycleRow(domain.StatusClean, domain.PreviewStatusPending), outcome)}
			}}
			approval := approvalInput()
			approval.Evidence = &signatures.Evidence{}
			_, err := storage.NewPGXAttachmentStore(pool).MarkScanClean(context.Background(), approval)
			if !errors.Is(err, want) || calls != 1 {
				t.Fatalf("outcome %s error=%v calls=%d", outcome, err, calls)
			}
		})
	}
}

func TestProvenApprovalPreservesSQLStateCategories(t *testing.T) {
	for code, want := range map[string]error{"NC001": service.ErrSignaturePolicyInactive, "NC002": service.ErrSignaturePolicyInvalid, "NC003": service.ErrScanConflict, "40001": service.ErrScanConflict, "40P01": service.ErrScanConflict} {
		t.Run(code, func(t *testing.T) {
			cause := &pgconn.PgError{Code: code}
			pool := &fakePool{queryRow: func(string, ...any) pgx.Row { return errRow{err: cause} }}
			a := approvalInput()
			a.Evidence = &signatures.Evidence{}
			_, err := storage.NewPGXAttachmentStore(pool).MarkScanClean(context.Background(), a)
			if !errors.Is(err, want) || !errors.Is(err, cause) || errors.Is(err, domain.ErrNotFound) {
				t.Fatal("classification lost", err)
			}
		})
	}
}

func TestProvenRescanReportsNoStatusTransition(t *testing.T) {
	pool := &fakePool{queryRow: func(string, ...any) pgx.Row {
		return valueRow{values: append(lifecycleRow(domain.StatusClean, domain.PreviewStatusPending), "rescanned")}
	}}
	a := approvalInput()
	a.Evidence = &signatures.Evidence{}
	state, err := storage.NewPGXAttachmentStore(pool).MarkScanClean(context.Background(), a)
	if err != nil || !state.ScanStatusUnchanged {
		t.Fatal("rescan lost transition classification", state, err)
	}
}
