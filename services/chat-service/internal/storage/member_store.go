package storage

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
)

// MemberStore is the persistence interface for workspace and channel membership.
type MemberStore interface {
	AddWorkspaceMember(ctx context.Context, workspaceID, userID string, role domain.WorkspaceRole) (domain.WorkspaceMember, error)
	GetWorkspaceMember(ctx context.Context, workspaceID, userID string) (domain.WorkspaceMember, error)
	AddChannelMember(ctx context.Context, channelID, userID string, role domain.ChannelRole) (domain.ChannelMember, error)
	GetChannelMember(ctx context.Context, channelID, userID string) (domain.ChannelMember, error)
}

// PGXMemberStore implements MemberStore using a pgx connection pool.
type PGXMemberStore struct {
	pool Pool
}

func NewPGXMemberStore(pool Pool) *PGXMemberStore {
	return &PGXMemberStore{pool: pool}
}

// AddWorkspaceMember inserts a workspace membership. Returns ErrAlreadyMember when
// ON CONFLICT DO NOTHING fires (no row returned). Callers should follow up with
// GetWorkspaceMember to retrieve the existing record if needed.
func (s *PGXMemberStore) AddWorkspaceMember(ctx context.Context, workspaceID, userID string, role domain.WorkspaceRole) (domain.WorkspaceMember, error) {
	var m domain.WorkspaceMember
	err := s.pool.QueryRow(ctx, `
		INSERT INTO chat.workspace_members (workspace_id, user_id, role, status)
		VALUES ($1, $2, $3, 'active')
		ON CONFLICT (workspace_id, user_id) DO NOTHING
		RETURNING workspace_id, user_id, role, status, joined_at`,
		workspaceID, userID, string(role),
	).Scan(&m.WorkspaceID, &m.UserID, (*string)(&m.Role), (*string)(&m.Status), &m.JoinedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.WorkspaceMember{}, domain.ErrAlreadyMember
		}
		return domain.WorkspaceMember{}, fmt.Errorf("add workspace member: %w", err)
	}
	return m, nil
}

func (s *PGXMemberStore) GetWorkspaceMember(ctx context.Context, workspaceID, userID string) (domain.WorkspaceMember, error) {
	var m domain.WorkspaceMember
	err := s.pool.QueryRow(ctx, `
		SELECT workspace_id, user_id, role, status, joined_at
		FROM chat.workspace_members
		WHERE workspace_id = $1 AND user_id = $2`,
		workspaceID, userID,
	).Scan(&m.WorkspaceID, &m.UserID, (*string)(&m.Role), (*string)(&m.Status), &m.JoinedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.WorkspaceMember{}, domain.ErrNotFound
		}
		return domain.WorkspaceMember{}, fmt.Errorf("get workspace member: %w", err)
	}
	return m, nil
}

// AddChannelMember inserts a channel membership. Returns ErrAlreadyMember when
// ON CONFLICT DO NOTHING fires (no row returned).
func (s *PGXMemberStore) AddChannelMember(ctx context.Context, channelID, userID string, role domain.ChannelRole) (domain.ChannelMember, error) {
	var m domain.ChannelMember
	err := s.pool.QueryRow(ctx, `
		INSERT INTO chat.channel_members (channel_id, user_id, role)
		VALUES ($1, $2, $3)
		ON CONFLICT (channel_id, user_id) DO NOTHING
		RETURNING channel_id, user_id, role, joined_at`,
		channelID, userID, string(role),
	).Scan(&m.ChannelID, &m.UserID, (*string)(&m.Role), &m.JoinedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ChannelMember{}, domain.ErrAlreadyMember
		}
		return domain.ChannelMember{}, fmt.Errorf("add channel member: %w", err)
	}
	return m, nil
}

func (s *PGXMemberStore) GetChannelMember(ctx context.Context, channelID, userID string) (domain.ChannelMember, error) {
	var m domain.ChannelMember
	err := s.pool.QueryRow(ctx, `
		SELECT channel_id, user_id, role, joined_at
		FROM chat.channel_members
		WHERE channel_id = $1 AND user_id = $2`,
		channelID, userID,
	).Scan(&m.ChannelID, &m.UserID, (*string)(&m.Role), &m.JoinedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ChannelMember{}, domain.ErrNotFound
		}
		return domain.ChannelMember{}, fmt.Errorf("get channel member: %w", err)
	}
	return m, nil
}
