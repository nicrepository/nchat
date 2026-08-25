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

func newAttachment(kind domain.DestinationKind, destinationID string) service.NewAttachment {
	return service.NewAttachment{
		ID:               testAttachmentID,
		WorkspaceID:      testWorkspaceID,
		UploaderID:       testUserID,
		Destination:      domain.Destination{Kind: kind, ID: destinationID},
		Filename:         "report.pdf",
		DeclaredMIME:     "application/pdf",
		StorageProvider:  domain.StorageProviderSeaweedFS,
		StorageObjectKey: testStorageObject,
		EnvelopeVersion:  1,
		KeyWrapVersion:   2,
	}
}

func TestCreatePendingSetsExactlyOneDestinationColumn(t *testing.T) {
	tests := []struct {
		name              string
		kind              domain.DestinationKind
		destinationID     string
		wantChannel       any
		wantConversation  any
		destinationColumn string
	}{
		{
			name: "channel", kind: domain.DestinationKindChannel, destinationID: testChannelID,
			wantChannel: any(testChannelID), wantConversation: nil,
		},
		{
			name: "dm", kind: domain.DestinationKindDM, destinationID: testConversation,
			wantChannel: nil, wantConversation: any(testConversation),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pool := &fakePool{}
			store := storage.NewPGXAttachmentStore(pool)

			if err := store.CreatePending(context.Background(),
				newAttachment(tt.kind, tt.destinationID)); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !strings.Contains(pool.lastSQL, "INSERT INTO files.attachments") {
				t.Fatalf("unexpected statement: %s", pool.lastSQL)
			}
			// args: id, workspace, uploader, kind, channel, conversation, ...
			if pool.lastArgs[4] != tt.wantChannel {
				t.Fatalf("expected channel_id %v, got %v", tt.wantChannel, pool.lastArgs[4])
			}
			if pool.lastArgs[5] != tt.wantConversation {
				t.Fatalf("expected conversation_id %v, got %v", tt.wantConversation, pool.lastArgs[5])
			}
		})
	}
}

func TestCreatePendingAlwaysStartsAtPendingUpload(t *testing.T) {
	pool := &fakePool{}
	if err := storage.NewPGXAttachmentStore(pool).CreatePending(context.Background(),
		newAttachment(domain.DestinationKindChannel, testChannelID)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	status, ok := pool.lastArgs[len(pool.lastArgs)-2].(string)
	if !ok || status != string(domain.StatusPendingUpload) {
		t.Fatalf("expected pending_upload, got %v", pool.lastArgs[len(pool.lastArgs)-2])
	}
}

func TestCreatePendingPersistsDraftExpiry(t *testing.T) {
	expiresAt := time.Now().UTC().Add(time.Hour)
	attachment := newAttachment(domain.DestinationKindChannel, testChannelID)
	attachment.DraftExpiresAt = &expiresAt
	pool := &fakePool{}
	if err := storage.NewPGXAttachmentStore(pool).CreatePending(context.Background(), attachment); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := pool.lastArgs[len(pool.lastArgs)-1]; got != attachment.DraftExpiresAt {
		t.Fatalf("expected draft expiry %v, got %v", attachment.DraftExpiresAt, got)
	}
}

func TestCancelDraftIsNonEnumeratingAndQueuesCleanupAtomically(t *testing.T) {
	pool := &fakePool{queryRow: func(sql string, _ ...any) pgx.Row {
		for _, fragment := range []string{
			"uploader_id = $2", "draft_expires_at IS NOT NULL",
			"chat.message_attachments", "FOR UPDATE OF a", "object_cleanup_jobs",
		} {
			if !strings.Contains(sql, fragment) {
				t.Fatalf("cancel query missing %q", fragment)
			}
		}
		return valueRow{values: []any{true}}
	}}
	if err := storage.NewPGXAttachmentStore(pool).CancelDraft(
		context.Background(), testAttachmentID, testUserID,
	); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCancelDraftReturnsTheSameNotFoundForEveryIneligibleCandidate(t *testing.T) {
	pool := &fakePool{queryRow: func(string, ...any) pgx.Row {
		return valueRow{values: []any{false}}
	}}
	err := storage.NewPGXAttachmentStore(pool).CancelDraft(
		context.Background(), testAttachmentID, testUserID,
	)
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected non-enumerating ErrNotFound, got %v", err)
	}
}

func TestExpireDraftsUsesBoundedSkipLockedClaim(t *testing.T) {
	pool := &fakePool{queryRow: func(sql string, args ...any) pgx.Row {
		for _, fragment := range []string{
			"draft_expires_at <= now()", "FOR UPDATE OF a SKIP LOCKED",
			"chat.message_attachments", "LIMIT $1", "object_cleanup_jobs",
		} {
			if !strings.Contains(sql, fragment) {
				t.Fatalf("expiry query missing %q", fragment)
			}
		}
		if len(args) != 1 || args[0] != 25 {
			t.Fatalf("unexpected expiry arguments: %v", args)
		}
		return valueRow{values: []any{3}}
	}}
	got, err := storage.NewPGXAttachmentStore(pool).ExpireDrafts(context.Background(), 25)
	if err != nil || got != 3 {
		t.Fatalf("expected three expired drafts, got %d, %v", got, err)
	}
}

func TestCreatePendingRejectsUnknownDestinationKind(t *testing.T) {
	err := storage.NewPGXAttachmentStore(&fakePool{}).CreatePending(context.Background(),
		newAttachment("workspace", testChannelID))
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput, got %v", err)
	}
}

