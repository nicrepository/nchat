package service_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/nicrepository/nchat/services/file-service/internal/crypto"
	"github.com/nicrepository/nchat/services/file-service/internal/domain"
	"github.com/nicrepository/nchat/services/file-service/internal/preview"
	"github.com/nicrepository/nchat/services/file-service/internal/service"
	"github.com/nicrepository/nchat/services/file-service/internal/storage"
)

// Opt-in integration coverage for preview generation (RF-31) against the same
// real infrastructure as the upload suite: the claim query runs against a real
// PostgreSQL with migration 000003 applied, the renderer really decodes, and
// the preview object really lands in SeaweedFS.
//
// It uses newIntegrationEnv from upload_integration_test.go and skips under the
// same conditions, so the default test run still depends on nothing external.
//
// The read path stops at the persisted binding for the same reason the upload
// suite's does: AttachmentService.Preview goes through GetAuthorized, which
// needs seeded auth.user_sessions and chat rows this suite deliberately avoids.
// What is asserted instead is the stronger half — that the stored object opens
// with the stored binding and yields the rendered image — which is exactly what
// the handler would go on to stream.

// markScanned applies a clean verdict through the canonical operation — the one
// the antimalware worker will call when it exists (RF-33) — and never through
// an UPDATE written in the test.
//
// The distinction is the same one SR-001 turned on: a test that writes the
// transition itself cannot fail when the transition is wrong. The preview claim
// requires clean, so every test that wants a preview generated passes through
// here, which means every one of them exercises the real approval path.
func (e *integrationEnv) markScanned(t *testing.T, attachmentID string) service.AttachmentLifecycleState {
	t.Helper()
	state, err := e.lifecycle.MarkScanClean(context.Background(), service.ScanApproval{
		AttachmentID: attachmentID,
		WorkspaceID:  e.workspaceID,
	})
	if err != nil {
		t.Fatalf("mark scanned: %v", err)
	}
	return state
}

// removeAttachment soft-deletes through the canonical operation.
func (e *integrationEnv) removeAttachment(t *testing.T, attachmentID string) service.AttachmentLifecycleState {
	t.Helper()
	state, err := e.lifecycle.MarkAttachmentDeleted(context.Background(), service.AttachmentRemoval{
		AttachmentID: attachmentID,
		WorkspaceID:  e.workspaceID,
	})
	if err != nil {
		t.Fatalf("remove attachment: %v", err)
	}
	return state
}

// deletionState reads back the three columns a removal has to move together.
func (e *integrationEnv) deletionState(t *testing.T, attachmentID string) (
	deletedAt *time.Time, previewStatus string, nextAttempt *time.Time,
) {
	t.Helper()
	if err := e.pool.QueryRow(context.Background(), `
		SELECT deleted_at, preview_status, preview_next_attempt_at
		  FROM files.attachments WHERE id = $1`, attachmentID,
	).Scan(&deletedAt, &previewStatus, &nextAttempt); err != nil {
		t.Fatalf("read deletion state: %v", err)
	}
	return deletedAt, previewStatus, nextAttempt
}

// rejectScan applies the antimalware verdict through the real storage
// operation, never through an UPDATE written in the test.
//
// That distinction is the whole point of SR-001: the test used to write
// `SET status = 'rejected'` itself, which is how a transition that left the
// preview scheduled forever went unnoticed. A test that invents the business
// rule cannot fail when the rule is wrong.
func (e *integrationEnv) rejectScan(t *testing.T, attachmentID string) service.ScanRejectionOutcome {
	t.Helper()
	outcome, err := e.lifecycle.MarkScanRejected(context.Background(), service.ScanRejection{
		AttachmentID: attachmentID,
		WorkspaceID:  e.workspaceID,
	})
	if err != nil {
		t.Fatalf("reject scan: %v", err)
	}
	return outcome
}

// attachmentState reads back the two columns the verdict has to move together.
func (e *integrationEnv) attachmentState(t *testing.T, attachmentID string) (status, previewStatus string, nextAttempt *time.Time) {
	t.Helper()
	if err := e.pool.QueryRow(context.Background(), `
		SELECT status, preview_status, preview_next_attempt_at
		  FROM files.attachments WHERE id = $1`, attachmentID,
	).Scan(&status, &previewStatus, &nextAttempt); err != nil {
		t.Fatalf("read attachment state: %v", err)
	}
	return status, previewStatus, nextAttempt
}

// previewJobs wires the preview use case against the live database, the live
// filer and the real decoders.
func (e *integrationEnv) previewJobs() *service.PreviewService {
	return service.NewPreviewService(
		storage.NewPGXPreviewStore(e.pool), e.objects, e.keys,
		preview.New(), e.fence, storage.NewPGXObjectCleanupStore(e.pool), nil, discardLogger(),
	)
}

// previewRow reads the persisted preview state straight from PostgreSQL.
func (e *integrationEnv) previewRow(t *testing.T, attachmentID string) (
	status string, objectID string, size int64, wrappedDEK []byte, keyID string,
	envelopeVersion, wrapVersion int, attempts int,
) {
	t.Helper()
	var (
		nullableObjectID   *string
		nullableSize       *int64
		nullableKeyID      *string
		nullableEnvelopeV  *int16
		nullableWrapV      *int16
		attemptsFromColumn int16
	)
	err := e.pool.QueryRow(context.Background(), `
		SELECT preview_status, preview_object_id::text, preview_size_bytes,
		       preview_wrapped_dek, preview_kek_key_id,
		       preview_envelope_version, preview_dek_wrap_version, preview_attempts
		  FROM files.attachments WHERE id = $1`, attachmentID,
	).Scan(&status, &nullableObjectID, &nullableSize, &wrappedDEK, &nullableKeyID,
		&nullableEnvelopeV, &nullableWrapV, &attemptsFromColumn)
	if err != nil {
		t.Fatalf("read preview row %s: %v", attachmentID, err)
	}
	if nullableObjectID != nil {
		objectID = *nullableObjectID
	}
	if nullableSize != nil {
		size = *nullableSize
	}
	if nullableKeyID != nil {
		keyID = *nullableKeyID
	}
	if nullableEnvelopeV != nil {
		envelopeVersion = int(*nullableEnvelopeV)
	}
	if nullableWrapV != nil {
		wrapVersion = int(*nullableWrapV)
	}
	return status, objectID, size, wrappedDEK, keyID, envelopeVersion, wrapVersion, int(attemptsFromColumn)
}

// integrationPNG is a real image, generated rather than committed: the suite
// carries no binary fixture.
func integrationPNG(t *testing.T, width, height int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.SetRGBA(x, y, color.RGBA{R: uint8(x % 256), G: uint8(y % 256), B: 0x80, A: 0xff}) //nolint:gosec // G115: masked to a byte.
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

// integrationPDF is a one-page document built by hand, for the same reason.
func integrationPDF() []byte {
	content := "0 0 1 rg\n20 20 160 60 re f\n"
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 200 100] /Contents 4 0 R /Resources << >> >>",
		"<< /Length " + strconv.Itoa(len(content)) + " >>\nstream\n" + content + "endstream",
	}
	var buf bytes.Buffer
	buf.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objects)+1)
	for i, object := range objects {
		offsets[i+1] = buf.Len()
		buf.WriteString(strconv.Itoa(i+1) + " 0 obj\n" + object + "\nendobj\n")
	}
	xref := buf.Len()
	buf.WriteString("xref\n0 " + strconv.Itoa(len(objects)+1) + "\n0000000000 65535 f \n")
	for i := 1; i <= len(objects); i++ {
		fmt.Fprintf(&buf, "%010d 00000 n \n", offsets[i])
	}
	buf.WriteString("trailer\n<< /Size " + strconv.Itoa(len(objects)+1) +
		" /Root 1 0 R >>\nstartxref\n" + strconv.Itoa(xref) + "\n%%EOF\n")
	return buf.Bytes()
}

// openStoredPreview decrypts a preview object using only what the row records,
// which is the real assertion: a preview that cannot be opened from its own
// persisted binding would reach a client as a broken image.
func (e *integrationEnv) openStoredPreview(
	t *testing.T, objectID string, size int64, wrappedDEK []byte,
	keyID string, envelopeVersion, wrapVersion int,
) []byte {
	t.Helper()
	previewID := uuid.MustParse(objectID)
	dataKey, err := e.keys.Unwrap(wrappedDEK, keyID, crypto.Binding{
		AttachmentID:           previewID,
		WorkspaceID:            uuid.MustParse(e.workspaceID),
		PlaintextSize:          size,
		KeyWrapVersion:         wrapVersion,
		ContentEnvelopeVersion: envelopeVersion,
	})
	if err != nil {
		t.Fatalf("unwrap the persisted preview key: %v", err)
	}
	body, err := e.objects.Open(context.Background(), domain.PreviewObjectKey(previewID))
	if err != nil {
		t.Fatalf("open the stored preview object: %v", err)
	}
	defer func() { _ = body.Close() }()

	plaintext, err := crypto.NewDecryptingReader(body, dataKey, previewID, size)
	if err != nil {
		t.Fatalf("decrypting reader: %v", err)
	}
	image, err := io.ReadAll(plaintext)
	if err != nil {
		t.Fatalf("read the preview plaintext: %v", err)
	}
	return image
}

