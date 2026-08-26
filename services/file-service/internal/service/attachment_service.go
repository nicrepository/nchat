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
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/nicrepository/nchat/libs/go/platform/uploadpolicy"
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

const (
	UploadPurposeMessageDraft = "message_draft"
	messageDraftTTL           = 24 * time.Hour
)

// Failure codes persisted on a failed row. They are short, closed and
// sanitised: no driver text, no storage host, no key material.
const (
	failureStorageWrite = "storage_write"
	// failureEnvelopeIncomplete records a stored object whose size is not the
	// envelope the counted plaintext must produce.
	failureEnvelopeIncomplete = "envelope_incomplete"
	// failureKeyWrap records a data key that could not be sealed against the
	// final binding, which leaves the object unopenable and so unusable.
	failureKeyWrap          = "key_wrap"
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
	ID          string
	WorkspaceID string
	// MaxUploadBytes is the destination workspace's RF-32 attachment size
	// policy, read from the same row in the same query as the workspace itself.
	// It is never taken from the request, and resolving it costs no extra round
	// trip, so the policy an upload is judged against is loaded exactly once,
	// before the first byte is read.
	//
	// Zero means "no usable policy on the row" — a workspace written before
	// migration 000020. Callers resolve it through uploadpolicy, which answers
	// the default and never "no limit".
	MaxUploadBytes   int64
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
	// KeyWrapVersion is the version of the wrapped key's binding format, tracked
	// separately from the content envelope's version.
	//
	// It is supplied here, on the pending row, rather than at finalisation even
	// though no key exists yet. The column is NOT NULL with no default, so an
	// INSERT that omits it fails: that is the schema fence migration 000002
	// installs against a file-service still running the previous build, and a
	// fence that only engaged at finalisation would let such a build create the
	// row in the first place.
	KeyWrapVersion int
	DraftExpiresAt *time.Time
}

// UploadedAttachment finalises a row once its object is durable. It carries the
// key material as well as the sizes, because the two are inseparable: the
// wrapped key authenticates Size, so persisting them apart would allow a row
// whose recorded length and whose sealed length disagree.
//
// The wrap version is absent by design: it was fixed when the row was created.
type UploadedAttachment struct {
	ID     string
	Status domain.Status
	// PreviewStatus schedules the preview job, or declares up front that there
	// will never be one. It is written by the same UPDATE that finishes the
	// upload, which is what makes the job durable without a queue: an
	// attachment that exists and can be previewed is, by that same statement,
	// already scheduled, so no restart can lose the work and no second write
	// can disagree with the first.
	PreviewStatus  domain.PreviewStatus
	DetectedMIME   string
	Size           int64
	CiphertextSize int64
	WrappedDEK     []byte
	// KEKKeyID names the key-encryption key that sealed WrappedDEK. It is not
	// secret and it is what makes rotation possible: a download selects the key
	// by this id instead of trying the ones it has.
	KEKKeyID string
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
	KEKKeyID         string
	KeyWrapVersion   int
	CreatedAt        time.Time
	SessionExpiresAt time.Time

	// Preview state (RF-31). The preview is a separate stored object with its
	// own identity and its own data key, so it carries its own binding
	// alongside the attachment's; nothing here is shared with the fields above
	// except the workspace.
	//
	// Every one of them is empty unless PreviewStatus is ready, which the
	// database CHECK enforces, so a partially written preview cannot be served.
	PreviewStatus          domain.PreviewStatus
	PreviewObjectID        string
	PreviewSize            int64
	PreviewWrappedDEK      []byte
	PreviewKEKKeyID        string
	PreviewEnvelopeVersion int
	PreviewKeyWrapVersion  int
	// PreviewPageCount is the total number of preview pages, page one
	// included. It is meaningless unless PreviewStatus is ready, exactly like
	// the fields above it, and is 1 for every preview this service produced
	// before multi-page rendering existed.
	PreviewPageCount int
	// PreviewContentType is what preview_object_id decodes to —
	// domain.PreviewContentTypeJPEG for every preview produced before sheet
	// previews existed, domain.PreviewContentTypeSheet for a spreadsheet/CSV
	// preview. Meaningless unless PreviewStatus is ready, exactly like the
	// fields above it.
	PreviewContentType string
}

// ListDestinationAttachmentsQuery is a resolved, already-authorised listing
// request. Every identifier is server-derived: the workspace and the
// destination both come from the destination row, never from the caller.
//
// Kind decides which destination column the query filters on, so a channel UUID
// can never select a conversation's attachments or the other way round.
type ListDestinationAttachmentsQuery struct {
	WorkspaceID   string
	Kind          domain.DestinationKind
	DestinationID string
	Limit         int
}

// ListedAttachment is the row a listing loads. It deliberately omits the
// storage object key, the envelope version and the wrapped data key: a list
// never needs them, and not selecting them means they cannot leak through a
// listing bug.
type ListedAttachment struct {
	ID            string
	Status        domain.Status
	PreviewStatus domain.PreviewStatus
	Filename      string
	DetectedMIME  string
	Size          int64
	CreatedAt     time.Time
}

// ScanRejection names the attachment a malware scan has condemned.
//
// It carries identifiers and nothing else on purpose. The caller does not get
// to say what the row should look like afterwards — not the preview state, not
// the schedule, not the attempt count — because the whole point of the
// operation is that the transition is decided in one place and applied in one
// statement. A verdict is an input; the resulting state is not.
type ScanRejection struct {
	AttachmentID string
	// WorkspaceID scopes the write to one tenant. The scanner reads the row it
	// is ruling on, so it always has this, and requiring it means a verdict can
	// never be applied across workspaces even if an id were confused.
	WorkspaceID string
}

