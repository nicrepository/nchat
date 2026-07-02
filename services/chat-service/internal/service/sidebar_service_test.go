package service_test

import (
	"context"
	"errors"
	"testing"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/service"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

// ── fake stores ───────────────────────────────────────────────────────────────

type sidebarFakeWorkspaceStore struct {
	workspace domain.Workspace
	err       error
}

func (f *sidebarFakeWorkspaceStore) GetDefaultWorkspace(_ context.Context) (domain.Workspace, error) {
	return f.workspace, f.err
}
func (f *sidebarFakeWorkspaceStore) GetWorkspaceByID(_ context.Context, _ string) (domain.Workspace, error) {
	return domain.Workspace{}, nil
}
func (f *sidebarFakeWorkspaceStore) GetWorkspaceBySlug(_ context.Context, _ string) (domain.Workspace, error) {
	return domain.Workspace{}, nil
}

type sidebarFakeMemberStore struct {
	member domain.WorkspaceMember
	err    error
}

func (f *sidebarFakeMemberStore) AddWorkspaceMember(_ context.Context, _, _ string, _ domain.WorkspaceRole) (domain.WorkspaceMember, error) {
	return domain.WorkspaceMember{}, nil
}
func (f *sidebarFakeMemberStore) ActivateWorkspaceMember(_ context.Context, _, _ string) (domain.WorkspaceMember, error) {
	return domain.WorkspaceMember{}, nil
}
func (f *sidebarFakeMemberStore) GetWorkspaceMember(_ context.Context, _, _ string) (domain.WorkspaceMember, error) {
	return f.member, f.err
}
func (f *sidebarFakeMemberStore) AddChannelMember(_ context.Context, _, _ string, _ domain.ChannelRole) (domain.ChannelMember, error) {
	return domain.ChannelMember{}, nil
}
func (f *sidebarFakeMemberStore) GetChannelMember(_ context.Context, _, _ string) (domain.ChannelMember, error) {
	return domain.ChannelMember{}, nil
}
func (f *sidebarFakeMemberStore) SearchChannelMembers(_ context.Context, _, _, _ string, _ int) ([]domain.MentionCandidate, error) {
	return nil, nil
}
func (f *sidebarFakeMemberStore) RemoveChannelMember(_ context.Context, _, _, _ string) error {
	return nil
}
func (f *sidebarFakeMemberStore) EnsureGeneralMembership(_ context.Context, _, _ string) error {
	return nil
}
func (f *sidebarFakeMemberStore) SyncGeneralMemberships(_ context.Context, _ string) (int64, error) {
	return 0, nil
}

type sidebarFakeChannelStore struct {
	channels []domain.Channel
	err      error
}

func (f *sidebarFakeChannelStore) CreateCategory(_ context.Context, _ storage.CreateCategoryInput) (domain.ChannelCategory, error) {
	return domain.ChannelCategory{}, nil
}
func (f *sidebarFakeChannelStore) CreateChannel(_ context.Context, _ storage.CreateChannelInput) (domain.Channel, error) {
	return domain.Channel{}, nil
}
func (f *sidebarFakeChannelStore) CreateChannelWithMember(_ context.Context, _ storage.CreateChannelInput, _ string, _ domain.ChannelRole) (domain.Channel, error) {
	return domain.Channel{}, nil
}
func (f *sidebarFakeChannelStore) GetCategoryByIDInWorkspace(_ context.Context, _, _ string) (domain.ChannelCategory, error) {
	return domain.ChannelCategory{}, nil
}
func (f *sidebarFakeChannelStore) GetChannelByID(_ context.Context, _ string) (domain.Channel, error) {
	return domain.Channel{}, nil
}
func (f *sidebarFakeChannelStore) GetChannelByIDInWorkspace(_ context.Context, _, _ string) (domain.Channel, error) {
	return domain.Channel{}, nil
}
func (f *sidebarFakeChannelStore) GetVisibleChannelByID(_ context.Context, _, _, _ string) (domain.Channel, error) {
	return domain.Channel{}, nil
}
func (f *sidebarFakeChannelStore) GetVisibleChannelBySlug(_ context.Context, _, _, _ string) (domain.Channel, error) {
	return domain.Channel{}, nil
}
func (f *sidebarFakeChannelStore) ListChannelsByWorkspace(_ context.Context, _ string) ([]domain.Channel, error) {
	return nil, nil
}
func (f *sidebarFakeChannelStore) ListVisibleChannelsByUser(_ context.Context, _, _ string) ([]domain.Channel, error) {
	return f.channels, f.err
}
func (f *sidebarFakeChannelStore) UpdateChannel(_ context.Context, _ storage.UpdateChannelInput) (domain.Channel, error) {
	return domain.Channel{}, nil
}
func (f *sidebarFakeChannelStore) ArchiveChannel(_ context.Context, _, _ string) (domain.Channel, error) {
	return domain.Channel{}, nil
}

type sidebarFakeDMStore struct {
	dms []domain.DMConversationWithParticipantIDs
	err error
}

func (f *sidebarFakeDMStore) CreateDirectConversation(_ context.Context, _ storage.CreateDirectConversationInput) (domain.DMConversation, error) {
	return domain.DMConversation{}, nil
}
func (f *sidebarFakeDMStore) CreateGroupConversation(_ context.Context, _ storage.CreateGroupConversationInput) (domain.DMConversation, error) {
	return domain.DMConversation{}, nil
}
func (f *sidebarFakeDMStore) ListVisibleConversationsByUser(_ context.Context, _, _ string) ([]domain.DMConversation, error) {
	return nil, nil
}
func (f *sidebarFakeDMStore) ListVisibleConversationsWithParticipantIDs(_ context.Context, _, _ string) ([]domain.DMConversationWithParticipantIDs, error) {
	return f.dms, f.err
}
func (f *sidebarFakeDMStore) GetVisibleConversationByID(_ context.Context, _, _, _ string) (domain.DMConversation, error) {
	return domain.DMConversation{}, nil
}

// ── helpers ───────────────────────────────────────────────────────────────────

const (
	sidebarUserID = "user-sidebar-test"
	sidebarWsID   = "ws-sidebar-1"
)

func activeWorkspace() domain.Workspace {
	return domain.Workspace{ID: sidebarWsID, Name: "NIC Labs", Slug: "default", Status: domain.WorkspaceStatusActive}
}

func activeMember() domain.WorkspaceMember {
	return domain.WorkspaceMember{WorkspaceID: sidebarWsID, UserID: sidebarUserID, Status: domain.MemberStatusActive}
}

func newSidebarService(
	ws storage.WorkspaceStore,
	ms storage.MemberStore,
	cs storage.ChannelStore,
	ds storage.DMStore,
) *service.SidebarService {
	return service.NewSidebarService(ws, cs, ms, ds) // real API: workspaces, channels, members, dms
}

// ── tests ─────────────────────────────────────────────────────────────────────

func TestSidebarService_WorkspaceNotFound_ReturnsNotFound(t *testing.T) {
	svc := newSidebarService(
		&sidebarFakeWorkspaceStore{err: domain.ErrNotFound},
		&sidebarFakeMemberStore{},
		&sidebarFakeChannelStore{},
		&sidebarFakeDMStore{},
	)
	_, err := svc.GetSidebar(context.Background(), sidebarUserID)
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestSidebarService_WorkspaceDisabled_ReturnsForbidden(t *testing.T) {
	ws := domain.Workspace{ID: sidebarWsID, Name: "NIC Labs", Slug: "default", Status: domain.WorkspaceStatusDisabled}
	svc := newSidebarService(
		&sidebarFakeWorkspaceStore{workspace: ws},
		&sidebarFakeMemberStore{},
		&sidebarFakeChannelStore{},
		&sidebarFakeDMStore{},
	)
	_, err := svc.GetSidebar(context.Background(), sidebarUserID)
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("expected ErrForbidden, got %v", err)
	}
}

func TestSidebarService_UserNotMember_ReturnsForbidden(t *testing.T) {
	svc := newSidebarService(
		&sidebarFakeWorkspaceStore{workspace: activeWorkspace()},
		&sidebarFakeMemberStore{err: domain.ErrNotFound},
		&sidebarFakeChannelStore{},
		&sidebarFakeDMStore{},
	)
	_, err := svc.GetSidebar(context.Background(), sidebarUserID)
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("expected ErrForbidden, got %v", err)
	}
}

