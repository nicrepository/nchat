package storage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
)

// CreateCategoryInput holds the fields for creating a channel category.
type CreateCategoryInput struct {
	WorkspaceID string
	Name        string
	Position    int
}

// CreateChannelInput holds the fields for creating a channel.
// CategoryID and CreatedBy are optional (empty string = NULL).
type CreateChannelInput struct {
	WorkspaceID string
	CategoryID  string
	Slug        string
	DisplayName string
	Type        domain.ChannelType
	IsGeneral   bool
	Position    int
	CreatedBy   string
	// EnsureCreatorMemberRole, when non-empty, adds CreatedBy to
	// chat.channel_members in the same transaction as the channel insert, the
	// way UpdateChannelInput.EnsureMemberUserID does for a public→private
	// switch. Private channels need it so the creator can see what they made;
	// public ones do not. Honoured by CreateChannelForActiveMember only.
	EnsureCreatorMemberRole domain.ChannelRole
}

// UpdateChannelInput holds the complete mutable channel state to persist.
// CategoryID and EnsureMemberUserID are optional (empty string = NULL / disabled).
type UpdateChannelInput struct {
	WorkspaceID        string
	ChannelID          string
	CategoryID         string
	Slug               string
	DisplayName        string
	Type               domain.ChannelType
	Position           int
	EnsureMemberUserID string
}

// VisibleChannelAccess carries the membership used to evaluate channel policy.
//
// LastMessageAt is the created_at of the channel's newest message, or nil when
// it has none (issue #414) — the same activity instant, with the same
// guarantees, that domain.DMConversationWithParticipantIDs documents for
// conversations.
type VisibleChannelAccess struct {
	Channel       domain.Channel
	ChannelMember *domain.ChannelMember
	LastMessageAt *time.Time
}

// ChannelStore is the persistence interface for channel operations.
type ChannelStore interface {
	CreateCategory(ctx context.Context, input CreateCategoryInput) (domain.ChannelCategory, error)
	CreateChannel(ctx context.Context, input CreateChannelInput) (domain.Channel, error)
	// CreateChannelForActiveMember creates a channel on behalf of input.CreatedBy,
	// serialising the authorization decision with the write itself.
	//
	// Returns domain.ErrForbidden — without saying which condition failed — when
	// the workspace is not active, or input.CreatedBy has no active membership in
	// it at the moment of the INSERT.
	CreateChannelForActiveMember(ctx context.Context, input CreateChannelInput) (domain.Channel, error)
	GetCategoryByIDInWorkspace(ctx context.Context, workspaceID, id string) (domain.ChannelCategory, error)
	GetChannelByID(ctx context.Context, id string) (domain.Channel, error)
	// GetChannelByIDInWorkspace returns the channel only if it belongs to workspaceID.
	// Returns ErrNotFound when the channel does not exist or belongs to a different workspace.
	GetChannelByIDInWorkspace(ctx context.Context, workspaceID, id string) (domain.Channel, error)
	GetVisibleChannelByID(ctx context.Context, workspaceID, channelID, userID string) (domain.Channel, error)
	GetVisibleChannelBySlug(ctx context.Context, workspaceID, slug, userID string) (domain.Channel, error)
	ListChannelsByWorkspace(ctx context.Context, workspaceID string) ([]domain.Channel, error)
	// ListVisibleChannelsByUser returns active channels in workspaceID visible to userID.
	// Visibility is enforced in SQL: active workspace + active workspace membership required;
	// public and general channels are always included; private channels require channel membership.
	// Returns an empty slice when the workspace is disabled or userID is not an active member.
	ListVisibleChannelsByUser(ctx context.Context, workspaceID, userID string) ([]domain.Channel, error)
	UpdateChannel(ctx context.Context, input UpdateChannelInput) (domain.Channel, error)
	ArchiveChannel(ctx context.Context, workspaceID, channelID string) (domain.Channel, error)
}

// PGXChannelStore implements ChannelStore using a pgx connection pool.
type PGXChannelStore struct {
	pool Pool
}

func NewPGXChannelStore(pool Pool) *PGXChannelStore {
	return &PGXChannelStore{pool: pool}
}

