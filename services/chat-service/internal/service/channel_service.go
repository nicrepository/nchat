package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

const generalChannelSlug = "geral"

// CreateChannelInput is the public CRUD input for channel creation.
// is_general, status, and created_by are intentionally not caller-provided.
type CreateChannelInput struct {
	WorkspaceID string
	CallerID    string
	CategoryID  string
	Slug        string
	DisplayName string
	Type        domain.ChannelType
	Position    int
}

// GetChannelInput supports lookup by either ChannelID or Slug.
type GetChannelInput struct {
	WorkspaceID string
	CallerID    string
	ChannelID   string
	Slug        string
}

// UpdateChannelInput updates mutable fields only.
// CategoryID nil means unchanged; pointer to empty string clears the category.
type UpdateChannelInput struct {
	WorkspaceID string
	CallerID    string
	ChannelID   string
	Slug        string
	DisplayName string
	CategoryID  *string
	Position    *int
	Type        *domain.ChannelType
}

// ChannelService handles channel CRUD authorization and validation.
type ChannelService struct {
	workspaces storage.WorkspaceStore
	channels   storage.ChannelStore
	members    storage.MemberStore
}

func NewChannelService(workspaces storage.WorkspaceStore, channels storage.ChannelStore, members storage.MemberStore) *ChannelService {
	return &ChannelService{workspaces: workspaces, channels: channels, members: members}
}

// CreateChannel creates a public or private channel in an active workspace.
// Private channels add the creator as a channel member in the storage transaction.
func (s *ChannelService) CreateChannel(ctx context.Context, input CreateChannelInput) (domain.Channel, error) {
	if _, err := s.requireManagePermission(ctx, input.WorkspaceID, input.CallerID); err != nil {
		return domain.Channel{}, err
	}

	slug, displayName, err := normalizeChannelFields(input.Slug, input.DisplayName)
	if err != nil {
		return domain.Channel{}, err
	}
	if slug == generalChannelSlug {
		return domain.Channel{}, fmt.Errorf("%w: geral is reserved", domain.ErrInvalidInput)
	}
	if err := validateChannelType(input.Type); err != nil {
		return domain.Channel{}, err
	}
	categoryID := strings.TrimSpace(input.CategoryID)
	if err := s.requireCategoryInWorkspace(ctx, input.WorkspaceID, categoryID); err != nil {
		return domain.Channel{}, err
	}

	createInput := storage.CreateChannelInput{
		WorkspaceID: input.WorkspaceID,
		CategoryID:  categoryID,
		Slug:        slug,
		DisplayName: displayName,
		Type:        input.Type,
		IsGeneral:   false,
		Position:    input.Position,
		CreatedBy:   input.CallerID,
	}
	if input.Type == domain.ChannelTypePrivate {
		return s.channels.CreateChannelWithMember(ctx, createInput, input.CallerID, domain.ChannelRoleMember)
	}
	return s.channels.CreateChannel(ctx, createInput)
}

// ListChannels returns channels visible to callerID in workspaceID.
func (s *ChannelService) ListChannels(ctx context.Context, workspaceID, callerID string) ([]domain.Channel, error) {
	if _, err := s.requireActiveWorkspaceMember(ctx, workspaceID, callerID); err != nil {
		return nil, err
	}
	channels, err := s.channels.ListVisibleChannelsByUser(ctx, workspaceID, callerID)
	if err != nil {
		return nil, fmt.Errorf("list visible channels: %w", err)
	}
	return channels, nil
}

// GetChannel returns one visibility-bound active channel by ID or slug.
func (s *ChannelService) GetChannel(ctx context.Context, input GetChannelInput) (domain.Channel, error) {
	if _, err := s.requireActiveWorkspaceMember(ctx, input.WorkspaceID, input.CallerID); err != nil {
		return domain.Channel{}, err
	}
	if input.ChannelID != "" {
		ch, err := s.channels.GetVisibleChannelByID(ctx, input.WorkspaceID, input.ChannelID, input.CallerID)
		if err != nil {
			return domain.Channel{}, err
		}
		return ch, nil
	}

	slug := strings.ToLower(strings.TrimSpace(input.Slug))
	if !slugRE.MatchString(slug) {
		return domain.Channel{}, fmt.Errorf("%w: slug must be lowercase alphanumeric with optional internal hyphens, no leading/trailing hyphens, max 63 chars", domain.ErrInvalidInput)
	}
	ch, err := s.channels.GetVisibleChannelBySlug(ctx, input.WorkspaceID, slug, input.CallerID)
	if err != nil {
		return domain.Channel{}, err
	}
	return ch, nil
}