func TestSidebarService_SuspendedMember_ReturnsForbidden(t *testing.T) {
	suspended := domain.WorkspaceMember{WorkspaceID: sidebarWsID, UserID: sidebarUserID, Status: domain.MemberStatusSuspended}
	svc := newSidebarService(
		&sidebarFakeWorkspaceStore{workspace: activeWorkspace()},
		&sidebarFakeMemberStore{member: suspended},
		&sidebarFakeChannelStore{},
		&sidebarFakeDMStore{},
	)
	_, err := svc.GetSidebar(context.Background(), sidebarUserID)
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("expected ErrForbidden for suspended member, got %v", err)
	}
}

func TestSidebarService_LeftMember_ReturnsForbidden(t *testing.T) {
	left := domain.WorkspaceMember{WorkspaceID: sidebarWsID, UserID: sidebarUserID, Status: domain.MemberStatusLeft}
	svc := newSidebarService(
		&sidebarFakeWorkspaceStore{workspace: activeWorkspace()},
		&sidebarFakeMemberStore{member: left},
		&sidebarFakeChannelStore{},
		&sidebarFakeDMStore{},
	)
	_, err := svc.GetSidebar(context.Background(), sidebarUserID)
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("expected ErrForbidden for left member, got %v", err)
	}
}

func TestSidebarService_ActiveMember_ReturnsChannelsAndDMs(t *testing.T) {
	channels := []domain.Channel{
		{ID: "ch-1", Slug: "geral", Type: domain.ChannelTypePublic, IsGeneral: true, Status: domain.ChannelStatusActive},
		{ID: "ch-2", Slug: "eng", Type: domain.ChannelTypePrivate, Status: domain.ChannelStatusActive},
	}
	dms := []domain.DMConversationWithParticipantIDs{
		{
			DMConversation: domain.DMConversation{ID: "dm-1", Type: domain.DMConversationTypeDirect},
			ParticipantIDs: []string{sidebarUserID, "other-user"},
		},
	}
	svc := newSidebarService(
		&sidebarFakeWorkspaceStore{workspace: activeWorkspace()},
		&sidebarFakeMemberStore{member: activeMember()},
		&sidebarFakeChannelStore{channels: channels},
		&sidebarFakeDMStore{dms: dms},
	)
	data, err := svc.GetSidebar(context.Background(), sidebarUserID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if data.Workspace.ID != sidebarWsID {
		t.Fatalf("expected workspace %q, got %q", sidebarWsID, data.Workspace.ID)
	}
	if len(data.Channels) != 2 {
		t.Fatalf("expected 2 channels, got %d", len(data.Channels))
	}
	if len(data.DMs) != 1 {
		t.Fatalf("expected 1 DM, got %d", len(data.DMs))
	}
}

func TestSidebarService_EmptyChannelsAndDMs_ReturnsEmptySlices(t *testing.T) {
	svc := newSidebarService(
		&sidebarFakeWorkspaceStore{workspace: activeWorkspace()},
		&sidebarFakeMemberStore{member: activeMember()},
		&sidebarFakeChannelStore{channels: []domain.Channel{}},
		&sidebarFakeDMStore{dms: []domain.DMConversationWithParticipantIDs{}},
	)
	data, err := svc.GetSidebar(context.Background(), sidebarUserID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if data.Channels == nil {
		t.Fatal("expected non-nil channels slice")
	}
	if data.DMs == nil {
		t.Fatal("expected non-nil DMs slice")
	}
}

func TestSidebarService_EmptyUserID_ReturnsInvalidInput(t *testing.T) {
	svc := newSidebarService(
		&sidebarFakeWorkspaceStore{workspace: activeWorkspace()},
		&sidebarFakeMemberStore{member: activeMember()},
		&sidebarFakeChannelStore{},
		&sidebarFakeDMStore{},
	)
	_, err := svc.GetSidebar(context.Background(), "")
	if err == nil {
		t.Fatal("expected error for empty userID, got nil")
	}
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput, got %v", err)
	}
}

func TestSidebarService_NoChannelLeakage_PrivateOnlyIfMember(t *testing.T) {
	// The channel store's ListVisibleChannelsByUser is responsible for filtering,
	// but the service must pass both workspaceID and userID so the store can apply
	// visibility rules. We verify the service does not bypass these parameters.
	var capturedWorkspaceID, capturedUserID string

	cs := &capturingChannelStore{
		onList: func(wsID, uID string) {
			capturedWorkspaceID = wsID
			capturedUserID = uID
		},
	}

	svc := newSidebarService(
		&sidebarFakeWorkspaceStore{workspace: activeWorkspace()},
		&sidebarFakeMemberStore{member: activeMember()},
		cs,
		&sidebarFakeDMStore{},
	)
	_, _ = svc.GetSidebar(context.Background(), sidebarUserID)

	if capturedWorkspaceID != sidebarWsID {
		t.Fatalf("expected workspaceID %q passed to channels, got %q", sidebarWsID, capturedWorkspaceID)
	}
	if capturedUserID != sidebarUserID {
		t.Fatalf("expected userID %q passed to channels, got %q", sidebarUserID, capturedUserID)
	}
}

// capturingChannelStore records the args passed to ListVisibleChannelsByUser.
type capturingChannelStore struct {
	onList func(workspaceID, userID string)
}

func (c *capturingChannelStore) CreateCategory(_ context.Context, _ storage.CreateCategoryInput) (domain.ChannelCategory, error) {
	return domain.ChannelCategory{}, nil
}
func (c *capturingChannelStore) CreateChannel(_ context.Context, _ storage.CreateChannelInput) (domain.Channel, error) {
	return domain.Channel{}, nil
}
func (c *capturingChannelStore) CreateChannelWithMember(_ context.Context, _ storage.CreateChannelInput, _ string, _ domain.ChannelRole) (domain.Channel, error) {
	return domain.Channel{}, nil
}
func (c *capturingChannelStore) GetCategoryByIDInWorkspace(_ context.Context, _, _ string) (domain.ChannelCategory, error) {
	return domain.ChannelCategory{}, nil
}
func (c *capturingChannelStore) GetChannelByID(_ context.Context, _ string) (domain.Channel, error) {
	return domain.Channel{}, nil
}
func (c *capturingChannelStore) GetChannelByIDInWorkspace(_ context.Context, _, _ string) (domain.Channel, error) {
	return domain.Channel{}, nil
}
func (c *capturingChannelStore) GetVisibleChannelByID(_ context.Context, _, _, _ string) (domain.Channel, error) {
	return domain.Channel{}, nil
}
func (c *capturingChannelStore) GetVisibleChannelBySlug(_ context.Context, _, _, _ string) (domain.Channel, error) {
	return domain.Channel{}, nil
}
func (c *capturingChannelStore) ListChannelsByWorkspace(_ context.Context, _ string) ([]domain.Channel, error) {
	return nil, nil
}
func (c *capturingChannelStore) ListVisibleChannelsByUser(_ context.Context, workspaceID, userID string) ([]domain.Channel, error) {
	if c.onList != nil {
		c.onList(workspaceID, userID)
	}
	return nil, nil
}
func (c *capturingChannelStore) UpdateChannel(_ context.Context, _ storage.UpdateChannelInput) (domain.Channel, error) {
	return domain.Channel{}, nil
}
func (c *capturingChannelStore) ArchiveChannel(_ context.Context, _, _ string) (domain.Channel, error) {
	return domain.Channel{}, nil
}