func TestCreatePendingWrapsDatabaseFailures(t *testing.T) {
	dbErr := errors.New("unique violation")
	pool := &fakePool{exec: func(string, ...any) (pgconn.CommandTag, error) {
		return pgconn.CommandTag{}, dbErr
	}}
	err := storage.NewPGXAttachmentStore(pool).CreatePending(context.Background(),
		newAttachment(domain.DestinationKindChannel, testChannelID))
	if !errors.Is(err, dbErr) {
		t.Fatalf("expected the database error to be wrapped, got %v", err)
	}
}

// finalisedAttachment is the shape MarkUploaded requires: the sizes and the key
// binding together, because the wrapped key authenticates the size.
func finalisedAttachment(status domain.Status) service.UploadedAttachment {
	return service.UploadedAttachment{
		ID: testAttachmentID, Status: status,
		PreviewStatus: domain.PreviewStatusPending,
		DetectedMIME:  "application/pdf", Size: 10, CiphertextSize: 42,
		WrappedDEK: []byte{7, 7, 7}, KEKKeyID: testKEKKeyID,
	}
}

func TestMarkUploadedOnlyAdvancesAPendingRow(t *testing.T) {
	pool := &fakePool{exec: func(string, ...any) (pgconn.CommandTag, error) {
		return pgconn.NewCommandTag("UPDATE 1"), nil
	}}
	err := storage.NewPGXAttachmentStore(pool).MarkUploaded(context.Background(),
		finalisedAttachment(domain.StatusPendingScan))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// The size and the key material must move in the same statement, so a row
	// can never be downloadable with a length its sealed key does not cover.
	for _, column := range []string{"size_bytes = $4", "wrapped_dek = $6", "kek_key_id = $7"} {
		if !strings.Contains(pool.lastSQL, column) {
			t.Fatalf("the finalising update must set %s", column)
		}
	}
	if !strings.Contains(pool.lastSQL, "AND status = $8") {
		t.Fatal("the update must pin the previous state so a compensated row cannot be revived")
	}
	if pool.lastArgs[7] != string(domain.StatusPendingUpload) {
		t.Fatalf("expected the guard to be pending_upload, got %v", pool.lastArgs[7])
	}
	// The preview job is scheduled by this same statement, so an attachment
	// that exists and can be previewed is already queued: there is no second
	// write that a crash could lose.
	if !strings.Contains(pool.lastSQL, "preview_status = $9") ||
		!strings.Contains(pool.lastSQL, "preview_next_attempt_at = CASE WHEN $9 = 'pending'") {
		t.Fatal("the finalising update must schedule the preview job")
	}
	if pool.lastArgs[8] != string(domain.PreviewStatusPending) {
		t.Fatalf("expected the preview to be queued, got %v", pool.lastArgs[8])
	}
}

func TestMarkUploadedRejectsAnUnknownStatus(t *testing.T) {
	err := storage.NewPGXAttachmentStore(&fakePool{}).MarkUploaded(context.Background(),
		service.UploadedAttachment{ID: testAttachmentID, Status: "scanned"})
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput, got %v", err)
	}
}

