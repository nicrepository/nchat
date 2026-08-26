package service_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/nicrepository/nchat/services/file-service/internal/crypto"
	"github.com/nicrepository/nchat/services/file-service/internal/domain"
	"github.com/nicrepository/nchat/services/file-service/internal/preview"
	"github.com/nicrepository/nchat/services/file-service/internal/service"
)

// --- fakes --------------------------------------------------------------

// fakePreviewStore models the claim semantics of files.attachments closely
// enough to run a job across several passes: a preview state, the scan verdict
// the claim filters on, an attempt counter and a lease, all moved by a clock the
// test controls.
//
// It models behaviour rather than recording calls because the properties under
// test are multi-pass ones — a lease that has not expired, a render budget that
// is spent, a terminal write that failed and is retried later — and none of
// them is observable from a single invocation.
type fakePreviewStore struct {
	mu  sync.Mutex
	now time.Time

	rows  []*fakePreviewRow
	order int

	claimErr    error
	readyErr    error
	terminalErr error
	// readyLost makes the conditional update report "no row", which is what a
	// concurrent attempt finishing first — or a scan verdict landing during the
	// render — looks like to the caller.
	readyLost bool

	claimCalls int
	claimBatch int
	claimLease time.Duration

	ready    []service.PreviewResult
	terminal []domain.PreviewStatus
}

// fakePreviewRow is one attachment's preview state.
type fakePreviewRow struct {
	job           service.PreviewJob
	status        domain.PreviewStatus
	scanClean     bool
	deleted       bool
	attempts      int
	nextAttemptAt time.Time
	sequence      int
}

func newFakePreviewStore() *fakePreviewStore {
	return &fakePreviewStore{now: time.Now()}
}

// enqueueUnscanned adds a pending row the scan has not approved, which is what
// every upload looks like while FILE_MALWARE_SCAN_REQUIRED is on and the
// scanner has not yet ruled.
func (s *fakePreviewStore) enqueueUnscanned(job service.PreviewJob) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.order++
	s.rows = append(s.rows, &fakePreviewRow{
		job: job, status: domain.PreviewStatusPending, scanClean: false, sequence: s.order,
	})
}

// enqueue adds a pending, scan-approved row, the state MarkUploaded leaves.
func (s *fakePreviewStore) enqueue(job service.PreviewJob) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.order++
	s.rows = append(s.rows, &fakePreviewRow{
		job: job, status: domain.PreviewStatusPending, scanClean: true, sequence: s.order,
	})
}

// advance moves the clock, which is how a test expires a lease.
func (s *fakePreviewStore) advance(d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.now = s.now.Add(d)
}

// reject models the scanner condemning an attachment, applying the same
// transition PGXAttachmentStore.MarkScanRejected applies in one statement: the
// verdict *and* the finalisation of a still-pending preview, including its
// schedule. Modelling only the verdict here is what let SR-001 pass unnoticed.
func (s *fakePreviewStore) reject(attachmentID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, row := range s.rows {
		if row.job.AttachmentID != attachmentID {
			continue
		}
		row.scanClean = false
		if row.status == domain.PreviewStatusPending {
			row.status = domain.PreviewStatusUnsupported
			row.nextAttemptAt = time.Time{}
		}
	}
}

// remove models MarkAttachmentDeleted: the row is gone and a pending preview is
// finalised in the same breath.
func (s *fakePreviewStore) remove(attachmentID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, row := range s.rows {
		if row.job.AttachmentID != attachmentID {
			continue
		}
		row.deleted = true
		if row.status == domain.PreviewStatusPending {
			row.status = domain.PreviewStatusUnsupported
			row.nextAttemptAt = time.Time{}
		}
	}
}

func (s *fakePreviewStore) row(t *testing.T, attachmentID string) fakePreviewRow {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, row := range s.rows {
		if row.job.AttachmentID == attachmentID {
			return *row
		}
	}
	t.Fatalf("no row for %s", attachmentID)
	return fakePreviewRow{}
}

func (s *fakePreviewStore) ClaimDuePreviews(
	_ context.Context, batchSize int, lease time.Duration,
) ([]service.PreviewJob, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.claimCalls++
	s.claimBatch, s.claimLease = batchSize, lease
	if s.claimErr != nil {
		return nil, s.claimErr
	}
	var claimed []service.PreviewJob
	for _, row := range s.rows {
		if len(claimed) >= batchSize {
			break
		}
		// The eligibility of the real claim: pending, scan-approved, and due.
		if row.status != domain.PreviewStatusPending || !row.scanClean || row.deleted {
			continue
		}
		if row.nextAttemptAt.After(s.now) {
			continue
		}
		row.attempts++
		row.nextAttemptAt = s.now.Add(lease)
		job := row.job
		job.Attempts = row.attempts
		claimed = append(claimed, job)
	}
	return claimed, nil
}

func (s *fakePreviewStore) MarkPreviewReady(
	_ context.Context, result service.PreviewResult,
) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.readyErr != nil {
		return false, s.readyErr
	}
	for _, row := range s.rows {
		if row.job.AttachmentID != result.AttachmentID {
			continue
		}
		// The conditional update: still pending, scan still clean, not removed,
		// and claimed by the attempt that is publishing.
		if s.readyLost || row.status != domain.PreviewStatusPending || !row.scanClean ||
			row.deleted || row.attempts != result.ClaimAttempt {
			return false, nil
		}
		row.status = domain.PreviewStatusReady
		s.ready = append(s.ready, result)
		return true, nil
	}
	return false, nil
}

// RevalidateClaim answers the question the fence makes meaningful: is this
// claim still the current one, and is the attachment still renderable.
func (s *fakePreviewStore) RevalidateClaim(
	_ context.Context, attachmentID string, claimAttempt int,
) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, row := range s.rows {
		if row.job.AttachmentID != attachmentID {
			continue
		}
		return row.scanClean && !row.deleted &&
			row.status == domain.PreviewStatusPending && row.attempts == claimAttempt, nil
	}
	return false, nil
}

func (s *fakePreviewStore) MarkPreviewTerminal(
	_ context.Context, attachmentID string, claimAttempt int, status domain.PreviewStatus,
) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.terminalErr != nil {
		return false, s.terminalErr
	}
	for _, row := range s.rows {
		if row.job.AttachmentID != attachmentID {
			continue
		}
		// The fence: only the claim that currently owns the row concludes it.
		if row.status != domain.PreviewStatusPending || row.attempts != claimAttempt {
			return false, nil
		}
		row.status = status
		s.terminal = append(s.terminal, status)
		return true, nil
	}
	return false, nil
}

func (s *fakePreviewStore) recorded() ([]service.PreviewResult, []domain.PreviewStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]service.PreviewResult(nil), s.ready...),
		append([]domain.PreviewStatus(nil), s.terminal...)
}

// fakeFence is a real mutual exclusion — a channel used as a one-slot
// semaphore — so ordering tests are deterministic without a sleep anywhere.
type fakeFence struct {
	slots chan struct{}

	mu       sync.Mutex
	acquired int
	released int
	// acquireErr makes the fence unavailable, which must stop a render rather
	// than let one proceed unfenced.
	acquireErr error
	// onAcquire runs while the fence is held, so a test can order events
	// against the exact window the render occupies.
	onAcquire func()
}

func newFakeFence() *fakeFence {
	return &fakeFence{slots: make(chan struct{}, 1)}
}

func (f *fakeFence) Acquire(ctx context.Context, _ string) (service.FenceHandle, error) {
	f.mu.Lock()
	err, hook := f.acquireErr, f.onAcquire
	f.mu.Unlock()
	if err != nil {
		return nil, err
	}
	select {
	case f.slots <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	f.mu.Lock()
	f.acquired++
	f.mu.Unlock()
	if hook != nil {
		hook()
	}
	return &fakeFenceHandle{fence: f}, nil
}

func (f *fakeFence) counts() (acquired, released int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.acquired, f.released
}

// held reports whether the fence is currently taken, which is how a test proves
// an invalidation is *waiting* rather than merely slow.
func (f *fakeFence) held() bool { return len(f.slots) == 1 }

type fakeFenceHandle struct {
	fence *fakeFence
	once  sync.Once
}

func (h *fakeFenceHandle) Release(context.Context) {
	h.once.Do(func() {
		h.fence.mu.Lock()
		h.fence.released++
		h.fence.mu.Unlock()
		<-h.fence.slots
	})
}

// fakeCleanupQueue is the durable queue a failed delete falls back to.
type fakeCleanupQueue struct {
	mu   sync.Mutex
	keys []string
	err  error
}

func (q *fakeCleanupQueue) Enqueue(_ context.Context, objectKey string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.err != nil {
		return q.err
	}
	q.keys = append(q.keys, objectKey)
	return nil
}

func (q *fakeCleanupQueue) queued() []string {
	q.mu.Lock()
	defer q.mu.Unlock()
	return append([]string(nil), q.keys...)
}

// stubRenderer stands in for the decoders. The job must not care what they do
// beyond the two error classes they promise.
type stubRenderer struct {
	mu sync.Mutex

	// pages is what Render returns on success. A single-element slice models
	// every image and every one-page PDF; a longer one models a multi-page
	// PDF.
	pages [][]byte
	err   error
	// duringRender runs while the renderer is "working", so a test can change
	// the world underneath a job exactly as a scan verdict would.
	duringRender func()

	calls     int
	lastMIME  string
	lastBytes []byte
}

func (r *stubRenderer) Render(_ context.Context, detectedMIME string, src io.Reader) ([][]byte, error) {
	// Always drain: the real renderer reads the decrypting stream, so an
	// integrity failure has to surface here exactly as it would in production.
	read, readErr := io.ReadAll(src)
	r.mu.Lock()
	hook := r.duringRender
	r.mu.Unlock()
	if hook != nil {
		// Outside the lock: the hook touches the store, not the renderer.
		hook()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	r.lastMIME, r.lastBytes = detectedMIME, read
	if readErr != nil {
		return nil, readErr
	}
	if r.err != nil {
		return nil, r.err
	}
	return r.pages, nil
}

func (r *stubRenderer) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

func (r *stubRenderer) source() []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]byte(nil), r.lastBytes...)
}

// previewObserver records the closed result vocabulary the worker emits.
type previewObserver struct {
	countingOrphans
	mu      sync.Mutex
	results []string
}

func (o *previewObserver) ObservePreview(result string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.results = append(o.results, result)
}

func (o *previewObserver) observed() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.results...)
}

// cleanupObserver records what the *cleanup* worker emits.
//
// It is a distinct type from previewObserver, and deliberately does not
// implement ObservePreview: that is what makes it a compile-time proof that the
// cleanup worker cannot reach the preview counter. Wiring the two together was
// the defect — a storage outage showed up as previews failing to render.
type cleanupObserver struct {
	mu      sync.Mutex
	results []string
}

func (o *cleanupObserver) ObserveCleanup(result string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.results = append(o.results, result)
}

func (o *cleanupObserver) observed() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.results...)
}

// previewFixture wires the job with in-memory dependencies and real crypto, so
// what it stores is a real envelope that has to open with a real key.
type previewFixture struct {
	store    *fakePreviewStore
	objects  *fakeObjects
	renderer *stubRenderer
	fence    *fakeFence
	cleanup  *fakeCleanupQueue
	observer *previewObserver
	keys     *crypto.Keyring
	service  *service.PreviewService
}

