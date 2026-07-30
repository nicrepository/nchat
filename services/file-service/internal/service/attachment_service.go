// Package service contains file-service application use cases.
package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/nicrepository/nchat/services/file-service/internal/crypto"
	"github.com/nicrepository/nchat/services/file-service/internal/domain"
)

// sniffLimit is how much plaintext is inspected to detect the real content
// type. It is the amount net/http.DetectContentType consumes; reading more
// would buy nothing and cost memory.
const sniffLimit = 512

// compensationTimeout bounds the cleanup that follows a failed upload. It runs
// on a context detached from the request so a client that has already hung up
// cannot leave an object behind.
const compensationTimeout = 10 * time.Second

// Failure codes persisted on a failed row. They are short, closed and
// sanitised: no driver text, no storage host, no key material.
const (
	failureStorageWrite     = "storage_write"
	failureMetadataFinalize = "metadata_finalize"
)

// DestinationAuthInput asks whether a session may attach a file to a
// destination. The workspace is deliberately not part of the input.
type DestinationAuthInput struct {
	Destination domain.Destination
	UserID      string
	SessionID   string
}

// AuthorizedDestination is the server's answer, including the canonical
// workspace read from the destination row.
type AuthorizedDestination struct {
	ID               string
	WorkspaceID      string
	SessionExpiresAt time.Time
}

// DestinationAuthorizer resolves upload authorization.
type DestinationAuthorizer interface {
	AuthorizeDestination(ctx context.Context, input DestinationAuthInput) (AuthorizedDestination, error)
}

// NewAttachment is the row an upload starts from.
type NewAttachment struct {
	ID               string
	WorkspaceID      string
	UploaderID       string
	Destination      domain.Destination
	Filename         string
	DeclaredMIME     string
	StorageProvider  string
	StorageObjectKey string
	EnvelopeVersion  int
	WrappedDEK       []byte
}

// UploadedAttachment finalises a row once its object is durable.
type UploadedAttachment struct {
	ID             string
	Status         domain.Status
	DetectedMIME   string
	Size           int64
	CiphertextSize int64
}

// AttachmentAuthInput identifies an attachment read attempt.
type AttachmentAuthInput struct {
	AttachmentID string
	UserID       string
	SessionID    string
}

// StoredAttachment is the persisted row, including the fields that must never
// leave this process: the storage object key and the wrapped data key.
type StoredAttachment struct {
	ID               string
	WorkspaceID      string
	Kind             domain.DestinationKind
	Status           domain.Status
	Filename         string
	DeclaredMIME     string
	DetectedMIME     string
	Size             int64
	StorageObjectKey string
	EnvelopeVersion  int
	WrappedDEK       []byte
	CreatedAt        time.Time
	SessionExpiresAt time.Time
}

// ListDestinationAttachmentsQuery is a resolved, already-authorised listing
// request. Both identifiers are server-derived: the workspace comes from the
// destination row, never from the caller.
type ListDestinationAttachmentsQuery struct {
	WorkspaceID string
	ChannelID   string
	Limit       int
}

// ListedAttachment is the row a listing loads. It deliberately omits the
// storage object key, the envelope version and the wrapped data key: a list
// never needs them, and not selecting them means they cannot leak through a
// listing bug.
type ListedAttachment struct {
	ID           string
	Status       domain.Status
	Filename     string
	DetectedMIME string
	Size         int64
	CreatedAt    time.Time
}

// AttachmentStore is the metadata half of attachment persistence.
type AttachmentStore interface {
	CreatePending(ctx context.Context, attachment NewAttachment) error
	MarkUploaded(ctx context.Context, update UploadedAttachment) error
	MarkFailed(ctx context.Context, attachmentID, failureCode string) error
	GetAuthorized(ctx context.Context, input AttachmentAuthInput) (StoredAttachment, error)
	ListChannelAttachments(ctx context.Context, query ListDestinationAttachmentsQuery) ([]ListedAttachment, error)
}

// ListChannelAttachmentsInput asks for a channel's most recent attachments.
// The workspace is absent by design — it is derived from the channel row during
// authorization, exactly like an upload's.
type ListChannelAttachmentsInput struct {
	ChannelID string
	UserID    string
	SessionID string
	Limit     int
}

