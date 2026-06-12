package service_test

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/service"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

func TestDMService_CreateDirectConversation_SucceedsForActiveMembers(t *testing.T) {
	ms := newFakeMemberStore()
	ms.workspaceMembers[wmKey("ws-1", "user-1")] = activeMembership("ws-1", "user-1")
	ms.workspaceMembers[wmKey("ws-1", "user-2")] = activeMembership("ws-1", "user-2")
	dms := &fakeDMStore{createdConversation: domain.DMConversation{
		ID: "dm-1", WorkspaceID: "ws-1", Type: domain.DMConversationTypeDirect, Status: domain.DMConversationStatusActive, CreatedBy: "user-1",
	}}

	got, err := service.NewDMService(activeWorkspaceStore("ws-1"), dms, ms).CreateDirectConversation(context.Background(), service.CreateDirectConversationInput{
		WorkspaceID: "ws-1",
		CallerID:    "user-1",
		OtherUserID: "user-2",
	})
	if err != nil {
		t.Fatalf("CreateDirectConversation: %v", err)
	}
	if got.ID != "dm-1" || got.Type != domain.DMConversationTypeDirect {
		t.Fatalf("unexpected conversation: %+v", got)
	}
	if dms.createDirectCalls != 1 {
		t.Fatalf("expected one direct create call, got %d", dms.createDirectCalls)
	}
	if dms.lastDirectInput.CreatedBy != "user-1" {
		t.Fatalf("service must set created_by from caller, input=%+v", dms.lastDirectInput)
	}
	if dms.lastDirectInput.DirectPairKey == "" {
		t.Fatal("direct pair key must be computed internally before storage")
	}
	assertSameStringSet(t, dms.lastDirectInput.ParticipantUserIDs, []string{"user-1", "user-2"})
}

func TestDMService_CreateDirectConversation_ReversedParticipantsUseSamePairKey(t *testing.T) {
	ms := newFakeMemberStore()
	ms.workspaceMembers[wmKey("ws-1", "user-1")] = activeMembership("ws-1", "user-1")
	ms.workspaceMembers[wmKey("ws-1", "user-2")] = activeMembership("ws-1", "user-2")
	dms := &fakeDMStore{createdConversation: domain.DMConversation{
		ID: "dm-1", WorkspaceID: "ws-1", Type: domain.DMConversationTypeDirect, Status: domain.DMConversationStatusActive,
	}}
	svc := service.NewDMService(activeWorkspaceStore("ws-1"), dms, ms)

	if _, err := svc.CreateDirectConversation(context.Background(), service.CreateDirectConversationInput{
		WorkspaceID: "ws-1", CallerID: "user-1", OtherUserID: "user-2",
	}); err != nil {
		t.Fatalf("first CreateDirectConversation: %v", err)
	}
	firstKey := dms.lastDirectInput.DirectPairKey

	if _, err := svc.CreateDirectConversation(context.Background(), service.CreateDirectConversationInput{
		WorkspaceID: "ws-1", CallerID: "user-2", OtherUserID: "user-1",
	}); err != nil {
		t.Fatalf("reversed CreateDirectConversation: %v", err)
	}
	if dms.lastDirectInput.DirectPairKey != firstKey {
		t.Fatal("pair key must be order-independent")
	}
}

func TestDMService_CreateDirectConversation_SelfDMDenied(t *testing.T) {
	ms := newFakeMemberStore()
	ms.workspaceMembers[wmKey("ws-1", "user-1")] = activeMembership("ws-1", "user-1")
	dms := &fakeDMStore{}

	_, err := service.NewDMService(activeWorkspaceStore("ws-1"), dms, ms).CreateDirectConversation(context.Background(), service.CreateDirectConversationInput{
		WorkspaceID: "ws-1", CallerID: "user-1", OtherUserID: "user-1",
	})
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput, got %v", err)
	}
	if dms.createDirectCalls != 0 {
		t.Fatalf("self-DM must be denied before storage, calls=%d", dms.createDirectCalls)
	}
}

func TestDMService_CreateDirectConversation_InvalidInputDenied(t *testing.T) {
	_, err := service.NewDMService(activeWorkspaceStore("ws-1"), &fakeDMStore{}, newFakeMemberStore()).CreateDirectConversation(context.Background(), service.CreateDirectConversationInput{
		WorkspaceID: " ", CallerID: "user-1", OtherUserID: "user-2",
	})
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput, got %v", err)
	}
}

