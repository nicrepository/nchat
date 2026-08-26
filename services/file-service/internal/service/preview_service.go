package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/nicrepository/nchat/services/file-service/internal/crypto"
	"github.com/nicrepository/nchat/services/file-service/internal/domain"
	"github.com/nicrepository/nchat/services/file-service/internal/preview"
)

// Preview job tuning. These are constants, not configuration: they bound how
// much work one process will take on, and every one of them is a safety
// property rather than a preference.
//
// The four durations are one system and are written as such — the lease is
// derived from the timeout it has to outlive rather than being a number chosen
// next to it. A lease shorter than the work it protects is not a small
// mistake: it hands a row that is still being rendered to a second worker, and
// the two then race to publish previews of the same attachment.
const (
	// previewBatchSize is how many attachments one pass claims: exactly one.
	//
	// A pass renders serially, so claiming N would put N rows under a single
	// lease while N-1 of them sit untouched — the last one's lease would have
	// to cover the whole batch's rendering time. One claim per pass makes the
	// lease cover exactly the work it is holding.
	previewBatchSize = 1

	// previewMaxRenderAttempts is the renderer's whole budget for one
	// attachment. It bounds how much CPU a single hostile or simply broken file
	// can consume across its lifetime, and it is deliberately the *only*
	// bounded budget: see ProcessDue for why claiming is not bounded.
	previewMaxRenderAttempts = 3

	// previewJobTimeout bounds one attachment end to end: reading it back,
	// rendering it, storing the result. It is what stops a hostile document
	// from occupying the worker.
	previewJobTimeout = 45 * time.Second

	// previewCompensationTimeout bounds one detached write that has to happen
	// after the job's own context is gone: discarding an object that must not
	// be kept, or recording a terminal state. A job can need two of them.
	previewCompensationTimeout = 10 * time.Second

	// previewLeaseMargin is what the lease adds on top of the job itself: the
	// detached cleanup and terminal write that follow a failure, plus room for
	// clock skew between replicas. It is not slack for a slower render — that
	// is what previewJobTimeout bounds.
	previewLeaseMargin = 30 * time.Second

	// previewLease is how long a claim is respected before another worker may
	// take the row. Derived, never chosen: a job that is still running can
	// therefore not be claimed a second time, while a job whose process died
	// becomes due again once the lease expires, with no janitor to run.
	previewLease = previewJobTimeout + previewLeaseMargin
)

// Compile-time proof that the lease outlives everything one claim can do: the
// render, and the two detached writes that can follow it. Converting a negative
// constant to uint does not compile, so shortening the lease — or lengthening
// the timeout — past that point fails the build rather than silently allowing
// two workers onto the same row.
const _ = uint(previewLease - (previewJobTimeout + 2*previewCompensationTimeout))

// Preview outcomes, used as metric label values and log results. Closed set,
// decided here, so no identifier can ever become a label.
const (
	previewResultReady       = "ready"
	previewResultUnsupported = "unsupported"
	previewResultFailed      = "failed"
	previewResultRetry       = "retry"
	// previewResultSuperseded is a claim that lost its right to run before it
	// rendered anything: the attachment was invalidated, or a newer attempt
	// took over. It is not a failure — the row is already in the state someone
	// else decided — so it is counted apart from one.
	previewResultSuperseded = "superseded"
)

