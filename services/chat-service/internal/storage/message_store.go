package storage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
)

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
	ParentMessageID        string
	ForwardedFromMessageID string
	ReferencedMessageID    string
}

// ListChannelMessagesInput identifies the paged message list for a channel.
type ListChannelMessagesInput struct {
	WorkspaceID string
	ChannelID   string
	// UserID is the requesting caller; SQL enforces channel visibility for this user.
	UserID string
}

// ListDMMessagesInput identifies the paged message list for a DM conversation.
type ListDMMessagesInput struct {
	WorkspaceID    string
	ConversationID string
	// UserID is the requesting caller; SQL enforces DM visibility for this user.
	UserID string
}

// MessageStore is the persistence interface for message operations.
type MessageStore interface {
	// CreateMessage inserts a new message and returns the persisted record.
	// Returns ErrInvalidMessageReference when any of the optional reference fields
	// (parent, forwarded_from, referenced) do not belong to the same workspace and
	// same target as the new message. This is a non-enumerating storage backstop:
	// callers cannot determine whether the referenced message exists.
	CreateMessage(ctx context.Context, input CreateMessageInput) (domain.Message, error)

	// GetMessageByIDInWorkspace returns the message only if it belongs to workspaceID.
	// Returns ErrNotFound when the message does not exist or belongs to a different
	// workspace, preventing cross-workspace enumeration via message IDs.
	GetMessageByIDInWorkspace(ctx context.Context, workspaceID, messageID string) (domain.Message, error)

	// ValidateRefMessageInTarget checks that messageID belongs to the given workspace
	// and target (channelID or dmConversationID). Returns nil when valid.
	// Returns ErrInvalidMessageReference for any invalid case — non-enumerating:
	// missing, cross-workspace, cross-channel, and cross-DM all return the same error.
	ValidateRefMessageInTarget(ctx context.Context, workspaceID, channelID, dmConversationID, messageID string) error

	// ListChannelMessages returns messages for a channel in created_at/id order.
	// Visibility is enforced in SQL: active workspace, active workspace membership,
	// active channel, and private-channel membership are all checked in the query.
	// Returns an empty slice when the channel is not visible to UserID.
	// Current implementation returns up to 100 messages (cursor pagination is future scope).
	ListChannelMessages(ctx context.Context, input ListChannelMessagesInput) ([]domain.Message, error)

	// ListDMMessages returns messages for a DM conversation in created_at/id order.
	// Visibility is enforced in SQL: active workspace, active workspace membership,
	// active DM conversation, and active DM membership are all checked in the query.
	// Returns an empty slice when the conversation is not visible to UserID.
	// Current implementation returns up to 100 messages (cursor pagination is future scope).
	ListDMMessages(ctx context.Context, input ListDMMessagesInput) ([]domain.Message, error)
}

// PGXMessageStore implements MessageStore using a pgx connection pool.
type PGXMessageStore struct {
	pool Pool
}

// NewPGXMessageStore creates a PGXMessageStore backed by the given pool.
func NewPGXMessageStore(pool Pool) *PGXMessageStore {
	return &PGXMessageStore{pool: pool}
}

// messageColumns returns the SELECT column list for message queries.
// When alias is non-empty (e.g. "m"), columns are prefixed to avoid ambiguity
// in JOIN queries.
func messageColumns(alias string) string {
	p := ""
	if alias != "" {
		p = alias + "."
	}
	return p + `id, ` + p + `workspace_id,
	COALESCE(` + p + `channel_id::text, ''),
	COALESCE(` + p + `dm_conversation_id::text, ''),
	` + p + `sender_id,
	` + p + `kind, ` + p + `body_text, ` + p + `status,
	COALESCE(` + p + `parent_message_id::text, ''),
	COALESCE(` + p + `forwarded_from_message_id::text, ''),
	COALESCE(` + p + `referenced_message_id::text, ''),
	` + p + `edited_at, ` + p + `deleted_at,
	` + p + `created_at, ` + p + `updated_at`
}

