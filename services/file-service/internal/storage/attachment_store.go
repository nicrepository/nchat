package storage

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/nicrepository/nchat/libs/go/platform/authsession"
	"github.com/nicrepository/nchat/services/file-service/internal/domain"
	"github.com/nicrepository/nchat/services/file-service/internal/service"
)

// PGXAttachmentStore persists attachment metadata in files.attachments.
type PGXAttachmentStore struct {
	pool Pool
	// fence serialises the two invalidations against a render in progress. It
	// is the transaction half of the same lock the preview worker holds.
	// Nil is only ever the case for the read and upload paths, which never
	// invalidate anything; the invalidating operations refuse to run without it
	// rather than proceeding unfenced.
	fence TransactionFence
}

// NewFencedAttachmentStore builds the store used by the lifecycle operations.
//
// The fence is a constructor argument rather than an optional setter because
// the two invalidations must not be reachable without it: a rejection that runs
// unfenced can commit while a renderer is already holding the plaintext, which
// is precisely the containment failure the fence exists to close.
func NewFencedAttachmentStore(pool Pool, fence TransactionFence) *PGXAttachmentStore {
	return &PGXAttachmentStore{pool: pool, fence: fence}
}

// The scan-verdict contract is asserted here rather than left to whichever
// caller happens to wire it: the antimalware worker (RF-33) does not exist yet,
// so nothing else in this build would notice the method drifting away from the
// interface the future worker has to program against.
var (
	_ service.ScanResultStore        = (*PGXAttachmentStore)(nil)
	_ service.AttachmentRemovalStore = (*PGXAttachmentStore)(nil)
)

func NewPGXAttachmentStore(pool Pool) *PGXAttachmentStore {
	return &PGXAttachmentStore{pool: pool}
}

// CreatePending writes the row an upload starts from. The status is fixed here
// and never taken from a request, and channel_id / conversation_id are filled
// from the destination kind so the exclusivity CHECK can never be reached with
// both set.
//
// dek_wrap_version is named in this INSERT even though no key exists yet. The
// column is NOT NULL with no default, so naming it is what distinguishes this
// build from the previous one at the database level: an INSERT emitted by a
// file-service that predates migration 000002 omits the column and is rejected
// by PostgreSQL rather than creating a row in a format nothing can open.
func (s *PGXAttachmentStore) CreatePending(ctx context.Context, attachment service.NewAttachment) error {
	if s == nil || s.pool == nil {
		return domain.ErrDependenciesUnavailable
	}
	// Refused before the statement so a mis-wired caller sees a domain error
	// instead of a not-null violation surfacing as driver text.
	if attachment.KeyWrapVersion <= 0 {
		return fmt.Errorf("%w: attachment requires a key wrap version", domain.ErrInvalidInput)
	}
	var channelID, conversationID any
	switch attachment.Destination.Kind {
	case domain.DestinationKindChannel:
		channelID = attachment.Destination.ID
	case domain.DestinationKindDM:
		conversationID = attachment.Destination.ID
	default:
		return fmt.Errorf("%w: invalid destination kind", domain.ErrInvalidInput)
	}

	_, err := s.pool.Exec(ctx, `
		INSERT INTO files.attachments (
			id, workspace_id, uploader_id, destination_kind,
			channel_id, conversation_id,
			original_filename, declared_mime,
			storage_provider, storage_object_key,
			envelope_version, dek_wrap_version, status
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`,
		attachment.ID, attachment.WorkspaceID, attachment.UploaderID,
		string(attachment.Destination.Kind), channelID, conversationID,
		attachment.Filename, attachment.DeclaredMIME,
		attachment.StorageProvider, attachment.StorageObjectKey,
		attachment.EnvelopeVersion, attachment.KeyWrapVersion,
		string(domain.StatusPendingUpload),
	)
	if err != nil {
		return fmt.Errorf("create pending attachment: %w", err)
	}
	return nil
}