func (s *PGXChannelStore) CreateCategory(ctx context.Context, input CreateCategoryInput) (domain.ChannelCategory, error) {
	var c domain.ChannelCategory
	err := s.pool.QueryRow(ctx, `
		INSERT INTO chat.channel_categories (workspace_id, name, position)
		VALUES ($1, $2, $3)
		RETURNING id, workspace_id, name, position, created_at, updated_at`,
		input.WorkspaceID, input.Name, input.Position,
	).Scan(&c.ID, &c.WorkspaceID, &c.Name, &c.Position, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return domain.ChannelCategory{}, fmt.Errorf("create category: %w", err)
	}
	return c, nil
}

func (s *PGXChannelStore) CreateChannel(ctx context.Context, input CreateChannelInput) (domain.Channel, error) {
	return createChannel(ctx, s.pool, input)
}

// CreateChannelForActiveMember is the authorization-bearing creation path used
// by ChannelService (BUG #393).
//
// Channel creation takes an active membership in an active workspace, and both
// are mutable: checking them and then inserting are two steps, and a membership
// revoked in between would still get its channel. Here the check and the insert
// are one statement — the INSERT draws its rows from an authorized context that
// locks the workspace and the membership, so there is nothing to interleave.
//
// The locks are FOR SHARE rather than FOR UPDATE. Revoking a membership or
// disabling a workspace is an UPDATE of a non-key column, which takes FOR NO KEY
// UPDATE; that conflicts with FOR SHARE, so either one is serialised against a
// creation in flight. Two concurrent creations, however, both take FOR SHARE and
// do not block each other, which FOR UPDATE would have made them do for no
// safety gained.
//
// Whichever side wins is a correct outcome: a revocation that commits first
// makes the locked SELECT re-evaluate its predicate against the new row version,
// find no active membership, and insert nothing; a creation that gets there
// first completes and the revocation waits for the commit.
//
// workspace_id and created_by are read back out of the authorized context rather
// than taken from the parameters, so the row can only ever record the workspace
// and the actor the database itself authorized.
func (s *PGXChannelStore) CreateChannelForActiveMember(ctx context.Context, input CreateChannelInput) (domain.Channel, error) {
	if input.CreatedBy == "" {
		return domain.Channel{}, domain.ErrForbidden
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.Channel{}, fmt.Errorf("begin create channel for active member: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			// Rolled back on every failure below, including a failed secondary
			// insert, so a denied or broken creation never leaves a channel or a
			// half-populated membership behind.
			_ = tx.Rollback(ctx)
		}
	}()

	var categoryID *string
	if input.CategoryID != "" {
		categoryID = &input.CategoryID
	}

	var ch domain.Channel
	err = tx.QueryRow(ctx, `
		WITH authorized_context AS (
			SELECT w.id AS workspace_id, wm.user_id
			FROM chat.workspaces w
			JOIN chat.workspace_members wm
			  ON wm.workspace_id = w.id
			WHERE w.id = $1
			  AND w.status = 'active'
			  AND wm.user_id = $8
			  AND wm.status = 'active'
			  -- The SQL statement of domain.CanCreateChannel: every role that
			  -- reaches the workspace's public channels may create one, and a
			  -- guest — whose reach is only the channels it was added to — may
			  -- not. Re-derived here rather than trusted from the service, so a
			  -- demotion to guest that commits mid-flight is serialised against
			  -- the insert by the FOR SHARE below instead of racing it.
			  AND wm.role IN ('owner', 'admin', 'moderator', 'member')
			FOR SHARE OF w, wm
		)
		INSERT INTO chat.channels
			(workspace_id, category_id, slug, display_name, type, is_general, position, created_by)
		SELECT ac.workspace_id, $2, $3, $4, $5, $6, $7, ac.user_id
		FROM authorized_context ac
		RETURNING id, workspace_id, COALESCE(category_id::text, ''), slug, display_name,
		          type, status, is_general, position, COALESCE(created_by::text, ''),
		          created_at, updated_at`,
		input.WorkspaceID, categoryID, input.Slug, input.DisplayName,
		string(input.Type), input.IsGeneral, input.Position, input.CreatedBy,
	).Scan(
		&ch.ID, &ch.WorkspaceID, &ch.CategoryID, &ch.Slug, &ch.DisplayName,
		(*string)(&ch.Type), (*string)(&ch.Status), &ch.IsGeneral, &ch.Position, &ch.CreatedBy,
		&ch.CreatedAt, &ch.UpdatedAt,
	)
	if err != nil {
		// No row from the authorized context means no INSERT: the workspace was
		// not active, or the membership was absent or no longer active. Which of
		// them is deliberately not distinguished — the caller must not learn the
		// workspace exists from a failure to create in it.
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Channel{}, domain.ErrForbidden
		}
		if mapped := mapChannelWriteError(err); mapped != nil {
			return domain.Channel{}, mapped
		}
		return domain.Channel{}, fmt.Errorf("create channel for active member: %w", err)
	}

	if input.EnsureCreatorMemberRole != "" {
		if err := addChannelMember(ctx, tx, ch.ID, ch.CreatedBy, input.EnsureCreatorMemberRole); err != nil {
			return domain.Channel{}, err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return domain.Channel{}, fmt.Errorf("commit create channel for active member: %w", err)
	}
	committed = true
	return ch, nil
}