// ScanApproval names the attachment a malware scan has cleared.
//
// It is a distinct type from ScanRejection even though the fields match: these
// are the two verdicts, they move the row in opposite directions, and a
// compiler that cannot tell them apart is one refactor away from applying the
// wrong one. Like a rejection, it carries identifiers only — the caller states
// what the scanner found, never what the row should become.
type ScanApproval struct {
	AttachmentID string
	WorkspaceID  string
}

// AttachmentRemoval names the attachment being taken out of its destination.
type AttachmentRemoval struct {
	AttachmentID string
	WorkspaceID  string
}

// AttachmentLifecycleState is the pair of states a lifecycle operation leaves
// behind, read back from the statement that wrote it so it describes the row as
// it actually stands rather than as the caller assumed.
type AttachmentLifecycleState struct {
	Status        domain.Status
	PreviewStatus domain.PreviewStatus
}

// ScanRejectionOutcome is the rejection's result. It is an alias rather than a
// second struct: the three lifecycle operations report the same two facts, and
// duplicating the type would only invite them to drift.
type ScanRejectionOutcome = AttachmentLifecycleState

// ScanResultStore persists an antimalware verdict.
//
// It is separate from AttachmentStore because it answers to a different caller:
// the antimalware worker (RF-33), which is **not implemented in this repository
// yet** — there is no ClamAV client, container or configuration anywhere in the
// tree, and the ADR lists it as its own planned task. When it exists, it is the
// one component that may call either method here, and it must record a verdict
// *only* through them, never with an UPDATE of its own.
//
// That rule is what these two operations exist to enforce. A rejection that set
// the status without finishing the preview leaves a row nothing can ever
// complete: not clean, so the preview worker skips it forever, and still
// pending, so no state machine concludes it. An approval written loosely is
// worse — it is the one transition that makes content renderable, so it must be
// impossible to reach from a state that was never scanned, from a rejection, or
// from a deleted row.
//
// Neither method is reachable from any HTTP route. There is deliberately no
// endpoint, no request field and no client-supplied value anywhere in this
// service that can produce a clean verdict: a user who could would be a user
// who can put an unscanned file in front of the renderer.
type ScanResultStore interface {
	// MarkScanClean records that the scanner found nothing, in one atomic
	// statement. It is the only way an attachment becomes downloadable and the
	// only way its preview becomes claimable.
	MarkScanClean(ctx context.Context, approval ScanApproval) (AttachmentLifecycleState, error)
	// MarkScanRejected condemns an attachment and finalises its preview in one
	// atomic statement. It is idempotent: applying the same verdict twice
	// leaves the same state and reports it, rather than failing.
	MarkScanRejected(ctx context.Context, rejection ScanRejection) (ScanRejectionOutcome, error)
}

// AttachmentRemovalStore takes an attachment out of its destination.
//
// Removal is not implemented as a use case in this service yet — there is no
// route and no retention job (RF-34) — but the operation exists here because
// the *lifecycle* is this package's responsibility and getting it wrong is what
// the preview state machine cannot survive: a row removed while its preview was
// still pending would keep a queued job that no claim can select, exactly like
// a rejection that only wrote the status.
type AttachmentRemovalStore interface {
	// MarkAttachmentDeleted soft-deletes an attachment and finalises a preview
	// that was still pending, in one atomic statement.
	MarkAttachmentDeleted(ctx context.Context, removal AttachmentRemoval) (AttachmentLifecycleState, error)
}

// AttachmentStore is the metadata half of attachment persistence.
type AttachmentStore interface {
	CreatePending(ctx context.Context, attachment NewAttachment) error
	MarkUploaded(ctx context.Context, update UploadedAttachment) error
	MarkFailed(ctx context.Context, attachmentID, failureCode string) error
	GetAuthorized(ctx context.Context, input AttachmentAuthInput) (StoredAttachment, error)
	ListDestinationAttachments(ctx context.Context, query ListDestinationAttachmentsQuery) ([]ListedAttachment, error)
	// GetPreviewPage reads one preview page beyond the first (page >= 2).
	// Page one is never read through this — it lives on the attachment row
	// and is served by the existing Preview path. Implementations answer
	// domain.ErrNotFound for a page that does not exist, non-enumerating like
	// every other absence on this route.
	GetPreviewPage(ctx context.Context, attachmentID string, page int) (PreviewPage, error)
}

type documentPreviewRegenerationStore interface {
	RegenerateDocumentPreview(ctx context.Context, attachmentID string) error
}

// DraftAttachmentStore owns the short-lived lifecycle used only by message
// composer uploads. It is kept separate so legacy upload-store fakes remain
// valid and a partially deployed database fails closed on draft operations.
type DraftAttachmentStore interface {
	CancelDraft(ctx context.Context, attachmentID, uploaderID string) error
	ExpireDrafts(ctx context.Context, limit int) (int, error)
}

type CancelDraftInput struct {
	AttachmentID string
	UploaderID   string
}

// ListDestinationAttachmentsInput asks for one destination's most recent
// attachments. The workspace is absent by design — it is derived from the
// destination row during authorization, exactly like an upload's.
type ListDestinationAttachmentsInput struct {
	Destination domain.Destination
	UserID      string
	SessionID   string
	Limit       int
}