// scanMessage reads a single message row into a domain.Message.
// It must be called with exactly the columns listed in messageSelectColumns.
func scanMessage(row pgx.Row) (domain.Message, error) {
	var msg domain.Message
	var editedAt, deletedAt *time.Time
	err := row.Scan(
		&msg.ID, &msg.WorkspaceID,
		&msg.ChannelID, &msg.DMConversationID,
		&msg.SenderID,
		(*string)(&msg.Kind), &msg.BodyText, (*string)(&msg.Status),
		&msg.ParentMessageID, &msg.ForwardedFromMessageID, &msg.ReferencedMessageID,
		&editedAt, &deletedAt,
		&msg.CreatedAt, &msg.UpdatedAt,
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
	// The CTE validates that any provided reference messages (parent, forwarded_from,
	// referenced) exist in the same workspace and the same target as the new message.
	// channel_id IS NOT DISTINCT FROM $2 and dm_conversation_id IS NOT DISTINCT FROM $3
	// enforce exact target match (including NULL equality) non-enumeratingly.
	// When invalid_refs has a row, the INSERT selects zero rows, and QueryRow returns
	// ErrNoRows, which is mapped to ErrInvalidMessageReference.
	row := s.pool.QueryRow(ctx, `
		WITH invalid_refs AS (
			SELECT 1 FROM (VALUES (1)) v(x)
			WHERE
				($7 IS NOT NULL AND NOT EXISTS (
					SELECT 1 FROM chat.messages
					WHERE id = $7
					  AND workspace_id = $1
					  AND channel_id IS NOT DISTINCT FROM $2
					  AND dm_conversation_id IS NOT DISTINCT FROM $3
				))
				OR ($8 IS NOT NULL AND NOT EXISTS (
					SELECT 1 FROM chat.messages
					WHERE id = $8
					  AND workspace_id = $1
					  AND channel_id IS NOT DISTINCT FROM $2
					  AND dm_conversation_id IS NOT DISTINCT FROM $3
				))
				OR ($9 IS NOT NULL AND NOT EXISTS (
					SELECT 1 FROM chat.messages
					WHERE id = $9
					  AND workspace_id = $1
					  AND channel_id IS NOT DISTINCT FROM $2
					  AND dm_conversation_id IS NOT DISTINCT FROM $3
				))
		)
		INSERT INTO chat.messages
			(workspace_id, channel_id, dm_conversation_id, sender_id,
			 kind, body_text, status,
			 parent_message_id, forwarded_from_message_id, referenced_message_id)
		SELECT $1, $2, $3, $4, $5, $6, 'active', $7, $8, $9
		WHERE NOT EXISTS (SELECT 1 FROM invalid_refs)
		RETURNING `+messageColumns(""),
		input.WorkspaceID,
		nullableUUID(input.ChannelID),
		nullableUUID(input.DMConversationID),
		input.SenderID,
		string(kind),
		input.BodyText,
		nullableUUID(input.ParentMessageID),
		nullableUUID(input.ForwardedFromMessageID),
		nullableUUID(input.ReferencedMessageID),
	)
	msg, err := scanMessage(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Message{}, domain.ErrInvalidMessageReference
		}
		return domain.Message{}, fmt.Errorf("create message: %w", err)
	}
	return msg, nil
}

func (s *PGXMessageStore) ValidateRefMessageInTarget(ctx context.Context, workspaceID, channelID, dmConversationID, messageID string) error {
	var exists int
	err := s.pool.QueryRow(ctx, `
		SELECT 1 FROM chat.messages
		WHERE id = $1
		  AND workspace_id = $2
		  AND channel_id IS NOT DISTINCT FROM $3
		  AND dm_conversation_id IS NOT DISTINCT FROM $4`,
		messageID, workspaceID, nullableUUID(channelID), nullableUUID(dmConversationID),
	).Scan(&exists)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrInvalidMessageReference
		}
		return fmt.Errorf("validate ref message in target: %w", err)
	}
	return nil
}