// PreviewJob is one claimed attachment, with everything needed to read its
// content back. It is not an authorization decision: the worker acts on rows
// the service itself finalised, never on behalf of a caller, and it never
// serves what it reads.
type PreviewJob struct {
	AttachmentID     string
	WorkspaceID      string
	DetectedMIME     string
	OriginalFilename string
	Size             int64
	StorageObjectKey string
	EnvelopeVersion  int
	WrappedDEK       []byte
	KEKKeyID         string
	KeyWrapVersion   int
	// Attempts is the count *including* this claim, so a job that reports
	// previewMaxRenderAttempts has no render left after this one.
	//
	// It is also this claim's **fencing token**, and that is the more important
	// of its two jobs. A lease is a promise about time, and time is exactly
	// what a stalled worker cannot be trusted about: an attempt whose lease
	// expired is still running, still holding a rendered image, and still able
	// to write. Without a fence it would write — concluding a job a newer
	// attempt is in the middle of, and throwing that attempt's valid preview
	// away.
	//
	// Because the claim increments the counter atomically and returns the new
	// value, every claim gets a strictly larger token than the one before it,
	// and every completion requires its own. A stale attempt then matches no
	// row and learns it has lost ownership.
	//
	// The one place the guarantee thins is saturation: the counter is capped so
	// it cannot overflow its column, so after tens of thousands of consecutive
	// claims — weeks of an unwritable database — two attempts could share a
	// token. That row is in permanent failure long before then.
	Attempts int
}

// previewPersistOutcome is what persist accomplished, for the cases where it
// accomplished something.
//
// It exists because "the row was not updated" is a third answer, and an error
// return only has two. A claim that loses the race has not failed — the
// attachment was rejected, removed, or taken over by a newer attempt, and every
// one of those is the system working — so representing it as an error would
// misreport it as one. Representing it as nil, which is what happened before,
// misreported it as a published preview.
type previewPersistOutcome uint8

const (
	// previewPersistPublished: the row now points at this attempt's object.
	previewPersistPublished previewPersistOutcome = iota
	// previewPersistSuperseded: the row moved on. The object this attempt
	// produced has been discarded and nothing was written.
	previewPersistSuperseded
)

// PreviewResult is a produced preview, ready to be recorded against its
// attachment. Like UploadedAttachment, the length and the key material travel
// together because the wrapped key authenticates the length.
//
// The fields above PageCount describe page one exactly as they always have —
// that object stays the one files.attachments.preview_object_id points at, so
// every existing reader of this struct is unaffected by pages beyond it.
type PreviewResult struct {
	AttachmentID string
	// ClaimAttempt is the fencing token of the claim that produced this
	// preview. Publishing requires it to still be the row's current one, so an
	// attempt that lost the race cannot overwrite the attempt that won it.
	ClaimAttempt    int
	PreviewObjectID string
	Size            int64
	WrappedDEK      []byte
	KEKKeyID        string
	EnvelopeVersion int
	KeyWrapVersion  int

	// PageCount is the total number of pages actually rendered and stored,
	// page one included. It is the count achieved inside the job's budget,
	// never the document's true page count if the two differ (see
	// preview.renderPDFPages) — a preview always describes exactly what was
	// published, never what was attempted.
	PageCount int
	// ExtraPages holds pages 2..PageCount. Empty for every image and every
	// one-page PDF, which is every preview this service produced before this
	// field existed.
	ExtraPages []PreviewPage
	// ContentType is what every page in this result decodes to — the same
	// value for page one and every extra page, since a render never mixes
	// content types across its own pages. domain.PreviewContentTypeJPEG for
	// every renderer that predates this field.
	ContentType string
}

// PreviewPage is one page beyond the first, ready to be recorded in
// files.attachment_preview_pages. It carries the same binding shape as page
// one because it is stored exactly the same way: its own object, its own data
// key, the same envelope.
type PreviewPage struct {
	PageNumber      int
	ObjectID        string
	Size            int64
	WrappedDEK      []byte
	KEKKeyID        string
	EnvelopeVersion int
	KeyWrapVersion  int
	// ContentType mirrors PreviewResult.ContentType — carried per page because
	// GetPreviewPage reads a page in isolation, never alongside the result
	// that produced it.
	ContentType string
}