func TestIntegrationImageUploadBecomesAStoredEncryptedPreview(t *testing.T) {
	env := newIntegrationEnv(t)
	svc := env.newService(env.objects)

	view, err := env.uploadContent(t, svc, bytes.NewReader(integrationPNG(t, 900, 600)), "photo.png")
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	// The upload only scheduled the work; nothing was rendered on its path.
	if view.PreviewStatus != string(domain.PreviewStatusPending) {
		t.Fatalf("upload left preview status %q, want pending", view.PreviewStatus)
	}

	// The scan gate, against the real claim: while the attachment is awaiting a
	// verdict it is not claimed, so it is never decrypted and never parsed.
	jobs := env.previewJobs()
	if processed, err := jobs.ProcessDue(context.Background()); err != nil || processed != 0 {
		t.Fatalf("an unscanned attachment was claimed: processed=%d err=%v", processed, err)
	}
	env.markScanned(t, view.ID)

	processed, err := jobs.ProcessDue(context.Background())
	if err != nil {
		t.Fatalf("process due previews: %v", err)
	}
	if processed == 0 {
		t.Fatal("the claim found nothing to do")
	}

	status, objectID, size, wrappedDEK, keyID, envelopeVersion, wrapVersion, attempts :=
		env.previewRow(t, view.ID)
	if status != string(domain.PreviewStatusReady) {
		t.Fatalf("preview status = %q, want ready", status)
	}
	if objectID == view.ID {
		t.Fatal("the preview must have its own identity, not the attachment's")
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want exactly one claim", attempts)
	}
	if keyID != integrationKeyID || wrapVersion != crypto.KeyWrapVersion ||
		envelopeVersion != crypto.EnvelopeVersion {
		t.Fatalf("unexpected persisted preview binding: id=%q wrap=%d envelope=%d",
			keyID, wrapVersion, envelopeVersion)
	}

	rendered := env.openStoredPreview(t, objectID, size, wrappedDEK, keyID, envelopeVersion, wrapVersion)
	decoded, format, err := image.Decode(bytes.NewReader(rendered))
	if err != nil {
		t.Fatalf("the stored preview is not a decodable image: %v", err)
	}
	if format != "jpeg" {
		t.Fatalf("stored preview format = %q, want jpeg", format)
	}
	if bounds := decoded.Bounds(); bounds.Dx() > domain.MaxPreviewDimension ||
		bounds.Dy() > domain.MaxPreviewDimension {
		t.Fatalf("stored preview is %dx%d, beyond the box", bounds.Dx(), bounds.Dy())
	}

	// The original attachment is untouched and still exactly where it was.
	if _, objectKey := env.row(t, view.ID); objectKey != domain.StorageObjectKey(uuid.MustParse(view.ID)) {
		t.Fatalf("the attachment's own object moved: %q", objectKey)
	}
}

func TestIntegrationPDFUploadBecomesAFirstPagePreview(t *testing.T) {
	env := newIntegrationEnv(t)
	svc := env.newService(env.objects)

	view, err := env.uploadContent(t, svc, bytes.NewReader(integrationPDF()), "report.pdf")
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	if view.ContentType != "application/pdf" {
		t.Fatalf("detected type = %q, want application/pdf", view.ContentType)
	}
	env.markScanned(t, view.ID)
	if _, err := env.previewJobs().ProcessDue(context.Background()); err != nil {
		t.Fatalf("process due previews: %v", err)
	}

	status, objectID, size, wrappedDEK, keyID, envelopeVersion, wrapVersion, _ :=
		env.previewRow(t, view.ID)
	if status != string(domain.PreviewStatusReady) {
		t.Fatalf("preview status = %q, want ready", status)
	}
	rendered := env.openStoredPreview(t, objectID, size, wrappedDEK, keyID, envelopeVersion, wrapVersion)
	decoded, _, err := image.Decode(bytes.NewReader(rendered))
	if err != nil {
		t.Fatalf("the stored preview is not a decodable image: %v", err)
	}
	// The fixture page is twice as wide as it is tall.
	if decoded.Bounds().Dx() <= decoded.Bounds().Dy() {
		t.Fatalf("first page rendered as %v, expected landscape", decoded.Bounds())
	}
}

// A format with no renderer must never enter the queue, so the worker never
// reads an attachment it would only refuse.
func TestIntegrationUnsupportedContentNeverEntersTheQueue(t *testing.T) {
	env := newIntegrationEnv(t)
	svc := env.newService(env.objects)

	view, err := env.upload(t, svc, "PK\x03\x04 this is an archive, not an image")
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	if view.PreviewStatus != string(domain.PreviewStatusUnsupported) {
		t.Fatalf("preview status = %q, want unsupported", view.PreviewStatus)
	}
	env.markScanned(t, view.ID)

	processed, err := env.previewJobs().ProcessDue(context.Background())
	if err != nil {
		t.Fatalf("process due previews: %v", err)
	}
	if processed != 0 {
		t.Fatalf("the claim picked up %d rows that can never be rendered", processed)
	}
	status, _, _, _, _, _, _, attempts := env.previewRow(t, view.ID)
	if status != string(domain.PreviewStatusUnsupported) || attempts != 0 {
		t.Fatalf("row moved to %q after %d attempts", status, attempts)
	}
}

// The lease is what stops two replicas from rendering the same attachment. A
// second pass immediately after the first must find nothing, and a finished row
// must never be claimed again.
func TestIntegrationASecondPassDoesNotRepeatFinishedOrLeasedWork(t *testing.T) {
	env := newIntegrationEnv(t)
	svc := env.newService(env.objects)
	jobs := env.previewJobs()

	view, err := env.uploadContent(t, svc, bytes.NewReader(integrationPNG(t, 200, 200)), "photo.png")
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	env.markScanned(t, view.ID)
	if _, err := jobs.ProcessDue(context.Background()); err != nil {
		t.Fatalf("first pass: %v", err)
	}
	_, firstObjectID, _, _, _, _, _, _ := env.previewRow(t, view.ID)

	processed, err := jobs.ProcessDue(context.Background())
	if err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if processed != 0 {
		t.Fatalf("a finished preview was claimed again by %d passes", processed)
	}
	status, objectID, _, _, _, _, _, attempts := env.previewRow(t, view.ID)
	if status != string(domain.PreviewStatusReady) || objectID != firstObjectID {
		t.Fatalf("the preview changed on a second pass: %q %q", status, objectID)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want one", attempts)
	}
}

// The containment control, end to end against the real query: an attachment the
// scanner condemned is never claimed, so the parser never sees it — and one
// condemned *after* a preview was produced stops being servable at once,
// because delivery re-checks the attachment's own status.
func TestIntegrationRejectedAttachmentsNeverReachTheRenderer(t *testing.T) {
	env := newIntegrationEnv(t)
	svc := env.newService(env.objects)
	jobs := env.previewJobs()

	view, err := env.uploadContent(t, svc, bytes.NewReader(integrationPNG(t, 120, 120)), "photo.png")
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	env.rejectScan(t, view.ID)

	processed, err := jobs.ProcessDue(context.Background())
	if err != nil {
		t.Fatalf("process: %v", err)
	}
	if processed != 0 {
		t.Fatalf("a rejected attachment was claimed %d times", processed)
	}
	// The verdict finished the preview off, so the row is terminal rather than
	// pending-and-unreachable.
	status, previewStatus, nextAttempt := env.attachmentState(t, view.ID)
	if status != string(domain.StatusRejected) ||
		previewStatus != string(domain.PreviewStatusUnsupported) {
		t.Fatalf("row is %q/%q, want rejected/unsupported", status, previewStatus)
	}
	if nextAttempt != nil {
		t.Fatalf("a rejected attachment must not stay scheduled, got %v", nextAttempt)
	}
}

// The publishing statement re-asserts the verdict, closing the window between a
// claim and the write that follows it.
func TestIntegrationAVerdictDuringRenderStopsThePublication(t *testing.T) {
	env := newIntegrationEnv(t)
	svc := env.newService(env.objects)

	view, err := env.uploadContent(t, svc, bytes.NewReader(integrationPNG(t, 120, 120)), "photo.png")
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	env.markScanned(t, view.ID)

	store := storage.NewPGXPreviewStore(env.pool)
	claimed, err := store.ClaimDuePreviews(context.Background(), 1, time.Minute)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim: %d rows, err %v", len(claimed), err)
	}

	// The scanner rules while the render is in flight.
	env.rejectScan(t, view.ID)

	recorded, err := store.MarkPreviewReady(context.Background(), service.PreviewResult{
		AttachmentID:    view.ID,
		ClaimAttempt:    claimed[0].Attempts,
		PreviewObjectID: uuid.NewString(),
		Size:            1024,
		WrappedDEK:      []byte{1, 2, 3},
		KEKKeyID:        integrationKeyID,
		EnvelopeVersion: crypto.EnvelopeVersion,
		KeyWrapVersion:  crypto.KeyWrapVersion,
	})
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if recorded {
		t.Fatal("a preview must not be published for an attachment the scan rejected")
	}
	if status, _, _, _, _, _, _, _ := env.previewRow(t, view.ID); status == string(domain.PreviewStatusReady) {
		t.Fatal("the row must not reach ready")
	}
}

// --- SR-001: the scan verdict is one atomic transition --------------------
//
// These run against real PostgreSQL because that is the only place the
// atomicity claim means anything: a mock cannot fail to be atomic.