// ObjectStore is the blob half. It is intentionally narrow so the service and
// the domain never depend on SeaweedFS or on HTTP.
type ObjectStore interface {
	Put(ctx context.Context, key string, body io.Reader) (int64, error)
	Open(ctx context.Context, key string) (io.ReadCloser, error)
	Delete(ctx context.Context, key string) error
}

// OrphanObserver counts the one failure the caller cannot see: an upload that
// failed and whose cleanup also failed, leaving an object in storage with no
// usable row. It carries no labels, so it can never leak an identifier.
type OrphanObserver interface {
	ObserveOrphanedObject()
}

// UploadInput is one attachment upload. Content is streamed, never buffered.
type UploadInput struct {
	Destination  domain.Destination
	UserID       string
	SessionID    string
	Filename     string
	DeclaredMIME string
	Content      io.Reader
}

// AttachmentView is the client-facing projection. Storage keys, object
// identifiers, workspace-internal wiring and every byte of key material are
// absent by construction: this struct is the only thing handlers serialise.
type AttachmentView struct {
	ID              string    `json:"id"`
	Filename        string    `json:"filename"`
	ContentType     string    `json:"contentType"`
	Size            int64     `json:"size"`
	Status          string    `json:"status"`
	DestinationKind string    `json:"destinationKind"`
	CreatedAt       time.Time `json:"createdAt"`
}

// Download is an authorised, decrypted content stream. Closing Content closes
// the underlying storage response.
type Download struct {
	Filename    string
	ContentType string
	Size        int64
	Content     io.ReadCloser
}

// AttachmentService owns the upload and download use cases, including the
// ordering that keeps PostgreSQL and SeaweedFS consistent without a shared
// transaction.
type AttachmentService struct {
	authorizer     DestinationAuthorizer
	store          AttachmentStore
	objects        ObjectStore
	kek            *crypto.KeyEncryptionKey
	maxUploadBytes int64
	scanRequired   bool
	orphans        OrphanObserver
	logger         *slog.Logger
}

// NewAttachmentService wires the use cases. maxUploadBytes is the validated
// RF-32 cap; scanRequired decides the state a finished upload lands in.
func NewAttachmentService(
	authorizer DestinationAuthorizer,
	store AttachmentStore,
	objects ObjectStore,
	kek *crypto.KeyEncryptionKey,
	maxUploadBytes int64,
	scanRequired bool,
	orphans OrphanObserver,
	logger *slog.Logger,
) *AttachmentService {
	if logger == nil {
		logger = slog.Default()
	}
	return &AttachmentService{
		authorizer:     authorizer,
		store:          store,
		objects:        objects,
		kek:            kek,
		maxUploadBytes: maxUploadBytes,
		scanRequired:   scanRequired,
		orphans:        orphans,
		logger:         logger,
	}
}

// Ready reports whether every dependency the use cases need is wired.
func (s *AttachmentService) Ready() bool {
	return s != nil && s.authorizer != nil && s.store != nil &&
		s.objects != nil && s.kek != nil && s.maxUploadBytes > 0
}

// uploadTarget is the authorised destination of an upload, with every value
// the server decided itself: the caller supplies none of them directly.
type uploadTarget struct {
	destination  domain.Destination
	workspaceID  string
	uploaderID   string
	filename     string
	declaredMIME string
}

// sniffedContent is the head of the stream plus the type detected from it.
type sniffedContent struct {
	head         []byte
	detectedMIME string
}

// pendingAttachment is a row in pending_upload whose object write has been
// started. It carries no key material: the data key lives only inside the
// encrypting stream and is never held alongside the row it protects.
type pendingAttachment struct {
	id        uuid.UUID
	objectKey string
}

