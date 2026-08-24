package ws

import (
	"context"
	"testing"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

// ── test doubles for serviceAuthorizer ───────────────────────────────────────

type fakeChannelChecker struct {
	result bool
	err    error
}

func (f *fakeChannelChecker) CanRead(_ context.Context, _, _, _ string) (bool, error) {
	return f.result, f.err
}

func (f *fakeChannelChecker) GetVisibleChannelByID(_ context.Context, workspaceID, channelID, _ string) (domain.Channel, error) {
	if f.err != nil {
		return domain.Channel{}, f.err
	}
	if !f.result {
		return domain.Channel{}, domain.ErrNotFound
	}
	return domain.Channel{ID: channelID, WorkspaceID: workspaceID}, nil
}

type exactChannelVisibilityChecker struct {
	canReadCalls    int
	visibilityCalls int
	workspaceID     string
	channelID       string
	userID          string
	visible         bool
}

func (f *exactChannelVisibilityChecker) CanRead(_ context.Context, _, _, _ string) (bool, error) {
	f.canReadCalls++
	return true, nil
}

func (f *exactChannelVisibilityChecker) GetVisibleChannelByID(
	_ context.Context,
	workspaceID, channelID, userID string,
) (domain.Channel, error) {
	f.visibilityCalls++
	f.workspaceID = workspaceID
	f.channelID = channelID
	f.userID = userID
	if f.visible {
		return domain.Channel{ID: channelID, WorkspaceID: workspaceID}, nil
	}
	return domain.Channel{}, domain.ErrNotFound
}

// fakeDMStore implements storage.DMStore for subscription tests.
type fakeDMStore struct {
	conversation   domain.DMConversation
	err            error
	workspaceID    string
	conversationID string
	userID         string
}

func (f *fakeDMStore) CreateDirectConversation(_ context.Context, _ storage.CreateDirectConversationInput) (storage.CreateDirectConversationResult, error) {
	return storage.CreateDirectConversationResult{}, nil
}
func (f *fakeDMStore) SearchGroupParticipantCandidates(_ context.Context, _, _, _, _ string, _ int) ([]domain.DMCandidate, error) {
	return nil, nil
}

func (f *fakeDMStore) AddGroupParticipants(_ context.Context, _ storage.AddGroupParticipantsInput) (storage.AddMembersResult, error) {
	return storage.AddMembersResult{}, nil
}

func (f *fakeDMStore) ListParticipantProfiles(_ context.Context, _, _ string, _ int) (storage.DMParticipantPage, error) {
	return storage.DMParticipantPage{}, nil
}

func (f *fakeDMStore) ListParticipantProfilesByIDs(_ context.Context, _, _ string, _ []string) ([]domain.CallParticipantProfile, error) {
	return nil, nil
}

func (f *fakeDMStore) CreateGroupConversation(_ context.Context, _ storage.CreateGroupConversationInput) (domain.DMConversation, error) {
	return domain.DMConversation{}, nil
}
func (f *fakeDMStore) ListVisibleConversationsByUser(_ context.Context, _, _ string) ([]domain.DMConversation, error) {
	return nil, nil
}
func (f *fakeDMStore) GetDirectCounterpartProfile(_ context.Context, _, _, _ string) (domain.DMDirectProfile, error) {
	return domain.DMDirectProfile{}, nil
}
func (f *fakeDMStore) GetVisibleConversationByID(_ context.Context, workspaceID, conversationID, userID string) (domain.DMConversation, error) {
	f.workspaceID = workspaceID
	f.conversationID = conversationID
	f.userID = userID
	return f.conversation, f.err
}
func (f *fakeDMStore) ListVisibleConversationsWithParticipantIDs(_ context.Context, _, _ string) ([]domain.DMConversationWithParticipantIDs, error) {
	return nil, nil
}

// ── serviceAuthorizer tests ───────────────────────────────────────────────────

func TestServiceAuthorizer_Channel_ReusesMessageVisibilityCheck(t *testing.T) {
	channels := &exactChannelVisibilityChecker{}
	auth := NewServiceAuthorizer(channels, &fakeDMStore{})

	ok, err := auth.CanAccess(context.Background(), "user-1", "ws-1", TargetTypeChannel, "ch-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatal("inaccessible channel must be denied")
	}
	if channels.visibilityCalls != 1 || channels.canReadCalls != 0 {
		t.Fatalf(
			"authorization must reuse GetVisibleChannelByID: visibility calls=%d, CanRead calls=%d",
			channels.visibilityCalls,
			channels.canReadCalls,
		)
	}
	if channels.workspaceID != "ws-1" || channels.channelID != "ch-1" || channels.userID != "user-1" {
		t.Fatalf(
			"visibility check received workspace=%q channel=%q user=%q",
			channels.workspaceID,
			channels.channelID,
			channels.userID,
		)
	}
}

func TestServiceAuthorizer_UsesCanonicalNormalizedChannelID(t *testing.T) {
	const canonicalID = "550e8400-e29b-41d4-a716-446655440000"
	target, err := normalizeSubscriptionTarget(ClientMessage{
		Type: ClientMessageTypeSubscribe, TargetType: TargetTypeChannel,
		TargetID: "550E8400E29B41D4A716446655440000",
	})
	if err != nil {
		t.Fatalf("normalize target: %v", err)
	}
	channels := &exactChannelVisibilityChecker{visible: true}
	auth := NewServiceAuthorizer(channels, &fakeDMStore{})

	ok, err := auth.CanAccess(context.Background(), "user-1", "ws-1", target.targetType, target.targetID)
	if err != nil || !ok {
		t.Fatalf("canonical channel authorization = (%v, %v), want allowed", ok, err)
	}
	if channels.channelID != canonicalID {
		t.Fatalf("GetVisibleChannelByID target = %q, want %q", channels.channelID, canonicalID)
	}
}

func TestServiceAuthorizer_UsesCanonicalNormalizedConversationID(t *testing.T) {
	const canonicalID = "550e8400-e29b-41d4-a716-446655440000"
	target, err := normalizeSubscriptionTarget(ClientMessage{
		Type: ClientMessageTypeSubscribe, TargetType: TargetTypeDM,
		TargetID: "{550E8400-E29B-41D4-A716-446655440000}",
	})
	if err != nil {
		t.Fatalf("normalize target: %v", err)
	}
	dms := &fakeDMStore{conversation: domain.DMConversation{ID: canonicalID, WorkspaceID: "ws-1"}}
	auth := NewServiceAuthorizer(&fakeChannelChecker{}, dms)

	ok, err := auth.CanAccess(context.Background(), "user-1", "ws-1", target.targetType, target.targetID)
	if err != nil || !ok {
		t.Fatalf("canonical DM authorization = (%v, %v), want allowed", ok, err)
	}
	if dms.conversationID != canonicalID {
		t.Fatalf("GetVisibleConversationByID target = %q, want %q", dms.conversationID, canonicalID)
	}
}

func TestServiceAuthorizer_Channel_ActiveMember_PublicChannel_Allowed(t *testing.T) {
	auth := NewServiceAuthorizer(&fakeChannelChecker{result: true}, &fakeDMStore{})
	ok, err := auth.CanAccess(context.Background(), "user-1", "ws-1", TargetTypeChannel, "ch-pub")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("active workspace member should access public channel")
	}
}