// UpdateChannel updates mutable fields for a non-general channel.
func (s *ChannelService) UpdateChannel(ctx context.Context, input UpdateChannelInput) (domain.Channel, error) {
	if _, err := s.requireManagePermission(ctx, input.WorkspaceID, input.CallerID); err != nil {
		return domain.Channel{}, err
	}

	current, err := s.channels.GetChannelByIDInWorkspace(ctx, input.WorkspaceID, input.ChannelID)
	if err != nil {
		return domain.Channel{}, err
	}
	if current.IsGeneral {
		return domain.Channel{}, fmt.Errorf("%w: geral is immutable", domain.ErrInvalidInput)
	}

	next := storage.UpdateChannelInput{
		WorkspaceID: input.WorkspaceID,
		ChannelID:   input.ChannelID,
		CategoryID:  current.CategoryID,
		Slug:        current.Slug,
		DisplayName: current.DisplayName,
		Type:        current.Type,
		Position:    current.Position,
	}
	if input.Slug != "" {
		slug := strings.ToLower(strings.TrimSpace(input.Slug))
		if !slugRE.MatchString(slug) {
			return domain.Channel{}, fmt.Errorf("%w: slug must be lowercase alphanumeric with optional internal hyphens, no leading/trailing hyphens, max 63 chars", domain.ErrInvalidInput)
		}
		if slug == generalChannelSlug {
			return domain.Channel{}, fmt.Errorf("%w: geral is reserved", domain.ErrInvalidInput)
		}
		next.Slug = slug
	}
	if input.DisplayName != "" {
		next.DisplayName = strings.TrimSpace(input.DisplayName)
		if next.DisplayName == "" {
			return domain.Channel{}, fmt.Errorf("%w: display_name is required", domain.ErrInvalidInput)
		}
	}
	if input.CategoryID != nil {
		categoryID := strings.TrimSpace(*input.CategoryID)
		if err := s.requireCategoryInWorkspace(ctx, input.WorkspaceID, categoryID); err != nil {
			return domain.Channel{}, err
		}
		next.CategoryID = categoryID
	}
	if input.Position != nil {
		next.Position = *input.Position
	}
	if input.Type != nil {
		if err := validateChannelType(*input.Type); err != nil {
			return domain.Channel{}, err
		}
		next.Type = *input.Type
	}
	if current.Type == domain.ChannelTypePublic && next.Type == domain.ChannelTypePrivate {
		next.EnsureMemberUserID = input.CallerID
	}

	updated, err := s.channels.UpdateChannel(ctx, next)
	if err != nil {
		return domain.Channel{}, err
	}
	return updated, nil
}

// ArchiveChannel marks a non-general channel archived. It never hard-deletes.
func (s *ChannelService) ArchiveChannel(ctx context.Context, workspaceID, channelID, callerID string) (domain.Channel, error) {
	if _, err := s.requireManagePermission(ctx, workspaceID, callerID); err != nil {
		return domain.Channel{}, err
	}
	current, err := s.channels.GetChannelByIDInWorkspace(ctx, workspaceID, channelID)
	if err != nil {
		return domain.Channel{}, err
	}
	if current.IsGeneral {
		return domain.Channel{}, fmt.Errorf("%w: geral is immutable", domain.ErrInvalidInput)
	}
	archived, err := s.channels.ArchiveChannel(ctx, workspaceID, channelID)
	if err != nil {
		return domain.Channel{}, err
	}
	return archived, nil
}

func (s *ChannelService) requireManagePermission(ctx context.Context, workspaceID, userID string) (domain.WorkspaceMember, error) {
	member, err := s.requireActiveWorkspaceMember(ctx, workspaceID, userID)
	if err != nil {
		return domain.WorkspaceMember{}, err
	}
	if !domain.CanManageWorkspace(&member) {
		return domain.WorkspaceMember{}, domain.ErrForbidden
	}
	return member, nil
}

func (s *ChannelService) requireActiveWorkspaceMember(ctx context.Context, workspaceID, userID string) (domain.WorkspaceMember, error) {
	workspace, err := s.workspaces.GetWorkspaceByID(ctx, workspaceID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return domain.WorkspaceMember{}, domain.ErrForbidden
		}
		return domain.WorkspaceMember{}, fmt.Errorf("get workspace: %w", err)
	}
	if workspace.Status != domain.WorkspaceStatusActive {
		return domain.WorkspaceMember{}, domain.ErrForbidden
	}

	member, err := s.members.GetWorkspaceMember(ctx, workspaceID, userID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return domain.WorkspaceMember{}, domain.ErrForbidden
		}
		return domain.WorkspaceMember{}, fmt.Errorf("get workspace member: %w", err)
	}
	if member.Status != domain.MemberStatusActive || member.WorkspaceID != workspaceID {
		return domain.WorkspaceMember{}, domain.ErrForbidden
	}
	return member, nil
}

func (s *ChannelService) requireCategoryInWorkspace(ctx context.Context, workspaceID, categoryID string) error {
	categoryID = strings.TrimSpace(categoryID)
	if categoryID == "" {
		return nil
	}
	if _, err := s.channels.GetCategoryByIDInWorkspace(ctx, workspaceID, categoryID); err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return fmt.Errorf("%w: category_id must belong to workspace", domain.ErrInvalidInput)
		}
		return fmt.Errorf("get category: %w", err)
	}
	return nil
}

func normalizeChannelFields(slug, displayName string) (string, string, error) {
	slug = strings.ToLower(strings.TrimSpace(slug))
	displayName = strings.TrimSpace(displayName)
	if !slugRE.MatchString(slug) {
		return "", "", fmt.Errorf("%w: slug must be lowercase alphanumeric with optional internal hyphens, no leading/trailing hyphens, max 63 chars", domain.ErrInvalidInput)
	}
	if displayName == "" {
		return "", "", fmt.Errorf("%w: display_name is required", domain.ErrInvalidInput)
	}
	return slug, displayName, nil
}

func validateChannelType(t domain.ChannelType) error {
	if t != domain.ChannelTypePublic && t != domain.ChannelTypePrivate {
		return fmt.Errorf("%w: type must be public or private", domain.ErrInvalidInput)
	}
	return nil
}