// Upload authorises, encrypts and persists one attachment.
//
// PostgreSQL and SeaweedFS cannot share a transaction, so the order is chosen
// so that no partial failure can ever produce a downloadable attachment:
//
//  1. authorise, and derive the workspace from the destination row;
//  2. reject an empty upload before anything is written anywhere;
//  3. insert the row as pending_upload — not downloadable in any state;
//  4. stream the encrypted object to storage;
//  5. only then move the row to its post-upload state.
//
// The row is created only once the write is about to start, so a failure is
// either before the row exists — nothing to undo — or after a write attempt,
// which goes through compensatePersistedObject. No path returns success for an
// upload that did not complete.
func (s *AttachmentService) Upload(ctx context.Context, input UploadInput) (AttachmentView, error) {
	if !s.Ready() {
		return AttachmentView{}, domain.ErrDependenciesUnavailable
	}
	target, err := s.authorizeUploadTarget(ctx, input)
	if err != nil {
		return AttachmentView{}, err
	}

	source := &boundedSource{src: input.Content, remaining: s.maxUploadBytes}
	content, err := sniffContent(source)
	if err != nil {
		return AttachmentView{}, err
	}

	pending, encrypted, err := s.preparePendingAttachment(ctx, target, content, source)
	if err != nil {
		return AttachmentView{}, err
	}

	ciphertextSize, err := s.persistEncryptedObject(ctx, pending, encrypted, source)
	if err != nil {
		return AttachmentView{}, err
	}
	return s.finalizeUpload(ctx, target, pending, content, source, ciphertextSize)
}

// authorizeUploadTarget validates what the caller sent and resolves the
// destination server-side. The workspace comes from the destination row, never
// from the request.
func (s *AttachmentService) authorizeUploadTarget(
	ctx context.Context, input UploadInput,
) (uploadTarget, error) {
	filename, err := domain.NormalizeFilename(input.Filename)
	if err != nil {
		return uploadTarget{}, err
	}
	uploaderID, sessionID, err := parsePrincipal(input.UserID, input.SessionID)
	if err != nil {
		return uploadTarget{}, err
	}
	authorized, err := s.authorizer.AuthorizeDestination(ctx, DestinationAuthInput{
		Destination: input.Destination,
		UserID:      uploaderID,
		SessionID:   sessionID,
	})
	if err != nil {
		return uploadTarget{}, err
	}
	return uploadTarget{
		destination:  domain.Destination{Kind: input.Destination.Kind, ID: authorized.ID},
		workspaceID:  authorized.WorkspaceID,
		uploaderID:   uploaderID,
		filename:     filename,
		declaredMIME: domain.NormalizeDeclaredMIME(input.DeclaredMIME),
	}, nil
}

// sniffContent reads the detection window and rejects an empty upload before
// anything is persisted. The declared type is never consulted here.
func sniffContent(source io.Reader) (sniffedContent, error) {
	head, err := readHead(source)
	if err != nil {
		return sniffedContent{}, uploadSourceError(err)
	}
	if len(head) == 0 {
		return sniffedContent{}, domain.ErrEmptyFile
	}
	return sniffedContent{head: head, detectedMIME: http.DetectContentType(head)}, nil
}

// preparePendingAttachment does everything that must succeed before the first
// byte can reach storage: identity, key material, the encrypting stream, and
// only then the row.
//
// The row is written last on purpose. It makes "a row exists but no storage
// write was ever attempted" unrepresentable, so compensation never has to guess
// whether there is an object to clean up — every failure is either before the
// row (nothing to undo) or after a write attempt (cleanup required).
func (s *AttachmentService) preparePendingAttachment(
	ctx context.Context, target uploadTarget, content sniffedContent, source io.Reader,
) (pendingAttachment, io.Reader, error) {
	attachmentID := uuid.New()
	dataKey, err := crypto.NewDataKey()
	if err != nil {
		return pendingAttachment{}, nil, fmt.Errorf("prepare attachment key: %w", err)
	}
	wrappedDEK, err := s.kek.Wrap(dataKey, attachmentID)
	if err != nil {
		return pendingAttachment{}, nil, fmt.Errorf("wrap attachment key: %w", err)
	}
	encrypted, err := crypto.NewEncryptingReader(
		io.MultiReader(bytes.NewReader(content.head), source), dataKey, attachmentID,
	)
	if err != nil {
		return pendingAttachment{}, nil, fmt.Errorf("prepare attachment envelope: %w", err)
	}

	pending := pendingAttachment{
		id:        attachmentID,
		objectKey: domain.StorageObjectKey(attachmentID),
	}
	if err := s.store.CreatePending(ctx, NewAttachment{
		ID:               attachmentID.String(),
		WorkspaceID:      target.workspaceID,
		UploaderID:       target.uploaderID,
		Destination:      target.destination,
		Filename:         target.filename,
		DeclaredMIME:     target.declaredMIME,
		StorageProvider:  domain.StorageProviderSeaweedFS,
		StorageObjectKey: pending.objectKey,
		EnvelopeVersion:  crypto.EnvelopeVersion,
		WrappedDEK:       wrappedDEK,
	}); err != nil {
		// The insert failed, so no row and no object exist: nothing to undo.
		return pendingAttachment{}, nil, fmt.Errorf("persist attachment metadata: %w", err)
	}
	return pending, encrypted, nil
}

