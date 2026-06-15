package storage

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
)

// CreateDirectConversationInput holds storage-only fields for creating or
// reactivating a canonical 1:1 DM.
type CreateDirectConversationInput struct {
	WorkspaceID        string
	CreatedBy          string
	DirectPairKey      string
	ParticipantUserIDs []string
}

// CreateGroupConversationInput holds storage-only fields for group DM creation.
type CreateGroupConversationInput struct {
	WorkspaceID        string
	CreatedBy          string
	Title              string
	ParticipantUserIDs []string
}

// DMStore is the persistence interface for direct and group DM conversations.
type DMStore interface {
	CreateDirectConversation(ctx context.Context, input CreateDirectConversationInput) (domain.DMConversation, error)
	CreateGroupConversation(ctx context.Context, input CreateGroupConversationInput) (domain.DMConversation, error)
	ListVisibleConversationsByUser(ctx context.Context, workspaceID, userID string) ([]domain.DMConversation, error)
	// ListVisibleConversationsWithParticipantIDs returns active DM conversations
	// visible to userID, each annotated with the full list of active member user
	// IDs. A single SQL query (no N+1) is used to fetch participants.
	ListVisibleConversationsWithParticipantIDs(ctx context.Context, workspaceID, userID string) ([]domain.DMConversationWithParticipantIDs, error)
	GetVisibleConversationByID(ctx context.Context, workspaceID, conversationID, userID string) (domain.DMConversation, error)
}

// PGXDMStore implements DMStore using a pgx connection pool.
type PGXDMStore struct {
	pool Pool
}

func NewPGXDMStore(pool Pool) *PGXDMStore {
	return &PGXDMStore{pool: pool}
}