func TestServiceAuthorizer_Channel_PrivateChannel_NonMember_Denied(t *testing.T) {
	// Checker returns false: user lacks channel membership for private channel.
	auth := NewServiceAuthorizer(&fakeChannelChecker{result: false}, &fakeDMStore{})
	ok, err := auth.CanAccess(context.Background(), "user-1", "ws-1", TargetTypeChannel, "ch-private")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatal("private channel non-member must be denied")
	}
}

func TestServiceAuthorizer_Channel_PrivateChannel_Member_Allowed(t *testing.T) {
	// Checker returns true: user has both workspace and channel membership.
	auth := NewServiceAuthorizer(&fakeChannelChecker{result: true}, &fakeDMStore{})
	ok, err := auth.CanAccess(context.Background(), "user-1", "ws-1", TargetTypeChannel, "ch-private")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("channel member should access private channel")
	}
}

func TestServiceAuthorizer_Channel_GuestWithReadAccess_Allowed(t *testing.T) {
	// Role is intentionally absent from the WebSocket decision: the canonical
	// visibility query has already established that this active Guest may read.
	auth := NewServiceAuthorizer(&fakeChannelChecker{result: true}, &fakeDMStore{})
	ok, err := auth.CanAccess(context.Background(), "guest-1", "ws-1", TargetTypeChannel, "ch-private")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("Guest with canonical channel read access must be allowed")
	}
}

