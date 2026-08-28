package storage_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/nicrepository/nchat/services/file-service/internal/domain"
	"github.com/nicrepository/nchat/services/file-service/internal/storage"
)

const (
	testScanAttachmentID = "88888888-8888-4888-8888-888888888888"
	testScanWorkspaceID  = "99999999-9999-4999-8999-999999999999"
	testScanChannelID    = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
)

func scanRow() []any {
	return []any{
		testScanAttachmentID, testScanWorkspaceID, pgtype.Text{String: "channel", Valid: true},
		pgtype.Text{String: testScanChannelID, Valid: true},
		int64(4096), "nchat/attachments/" + testScanAttachmentID,
		pgtype.Int2{Int16: 1, Valid: true},
		[]byte("wrapped"), "kek-active",
		pgtype.Int2{Int16: 2, Valid: true},
		pgtype.Int2{Int16: 1, Valid: true},
	}
}

// The claim is the whole concurrency control, so its shape is asserted rather
// than assumed: without SKIP LOCKED two replicas block on each other, and
// without the lease they both take the same row.
func TestClaimDueScansLeasesRowsWithSkipLocked(t *testing.T) {
	pool := &fakePool{query: func(string, ...any) (pgx.Rows, error) {
		return &valueRows{rows: [][]any{scanRow()}}, nil
	}}

	jobs, err := storage.NewPGXScanStore(pool).ClaimDueScans(context.Background(), 1, time.Minute)
	if err != nil {
		t.Fatalf("ClaimDueScans: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("claimed %d jobs, want 1", len(jobs))
	}
	if jobs[0].AttachmentID != testScanAttachmentID || jobs[0].WorkspaceID != testScanWorkspaceID {
		t.Fatalf("unexpected job: %+v", jobs[0])
	}
	// The destination is what a status change is later addressed to; a claim
	// that dropped it would leave the verdict unannounceable.
	if jobs[0].DestinationKind != domain.DestinationKindChannel ||
		jobs[0].DestinationID != testScanChannelID {
		t.Fatalf("job lost its destination: %+v", jobs[0])
	}
	if jobs[0].Attempts != 1 {
		t.Fatalf("attempts = %d, want the post-claim count", jobs[0].Attempts)
	}

	for _, fragment := range []string{
		"FOR UPDATE SKIP LOCKED",
		"scan_next_attempt_at",
		"scan_attempts",
	} {
		if !strings.Contains(pool.lastSQL, fragment) {
			t.Fatalf("claim is missing %q:\n%s", fragment, pool.lastSQL)
		}
	}
}

// The queue is the status column. A claim that selected on anything else could
// hand the worker a row whose state machine says it has already been decided.
func TestClaimDueScansOnlySelectsUndecidedLiveAttachments(t *testing.T) {
	pool := &fakePool{query: func(string, ...any) (pgx.Rows, error) {
		return &valueRows{}, nil
	}}
	if _, err := storage.NewPGXScanStore(pool).ClaimDueScans(
		context.Background(), 1, time.Minute); err != nil {
		t.Fatalf("ClaimDueScans: %v", err)
	}

	for _, fragment := range []string{
		"a.status = 'pending_scan'",
		"a.deleted_at IS NULL",
		"a.wrapped_dek IS NOT NULL",
		"a.kek_key_id IS NOT NULL",
	} {
		if !strings.Contains(pool.lastSQL, fragment) {
			t.Fatalf("claim is missing %q:\n%s", fragment, pool.lastSQL)
		}
	}
	// Nothing may make an attachment downloadable from here. The verdicts live
	// on PGXAttachmentStore and there must not be a second way to write one.
	if strings.Contains(pool.lastSQL, "'clean'") || strings.Contains(pool.lastSQL, "'rejected'") {
		t.Fatalf("the claim writes a verdict:\n%s", pool.lastSQL)
	}
}

// A NULL schedule counts as due. That is what makes the migration's backfill
// fail-safe: a row with no schedule is scanned, never skipped.
func TestClaimDueScansTreatsAnUnscheduledRowAsDue(t *testing.T) {
	pool := &fakePool{query: func(string, ...any) (pgx.Rows, error) {
		return &valueRows{}, nil
	}}
	if _, err := storage.NewPGXScanStore(pool).ClaimDueScans(
		context.Background(), 1, time.Minute); err != nil {
		t.Fatalf("ClaimDueScans: %v", err)
	}
	if !strings.Contains(pool.lastSQL, "a.scan_next_attempt_at IS NULL OR a.scan_next_attempt_at <= now()") {
		t.Fatalf("an unscheduled row is not due:\n%s", pool.lastSQL)
	}
}

// The retry interval has to grow with the attempt count and stop growing, or a
// daemon outage is either a storm or an unbounded wait.
func TestClaimDueScansBacksOffWithABoundedMultiplier(t *testing.T) {
	pool := &fakePool{query: func(string, ...any) (pgx.Rows, error) {
		return &valueRows{}, nil
	}}
	if _, err := storage.NewPGXScanStore(pool).ClaimDueScans(
		context.Background(), 1, 90*time.Second); err != nil {
		t.Fatalf("ClaimDueScans: %v", err)
	}
	if !strings.Contains(pool.lastSQL, "LEAST(a.scan_attempts + 1, $3)") {
		t.Fatalf("the backoff multiplier is not bounded:\n%s", pool.lastSQL)
	}
	// The lease reaches the statement as seconds, so a claim that rounded it to
	// zero would hand the row to every worker at once.
	if pool.lastArgs[1] != int64(90) {
		t.Fatalf("lease argument = %v, want 90 seconds", pool.lastArgs[1])
	}
	steps, ok := pool.lastArgs[2].(int)
	if !ok || steps <= 0 {
		t.Fatalf("backoff ceiling = %v, want a positive bound", pool.lastArgs[2])
	}
	// The counter is saturated so it cannot overflow its SMALLINT column and
	// stall the queue behind a failing statement.
	if !strings.Contains(pool.lastSQL, "LEAST(a.scan_attempts + 1, $4)") {
		t.Fatalf("the attempt counter is not saturated:\n%s", pool.lastSQL)
	}
}

func TestClaimDueScansRefusesUnusableParameters(t *testing.T) {
	pool := &fakePool{}
	store := storage.NewPGXScanStore(pool)

	for name, claim := range map[string]func() error{
		"zero batch": func() error {
			_, err := store.ClaimDueScans(context.Background(), 0, time.Minute)
			return err
		},
		"zero lease": func() error {
			_, err := store.ClaimDueScans(context.Background(), 1, 0)
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := claim(); !errors.Is(err, domain.ErrInvalidInput) {
				t.Fatalf("error = %v, want ErrInvalidInput", err)
			}
			if pool.lastSQL != "" {
				t.Fatal("an invalid claim reached the database")
			}
		})
	}
}