// PreviewStore is the persistence half of preview generation.
type PreviewStore interface {
	// ClaimDuePreviews takes up to batchSize due rows and leases them.
	ClaimDuePreviews(ctx context.Context, batchSize int, lease time.Duration) ([]PreviewJob, error)
	// MarkPreviewReady records a produced preview. It reports false when the
	// row was no longer pending, which means another attempt won the race and
	// this one's object must be discarded.
	MarkPreviewReady(ctx context.Context, result PreviewResult) (bool, error)
	// MarkPreviewTerminal records an outcome with no object: unsupported or
	// failed. It reports false when the row was no longer pending or when the
	// claim that is concluding it is no longer the current one.
	MarkPreviewTerminal(
		ctx context.Context, attachmentID string, claimAttempt int, status domain.PreviewStatus,
	) (bool, error)
	// RevalidateClaim reports whether this claim may still open the attachment.
	// It is called inside the fence, immediately before anything is decrypted.
	RevalidateClaim(ctx context.Context, attachmentID string, claimAttempt int) (bool, error)
}

// AttachmentFence serialises rendering an attachment against invalidating it.
//
// The preview job holds it across the whole handoff — revalidation, decryption
// and the parser — so a scan verdict or a removal cannot commit underneath a
// running decoder. It is an interface here so the job can be tested with
// deterministic ordering, and PostgreSQL advisory locks in production.
type AttachmentFence interface {
	Acquire(ctx context.Context, attachmentID string) (FenceHandle, error)
}

// FenceHandle is a held fence, released exactly once on every path.
type FenceHandle interface {
	Release(ctx context.Context)
}

// PreviewRenderer turns plaintext into the preview pages plus the one content
// type every one of those pages decodes to. It is an interface so the job can
// be tested without a decoder, and so the decoders stay in a package that
// knows nothing about storage or the database. Every non-PDF/sheet render is
// a length-1 slice; a PDF may return more, all JPEG; a spreadsheet/CSV
// returns exactly one page of bounded table JSON.
type PreviewRenderer interface {
	Render(ctx context.Context, detectedMIME string, src io.Reader) (pages [][]byte, contentType string, err error)
}

type documentPreviewRenderer interface {
	RenderDocument(ctx context.Context, detectedMIME, originalFilename string, src io.Reader) (pages [][]byte, contentType string, err error)
}

// PreviewObserver counts preview outcomes. It carries no labels beyond the
// closed result set above.
type PreviewObserver interface {
	ObservePreview(result string)
	ObserveOrphanedObject()
}

// PreviewService generates and persists attachment previews (RF-31).
//
// It is the asynchronous half of the feature: the upload path only records that
// a preview is wanted, and everything expensive — reading the object back,
// decoding it, rendering, encrypting, storing — happens here, later, on a
// worker's schedule.
type PreviewService struct {
	store    PreviewStore
	objects  ObjectStore
	keys     *crypto.Keyring
	renderer PreviewRenderer
	fence    AttachmentFence
	// cleanup is the durable queue a failed delete falls back to. It is
	// optional only in the sense that the job still runs without one — it then
	// counts an orphan it cannot recover, which is the behaviour this field
	// exists to replace.
	cleanup  ObjectCleanupEnqueuer
	observer PreviewObserver
	logger   *slog.Logger
}

// ObjectCleanupEnqueuer records an object that has to be removed later.
type ObjectCleanupEnqueuer interface {
	Enqueue(ctx context.Context, objectKey string) error
}

func NewPreviewService(
	store PreviewStore,
	objects ObjectStore,
	keys *crypto.Keyring,
	renderer PreviewRenderer,
	fence AttachmentFence,
	cleanup ObjectCleanupEnqueuer,
	observer PreviewObserver,
	logger *slog.Logger,
) *PreviewService {
	if logger == nil {
		logger = slog.Default()
	}
	return &PreviewService{
		store: store, objects: objects, keys: keys,
		renderer: renderer, fence: fence, cleanup: cleanup,
		observer: observer, logger: logger,
	}
}

// Ready reports whether every dependency the job needs is wired.
//
// The fence counts: without it the job would decrypt and parse content that an
// invalidation may already have condemned, so a service missing one does not
// run at all rather than running unfenced.
func (s *PreviewService) Ready() bool {
	return s != nil && s.store != nil && s.objects != nil && s.keys != nil &&
		s.renderer != nil && s.fence != nil
}

