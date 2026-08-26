package storage_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/nicrepository/nchat/services/file-service/internal/domain"
	"github.com/nicrepository/nchat/services/file-service/internal/service"
	"github.com/nicrepository/nchat/services/file-service/internal/storage"
)

func previewResult() service.PreviewResult {
	return service.PreviewResult{
		AttachmentID:    testAttachmentID,
		PreviewObjectID: testPreviewObject,
		Size:            2048,
		WrappedDEK:      []byte{4, 4, 4},
		KEKKeyID:        testKEKKeyID,
		EnvelopeVersion: 1,
		KeyWrapVersion:  2,
		ClaimAttempt:    1,
	}
}

func claimedPreviewRow() []any {
	return []any{
		testAttachmentID,
		testWorkspaceID,
		"image/png",
		int64(4096),
		testStorageObject,
		pgtype.Int2{Int16: 1, Valid: true},
		[]byte{9, 9, 9},
		testKEKKeyID,
		pgtype.Int2{Int16: 2, Valid: true},
		pgtype.Int2{Int16: 1, Valid: true},
	}
}

// The claim is the whole concurrency control, so its shape is asserted rather
// than only its result: skipping locked rows is what lets every replica run the
// same loop, and the lease is what makes an abandoned claim recover itself.
func TestClaimDuePreviewsLeasesRowsWithoutBlockingOtherWorkers(t *testing.T) {
	pool := &fakePool{query: func(string, ...any) (pgx.Rows, error) {
		return &valueRows{rows: [][]any{claimedPreviewRow()}}, nil
	}}

	jobs, err := storage.NewPGXPreviewStore(pool).ClaimDuePreviews(
		context.Background(), 1, 2*time.Minute)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(jobs) != 1 || jobs[0].AttachmentID != testAttachmentID {
		t.Fatalf("unexpected jobs: %+v", jobs)
	}
	if jobs[0].Attempts != 1 || jobs[0].EnvelopeVersion != 1 || jobs[0].KeyWrapVersion != 2 {
		t.Fatalf("unexpected job binding: %+v", jobs[0])
	}

	for _, fragment := range []string{
		"FOR UPDATE SKIP LOCKED",
		"preview_status = 'pending'",
		"LIMIT $1",
		"preview_attempts = LEAST(a.preview_attempts + 1, $3)",
		"preview_next_attempt_at = now() + ($2 * interval '1 second')",
		"a.deleted_at IS NULL",
		"a.wrapped_dek IS NOT NULL",
	} {
		if !strings.Contains(pool.lastSQL, fragment) {
			t.Fatalf("claim query is missing %q:\n%s", fragment, pool.lastSQL)
		}
	}
	// The containment control: only an attachment the scan has approved is ever
	// decrypted and handed to a parser, and the condition lives in the atomic
	// claim rather than in a check the job could forget or that could go stale.
	if !strings.Contains(pool.lastSQL, "a.status = 'clean'") {
		t.Fatalf("claim query must require an approved scan:\n%s", pool.lastSQL)
	}
	for _, forbidden := range []string{"pending_scan", "pending_upload", "rejected"} {
		if strings.Contains(pool.lastSQL, forbidden) {
			t.Fatalf("claim query must not select %q rows:\n%s", forbidden, pool.lastSQL)
		}
	}
	// The claim carries no attempts ceiling: bounding it would strand any row
	// whose terminal state could not be written, since that state is itself a
	// database write. The renderer's budget is enforced in the service.
	if strings.Contains(pool.lastSQL, "preview_attempts <") {
		t.Fatalf("claim eligibility must not depend on the attempt count:\n%s", pool.lastSQL)
	}
	if pool.lastArgs[0] != 1 || pool.lastArgs[1] != int64(120) {
		t.Fatalf("unexpected claim arguments: %v", pool.lastArgs)
	}
}

