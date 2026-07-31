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
func (f *sidebarFakeMemberStore) GetEligibleDMMember(_ context.Context, _, _ string) (domain.WorkspaceMember, error) {
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
func (f *sidebarFakeMemberStore) ListOnlineChannelMemberProfiles(_ context.Context, _, _ string, _ []string, _ int) (storage.ChannelMemberPage, error) {
	return storage.ChannelMemberPage{}, nil
}
func (f *sidebarFakeMemberStore) SearchDMCandidates(_ context.Context, _, _, _ string, _ int) ([]domain.DMCandidate, error) {
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
	accesses        []storage.VisibleChannelAccess
	err             error
	listAccessCalls int
}

func (f *sidebarFakeChannelStore) CreateCategory(_ context.Context, _ storage.CreateCategoryInput) (domain.ChannelCategory, error) {
	return domain.ChannelCategory{}, nil
}
func (f *sidebarFakeChannelStore) CreateChannel(_ context.Context, _ storage.CreateChannelInput) (domain.Channel, error) {
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
	return nil, nil
}
func (f *sidebarFakeChannelStore) ListVisibleChannelAccessByUser(_ context.Context, _, _ string) ([]storage.VisibleChannelAccess, error) {
	f.listAccessCalls++
	return f.accesses, f.err
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

func (f *sidebarFakeDMStore) CreateDirectConversation(_ context.Context, _ storage.CreateDirectConversationInput) (storage.CreateDirectConversationResult, error) {
	return storage.CreateDirectConversationResult{}, nil
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
func (f *sidebarFakeDMStore) GetDirectCounterpartProfile(_ context.Context, _, _, _ string) (domain.DMDirectProfile, error) {
	return domain.DMDirectProfile{}, nil
}
func (f *sidebarFakeDMStore) ListParticipantProfiles(_ context.Context, _, _ string, _ int) (storage.DMParticipantPage, error) {
	return storage.DMParticipantPage{}, nil
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
	cs interface {
		ListVisibleChannelAccessByUser(context.Context, string, string) ([]storage.VisibleChannelAccess, error)
	},
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
	accesses := []storage.VisibleChannelAccess{
		{Channel: domain.Channel{ID: "ch-1", WorkspaceID: sidebarWsID, Slug: "geral", Type: domain.ChannelTypePublic, IsGeneral: true, Status: domain.ChannelStatusActive}},
		{
			Channel: domain.Channel{ID: "ch-2", WorkspaceID: sidebarWsID, Slug: "eng", Type: domain.ChannelTypePrivate, Status: domain.ChannelStatusActive},
			ChannelMember: &domain.ChannelMember{
				ChannelID: "ch-2", UserID: sidebarUserID, Role: domain.ChannelRoleMember,
			},
		},
	}
	dms := []domain.DMConversationWithParticipantIDs{
		{
			DMConversation:         domain.DMConversation{ID: "dm-1", Type: domain.DMConversationTypeDirect},
			ParticipantIDs:         []string{sidebarUserID, "other-user"},
			CounterpartDisplayName: "Juliane Lino",
		},
	}
	svc := newSidebarService(
		&sidebarFakeWorkspaceStore{workspace: activeWorkspace()},
		&sidebarFakeMemberStore{member: activeMember()},
		&sidebarFakeChannelStore{accesses: accesses},
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
	for _, channel := range data.Channels {
		if !channel.CanWrite {
			t.Fatalf("visible active channel must be writable under the current policy: %+v", channel)
		}
	}
	if len(data.DMs) != 1 {
		t.Fatalf("expected 1 DM, got %d", len(data.DMs))
	}
	// The service must forward the storage-resolved counterpart untouched; it is
	// the handler's job to turn it into a display name.
	if data.DMs[0].CounterpartDisplayName != "Juliane Lino" {
		t.Fatalf("expected counterpart name to reach the caller, got %q", data.DMs[0].CounterpartDisplayName)
	}
}

func TestSidebarService_EmptyChannelsAndDMs_ReturnsEmptySlices(t *testing.T) {
	svc := newSidebarService(
		&sidebarFakeWorkspaceStore{workspace: activeWorkspace()},
		&sidebarFakeMemberStore{member: activeMember()},
		&sidebarFakeChannelStore{accesses: []storage.VisibleChannelAccess{}},
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

func TestSidebarService_InfrastructureErrorsAreWrapped(t *testing.T) {
	storeErr := errors.New("store unavailable")
	tests := []struct {
		name     string
		ws       *sidebarFakeWorkspaceStore
		member   *sidebarFakeMemberStore
		channels *sidebarFakeChannelStore
		dms      *sidebarFakeDMStore
	}{
		{
			name:     "workspace",
			ws:       &sidebarFakeWorkspaceStore{err: storeErr},
			member:   &sidebarFakeMemberStore{},
			channels: &sidebarFakeChannelStore{},
			dms:      &sidebarFakeDMStore{},
		},
		{
			name:     "membership",
			ws:       &sidebarFakeWorkspaceStore{workspace: activeWorkspace()},
			member:   &sidebarFakeMemberStore{err: storeErr},
			channels: &sidebarFakeChannelStore{},
			dms:      &sidebarFakeDMStore{},
		},
		{
			name:     "channels",
			ws:       &sidebarFakeWorkspaceStore{workspace: activeWorkspace()},
			member:   &sidebarFakeMemberStore{member: activeMember()},
			channels: &sidebarFakeChannelStore{err: storeErr},
			dms:      &sidebarFakeDMStore{},
		},
		{
			name:     "direct messages",
			ws:       &sidebarFakeWorkspaceStore{workspace: activeWorkspace()},
			member:   &sidebarFakeMemberStore{member: activeMember()},
			channels: &sidebarFakeChannelStore{},
			dms:      &sidebarFakeDMStore{err: storeErr},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := newSidebarService(tt.ws, tt.member, tt.channels, tt.dms)
			_, err := svc.GetSidebar(context.Background(), sidebarUserID)
			if !errors.Is(err, storeErr) {
				t.Fatalf("expected wrapped store error, got %v", err)
			}
		})
	}
}

func TestSidebarService_NoChannelLeakage_PrivateOnlyIfMember(t *testing.T) {
	// The channel store's ListVisibleChannelAccessByUser is responsible for filtering,
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

func TestSidebarService_CanWriteUsesDomainPolicyWithoutNPlusOne(t *testing.T) {
	store := &sidebarFakeChannelStore{accesses: []storage.VisibleChannelAccess{
		{Channel: domain.Channel{ID: "public", WorkspaceID: sidebarWsID, Type: domain.ChannelTypePublic, Status: domain.ChannelStatusActive}},
		{Channel: domain.Channel{ID: "private", WorkspaceID: sidebarWsID, Type: domain.ChannelTypePrivate, Status: domain.ChannelStatusActive}},
		{
			Channel: domain.Channel{ID: "archived", WorkspaceID: sidebarWsID, Type: domain.ChannelTypePublic, Status: domain.ChannelStatusArchived},
			ChannelMember: &domain.ChannelMember{
				ChannelID: "archived", UserID: sidebarUserID, Role: domain.ChannelRoleMember,
			},
		},
	}}
	svc := newSidebarService(
		&sidebarFakeWorkspaceStore{workspace: activeWorkspace()},
		&sidebarFakeMemberStore{member: activeMember()},
		store,
		&sidebarFakeDMStore{},
	)

	data, err := svc.GetSidebar(context.Background(), sidebarUserID)
	if err != nil {
		t.Fatalf("GetSidebar: %v", err)
	}
	if store.listAccessCalls != 1 {
		t.Fatalf("expected one batched channel access query, got %d", store.listAccessCalls)
	}
	if !data.Channels[0].CanWrite {
		t.Fatal("active public channel must follow CanWriteChannel=true")
	}
	if data.Channels[1].CanWrite {
		t.Fatal("private channel without membership must follow CanWriteChannel=false")
	}
	if data.Channels[2].CanWrite {
		t.Fatal("archived channel must follow CanWriteChannel=false")
	}
}

// capturingChannelStore records the args passed to ListVisibleChannelAccessByUser.
type capturingChannelStore struct {
	onList func(workspaceID, userID string)
}

func (c *capturingChannelStore) CreateCategory(_ context.Context, _ storage.CreateCategoryInput) (domain.ChannelCategory, error) {
	return domain.ChannelCategory{}, nil
}
func (c *capturingChannelStore) CreateChannel(_ context.Context, _ storage.CreateChannelInput) (domain.Channel, error) {
	return domain.Channel{}, nil
}
func (c *capturingChannelStore) CreateChannelForActiveMember(_ context.Context, _ storage.CreateChannelInput) (domain.Channel, error) {
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
	return nil, nil
}
func (c *capturingChannelStore) ListVisibleChannelAccessByUser(_ context.Context, workspaceID, userID string) ([]storage.VisibleChannelAccess, error) {
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

// CanCreateChannel is a compatibility field, not a role lookup: every active
// role sees true, because a returned sidebar already proves the only condition
// channel creation has (BUG #393).
func TestSidebarService_CanCreateChannelIsTrueForEveryActiveRole(t *testing.T) {
	for _, role := range []domain.WorkspaceRole{
		domain.WorkspaceRoleOwner,
		domain.WorkspaceRoleAdmin,
		domain.WorkspaceRoleMember,
		domain.WorkspaceRoleGuest,
	} {
		t.Run(string(role), func(t *testing.T) {
			member := activeMember()
			member.Role = role
			svc := newSidebarService(
				&sidebarFakeWorkspaceStore{workspace: activeWorkspace()},
				&sidebarFakeMemberStore{member: member},
				&sidebarFakeChannelStore{accesses: []storage.VisibleChannelAccess{}},
				&sidebarFakeDMStore{dms: nil},
			)
			data, err := svc.GetSidebar(context.Background(), sidebarUserID)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !data.CanCreateChannel {
				t.Fatalf("CanCreateChannel = false for active %s, want true", role)
			}
		})
	}
}