// ProcessDue claims and processes one batch, returning how many rows it took.
//
// A zero return means there was nothing to do, which is the normal case: the
// caller uses it to decide nothing, it exists so a test can assert progress.
// Cancellation stops between jobs and inside them — ctx is passed all the way
// to storage, to the renderer and back — so a shutdown does not wait for a
// whole batch.
//
// # Why claiming is unbounded and rendering is not
//
// The two budgets are separate on purpose, and conflating them is what makes a
// row unrecoverable. A claim is one indexed UPDATE; a render decrypts an
// attachment and feeds it to a parser. Bounding the claim by the same counter
// as the render means that when PostgreSQL is unavailable at the moment a
// terminal state has to be written, the row keeps its "pending" state, keeps
// its exhausted attempt count, and is never selected again — a preview stuck
// forever because the database blinked at the wrong moment.
//
// So the claim has no ceiling: while a row is pending it stays eligible, and a
// database that comes back finds it waiting. The renderer's budget is what is
// bounded, in process below — past previewMaxRenderAttempts a claim no longer
// decrypts or renders anything, it only finishes the row off. The cost of a row
// nobody can finalise is therefore one UPDATE per lease, not one render.
func (s *PreviewService) ProcessDue(ctx context.Context) (int, error) {
	if !s.Ready() {
		return 0, domain.ErrDependenciesUnavailable
	}
	jobs, err := s.store.ClaimDuePreviews(ctx, previewBatchSize, previewLease)
	if err != nil {
		return 0, fmt.Errorf("claim due previews: %w", err)
	}
	processed := 0
	for _, job := range jobs {
		if ctx.Err() != nil {
			// The claim's lease is still held, so the rows this pass did not
			// reach simply become due again. Nothing is lost by stopping here.
			return processed, ctx.Err()
		}
		s.process(ctx, job)
		processed++
	}
	return processed, nil
}

// process runs one attachment to a conclusion, or leaves it for another attempt.
//
// Every outcome is deliberate and none of them touches the attachment itself:
// a preview that cannot be produced leaves the file exactly as it was, still
// downloadable, and only changes what the client draws next to it.
func (s *PreviewService) process(ctx context.Context, job PreviewJob) {
	started := time.Now()

	// The render budget is spent. This claim exists only to finish the row off,
	// so nothing is decrypted and the parser is never reached: the attachment
	// has already had every render it is going to get.
	if job.Attempts > previewMaxRenderAttempts {
		s.finalizeExhausted(ctx, job, started)
		return
	}

	jobCtx, cancel := context.WithTimeout(ctx, previewJobTimeout)
	defer cancel()

	// The fence is taken before anything is opened and held until the render
	// has finished, so an invalidation either commits before this — and the
	// revalidation below then refuses the job — or waits for it. There is no
	// third ordering in which a verdict lands while the parser is running.
	handle, err := s.fence.Acquire(jobCtx, job.AttachmentID)
	if err != nil {
		// Never assume the fence: no fence, no render.
		s.finishFailed(ctx, job, fmt.Errorf("acquire attachment fence: %w", err), started)
		return
	}
	defer handle.Release(ctx)

	// Inside the fence, so this answer cannot go stale before it is used.
	valid, err := s.store.RevalidateClaim(jobCtx, job.AttachmentID, job.Attempts)
	if err != nil {
		s.finishFailed(ctx, job, fmt.Errorf("revalidate claim: %w", err), started)
		return
	}
	if !valid {
		// The attachment was condemned, removed, or taken by a newer attempt
		// while this one was queuing for the fence. Nothing is decrypted,
		// nothing is parsed, and nothing is written: whoever invalidated the
		// row already recorded the state it belongs in.
		s.observePreview(previewResultSuperseded)
		s.logPreview(ctx, slog.LevelInfo, job, previewResultSuperseded, started)
		return
	}

	rendered, contentType, err := s.render(jobCtx, job)
	if err != nil {
		s.finishFailed(ctx, job, err, started)
		return
	}
	outcome, err := s.persist(jobCtx, job, rendered, contentType)
	if err != nil {
		s.finishFailed(ctx, job, err, started)
		return
	}
	if outcome == previewPersistSuperseded {
		// The render happened but nothing was published: this attempt lost the
		// row. It is the same outcome the revalidation above reports, reached a
		// few seconds later, and it is counted as that rather than as a preview
		// this attempt produced. Nothing is retried and nothing is marked failed
		// — whoever won the row already recorded the state it belongs in.
		s.observePreview(previewResultSuperseded)
		s.logPreview(ctx, slog.LevelInfo, job, previewResultSuperseded, started)
		return
	}
	s.observePreview(previewResultReady)
	s.logPreview(ctx, slog.LevelInfo, job, previewResultReady, started)
}