// persistEncryptedObject streams the ciphertext to storage and compensates on
// failure. A failed write may still have left a partial object behind, so it is
// treated as persisted and cleaned up.
func (s *AttachmentService) persistEncryptedObject(
	ctx context.Context, pending pendingAttachment, encrypted io.Reader, source *boundedSource,
) (int64, error) {
	ciphertextSize, err := s.objects.Put(ctx, pending.objectKey, encrypted)
	if err == nil {
		return ciphertextSize, nil
	}
	return 0, s.compensatePersistedObject(ctx, pending, failureStorageWrite,
		uploadFailureCause(err, source))
}

// finalizeUpload advances the row once the object is durable. Only here can an
// attachment leave pending_upload for a post-upload state.
func (s *AttachmentService) finalizeUpload(
	ctx context.Context,
	target uploadTarget,
	pending pendingAttachment,
	content sniffedContent,
	source *boundedSource,
	ciphertextSize int64,
) (AttachmentView, error) {
	status := domain.StatusPendingScan
	if !s.scanRequired {
		status = domain.StatusClean
	}
	size := s.maxUploadBytes - source.remaining

	if err := s.store.MarkUploaded(ctx, UploadedAttachment{
		ID:             pending.id.String(),
		Status:         status,
		DetectedMIME:   content.detectedMIME,
		Size:           size,
		CiphertextSize: ciphertextSize,
	}); err != nil {
		return AttachmentView{}, s.compensatePersistedObject(ctx, pending, failureMetadataFinalize,
			fmt.Errorf("finalize attachment metadata: %w", err))
	}
	return AttachmentView{
		ID:              pending.id.String(),
		Filename:        target.filename,
		ContentType:     content.detectedMIME,
		Size:            size,
		Status:          string(status),
		DestinationKind: string(target.destination.Kind),
		CreatedAt:       time.Now().UTC(),
	}, nil
}

// uploadFailureCause attributes a storage write failure to whichever side
// actually broke: a client stream that stopped, or storage itself.
func uploadFailureCause(storageErr error, source *boundedSource) error {
	if source.err != nil {
		return uploadSourceError(source.err)
	}
	return storageErr
}

// Metadata returns the client-facing projection of an attachment the caller can
// currently reach. It is authorised on every call, like the download.
func (s *AttachmentService) Metadata(ctx context.Context, input AttachmentAuthInput) (AttachmentView, error) {
	if !s.Ready() {
		return AttachmentView{}, domain.ErrDependenciesUnavailable
	}
	record, err := s.authorizedAttachment(ctx, input)
	if err != nil {
		return AttachmentView{}, err
	}
	return AttachmentView{
		ID:              record.ID,
		Filename:        record.Filename,
		ContentType:     contentType(record),
		Size:            record.Size,
		Status:          string(record.Status),
		DestinationKind: string(record.Kind),
		CreatedAt:       record.CreatedAt,
	}, nil
}