func createChannel(ctx context.Context, q channelQuerier, input CreateChannelInput) (domain.Channel, error) {
	var categoryID *string
	if input.CategoryID != "" {
		categoryID = &input.CategoryID
	}
	var createdBy *string
	if input.CreatedBy != "" {
		createdBy = &input.CreatedBy
	}

	var ch domain.Channel
	err := q.QueryRow(ctx, `
		INSERT INTO chat.channels
			(workspace_id, category_id, slug, display_name, type, is_general, position, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING id, workspace_id, COALESCE(category_id::text, ''), slug, display_name,
		          type, status, is_general, position, COALESCE(created_by::text, ''),
		          created_at, updated_at`,
		input.WorkspaceID, categoryID, input.Slug, input.DisplayName,
		string(input.Type), input.IsGeneral, input.Position, createdBy,
	).Scan(
		&ch.ID, &ch.WorkspaceID, &ch.CategoryID, &ch.Slug, &ch.DisplayName,
		(*string)(&ch.Type), (*string)(&ch.Status), &ch.IsGeneral, &ch.Position, &ch.CreatedBy,
		&ch.CreatedAt, &ch.UpdatedAt,
	)
	if err != nil {
		if mapped := mapChannelWriteError(err); mapped != nil {
			return domain.Channel{}, mapped
		}
		return domain.Channel{}, fmt.Errorf("create channel: %w", err)
	}
	return ch, nil
}

type channelQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

func addChannelMember(ctx context.Context, q channelQuerier, channelID, userID string, role domain.ChannelRole) error {
	_, err := q.Exec(ctx, `
		INSERT INTO chat.channel_members (channel_id, user_id, role)
		VALUES ($1, $2, $3)
		ON CONFLICT (channel_id, user_id) DO NOTHING`,
		channelID, userID, string(role),
	)
	if err != nil {
		return fmt.Errorf("add channel member: %w", err)
	}
	return nil
}

func (s *PGXChannelStore) GetCategoryByIDInWorkspace(ctx context.Context, workspaceID, id string) (domain.ChannelCategory, error) {
	var c domain.ChannelCategory
	err := s.pool.QueryRow(ctx, `
		SELECT id, workspace_id, name, position, created_at, updated_at
		FROM chat.channel_categories
		WHERE workspace_id = $1 AND id = $2`,
		workspaceID, id,
	).Scan(&c.ID, &c.WorkspaceID, &c.Name, &c.Position, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ChannelCategory{}, domain.ErrNotFound
		}
		return domain.ChannelCategory{}, fmt.Errorf("get category by id in workspace: %w", err)
	}
	return c, nil
}

func (s *PGXChannelStore) GetChannelByID(ctx context.Context, id string) (domain.Channel, error) {
	var ch domain.Channel
	err := s.pool.QueryRow(ctx, `
		SELECT id, workspace_id, COALESCE(category_id::text, ''), slug, display_name,
		       type, status, is_general, position, COALESCE(created_by::text, ''),
		       created_at, updated_at
		FROM chat.channels
		WHERE id = $1 AND status = 'active'`,
		id,
	).Scan(
		&ch.ID, &ch.WorkspaceID, &ch.CategoryID, &ch.Slug, &ch.DisplayName,
		(*string)(&ch.Type), (*string)(&ch.Status), &ch.IsGeneral, &ch.Position, &ch.CreatedBy,
		&ch.CreatedAt, &ch.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Channel{}, domain.ErrNotFound
		}
		return domain.Channel{}, fmt.Errorf("get channel by id: %w", err)
	}
	return ch, nil
}