// finalizeExhausted ends a row whose renders are all spent.
//
// It is the answer to "the render worked, or classified itself, and then the
// database would not take the answer". Rather than rendering again — the budget
// is gone — the row is recorded as failed, which is the honest description of
// what happened: this service could not produce and publish a preview for it.
//
// The classification is only lost in that doubly-degraded case. Whenever
// PostgreSQL accepts the write at any point inside the render budget, the state
// the renderer actually decided is what lands, unsupported included.
//
// A failure here writes nothing, and that is safe: the row stays pending and
// stays claimable, so the next pass tries the same single UPDATE again.
func (s *PreviewService) finalizeExhausted(ctx context.Context, job PreviewJob, started time.Time) {
	if !s.markTerminal(ctx, job, domain.PreviewStatusFailed, started) {
		return
	}
	s.observePreview(previewResultFailed)
	s.logPreview(ctx, slog.LevelWarn, job, previewResultFailed, started)
}

// render reads the attachment back and produces the preview image, one JPEG
// per page.
//
// The content is opened exactly the way a download opens it — same key ring,
// same binding, same verifying reader — so a tampered or truncated object fails
// here too rather than being rendered into a preview of something else.
func (s *PreviewService) render(ctx context.Context, job PreviewJob) ([][]byte, string, error) {
	content, err := openEncryptedObject(ctx, s.keys, s.objects, s.logger, encryptedObject{
		objectID:        job.AttachmentID,
		workspaceID:     job.WorkspaceID,
		objectKey:       job.StorageObjectKey,
		size:            job.Size,
		wrappedDEK:      job.WrappedDEK,
		kekKeyID:        job.KEKKeyID,
		envelopeVersion: job.EnvelopeVersion,
		keyWrapVersion:  job.KeyWrapVersion,
	})
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = content.Close() }()

	if renderer, ok := s.renderer.(documentPreviewRenderer); ok {
		return renderer.RenderDocument(ctx, job.DetectedMIME, job.OriginalFilename, content)
	}
	return s.renderer.Render(ctx, job.DetectedMIME, content)
}

// persistedPage is one encrypted-and-stored page, kept around only long
// enough to either land in the published PreviewResult or be discarded.
type persistedPage struct {
	objectID   uuid.UUID
	objectKey  string
	size       int64
	wrappedDEK []byte
	kekKeyID   string
}