func newPreviewFixture(t *testing.T) *previewFixture {
	t.Helper()
	f := &previewFixture{
		store:    newFakePreviewStore(),
		objects:  newFakeObjects(),
		renderer: &stubRenderer{pages: [][]byte{[]byte("a rendered jpeg, near enough")}},
		fence:    newFakeFence(),
		cleanup:  &fakeCleanupQueue{},
		observer: &previewObserver{},
		keys:     testKeyring(t),
	}
	f.service = service.NewPreviewService(
		f.store, f.objects, f.keys, f.renderer, f.fence, f.cleanup, f.observer, discardLogger(),
	)
	return f
}

// storeAttachment writes a real encrypted attachment object and returns the job
// that points at it, exactly as the claim query would have built it.
func (f *previewFixture) storeAttachment(t *testing.T, plaintext []byte, detectedMIME string) service.PreviewJob {
	t.Helper()
	attachmentID, workspaceID := uuid.New(), uuid.MustParse(testWorkspaceID)

	dataKey, err := crypto.NewDataKey()
	if err != nil {
		t.Fatalf("data key: %v", err)
	}
	encrypted, err := crypto.NewEncryptingReader(bytes.NewReader(plaintext), dataKey, attachmentID)
	if err != nil {
		t.Fatalf("encrypting reader: %v", err)
	}
	objectKey := domain.StorageObjectKey(attachmentID)
	if _, err := f.objects.Put(context.Background(), objectKey, encrypted); err != nil {
		t.Fatalf("store attachment object: %v", err)
	}
	wrapped, keyID, err := f.keys.Wrap(dataKey, crypto.Binding{
		AttachmentID:           attachmentID,
		WorkspaceID:            workspaceID,
		PlaintextSize:          int64(len(plaintext)),
		KeyWrapVersion:         crypto.KeyWrapVersion,
		ContentEnvelopeVersion: crypto.EnvelopeVersion,
	})
	if err != nil {
		t.Fatalf("wrap: %v", err)
	}
	return service.PreviewJob{
		AttachmentID:     attachmentID.String(),
		WorkspaceID:      testWorkspaceID,
		DetectedMIME:     detectedMIME,
		Size:             int64(len(plaintext)),
		StorageObjectKey: objectKey,
		EnvelopeVersion:  crypto.EnvelopeVersion,
		WrappedDEK:       wrapped,
		KEKKeyID:         keyID,
		KeyWrapVersion:   crypto.KeyWrapVersion,
		Attempts:         1,
	}
}

// openRecordedPreview decrypts a stored preview using only what was persisted,
// which is the real assertion: a preview that cannot be opened from its own
// recorded binding is not a preview.
func (f *previewFixture) openRecordedPreview(t *testing.T, result service.PreviewResult) []byte {
	t.Helper()
	previewID, err := uuid.Parse(result.PreviewObjectID)
	if err != nil {
		t.Fatalf("preview object id: %v", err)
	}
	dataKey, err := f.keys.Unwrap(result.WrappedDEK, result.KEKKeyID, crypto.Binding{
		AttachmentID:           previewID,
		WorkspaceID:            uuid.MustParse(testWorkspaceID),
		PlaintextSize:          result.Size,
		KeyWrapVersion:         result.KeyWrapVersion,
		ContentEnvelopeVersion: result.EnvelopeVersion,
	})
	if err != nil {
		t.Fatalf("unwrap preview key: %v", err)
	}
	stored, err := f.objects.Open(context.Background(), domain.PreviewObjectKey(previewID))
	if err != nil {
		t.Fatalf("open preview object: %v", err)
	}
	defer func() { _ = stored.Close() }()

	plaintext, err := crypto.NewDecryptingReader(stored, dataKey, previewID, result.Size)
	if err != nil {
		t.Fatalf("decrypting reader: %v", err)
	}
	decrypted, err := io.ReadAll(plaintext)
	if err != nil {
		t.Fatalf("read preview plaintext: %v", err)
	}
	return decrypted
}

// --- the job ------------------------------------------------------------

func TestProcessDueStoresAnEncryptedPreviewAndRecordsIt(t *testing.T) {
	f := newPreviewFixture(t)
	job := f.storeAttachment(t, []byte("the original attachment bytes"), "image/png")
	f.store.enqueue(job)

	processed, err := f.service.ProcessDue(context.Background())
	if err != nil {
		t.Fatalf("process: %v", err)
	}
	if processed != 1 {
		t.Fatalf("processed = %d, want 1", processed)
	}

	// The renderer sees the *plaintext* of the attachment, decrypted through
	// the same path a download uses.
	if got := string(f.renderer.source()); got != "the original attachment bytes" {
		t.Fatalf("renderer saw %q", got)
	}

	ready, terminal := f.store.recorded()
	if len(ready) != 1 || len(terminal) != 0 {
		t.Fatalf("recorded ready=%v terminal=%v", ready, terminal)
	}
	result := ready[0]
	if result.AttachmentID != job.AttachmentID {
		t.Fatalf("preview recorded against %q, want %q", result.AttachmentID, job.AttachmentID)
	}
	if result.PreviewObjectID == job.AttachmentID {
		t.Fatal("the preview must have its own identity, not the attachment's")
	}
	if decrypted := string(f.openRecordedPreview(t, result)); decrypted != string(f.renderer.pages[0]) {
		t.Fatalf("stored preview decrypts to %q", decrypted)
	}
	if results := f.observer.observed(); len(results) != 1 || results[0] != "ready" {
		t.Fatalf("observed %v, want one ready", results)
	}
}

// A multi-page render (a PDF, in production) must persist every page as its
// own object and record the whole set — page one on the result itself, the
// rest in ExtraPages — in the one write that publishes the preview.
func TestProcessDuePersistsEveryRenderedPageAndRecordsThePageCount(t *testing.T) {
	f := newPreviewFixture(t)
	f.renderer.pages = [][]byte{
		[]byte("page one bytes"), []byte("page two bytes"), []byte("page three bytes"),
	}
	job := f.storeAttachment(t, []byte("a three page pdf, near enough"), "application/pdf")
	f.store.enqueue(job)

	if _, err := f.service.ProcessDue(context.Background()); err != nil {
		t.Fatalf("process: %v", err)
	}

	ready, _ := f.store.recorded()
	if len(ready) != 1 {
		t.Fatalf("recorded %d ready results, want 1", len(ready))
	}
	result := ready[0]
	if result.PageCount != 3 {
		t.Fatalf("page count = %d, want 3", result.PageCount)
	}
	if len(result.ExtraPages) != 2 {
		t.Fatalf("extra pages = %d, want 2", len(result.ExtraPages))
	}
	for i, page := range result.ExtraPages {
		if want := i + 2; page.PageNumber != want {
			t.Fatalf("extra page %d has number %d, want %d", i, page.PageNumber, want)
		}
		if page.ObjectID == result.PreviewObjectID {
			t.Fatal("an extra page must not share page one's object identity")
		}
	}
	if a, b := result.ExtraPages[0].ObjectID, result.ExtraPages[1].ObjectID; a == b {
		t.Fatal("the two extra pages must not share an object identity")
	}
	// The attachment's own object, plus one stored object per rendered page.
	if got := f.objects.count(); got != 4 {
		t.Fatalf("stored objects = %d, want 4 (1 attachment + 3 pages)", got)
	}
}

// A publish that loses the race — the row was claimed by a newer attempt, or
// the scan condemned the attachment mid-render — must discard every page this
// attempt produced, not only the first. A partial discard would leak every
// object beyond page one.
func TestProcessDueDiscardsEveryPageWhenThePreviewIsSuperseded(t *testing.T) {
	f := newPreviewFixture(t)
	f.renderer.pages = [][]byte{[]byte("page one"), []byte("page two"), []byte("page three")}
	f.store.readyLost = true
	job := f.storeAttachment(t, []byte("a three page pdf, near enough"), "application/pdf")
	f.store.enqueue(job)

	if _, err := f.service.ProcessDue(context.Background()); err != nil {
		t.Fatalf("process: %v", err)
	}

	ready, terminal := f.store.recorded()
	if len(ready) != 0 || len(terminal) != 0 {
		t.Fatalf("a lost race must record nothing: ready=%v terminal=%v", ready, terminal)
	}
	// Only the attachment's own object survives; every page this attempt wrote
	// must have been deleted, not just the first.
	if got := f.objects.count(); got != 1 {
		t.Fatalf("stored objects = %d, want 1 (the attachment only)", got)
	}
	if got := len(f.objects.deletedKeys()); got != 3 {
		t.Fatalf("deleted objects = %d, want 3 (one per rendered page)", got)
	}
}

// The preview object must be openable *only* through its own binding. Reusing
// the attachment's identity would make the two objects substitutable.
func TestPreviewObjectIsBoundToItsOwnIdentity(t *testing.T) {
	f := newPreviewFixture(t)
	job := f.storeAttachment(t, []byte("content"), "image/png")
	f.store.enqueue(job)

	if _, err := f.service.ProcessDue(context.Background()); err != nil {
		t.Fatalf("process: %v", err)
	}
	ready, _ := f.store.recorded()
	result := ready[0]

	_, err := f.keys.Unwrap(result.WrappedDEK, result.KEKKeyID, crypto.Binding{
		// The attachment's id instead of the preview's.
		AttachmentID:           uuid.MustParse(job.AttachmentID),
		WorkspaceID:            uuid.MustParse(testWorkspaceID),
		PlaintextSize:          result.Size,
		KeyWrapVersion:         result.KeyWrapVersion,
		ContentEnvelopeVersion: result.EnvelopeVersion,
	})
	if err == nil {
		t.Fatal("the preview key opened under the attachment's binding")
	}
}

func TestProcessDueRecordsUnsupportedContentWithoutStoringAnything(t *testing.T) {
	f := newPreviewFixture(t)
	f.renderer.err = fmt.Errorf("%w: nothing renders this", preview.ErrUnsupported)
	job := f.storeAttachment(t, []byte("some bytes"), "image/png")
	f.store.enqueue(job)

	if _, err := f.service.ProcessDue(context.Background()); err != nil {
		t.Fatalf("process: %v", err)
	}
	ready, terminal := f.store.recorded()
	if len(ready) != 0 || len(terminal) != 1 || terminal[0] != domain.PreviewStatusUnsupported {
		t.Fatalf("recorded ready=%v terminal=%v", ready, terminal)
	}
	// Only the attachment's own object exists: no preview was written.
	if f.objects.count() != 1 {
		t.Fatalf("expected only the attachment object, found %d", f.objects.count())
	}
}

func TestProcessDueRecordsARenderFailureAndLeavesTheAttachmentIntact(t *testing.T) {
	f := newPreviewFixture(t)
	f.renderer.err = fmt.Errorf("%w: broken file", preview.ErrRender)
	job := f.storeAttachment(t, []byte("the original attachment bytes"), "application/pdf")
	f.store.enqueue(job)

	if _, err := f.service.ProcessDue(context.Background()); err != nil {
		t.Fatalf("process: %v", err)
	}
	_, terminal := f.store.recorded()
	if len(terminal) != 1 || terminal[0] != domain.PreviewStatusFailed {
		t.Fatalf("terminal = %v, want one failed", terminal)
	}
	// The attachment itself is untouched: still exactly one object, and it is
	// still the one the job pointed at.
	if _, ok := f.objects.objects[job.StorageObjectKey]; !ok || f.objects.count() != 1 {
		t.Fatal("a failed preview must not disturb the attachment's own object")
	}
	if deleted := f.objects.deletedKeys(); len(deleted) != 0 {
		t.Fatalf("nothing should have been deleted, got %v", deleted)
	}
}