func (s *PGXDMStore) CreateDirectConversation(ctx context.Context, input CreateDirectConversationInput) (domain.DMConversation, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.DMConversation{}, fmt.Errorf("begin create direct conversation: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()

	conversation, err := createDirectConversation(ctx, tx, input)
	if err != nil {
		return domain.DMConversation{}, err
	}
	for _, userID := range input.ParticipantUserIDs {
		if err := upsertDMMember(ctx, tx, conversation.ID, input.WorkspaceID, userID); err != nil {
			return domain.DMConversation{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.DMConversation{}, fmt.Errorf("commit create direct conversation: %w", err)
	}
	committed = true
	return conversation, nil
}

func createDirectConversation(ctx context.Context, q dmQuerier, input CreateDirectConversationInput) (domain.DMConversation, error) {
	var conversation domain.DMConversation
	err := q.QueryRow(ctx, `
		INSERT INTO chat.dm_conversations
			(workspace_id, type, title, status, created_by, direct_pair_key)
		VALUES ($1, 'direct', NULL, 'active', $2, $3)
		ON CONFLICT (workspace_id, direct_pair_key) WHERE type = 'direct'
		DO UPDATE SET status = 'active',
		              updated_at = now()
		RETURNING id, workspace_id, type, COALESCE(title, ''), status, created_by,
		          created_at, updated_at`,
		input.WorkspaceID, input.CreatedBy, input.DirectPairKey,
	).Scan(
		&conversation.ID, &conversation.WorkspaceID, (*string)(&conversation.Type),
		&conversation.Title, (*string)(&conversation.Status), &conversation.CreatedBy,
		&conversation.CreatedAt, &conversation.UpdatedAt,
	)
	if err != nil {
		return domain.DMConversation{}, fmt.Errorf("create direct conversation: %w", err)
	}
	return conversation, nil
}

func (s *PGXDMStore) CreateGroupConversation(ctx context.Context, input CreateGroupConversationInput) (domain.DMConversation, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.DMConversation{}, fmt.Errorf("begin create group conversation: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()

	conversation, err := createGroupConversation(ctx, tx, input)
	if err != nil {
		return domain.DMConversation{}, err
	}
	for _, userID := range input.ParticipantUserIDs {
		if err := upsertDMMember(ctx, tx, conversation.ID, input.WorkspaceID, userID); err != nil {
			return domain.DMConversation{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.DMConversation{}, fmt.Errorf("commit create group conversation: %w", err)
	}
	committed = true
	return conversation, nil
}

func createGroupConversation(ctx context.Context, q dmQuerier, input CreateGroupConversationInput) (domain.DMConversation, error) {
	var title *string
	if input.Title != "" {
		title = &input.Title
	}

	var conversation domain.DMConversation
	err := q.QueryRow(ctx, `
		INSERT INTO chat.dm_conversations
			(workspace_id, type, title, status, created_by)
		VALUES ($1, 'group', $2, 'active', $3)
		RETURNING id, workspace_id, type, COALESCE(title, ''), status, created_by,
		          created_at, updated_at`,
		input.WorkspaceID, title, input.CreatedBy,
	).Scan(
		&conversation.ID, &conversation.WorkspaceID, (*string)(&conversation.Type),
		&conversation.Title, (*string)(&conversation.Status), &conversation.CreatedBy,
		&conversation.CreatedAt, &conversation.UpdatedAt,
	)
	if err != nil {
		return domain.DMConversation{}, fmt.Errorf("create group conversation: %w", err)
	}
	return conversation, nil
}

type dmQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

func upsertDMMember(ctx context.Context, q dmQuerier, conversationID, workspaceID, userID string) error {
	tag, err := q.Exec(ctx, `
		INSERT INTO chat.dm_members (conversation_id, user_id, role, status, left_at)
		SELECT $1, wm.user_id, 'member', 'active', NULL
		FROM chat.workspace_members wm
		JOIN chat.workspaces w
		  ON w.id = wm.workspace_id AND w.status = 'active'
		JOIN chat.dm_conversations dc
		  ON dc.id = $1 AND dc.workspace_id = wm.workspace_id
		WHERE wm.workspace_id = $2
		  AND wm.user_id = $3
		  AND wm.status = 'active'
		ON CONFLICT (conversation_id, user_id)
		DO UPDATE SET role = 'member',
		              status = 'active',
		              left_at = NULL`,
		conversationID, workspaceID, userID,
	)
	if err != nil {
		return fmt.Errorf("upsert dm member: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrForbidden
	}
	return nil
}

func (s *PGXDMStore) ListVisibleConversationsByUser(ctx context.Context, workspaceID, userID string) ([]domain.DMConversation, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT dc.id, dc.workspace_id, dc.type, COALESCE(dc.title, ''), dc.status,
		       dc.created_by, dc.created_at, dc.updated_at
		FROM chat.dm_conversations dc
		JOIN chat.workspaces w
		  ON w.id = dc.workspace_id AND w.status = 'active'
		JOIN chat.workspace_members wm
		  ON wm.workspace_id = dc.workspace_id AND wm.user_id = $2 AND wm.status = 'active'
		JOIN chat.dm_members dm
		  ON dm.conversation_id = dc.id AND dm.user_id = $2 AND dm.status = 'active'
		WHERE dc.workspace_id = $1
		  AND dc.status = 'active'
		ORDER BY dc.updated_at DESC, dc.created_at DESC`,
		workspaceID, userID,
	)
	if err != nil {
		return nil, fmt.Errorf("list visible dm conversations: %w", err)
	}
	defer rows.Close()

	var conversations []domain.DMConversation
	for rows.Next() {
		var conversation domain.DMConversation
		if err := rows.Scan(
			&conversation.ID, &conversation.WorkspaceID, (*string)(&conversation.Type),
			&conversation.Title, (*string)(&conversation.Status), &conversation.CreatedBy,
			&conversation.CreatedAt, &conversation.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan visible dm conversation: %w", err)
		}
		conversations = append(conversations, conversation)
	}
	return conversations, rows.Err()
}

func (s *PGXDMStore) ListVisibleConversationsWithParticipantIDs(ctx context.Context, workspaceID, userID string) ([]domain.DMConversationWithParticipantIDs, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT dc.id, dc.workspace_id, dc.type, COALESCE(dc.title, ''), dc.status,
		       dc.created_by, dc.created_at, dc.updated_at,
		       ARRAY(
		           SELECT dm2.user_id::text
		           FROM chat.dm_members dm2
		           WHERE dm2.conversation_id = dc.id AND dm2.status = 'active'
		           ORDER BY dm2.user_id
		       ) AS participant_ids
		FROM chat.dm_conversations dc
		JOIN chat.workspaces w
		  ON w.id = dc.workspace_id AND w.status = 'active'
		JOIN chat.workspace_members wm
		  ON wm.workspace_id = dc.workspace_id AND wm.user_id = $2 AND wm.status = 'active'
		JOIN chat.dm_members dm
		  ON dm.conversation_id = dc.id AND dm.user_id = $2 AND dm.status = 'active'
		WHERE dc.workspace_id = $1
		  AND dc.status = 'active'
		ORDER BY dc.updated_at DESC, dc.created_at DESC`,
		workspaceID, userID,
	)
	if err != nil {
		return nil, fmt.Errorf("list visible dm conversations with participants: %w", err)
	}
	defer rows.Close()

	var conversations []domain.DMConversationWithParticipantIDs
	for rows.Next() {
		var c domain.DMConversationWithParticipantIDs
		if err := rows.Scan(
			&c.ID, &c.WorkspaceID, (*string)(&c.Type),
			&c.Title, (*string)(&c.Status), &c.CreatedBy,
			&c.CreatedAt, &c.UpdatedAt, &c.ParticipantIDs,
		); err != nil {
			return nil, fmt.Errorf("scan visible dm conversation with participants: %w", err)
		}
		conversations = append(conversations, c)
	}
	return conversations, rows.Err()
}

func (s *PGXDMStore) GetVisibleConversationByID(ctx context.Context, workspaceID, conversationID, userID string) (domain.DMConversation, error) {
	var conversation domain.DMConversation
	err := s.pool.QueryRow(ctx, `
		SELECT dc.id, dc.workspace_id, dc.type, COALESCE(dc.title, ''), dc.status,
		       dc.created_by, dc.created_at, dc.updated_at
		FROM chat.dm_conversations dc
		JOIN chat.workspaces w
		  ON w.id = dc.workspace_id AND w.status = 'active'
		JOIN chat.workspace_members wm
		  ON wm.workspace_id = dc.workspace_id AND wm.user_id = $3 AND wm.status = 'active'
		JOIN chat.dm_members dm
		  ON dm.conversation_id = dc.id AND dm.user_id = $3 AND dm.status = 'active'
		WHERE dc.workspace_id = $1
		  AND dc.id = $2
		  AND dc.status = 'active'`,
		workspaceID, conversationID, userID,
	).Scan(
		&conversation.ID, &conversation.WorkspaceID, (*string)(&conversation.Type),
		&conversation.Title, (*string)(&conversation.Status), &conversation.CreatedBy,
		&conversation.CreatedAt, &conversation.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.DMConversation{}, domain.ErrNotFound
		}
		return domain.DMConversation{}, fmt.Errorf("get visible dm conversation: %w", err)
	}
	return conversation, nil
}