// persist encrypts every rendered page, stores each as its own object and
// records the whole set in one write.
//
// Every page is a first-class encrypted object: its own random identity, its
// own data key, the same envelope and the same key ring as the attachment it
// belongs to. Nothing about any of them is weaker for being derived — a
// thumbnail of a document is still that document's content.
//
// The ordering mirrors the upload path for the same reason: the row only points
// at objects that are already durable, so no state is reachable in which a
// preview is servable but some of its pages are absent.
func (s *PreviewService) persist(
	ctx context.Context, job PreviewJob, images [][]byte, contentType string,
) (previewPersistOutcome, error) {
	workspaceID, err := uuid.Parse(job.WorkspaceID)
	if err != nil {
		return previewPersistSuperseded, fmt.Errorf("invalid stored workspace id")
	}
	if contentType == "" {
		// Every renderer built before this field existed produces JPEG and
		// nothing else.
		contentType = domain.PreviewContentTypeJPEG
	}

	pages := make([]persistedPage, 0, len(images))
	for _, image := range images {
		page, err := s.persistPage(ctx, workspaceID, image)
		if err != nil {
			// page may itself name an object that reached storage before the
			// failure, so it joins the pages already persisted for cleanup.
			s.discardPersistedPages(ctx, job, append(pages, page))
			return previewPersistSuperseded, err
		}
		pages = append(pages, page)
	}

	first := pages[0]
	extra := make([]PreviewPage, 0, len(pages)-1)
	for i, page := range pages[1:] {
		extra = append(extra, PreviewPage{
			PageNumber:      i + 2,
			ObjectID:        page.objectID.String(),
			Size:            page.size,
			WrappedDEK:      page.wrappedDEK,
			KEKKeyID:        page.kekKeyID,
			EnvelopeVersion: crypto.EnvelopeVersion,
			KeyWrapVersion:  crypto.KeyWrapVersion,
			ContentType:     contentType,
		})
	}

	recorded, err := s.store.MarkPreviewReady(ctx, PreviewResult{
		AttachmentID:    job.AttachmentID,
		ClaimAttempt:    job.Attempts,
		PreviewObjectID: first.objectID.String(),
		Size:            first.size,
		WrappedDEK:      first.wrappedDEK,
		KEKKeyID:        first.kekKeyID,
		EnvelopeVersion: crypto.EnvelopeVersion,
		KeyWrapVersion:  crypto.KeyWrapVersion,
		PageCount:       len(pages),
		ExtraPages:      extra,
		ContentType:     contentType,
	})
	if err != nil {
		s.discardPersistedPages(ctx, job, pages)
		return previewPersistSuperseded, fmt.Errorf("record preview: %w", err)
	}
	if !recorded {
		// This attempt no longer owns the job: another claim superseded it, the
		// scan condemned the attachment, or it was removed. Every object here
		// is this attempt's own — nothing else ever writes to these keys — so
		// discarding all of them cannot affect whatever won, and keeping any
		// would leave an object nothing points at.
		//
		// The outcome goes back to the caller rather than being logged here, so
		// there is one record of it, with the job's real duration, on the same
		// path every other outcome is reported from.
		s.discardPersistedPages(ctx, job, pages)
		return previewPersistSuperseded, nil
	}
	return previewPersistPublished, nil
}

// persistPage encrypts and stores one rendered page. It does not record
// anything against the attachment — that happens once, for every page
// together, in persist.
func (s *PreviewService) persistPage(
	ctx context.Context, workspaceID uuid.UUID, image []byte,
) (persistedPage, error) {
	objectID := uuid.New()
	dataKey, err := crypto.NewDataKey()
	if err != nil {
		return persistedPage{}, fmt.Errorf("prepare preview key: %w", err)
	}
	encrypted, err := crypto.NewEncryptingReader(bytes.NewReader(image), dataKey, objectID)
	if err != nil {
		return persistedPage{}, fmt.Errorf("prepare preview envelope: %w", err)
	}

	objectKey := domain.PreviewObjectKey(objectID)
	ciphertextSize, err := s.objects.Put(ctx, objectKey, encrypted)
	if err != nil {
		// A failed write may still have left a partial object behind, so it is
		// treated as stored and left for the caller to discard.
		return persistedPage{objectID: objectID, objectKey: objectKey},
			fmt.Errorf("store preview object: %w", err)
	}
	size := int64(len(image))
	if want := crypto.CiphertextSize(size); ciphertextSize != want {
		// A failed write may still have left a partial object behind, so it is
		// treated as stored and left for the caller to discard.
		return persistedPage{objectID: objectID, objectKey: objectKey}, fmt.Errorf(
			"stored preview envelope is %d bytes, expected %d", ciphertextSize, want)
	}

	wrappedDEK, keyID, err := s.keys.Wrap(dataKey, crypto.Binding{
		AttachmentID:           objectID,
		WorkspaceID:            workspaceID,
		PlaintextSize:          size,
		KeyWrapVersion:         crypto.KeyWrapVersion,
		ContentEnvelopeVersion: crypto.EnvelopeVersion,
	})
	if err != nil {
		return persistedPage{objectID: objectID, objectKey: objectKey}, fmt.Errorf(
			"wrap preview key: %w", err)
	}
	return persistedPage{
		objectID: objectID, objectKey: objectKey, size: size,
		wrappedDEK: wrappedDEK, kekKeyID: keyID,
	}, nil
}