func TestProcessDueLeavesATransientFailureForAnotherAttempt(t *testing.T) {
	f := newPreviewFixture(t)
	f.renderer.err = errors.New("storage hiccup")
	job := f.storeAttachment(t, []byte("bytes"), "image/png")
	job.Attempts = 1
	f.store.enqueue(job)

	if _, err := f.service.ProcessDue(context.Background()); err != nil {
		t.Fatalf("process: %v", err)
	}
	ready, terminal := f.store.recorded()
	if len(ready) != 0 || len(terminal) != 0 {
		t.Fatalf("a retryable failure must write no state, got ready=%v terminal=%v", ready, terminal)
	}
	if results := f.observer.observed(); len(results) != 1 || results[0] != "retry" {
		t.Fatalf("observed %v, want one retry", results)
	}
}

// The renderer's budget is spent by claims, and once it is, a claim stops
// decrypting anything: it exists only to write the row's terminal state.
func TestProcessDueStopsRenderingOnceTheBudgetIsSpent(t *testing.T) {
	f := newPreviewFixture(t)
	f.renderer.err = errors.New("storage hiccup")
	job := f.storeAttachment(t, []byte("bytes"), "image/png")
	f.store.enqueue(job)

	tuning := service.PreviewTuningForTest()
	// Every pass a render is allowed on, plus the one that has to finish the
	// row off without rendering.
	for pass := 0; pass <= tuning.MaxRenderAttempts; pass++ {
		if _, err := f.service.ProcessDue(context.Background()); err != nil {
			t.Fatalf("pass %d: %v", pass, err)
		}
		f.store.advance(tuning.Lease + time.Second)
	}

	if calls := f.renderer.callCount(); calls != tuning.MaxRenderAttempts {
		t.Fatalf("renderer ran %d times, want %d", calls, tuning.MaxRenderAttempts)
	}
	row := f.store.row(t, job.AttachmentID)
	if row.status != domain.PreviewStatusFailed {
		t.Fatalf("row ended in %q, want failed", row.status)
	}
}

// Achado 3: the row must never be stranded by a database that was unavailable
// at the moment a terminal state had to be written.
//
// The old model bound the claim by the same counter as the render, so a failed
// terminal write on the last attempt left the row pending forever: exhausted,
// and therefore never selected again. Claiming is now unbounded, so the row
// stays eligible and the next pass writes the state without rendering again.
func TestProcessDueRecoversARowWhoseTerminalWriteFailed(t *testing.T) {
	f := newPreviewFixture(t)
	f.renderer.err = fmt.Errorf("%w: broken file", preview.ErrRender)
	f.store.terminalErr = errors.New("connection refused")
	job := f.storeAttachment(t, []byte("bytes"), "image/png")
	f.store.enqueue(job)

	tuning := service.PreviewTuningForTest()
	for pass := 0; pass < tuning.MaxRenderAttempts; pass++ {
		if _, err := f.service.ProcessDue(context.Background()); err != nil {
			t.Fatalf("pass %d: %v", pass, err)
		}
		f.store.advance(tuning.Lease + time.Second)
	}

	// Every write failed, so the row is still pending — and, crucially, still
	// claimable rather than exhausted.
	if row := f.store.row(t, job.AttachmentID); row.status != domain.PreviewStatusPending {
		t.Fatalf("row is %q, want pending while the database is refusing writes", row.status)
	}

	// PostgreSQL comes back.
	f.store.mu.Lock()
	f.store.terminalErr = nil
	f.store.mu.Unlock()

	processed, err := f.service.ProcessDue(context.Background())
	if err != nil {
		t.Fatalf("recovery pass: %v", err)
	}
	if processed != 1 {
		t.Fatalf("the recovered row was not claimed, processed = %d", processed)
	}
	row := f.store.row(t, job.AttachmentID)
	if row.status == domain.PreviewStatusPending {
		t.Fatal("the row must leave pending once the database accepts the write")
	}
	if row.status != domain.PreviewStatusFailed {
		t.Fatalf("row ended in %q, want failed", row.status)
	}
	// The recovery pass finished the row off without decrypting it again.
	if calls := f.renderer.callCount(); calls != tuning.MaxRenderAttempts {
		t.Fatalf("renderer ran %d times, want %d — recovery must not re-render",
			calls, tuning.MaxRenderAttempts)
	}
}

// When the database recovers while renders remain, the state the renderer
// actually decided is what lands — "unsupported" is not degraded to "failed".
func TestProcessDuePreservesTheRenderersClassificationOnRecovery(t *testing.T) {
	f := newPreviewFixture(t)
	f.renderer.err = fmt.Errorf("%w: nothing renders this", preview.ErrUnsupported)
	f.store.terminalErr = errors.New("connection refused")
	job := f.storeAttachment(t, []byte("bytes"), "image/png")
	f.store.enqueue(job)

	if _, err := f.service.ProcessDue(context.Background()); err != nil {
		t.Fatalf("first pass: %v", err)
	}
	if row := f.store.row(t, job.AttachmentID); row.status != domain.PreviewStatusPending {
		t.Fatalf("row is %q, want pending", row.status)
	}

	f.store.mu.Lock()
	f.store.terminalErr = nil
	f.store.mu.Unlock()
	f.store.advance(service.PreviewTuningForTest().Lease + time.Second)

	if _, err := f.service.ProcessDue(context.Background()); err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if row := f.store.row(t, job.AttachmentID); row.status != domain.PreviewStatusUnsupported {
		t.Fatalf("row ended in %q, want the renderer's own classification", row.status)
	}
}

// A restart between rendering and persisting looks exactly like a lease that
// expired: nothing was written, so the row simply becomes due again.
func TestProcessDueRecoversFromARestartBetweenRenderAndPersistence(t *testing.T) {
	f := newPreviewFixture(t)
	f.store.readyErr = errors.New("connection reset")
	job := f.storeAttachment(t, []byte("bytes"), "image/png")
	f.store.enqueue(job)

	if _, err := f.service.ProcessDue(context.Background()); err != nil {
		t.Fatalf("first pass: %v", err)
	}
	// The object the interrupted attempt wrote must not survive it.
	if f.objects.count() != 1 {
		t.Fatalf("an unrecorded preview object was left behind: %d objects", f.objects.count()-1)
	}

	f.store.mu.Lock()
	f.store.readyErr = nil
	f.store.mu.Unlock()
	f.store.advance(service.PreviewTuningForTest().Lease + time.Second)

	if _, err := f.service.ProcessDue(context.Background()); err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if row := f.store.row(t, job.AttachmentID); row.status != domain.PreviewStatusReady {
		t.Fatalf("row is %q, want ready after the retry", row.status)
	}
	ready, _ := f.store.recorded()
	if len(ready) != 1 {
		t.Fatalf("recorded %d previews, want exactly one", len(ready))
	}
	// Exactly the attachment and the surviving preview: the discarded attempt
	// left nothing behind.
	if f.objects.count() != 2 {
		t.Fatalf("storage holds %d objects, want the attachment and one preview", f.objects.count())
	}
}

// A leased row belongs to the worker holding it: a second replica polling in
// the meantime must find nothing rather than render the same attachment twice.
func TestASecondWorkerCannotClaimARowUnderALiveLease(t *testing.T) {
	f := newPreviewFixture(t)
	job := f.storeAttachment(t, []byte("bytes"), "image/png")
	f.store.enqueue(job)

	// The first replica claims it and is still rendering.
	claimed, err := f.store.ClaimDuePreviews(context.Background(), 1, service.PreviewTuningForTest().Lease)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("first claim: %d rows, err %v", len(claimed), err)
	}

	// A second replica, sharing the store, polls inside the lease.
	second := service.NewPreviewService(
		f.store, f.objects, f.keys, f.renderer, f.fence, f.cleanup, f.observer, discardLogger(),
	)
	processed, err := second.ProcessDue(context.Background())
	if err != nil {
		t.Fatalf("second replica: %v", err)
	}
	if processed != 0 || f.renderer.callCount() != 0 {
		t.Fatalf("a leased row was picked up again: processed=%d renders=%d",
			processed, f.renderer.callCount())
	}

	// Once the lease expires — the holder died — the row is due again.
	f.store.advance(service.PreviewTuningForTest().Lease + time.Second)
	processed, err = second.ProcessDue(context.Background())
	if err != nil {
		t.Fatalf("after the lease expired: %v", err)
	}
	if processed != 1 {
		t.Fatal("an expired lease must make the row eligible again")
	}
}

// Two finalisers racing on the same row: the conditional update lets exactly
// one through, and the loser writes nothing rather than overwriting the winner.
func TestTwoFinalisersProduceOneTerminalState(t *testing.T) {
	f := newPreviewFixture(t)
	job := f.storeAttachment(t, []byte("bytes"), "image/png")
	f.store.enqueue(job)

	claim := f.store.row(t, job.AttachmentID).attempts
	recordedFirst, err := f.store.MarkPreviewTerminal(
		context.Background(), job.AttachmentID, claim, domain.PreviewStatusUnsupported)
	if err != nil || !recordedFirst {
		t.Fatalf("first finaliser: recorded=%v err=%v", recordedFirst, err)
	}
	recordedSecond, err := f.store.MarkPreviewTerminal(
		context.Background(), job.AttachmentID, claim, domain.PreviewStatusFailed)
	if err != nil {
		t.Fatalf("second finaliser: %v", err)
	}
	if recordedSecond {
		t.Fatal("the second finaliser must not overwrite a terminal state")
	}
	if row := f.store.row(t, job.AttachmentID); row.status != domain.PreviewStatusUnsupported {
		t.Fatalf("row is %q, want the first finaliser's state", row.status)
	}
}

// Two attempts can overlap only if a lease expired under a slow render. The
// loser must not leave its object behind, and must not overwrite the winner's:
// every attempt writes to its own key, so discarding is always safe.
func TestProcessDueDiscardsItsObjectWhenAnotherAttemptWonTheRace(t *testing.T) {
	f := newPreviewFixture(t)
	f.store.readyLost = true
	job := f.storeAttachment(t, []byte("bytes"), "image/png")
	f.store.enqueue(job)

	if _, err := f.service.ProcessDue(context.Background()); err != nil {
		t.Fatalf("process: %v", err)
	}
	if f.objects.count() != 1 {
		t.Fatalf("the losing attempt left %d objects behind", f.objects.count()-1)
	}
	deleted := f.objects.deletedKeys()
	if len(deleted) != 1 || deleted[0] == job.StorageObjectKey {
		t.Fatalf("expected the preview object to be discarded, deleted %v", deleted)
	}
	if f.observer.value() != 0 {
		t.Fatal("a cleaned-up object is not an orphan")
	}
}