// ObjectStore is the blob half. It is intentionally narrow so the service and
// the domain never depend on SeaweedFS or on HTTP.
type ObjectStore interface {
	Put(ctx context.Context, key string, body io.Reader) (int64, error)
	Open(ctx context.Context, key string) (io.ReadCloser, error)
	// OpenRange returns the object from a byte offset to its end. It is what
	// makes serving an HTTP byte range possible without reading — or
	// decrypting — everything before it.
	OpenRange(ctx context.Context, key string, offset int64) (io.ReadCloser, error)
	Delete(ctx context.Context, key string) error
}

// OrphanObserver counts the one failure the caller cannot see: an upload that
// failed and whose cleanup also failed, leaving an object in storage with no
// usable row. It carries no labels, so it can never leak an identifier.
type OrphanObserver interface {
	ObserveOrphanedObject()
}

// AuthorizeUploadInput asks whether a session may upload to a destination. It
// carries nothing that comes from the request body.
type AuthorizeUploadInput struct {
	Destination domain.Destination
	UserID      string
	SessionID   string
}

// UploadTarget is a destination the caller is allowed to write to, with the
// size policy that governs it.
//
// It is resolved before the body is touched, which is what lets the handler
// authorise the caller and reserve cluster capacity while the request is still
// nothing but headers. Every field is server-derived: the workspace and the
// limit come from the destination row, the uploader from the validated session.
type UploadTarget struct {
	Destination    domain.Destination
	WorkspaceID    string
	UploaderID     string
	MaxUploadBytes int64
}

// UploadInput is one attachment upload against an already authorised target.
// Content is streamed, never buffered.
type UploadInput struct {
	Target       UploadTarget
	Filename     string
	DeclaredMIME string
	Purpose      string
	Content      io.Reader
}

// AttachmentView is the client-facing projection. Storage keys, object
// identifiers, workspace-internal wiring and every byte of key material are
// absent by construction: this struct is the only thing handlers serialise.
type AttachmentView struct {
	ID          string `json:"id"`
	Filename    string `json:"filename"`
	ContentType string `json:"contentType"`
	Size        int64  `json:"size"`
	Status      string `json:"status"`
	// PreviewStatus tells the client which of three things to draw: a preview
	// it may request, a spinner while one is being produced, or the icon and
	// download button that stand in for both kinds of absence. It is the whole
	// fallback contract, which is why it is on every projection rather than
	// only on the preview route: a client never has to probe for a preview to
	// discover there is none.
	PreviewStatus string `json:"previewStatus"`
	// PreviewPageCount is only meaningful once PreviewStatus is "ready"; it is
	// how the document preview viewer learns how many pages to offer without
	// a second request. 1 for every attachment with a single-page preview,
	// which is every image and every preview produced before multi-page
	// rendering existed.
	PreviewPageCount int `json:"previewPageCount"`
	// PreviewContentType is not serialised to the client — it is what
	// GetDocumentPreviewManifest reads to compute the manifest's own "kind"
	// field ("pages" vs. "sheets"). The client learns the *kind*, never the
	// raw content type: exposing this here would be metadata this
	// projection's own contract deliberately keeps out of the general
	// listing (see the struct's own doc comment).
	PreviewContentType string    `json:"-"`
	DestinationKind    string    `json:"destinationKind"`
	CreatedAt          time.Time `json:"createdAt"`
}

// Download is an authorised, decrypted content stream. Closing Content closes
// the underlying storage response.
//
// Content seeks as well as reads, which is what lets a handler answer an HTTP
// byte range without a second code path: a seek moves a plaintext cursor and
// costs nothing until the next read, which then opens storage at the one chunk
// the offset lands in. Seeking is a capability of the stream, never a relaxation
// of anything above it — the authorization, the scan gate and the key unwrap
// have all already happened by the time this struct exists.
type Download struct {
	Filename    string
	ContentType string
	Size        int64
	Content     io.ReadSeekCloser
}

// AttachmentService owns the upload and download use cases, including the
// ordering that keeps PostgreSQL and SeaweedFS consistent without a shared
// transaction.
type AttachmentService struct {
	authorizer     DestinationAuthorizer
	store          AttachmentStore
	objects        ObjectStore
	keys           *crypto.Keyring
	maxUploadBytes int64
	scanRequired   bool
	orphans        OrphanObserver
	logger         *slog.Logger
}

// NewAttachmentService wires the use cases. maxUploadBytes is the validated
// deployment ceiling — the RF-32 limit itself is administrative and arrives per
// request with the authorised destination; scanRequired decides the state a
// finished upload lands in.
func NewAttachmentService(
	authorizer DestinationAuthorizer,
	store AttachmentStore,
	objects ObjectStore,
	keys *crypto.Keyring,
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
		keys:           keys,
		maxUploadBytes: maxUploadBytes,
		scanRequired:   scanRequired,
		orphans:        orphans,
		logger:         logger,
	}
}

// Ready reports whether every dependency the use cases need is wired.
func (s *AttachmentService) Ready() bool {
	return s != nil && s.authorizer != nil && s.store != nil &&
		s.objects != nil && s.keys != nil && s.maxUploadBytes > 0
}

// CancelDraft removes an unassociated message draft. The store deliberately
// returns the same not-found result for absence, another uploader and a draft
// that has already been published.
func (s *AttachmentService) CancelDraft(ctx context.Context, input CancelDraftInput) error {
	if input.AttachmentID == "" || input.UploaderID == "" {
		return fmt.Errorf("%w: attachment and uploader are required", domain.ErrInvalidInput)
	}
	if _, err := uuid.Parse(input.AttachmentID); err != nil {
		return fmt.Errorf("%w: invalid attachment id", domain.ErrInvalidInput)
	}
	if _, err := uuid.Parse(input.UploaderID); err != nil {
		return fmt.Errorf("%w: invalid uploader id", domain.ErrInvalidInput)
	}
	drafts, ok := s.store.(DraftAttachmentStore)
	if !ok {
		return domain.ErrDependenciesUnavailable
	}
	return drafts.CancelDraft(ctx, input.AttachmentID, input.UploaderID)
}