func TestServiceAuthorizer_Channel_GuestWithoutReadAccess_Denied(t *testing.T) {
	auth := NewServiceAuthorizer(&fakeChannelChecker{result: false}, &fakeDMStore{})
	ok, err := auth.CanAccess(context.Background(), "guest-1", "ws-1", TargetTypeChannel, "ch-private")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatal("Guest without canonical channel read access must be denied")
	}
}

func TestServiceAuthorizer_Channel_StaleWorkspaceMembership_Denied(t *testing.T) {
	// Canonical visibility returns not found when workspace membership is
	// inactive (suspended/left), which the authorizer maps to a denial.
	auth := NewServiceAuthorizer(&fakeChannelChecker{result: false}, &fakeDMStore{})
	ok, err := auth.CanAccess(context.Background(), "user-1", "ws-1", TargetTypeChannel, "ch-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatal("stale/inactive workspace membership must deny channel access")
	}
}

func TestServiceAuthorizer_Channel_ArchivedChannel_Denied(t *testing.T) {
	// Archived channels are absent from the canonical visibility query.
	auth := NewServiceAuthorizer(&fakeChannelChecker{result: false}, &fakeDMStore{})
	ok, err := auth.CanAccess(context.Background(), "user-1", "ws-1", TargetTypeChannel, "ch-archived")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatal("archived channel must be denied")
	}
}

func TestServiceAuthorizer_Channel_CrossWorkspace_Denied(t *testing.T) {
	// The canonical visibility query is scoped by the client's workspaceID.
	auth := NewServiceAuthorizer(&fakeChannelChecker{result: false}, &fakeDMStore{})
	ok, err := auth.CanAccess(context.Background(), "user-1", "ws-a", TargetTypeChannel, "ch-from-ws-b")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatal("cross-workspace channel must be denied")
	}
}

func TestServiceAuthorizer_Channel_VisibilityError_Propagates(t *testing.T) {
	want := domain.ErrInvalidInput // any non-ErrNotFound error
	auth := NewServiceAuthorizer(&fakeChannelChecker{err: want}, &fakeDMStore{})
	_, err := auth.CanAccess(context.Background(), "user-1", "ws-1", TargetTypeChannel, "ch-1")
	if err == nil {
		t.Fatal("expected channel checker error to propagate")
	}
}

func TestServiceAuthorizer_DM_ActiveMember_Allowed(t *testing.T) {
	conv := domain.DMConversation{ID: "dm-1", WorkspaceID: "ws-1", Status: domain.DMConversationStatusActive}
	auth := NewServiceAuthorizer(&fakeChannelChecker{}, &fakeDMStore{conversation: conv})
	ok, err := auth.CanAccess(context.Background(), "user-1", "ws-1", TargetTypeDM, "dm-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("active DM member should be allowed")
	}
}

func TestServiceAuthorizer_DM_DirectGuestParticipant_Allowed(t *testing.T) {
	conv := domain.DMConversation{
		ID: "dm-direct", WorkspaceID: "ws-1",
		Type: domain.DMConversationTypeDirect, Status: domain.DMConversationStatusActive,
	}
	auth := NewServiceAuthorizer(&fakeChannelChecker{}, &fakeDMStore{conversation: conv})
	ok, err := auth.CanAccess(context.Background(), "guest-1", "ws-1", TargetTypeDM, "dm-direct")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("Guest who is an active direct-DM participant must be allowed")
	}
}