// discardPersistedPages discards every page in pages that reached storage.
//
// It is the multi-page answer to what a single-page job always guaranteed:
// nothing servable is left half-written, and nothing storable is left
// unaccounted-for. A render that produces N pages but fails to publish must
// not leak N-1 of them just because only the last one hit an error — every
// object this attempt put into storage is this attempt's own, and all of them
// are discarded together, in one pass, on every failure path in persist.
func (s *PreviewService) discardPersistedPages(ctx context.Context, job PreviewJob, pages []persistedPage) {
	for _, page := range pages {
		if page.objectKey == "" {
			// Nothing reached storage for this page — a key-prep failure
			// before the first Put — so there is nothing to discard.
			continue
		}
		s.discardPreviewObject(ctx, job, page.objectKey)
	}
}

// discardPreviewObject removes a preview object that must not survive: a failed
// write, a mismatched envelope, a lost race, or a row that could not be updated.
//
// It runs on a context detached from the job's, because the job's may be
// precisely what expired. A delete that fails leaves an object nothing points
// at, which is counted on the same orphan metric the upload path uses.
func (s *PreviewService) discardPreviewObject(ctx context.Context, job PreviewJob, objectKey string) {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), previewCompensationTimeout)
	defer cancel()

	if err := s.objects.Delete(cleanupCtx, objectKey); err == nil {
		return
	}
	// The delete failed, so the key must outlive this process (SR-002). A log
	// line and a counter are not a work queue: they tell an operator that an
	// object leaked without giving anything the means to remove it. Recording
	// the key durably is what turns a leak into a retry.
	if s.cleanup != nil {
		if err := s.cleanup.Enqueue(cleanupCtx, objectKey); err == nil {
			s.logger.LogAttrs(cleanupCtx, slog.LevelWarn, "preview object queued for cleanup",
				slog.String("attachment_id", job.AttachmentID),
			)
			return
		}
	}
	// Storage refused the delete *and* the queue could not take the key. This
	// is the only path that still leaks, and it is counted rather than hidden.
	s.observeOrphan()
	s.logger.LogAttrs(cleanupCtx, slog.LevelError, "preview object cleanup pending",
		slog.String("attachment_id", job.AttachmentID),
		slog.Bool("object_stored", true),
		slog.Bool("cleanup_failed", true),
		slog.Bool("cleanup_queued", false),
	)
}

// finishFailed decides what a failed attempt leaves behind.
//
// The three outcomes are genuinely different and the client and the operator
// both need them apart:
//
//   - unsupported: this content will never have a preview, and that is normal.
//     The UI shows an icon; nobody is paged;
//   - failed: a file that claimed a supported type could not be rendered, or a
//     transient failure has used up its attempts. The UI shows the same icon,
//     but the metric says something is wrong;
//   - retry: a transient failure with attempts left. Nothing is written at all
//     — the lease simply expires and the row becomes due again, which is why
//     there is no 'processing' state to get stuck in.
func (s *PreviewService) finishFailed(ctx context.Context, job PreviewJob, cause error, started time.Time) {
	status, result := previewFailureOutcome(cause)
	if result == previewResultRetry {
		s.observePreview(previewResultRetry)
		s.logPreview(ctx, slog.LevelWarn, job, previewResultRetry, started)
		return
	}
	if !s.markTerminal(ctx, job, status, started) {
		return
	}
	level := slog.LevelInfo
	if result == previewResultFailed {
		level = slog.LevelWarn
	}
	s.observePreview(result)
	s.logPreview(ctx, level, job, result, started)
}

