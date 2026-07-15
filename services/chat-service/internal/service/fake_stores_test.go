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
	createdCategory        domain.ChannelCategory
	createCatErr           error
	category               domain.ChannelCategory
	getCategoryErr         error
	createdChannel         domain.Channel
	createChanErr          error
	channel                domain.Channel
	visibleChannel         domain.Channel
	updatedChannel         domain.Channel
	archivedChannel        domain.Channel
	getByIDErr             error
	getInWorkspaceErr      error
	getVisibleErr          error
	getVisibleBySlugErr    error
	updateErr              error
	archiveErr             error
	channels               []domain.Channel
	listErr                error
	visibleChannels        []domain.Channel
	listVisibleErr         error
	lastCreateInput        storage.CreateChannelInput
	lastCreateMemberUserID string
	lastUpdateInput        storage.UpdateChannelInput
	listCalls              int
	listVisibleCalls       int
	getVisibleByIDCalls    int
	getVisibleBySlugCalls  int
	createWithMemberCalls  int
	archiveCalls           int
}

func (f *fakeChannelStore) CreateCategory(_ context.Context, _ storage.CreateCategoryInput) (domain.ChannelCategory, error) {
	return f.createdCategory, f.createCatErr
}
func (f *fakeChannelStore) CreateChannel(_ context.Context, input storage.CreateChannelInput) (domain.Channel, error) {
	f.lastCreateInput = input
	return f.createdChannel, f.createChanErr
}
func (f *fakeChannelStore) CreateChannelWithMember(_ context.Context, input storage.CreateChannelInput, userID string, _ domain.ChannelRole) (domain.Channel, error) {
	f.createWithMemberCalls++
	f.lastCreateInput = input
	f.lastCreateMemberUserID = userID
	return f.createdChannel, f.createChanErr
}
func (f *fakeChannelStore) GetCategoryByIDInWorkspace(_ context.Context, workspaceID, id string) (domain.ChannelCategory, error) {
	if f.getCategoryErr != nil {
		return domain.ChannelCategory{}, f.getCategoryErr
	}
	if f.category.ID != "" {
		if f.category.ID != id || f.category.WorkspaceID != workspaceID {
			return domain.ChannelCategory{}, domain.ErrNotFound
		}
		return f.category, nil
	}
	return domain.ChannelCategory{ID: id, WorkspaceID: workspaceID}, nil
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
func (f *fakeChannelStore) GetVisibleChannelByID(_ context.Context, workspaceID, id, _ string) (domain.Channel, error) {
	f.getVisibleByIDCalls++
	if f.getVisibleErr != nil {
		return domain.Channel{}, f.getVisibleErr
	}
	ch := f.visibleChannel
	if ch.ID == "" {
		ch = f.channel
	}
	if ch.ID != id || ch.WorkspaceID != workspaceID || ch.Status != domain.ChannelStatusActive {
		return domain.Channel{}, domain.ErrNotFound
	}
	return ch, nil
}
func (f *fakeChannelStore) GetVisibleChannelBySlug(_ context.Context, workspaceID, slug, _ string) (domain.Channel, error) {
	f.getVisibleBySlugCalls++
	if f.getVisibleBySlugErr != nil {
		return domain.Channel{}, f.getVisibleBySlugErr
	}
	if f.getVisibleErr != nil {
		return domain.Channel{}, f.getVisibleErr
	}
	ch := f.visibleChannel
	if ch.ID == "" {
		ch = f.channel
	}
	if ch.Slug != slug || ch.WorkspaceID != workspaceID || ch.Status != domain.ChannelStatusActive {
		return domain.Channel{}, domain.ErrNotFound
	}
	return ch, nil
}
func (f *fakeChannelStore) ListChannelsByWorkspace(_ context.Context, _ string) ([]domain.Channel, error) {
	f.listCalls++
	return f.channels, f.listErr
}
func (f *fakeChannelStore) ListVisibleChannelsByUser(_ context.Context, _, _ string) ([]domain.Channel, error) {
	f.listVisibleCalls++
	return f.visibleChannels, f.listVisibleErr
}
func (f *fakeChannelStore) UpdateChannel(_ context.Context, input storage.UpdateChannelInput) (domain.Channel, error) {
	f.lastUpdateInput = input
	if f.updateErr != nil {
		return domain.Channel{}, f.updateErr
	}
	if f.updatedChannel.ID != "" {
		return f.updatedChannel, nil
	}
	return domain.Channel{
		ID:          input.ChannelID,
		WorkspaceID: input.WorkspaceID,
		CategoryID:  input.CategoryID,
		Slug:        input.Slug,
		DisplayName: input.DisplayName,
		Type:        input.Type,
		Status:      domain.ChannelStatusActive,
		Position:    input.Position,
	}, nil
}
func (f *fakeChannelStore) ArchiveChannel(_ context.Context, workspaceID, channelID string) (domain.Channel, error) {
	f.archiveCalls++
	if f.archiveErr != nil {
		return domain.Channel{}, f.archiveErr
	}
	if f.archivedChannel.ID != "" {
		return f.archivedChannel, nil
	}
	return domain.Channel{ID: channelID, WorkspaceID: workspaceID, Status: domain.ChannelStatusArchived}, nil
}

// fakeMemberStore implements storage.MemberStore.
type fakeMemberStore struct {
	workspaceMembers  map[string]domain.WorkspaceMember
	channelMembers    map[string]domain.ChannelMember
	workspaceStatus   map[string]domain.WorkspaceStatus
	generalChannels   map[string]string
	addWMErr          error
	addCMErr          error
	removeCMErr       error
	getWMErr          error
	getCMErr          error
	mentionCandidates []domain.MentionCandidate
	mentionErr        error
	dmCandidates      []domain.DMCandidate
	dmCandidateErr    error
	dmCandidateQuery  string
	dmCandidateLimit  int
	getEligibleCalls  int
}

func (f *fakeMemberStore) SearchChannelMembers(_ context.Context, _, _, _ string, _ int) ([]domain.MentionCandidate, error) {
	return f.mentionCandidates, f.mentionErr
}

func (f *fakeMemberStore) SearchDMCandidates(_ context.Context, _, _, query string, limit int) ([]domain.DMCandidate, error) {
	f.dmCandidateQuery = query
	f.dmCandidateLimit = limit
	return f.dmCandidates, f.dmCandidateErr
}

func (f *fakeMemberStore) GetEligibleDMMember(_ context.Context, workspaceID, userID string) (domain.WorkspaceMember, error) {
	f.getEligibleCalls++
	if f.getWMErr != nil {
		return domain.WorkspaceMember{}, f.getWMErr
	}
	if status, ok := f.workspaceStatus[workspaceID]; ok && status != domain.WorkspaceStatusActive {
		return domain.WorkspaceMember{}, domain.ErrNotFound
	}
	member, ok := f.workspaceMembers[wmKey(workspaceID, userID)]
	if !ok || member.Status != domain.MemberStatusActive || member.WorkspaceID != workspaceID {
		return domain.WorkspaceMember{}, domain.ErrNotFound
	}
	return member, nil
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

func (f *fakeMemberStore) RemoveChannelMember(_ context.Context, _, channelID, userID string) error {
	if f.removeCMErr != nil {
		return f.removeCMErr
	}
	delete(f.channelMembers, cmKey(channelID, userID))
	return nil
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
