package service

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

// slugRE accepts lowercase alphanumeric slugs with internal hyphens (no leading/trailing hyphens).
// Single-char slugs are valid. Max 63 chars.
// Rejects backslash, spaces, uppercase, leading/trailing hyphens.
var slugRE = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

// WorkspaceService handles workspace and channel creation use cases.
type WorkspaceService struct {
	workspaces storage.WorkspaceStore
	channels   storage.ChannelStore
}

func NewWorkspaceService(workspaces storage.WorkspaceStore, channels storage.ChannelStore) *WorkspaceService {
	return &WorkspaceService{workspaces: workspaces, channels: channels}
}

// GetDefault returns the default workspace (slug='default').
func (s *WorkspaceService) GetDefault(ctx context.Context) (domain.Workspace, error) {
	return s.workspaces.GetDefaultWorkspace(ctx)
}

// CreateCategory creates a channel category in a workspace.
func (s *WorkspaceService) CreateCategory(ctx context.Context, workspaceID, name string, position int) (domain.ChannelCategory, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return domain.ChannelCategory{}, fmt.Errorf("%w: name is required", domain.ErrInvalidInput)
	}
	if err := s.requireActiveWorkspace(ctx, workspaceID); err != nil {
		return domain.ChannelCategory{}, err
	}
	return s.channels.CreateCategory(ctx, storage.CreateCategoryInput{
		WorkspaceID: workspaceID,
		Name:        name,
		Position:    position,
	})
}

// CreateChannel validates and creates a channel. The slug is normalized to lowercase.
func (s *WorkspaceService) CreateChannel(ctx context.Context, input storage.CreateChannelInput) (domain.Channel, error) {
	input.Slug = strings.ToLower(strings.TrimSpace(input.Slug))

	if !slugRE.MatchString(input.Slug) {
		return domain.Channel{}, fmt.Errorf("%w: slug must be lowercase alphanumeric with optional internal hyphens, no leading/trailing hyphens, max 63 chars", domain.ErrInvalidInput)
	}
	// Same helper as ChannelService: this path also persists display_name, so it
	// cannot be the one that skips the cap.
	displayName, err := domain.NormalizeChannelDisplayName(input.DisplayName)
	if err != nil {
		return domain.Channel{}, err
	}
	input.DisplayName = displayName
	if input.Type != domain.ChannelTypePublic && input.Type != domain.ChannelTypePrivate {
		return domain.Channel{}, fmt.Errorf("%w: type must be public or private", domain.ErrInvalidInput)
	}
	if err := s.requireActiveWorkspace(ctx, input.WorkspaceID); err != nil {
		return domain.Channel{}, err
	}
	return s.channels.CreateChannel(ctx, input)
}

func (s *WorkspaceService) requireActiveWorkspace(ctx context.Context, workspaceID string) error {
	workspace, err := s.workspaces.GetWorkspaceByID(ctx, workspaceID)
	if err != nil {
		return fmt.Errorf("get workspace: %w", err)
	}
	if workspace.Status != domain.WorkspaceStatusActive {
		return domain.ErrForbidden
	}
	return nil
}