func TestMarkUploadedReportsAMissingPendingRow(t *testing.T) {
	pool := &fakePool{exec: func(string, ...any) (pgconn.CommandTag, error) {
		return pgconn.NewCommandTag("UPDATE 0"), nil
	}}
	err := storage.NewPGXAttachmentStore(pool).MarkUploaded(context.Background(),
		finalisedAttachment(domain.StatusClean))
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestMarkUploadedWrapsDatabaseFailures(t *testing.T) {
	dbErr := errors.New("deadlock detected")
	pool := &fakePool{exec: func(string, ...any) (pgconn.CommandTag, error) {
		return pgconn.CommandTag{}, dbErr
	}}
	err := storage.NewPGXAttachmentStore(pool).MarkUploaded(context.Background(),
		finalisedAttachment(domain.StatusClean))
	if !errors.Is(err, dbErr) {
		t.Fatalf("expected the database error to be wrapped, got %v", err)
	}
}

// A finalising update without the full binding never reaches the database. It
// would violate attachments_dek_binding_complete_check there anyway; refusing it
// here keeps the failure a domain error instead of driver text.
func TestMarkUploadedRefusesAnIncompleteKeyBinding(t *testing.T) {
	for name, mutate := range map[string]func(*service.UploadedAttachment){
		"no wrapped key":    func(u *service.UploadedAttachment) { u.WrappedDEK = nil },
		"empty wrapped key": func(u *service.UploadedAttachment) { u.WrappedDEK = []byte{} },
		"no key id":         func(u *service.UploadedAttachment) { u.KEKKeyID = "" },
	} {
		t.Run(name, func(t *testing.T) {
			pool := &fakePool{}
			update := finalisedAttachment(domain.StatusClean)
			mutate(&update)

			if err := storage.NewPGXAttachmentStore(pool).MarkUploaded(
				context.Background(), update,
			); !errors.Is(err, domain.ErrInvalidInput) {
				t.Fatalf("expected ErrInvalidInput, got %v", err)
			}
			if pool.lastSQL != "" {
				t.Fatal("an incomplete binding must not reach the database")
			}
		})
	}
}

// The pending insert must carry no key material: there is none yet.
func TestCreatePendingWritesNoKeyMaterial(t *testing.T) {
	pool := &fakePool{}
	if err := storage.NewPGXAttachmentStore(pool).CreatePending(context.Background(),
		newAttachment(domain.DestinationKindChannel, testChannelID)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, column := range []string{"wrapped_dek", "kek_key_id"} {
		if strings.Contains(pool.lastSQL, column) {
			t.Fatalf("the pending insert must not write %s", column)
		}
	}
	// It must write the wrap version, though: that column is NOT NULL with no
	// default, so naming it is the schema fence against the previous build.
	if !strings.Contains(pool.lastSQL, "dek_wrap_version") {
		t.Fatal("the pending insert must name dek_wrap_version")
	}
}

// A caller that omits the wrap version never reaches the database: the not-null
// violation would otherwise surface as driver text.
func TestCreatePendingRequiresTheKeyWrapVersion(t *testing.T) {
	pool := &fakePool{}
	attachment := newAttachment(domain.DestinationKindChannel, testChannelID)
	attachment.KeyWrapVersion = 0

	if err := storage.NewPGXAttachmentStore(pool).CreatePending(
		context.Background(), attachment,
	); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput, got %v", err)
	}
	if pool.lastSQL != "" {
		t.Fatal("an attachment without a wrap version must not reach the database")
	}
}

func TestMarkFailedIsIdempotentOnAlreadyTerminalRows(t *testing.T) {
	pool := &fakePool{exec: func(string, ...any) (pgconn.CommandTag, error) {
		return pgconn.NewCommandTag("UPDATE 0"), nil
	}}
	if err := storage.NewPGXAttachmentStore(pool).MarkFailed(
		context.Background(), testAttachmentID, "storage_write"); err != nil {
		t.Fatalf("compensation must tolerate an already-terminal row, got %v", err)
	}
	if pool.lastArgs[1] != string(domain.StatusFailed) {
		t.Fatalf("expected the row to be marked failed, got %v", pool.lastArgs[1])
	}
}

func TestMarkFailedWrapsDatabaseFailures(t *testing.T) {
	dbErr := errors.New("connection lost")
	pool := &fakePool{exec: func(string, ...any) (pgconn.CommandTag, error) {
		return pgconn.CommandTag{}, dbErr
	}}
	err := storage.NewPGXAttachmentStore(pool).MarkFailed(
		context.Background(), testAttachmentID, "storage_write")
	if !errors.Is(err, dbErr) {
		t.Fatalf("expected the database error to be wrapped, got %v", err)
	}
}

func authorizedAttachmentRow(status domain.Status) []any {
	return []any{
		timestamp(time.Now().Add(time.Hour)),
		text(testAttachmentID),
		text(testWorkspaceID),
		text(string(domain.DestinationKindChannel)),
		text(string(status)),
		text("report.pdf"),
		text("application/pdf"),
		text("application/pdf"),
		pgtype.Int8{Int64: 1234, Valid: true},
		text(testStorageObject),
		pgtype.Int2{Int16: 1, Valid: true},
		[]byte{9, 9, 9},
		text(testKEKKeyID),
		pgtype.Int2{Int16: 2, Valid: true},
		timestamp(time.Now()),
		text(string(domain.PreviewStatusReady)),
		text(testPreviewObject),
		pgtype.Int8{Int64: 4096, Valid: true},
		[]byte{5, 5, 5},
		text(testKEKKeyID),
		pgtype.Int2{Int16: 1, Valid: true},
		pgtype.Int2{Int16: 2, Valid: true},
	}
}

// A row written before migration 000003, or one carrying a value outside the
// closed set, must read as "no preview" rather than as one the service would
// then try to open. It fails closed towards the client's fallback.
func TestGetAuthorizedReadsAnAbsentPreviewStateAsUnsupported(t *testing.T) {
	for name, stored := range map[string]any{
		"null":    pgtype.Text{},
		"unknown": text("processing"),
	} {
		t.Run(name, func(t *testing.T) {
			row := authorizedAttachmentRow(domain.StatusClean)
			row[15] = stored
			pool := &fakePool{queryRow: func(string, ...any) pgx.Row {
				return valueRow{values: row}
			}}
			record, err := storage.NewPGXAttachmentStore(pool).GetAuthorized(context.Background(),
				service.AttachmentAuthInput{
					AttachmentID: testAttachmentID, UserID: testUserID, SessionID: testSessionID,
				})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if record.PreviewStatus != domain.PreviewStatusUnsupported {
				t.Fatalf("preview status = %q, want unsupported", record.PreviewStatus)
			}
		})
	}
}

func TestGetAuthorizedReturnsTheStoredAttachment(t *testing.T) {
	pool := &fakePool{queryRow: func(string, ...any) pgx.Row {
		return valueRow{values: authorizedAttachmentRow(domain.StatusClean)}
	}}
	record, err := storage.NewPGXAttachmentStore(pool).GetAuthorized(context.Background(),
		service.AttachmentAuthInput{
			AttachmentID: testAttachmentID, UserID: testUserID, SessionID: testSessionID,
		})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if record.ID != testAttachmentID || record.Status != domain.StatusClean {
		t.Fatalf("unexpected record: %+v", record)
	}
	if record.StorageObjectKey != testStorageObject || record.EnvelopeVersion != 1 {
		t.Fatalf("unexpected storage fields: %+v", record)
	}
	// The key id has to survive the read: without it the download cannot pick a
	// key, and a row that lost it must fail closed rather than default to one.
	if record.KEKKeyID != testKEKKeyID || record.KeyWrapVersion != 2 {
		t.Fatalf("unexpected key binding: id=%q version=%d", record.KEKKeyID, record.KeyWrapVersion)
	}
	if record.Size != 1234 || record.Filename != "report.pdf" {
		t.Fatalf("unexpected metadata: %+v", record)
	}
}

func TestGetAuthorizedReEvaluatesMembershipInTheSameQuery(t *testing.T) {
	pool := &fakePool{queryRow: func(string, ...any) pgx.Row {
		return valueRow{values: authorizedAttachmentRow(domain.StatusClean)}
	}}
	if _, err := storage.NewPGXAttachmentStore(pool).GetAuthorized(context.Background(),
		service.AttachmentAuthInput{
			AttachmentID: testAttachmentID, UserID: testUserID, SessionID: testSessionID,
		}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, fragment := range []string{
		"active_session",
		"chat.workspace_members",
		"chat.channel_visible_to_user(c.id, active.user_id)",
		"chat.dm_members",
		"a.deleted_at IS NULL",
		"m.status <> 'active'",
		"a.workspace_id",
	} {
		if !strings.Contains(pool.lastSQL, fragment) {
			t.Fatalf("the read query must contain %q", fragment)
		}
	}
	if strings.Contains(pool.lastSQL, testAttachmentID) {
		t.Fatal("the attachment id must be a bound parameter, not interpolated SQL")
	}
}

func TestGetAuthorizedRejectsAnInvalidSession(t *testing.T) {
	row := authorizedAttachmentRow(domain.StatusClean)
	row[0] = pgtype.Timestamptz{}
	pool := &fakePool{queryRow: func(string, ...any) pgx.Row { return valueRow{values: row} }}

	_, err := storage.NewPGXAttachmentStore(pool).GetAuthorized(context.Background(),
		service.AttachmentAuthInput{
			AttachmentID: testAttachmentID, UserID: testUserID, SessionID: testSessionID,
		})
	if !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("expected ErrUnauthorized, got %v", err)
	}
}

func TestGetAuthorizedHidesInvisibleAttachments(t *testing.T) {
	row := authorizedAttachmentRow(domain.StatusClean)
	for i := 1; i < len(row); i++ {
		row[i] = nil
	}
	pool := &fakePool{queryRow: func(string, ...any) pgx.Row { return valueRow{values: row} }}

	_, err := storage.NewPGXAttachmentStore(pool).GetAuthorized(context.Background(),
		service.AttachmentAuthInput{
			AttachmentID: testAttachmentID, UserID: testUserID, SessionID: testSessionID,
		})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestGetAuthorizedRefusesAnUnknownStatus(t *testing.T) {
	row := authorizedAttachmentRow(domain.StatusClean)
	row[4] = text("quarantined")
	pool := &fakePool{queryRow: func(string, ...any) pgx.Row { return valueRow{values: row} }}

	_, err := storage.NewPGXAttachmentStore(pool).GetAuthorized(context.Background(),
		service.AttachmentAuthInput{
			AttachmentID: testAttachmentID, UserID: testUserID, SessionID: testSessionID,
		})
	if err == nil {
		t.Fatal("expected an error for a status outside the closed set")
	}
	if errors.Is(err, domain.ErrNotFound) || errors.Is(err, domain.ErrUnauthorized) {
		t.Fatal("a data-integrity problem must not be reported as a client error")
	}
}

func TestGetAuthorizedWrapsDatabaseFailures(t *testing.T) {
	dbErr := errors.New("read timeout")
	pool := &fakePool{queryRow: func(string, ...any) pgx.Row { return errRow{err: dbErr} }}
	_, err := storage.NewPGXAttachmentStore(pool).GetAuthorized(context.Background(),
		service.AttachmentAuthInput{
			AttachmentID: testAttachmentID, UserID: testUserID, SessionID: testSessionID,
		})
	if !errors.Is(err, dbErr) {
		t.Fatalf("expected the database error to be wrapped, got %v", err)
	}
}

func TestAttachmentStoreWithoutPoolIsUnavailable(t *testing.T) {
	stores := []*storage.PGXAttachmentStore{nil, storage.NewPGXAttachmentStore(nil)}
	for _, store := range stores {
		if err := store.CreatePending(context.Background(),
			newAttachment(domain.DestinationKindChannel, testChannelID)); !errors.Is(err, domain.ErrUnavailable) {
			t.Fatalf("expected ErrUnavailable from CreatePending, got %v", err)
		}
		if err := store.MarkUploaded(context.Background(),
			service.UploadedAttachment{ID: testAttachmentID, Status: domain.StatusClean}); !errors.Is(err, domain.ErrUnavailable) {
			t.Fatalf("expected ErrUnavailable from MarkUploaded, got %v", err)
		}
		if err := store.MarkFailed(context.Background(), testAttachmentID, "x"); !errors.Is(err, domain.ErrUnavailable) {
			t.Fatalf("expected ErrUnavailable from MarkFailed, got %v", err)
		}
		if _, err := store.GetAuthorized(context.Background(),
			service.AttachmentAuthInput{}); !errors.Is(err, domain.ErrUnavailable) {
			t.Fatalf("expected ErrUnavailable from GetAuthorized, got %v", err)
		}
	}
}

// --- scan verdicts (SR-001) -----------------------------------------------

// fencedStore builds the store the invalidations need, with a fence that
// records what it was asked for. The real fence is exercised against
// PostgreSQL in the integration suite; here the point is that the operations
// take it at all.
func fencedStore(pool storage.Pool) (*storage.PGXAttachmentStore, *recordingFence) {
	fence := &recordingFence{querier: pool}
	return storage.NewFencedAttachmentStore(pool, fence), fence
}

// mustFencedStore is the fenced store without the handle, for the many cases
// that only need the operation to run.
func mustFencedStore(pool storage.Pool) *storage.PGXAttachmentStore {
	store, _ := fencedStore(pool)
	return store
}

type recordingFence struct {
	querier  storage.TransactionalQuerier
	acquired []int64
	released int
	err      error
}

// WithinTransaction stands in for the transaction-scoped advisory lock: it
// records that the fence was taken and runs the statement on the pool the test
// wired, which is what the real one does inside its transaction.
func (f *recordingFence) WithinTransaction(
	_ context.Context, key int64,
	run func(storage.TransactionalQuerier) (service.AttachmentLifecycleState, error),
) (service.AttachmentLifecycleState, error) {
	if f.err != nil {
		return service.AttachmentLifecycleState{}, f.err
	}
	f.acquired = append(f.acquired, key)
	state, err := run(f.querier)
	f.released++
	return state, err
}

func rejectionInput() service.ScanRejection {
	return service.ScanRejection{AttachmentID: testAttachmentID, WorkspaceID: testWorkspaceID}
}

// The bug this operation exists for: a rejection that only wrote `status` left
// the row unclaimable *and* unfinished — the preview worker skips anything that
// is not clean, and nothing else ever concludes a pending preview. Both facts
// therefore have to move in the same statement.
func TestMarkScanRejectedFinalisesTheStatusAndThePreviewTogether(t *testing.T) {
	pool := &fakePool{queryRow: func(string, ...any) pgx.Row {
		return valueRow{values: []any{
			text(string(domain.StatusRejected)),
			text(string(domain.PreviewStatusUnsupported)),
		}}
	}}

	outcome, err := mustFencedStore(pool).MarkScanRejected(
		context.Background(), rejectionInput())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if outcome.Status != domain.StatusRejected ||
		outcome.PreviewStatus != domain.PreviewStatusUnsupported {
		t.Fatalf("unexpected outcome: %+v", outcome)
	}

	// One statement, and it carries the whole transition.
	if strings.Count(pool.lastSQL, "UPDATE") != 1 {
		t.Fatalf("the verdict must be a single statement:\n%s", pool.lastSQL)
	}
	for _, fragment := range []string{
		"SET status = 'rejected'",
		"preview_status = CASE",
		"WHEN preview_status = 'pending' THEN 'unsupported'",
		"preview_next_attempt_at = CASE",
		"WHEN preview_status = 'pending' THEN NULL",
		"RETURNING status, preview_status",
	} {
		if !strings.Contains(pool.lastSQL, fragment) {
			t.Fatalf("the verdict statement is missing %q:\n%s", fragment, pool.lastSQL)
		}
	}
	// Scoped to one row of one tenant, and never to a deleted one.
	for _, guard := range []string{"WHERE id = $1", "AND workspace_id = $2", "AND deleted_at IS NULL"} {
		if !strings.Contains(pool.lastSQL, guard) {
			t.Fatalf("the verdict statement is missing %q:\n%s", guard, pool.lastSQL)
		}
	}
	if pool.lastArgs[0] != testAttachmentID || pool.lastArgs[1] != testWorkspaceID {
		t.Fatalf("unexpected arguments: %v", pool.lastArgs)
	}
}

// The set is exactly the states a verdict can apply to. 'rejected' is in it for
// two reasons: the same verdict arriving twice, and repairing a row an earlier
// build left in the broken rejected+pending state.
func TestMarkScanRejectedAcceptsOnlyTheStatesAVerdictAppliesTo(t *testing.T) {
	pool := &fakePool{queryRow: func(string, ...any) pgx.Row {
		return valueRow{values: []any{
			text(string(domain.StatusRejected)), text(string(domain.PreviewStatusUnsupported)),
		}}
	}}
	if _, err := mustFencedStore(pool).MarkScanRejected(
		context.Background(), rejectionInput()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(pool.lastSQL, "AND status IN ('pending_scan', 'clean', 'rejected')") {
		t.Fatalf("unexpected accepted states:\n%s", pool.lastSQL)
	}
	// An upload that never finished has no scanned object, and a deleted row is
	// not a live attachment.
	for _, forbidden := range []string{"'pending_upload'", "'failed'", "'deleted'"} {
		if strings.Contains(pool.lastSQL, forbidden) {
			t.Fatalf("a verdict must not apply to %s:\n%s", forbidden, pool.lastSQL)
		}
	}
	// The audit counter survives the verdict: it records work that was really
	// done, and a verdict does not undo it.
	if strings.Contains(pool.lastSQL, "preview_attempts") {
		t.Fatalf("the verdict must not touch the attempt counter:\n%s", pool.lastSQL)
	}
}

// No row matched means the verdict was not recorded. Reporting success would
// tell the scanner a file is condemned when the row still says otherwise.
func TestMarkScanRejectedReportsARowItCouldNotTouch(t *testing.T) {
	pool := &fakePool{queryRow: func(string, ...any) pgx.Row {
		return errRow{err: pgx.ErrNoRows}
	}}
	_, err := mustFencedStore(pool).MarkScanRejected(
		context.Background(), rejectionInput())
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("error = %v, want ErrNotFound", err)
	}
}

func TestMarkScanRejectedRefusesAnIncompleteVerdict(t *testing.T) {
	for name, rejection := range map[string]service.ScanRejection{
		"no attachment": {WorkspaceID: testWorkspaceID},
		"no workspace":  {AttachmentID: testAttachmentID},
	} {
		t.Run(name, func(t *testing.T) {
			pool := &fakePool{}
			_, err := mustFencedStore(pool).MarkScanRejected(
				context.Background(), rejection)
			if !errors.Is(err, domain.ErrInvalidInput) {
				t.Fatalf("error = %v, want ErrInvalidInput", err)
			}
			if pool.lastSQL != "" {
				t.Fatal("an incomplete verdict must not reach the database")
			}
		})
	}
}

func TestMarkScanRejectedWrapsDatabaseFailures(t *testing.T) {
	dbErr := errors.New("deadlock detected")
	pool := &fakePool{queryRow: func(string, ...any) pgx.Row { return errRow{err: dbErr} }}
	_, err := mustFencedStore(pool).MarkScanRejected(
		context.Background(), rejectionInput())
	if !errors.Is(err, dbErr) {
		t.Fatalf("expected the database error to be wrapped, got %v", err)
	}
}

func TestMarkScanRejectedRefusesToRunWithoutAPool(t *testing.T) {
	_, err := storage.NewPGXAttachmentStore(nil).MarkScanRejected(
		context.Background(), rejectionInput())
	if !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("error = %v, want ErrUnavailable", err)
	}
}

// --- clean verdicts --------------------------------------------------------

func approvalInput() service.ScanApproval {
	return service.ScanApproval{AttachmentID: testAttachmentID, WorkspaceID: testWorkspaceID}
}

func lifecycleRow(status domain.Status, preview domain.PreviewStatus) []any {
	return []any{text(string(status)), text(string(preview))}
}

// The clean verdict is the only transition that makes content reachable, so the
// states it accepts are a security property rather than a detail: an approval
// must be impossible to apply to a file that was never scanned, to one the
// scanner condemned, or to one that has been removed.
func TestMarkScanCleanOnlyApprovesAScannableAttachment(t *testing.T) {
	pool := &fakePool{queryRow: func(string, ...any) pgx.Row {
		return valueRow{values: lifecycleRow(domain.StatusClean, domain.PreviewStatusPending)}
	}}

	state, err := mustFencedStore(pool).MarkScanClean(
		context.Background(), approvalInput())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state.Status != domain.StatusClean {
		t.Fatalf("status = %q, want clean", state.Status)
	}

	if !strings.Contains(pool.lastSQL, "AND status IN ('pending_scan', 'clean')") {
		t.Fatalf("unexpected accepted states:\n%s", pool.lastSQL)
	}
	// A rejection is final in this direction: no replayed or stale approval can
	// reopen a condemned file.
	if strings.Contains(pool.lastSQL, "'rejected'") {
		t.Fatalf("an approval must never apply to a rejected attachment:\n%s", pool.lastSQL)
	}
	// A removed attachment cannot be brought back into circulation, and an
	// upload that never finished has no scanned object to approve.
	if !strings.Contains(pool.lastSQL, "AND deleted_at IS NULL") {
		t.Fatalf("an approval must not reach a deleted row:\n%s", pool.lastSQL)
	}
	for _, forbidden := range []string{"'pending_upload'", "'failed'"} {
		if strings.Contains(pool.lastSQL, forbidden) {
			t.Fatalf("an approval must not apply to %s:\n%s", forbidden, pool.lastSQL)
		}
	}
	if pool.lastArgs[0] != testAttachmentID || pool.lastArgs[1] != testWorkspaceID {
		t.Fatalf("unexpected arguments: %v", pool.lastArgs)
	}
}

// The approval writes nothing about the preview: the upload already scheduled
// it, so the row becomes claimable the instant the status turns clean, with no
// second write and no window between the two facts.
func TestMarkScanCleanLeavesThePreviewScheduleAlone(t *testing.T) {
	pool := &fakePool{queryRow: func(string, ...any) pgx.Row {
		return valueRow{values: lifecycleRow(domain.StatusClean, domain.PreviewStatusPending)}
	}}
	if _, err := mustFencedStore(pool).MarkScanClean(
		context.Background(), approvalInput()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, forbidden := range []string{
		"preview_status =", "preview_next_attempt_at =", "preview_attempts",
	} {
		if strings.Contains(pool.lastSQL, forbidden) {
			t.Fatalf("an approval must not write %q:\n%s", forbidden, pool.lastSQL)
		}
	}
	if strings.Count(pool.lastSQL, "UPDATE") != 1 {
		t.Fatalf("the verdict must be a single statement:\n%s", pool.lastSQL)
	}
}

func TestMarkScanCleanReportsARowItCouldNotApprove(t *testing.T) {
	pool := &fakePool{queryRow: func(string, ...any) pgx.Row { return errRow{err: pgx.ErrNoRows} }}
	_, err := mustFencedStore(pool).MarkScanClean(
		context.Background(), approvalInput())
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("error = %v, want ErrNotFound", err)
	}
}

func TestMarkScanCleanRefusesAnIncompleteVerdict(t *testing.T) {
	for name, approval := range map[string]service.ScanApproval{
		"no attachment": {WorkspaceID: testWorkspaceID},
		"no workspace":  {AttachmentID: testAttachmentID},
	} {
		t.Run(name, func(t *testing.T) {
			pool := &fakePool{}
			_, err := mustFencedStore(pool).MarkScanClean(context.Background(), approval)
			if !errors.Is(err, domain.ErrInvalidInput) {
				t.Fatalf("error = %v, want ErrInvalidInput", err)
			}
			if pool.lastSQL != "" {
				t.Fatal("an incomplete verdict must not reach the database")
			}
		})
	}
}

func TestMarkScanCleanWrapsDatabaseFailures(t *testing.T) {
	dbErr := errors.New("connection reset")
	pool := &fakePool{queryRow: func(string, ...any) pgx.Row { return errRow{err: dbErr} }}
	_, err := mustFencedStore(pool).MarkScanClean(
		context.Background(), approvalInput())
	if !errors.Is(err, dbErr) {
		t.Fatalf("expected the database error to be wrapped, got %v", err)
	}
}

// --- removal ---------------------------------------------------------------

func removalInput() service.AttachmentRemoval {
	return service.AttachmentRemoval{AttachmentID: testAttachmentID, WorkspaceID: testWorkspaceID}
}

// Removing an attachment has to finish its preview in the same breath. Writing
// only deleted_at leaves a queued job no claim can ever select — the claim
// requires deleted_at IS NULL — and that nothing ever concludes.
func TestMarkAttachmentDeletedFinalisesAPendingPreviewInTheSameStatement(t *testing.T) {
	pool := &fakePool{queryRow: func(string, ...any) pgx.Row {
		return valueRow{values: lifecycleRow(domain.StatusClean, domain.PreviewStatusUnsupported)}
	}}

	state, err := mustFencedStore(pool).MarkAttachmentDeleted(
		context.Background(), removalInput())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state.PreviewStatus != domain.PreviewStatusUnsupported {
		t.Fatalf("preview status = %q, want unsupported", state.PreviewStatus)
	}

	if strings.Count(pool.lastSQL, "UPDATE") != 1 {
		t.Fatalf("the removal must be a single statement:\n%s", pool.lastSQL)
	}
	for _, fragment := range []string{
		"deleted_at = COALESCE(deleted_at, now())",
		"WHEN preview_status = 'pending' THEN 'unsupported'",
		"WHEN preview_status = 'pending' THEN NULL",
		"RETURNING status, preview_status",
	} {
		if !strings.Contains(pool.lastSQL, fragment) {
			t.Fatalf("the removal statement is missing %q:\n%s", fragment, pool.lastSQL)
		}
	}
	// The scan verdict is a different fact about the row and survives removal:
	// a file that was rejected and then removed is still a condemned file.
	for _, line := range strings.Split(pool.lastSQL, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "status =") ||
			strings.HasPrefix(strings.TrimSpace(line), "SET status") {
			t.Fatalf("a removal must not rewrite the scan verdict:\n%s", pool.lastSQL)
		}
	}
	if pool.lastArgs[0] != testAttachmentID || pool.lastArgs[1] != testWorkspaceID {
		t.Fatalf("unexpected arguments: %v", pool.lastArgs)
	}
}