func TestClaimDueScansFailsClosedWithoutAPool(t *testing.T) {
	if _, err := storage.NewPGXScanStore(nil).ClaimDueScans(
		context.Background(), 1, time.Minute); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("error = %v, want ErrUnavailable", err)
	}
}

// The gauge counts the backlog, not the due subset: a metric that read zero
// while every row was leased would be zero exactly when the queue is busiest.
func TestCountPendingScansCountsTheWholeBacklog(t *testing.T) {
	pool := &fakePool{queryRow: func(string, ...any) pgx.Row {
		return valueRow{values: []any{int64(7)}}
	}}
	depth, err := storage.NewPGXScanStore(pool).CountPendingScans(context.Background())
	if err != nil {
		t.Fatalf("CountPendingScans: %v", err)
	}
	if depth != 7 {
		t.Fatalf("depth = %d, want 7", depth)
	}
	if !strings.Contains(pool.lastSQL, "status = 'pending_scan'") ||
		!strings.Contains(pool.lastSQL, "deleted_at IS NULL") {
		t.Fatalf("unexpected queue-depth query:\n%s", pool.lastSQL)
	}
	if strings.Contains(pool.lastSQL, "next_attempt_at") {
		t.Fatalf("the gauge counts only due rows:\n%s", pool.lastSQL)
	}
}

func TestCountPendingScansWrapsDatabaseFailures(t *testing.T) {
	dbErr := errors.New("connection refused")
	pool := &fakePool{queryRow: func(string, ...any) pgx.Row {
		return errRow{err: dbErr}
	}}
	if _, err := storage.NewPGXScanStore(pool).CountPendingScans(
		context.Background()); !errors.Is(err, dbErr) {
		t.Fatalf("error = %v, want the database failure wrapped", err)
	}
}

