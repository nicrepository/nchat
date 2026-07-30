package storage

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/nicrepository/nchat/libs/go/platform/authsession"
	"github.com/nicrepository/nchat/services/file-service/internal/domain"
	"github.com/nicrepository/nchat/services/file-service/internal/service"
)

// PGXAttachmentStore persists attachment metadata in files.attachments.
type PGXAttachmentStore struct {
	pool Pool
}

func NewPGXAttachmentStore(pool Pool) *PGXAttachmentStore {
	return &PGXAttachmentStore{pool: pool}
}

// CreatePending writes the row an upload starts from. The status is fixed here
// and never taken from a request, and channel_id / conversation_id are filled
// from the destination kind so the exclusivity CHECK can never be reached with
// both set.
func (s *PGXAttachmentStore) CreatePending(ctx context.Context, attachment service.NewAttachment) error {
	if s == nil || s.pool == nil {
		return domain.ErrDependenciesUnavailable
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
			envelope_version, wrapped_dek, status
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`,
		attachment.ID, attachment.WorkspaceID, attachment.UploaderID,
		string(attachment.Destination.Kind), channelID, conversationID,
		attachment.Filename, attachment.DeclaredMIME,
		attachment.StorageProvider, attachment.StorageObjectKey,
		attachment.EnvelopeVersion, attachment.WrappedDEK,
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
func (s *PGXAttachmentStore) MarkUploaded(ctx context.Context, update service.UploadedAttachment) error {
	if s == nil || s.pool == nil {
		return domain.ErrDependenciesUnavailable
	}
	if !update.Status.Valid() {
		return fmt.Errorf("%w: invalid attachment status", domain.ErrInvalidInput)
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE files.attachments
		   SET status = $2,
		       detected_mime = $3,
		       size_bytes = $4,
		       ciphertext_size_bytes = $5,
		       uploaded_at = now(),
		       updated_at = now()
		 WHERE id = $1
		   AND status = $6`,
		update.ID, string(update.Status), update.DetectedMIME,
		update.Size, update.CiphertextSize, string(domain.StatusPendingUpload),
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
		createdAt        pgtype.Timestamptz
	)
	err := s.pool.QueryRow(ctx, authorizedAttachmentQuery,
		input.SessionID, input.UserID, input.AttachmentID,
	).Scan(
		&sessionExpiresAt, &id, &workspaceID, &destinationKind, &status,
		&filename, &declaredMIME, &detectedMIME, &size, &objectKey,
		&envelopeVersion, &wrappedDEK, &createdAt,
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
		CreatedAt:        createdAt.Time.UTC(),
		SessionExpiresAt: sessionExpiresAt.Time.UTC(),
	}, nil
}

// ListChannelAttachments returns a channel's most recent listable attachments.
//
// The caller's access to the channel has already been settled by
// AuthorizeDestination, and both identifiers below come from that answer — the
// workspace in particular is the destination row's own, never a request value.
// Filtering on both is what keeps a channel UUID from another tenant from
// selecting anything, even if the authorization ever regressed.
//
// The predicate and the order match idx_attachments_channel
// (workspace_id, channel_id, created_at DESC WHERE destination_kind = 'channel'
// AND deleted_at IS NULL), so this is an index scan of at most Limit rows and
// never a table scan of a channel's history. id DESC breaks a created_at tie so
// two attachments written in the same transaction always come back in the same
// order.
func (s *PGXAttachmentStore) ListChannelAttachments(
	ctx context.Context, query service.ListDestinationAttachmentsQuery,
) ([]service.ListedAttachment, error) {
	if s == nil || s.pool == nil {
		return nil, domain.ErrDependenciesUnavailable
	}
	limit := domain.NormalizeAttachmentListLimit(query.Limit)
	rows, err := s.pool.Query(ctx, `
		SELECT a.id::text, a.status, a.original_filename,
		       COALESCE(a.detected_mime, ''), a.size_bytes, a.created_at
		FROM files.attachments AS a
		WHERE a.destination_kind = 'channel'
		  AND a.deleted_at IS NULL
		  AND a.workspace_id = $1
		  AND a.channel_id = $2
		  AND a.status = ANY($3)
		ORDER BY a.created_at DESC, a.id DESC
		LIMIT $4`,
		query.WorkspaceID, query.ChannelID, listableStatuses(), limit,
	)
	if err != nil {
		return nil, fmt.Errorf("list channel attachments: %w", err)
	}
	defer rows.Close()

	listed := make([]service.ListedAttachment, 0, limit)
	for rows.Next() {
		var (
			record    service.ListedAttachment
			status    string
			createdAt pgtype.Timestamptz
		)
		if err := rows.Scan(
			&record.ID, &status, &record.Filename,
			&record.DetectedMIME, &record.Size, &createdAt,
		); err != nil {
			return nil, fmt.Errorf("scan channel attachment: %w", err)
		}
		attachmentStatus := domain.Status(status)
		if !attachmentStatus.Valid() {
			// A row outside the CHECK's closed set is a data-integrity problem,
			// not something to serve.
			return nil, errors.New("attachment has an unknown status")
		}
		record.Status = attachmentStatus
		record.CreatedAt = createdAt.Time.UTC()
		listed = append(listed, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate channel attachments: %w", err)
	}
	return listed, nil
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
		       a.wrapped_dek, a.created_at
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
		authorized.created_at
	FROM (SELECT 1) AS single_row
	LEFT JOIN authorized ON true`