func TestClaimDuePreviewsRefusesParametersThatWouldBreakTheLease(t *testing.T) {
	store := storage.NewPGXPreviewStore(&fakePool{})
	for name, tt := range map[string]struct {
		batch int
		lease time.Duration
	}{
		"no batch": {batch: 0, lease: time.Minute},
		"no lease": {batch: 1, lease: 0},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := store.ClaimDuePreviews(context.Background(), tt.batch, tt.lease)
			if !errors.Is(err, domain.ErrInvalidInput) {
				t.Fatalf("error = %v, want ErrInvalidInput", err)
			}
		})
	}
}

func TestClaimDuePreviewsWrapsDatabaseFailures(t *testing.T) {
	dbErr := errors.New("connection reset")
	pool := &fakePool{query: func(string, ...any) (pgx.Rows, error) { return nil, dbErr }}
	_, err := storage.NewPGXPreviewStore(pool).ClaimDuePreviews(
		context.Background(), 1, time.Minute)
	if !errors.Is(err, dbErr) {
		t.Fatalf("expected the database error to be wrapped, got %v", err)
	}
}

func TestMarkPreviewReadyWritesTheWholeBindingAtOnce(t *testing.T) {
	pool := &fakePool{queryRow: func(string, ...any) pgx.Row {
		return valueRow{values: []any{1}}
	}}
	recorded, err := storage.NewPGXPreviewStore(pool).MarkPreviewReady(
		context.Background(), previewResult())
	if err != nil || !recorded {
		t.Fatalf("recorded = %v, err = %v", recorded, err)
	}
	for _, column := range []string{
		"preview_status = 'ready'",
		"preview_object_id = $2",
		"preview_size_bytes = $3",
		"preview_wrapped_dek = $4",
		"preview_kek_key_id = $5",
		"preview_envelope_version = $6",
		"preview_dek_wrap_version = $7",
		"preview_page_count = $9",
		"preview_next_attempt_at = NULL",
	} {
		if !strings.Contains(pool.lastSQL, column) {
			t.Fatalf("the recording update must set %s:\n%s", column, pool.lastSQL)
		}
	}
	// The idempotency fence: a late attempt must not overwrite a preview that
	// is already being served.
	if !strings.Contains(pool.lastSQL, "AND preview_status = 'pending'") {
		t.Fatalf("the update must pin the pending state:\n%s", pool.lastSQL)
	}
}

