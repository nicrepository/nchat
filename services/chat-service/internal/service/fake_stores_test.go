package service_test

import (
	"context"
	"time"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

// fakeWorkspaceStore implements storage.WorkspaceStore.
type fakeWorkspaceStore struct {
	workspace domain.Workspace
	getErr    error
}

func (f *fakeWorkspaceStore) GetDefaultWorkspace(_ context.Context) (domain.Workspace, error) {
	return f.workspace, f.getErr
}
func (f *fakeWorkspaceStore) GetWorkspaceBySlug(_ context.Context, _ string) (domain.Workspace, error) {
	return f.workspace, f.getErr
}

// fakeChannelStore implements storage.ChannelStore.
type fakeChannelStore struct {
	createdCategory domain.ChannelCategory
	createCatErr    error
	createdChannel  domain.Channel
	createChanErr   error
	channel         domain.Channel
	getByIDErr      error
	channels        []domain.Channel
	listErr         error
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
func (f *fakeChannelStore) ListChannelsByWorkspace(_ context.Context, _ string) ([]domain.Channel, error) {
	return f.channels, f.listErr
}

// fakeMemberStore implements storage.MemberStore.
type fakeMemberStore struct {
	workspaceMembers map[string]domain.WorkspaceMember
	channelMembers   map[string]domain.ChannelMember
	addWMErr         error
	addCMErr         error
}

func newFakeMemberStore() *fakeMemberStore {
	return &fakeMemberStore{
		workspaceMembers: make(map[string]domain.WorkspaceMember),
		channelMembers:   make(map[string]domain.ChannelMember),
	}
}

func wmKey(workspaceID, userID string) string { return workspaceID + ":" + userID }
func cmKey(channelID, userID string) string   { return channelID + ":" + userID }

func (f *fakeMemberStore) AddWorkspaceMember(_ context.Context, workspaceID, userID string, role domain.WorkspaceRole) (domain.WorkspaceMember, error) {
	if f.addWMErr != nil {
		return domain.WorkspaceMember{}, f.addWMErr
	}
	key := wmKey(workspaceID, userID)
	if _, ok := f.workspaceMembers[key]; ok {
		return domain.WorkspaceMember{}, domain.ErrAlreadyMember
	}
	m := domain.WorkspaceMember{
		WorkspaceID: workspaceID, UserID: userID,
		Role: role, Status: domain.MemberStatusActive, JoinedAt: time.Now(),
	}
	f.workspaceMembers[key] = m
	return m, nil
}

func (f *fakeMemberStore) GetWorkspaceMember(_ context.Context, workspaceID, userID string) (domain.WorkspaceMember, error) {
	m, ok := f.workspaceMembers[wmKey(workspaceID, userID)]
	if !ok {
		return domain.WorkspaceMember{}, domain.ErrNotFound
	}
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
	m, ok := f.channelMembers[cmKey(channelID, userID)]
	if !ok {
		return domain.ChannelMember{}, domain.ErrNotFound
	}
	return m, nil
}