func (s *PGXChannelStore) GetChannelByIDInWorkspace(ctx context.Context, workspaceID, id string) (domain.Channel, error) {
	var ch domain.Channel
	err := s.pool.QueryRow(ctx, `
		SELECT id, workspace_id, COALESCE(category_id::text, ''), slug, display_name,
		       type, status, is_general, position, COALESCE(created_by::text, ''),
		       created_at, updated_at
		FROM chat.channels
		WHERE id = $1 AND workspace_id = $2 AND status = 'active'`,
		id, workspaceID,
	).Scan(
		&ch.ID, &ch.WorkspaceID, &ch.CategoryID, &ch.Slug, &ch.DisplayName,
		(*string)(&ch.Type), (*string)(&ch.Status), &ch.IsGeneral, &ch.Position, &ch.CreatedBy,
		&ch.CreatedAt, &ch.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Channel{}, domain.ErrNotFound
		}
		return domain.Channel{}, fmt.Errorf("get channel by id in workspace: %w", err)
	}
	return ch, nil
}

func (s *PGXChannelStore) GetVisibleChannelByID(ctx context.Context, workspaceID, channelID, userID string) (domain.Channel, error) {
	return s.getVisibleChannel(ctx, `
		SELECT c.id, c.workspace_id, COALESCE(c.category_id::text, ''), c.slug, c.display_name,
		       c.type, c.status, c.is_general, c.position, COALESCE(c.created_by::text, ''),
		       c.created_at, c.updated_at
		FROM chat.channels c
		JOIN chat.workspaces w
		  ON c.workspace_id = w.id AND w.status = 'active'
		JOIN chat.workspace_members wm
		  ON wm.workspace_id = c.workspace_id AND wm.user_id = $3 AND wm.status = 'active'
		WHERE c.workspace_id = $1
		  AND c.id = $2
		  AND c.status = 'active'
		  AND chat.channel_visible_to_user(c.id, $3::uuid)`,
		workspaceID, channelID, userID,
	)
}

func (s *PGXChannelStore) GetVisibleChannelBySlug(ctx context.Context, workspaceID, slug, userID string) (domain.Channel, error) {
	return s.getVisibleChannel(ctx, `
		SELECT c.id, c.workspace_id, COALESCE(c.category_id::text, ''), c.slug, c.display_name,
		       c.type, c.status, c.is_general, c.position, COALESCE(c.created_by::text, ''),
		       c.created_at, c.updated_at
		FROM chat.channels c
		JOIN chat.workspaces w
		  ON c.workspace_id = w.id AND w.status = 'active'
		JOIN chat.workspace_members wm
		  ON wm.workspace_id = c.workspace_id AND wm.user_id = $3 AND wm.status = 'active'
		WHERE c.workspace_id = $1
		  AND c.slug = $2
		  AND c.status = 'active'
		  AND chat.channel_visible_to_user(c.id, $3::uuid)`,
		workspaceID, slug, userID,
	)
}

func (s *PGXChannelStore) getVisibleChannel(ctx context.Context, query string, args ...any) (domain.Channel, error) {
	var ch domain.Channel
	err := s.pool.QueryRow(ctx, query, args...).Scan(
		&ch.ID, &ch.WorkspaceID, &ch.CategoryID, &ch.Slug, &ch.DisplayName,
		(*string)(&ch.Type), (*string)(&ch.Status), &ch.IsGeneral, &ch.Position, &ch.CreatedBy,
		&ch.CreatedAt, &ch.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Channel{}, domain.ErrNotFound
		}
		return domain.Channel{}, fmt.Errorf("get visible channel: %w", err)
	}
	return ch, nil
}

