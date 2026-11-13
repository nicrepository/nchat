package storage_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/nicrepository/nchat/services/file-service/internal/storage"
)

type snapshotBudgetPool struct {
	*fakePool
	t *testing.T
}

func (p snapshotBudgetPool) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) > 2*time.Second {
		p.t.Fatal("snapshot has no bounded context")
	}
	return p.fakePool.QueryRow(ctx, sql, args...)
}
func TestQueueSnapshotBoundsReadAndDiscardsPartialResults(t *testing.T) {
	now := time.Now()
	values := []any{int64(9), int64(3), int64(2), int64(4), int64(5), int64(1), float64(12), now, true}
	pool := &fakePool{queryRow: func(sql string, _ ...any) pgx.Row {
		for _, part := range []string{"LIMIT 10001", "status='pending_scan' AND deleted_at IS NULL", "uploaded_at IS NULL OR uploaded_at>now()"} {
			if !strings.Contains(sql, part) {
				t.Errorf("missing bound/filter %s", part)
			}
		}
		if strings.Contains(sql, "ORDER BY") || strings.Contains(sql, "FOR UPDATE") || strings.Contains(sql, "COALESCE(uploaded_at,created_at)") {
			t.Fatal("observation locks or invents time")
		}
		return valueRow{values: values}
	}}
	s := storage.NewPGXScanStore(snapshotBudgetPool{pool, t})
	snapshot, err := s.ScanQueueSnapshot(context.Background())
	if err != nil || snapshot.Depth != 9 || snapshot.Eligible != 3 || snapshot.Leased != 2 || snapshot.Backoff != 4 || snapshot.OverFiveSeconds != 5 || snapshot.UnknownFinalization != 1 || snapshot.OldestAge != 12*time.Second || !snapshot.PolicyEnabled || !snapshot.ObservedAt.Equal(now) {
		t.Fatalf("snapshot=%+v err=%v", snapshot, err)
	}
	values[6] = float64(-1)
	if _, err := s.ScanQueueSnapshot(context.Background()); err == nil {
		t.Fatal("negative duration accepted")
	}
	values[6] = float64(12)
	values[0] = int64(10001)
	if partial, err := s.ScanQueueSnapshot(context.Background()); err == nil || partial.Depth != 0 {
		t.Fatal("partial count published as authoritative")
	}
	pool.queryRow = func(string, ...any) pgx.Row { return errRow{err: errors.New("database unavailable")} }
	if _, err := s.ScanQueueSnapshot(context.Background()); err == nil {
		t.Fatal("database failure hidden")
	}
	if _, err := storage.NewPGXScanStore(nil).ScanQueueSnapshot(context.Background()); err == nil {
		t.Fatal("nil pool hidden")
	}
}
func TestScanClaimDoesNotSubstituteCreationForFinalization(t *testing.T) {
	row := scanRow()
	row[len(row)-1] = pgtype.Timestamptz{}
	pool := &fakePool{query: func(sql string, _ ...any) (pgx.Rows, error) {
		if strings.Contains(sql, "COALESCE(a.uploaded_at,a.created_at)") {
			t.Fatal("invented finalization")
		}
		return &valueRows{rows: [][]any{row}}, nil
	}}
	jobs, err := storage.NewPGXScanStore(pool).ClaimDueScans(context.Background(), 1, time.Minute)
	if err != nil || len(jobs) != 1 || !jobs[0].QueuedAt.IsZero() {
		t.Fatalf("missing timestamp lost: %v", err)
	}
}
