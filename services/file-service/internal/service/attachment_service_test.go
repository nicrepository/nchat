package service_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/nicrepository/nchat/services/file-service/internal/crypto"
	"github.com/nicrepository/nchat/services/file-service/internal/domain"
	"github.com/nicrepository/nchat/services/file-service/internal/service"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// --- upload -------------------------------------------------------------

func TestUploadToChannelStoresEncryptedContentAndPendingScanMetadata(t *testing.T) {
	f := newFixture(t)
	payload := []byte("%PDF-1.7 quarterly report body")

	view, err := f.upload(context.Background(), bytes.NewReader(payload), "report.pdf")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, parseErr := uuid.Parse(view.ID); parseErr != nil {
		t.Fatalf("the public id must be a UUID, got %q", view.ID)
	}
	if view.Filename != "report.pdf" || view.Size != int64(len(payload)) {
		t.Fatalf("unexpected view: %+v", view)
	}
	if view.Status != string(domain.StatusPendingScan) {
		t.Fatalf("a fresh upload must await the scan, got %q", view.Status)
	}
	if view.DestinationKind != string(domain.DestinationKindChannel) {
		t.Fatalf("unexpected destination kind %q", view.DestinationKind)
	}

	created, uploaded, markFailedCalls := f.store.snapshot()
	if len(created) != 1 || len(uploaded) != 1 || markFailedCalls != 0 {
		t.Fatalf("unexpected lifecycle: created=%d uploaded=%d markFailed=%d",
			len(created), len(uploaded), markFailedCalls)
	}
	if created[0].WorkspaceID != testWorkspaceID {
		t.Fatalf("the workspace must come from the destination, got %q", created[0].WorkspaceID)
	}
	if created[0].StorageObjectKey != domain.StorageObjectKey(uuid.MustParse(view.ID)) {
		t.Fatalf("unexpected storage key %q", created[0].StorageObjectKey)
	}
	if created[0].EnvelopeVersion != crypto.EnvelopeVersion {
		t.Fatalf("unexpected envelope version %d", created[0].EnvelopeVersion)
	}

	key, ciphertext := f.objects.only(t)
	if key != created[0].StorageObjectKey {
		t.Fatalf("stored under %q, expected %q", key, created[0].StorageObjectKey)
	}
	if bytes.Contains(ciphertext, payload) {
		t.Fatal("the stored object must not contain the plaintext")
	}
	if uploaded[0].CiphertextSize != int64(len(ciphertext)) {
		t.Fatalf("expected ciphertext size %d, got %d", len(ciphertext), uploaded[0].CiphertextSize)
	}
	if uploaded[0].Size != int64(len(payload)) {
		t.Fatalf("expected plaintext size %d, got %d", len(payload), uploaded[0].Size)
	}
}

func TestUploadToConversationRecordsTheDMDestination(t *testing.T) {
	f := newFixture(t)
	f.authorizer.result = service.AuthorizedDestination{
		ID: testConversation, WorkspaceID: testWorkspaceID,
		SessionExpiresAt: time.Now().Add(time.Hour),
	}

	view, err := f.uploadTo(context.Background(),
		domain.Destination{Kind: domain.DestinationKindDM, ID: testConversation},
		strings.NewReader("private notes"), "notes.txt", "text/plain")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if view.DestinationKind != string(domain.DestinationKindDM) {
		t.Fatalf("unexpected destination kind %q", view.DestinationKind)
	}
	created, _, _ := f.store.snapshot()
	if created[0].Destination.Kind != domain.DestinationKindDM ||
		created[0].Destination.ID != testConversation {
		t.Fatalf("unexpected destination %+v", created[0].Destination)
	}
}