// --- Publishing and losing the row are different outcomes -------------------
//
// A claim whose conditional UPDATE matches no row has not produced a preview:
// the attachment was rejected, removed, or taken over by a newer attempt. The
// render happened, but nothing was published. Counting that as "ready" made the
// metric say a preview existed when none did, and it is the one number an
// operator reads to answer "are previews being produced".

// countObserved reports how many times a result was recorded.
func countObserved(results []string, want string) int {
	count := 0
	for _, result := range results {
		if result == want {
			count++
		}
	}
	return count
}

// A: the ordinary publication is unchanged.
func TestProcessDueCountsAPublishedPreviewAsReadyExactlyOnce(t *testing.T) {
	f := newPreviewFixture(t)
	f.store.enqueue(f.storeAttachment(t, []byte("bytes"), "image/png"))

	if _, err := f.service.ProcessDue(context.Background()); err != nil {
		t.Fatalf("process: %v", err)
	}

	observed := f.observer.observed()
	if got := countObserved(observed, "ready"); got != 1 {
		t.Fatalf("observed ready %d times in %v, want exactly once", got, observed)
	}
	if got := countObserved(observed, "superseded"); got != 0 {
		t.Fatalf("a published preview was also counted superseded: %v", observed)
	}
	// The published object stays, and nothing was cleaned up.
	if f.objects.count() != 2 {
		t.Fatalf("storage holds %d objects, want the attachment and its preview", f.objects.count())
	}
	if deleted := f.objects.deletedKeys(); len(deleted) != 0 {
		t.Fatalf("a published preview was discarded: %v", deleted)
	}
	if queued := f.cleanup.queued(); len(queued) != 0 {
		t.Fatalf("a published preview was queued for cleanup: %v", queued)
	}
}

// B: the claim lost the row. This is the finding.
func TestProcessDueCountsALostRowAsSupersededNotReady(t *testing.T) {
	f := newPreviewFixture(t)
	f.store.readyLost = true
	f.store.enqueue(f.storeAttachment(t, []byte("bytes"), "image/png"))

	if _, err := f.service.ProcessDue(context.Background()); err != nil {
		t.Fatalf("process: %v", err)
	}

	observed := f.observer.observed()
	if got := countObserved(observed, "ready"); got != 0 {
		t.Fatalf("a preview that was never published was counted ready: %v", observed)
	}
	if got := countObserved(observed, "superseded"); got != 1 {
		t.Fatalf("observed superseded %d times in %v, want exactly once", got, observed)
	}
	// Not a failure: nothing is marked failed and nothing is retried.
	if got := countObserved(observed, "failed"); got != 0 {
		t.Fatalf("losing the row was counted as a failure: %v", observed)
	}
	if got := countObserved(observed, "retry"); got != 0 {
		t.Fatalf("losing the row scheduled a retry: %v", observed)
	}
	if f.renderer.callCount() != 1 {
		t.Fatalf("the renderer ran %d times, want exactly one", f.renderer.callCount())
	}
	// The preview state is untouched — whoever won the row owns it.
	if _, terminal := f.store.recorded(); len(terminal) != 0 {
		t.Fatalf("a superseded attempt wrote a terminal state: %v", terminal)
	}
}

// C: superseded with a working Delete needs no queue.
func TestSupersededDiscardsThroughDeleteWithoutTheQueue(t *testing.T) {
	f := newPreviewFixture(t)
	f.store.readyLost = true
	job := f.storeAttachment(t, []byte("bytes"), "image/png")
	f.store.enqueue(job)

	if _, err := f.service.ProcessDue(context.Background()); err != nil {
		t.Fatalf("process: %v", err)
	}

	deleted := f.objects.deletedKeys()
	if len(deleted) != 1 || deleted[0] == job.StorageObjectKey {
		t.Fatalf("expected the preview object to be deleted, deleted %v", deleted)
	}
	if queued := f.cleanup.queued(); len(queued) != 0 {
		t.Fatalf("a successful delete still used the queue: %v", queued)
	}
	if got := countObserved(f.observer.observed(), "ready"); got != 0 {
		t.Fatal("ready was emitted for an unpublished preview")
	}
}

// D: superseded, Delete fails, the queue takes the key.
func TestSupersededQueuesTheObjectWhenDeleteFails(t *testing.T) {
	f := newPreviewFixture(t)
	f.store.readyLost = true
	f.objects.deleteErr = errors.New("storage unavailable")
	f.store.enqueue(f.storeAttachment(t, []byte("bytes"), "image/png"))

	if _, err := f.service.ProcessDue(context.Background()); err != nil {
		t.Fatalf("process: %v", err)
	}

	if queued := f.cleanup.queued(); len(queued) != 1 {
		t.Fatalf("queued %v, want the one preview object", queued)
	}
	observed := f.observer.observed()
	if got := countObserved(observed, "ready"); got != 0 {
		t.Fatalf("ready was emitted for an unpublished preview: %v", observed)
	}
	if got := countObserved(observed, "superseded"); got != 1 {
		t.Fatalf("observed superseded %d times in %v, want once", got, observed)
	}
	// A queued cleanup is not an orphan, and the render is not repeated.
	if f.observer.value() != 0 {
		t.Fatal("a queued object was counted as an orphan")
	}
	if f.renderer.callCount() != 1 {
		t.Fatalf("the renderer ran %d times after losing the row", f.renderer.callCount())
	}
}

// E: superseded, Delete fails, the queue fails too. Asserted against the
// behaviour that already exists — the orphan counter — not a new mechanism.
func TestSupersededCountsAnOrphanWhenBothDiscardPathsFail(t *testing.T) {
	f := newPreviewFixture(t)
	f.store.readyLost = true
	f.objects.deleteErr = errors.New("storage unavailable")
	f.cleanup.err = errors.New("database unavailable")
	f.store.enqueue(f.storeAttachment(t, []byte("bytes"), "image/png"))

	if _, err := f.service.ProcessDue(context.Background()); err != nil {
		t.Fatalf("process: %v", err)
	}

	if f.observer.value() != 1 {
		t.Fatalf("orphan counter = %d, want the one object neither path could remove",
			f.observer.value())
	}
	observed := f.observer.observed()
	if got := countObserved(observed, "ready"); got != 0 {
		t.Fatalf("ready was emitted for an unpublished preview: %v", observed)
	}
	if got := countObserved(observed, "superseded"); got != 1 {
		t.Fatalf("observed superseded %d times in %v, want once", got, observed)
	}
	if _, terminal := f.store.recorded(); len(terminal) != 0 {
		t.Fatalf("a failed discard published a state: %v", terminal)
	}
	if f.renderer.callCount() != 1 {
		t.Fatalf("the renderer ran %d times", f.renderer.callCount())
	}
}

// F: a stale attempt whose lease expired under a slow render. It must not
// report a preview, and must not disturb the attempt that took the row.
func TestStaleAttemptReportsSupersededAndLeavesTheCurrentAttemptAlone(t *testing.T) {
	f := newPreviewFixture(t)
	job := f.storeAttachment(t, []byte("bytes"), "image/png")
	f.store.enqueue(job)

	// The row is claimed again while this attempt is still rendering, so the
	// publishing statement no longer matches its token.
	f.store.readyLost = true

	if _, err := f.service.ProcessDue(context.Background()); err != nil {
		t.Fatalf("process: %v", err)
	}

	observed := f.observer.observed()
	if got := countObserved(observed, "ready"); got != 0 {
		t.Fatalf("a stale attempt reported a preview: %v", observed)
	}
	if got := countObserved(observed, "superseded"); got != 1 {
		t.Fatalf("observed superseded %d times in %v, want once", got, observed)
	}
	// Its object is gone, and the row still says pending for the winner.
	if f.objects.count() != 1 {
		t.Fatalf("the stale attempt left %d objects behind", f.objects.count()-1)
	}
	if row := f.store.row(t, job.AttachmentID); row.status != domain.PreviewStatusPending {
		t.Fatalf("the stale attempt moved the row to %q", row.status)
	}
}

func TestProcessDueDiscardsItsObjectWhenTheStateCannotBeRecorded(t *testing.T) {
	f := newPreviewFixture(t)
	f.store.readyErr = errors.New("deadlock detected")
	f.store.enqueue(f.storeAttachment(t, []byte("bytes"), "image/png"))

	if _, err := f.service.ProcessDue(context.Background()); err != nil {
		t.Fatalf("process: %v", err)
	}
	if f.objects.count() != 1 {
		t.Fatal("a preview whose row could not be updated must not stay in storage")
	}
}

// One claim per pass is what keeps the lease honest: a pass renders serially,
// so claiming more would put rows under a lease that has to cover work that has
// not started yet.
func TestProcessDueClaimsExactlyOneJobPerPass(t *testing.T) {
	f := newPreviewFixture(t)
	if _, err := f.service.ProcessDue(context.Background()); err != nil {
		t.Fatalf("process: %v", err)
	}
	if f.store.claimBatch != 1 {
		t.Fatalf("claim asked for %d rows, want exactly 1", f.store.claimBatch)
	}
}

// The relationship between the three durations is the property, not their
// values: the lease has to outlive the render plus the detached writes that can
// follow it, or a second worker takes a row that is still being processed.
func TestPreviewTuningKeepsTheLeaseLongerThanTheWorkItProtects(t *testing.T) {
	tuning := service.PreviewTuningForTest()

	if tuning.BatchSize != 1 {
		t.Fatalf("batch size = %d, want 1", tuning.BatchSize)
	}
	if tuning.MaxRenderAttempts <= 0 {
		t.Fatalf("render attempts = %d, want a positive budget", tuning.MaxRenderAttempts)
	}
	minimum := tuning.JobTimeout + 2*tuning.CompensationTimeout
	if tuning.Lease <= minimum {
		t.Fatalf("lease %v must exceed %v (job timeout plus its detached writes)",
			tuning.Lease, minimum)
	}
	// The lease is derived from the timeout rather than chosen next to it, so
	// raising the timeout cannot silently overtake it.
	if tuning.Lease != tuning.JobTimeout+tuning.LeaseMargin {
		t.Fatalf("lease %v is not derived from the job timeout", tuning.Lease)
	}
}

