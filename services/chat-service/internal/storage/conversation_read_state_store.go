package storage

import (
	"context"
	"fmt"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
)

const (
	ConversationReadTargetChannel = "channel"
	ConversationReadTargetDM      = "dm"
)

type ConversationReadStateStore interface {
	MarkRead(ctx context.Context, workspaceID, userID, targetType, targetID string, lastReadMessageID *string) error
	UnreadCounts(ctx context.Context, workspaceID, userID string) (map[string]int, error)
}

type PGXConversationReadStateStore struct{ pool Pool }

func NewPGXConversationReadStateStore(pool Pool) *PGXConversationReadStateStore {
	return &PGXConversationReadStateStore{pool: pool}
}

func (s *PGXConversationReadStateStore) MarkRead(ctx context.Context, workspaceID, userID, targetType, targetID string, lastReadMessageID *string) error {
	var query string
	switch targetType {
	case ConversationReadTargetChannel:
		query = `
			WITH authorized AS (
				SELECT c.id, c.workspace_id
				FROM chat.channels c
				JOIN chat.workspaces w ON w.id = c.workspace_id AND w.status = 'active'
				JOIN chat.workspace_members wm ON wm.workspace_id = c.workspace_id AND wm.user_id = $2 AND wm.status = 'active'
				WHERE c.id = $3 AND c.workspace_id = $1 AND c.status = 'active'
				  AND chat.channel_visible_to_user(c.id, $2::uuid)
			), ins AS (
				INSERT INTO chat.conversation_read_state
					(user_id, workspace_id, channel_id, last_read_message_id, last_read_at)
				SELECT $2, workspace_id, id, $4, now() FROM authorized
				ON CONFLICT (user_id, channel_id) WHERE channel_id IS NOT NULL DO UPDATE
				SET last_read_message_id = EXCLUDED.last_read_message_id,
					last_read_at = EXCLUDED.last_read_at,
					updated_at = EXCLUDED.last_read_at
				WHERE chat.conversation_read_state.last_read_at <= EXCLUDED.last_read_at
			)
			SELECT EXISTS (SELECT 1 FROM authorized)`
	case ConversationReadTargetDM:
		query = `
			WITH authorized AS (
				SELECT dc.id, dc.workspace_id
				FROM chat.dm_conversations dc
				JOIN chat.workspaces w ON w.id = dc.workspace_id AND w.status = 'active'
				JOIN chat.workspace_members wm ON wm.workspace_id = dc.workspace_id AND wm.user_id = $2 AND wm.status = 'active'
				JOIN chat.dm_members dm ON dm.conversation_id = dc.id AND dm.user_id = $2 AND dm.status = 'active'
				WHERE dc.id = $3 AND dc.workspace_id = $1 AND dc.status = 'active'
			), ins AS (
				INSERT INTO chat.conversation_read_state
					(user_id, workspace_id, dm_conversation_id, last_read_message_id, last_read_at)
				SELECT $2, workspace_id, id, $4, now() FROM authorized
				ON CONFLICT (user_id, dm_conversation_id) WHERE dm_conversation_id IS NOT NULL DO UPDATE
				SET last_read_message_id = EXCLUDED.last_read_message_id,
					last_read_at = EXCLUDED.last_read_at,
					updated_at = EXCLUDED.last_read_at
				WHERE chat.conversation_read_state.last_read_at <= EXCLUDED.last_read_at
			)
			SELECT EXISTS (SELECT 1 FROM authorized)`
	default:
		return domain.ErrInvalidInput
	}
	var allowed bool
	if err := s.pool.QueryRow(ctx, query, workspaceID, userID, targetID, lastReadMessageID).Scan(&allowed); err != nil {
		return fmt.Errorf("mark conversation read: %w", err)
	}
	if !allowed {
		return domain.ErrNotFound
	}
	return nil
}

func (s *PGXConversationReadStateStore) UnreadCounts(ctx context.Context, workspaceID, userID string) (map[string]int, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT 'channel', c.id::text,
			(SELECT COUNT(*) FROM chat.messages m
			 WHERE m.workspace_id = c.workspace_id AND m.channel_id = c.id
			   AND m.status = 'active' AND m.sender_id <> $2
			   AND m.created_at > COALESCE(rs.last_read_at, '-infinity'::timestamptz))
		FROM chat.channels c
		JOIN chat.workspaces w ON w.id = c.workspace_id AND w.status = 'active'
		JOIN chat.workspace_members wm ON wm.workspace_id = c.workspace_id AND wm.user_id = $2 AND wm.status = 'active'
		LEFT JOIN chat.conversation_read_state rs ON rs.user_id = $2 AND rs.workspace_id = $1 AND rs.channel_id = c.id
		WHERE c.workspace_id = $1 AND c.status = 'active' AND chat.channel_visible_to_user(c.id, $2::uuid)
		UNION ALL
		SELECT 'dm', dc.id::text,
			(SELECT COUNT(*) FROM chat.messages m
			 WHERE m.workspace_id = dc.workspace_id AND m.dm_conversation_id = dc.id
			   AND m.status = 'active' AND m.sender_id <> $2
			   AND m.created_at > COALESCE(rs.last_read_at, '-infinity'::timestamptz))
		FROM chat.dm_conversations dc
		JOIN chat.workspaces w ON w.id = dc.workspace_id AND w.status = 'active'
		JOIN chat.workspace_members wm ON wm.workspace_id = dc.workspace_id AND wm.user_id = $2 AND wm.status = 'active'
		JOIN chat.dm_members dm ON dm.conversation_id = dc.id AND dm.user_id = $2 AND dm.status = 'active'
		LEFT JOIN chat.conversation_read_state rs ON rs.user_id = $2 AND rs.workspace_id = $1 AND rs.dm_conversation_id = dc.id
		WHERE dc.workspace_id = $1 AND dc.status = 'active'`, workspaceID, userID)
	if err != nil {
		return nil, fmt.Errorf("list conversation unread counts: %w", err)
	}
	defer rows.Close()
	counts := make(map[string]int)
	for rows.Next() {
		var targetType, targetID string
		var count int64
		if err := rows.Scan(&targetType, &targetID, &count); err != nil {
			return nil, fmt.Errorf("scan conversation unread count: %w", err)
		}
		counts[targetType+"\x00"+targetID] = int(count)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate conversation unread counts: %w", err)
	}
	return counts, nil
}