func TestDMService_CreateDirectConversation_InactiveParticipantDenied(t *testing.T) {
	for _, tc := range []struct {
		name   string
		member domain.WorkspaceMember
	}{
		{name: "suspended", member: domain.WorkspaceMember{WorkspaceID: "ws-1", UserID: "user-2", Role: domain.WorkspaceRoleMember, Status: domain.MemberStatusSuspended}},
		{name: "left", member: domain.WorkspaceMember{WorkspaceID: "ws-1", UserID: "user-2", Role: domain.WorkspaceRoleMember, Status: domain.MemberStatusLeft}},
		{name: "different workspace", member: activeMembership("ws-2", "user-2")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ms := newFakeMemberStore()
			ms.workspaceMembers[wmKey("ws-1", "user-1")] = activeMembership("ws-1", "user-1")
			ms.workspaceMembers[wmKey(tc.member.WorkspaceID, tc.member.UserID)] = tc.member
			dms := &fakeDMStore{}

			_, err := service.NewDMService(activeWorkspaceStore("ws-1"), dms, ms).CreateDirectConversation(context.Background(), service.CreateDirectConversationInput{
				WorkspaceID: "ws-1", CallerID: "user-1", OtherUserID: "user-2",
			})
			if !errors.Is(err, domain.ErrForbidden) {
				t.Fatalf("expected ErrForbidden, got %v", err)
			}
			if dms.createDirectCalls != 0 {
				t.Fatalf("inactive participant must be denied before storage, calls=%d", dms.createDirectCalls)
			}
		})
	}
}

func TestDMService_CreateDirectConversation_DisabledWorkspaceDenied(t *testing.T) {
	ms := newFakeMemberStore()
	ms.workspaceMembers[wmKey("ws-1", "user-1")] = activeMembership("ws-1", "user-1")
	ms.workspaceMembers[wmKey("ws-1", "user-2")] = activeMembership("ws-1", "user-2")
	workspaces := &fakeWorkspaceStore{workspace: domain.Workspace{ID: "ws-1", Status: domain.WorkspaceStatusDisabled}}

	_, err := service.NewDMService(workspaces, &fakeDMStore{}, ms).CreateDirectConversation(context.Background(), service.CreateDirectConversationInput{
		WorkspaceID: "ws-1", CallerID: "user-1", OtherUserID: "user-2",
	})
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("expected ErrForbidden, got %v", err)
	}
}

func TestDMService_CreateDirectConversation_StorageErrorPropagates(t *testing.T) {
	ms := newFakeMemberStore()
	ms.workspaceMembers[wmKey("ws-1", "user-1")] = activeMembership("ws-1", "user-1")
	ms.workspaceMembers[wmKey("ws-1", "user-2")] = activeMembership("ws-1", "user-2")
	want := errors.New("dm storage unavailable")

	_, err := service.NewDMService(activeWorkspaceStore("ws-1"), &fakeDMStore{createDirectErr: want}, ms).CreateDirectConversation(context.Background(), service.CreateDirectConversationInput{
		WorkspaceID: "ws-1", CallerID: "user-1", OtherUserID: "user-2",
	})
	if !errors.Is(err, want) {
		t.Fatalf("expected storage error, got %v", err)
	}
}

func TestDMService_CreateGroupConversation_SucceedsWithActiveMembersAndAddsCaller(t *testing.T) {
	ms := newFakeMemberStore()
	for _, userID := range []string{"user-1", "user-2", "user-3"} {
		ms.workspaceMembers[wmKey("ws-1", userID)] = activeMembership("ws-1", userID)
	}
	dms := &fakeDMStore{createdConversation: domain.DMConversation{
		ID: "dm-group", WorkspaceID: "ws-1", Type: domain.DMConversationTypeGroup, Title: "Project", Status: domain.DMConversationStatusActive, CreatedBy: "user-1",
	}}

	got, err := service.NewDMService(activeWorkspaceStore("ws-1"), dms, ms).CreateGroupConversation(context.Background(), service.CreateGroupConversationInput{
		WorkspaceID:        "ws-1",
		CallerID:           "user-1",
		ParticipantUserIDs: []string{"user-2", "user-3", "user-2"},
		Title:              " Project ",
	})
	if err != nil {
		t.Fatalf("CreateGroupConversation: %v", err)
	}
	if got.ID != "dm-group" || got.Type != domain.DMConversationTypeGroup {
		t.Fatalf("unexpected conversation: %+v", got)
	}
	if dms.createGroupCalls != 1 {
		t.Fatalf("expected one group create call, got %d", dms.createGroupCalls)
	}
	if dms.lastGroupInput.CreatedBy != "user-1" || dms.lastGroupInput.Title != "Project" {
		t.Fatalf("service must own created_by and normalize title, input=%+v", dms.lastGroupInput)
	}
	assertSameStringSet(t, dms.lastGroupInput.ParticipantUserIDs, []string{"user-1", "user-2", "user-3"})
}