func TestProcessDueStopsWhenTheContextIsCancelled(t *testing.T) {
	f := newPreviewFixture(t)
	f.store.enqueue(f.storeAttachment(t, []byte("one"), "image/png"))
	f.store.enqueue(f.storeAttachment(t, []byte("two"), "image/png"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	processed, err := f.service.ProcessDue(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if processed != 0 || f.renderer.callCount() != 0 {
		t.Fatalf("a cancelled pass rendered %d of %d jobs", f.renderer.callCount(), processed)
	}

	// Stopping must not leave an attachment fenced. A leaked session lock is
	// invisible until the next verdict on that attachment blocks forever behind
	// it, so the counters are asserted rather than the absence of a symptom.
	acquired, released := f.fence.counts()
	if acquired != released {
		t.Fatalf("fence acquired %d times, released %d: a cancelled pass leaked one",
			acquired, released)
	}
	// The claim is checked before the fence, so a pass that never starts a job
	// never takes one either.
	if acquired != 0 {
		t.Fatalf("a pass cancelled before it began took the fence %d times", acquired)
	}
	if f.fence.held() {
		t.Fatal("the fence is still held after a cancelled pass")
	}
}

func TestProcessDueReportsAFailedClaim(t *testing.T) {
	f := newPreviewFixture(t)
	f.store.claimErr = errors.New("connection refused")
	if _, err := f.service.ProcessDue(context.Background()); err == nil {
		t.Fatal("expected the claim failure to be reported")
	}
}

func TestPreviewServiceRefusesToRunHalfWired(t *testing.T) {
	incomplete := service.NewPreviewService(nil, nil, nil, nil, nil, nil, nil, discardLogger())
	if incomplete.Ready() {
		t.Fatal("a service with no dependencies must not report ready")
	}
	if _, err := incomplete.ProcessDue(context.Background()); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("error = %v, want ErrUnavailable", err)
	}
}

// --- the read path ------------------------------------------------------

// previewableRecord is an attachment with a real, servable preview behind it.
func storedPreviewRecord(t *testing.T, f *fixture, image []byte) service.StoredAttachment {
	t.Helper()
	previewID, workspaceID := uuid.New(), uuid.MustParse(testWorkspaceID)

	dataKey, err := crypto.NewDataKey()
	if err != nil {
		t.Fatalf("data key: %v", err)
	}
	encrypted, err := crypto.NewEncryptingReader(bytes.NewReader(image), dataKey, previewID)
	if err != nil {
		t.Fatalf("encrypting reader: %v", err)
	}
	if _, err := f.objects.Put(
		context.Background(), domain.PreviewObjectKey(previewID), encrypted,
	); err != nil {
		t.Fatalf("store preview: %v", err)
	}
	wrapped, keyID, err := f.keys.Wrap(dataKey, crypto.Binding{
		AttachmentID:           previewID,
		WorkspaceID:            workspaceID,
		PlaintextSize:          int64(len(image)),
		KeyWrapVersion:         crypto.KeyWrapVersion,
		ContentEnvelopeVersion: crypto.EnvelopeVersion,
	})
	if err != nil {
		t.Fatalf("wrap: %v", err)
	}
	return service.StoredAttachment{
		ID:                     uuid.NewString(),
		WorkspaceID:            testWorkspaceID,
		Kind:                   domain.DestinationKindChannel,
		Status:                 domain.StatusClean,
		Filename:               "diagram.png",
		DetectedMIME:           "image/png",
		Size:                   int64(len(image)),
		EnvelopeVersion:        crypto.EnvelopeVersion,
		KeyWrapVersion:         crypto.KeyWrapVersion,
		PreviewStatus:          domain.PreviewStatusReady,
		PreviewObjectID:        previewID.String(),
		PreviewSize:            int64(len(image)),
		PreviewWrappedDEK:      wrapped,
		PreviewKEKKeyID:        keyID,
		PreviewEnvelopeVersion: crypto.EnvelopeVersion,
		PreviewKeyWrapVersion:  crypto.KeyWrapVersion,
	}
}

func TestPreviewServesTheStoredPreviewOfAVisibleCleanAttachment(t *testing.T) {
	f := newFixture(t)
	image := []byte("rendered bytes")
	f.store.authorized = storedPreviewRecord(t, f, image)

	preview, err := f.service.Preview(context.Background(), service.AttachmentAuthInput{
		AttachmentID: f.store.authorized.ID, UserID: testUserID, SessionID: testSessionID,
	})
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	defer func() { _ = preview.Content.Close() }()

	if preview.ContentType != domain.PreviewContentType {
		t.Fatalf("content type = %q, want %q", preview.ContentType, domain.PreviewContentType)
	}
	if preview.Size != int64(len(image)) {
		t.Fatalf("size = %d, want %d", preview.Size, len(image))
	}
	served, err := io.ReadAll(preview.Content)
	if err != nil {
		t.Fatalf("read preview: %v", err)
	}
	if string(served) != string(image) {
		t.Fatalf("served %q, want %q", served, image)
	}
}

// The preview is behind the same scan gate as the content, because it *is* the
// content: a rendering of a file nobody has cleared must not be a way to see it.
func TestPreviewRefusesAnAttachmentTheScanHasNotCleared(t *testing.T) {
	for _, status := range []domain.Status{
		domain.StatusPendingScan, domain.StatusRejected, domain.StatusPendingUpload,
	} {
		t.Run(string(status), func(t *testing.T) {
			f := newFixture(t)
			record := storedPreviewRecord(t, f, []byte("rendered"))
			record.Status = status
			f.store.authorized = record

			_, err := f.service.Preview(context.Background(), service.AttachmentAuthInput{
				AttachmentID: record.ID, UserID: testUserID, SessionID: testSessionID,
			})
			if !errors.Is(err, domain.ErrNotDownloadable) {
				t.Fatalf("error = %v, want ErrNotDownloadable", err)
			}
			// Refused before storage was touched at all.
			if f.objects.openCount() != 0 {
				t.Fatal("a blocked attachment must not be read from storage")
			}
		})
	}
}

func TestPreviewReportsEveryAbsenceTheSameWay(t *testing.T) {
	for _, status := range []domain.PreviewStatus{
		domain.PreviewStatusPending, domain.PreviewStatusUnsupported, domain.PreviewStatusFailed,
	} {
		t.Run(string(status), func(t *testing.T) {
			f := newFixture(t)
			record := storedPreviewRecord(t, f, []byte("rendered"))
			record.PreviewStatus = status
			f.store.authorized = record

			_, err := f.service.Preview(context.Background(), service.AttachmentAuthInput{
				AttachmentID: record.ID, UserID: testUserID, SessionID: testSessionID,
			})
			if !errors.Is(err, domain.ErrPreviewUnavailable) {
				t.Fatalf("error = %v, want ErrPreviewUnavailable", err)
			}
		})
	}
}

// A row that claims a ready preview but carries an unusable preview id is
// corrupt metadata. It must fail closed, before storage, as the same controlled
// absence every other missing preview reports — not as a generic failure, which
// would turn a row-level inconsistency into a 500 and page someone for a file
// that simply has no preview.
func TestPreviewRefusesACorruptPreviewObjectID(t *testing.T) {
	for name, objectID := range map[string]string{
		"empty":        "",
		"not a uuid":   "not-a-uuid",
		"whitespace":   "   ",
		"truncated":    "3fa301d5-455c-4026-beb7",
		"storage path": "nchat/previews/3fa301d5-455c-4026-beb7-4c1b7b037d8c",
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			record := storedPreviewRecord(t, f, []byte("rendered"))
			record.PreviewObjectID = objectID
			f.store.authorized = record

			_, err := f.service.Preview(context.Background(), service.AttachmentAuthInput{
				AttachmentID: record.ID, UserID: testUserID, SessionID: testSessionID,
			})

			if !errors.Is(err, domain.ErrPreviewUnavailable) {
				t.Fatalf("error = %v, want ErrPreviewUnavailable", err)
			}
			// The whole point: the object store is never asked for a key that
			// could not be derived, so it never receives an empty one.
			if f.objects.openCount() != 0 {
				t.Fatalf("storage was read %d times for an underivable key", f.objects.openCount())
			}
			// The corrupt value is not echoed back to the caller.
			if objectID != "" && strings.Contains(err.Error(), objectID) {
				t.Fatalf("the error carries the stored id: %v", err)
			}
		})
	}
}

// The valid path is unchanged, and the key is the domain's, not one this
// function invents.
func TestPreviewDerivesTheObjectKeyFromTheDomain(t *testing.T) {
	f := newFixture(t)
	record := storedPreviewRecord(t, f, []byte("rendered"))
	f.store.authorized = record

	preview, err := f.service.Preview(context.Background(), service.AttachmentAuthInput{
		AttachmentID: record.ID, UserID: testUserID, SessionID: testSessionID,
	})
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	defer func() { _ = preview.Content.Close() }()

	want := domain.PreviewObjectKey(uuid.MustParse(record.PreviewObjectID))
	if opened := f.objects.openedKeys(); len(opened) != 1 || opened[0] != want {
		t.Fatalf("opened %v, want exactly [%s]", opened, want)
	}
}

func TestPreviewAnswersAnInvisibleAttachmentLikeAMissingOne(t *testing.T) {
	f := newFixture(t)
	f.store.authorizedErr = domain.ErrNotFound

	_, err := f.service.Preview(context.Background(), service.AttachmentAuthInput{
		AttachmentID: uuid.NewString(), UserID: testUserID, SessionID: testSessionID,
	})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("error = %v, want ErrNotFound", err)
	}
}

// --- scheduling ---------------------------------------------------------

func TestUploadSchedulesAPreviewWithoutWaitingForOne(t *testing.T) {
	for name, tt := range map[string]struct {
		content []byte
		want    domain.PreviewStatus
	}{
		"png":   {content: pngBytes(), want: domain.PreviewStatusPending},
		"pdf":   {content: []byte("%PDF-1.4 a document"), want: domain.PreviewStatusPending},
		"other": {content: []byte("PK\x03\x04 an archive"), want: domain.PreviewStatusUnsupported},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			view, err := f.upload(context.Background(), bytes.NewReader(tt.content), "file.bin")
			if err != nil {
				t.Fatalf("upload: %v", err)
			}
			if view.PreviewStatus != string(tt.want) {
				t.Fatalf("view preview status = %q, want %q", view.PreviewStatus, tt.want)
			}
			_, uploaded, _ := f.store.snapshot()
			if len(uploaded) != 1 || uploaded[0].PreviewStatus != tt.want {
				t.Fatalf("persisted preview status = %v, want %q", uploaded, tt.want)
			}
			// Exactly one object: the attachment's. The upload path renders
			// nothing, so nothing about the response waited on a decoder.
			if f.objects.count() != 1 {
				t.Fatalf("upload stored %d objects", f.objects.count())
			}
		})
	}
}

// A file too large to render never enters the queue, so the worker never reads
// back something it would refuse anyway.
func TestUploadDoesNotQueueAPreviewForContentBeyondTheRenderLimit(t *testing.T) {
	if got := domain.InitialPreviewStatus("image/png", domain.MaxPreviewSourceBytes+1); got != domain.PreviewStatusUnsupported {
		t.Fatalf("status = %q, want unsupported", got)
	}
}

// pngBytes is the smallest thing net/http sniffs as a PNG. The upload path only
// looks at the detected type, so the pixels are irrelevant here.
func pngBytes() []byte {
	return append([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}, make([]byte, 16)...)
}

// A storage write that fails may still have left a partial object, so it is
// treated as stored and cleaned up — the same rule the upload path follows.
func TestProcessDueCleansUpAfterAFailedPreviewWrite(t *testing.T) {
	f := newPreviewFixture(t)
	job := f.storeAttachment(t, []byte("bytes"), "image/png")
	f.store.enqueue(job)
	f.objects.putErr = errors.New("storage unavailable")

	if _, err := f.service.ProcessDue(context.Background()); err != nil {
		t.Fatalf("process: %v", err)
	}
	ready, terminal := f.store.recorded()
	if len(ready) != 0 {
		t.Fatal("a preview that was never stored must not be recorded")
	}
	// Transient: storage failing says nothing about the content, so the row is
	// left for another attempt rather than being failed.
	if len(terminal) != 0 {
		t.Fatalf("terminal = %v, want none", terminal)
	}
	if deleted := f.objects.deletedKeys(); len(deleted) != 1 {
		t.Fatalf("expected exactly one cleanup, got %v", deleted)
	}
}

