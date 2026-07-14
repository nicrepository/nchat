package storage

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
)

// defaultMessageLimit is the number of messages returned when no limit is specified.
const defaultMessageLimit = 50

// maxMessageLimit is the maximum number of messages that may be requested per page.
const maxMessageLimit = 100

// MessageCursor identifies the stable position of a message in the time-ordered list.
// It encodes (created_at, id) to allow keyset pagination without offset drift.
type MessageCursor struct {
	CreatedAt time.Time
	ID        string
}

// EncodeCursor serializes a MessageCursor to an opaque URL-safe base64 string.
// Format (plaintext before encoding): RFC3339Nano "|" UUID.
func EncodeCursor(c MessageCursor) string {
	raw := c.CreatedAt.UTC().Format(time.RFC3339Nano) + "|" + c.ID
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// DecodeCursor parses an opaque cursor string produced by EncodeCursor.
// Returns domain.ErrInvalidCursor for any malformed or invalid input.
func DecodeCursor(s string) (MessageCursor, error) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return MessageCursor{}, domain.ErrInvalidCursor
	}
	parts := strings.SplitN(string(b), "|", 2)
	if len(parts) != 2 {
		return MessageCursor{}, domain.ErrInvalidCursor
	}
	ts, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return MessageCursor{}, domain.ErrInvalidCursor
	}
	if _, err := uuid.Parse(parts[1]); err != nil {
		return MessageCursor{}, domain.ErrInvalidCursor
	}
	return MessageCursor{CreatedAt: ts.UTC(), ID: parts[1]}, nil
}

// ListMessagesResult is the result of a paginated message list query.
// Messages are sorted oldest-first (ASC by created_at, id).
// NextCursor, when non-nil, is the cursor to pass as BeforeCursor in a subsequent
// request to retrieve the next (older) page.
type ListMessagesResult struct {
	Messages   []domain.Message
	NextCursor *MessageCursor
}

// CreateMessageInput holds caller-validated fields for inserting a message.
// Exactly one of ChannelID and DMConversationID must be non-empty.
// ParentMessageID, ForwardedFromMessageID, and ReferencedMessageID are optional
// (empty string = NULL). Kind defaults to 'user' when empty.
type CreateMessageInput struct {
	WorkspaceID            string
	ChannelID              string
	DMConversationID       string
	SenderID               string
	Kind                   domain.MessageKind
	BodyText               string
	BodyFormat             domain.MessageBodyFormat
	ParentMessageID        string
	ForwardedFromMessageID string
	ReferencedMessageID    string
	MentionedUserIDs       []string
	MentionedChannelIDs    []string
}

// EditMessageInput contains only server-derived identity and validated body fields.
type EditMessageInput struct {
	WorkspaceID string
	MessageID   string
	EditorID    string
	Body        string
	BodyFormat  domain.MessageBodyFormat
}

// DeleteMessageInput contains only server-derived identity and scope fields.
type DeleteMessageInput struct {
	WorkspaceID string
	MessageID   string
	RequesterID string
}

// ListMessageEditHistoryInput identifies one authorized history page.
type ListMessageEditHistoryInput struct {
	WorkspaceID string
	MessageID   string
	UserID      string
	Limit       int
	Offset      int
}

// ListChannelMessagesInput identifies the paged message list for a channel.
type ListChannelMessagesInput struct {
	WorkspaceID string
	ChannelID   string
	// UserID is the requesting caller; SQL enforces channel visibility for this user.
	UserID string
	// BeforeCursor, when non-nil, restricts results to messages older than the cursor.
	// When nil, the most recent Limit messages are returned.
	BeforeCursor *MessageCursor
	// Limit is the maximum number of messages to return. 0 uses the default (50).
	// Values above maxMessageLimit (100) are capped.
	Limit int
}

// ListDMMessagesInput identifies the paged message list for a DM conversation.
type ListDMMessagesInput struct {
	WorkspaceID    string
	ConversationID string
	// UserID is the requesting caller; SQL enforces DM visibility for this user.
	UserID string
	// BeforeCursor, when non-nil, restricts results to messages older than the cursor.
	// When nil, the most recent Limit messages are returned.
	BeforeCursor *MessageCursor
	// Limit is the maximum number of messages to return. 0 uses the default (50).
	// Values above maxMessageLimit (100) are capped.
	Limit int
}