type DraftExpiryService struct {
	store DraftAttachmentStore
	limit int
}

func NewDraftExpiryService(store DraftAttachmentStore, limit int) *DraftExpiryService {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	return &DraftExpiryService{store: store, limit: limit}
}

func (s *DraftExpiryService) ProcessDue(ctx context.Context) (int, error) {
	if s == nil || s.store == nil {
		return 0, domain.ErrDependenciesUnavailable
	}
	return s.store.ExpireDrafts(ctx, s.limit)
}

// uploadTarget is the authorised destination of an upload, with every value
// the server decided itself: the caller supplies none of them directly.
type uploadTarget struct {
	destination    domain.Destination
	workspaceID    string
	uploaderID     string
	filename       string
	declaredMIME   string
	draftExpiresAt *time.Time
	// maxUploadBytes is the limit this one upload is judged against: the
	// destination workspace's administrative policy, narrowed by the deployment
	// ceiling. Resolved once, here, so the policy cannot change underneath a
	// transfer already in progress.
	maxUploadBytes int64
}

// sniffedContent is the head of the stream plus the type detected from it.
type sniffedContent struct {
	head         []byte
	detectedMIME string
	scanMarkup   bool
}

// pendingAttachment is a row in pending_upload whose object write has been
// started.
//
// It carries the data key, in process memory only, for the length of one
// request. That is unavoidable now that the key is wrapped at the end of the
// upload rather than the beginning: the wrapped form authenticates the plaintext
// length, which is not known until the last byte has been read. The key is never
// written anywhere in this state — the row's wrapped_dek stays NULL until
// finalisation, so a pending row can never yield a downloadable object.
type pendingAttachment struct {
	id          uuid.UUID
	workspaceID uuid.UUID
	objectKey   string
	dataKey     []byte
	markupScan  *activeMarkupReader
}

// AuthorizeUpload resolves and authorises an upload destination without
// touching the request body.
//
// It is split out of Upload deliberately. The handler must know the caller may
// write here, and must know the policy that governs the destination, *before*
// it reads a single byte — otherwise cluster capacity would be reserved for
// callers who turn out to have no access, and an unauthorised client could make
// the service read a body it was never going to accept. Everything this needs
// is in the path and the validated session; nothing comes from the body.
func (s *AttachmentService) AuthorizeUpload(
	ctx context.Context, input AuthorizeUploadInput,
) (UploadTarget, error) {
	if !s.Ready() {
		return UploadTarget{}, domain.ErrDependenciesUnavailable
	}
	uploaderID, sessionID, err := parsePrincipal(input.UserID, input.SessionID)
	if err != nil {
		return UploadTarget{}, err
	}
	authorized, err := s.authorizer.AuthorizeDestination(ctx, DestinationAuthInput{
		Destination: input.Destination,
		UserID:      uploaderID,
		SessionID:   sessionID,
	})
	if err != nil {
		return UploadTarget{}, err
	}
	return UploadTarget{
		Destination: domain.Destination{Kind: input.Destination.Kind, ID: authorized.ID},
		WorkspaceID: authorized.WorkspaceID,
		UploaderID:  uploaderID,
		// The workspace policy decides, the deployment ceiling can only narrow
		// it, and a missing or out-of-range policy resolves to the default —
		// never to "unlimited". Resolved once, here, so the policy cannot
		// change underneath a transfer already in progress.
		MaxUploadBytes: uploadpolicy.EffectiveUnder(authorized.MaxUploadBytes, s.maxUploadBytes),
	}, nil
}

