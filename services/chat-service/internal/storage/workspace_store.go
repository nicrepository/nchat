package storage

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
)

// WorkspaceStore is the persistence interface for workspace read operations.
type WorkspaceStore interface {
	GetDefaultWorkspace(ctx context.Context) (domain.Workspace, error)
	GetWorkspaceByID(ctx context.Context, id string) (domain.Workspace, error)
	GetWorkspaceBySlug(ctx context.Context, slug string) (domain.Workspace, error)
}

// WorkspaceSettingsStore reads and updates workspace-scoped settings with an
// atomic RBAC backstop on every write.
type WorkspaceSettingsStore interface {
	GetWorkspaceByID(ctx context.Context, id string) (domain.Workspace, error)
	UpdateEditWindow(ctx context.Context, workspaceID, userID string, seconds *int) (domain.Workspace, error)
	UpdateMessageRateLimit(ctx context.Context, workspaceID, userID string, perMinute int) (domain.Workspace, error)
}

// PGXWorkspaceStore implements WorkspaceStore using a pgx connection pool.
type PGXWorkspaceStore struct {
	pool Pool
}

func NewPGXWorkspaceStore(pool Pool) *PGXWorkspaceStore {
	return &PGXWorkspaceStore{pool: pool}
}

func (s *PGXWorkspaceStore) GetDefaultWorkspace(ctx context.Context) (domain.Workspace, error) {
	return s.GetWorkspaceBySlug(ctx, "default")
}

func (s *PGXWorkspaceStore) GetWorkspaceByID(ctx context.Context, id string) (domain.Workspace, error) {
	var w domain.Workspace
	err := s.pool.QueryRow(ctx, `
		SELECT id, slug, name, status, message_rate_limit_per_minute, created_at, updated_at
		FROM chat.workspaces
		WHERE id = $1`,
		id,
	).Scan(&w.ID, &w.Slug, &w.Name, (*string)(&w.Status), &w.MessageRateLimitPerMinute, &w.CreatedAt, &w.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Workspace{}, domain.ErrNotFound
		}
		return domain.Workspace{}, fmt.Errorf("get workspace by id: %w", err)
	}
	return w, nil
}

func (s *PGXWorkspaceStore) GetWorkspaceBySlug(ctx context.Context, slug string) (domain.Workspace, error) {
	var w domain.Workspace
	err := s.pool.QueryRow(ctx, `
		SELECT id, slug, name, status, message_rate_limit_per_minute, created_at, updated_at
		FROM chat.workspaces
		WHERE slug = $1 AND status = 'active'`,
		slug,
	).Scan(&w.ID, &w.Slug, &w.Name, (*string)(&w.Status), &w.MessageRateLimitPerMinute, &w.CreatedAt, &w.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Workspace{}, domain.ErrNotFound
		}
		return domain.Workspace{}, fmt.Errorf("get workspace by slug: %w", err)
	}
	return w, nil
}

func (s *PGXWorkspaceStore) UpdateEditWindow(ctx context.Context, workspaceID, userID string, seconds *int) (domain.Workspace, error) {
	var w domain.Workspace
	err := s.pool.QueryRow(ctx, `
		UPDATE chat.workspaces w
		SET edit_window_seconds = $3, updated_at = now()
		FROM chat.workspace_members wm
		WHERE w.id = $1 AND w.status = 'active'
		  AND wm.workspace_id = w.id AND wm.user_id = $2
		  AND wm.status = 'active' AND wm.role IN ('owner', 'admin')
		RETURNING w.id::text, w.slug, w.name, w.status,
		          w.edit_window_seconds, w.created_at, w.updated_at`,
		workspaceID, userID, seconds,
	).Scan(&w.ID, &w.Slug, &w.Name, (*string)(&w.Status), &w.EditWindowSeconds, &w.CreatedAt, &w.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Workspace{}, domain.ErrForbidden
		}
		return domain.Workspace{}, fmt.Errorf("update workspace edit window: %w", err)
	}
	return w, nil
}

// UpdateMessageRateLimit sets the RF-19 anti-spam policy for a workspace.
//
// The join against chat.workspace_members is the authorization, not a lookup:
// the row is only updated when userID is an active owner/admin *of that same
// workspace*, so a caller who aims the endpoint at a workspace they do not
// administer changes nothing and gets ErrNoRows. The handler checks the same
// thing before calling; this is the backstop that holds even if that check is
// ever removed, and it is what makes the write and the authorization a single
// atomic statement rather than a check followed by a racy update.
//
// perMinute is expected to be pre-validated against the domain bounds; the
// CHECK constraint from migration 000018 rejects anything outside them, which
// surfaces here as a wrapped error rather than a silent clamp.
func (s *PGXWorkspaceStore) UpdateMessageRateLimit(ctx context.Context, workspaceID, userID string, perMinute int) (domain.Workspace, error) {
	var w domain.Workspace
	err := s.pool.QueryRow(ctx, `
		UPDATE chat.workspaces w
		SET message_rate_limit_per_minute = $3, updated_at = now()
		FROM chat.workspace_members wm
		WHERE w.id = $1 AND w.status = 'active'
		  AND wm.workspace_id = w.id AND wm.user_id = $2
		  AND wm.status = 'active' AND wm.role IN ('owner', 'admin')
		RETURNING w.id::text, w.slug, w.name, w.status,
		          w.message_rate_limit_per_minute, w.created_at, w.updated_at`,
		workspaceID, userID, perMinute,
	).Scan(&w.ID, &w.Slug, &w.Name, (*string)(&w.Status), &w.MessageRateLimitPerMinute, &w.CreatedAt, &w.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Workspace{}, domain.ErrForbidden
		}
		return domain.Workspace{}, fmt.Errorf("update workspace message rate limit: %w", err)
	}
	return w, nil
}