func TestMarkAttachmentDeletedReportsARowItCouldNotRemove(t *testing.T) {
	pool := &fakePool{queryRow: func(string, ...any) pgx.Row { return errRow{err: pgx.ErrNoRows} }}
	_, err := mustFencedStore(pool).MarkAttachmentDeleted(
		context.Background(), removalInput())
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("error = %v, want ErrNotFound", err)
	}
}

func TestMarkAttachmentDeletedRefusesAnIncompleteRemoval(t *testing.T) {
	for name, removal := range map[string]service.AttachmentRemoval{
		"no attachment": {WorkspaceID: testWorkspaceID},
		"no workspace":  {AttachmentID: testAttachmentID},
	} {
		t.Run(name, func(t *testing.T) {
			pool := &fakePool{}
			_, err := mustFencedStore(pool).MarkAttachmentDeleted(
				context.Background(), removal)
			if !errors.Is(err, domain.ErrInvalidInput) {
				t.Fatalf("error = %v, want ErrInvalidInput", err)
			}
			if pool.lastSQL != "" {
				t.Fatal("an incomplete removal must not reach the database")
			}
		})
	}
}

// The fence is not optional for an invalidation: running one without it is the
// containment failure it exists to prevent, so a store without a fence refuses
// rather than proceeding.
func TestInvalidationsRefuseToRunWithoutTheFence(t *testing.T) {
	unfenced := storage.NewPGXAttachmentStore(&fakePool{})

	if _, err := unfenced.MarkScanRejected(context.Background(), rejectionInput()); !errors.Is(
		err, domain.ErrUnavailable,
	) {
		t.Fatalf("rejection error = %v, want ErrUnavailable", err)
	}
	if _, err := unfenced.MarkAttachmentDeleted(context.Background(), removalInput()); !errors.Is(
		err, domain.ErrUnavailable,
	) {
		t.Fatalf("removal error = %v, want ErrUnavailable", err)
	}
}