// Upload encrypts and persists one attachment into an already authorised
// target.
//
// PostgreSQL and SeaweedFS cannot share a transaction, so the order is chosen
// so that no partial failure can ever produce a downloadable attachment:
//
//  1. reject an empty upload before anything is written anywhere;
//  2. insert the row as pending_upload — not downloadable in any state;
//  3. stream the encrypted object to storage;
//  4. only then move the row to its post-upload state.
//
// The row is created only once the write is about to start, so a failure is
// either before the row exists — nothing to undo — or after a write attempt,
// which goes through compensatePersistedObject. No path returns success for an
// upload that did not complete.
func (s *AttachmentService) Upload(ctx context.Context, input UploadInput) (AttachmentView, error) {
	if !s.Ready() {
		return AttachmentView{}, domain.ErrDependenciesUnavailable
	}
	target, err := s.resolveUploadTarget(input)
	if err != nil {
		return AttachmentView{}, err
	}

	// The cap belongs to the destination that was authorised before the body
	// was touched, so it is already fixed when the first byte arrives.
	source := &boundedSource{src: input.Content, remaining: target.maxUploadBytes}
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

// resolveUploadTarget turns the already-authorised target plus the body-derived
// display fields into the internal shape the rest of the pipeline uses.
//
// The filename is the only value here that comes from the request body, which
// is why it is normalised at this point and not during authorization: it is not
// known until the multipart part header has been read.
func (s *AttachmentService) resolveUploadTarget(input UploadInput) (uploadTarget, error) {
	filename, err := domain.NormalizeFilename(input.Filename)
	if err != nil {
		return uploadTarget{}, err
	}
	// A zero-valued target can only come from a caller that skipped
	// AuthorizeUpload. It is refused rather than defaulted: a permissive
	// fallback here would be an unauthenticated write with no size budget.
	if input.Target.UploaderID == "" || input.Target.WorkspaceID == "" ||
		input.Target.MaxUploadBytes <= 0 {
		return uploadTarget{}, domain.ErrUnauthorized
	}
	var draftExpiresAt *time.Time
	switch input.Purpose {
	case "":
	case UploadPurposeMessageDraft:
		expiresAt := time.Now().UTC().Add(messageDraftTTL)
		draftExpiresAt = &expiresAt
	default:
		return uploadTarget{}, fmt.Errorf("%w: unsupported upload purpose", domain.ErrInvalidInput)
	}
	return uploadTarget{
		destination:    input.Target.Destination,
		workspaceID:    input.Target.WorkspaceID,
		uploaderID:     input.Target.UploaderID,
		filename:       filename,
		declaredMIME:   domain.NormalizeDeclaredMIME(input.DeclaredMIME),
		draftExpiresAt: draftExpiresAt,
		maxUploadBytes: input.Target.MaxUploadBytes,
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
	detected := http.DetectContentType(head)
	if !allowedUploadContent(detected, head) {
		return sniffedContent{}, domain.ErrUnsupportedMedia
	}
	return sniffedContent{
		head: head, detectedMIME: detected,
		scanMarkup: domain.NormalizeDetectedMIME(detected) == "text/plain",
	}, nil
}

func allowedUploadContent(detected string, head []byte) bool {
	mediaType := domain.NormalizeDetectedMIME(detected)
	if mediaType == "text/plain" {
		prefix := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(string(head), "\ufeff")))
		for _, active := range []string{"<!doctype html", "<html", "<script", "<svg", "<?xml"} {
			if strings.Contains(prefix, active) {
				return false
			}
		}
	}
	// Legacy Office documents use the Compound File Binary signature. It is a
	// recognized container, not an arbitrary octet stream; PE/executable magic
	// and every other unknown binary remain rejected.
	if mediaType == "application/octet-stream" && len(head) >= 8 &&
		bytes.Equal(head[:8], []byte{0xd0, 0xcf, 0x11, 0xe0, 0xa1, 0xb1, 0x1a, 0xe1}) {
		return true
	}
	switch mediaType {
	case "image/jpeg", "image/png", "image/gif", "image/webp",
		"application/pdf", "application/zip", "application/gzip",
		"text/plain", "text/csv", "application/json",
		"audio/mpeg", "audio/ogg", "audio/wav", "audio/wave", "audio/x-wav", "application/ogg",
		"video/mp4", "video/webm", "video/quicktime", "video/x-msvideo":
		return true
	default:
		return false
	}
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
	workspaceID, err := uuid.Parse(target.workspaceID)
	if err != nil {
		// The workspace comes from the destination row, so an unparseable one is
		// a data-integrity problem. It is refused rather than encrypted under a
		// binding that could not be reproduced at download.
		return pendingAttachment{}, nil, fmt.Errorf("invalid authorized workspace id")
	}
	dataKey, err := crypto.NewDataKey()
	if err != nil {
		return pendingAttachment{}, nil, fmt.Errorf("prepare attachment key: %w", err)
	}
	plaintext := io.MultiReader(bytes.NewReader(content.head), source)
	var markupScan *activeMarkupReader
	if content.scanMarkup {
		markupScan = &activeMarkupReader{source: plaintext}
		plaintext = markupScan
	}
	encrypted, err := crypto.NewEncryptingReader(plaintext, dataKey, attachmentID)
	if err != nil {
		return pendingAttachment{}, nil, fmt.Errorf("prepare attachment envelope: %w", err)
	}

	pending := pendingAttachment{
		id:          attachmentID,
		workspaceID: workspaceID,
		objectKey:   domain.StorageObjectKey(attachmentID),
		dataKey:     dataKey,
		markupScan:  markupScan,
	}
	// The row is created without any key material. It cannot be: the wrapped
	// form authenticates the plaintext length, which nothing knows yet. A
	// pending row therefore has wrapped_dek NULL and is not openable by
	// construction, rather than carrying a placeholder that would be.
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
		KeyWrapVersion:   crypto.KeyWrapVersion,
		DraftExpiresAt:   target.draftExpiresAt,
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
		uploadFailureCause(err, source, pending.markupScan))
}