func (s *PGXChannelStore) ListChannelsByWorkspace(ctx context.Context, workspaceID string) ([]domain.Channel, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, workspace_id, COALESCE(category_id::text, ''), slug, display_name,
		       type, status, is_general, position, COALESCE(created_by::text, ''),
		       created_at, updated_at
		FROM chat.channels
		WHERE workspace_id = $1 AND status = 'active'
		ORDER BY position, display_name`,
		workspaceID,
	)
	if err != nil {
		return nil, fmt.Errorf("list channels: %w", err)
	}
	defer rows.Close()

	var channels []domain.Channel
	for rows.Next() {
		var ch domain.Channel
		if err := rows.Scan(
			&ch.ID, &ch.WorkspaceID, &ch.CategoryID, &ch.Slug, &ch.DisplayName,
			(*string)(&ch.Type), (*string)(&ch.Status), &ch.IsGeneral, &ch.Position, &ch.CreatedBy,
			&ch.CreatedAt, &ch.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan channel: %w", err)
		}
		channels = append(channels, ch)
	}
	return channels, rows.Err()
}

func (s *PGXChannelStore) ListVisibleChannelsByUser(ctx context.Context, workspaceID, userID string) ([]domain.Channel, error) {
	accesses, err := s.ListVisibleChannelAccessByUser(ctx, workspaceID, userID)
	if err != nil {
		return nil, err
	}
	channels := make([]domain.Channel, 0, len(accesses))
	for _, access := range accesses {
		channels = append(channels, access.Channel)
	}
	return channels, nil
}

// ListVisibleChannelAccessByUser returns visible channels and the caller's
// optional channel membership in one query.
//
// Each row also carries the channel's activity instant (issue #414), resolved
// by a lateral join in this same statement rather than by one read per channel:
// the cost of the listing does not grow with a query per row however many
// channels the user can see. The lateral hangs off rows the visibility
// predicate has already admitted — the same predicate that decides whether the
// channel appears at all — so a private channel the caller is not a member of
// neither appears here nor has its activity read. It projects created_at and
// nothing else; no message content, author or id is selected.
func (s *PGXChannelStore) ListVisibleChannelAccessByUser(ctx context.Context, workspaceID, userID string) ([]VisibleChannelAccess, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT c.id, c.workspace_id, COALESCE(c.category_id::text, ''), c.slug, c.display_name,
		       c.type, c.status, c.is_general, c.position, COALESCE(c.created_by::text, ''),
		       c.created_at, c.updated_at,
		       COALESCE(cm.channel_id::text, ''), COALESCE(cm.user_id::text, ''),
		       COALESCE(cm.role::text, ''),
		       lm.created_at AS last_message_at
		FROM chat.channels c
		JOIN chat.workspaces w
		  ON c.workspace_id = w.id AND w.status = 'active'
		JOIN chat.workspace_members wm
		  ON wm.workspace_id = c.workspace_id AND wm.user_id = $2 AND wm.status = 'active'
		LEFT JOIN chat.channel_members cm
		  ON cm.channel_id = c.id AND cm.user_id = $2
		LEFT JOIN LATERAL (
		    SELECT m.created_at
		    FROM chat.messages m
		    WHERE m.workspace_id = c.workspace_id
		      AND m.channel_id = c.id
		    ORDER BY m.created_at DESC, m.id DESC
		    LIMIT 1
		) lm ON true
		WHERE c.workspace_id = $1
		  AND c.status = 'active'
		  AND chat.channel_visible_to_user(c.id, $2::uuid)
		ORDER BY c.position, c.display_name`,
		workspaceID, userID,
	)
	if err != nil {
		return nil, fmt.Errorf("list visible channels: %w", err)
	}
	defer rows.Close()

	var accesses []VisibleChannelAccess
	for rows.Next() {
		var ch domain.Channel
		var memberChannelID, memberUserID, memberRole string
		var lastMessageAt *time.Time
		if err := rows.Scan(
			&ch.ID, &ch.WorkspaceID, &ch.CategoryID, &ch.Slug, &ch.DisplayName,
			(*string)(&ch.Type), (*string)(&ch.Status), &ch.IsGeneral, &ch.Position, &ch.CreatedBy,
			&ch.CreatedAt, &ch.UpdatedAt,
			&memberChannelID, &memberUserID, &memberRole,
			&lastMessageAt,
		); err != nil {
			return nil, fmt.Errorf("scan visible channel: %w", err)
		}
		var member *domain.ChannelMember
		if memberChannelID != "" {
			member = &domain.ChannelMember{
				ChannelID: memberChannelID,
				UserID:    memberUserID,
				Role:      domain.ChannelRole(memberRole),
			}
		}
		accesses = append(accesses, VisibleChannelAccess{Channel: ch, ChannelMember: member, LastMessageAt: lastMessageAt})
	}
	return accesses, rows.Err()
}

