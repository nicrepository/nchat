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

// fakeDMStore implements storage.DMStore for subscription tests.
type fakeDMStore struct {
	conversation domain.DMConversation
	err          error
}

func (f *fakeDMStore) CreateDirectConversation(_ context.Context, _ storage.CreateDirectConversationInput) (domain.DMConversation, error) {
	return domain.DMConversation{}, nil
}
func (f *fakeDMStore) CreateGroupConversation(_ context.Context, _ storage.CreateGroupConversationInput) (domain.DMConversation, error) {
	return domain.DMConversation{}, nil
}
func (f *fakeDMStore) ListVisibleConversationsByUser(_ context.Context, _, _ string) ([]domain.DMConversation, error) {
	return nil, nil
}
func (f *fakeDMStore) GetVisibleConversationByID(_ context.Context, _, _, _ string) (domain.DMConversation, error) {
	return f.conversation, f.err
}
func (f *fakeDMStore) ListVisibleConversationsWithParticipantIDs(_ context.Context, _, _ string) ([]domain.DMConversationWithParticipantIDs, error) {
	return nil, nil
}

// ── serviceAuthorizer tests ───────────────────────────────────────────────────

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

func TestServiceAuthorizer_Channel_StaleWorkspaceMembership_Denied(t *testing.T) {
	// Checker returns false when workspace membership is inactive (suspended/left).
	// PermissionService.CanRead enforces active workspace membership in SQL, so it
	// returns false rather than an error when membership is stale.
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
	// Archived channels are denied by PermissionService.CanRead; checker returns false.
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
	// Cross-workspace channel: checker is called with the client's workspaceID,
	// which does not match the channel's workspace, so CanRead returns false.
	auth := NewServiceAuthorizer(&fakeChannelChecker{result: false}, &fakeDMStore{})
	ok, err := auth.CanAccess(context.Background(), "user-1", "ws-a", TargetTypeChannel, "ch-from-ws-b")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatal("cross-workspace channel must be denied")
	}
}

func TestServiceAuthorizer_Channel_CheckerError_Propagates(t *testing.T) {
	want := domain.ErrNotFound // any non-nil error
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
