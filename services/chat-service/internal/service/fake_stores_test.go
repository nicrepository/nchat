package service_test

import (
	"context"
	"time"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

// fakeWorkspaceStore implements storage.WorkspaceStore.
type fakeWorkspaceStore struct {
	workspace    domain.Workspace
	workspaces   map[string]domain.Workspace
	getErr       error
	getByIDErr   error
	getByIDCalls int
}

func (f *fakeWorkspaceStore) GetDefaultWorkspace(_ context.Context) (domain.Workspace, error) {
	return f.workspace, f.getErr
}
func (f *fakeWorkspaceStore) GetWorkspaceBySlug(_ context.Context, _ string) (domain.Workspace, error) {
	return f.workspace, f.getErr
}
func (f *fakeWorkspaceStore) GetWorkspaceByID(_ context.Context, id string) (domain.Workspace, error) {
	f.getByIDCalls++
	if f.getByIDErr != nil {
		return domain.Workspace{}, f.getByIDErr
	}
	if f.workspaces != nil {
		workspace, ok := f.workspaces[id]
		if !ok {
			return domain.Workspace{}, domain.ErrNotFound
		}
		return workspace, nil
	}
	if f.workspace.ID == id {
		return f.workspace, nil
	}
	return domain.Workspace{}, domain.ErrNotFound
}

// fakeChannelStore implements storage.ChannelStore.
type fakeChannelStore struct {
	createdCategory   domain.ChannelCategory
	createCatErr      error
	createdChannel    domain.Channel
	createChanErr     error
	channel           domain.Channel
	getByIDErr        error
	getInWorkspaceErr error
	channels          []domain.Channel
	listErr           error
	visibleChannels   []domain.Channel
	listVisibleErr    error
	listCalls         int
	listVisibleCalls  int
}

func (f *fakeChannelStore) CreateCategory(_ context.Context, _ storage.CreateCategoryInput) (domain.ChannelCategory, error) {
	return f.createdCategory, f.createCatErr
}
func (f *fakeChannelStore) CreateChannel(_ context.Context, _ storage.CreateChannelInput) (domain.Channel, error) {
	return f.createdChannel, f.createChanErr
}
func (f *fakeChannelStore) GetChannelByID(_ context.Context, _ string) (domain.Channel, error) {
	return f.channel, f.getByIDErr
}
func (f *fakeChannelStore) GetChannelByIDInWorkspace(_ context.Context, workspaceID, id string) (domain.Channel, error) {
	if f.getInWorkspaceErr != nil {
		return domain.Channel{}, f.getInWorkspaceErr
	}
	if f.getByIDErr != nil {
		return domain.Channel{}, f.getByIDErr
	}
	if f.channel.ID != id || f.channel.WorkspaceID != workspaceID || f.channel.Status != domain.ChannelStatusActive {
		return domain.Channel{}, domain.ErrNotFound
	}
	return f.channel, nil
}
func (f *fakeChannelStore) ListChannelsByWorkspace(_ context.Context, _ string) ([]domain.Channel, error) {
	f.listCalls++
	return f.channels, f.listErr
}
func (f *fakeChannelStore) ListVisibleChannelsByUser(_ context.Context, _, _ string) ([]domain.Channel, error) {
	f.listVisibleCalls++
	return f.visibleChannels, f.listVisibleErr
}

// fakeMemberStore implements storage.MemberStore.
type fakeMemberStore struct {
	workspaceMembers map[string]domain.WorkspaceMember
	channelMembers   map[string]domain.ChannelMember
	workspaceStatus  map[string]domain.WorkspaceStatus
	generalChannels  map[string]string
	addWMErr         error
	addCMErr         error
	getWMErr         error
	getCMErr         error
}

func newFakeMemberStore() *fakeMemberStore {
	return &fakeMemberStore{
		workspaceMembers: make(map[string]domain.WorkspaceMember),
		channelMembers:   make(map[string]domain.ChannelMember),
		workspaceStatus:  make(map[string]domain.WorkspaceStatus),
		generalChannels:  make(map[string]string),
	}
}

func wmKey(workspaceID, userID string) string { return workspaceID + ":" + userID }
func cmKey(channelID, userID string) string   { return channelID + ":" + userID }

func (f *fakeMemberStore) AddWorkspaceMember(_ context.Context, workspaceID, userID string, role domain.WorkspaceRole) (domain.WorkspaceMember, error) {
	if f.addWMErr != nil {
		return domain.WorkspaceMember{}, f.addWMErr
	}
	if err := f.requireActiveWorkspace(workspaceID); err != nil {
		return domain.WorkspaceMember{}, err
	}
	key := wmKey(workspaceID, userID)
	if existing, ok := f.workspaceMembers[key]; ok {
		if existing.Status == domain.MemberStatusActive {
			if err := f.addGeneralMembership(workspaceID, userID); err != nil {
				return domain.WorkspaceMember{}, err
			}
		}
		return domain.WorkspaceMember{}, domain.ErrAlreadyMember
	}
	if err := f.addGeneralMembership(workspaceID, userID); err != nil {
		return domain.WorkspaceMember{}, err
	}
	m := domain.WorkspaceMember{
		WorkspaceID: workspaceID, UserID: userID,
		Role: role, Status: domain.MemberStatusActive, JoinedAt: time.Now(),
	}
	f.workspaceMembers[key] = m
	return m, nil
}

func (f *fakeMemberStore) GetWorkspaceMember(_ context.Context, workspaceID, userID string) (domain.WorkspaceMember, error) {
	if f.getWMErr != nil {
		return domain.WorkspaceMember{}, f.getWMErr
	}
	m, ok := f.workspaceMembers[wmKey(workspaceID, userID)]
	if !ok {
		return domain.WorkspaceMember{}, domain.ErrNotFound
	}
	return m, nil
}

func (f *fakeMemberStore) ActivateWorkspaceMember(_ context.Context, workspaceID, userID string) (domain.WorkspaceMember, error) {
	if err := f.requireActiveWorkspace(workspaceID); err != nil {
		return domain.WorkspaceMember{}, err
	}
	key := wmKey(workspaceID, userID)
	m, ok := f.workspaceMembers[key]
	if !ok {
		return domain.WorkspaceMember{}, domain.ErrNotFound
	}
	previous := m
	m.Status = domain.MemberStatusActive
	if err := f.addGeneralMembership(workspaceID, userID); err != nil {
		f.workspaceMembers[key] = previous
		return domain.WorkspaceMember{}, err
	}
	f.workspaceMembers[key] = m
	return m, nil
}

func (f *fakeMemberStore) AddChannelMember(_ context.Context, channelID, userID string, role domain.ChannelRole) (domain.ChannelMember, error) {
	if f.addCMErr != nil {
		return domain.ChannelMember{}, f.addCMErr
	}
	key := cmKey(channelID, userID)
	if _, ok := f.channelMembers[key]; ok {
		return domain.ChannelMember{}, domain.ErrAlreadyMember
	}
	m := domain.ChannelMember{ChannelID: channelID, UserID: userID, Role: role, JoinedAt: time.Now()}
	f.channelMembers[key] = m
	return m, nil
}

func (f *fakeMemberStore) GetChannelMember(_ context.Context, channelID, userID string) (domain.ChannelMember, error) {
	if f.getCMErr != nil {
		return domain.ChannelMember{}, f.getCMErr
	}
	m, ok := f.channelMembers[cmKey(channelID, userID)]
	if !ok {
		return domain.ChannelMember{}, domain.ErrNotFound
	}
	return m, nil
}

func (f *fakeMemberStore) EnsureGeneralMembership(_ context.Context, workspaceID, userID string) error {
	if err := f.requireActiveWorkspace(workspaceID); err != nil {
		return err
	}
	m, ok := f.workspaceMembers[wmKey(workspaceID, userID)]
	if !ok {
		return domain.ErrForbidden
	}
	if m.Status != domain.MemberStatusActive {
		return domain.ErrMemberInactive
	}
	return f.addGeneralMembership(workspaceID, userID)
}

func (f *fakeMemberStore) SyncGeneralMemberships(_ context.Context, workspaceID string) (int64, error) {
	if err := f.requireActiveWorkspace(workspaceID); err != nil {
		return 0, err
	}
	channelID, ok := f.generalChannels[workspaceID]
	if !ok {
		return 0, domain.ErrGeneralChannelMissing
	}
	if f.addCMErr != nil {
		return 0, f.addCMErr
	}
	var inserted int64
	for _, m := range f.workspaceMembers {
		if m.WorkspaceID != workspaceID || m.Status != domain.MemberStatusActive {
			continue
		}
		key := cmKey(channelID, m.UserID)
		if _, ok := f.channelMembers[key]; ok {
			continue
		}
		f.channelMembers[key] = domain.ChannelMember{
			ChannelID: channelID, UserID: m.UserID, Role: domain.ChannelRoleMember, JoinedAt: time.Now(),
		}
		inserted++
	}
	return inserted, nil
}

func (f *fakeMemberStore) requireActiveWorkspace(workspaceID string) error {
	if status, ok := f.workspaceStatus[workspaceID]; ok && status != domain.WorkspaceStatusActive {
		return domain.ErrForbidden
	}
	return nil
}

func (f *fakeMemberStore) addGeneralMembership(workspaceID, userID string) error {
	channelID, ok := f.generalChannels[workspaceID]
	if !ok {
		return domain.ErrGeneralChannelMissing
	}
	if f.addCMErr != nil {
		return f.addCMErr
	}
	key := cmKey(channelID, userID)
	if _, ok := f.channelMembers[key]; ok {
		return nil
	}
	f.channelMembers[key] = domain.ChannelMember{
		ChannelID: channelID, UserID: userID, Role: domain.ChannelRoleMember, JoinedAt: time.Now(),
	}
	return nil
}