// ListChannelAttachments returns a channel's most recent attachments for a
// caller who may currently reach that channel (issue #435).
//
// Authorization is the upload path's, unchanged: AuthorizeDestination resolves
// the channel and its canonical workspace in one query and answers ErrNotFound
// for a channel that does not exist, is archived, is private and not the
// caller's, or belongs to another workspace. The listing query is then bound to
// the workspace that query returned, so a channel UUID from another tenant can
// never select that tenant's rows.
//
// The result is a preview, not an archive: the limit is clamped in the domain
// and the order is fixed server-side, so a client cannot ask for an unbounded
// scan or a different one.
func (s *AttachmentService) ListChannelAttachments(
	ctx context.Context, input ListChannelAttachmentsInput,
) ([]AttachmentView, error) {
	if !s.Ready() {
		return nil, domain.ErrDependenciesUnavailable
	}
	destination, err := domain.NewDestination(domain.DestinationKindChannel, input.ChannelID)
	if err != nil {
		// An unparseable channel id is answered like an invisible one, so the
		// route cannot be used to tell "malformed" from "not yours".
		return nil, domain.ErrNotFound
	}
	userID, sessionID, err := parsePrincipal(input.UserID, input.SessionID)
	if err != nil {
		return nil, err
	}
	authorized, err := s.authorizer.AuthorizeDestination(ctx, DestinationAuthInput{
		Destination: destination,
		UserID:      userID,
		SessionID:   sessionID,
	})
	if err != nil {
		return nil, err
	}
	records, err := s.store.ListChannelAttachments(ctx, ListDestinationAttachmentsQuery{
		WorkspaceID: authorized.WorkspaceID,
		ChannelID:   authorized.ID,
		Limit:       domain.NormalizeAttachmentListLimit(input.Limit),
	})
	if err != nil {
		return nil, err
	}
	views := make([]AttachmentView, 0, len(records))
	for _, record := range records {
		contentType := record.DetectedMIME
		if contentType == "" {
			contentType = domain.DefaultContentType
		}
		views = append(views, AttachmentView{
			ID:              record.ID,
			Filename:        record.Filename,
			ContentType:     contentType,
			Size:            record.Size,
			Status:          string(record.Status),
			DestinationKind: string(domain.DestinationKindChannel),
			CreatedAt:       record.CreatedAt,
		})
	}
	return views, nil
}

// Download re-authorises, refuses anything the scan has not cleared, and
// returns a decrypting stream. Integrity is verified chunk by chunk as the
// caller reads, so a modified or truncated object fails mid-response rather
// than being served as valid content.
func (s *AttachmentService) Download(ctx context.Context, input AttachmentAuthInput) (Download, error) {
	if !s.Ready() {
		return Download{}, domain.ErrDependenciesUnavailable
	}
	record, err := s.downloadableAttachment(ctx, input)
	if err != nil {
		return Download{}, err
	}
	content, err := s.openDecryptedContent(ctx, record)
	if err != nil {
		return Download{}, err
	}
	return Download{
		Filename:    record.Filename,
		ContentType: contentType(record),
		Size:        record.Size,
		Content:     content,
	}, nil
}

// downloadableAttachment resolves the row and decides whether it may be served
// at all. It answers the access questions only; nothing here touches storage or
// key material.
func (s *AttachmentService) downloadableAttachment(
	ctx context.Context, input AttachmentAuthInput,
) (StoredAttachment, error) {
	record, err := s.authorizedAttachment(ctx, input)
	if err != nil {
		return StoredAttachment{}, err
	}
	if !record.Status.Downloadable() {
		return StoredAttachment{}, domain.ErrNotDownloadable
	}
	if record.EnvelopeVersion != crypto.EnvelopeVersion {
		return StoredAttachment{}, fmt.Errorf("unsupported envelope version %d", record.EnvelopeVersion)
	}
	return record, nil
}

// openDecryptedContent turns an authorised row into a verified plaintext
// stream. Integrity is checked chunk by chunk as the caller reads, so a
// modified or truncated object fails mid-stream instead of being served whole.
func (s *AttachmentService) openDecryptedContent(
	ctx context.Context, record StoredAttachment,
) (io.ReadCloser, error) {
	attachmentID, err := uuid.Parse(record.ID)
	if err != nil {
		return nil, fmt.Errorf("invalid stored attachment id")
	}
	dataKey, err := s.kek.Unwrap(record.WrappedDEK, attachmentID)
	if err != nil {
		return nil, fmt.Errorf("unwrap attachment key: %w", err)
	}
	body, err := s.openStoredObject(ctx, record)
	if err != nil {
		return nil, err
	}
	plaintext, err := crypto.NewDecryptingReader(body, dataKey, attachmentID)
	if err != nil {
		_ = body.Close()
		return nil, fmt.Errorf("prepare attachment envelope: %w", err)
	}
	return readCloser{Reader: plaintext, closer: body}, nil
}