// Scenario 1: the ordinary verdict, on a file still awaiting one.
func TestIntegrationRejectionFinalisesAPendingPreviewAtomically(t *testing.T) {
	env := newIntegrationEnv(t)
	svc := env.newService(env.objects)

	view, err := env.uploadContent(t, svc, bytes.NewReader(integrationPNG(t, 120, 120)), "photo.png")
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	// The upload left the row scheduled: pending_scan, preview pending, with a
	// next attempt set. That is exactly the state SR-001 stranded.
	status, previewStatus, nextAttempt := env.attachmentState(t, view.ID)
	if status != string(domain.StatusPendingScan) ||
		previewStatus != string(domain.PreviewStatusPending) || nextAttempt == nil {
		t.Fatalf("precondition not met: %q/%q scheduled=%v", status, previewStatus, nextAttempt != nil)
	}

	outcome := env.rejectScan(t, view.ID)
	if outcome.Status != domain.StatusRejected ||
		outcome.PreviewStatus != domain.PreviewStatusUnsupported {
		t.Fatalf("operation reported %+v", outcome)
	}

	status, previewStatus, nextAttempt = env.attachmentState(t, view.ID)
	if status != string(domain.StatusRejected) {
		t.Fatalf("status = %q, want rejected", status)
	}
	if previewStatus != string(domain.PreviewStatusUnsupported) {
		t.Fatalf("preview status = %q, want unsupported", previewStatus)
	}
	if nextAttempt != nil {
		t.Fatalf("the schedule must be cleared, got %v", nextAttempt)
	}

	// The row is not merely unclaimable — it is finished. Both matter: the
	// first stops the renderer, the second stops it being stuck.
	processed, err := env.previewJobs().ProcessDue(context.Background())
	if err != nil {
		t.Fatalf("process: %v", err)
	}
	if processed != 0 {
		t.Fatalf("a rejected attachment was claimed %d times", processed)
	}
}

// Scenario 2: a rescan reversing an approval, or a verdict landing while the
// preview is queued and claimable.
func TestIntegrationRejectionFinalisesAPreviewThatWasAlreadyClean(t *testing.T) {
	env := newIntegrationEnv(t)
	svc := env.newService(env.objects)

	view, err := env.uploadContent(t, svc, bytes.NewReader(integrationPNG(t, 120, 120)), "photo.png")
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	env.markScanned(t, view.ID)

	env.rejectScan(t, view.ID)

	status, previewStatus, nextAttempt := env.attachmentState(t, view.ID)
	if status != string(domain.StatusRejected) ||
		previewStatus != string(domain.PreviewStatusUnsupported) || nextAttempt != nil {
		t.Fatalf("row is %q/%q scheduled=%v", status, previewStatus, nextAttempt != nil)
	}
	if processed, err := env.previewJobs().ProcessDue(context.Background()); err != nil ||
		processed != 0 {
		t.Fatalf("claimed %d rows after rejection, err %v", processed, err)
	}
}

// Repeating a verdict — the scanner retrying, or two workers ruling at once —
// must leave the same state and say so, not fail and not reopen anything.
func TestIntegrationRepeatedRejectionIsIdempotent(t *testing.T) {
	env := newIntegrationEnv(t)
	svc := env.newService(env.objects)

	view, err := env.uploadContent(t, svc, bytes.NewReader(integrationPNG(t, 120, 120)), "photo.png")
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	first := env.rejectScan(t, view.ID)
	second := env.rejectScan(t, view.ID)

	if first != second {
		t.Fatalf("second verdict reported %+v, want %+v", second, first)
	}
	status, previewStatus, nextAttempt := env.attachmentState(t, view.ID)
	if status != string(domain.StatusRejected) ||
		previewStatus != string(domain.PreviewStatusUnsupported) || nextAttempt != nil {
		t.Fatalf("row drifted to %q/%q scheduled=%v", status, previewStatus, nextAttempt != nil)
	}
}

// The repair path: a row an earlier build left rejected-and-still-pending is
// brought to a terminal state by re-applying the verdict. This is why the
// accepted-state set includes 'rejected' and why no backfill migration is
// needed.
func TestIntegrationRejectionRepairsAStrandedPendingPreview(t *testing.T) {
	env := newIntegrationEnv(t)
	svc := env.newService(env.objects)

	view, err := env.uploadContent(t, svc, bytes.NewReader(integrationPNG(t, 120, 120)), "photo.png")
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	// Reproduce the broken state exactly as the previous build produced it: the
	// status alone, leaving the preview pending and scheduled.
	if _, err := env.pool.Exec(context.Background(),
		`UPDATE files.attachments SET status = 'rejected' WHERE id = $1`, view.ID,
	); err != nil {
		t.Fatalf("stage the stranded row: %v", err)
	}
	status, previewStatus, nextAttempt := env.attachmentState(t, view.ID)
	if status != string(domain.StatusRejected) ||
		previewStatus != string(domain.PreviewStatusPending) || nextAttempt == nil {
		t.Fatalf("staging failed: %q/%q scheduled=%v", status, previewStatus, nextAttempt != nil)
	}

	env.rejectScan(t, view.ID)

	status, previewStatus, nextAttempt = env.attachmentState(t, view.ID)
	if status != string(domain.StatusRejected) ||
		previewStatus != string(domain.PreviewStatusUnsupported) || nextAttempt != nil {
		t.Fatalf("row was not repaired: %q/%q scheduled=%v", status, previewStatus, nextAttempt != nil)
	}
}

// A preview that already reached a terminal state keeps it: the verdict is
// about the file, and rewriting `failed` as `unsupported` would lose the
// difference between "we tried and could not" and "we were never allowed to".
func TestIntegrationRejectionLeavesATerminalPreviewAlone(t *testing.T) {
	env := newIntegrationEnv(t)
	svc := env.newService(env.objects)

	// An archive is never previewable, so the upload finalises as unsupported.
	view, err := env.upload(t, svc, "PK\x03\x04 an archive, never previewable")
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	outcome := env.rejectScan(t, view.ID)

	if outcome.PreviewStatus != domain.PreviewStatusUnsupported {
		t.Fatalf("preview status = %q, want it untouched", outcome.PreviewStatus)
	}
	if _, previewStatus, nextAttempt := env.attachmentState(t, view.ID); previewStatus !=
		string(domain.PreviewStatusUnsupported) || nextAttempt != nil {
		t.Fatalf("row is %q scheduled=%v", previewStatus, nextAttempt != nil)
	}
}

// Scenario 3 and 5: a worker holding a lease — expired or not — cannot publish
// after the verdict, and the object it produced does not survive.
func TestIntegrationAWorkerHoldingALeaseCannotPublishAfterRejection(t *testing.T) {
	env := newIntegrationEnv(t)
	svc := env.newService(env.objects)

	view, err := env.uploadContent(t, svc, bytes.NewReader(integrationPNG(t, 120, 120)), "photo.png")
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	env.markScanned(t, view.ID)

	store := storage.NewPGXPreviewStore(env.pool)
	claimed, err := store.ClaimDuePreviews(context.Background(), 1, time.Minute)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim: %d rows, err %v", len(claimed), err)
	}

	// The verdict lands while that worker is still rendering.
	env.rejectScan(t, view.ID)

	recorded, err := store.MarkPreviewReady(context.Background(), service.PreviewResult{
		AttachmentID:    view.ID,
		ClaimAttempt:    claimed[0].Attempts,
		PreviewObjectID: uuid.NewString(),
		Size:            1024,
		WrappedDEK:      []byte{1, 2, 3},
		KEKKeyID:        integrationKeyID,
		EnvelopeVersion: crypto.EnvelopeVersion,
		KeyWrapVersion:  crypto.KeyWrapVersion,
	})
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if recorded {
		t.Fatal("a stale worker published a preview for a rejected attachment")
	}

	// And the expired lease does not bring the row back: the schedule is gone
	// and the state is terminal, so nothing re-queues it.
	status, previewStatus, nextAttempt := env.attachmentState(t, view.ID)
	if status != string(domain.StatusRejected) ||
		previewStatus != string(domain.PreviewStatusUnsupported) || nextAttempt != nil {
		t.Fatalf("row is %q/%q scheduled=%v", status, previewStatus, nextAttempt != nil)
	}
	if processed, err := env.previewJobs().ProcessDue(context.Background()); err != nil ||
		processed != 0 {
		t.Fatalf("claimed %d rows after the lease expired, err %v", processed, err)
	}
}

// Scenario 4: two verdicts at once. Each is a single statement, so one waits for
// the other's row lock and both observe the same final state — there is no
// interleaving that can leave half a transition.
func TestIntegrationConcurrentRejectionsConverge(t *testing.T) {
	env := newIntegrationEnv(t)
	svc := env.newService(env.objects)

	view, err := env.uploadContent(t, svc, bytes.NewReader(integrationPNG(t, 120, 120)), "photo.png")
	if err != nil {
		t.Fatalf("upload: %v", err)
	}

	const racers = 4
	outcomes := make(chan service.ScanRejectionOutcome, racers)
	errs := make(chan error, racers)
	var wg sync.WaitGroup
	for range racers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			outcome, err := env.lifecycle.MarkScanRejected(context.Background(), service.ScanRejection{
				AttachmentID: view.ID,
				WorkspaceID:  env.workspaceID,
			})
			if err != nil {
				errs <- err
				return
			}
			outcomes <- outcome
		}()
	}
	wg.Wait()
	close(outcomes)
	close(errs)

	for err := range errs {
		t.Fatalf("concurrent verdict failed: %v", err)
	}
	for outcome := range outcomes {
		if outcome.Status != domain.StatusRejected ||
			outcome.PreviewStatus != domain.PreviewStatusUnsupported {
			t.Fatalf("a racer observed %+v", outcome)
		}
	}
	status, previewStatus, nextAttempt := env.attachmentState(t, view.ID)
	if status != string(domain.StatusRejected) ||
		previewStatus != string(domain.PreviewStatusUnsupported) || nextAttempt != nil {
		t.Fatalf("final row is %q/%q scheduled=%v", status, previewStatus, nextAttempt != nil)
	}
}