// MessageStore is the persistence interface for message operations.
type MessageStore interface {
	// CreateMessage inserts a new message and returns the persisted record.
	// Returns ErrInvalidMessageReference when any of the optional reference fields
	// (parent, forwarded_from, referenced) do not belong to the same workspace and
	// same target as the new message. This is a non-enumerating storage backstop:
	// callers cannot determine whether the referenced message exists.
	CreateMessage(ctx context.Context, input CreateMessageInput) (domain.Message, error)
	EditMessage(ctx context.Context, input EditMessageInput) (domain.Message, error)
	DeleteMessage(ctx context.Context, input DeleteMessageInput) (domain.Message, bool, error)
	ListMessageEditHistory(ctx context.Context, input ListMessageEditHistoryInput) ([]domain.MessageEditHistory, error)

	// GetMessageByIDInWorkspace returns the message only if it belongs to workspaceID.
	// Returns ErrNotFound when the message does not exist or belongs to a different
	// workspace, preventing cross-workspace enumeration via message IDs.
	GetMessageByIDInWorkspace(ctx context.Context, workspaceID, messageID, userID string) (domain.Message, error)

	// ValidateRefMessageInTarget checks that messageID belongs to the given workspace
	// and target (channelID or dmConversationID). Returns nil when valid.
	// Returns ErrInvalidMessageReference for any invalid case — non-enumerating:
	// missing, cross-workspace, cross-channel, and cross-DM all return the same error.
	ValidateRefMessageInTarget(ctx context.Context, workspaceID, channelID, dmConversationID, messageID, userID string) error

	// ResolveMentionLabels returns current display labels keyed by "user:<uuid>"
	// or "channel:<uuid>", scoped to workspaceID.
	ResolveMentionLabels(ctx context.Context, workspaceID string, userIDs, channelIDs []string) (map[string]string, error)

	// ResolveAuthorizedMentionLabels returns labels only for references that are
	// valid in sourceChannelID for requesterID. CreateMessage repeats this check
	// atomically as the final authorization backstop.
	ResolveAuthorizedMentionLabels(ctx context.Context, workspaceID, sourceChannelID, requesterID string, userIDs, channelIDs []string) (map[string]string, error)

	// ListChannelMessages returns a paginated set of messages for a channel.
	// Visibility is enforced in SQL: active workspace, active workspace membership,
	// active channel, and private-channel membership are all required.
	// Returns an empty result when the channel is not visible to UserID.
	// Results are sorted oldest-first (ASC). NextCursor is nil when no older page exists.
	ListChannelMessages(ctx context.Context, input ListChannelMessagesInput) (ListMessagesResult, error)

	// ListDMMessages returns a paginated set of messages for a DM conversation.
	// Visibility is enforced in SQL: active workspace, active workspace membership,
	// active DM conversation, and active DM membership are all required.
	// Returns an empty result when the conversation is not visible to UserID.
	// Results are sorted oldest-first (ASC). NextCursor is nil when no older page exists.
	ListDMMessages(ctx context.Context, input ListDMMessagesInput) (ListMessagesResult, error)
}

// PGXMessageStore implements MessageStore using a pgx connection pool.
type PGXMessageStore struct {
	pool Pool
}

// NewPGXMessageStore creates a PGXMessageStore backed by the given pool.
func NewPGXMessageStore(pool Pool) *PGXMessageStore {
	return &PGXMessageStore{pool: pool}
}

// When alias is non-empty (e.g. "m"), columns are prefixed to avoid ambiguity
// in JOIN queries.
func messageColumns(alias string) string {
	p := ""
	if alias != "" {
		p = alias + "."
	}
	return p + `id::text, ` + p + `workspace_id::text,
	COALESCE(` + p + `channel_id::text, ''),
	COALESCE(` + p + `dm_conversation_id::text, ''),
	` + p + `sender_id::text,
	` + p + `kind, ` + p + `body_text, ` + p + `body_format, ` + p + `status,
	COALESCE(` + p + `parent_message_id::text, ''),
	COALESCE(` + p + `forwarded_from_message_id::text, ''),
	COALESCE(` + p + `referenced_message_id::text, ''),
	` + p + `edited_at, ` + p + `edit_count, ` + p + `deleted_at,
	` + p + `created_at, ` + p + `updated_at`
}

// listMessageColumns returns messageColumns plus sender display info from
// auth.users and the caller-scoped is_favorited flag. Requires a LEFT JOIN
// auth.users aliased as "u" in the query. userParam is the positional
// placeholder (e.g. "$3") holding the requesting user's ID — a code literal,
// never user input. Computing the flag inline keeps favorite marking a single
// index-backed query instead of a per-message lookup (RF-06 anti-N+1).
func listMessageColumns(alias, userParam string) string {
	return messageColumns(alias) + `,
	COALESCE(u.display_name, ''),
	COALESCE(u.email::text, ''),
	EXISTS (
		SELECT 1 FROM chat.message_favorites mf
		WHERE mf.message_id = ` + alias + `.id AND mf.user_id = ` + userParam + `::uuid
	)`
}

func quotedMessageColumns(alias string) string {
	return `
	COALESCE(` + alias + `.id::text, ''),
	COALESCE(` + alias + `.sender_id::text, ''),
	COALESCE(` + alias + `.body_text, ''),
	COALESCE(` + alias + `.body_format::text, ''),
	COALESCE(` + alias + `.status::text, ''),
	` + alias + `.deleted_at,
	` + alias + `.created_at`
}

func listMessageWithQuoteColumns(alias, userParam, quoteAlias string) string {
	return listMessageColumns(alias, userParam) + `,` + quotedMessageColumns(quoteAlias)
}

func quotedMessageJoin(alias, quoteAlias string) string {
	return `
		LEFT JOIN chat.messages ` + quoteAlias + `
		  ON ` + quoteAlias + `.id = ` + alias + `.parent_message_id
		 AND ` + quoteAlias + `.workspace_id = ` + alias + `.workspace_id
		 AND ` + quoteAlias + `.channel_id IS NOT DISTINCT FROM ` + alias + `.channel_id
		 AND ` + quoteAlias + `.dm_conversation_id IS NOT DISTINCT FROM ` + alias + `.dm_conversation_id`
}

