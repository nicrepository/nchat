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

// WorkspaceSettingsStore updates workspace-scoped settings with an atomic RBAC backstop.
type WorkspaceSettingsStore interface {
	UpdateEditWindow(ctx context.Context, workspaceID, userID string, seconds *int) (domain.Workspace, error)
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
		SELECT id, slug, name, status, created_at, updated_at
		FROM chat.workspaces
		WHERE id = $1`,
		id,
	).Scan(&w.ID, &w.Slug, &w.Name, (*string)(&w.Status), &w.CreatedAt, &w.UpdatedAt)
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
		SELECT id, slug, name, status, created_at, updated_at
		FROM chat.workspaces
		WHERE slug = $1 AND status = 'active'`,
		slug,
	).Scan(&w.ID, &w.Slug, &w.Name, (*string)(&w.Status), &w.CreatedAt, &w.UpdatedAt)
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