// A store that reports a length the envelope cannot have has not stored the
// closed stream this service produced. Recording it would publish a preview
// nothing can open, so the object is discarded instead.
func TestProcessDueRefusesAPreviewStoredAsTheWrongLength(t *testing.T) {
	f := newPreviewFixture(t)
	f.store.enqueue(f.storeAttachment(t, []byte("bytes"), "image/png"))
	f.objects.shortPutBy = 1

	if _, err := f.service.ProcessDue(context.Background()); err != nil {
		t.Fatalf("process: %v", err)
	}
	if ready, _ := f.store.recorded(); len(ready) != 0 {
		t.Fatal("an incomplete envelope must never be recorded as ready")
	}
	if f.objects.count() != 1 {
		t.Fatal("the incomplete preview object must not stay in storage")
	}
}

// The workspace comes from the attachment's own row, so an unparseable one is a
// data-integrity problem: it must fail rather than be encrypted under a binding
// that could never be reproduced.
func TestProcessDueRefusesAJobWithAnUnusableWorkspace(t *testing.T) {
	f := newPreviewFixture(t)
	job := f.storeAttachment(t, []byte("bytes"), "image/png")
	job.WorkspaceID = "not-a-uuid"
	f.store.enqueue(job)

	if _, err := f.service.ProcessDue(context.Background()); err != nil {
		t.Fatalf("process: %v", err)
	}
	if ready, _ := f.store.recorded(); len(ready) != 0 {
		t.Fatal("nothing may be recorded for a row whose workspace cannot be parsed")
	}
	if f.objects.count() != 1 {
		t.Fatalf("no preview object should exist, found %d", f.objects.count()-1)
	}
}

// The attachment is opened through the download's own path, so an envelope
// version this build does not implement stops the job instead of being guessed
// at — and nothing is written for it.
func TestProcessDueRefusesAnUnsupportedAttachmentEnvelope(t *testing.T) {
	f := newPreviewFixture(t)
	job := f.storeAttachment(t, []byte("bytes"), "image/png")
	job.EnvelopeVersion = 99
	f.store.enqueue(job)

	if _, err := f.service.ProcessDue(context.Background()); err != nil {
		t.Fatalf("process: %v", err)
	}
	if f.renderer.callCount() != 0 {
		t.Fatal("an unopenable attachment must never reach the renderer")
	}
	if ready, _ := f.store.recorded(); len(ready) != 0 {
		t.Fatal("nothing may be recorded for an unopenable attachment")
	}
}

// --- the scan gate -------------------------------------------------------

// The worker is the one component that decrypts an attachment and hands it to a
// parser, so it must never do that to a file the scan has not approved. The
// gate is in the claim, not in a check after it.
func TestProcessDueNeverRendersAnAttachmentTheScanHasNotApproved(t *testing.T) {
	f := newPreviewFixture(t)
	job := f.storeAttachment(t, []byte("bytes"), "image/png")
	f.store.enqueueUnscanned(job)

	processed, err := f.service.ProcessDue(context.Background())
	if err != nil {
		t.Fatalf("process: %v", err)
	}
	if processed != 0 {
		t.Fatalf("claimed %d unscanned rows, want none", processed)
	}
	if f.renderer.callCount() != 0 {
		t.Fatal("an unapproved attachment must never reach the renderer")
	}
	// It was not decrypted either: the attachment's object was never opened.
	if f.objects.openCount() != 0 {
		t.Fatal("an unapproved attachment must never be decrypted")
	}
	if row := f.store.row(t, job.AttachmentID); row.status != domain.PreviewStatusPending {
		t.Fatalf("row is %q, want pending until the scan rules", row.status)
	}
}

// A verdict can land while a render is in flight. The publishing statement
// re-asserts it, so the preview of a file that has since been condemned is
// discarded instead of published.
func TestProcessDueDoesNotPublishAPreviewForAnAttachmentRejectedMidRender(t *testing.T) {
	f := newPreviewFixture(t)
	job := f.storeAttachment(t, []byte("bytes"), "image/png")
	f.store.enqueue(job)
	f.renderer.duringRender = func() { f.store.reject(job.AttachmentID) }

	if _, err := f.service.ProcessDue(context.Background()); err != nil {
		t.Fatalf("process: %v", err)
	}
	ready, _ := f.store.recorded()
	if len(ready) != 0 {
		t.Fatal("a preview must not be published for a rejected attachment")
	}
	if row := f.store.row(t, job.AttachmentID); row.status == domain.PreviewStatusReady {
		t.Fatal("the row must not reach ready")
	}
	// And the intermediate result is gone: only the attachment's own object is
	// left in storage.
	if f.objects.count() != 1 {
		t.Fatalf("storage holds %d objects, want only the attachment", f.objects.count())
	}
	if deleted := f.objects.deletedKeys(); len(deleted) != 1 {
		t.Fatalf("expected the orphaned preview to be discarded, deleted %v", deleted)
	}
}

// Once rejected, the row is not claimed again either: no retry path renders it.
func TestProcessDueStopsClaimingAnAttachmentAfterItIsRejected(t *testing.T) {
	f := newPreviewFixture(t)
	job := f.storeAttachment(t, []byte("bytes"), "image/png")
	f.store.enqueue(job)
	f.store.reject(job.AttachmentID)
	f.store.advance(service.PreviewTuningForTest().Lease + time.Second)

	processed, err := f.service.ProcessDue(context.Background())
	if err != nil {
		t.Fatalf("process: %v", err)
	}
	if processed != 0 || f.renderer.callCount() != 0 {
		t.Fatalf("a rejected attachment was picked up: processed=%d renders=%d",
			processed, f.renderer.callCount())
	}
}

// --- the attachment fence (SR-001) ----------------------------------------
//
// The orderings below are forced with channels, never with sleeps: each test
// blocks one side at a known point and releases it, so the assertion is about
// the order the code actually enforces rather than about timing.

// Ordering 1: the invalidation got there first. The job must find out inside
// the fence and never open, decrypt or parse anything.
func TestProcessDueNeverRendersWhenTheAttachmentWasInvalidatedFirst(t *testing.T) {
	f := newPreviewFixture(t)
	job := f.storeAttachment(t, []byte("bytes"), "image/png")
	f.store.enqueue(job)

	// The rejection lands while the job is queuing for the fence.
	f.fence.onAcquire = func() { f.store.reject(job.AttachmentID) }

	if _, err := f.service.ProcessDue(context.Background()); err != nil {
		t.Fatalf("process: %v", err)
	}
	if f.renderer.callCount() != 0 {
		t.Fatal("a condemned attachment reached the renderer")
	}
	// Nothing was decrypted either: the attachment's object was never opened.
	if f.objects.openCount() != 0 {
		t.Fatal("a condemned attachment was decrypted")
	}
	if results := f.observer.observed(); len(results) != 1 || results[0] != "superseded" {
		t.Fatalf("observed %v, want one superseded", results)
	}
	acquired, released := f.fence.counts()
	if acquired != 1 || released != 1 {
		t.Fatalf("fence acquired %d, released %d, want 1 and 1", acquired, released)
	}
}

// Ordering 2: the job got there first. The invalidation has to wait for the
// render to finish rather than commit underneath it.
func TestAnInvalidationWaitsForARenderHoldingTheFence(t *testing.T) {
	f := newPreviewFixture(t)
	job := f.storeAttachment(t, []byte("bytes"), "image/png")
	f.store.enqueue(job)

	renderStarted := make(chan struct{})
	releaseRender := make(chan struct{})
	f.renderer.duringRender = func() {
		close(renderStarted)
		<-releaseRender
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := f.service.ProcessDue(context.Background()); err != nil {
			t.Errorf("process: %v", err)
		}
	}()

	<-renderStarted
	// The fence is held by the render, so an invalidation that needs it cannot
	// proceed. This is the window SR-001 was about.
	if !f.fence.held() {
		t.Fatal("the render must hold the fence while the parser runs")
	}

	invalidated := make(chan struct{})
	go func() {
		defer close(invalidated)
		handle, err := f.fence.Acquire(context.Background(), job.AttachmentID)
		if err != nil {
			t.Errorf("acquire: %v", err)
			return
		}
		f.store.reject(job.AttachmentID)
		handle.Release(context.Background())
	}()

	select {
	case <-invalidated:
		t.Fatal("the invalidation did not wait for the render")
	case <-time.After(50 * time.Millisecond):
		// Still waiting, which is the point.
	}

	close(releaseRender)
	<-done
	<-invalidated

	// The render ran to completion while the attachment was still logically
	// clean, and the verdict landed after it.
	if f.renderer.callCount() != 1 {
		t.Fatalf("renderer ran %d times, want 1", f.renderer.callCount())
	}
	if f.fence.held() {
		t.Fatal("the fence was not given back")
	}
}

// The fence is released on every path, so a failing render cannot leave an
// attachment locked against every future invalidation.
func TestTheFenceIsReleasedWhenTheRenderFails(t *testing.T) {
	f := newPreviewFixture(t)
	f.renderer.err = fmt.Errorf("%w: broken file", preview.ErrRender)
	f.store.enqueue(f.storeAttachment(t, []byte("bytes"), "image/png"))

	if _, err := f.service.ProcessDue(context.Background()); err != nil {
		t.Fatalf("process: %v", err)
	}
	if f.fence.held() {
		t.Fatal("a failed render must still give the fence back")
	}
	if _, released := f.fence.counts(); released != 1 {
		t.Fatalf("fence released %d times, want 1", released)
	}
}

// A cancelled job releases the fence too: a shutdown must not leave an
// attachment fenced for the next process.
func TestTheFenceIsReleasedWhenTheJobIsCancelled(t *testing.T) {
	f := newPreviewFixture(t)
	job := f.storeAttachment(t, []byte("bytes"), "image/png")
	f.store.enqueue(job)

	ctx, cancel := context.WithCancel(context.Background())
	f.renderer.duringRender = cancel

	if _, err := f.service.ProcessDue(ctx); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("process: %v", err)
	}
	if f.fence.held() {
		t.Fatal("a cancelled job must still give the fence back")
	}
}

// No fence, no render. An unavailable fence is a reason to stop, never a reason
// to proceed without one.
func TestProcessDueDoesNotRenderWhenTheFenceIsUnavailable(t *testing.T) {
	f := newPreviewFixture(t)
	f.fence.acquireErr = errors.New("no connection available")
	f.store.enqueue(f.storeAttachment(t, []byte("bytes"), "image/png"))

	if _, err := f.service.ProcessDue(context.Background()); err != nil {
		t.Fatalf("process: %v", err)
	}
	if f.renderer.callCount() != 0 {
		t.Fatal("the renderer ran without a fence")
	}
	if f.objects.openCount() != 0 {
		t.Fatal("the attachment was decrypted without a fence")
	}
}

// A service without a fence does not run at all.
func TestPreviewServiceRefusesToRunWithoutAFence(t *testing.T) {
	f := newPreviewFixture(t)
	unfenced := service.NewPreviewService(
		f.store, f.objects, f.keys, f.renderer, nil, f.cleanup, f.observer, discardLogger(),
	)
	if unfenced.Ready() {
		t.Fatal("a service with no fence must not report ready")
	}
	if _, err := unfenced.ProcessDue(context.Background()); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("error = %v, want ErrUnavailable", err)
	}
}

// --- claim fencing (CQ-002) -----------------------------------------------