func (s *PGXChannelStore) UpdateChannel(ctx context.Context, input UpdateChannelInput) (domain.Channel, error) {
	if input.EnsureMemberUserID == "" {
		return updateChannel(ctx, s.pool, input)
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.Channel{}, fmt.Errorf("begin update channel: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()

	ch, err := updateChannel(ctx, tx, input)
	if err != nil {
		return domain.Channel{}, err
	}
	if err := addChannelMember(ctx, tx, ch.ID, input.EnsureMemberUserID, domain.ChannelRoleMember); err != nil {
		return domain.Channel{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Channel{}, fmt.Errorf("commit update channel: %w", err)
	}
	committed = true
	return ch, nil
}

func updateChannel(ctx context.Context, q channelQuerier, input UpdateChannelInput) (domain.Channel, error) {
	var categoryID *string
	if input.CategoryID != "" {
		categoryID = &input.CategoryID
	}
	var ch domain.Channel
	err := q.QueryRow(ctx, `
		UPDATE chat.channels
		SET category_id = $3,
		    slug = $4,
		    display_name = $5,
		    type = $6,
		    position = $7,
		    updated_at = now()
		WHERE workspace_id = $1
		  AND id = $2
		  AND status = 'active'
		  AND is_general = false
		RETURNING id, workspace_id, COALESCE(category_id::text, ''), slug, display_name,
		          type, status, is_general, position, COALESCE(created_by::text, ''),
		          created_at, updated_at`,
		input.WorkspaceID, input.ChannelID, categoryID, input.Slug, input.DisplayName, string(input.Type), input.Position,
	).Scan(
		&ch.ID, &ch.WorkspaceID, &ch.CategoryID, &ch.Slug, &ch.DisplayName,
		(*string)(&ch.Type), (*string)(&ch.Status), &ch.IsGeneral, &ch.Position, &ch.CreatedBy,
		&ch.CreatedAt, &ch.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Channel{}, domain.ErrNotFound
		}
		if mapped := mapChannelWriteError(err); mapped != nil {
			return domain.Channel{}, mapped
		}
		return domain.Channel{}, fmt.Errorf("update channel: %w", err)
	}
	return ch, nil
}

func (s *PGXChannelStore) ArchiveChannel(ctx context.Context, workspaceID, channelID string) (domain.Channel, error) {
	var ch domain.Channel
	err := s.pool.QueryRow(ctx, `
		UPDATE chat.channels
		SET status = 'archived',
		    updated_at = now()
		WHERE workspace_id = $1
		  AND id = $2
		  AND status = 'active'
		  AND is_general = false
		RETURNING id, workspace_id, COALESCE(category_id::text, ''), slug, display_name,
		          type, status, is_general, position, COALESCE(created_by::text, ''),
		          created_at, updated_at`,
		workspaceID, channelID,
	).Scan(
		&ch.ID, &ch.WorkspaceID, &ch.CategoryID, &ch.Slug, &ch.DisplayName,
		(*string)(&ch.Type), (*string)(&ch.Status), &ch.IsGeneral, &ch.Position, &ch.CreatedBy,
		&ch.CreatedAt, &ch.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Channel{}, domain.ErrNotFound
		}
		if mapped := mapChannelWriteError(err); mapped != nil {
			return domain.Channel{}, mapped
		}
		return domain.Channel{}, fmt.Errorf("archive channel: %w", err)
	}
	return ch, nil
}

func mapChannelWriteError(err error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return nil
	}
	switch pgErr.ConstraintName {
	case "idx_channels_one_general_per_workspace":
		return domain.ErrGeneralChannelExists
	case "channels_workspace_slug_unique":
		return domain.ErrDuplicateSlug
	case "channels_workspace_category_fk":
		return domain.ErrInvalidInput
	}
	return nil
}