// Both invalidations take the fence for the attachment they are invalidating,
// and give it back.
func TestInvalidationsTakeAndReleaseTheAttachmentFence(t *testing.T) {
	pool := &fakePool{queryRow: func(string, ...any) pgx.Row {
		return valueRow{values: lifecycleRow(domain.StatusRejected, domain.PreviewStatusUnsupported)}
	}}
	store, fence := fencedStore(pool)

	if _, err := store.MarkScanRejected(context.Background(), rejectionInput()); err != nil {
		t.Fatalf("rejection: %v", err)
	}
	if _, err := store.MarkAttachmentDeleted(context.Background(), removalInput()); err != nil {
		t.Fatalf("removal: %v", err)
	}

	if len(fence.acquired) != 2 {
		t.Fatalf("fence taken %d times, want 2", len(fence.acquired))
	}
	// Both invalidations fence the *same* attachment, so they exclude each
	// other and exclude a render of it.
	if fence.acquired[0] != fence.acquired[1] {
		t.Fatalf("the two invalidations fenced different keys: %v", fence.acquired)
	}
	if fence.released != 2 {
		t.Fatalf("fence released %d times, want 2", fence.released)
	}
}

// A fence that cannot be taken stops the invalidation: it must never fall back
// to running unfenced.
func TestInvalidationsFailWhenTheFenceCannotBeTaken(t *testing.T) {
	pool := &fakePool{}
	fence := &recordingFence{err: errors.New("no connection")}
	store := storage.NewFencedAttachmentStore(pool, fence)

	if _, err := store.MarkScanRejected(context.Background(), rejectionInput()); err == nil {
		t.Fatal("a rejection must not proceed without the fence")
	}
	if pool.lastSQL != "" {
		t.Fatal("nothing may reach the database without the fence")
	}
}

func TestLifecycleOperationsRefuseToRunWithoutAPool(t *testing.T) {
	store := storage.NewFencedAttachmentStore(nil, &recordingFence{querier: &fakePool{}})
	if _, err := store.MarkScanClean(context.Background(), approvalInput()); !errors.Is(
		err, domain.ErrUnavailable,
	) {
		t.Fatalf("clean error = %v, want ErrUnavailable", err)
	}
	if _, err := store.MarkAttachmentDeleted(context.Background(), removalInput()); !errors.Is(
		err, domain.ErrUnavailable,
	) {
		t.Fatalf("removal error = %v, want ErrUnavailable", err)
	}
}