// scanMessageWithSenderAndQuote reads a single message row including sender
// display info and optional quote preview. It must be called with exactly the
// columns listed in listMessageWithQuoteColumns.
func scanMessageWithSenderAndQuote(row pgx.Row) (domain.Message, error) {
	var msg domain.Message
	var editedAt, deletedAt *time.Time
	var quote domain.QuotedMessage
	var quoteDeletedAt, quoteCreatedAt *time.Time
	err := row.Scan(
		&msg.ID, &msg.WorkspaceID,
		&msg.ChannelID, &msg.DMConversationID,
		&msg.SenderID,
		(*string)(&msg.Kind), &msg.BodyText, (*string)(&msg.BodyFormat), (*string)(&msg.Status),
		&msg.ParentMessageID, &msg.ForwardedFromMessageID, &msg.ReferencedMessageID,
		&editedAt, &msg.EditCount, &deletedAt,
		&msg.CreatedAt, &msg.UpdatedAt,
		&msg.SenderDisplayName, &msg.SenderEmail,
		&msg.IsFavorited,
		&quote.ID, &quote.AuthorID, &quote.BodyText, (*string)(&quote.BodyFormat), (*string)(&quote.Status),
		&quoteDeletedAt, &quoteCreatedAt,
	)
	if err != nil {
		return domain.Message{}, err
	}
	if editedAt != nil {
		msg.EditedAt = *editedAt
	}
	if deletedAt != nil {
		msg.DeletedAt = *deletedAt
	}
	if quote.ID != "" {
		if quoteDeletedAt != nil {
			quote.DeletedAt = *quoteDeletedAt
		}
		if quoteCreatedAt != nil {
			quote.CreatedAt = *quoteCreatedAt
		}
		msg.Quoted = &quote
	}
	return msg, nil
}

// nullableUUID converts an empty string to nil for pgx nullable UUID parameters.
func nullableUUID(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func (s *PGXMessageStore) CreateMessage(ctx context.Context, input CreateMessageInput) (domain.Message, error) {
	kind := input.Kind
	if kind == "" {
		kind = domain.MessageKindUser
	}
	bodyFormat := input.BodyFormat
	if bodyFormat == "" {
		bodyFormat = domain.MessageBodyFormatV1
	}
	// Authorization and reference integrity are enforced atomically in one INSERT.
	//
	// The auth subquery (UNION ALL of channel branch + DM branch) yields exactly one
	// row only when the sender is authorized at insert time:
	//   channel branch ($2 IS NOT NULL):
	//     - workspace active, sender is active workspace_member
	//     - channel belongs to workspace, channel is active
	//     - public channel: active workspace member is sufficient
	//     - private channel: sender must also be an active channel_member
	//   DM branch ($3 IS NOT NULL):
	//     - workspace active, sender is active workspace_member
	//     - DM conversation belongs to workspace, DM conversation is active
	//     - sender must be an active dm_member
	//
	// Stale channel_members / dm_members cannot bypass an inactive/suspended/left
	// workspace_member because the workspace_members JOIN filters wm.status = 'active'
	// independently.
	//
	// The invalid_refs CTE fires when any provided reference message does not belong
	// to the same workspace + same target. Both conditions are checked together;
	// either failure results in 0 rows, which maps to ErrNotFound (non-enumerating
	// TOCTOU backstop — the service layer performs typed pre-validation before insert).
	//
	// The INSERT is wrapped in a CTE so the outer SELECT can JOIN auth.users and
	// return sender display info (sender_display_name, sender_email) in the same
	// round-trip. This avoids a separate GET after insert for the broadcast payload.
	row := s.pool.QueryRow(ctx, `
		WITH user_mentions AS (
			SELECT DISTINCT id::uuid AS user_id
			FROM unnest($11::text[]) AS ids(id)
		),
		channel_mentions AS (
			SELECT DISTINCT id::uuid AS channel_id
			FROM unnest($12::text[]) AS ids(id)
		),
		invalid_refs AS (
			SELECT 1 FROM (VALUES (1)) v(x)
			WHERE
				($8::uuid IS NOT NULL AND NOT EXISTS (
					SELECT 1 FROM chat.messages
					WHERE id = $8::uuid
					  AND workspace_id = $1::uuid
					  AND channel_id IS NOT DISTINCT FROM $2::uuid
					  AND dm_conversation_id IS NOT DISTINCT FROM $3::uuid
				))
				OR ($9::uuid IS NOT NULL AND NOT EXISTS (
					SELECT 1 FROM chat.messages
					WHERE id = $9::uuid
					  AND workspace_id = $1::uuid
					  AND channel_id IS NOT DISTINCT FROM $2::uuid
					  AND dm_conversation_id IS NOT DISTINCT FROM $3::uuid
				))
				OR ($10::uuid IS NOT NULL AND NOT EXISTS (
					SELECT 1 FROM chat.messages
					WHERE id = $10::uuid
					  AND workspace_id = $1::uuid
					  AND channel_id IS NOT DISTINCT FROM $2::uuid
					  AND dm_conversation_id IS NOT DISTINCT FROM $3::uuid
				))
		),
		invalid_mentions AS (
			SELECT 1
			FROM user_mentions um
			WHERE $2::uuid IS NULL OR NOT EXISTS (
				SELECT 1
				FROM chat.channels source_channel
				JOIN chat.channel_members cm
				  ON cm.channel_id = source_channel.id AND cm.user_id = um.user_id
				JOIN chat.workspace_members mentioned_member
				  ON mentioned_member.workspace_id = source_channel.workspace_id
				 AND mentioned_member.user_id = um.user_id
				 AND mentioned_member.status = 'active'
				JOIN auth.users mentioned_user
				  ON mentioned_user.id = um.user_id
				 AND mentioned_user.status = 'active'
				 AND mentioned_user.deleted_at IS NULL
				WHERE source_channel.id = $2::uuid
				  AND source_channel.workspace_id = $1::uuid
				  AND source_channel.status = 'active'
			)
			UNION ALL
			SELECT 1
			FROM channel_mentions mentioned
			WHERE NOT EXISTS (
				SELECT 1
				FROM chat.channels c
				JOIN chat.workspaces w
				  ON w.id = c.workspace_id AND w.status = 'active'
				JOIN chat.workspace_members requester
				  ON requester.workspace_id = c.workspace_id
				 AND requester.user_id = $4::uuid
				 AND requester.status = 'active'
				WHERE c.id = mentioned.channel_id
				  AND c.workspace_id = $1::uuid
				  AND c.status = 'active'
				  AND chat.channel_visible_to_user(c.id, $4::uuid)
			)
		),
		inserted AS (
			INSERT INTO chat.messages
				(workspace_id, channel_id, dm_conversation_id, sender_id,
				 kind, body_text, body_format, status,
				 parent_message_id, forwarded_from_message_id, referenced_message_id)
			SELECT $1::uuid, $2::uuid, $3::uuid, $4::uuid, $5, $6, $7, 'active',
			       $8::uuid, $9::uuid, $10::uuid
			FROM (
				-- Channel message authorization branch.
				SELECT 1
				FROM chat.workspaces w
				JOIN chat.workspace_members wm
				  ON wm.workspace_id = w.id AND wm.user_id = $4::uuid AND wm.status = 'active'
				JOIN chat.channels c
				  ON c.id = $2::uuid AND c.workspace_id = $1::uuid AND c.status = 'active'
				LEFT JOIN chat.channel_members cm
				  ON cm.channel_id = c.id AND cm.user_id = $4::uuid
				WHERE $2::uuid IS NOT NULL
				  AND w.id = $1::uuid AND w.status = 'active'
				  AND (c.type = 'public' OR cm.user_id IS NOT NULL)
				UNION ALL
				-- DM message authorization branch.
				SELECT 1
				FROM chat.workspaces w
				JOIN chat.workspace_members wm
				  ON wm.workspace_id = w.id AND wm.user_id = $4::uuid AND wm.status = 'active'
				JOIN chat.dm_conversations dc
				  ON dc.id = $3::uuid AND dc.workspace_id = $1::uuid AND dc.status = 'active'
				JOIN chat.dm_members dm
				  ON dm.conversation_id = dc.id AND dm.user_id = $4::uuid AND dm.status = 'active'
				WHERE $3::uuid IS NOT NULL
				  AND w.id = $1::uuid AND w.status = 'active'
			) auth
			WHERE NOT EXISTS (SELECT 1 FROM invalid_refs)
			  AND NOT EXISTS (SELECT 1 FROM invalid_mentions)
			RETURNING id, workspace_id, channel_id, dm_conversation_id, sender_id,
				          kind, body_text, body_format, status,
			          parent_message_id, forwarded_from_message_id, referenced_message_id,
			          edited_at, edit_count, deleted_at, created_at, updated_at
		),
		mention_outbox AS (
			INSERT INTO chat.notification_outbox
				(workspace_id, message_id, recipient_user_id, kind, status)
			SELECT inserted.workspace_id, inserted.id, user_mentions.user_id, 'mention', 'pending'
			FROM inserted
			CROSS JOIN user_mentions
			ON CONFLICT (message_id, recipient_user_id, kind) DO NOTHING
			RETURNING id
		)
		SELECT `+listMessageWithQuoteColumns("m", "$4", "q")+`
		FROM inserted m
		LEFT JOIN auth.users u ON u.id = m.sender_id`+quotedMessageJoin("m", "q"),
		input.WorkspaceID,
		nullableUUID(input.ChannelID),
		nullableUUID(input.DMConversationID),
		input.SenderID,
		string(kind),
		input.BodyText,
		string(bodyFormat),
		nullableUUID(input.ParentMessageID),
		nullableUUID(input.ForwardedFromMessageID),
		nullableUUID(input.ReferencedMessageID),
		input.MentionedUserIDs,
		input.MentionedChannelIDs,
	)
	msg, err := scanMessageWithSenderAndQuote(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Non-enumerating TOCTOU backstop: both auth failure and reference failure
			// produce 0 rows. The service layer returns typed errors from pre-validation;
			// this backstop returns ErrNotFound to avoid leaking target existence.
			return domain.Message{}, domain.ErrNotFound
		}
		return domain.Message{}, fmt.Errorf("create message: %w", err)
	}
	return msg, nil
}