// The publishing statement re-asserts the scan verdict, because a render takes
// time and a verdict can land inside that window. Without it, a preview of a
// file the scanner condemned mid-render would be published as ready.
func TestMarkPreviewReadyRevalidatesTheScanVerdictBeforePublishing(t *testing.T) {
	pool := &fakePool{queryRow: func(string, ...any) pgx.Row {
		return valueRow{values: []any{1}}
	}}
	if _, err := storage.NewPGXPreviewStore(pool).MarkPreviewReady(
		context.Background(), previewResult()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, condition := range []string{
		"AND preview_status = 'pending'",
		"AND status = 'clean'",
		"AND deleted_at IS NULL",
	} {
		if !strings.Contains(pool.lastSQL, condition) {
			t.Fatalf("the publishing update must require %q:\n%s", condition, pool.lastSQL)
		}
	}
}

// The extra pages ride on the same statement, and the pages a previous attempt
// might have left behind are cleared unconditionally rather than merged with
// the new set: cleared and inserted are data-modifying CTEs, which PostgreSQL
// always runs to completion even though the final SELECT reads only updated.
func TestMarkPreviewReadyClearsAndReinsertsExtraPages(t *testing.T) {
	pool := &fakePool{queryRow: func(string, ...any) pgx.Row {
		return valueRow{values: []any{1}}
	}}
	result := previewResult()
	result.PageCount = 3
	result.ExtraPages = []service.PreviewPage{
		{PageNumber: 2, ObjectID: testPreviewObject, Size: 512,
			WrappedDEK: []byte{1}, KEKKeyID: testKEKKeyID, EnvelopeVersion: 1, KeyWrapVersion: 2},
		{PageNumber: 3, ObjectID: testPreviewObject, Size: 512,
			WrappedDEK: []byte{2}, KEKKeyID: testKEKKeyID, EnvelopeVersion: 1, KeyWrapVersion: 2},
	}
	recorded, err := storage.NewPGXPreviewStore(pool).MarkPreviewReady(context.Background(), result)
	if err != nil || !recorded {
		t.Fatalf("recorded = %v, err = %v", recorded, err)
	}
	for _, fragment := range []string{
		"DELETE FROM files.attachment_preview_pages",
		"INSERT INTO files.attachment_preview_pages",
		"unnest(",
	} {
		if !strings.Contains(pool.lastSQL, fragment) {
			t.Fatalf("the statement must %q:\n%s", fragment, pool.lastSQL)
		}
	}
	pageNumbers, ok := pool.lastArgs[9].([]int16)
	if !ok || len(pageNumbers) != 2 || pageNumbers[0] != 2 || pageNumbers[1] != 3 {
		t.Fatalf("unexpected page numbers argument: %#v", pool.lastArgs[9])
	}
}

func TestMarkPreviewReadyReportsALostRaceWithoutFailing(t *testing.T) {
	pool := &fakePool{queryRow: func(string, ...any) pgx.Row {
		return valueRow{values: []any{0}}
	}}
	recorded, err := storage.NewPGXPreviewStore(pool).MarkPreviewReady(
		context.Background(), previewResult())
	if err != nil {
		t.Fatalf("a lost race is not an error: %v", err)
	}
	if recorded {
		t.Fatal("a row that was no longer pending must be reported as not recorded")
	}
}

// A preview recorded without its full binding could not be opened, and the
// CHECK would refuse it anyway. Refusing here keeps it a domain error rather
// than driver text.
func TestMarkPreviewReadyRefusesAnIncompleteBinding(t *testing.T) {
	for name, mutate := range map[string]func(*service.PreviewResult){
		"no object id": func(r *service.PreviewResult) { r.PreviewObjectID = "" },
		"no size":      func(r *service.PreviewResult) { r.Size = 0 },
		"no key":       func(r *service.PreviewResult) { r.WrappedDEK = nil },
		"no key id":    func(r *service.PreviewResult) { r.KEKKeyID = "" },
	} {
		t.Run(name, func(t *testing.T) {
			result := previewResult()
			mutate(&result)
			pool := &fakePool{}
			_, err := storage.NewPGXPreviewStore(pool).MarkPreviewReady(context.Background(), result)
			if !errors.Is(err, domain.ErrInvalidInput) {
				t.Fatalf("error = %v, want ErrInvalidInput", err)
			}
			if pool.lastSQL != "" {
				t.Fatal("an incomplete binding must not reach the database")
			}
		})
	}
}

func TestMarkPreviewTerminalAcceptsOnlyTheStatesWithNoObject(t *testing.T) {
	for _, status := range []domain.PreviewStatus{
		domain.PreviewStatusUnsupported, domain.PreviewStatusFailed,
	} {
		pool := &fakePool{exec: func(string, ...any) (pgconn.CommandTag, error) {
			return pgconn.NewCommandTag("UPDATE 1"), nil
		}}
		recorded, err := storage.NewPGXPreviewStore(pool).MarkPreviewTerminal(
			context.Background(), testAttachmentID, 1, status)
		if err != nil || !recorded {
			t.Fatalf("%q: recorded = %v, err = %v", status, recorded, err)
		}
		if !strings.Contains(pool.lastSQL, "AND preview_status = 'pending'") {
			t.Fatalf("%q: the update must pin the pending state", status)
		}
	}

	for _, status := range []domain.PreviewStatus{
		domain.PreviewStatusReady, domain.PreviewStatusPending, "anything",
	} {
		pool := &fakePool{}
		if _, err := storage.NewPGXPreviewStore(pool).MarkPreviewTerminal(
			context.Background(), testAttachmentID, 1, status,
		); !errors.Is(err, domain.ErrInvalidInput) {
			t.Fatalf("%q must be refused, got %v", status, err)
		}
		if pool.lastSQL != "" {
			t.Fatalf("%q must not reach the database", status)
		}
	}
}

func TestPreviewStoreRefusesToRunWithoutAPool(t *testing.T) {
	store := storage.NewPGXPreviewStore(nil)
	if _, err := store.ClaimDuePreviews(context.Background(), 1, time.Minute); !errors.Is(
		err, domain.ErrUnavailable,
	) {
		t.Fatalf("claim error = %v, want ErrUnavailable", err)
	}
	if _, err := store.MarkPreviewReady(context.Background(), previewResult()); !errors.Is(
		err, domain.ErrUnavailable,
	) {
		t.Fatalf("ready error = %v, want ErrUnavailable", err)
	}
	if _, err := store.MarkPreviewTerminal(
		context.Background(), testAttachmentID, 1, domain.PreviewStatusFailed,
	); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("terminal error = %v, want ErrUnavailable", err)
	}
}

