package service_test

import (
	"context"
	"errors"
	"testing"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/service"
)

type fakePinStore struct {
	addErr  error
	listOut []domain.PinnedMessage
	listErr error

	addCalls    int
	removeCalls int
	lastChannel string
	lastMessage string
	lastActor   string
}

func (f *fakePinStore) AddPin(_ context.Context, channelID, messageID, pinnedByUserID string) error {
	f.addCalls++
	f.lastChannel, f.lastMessage, f.lastActor = channelID, messageID, pinnedByUserID
	return f.addErr
}

func (f *fakePinStore) RemovePin(_ context.Context, channelID, messageID string) error {
	f.removeCalls++
	f.lastChannel, f.lastMessage = channelID, messageID
	return nil
}

func (f *fakePinStore) ListPins(_ context.Context, channelID, viewerID string) ([]domain.PinnedMessage, error) {
	f.lastChannel = channelID
	return f.listOut, f.listErr
}

// pinFixture builds a PinService whose permission layer sees an active
// workspace member of the given role, a channel of the given type, and
// (optionally) a channel membership.
func pinFixture(t *testing.T, role domain.WorkspaceRole, chType domain.ChannelType, cm *domain.ChannelMember) (*service.PinService, *fakePinStore) {
	t.Helper()
	ms := newFakeMemberStore()
	ms.workspaceMembers[wmKey("ws-1", "user-1")] = domain.WorkspaceMember{
		WorkspaceID: "ws-1", UserID: "user-1", Role: role, Status: domain.MemberStatusActive,
	}
	if cm != nil {
		ms.channelMembers[cmKey(cm.ChannelID, cm.UserID)] = *cm
	}
	channels := &fakeChannelStore{channel: domain.Channel{
		ID: "ch-1", WorkspaceID: "ws-1", Type: chType, Status: domain.ChannelStatusActive,
	}}
	store := &fakePinStore{}
	svc := service.NewPinService(store, service.NewPermissionService(ms, channels))
	return svc, store
}

func pinInput() service.PinActionInput {
	return service.PinActionInput{WorkspaceID: " ws-1 ", ChannelID: " ch-1 ", MessageID: " msg-1 ", ActorUserID: " user-1 "}
}

func TestPinService_Pin_AdminAllowed_TrimsAndDelegates(t *testing.T) {
	svc, store := pinFixture(t, domain.WorkspaceRoleAdmin, domain.ChannelTypePublic, nil)
	if err := svc.Pin(context.Background(), pinInput()); err != nil {
		t.Fatalf("Pin: %v", err)
	}
	if store.addCalls != 1 || store.lastChannel != "ch-1" || store.lastMessage != "msg-1" || store.lastActor != "user-1" {
		t.Fatalf("expected trimmed delegation, got ch=%q msg=%q actor=%q calls=%d",
			store.lastChannel, store.lastMessage, store.lastActor, store.addCalls)
	}
}

func TestPinService_Pin_RegularMember_Forbidden(t *testing.T) {
	svc, store := pinFixture(t, domain.WorkspaceRoleMember, domain.ChannelTypePublic, nil)
	if err := svc.Pin(context.Background(), pinInput()); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("expected ErrForbidden, got %v", err)
	}
	if store.addCalls != 0 {
		t.Fatal("store must not be called when authorization fails")
	}
}

func TestPinService_Pin_NonMember_NotFound(t *testing.T) {
	// No workspace membership at all → cannot read channel → 404 (non-enumerating).
	channels := &fakeChannelStore{channel: domain.Channel{ID: "ch-1", WorkspaceID: "ws-1", Type: domain.ChannelTypePublic, Status: domain.ChannelStatusActive}}
	svc := service.NewPinService(&fakePinStore{}, service.NewPermissionService(newFakeMemberStore(), channels))
	if err := svc.Pin(context.Background(), pinInput()); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestPinService_Pin_ChannelModerator_Allowed(t *testing.T) {
	cm := &domain.ChannelMember{ChannelID: "ch-1", UserID: "user-1", Role: domain.ChannelRoleModerator}
	svc, store := pinFixture(t, domain.WorkspaceRoleMember, domain.ChannelTypePrivate, cm)
	if err := svc.Pin(context.Background(), pinInput()); err != nil {
		t.Fatalf("moderator Pin: %v", err)
	}
	if store.addCalls != 1 {
		t.Fatal("expected store.AddPin to be called for moderator")
	}
}

func TestPinService_Pin_PropagatesLimitReached(t *testing.T) {
	svc, store := pinFixture(t, domain.WorkspaceRoleAdmin, domain.ChannelTypePublic, nil)
	store.addErr = domain.ErrPinLimitReached
	if err := svc.Pin(context.Background(), pinInput()); !errors.Is(err, domain.ErrPinLimitReached) {
		t.Fatalf("expected ErrPinLimitReached, got %v", err)
	}
}

func TestPinService_Pin_MissingFields_InvalidInput(t *testing.T) {
	svc, _ := pinFixture(t, domain.WorkspaceRoleAdmin, domain.ChannelTypePublic, nil)
	for _, in := range []service.PinActionInput{
		{ChannelID: "c", MessageID: "m", ActorUserID: "u"},
		{WorkspaceID: "w", MessageID: "m", ActorUserID: "u"},
		{WorkspaceID: "w", ChannelID: "c", ActorUserID: "u"},
		{WorkspaceID: "w", ChannelID: "c", MessageID: "m"},
	} {
		if err := svc.Pin(context.Background(), in); !errors.Is(err, domain.ErrInvalidInput) {
			t.Fatalf("input %+v: expected ErrInvalidInput, got %v", in, err)
		}
	}
}

func TestPinService_ListPins_MemberAllowed(t *testing.T) {
	svc, store := pinFixture(t, domain.WorkspaceRoleMember, domain.ChannelTypePublic, nil)
	store.listOut = []domain.PinnedMessage{{PinnedByUserID: "user-9"}}
	got, err := svc.ListPins(context.Background(), service.ListPinsInput{WorkspaceID: "ws-1", ChannelID: "ch-1", ViewerID: "user-1"})
	if err != nil {
		t.Fatalf("ListPins: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 pin, got %d", len(got))
	}
}

func TestPinService_ListPins_NoReadAccess_NotFound(t *testing.T) {
	// Private channel, no channel membership → cannot read → 404.
	svc, _ := pinFixture(t, domain.WorkspaceRoleMember, domain.ChannelTypePrivate, nil)
	_, err := svc.ListPins(context.Background(), service.ListPinsInput{WorkspaceID: "ws-1", ChannelID: "ch-1", ViewerID: "user-1"})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}