// Scenario 6: a deleted attachment, and a verdict aimed at another tenant's
// row. Neither is applied, and neither is silently reported as done.
func TestIntegrationRejectionRefusesRowsItMustNotTouch(t *testing.T) {
	env := newIntegrationEnv(t)
	svc := env.newService(env.objects)

	deleted, err := env.uploadContent(t, svc, bytes.NewReader(integrationPNG(t, 64, 64)), "gone.png")
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	env.removeAttachment(t, deleted.ID)

	live, err := env.uploadContent(t, svc, bytes.NewReader(integrationPNG(t, 64, 64)), "live.png")
	if err != nil {
		t.Fatalf("upload: %v", err)
	}

	for name, rejection := range map[string]service.ScanRejection{
		"deleted attachment": {AttachmentID: deleted.ID, WorkspaceID: env.workspaceID},
		"another workspace":  {AttachmentID: live.ID, WorkspaceID: uuid.NewString()},
		"unknown attachment": {AttachmentID: uuid.NewString(), WorkspaceID: env.workspaceID},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := env.lifecycle.MarkScanRejected(context.Background(), rejection)
			if !errors.Is(err, domain.ErrNotFound) {
				t.Fatalf("error = %v, want ErrNotFound", err)
			}
		})
	}

	// The live row was never touched by the mis-aimed verdict.
	if status, _, _ := env.attachmentState(t, live.ID); status != string(domain.StatusPendingScan) {
		t.Fatalf("live attachment moved to %q", status)
	}
}

// --- the canonical clean verdict ------------------------------------------

// The whole default flow, end to end, with nothing written by hand: upload
// lands in pending_scan, the scan verdict is applied through the canonical
// operation, the preview worker claims the row, the renderer runs, and the
// preview is stored encrypted and readable.
//
// The producer of that verdict — the ClamAV worker — does not exist in this
// repository yet, so this test stands in for it by calling the same operation
// it will have to call. Everything downstream of the verdict is the real
// pipeline.
func TestIntegrationCleanVerdictUnlocksTheWholePreviewPipeline(t *testing.T) {
	env := newIntegrationEnv(t)
	svc := env.newService(env.objects)
	jobs := env.previewJobs()

	view, err := env.uploadContent(t, svc, bytes.NewReader(integrationPNG(t, 640, 480)), "photo.png")
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	// 1. The upload is awaiting a verdict and the preview is queued behind it.
	status, previewStatus, _ := env.attachmentState(t, view.ID)
	if status != string(domain.StatusPendingScan) ||
		previewStatus != string(domain.PreviewStatusPending) {
		t.Fatalf("upload left %q/%q, want pending_scan/pending", status, previewStatus)
	}
	// 2. Nothing is claimed, so nothing is decrypted and no parser runs.
	if processed, err := jobs.ProcessDue(context.Background()); err != nil || processed != 0 {
		t.Fatalf("claimed %d rows before the verdict, err %v", processed, err)
	}

	// 3. The scanner approves, through the canonical operation.
	approved := env.markScanned(t, view.ID)
	if approved.Status != domain.StatusClean {
		t.Fatalf("verdict reported %q, want clean", approved.Status)
	}
	// The approval touched no preview column: the upload had already scheduled
	// the job, so the row is claimable the instant the status turns clean.
	if approved.PreviewStatus != domain.PreviewStatusPending {
		t.Fatalf("preview status = %q, want it untouched", approved.PreviewStatus)
	}

	// 4. Now — and only now — the preview worker takes the row and renders it.
	processed, err := jobs.ProcessDue(context.Background())
	if err != nil {
		t.Fatalf("process: %v", err)
	}
	if processed != 1 {
		t.Fatalf("claimed %d rows after the verdict, want 1", processed)
	}

	// 5. The preview is stored, encrypted, and opens from its own binding.
	previewStatusAfter, objectID, size, wrappedDEK, keyID, envelopeVersion, wrapVersion, _ :=
		env.previewRow(t, view.ID)
	if previewStatusAfter != string(domain.PreviewStatusReady) {
		t.Fatalf("preview status = %q, want ready", previewStatusAfter)
	}
	rendered := env.openStoredPreview(t, objectID, size, wrappedDEK, keyID, envelopeVersion, wrapVersion)
	decoded, format, err := image.Decode(bytes.NewReader(rendered))
	if err != nil {
		t.Fatalf("the stored preview is not a decodable image: %v", err)
	}
	if format != "jpeg" {
		t.Fatalf("stored preview format = %q, want jpeg", format)
	}
	if bounds := decoded.Bounds(); bounds.Dx() > domain.MaxPreviewDimension ||
		bounds.Dy() > domain.MaxPreviewDimension {
		t.Fatalf("stored preview is %dx%d, beyond the box", bounds.Dx(), bounds.Dy())
	}
}

// A repeated clean verdict — a scanner retrying after a lost acknowledgement —
// must converge rather than fail.
func TestIntegrationRepeatedCleanVerdictIsIdempotent(t *testing.T) {
	env := newIntegrationEnv(t)
	svc := env.newService(env.objects)

	view, err := env.uploadContent(t, svc, bytes.NewReader(integrationPNG(t, 64, 64)), "photo.png")
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	first := env.markScanned(t, view.ID)
	second := env.markScanned(t, view.ID)
	if first != second {
		t.Fatalf("second verdict reported %+v, want %+v", second, first)
	}
	if status, _, _ := env.attachmentState(t, view.ID); status != string(domain.StatusClean) {
		t.Fatalf("status = %q, want clean", status)
	}
}

// The direction that must never reverse: a condemned file cannot be approved by
// a stale or replayed verdict, and neither can a removed one.
func TestIntegrationCleanVerdictCannotReopenARejectedOrRemovedAttachment(t *testing.T) {
	env := newIntegrationEnv(t)
	svc := env.newService(env.objects)

	rejected, err := env.uploadContent(t, svc, bytes.NewReader(integrationPNG(t, 64, 64)), "bad.png")
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	env.rejectScan(t, rejected.ID)

	removed, err := env.uploadContent(t, svc, bytes.NewReader(integrationPNG(t, 64, 64)), "gone.png")
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	env.removeAttachment(t, removed.ID)

	for name, id := range map[string]string{"rejected": rejected.ID, "removed": removed.ID} {
		t.Run(name, func(t *testing.T) {
			_, err := env.lifecycle.MarkScanClean(context.Background(), service.ScanApproval{
				AttachmentID: id, WorkspaceID: env.workspaceID,
			})
			if !errors.Is(err, domain.ErrNotFound) {
				t.Fatalf("error = %v, want ErrNotFound", err)
			}
		})
	}

	// And neither row moved.
	if status, _, _ := env.attachmentState(t, rejected.ID); status != string(domain.StatusRejected) {
		t.Fatalf("rejected attachment moved to %q", status)
	}
	if deletedAt, _, _ := env.deletionState(t, removed.ID); deletedAt == nil {
		t.Fatal("removed attachment was resurrected")
	}
}

// --- removal ---------------------------------------------------------------

// Removing an attachment whose preview was still pending must finish the
// preview in the same statement, or the row keeps a queued job that no claim
// can ever select and nothing ever concludes.
func TestIntegrationRemovalFinalisesAPendingPreviewAtomically(t *testing.T) {
	env := newIntegrationEnv(t)
	svc := env.newService(env.objects)
	jobs := env.previewJobs()

	view, err := env.uploadContent(t, svc, bytes.NewReader(integrationPNG(t, 120, 120)), "photo.png")
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	env.markScanned(t, view.ID)
	// The precondition: clean, preview pending, and scheduled.
	if _, previewStatus, nextAttempt := env.deletionState(t, view.ID); previewStatus !=
		string(domain.PreviewStatusPending) || nextAttempt == nil {
		t.Fatalf("precondition not met: %q scheduled=%v", previewStatus, nextAttempt != nil)
	}

	state := env.removeAttachment(t, view.ID)
	if state.PreviewStatus != domain.PreviewStatusUnsupported {
		t.Fatalf("removal reported preview %q, want unsupported", state.PreviewStatus)
	}

	deletedAt, previewStatus, nextAttempt := env.deletionState(t, view.ID)
	if deletedAt == nil {
		t.Fatal("deleted_at was not written")
	}
	if previewStatus != string(domain.PreviewStatusUnsupported) {
		t.Fatalf("preview status = %q, want unsupported", previewStatus)
	}
	if nextAttempt != nil {
		t.Fatalf("the schedule must be cleared, got %v", nextAttempt)
	}
	// The row is gone from the queue: no claim, so no decryption and no parser.
	if processed, err := jobs.ProcessDue(context.Background()); err != nil || processed != 0 {
		t.Fatalf("claimed %d removed rows, err %v", processed, err)
	}
}

// Repeating a removal converges and keeps the original deletion timestamp,
// which is what a retention policy will count from.
func TestIntegrationRepeatedRemovalIsIdempotentAndKeepsTheTimestamp(t *testing.T) {
	env := newIntegrationEnv(t)
	svc := env.newService(env.objects)

	view, err := env.uploadContent(t, svc, bytes.NewReader(integrationPNG(t, 64, 64)), "photo.png")
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	env.removeAttachment(t, view.ID)
	firstDeletedAt, firstPreview, _ := env.deletionState(t, view.ID)

	env.removeAttachment(t, view.ID)
	secondDeletedAt, secondPreview, secondNextAttempt := env.deletionState(t, view.ID)

	if firstDeletedAt == nil || secondDeletedAt == nil || !firstDeletedAt.Equal(*secondDeletedAt) {
		t.Fatalf("deletion timestamp moved: %v -> %v", firstDeletedAt, secondDeletedAt)
	}
	if firstPreview != secondPreview || secondNextAttempt != nil {
		t.Fatalf("state drifted to %q scheduled=%v", secondPreview, secondNextAttempt != nil)
	}
}

