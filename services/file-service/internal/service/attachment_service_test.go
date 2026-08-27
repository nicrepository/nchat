package service_test

import (
	"bytes"
	"context"
	"encoding/json"
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

func TestMessageDraftUploadGetsAServerControlledExpiry(t *testing.T) {
	f := newFixture(t)
	target, err := f.service.AuthorizeUpload(context.Background(), service.AuthorizeUploadInput{
		Destination: domain.Destination{Kind: domain.DestinationKindChannel, ID: testChannelID},
		UserID:      testUserID, SessionID: testSessionID,
	})
	if err != nil {
		t.Fatalf("authorize: %v", err)
	}
	before := time.Now().UTC().Add(23 * time.Hour)
	_, err = f.service.Upload(context.Background(), service.UploadInput{
		Target: target, Filename: "draft.txt", DeclaredMIME: "text/plain",
		Purpose: service.UploadPurposeMessageDraft, Content: strings.NewReader("draft body"),
	})
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	created, _, _ := f.store.snapshot()
	if created[0].DraftExpiresAt == nil || created[0].DraftExpiresAt.Before(before) ||
		created[0].DraftExpiresAt.After(time.Now().UTC().Add(25*time.Hour)) {
		t.Fatalf("unexpected server draft expiry: %v", created[0].DraftExpiresAt)
	}
}

// --- voice messages (issue #670) -----------------------------------------

func TestVoiceMessageUploadTagsAudioKindAndCarriesDeclaredDuration(t *testing.T) {
	f := newFixture(t)
	target, err := f.service.AuthorizeUpload(context.Background(), service.AuthorizeUploadInput{
		Destination: domain.Destination{Kind: domain.DestinationKindChannel, ID: testChannelID},
		UserID:      testUserID, SessionID: testSessionID,
	})
	if err != nil {
		t.Fatalf("authorize: %v", err)
	}
	view, err := f.service.Upload(context.Background(), service.UploadInput{
		Target: target, Filename: "voice-message.ogg", DeclaredMIME: "audio/ogg",
		Purpose: service.UploadPurposeVoiceMessage, DurationMs: "4200",
		Content: strings.NewReader("OggS\x00" + strings.Repeat("x", 40)),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if view.AudioKind != string(domain.AudioKindVoice) {
		t.Fatalf("expected audio kind voice, got %q", view.AudioKind)
	}
	if view.DurationMs != 4200 {
		t.Fatalf("expected declared duration 4200, got %d", view.DurationMs)
	}
	created, _, _ := f.store.snapshot()
	if created[0].AudioKind != domain.AudioKindVoice || created[0].DeclaredDurationMs != 4200 {
		t.Fatalf("unexpected persisted voice fields: %+v", created[0])
	}
	if created[0].DraftExpiresAt == nil {
		t.Fatal("a voice message is a draft until a message binds it")
	}
}

// A garbage or absent duration must never fail the upload: it is a display
// hint, not a requirement.
func TestVoiceMessageUploadToleratesAMissingOrGarbageDuration(t *testing.T) {
	f := newFixture(t)
	target, err := f.service.AuthorizeUpload(context.Background(), service.AuthorizeUploadInput{
		Destination: domain.Destination{Kind: domain.DestinationKindChannel, ID: testChannelID},
		UserID:      testUserID, SessionID: testSessionID,
	})
	if err != nil {
		t.Fatalf("authorize: %v", err)
	}
	view, err := f.service.Upload(context.Background(), service.UploadInput{
		Target: target, Filename: "voice-message.ogg", DeclaredMIME: "audio/ogg",
		Purpose: service.UploadPurposeVoiceMessage, DurationMs: "not-a-number",
		Content: strings.NewReader("OggS\x00" + strings.Repeat("x", 40)),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if view.DurationMs != 0 {
		t.Fatalf("expected no declared duration, got %d", view.DurationMs)
	}
}

// A request tagged voice_message whose real bytes cannot plausibly be a
// browser recording is refused before anything is persisted — it must never
// become a voice bubble with nothing playable inside.
func TestVoiceMessageUploadRejectsContentThatCannotBeARecording(t *testing.T) {
	f := newFixture(t)
	target, err := f.service.AuthorizeUpload(context.Background(), service.AuthorizeUploadInput{
		Destination: domain.Destination{Kind: domain.DestinationKindChannel, ID: testChannelID},
		UserID:      testUserID, SessionID: testSessionID,
	})
	if err != nil {
		t.Fatalf("authorize: %v", err)
	}
	_, err = f.service.Upload(context.Background(), service.UploadInput{
		Target: target, Filename: "not-audio.pdf", DeclaredMIME: "application/pdf",
		Purpose: service.UploadPurposeVoiceMessage,
		Content: strings.NewReader("%PDF-1.7 not actually a recording"),
	})
	if !errors.Is(err, domain.ErrUnsupportedMedia) {
		t.Fatalf("expected ErrUnsupportedMedia, got %v", err)
	}
	assertNothingPersisted(t, f)
}

// The client's declared type is recorded but never trusted: the served type
// comes from the real bytes.
func TestUploadRejectsActiveContentDisguisedAsAnImage(t *testing.T) {
	f := newFixture(t)
	_, err := f.uploadTo(context.Background(),
		domain.Destination{Kind: domain.DestinationKindChannel, ID: testChannelID},
		strings.NewReader("<html><script>alert(1)</script></html>"),
		"totally-an-image.png", "image/png")
	if !errors.Is(err, domain.ErrUnsupportedMedia) {
		t.Fatalf("active content must be rejected, got %v", err)
	}
	assertNothingPersisted(t, f)
}

func TestUploadRejectsActiveMarkupBeyondTheDetectionWindowAndCleansThePartialObject(t *testing.T) {
	f := newFixture(t)
	payload := strings.Repeat("safe text ", 80) + "<script>alert(1)</script>"
	_, err := f.uploadTo(context.Background(),
		domain.Destination{Kind: domain.DestinationKindChannel, ID: testChannelID},
		strings.NewReader(payload), "notes.txt", "text/plain")
	if !errors.Is(err, domain.ErrUnsupportedMedia) {
		t.Fatalf("active markup must be rejected across the full stream, got %v", err)
	}
	assertCompensated(t, f)
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
	_, err := f.upload(context.Background(), bytes.NewReader(bytes.Repeat([]byte("x"), 1025)), "big.txt")
	if !errors.Is(err, domain.ErrTooLarge) {
		t.Fatalf("expected ErrTooLarge, got %v", err)
	}
	assertCompensated(t, f)
}

func TestUploadAcceptsExactlyTheCap(t *testing.T) {
	f := newFixture(t, fixtureOptions{maxUploadBytes: 1024})
	view, err := f.upload(context.Background(), bytes.NewReader(bytes.Repeat([]byte("x"), 1024)), "exact.txt")
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
	source := &failingReader{data: bytes.Repeat([]byte("x"), 4096), err: io.ErrUnexpectedEOF}

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
		&fakeAuthorizer{}, &fakeStore{}, newFakeObjects(), testKeyring(t),
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
		// The key material comes from the finalising update, not from the
		// pending insert: it does not exist until the upload has finished.
		WrappedDEK:       uploaded[0].WrappedDEK,
		KEKKeyID:         uploaded[0].KEKKeyID,
		KeyWrapVersion:   created[0].KeyWrapVersion,
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

// --- key identity and rotation -----------------------------------------

// Every row records which key-encryption key sealed its data key. Without it a
// rotation could not tell the two generations of rows apart, and a download
// would have to guess.
func TestUploadPersistsTheActiveKeyID(t *testing.T) {
	f := newFixture(t)
	if _, err := f.upload(context.Background(), bytes.NewReader([]byte("payload")), "report.pdf"); err != nil {
		t.Fatalf("upload: %v", err)
	}
	created, uploaded, _ := f.store.snapshot()
	// The pending row carries no key material at all — it cannot, because the
	// binding authenticates a length nothing knows yet.
	if len(created) != 1 || len(uploaded) != 1 {
		t.Fatalf("unexpected lifecycle: created=%d uploaded=%d", len(created), len(uploaded))
	}
	if uploaded[0].KEKKeyID != testKeyID {
		t.Fatalf("expected the active key id %q, got %q", testKeyID, uploaded[0].KEKKeyID)
	}
	// The wrap version is fixed on the pending row, not at finalisation: it is
	// the schema fence, and a fence that engaged only at the end would let a
	// writer running the previous build create the row first.
	if created[0].KeyWrapVersion != crypto.KeyWrapVersion {
		t.Fatalf("expected key wrap version %d on the pending row, got %d",
			crypto.KeyWrapVersion, created[0].KeyWrapVersion)
	}
	if len(uploaded[0].WrappedDEK) == 0 {
		t.Fatal("finalisation must persist the wrapped data key")
	}
}

// A key id this deployment does not have is refused outright: nothing is tried,
// and the object is not served under any other key.
func TestDownloadRejectsAnUnknownKeyID(t *testing.T) {
	f := newFixture(t)
	f.store.authorized = storedAttachment(t, f, []byte("payload"), domain.StatusClean)
	for _, keyID := range []string{"", "kek-never-configured"} {
		f.store.authorized.KEKKeyID = keyID

		_, err := f.service.Download(context.Background(), downloadInput(f.store.authorized.ID))
		if !errors.Is(err, crypto.ErrUnknownKey) {
			t.Fatalf("expected ErrUnknownKey for %q, got %v", keyID, err)
		}
		// It must not read like a client mistake or a missing attachment.
		if errors.Is(err, domain.ErrNotFound) || errors.Is(err, domain.ErrInvalidInput) {
			t.Fatalf("a missing key must not be reported as a client error: %v", err)
		}
	}
}

// A row moved to another workspace — by a compromised database, a bad restore
// or a buggy admin query — must stop being readable, because the workspace is
// bound into the wrapped data key.
func TestDownloadRejectsARowMovedToAnotherWorkspace(t *testing.T) {
	f := newFixture(t)
	f.store.authorized = storedAttachment(t, f, []byte("payload"), domain.StatusClean)
	f.store.authorized.WorkspaceID = uuid.NewString()

	if _, err := f.service.Download(context.Background(),
		downloadInput(f.store.authorized.ID)); !errors.Is(err, crypto.ErrCiphertext) {
		t.Fatalf("expected ErrCiphertext for a relocated row, got %v", err)
	}
}

// The wrapped data key of one attachment must not open another's, even though
// both were sealed under the same key-encryption key.
func TestDownloadRejectsAWrappedKeyFromAnotherAttachment(t *testing.T) {
	f := newFixture(t)
	first := storedAttachment(t, f, []byte("first payload"), domain.StatusClean)
	f.store.created = nil
	f.store.uploaded = nil
	second := storedAttachment(t, f, []byte("second payload"), domain.StatusClean)

	first.WrappedDEK = second.WrappedDEK
	f.store.authorized = first
	if _, err := f.service.Download(context.Background(),
		downloadInput(first.ID)); !errors.Is(err, crypto.ErrCiphertext) {
		t.Fatalf("expected ErrCiphertext for a foreign wrapped key, got %v", err)
	}
}

// Nothing that identifies or protects the key material may reach the client
// projection, on either the metadata or the listing path.
func TestPublicProjectionCarriesNoKeyMaterial(t *testing.T) {
	f := newFixture(t)
	f.store.authorized = storedAttachment(t, f, []byte("payload"), domain.StatusPendingScan)

	view, err := f.service.Metadata(context.Background(), downloadInput(f.store.authorized.ID))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	encoded, err := json.Marshal(view)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, forbidden := range []string{
		testKeyID, f.store.authorized.StorageObjectKey, "dek", "kek", "wrapped", "envelope", "nonce",
	} {
		if strings.Contains(strings.ToLower(string(encoded)), strings.ToLower(forbidden)) {
			t.Fatalf("the client projection must not mention %q: %s", forbidden, encoded)
		}
	}
}

// --- authenticated plaintext size ---------------------------------------

// The attack this closes: an attacker with write access to PostgreSQL lowers
// size_bytes, the handler publishes that smaller Content-Length, and the client
// accepts a prefix as the whole file. The size is part of the wrapped key's
// binding, so the unwrap fails and no stream is ever opened.
func TestDownloadRejectsATamperedSize(t *testing.T) {
	payload := bytes.Repeat([]byte("attachment payload "), 5000)
	real := int64(len(payload))

	for name, size := range map[string]int64{
		"halved":        real / 2,
		"one byte less": real - 1,
		"one byte more": real + 1,
		"zero":          0,
		"inflated":      real * 3,
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.store.authorized = storedAttachment(t, f, payload, domain.StatusClean)
			f.store.authorized.Size = size

			_, err := f.service.Download(context.Background(), downloadInput(f.store.authorized.ID))
			if !errors.Is(err, crypto.ErrCiphertext) {
				t.Fatalf("expected ErrCiphertext for size %d, got %v", size, err)
			}
			// Nothing may be read from storage: the failure happens on metadata,
			// before the object is touched and long before any header is written.
			if opened := f.objects.openCount(); opened != 0 {
				t.Fatalf("the object was opened %d times despite a failed unwrap", opened)
			}
		})
	}
}

// The honest size still works, and the bytes still round trip. Without this the
// test above could pass on a service that simply refused every download.
func TestDownloadAcceptsTheAuthenticatedSize(t *testing.T) {
	f := newFixture(t)
	payload := bytes.Repeat([]byte("attachment payload "), 5000)
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
}

// The persisted wrap version selects the binding; an unknown one is refused
// rather than tried against the format this build implements.
func TestDownloadRejectsAnUnknownKeyWrapVersion(t *testing.T) {
	f := newFixture(t)
	f.store.authorized = storedAttachment(t, f, []byte("payload"), domain.StatusClean)
	for _, version := range []int{0, crypto.KeyWrapVersion - 1, crypto.KeyWrapVersion + 1, 99} {
		f.store.authorized.KeyWrapVersion = version

		_, err := f.service.Download(context.Background(), downloadInput(f.store.authorized.ID))
		if !errors.Is(err, crypto.ErrUnsupportedVersion) {
			t.Fatalf("expected ErrUnsupportedVersion for %d, got %v", version, err)
		}
		if errors.Is(err, domain.ErrNotFound) || errors.Is(err, domain.ErrInvalidInput) {
			t.Fatalf("an unsupported version must not read as a client error: %v", err)
		}
	}
}

// --- finalisation ordering and compensation -----------------------------

// The pending row exists while the object is being written, and it carries no
// key material at all. A row that cannot be opened is the only safe
// intermediate state.
func TestPendingRowCarriesNoKeyMaterialUntilFinalisation(t *testing.T) {
	f := newFixture(t)
	// The finalising update fails, so the row is observed exactly as it was
	// written by CreatePending.
	f.store.uploadErr = errors.New("update failed")

	if _, err := f.upload(context.Background(),
		bytes.NewReader([]byte("payload")), "report.pdf"); err == nil {
		t.Fatal("expected the upload to fail")
	}
	created, uploaded, _ := f.store.snapshot()
	if len(created) != 1 || len(uploaded) != 0 {
		t.Fatalf("unexpected lifecycle: created=%d uploaded=%d", len(created), len(uploaded))
	}
	// NewAttachment carries no key material — the pending insert cannot, because
	// the binding authenticates a length nothing knows yet — but it does carry
	// the wrap version, which is the fence.
	if created[0].EnvelopeVersion != crypto.EnvelopeVersion {
		t.Fatalf("unexpected envelope version %d", created[0].EnvelopeVersion)
	}
	if created[0].KeyWrapVersion != crypto.KeyWrapVersion {
		t.Fatalf("the pending row must carry the wrap version, got %d", created[0].KeyWrapVersion)
	}
}

// The size that is sealed is the size that was read, not anything the client
// said. The declared Content-Length here is a lie an order of magnitude out.
func TestFinalizedSizeCountsTheBytesActuallyRead(t *testing.T) {
	f := newFixture(t)
	payload := bytes.Repeat([]byte("x"), 4096)

	if _, err := f.uploadTo(context.Background(),
		domain.Destination{Kind: domain.DestinationKindChannel, ID: testChannelID},
		&lyingReader{Reader: bytes.NewReader(payload)}, "report.pdf", "application/octet-stream",
	); err != nil {
		t.Fatalf("upload: %v", err)
	}
	_, uploaded, _ := f.store.snapshot()
	if uploaded[0].Size != int64(len(payload)) {
		t.Fatalf("expected the counted size %d, got %d", len(payload), uploaded[0].Size)
	}
	// And the sealed key really is bound to that number: the download only works
	// because the two agree.
	created, _, _ := f.store.snapshot()
	f.store.authorized = service.StoredAttachment{
		ID: created[0].ID, WorkspaceID: testWorkspaceID, Kind: domain.DestinationKindChannel,
		Status: domain.StatusPendingScan, Filename: created[0].Filename, Size: uploaded[0].Size,
		StorageObjectKey: created[0].StorageObjectKey, EnvelopeVersion: created[0].EnvelopeVersion,
		WrappedDEK: uploaded[0].WrappedDEK, KEKKeyID: uploaded[0].KEKKeyID,
		KeyWrapVersion: created[0].KeyWrapVersion,
	}
	f.store.authorized.Status = domain.StatusClean
	if _, err := f.service.Download(context.Background(), downloadInput(created[0].ID)); err != nil {
		t.Fatalf("the counted size must be the sealed size: %v", err)
	}
}

// A store that reports a byte count the plaintext cannot produce means the
// object is not a complete NCF1 stream. The upload fails and compensates rather
// than sealing a key against a length the stored bytes do not have.
func TestUploadCompensatesWhenTheStoredEnvelopeSizeIsWrong(t *testing.T) {
	f := newFixture(t)
	f.objects.shortPutBy = 1

	if _, err := f.upload(context.Background(),
		bytes.NewReader([]byte("payload")), "report.pdf"); err == nil {
		t.Fatal("expected the upload to fail")
	}
	_, uploaded, _ := f.store.snapshot()
	if len(uploaded) != 0 {
		t.Fatal("no row may be finalised when the stored envelope is not the expected size")
	}
	if f.objects.count() != 0 {
		t.Fatal("the incomplete object must be deleted")
	}
	if f.store.statusOf(t, f.store.onlyRowID(t)) != domain.StatusFailed {
		t.Fatal("the row must end failed")
	}
}

// lyingReader claims a length it does not have. Nothing in the pipeline reads
// it, which is the point: only the counted bytes reach the binding.
type lyingReader struct{ io.Reader }

func (r *lyingReader) Len() int    { return 1 << 30 }
func (r *lyingReader) Size() int64 { return 1 << 30 }

type draftStoreStub struct {
	*fakeStore
	cancelAttachmentID string
	cancelUploaderID   string
	cancelErr          error
	expireLimit        int
	expireCount        int
	expireErr          error
}

func newDraftStoreStub() *draftStoreStub {
	return &draftStoreStub{fakeStore: newFakeStore()}
}

func (s *draftStoreStub) CancelDraft(_ context.Context, attachmentID, uploaderID string) error {
	s.cancelAttachmentID = attachmentID
	s.cancelUploaderID = uploaderID
	return s.cancelErr
}

func (s *draftStoreStub) ExpireDrafts(_ context.Context, limit int) (int, error) {
	s.expireLimit = limit
	return s.expireCount, s.expireErr
}

func attachmentServiceWithStore(store service.AttachmentStore) *service.AttachmentService {
	return service.NewAttachmentService(nil, store, nil, nil, 0, false, nil, nil)
}

func TestCancelDraftValidatesIdentifiersBeforeCallingTheStore(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input service.CancelDraftInput
	}{
		{"missing attachment", service.CancelDraftInput{UploaderID: testUserID}},
		{"missing uploader", service.CancelDraftInput{AttachmentID: testChannelID}},
		{"invalid attachment", service.CancelDraftInput{AttachmentID: "not-a-uuid", UploaderID: testUserID}},
		{"invalid uploader", service.CancelDraftInput{AttachmentID: testChannelID, UploaderID: "not-a-uuid"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newDraftStoreStub()
			err := attachmentServiceWithStore(store).CancelDraft(context.Background(), tc.input)
			if !errors.Is(err, domain.ErrInvalidInput) {
				t.Fatalf("expected ErrInvalidInput, got %v", err)
			}
			if store.cancelAttachmentID != "" || store.cancelUploaderID != "" {
				t.Fatalf("store called with %q, %q", store.cancelAttachmentID, store.cancelUploaderID)
			}
		})
	}
}

func TestCancelDraftRequiresDraftCapableStore(t *testing.T) {
	err := attachmentServiceWithStore(newFakeStore()).CancelDraft(context.Background(), service.CancelDraftInput{
		AttachmentID: testChannelID,
		UploaderID:   testUserID,
	})
	if !errors.Is(err, domain.ErrDependenciesUnavailable) {
		t.Fatalf("expected ErrDependenciesUnavailable, got %v", err)
	}
}

func TestCancelDraftDelegatesExactIdentifiersAndPropagatesErrors(t *testing.T) {
	store := newDraftStoreStub()
	svc := attachmentServiceWithStore(store)
	input := service.CancelDraftInput{AttachmentID: testChannelID, UploaderID: testUserID}
	if err := svc.CancelDraft(context.Background(), input); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if store.cancelAttachmentID != input.AttachmentID || store.cancelUploaderID != input.UploaderID {
		t.Fatalf("delegated identifiers = %q, %q", store.cancelAttachmentID, store.cancelUploaderID)
	}

	store.cancelErr = domain.ErrNotFound
	if err := svc.CancelDraft(context.Background(), input); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected delegated ErrNotFound, got %v", err)
	}
}

func TestDraftExpiryUsesConfiguredLimitAndReturnsStoreResult(t *testing.T) {
	store := newDraftStoreStub()
	store.expireCount = 3
	svc := service.NewDraftExpiryService(store, 25)
	got, err := svc.ProcessDue(context.Background())
	if err != nil || got != 3 {
		t.Fatalf("ProcessDue() = %d, %v; want 3, nil", got, err)
	}
	if store.expireLimit != 25 {
		t.Fatalf("expiry limit = %d, want 25", store.expireLimit)
	}
}

func TestDraftExpiryFallsBackToBoundedDefaultAndPropagatesErrors(t *testing.T) {
	for _, limit := range []int{0, -1, 101} {
		store := newDraftStoreStub()
		store.expireErr = errors.New("database unavailable")
		got, err := service.NewDraftExpiryService(store, limit).ProcessDue(context.Background())
		if got != 0 || !errors.Is(err, store.expireErr) {
			t.Fatalf("limit %d: ProcessDue() = %d, %v", limit, got, err)
		}
		if store.expireLimit != 50 {
			t.Fatalf("limit %d: delegated limit = %d, want 50", limit, store.expireLimit)
		}
	}
}

func TestDraftExpiryFailsClosedWithoutDependency(t *testing.T) {
	var nilService *service.DraftExpiryService
	for name, svc := range map[string]*service.DraftExpiryService{
		"nil service": nilService,
		"nil store":   service.NewDraftExpiryService(nil, 25),
	} {
		t.Run(name, func(t *testing.T) {
			got, err := svc.ProcessDue(context.Background())
			if got != 0 || !errors.Is(err, domain.ErrDependenciesUnavailable) {
				t.Fatalf("ProcessDue() = %d, %v", got, err)
			}
		})
	}
}