func TestServiceAuthorizer_DM_DirectGuestOutsider_Denied(t *testing.T) {
	auth := NewServiceAuthorizer(&fakeChannelChecker{}, &fakeDMStore{err: domain.ErrNotFound})
	ok, err := auth.CanAccess(context.Background(), "guest-outsider", "ws-1", TargetTypeDM, "dm-direct")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatal("Guest outside a direct DM must be denied")
	}
}

func TestServiceAuthorizer_DM_GroupActiveParticipant_Allowed(t *testing.T) {
	conv := domain.DMConversation{
		ID: "dm-group", WorkspaceID: "ws-1",
		Type: domain.DMConversationTypeGroup, Status: domain.DMConversationStatusActive,
	}
	auth := NewServiceAuthorizer(&fakeChannelChecker{}, &fakeDMStore{conversation: conv})
	ok, err := auth.CanAccess(context.Background(), "user-1", "ws-1", TargetTypeDM, "dm-group")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("active group-DM participant must be allowed")
	}
}

func TestServiceAuthorizer_DM_GroupOutsiderOrRemovedParticipant_Denied(t *testing.T) {
	auth := NewServiceAuthorizer(&fakeChannelChecker{}, &fakeDMStore{err: domain.ErrNotFound})
	for _, userID := range []string{"external-user", "removed-user"} {
		ok, err := auth.CanAccess(context.Background(), userID, "ws-1", TargetTypeDM, "dm-group")
		if err != nil {
			t.Fatalf("%s: unexpected error: %v", userID, err)
		}
		if ok {
			t.Fatalf("%s must be denied group-DM access", userID)
		}
	}
}

func TestServiceAuthorizer_DM_NotMember_Denied(t *testing.T) {
	// GetVisibleConversationByID returns ErrNotFound for non-members (non-enumerating).
	auth := NewServiceAuthorizer(&fakeChannelChecker{}, &fakeDMStore{err: domain.ErrNotFound})
	ok, err := auth.CanAccess(context.Background(), "user-1", "ws-1", TargetTypeDM, "dm-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatal("DM non-member must be denied")
	}
}

func TestServiceAuthorizer_DM_StaleWorkspaceMembership_Denied(t *testing.T) {
	// SQL query enforces workspace membership: returns ErrNotFound if workspace
	// membership is inactive, so stale dm_members cannot bypass the check.
	auth := NewServiceAuthorizer(&fakeChannelChecker{}, &fakeDMStore{err: domain.ErrNotFound})
	ok, err := auth.CanAccess(context.Background(), "user-1", "ws-1", TargetTypeDM, "dm-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatal("stale workspace membership must deny DM access")
	}
}

func TestServiceAuthorizer_DM_CrossWorkspace_Denied(t *testing.T) {
	// GetVisibleConversationByID is called with the client's workspaceID; if the DM
	// belongs to another workspace, the SQL returns ErrNotFound.
	auth := NewServiceAuthorizer(&fakeChannelChecker{}, &fakeDMStore{err: domain.ErrNotFound})
	ok, err := auth.CanAccess(context.Background(), "user-1", "ws-a", TargetTypeDM, "dm-in-ws-b")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatal("cross-workspace DM must be denied")
	}
}

func TestServiceAuthorizer_DM_StoreError_Propagates(t *testing.T) {
	want := domain.ErrInvalidInput // any non-ErrNotFound error
	auth := NewServiceAuthorizer(&fakeChannelChecker{}, &fakeDMStore{err: want})
	_, err := auth.CanAccess(context.Background(), "user-1", "ws-1", TargetTypeDM, "dm-1")
	if err == nil {
		t.Fatal("expected DM store error to propagate")
	}
}

func TestServiceAuthorizer_UnknownTargetType_DeniedSecurely(t *testing.T) {
	auth := NewServiceAuthorizer(&fakeChannelChecker{result: true}, &fakeDMStore{})
	ok, err := auth.CanAccess(context.Background(), "user-1", "ws-1", TargetType("unknown"), "target-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatal("unknown target type must be denied (fail secure)")
	}
}