// The upload's finalising statement is what enqueues the scan. If it did not,
// an attachment could exist in pending_scan with nothing scheduled to look at
// it — the dual-write failure this design exists to make unrepresentable.
func TestMarkUploadedSchedulesTheScanInTheSameStatement(t *testing.T) {
	pool := &fakePool{}
	store := storage.NewPGXAttachmentStore(pool)

	if err := store.MarkUploaded(context.Background(), finalisedAttachment(domain.StatusPendingScan)); err != nil {
		t.Fatalf("MarkUploaded: %v", err)
	}
	if !strings.Contains(pool.lastSQL,
		"scan_next_attempt_at = CASE WHEN $2 = 'pending_scan' THEN now() ELSE NULL END") {
		t.Fatalf("the finalising statement does not enqueue the scan:\n%s", pool.lastSQL)
	}
	// One statement, so the row and its job become visible together or not at
	// all. A second UPDATE would be a window a crash could land in.
	if strings.Count(pool.lastSQL, "UPDATE") != 1 {
		t.Fatalf("the scan job is not scheduled by the same statement:\n%s", pool.lastSQL)
	}
}

func TestClaimDueScansReportsIterationFailures(t *testing.T) {
	iterErr := errors.New("connection reset mid-read")
	pool := &fakePool{query: func(string, ...any) (pgx.Rows, error) {
		return &valueRows{rows: [][]any{scanRow()}, err: iterErr}, nil
	}}
	if _, err := storage.NewPGXScanStore(pool).ClaimDueScans(
		context.Background(), 1, time.Minute); !errors.Is(err, iterErr) {
		t.Fatalf("error = %v, want the iteration failure wrapped", err)
	}
}

func TestClaimDueScansWrapsQueryFailures(t *testing.T) {
	dbErr := errors.New("connection refused")
	pool := &fakePool{query: func(string, ...any) (pgx.Rows, error) {
		return nil, dbErr
	}}
	if _, err := storage.NewPGXScanStore(pool).ClaimDueScans(
		context.Background(), 1, time.Minute); !errors.Is(err, dbErr) {
		t.Fatalf("error = %v, want the database failure wrapped", err)
	}
}

// A row this build cannot decode is a failure, never a partially populated job:
// a job missing its key material would be handed to the worker and fail again
// halfway, after the claim had already leased the row.
func TestClaimDueScansRefusesARowItCannotDecode(t *testing.T) {
	pool := &fakePool{query: func(string, ...any) (pgx.Rows, error) {
		return &valueRows{rows: [][]any{{"only", "two"}}}, nil
	}}
	if _, err := storage.NewPGXScanStore(pool).ClaimDueScans(
		context.Background(), 1, time.Minute); err == nil {
		t.Fatal("a malformed row was accepted as a claim")
	}
}

func TestCountPendingScansFailsClosedWithoutAPool(t *testing.T) {
	if _, err := storage.NewPGXScanStore(nil).CountPendingScans(
		context.Background()); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("error = %v, want ErrUnavailable", err)
	}
}

// A decided row has left the queue, so its schedule is cleared. Not
// load-bearing — the claim selects on status, so a decided row is already
// invisible to it — but a stale "next attempt at" on an approved file is a lie
// an operator reading the table has to work out for themselves.
func TestATerminalVerdictClearsTheScanSchedule(t *testing.T) {
	t.Run("clean", func(t *testing.T) {
		pool := &fakePool{queryRow: func(string, ...any) pgx.Row {
			return valueRow{values: []any{
				text(string(domain.StatusClean)), text(string(domain.PreviewStatusPending)),
			}}
		}}
		if _, err := storage.NewPGXAttachmentStore(pool).MarkScanClean(
			context.Background(), approvalInput()); err != nil {
			t.Fatalf("MarkScanClean: %v", err)
		}
		if !strings.Contains(pool.lastSQL, "scan_next_attempt_at = NULL") {
			t.Fatalf("an approval left the row scheduled:\n%s", pool.lastSQL)
		}
	})

	t.Run("rejected", func(t *testing.T) {
		pool := &fakePool{queryRow: func(string, ...any) pgx.Row {
			return valueRow{values: []any{
				text(string(domain.StatusRejected)), text(string(domain.PreviewStatusUnsupported)),
			}}
		}}
		if _, err := mustFencedStore(pool).MarkScanRejected(
			context.Background(), rejectionInput()); err != nil {
			t.Fatalf("MarkScanRejected: %v", err)
		}
		if !strings.Contains(pool.lastSQL, "scan_next_attempt_at = NULL") {
			t.Fatalf("a rejection left the row scheduled:\n%s", pool.lastSQL)
		}
	})
}
