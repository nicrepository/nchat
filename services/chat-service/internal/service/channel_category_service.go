package service

import (
	"context"
	"fmt"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

// CreateChannelCategoryInput, RenameChannelCategoryInput and
// ReorderChannelCategoriesInput are the whole caller-controlled surface of RF-17
// category management.
//
// WorkspaceID and CallerID are never taken from a request body: the handler
// resolves the workspace server-side and reads the caller from the authenticated
// context. Position, timestamps and IDs are absent by construction, so there is
// nothing for a client to mass-assign — new categories are appended and move only
// through reordering.
type CreateChannelCategoryInput struct {
	WorkspaceID string
	CallerID    string
	Name        string
}

type RenameChannelCategoryInput struct {
	WorkspaceID string
	CallerID    string
	CategoryID  string
	Name        string
}

type ReorderChannelCategoriesInput struct {
	WorkspaceID string
	CallerID    string
	OrderedIDs  []string
}

// ChannelCategoryGroup is one section of the grouped channel listing.
//
// Category is nil for exactly one group — the virtual one that holds channels
// with no category — which is what lets a reader tell a persisted category from
// the virtual grouping without a synthetic ID. Channels reuses SidebarChannel so
// the grouped listing carries the same server-derived write eligibility the
// sidebar already exposes, from the same computation.
type ChannelCategoryGroup struct {
	Category *domain.ChannelCategory
	Channels []SidebarChannel
}

// ChannelCategoryService implements RF-17: channel categories per workspace, with
// explicit ordering, channels without a category grouped under "Geral", and
// management restricted to workspace management roles.
type ChannelCategoryService struct {
	workspaces storage.WorkspaceStore
	members    storage.MemberStore
	categories storage.ChannelCategoryStore
	channels   sidebarChannelStore
}

// NewChannelCategoryService wires the service. channels is the read side of the
// existing channel visibility policy and is intentionally the same narrow
// interface SidebarService uses.
func NewChannelCategoryService(
	workspaces storage.WorkspaceStore,
	members storage.MemberStore,
	categories storage.ChannelCategoryStore,
	channels sidebarChannelStore,
) *ChannelCategoryService {
	return &ChannelCategoryService{
		workspaces: workspaces,
		members:    members,
		categories: categories,
		channels:   channels,
	}
}

// ListGroupedChannels returns the workspace's categories in order, each with the
// channels the caller may see, plus the virtual uncategorized group.
//
// Reading categories must not widen channel access, so the channels are not
// re-filtered here: they come from ListVisibleChannelAccessByUser, the same
// single SQL policy the sidebar reads through (active workspace, active
// membership, public/general channels always, private channels only with a
// channel_members row). Grouping is done over that result in memory, so a
// visibility rule cannot drift between this endpoint and the sidebar, and adding a
// category can never reveal a channel the caller could not already list.
//
// Two queries total, regardless of how many categories exist: one for the
// categories and one for the visible channels. There is no per-category query.
//
// The virtual group comes first and is always present, even when empty, so the
// response shape is the same on every call. It holds every visible channel whose
// category_id is empty — including the workspace's #geral channel, which is an
// ordinary uncategorized channel and not a category of its own.
func (s *ChannelCategoryService) ListGroupedChannels(ctx context.Context, workspaceID, callerID string) ([]ChannelCategoryGroup, error) {
	member, err := requireActiveWorkspaceMember(ctx, s.workspaces, s.members, workspaceID, callerID)
	if err != nil {
		return nil, err
	}

	categories, err := s.categories.ListChannelCategories(ctx, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("list channel categories: %w", err)
	}
	accesses, err := s.channels.ListVisibleChannelAccessByUser(ctx, workspaceID, callerID)
	if err != nil {
		return nil, fmt.Errorf("list visible channels: %w", err)
	}

	groups := make([]ChannelCategoryGroup, 0, len(categories)+1)
	groups = append(groups, ChannelCategoryGroup{Channels: make([]SidebarChannel, 0)})
	byCategoryID := make(map[string]int, len(categories))
	for i := range categories {
		byCategoryID[categories[i].ID] = len(groups)
		groups = append(groups, ChannelCategoryGroup{
			Category: &categories[i],
			Channels: make([]SidebarChannel, 0),
		})
	}

	for _, access := range accesses {
		channel := SidebarChannel{
			Channel:  access.Channel,
			CanWrite: domain.CanWriteChannel(&member, access.ChannelMember, access.Channel),
		}
		// An unknown category ID cannot happen while the composite FK holds — it
		// keeps a channel's category inside the channel's own workspace — but
		// falling back to the uncategorized group means an unexpected value costs
		// the caller a section, never the channel.
		index, ok := byCategoryID[access.Channel.CategoryID]
		if !ok {
			index = 0
		}
		groups[index].Channels = append(groups[index].Channels, channel)
	}
	return groups, nil
}

// CanManageCategories reports whether callerID currently holds
// domain.CanManageChannelCategories in workspaceID — the same predicate every
// write in this service enforces via requireWorkspaceManager.
//
// It exists as its own read so a caller (the sidebar, the "criar canal" form)
// can decide whether to offer category management or assignment *before*
// attempting a write, without duplicating the role predicate. It is a UI hint
// only, exactly like ChannelDetails.CanManageMembers: every write endpoint
// re-derives the same decision from the session, so this answer grants
// nothing by itself.
func (s *ChannelCategoryService) CanManageCategories(ctx context.Context, workspaceID, callerID string) (bool, error) {
	member, err := requireActiveWorkspaceMember(ctx, s.workspaces, s.members, workspaceID, callerID)
	if err != nil {
		return false, err
	}
	return domain.CanManageChannelCategories(&member), nil
}

// CreateChannelCategory appends a category to the workspace.
//
// The management role is checked here so a caller with no business in this
// workspace is refused before the name validation can tell them anything about
// it, and again inside the INSERT, which is the decision that counts.
func (s *ChannelCategoryService) CreateChannelCategory(ctx context.Context, input CreateChannelCategoryInput) (domain.ChannelCategory, error) {
	if _, err := requireWorkspaceManager(ctx, s.workspaces, s.members, input.WorkspaceID, input.CallerID); err != nil {
		return domain.ChannelCategory{}, err
	}
	name, err := domain.NormalizeChannelCategoryName(input.Name)
	if err != nil {
		return domain.ChannelCategory{}, err
	}

	// A ceiling rather than an invariant: two concurrent creates can both read a
	// count just under it and land one row over. That is acceptable for an abuse
	// guard on a rate-limited, management-only write, and keeping it here rather
	// than inside the INSERT is what lets "limit reached" and "not authorized"
	// stay two distinguishable answers.
	total, err := s.categories.CountChannelCategories(ctx, input.WorkspaceID)
	if err != nil {
		return domain.ChannelCategory{}, fmt.Errorf("count channel categories: %w", err)
	}
	if total >= domain.MaxCategoriesPerWorkspace {
		return domain.ChannelCategory{}, domain.ErrChannelCategoryLimitReached
	}

	category, err := s.categories.CreateChannelCategoryForManager(ctx, storage.CreateChannelCategoryInput{
		WorkspaceID: input.WorkspaceID,
		CallerID:    input.CallerID,
		Name:        name,
	})
	if err != nil {
		return domain.ChannelCategory{}, err
	}
	return category, nil
}

// RenameChannelCategory renames one category of the workspace. The name is the
// only field a caller may change; position moves only through reordering.
func (s *ChannelCategoryService) RenameChannelCategory(ctx context.Context, input RenameChannelCategoryInput) (domain.ChannelCategory, error) {
	if _, err := requireWorkspaceManager(ctx, s.workspaces, s.members, input.WorkspaceID, input.CallerID); err != nil {
		return domain.ChannelCategory{}, err
	}
	name, err := domain.NormalizeChannelCategoryName(input.Name)
	if err != nil {
		return domain.ChannelCategory{}, err
	}

	category, err := s.categories.RenameChannelCategoryForManager(ctx, storage.RenameChannelCategoryInput{
		WorkspaceID: input.WorkspaceID,
		CallerID:    input.CallerID,
		CategoryID:  input.CategoryID,
		Name:        name,
	})
	if err != nil {
		return domain.ChannelCategory{}, err
	}
	return category, nil
}

// ReorderChannelCategories rewrites the positions of the workspace's categories.
//
// The payload must be the workspace's complete category set, each ID once. That
// makes the outcome a total order rather than a patch whose result depends on
// what the caller happened to omit, and it bounds the request: the set cannot
// exceed MaxCategoriesPerWorkspace. The set itself is verified against the rows
// the store locks, not against a count read beforehand.
func (s *ChannelCategoryService) ReorderChannelCategories(ctx context.Context, input ReorderChannelCategoriesInput) ([]domain.ChannelCategory, error) {
	if _, err := requireWorkspaceManager(ctx, s.workspaces, s.members, input.WorkspaceID, input.CallerID); err != nil {
		return nil, err
	}
	if len(input.OrderedIDs) == 0 || len(input.OrderedIDs) > domain.MaxCategoriesPerWorkspace {
		return nil, domain.ErrInvalidChannelCategoryOrder
	}

	reordered, err := s.categories.ReorderChannelCategoriesForManager(ctx, storage.ReorderChannelCategoriesInput{
		WorkspaceID: input.WorkspaceID,
		CallerID:    input.CallerID,
		OrderedIDs:  input.OrderedIDs,
	})
	if err != nil {
		return nil, err
	}
	return reordered, nil
}

// DeleteChannelCategory deletes one category of the workspace.
//
// It never deletes a channel. The channels that were in the category keep
// existing with no category and appear in the virtual "Geral" group on the next
// listing; the database performs that as part of the same DELETE through the
// composite FK's ON DELETE SET NULL, so there is no window where a channel points
// at a category that is gone.
//
// A category that does not exist is an error rather than a silent success: this
// service has no idempotent deletes, and reporting "deleted" for something that
// was never there would hide a client bug.
func (s *ChannelCategoryService) DeleteChannelCategory(ctx context.Context, workspaceID, categoryID, callerID string) error {
	if _, err := requireWorkspaceManager(ctx, s.workspaces, s.members, workspaceID, callerID); err != nil {
		return err
	}
	return s.categories.DeleteChannelCategoryForManager(ctx, workspaceID, categoryID, callerID)
}
