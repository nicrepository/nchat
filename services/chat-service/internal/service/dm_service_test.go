package service_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/service"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

// UUID constants used across DM service tests.
const (
	user1   = "aabbccdd-1111-2222-3333-000000000001"
	user1Up = "AABBCCDD-1111-2222-3333-000000000001" // same UUID, uppercase text form
	user2   = "aabbccdd-1111-2222-3333-000000000002"
	user2Up = "AABBCCDD-1111-2222-3333-000000000002"
	user3   = "aabbccdd-1111-2222-3333-000000000003"
)

func TestDMService_CreateDirectConversation_SucceedsForActiveMembers(t *testing.T) {
	ms := newFakeMemberStore()
	ms.workspaceMembers[wmKey("ws-1", user1)] = activeMembership("ws-1", user1)
	ms.workspaceMembers[wmKey("ws-1", user2)] = activeMembership("ws-1", user2)
	dms := &fakeDMStore{createdConversation: domain.DMConversation{
		ID: "dm-1", WorkspaceID: "ws-1", Type: domain.DMConversationTypeDirect, Status: domain.DMConversationStatusActive, CreatedBy: user1,
	}}

	got, err := service.NewDMService(dms, ms).CreateDirectConversation(context.Background(), service.CreateDirectConversationInput{
		WorkspaceID: "ws-1",
		CallerID:    user1,
		OtherUserID: user2,
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
	if dms.lastDirectInput.CreatedBy != user1 {
		t.Fatalf("service must set created_by from caller, input=%+v", dms.lastDirectInput)
	}
	if dms.lastDirectInput.DirectPairKey == "" {
		t.Fatal("direct pair key must be computed internally before storage")
	}
	assertSameStringSet(t, dms.lastDirectInput.ParticipantUserIDs, []string{user1, user2})
}

func TestDMService_CreateDirectConversation_ReversedParticipantsUseSamePairKey(t *testing.T) {
	ms := newFakeMemberStore()
	ms.workspaceMembers[wmKey("ws-1", user1)] = activeMembership("ws-1", user1)
	ms.workspaceMembers[wmKey("ws-1", user2)] = activeMembership("ws-1", user2)
	dms := &fakeDMStore{createdConversation: domain.DMConversation{
		ID: "dm-1", WorkspaceID: "ws-1", Type: domain.DMConversationTypeDirect, Status: domain.DMConversationStatusActive,
	}}
	svc := service.NewDMService(dms, ms)

	if _, err := svc.CreateDirectConversation(context.Background(), service.CreateDirectConversationInput{
		WorkspaceID: "ws-1", CallerID: user1, OtherUserID: user2,
	}); err != nil {
		t.Fatalf("first CreateDirectConversation: %v", err)
	}
	firstKey := dms.lastDirectInput.DirectPairKey

	if _, err := svc.CreateDirectConversation(context.Background(), service.CreateDirectConversationInput{
		WorkspaceID: "ws-1", CallerID: user2, OtherUserID: user1,
	}); err != nil {
		t.Fatalf("reversed CreateDirectConversation: %v", err)
	}
	if dms.lastDirectInput.DirectPairKey != firstKey {
		t.Fatal("pair key must be order-independent")
	}
}

func TestDMService_CreateDirectConversation_SelfDMDenied(t *testing.T) {
	ms := newFakeMemberStore()
	ms.workspaceMembers[wmKey("ws-1", user1)] = activeMembership("ws-1", user1)
	dms := &fakeDMStore{}

	_, err := service.NewDMService(dms, ms).CreateDirectConversation(context.Background(), service.CreateDirectConversationInput{
		WorkspaceID: "ws-1", CallerID: user1, OtherUserID: user1,
	})
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput, got %v", err)
	}
	if dms.createDirectCalls != 0 {
		t.Fatalf("self-DM must be denied before storage, calls=%d", dms.createDirectCalls)
	}
}

func TestDMService_CreateDirectConversation_InvalidInputDenied(t *testing.T) {
	_, err := service.NewDMService(&fakeDMStore{}, newFakeMemberStore()).CreateDirectConversation(context.Background(), service.CreateDirectConversationInput{
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
		{name: "suspended", member: domain.WorkspaceMember{WorkspaceID: "ws-1", UserID: user2, Role: domain.WorkspaceRoleMember, Status: domain.MemberStatusSuspended}},
		{name: "left", member: domain.WorkspaceMember{WorkspaceID: "ws-1", UserID: user2, Role: domain.WorkspaceRoleMember, Status: domain.MemberStatusLeft}},
		{name: "different workspace", member: activeMembership("ws-2", user2)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ms := newFakeMemberStore()
			ms.workspaceMembers[wmKey("ws-1", user1)] = activeMembership("ws-1", user1)
			ms.workspaceMembers[wmKey(tc.member.WorkspaceID, tc.member.UserID)] = tc.member
			dms := &fakeDMStore{}

			_, err := service.NewDMService(dms, ms).CreateDirectConversation(context.Background(), service.CreateDirectConversationInput{
				WorkspaceID: "ws-1", CallerID: user1, OtherUserID: user2,
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
	ms.workspaceStatus["ws-1"] = domain.WorkspaceStatusDisabled
	ms.workspaceMembers[wmKey("ws-1", user1)] = activeMembership("ws-1", user1)
	ms.workspaceMembers[wmKey("ws-1", user2)] = activeMembership("ws-1", user2)

	_, err := service.NewDMService(&fakeDMStore{}, ms).CreateDirectConversation(context.Background(), service.CreateDirectConversationInput{
		WorkspaceID: "ws-1", CallerID: user1, OtherUserID: user2,
	})
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("expected ErrForbidden, got %v", err)
	}
}

func TestDMService_GetOrCreateDirectConversation_DoesNotReloadWorkspace(t *testing.T) {
	ms := newFakeMemberStore()
	ms.workspaceMembers[wmKey("ws-1", user1)] = activeMembership("ws-1", user1)
	ms.workspaceMembers[wmKey("ws-1", user2)] = activeMembership("ws-1", user2)

	_, err := service.NewDMService(&fakeDMStore{}, ms).GetOrCreateDirectConversation(
		context.Background(), service.CreateDirectConversationInput{WorkspaceID: "ws-1", CallerID: user1, OtherUserID: user2},
	)
	if err != nil {
		t.Fatalf("GetOrCreateDirectConversation: %v", err)
	}
	// One eligibility query per participant and nothing else: the workspace state
	// rides along inside that same query instead of a separate read.
	if ms.getEligibleCalls != 2 {
		t.Fatalf("eligible lookups=%d, want 2", ms.getEligibleCalls)
	}
}

func TestDMService_GetOrCreateDirectConversation_RejectsIneligibleCallerBeforeCreate(t *testing.T) {
	for _, test := range []struct {
		name   string
		member *domain.WorkspaceMember
	}{
		{name: "suspended", member: &domain.WorkspaceMember{WorkspaceID: "ws-1", UserID: user1, Status: domain.MemberStatusSuspended}},
		{name: "removed", member: &domain.WorkspaceMember{WorkspaceID: "ws-1", UserID: user1, Status: domain.MemberStatusLeft}},
		{name: "without membership"},
	} {
		t.Run(test.name, func(t *testing.T) {
			ms := newFakeMemberStore()
			if test.member != nil {
				ms.workspaceMembers[wmKey("ws-1", user1)] = *test.member
			}
			ms.workspaceMembers[wmKey("ws-1", user2)] = activeMembership("ws-1", user2)
			dms := &fakeDMStore{}
			_, err := service.NewDMService(dms, ms).GetOrCreateDirectConversation(
				context.Background(), service.CreateDirectConversationInput{WorkspaceID: "ws-1", CallerID: user1, OtherUserID: user2},
			)
			if !errors.Is(err, domain.ErrForbidden) || dms.createDirectCalls != 0 {
				t.Fatalf("error=%v create calls=%d", err, dms.createDirectCalls)
			}
		})
	}
}

func TestDMService_CreateDirectConversation_StorageErrorPropagates(t *testing.T) {
	ms := newFakeMemberStore()
	ms.workspaceMembers[wmKey("ws-1", user1)] = activeMembership("ws-1", user1)
	ms.workspaceMembers[wmKey("ws-1", user2)] = activeMembership("ws-1", user2)
	want := errors.New("dm storage unavailable")

	_, err := service.NewDMService(&fakeDMStore{createDirectErr: want}, ms).CreateDirectConversation(context.Background(), service.CreateDirectConversationInput{
		WorkspaceID: "ws-1", CallerID: user1, OtherUserID: user2,
	})
	if !errors.Is(err, want) {
		t.Fatalf("expected storage error, got %v", err)
	}
}

func TestDMService_CreateGroupConversation_SucceedsWithActiveMembersAndAddsCaller(t *testing.T) {
	ms := newFakeMemberStore()
	for _, uid := range []string{user1, user2, user3} {
		ms.workspaceMembers[wmKey("ws-1", uid)] = activeMembership("ws-1", uid)
	}
	dms := &fakeDMStore{createdConversation: domain.DMConversation{
		ID: "dm-group", WorkspaceID: "ws-1", Type: domain.DMConversationTypeGroup, Title: "Project", Status: domain.DMConversationStatusActive, CreatedBy: user1,
	}}

	got, err := service.NewDMService(dms, ms).CreateGroupConversation(context.Background(), service.CreateGroupConversationInput{
		WorkspaceID:        "ws-1",
		CallerID:           user1,
		ParticipantUserIDs: []string{user2, user3, user2},
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
	if dms.lastGroupInput.CreatedBy != user1 || dms.lastGroupInput.Title != "Project" {
		t.Fatalf("service must own created_by and normalize title, input=%+v", dms.lastGroupInput)
	}
	assertSameStringSet(t, dms.lastGroupInput.ParticipantUserIDs, []string{user1, user2, user3})
}

func TestDMService_CreateGroupConversation_InvalidInputsDenied(t *testing.T) {
	ms := newFakeMemberStore()
	for _, uid := range []string{user1, user2, user3} {
		ms.workspaceMembers[wmKey("ws-1", uid)] = activeMembership("ws-1", uid)
	}
	svc := service.NewDMService(&fakeDMStore{}, ms)

	for _, tc := range []struct {
		name  string
		input service.CreateGroupConversationInput
	}{
		{
			name:  "missing caller",
			input: service.CreateGroupConversationInput{WorkspaceID: "ws-1", CallerID: " ", ParticipantUserIDs: []string{user2, user3}},
		},
		{
			name:  "empty participant",
			input: service.CreateGroupConversationInput{WorkspaceID: "ws-1", CallerID: user1, ParticipantUserIDs: []string{user2, " "}},
		},
		{
			name:  "title too long",
			input: service.CreateGroupConversationInput{WorkspaceID: "ws-1", CallerID: user1, ParticipantUserIDs: []string{user2, user3}, Title: longString(121)},
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
	ms.workspaceMembers[wmKey("ws-1", user1)] = activeMembership("ws-1", user1)
	ms.workspaceMembers[wmKey("ws-1", user2)] = activeMembership("ws-1", user2)
	dms := &fakeDMStore{}

	_, err := service.NewDMService(dms, ms).CreateGroupConversation(context.Background(), service.CreateGroupConversationInput{
		WorkspaceID: "ws-1", CallerID: user1, ParticipantUserIDs: []string{user2},
	})
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput, got %v", err)
	}
	if dms.createGroupCalls != 0 {
		t.Fatalf("undersized group must be denied before storage, calls=%d", dms.createGroupCalls)
	}
}

// An oversized list is rejected before any membership look-up, so a hostile
// payload cannot turn one request into thousands of database round-trips.
func TestDMService_CreateGroupConversation_RejectsOversizedParticipantList(t *testing.T) {
	ms := newFakeMemberStore()
	ms.workspaceMembers[wmKey("ws-1", user1)] = activeMembership("ws-1", user1)
	dms := &fakeDMStore{}
	invited := make([]string, 50)
	for i := range invited {
		invited[i] = fmt.Sprintf("aabbccdd-1111-2222-3333-%012d", i+100)
	}

	_, err := service.NewDMService(dms, ms).CreateGroupConversation(context.Background(), service.CreateGroupConversationInput{
		WorkspaceID: "ws-1", CallerID: user1, ParticipantUserIDs: invited,
	})
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput, got %v", err)
	}
	// ErrInvalidInput (not ErrForbidden) proves the cap fired before the first
	// membership look-up: only the caller is a registered member here, so an
	// uncapped run would have failed on the very first unknown participant.
	if dms.createGroupCalls != 0 {
		t.Fatalf("oversized group must be denied before storage, calls=%d", dms.createGroupCalls)
	}
}

// A workspace membership row survives the account it points at: deactivating or
// deleting a user in auth does not remove chat.workspace_members. Checking only
// the membership would therefore let a disabled account be pulled into a new
// group, so group creation must use the same eligibility rule as a 1:1 DM.
func TestDMService_CreateGroupConversation_RejectsParticipantWithIneligibleAccount(t *testing.T) {
	for _, accountState := range []string{"suspended", "deleted"} {
		t.Run(accountState, func(t *testing.T) {
			ms := newFakeMemberStore()
			for _, uid := range []string{user1, user2, user3} {
				ms.workspaceMembers[wmKey("ws-1", uid)] = activeMembership("ws-1", uid)
			}
			// Membership stays active; only the account is gone.
			ms.ineligibleAccounts[user3] = struct{}{}
			dms := &fakeDMStore{}

			_, err := service.NewDMService(dms, ms).CreateGroupConversation(context.Background(), service.CreateGroupConversationInput{
				WorkspaceID: "ws-1", CallerID: user1, ParticipantUserIDs: []string{user2, user3},
			})
			// The same error a missing or foreign-workspace user produces: the
			// caller cannot tell account state from membership state.
			if !errors.Is(err, domain.ErrForbidden) {
				t.Fatalf("expected ErrForbidden, got %v", err)
			}
			if err != nil && strings.Contains(err.Error(), user3) {
				t.Fatalf("error must not name the rejected participant: %v", err)
			}
			if dms.createGroupCalls != 0 {
				t.Fatalf("nothing may be persisted, calls=%d", dms.createGroupCalls)
			}
		})
	}
}

// The eligible participants of a partially valid list must not be created on
// their own — the group is all or nothing, decided before storage is touched.
func TestDMService_CreateGroupConversation_DoesNotDegradeToTheEligibleSubset(t *testing.T) {
	ms := newFakeMemberStore()
	for _, uid := range []string{user1, user2, user3} {
		ms.workspaceMembers[wmKey("ws-1", uid)] = activeMembership("ws-1", uid)
	}
	ms.ineligibleAccounts[user2] = struct{}{}
	dms := &fakeDMStore{}

	_, err := service.NewDMService(dms, ms).CreateGroupConversation(context.Background(), service.CreateGroupConversationInput{
		WorkspaceID: "ws-1", CallerID: user1, ParticipantUserIDs: []string{user2, user3},
	})
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("expected ErrForbidden, got %v", err)
	}
	if dms.createGroupCalls != 0 {
		t.Fatalf("partial group must never reach storage, calls=%d", dms.createGroupCalls)
	}
}

// The caller is a participant too, so an account that lost eligibility cannot
// open a group for others either.
func TestDMService_CreateGroupConversation_RejectsIneligibleCaller(t *testing.T) {
	ms := newFakeMemberStore()
	for _, uid := range []string{user1, user2, user3} {
		ms.workspaceMembers[wmKey("ws-1", uid)] = activeMembership("ws-1", uid)
	}
	ms.ineligibleAccounts[user1] = struct{}{}
	dms := &fakeDMStore{}

	_, err := service.NewDMService(dms, ms).CreateGroupConversation(context.Background(), service.CreateGroupConversationInput{
		WorkspaceID: "ws-1", CallerID: user1, ParticipantUserIDs: []string{user2, user3},
	})
	if !errors.Is(err, domain.ErrForbidden) || dms.createGroupCalls != 0 {
		t.Fatalf("err=%v calls=%d", err, dms.createGroupCalls)
	}
}

func TestDMService_CreateGroupConversation_StorageErrorPropagates(t *testing.T) {
	ms := newFakeMemberStore()
	for _, uid := range []string{user1, user2, user3} {
		ms.workspaceMembers[wmKey("ws-1", uid)] = activeMembership("ws-1", uid)
	}
	want := errors.New("dm group storage unavailable")

	_, err := service.NewDMService(&fakeDMStore{createGroupErr: want}, ms).CreateGroupConversation(context.Background(), service.CreateGroupConversationInput{
		WorkspaceID: "ws-1", CallerID: user1, ParticipantUserIDs: []string{user2, user3},
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
		{name: "suspended", member: domain.WorkspaceMember{WorkspaceID: "ws-1", UserID: user3, Role: domain.WorkspaceRoleMember, Status: domain.MemberStatusSuspended}},
		{name: "left", member: domain.WorkspaceMember{WorkspaceID: "ws-1", UserID: user3, Role: domain.WorkspaceRoleMember, Status: domain.MemberStatusLeft}},
		{name: "different workspace", member: activeMembership("ws-2", user3)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ms := newFakeMemberStore()
			ms.workspaceMembers[wmKey("ws-1", user1)] = activeMembership("ws-1", user1)
			ms.workspaceMembers[wmKey("ws-1", user2)] = activeMembership("ws-1", user2)
			ms.workspaceMembers[wmKey(tc.member.WorkspaceID, tc.member.UserID)] = tc.member
			dms := &fakeDMStore{}

			_, err := service.NewDMService(dms, ms).CreateGroupConversation(context.Background(), service.CreateGroupConversationInput{
				WorkspaceID: "ws-1", CallerID: user1, ParticipantUserIDs: []string{user2, user3},
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

	got, err := service.NewDMService(dms, newFakeMemberStore()).ListConversations(context.Background(), "ws-1", "user-1")
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

	_, err := service.NewDMService(&fakeDMStore{listVisibleErr: want}, newFakeMemberStore()).ListConversations(context.Background(), "ws-1", "user-1")
	if !errors.Is(err, want) {
		t.Fatalf("expected storage error, got %v", err)
	}
}

func TestDMService_GetConversation_ReturnsVisibleConversation(t *testing.T) {
	dms := &fakeDMStore{visibleConversation: domain.DMConversation{
		ID: "dm-1", WorkspaceID: "ws-1", Type: domain.DMConversationTypeGroup, Status: domain.DMConversationStatusActive,
	}}

	got, err := service.NewDMService(dms, newFakeMemberStore()).GetConversation(context.Background(), service.GetDMConversationInput{
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

	_, err := service.NewDMService(dms, newFakeMemberStore()).GetConversation(context.Background(), service.GetDMConversationInput{
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

func TestDMService_CreateDirectConversation_SelfDMDeniedSameUUIDDifferentForm(t *testing.T) {
	ms := newFakeMemberStore()
	ms.workspaceMembers[wmKey("ws-1", user1)] = activeMembership("ws-1", user1)
	dms := &fakeDMStore{}

	_, err := service.NewDMService(dms, ms).CreateDirectConversation(context.Background(), service.CreateDirectConversationInput{
		WorkspaceID: "ws-1", CallerID: user1, OtherUserID: user1Up,
	})
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput for self-DM via alternate UUID form, got %v", err)
	}
	if dms.createDirectCalls != 0 {
		t.Fatalf("self-DM must be denied before storage, calls=%d", dms.createDirectCalls)
	}
}

func TestDMService_CreateDirectConversation_AlternateCasingGivesSamePairKey(t *testing.T) {
	ms := newFakeMemberStore()
	ms.workspaceMembers[wmKey("ws-1", user1)] = activeMembership("ws-1", user1)
	ms.workspaceMembers[wmKey("ws-1", user2)] = activeMembership("ws-1", user2)
	dms := &fakeDMStore{createdConversation: domain.DMConversation{
		ID: "dm-1", WorkspaceID: "ws-1", Type: domain.DMConversationTypeDirect, Status: domain.DMConversationStatusActive,
	}}
	svc := service.NewDMService(dms, ms)

	if _, err := svc.CreateDirectConversation(context.Background(), service.CreateDirectConversationInput{
		WorkspaceID: "ws-1", CallerID: user1, OtherUserID: user2,
	}); err != nil {
		t.Fatalf("first call: %v", err)
	}
	key1 := dms.lastDirectInput.DirectPairKey

	if _, err := svc.CreateDirectConversation(context.Background(), service.CreateDirectConversationInput{
		WorkspaceID: "ws-1", CallerID: user2Up, OtherUserID: user1Up,
	}); err != nil {
		t.Fatalf("alternate-form reversed call: %v", err)
	}
	if dms.lastDirectInput.DirectPairKey != key1 {
		t.Fatalf("pair key must be identical regardless of UUID text form or call order: %q vs %q", key1, dms.lastDirectInput.DirectPairKey)
	}
}

func TestDMService_CreateDirectConversation_CannotCreateDuplicatePairByAlternateForm(t *testing.T) {
	ms := newFakeMemberStore()
	ms.workspaceMembers[wmKey("ws-1", user1)] = activeMembership("ws-1", user1)
	ms.workspaceMembers[wmKey("ws-1", user2)] = activeMembership("ws-1", user2)
	dms := &fakeDMStore{createdConversation: domain.DMConversation{
		ID: "dm-1", WorkspaceID: "ws-1", Type: domain.DMConversationTypeDirect, Status: domain.DMConversationStatusActive,
	}}
	svc := service.NewDMService(dms, ms)

	if _, err := svc.CreateDirectConversation(context.Background(), service.CreateDirectConversationInput{
		WorkspaceID: "ws-1", CallerID: user1, OtherUserID: user2,
	}); err != nil {
		t.Fatalf("first call: %v", err)
	}
	key1 := dms.lastDirectInput.DirectPairKey

	if _, err := svc.CreateDirectConversation(context.Background(), service.CreateDirectConversationInput{
		WorkspaceID: "ws-1", CallerID: user1Up, OtherUserID: user2Up,
	}); err != nil {
		t.Fatalf("alternate-form call: %v", err)
	}
	if dms.lastDirectInput.DirectPairKey != key1 {
		t.Fatalf("alternate UUID form must produce the same pair key, preventing duplicate conversations: %q vs %q", key1, dms.lastDirectInput.DirectPairKey)
	}
}

func TestDMService_CreateDirectConversation_SameCanonicalPairInDifferentWorkspacesIsolated(t *testing.T) {
	ms := newFakeMemberStore()
	for _, wsID := range []string{"ws-1", "ws-2"} {
		ms.workspaceMembers[wmKey(wsID, user1)] = activeMembership(wsID, user1)
		ms.workspaceMembers[wmKey(wsID, user2)] = activeMembership(wsID, user2)
	}
	dms := &fakeDMStore{createdConversation: domain.DMConversation{
		ID: "dm-x", Type: domain.DMConversationTypeDirect, Status: domain.DMConversationStatusActive,
	}}
	svc := service.NewDMService(dms, ms)

	if _, err := svc.CreateDirectConversation(context.Background(), service.CreateDirectConversationInput{
		WorkspaceID: "ws-1", CallerID: user1, OtherUserID: user2,
	}); err != nil {
		t.Fatalf("ws-1 call: %v", err)
	}
	key1, wsID1 := dms.lastDirectInput.DirectPairKey, dms.lastDirectInput.WorkspaceID

	if _, err := svc.CreateDirectConversation(context.Background(), service.CreateDirectConversationInput{
		WorkspaceID: "ws-2", CallerID: user1Up, OtherUserID: user2Up,
	}); err != nil {
		t.Fatalf("ws-2 alternate-form call: %v", err)
	}
	key2, wsID2 := dms.lastDirectInput.DirectPairKey, dms.lastDirectInput.WorkspaceID

	if key1 != key2 {
		t.Fatalf("same canonical pair must yield same pair key: %q vs %q", key1, key2)
	}
	if wsID1 == wsID2 {
		t.Fatal("workspace IDs must differ to enforce per-workspace isolation")
	}
	if wsID1 != "ws-1" || wsID2 != "ws-2" {
		t.Fatalf("wrong workspace IDs: wsID1=%q wsID2=%q", wsID1, wsID2)
	}
}

func TestDMService_CreateDirectConversation_InvalidUUIDRejected(t *testing.T) {
	svc := service.NewDMService(&fakeDMStore{}, newFakeMemberStore())
	for _, tc := range []struct {
		name  string
		input service.CreateDirectConversationInput
	}{
		{"invalid caller UUID", service.CreateDirectConversationInput{WorkspaceID: "ws-1", CallerID: "not-a-uuid", OtherUserID: user2}},
		{"invalid other user UUID", service.CreateDirectConversationInput{WorkspaceID: "ws-1", CallerID: user1, OtherUserID: "not-a-uuid"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.CreateDirectConversation(context.Background(), tc.input)
			if !errors.Is(err, domain.ErrInvalidInput) {
				t.Fatalf("expected ErrInvalidInput for invalid UUID, got %v", err)
			}
		})
	}
}

func TestDMService_CreateDirectConversation_NilUUIDRejectedBeforeStores(t *testing.T) {
	const nilUUID = "00000000-0000-0000-0000-000000000000"
	for _, input := range []service.CreateDirectConversationInput{
		{WorkspaceID: "ws-1", CallerID: nilUUID, OtherUserID: user2},
		{WorkspaceID: "ws-1", CallerID: user1, OtherUserID: nilUUID},
	} {
		ms := newFakeMemberStore()
		dms := &fakeDMStore{}
		_, err := service.NewDMService(dms, ms).CreateDirectConversation(context.Background(), input)
		if !errors.Is(err, domain.ErrInvalidInput) || ms.getEligibleCalls != 0 || dms.createDirectCalls != 0 {
			t.Fatalf("input=%+v error=%v eligible calls=%d create calls=%d", input, err, ms.getEligibleCalls, dms.createDirectCalls)
		}
	}
}

func TestDMService_CreateGroupConversation_DeduplicatesByCanonicalUUID(t *testing.T) {
	ms := newFakeMemberStore()
	for _, uid := range []string{user1, user2, user3} {
		ms.workspaceMembers[wmKey("ws-1", uid)] = activeMembership("ws-1", uid)
	}
	dms := &fakeDMStore{createdConversation: domain.DMConversation{
		ID: "gm-1", WorkspaceID: "ws-1", Type: domain.DMConversationTypeGroup, Status: domain.DMConversationStatusActive,
	}}

	_, err := service.NewDMService(dms, ms).CreateGroupConversation(context.Background(), service.CreateGroupConversationInput{
		WorkspaceID:        "ws-1",
		CallerID:           user1,
		ParticipantUserIDs: []string{user2, user2Up, user3},
	})
	if err != nil {
		t.Fatalf("CreateGroupConversation: %v", err)
	}
	assertSameStringSet(t, dms.lastGroupInput.ParticipantUserIDs, []string{user1, user2, user3})
}

func TestDMService_CreateGroupConversation_CannotBypassMinimumWithDuplicateUUIDForms(t *testing.T) {
	ms := newFakeMemberStore()
	ms.workspaceMembers[wmKey("ws-1", user1)] = activeMembership("ws-1", user1)
	ms.workspaceMembers[wmKey("ws-1", user2)] = activeMembership("ws-1", user2)
	dms := &fakeDMStore{}

	// caller=user1, invited=[user2, user1Up] — user1Up canonicalizes to user1 → only 2 unique
	_, err := service.NewDMService(dms, ms).CreateGroupConversation(context.Background(), service.CreateGroupConversationInput{
		WorkspaceID:        "ws-1",
		CallerID:           user1,
		ParticipantUserIDs: []string{user2, user1Up},
	})
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput when alternate UUID forms collapse below minimum, got %v", err)
	}
	if dms.createGroupCalls != 0 {
		t.Fatalf("undersized group must be denied before storage, calls=%d", dms.createGroupCalls)
	}
}

func TestDMService_CreateGroupConversation_InvalidUUIDRejected(t *testing.T) {
	svc := service.NewDMService(&fakeDMStore{}, newFakeMemberStore())
	for _, tc := range []struct {
		name  string
		input service.CreateGroupConversationInput
	}{
		{"invalid caller UUID", service.CreateGroupConversationInput{WorkspaceID: "ws-1", CallerID: "not-a-uuid", ParticipantUserIDs: []string{user2, user3}}},
		{"invalid participant UUID", service.CreateGroupConversationInput{WorkspaceID: "ws-1", CallerID: user1, ParticipantUserIDs: []string{"not-a-uuid", user3}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.CreateGroupConversation(context.Background(), tc.input)
			if !errors.Is(err, domain.ErrInvalidInput) {
				t.Fatalf("expected ErrInvalidInput for invalid UUID, got %v", err)
			}
		})
	}
}

func TestDMService_GetOrCreateDirectConversation_ReturnsCreatedSemantics(t *testing.T) {
	ms := newFakeMemberStore()
	ms.workspaceMembers[wmKey("ws-1", user1)] = activeMembership("ws-1", user1)
	ms.workspaceMembers[wmKey("ws-1", user2)] = activeMembership("ws-1", user2)
	dms := &fakeDMStore{
		createdConversation: domain.DMConversation{ID: "dm-1", Type: domain.DMConversationTypeDirect},
		directCreated:       true,
	}

	result, err := service.NewDMService(dms, ms).GetOrCreateDirectConversation(
		context.Background(),
		service.CreateDirectConversationInput{WorkspaceID: "ws-1", CallerID: user1, OtherUserID: user2},
	)
	if err != nil {
		t.Fatalf("GetOrCreateDirectConversation: %v", err)
	}
	if !result.Created || result.Conversation.ID != "dm-1" {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestDMService_SearchDMCandidates_UsesTrimmedQueryAndBoundedLimit(t *testing.T) {
	ms := newFakeMemberStore()
	ms.workspaceMembers[wmKey("ws-1", user1)] = activeMembership("ws-1", user1)
	ms.dmCandidates = []domain.DMCandidate{{UserID: user2, DisplayName: "Ana"}}
	svc := service.NewDMService(&fakeDMStore{}, ms)

	got, err := svc.SearchDMCandidates(context.Background(), service.SearchDMCandidatesInput{
		WorkspaceID: "ws-1", CallerID: user1, Query: "  an  ", Limit: 999,
	})
	if err != nil {
		t.Fatalf("SearchDMCandidates: %v", err)
	}
	if len(got) != 1 || ms.dmCandidateQuery != "an" || ms.dmCandidateLimit != 50 {
		t.Fatalf("unexpected search: got=%+v query=%q limit=%d", got, ms.dmCandidateQuery, ms.dmCandidateLimit)
	}

	if _, err := svc.SearchDMCandidates(context.Background(), service.SearchDMCandidatesInput{
		WorkspaceID: "ws-1", CallerID: user1, Query: "ana",
	}); err != nil {
		t.Fatalf("default-limit search: %v", err)
	}
	if ms.dmCandidateLimit != 20 {
		t.Fatalf("default limit = %d, want 20", ms.dmCandidateLimit)
	}
}

func TestDMService_SearchDMCandidates_RejectsInvalidSearch(t *testing.T) {
	svc := service.NewDMService(&fakeDMStore{}, newFakeMemberStore())
	for _, input := range []service.SearchDMCandidatesInput{
		{WorkspaceID: "ws-1", CallerID: user1, Query: ""},
		{WorkspaceID: "ws-1", CallerID: user1, Query: "a"},
		{WorkspaceID: "ws-1", CallerID: user1, Query: longString(65)},
		{WorkspaceID: "ws-1", CallerID: user1, Query: "ana", Limit: -1},
	} {
		if _, err := svc.SearchDMCandidates(context.Background(), input); !errors.Is(err, domain.ErrInvalidInput) {
			t.Fatalf("expected invalid input for %+v, got %v", input, err)
		}
	}
}

func TestDMService_SearchDMCandidates_RequiresActiveCallerAndPropagatesStoreError(t *testing.T) {
	ms := newFakeMemberStore()
	ms.workspaceMembers[wmKey("ws-1", user1)] = domain.WorkspaceMember{
		WorkspaceID: "ws-1", UserID: user1, Status: domain.MemberStatusSuspended,
	}
	svc := service.NewDMService(&fakeDMStore{}, ms)
	if _, err := svc.SearchDMCandidates(context.Background(), service.SearchDMCandidatesInput{
		WorkspaceID: "ws-1", CallerID: user1, Query: "an",
	}); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("inactive caller: got %v", err)
	}

	ms.workspaceMembers[wmKey("ws-1", user1)] = activeMembership("ws-1", user1)
	want := errors.New("candidate store failed")
	ms.dmCandidateErr = want
	if _, err := svc.SearchDMCandidates(context.Background(), service.SearchDMCandidatesInput{
		WorkspaceID: "ws-1", CallerID: user1, Query: "an",
	}); !errors.Is(err, want) {
		t.Fatalf("store error: got %v", err)
	}
}

type fakeDMStore struct {
	createdConversation  domain.DMConversation
	directCreated        bool
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

	groupCandidates       []domain.DMCandidate
	groupCandidatesErr    error
	candidateCalls        []groupCandidateCall
	participantPage       storage.DMParticipantPage
	participantPageErr    error
	participantPageCalls  int
	addParticipantsResult storage.AddMembersResult
	addParticipantsErr    error
	addParticipantsCalls  int
	lastAddParticipants   storage.AddGroupParticipantsInput
}

// groupCandidateCall records the scope the service handed the store.
type groupCandidateCall struct {
	WorkspaceID    string
	ConversationID string
	CallerID       string
	Query          string
	Limit          int
}

func (f *fakeDMStore) CreateDirectConversation(_ context.Context, input storage.CreateDirectConversationInput) (storage.CreateDirectConversationResult, error) {
	f.createDirectCalls++
	f.lastDirectInput = input
	return storage.CreateDirectConversationResult{Conversation: f.createdConversation, Created: f.directCreated}, f.createDirectErr
}

func (f *fakeDMStore) CreateGroupConversation(_ context.Context, input storage.CreateGroupConversationInput) (domain.DMConversation, error) {
	f.createGroupCalls++
	f.lastGroupInput = input
	return f.createdConversation, f.createGroupErr
}

func (f *fakeDMStore) SearchGroupParticipantCandidates(
	_ context.Context, workspaceID, conversationID, callerID, prefix string, limit int,
) ([]domain.DMCandidate, error) {
	f.candidateCalls = append(f.candidateCalls, groupCandidateCall{
		WorkspaceID: workspaceID, ConversationID: conversationID, CallerID: callerID,
		Query: prefix, Limit: limit,
	})
	return f.groupCandidates, f.groupCandidatesErr
}

func (f *fakeDMStore) ListParticipantProfiles(_ context.Context, _, _ string, _ int) (storage.DMParticipantPage, error) {
	f.participantPageCalls++
	return f.participantPage, f.participantPageErr
}

func (f *fakeDMStore) AddGroupParticipants(_ context.Context, input storage.AddGroupParticipantsInput) (storage.AddMembersResult, error) {
	f.addParticipantsCalls++
	f.lastAddParticipants = input
	return f.addParticipantsResult, f.addParticipantsErr
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

func (f *fakeDMStore) ListVisibleConversationsWithParticipantIDs(_ context.Context, _, _ string) ([]domain.DMConversationWithParticipantIDs, error) {
	return nil, nil
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
