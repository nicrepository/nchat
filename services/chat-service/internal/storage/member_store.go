package storage

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
)

// MemberStore is the persistence interface for workspace and channel membership.
type MemberStore interface {
	// AddWorkspaceMember inserts an active workspace membership and syncs #geral.
	// ErrAlreadyMember may be returned after a successful commit; the call may
	// have repaired missing #geral membership before returning it. Callers must
	// not treat ErrAlreadyMember as proof that no side effects occurred.
	AddWorkspaceMember(ctx context.Context, workspaceID, userID string, role domain.WorkspaceRole) (domain.WorkspaceMember, error)
	ActivateWorkspaceMember(ctx context.Context, workspaceID, userID string) (domain.WorkspaceMember, error)
	GetWorkspaceMember(ctx context.Context, workspaceID, userID string) (domain.WorkspaceMember, error)
	GetEligibleDMMember(ctx context.Context, workspaceID, userID string) (domain.WorkspaceMember, error)
	AddChannelMember(ctx context.Context, channelID, userID string, role domain.ChannelRole) (domain.ChannelMember, error)
	GetChannelMember(ctx context.Context, channelID, userID string) (domain.ChannelMember, error)
	SearchChannelMembers(ctx context.Context, workspaceID, channelID, prefix string, limit int) ([]domain.MentionCandidate, error)
	SearchDMCandidates(ctx context.Context, workspaceID, callerID, prefix string, limit int) ([]domain.DMCandidate, error)
	// RemoveChannelMember deletes the channel membership for userID in channelID, scoped to
	// workspaceID. Returns ErrCannotLeaveGeneralChannel if the channel has is_general=true.
	// Returns nil when the membership does not exist (idempotent).
	RemoveChannelMember(ctx context.Context, workspaceID, channelID, userID string) error
	EnsureGeneralMembership(ctx context.Context, workspaceID, userID string) error
	SyncGeneralMemberships(ctx context.Context, workspaceID string) (int64, error)
}

// PGXMemberStore implements MemberStore using a pgx connection pool.
type PGXMemberStore struct {
	pool Pool
}

func NewPGXMemberStore(pool Pool) *PGXMemberStore {
	return &PGXMemberStore{pool: pool}
}

type memberQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// AddWorkspaceMember inserts an active workspace membership and atomically syncs
// the user to that workspace's #geral channel. Existing active members are also
// synced idempotently before ErrAlreadyMember is returned. ErrAlreadyMember may
// be returned after a successful commit; callers must not assume it means
// rollback or no side effects.
func (s *PGXMemberStore) AddWorkspaceMember(ctx context.Context, workspaceID, userID string, role domain.WorkspaceRole) (domain.WorkspaceMember, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.WorkspaceMember{}, fmt.Errorf("begin add workspace member: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()

	if err := ensureWorkspaceActive(ctx, tx, workspaceID); err != nil {
		return domain.WorkspaceMember{}, err
	}

	m, inserted, err := addWorkspaceMember(ctx, tx, workspaceID, userID, role)
	if err != nil {
		return domain.WorkspaceMember{}, err
	}
	if m.Status == domain.MemberStatusActive {
		if err := ensureGeneralMembership(ctx, tx, workspaceID, userID); err != nil {
			return domain.WorkspaceMember{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.WorkspaceMember{}, fmt.Errorf("commit add workspace member: %w", err)
	}
	committed = true
	if !inserted {
		return domain.WorkspaceMember{}, domain.ErrAlreadyMember
	}
	return m, nil
}

func addWorkspaceMember(ctx context.Context, q memberQuerier, workspaceID, userID string, role domain.WorkspaceRole) (domain.WorkspaceMember, bool, error) {
	var m domain.WorkspaceMember
	err := q.QueryRow(ctx, `
		INSERT INTO chat.workspace_members (workspace_id, user_id, role, status)
		VALUES ($1, $2, $3, 'active')
		ON CONFLICT (workspace_id, user_id) DO NOTHING
		RETURNING workspace_id, user_id, role, status, joined_at`,
		workspaceID, userID, string(role),
	).Scan(&m.WorkspaceID, &m.UserID, (*string)(&m.Role), (*string)(&m.Status), &m.JoinedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			existing, getErr := getWorkspaceMember(ctx, q, workspaceID, userID)
			if getErr != nil {
				return domain.WorkspaceMember{}, false, getErr
			}
			return existing, false, nil
		}
		return domain.WorkspaceMember{}, false, fmt.Errorf("add workspace member: %w", err)
	}
	return m, true, nil
}

// ActivateWorkspaceMember marks an existing workspace member active and
// atomically syncs them to that workspace's #geral channel.
func (s *PGXMemberStore) ActivateWorkspaceMember(ctx context.Context, workspaceID, userID string) (domain.WorkspaceMember, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.WorkspaceMember{}, fmt.Errorf("begin activate workspace member: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()

	if err := ensureWorkspaceActive(ctx, tx, workspaceID); err != nil {
		return domain.WorkspaceMember{}, err
	}

	var m domain.WorkspaceMember
	err = tx.QueryRow(ctx, `
		UPDATE chat.workspace_members
		SET status = 'active'
		WHERE workspace_id = $1 AND user_id = $2
		RETURNING workspace_id, user_id, role, status, joined_at`,
		workspaceID, userID,
	).Scan(&m.WorkspaceID, &m.UserID, (*string)(&m.Role), (*string)(&m.Status), &m.JoinedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.WorkspaceMember{}, domain.ErrNotFound
		}
		return domain.WorkspaceMember{}, fmt.Errorf("activate workspace member: %w", err)
	}
	if err := ensureGeneralMembership(ctx, tx, workspaceID, userID); err != nil {
		return domain.WorkspaceMember{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.WorkspaceMember{}, fmt.Errorf("commit activate workspace member: %w", err)
	}
	committed = true
	return m, nil
}

func (s *PGXMemberStore) GetWorkspaceMember(ctx context.Context, workspaceID, userID string) (domain.WorkspaceMember, error) {
	return getWorkspaceMember(ctx, s.pool, workspaceID, userID)
}

func getWorkspaceMember(ctx context.Context, q memberQuerier, workspaceID, userID string) (domain.WorkspaceMember, error) {
	var m domain.WorkspaceMember
	err := q.QueryRow(ctx, `
		SELECT wm.workspace_id, wm.user_id, wm.role, wm.status, wm.joined_at
		FROM chat.workspace_members wm
		JOIN chat.workspaces w ON wm.workspace_id = w.id AND w.status = 'active'
		WHERE wm.workspace_id = $1 AND wm.user_id = $2`,
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

// EnsureGeneralMembership idempotently adds an active workspace member to that
// workspace's #geral channel. Suspended and left members return
// ErrMemberInactive and are not inserted.
func (s *PGXMemberStore) EnsureGeneralMembership(ctx context.Context, workspaceID, userID string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin ensure general membership: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()

	if err := ensureWorkspaceActive(ctx, tx, workspaceID); err != nil {
		return err
	}
	member, err := getWorkspaceMember(ctx, tx, workspaceID, userID)
	if errors.Is(err, domain.ErrNotFound) {
		return domain.ErrForbidden
	}
	if err != nil {
		return err
	}
	if member.Status != domain.MemberStatusActive {
		return domain.ErrMemberInactive
	}
	if err := ensureGeneralMembership(ctx, tx, workspaceID, userID); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit ensure general membership: %w", err)
	}
	committed = true
	return nil
}

// SyncGeneralMemberships backfills missing #geral memberships for active
// workspace members only. It returns the number of inserted channel_members rows.
func (s *PGXMemberStore) SyncGeneralMemberships(ctx context.Context, workspaceID string) (int64, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin sync general memberships: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()

	if err := ensureWorkspaceActive(ctx, tx, workspaceID); err != nil {
		return 0, err
	}
	generalChannelID, err := getGeneralChannelID(ctx, tx, workspaceID)
	if err != nil {
		return 0, err
	}
	tag, err := tx.Exec(ctx, `
		INSERT INTO chat.channel_members (channel_id, user_id, role)
		SELECT $1, wm.user_id, $3
		FROM chat.workspace_members wm
		WHERE wm.workspace_id = $2
		  AND wm.status = 'active'
		ON CONFLICT (channel_id, user_id) DO NOTHING`,
		generalChannelID, workspaceID, string(domain.ChannelRoleMember),
	)
	if err != nil {
		return 0, fmt.Errorf("sync general memberships: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit sync general memberships: %w", err)
	}
	committed = true
	return tag.RowsAffected(), nil
}

func ensureWorkspaceActive(ctx context.Context, q memberQuerier, workspaceID string) error {
	var status domain.WorkspaceStatus
	err := q.QueryRow(ctx, `
		SELECT status
		FROM chat.workspaces
		WHERE id = $1
		FOR SHARE`,
		workspaceID,
	).Scan((*string)(&status))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrNotFound
		}
		return fmt.Errorf("get workspace status: %w", err)
	}
	if status != domain.WorkspaceStatusActive {
		return domain.ErrForbidden
	}
	return nil
}

func ensureGeneralMembership(ctx context.Context, q memberQuerier, workspaceID, userID string) error {
	generalChannelID, err := getGeneralChannelID(ctx, q, workspaceID)
	if err != nil {
		return err
	}
	if err := addGeneralChannelMember(ctx, q, generalChannelID, userID); err != nil {
		return err
	}
	return nil
}

func getGeneralChannelID(ctx context.Context, q memberQuerier, workspaceID string) (string, error) {
	var channelID string
	err := q.QueryRow(ctx, `
		SELECT id
		FROM chat.channels
		WHERE workspace_id = $1
		  AND is_general = true
		  AND type = 'public'
		  AND status = 'active'
		FOR SHARE`,
		workspaceID,
	).Scan(&channelID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", domain.ErrGeneralChannelMissing
		}
		return "", fmt.Errorf("get general channel: %w", err)
	}
	return channelID, nil
}

func addGeneralChannelMember(ctx context.Context, q memberQuerier, channelID, userID string) error {
	_, err := q.Exec(ctx, `
		INSERT INTO chat.channel_members (channel_id, user_id, role)
		VALUES ($1, $2, $3)
		ON CONFLICT (channel_id, user_id) DO NOTHING`,
		channelID, userID, string(domain.ChannelRoleMember),
	)
	if err != nil {
		return fmt.Errorf("add general channel member: %w", err)
	}
	return nil
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

func (s *PGXMemberStore) SearchChannelMembers(ctx context.Context, workspaceID, channelID, prefix string, limit int) ([]domain.MentionCandidate, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT u.id::text, u.display_name
		FROM chat.channel_members cm
		JOIN chat.channels c
		  ON c.id = cm.channel_id
		 AND c.workspace_id = $1::uuid
		 AND c.status = 'active'
		JOIN chat.workspace_members wm
		  ON wm.workspace_id = c.workspace_id
		 AND wm.user_id = cm.user_id
		 AND wm.status = 'active'
		JOIN auth.users u ON u.id = cm.user_id AND u.status = 'active' AND u.deleted_at IS NULL
		WHERE cm.channel_id = $2::uuid
		  AND left(lower(u.display_name), length($3)) = lower($3)
		ORDER BY lower(u.display_name), u.id
		LIMIT $4`, workspaceID, channelID, prefix, limit)
	if err != nil {
		return nil, fmt.Errorf("search channel members: %w", err)
	}
	defer rows.Close()
	results := make([]domain.MentionCandidate, 0, limit)
	for rows.Next() {
		var candidate domain.MentionCandidate
		candidate.Type = domain.MentionTypeUser
		if err := rows.Scan(&candidate.ID, &candidate.Label); err != nil {
			return nil, fmt.Errorf("scan channel member mention: %w", err)
		}
		results = append(results, candidate)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate channel member mentions: %w", err)
	}
	return results, nil
}

func (s *PGXMemberStore) SearchDMCandidates(ctx context.Context, workspaceID, callerID, prefix string, limit int) ([]domain.DMCandidate, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT u.id::text, u.display_name
		FROM chat.workspace_members wm
		JOIN chat.workspaces w
		  ON w.id = wm.workspace_id AND w.status = 'active'
		JOIN auth.users u
		  ON u.id = wm.user_id AND u.status = 'active' AND u.deleted_at IS NULL
		WHERE wm.workspace_id = $1::uuid
		  AND wm.status = 'active'
		  AND wm.user_id <> $2::uuid
		  AND left(lower(u.display_name), length($3)) = lower($3)
		  AND EXISTS (
		      SELECT 1
		      FROM chat.workspace_members caller
		      WHERE caller.workspace_id = wm.workspace_id
		        AND caller.user_id = $2::uuid
		        AND caller.status = 'active'
		  )
		ORDER BY lower(u.display_name), u.id
		LIMIT $4`, workspaceID, callerID, prefix, limit)
	if err != nil {
		return nil, fmt.Errorf("search dm candidates: %w", err)
	}
	defer rows.Close()

	results := make([]domain.DMCandidate, 0, limit)
	for rows.Next() {
		var candidate domain.DMCandidate
		if err := rows.Scan(&candidate.UserID, &candidate.DisplayName); err != nil {
			return nil, fmt.Errorf("scan dm candidate: %w", err)
		}
		results = append(results, candidate)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate dm candidates: %w", err)
	}
	return results, nil
}

func (s *PGXMemberStore) GetEligibleDMMember(ctx context.Context, workspaceID, userID string) (domain.WorkspaceMember, error) {
	var member domain.WorkspaceMember
	err := s.pool.QueryRow(ctx, `
		SELECT wm.workspace_id, wm.user_id, wm.role, wm.status, wm.joined_at
		FROM chat.workspace_members wm
		JOIN chat.workspaces w
		  ON w.id = wm.workspace_id AND w.status = 'active'
		JOIN auth.users u
		  ON u.id = wm.user_id AND u.status = 'active' AND u.deleted_at IS NULL
		WHERE wm.workspace_id = $1::uuid
		  AND wm.user_id = $2::uuid
		  AND wm.status = 'active'`, workspaceID, userID).Scan(
		&member.WorkspaceID, &member.UserID, (*string)(&member.Role), (*string)(&member.Status), &member.JoinedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.WorkspaceMember{}, domain.ErrNotFound
		}
		return domain.WorkspaceMember{}, fmt.Errorf("get eligible dm member: %w", err)
	}
	return member, nil
}

// RemoveChannelMember deletes a channel membership, scoped to workspaceID.
// Checks is_general first and returns ErrCannotLeaveGeneralChannel to prevent
// bypassing the service-level guard via direct storage calls.
// Returns nil when the channel is not in the workspace or the user is not a member.
func (s *PGXMemberStore) RemoveChannelMember(ctx context.Context, workspaceID, channelID, userID string) error {
	var isGeneral bool
	err := s.pool.QueryRow(ctx, `
		SELECT is_general FROM chat.channels
		WHERE id = $1 AND workspace_id = $2`,
		channelID, workspaceID,
	).Scan(&isGeneral)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return fmt.Errorf("check channel for remove: %w", err)
	}
	if isGeneral {
		return domain.ErrCannotLeaveGeneralChannel
	}
	if _, err := s.pool.Exec(ctx, `
		DELETE FROM chat.channel_members
		WHERE channel_id = $1 AND user_id = $2`,
		channelID, userID,
	); err != nil {
		return fmt.Errorf("remove channel member: %w", err)
	}
	return nil
}