// finalizeUpload advances the row once the object is durable. Only here can an
// attachment leave pending_upload for a post-upload state.
//
// This is also where the data key is wrapped, and it cannot happen earlier. The
// wrapped form authenticates the plaintext length, so it can only be produced
// once the stream has been read to its end and that length is a fact rather than
// a claim. The whole result — size, wrapped key, key id, wrap version, state —
// lands in one UPDATE, so there is no window in which a row is downloadable
// while any part of its binding is missing or stale.
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
	// The budget the source started with is the target's, not the service's, so
	// the recorded plaintext size stays correct whatever the workspace policy is.
	// It counts the bytes actually read from the body; Content-Length never
	// reaches this number, and neither does anything else the client sent.
	size := target.maxUploadBytes - source.remaining

	// What storage accepted must be exactly the envelope this plaintext produces.
	// A mismatch means the object is not the complete, closed NCF1 stream — a
	// short write that still reported success, or a store that transformed the
	// body — so the upload is failed and compensated instead of being sealed
	// against a length the stored bytes do not have.
	if want := crypto.CiphertextSize(size); ciphertextSize != want {
		return AttachmentView{}, s.compensatePersistedObject(ctx, pending, failureEnvelopeIncomplete,
			fmt.Errorf("stored envelope is %d bytes, expected %d", ciphertextSize, want))
	}

	wrappedDEK, keyID, err := s.keys.Wrap(pending.dataKey, crypto.Binding{
		AttachmentID:           pending.id,
		WorkspaceID:            pending.workspaceID,
		PlaintextSize:          size,
		KeyWrapVersion:         crypto.KeyWrapVersion,
		ContentEnvelopeVersion: crypto.EnvelopeVersion,
	})
	if err != nil {
		return AttachmentView{}, s.compensatePersistedObject(ctx, pending, failureKeyWrap,
			fmt.Errorf("wrap attachment key: %w", err))
	}

	// Decided from the detected type and the counted size, both facts by now.
	// An attachment nothing can render never enters the worker's queue.
	previewStatus := domain.InitialDocumentPreviewStatus(content.detectedMIME, target.filename, size)

	if err := s.store.MarkUploaded(ctx, UploadedAttachment{
		ID:             pending.id.String(),
		Status:         status,
		PreviewStatus:  previewStatus,
		DetectedMIME:   content.detectedMIME,
		Size:           size,
		CiphertextSize: ciphertextSize,
		WrappedDEK:     wrappedDEK,
		KEKKeyID:       keyID,
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
		PreviewStatus:   string(previewStatus),
		DestinationKind: string(target.destination.Kind),
		CreatedAt:       time.Now().UTC(),
	}, nil
}

// uploadFailureCause attributes a storage write failure to whichever side
// actually broke: a client stream that stopped, or storage itself.
func uploadFailureCause(storageErr error, source *boundedSource, markup *activeMarkupReader) error {
	if markup != nil && markup.err != nil {
		return markup.err
	}
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
		ID:                 record.ID,
		Filename:           record.Filename,
		ContentType:        contentType(record),
		Size:               record.Size,
		Status:             string(record.Status),
		PreviewStatus:      string(record.PreviewStatus),
		PreviewPageCount:   record.PreviewPageCount,
		PreviewContentType: record.PreviewContentType,
		DestinationKind:    string(record.Kind),
		CreatedAt:          record.CreatedAt,
	}, nil
}

// RegenerateDocumentPreview reuses authorizedAttachment so DM, group and
// channel visibility — including active-session checks and hidden 404s — are
// identical to metadata, content and preview reads.
func (s *AttachmentService) RegenerateDocumentPreview(ctx context.Context, input AttachmentAuthInput) error {
	if !s.Ready() {
		return domain.ErrDependenciesUnavailable
	}
	record, err := s.authorizedAttachment(ctx, input)
	if err != nil {
		return err
	}
	store, ok := s.store.(documentPreviewRegenerationStore)
	if !ok {
		return domain.ErrDependenciesUnavailable
	}
	return store.RegenerateDocumentPreview(ctx, record.ID)
}