// The client's declared type is recorded but never trusted: the served type
// comes from the real bytes.
func TestUploadDetectsTheContentTypeFromTheBytes(t *testing.T) {
	f := newFixture(t)
	view, err := f.uploadTo(context.Background(),
		domain.Destination{Kind: domain.DestinationKindChannel, ID: testChannelID},
		strings.NewReader("<html><script>alert(1)</script></html>"),
		"totally-an-image.png", "image/png")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.HasPrefix(view.ContentType, "image/") {
		t.Fatalf("the declared type must not win, got %q", view.ContentType)
	}
	created, uploaded, _ := f.store.snapshot()
	if created[0].DeclaredMIME != "image/png" {
		t.Fatalf("the declared type must be kept for auditing, got %q", created[0].DeclaredMIME)
	}
	if uploaded[0].DetectedMIME == created[0].DeclaredMIME {
		t.Fatal("the detected type must be recorded separately from the declared one")
	}
}

func TestUploadNormalisesTheFilenameBeforePersisting(t *testing.T) {
	f := newFixture(t)
	view, err := f.upload(context.Background(), strings.NewReader("data"), "../../etc/pass\x00wd")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if view.Filename != "passwd" {
		t.Fatalf("expected a normalised filename, got %q", view.Filename)
	}
	created, _, _ := f.store.snapshot()
	if strings.ContainsAny(created[0].StorageObjectKey, `\`) ||
		strings.Contains(created[0].StorageObjectKey, "passwd") {
		t.Fatalf("the filename must never influence the storage key: %q", created[0].StorageObjectKey)
	}
}

func TestUploadRejectsAnUnusableFilenameBeforeTouchingAnything(t *testing.T) {
	f := newFixture(t)
	_, err := f.upload(context.Background(), strings.NewReader("data"), "   ")
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput, got %v", err)
	}
	assertNothingPersisted(t, f)
	// Authorization now runs first — it must, because it decides whether the
	// body may be read at all — so the filename is rejected right after it and
	// still before anything is written anywhere.
	if len(f.authorizer.calls) != 1 {
		t.Fatalf("expected exactly one authorization, got %d", len(f.authorizer.calls))
	}
}

func TestUploadRejectsAnEmptyFileBeforeTouchingStorage(t *testing.T) {
	f := newFixture(t)
	_, err := f.upload(context.Background(), strings.NewReader(""), "empty.txt")
	if !errors.Is(err, domain.ErrEmptyFile) {
		t.Fatalf("expected ErrEmptyFile, got %v", err)
	}
	assertNothingPersisted(t, f)
}

func TestUploadEnforcesTheConfiguredCapOnBytesActuallyRead(t *testing.T) {
	f := newFixture(t, fixtureOptions{maxUploadBytes: 1024, scanRequired: true})

	// One byte over the cap, streamed: Content-Length is irrelevant here, the
	// limit is applied to what is read.
	_, err := f.upload(context.Background(), bytes.NewReader(make([]byte, 1025)), "big.bin")
	if !errors.Is(err, domain.ErrTooLarge) {
		t.Fatalf("expected ErrTooLarge, got %v", err)
	}
	assertCompensated(t, f)
}

func TestUploadAcceptsExactlyTheCap(t *testing.T) {
	f := newFixture(t, fixtureOptions{maxUploadBytes: 1024})
	view, err := f.upload(context.Background(), bytes.NewReader(make([]byte, 1024)), "exact.bin")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if view.Size != 1024 {
		t.Fatalf("expected 1024 bytes, got %d", view.Size)
	}
}

func TestUploadRejectsMoreThanOneFile(t *testing.T) {
	f := newFixture(t)
	// The handler's single-file reader reports this sentinel at end of part.
	source := &failingReader{data: []byte("first file body"), err: domain.ErrTooManyFiles}

	_, err := f.upload(context.Background(), source, "first.txt")
	if !errors.Is(err, domain.ErrTooManyFiles) {
		t.Fatalf("expected ErrTooManyFiles, got %v", err)
	}
	assertNothingPersisted(t, f)
}

func TestUploadTreatsATruncatedRequestAsAClientError(t *testing.T) {
	f := newFixture(t)
	// A body larger than the sniff window so the failure lands during storage.
	source := &failingReader{data: make([]byte, 4096), err: io.ErrUnexpectedEOF}

	_, err := f.upload(context.Background(), source, "truncated.bin")
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput, got %v", err)
	}
	assertCompensated(t, f)
}

func TestUploadPropagatesAuthorizationFailures(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{name: "invalid session", err: domain.ErrUnauthorized},
		{name: "invisible destination", err: domain.ErrNotFound},
		{name: "database down", err: domain.ErrDependenciesUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			f.authorizer.err = tt.err

			_, err := f.upload(context.Background(), strings.NewReader("data"), "x.txt")
			if !errors.Is(err, tt.err) {
				t.Fatalf("expected %v, got %v", tt.err, err)
			}
			assertNothingPersisted(t, f)
		})
	}
}

func TestUploadRejectsANonUUIDPrincipal(t *testing.T) {
	f := newFixture(t)
	tests := []struct{ user, session string }{
		{user: "not-a-uuid", session: testSessionID},
		{user: testUserID, session: "not-a-uuid"},
	}
	for _, tt := range tests {
		// A malformed principal is refused during authorization, which is now the
		// step that runs before the body is ever touched.
		_, err := f.service.AuthorizeUpload(context.Background(), service.AuthorizeUploadInput{
			Destination: domain.Destination{Kind: domain.DestinationKindChannel, ID: testChannelID},
			UserID:      tt.user, SessionID: tt.session,
		})
		if !errors.Is(err, domain.ErrUnauthorized) {
			t.Fatalf("expected ErrUnauthorized, got %v", err)
		}
	}
	assertNothingPersisted(t, f)
}

func TestUploadFailsWhenMetadataCannotBeCreated(t *testing.T) {
	f := newFixture(t)
	f.store.createErr = errors.New("database unavailable")

	_, err := f.upload(context.Background(), strings.NewReader("data"), "x.txt")
	if err == nil {
		t.Fatal("expected an error")
	}
	if f.objects.count() != 0 {
		t.Fatal("no object may be written when the metadata row does not exist")
	}
}

// A storage failure must compensate and must never report success. Delete
// succeeding is what allows the row to advance to failed.
func TestUploadCompensatesWhenStorageFails(t *testing.T) {
	f := newFixture(t)
	f.objects.putErr = domain.ErrUnavailable

	_, err := f.upload(context.Background(), strings.NewReader("data"), "x.txt")
	if !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable, got %v", err)
	}
	assertCompensated(t, f)
	if f.orphans.value() != 0 {
		t.Fatal("a successful cleanup must not count as an orphan")
	}
}

// The row is only advanced after the object is durable; if that update fails
// the object is removed again, so nothing is left downloadable.
func TestUploadCompensatesWhenFinalizingMetadataFails(t *testing.T) {
	f := newFixture(t)
	f.store.uploadErr = errors.New("update failed")

	_, err := f.upload(context.Background(), strings.NewReader("data"), "x.txt")
	if err == nil {
		t.Fatal("expected an error")
	}
	if f.objects.count() != 0 {
		t.Fatal("the stored object must be removed when the row cannot be finalised")
	}
	id := f.store.onlyRowID(t)
	if status := f.store.statusOf(t, id); status != domain.StatusFailed {
		t.Fatalf("expected the row to be failed, got %q", status)
	}
	if f.orphans.value() != 0 {
		t.Fatal("the object was removed, so no orphan exists")
	}
}

// The regression this suite exists for.
//
// The object is in storage, a later step failed, and cleanup cannot remove it.
// Advancing the row to failed here would drop the only stored pointer to a live
// object out of idx_attachments_pending, so the row must stay pending_upload
// and MarkFailed must not be called at all.
func TestFailedCleanupKeepsTheRowRecoverableAndNeverMarksItFailed(t *testing.T) {
	f := newFixture(t)
	finalizeErr := errors.New("update failed")
	deleteErr := errors.New("storage still down")
	f.store.uploadErr = finalizeErr
	f.objects.deleteErr = deleteErr

	view, err := f.upload(context.Background(), strings.NewReader("data"), "x.txt")

	if err == nil {
		t.Fatal("a failed upload must never report success")
	}
	if view.ID != "" || view.Status != "" {
		t.Fatalf("no view may be returned on failure, got %+v", view)
	}

	// Cleanup was attempted exactly once, and the row was left alone.
	if deletes := f.objects.deletedKeys(); len(deletes) != 1 {
		t.Fatalf("expected exactly one delete attempt, got %d", len(deletes))
	}
	if calls := f.store.markFailedCallCount(); calls != 0 {
		t.Fatalf("MarkFailed must not run after a failed delete, got %d calls", calls)
	}

	// The persisted state, not the mock, is what has to be right.
	id := f.store.onlyRowID(t)
	status := f.store.statusOf(t, id)
	if status != domain.StatusPendingUpload {
		t.Fatalf("expected the row to stay pending_upload, got %q", status)
	}
	if status == domain.StatusPendingScan {
		t.Fatal("a failed upload must never reach pending_scan")
	}
	if recoverable := f.store.recoverable(); len(recoverable) != 1 || recoverable[0] != id {
		t.Fatalf("the row must stay in the pending index, got %v", recoverable)
	}

	// The object really is still there: that is what makes it an orphan.
	if f.objects.count() != 1 {
		t.Fatalf("expected the object to remain in storage, got %d", f.objects.count())
	}
	if f.orphans.value() != 1 {
		t.Fatalf("expected exactly one orphan to be counted, got %d", f.orphans.value())
	}

	// Both failures survive in the returned error.
	if !errors.Is(err, finalizeErr) {
		t.Fatalf("the original cause must be preserved, got %v", err)
	}
	if !errors.Is(err, deleteErr) {
		t.Fatalf("the cleanup failure must be preserved, got %v", err)
	}
}

// Same shape, but the failure that triggers compensation is the storage write
// itself rather than the finalising update.
func TestFailedCleanupAfterAFailedWriteKeepsTheRowRecoverable(t *testing.T) {
	f := newFixture(t)
	putErr := domain.ErrUnavailable
	deleteErr := errors.New("storage still down")
	f.objects.putErr = putErr
	f.objects.deleteErr = deleteErr

	_, err := f.upload(context.Background(), strings.NewReader("data"), "x.txt")
	if err == nil {
		t.Fatal("expected an error")
	}
	if calls := f.store.markFailedCallCount(); calls != 0 {
		t.Fatalf("MarkFailed must not run after a failed delete, got %d calls", calls)
	}
	id := f.store.onlyRowID(t)
	if status := f.store.statusOf(t, id); status != domain.StatusPendingUpload {
		t.Fatalf("expected pending_upload, got %q", status)
	}
	if recoverable := f.store.recoverable(); len(recoverable) != 1 {
		t.Fatalf("the row must stay in the pending index, got %v", recoverable)
	}
	if f.orphans.value() != 1 {
		t.Fatalf("expected one orphan, got %d", f.orphans.value())
	}
	if !errors.Is(err, putErr) || !errors.Is(err, deleteErr) {
		t.Fatalf("both causes must be preserved, got %v", err)
	}
}

// Delete worked but the row could not be marked: nothing is left in storage,
// so this is not an orphan. The row stays pending_upload, which is recoverable.
func TestSuccessfulCleanupWithAFailedMarkLeavesARecoverableRow(t *testing.T) {
	f := newFixture(t)
	putErr := domain.ErrUnavailable
	markErr := errors.New("database still down")
	f.objects.putErr = putErr
	f.store.markFailErr = markErr

	_, err := f.upload(context.Background(), strings.NewReader("data"), "x.txt")
	if err == nil {
		t.Fatal("expected an error")
	}
	if deletes := f.objects.deletedKeys(); len(deletes) != 1 {
		t.Fatalf("expected one delete, got %d", len(deletes))
	}
	if f.objects.count() != 0 {
		t.Fatal("the object must be gone")
	}
	if calls := f.store.markFailedCallCount(); calls != 1 {
		t.Fatalf("MarkFailed must be attempted once after a successful delete, got %d", calls)
	}
	id := f.store.onlyRowID(t)
	if status := f.store.statusOf(t, id); status != domain.StatusPendingUpload {
		t.Fatalf("expected the row to stay recoverable, got %q", status)
	}
	if recoverable := f.store.recoverable(); len(recoverable) != 1 {
		t.Fatalf("the row must stay in the pending index, got %v", recoverable)
	}
	if f.orphans.value() != 0 {
		t.Fatal("no object remains, so no orphan may be counted")
	}
	if !errors.Is(err, putErr) || !errors.Is(err, markErr) {
		t.Fatalf("both causes must be preserved, got %v", err)
	}
}

// A failure before the row exists must not touch storage at all: cleaning up an
// object that was never written could only fail spuriously. The flow makes this
// structural — the row is inserted only once the write is about to start — so
// every pre-row failure is asserted the same way.
func TestFailuresBeforeTheRowExistsNeverTouchStorage(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*fixture)
		body  string
		file  string
	}{
		{
			name:  "unauthorized destination",
			setup: func(f *fixture) { f.authorizer.err = domain.ErrNotFound },
			body:  "data", file: "x.txt",
		},
		{
			name:  "unusable filename",
			setup: func(*fixture) {},
			body:  "data", file: "   ",
		},
		{
			name:  "empty file",
			setup: func(*fixture) {},
			body:  "", file: "x.txt",
		},
		{
			name:  "metadata insert fails",
			setup: func(f *fixture) { f.store.createErr = errors.New("insert failed") },
			body:  "data", file: "x.txt",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			tt.setup(f)

			if _, err := f.upload(context.Background(), strings.NewReader(tt.body), tt.file); err == nil {
				t.Fatal("expected an error")
			}
			if deletes := f.objects.deletedKeys(); len(deletes) != 0 {
				t.Fatalf("no cleanup may run before the row exists, got %d deletes", len(deletes))
			}
			if calls := f.store.markFailedCallCount(); calls != 0 {
				t.Fatalf("no row exists to mark failed, got %d calls", calls)
			}
			if f.objects.count() != 0 {
				t.Fatal("no object may be stored")
			}
			if f.orphans.value() != 0 {
				t.Fatal("no orphan may be counted")
			}
		})
	}
}

// Cleanup runs on a detached context so a client that hung up still gets its
// partial object removed.
func TestUploadCompensatesEvenWhenTheRequestContextIsCancelled(t *testing.T) {
	f := newFixture(t)
	f.objects.putErr = domain.ErrUnavailable

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := f.upload(ctx, strings.NewReader("data"), "x.txt"); err == nil {
		t.Fatal("expected an error")
	}
	if len(f.objects.deletedKeys()) != 1 {
		t.Fatal("cleanup must run despite the cancelled request context")
	}
}

func TestUploadFinalisesAsCleanOnlyWhenScanningIsNotRequired(t *testing.T) {
	f := newFixture(t, fixtureOptions{scanRequired: false})
	view, err := f.upload(context.Background(), strings.NewReader("data"), "x.txt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if view.Status != string(domain.StatusClean) {
		t.Fatalf("expected clean, got %q", view.Status)
	}
}

func TestUploadWithoutDependenciesIsUnavailable(t *testing.T) {
	var nilService *service.AttachmentService
	if _, err := nilService.Upload(context.Background(), service.UploadInput{}); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable, got %v", err)
	}
	if nilService.Ready() {
		t.Fatal("a nil service must never report ready")
	}

	partial := service.NewAttachmentService(nil, nil, nil, nil, 0, true, nil, discardLogger())
	if partial.Ready() {
		t.Fatal("an unwired service must never report ready")
	}
	if _, err := partial.Upload(context.Background(), service.UploadInput{}); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable, got %v", err)
	}
	if _, err := partial.Metadata(context.Background(), service.AttachmentAuthInput{}); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable, got %v", err)
	}
	if _, err := partial.Download(context.Background(), service.AttachmentAuthInput{}); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable, got %v", err)
	}
}

func TestNewAttachmentServiceToleratesANilLogger(t *testing.T) {
	svc := service.NewAttachmentService(
		&fakeAuthorizer{}, &fakeStore{}, newFakeObjects(), testKEK(t),
		domain.DefaultMaxUploadBytes, true, nil, nil,
	)
	if !svc.Ready() {
		t.Fatal("expected a fully wired service to be ready")
	}
}

// --- download and metadata ---------------------------------------------

// storedAttachment uploads real content and returns the row a reader would see.
func storedAttachment(t *testing.T, f *fixture, payload []byte, status domain.Status) service.StoredAttachment {
	t.Helper()
	view, err := f.upload(context.Background(), bytes.NewReader(payload), "report.pdf")
	if err != nil {
		t.Fatalf("seed upload: %v", err)
	}
	created, uploaded, _ := f.store.snapshot()
	return service.StoredAttachment{
		ID:               view.ID,
		WorkspaceID:      testWorkspaceID,
		Kind:             domain.DestinationKindChannel,
		Status:           status,
		Filename:         view.Filename,
		DeclaredMIME:     created[0].DeclaredMIME,
		DetectedMIME:     uploaded[0].DetectedMIME,
		Size:             uploaded[0].Size,
		StorageObjectKey: created[0].StorageObjectKey,
		EnvelopeVersion:  created[0].EnvelopeVersion,
		WrappedDEK:       created[0].WrappedDEK,
		CreatedAt:        time.Now().UTC(),
		SessionExpiresAt: time.Now().Add(time.Hour),
	}
}

func downloadInput(id string) service.AttachmentAuthInput {
	return service.AttachmentAuthInput{AttachmentID: id, UserID: testUserID, SessionID: testSessionID}
}

func TestDownloadReturnsTheOriginalPlaintext(t *testing.T) {
	f := newFixture(t)
	payload := bytes.Repeat([]byte("attachment payload "), 20000) // spans several chunks
	f.store.authorized = storedAttachment(t, f, payload, domain.StatusClean)

	download, err := f.service.Download(context.Background(), downloadInput(f.store.authorized.ID))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer func() { _ = download.Content.Close() }()

	got, err := io.ReadAll(download.Content)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatal("download returned different bytes than were uploaded")
	}
	if download.Size != int64(len(payload)) {
		t.Fatalf("expected size %d, got %d", len(payload), download.Size)
	}
	if download.Filename != "report.pdf" {
		t.Fatalf("unexpected filename %q", download.Filename)
	}
}

func TestDownloadRefusesEveryStateButClean(t *testing.T) {
	for _, status := range []domain.Status{
		domain.StatusPendingUpload, domain.StatusPendingScan,
		domain.StatusRejected, domain.StatusFailed, domain.StatusDeleted,
	} {
		t.Run(string(status), func(t *testing.T) {
			f := newFixture(t)
			f.store.authorized = storedAttachment(t, f, []byte("payload"), status)

			_, err := f.service.Download(context.Background(), downloadInput(f.store.authorized.ID))
			if !errors.Is(err, domain.ErrNotDownloadable) {
				t.Fatalf("expected ErrNotDownloadable, got %v", err)
			}
		})
	}
}

func TestDownloadPropagatesAuthorizationFailures(t *testing.T) {
	for _, want := range []error{domain.ErrUnauthorized, domain.ErrNotFound, domain.ErrUnavailable} {
		f := newFixture(t)
		f.store.authorizedErr = want
		if _, err := f.service.Download(context.Background(),
			downloadInput(uuid.NewString())); !errors.Is(err, want) {
			t.Fatalf("expected %v, got %v", want, err)
		}
	}
}

func TestDownloadTreatsAMalformedIDAsNotFound(t *testing.T) {
	f := newFixture(t)
	if _, err := f.service.Download(context.Background(),
		downloadInput("not-a-uuid")); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestDownloadRejectsANonUUIDPrincipal(t *testing.T) {
	f := newFixture(t)
	_, err := f.service.Download(context.Background(), service.AttachmentAuthInput{
		AttachmentID: uuid.NewString(), UserID: "nope", SessionID: testSessionID,
	})
	if !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("expected ErrUnauthorized, got %v", err)
	}
}

// Metadata says the object exists; storage disagrees. That is an operational
// inconsistency, never a client-visible 404 about the storage layer.
func TestDownloadReportsAMissingObjectAsUnavailable(t *testing.T) {
	f := newFixture(t)
	f.store.authorized = storedAttachment(t, f, []byte("payload"), domain.StatusClean)
	f.objects.objects = map[string][]byte{}

	_, err := f.service.Download(context.Background(), downloadInput(f.store.authorized.ID))
	if !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable, got %v", err)
	}
	if errors.Is(err, domain.ErrNotFound) {
		t.Fatal("a storage inconsistency must not be reported as a missing attachment")
	}
}

func TestDownloadPropagatesStorageFailures(t *testing.T) {
	f := newFixture(t)
	f.store.authorized = storedAttachment(t, f, []byte("payload"), domain.StatusClean)
	f.objects.openErr = domain.ErrUnavailable

	if _, err := f.service.Download(context.Background(),
		downloadInput(f.store.authorized.ID)); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable, got %v", err)
	}
}

func TestDownloadFailsOnACorruptedObject(t *testing.T) {
	f := newFixture(t)
	f.store.authorized = storedAttachment(t, f, bytes.Repeat([]byte("x"), 5000), domain.StatusClean)
	key, ciphertext := f.objects.only(t)

	corrupted := append([]byte(nil), ciphertext...)
	corrupted[len(corrupted)/2] ^= 0xff
	f.objects.replace(key, corrupted)

	download, err := f.service.Download(context.Background(), downloadInput(f.store.authorized.ID))
	if err != nil {
		t.Fatalf("unexpected error opening the stream: %v", err)
	}
	defer func() { _ = download.Content.Close() }()

	if _, err := io.ReadAll(download.Content); !errors.Is(err, crypto.ErrCiphertext) {
		t.Fatalf("expected ErrCiphertext while streaming, got %v", err)
	}
}

func TestDownloadFailsOnATruncatedObject(t *testing.T) {
	f := newFixture(t)
	f.store.authorized = storedAttachment(t, f, bytes.Repeat([]byte("x"), 5000), domain.StatusClean)
	key, ciphertext := f.objects.only(t)
	f.objects.replace(key, ciphertext[:len(ciphertext)-32])

	download, err := f.service.Download(context.Background(), downloadInput(f.store.authorized.ID))
	if err != nil {
		t.Fatalf("unexpected error opening the stream: %v", err)
	}
	defer func() { _ = download.Content.Close() }()

	if _, err := io.ReadAll(download.Content); !errors.Is(err, crypto.ErrCiphertext) {
		t.Fatalf("expected ErrCiphertext while streaming, got %v", err)
	}
}

func TestDownloadRejectsAnUnwrappableDataKey(t *testing.T) {
	f := newFixture(t)
	f.store.authorized = storedAttachment(t, f, []byte("payload"), domain.StatusClean)
	f.store.authorized.WrappedDEK = []byte("garbage that is definitely not a wrapped key")

	if _, err := f.service.Download(context.Background(),
		downloadInput(f.store.authorized.ID)); !errors.Is(err, crypto.ErrCiphertext) {
		t.Fatalf("expected ErrCiphertext, got %v", err)
	}
}

func TestDownloadRejectsAnUnknownEnvelopeVersion(t *testing.T) {
	f := newFixture(t)
	f.store.authorized = storedAttachment(t, f, []byte("payload"), domain.StatusClean)
	f.store.authorized.EnvelopeVersion = 99

	_, err := f.service.Download(context.Background(), downloadInput(f.store.authorized.ID))
	if err == nil {
		t.Fatal("expected an error for an unsupported envelope version")
	}
	if errors.Is(err, domain.ErrNotFound) || errors.Is(err, domain.ErrInvalidInput) {
		t.Fatal("an unsupported envelope must not be reported as a client error")
	}
}

func TestDownloadFallsBackToAnInertContentType(t *testing.T) {
	f := newFixture(t)
	f.store.authorized = storedAttachment(t, f, []byte("payload"), domain.StatusClean)
	f.store.authorized.DetectedMIME = ""

	download, err := f.service.Download(context.Background(), downloadInput(f.store.authorized.ID))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer func() { _ = download.Content.Close() }()
	if download.ContentType != domain.DefaultContentType {
		t.Fatalf("expected %q, got %q", domain.DefaultContentType, download.ContentType)
	}
}

func TestMetadataReturnsTheClientProjection(t *testing.T) {
	f := newFixture(t)
	f.store.authorized = storedAttachment(t, f, []byte("payload"), domain.StatusPendingScan)

	view, err := f.service.Metadata(context.Background(), downloadInput(f.store.authorized.ID))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if view.ID != f.store.authorized.ID || view.Status != string(domain.StatusPendingScan) {
		t.Fatalf("unexpected view: %+v", view)
	}
	if view.Filename != "report.pdf" {
		t.Fatalf("unexpected filename %q", view.Filename)
	}
}

func TestMetadataPropagatesAuthorizationFailures(t *testing.T) {
	f := newFixture(t)
	f.store.authorizedErr = domain.ErrNotFound
	if _, err := f.service.Metadata(context.Background(),
		downloadInput(uuid.NewString())); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestMetadataFallsBackToAnInertContentType(t *testing.T) {
	f := newFixture(t)
	f.store.authorized = storedAttachment(t, f, []byte("payload"), domain.StatusPendingScan)
	f.store.authorized.DetectedMIME = ""

	view, err := f.service.Metadata(context.Background(), downloadInput(f.store.authorized.ID))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if view.ContentType != domain.DefaultContentType {
		t.Fatalf("expected %q, got %q", domain.DefaultContentType, view.ContentType)
	}
}

// --- assertions ---------------------------------------------------------

func assertNothingPersisted(t *testing.T, f *fixture) {
	t.Helper()
	created, uploaded, _ := f.store.snapshot()
	if len(created) != 0 || len(uploaded) != 0 {
		t.Fatalf("expected no metadata, got created=%d uploaded=%d", len(created), len(uploaded))
	}
	if f.objects.count() != 0 {
		t.Fatal("expected no stored object")
	}
	if len(f.objects.deletedKeys()) != 0 {
		t.Fatal("no cleanup may run when nothing was persisted")
	}
}

// assertCompensated pins the invariant a failed upload must satisfy: nothing
// is left in storage, the row is terminal, and it never became downloadable.
func assertCompensated(t *testing.T, f *fixture) {
	t.Helper()
	if f.objects.count() != 0 {
		t.Fatal("a failed upload must leave no object behind")
	}
	_, uploaded, markFailedCalls := f.store.snapshot()
	if len(uploaded) != 0 {
		t.Fatal("a failed upload must never finalise its row")
	}
	if markFailedCalls != 1 {
		t.Fatalf("expected the row to be marked failed once, got %d", markFailedCalls)
	}
	if status := f.store.statusOf(t, f.store.onlyRowID(t)); status != domain.StatusFailed {
		t.Fatalf("expected the row to be failed, got %q", status)
	}
	if len(f.objects.deletedKeys()) != 1 {
		t.Fatalf("expected one cleanup delete, got %d", len(f.objects.deletedKeys()))
	}
}
