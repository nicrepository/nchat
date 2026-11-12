package storage_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/nicrepository/nchat/services/file-service/internal/domain"
	"github.com/nicrepository/nchat/services/file-service/internal/service"
	"github.com/nicrepository/nchat/services/file-service/internal/storage"
)

func TestScanAttemptForwardsAuthorityAndOutcomes(t *testing.T) {
	for _, verdict := range []string{"clean", "rejected"} {
		pool := &fakePool{queryRow: func(sql string, args ...any) pgx.Row {
			if len(args) != 11 || args[2] != "claim-token" || args[3] != verdict {
				t.Fatalf("lost token or verdict: %v", args)
			}
			return valueRow{values: append(lifecycleRow(domain.Status(verdict), domain.PreviewStatusUnsupported), "applied")}
		}}
		s := mustFencedStore(pool)
		approval := approvalInput()
		approval.ClaimToken = "claim-token"
		var err error
		if verdict == "clean" {
			_, err = s.MarkScanClean(context.Background(), approval)
		} else {
			_, err = s.MarkScanRejected(context.Background(), service.ScanRejection{AttachmentID: approval.AttachmentID, WorkspaceID: approval.WorkspaceID, ClaimToken: approval.ClaimToken})
		}
		if err != nil {
			t.Fatal(err)
		}
	}
}
func TestScanAttemptClassifiesDatabaseFencingErrors(t *testing.T) {
	for code, want := range map[string]error{"NC005": service.ErrScanSuperseded, "NC004": service.ErrScanFencingInactive} {
		pool := &fakePool{queryRow: func(string, ...any) pgx.Row { return errRow{err: &pgconn.PgError{Code: code}} }}
		a := approvalInput()
		a.ClaimToken = "claim-token"
		if _, err := storage.NewPGXAttachmentStore(pool).MarkScanClean(context.Background(), a); !errors.Is(err, want) {
			t.Fatalf("%s = %v", code, err)
		}
	}
}
func TestScanRetryAndStatisticsHandleErrors(t *testing.T) {
	q := storage.NewPGXScanStore(nil)
	if q.RetryScan(context.Background(), service.ScanJob{}, time.Second, "timeout") == nil {
		t.Fatal("nil pool accepted")
	}
	if _, _, _, err := q.ScanQueueState(context.Background()); err == nil {
		t.Fatal("nil statistics pool accepted")
	}
	pool := &fakePool{queryRow: func(string, ...any) pgx.Row { return valueRow{values: []any{false}} }}
	if err := storage.NewPGXScanStore(pool).RetryScan(context.Background(), service.ScanJob{}, 10*time.Second, "timeout"); !errors.Is(err, service.ErrScanSuperseded) {
		t.Fatal(err)
	}
	pool.queryRow = func(string, ...any) pgx.Row { return valueRow{values: []any{int64(9), int64(3), int64(2)}} }
	total, due, leased, err := storage.NewPGXScanStore(pool).ScanQueueState(context.Background())
	if err != nil || total != 9 || due != 3 || leased != 2 {
		t.Fatalf("statistics=%d,%d,%d err=%v", total, due, leased, err)
	}
}