// EditMessage atomically snapshots the current body and replaces it after
// server-side access, author, deletion, and edit-window validation.
func (s *PGXMessageStore) EditMessage(ctx context.Context, input EditMessageInput) (domain.Message, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.Message{}, fmt.Errorf("begin message edit: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var current domain.Message
	var deletedAt *time.Time
	var editWindowSeconds *int
	var databaseNow time.Time
	err = tx.QueryRow(ctx, `
		SELECT m.sender_id::text, m.status, m.deleted_at, m.created_at,
		       w.edit_window_seconds, clock_timestamp()
		FROM chat.messages m`+messageAccessJoins("$3")+`
		WHERE m.workspace_id = $1 AND m.id = $2
		  AND `+messageAccessPredicate+`
		FOR UPDATE OF m`,
		input.WorkspaceID, input.MessageID, input.EditorID,
	).Scan(&current.SenderID, (*string)(&current.Status), &deletedAt, &current.CreatedAt, &editWindowSeconds, &databaseNow)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Message{}, domain.ErrNotFound
		}
		return domain.Message{}, fmt.Errorf("lock editable message: %w", err)
	}
	if deletedAt != nil {
		current.DeletedAt = *deletedAt
	}
	if err := domain.ValidateMessageEdit(current, input.EditorID, editWindowSeconds, databaseNow); err != nil {
		return domain.Message{}, err
	}

	tag, err := tx.Exec(ctx, `
		INSERT INTO chat.message_edit_history (message_id, body, body_format, editor_user_id, versioned_at)
		SELECT id, body_text, body_format, $2, $3
		FROM chat.messages
		WHERE id = $1`, input.MessageID, input.EditorID, databaseNow)
	if err != nil {
		return domain.Message{}, fmt.Errorf("snapshot message edit: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return domain.Message{}, domain.ErrNotFound
	}

	row := tx.QueryRow(ctx, `
		WITH updated AS (
			UPDATE chat.messages
			SET body_text = $2, body_format = $3, edited_at = $5,
			    edit_count = edit_count + 1, updated_at = $5
			WHERE id = $1
			RETURNING *
		)
		SELECT `+listMessageWithQuoteColumns("m", "$4", "q")+`
		FROM updated m
		LEFT JOIN auth.users u ON u.id = m.sender_id`+quotedMessageJoin("m", "q"),
		input.MessageID, input.Body, string(input.BodyFormat), input.EditorID, databaseNow,
	)
	updated, err := scanMessageWithSenderAndQuote(row)
	if err != nil {
		return domain.Message{}, fmt.Errorf("update message body: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Message{}, fmt.Errorf("commit message edit: %w", err)
	}
	return updated, nil
}

// DeleteMessage atomically re-checks read access and authorship, then marks the
// row deleted. The bool reports whether this call performed the state change.
func (s *PGXMessageStore) DeleteMessage(ctx context.Context, input DeleteMessageInput) (domain.Message, bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.Message{}, false, fmt.Errorf("begin message delete: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var current domain.Message
	var deletedAt *time.Time
	var databaseNow time.Time
	err = tx.QueryRow(ctx, `
		SELECT m.sender_id::text, m.kind, m.status, m.deleted_at, clock_timestamp()
		FROM chat.messages m`+messageAccessJoins("$3")+`
		WHERE m.workspace_id = $1 AND m.id = $2
		  AND `+messageAccessPredicate+`
		FOR UPDATE OF m`,
		input.WorkspaceID, input.MessageID, input.RequesterID,
	).Scan(&current.SenderID, (*string)(&current.Kind), (*string)(&current.Status), &deletedAt, &databaseNow)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Message{}, false, domain.ErrNotFound
		}
		return domain.Message{}, false, fmt.Errorf("lock deletable message: %w", err)
	}
	if deletedAt != nil {
		current.DeletedAt = *deletedAt
	}
	if err := domain.ValidateMessageDelete(current, input.RequesterID); err != nil {
		return domain.Message{}, false, err
	}

	changed := current.Status != domain.MessageStatusDeleted || deletedAt == nil
	if changed {
		tag, err := tx.Exec(ctx, `
			UPDATE chat.messages
			SET status = 'deleted', deleted_at = COALESCE(deleted_at, $4), updated_at = $4
			WHERE id = $1 AND workspace_id = $2 AND sender_id = $3`,
			input.MessageID, input.WorkspaceID, input.RequesterID, databaseNow,
		)
		if err != nil {
			return domain.Message{}, false, fmt.Errorf("soft delete message: %w", err)
		}
		if tag.RowsAffected() != 1 {
			return domain.Message{}, false, domain.ErrNotFound
		}
	}

	row := tx.QueryRow(ctx, `
		SELECT `+listMessageWithQuoteColumns("m", "$3", "q")+`
		FROM chat.messages m
		LEFT JOIN auth.users u ON u.id = m.sender_id`+quotedMessageJoin("m", "q")+`
		WHERE m.id = $1 AND m.workspace_id = $2`,
		input.MessageID, input.WorkspaceID, input.RequesterID,
	)
	deleted, err := scanMessageWithSenderAndQuote(row)
	if err != nil {
		return domain.Message{}, false, fmt.Errorf("read deleted message: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Message{}, false, fmt.Errorf("commit message delete: %w", err)
	}
	return deleted, changed, nil
}

func (s *PGXMessageStore) ListMessageEditHistory(ctx context.Context, input ListMessageEditHistoryInput) ([]domain.MessageEditHistory, error) {
	limit := resolveLimit(input.Limit)
	offset := max(input.Offset, 0)
	rows, err := s.pool.Query(ctx, `
		WITH authorized AS (
			SELECT m.id
			FROM chat.messages m`+messageAccessJoins("$3")+`
			WHERE m.workspace_id = $1 AND m.id = $2
			  AND m.status = 'active' AND m.deleted_at IS NULL
			  AND `+messageAccessPredicate+`
		)
		SELECT COALESCE(h.id::text, ''), COALESCE(h.message_id::text, ''),
		       COALESCE(h.body, ''), COALESCE(h.body_format, ''),
		       COALESCE(h.editor_user_id::text, ''), h.versioned_at
		FROM authorized a
		LEFT JOIN LATERAL (
			SELECT id, message_id, body, body_format, editor_user_id, versioned_at
			FROM chat.message_edit_history
			WHERE message_id = a.id
			ORDER BY versioned_at DESC, id DESC
			LIMIT $4 OFFSET $5
		) h ON true
		ORDER BY h.versioned_at DESC NULLS LAST, h.id DESC`,
		input.WorkspaceID, input.MessageID, input.UserID, limit, offset,
	)
	if err != nil {
		return nil, fmt.Errorf("list message edit history: %w", err)
	}
	defer rows.Close()

	found := false
	history := make([]domain.MessageEditHistory, 0, limit)
	for rows.Next() {
		found = true
		var version domain.MessageEditHistory
		var versionedAt *time.Time
		if err := rows.Scan(
			&version.ID, &version.MessageID, &version.Body, (*string)(&version.BodyFormat),
			&version.EditorUserID, &versionedAt,
		); err != nil {
			return nil, fmt.Errorf("scan message edit history: %w", err)
		}
		if version.ID != "" {
			version.VersionedAt = *versionedAt
			history = append(history, version)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate message edit history: %w", err)
	}
	if !found {
		return nil, domain.ErrNotFound
	}
	return history, nil
}

func (s *PGXMessageStore) ResolveMentionLabels(ctx context.Context, workspaceID string, userIDs, channelIDs []string) (map[string]string, error) {
	labels := make(map[string]string, len(userIDs)+len(channelIDs))
	if len(userIDs)+len(channelIDs) == 0 {
		return labels, nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT 'user', u.id::text, u.display_name
		FROM unnest($2::text[]) AS ids(id)
		JOIN auth.users u
		  ON u.id = ids.id::uuid AND u.status = 'active' AND u.deleted_at IS NULL
		JOIN chat.workspace_members wm
		  ON wm.user_id = u.id AND wm.workspace_id = $1::uuid AND wm.status = 'active'
		UNION ALL
		SELECT 'channel', c.id::text, c.display_name
		FROM unnest($3::text[]) AS ids(id)
		JOIN chat.channels c
		  ON c.id = ids.id::uuid AND c.workspace_id = $1::uuid AND c.status = 'active'`,
		workspaceID, userIDs, channelIDs,
	)
	if err != nil {
		return nil, fmt.Errorf("resolve mention labels: %w", err)
	}
	defer rows.Close()
	return scanMentionLabels(rows, labels)
}

func (s *PGXMessageStore) ResolveAuthorizedMentionLabels(ctx context.Context, workspaceID, sourceChannelID, requesterID string, userIDs, channelIDs []string) (map[string]string, error) {
	labels := make(map[string]string, len(userIDs)+len(channelIDs))
	if len(userIDs)+len(channelIDs) == 0 {
		return labels, nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT 'user', u.id::text, u.display_name
		FROM unnest($4::text[]) AS ids(id)
		JOIN chat.channels source_channel
		  ON source_channel.id = $2::uuid
		 AND source_channel.workspace_id = $1::uuid
		 AND source_channel.status = 'active'
		JOIN chat.channel_members cm
		  ON cm.channel_id = source_channel.id AND cm.user_id = ids.id::uuid
		JOIN chat.workspace_members wm
		  ON wm.workspace_id = source_channel.workspace_id
		 AND wm.user_id = cm.user_id
		 AND wm.status = 'active'
		JOIN auth.users u
		  ON u.id = cm.user_id AND u.status = 'active' AND u.deleted_at IS NULL
		UNION ALL
		SELECT 'channel', c.id::text, c.display_name
		FROM unnest($5::text[]) AS ids(id)
		JOIN chat.channels c
		  ON c.id = ids.id::uuid
		 AND c.workspace_id = $1::uuid
		 AND c.status = 'active'
		JOIN chat.workspaces w
		  ON w.id = c.workspace_id AND w.status = 'active'
		JOIN chat.workspace_members requester
		  ON requester.workspace_id = c.workspace_id
		 AND requester.user_id = $3::uuid
		 AND requester.status = 'active'
		WHERE chat.channel_visible_to_user(c.id, $3::uuid)`,
		workspaceID, sourceChannelID, requesterID, userIDs, channelIDs,
	)
	if err != nil {
		return nil, fmt.Errorf("resolve authorized mention labels: %w", err)
	}
	defer rows.Close()
	return scanMentionLabels(rows, labels)
}

func scanMentionLabels(rows pgx.Rows, labels map[string]string) (map[string]string, error) {
	for rows.Next() {
		var kind, id, label string
		if err := rows.Scan(&kind, &id, &label); err != nil {
			return nil, fmt.Errorf("scan mention label: %w", err)
		}
		labels[kind+":"+id] = label
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate mention labels: %w", err)
	}
	return labels, nil
}

func (s *PGXMessageStore) ValidateRefMessageInTarget(ctx context.Context, workspaceID, channelID, dmConversationID, messageID, userID string) error {
	var exists int
	err := s.pool.QueryRow(ctx, `
		SELECT 1
		FROM chat.messages m`+messageAccessJoins("$5")+`
		WHERE m.id = $1
		  AND m.workspace_id = $2
		  AND m.status = 'active'
		  AND m.channel_id IS NOT DISTINCT FROM $3
		  AND m.dm_conversation_id IS NOT DISTINCT FROM $4
		  AND `+messageAccessPredicate,
		messageID, workspaceID, nullableUUID(channelID), nullableUUID(dmConversationID), userID,
	).Scan(&exists)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrInvalidMessageReference
		}
		return fmt.Errorf("validate ref message in target: %w", err)
	}
	return nil
}