// markTerminal records a terminal state on a context detached from the job's,
// because the job's own may be exactly what expired.
//
// It reports whether the state is now written. A false return has left the row
// pending on purpose: the next claim retries the write, and that retry is what
// keeps a database outage from stranding a row forever. It is deliberately
// *not* an error the caller has to decide about — there is only one correct
// response and it is "leave it for the next pass".
func (s *PreviewService) markTerminal(
	ctx context.Context, job PreviewJob, status domain.PreviewStatus, started time.Time,
) bool {
	markCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), previewCompensationTimeout)
	defer cancel()

	if _, err := s.store.MarkPreviewTerminal(markCtx, job.AttachmentID, job.Attempts, status); err != nil {
		s.observePreview(previewResultRetry)
		s.logPreviewOutcome(markCtx, slog.LevelError, job, "state_write_failed", time.Since(started))
		return false
	}
	return true
}

// previewFailureOutcome classifies a failure into a persisted state.
//
// The renderer's two sentinels are permanent by construction. Everything else —
// storage, the database, a cancelled context — is transient, and a transient
// failure writes nothing at all: the row stays pending and the lease expiring
// is what makes it due again. Whether any renders remain is not this function's
// question; process answers it before a render is ever attempted.
func previewFailureOutcome(cause error) (domain.PreviewStatus, string) {
	switch {
	case errors.Is(cause, preview.ErrUnsupported):
		return domain.PreviewStatusUnsupported, previewResultUnsupported
	case errors.Is(cause, preview.ErrRender):
		return domain.PreviewStatusFailed, previewResultFailed
	default:
		return domain.PreviewStatusPending, previewResultRetry
	}
}

func (s *PreviewService) observePreview(result string) {
	if s.observer != nil {
		s.observer.ObservePreview(result)
	}
}

func (s *PreviewService) observeOrphan() {
	if s.observer != nil {
		s.observer.ObserveOrphanedObject()
	}
}

func (s *PreviewService) logPreview(
	ctx context.Context, level slog.Level, job PreviewJob, result string, started time.Time,
) {
	s.logPreviewOutcome(ctx, level, job, result, time.Since(started))
}

// logPreviewOutcome records the operational facts and only those.
//
// The attachment id is here because it is the identifier operators already
// correlate uploads and downloads by, and it is not secret. The filename, the
// content type of a specific file, the storage keys, the renderer's message and
// every byte of key material are deliberately absent: a preview failure must
// never become the log line that describes what someone uploaded.
func (s *PreviewService) logPreviewOutcome(
	ctx context.Context, level slog.Level, job PreviewJob, result string, duration time.Duration,
) {
	format := previewFormat(job)
	reason := previewReason(result)
	workerName, _ := os.Hostname()
	s.logger.LogAttrs(ctx, level, "attachment preview completed",
		slog.String("attachment_id", job.AttachmentID),
		slog.String("format", format),
		slog.Int64("size_bytes", job.Size),
		slog.String("result", result),
		slog.String("reason", reason),
		slog.String("worker", workerName),
		slog.Int("attempt", job.Attempts),
		slog.Int64("duration_ms", duration.Milliseconds()),
	)
	if observer, ok := s.observer.(interface {
		ObserveDocumentPreview(string, string, string, time.Duration)
	}); ok {
		observer.ObserveDocumentPreview(format, result, reason, duration)
	}
}

func previewFormat(job PreviewJob) string {
	mime := domain.NormalizeDetectedMIME(job.DetectedMIME)
	switch {
	case mime == "application/pdf":
		return "pdf"
	case mime == "text/plain":
		return "csv"
	case mime == "application/zip":
		return "office_zip"
	case mime == "application/octet-stream" && strings.EqualFold(filepath.Ext(job.OriginalFilename), ".ppt"):
		return "ppt"
	case strings.HasPrefix(mime, "image/"):
		return "image"
	default:
		return "unknown"
	}
}

func previewReason(result string) string {
	switch result {
	case previewResultReady:
		return "none"
	case previewResultUnsupported:
		return "unsupported_format"
	case previewResultFailed:
		return "render_failed"
	case previewResultRetry:
		return "transient"
	case previewResultSuperseded:
		return "superseded"
	default:
		return "state_write_failed"
	}
}
