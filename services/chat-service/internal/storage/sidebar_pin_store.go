package storage

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
)

type SidebarConversationPin struct {
	ConversationType string
	ConversationID   string
	PinnedAt         time.Time
}

type AddSidebarConversationPinInput struct {
	WorkspaceID, UserID, ConversationType, ConversationID string
}

type SidebarPinStore interface {
	Add(context.Context, AddSidebarConversationPinInput) (SidebarConversationPin, error)
	Remove(context.Context, string, string, string, string) error
	List(context.Context, string, string) ([]SidebarConversationPin, error)
}

type PGXSidebarPinStore struct{ pool Pool }

func NewPGXSidebarPinStore(pool Pool) *PGXSidebarPinStore { return &PGXSidebarPinStore{pool: pool} }

func (s *PGXSidebarPinStore) Add(ctx context.Context, in AddSidebarConversationPinInput) (SidebarConversationPin, error) {
	var pin SidebarConversationPin
	err := s.pool.QueryRow(ctx, `
		WITH eligible AS (
		  SELECT c.id FROM chat.channels c
		  JOIN chat.workspaces w ON w.id=c.workspace_id AND w.status='active'
		  JOIN chat.workspace_members wm ON wm.workspace_id=c.workspace_id AND wm.user_id=$2 AND wm.status='active'
		  LEFT JOIN chat.channel_members cm ON cm.channel_id=c.id AND cm.user_id=$2
		  WHERE $3='channel' AND c.id=$4 AND c.workspace_id=$1 AND c.status='active'
		    AND (c.is_general OR c.type='public' OR cm.channel_id IS NOT NULL)
		  UNION ALL
		  SELECT dc.id FROM chat.dm_conversations dc
		  JOIN chat.workspaces w ON w.id=dc.workspace_id AND w.status='active'
		  JOIN chat.workspace_members wm ON wm.workspace_id=dc.workspace_id AND wm.user_id=$2 AND wm.status='active'
		  JOIN chat.dm_members dm ON dm.conversation_id=dc.id AND dm.user_id=$2 AND dm.status='active'
		  WHERE $3='dm' AND dc.id=$4 AND dc.workspace_id=$1 AND dc.status='active'
		), inserted AS (
		  INSERT INTO chat.sidebar_conversation_pins(user_id,workspace_id,conversation_type,conversation_id)
		  SELECT $2,$1,$3,id FROM eligible
		  ON CONFLICT (user_id,workspace_id,conversation_type,conversation_id)
		  DO UPDATE SET pinned_at=chat.sidebar_conversation_pins.pinned_at
		  RETURNING conversation_type, conversation_id, pinned_at
		) SELECT conversation_type, conversation_id, pinned_at FROM inserted`,
		in.WorkspaceID, in.UserID, in.ConversationType, in.ConversationID,
	).Scan(&pin.ConversationType, &pin.ConversationID, &pin.PinnedAt)
	if err == pgx.ErrNoRows {
		return pin, domain.ErrNotFound
	}
	if err != nil {
		return pin, fmt.Errorf("add sidebar pin: %w", err)
	}
	return pin, nil
}

func (s *PGXSidebarPinStore) Remove(ctx context.Context, workspaceID, userID, conversationType, conversationID string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM chat.sidebar_conversation_pins WHERE workspace_id=$1 AND user_id=$2 AND conversation_type=$3 AND conversation_id=$4`, workspaceID, userID, conversationType, conversationID)
	if err != nil {
		return fmt.Errorf("remove sidebar pin: %w", err)
	}
	return nil
}

func (s *PGXSidebarPinStore) List(ctx context.Context, workspaceID, userID string) ([]SidebarConversationPin, error) {
	rows, err := s.pool.Query(ctx, `SELECT conversation_type, conversation_id, pinned_at FROM chat.sidebar_conversation_pins WHERE workspace_id=$1 AND user_id=$2 ORDER BY pinned_at, conversation_id`, workspaceID, userID)
	if err != nil {
		return nil, fmt.Errorf("list sidebar pins: %w", err)
	}
	defer rows.Close()
	result := make([]SidebarConversationPin, 0)
	for rows.Next() {
		var p SidebarConversationPin
		if err := rows.Scan(&p.ConversationType, &p.ConversationID, &p.PinnedAt); err != nil {
			return nil, fmt.Errorf("scan sidebar pin: %w", err)
		}
		result = append(result, p)
	}
	return result, rows.Err()
}