func (s *PGXMessageStore) GetMessageByIDInWorkspace(ctx context.Context, workspaceID, messageID, userID string) (domain.Message, error) {
	// Use listMessageWithQuoteColumns so the result matches the list endpoint
	// contract: sender display info, favorite flag, and optional quote preview.
	row := s.pool.QueryRow(ctx, `
		SELECT `+listMessageWithQuoteColumns("m", "$3", "q")+`
		FROM chat.messages m`+messageAccessJoins("$3")+`
		LEFT JOIN auth.users u ON u.id = m.sender_id`+quotedMessageJoin("m", "q")+`
		WHERE m.id = $1 AND m.workspace_id = $2
		  AND `+messageAccessPredicate,
		messageID, workspaceID, userID,
	)
	msg, err := scanMessageWithSenderAndQuote(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Message{}, domain.ErrNotFound
		}
		return domain.Message{}, fmt.Errorf("get message by id in workspace: %w", err)
	}
	messages := []domain.Message{msg}
	if err := s.loadReactionBatch(ctx, messages, userID); err != nil {
		return domain.Message{}, err
	}
	return messages[0], nil
}

func (s *PGXMessageStore) ListChannelMessages(ctx context.Context, input ListChannelMessagesInput) (ListMessagesResult, error) {
	limit := resolveLimit(input.Limit)

	var rows pgx.Rows
	var err error

	if input.BeforeCursor != nil {
		// Keyset pagination: fetch messages older than the cursor.
		// Fetch limit+1 to detect whether a next page exists.
		rows, err = s.pool.Query(ctx, `
			SELECT `+listMessageWithQuoteColumns("m", "$3", "q")+`
			FROM chat.messages m
			JOIN chat.channels c
			  ON c.id = m.channel_id
			JOIN chat.workspaces w
			  ON w.id = m.workspace_id AND w.status = 'active'
			JOIN chat.workspace_members wm
			  ON wm.workspace_id = m.workspace_id AND wm.user_id = $3 AND wm.status = 'active'
			LEFT JOIN chat.channel_members cm
			  ON cm.channel_id = m.channel_id AND cm.user_id = $3
			LEFT JOIN auth.users u
			  ON u.id = m.sender_id`+quotedMessageJoin("m", "q")+`
			WHERE m.workspace_id = $1
			  AND m.channel_id = $2
			  AND c.status = 'active'
			  AND (c.type = 'public' OR cm.user_id IS NOT NULL)
			  AND (m.created_at, m.id) < ($4, $5::uuid)
			ORDER BY m.created_at DESC, m.id DESC
			LIMIT $6`,
			input.WorkspaceID, input.ChannelID, input.UserID,
			input.BeforeCursor.CreatedAt, input.BeforeCursor.ID,
			limit+1,
		)
	} else {
		rows, err = s.pool.Query(ctx, `
			SELECT `+listMessageWithQuoteColumns("m", "$3", "q")+`
			FROM chat.messages m
			JOIN chat.channels c
			  ON c.id = m.channel_id
			JOIN chat.workspaces w
			  ON w.id = m.workspace_id AND w.status = 'active'
			JOIN chat.workspace_members wm
			  ON wm.workspace_id = m.workspace_id AND wm.user_id = $3 AND wm.status = 'active'
			LEFT JOIN chat.channel_members cm
			  ON cm.channel_id = m.channel_id AND cm.user_id = $3
			LEFT JOIN auth.users u
			  ON u.id = m.sender_id`+quotedMessageJoin("m", "q")+`
			WHERE m.workspace_id = $1
			  AND m.channel_id = $2
			  AND c.status = 'active'
			  AND (c.type = 'public' OR cm.user_id IS NOT NULL)
			ORDER BY m.created_at DESC, m.id DESC
			LIMIT $4`,
			input.WorkspaceID, input.ChannelID, input.UserID,
			limit+1,
		)
	}
	if err != nil {
		return ListMessagesResult{}, fmt.Errorf("list channel messages: %w", err)
	}
	defer rows.Close()
	result, err := collectListMessagesResult(rows, limit)
	rows.Close()
	if err != nil {
		return ListMessagesResult{}, err
	}
	if err := s.loadReactionBatch(ctx, result.Messages, input.UserID); err != nil {
		return ListMessagesResult{}, err
	}
	return result, nil
}

