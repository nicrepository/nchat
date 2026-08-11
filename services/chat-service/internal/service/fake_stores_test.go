package service_test

import (
	"context"
	"sort"
	"strings"
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
	lastSeededMemberUserID string
	lastUpdateInput        storage.UpdateChannelInput
	listCalls              int
	listVisibleCalls       int
	getVisibleByIDCalls    int
	getVisibleBySlugCalls  int
	creatorMembershipSeeds int
	archiveCalls           int
}

func (f *fakeChannelStore) CreateCategory(_ context.Context, _ storage.CreateCategoryInput) (domain.ChannelCategory, error) {
	return f.createdCategory, f.createCatErr
}
func (f *fakeChannelStore) CreateChannel(_ context.Context, input storage.CreateChannelInput) (domain.Channel, error) {
	f.lastCreateInput = input
	return f.createdChannel, f.createChanErr
}

// CreateChannelForActiveMember records what the service asked for, so a test can
// tell a public creation from a private one and see the actor it recorded. The
// atomicity this method carries in the real store cannot be faked here; it is
// proved against PostgreSQL in storage.
func (f *fakeChannelStore) CreateChannelForActiveMember(_ context.Context, input storage.CreateChannelInput) (domain.Channel, error) {
	f.lastCreateInput = input
	if input.EnsureCreatorMemberRole != "" {
		f.creatorMembershipSeeds++
		f.lastSeededMemberUserID = input.CreatedBy
	}
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
	getCMCalls        int
	mentionCandidates []domain.MentionCandidate
	mentionErr        error
	dmCandidates      []domain.DMCandidate
	dmCandidateErr    error
	dmCandidateQuery  string
	dmCandidateLimit  int
	getEligibleCalls  int
	// ineligibleAccounts models what the GetEligibleDMMember query enforces with
	// its auth.users join: a workspace membership row outlives the account, so a
	// suspended or deleted user is still an active member here yet is not
	// eligible for a DM.
	ineligibleAccounts map[string]struct{}

	memberProfiles     storage.ChannelMemberPage
	memberProfilesErr  error
	memberProfileCalls []memberProfileCall

	candidateErr           error
	candidateCalls         []candidateSearchCall
	addCMsErr              error
	addChannelMembersCalls []addChannelMembersCall
}

// addChannelMembersCall records what the service handed the store, so a test can
// assert the IDs were canonicalised, de-duplicated and workspace-scoped before
// they ever reached SQL.
// candidateSearchCall records what the service handed the store, so a test can
// assert the target and the server-derived actor reached SQL.
type candidateSearchCall struct {
	WorkspaceID string
	TargetID    string
	CallerID    string
	Query       string
	Limit       int
}

type addChannelMembersCall struct {
	WorkspaceID string
	ChannelID   string
	CallerID    string
	UserIDs     []string
}

// memberProfileCall records what the service asked the store for, so a test can
// assert that the presence snapshot reached the query rather than being applied
// somewhere after the limit.
type memberProfileCall struct {
	workspaceID   string
	channelID     string
	onlineUserIDs []string
	limit         int
}

func (f *fakeMemberStore) SearchChannelMembers(_ context.Context, _, _, _ string, _ int) ([]domain.MentionCandidate, error) {
	return f.mentionCandidates, f.mentionErr
}