// A removal that lands while a worker holds the row: the publication is refused
// by the same conditional update that refuses a rejection, and the intermediate
// object the worker produced is discarded rather than left behind.
func TestIntegrationRemovalDuringRenderStopsThePublication(t *testing.T) {
	env := newIntegrationEnv(t)
	svc := env.newService(env.objects)

	view, err := env.uploadContent(t, svc, bytes.NewReader(integrationPNG(t, 120, 120)), "photo.png")
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	env.markScanned(t, view.ID)

	store := storage.NewPGXPreviewStore(env.pool)
	claimed, err := store.ClaimDuePreviews(context.Background(), 1, time.Minute)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim: %d rows, err %v", len(claimed), err)
	}

	env.removeAttachment(t, view.ID)

	recorded, err := store.MarkPreviewReady(context.Background(), service.PreviewResult{
		AttachmentID:    view.ID,
		ClaimAttempt:    claimed[0].Attempts,
		PreviewObjectID: uuid.NewString(),
		Size:            1024,
		WrappedDEK:      []byte{1, 2, 3},
		KEKKeyID:        integrationKeyID,
		EnvelopeVersion: crypto.EnvelopeVersion,
		KeyWrapVersion:  crypto.KeyWrapVersion,
	})
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if recorded {
		t.Fatal("a stale worker published a preview for a removed attachment")
	}
	// The lease is gone with the schedule, so the expiry cannot re-queue it.
	if processed, err := env.previewJobs().ProcessDue(context.Background()); err != nil ||
		processed != 0 {
		t.Fatalf("claimed %d rows after removal, err %v", processed, err)
	}
}

// A preview that had already reached ready is left as it is: the object stays
// under whatever retention policy governs it, and the attachment's own
// visibility is what makes it unreachable.
func TestIntegrationRemovalLeavesAReadyPreviewTerminal(t *testing.T) {
	env := newIntegrationEnv(t)
	svc := env.newService(env.objects)

	view, err := env.uploadContent(t, svc, bytes.NewReader(integrationPNG(t, 120, 120)), "photo.png")
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	env.markScanned(t, view.ID)
	if _, err := env.previewJobs().ProcessDue(context.Background()); err != nil {
		t.Fatalf("process: %v", err)
	}
	if previewStatus, _, _, _, _, _, _, _ := env.previewRow(t, view.ID); previewStatus !=
		string(domain.PreviewStatusReady) {
		t.Fatalf("precondition not met: preview is %q", previewStatus)
	}

	state := env.removeAttachment(t, view.ID)
	if state.PreviewStatus != domain.PreviewStatusReady {
		t.Fatalf("removal rewrote a ready preview to %q", state.PreviewStatus)
	}
	// Unreachable all the same: every read path gates on deleted_at.
	if deletedAt, _, nextAttempt := env.deletionState(t, view.ID); deletedAt == nil ||
		nextAttempt != nil {
		t.Fatalf("deleted=%v scheduled=%v", deletedAt != nil, nextAttempt != nil)
	}
}

// Concurrent removals converge on one state with no partial update.
func TestIntegrationConcurrentRemovalsConverge(t *testing.T) {
	env := newIntegrationEnv(t)
	svc := env.newService(env.objects)

	view, err := env.uploadContent(t, svc, bytes.NewReader(integrationPNG(t, 64, 64)), "photo.png")
	if err != nil {
		t.Fatalf("upload: %v", err)
	}

	const racers = 4
	states := make(chan service.AttachmentLifecycleState, racers)
	errs := make(chan error, racers)
	var wg sync.WaitGroup
	for range racers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			state, err := env.lifecycle.MarkAttachmentDeleted(context.Background(),
				service.AttachmentRemoval{AttachmentID: view.ID, WorkspaceID: env.workspaceID})
			if err != nil {
				errs <- err
				return
			}
			states <- state
		}()
	}
	wg.Wait()
	close(states)
	close(errs)

	for err := range errs {
		t.Fatalf("concurrent removal failed: %v", err)
	}
	for state := range states {
		if state.PreviewStatus != domain.PreviewStatusUnsupported {
			t.Fatalf("a racer observed preview %q", state.PreviewStatus)
		}
	}
	if _, previewStatus, nextAttempt := env.deletionState(t, view.ID); previewStatus !=
		string(domain.PreviewStatusUnsupported) || nextAttempt != nil {
		t.Fatalf("final state %q scheduled=%v", previewStatus, nextAttempt != nil)
	}
}

// A removal aimed at another tenant's row, or at nothing, is refused and
// changes nothing.
func TestIntegrationRemovalRefusesRowsItMustNotTouch(t *testing.T) {
	env := newIntegrationEnv(t)
	svc := env.newService(env.objects)

	view, err := env.uploadContent(t, svc, bytes.NewReader(integrationPNG(t, 64, 64)), "photo.png")
	if err != nil {
		t.Fatalf("upload: %v", err)
	}

	for name, removal := range map[string]service.AttachmentRemoval{
		"another workspace":  {AttachmentID: view.ID, WorkspaceID: uuid.NewString()},
		"unknown attachment": {AttachmentID: uuid.NewString(), WorkspaceID: env.workspaceID},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := env.lifecycle.MarkAttachmentDeleted(context.Background(), removal)
			if !errors.Is(err, domain.ErrNotFound) {
				t.Fatalf("error = %v, want ErrNotFound", err)
			}
		})
	}
	if deletedAt, _, _ := env.deletionState(t, view.ID); deletedAt != nil {
		t.Fatal("a mis-aimed removal deleted the live attachment")
	}
}

// --- migration classification (CQ-001) ------------------------------------

// insertHistoricalRow writes a row the way a build predating migration 000003
// would have, then applies the migration's classification to it. The migration
// itself already ran on this database, so the classification is re-applied to a
// freshly inserted row with the same predicate — which is what proves the
// predicate, not the DDL.
func (e *integrationEnv) insertPreMigrationRow(t *testing.T, status string, deleted bool) string {
	t.Helper()
	id := uuid.NewString()
	var deletedAt any
	if deleted {
		deletedAt = time.Now().UTC()
	}
	if _, err := e.pool.Exec(context.Background(), `
		INSERT INTO files.attachments (
			id, workspace_id, uploader_id, destination_kind, channel_id,
			original_filename, declared_mime, storage_provider, storage_object_key,
			envelope_version, dek_wrap_version, wrapped_dek, kek_key_id,
			status, deleted_at, preview_status, preview_next_attempt_at
		) VALUES ($1, $2, $3, 'channel', $4, 'historic.bin', 'application/octet-stream',
			'seaweedfs', $5, 1, 2, '\x0102', $6, $7, $8, 'pending', NULL)`,
		id, e.workspaceID, e.uploaderID, e.channelID,
		"nchat/attachments/"+id, integrationKeyID, status, deletedAt,
	); err != nil {
		t.Fatalf("insert historical row: %v", err)
	}
	return id
}

// applyMigrationClassification runs migration 000003's backfill predicate.
func (e *integrationEnv) applyMigrationClassification(t *testing.T) {
	t.Helper()
	if _, err := e.pool.Exec(context.Background(), `
		UPDATE files.attachments
		   SET preview_status = 'unsupported',
		       preview_next_attempt_at = NULL
		 WHERE preview_status = 'pending'
		   AND workspace_id = $1
		   AND (
		       deleted_at IS NOT NULL
		       OR status IN ('rejected', 'failed', 'deleted')
		   )`, e.workspaceID); err != nil {
		t.Fatalf("apply classification: %v", err)
	}
}

// Historical rows must not all start pending: a row that can never be claimed
// would sit in the queue forever, counted as a backlog that does not exist.
func TestIntegrationMigrationClassifiesHistoricalRows(t *testing.T) {
	env := newIntegrationEnv(t)

	processable := map[string]string{
		"pending_upload": env.insertPreMigrationRow(t, "pending_upload", false),
		"pending_scan":   env.insertPreMigrationRow(t, "pending_scan", false),
		"clean":          env.insertPreMigrationRow(t, "clean", false),
	}
	terminal := map[string]string{
		"rejected":     env.insertPreMigrationRow(t, "rejected", false),
		"failed":       env.insertPreMigrationRow(t, "failed", false),
		"deleted":      env.insertPreMigrationRow(t, "deleted", false),
		"soft-deleted": env.insertPreMigrationRow(t, "clean", true),
	}

	env.applyMigrationClassification(t)

	for name, id := range processable {
		status, _, _ := env.attachmentState(t, id)
		_ = status
		if _, previewStatus, _ := env.deletionState(t, id); previewStatus !=
			string(domain.PreviewStatusPending) {
			t.Fatalf("%s: preview status = %q, want pending", name, previewStatus)
		}
	}
	for name, id := range terminal {
		_, previewStatus, nextAttempt := env.deletionState(t, id)
		if previewStatus != string(domain.PreviewStatusUnsupported) {
			t.Fatalf("%s: preview status = %q, want unsupported", name, previewStatus)
		}
		if nextAttempt != nil {
			t.Fatalf("%s: a terminal row must not be scheduled", name)
		}
	}

	// And the queue only offers the one row that is actually claimable.
	claimed, err := storage.NewPGXPreviewStore(env.pool).ClaimDuePreviews(
		context.Background(), 10, time.Minute)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	for _, job := range claimed {
		if job.AttachmentID != processable["clean"] {
			t.Fatalf("the claim returned %s, which is not the clean row", job.AttachmentID)
		}
	}
}