func (s *PGXMessageStore) GetMessageByIDInWorkspace(ctx context.Context, workspaceID, messageID string) (domain.Message, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT `+messageColumns("")+`
		FROM chat.messages
		WHERE id = $1 AND workspace_id = $2`,
		messageID, workspaceID,
	)
	msg, err := scanMessage(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Message{}, domain.ErrNotFound
		}
		return domain.Message{}, fmt.Errorf("get message by id in workspace: %w", err)
	}
	return msg, nil
}

func (s *PGXMessageStore) ListChannelMessages(ctx context.Context, input ListChannelMessagesInput) ([]domain.Message, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+messageColumns("m")+`
		FROM chat.messages m
		JOIN chat.channels c
		  ON c.id = m.channel_id
		JOIN chat.workspaces w
		  ON w.id = m.workspace_id AND w.status = 'active'
		JOIN chat.workspace_members wm
		  ON wm.workspace_id = m.workspace_id AND wm.user_id = $3 AND wm.status = 'active'
		LEFT JOIN chat.channel_members cm
		  ON cm.channel_id = m.channel_id AND cm.user_id = $3
		WHERE m.workspace_id = $1
		  AND m.channel_id = $2
		  AND c.status = 'active'
		  AND (c.type = 'public' OR cm.user_id IS NOT NULL)
		ORDER BY m.created_at ASC, m.id ASC
		LIMIT 100`,
		input.WorkspaceID, input.ChannelID, input.UserID,
	)
	if err != nil {
		return nil, fmt.Errorf("list channel messages: %w", err)
	}
	defer rows.Close()
	return collectMessages(rows)
}

func (s *PGXMessageStore) ListDMMessages(ctx context.Context, input ListDMMessagesInput) ([]domain.Message, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+messageColumns("m")+`
		FROM chat.messages m
		JOIN chat.dm_conversations dc
		  ON dc.id = m.dm_conversation_id
		JOIN chat.workspaces w
		  ON w.id = m.workspace_id AND w.status = 'active'
		JOIN chat.workspace_members wm
		  ON wm.workspace_id = m.workspace_id AND wm.user_id = $3 AND wm.status = 'active'
		JOIN chat.dm_members dm
		  ON dm.conversation_id = m.dm_conversation_id AND dm.user_id = $3 AND dm.status = 'active'
		WHERE m.workspace_id = $1
		  AND m.dm_conversation_id = $2
		  AND dc.status = 'active'
		ORDER BY m.created_at ASC, m.id ASC
		LIMIT 100`,
		input.WorkspaceID, input.ConversationID, input.UserID,
	)
	if err != nil {
		return nil, fmt.Errorf("list dm messages: %w", err)
	}
	defer rows.Close()
	return collectMessages(rows)
}

type messageRows interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
}

func collectMessages(rows messageRows) ([]domain.Message, error) {
	var messages []domain.Message
	for rows.Next() {
		var msg domain.Message
		var editedAt, deletedAt *time.Time
		err := rows.Scan(
			&msg.ID, &msg.WorkspaceID,
			&msg.ChannelID, &msg.DMConversationID,
			&msg.SenderID,
			(*string)(&msg.Kind), &msg.BodyText, (*string)(&msg.Status),
			&msg.ParentMessageID, &msg.ForwardedFromMessageID, &msg.ReferencedMessageID,
			&editedAt, &deletedAt,
			&msg.CreatedAt, &msg.UpdatedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("scan message row: %w", err)
		}
		if editedAt != nil {
			msg.EditedAt = *editedAt
		}
		if deletedAt != nil {
			msg.DeletedAt = *deletedAt
		}
		messages = append(messages, msg)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate message rows: %w", err)
	}
	return messages, nil
}
