package storage

import (
	"context"
	"fmt"
	"time"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
)

const (
	SidebarPinTargetChannel = "channel"
	SidebarPinTargetDM      = "dm"
)

type SidebarPin struct {
	TargetType string
	TargetID   string
	PinnedAt   time.Time
}

// SidebarPinStore persists the caller's private sidebar ordering preference.
// Its write statement repeats the current visibility predicate, so an old
// client-side list cannot turn a revoked membership into a pin.
type SidebarPinStore interface {
	Pin(ctx context.Context, workspaceID, userID, targetType, targetID string) error
	Unpin(ctx context.Context, userID, targetType, targetID string) error
	ListVisible(ctx context.Context, workspaceID, userID string) ([]SidebarPin, error)
}

type PGXSidebarPinStore struct{ pool Pool }

func NewPGXSidebarPinStore(pool Pool) *PGXSidebarPinStore { return &PGXSidebarPinStore{pool: pool} }

func (s *PGXSidebarPinStore) Pin(ctx context.Context, workspaceID, userID, targetType, targetID string) error {
	var allowed bool
	var query string
	switch targetType {
	case SidebarPinTargetChannel:
		query = `
			WITH authorized AS (
				SELECT c.id, c.workspace_id
				FROM chat.channels c
				JOIN chat.workspaces w ON w.id = c.workspace_id AND w.status = 'active'
				JOIN chat.workspace_members wm ON wm.workspace_id = c.workspace_id AND wm.user_id = $2 AND wm.status = 'active'
				WHERE c.id = $3 AND c.workspace_id = $1 AND c.status = 'active'
				  AND chat.channel_visible_to_user(c.id, $2::uuid)
			), ins AS (
				INSERT INTO chat.sidebar_conversation_pins (user_id, workspace_id, channel_id)
				SELECT $2, workspace_id, id FROM authorized
				ON CONFLICT (user_id, channel_id) WHERE channel_id IS NOT NULL DO NOTHING
			)
			SELECT EXISTS (SELECT 1 FROM authorized)`
	case SidebarPinTargetDM:
		query = `
			WITH authorized AS (
				SELECT dc.id, dc.workspace_id
				FROM chat.dm_conversations dc
				JOIN chat.workspaces w ON w.id = dc.workspace_id AND w.status = 'active'
				JOIN chat.workspace_members wm ON wm.workspace_id = dc.workspace_id AND wm.user_id = $2 AND wm.status = 'active'
				JOIN chat.dm_members dm ON dm.conversation_id = dc.id AND dm.user_id = $2 AND dm.status = 'active'
				WHERE dc.id = $3 AND dc.workspace_id = $1 AND dc.status = 'active'
			), ins AS (
				INSERT INTO chat.sidebar_conversation_pins (user_id, workspace_id, dm_conversation_id)
				SELECT $2, workspace_id, id FROM authorized
				ON CONFLICT (user_id, dm_conversation_id) WHERE dm_conversation_id IS NOT NULL DO NOTHING
			)
			SELECT EXISTS (SELECT 1 FROM authorized)`
	default:
		return domain.ErrInvalidInput
	}
	if err := s.pool.QueryRow(ctx, query, workspaceID, userID, targetID).Scan(&allowed); err != nil {
		return fmt.Errorf("pin sidebar conversation: %w", err)
	}
	if !allowed {
		return domain.ErrNotFound
	}
	return nil
}

func (s *PGXSidebarPinStore) Unpin(ctx context.Context, userID, targetType, targetID string) error {
	var query string
	switch targetType {
	case SidebarPinTargetChannel:
		query = `DELETE FROM chat.sidebar_conversation_pins WHERE user_id = $1 AND channel_id = $2`
	case SidebarPinTargetDM:
		query = `DELETE FROM chat.sidebar_conversation_pins WHERE user_id = $1 AND dm_conversation_id = $2`
	default:
		return domain.ErrInvalidInput
	}
	if _, err := s.pool.Exec(ctx, query, userID, targetID); err != nil {
		return fmt.Errorf("unpin sidebar conversation: %w", err)
	}
	return nil
}

func (s *PGXSidebarPinStore) ListVisible(ctx context.Context, workspaceID, userID string) ([]SidebarPin, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT 'channel', p.channel_id::text, p.pinned_at
		FROM chat.sidebar_conversation_pins p
		JOIN chat.channels c ON c.id = p.channel_id AND c.workspace_id = $1 AND c.status = 'active'
		JOIN chat.workspaces w ON w.id = c.workspace_id AND w.status = 'active'
		JOIN chat.workspace_members wm ON wm.workspace_id = c.workspace_id AND wm.user_id = $2 AND wm.status = 'active'
		WHERE p.user_id = $2 AND p.workspace_id = $1 AND chat.channel_visible_to_user(c.id, $2::uuid)
		UNION ALL
		SELECT 'dm', p.dm_conversation_id::text, p.pinned_at
		FROM chat.sidebar_conversation_pins p
		JOIN chat.dm_conversations dc ON dc.id = p.dm_conversation_id AND dc.workspace_id = $1 AND dc.status = 'active'
		JOIN chat.workspaces w ON w.id = dc.workspace_id AND w.status = 'active'
		JOIN chat.workspace_members wm ON wm.workspace_id = dc.workspace_id AND wm.user_id = $2 AND wm.status = 'active'
		JOIN chat.dm_members dm ON dm.conversation_id = dc.id AND dm.user_id = $2 AND dm.status = 'active'
		WHERE p.user_id = $2 AND p.workspace_id = $1
		ORDER BY 3 ASC`, workspaceID, userID)
	if err != nil {
		return nil, fmt.Errorf("list visible sidebar pins: %w", err)
	}
	defer rows.Close()
	pins := make([]SidebarPin, 0)
	for rows.Next() {
		var pin SidebarPin
		if err := rows.Scan(&pin.TargetType, &pin.TargetID, &pin.PinnedAt); err != nil {
			return nil, fmt.Errorf("scan visible sidebar pin: %w", err)
		}
		pins = append(pins, pin)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate visible sidebar pins: %w", err)
	}
	return pins, nil
}