// ListOnlineChannelMemberProfiles models the store contract faithfully: it
// intersects the channel's members with the presence snapshot FIRST and only
// then sorts and truncates. A fake that limited before filtering would hide the
// very defect issue #435 is about.
func (f *fakeMemberStore) ListOnlineChannelMemberProfiles(
	_ context.Context, workspaceID, channelID string, onlineUserIDs []string, limit int,
) (storage.ChannelMemberPage, error) {
	f.memberProfileCalls = append(f.memberProfileCalls, memberProfileCall{
		workspaceID:   workspaceID,
		channelID:     channelID,
		onlineUserIDs: append([]string(nil), onlineUserIDs...),
		limit:         limit,
	})
	if f.memberProfilesErr != nil {
		return storage.ChannelMemberPage{}, f.memberProfilesErr
	}
	if limit <= 0 || limit > domain.MaxChannelDetailsMembers {
		limit = domain.MaxChannelDetailsMembers
	}
	online := map[string]struct{}{}
	for _, userID := range onlineUserIDs {
		online[userID] = struct{}{}
	}
	matched := make([]domain.ChannelMemberProfile, 0, len(f.memberProfiles.Online))
	for _, member := range f.memberProfiles.Online {
		if _, ok := online[member.UserID]; ok {
			matched = append(matched, member)
		}
	}
	sort.Slice(matched, func(i, j int) bool {
		left, right := strings.ToLower(matched[i].DisplayName), strings.ToLower(matched[j].DisplayName)
		if left != right {
			return left < right
		}
		return matched[i].UserID < matched[j].UserID
	})
	page := storage.ChannelMemberPage{
		OnlineCount: len(matched),
		TotalCount:  f.memberProfiles.TotalCount,
	}
	if len(matched) > limit {
		matched = matched[:limit]
	}
	page.Online = matched
	return page, nil
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
	if _, ineligible := f.ineligibleAccounts[userID]; ineligible {
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

		ineligibleAccounts: make(map[string]struct{}),
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
			if err := f.addGeneralMembership(existing); err != nil {
				return domain.WorkspaceMember{}, err
			}
		}
		return domain.WorkspaceMember{}, domain.ErrAlreadyMember
	}
	// The member is built before the #geral join because the real statement
	// reads the role off the row it just inserted, and the role decides.
	m := domain.WorkspaceMember{
		WorkspaceID: workspaceID, UserID: userID,
		Role: role, Status: domain.MemberStatusActive, JoinedAt: time.Now(),
	}
	if err := f.addGeneralMembership(m); err != nil {
		return domain.WorkspaceMember{}, err
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
	if err := f.addGeneralMembership(m); err != nil {
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

// AddChannelMembers models the real statement's two properties that matter to
// the service: it is all-or-nothing, and an already-present member is counted
// rather than treated as an error.
//
// Eligibility mirrors what the SQL join enforces — an active workspace
// membership whose account is not in ineligibleAccounts — so a service test can
// exercise the "one user is not eligible, nothing is written" path without a
// database. addCMsErr lets a test force a storage failure instead.
func (f *fakeMemberStore) AddChannelMembers(
	_ context.Context, workspaceID, channelID, callerID string, userIDs []string,
) (storage.AddMembersResult, error) {
	f.addChannelMembersCalls = append(f.addChannelMembersCalls, addChannelMembersCall{
		WorkspaceID: workspaceID, ChannelID: channelID, CallerID: callerID,
		UserIDs: append([]string(nil), userIDs...),
	})
	if f.addCMsErr != nil {
		return storage.AddMembersResult{}, f.addCMsErr
	}
	// Models the transactional re-check the real store performs: the actor must
	// still hold the capability at write time, so a test that revokes the role
	// between the service check and here sees the write refused. It asks the
	// same domain predicate the store's SQL role list restates, so the two
	// cannot drift as RF-74 widened that list.
	actor, ok := f.workspaceMembers[wmKey(workspaceID, callerID)]
	if !ok || !domain.CanManageChannelMembers(&actor) {
		return storage.AddMembersResult{}, domain.ErrForbidden
	}
	for _, userID := range userIDs {
		member, ok := f.workspaceMembers[wmKey(workspaceID, userID)]
		if !ok || member.Status != domain.MemberStatusActive {
			return storage.AddMembersResult{}, domain.ErrForbidden
		}
		if _, blocked := f.ineligibleAccounts[userID]; blocked {
			return storage.AddMembersResult{}, domain.ErrForbidden
		}
	}
	// Mirrors the real store's RETURNING: only rows the insert actually created.
	addedUserIDs := make([]string, 0, len(userIDs))
	for _, userID := range userIDs {
		key := cmKey(channelID, userID)
		if _, exists := f.channelMembers[key]; exists {
			continue
		}
		f.channelMembers[key] = domain.ChannelMember{
			ChannelID: channelID, UserID: userID,
			Role: domain.ChannelRoleMember, JoinedAt: time.Now(),
		}
		addedUserIDs = append(addedUserIDs, userID)
	}
	added := len(addedUserIDs)
	total := 0
	for key := range f.channelMembers {
		if strings.HasPrefix(key, channelID+":") {
			total++
		}
	}
	return storage.AddMembersResult{
		Added: added, AlreadyMembers: len(userIDs) - added, TotalCount: total,
		AddedUserIDs: addedUserIDs,
	}, nil
}

// SearchChannelMemberCandidates models the store's NOT EXISTS: current members
// of the channel are excluded by the query, so a service test can prove the
// picker no longer depends on the panel's preview.
func (f *fakeMemberStore) SearchChannelMemberCandidates(
	_ context.Context, workspaceID, channelID, callerID, prefix string, limit int,
) ([]domain.DMCandidate, error) {
	f.candidateCalls = append(f.candidateCalls, candidateSearchCall{
		WorkspaceID: workspaceID, TargetID: channelID, CallerID: callerID,
		Query: prefix, Limit: limit,
	})
	if f.candidateErr != nil {
		return nil, f.candidateErr
	}
	results := make([]domain.DMCandidate, 0, len(f.dmCandidates))
	for _, candidate := range f.dmCandidates {
		if _, member := f.channelMembers[cmKey(channelID, candidate.UserID)]; member {
			continue
		}
		if candidate.UserID == callerID {
			continue
		}
		results = append(results, candidate)
	}
	return results, nil
}

func (f *fakeMemberStore) GetChannelMember(_ context.Context, channelID, userID string) (domain.ChannelMember, error) {
	f.getCMCalls++
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
	return f.addGeneralMembership(m)
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
		if m.WorkspaceID != workspaceID {
			continue
		}
		// Same gate as the real backfill's `status = 'active' AND role IN (...)`:
		// CanReachPublicChannels covers both, so a guest is skipped here exactly
		// as it is skipped there. Note this only declines to *insert* — the loop
		// never deletes, so a guest that was explicitly added to #geral keeps
		// that membership across a sync.
		if !domain.CanReachPublicChannels(&m) {
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

// addGeneralMembership models PGXMemberStore's automatic #geral join.
//
// It takes the whole membership rather than a user ID because the real
// statement selects the role from the membership row and inserts only for the
// roles in generalMembershipRoles. A guest is excluded there (RF-74), so it is
// excluded here: a fake that joined a guest to #geral would let a regression in
// the guest boundary pass every service test.
//
// The exclusion is domain.CanReachPublicChannels, not a `role != guest`
// comparison, so the fake and the store stay tied to the same rule and an
// unrecognised role is denied by both.
//
// Silently doing nothing, rather than erroring, is what the real INSERT ...
// SELECT does when the role does not match: zero rows, no error. Only the
// *automatic* join is gated — an explicit AddChannelMember into #geral is a
// different path and stays available to a guest, which is how RF-74 says a
// guest reaches any channel.
func (f *fakeMemberStore) addGeneralMembership(m domain.WorkspaceMember) error {
	channelID, ok := f.generalChannels[m.WorkspaceID]
	if !ok {
		return domain.ErrGeneralChannelMissing
	}
	if f.addCMErr != nil {
		return f.addCMErr
	}
	if !domain.CanReachPublicChannels(&m) {
		return nil
	}
	key := cmKey(channelID, m.UserID)
	if _, ok := f.channelMembers[key]; ok {
		return nil
	}
	f.channelMembers[key] = domain.ChannelMember{
		ChannelID: channelID, UserID: m.UserID, Role: domain.ChannelRoleMember, JoinedAt: time.Now(),
	}
	return nil
}
