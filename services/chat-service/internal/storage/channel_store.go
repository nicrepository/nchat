package storage

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
)

const pgCodeUniqueViolation = "23505"

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
}

// ChannelStore is the persistence interface for channel operations.
type ChannelStore interface {
	CreateCategory(ctx context.Context, input CreateCategoryInput) (domain.ChannelCategory, error)
	CreateChannel(ctx context.Context, input CreateChannelInput) (domain.Channel, error)
	GetChannelByID(ctx context.Context, id string) (domain.Channel, error)
	// GetChannelByIDInWorkspace returns the channel only if it belongs to workspaceID.
	// Returns ErrNotFound when the channel does not exist or belongs to a different workspace.
	GetChannelByIDInWorkspace(ctx context.Context, workspaceID, id string) (domain.Channel, error)
	ListChannelsByWorkspace(ctx context.Context, workspaceID string) ([]domain.Channel, error)
	// ListVisibleChannelsByUser returns active channels in workspaceID visible to userID.
	// Visibility is enforced in SQL: active workspace + active workspace membership required;
	// public and general channels are always included; private channels require channel membership.
	// Returns an empty slice when the workspace is disabled or userID is not an active member.
	ListVisibleChannelsByUser(ctx context.Context, workspaceID, userID string) ([]domain.Channel, error)
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
	var categoryID *string
	if input.CategoryID != "" {
		categoryID = &input.CategoryID
	}
	var createdBy *string
	if input.CreatedBy != "" {
		createdBy = &input.CreatedBy
	}

	var ch domain.Channel
	err := s.pool.QueryRow(ctx, `
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
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgCodeUniqueViolation {
			if pgErr.ConstraintName == "idx_channels_one_general_per_workspace" {
				return domain.Channel{}, domain.ErrGeneralChannelExists
			}
			return domain.Channel{}, domain.ErrDuplicateSlug
		}
		return domain.Channel{}, fmt.Errorf("create channel: %w", err)
	}
	return ch, nil
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
	rows, err := s.pool.Query(ctx, `
		SELECT c.id, c.workspace_id, COALESCE(c.category_id::text, ''), c.slug, c.display_name,
		       c.type, c.status, c.is_general, c.position, COALESCE(c.created_by::text, ''),
		       c.created_at, c.updated_at
		FROM chat.channels c
		JOIN chat.workspaces w
		  ON c.workspace_id = w.id AND w.status = 'active'
		JOIN chat.workspace_members wm
		  ON wm.workspace_id = c.workspace_id AND wm.user_id = $2 AND wm.status = 'active'
		LEFT JOIN chat.channel_members cm
		  ON cm.channel_id = c.id AND cm.user_id = $2
		WHERE c.workspace_id = $1
		  AND c.status = 'active'
		  AND (c.is_general = true OR c.type = 'public' OR cm.channel_id IS NOT NULL)
		ORDER BY c.position, c.display_name`,
		workspaceID, userID,
	)
	if err != nil {
		return nil, fmt.Errorf("list visible channels: %w", err)
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
			return nil, fmt.Errorf("scan visible channel: %w", err)
		}
		channels = append(channels, ch)
	}
	return channels, rows.Err()
}