// openStoredObject fetches the ciphertext, translating a missing object into an
// operational failure rather than a client-visible 404: metadata says the object
// exists, so reporting "not found" would describe storage to the caller.
func (s *AttachmentService) openStoredObject(
	ctx context.Context, record StoredAttachment,
) (io.ReadCloser, error) {
	body, err := s.objects.Open(ctx, record.StorageObjectKey)
	if err == nil {
		return body, nil
	}
	if !errors.Is(err, domain.ErrNotFound) {
		return nil, err
	}
	s.logger.LogAttrs(ctx, slog.LevelError, "attachment object missing in storage",
		slog.String("attachment_id", record.ID),
		slog.String("workspace_id", record.WorkspaceID),
	)
	return nil, fmt.Errorf("%w: attachment object unavailable", domain.ErrUnavailable)
}

func (s *AttachmentService) authorizedAttachment(
	ctx context.Context, input AttachmentAuthInput,
) (StoredAttachment, error) {
	userID, sessionID, err := parsePrincipal(input.UserID, input.SessionID)
	if err != nil {
		return StoredAttachment{}, err
	}
	attachmentID, err := uuid.Parse(input.AttachmentID)
	if err != nil {
		// An unparseable id is answered like an invisible one, so the route
		// cannot be used to tell "malformed" from "not yours".
		return StoredAttachment{}, domain.ErrNotFound
	}
	return s.store.GetAuthorized(ctx, AttachmentAuthInput{
		AttachmentID: attachmentID.String(),
		UserID:       userID,
		SessionID:    sessionID,
	})
}

// compensatePersistedObject ends an upload whose object may already be in
// storage. The order is deliberate and the two steps are dependent, never
// independent:
//
//   - Delete succeeds: the object is gone, so the row may safely advance to
//     failed. If MarkFailed then fails, the row stays pending_upload, which is
//     recoverable and still indexed; nothing is left in storage, so no orphan
//     is counted and the object is never recreated.
//   - Delete fails: the object is still there. MarkFailed is NOT called, so the
//     row stays pending_upload and remains covered by idx_attachments_pending —
//     advancing it to failed would drop the only stored pointer to an object
//     that still exists. The orphan metric is incremented and a structured log
//     records that cleanup is outstanding.
//
// Either way the caller receives an error carrying both the original cause and
// the cleanup failure; no branch reports success.
func (s *AttachmentService) compensatePersistedObject(
	ctx context.Context, pending pendingAttachment, failureCode string, cause error,
) error {
	cleanupCtx, cancel := s.compensationContext(ctx)
	defer cancel()

	if deleteErr := s.objects.Delete(cleanupCtx, pending.objectKey); deleteErr != nil {
		s.observeOrphan()
		s.logCompensation(cleanupCtx, pending, failureCode, compensationOutcome{
			objectStored:   true,
			cleanupRun:     true,
			cleanupFailed:  true,
			stateAdvanced:  false,
			remainingState: string(domain.StatusPendingUpload),
		})
		return errors.Join(cause, fmt.Errorf("delete attachment object: %w", deleteErr))
	}

	if markErr := s.store.MarkFailed(cleanupCtx, pending.id.String(), failureCode); markErr != nil {
		s.logCompensation(cleanupCtx, pending, failureCode, compensationOutcome{
			objectStored:   true,
			cleanupRun:     true,
			stateAdvanced:  false,
			remainingState: string(domain.StatusPendingUpload),
		})
		return errors.Join(cause, fmt.Errorf("mark attachment failed: %w", markErr))
	}
	return cause
}

// compensationOutcome is what the operator needs to know from a log line: did
// an object exist, was cleanup attempted, did it work, and what state is the
// row recoverable from.
type compensationOutcome struct {
	objectStored   bool
	cleanupRun     bool
	cleanupFailed  bool
	stateAdvanced  bool
	remainingState string
}

// compensationContext detaches cleanup from the request so a client that has
// already hung up cannot leave an object behind.
func (s *AttachmentService) compensationContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), compensationTimeout)
}