// ListDestinationAttachments returns a destination's most recent attachments
// for a caller who can currently reach it (issues #435 and #441).
//
// Authorization is the upload path's, unchanged: AuthorizeDestination resolves
// the destination and its canonical workspace in one query and answers
// ErrNotFound for anything that does not exist, is archived, is private and not
// the caller's, or belongs to another workspace. Channels and conversations
// each go through their own policy inside that call — there is no second,
// divergent rule here, and a group's attachments are gated by active
// participation in the conversation exactly as its messages are.
//
// The listing query is then bound to the workspace, the kind and the ID that
// query returned, so a channel UUID can never select a conversation's rows and
// a UUID from another tenant can never select that tenant's.
//
// The result is a preview, not an archive: the limit is clamped in the domain
// and the order is fixed server-side, so a client cannot ask for an unbounded
// scan or a different one.
func (s *AttachmentService) ListDestinationAttachments(
	ctx context.Context, input ListDestinationAttachmentsInput,
) ([]AttachmentView, error) {
	if !s.Ready() {
		return nil, domain.ErrDependenciesUnavailable
	}
	destination, err := domain.NewDestination(input.Destination.Kind, input.Destination.ID)
	if err != nil {
		// An unparseable or unknown destination is answered like an invisible
		// one, so the route cannot be used to tell "malformed" from "not yours".
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
	records, err := s.store.ListDestinationAttachments(ctx, ListDestinationAttachmentsQuery{
		WorkspaceID:   authorized.WorkspaceID,
		Kind:          destination.Kind,
		DestinationID: authorized.ID,
		Limit:         domain.NormalizeAttachmentListLimit(input.Limit),
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
			PreviewStatus:   string(record.PreviewStatus),
			DestinationKind: string(destination.Kind),
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
	return record, nil
}

// Preview returns the inline preview of an attachment the caller may read
// (RF-31).
//
// It is the download path's authorization, unchanged and re-evaluated here:
// the same query decides visibility, and Status.Downloadable decides delivery,
// so a preview can never be served for an attachment whose content could not
// be. That ordering is what keeps the malware-scan gate closed — a rendering of
// a file is still that file's content, and this route must not become the way
// around a scan that has not approved it.
//
// A caller that may read the attachment but has no preview to read gets
// ErrPreviewUnavailable, whatever the reason. Which reason it is comes from the
// attachment's own metadata, so this route never has to explain itself and
// never becomes a way to probe internal state.
func (s *AttachmentService) Preview(ctx context.Context, input AttachmentAuthInput) (Download, error) {
	if !s.Ready() {
		return Download{}, domain.ErrDependenciesUnavailable
	}
	record, err := s.downloadableAttachment(ctx, input)
	if err != nil {
		return Download{}, err
	}
	if !record.PreviewStatus.Servable() {
		return Download{}, domain.ErrPreviewUnavailable
	}
	object, err := previewObject(record)
	if err != nil {
		return Download{}, err
	}
	content, err := s.openEncryptedObject(ctx, object)
	if err != nil {
		return Download{}, err
	}
	return Download{
		Filename: record.Filename,
		// Never the attachment's own type: the bytes are this service's own
		// produced shape (a raster, or a bounded table document), and saying
		// so is what makes them safe to render inline.
		ContentType: record.PreviewContentType,
		Size:        record.PreviewSize,
		Content:     content,
	}, nil
}

// DocumentPreviewPage returns page N (N >= 2) of a multi-page preview
// (task #494, Fase 1). Page 1 is deliberately not handled here — it is
// domain.PreviewObjectKey's existing cover object, served by Preview()/
// GetPreview unchanged.
//
// Authorization and delivery follow Preview() exactly: the same visibility
// query, the same Status.Downloadable gate, the same requirement that the
// preview itself be ready. A page number outside [2, PreviewPageCount] is
// domain.ErrNotFound, non-enumerating like every other bound on this route —
// it never distinguishes "this attachment has no such page" from "this
// attachment has no multi-page preview at all".
func (s *AttachmentService) DocumentPreviewPage(
	ctx context.Context, input AttachmentAuthInput, page int,
) (Download, error) {
	if !s.Ready() {
		return Download{}, domain.ErrDependenciesUnavailable
	}
	record, err := s.downloadableAttachment(ctx, input)
	if err != nil {
		return Download{}, err
	}
	if !record.PreviewStatus.Servable() {
		return Download{}, domain.ErrPreviewUnavailable
	}
	if page < 2 || page > record.PreviewPageCount {
		return Download{}, domain.ErrNotFound
	}
	pageRecord, err := s.store.GetPreviewPage(ctx, record.ID, page)
	if err != nil {
		return Download{}, err
	}
	object, err := documentPreviewPageObject(record, pageRecord)
	if err != nil {
		return Download{}, err
	}
	content, err := s.openEncryptedObject(ctx, object)
	if err != nil {
		return Download{}, err
	}
	return Download{
		Filename:    record.Filename,
		ContentType: pageRecord.ContentType,
		Size:        pageRecord.Size,
		Content:     content,
	}, nil
}

// documentPreviewPageObject builds an extra page's descriptor exactly the way
// previewObject builds page one's: the storage key is derived from the page's
// own object id, never read from a column holding a path, so no stored value
// can redirect a read.
func documentPreviewPageObject(record StoredAttachment, page PreviewPage) (encryptedObject, error) {
	pageID, err := uuid.Parse(page.ObjectID)
	if err != nil {
		return encryptedObject{}, domain.ErrPreviewUnavailable
	}
	return encryptedObject{
		objectID:        page.ObjectID,
		workspaceID:     record.WorkspaceID,
		objectKey:       domain.PreviewObjectKey(pageID),
		size:            page.Size,
		wrappedDEK:      page.WrappedDEK,
		kekKeyID:        page.KEKKeyID,
		envelopeVersion: page.EnvelopeVersion,
		keyWrapVersion:  page.KeyWrapVersion,
	}, nil
}

// encryptedObject is one stored NCF1 object together with everything needed to
// open it. An attachment's content and its preview are two of these: separate
// identities, separate data keys, separate lengths, one code path.
type encryptedObject struct {
	// objectID is the identity the envelope's chunks and the wrapped key's
	// binding are both tied to. For content it is the attachment id; for a
	// preview it is that preview's own id, which is what makes the two objects
	// impossible to substitute for one another.
	objectID        string
	workspaceID     string
	objectKey       string
	size            int64
	wrappedDEK      []byte
	kekKeyID        string
	envelopeVersion int
	keyWrapVersion  int
}

func contentObject(record StoredAttachment) encryptedObject {
	return encryptedObject{
		objectID:        record.ID,
		workspaceID:     record.WorkspaceID,
		objectKey:       record.StorageObjectKey,
		size:            record.Size,
		wrappedDEK:      record.WrappedDEK,
		kekKeyID:        record.KEKKeyID,
		envelopeVersion: record.EnvelopeVersion,
		keyWrapVersion:  record.KeyWrapVersion,
	}
}

// previewObject builds the preview's descriptor. The storage key is derived
// from the preview id rather than read from the database: there is no column
// holding a path, so no stored value can redirect a read.
//
// It returns an error rather than an object with an empty key, so a descriptor
// that could not name its object cannot exist. A row that says `ready` without a
// usable preview id is corrupt metadata, and the honest answer for it is the one
// this route already gives for every other absence — the caller asked for a
// preview and there is none to serve. Reporting it as a generic failure instead
// would turn a row-level inconsistency into a 500, which tells the client
// nothing and pages someone for a file that simply has no preview.
//
// Nothing is repaired, guessed at or deleted here: the row is left exactly as it
// is for an operator to look at, and the invalid id never appears in a response.
func previewObject(record StoredAttachment) (encryptedObject, error) {
	previewID, err := uuid.Parse(record.PreviewObjectID)
	if err != nil {
		return encryptedObject{}, domain.ErrPreviewUnavailable
	}
	return encryptedObject{
		objectID:        record.PreviewObjectID,
		workspaceID:     record.WorkspaceID,
		objectKey:       domain.PreviewObjectKey(previewID),
		size:            record.PreviewSize,
		wrappedDEK:      record.PreviewWrappedDEK,
		kekKeyID:        record.PreviewKEKKeyID,
		envelopeVersion: record.PreviewEnvelopeVersion,
		keyWrapVersion:  record.PreviewKeyWrapVersion,
	}, nil
}

// openDecryptedContent turns an authorised row into a verified plaintext
// stream. Integrity is checked chunk by chunk as the caller reads, so a
// modified or truncated object fails mid-stream instead of being served whole.
func (s *AttachmentService) openDecryptedContent(
	ctx context.Context, record StoredAttachment,
) (io.ReadSeekCloser, error) {
	return s.openEncryptedObject(ctx, contentObject(record))
}

// openEncryptedObject unwraps an object's data key and returns a verifying
// plaintext reader.
//
// The version check comes first and there is no fallback: a persisted format
// this build does not implement is refused rather than guessed at. The unwrap
// then runs before storage is touched and long before a handler writes a status
// line, which is what stops an edited length from ever reaching a client as a
// shorter, complete-looking file.
func (s *AttachmentService) openEncryptedObject(
	ctx context.Context, object encryptedObject,
) (io.ReadSeekCloser, error) {
	return openEncryptedObject(ctx, s.keys, s.objects, s.logger, object)
}

// openEncryptedObject is the free function behind the method, so the preview
// worker — which has the same key ring and the same object store but no request
// and no session — opens an attachment exactly the way a download does, rather
// than through a second implementation that could drift from it.
func openEncryptedObject(
	ctx context.Context,
	keys *crypto.Keyring,
	objects ObjectStore,
	logger *slog.Logger,
	object encryptedObject,
) (io.ReadSeekCloser, error) {
	if object.envelopeVersion != crypto.EnvelopeVersion {
		return nil, fmt.Errorf("unsupported envelope version %d", object.envelopeVersion)
	}
	objectID, err := uuid.Parse(object.objectID)
	if err != nil {
		return nil, fmt.Errorf("invalid stored object id")
	}
	workspaceID, err := uuid.Parse(object.workspaceID)
	if err != nil {
		return nil, fmt.Errorf("invalid stored workspace id")
	}
	// The key is selected by the id persisted with the row, never searched for:
	// an id this deployment does not have configured is an operational failure,
	// not something to work around by trying the keys it does have.
	//
	// The recorded size is part of the binding, so this call is also the check
	// on that length. It runs before the object is opened and long before the
	// handler writes a status line or a Content-Length, which is what stops an
	// edited size from ever reaching a client as a shorter, complete looking
	// file.
	dataKey, err := keys.Unwrap(object.wrappedDEK, object.kekKeyID, crypto.Binding{
		AttachmentID:           objectID,
		WorkspaceID:            workspaceID,
		PlaintextSize:          object.size,
		KeyWrapVersion:         object.keyWrapVersion,
		ContentEnvelopeVersion: object.envelopeVersion,
	})
	if err != nil {
		return nil, fmt.Errorf("unwrap attachment key: %w", err)
	}
	// The stream is opened here, eagerly, exactly as it always was: a missing or
	// unreachable object has to fail now — while the caller can still turn it
	// into a status code — and never halfway through a response. Only a *seek*
	// away from the start defers work, and by then this open has already proved
	// the object is there.
	reader := &rangeReader{
		ctx: ctx, objects: objects, logger: logger,
		object: object, dataKey: dataKey, objectID: objectID,
	}
	if err := reader.openAt(0); err != nil {
		return nil, err
	}
	return reader, nil
}

// openStoredObject fetches the ciphertext, translating a missing object into an
// operational failure rather than a client-visible 404: metadata says the object
// exists, so reporting "not found" would describe storage to the caller.
func openStoredObject(
	ctx context.Context, objects ObjectStore, logger *slog.Logger, object encryptedObject,
) (io.ReadCloser, error) {
	body, err := objects.Open(ctx, object.objectKey)
	if err == nil {
		return body, nil
	}
	if !errors.Is(err, domain.ErrNotFound) {
		return nil, err
	}
	logger.LogAttrs(ctx, slog.LevelError, "attachment object missing in storage",
		slog.String("object_id", object.objectID),
		slog.String("workspace_id", object.workspaceID),
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

var activeMarkupTokens = [][]byte{
	[]byte("<!doctype html"), []byte("<html"), []byte("<script"),
	[]byte("<svg"), []byte("<?xml"),
}

// activeMarkupReader scans the complete text stream, including token matches
// split across read boundaries. Detection after object persistence begins is
// still safe: the upload pipeline treats this error like any failed write and
// deletes the partial encrypted object before returning 415.
type activeMarkupReader struct {
	source io.Reader
	tail   []byte
	err    error
}

func (r *activeMarkupReader) Read(p []byte) (int, error) {
	n, err := r.source.Read(p)
	if n == 0 {
		return n, err
	}
	window := make([]byte, 0, len(r.tail)+n)
	window = append(window, r.tail...)
	window = append(window, p[:n]...)
	lower := bytes.ToLower(window)
	for _, token := range activeMarkupTokens {
		if bytes.Contains(lower, token) {
			r.err = fmt.Errorf("%w: active markup in text upload", domain.ErrUnsupportedMedia)
			return n, r.err
		}
	}
	const longestToken = len("<!doctype html")
	keep := min(len(window), longestToken-1)
	r.tail = append(r.tail[:0], window[len(window)-keep:]...)
	return n, err
}

// boundedSource enforces the effective RF-32 cap on the bytes actually read,
// regardless of what Content-Length claimed, and remembers the first failure so
// the service can tell a client-side stream problem from a storage problem.
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