// MarkUploaded moves a row out of pending_upload once the object is durable.
// The WHERE clause pins the previous state, so a concurrent failure path that
// already finalised or failed the row cannot be overwritten.
//
// The size and the key material are written by the same statement on purpose.
// The wrapped key authenticates size_bytes, so the two are one fact: splitting
// them across statements would leave a window where a row is downloadable with a
// length its sealed key does not cover, which is precisely the state the binding
// exists to make unrepresentable.
func (s *PGXAttachmentStore) MarkUploaded(ctx context.Context, update service.UploadedAttachment) error {
	if s == nil || s.pool == nil {
		return domain.ErrDependenciesUnavailable
	}
	if !update.Status.Valid() {
		return fmt.Errorf("%w: invalid attachment status", domain.ErrInvalidInput)
	}
	// Refused here as well as in the CHECK: a finalising update without key
	// material would otherwise reach the database only to violate a constraint,
	// and the caller would learn about it as a driver error. The wrap version is
	// not part of this: it was fixed when the row was created.
	if len(update.WrappedDEK) == 0 || update.KEKKeyID == "" {
		return fmt.Errorf("%w: attachment cannot be finalised without its key binding", domain.ErrInvalidInput)
	}
	// The preview job is scheduled by this same statement (RF-31). Only the two
	// states an upload can decide are accepted: 'pending' queues the work,
	// 'unsupported' says up front that there will never be a preview. 'ready'
	// is refused here because no preview exists yet, and 'failed' because
	// nothing has been attempted.
	if update.PreviewStatus != domain.PreviewStatusPending &&
		update.PreviewStatus != domain.PreviewStatusUnsupported {
		return fmt.Errorf("%w: invalid initial preview status", domain.ErrInvalidInput)
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE files.attachments
		   SET status = $2,
		       detected_mime = $3,
		       size_bytes = $4,
		       ciphertext_size_bytes = $5,
		       wrapped_dek = $6,
		       kek_key_id = $7,
		       preview_status = $9,
		       preview_next_attempt_at = CASE WHEN $9 = 'pending' THEN now() ELSE NULL END,
		       uploaded_at = now(),
		       updated_at = now()
		 WHERE id = $1
		   AND status = $8`,
		update.ID, string(update.Status), update.DetectedMIME,
		update.Size, update.CiphertextSize,
		update.WrappedDEK, update.KEKKeyID,
		string(domain.StatusPendingUpload),
		string(update.PreviewStatus),
	)
	if err != nil {
		return fmt.Errorf("mark attachment uploaded: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("%w: attachment not pending upload", domain.ErrNotFound)
	}
	return nil
}

// MarkFailed records a terminal failure with a short sanitised code. It is the
// compensation path, so it tolerates a row that is already terminal rather than
// reporting a second error on top of the first.
func (s *PGXAttachmentStore) MarkFailed(ctx context.Context, attachmentID, failureCode string) error {
	if s == nil || s.pool == nil {
		return domain.ErrDependenciesUnavailable
	}
	_, err := s.pool.Exec(ctx, `
		UPDATE files.attachments
		   SET status = $2,
		       failure_code = $3,
		       updated_at = now()
		 WHERE id = $1
		   AND status = $4`,
		attachmentID, string(domain.StatusFailed), failureCode,
		string(domain.StatusPendingUpload),
	)
	if err != nil {
		return fmt.Errorf("mark attachment failed: %w", err)
	}
	return nil
}

// markScanRejectedQuery applies an antimalware verdict of "rejected" and
// finishes the row's preview lifecycle in the same statement.
//
// # Why one statement and not two
//
// status and the preview columns live on the same row, so this needs no
// transaction and no writable CTE — a single UPDATE is already atomic, and
// choosing anything looser would be choosing a window. That window is not
// theoretical: setting status = 'rejected' on its own leaves the row in a state
// no code can ever leave. ClaimDuePreviews requires status = 'clean', so the
// preview worker will never look at it again; preview_status stays 'pending',
// so nothing ever concludes it. The row would sit there, scheduled, forever.
//
// # What the CASE expressions are for
//
// Only a preview that is still *pending* is finalised. Every SET expression
// reads the row as it was before the update, so all three branches decide on
// the same pre-update preview_status:
//
//   - pending  -> unsupported, and its schedule is cleared. "Unsupported" is
//     the honest terminal state: this content will never have a preview,
//     because the only path to one requires a clean verdict it will not get;
//   - ready, failed, unsupported -> untouched. They are already terminal, and
//     rewriting them would lose the distinction between "we tried and could
//     not" and "we were never allowed to try". A ready preview keeps existing
//     and stops being servable, because delivery reads the attachment's status,
//     not the preview's.
//
// preview_attempts is deliberately not reset: it is what an operator reads to
// see how much work a row consumed, and a verdict does not undo that history.
//
// # Which rows it accepts
//
//   - pending_scan: the ordinary verdict, on a file awaiting one;
//   - clean: a rescan reversing an earlier approval, or a verdict landing while
//     the preview worker holds the row;
//   - rejected: the same verdict arriving twice, and — the reason this is not
//     merely tolerated but required — a row already left in the broken
//     rejected+pending state by an earlier build. Re-applying the verdict
//     repairs it, which is why the repair needs no migration and no sweep.
//
// pending_upload, failed and deleted are excluded: an upload that never
// finished has no stored object to have been scanned, and a deleted row is not
// a live attachment. deleted_at is checked for the same reason.
//
// # Why the cleanup job is in this statement
//
// A rescan can condemn a file whose preview was already published. The preview
// object then belongs to nothing: it can never be served again — delivery reads
// the attachment's status — but it is still sitting in SeaweedFS, and nothing
// would ever remove it. The row still pointed at the key, so the cleanup worker
// used to see it as referenced and drop the job.
//
// So the verdict enqueues it, here, in the same statement. Not the same
// transaction — the same statement, which is stronger and needs no ordering
// argument: the state change and the job that compensates for it become visible
// together or not at all. There is no window in which the object is unservable
// and unqueued, and none in which a job exists for a preview still being served.
//
// The CTE reads the *post*-update preview_status on purpose. A preview that was
// pending has no object to reclaim and becomes unsupported; only one that says
// ready has published bytes, and it stays ready — the audit record that a
// preview once existed — while its object goes. ON CONFLICT DO NOTHING keeps a
// repeated verdict from queueing the key twice.
const markScanRejectedQuery = `
	WITH rejected AS (
		UPDATE files.attachments
		   SET status = 'rejected',
		       preview_status = CASE
		           WHEN preview_status = 'pending' THEN 'unsupported'
		           ELSE preview_status
		       END,
		       preview_next_attempt_at = CASE
		           WHEN preview_status = 'pending' THEN NULL
		           ELSE preview_next_attempt_at
		       END,
		       updated_at = now()
		 WHERE id = $1
		   AND workspace_id = $2
		   AND deleted_at IS NULL
		   AND status IN ('pending_scan', 'clean', 'rejected')
		RETURNING status, preview_status, preview_object_id
	), orphaned AS (
		INSERT INTO files.object_cleanup_jobs (object_key)
		SELECT ` + previewObjectKeyExpr + `
		  FROM rejected
		 WHERE preview_object_id IS NOT NULL
		   AND preview_status = 'ready'
		ON CONFLICT (object_key) DO NOTHING
	)
	SELECT status, preview_status FROM rejected`

// MarkScanRejected records an antimalware rejection and finalises the preview.
//
// The two facts move together or not at all. A row that comes back from this
// call is condemned *and* concluded: it cannot be claimed (the claim requires
// clean), cannot be published (MarkPreviewReady re-checks clean and pending),
// and is no longer scheduled for anything.
//
// It returns what the row now is, read back from the statement that wrote it.
// No row matched means the attachment does not exist, belongs to another
// workspace, is deleted, or was never in a state a scan verdict applies to —
// all reported as ErrNotFound, the same way GetAuthorized refuses to
// distinguish "absent" from "not yours".
func (s *PGXAttachmentStore) MarkScanRejected(
	ctx context.Context, rejection service.ScanRejection,
) (service.ScanRejectionOutcome, error) {
	// Fenced: a verdict must not commit while a renderer is already holding
	// this attachment's plaintext. Acquiring waits for a render in progress, so
	// the two possible orderings are the only two outcomes — reject then no
	// render, or render then reject — and never both at once.
	return s.applyFencedTransition(ctx, rejection.AttachmentID, markScanRejectedQuery,
		rejection.WorkspaceID,
		"a scan verdict needs an attachment and its workspace",
		"attachment cannot take a scan verdict")
}

// markScanCleanQuery records an antimalware verdict of "clean".
//
// This is the one transition that makes content reachable: it is what lets a
// download be served and what makes a preview claimable, so it is written as
// narrowly as the rejection is written thoroughly.
//
// # What it does not touch
//
// Nothing about the preview. That is not an omission — it is the design. An
// upload that finished with a previewable type left preview_status = 'pending'
// and preview_next_attempt_at = now(), so the row becomes claimable the instant
// its status turns clean, with no second write and therefore no window in which
// the status says clean and the schedule says otherwise. A preview that had
// already reached a terminal state stays there: an approval is news about the
// file, not about a render that already happened.
//
// # Which rows it accepts
//
//   - pending_scan: the real transition, the only way an attachment becomes
//     downloadable;
//   - clean: the same verdict arriving twice, which must converge rather than
//     fail — a scanner retrying after a lost acknowledgement is normal.
//
// Every other state is excluded, and each exclusion is a security property:
//
//   - rejected is absent, so a stale or replayed approval can never reopen a
//     file the scanner condemned. Rejection is final in this direction;
//   - pending_upload and failed are absent, so an upload that never completed
//     cannot be approved — there is no stored object that was scanned;
//   - deleted_at IS NULL, so a removed attachment cannot be brought back into
//     circulation by a verdict that arrived late.
const markScanCleanQuery = `
	UPDATE files.attachments
	   SET status = 'clean',
	       updated_at = now()
	 WHERE id = $1
	   AND workspace_id = $2
	   AND deleted_at IS NULL
	   AND status IN ('pending_scan', 'clean')
	RETURNING status, preview_status`

// MarkScanClean records that the antimalware scan found nothing.
//
// It is the counterpart of MarkScanRejected and the only path to a clean
// status. Nothing in the HTTP surface reaches it: there is no route, no request
// field and no client value anywhere that decides a scan verdict, because a
// caller who could choose "clean" could put an unscanned file in front of the
// PDF parser.
//
// No row matched means the verdict was not recorded — the attachment does not
// exist, is another workspace's, is deleted, or is in a state no approval
// applies to, rejection included. All are reported as ErrNotFound, and never as
// success.
func (s *PGXAttachmentStore) MarkScanClean(
	ctx context.Context, approval service.ScanApproval,
) (service.AttachmentLifecycleState, error) {
	return s.applyLifecycleTransition(ctx, markScanCleanQuery,
		approval.AttachmentID, approval.WorkspaceID,
		"a scan verdict needs an attachment and its workspace",
		"attachment cannot take a clean verdict")
}

// markAttachmentDeletedQuery removes an attachment and finishes a preview that
// was still pending, in the same statement.
//
// The reason it is one statement is the reason the rejection is: status and the
// preview columns live on the same row, and writing only deleted_at leaves a
// queued preview job that no claim can ever select — ClaimDuePreviews requires
// deleted_at IS NULL — and that nothing ever concludes. The row would stay
// pending forever, scheduled, for an attachment that no longer exists.
//
// deleted_at uses COALESCE rather than an unconditional now(): a second removal
// must converge without moving the moment the attachment was removed, which is
// the timestamp a retention policy will one day count from. Because the
// statement therefore matches an already-deleted row, it also repairs one left
// with a pending preview by a caller that only wrote deleted_at.
//
// status is deliberately untouched. Deletion and the scan verdict are different
// facts about the row: a file that was rejected and then removed is still a
// file the scanner condemned, and every read path already gates on deleted_at,
// not on the status. A preview that had reached a terminal state — ready
// included — keeps that state, because it is the record that a preview once
// existed; what does not survive is its *object*, which the CTE below reclaims.
//
// # Why the object is reclaimed and the attachment's is not
//
// They are not the same kind of thing. The attachment's object is the user's
// file, and how long a removed file is retained is a policy decision (RF-34)
// that this statement has no business making. A preview is a derived artefact
// with no independent value: once the attachment it was derived from is gone,
// nothing can ever serve it, nothing can ever regenerate a use for it, and
// keeping it is pure accumulation. Leaving it behind was a permanent leak —
// unreachable to every read path and invisible to the cleanup worker, which saw
// the row still pointing at the key and called it referenced.
//
// Same statement, same reasoning as the rejection: the removal and the job that
// reclaims its object commit together or not at all.
const markAttachmentDeletedQuery = `
	WITH removed AS (
		UPDATE files.attachments
		   SET deleted_at = COALESCE(deleted_at, now()),
		       preview_status = CASE
		           WHEN preview_status = 'pending' THEN 'unsupported'
		           ELSE preview_status
		       END,
		       preview_next_attempt_at = CASE
		           WHEN preview_status = 'pending' THEN NULL
		           ELSE preview_next_attempt_at
		       END,
		       updated_at = now()
		 WHERE id = $1
		   AND workspace_id = $2
		RETURNING status, preview_status, preview_object_id
	), orphaned AS (
		INSERT INTO files.object_cleanup_jobs (object_key)
		SELECT ` + previewObjectKeyExpr + `
		  FROM removed
		 WHERE preview_object_id IS NOT NULL
		   AND preview_status = 'ready'
		ON CONFLICT (object_key) DO NOTHING
	)
	SELECT status, preview_status FROM removed`

// MarkAttachmentDeleted soft-deletes an attachment and finalises a preview that
// was still pending.
//
// Both facts move together or not at all, so there is no state in which an
// attachment is gone and a render is still queued for it. Repeating the removal
// converges: the deletion timestamp is kept, the preview stays terminal, and
// the same state is reported.
func (s *PGXAttachmentStore) MarkAttachmentDeleted(
	ctx context.Context, removal service.AttachmentRemoval,
) (service.AttachmentLifecycleState, error) {
	// Fenced for the same reason a rejection is: a removal that commits while a
	// renderer holds the plaintext would leave the parser working on content
	// the system has already decided nobody may see.
	return s.applyFencedTransition(ctx, removal.AttachmentID, markAttachmentDeletedQuery,
		removal.WorkspaceID,
		"a removal needs an attachment and its workspace",
		"attachment cannot be removed")
}

// applyFencedTransition takes the attachment fence and applies the transition
// under it, so no invalidation can overlap a render.
//
// # Why this side uses a transaction-scoped lock
//
// The preview worker holds a *session* lock, on a connection it keeps for the
// length of a render, because a render is tens of seconds long and no
// transaction should be. An invalidation is the opposite shape: one indexed
// UPDATE, over in milliseconds. So it takes the same lock in the same lock
// space with pg_advisory_xact_lock, inside a transaction that also carries the
// UPDATE — the two conflict with each other exactly as two session locks would,
// because PostgreSQL does not distinguish them by holder.
//
// That is not a stylistic choice. Taking a session lock here would mean holding
// one pooled connection for the lock and asking for a *second* to run the
// statement: with several invalidations racing, every connection can end up
// held by a waiter while the holder waits for a connection that will never come
// free. One connection, one transaction, no ordering to get wrong.
//
// The lock is released by the commit, and by the rollback if anything fails, so
// there is no path that leaves an attachment fenced.
//
// # One id, not two
//
// The attachment is named once and used twice — to derive the fence key and as
// the statement's $1 — because those two must be the same row or the fence
// protects nothing. An earlier signature took them as separate parameters; every
// caller passed the same value, but nothing said they had to, and a caller that
// eventually did not would take the lock on A while the UPDATE modified B. That
// is not a defect a test finds later, so the signature carries the invariant
// instead.
func (s *PGXAttachmentStore) applyFencedTransition(
	ctx context.Context, attachmentID, query, workspaceID, invalidInput, notApplicable string,
) (service.AttachmentLifecycleState, error) {
	if s == nil || s.pool == nil {
		return service.AttachmentLifecycleState{}, domain.ErrDependenciesUnavailable
	}
	if s.fence == nil {
		// Fail closed. An unfenced invalidation is not a degraded mode, it is
		// the bug: it can commit underneath a running parser.
		return service.AttachmentLifecycleState{}, fmt.Errorf(
			"%w: %s", domain.ErrDependenciesUnavailable, errFenceUnavailable)
	}
	// Validated before anything is locked, so a malformed request cannot make a
	// legitimate one wait.
	if attachmentID == "" || workspaceID == "" {
		return service.AttachmentLifecycleState{}, fmt.Errorf(
			"%w: %s", domain.ErrInvalidInput, invalidInput)
	}
	key, err := attachmentFenceKey(attachmentID)
	if err != nil {
		return service.AttachmentLifecycleState{}, err
	}
	return s.fence.WithinTransaction(ctx, key, func(tx TransactionalQuerier) (service.AttachmentLifecycleState, error) {
		return scanLifecycleTransition(ctx, tx, query, attachmentID, workspaceID, notApplicable)
	})
}

// applyLifecycleTransition runs one conditional lifecycle statement and reads
// the resulting state back from it.
//
// The three lifecycle operations — approve, reject, remove — differ only in
// their statement. Sharing the execution keeps them identical where they must
// be: the same tenant scoping, the same refusal to treat "no row matched" as
// success, and the same validation of what came back.
func (s *PGXAttachmentStore) applyLifecycleTransition(
	ctx context.Context, query, attachmentID, workspaceID, invalidInput, notApplicable string,
) (service.AttachmentLifecycleState, error) {
	if s == nil || s.pool == nil {
		return service.AttachmentLifecycleState{}, domain.ErrDependenciesUnavailable
	}
	if attachmentID == "" || workspaceID == "" {
		return service.AttachmentLifecycleState{}, fmt.Errorf(
			"%w: %s", domain.ErrInvalidInput, invalidInput)
	}
	return scanLifecycleTransition(ctx, s.pool, query, attachmentID, workspaceID, notApplicable)
}

// TransactionalQuerier is the sliver of pgx a lifecycle statement needs. It is
// satisfied by both a pool and a transaction, which is what lets the fenced and
// unfenced paths share one implementation of the statement.
type TransactionalQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// scanLifecycleTransition runs the statement and reads the resulting state back.
func scanLifecycleTransition(
	ctx context.Context, q TransactionalQuerier, query, attachmentID, workspaceID, notApplicable string,
) (service.AttachmentLifecycleState, error) {
	var status, previewStatus pgtype.Text
	err := q.QueryRow(ctx, query, attachmentID, workspaceID).Scan(&status, &previewStatus)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Never silently a success: a transition that matched nothing has
			// not been recorded, and the caller has to be able to tell.
			return service.AttachmentLifecycleState{}, fmt.Errorf(
				"%w: %s", domain.ErrNotFound, notApplicable)
		}
		return service.AttachmentLifecycleState{}, fmt.Errorf("apply attachment transition: %w", err)
	}

	state := service.AttachmentLifecycleState{
		Status:        domain.Status(status.String),
		PreviewStatus: previewStatusOf(previewStatus),
	}
	if !state.Status.Valid() {
		return service.AttachmentLifecycleState{}, errors.New("attachment has an unknown status")
	}
	return state, nil
}

// GetAuthorized returns an attachment only if the caller's session is still
// active and the caller can currently reach the attachment's destination. The
// membership check is re-evaluated on every read, so revoking access takes
// effect on the next download rather than at some cache expiry.
//
// It always returns exactly one row: NULL columns mean "not visible", which is
// reported as domain.ErrNotFound whether the attachment is absent, soft
// deleted, or in a destination the caller cannot reach.
func (s *PGXAttachmentStore) GetAuthorized(
	ctx context.Context, input service.AttachmentAuthInput,
) (service.StoredAttachment, error) {
	if s == nil || s.pool == nil {
		return service.StoredAttachment{}, domain.ErrDependenciesUnavailable
	}

	var (
		sessionExpiresAt pgtype.Timestamptz
		id               pgtype.Text
		workspaceID      pgtype.Text
		destinationKind  pgtype.Text
		status           pgtype.Text
		filename         pgtype.Text
		declaredMIME     pgtype.Text
		detectedMIME     pgtype.Text
		size             pgtype.Int8
		objectKey        pgtype.Text
		envelopeVersion  pgtype.Int2
		wrappedDEK       []byte
		kekKeyID         pgtype.Text
		keyWrapVersion   pgtype.Int2
		createdAt        pgtype.Timestamptz

		previewStatus          pgtype.Text
		previewObjectID        pgtype.Text
		previewSize            pgtype.Int8
		previewWrappedDEK      []byte
		previewKEKKeyID        pgtype.Text
		previewEnvelopeVersion pgtype.Int2
		previewKeyWrapVersion  pgtype.Int2
	)
	err := s.pool.QueryRow(ctx, authorizedAttachmentQuery,
		input.SessionID, input.UserID, input.AttachmentID,
	).Scan(
		&sessionExpiresAt, &id, &workspaceID, &destinationKind, &status,
		&filename, &declaredMIME, &detectedMIME, &size, &objectKey,
		&envelopeVersion, &wrappedDEK, &kekKeyID, &keyWrapVersion, &createdAt,
		&previewStatus, &previewObjectID, &previewSize, &previewWrappedDEK,
		&previewKEKKeyID, &previewEnvelopeVersion, &previewKeyWrapVersion,
	)
	if err != nil {
		return service.StoredAttachment{}, fmt.Errorf("read authorized attachment: %w", err)
	}
	if !sessionExpiresAt.Valid {
		return service.StoredAttachment{}, domain.ErrUnauthorized
	}
	if !id.Valid {
		return service.StoredAttachment{}, domain.ErrNotFound
	}

	attachmentStatus := domain.Status(status.String)
	if !attachmentStatus.Valid() {
		// A row outside the CHECK's closed set is a data-integrity problem, not
		// something to serve.
		return service.StoredAttachment{}, errors.New("attachment has an unknown status")
	}
	return service.StoredAttachment{
		ID:               id.String,
		WorkspaceID:      workspaceID.String,
		Kind:             domain.DestinationKind(destinationKind.String),
		Status:           attachmentStatus,
		Filename:         filename.String,
		DeclaredMIME:     declaredMIME.String,
		DetectedMIME:     detectedMIME.String,
		Size:             size.Int64,
		StorageObjectKey: objectKey.String,
		EnvelopeVersion:  int(envelopeVersion.Int16),
		WrappedDEK:       wrappedDEK,
		// A NULL key id means the row is still pending_upload: no key has been
		// sealed for it yet. It stays empty rather than being defaulted to the
		// active key, so a download fails closed instead of claiming a binding
		// that was never produced. The wrap version is NOT NULL from creation,
		// which is what fences out a writer running the previous build.
		KEKKeyID:         kekKeyID.String,
		KeyWrapVersion:   int(keyWrapVersion.Int16),
		CreatedAt:        createdAt.Time.UTC(),
		SessionExpiresAt: sessionExpiresAt.Time.UTC(),

		// The preview columns are NULL until one exists, which the CHECK ties
		// to preview_status: an unknown or absent status therefore reads as
		// "no preview", never as a preview with missing material.
		PreviewStatus:          previewStatusOf(previewStatus),
		PreviewObjectID:        previewObjectID.String,
		PreviewSize:            previewSize.Int64,
		PreviewWrappedDEK:      previewWrappedDEK,
		PreviewKEKKeyID:        previewKEKKeyID.String,
		PreviewEnvelopeVersion: int(previewEnvelopeVersion.Int16),
		PreviewKeyWrapVersion:  int(previewKeyWrapVersion.Int16),
	}, nil
}

// Listing queries, one per destination kind.
//
// They are separate static constants rather than one query with a computed
// predicate because each has to line up with its own partial index:
//
//	idx_attachments_channel      (workspace_id, channel_id,      created_at DESC)
//	                             WHERE destination_kind = 'channel' AND deleted_at IS NULL
//	idx_attachments_conversation (workspace_id, conversation_id, created_at DESC)
//	                             WHERE destination_kind = 'dm'      AND deleted_at IS NULL
//
// Two things make each index actually usable. The destination column is
// compared directly — a COALESCE over both columns is an expression neither
// index covers, so the planner would fall back to scanning and sorting the
// workspace's attachments before applying LIMIT. And destination_kind is a
// literal, so the partial index predicate is satisfied at plan time; as a bind
// parameter the planner cannot prove it matches the index's WHERE clause and
// would skip the index for that reason alone.
//
// With equality on (workspace_id, destination) the index also supplies
// created_at DESC directly, so ORDER BY … LIMIT reads at most Limit index
// entries instead of sorting the destination's whole history. The id DESC
// tie-break only orders rows sharing a timestamp.
//
// The two share their parameter positions, so the caller passes the same
// arguments in the same order whichever one it picks:
//
//	$1 workspace_id   $2 destination id   $3 listable statuses   $4 limit
const (
	listChannelAttachmentsQuery = `
		SELECT a.id::text, a.status, a.preview_status, a.original_filename,
		       COALESCE(a.detected_mime, ''), a.size_bytes, a.created_at
		FROM files.attachments AS a
		WHERE a.destination_kind = 'channel'
		  AND a.deleted_at IS NULL
		  AND a.workspace_id = $1
		  AND a.channel_id = $2
		  AND a.status = ANY($3)
		ORDER BY a.created_at DESC, a.id DESC
		LIMIT $4`

	listDMAttachmentsQuery = `
		SELECT a.id::text, a.status, a.preview_status, a.original_filename,
		       COALESCE(a.detected_mime, ''), a.size_bytes, a.created_at
		FROM files.attachments AS a
		WHERE a.destination_kind = 'dm'
		  AND a.deleted_at IS NULL
		  AND a.workspace_id = $1
		  AND a.conversation_id = $2
		  AND a.status = ANY($3)
		ORDER BY a.created_at DESC, a.id DESC
		LIMIT $4`
)

// listAttachmentsQueryForKind picks one of the two constants above.
//
// It switches on the validated domain enum and returns one of two strings known
// at compile time — no column name, table name or predicate is ever built from
// a request value, so there is nothing here for a caller to influence.
func listAttachmentsQueryForKind(kind domain.DestinationKind) (string, error) {
	switch kind {
	case domain.DestinationKindChannel:
		return listChannelAttachmentsQuery, nil
	case domain.DestinationKindDM:
		return listDMAttachmentsQuery, nil
	default:
		return "", fmt.Errorf("%w: invalid destination kind", domain.ErrInvalidInput)
	}
}

// ListDestinationAttachments returns a destination's most recent listable
// attachments.
//
// The caller's access has already been settled by AuthorizeDestination, and
// every identifier below comes from that answer — the workspace in particular
// is the destination row's own, never a request value. Each query pins its own
// destination_kind and compares its own destination column, so a channel UUID
// cannot select a conversation's attachments (or the reverse) even if the
// authorization above ever regressed: the two kinds do not share a predicate.
//
// Only the query string varies by kind. Scanning, mapping, error handling and
// row cleanup are shared, so the two paths cannot drift.
func (s *PGXAttachmentStore) ListDestinationAttachments(
	ctx context.Context, query service.ListDestinationAttachmentsQuery,
) ([]service.ListedAttachment, error) {
	if s == nil || s.pool == nil {
		return nil, domain.ErrDependenciesUnavailable
	}
	// Resolved before any database work, so an unknown kind never reaches the
	// pool and never becomes an unfiltered read.
	sql, err := listAttachmentsQueryForKind(query.Kind)
	if err != nil {
		return nil, err
	}
	limit := domain.NormalizeAttachmentListLimit(query.Limit)
	rows, err := s.pool.Query(ctx, sql,
		query.WorkspaceID, query.DestinationID, listableStatuses(), limit,
	)
	if err != nil {
		return nil, fmt.Errorf("list destination attachments: %w", err)
	}
	defer rows.Close()

	listed := make([]service.ListedAttachment, 0, limit)
	for rows.Next() {
		var (
			record        service.ListedAttachment
			status        string
			previewStatus pgtype.Text
			createdAt     pgtype.Timestamptz
		)
		if err := rows.Scan(
			&record.ID, &status, &previewStatus, &record.Filename,
			&record.DetectedMIME, &record.Size, &createdAt,
		); err != nil {
			return nil, fmt.Errorf("scan destination attachment: %w", err)
		}
		attachmentStatus := domain.Status(status)
		if !attachmentStatus.Valid() {
			// A row outside the CHECK's closed set is a data-integrity problem,
			// not something to serve.
			return nil, errors.New("attachment has an unknown status")
		}
		record.Status = attachmentStatus
		record.PreviewStatus = previewStatusOf(previewStatus)
		record.CreatedAt = createdAt.Time.UTC()
		listed = append(listed, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate destination attachments: %w", err)
	}
	return listed, nil
}

// previewStatusOf maps a stored preview state onto the domain enum, failing
// closed: a NULL — which only a row written before migration 000003 can have —
// and any value outside the closed set both read as "unsupported", the state
// that makes a client draw its fallback. It never invents "ready".
func previewStatusOf(stored pgtype.Text) domain.PreviewStatus {
	status := domain.PreviewStatus(stored.String)
	if !stored.Valid || !status.Valid() {
		return domain.PreviewStatusUnsupported
	}
	return status
}

// listableStatuses is the closed set the listing selects, derived from the
// domain predicate so the SQL filter and Status.Listable can never disagree.
func listableStatuses() []string {
	all := []domain.Status{
		domain.StatusPendingUpload, domain.StatusPendingScan, domain.StatusClean,
		domain.StatusRejected, domain.StatusFailed, domain.StatusDeleted,
	}
	values := make([]string, 0, len(all))
	for _, status := range all {
		if status.Listable() {
			values = append(values, string(status))
		}
	}
	return values
}

// authorizedAttachmentQuery applies the same destination visibility rules the
// upload path uses, against the attachment's stored destination. The LEFT JOIN
// onto a one-row source guarantees a single result row so an invalid session
// (401) stays distinguishable from an invisible attachment (404) without a
// second query.
const authorizedAttachmentQuery = authsession.ActiveSessionCTE + `,
	authorized AS (
		SELECT a.id, a.workspace_id, a.destination_kind, a.status,
		       a.original_filename, a.declared_mime, a.detected_mime,
		       a.size_bytes, a.storage_object_key, a.envelope_version,
		       a.wrapped_dek, a.kek_key_id, a.dek_wrap_version, a.created_at,
		       a.preview_status, a.preview_object_id, a.preview_size_bytes,
		       a.preview_wrapped_dek, a.preview_kek_key_id,
		       a.preview_envelope_version, a.preview_dek_wrap_version
		FROM files.attachments AS a
		CROSS JOIN active_session AS active
		WHERE a.id = $3
		  AND a.deleted_at IS NULL
		  AND (
		    (a.destination_kind = 'channel' AND EXISTS (
		        SELECT 1
		        FROM chat.channels AS c
		        JOIN chat.workspaces AS w
		          ON w.id = c.workspace_id AND w.status = 'active'
		        JOIN chat.workspace_members AS wm
		          ON wm.workspace_id = c.workspace_id
		         AND wm.user_id = active.user_id
		         AND wm.status = 'active'
		        LEFT JOIN chat.channel_members AS cm
		          ON cm.channel_id = c.id AND cm.user_id = active.user_id
		        WHERE c.id = a.channel_id
		          AND c.workspace_id = a.workspace_id
		          AND c.status = 'active'
		          AND (c.type = 'public' OR cm.user_id IS NOT NULL)
		    ))
		    OR
		    (a.destination_kind = 'dm' AND EXISTS (
		        SELECT 1
		        FROM chat.dm_conversations AS dc
		        JOIN chat.workspaces AS w
		          ON w.id = dc.workspace_id AND w.status = 'active'
		        JOIN chat.workspace_members AS wm
		          ON wm.workspace_id = dc.workspace_id
		         AND wm.user_id = active.user_id
		         AND wm.status = 'active'
		        JOIN chat.dm_members AS dm
		          ON dm.conversation_id = dc.id
		         AND dm.user_id = active.user_id
		         AND dm.status = 'active'
		        WHERE dc.id = a.conversation_id
		          AND dc.workspace_id = a.workspace_id
		          AND dc.status = 'active'
		    ))
		  )
	)
	SELECT
		(SELECT session_expires_at FROM active_session) AS session_expires_at,
		authorized.id::text,
		authorized.workspace_id::text,
		authorized.destination_kind,
		authorized.status,
		authorized.original_filename,
		authorized.declared_mime,
		authorized.detected_mime,
		authorized.size_bytes,
		authorized.storage_object_key,
		authorized.envelope_version,
		authorized.wrapped_dek,
		authorized.kek_key_id,
		authorized.dek_wrap_version,
		authorized.created_at,
		authorized.preview_status,
		authorized.preview_object_id::text,
		authorized.preview_size_bytes,
		authorized.preview_wrapped_dek,
		authorized.preview_kek_key_id,
		authorized.preview_envelope_version,
		authorized.preview_dek_wrap_version
	FROM (SELECT 1) AS single_row
	LEFT JOIN authorized ON true`