func (s *PGXMessageStore) ListDMMessages(ctx context.Context, input ListDMMessagesInput) (ListMessagesResult, error) {
	limit := resolveLimit(input.Limit)

	var rows pgx.Rows
	var err error

	if input.BeforeCursor != nil {
		rows, err = s.pool.Query(ctx, `
			SELECT `+listMessageWithQuoteColumns("m", "$3", "q")+`
			FROM chat.messages m
			JOIN chat.dm_conversations dc
			  ON dc.id = m.dm_conversation_id
			JOIN chat.workspaces w
			  ON w.id = m.workspace_id AND w.status = 'active'
			JOIN chat.workspace_members wm
			  ON wm.workspace_id = m.workspace_id AND wm.user_id = $3 AND wm.status = 'active'
			JOIN chat.dm_members dm
			  ON dm.conversation_id = m.dm_conversation_id AND dm.user_id = $3 AND dm.status = 'active'
			LEFT JOIN auth.users u
			  ON u.id = m.sender_id`+quotedMessageJoin("m", "q")+`
			WHERE m.workspace_id = $1
			  AND m.dm_conversation_id = $2
			  AND dc.status = 'active'
			  AND (m.created_at, m.id) < ($4, $5::uuid)
			ORDER BY m.created_at DESC, m.id DESC
			LIMIT $6`,
			input.WorkspaceID, input.ConversationID, input.UserID,
			input.BeforeCursor.CreatedAt, input.BeforeCursor.ID,
			limit+1,
		)
	} else {
		rows, err = s.pool.Query(ctx, `
			SELECT `+listMessageWithQuoteColumns("m", "$3", "q")+`
			FROM chat.messages m
			JOIN chat.dm_conversations dc
			  ON dc.id = m.dm_conversation_id
			JOIN chat.workspaces w
			  ON w.id = m.workspace_id AND w.status = 'active'
			JOIN chat.workspace_members wm
			  ON wm.workspace_id = m.workspace_id AND wm.user_id = $3 AND wm.status = 'active'
			JOIN chat.dm_members dm
			  ON dm.conversation_id = m.dm_conversation_id AND dm.user_id = $3 AND dm.status = 'active'
			LEFT JOIN auth.users u
			  ON u.id = m.sender_id`+quotedMessageJoin("m", "q")+`
			WHERE m.workspace_id = $1
			  AND m.dm_conversation_id = $2
			  AND dc.status = 'active'
			ORDER BY m.created_at DESC, m.id DESC
			LIMIT $4`,
			input.WorkspaceID, input.ConversationID, input.UserID,
			limit+1,
		)
	}
	if err != nil {
		return ListMessagesResult{}, fmt.Errorf("list dm messages: %w", err)
	}
	defer rows.Close()
	result, err := collectListMessagesResult(rows, limit)
	rows.Close()
	if err != nil {
		return ListMessagesResult{}, err
	}
	if err := s.loadReactionBatch(ctx, result.Messages, input.UserID); err != nil {
		return ListMessagesResult{}, err
	}
	return result, nil
}