// RevalidateClaim is what the preview worker asks inside the fence, immediately
// before it decrypts anything. Every condition in it is a reason not to.
func TestRevalidateClaimRequiresEveryConditionTheClaimAsserted(t *testing.T) {
	pool := &fakePool{queryRow: func(string, ...any) pgx.Row {
		return valueRow{values: []any{true}}
	}}
	valid, err := storage.NewPGXPreviewStore(pool).RevalidateClaim(
		context.Background(), testAttachmentID, 2)
	if err != nil || !valid {
		t.Fatalf("valid = %v, err = %v", valid, err)
	}
	for _, condition := range []string{
		"status = 'clean'",
		"deleted_at IS NULL",
		"preview_status = 'pending'",
		"preview_attempts = $2",
	} {
		if !strings.Contains(pool.lastSQL, condition) {
			t.Fatalf("revalidation must require %q:\n%s", condition, pool.lastSQL)
		}
	}
	if pool.lastArgs[0] != testAttachmentID || pool.lastArgs[1] != 2 {
		t.Fatalf("unexpected arguments: %v", pool.lastArgs)
	}
}

func TestRevalidateClaimReportsAnInvalidatedRow(t *testing.T) {
	pool := &fakePool{queryRow: func(string, ...any) pgx.Row {
		return valueRow{values: []any{false}}
	}}
	valid, err := storage.NewPGXPreviewStore(pool).RevalidateClaim(
		context.Background(), testAttachmentID, 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if valid {
		t.Fatal("an invalidated row must not revalidate")
	}
}

func TestRevalidateClaimRefusesARequestWithoutAClaim(t *testing.T) {
	pool := &fakePool{}
	store := storage.NewPGXPreviewStore(pool)
	for name, tt := range map[string]struct {
		id      string
		attempt int
	}{
		"no attachment": {id: "", attempt: 1},
		"no claim":      {id: testAttachmentID, attempt: 0},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := store.RevalidateClaim(context.Background(), tt.id, tt.attempt); !errors.Is(
				err, domain.ErrInvalidInput,
			) {
				t.Fatalf("error = %v, want ErrInvalidInput", err)
			}
			if pool.lastSQL != "" {
				t.Fatal("an incomplete revalidation must not reach the database")
			}
		})
	}
	if _, err := storage.NewPGXPreviewStore(nil).RevalidateClaim(
		context.Background(), testAttachmentID, 1); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("error = %v, want ErrUnavailable", err)
	}
}

func TestRevalidateClaimWrapsDatabaseFailures(t *testing.T) {
	dbErr := errors.New("connection reset")
	pool := &fakePool{queryRow: func(string, ...any) pgx.Row { return errRow{err: dbErr} }}
	if _, err := storage.NewPGXPreviewStore(pool).RevalidateClaim(
		context.Background(), testAttachmentID, 1); !errors.Is(err, dbErr) {
		t.Fatalf("expected the database error to be wrapped, got %v", err)
	}
}