// The scenario from the review: an attempt whose lease expired must not
// conclude the job a newer attempt is doing.
func TestAStaleAttemptCannotConcludeANewerOne(t *testing.T) {
	f := newPreviewFixture(t)
	job := f.storeAttachment(t, []byte("bytes"), "image/png")
	f.store.enqueue(job)

	// Attempt 1 claims and stalls; its lease expires and attempt 2 takes over.
	first, err := f.store.ClaimDuePreviews(context.Background(), 1, service.PreviewTuningForTest().Lease)
	if err != nil || len(first) != 1 {
		t.Fatalf("first claim: %d rows, err %v", len(first), err)
	}
	f.store.advance(service.PreviewTuningForTest().Lease + time.Second)
	second, err := f.store.ClaimDuePreviews(context.Background(), 1, service.PreviewTuningForTest().Lease)
	if err != nil || len(second) != 1 {
		t.Fatalf("second claim: %d rows, err %v", len(second), err)
	}
	if second[0].Attempts <= first[0].Attempts {
		t.Fatalf("claim token did not advance: %d then %d", first[0].Attempts, second[0].Attempts)
	}

	// The stale attempt now tries to conclude the job with a permanent failure.
	recorded, err := f.store.MarkPreviewTerminal(
		context.Background(), job.AttachmentID, first[0].Attempts, domain.PreviewStatusFailed)
	if err != nil {
		t.Fatalf("stale terminal: %v", err)
	}
	if recorded {
		t.Fatal("a stale attempt concluded a job it no longer owns")
	}

	// And the newer attempt's own publication still succeeds.
	published, err := f.store.MarkPreviewReady(context.Background(), service.PreviewResult{
		AttachmentID: job.AttachmentID, ClaimAttempt: second[0].Attempts,
		PreviewObjectID: uuid.NewString(), Size: 10,
		WrappedDEK: []byte{1}, KEKKeyID: "kek", EnvelopeVersion: 1, KeyWrapVersion: 2,
	})
	if err != nil || !published {
		t.Fatalf("the current attempt could not publish: %v %v", published, err)
	}
	if row := f.store.row(t, job.AttachmentID); row.status != domain.PreviewStatusReady {
		t.Fatalf("row is %q, want ready", row.status)
	}
}

// The reverse order: the newer attempt publishes first, and the stale one
// cannot overwrite a ready preview with a failure.
func TestAStaleAttemptCannotOverwriteAPublishedPreview(t *testing.T) {
	f := newPreviewFixture(t)
	job := f.storeAttachment(t, []byte("bytes"), "image/png")
	f.store.enqueue(job)

	first, _ := f.store.ClaimDuePreviews(context.Background(), 1, service.PreviewTuningForTest().Lease)
	f.store.advance(service.PreviewTuningForTest().Lease + time.Second)
	second, _ := f.store.ClaimDuePreviews(context.Background(), 1, service.PreviewTuningForTest().Lease)

	published, err := f.store.MarkPreviewReady(context.Background(), service.PreviewResult{
		AttachmentID: job.AttachmentID, ClaimAttempt: second[0].Attempts,
		PreviewObjectID: uuid.NewString(), Size: 10,
		WrappedDEK: []byte{1}, KEKKeyID: "kek", EnvelopeVersion: 1, KeyWrapVersion: 2,
	})
	if err != nil || !published {
		t.Fatalf("publish: %v %v", published, err)
	}

	recorded, err := f.store.MarkPreviewTerminal(
		context.Background(), job.AttachmentID, first[0].Attempts, domain.PreviewStatusFailed)
	if err != nil {
		t.Fatalf("stale terminal: %v", err)
	}
	if recorded {
		t.Fatal("a stale attempt overwrote a published preview")
	}
	if row := f.store.row(t, job.AttachmentID); row.status != domain.PreviewStatusReady {
		t.Fatalf("row is %q, want ready", row.status)
	}
}

// A stale attempt cannot publish either, so two workers cannot both install a
// preview for the same attachment.
func TestAStaleAttemptCannotPublish(t *testing.T) {
	f := newPreviewFixture(t)
	job := f.storeAttachment(t, []byte("bytes"), "image/png")
	f.store.enqueue(job)

	first, _ := f.store.ClaimDuePreviews(context.Background(), 1, service.PreviewTuningForTest().Lease)
	f.store.advance(service.PreviewTuningForTest().Lease + time.Second)
	if _, err := f.store.ClaimDuePreviews(
		context.Background(), 1, service.PreviewTuningForTest().Lease); err != nil {
		t.Fatalf("second claim: %v", err)
	}

	published, err := f.store.MarkPreviewReady(context.Background(), service.PreviewResult{
		AttachmentID: job.AttachmentID, ClaimAttempt: first[0].Attempts,
		PreviewObjectID: uuid.NewString(), Size: 10,
		WrappedDEK: []byte{1}, KEKKeyID: "kek", EnvelopeVersion: 1, KeyWrapVersion: 2,
	})
	if err != nil {
		t.Fatalf("stale publish: %v", err)
	}
	if published {
		t.Fatal("a stale attempt published over a newer claim")
	}
}

// An invalidation makes every outstanding token irrelevant without knowing any
// of them: the row is no longer pending, so no claim can conclude it.
func TestAnInvalidationInvalidatesEveryOutstandingClaim(t *testing.T) {
	for name, invalidate := range map[string]func(*previewFixture, string){
		"rejection": func(f *previewFixture, id string) { f.store.reject(id) },
		"removal":   func(f *previewFixture, id string) { f.store.remove(id) },
	} {
		t.Run(name, func(t *testing.T) {
			f := newPreviewFixture(t)
			job := f.storeAttachment(t, []byte("bytes"), "image/png")
			f.store.enqueue(job)

			claimed, _ := f.store.ClaimDuePreviews(
				context.Background(), 1, service.PreviewTuningForTest().Lease)
			invalidate(f, job.AttachmentID)

			published, err := f.store.MarkPreviewReady(context.Background(), service.PreviewResult{
				AttachmentID: job.AttachmentID, ClaimAttempt: claimed[0].Attempts,
				PreviewObjectID: uuid.NewString(), Size: 10,
				WrappedDEK: []byte{1}, KEKKeyID: "kek", EnvelopeVersion: 1, KeyWrapVersion: 2,
			})
			if err != nil || published {
				t.Fatalf("an invalidated claim published: %v %v", published, err)
			}
			recorded, err := f.store.MarkPreviewTerminal(
				context.Background(), job.AttachmentID, claimed[0].Attempts, domain.PreviewStatusFailed)
			if err != nil || recorded {
				t.Fatalf("an invalidated claim concluded: %v %v", recorded, err)
			}
		})
	}
}

// --- durable cleanup (SR-002) ---------------------------------------------

// A delete that fails must not end at a log line: the key has to reach the
// durable queue, or the object is lost to everyone.
func TestAFailedDiscardQueuesTheObjectForCleanup(t *testing.T) {
	f := newPreviewFixture(t)
	f.store.readyLost = true
	f.objects.deleteErr = errors.New("storage unavailable")
	f.store.enqueue(f.storeAttachment(t, []byte("bytes"), "image/png"))

	if _, err := f.service.ProcessDue(context.Background()); err != nil {
		t.Fatalf("process: %v", err)
	}

	queued := f.cleanup.queued()
	if len(queued) != 1 {
		t.Fatalf("queued %v, want exactly one object key", queued)
	}
	if !strings.HasPrefix(queued[0], "nchat/previews/") {
		t.Fatalf("queued %q, want the preview object's key", queued[0])
	}
	// The orphan counter is for objects nothing can recover. This one is
	// recoverable, so it must not be counted as leaked.
	if f.observer.value() != 0 {
		t.Fatalf("orphan metric = %d, want 0 for a queued object", f.observer.value())
	}
}

// A delete that succeeds queues nothing: the queue is for failures only.
func TestASuccessfulDiscardQueuesNothing(t *testing.T) {
	f := newPreviewFixture(t)
	f.store.readyLost = true
	f.store.enqueue(f.storeAttachment(t, []byte("bytes"), "image/png"))

	if _, err := f.service.ProcessDue(context.Background()); err != nil {
		t.Fatalf("process: %v", err)
	}
	if queued := f.cleanup.queued(); len(queued) != 0 {
		t.Fatalf("queued %v after a successful delete", queued)
	}
}

// Storage refused the delete and the queue refused the key: the only remaining
// leak, and it is counted rather than hidden.
func TestAnUnqueueableObjectIsCountedAsAnOrphan(t *testing.T) {
	f := newPreviewFixture(t)
	f.store.readyLost = true
	f.objects.deleteErr = errors.New("storage unavailable")
	f.cleanup.err = errors.New("database unavailable")
	f.store.enqueue(f.storeAttachment(t, []byte("bytes"), "image/png"))

	if _, err := f.service.ProcessDue(context.Background()); err != nil {
		t.Fatalf("process: %v", err)
	}
	if f.observer.value() != 1 {
		t.Fatalf("orphan metric = %d, want 1", f.observer.value())
	}
}

// --- the cleanup worker ---------------------------------------------------

// fakeCleanupStore models the durable queue closely enough to run a job across
// restarts: rows survive, claims lease them, and completing needs the token.
type fakeCleanupStore struct {
	mu sync.Mutex

	rows      map[string]*fakeCleanupRow
	claimErr  error
	completed []string
	// referenced makes IsObjectReferenced answer true, which is how a stale job
	// aimed at a live preview is stopped.
	referenced bool
	refErr     error
}

type fakeCleanupRow struct {
	key      string
	attempts int
}

func newFakeCleanupStore() *fakeCleanupStore {
	return &fakeCleanupStore{rows: map[string]*fakeCleanupRow{}}
}

func (s *fakeCleanupStore) Enqueue(_ context.Context, objectKey string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.rows[objectKey]; exists {
		return nil // the unique constraint: one job per object
	}
	s.rows[objectKey] = &fakeCleanupRow{key: objectKey}
	return nil
}

func (s *fakeCleanupStore) ClaimDueCleanups(
	_ context.Context, batchSize int, _ time.Duration,
) ([]service.ObjectCleanupJob, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.claimErr != nil {
		return nil, s.claimErr
	}
	var claimed []service.ObjectCleanupJob
	for key, row := range s.rows {
		if len(claimed) >= batchSize {
			break
		}
		row.attempts++
		claimed = append(claimed, service.ObjectCleanupJob{
			ID: key, ObjectKey: row.key, Attempts: row.attempts,
		})
	}
	return claimed, nil
}

func (s *fakeCleanupStore) Complete(_ context.Context, jobID string, claimAttempt int) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	row, exists := s.rows[jobID]
	if !exists || row.attempts != claimAttempt {
		return false, nil
	}
	delete(s.rows, jobID)
	s.completed = append(s.completed, jobID)
	return true, nil
}

func (s *fakeCleanupStore) IsObjectReferenced(_ context.Context, _ string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.referenced, s.refErr
}

func (s *fakeCleanupStore) pending() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.rows)
}