func TestDMService_CreateGroupConversation_InvalidInputsDenied(t *testing.T) {
	ms := newFakeMemberStore()
	for _, userID := range []string{"user-1", "user-2", "user-3"} {
		ms.workspaceMembers[wmKey("ws-1", userID)] = activeMembership("ws-1", userID)
	}
	svc := service.NewDMService(activeWorkspaceStore("ws-1"), &fakeDMStore{}, ms)

	for _, tc := range []struct {
		name  string
		input service.CreateGroupConversationInput
	}{
		{
			name:  "missing caller",
			input: service.CreateGroupConversationInput{WorkspaceID: "ws-1", CallerID: " ", ParticipantUserIDs: []string{"user-2", "user-3"}},
		},
		{
			name:  "empty participant",
			input: service.CreateGroupConversationInput{WorkspaceID: "ws-1", CallerID: "user-1", ParticipantUserIDs: []string{"user-2", " "}},
		},
		{
			name:  "title too long",
			input: service.CreateGroupConversationInput{WorkspaceID: "ws-1", CallerID: "user-1", ParticipantUserIDs: []string{"user-2", "user-3"}, Title: longString(121)},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.CreateGroupConversation(context.Background(), tc.input)
			if !errors.Is(err, domain.ErrInvalidInput) {
				t.Fatalf("expected ErrInvalidInput, got %v", err)
			}
		})
	}
}

func TestDMService_CreateGroupConversation_RequiresAtLeastThreeUniqueParticipants(t *testing.T) {
	ms := newFakeMemberStore()
	ms.workspaceMembers[wmKey("ws-1", "user-1")] = activeMembership("ws-1", "user-1")
	ms.workspaceMembers[wmKey("ws-1", "user-2")] = activeMembership("ws-1", "user-2")
	dms := &fakeDMStore{}

	_, err := service.NewDMService(activeWorkspaceStore("ws-1"), dms, ms).CreateGroupConversation(context.Background(), service.CreateGroupConversationInput{
		WorkspaceID: "ws-1", CallerID: "user-1", ParticipantUserIDs: []string{"user-2"},
	})
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput, got %v", err)
	}
	if dms.createGroupCalls != 0 {
		t.Fatalf("undersized group must be denied before storage, calls=%d", dms.createGroupCalls)
	}
}

func TestDMService_CreateGroupConversation_StorageErrorPropagates(t *testing.T) {
	ms := newFakeMemberStore()
	for _, userID := range []string{"user-1", "user-2", "user-3"} {
		ms.workspaceMembers[wmKey("ws-1", userID)] = activeMembership("ws-1", userID)
	}
	want := errors.New("dm group storage unavailable")

	_, err := service.NewDMService(activeWorkspaceStore("ws-1"), &fakeDMStore{createGroupErr: want}, ms).CreateGroupConversation(context.Background(), service.CreateGroupConversationInput{
		WorkspaceID: "ws-1", CallerID: "user-1", ParticipantUserIDs: []string{"user-2", "user-3"},
	})
	if !errors.Is(err, want) {
		t.Fatalf("expected storage error, got %v", err)
	}
}

func TestDMService_CreateGroupConversation_InvalidParticipantDenied(t *testing.T) {
	for _, tc := range []struct {
		name   string
		member domain.WorkspaceMember
	}{
		{name: "suspended", member: domain.WorkspaceMember{WorkspaceID: "ws-1", UserID: "user-3", Role: domain.WorkspaceRoleMember, Status: domain.MemberStatusSuspended}},
		{name: "left", member: domain.WorkspaceMember{WorkspaceID: "ws-1", UserID: "user-3", Role: domain.WorkspaceRoleMember, Status: domain.MemberStatusLeft}},
		{name: "different workspace", member: activeMembership("ws-2", "user-3")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ms := newFakeMemberStore()
			ms.workspaceMembers[wmKey("ws-1", "user-1")] = activeMembership("ws-1", "user-1")
			ms.workspaceMembers[wmKey("ws-1", "user-2")] = activeMembership("ws-1", "user-2")
			ms.workspaceMembers[wmKey(tc.member.WorkspaceID, tc.member.UserID)] = tc.member
			dms := &fakeDMStore{}

			_, err := service.NewDMService(activeWorkspaceStore("ws-1"), dms, ms).CreateGroupConversation(context.Background(), service.CreateGroupConversationInput{
				WorkspaceID: "ws-1", CallerID: "user-1", ParticipantUserIDs: []string{"user-2", "user-3"},
			})
			if !errors.Is(err, domain.ErrForbidden) {
				t.Fatalf("expected ErrForbidden, got %v", err)
			}
			if dms.createGroupCalls != 0 {
				t.Fatalf("invalid participant must be denied before storage, calls=%d", dms.createGroupCalls)
			}
		})
	}
}

