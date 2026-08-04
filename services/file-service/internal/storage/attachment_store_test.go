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
	status, ok := pool.lastArgs[len(pool.lastArgs)-1].(string)
	if !ok || status != string(domain.StatusPendingUpload) {
		t.Fatalf("expected pending_upload, got %v", pool.lastArgs[len(pool.lastArgs)-1])
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
		DetectedMIME: "application/pdf", Size: 10, CiphertextSize: 42,
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
	if pool.lastArgs[len(pool.lastArgs)-1] != string(domain.StatusPendingUpload) {
		t.Fatalf("expected the guard to be pending_upload, got %v", pool.lastArgs[len(pool.lastArgs)-1])
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
		"chat.channel_members",
		"chat.dm_members",
		"a.deleted_at IS NULL",
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