// --- claim fencing against PostgreSQL (CQ-002) ----------------------------

// The review's exact scenario, against the real conditional updates.
func TestIntegrationStaleClaimCannotConcludeTheCurrentAttempt(t *testing.T) {
	env := newIntegrationEnv(t)
	svc := env.newService(env.objects)
	store := storage.NewPGXPreviewStore(env.pool)

	view, err := env.uploadContent(t, svc, bytes.NewReader(integrationPNG(t, 64, 64)), "photo.png")
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	env.markScanned(t, view.ID)

	// Worker A claims; its lease expires; worker B claims.
	first, err := store.ClaimDuePreviews(context.Background(), 1, time.Millisecond)
	if err != nil || len(first) != 1 {
		t.Fatalf("first claim: %d rows, err %v", len(first), err)
	}
	time.Sleep(5 * time.Millisecond) //nolint:forbidigo // the lease is a wall-clock interval in PostgreSQL.
	second, err := store.ClaimDuePreviews(context.Background(), 1, time.Minute)
	if err != nil || len(second) != 1 {
		t.Fatalf("second claim: %d rows, err %v", len(second), err)
	}
	if second[0].Attempts <= first[0].Attempts {
		t.Fatalf("claim token did not advance: %d then %d", first[0].Attempts, second[0].Attempts)
	}

	// A concludes with a permanent failure — and must not be allowed to.
	recorded, err := store.MarkPreviewTerminal(
		context.Background(), view.ID, first[0].Attempts, domain.PreviewStatusFailed)
	if err != nil {
		t.Fatalf("stale terminal: %v", err)
	}
	if recorded {
		t.Fatal("a stale attempt concluded the current one's job")
	}

	// B publishes, and its preview is what survives.
	published, err := store.MarkPreviewReady(context.Background(), service.PreviewResult{
		AttachmentID: view.ID, ClaimAttempt: second[0].Attempts,
		PreviewObjectID: uuid.NewString(), Size: 2048,
		WrappedDEK: []byte{1, 2, 3}, KEKKeyID: integrationKeyID,
		EnvelopeVersion: crypto.EnvelopeVersion, KeyWrapVersion: crypto.KeyWrapVersion,
	})
	if err != nil || !published {
		t.Fatalf("the current attempt could not publish: %v %v", published, err)
	}
	if previewStatus, _, _, _, _, _, _, _ := env.previewRow(t, view.ID); previewStatus !=
		string(domain.PreviewStatusReady) {
		t.Fatalf("preview status = %q, want ready", previewStatus)
	}

	// And a stale publish is refused as well.
	stale, err := store.MarkPreviewReady(context.Background(), service.PreviewResult{
		AttachmentID: view.ID, ClaimAttempt: first[0].Attempts,
		PreviewObjectID: uuid.NewString(), Size: 2048,
		WrappedDEK: []byte{1, 2, 3}, KEKKeyID: integrationKeyID,
		EnvelopeVersion: crypto.EnvelopeVersion, KeyWrapVersion: crypto.KeyWrapVersion,
	})
	if err != nil || stale {
		t.Fatalf("a stale attempt published: %v %v", stale, err)
	}
}

// --- the advisory-lock fence against PostgreSQL (SR-001) ------------------

// Two real connections, one attachment: the second acquire must wait for the
// first to release. This is the property a mock cannot demonstrate.
func TestIntegrationAttachmentFenceExcludesAcrossConnections(t *testing.T) {
	env := newIntegrationEnv(t)
	attachmentID := uuid.NewString()

	first, err := env.fence.Acquire(context.Background(), attachmentID)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}

	acquired := make(chan struct{})
	go func() {
		second, err := env.fence.Acquire(context.Background(), attachmentID)
		if err != nil {
			t.Errorf("second acquire: %v", err)
			return
		}
		close(acquired)
		second.Release(context.Background())
	}()

	select {
	case <-acquired:
		t.Fatal("two holders had the same attachment fenced at once")
	case <-time.After(100 * time.Millisecond):
		// Still blocked in PostgreSQL, which is the point.
	}

	first.Release(context.Background())

	select {
	case <-acquired:
	case <-time.After(5 * time.Second):
		t.Fatal("releasing the fence did not unblock the waiter")
	}
}

// Different attachments do not serialise against each other.
func TestIntegrationAttachmentFenceIsPerAttachment(t *testing.T) {
	env := newIntegrationEnv(t)

	first, err := env.fence.Acquire(context.Background(), uuid.NewString())
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	defer first.Release(context.Background())

	done := make(chan struct{})
	go func() {
		defer close(done)
		second, err := env.fence.Acquire(context.Background(), uuid.NewString())
		if err != nil {
			t.Errorf("second acquire: %v", err)
			return
		}
		second.Release(context.Background())
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("unrelated attachments serialised against each other")
	}
}

// A waiter gives up when its context ends, instead of inheriting the wait.
func TestIntegrationAttachmentFenceRespectsCancellation(t *testing.T) {
	env := newIntegrationEnv(t)
	attachmentID := uuid.NewString()

	held, err := env.fence.Acquire(context.Background(), attachmentID)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer held.Release(context.Background())

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err := env.fence.Acquire(ctx, attachmentID); err == nil {
		t.Fatal("a cancelled waiter acquired the fence")
	}
}

// The invalidation really does take the fence: while a fence is held, a
// rejection for that attachment cannot complete.
func TestIntegrationRejectionWaitsForTheAttachmentFence(t *testing.T) {
	env := newIntegrationEnv(t)
	svc := env.newService(env.objects)

	view, err := env.uploadContent(t, svc, bytes.NewReader(integrationPNG(t, 64, 64)), "photo.png")
	if err != nil {
		t.Fatalf("upload: %v", err)
	}

	held, err := env.fence.Acquire(context.Background(), view.ID)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}

	rejected := make(chan error, 1)
	go func() {
		_, err := env.lifecycle.MarkScanRejected(context.Background(), service.ScanRejection{
			AttachmentID: view.ID, WorkspaceID: env.workspaceID,
		})
		rejected <- err
	}()

	select {
	case err := <-rejected:
		t.Fatalf("the rejection did not wait for the fence: %v", err)
	case <-time.After(150 * time.Millisecond):
	}

	held.Release(context.Background())

	select {
	case err := <-rejected:
		if err != nil {
			t.Fatalf("rejection: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the rejection never completed after the fence was released")
	}
	if status, _, _ := env.attachmentState(t, view.ID); status != string(domain.StatusRejected) {
		t.Fatalf("status = %q, want rejected", status)
	}
}

// --- durable object cleanup (SR-002) --------------------------------------