func TestDMService_ListConversations_UsesSQLVisibility(t *testing.T) {
	dms := &fakeDMStore{visibleConversations: []domain.DMConversation{
		{ID: "dm-1", WorkspaceID: "ws-1", Type: domain.DMConversationTypeDirect, Status: domain.DMConversationStatusActive},
	}}

	got, err := service.NewDMService(activeWorkspaceStore("ws-1"), dms, newFakeMemberStore()).ListConversations(context.Background(), "ws-1", "user-1")
	if err != nil {
		t.Fatalf("ListConversations: %v", err)
	}
	if len(got) != 1 || got[0].ID != "dm-1" {
		t.Fatalf("expected SQL-visible conversations only, got %+v", got)
	}
	if dms.listVisibleCalls != 1 {
		t.Fatalf("expected SQL visible list call, got %d", dms.listVisibleCalls)
	}
}

func TestDMService_ListConversations_StorageErrorPropagates(t *testing.T) {
	want := errors.New("list dm unavailable")

	_, err := service.NewDMService(activeWorkspaceStore("ws-1"), &fakeDMStore{listVisibleErr: want}, newFakeMemberStore()).ListConversations(context.Background(), "ws-1", "user-1")
	if !errors.Is(err, want) {
		t.Fatalf("expected storage error, got %v", err)
	}
}

func TestDMService_GetConversation_ReturnsVisibleConversation(t *testing.T) {
	dms := &fakeDMStore{visibleConversation: domain.DMConversation{
		ID: "dm-1", WorkspaceID: "ws-1", Type: domain.DMConversationTypeGroup, Status: domain.DMConversationStatusActive,
	}}

	got, err := service.NewDMService(activeWorkspaceStore("ws-1"), dms, newFakeMemberStore()).GetConversation(context.Background(), service.GetDMConversationInput{
		WorkspaceID: " ws-1 ", CallerID: " user-1 ", ConversationID: " dm-1 ",
	})
	if err != nil {
		t.Fatalf("GetConversation: %v", err)
	}
	if got.ID != "dm-1" || dms.getVisibleCalls != 1 {
		t.Fatalf("expected visible conversation, got=%+v calls=%d", got, dms.getVisibleCalls)
	}
}

func TestDMService_GetConversation_NonParticipantLooksNotFound(t *testing.T) {
	dms := &fakeDMStore{getVisibleErr: domain.ErrNotFound}

	_, err := service.NewDMService(activeWorkspaceStore("ws-1"), dms, newFakeMemberStore()).GetConversation(context.Background(), service.GetDMConversationInput{
		WorkspaceID:    "ws-1",
		CallerID:       "user-1",
		ConversationID: "dm-private",
	})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("hidden conversation should look not found, got %v", err)
	}
	if dms.getVisibleCalls != 1 {
		t.Fatalf("expected SQL visible get call, got %d", dms.getVisibleCalls)
	}
}

type fakeDMStore struct {
	createdConversation  domain.DMConversation
	createDirectErr      error
	createGroupErr       error
	visibleConversations []domain.DMConversation
	listVisibleErr       error
	visibleConversation  domain.DMConversation
	getVisibleErr        error

	lastDirectInput   storage.CreateDirectConversationInput
	lastGroupInput    storage.CreateGroupConversationInput
	createDirectCalls int
	createGroupCalls  int
	listVisibleCalls  int
	getVisibleCalls   int
}

func (f *fakeDMStore) CreateDirectConversation(_ context.Context, input storage.CreateDirectConversationInput) (domain.DMConversation, error) {
	f.createDirectCalls++
	f.lastDirectInput = input
	return f.createdConversation, f.createDirectErr
}

func (f *fakeDMStore) CreateGroupConversation(_ context.Context, input storage.CreateGroupConversationInput) (domain.DMConversation, error) {
	f.createGroupCalls++
	f.lastGroupInput = input
	return f.createdConversation, f.createGroupErr
}

func (f *fakeDMStore) ListVisibleConversationsByUser(_ context.Context, _, _ string) ([]domain.DMConversation, error) {
	f.listVisibleCalls++
	return f.visibleConversations, f.listVisibleErr
}

func (f *fakeDMStore) GetVisibleConversationByID(_ context.Context, _, _, _ string) (domain.DMConversation, error) {
	f.getVisibleCalls++
	if f.getVisibleErr != nil {
		return domain.DMConversation{}, f.getVisibleErr
	}
	return f.visibleConversation, nil
}

func assertSameStringSet(t *testing.T, got, want []string) {
	t.Helper()
	gotCopy := append([]string(nil), got...)
	wantCopy := append([]string(nil), want...)
	sort.Strings(gotCopy)
	sort.Strings(wantCopy)
	if !reflect.DeepEqual(gotCopy, wantCopy) {
		t.Fatalf("expected set %v, got %v", wantCopy, gotCopy)
	}
}

func longString(length int) string {
	var b strings.Builder
	for i := 0; i < length; i++ {
		b.WriteByte('x')
	}
	return b.String()
}