func (s *PGXMessageStore) loadReactionBatch(ctx context.Context, messages []domain.Message, userID string) error {
	if len(messages) == 0 {
		return nil
	}
	ids := make([]string, len(messages))
	byID := make(map[string]int, len(messages))
	for i := range messages {
		ids[i] = messages[i].ID
		byID[messages[i].ID] = i
		messages[i].Reactions = []domain.MessageReaction{}
	}
	rows, err := s.pool.Query(ctx, `
		SELECT message_id::text, emoji, count(*)::int, bool_or(user_id = $2)
		FROM chat.message_reactions
		WHERE message_id = ANY($1::uuid[])
		GROUP BY message_id, emoji
		ORDER BY message_id, min(created_at), emoji`, ids, userID)
	if err != nil {
		return fmt.Errorf("load message reactions: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var messageID string
		var reaction domain.MessageReaction
		if err := rows.Scan(&messageID, &reaction.Emoji, &reaction.Count, &reaction.ReactedByMe); err != nil {
			return fmt.Errorf("scan message reaction: %w", err)
		}
		if i, ok := byID[messageID]; ok {
			messages[i].Reactions = append(messages[i].Reactions, reaction)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate message reactions: %w", err)
	}
	return nil
}

// resolveLimit returns a valid limit value: defaults to defaultMessageLimit when
// input is 0, capped at maxMessageLimit.
func resolveLimit(n int) int {
	if n <= 0 {
		return defaultMessageLimit
	}
	if n > maxMessageLimit {
		return maxMessageLimit
	}
	return n
}

type messageRows interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
}

func collectListMessagesResult(rows messageRows, limit int) (ListMessagesResult, error) {
	return doCollectMessagesResult(rows, limit, true)
}

func doCollectMessagesResult(rows messageRows, limit int, withSender bool) (ListMessagesResult, error) {
	msgs, err := collectMessagesWithSenderAndQuote(rows, withSender)
	if err != nil {
		return ListMessagesResult{}, err
	}

	var nextCursor *MessageCursor
	if len(msgs) > limit {
		// Extra row means there is an older page. Trim to limit.
		msgs = msgs[:limit]
		// The last element (after trimming, still DESC) is the oldest we're returning.
		oldest := msgs[len(msgs)-1]
		c := MessageCursor{CreatedAt: oldest.CreatedAt, ID: oldest.ID}
		nextCursor = &c
	}

	// Reverse from DESC to ASC (oldest-first) for display.
	for i, j := 0, len(msgs)-1; i < j; i, j = i+1, j-1 {
		msgs[i], msgs[j] = msgs[j], msgs[i]
	}

	return ListMessagesResult{Messages: msgs, NextCursor: nextCursor}, nil
}

func collectMessagesWithSenderAndQuote(rows messageRows, withSender bool) ([]domain.Message, error) {
	var messages []domain.Message
	for rows.Next() {
		var msg domain.Message
		var editedAt, deletedAt *time.Time
		var quote domain.QuotedMessage
		var quoteDeletedAt, quoteCreatedAt *time.Time
		dest := []any{
			&msg.ID, &msg.WorkspaceID,
			&msg.ChannelID, &msg.DMConversationID,
			&msg.SenderID,
			(*string)(&msg.Kind), &msg.BodyText, (*string)(&msg.BodyFormat), (*string)(&msg.Status),
			&msg.ParentMessageID, &msg.ForwardedFromMessageID, &msg.ReferencedMessageID,
			&editedAt, &msg.EditCount, &deletedAt,
			&msg.CreatedAt, &msg.UpdatedAt,
		}
		if withSender {
			dest = append(dest, &msg.SenderDisplayName, &msg.SenderEmail, &msg.IsFavorited)
			dest = append(dest,
				&quote.ID, &quote.AuthorID, &quote.BodyText, (*string)(&quote.BodyFormat), (*string)(&quote.Status),
				&quoteDeletedAt, &quoteCreatedAt,
			)
		}
		err := rows.Scan(dest...)
		if err != nil {
			return nil, fmt.Errorf("scan message row: %w", err)
		}
		if editedAt != nil {
			msg.EditedAt = *editedAt
		}
		if deletedAt != nil {
			msg.DeletedAt = *deletedAt
		}
		if quote.ID != "" {
			if quoteDeletedAt != nil {
				quote.DeletedAt = *quoteDeletedAt
			}
			if quoteCreatedAt != nil {
				quote.CreatedAt = *quoteCreatedAt
			}
			msg.Quoted = &quote
		}
		messages = append(messages, msg)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate message rows: %w", err)
	}
	return messages, nil
}