// The end-to-end shape of SR-002: the delete fails, the key survives, and a
// later pass — a different process, as far as the queue is concerned — removes
// the object and only then forgets the job.
func TestCleanupRetriesUntilTheObjectIsGone(t *testing.T) {
	store := newFakeCleanupStore()
	objects := newFakeObjects()
	objects.objects["nchat/previews/abc"] = []byte("an orphaned preview")
	if err := store.Enqueue(context.Background(), "nchat/previews/abc"); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	// First pass: storage is down, so the job survives.
	objects.deleteErr = errors.New("storage unavailable")
	cleanups := service.NewObjectCleanupService(store, objects, nil, discardLogger())
	if _, err := cleanups.ProcessDue(context.Background()); err != nil {
		t.Fatalf("first pass: %v", err)
	}
	if store.pending() != 1 {
		t.Fatal("a failed delete must leave the job in the queue")
	}
	if objects.count() != 1 {
		t.Fatal("the object must still be there")
	}

	// A fresh service, as after a restart: the queue is the only state.
	objects.deleteErr = nil
	restarted := service.NewObjectCleanupService(store, objects, nil, discardLogger())
	if _, err := restarted.ProcessDue(context.Background()); err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if store.pending() != 0 {
		t.Fatal("a completed cleanup must leave the queue")
	}
	if objects.count() != 0 {
		t.Fatal("the object must be gone")
	}
}

// An object that already vanished is a success: the storage client treats a
// missing object as deleted, which is exactly right for a retry after a delete
// that worked but whose acknowledgement was lost.
func TestCleanupTreatsAnAbsentObjectAsDone(t *testing.T) {
	store := newFakeCleanupStore()
	objects := newFakeObjects()
	if err := store.Enqueue(context.Background(), "nchat/previews/gone"); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	cleanups := service.NewObjectCleanupService(store, objects, nil, discardLogger())
	if _, err := cleanups.ProcessDue(context.Background()); err != nil {
		t.Fatalf("process: %v", err)
	}
	if store.pending() != 0 {
		t.Fatal("an absent object must complete the job")
	}
}

// A stale job aimed at an object a live preview now points at must not delete
// it: the cleanup worker must never be the thing that breaks a working preview.
func TestCleanupRefusesToDeleteAReferencedObject(t *testing.T) {
	store := newFakeCleanupStore()
	store.referenced = true
	objects := newFakeObjects()
	objects.objects["nchat/previews/live"] = []byte("a published preview")
	if err := store.Enqueue(context.Background(), "nchat/previews/live"); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	cleanups := service.NewObjectCleanupService(store, objects, nil, discardLogger())
	if _, err := cleanups.ProcessDue(context.Background()); err != nil {
		t.Fatalf("process: %v", err)
	}
	if objects.count() != 1 {
		t.Fatal("a referenced object was deleted")
	}
	// The job is finished because it was wrong, not because it succeeded.
	if store.pending() != 0 {
		t.Fatal("a job for a referenced object must not stay in the queue")
	}
}

// Enqueueing the same key twice produces one job: the unique constraint is what
// keeps a fast-failing caller from growing the queue without bound.
func TestCleanupEnqueueIsIdempotent(t *testing.T) {
	store := newFakeCleanupStore()
	for range 5 {
		if err := store.Enqueue(context.Background(), "nchat/previews/abc"); err != nil {
			t.Fatalf("enqueue: %v", err)
		}
	}
	if store.pending() != 1 {
		t.Fatalf("queue holds %d jobs, want 1", store.pending())
	}
}

// A worker whose lease expired cannot delete the queue entry a newer attempt is
// working on.
func TestAStaleCleanupWorkerCannotCompleteTheJob(t *testing.T) {
	store := newFakeCleanupStore()
	if err := store.Enqueue(context.Background(), "nchat/previews/abc"); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	first, _ := store.ClaimDueCleanups(context.Background(), 1, time.Minute)
	second, _ := store.ClaimDueCleanups(context.Background(), 1, time.Minute)
	if second[0].Attempts <= first[0].Attempts {
		t.Fatalf("claim token did not advance: %d then %d", first[0].Attempts, second[0].Attempts)
	}

	completed, err := store.Complete(context.Background(), first[0].ID, first[0].Attempts)
	if err != nil {
		t.Fatalf("stale complete: %v", err)
	}
	if completed {
		t.Fatal("a stale worker completed a job it no longer owns")
	}
	if store.pending() != 1 {
		t.Fatal("the job must still be in the queue")
	}
}

func TestCleanupReportsAFailedClaim(t *testing.T) {
	store := newFakeCleanupStore()
	store.claimErr = errors.New("connection refused")
	cleanups := service.NewObjectCleanupService(store, newFakeObjects(), nil, discardLogger())
	if _, err := cleanups.ProcessDue(context.Background()); err == nil {
		t.Fatal("expected the claim failure to be reported")
	}
}

func TestCleanupRefusesToRunHalfWired(t *testing.T) {
	incomplete := service.NewObjectCleanupService(nil, nil, nil, discardLogger())
	if incomplete.Ready() {
		t.Fatal("a service with no dependencies must not report ready")
	}
	if _, err := incomplete.ProcessDue(context.Background()); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("error = %v, want ErrUnavailable", err)
	}
}

// The cleanup worker's remaining branches: a reference check that fails, and a
// completion that fails after the object is already gone.
func TestCleanupRetriesWhenTheReferenceCheckFails(t *testing.T) {
	store := newFakeCleanupStore()
	store.refErr = errors.New("database unavailable")
	objects := newFakeObjects()
	objects.objects["nchat/previews/abc"] = []byte("an orphaned preview")
	if err := store.Enqueue(context.Background(), "nchat/previews/abc"); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	cleanups := service.NewObjectCleanupService(store, objects, nil, discardLogger())
	if _, err := cleanups.ProcessDue(context.Background()); err != nil {
		t.Fatalf("process: %v", err)
	}
	// Nothing was deleted and nothing was forgotten: the job waits.
	if objects.count() != 1 || store.pending() != 1 {
		t.Fatalf("objects=%d jobs=%d, want 1 and 1", objects.count(), store.pending())
	}
}

// The object is gone but the queue entry could not be removed. The next attempt
// finds nothing to delete and completes: duplicated work, never a lost object.
func TestCleanupConvergesWhenCompletionFailsAfterTheDelete(t *testing.T) {
	store := newCompletionFailingStore()
	objects := newFakeObjects()
	objects.objects["nchat/previews/abc"] = []byte("an orphaned preview")
	if err := store.Enqueue(context.Background(), "nchat/previews/abc"); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	cleanups := service.NewObjectCleanupService(store, objects, nil, discardLogger())
	if _, err := cleanups.ProcessDue(context.Background()); err != nil {
		t.Fatalf("first pass: %v", err)
	}
	if objects.count() != 0 {
		t.Fatal("the object should have been deleted")
	}
	if store.pending() != 1 {
		t.Fatal("a failed completion must leave the job in the queue")
	}

	store.failComplete = false
	if _, err := cleanups.ProcessDue(context.Background()); err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if store.pending() != 0 {
		t.Fatal("the retry must complete the job")
	}
}

// completionFailingStore fails Complete once, which is the window between a
// successful delete and forgetting the job.
type completionFailingStore struct {
	*fakeCleanupStore
	failComplete bool
}

func newCompletionFailingStore() *completionFailingStore {
	return &completionFailingStore{fakeCleanupStore: newFakeCleanupStore(), failComplete: true}
}

func (s *completionFailingStore) Complete(
	ctx context.Context, jobID string, claimAttempt int,
) (bool, error) {
	if s.failComplete {
		return false, errors.New("database unavailable")
	}
	return s.fakeCleanupStore.Complete(ctx, jobID, claimAttempt)
}

// A cancelled pass stops between jobs, leaving the rest for the next one.
func TestCleanupStopsWhenTheContextIsCancelled(t *testing.T) {
	store := newFakeCleanupStore()
	for _, key := range []string{"nchat/previews/a", "nchat/previews/b"} {
		if err := store.Enqueue(context.Background(), key); err != nil {
			t.Fatalf("enqueue: %v", err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	cleanups := service.NewObjectCleanupService(store, newFakeObjects(), nil, discardLogger())
	processed, err := cleanups.ProcessDue(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if processed != 0 {
		t.Fatalf("a cancelled pass processed %d jobs", processed)
	}
}

// A nil logger is replaced rather than dereferenced, like every other service
// in this package.
func TestObjectCleanupServiceToleratesANilLogger(t *testing.T) {
	store := newFakeCleanupStore()
	if err := store.Enqueue(context.Background(), "nchat/previews/abc"); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	cleanups := service.NewObjectCleanupService(store, newFakeObjects(), nil, nil)
	if !cleanups.Ready() {
		t.Fatal("a fully wired service must be ready")
	}
	if _, err := cleanups.ProcessDue(context.Background()); err != nil {
		t.Fatalf("process: %v", err)
	}
}

// The cleanup outcomes reach the observer, so an operator can tell a retry from
// a removal without reading logs.
func TestCleanupOutcomesAreObserved(t *testing.T) {
	store := newFakeCleanupStore()
	objects := newFakeObjects()
	objects.objects["nchat/previews/abc"] = []byte("an orphaned preview")
	if err := store.Enqueue(context.Background(), "nchat/previews/abc"); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	observer := &cleanupObserver{}

	cleanups := service.NewObjectCleanupService(store, objects, observer, discardLogger())
	if _, err := cleanups.ProcessDue(context.Background()); err != nil {
		t.Fatalf("process: %v", err)
	}
	if results := observer.observed(); len(results) != 1 || results[0] != "removed" {
		t.Fatalf("observed %v, want one removed", results)
	}
}

// The two workers count on two different series, and this is the test that
// keeps them apart.
//
// It is a compile-time property as much as a runtime one: the cleanup worker
// takes an ObjectCleanupObserver, which has no ObservePreview method, so a
// future wiring that pointed it back at the preview counter would not build.
// The runtime half asserts the vocabulary each one actually emits — "retry"
// exists in both, and that collision is exactly what made the shared counter
// unreadable during a storage outage.
func TestCleanupResultsNeverReachThePreviewCounter(t *testing.T) {
	previews := &previewObserver{}
	cleanups := &cleanupObserver{}

	store := newFakeCleanupStore()
	objects := newFakeObjects()
	objects.objects["nchat/previews/removable"] = []byte("an orphaned preview")
	if err := store.Enqueue(context.Background(), "nchat/previews/removable"); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	svc := service.NewObjectCleanupService(store, objects, cleanups, discardLogger())
	if _, err := svc.ProcessDue(context.Background()); err != nil {
		t.Fatalf("process: %v", err)
	}

	// A second pass where storage refuses, so the "retry" label — the one that
	// collides with the preview worker's vocabulary — is exercised too.
	objects.deleteErr = errors.New("storage unavailable")
	if err := store.Enqueue(context.Background(), "nchat/previews/stuck"); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if _, err := svc.ProcessDue(context.Background()); err != nil {
		t.Fatalf("process: %v", err)
	}

	observed := cleanups.observed()
	if len(observed) != 2 {
		t.Fatalf("the cleanup worker observed %v, want two results", observed)
	}
	for _, result := range observed {
		if result != "removed" && result != "retry" {
			t.Fatalf("cleanup emitted %q, which is not in its vocabulary", result)
		}
	}
	// The point of the whole change: nothing the cleanup worker did shows up as
	// a preview outcome.
	if results := previews.observed(); len(results) != 0 {
		t.Fatalf("cleanup results leaked onto the preview counter: %v", results)
	}
}