// The end-to-end shape, against real PostgreSQL and a real filer: an object is
// stored, its cleanup is recorded durably, a *new* service — the restart — picks
// the job up, deletes the object and only then forgets it.
func TestIntegrationObjectCleanupSurvivesARestart(t *testing.T) {
	env := newIntegrationEnv(t)
	store := storage.NewPGXObjectCleanupStore(env.pool)

	// An object nothing points at, exactly like a preview whose publication was
	// refused after it had already been uploaded.
	objectKey := domain.PreviewObjectKey(uuid.New())
	if _, err := env.objects.Put(context.Background(), objectKey,
		bytes.NewReader([]byte("an orphaned preview"))); err != nil {
		t.Fatalf("store object: %v", err)
	}
	t.Cleanup(func() { _ = env.objects.Delete(context.Background(), objectKey) })

	if err := store.Enqueue(context.Background(), objectKey); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	// Idempotent: the same failed delete recorded again is still one job.
	if err := store.Enqueue(context.Background(), objectKey); err != nil {
		t.Fatalf("second enqueue: %v", err)
	}

	// A service built now, holding no memory of the enqueue, drains the queue.
	cleanups := service.NewObjectCleanupService(store, env.objects, nil, discardLogger())
	processed, err := cleanups.ProcessDue(context.Background())
	if err != nil {
		t.Fatalf("process: %v", err)
	}
	if processed == 0 {
		t.Fatal("the restarted worker found no job")
	}

	if _, err := env.objects.Open(context.Background(), objectKey); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("the object is still in storage: %v", err)
	}
	var remaining int
	if err := env.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM files.object_cleanup_jobs WHERE object_key = $1`, objectKey,
	).Scan(&remaining); err != nil {
		t.Fatalf("count jobs: %v", err)
	}
	if remaining != 0 {
		t.Fatalf("%d jobs remain after the object was removed", remaining)
	}
}

// A stale claim cannot complete the job a newer one holds, and the claim leases
// the row so two workers never take it at once.
func TestIntegrationCleanupClaimIsLeasedAndFenced(t *testing.T) {
	env := newIntegrationEnv(t)
	store := storage.NewPGXObjectCleanupStore(env.pool)
	objectKey := domain.PreviewObjectKey(uuid.New())

	if err := store.Enqueue(context.Background(), objectKey); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	t.Cleanup(func() {
		_, _ = env.pool.Exec(context.Background(),
			`DELETE FROM files.object_cleanup_jobs WHERE object_key = $1`, objectKey)
	})

	first, err := store.ClaimDueCleanups(context.Background(), 5, time.Minute)
	if err != nil {
		t.Fatalf("first claim: %v", err)
	}
	var claimed *service.ObjectCleanupJob
	for i := range first {
		if first[i].ObjectKey == objectKey {
			claimed = &first[i]
		}
	}
	if claimed == nil {
		t.Fatal("the enqueued job was not claimed")
	}

	// A second worker polling inside the lease must not see it.
	second, err := store.ClaimDueCleanups(context.Background(), 5, time.Minute)
	if err != nil {
		t.Fatalf("second claim: %v", err)
	}
	for _, job := range second {
		if job.ObjectKey == objectKey {
			t.Fatal("a leased cleanup job was claimed twice")
		}
	}

	// A stale attempt cannot complete it. The token is one *ahead* rather than
	// behind, because a first claim is attempt 1 and zero is not a claim at
	// all — the store refuses that as malformed input rather than as a lost
	// race, and both refusals are correct for their own reason.
	completed, err := store.Complete(context.Background(), claimed.ID, claimed.Attempts+1)
	if err != nil {
		t.Fatalf("stale complete: %v", err)
	}
	if completed {
		t.Fatal("a claim that does not own the job completed it")
	}
	// The current one can.
	completed, err = store.Complete(context.Background(), claimed.ID, claimed.Attempts)
	if err != nil || !completed {
		t.Fatalf("the current claim could not complete: %v %v", completed, err)
	}
}

// An object a published preview points at is never deleted, however stale the
// job that names it.
func TestIntegrationCleanupNeverDeletesAReferencedObject(t *testing.T) {
	env := newIntegrationEnv(t)
	svc := env.newService(env.objects)
	store := storage.NewPGXObjectCleanupStore(env.pool)

	view, err := env.uploadContent(t, svc, bytes.NewReader(integrationPNG(t, 64, 64)), "photo.png")
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	env.markScanned(t, view.ID)
	if _, err := env.previewJobs().ProcessDue(context.Background()); err != nil {
		t.Fatalf("render: %v", err)
	}
	previewStatus, objectID, _, _, _, _, _, _ := env.previewRow(t, view.ID)
	if previewStatus != string(domain.PreviewStatusReady) {
		t.Fatalf("precondition not met: preview is %q", previewStatus)
	}
	liveKey := domain.PreviewObjectKey(uuid.MustParse(objectID))

	// A stale job names the object of a preview that is now live.
	if err := store.Enqueue(context.Background(), liveKey); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	referenced, err := store.IsObjectReferenced(context.Background(), liveKey)
	if err != nil {
		t.Fatalf("reference check: %v", err)
	}
	if !referenced {
		t.Fatal("a published preview's object must read as referenced")
	}

	cleanups := service.NewObjectCleanupService(store, env.objects, nil, discardLogger())
	if _, err := cleanups.ProcessDue(context.Background()); err != nil {
		t.Fatalf("process: %v", err)
	}

	// The preview still opens, which is the only assertion that matters.
	if _, err := env.objects.Open(context.Background(), liveKey); err != nil {
		t.Fatalf("the cleanup worker deleted a live preview: %v", err)
	}
}

// --- The object lifecycle across invalidation, through the real worker -------
//
// Everything below drives PreviewService.ProcessDue rather than calling the
// store's claim and publish directly. That distinction is the point: a test
// that claims and publishes by hand proves the *statements* refuse what they
// should, but says nothing about whether the flow can reach them. Two of the
// findings this section answers were invisible for exactly that reason.

// renderHook runs fn while the renderer holds the attachment's plaintext, then
// renders normally. It is how a test lands an event *inside* the render window
// without stubbing out the render itself.
type renderHook struct {
	inner service.PreviewRenderer
	once  sync.Once
	fn    func()
}

func (r *renderHook) Render(ctx context.Context, detectedMIME string, src io.Reader) ([]byte, error) {
	r.once.Do(r.fn)
	return r.inner.Render(ctx, detectedMIME, src)
}

// lostFence models a session advisory lock that is no longer held: the
// connection carrying it dropped, PostgreSQL released it, and the worker — which
// has no way to notice — carries on rendering.
//
// It is the one condition under which an invalidation can commit *during* a
// render in production, and therefore the only way to reach the publishing
// statement's own re-assertion of the scan gate. Modelling it is legitimate
// precisely because AttachmentFence is an interface; the code under test is the
// unmodified production path in PreviewService.process.
type lostFence struct{}

func (lostFence) Acquire(context.Context, string) (service.FenceHandle, error) {
	return noopFenceHandle{}, nil
}

type noopFenceHandle struct{}

func (noopFenceHandle) Release(context.Context) {}

// previewJobsWith builds the real preview use case with a substituted renderer
// and fence. Everything else — the store, the live filer, the real keyring, the
// durable cleanup queue — is production wiring.
func (e *integrationEnv) previewJobsWith(
	renderer service.PreviewRenderer, fence service.AttachmentFence,
) *service.PreviewService {
	return service.NewPreviewService(
		storage.NewPGXPreviewStore(e.pool), e.objects, e.keys,
		renderer, fence, storage.NewPGXObjectCleanupStore(e.pool), nil, discardLogger(),
	)
}

// cleanupJobs wires the cleanup worker against the live database and filer.
func (e *integrationEnv) cleanupJobs() *service.ObjectCleanupService {
	return service.NewObjectCleanupService(
		storage.NewPGXObjectCleanupStore(e.pool), e.objects, nil, discardLogger(),
	)
}

// queuedCleanupKeys reads the durable queue back.
func (e *integrationEnv) queuedCleanupKeys(t *testing.T) []string {
	t.Helper()
	rows, err := e.pool.Query(context.Background(),
		`SELECT object_key FROM files.object_cleanup_jobs ORDER BY object_key`)
	if err != nil {
		t.Fatalf("read cleanup queue: %v", err)
	}
	defer rows.Close()
	var keys []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			t.Fatalf("scan cleanup job: %v", err)
		}
		keys = append(keys, key)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate cleanup queue: %v", err)
	}
	return keys
}

// objectPresent reports whether SeaweedFS still holds the key.
func (e *integrationEnv) objectPresent(t *testing.T, key string) bool {
	t.Helper()
	body, err := e.objects.Open(context.Background(), key)
	if err != nil {
		return false
	}
	_ = body.Close()
	return true
}

// countQueued reports how many jobs name this key.
//
// The queue is keyed by object alone — it has no workspace column, because a
// storage key is all a delete needs — so assertions here count occurrences of
// the key under test rather than the size of the whole table. Another run's
// leftovers must not decide whether this one passes.
func (e *integrationEnv) countQueued(t *testing.T, key string) int {
	t.Helper()
	count := 0
	for _, candidate := range e.queuedCleanupKeys(t) {
		if candidate == key {
			count++
		}
	}
	return count
}

// requireQueued fails unless the key is waiting for cleanup exactly once.
func (e *integrationEnv) requireQueued(t *testing.T, key string) {
	t.Helper()
	if count := e.countQueued(t, key); count != 1 {
		t.Fatalf("%q is queued %d times, want exactly once", key, count)
	}
}

// generateReadyPreview runs the real worker to completion and returns the
// published preview's storage key.
func (e *integrationEnv) generateReadyPreview(t *testing.T, attachmentID string) string {
	t.Helper()
	if processed, err := e.previewJobs().ProcessDue(context.Background()); err != nil || processed != 1 {
		t.Fatalf("generate preview: processed %d, err %v", processed, err)
	}
	status, objectID, _, _, _, _, _, _ := e.previewRow(t, attachmentID)
	if status != string(domain.PreviewStatusReady) {
		t.Fatalf("preview status = %q, want ready", status)
	}
	key := domain.PreviewObjectKey(uuid.MustParse(objectID))
	if !e.objectPresent(t, key) {
		t.Fatal("the published preview object is not in storage")
	}
	return key
}

// Finding 1: a removed attachment used to leave its preview object behind
// forever — unreachable to every read path, and invisible to the cleanup worker,
// which saw the row still pointing at the key and called it referenced.
func TestIntegrationRemovalReclaimsAReadyPreviewObject(t *testing.T) {
	env := newIntegrationEnv(t)
	svc := env.newService(env.objects)

	view, err := env.uploadContent(t, svc, bytes.NewReader(integrationPNG(t, 120, 120)), "photo.png")
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	env.markScanned(t, view.ID)
	key := env.generateReadyPreview(t, view.ID)

	// The removal and the job that reclaims its object are one statement.
	env.removeAttachment(t, view.ID)
	env.requireQueued(t, key)

	// A removed attachment references nothing: that is what lets the worker act.
	cleanupStore := storage.NewPGXObjectCleanupStore(env.pool)
	referenced, err := cleanupStore.IsObjectReferenced(context.Background(), key)
	if err != nil {
		t.Fatalf("reference check: %v", err)
	}
	if referenced {
		t.Fatal("a removed attachment must not hold its preview object referenced")
	}

	if _, err := env.cleanupJobs().ProcessDue(context.Background()); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if env.objectPresent(t, key) {
		t.Fatal("the preview object of a removed attachment is still in storage")
	}
	if count := env.countQueued(t, key); count != 0 {
		t.Fatalf("the finished job is still queued %d times", count)
	}
}

// The same path, with the delete failing first: the job has to survive the
// failure and succeed on a later pass, or the leak is only postponed.
func TestIntegrationRemovalCleanupSurvivesAFailedDelete(t *testing.T) {
	env := newIntegrationEnv(t)
	svc := env.newService(env.objects)

	view, err := env.uploadContent(t, svc, bytes.NewReader(integrationPNG(t, 120, 120)), "photo.png")
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	env.markScanned(t, view.ID)
	key := env.generateReadyPreview(t, view.ID)
	env.removeAttachment(t, view.ID)

	// A worker whose storage is unreachable must leave the job exactly where it
	// was, not conclude it.
	failing := service.NewObjectCleanupService(
		storage.NewPGXObjectCleanupStore(env.pool), unreachableObjects{}, nil, discardLogger(),
	)
	if _, err := failing.ProcessDue(context.Background()); err != nil {
		t.Fatalf("cleanup pass: %v", err)
	}
	env.requireQueued(t, key)
	if !env.objectPresent(t, key) {
		t.Fatal("a failed delete must not remove the object")
	}

	// The lease has to expire before the next attempt may claim it, which is the
	// retry the finding asks for.
	if _, err := env.pool.Exec(context.Background(),
		`UPDATE files.object_cleanup_jobs SET next_attempt_at = now() - interval '1 minute'
		  WHERE object_key = $1`, key); err != nil {
		t.Fatalf("expire the lease: %v", err)
	}
	if _, err := env.cleanupJobs().ProcessDue(context.Background()); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if env.objectPresent(t, key) {
		t.Fatal("the retry did not remove the object")
	}
}

// unreachableObjects fails every delete, the way a storage outage would.
type unreachableObjects struct{}

func (unreachableObjects) Put(context.Context, string, io.Reader) (int64, error) {
	return 0, errors.New("storage unavailable")
}

func (unreachableObjects) Open(context.Context, string) (io.ReadCloser, error) {
	return nil, errors.New("storage unavailable")
}

func (unreachableObjects) OpenRange(context.Context, string, int64) (io.ReadCloser, error) {
	return nil, errors.New("storage unavailable")
}

func (unreachableObjects) Delete(context.Context, string) error {
	return errors.New("storage unavailable")
}

// SR-001, the ordering the fence actually produces: the worker wins the race,
// publishes, and the verdict lands immediately after.
//
// The reviewer is right that this is what happens — the fence is held across the
// render *and* the publish, so a verdict cannot commit in between. The answer is
// not to make that interleaving possible but to make it harmless: the rejection
// reclaims the object the worker just published, in the statement that rejects.
// Both orderings therefore converge on the same end state, which is the property
// that actually matters.
func TestIntegrationAVerdictRacingTheRenderReclaimsThePublishedObject(t *testing.T) {
	env := newIntegrationEnv(t)
	svc := env.newService(env.objects)

	view, err := env.uploadContent(t, svc, bytes.NewReader(integrationPNG(t, 120, 120)), "photo.png")
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	env.markScanned(t, view.ID)

	// The verdict is issued while the renderer holds the plaintext. It blocks on
	// the fence — which is the whole design — and commits the moment the worker
	// lets go.
	rejected := make(chan struct{})
	hooked := &renderHook{inner: preview.New(), fn: func() {
		go func() {
			defer close(rejected)
			env.rejectScan(t, view.ID)
		}()
		// Give the verdict time to reach the fence and block on it, so this is a
		// real contention rather than a sequential call that happens to be late.
		time.Sleep(200 * time.Millisecond)
	}}

	if processed, err := env.previewJobsWith(hooked, env.fence).ProcessDue(context.Background()); err != nil ||
		processed != 1 {
		t.Fatalf("process: processed %d, err %v", processed, err)
	}
	<-rejected

	// Whichever side won, the attachment is condemned and nothing is servable.
	status, previewStatus, nextAttempt := env.attachmentState(t, view.ID)
	if status != string(domain.StatusRejected) {
		t.Fatalf("status = %q, want rejected", status)
	}
	if nextAttempt != nil {
		t.Fatalf("a rejected attachment must not stay scheduled, got %v", nextAttempt)
	}

	// And no object is left behind. If the worker published before the verdict,
	// the rejection queued that object; if the verdict landed first, the worker
	// never published one.
	_, objectID, _, _, _, _, _, _ := env.previewRow(t, view.ID)
	if previewStatus != string(domain.PreviewStatusReady) {
		// The verdict won: nothing was published, so there is nothing to reclaim.
		if objectID != "" {
			t.Fatalf("no preview was published, yet the row points at %q", objectID)
		}
		return
	}
	key := domain.PreviewObjectKey(uuid.MustParse(objectID))
	env.requireQueued(t, key)
	if _, err := env.cleanupJobs().ProcessDue(context.Background()); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if env.objectPresent(t, key) {
		t.Fatal("the preview object of a rejected attachment survived the cleanup")
	}
}

// SR-001, the ordering that reaches the publishing statement's own scan gate.
//
// A session advisory lock lives on a connection, so losing that connection
// releases the fence while the worker keeps rendering. That is the one way an
// invalidation commits mid-render in production, and it is what makes the
// re-assertion in MarkPreviewReady load-bearing rather than decorative. This
// drives the real ProcessDue — the refusal happens inside it, not in a store
// call the test wrote.
func TestIntegrationProcessDueRefusesToPublishWhenTheFenceWasLost(t *testing.T) {
	env := newIntegrationEnv(t)
	svc := env.newService(env.objects)

	view, err := env.uploadContent(t, svc, bytes.NewReader(integrationPNG(t, 120, 120)), "photo.png")
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	env.markScanned(t, view.ID)

	// With the fence gone, the verdict commits during the render instead of
	// queuing behind it.
	hooked := &renderHook{inner: preview.New(), fn: func() {
		env.rejectScan(t, view.ID)
	}}

	before := len(env.queuedCleanupKeys(t))
	if processed, err := env.previewJobsWith(hooked, lostFence{}).ProcessDue(context.Background()); err != nil ||
		processed != 1 {
		t.Fatalf("process: processed %d, err %v", processed, err)
	}

	// The publication was refused by the statement, not by the fence.
	status, previewStatus, nextAttempt := env.attachmentState(t, view.ID)
	if status != string(domain.StatusRejected) {
		t.Fatalf("status = %q, want rejected", status)
	}
	if previewStatus == string(domain.PreviewStatusReady) {
		t.Fatal("a preview was published for an attachment the scan had already rejected")
	}
	if nextAttempt != nil {
		t.Fatalf("the row must not stay scheduled, got %v", nextAttempt)
	}

	// The object the refused attempt produced does not survive. It is deleted
	// outright on the compensation path, so nothing new should even reach the
	// queue — and the row points at no object at all, which is what makes the
	// refusal complete rather than merely recorded.
	if after := len(env.queuedCleanupKeys(t)); after != before {
		t.Fatalf("the refused publication queued %d jobs; its object should have been deleted outright",
			after-before)
	}
	if _, objectID, _, _, _, _, _, _ := env.previewRow(t, view.ID); objectID != "" {
		t.Fatalf("the row points at preview object %q it never published", objectID)
	}
}

// A rejection after a preview was published reclaims its object, and does so
// exactly once however many times the verdict arrives.
func TestIntegrationRepeatedRejectionQueuesTheObjectOnce(t *testing.T) {
	env := newIntegrationEnv(t)
	svc := env.newService(env.objects)

	view, err := env.uploadContent(t, svc, bytes.NewReader(integrationPNG(t, 120, 120)), "photo.png")
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	env.markScanned(t, view.ID)
	key := env.generateReadyPreview(t, view.ID)

	env.rejectScan(t, view.ID)
	env.rejectScan(t, view.ID)
	env.rejectScan(t, view.ID)

	// Three verdicts, one job: ON CONFLICT DO NOTHING is what keeps a repeated
	// rejection from queueing the same key again.
	if count := env.countQueued(t, key); count != 1 {
		t.Fatalf("three rejections queued %q %d times, want once", key, count)
	}

	// The preview keeps saying ready — the record that one existed — while its
	// object goes. Delivery was already refused by the attachment's status.
	if _, previewStatus, _ := env.attachmentState(t, view.ID); previewStatus !=
		string(domain.PreviewStatusReady) {
		t.Fatalf("preview status = %q, want the terminal state preserved", previewStatus)
	}
	if _, err := env.cleanupJobs().ProcessDue(context.Background()); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if env.objectPresent(t, key) {
		t.Fatal("the object survived the rejection")
	}
}

// The reference check is the delivery gate, expressed once. A live preview is
// referenced; the same row rejected or removed is not.
func TestIntegrationOnlyAServablePreviewCountsAsReferenced(t *testing.T) {
	env := newIntegrationEnv(t)
	svc := env.newService(env.objects)
	cleanupStore := storage.NewPGXObjectCleanupStore(env.pool)

	view, err := env.uploadContent(t, svc, bytes.NewReader(integrationPNG(t, 120, 120)), "photo.png")
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	env.markScanned(t, view.ID)
	key := env.generateReadyPreview(t, view.ID)

	referenced, err := cleanupStore.IsObjectReferenced(context.Background(), key)
	if err != nil {
		t.Fatalf("reference check: %v", err)
	}
	if !referenced {
		t.Fatal("a servable preview must read as referenced")
	}

	env.rejectScan(t, view.ID)
	referenced, err = cleanupStore.IsObjectReferenced(context.Background(), key)
	if err != nil {
		t.Fatalf("reference check after rejection: %v", err)
	}
	if referenced {
		t.Fatal("a rejected attachment's preview must not read as referenced")
	}
}

// The SQL that derives a preview's key and the Go function that derives it must
// agree, or the queue fills with keys that match no object while live previews
// read as unreferenced. Neither failure is loud, so it is asserted here against
// the real database.
func TestIntegrationPreviewObjectKeyExprMatchesDomain(t *testing.T) {
	env := newIntegrationEnv(t)
	svc := env.newService(env.objects)

	view, err := env.uploadContent(t, svc, bytes.NewReader(integrationPNG(t, 120, 120)), "photo.png")
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	env.markScanned(t, view.ID)
	key := env.generateReadyPreview(t, view.ID)

	// The removal derives the key in SQL; domain.PreviewObjectKey derived the one
	// the object was actually stored under. If the two ever drift, the job names
	// a key no object has and this finds nothing.
	env.removeAttachment(t, view.ID)
	if count := env.countQueued(t, key); count != 1 {
		t.Fatalf("the SQL-derived key does not match %q (found %d)", key, count)
	}
}