func (s *AttachmentService) observeOrphan() {
	if s.orphans != nil {
		s.orphans.ObserveOrphanedObject()
	}
}

// logCompensation records the operational facts, and only those: no filename,
// no storage key, no driver text, no key material.
func (s *AttachmentService) logCompensation(
	ctx context.Context, pending pendingAttachment, failureCode string, outcome compensationOutcome,
) {
	message := "attachment upload compensation incomplete"
	if outcome.cleanupFailed {
		message = "attachment object cleanup pending"
	}
	s.logger.LogAttrs(ctx, slog.LevelError, message,
		slog.String("attachment_id", pending.id.String()),
		slog.String("failure_code", failureCode),
		slog.Bool("object_stored", outcome.objectStored),
		slog.Bool("cleanup_attempted", outcome.cleanupRun),
		slog.Bool("cleanup_failed", outcome.cleanupFailed),
		slog.Bool("state_advanced", outcome.stateAdvanced),
		slog.String("recoverable_state", outcome.remainingState),
	)
}

func contentType(record StoredAttachment) string {
	if record.DetectedMIME == "" {
		return domain.DefaultContentType
	}
	return record.DetectedMIME
}

func parsePrincipal(userID, sessionID string) (string, string, error) {
	parsedUser, err := uuid.Parse(userID)
	if err != nil {
		return "", "", domain.ErrUnauthorized
	}
	parsedSession, err := uuid.Parse(sessionID)
	if err != nil {
		return "", "", domain.ErrUnauthorized
	}
	return parsedUser.String(), parsedSession.String(), nil
}

// readHead pulls at most sniffLimit bytes so the content type can be detected
// before anything is persisted, without consuming the rest of the stream.
func readHead(source io.Reader) ([]byte, error) {
	head := make([]byte, sniffLimit)
	n, err := io.ReadFull(source, head)
	switch {
	case err == nil, errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return head[:n], nil
	default:
		return nil, err
	}
}

// uploadSourceError maps a failure of the client's stream to a client error.
// A storage failure never reaches this function.
func uploadSourceError(err error) error {
	switch {
	case errors.Is(err, domain.ErrTooLarge):
		return domain.ErrTooLarge
	case errors.Is(err, domain.ErrInvalidInput):
		return err
	default:
		return fmt.Errorf("%w: upload stream failed", domain.ErrInvalidInput)
	}
}

// boundedSource enforces the RF-32 cap on the bytes actually read, regardless
// of what Content-Length claimed, and remembers the first failure so the
// service can tell a client-side stream problem from a storage problem.
type boundedSource struct {
	src       io.Reader
	remaining int64
	err       error
}

func (b *boundedSource) Read(p []byte) (int, error) {
	if b.err != nil {
		return 0, b.err
	}
	// Read at most one byte past the budget: enough to detect an over-sized
	// upload, never enough to buffer one.
	if int64(len(p)) > b.remaining+1 {
		p = p[:b.remaining+1]
	}
	n, err := b.src.Read(p)
	b.remaining -= int64(n)
	if b.remaining < 0 {
		b.err = domain.ErrTooLarge
		return 0, b.err
	}
	if err != nil && !errors.Is(err, io.EOF) {
		b.err = sourceFailure(err)
		return n, b.err
	}
	return n, err
}

// sourceFailure normalises a client-stream failure.
//
// The translation is not cosmetic. A truncated request surfaces as
// io.ErrUnexpectedEOF, which io.ReadFull also uses to mean "short final read";
// letting it through would make the encrypting reader close the envelope and
// accept a half-transferred upload as a complete, shorter file. Replacing it
// with an error outside the EOF family keeps truncation a failure.
func sourceFailure(err error) error {
	if errors.Is(err, domain.ErrTooLarge) || errors.Is(err, domain.ErrInvalidInput) {
		return err
	}
	return fmt.Errorf("%w: upload stream failed", domain.ErrInvalidInput)
}

// readCloser pairs a decrypting reader with the storage stream underneath it,
// so a handler closing the response body closes the connection too.
type readCloser struct {
	io.Reader
	closer io.Closer
}

func (r readCloser) Close() error { return r.closer.Close() }
