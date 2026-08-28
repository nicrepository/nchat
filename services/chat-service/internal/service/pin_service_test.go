package service_test

import (
	"context"
	"errors"
	"testing"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/service"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

type fakePinStore struct {
	addErr    error
	removeErr error
	listOut   []domain.PinnedMessage
	listErr   error

	addCalls       int
	removeCalls    int
	lastTargetType string
	lastTargetID   string
	lastMessage    string
	lastActor      string
}

func (f *fakePinStore) AddPin(_ context.Context, _ string, targetType, targetID, messageID, pinnedByUserID string) error {
	f.addCalls++
	f.lastTargetType, f.lastTargetID, f.lastMessage, f.lastActor = targetType, targetID, messageID, pinnedByUserID
	return f.addErr
}

func (f *fakePinStore) RemovePin(_ context.Context, _ string, targetType, targetID, messageID, actorUserID string) error {
	f.removeCalls++
	f.lastTargetType, f.lastTargetID, f.lastMessage, f.lastActor = targetType, targetID, messageID, actorUserID
	return f.removeErr
}

func (f *fakePinStore) ListPins(_ context.Context, _ string, targetType, targetID, viewerID string) (storage.ListPinsResult, error) {
	f.lastTargetType, f.lastTargetID = targetType, targetID
	f.lastActor = viewerID
	return storage.ListPinsResult{Pins: f.listOut, TotalCount: len(f.listOut)}, f.listErr
}

// pinFixture builds a PinService backed by a store fake. Authorization lives in
// storage, so service tests assert validation and delegation only.
func pinFixture(t *testing.T) (*service.PinService, *fakePinStore) {
	t.Helper()
	store := &fakePinStore{}
	svc := service.NewPinService(store)
	return svc, store
}

func pinInput() service.PinActionInput {
	return service.PinActionInput{WorkspaceID: " ws-1 ", TargetType: " channel ", TargetID: " ch-1 ", MessageID: " msg-1 ", ActorUserID: " user-1 "}
}

func TestPinService_Pin_TrimsAndDelegatesChannelTarget(t *testing.T) {
	svc, store := pinFixture(t)
	if err := svc.Pin(context.Background(), pinInput()); err != nil {
		t.Fatalf("Pin: %v", err)
	}
	if store.addCalls != 1 || store.lastTargetType != "channel" || store.lastTargetID != "ch-1" || store.lastMessage != "msg-1" || store.lastActor != "user-1" {
		t.Fatalf("expected trimmed delegation, got type=%q target=%q msg=%q actor=%q calls=%d",
			store.lastTargetType, store.lastTargetID, store.lastMessage, store.lastActor, store.addCalls)
	}
}

func TestPinService_GuestCanPinAndUnpinReadableChannel(t *testing.T) {
	svc, store := pinFixture(t)
	if err := svc.Pin(context.Background(), pinInput()); err != nil {
		t.Fatalf("guest with read access should pin: %v", err)
	}
	if err := svc.Unpin(context.Background(), pinInput()); err != nil {
		t.Fatalf("guest with read access should unpin: %v", err)
	}
	if store.addCalls != 1 || store.removeCalls != 1 {
		t.Fatalf("store should be called for readable guest pin/unpin, add=%d remove=%d", store.addCalls, store.removeCalls)
	}
}

func TestPinService_GuestCanPinAndUnpinReadableDM(t *testing.T) {
	svc, store := pinFixture(t)
	input := service.PinActionInput{
		WorkspaceID: "ws-1", TargetType: "dm", TargetID: "dm-1", MessageID: "msg-1", ActorUserID: "user-1",
	}
	if err := svc.Pin(context.Background(), input); err != nil {
		t.Fatalf("guest with DM read access should pin: %v", err)
	}
	if err := svc.Unpin(context.Background(), input); err != nil {
		t.Fatalf("guest with DM read access should unpin: %v", err)
	}
	if store.addCalls != 1 || store.removeCalls != 1 || store.lastTargetType != "dm" || store.lastTargetID != "dm-1" {
		t.Fatalf("expected DM pin/unpin delegation, type=%q target=%q add=%d remove=%d",
			store.lastTargetType, store.lastTargetID, store.addCalls, store.removeCalls)
	}
}

func TestPinService_Pin_PropagatesStoreNotFound(t *testing.T) {
	store := &fakePinStore{addErr: domain.ErrNotFound}
	svc := service.NewPinService(store)
	if err := svc.Pin(context.Background(), pinInput()); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestPinService_Pin_DelegatesReadAuthorizationToStore(t *testing.T) {
	svc, store := pinFixture(t)
	if err := svc.Pin(context.Background(), pinInput()); err != nil {
		t.Fatalf("Pin: %v", err)
	}
	if store.addCalls != 1 {
		t.Fatal("expected store.AddPin to enforce read authorization")
	}
}

func TestPinService_Pin_PropagatesLimitReached(t *testing.T) {
	svc, store := pinFixture(t)
	store.addErr = domain.ErrPinLimitReached
	if err := svc.Pin(context.Background(), pinInput()); !errors.Is(err, domain.ErrPinLimitReached) {
		t.Fatalf("expected ErrPinLimitReached, got %v", err)
	}
}

func TestPinService_Pin_MissingFields_InvalidInput(t *testing.T) {
	svc, _ := pinFixture(t)
	for _, in := range []service.PinActionInput{
		{TargetType: "channel", TargetID: "c", MessageID: "m", ActorUserID: "u"},
		{WorkspaceID: "w", TargetID: "c", MessageID: "m", ActorUserID: "u"},
		{WorkspaceID: "w", TargetType: "channel", MessageID: "m", ActorUserID: "u"},
		{WorkspaceID: "w", TargetType: "channel", TargetID: "c", ActorUserID: "u"},
		{WorkspaceID: "w", TargetType: "channel", TargetID: "c", MessageID: "m"},
		{WorkspaceID: "w", TargetType: "thread", TargetID: "c", MessageID: "m", ActorUserID: "u"},
	} {
		if err := svc.Pin(context.Background(), in); !errors.Is(err, domain.ErrInvalidInput) {
			t.Fatalf("input %+v: expected ErrInvalidInput, got %v", in, err)
		}
	}
}

func TestPinService_ListPins_DelegatesToStore(t *testing.T) {
	svc, store := pinFixture(t)
	store.listOut = []domain.PinnedMessage{{PinnedByUserID: "user-9"}}
	got, err := svc.ListPins(context.Background(), service.ListPinsInput{WorkspaceID: "ws-1", TargetType: "channel", TargetID: "ch-1", ViewerID: "user-1"})
	if err != nil {
		t.Fatalf("ListPins: %v", err)
	}
	if len(got.Pins) != 1 || got.TotalCount != 1 {
		t.Fatalf("expected 1 pin with total, got %+v", got)
	}
}

func TestPinService_ListPins_PropagatesStoreNotFound(t *testing.T) {
	svc, store := pinFixture(t)
	store.listErr = domain.ErrNotFound
	_, err := svc.ListPins(context.Background(), service.ListPinsInput{WorkspaceID: "ws-1", TargetType: "channel", TargetID: "ch-1", ViewerID: "user-1"})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestPinService_Pin_DMDelegatesToStoreForUnifiedAuthorization(t *testing.T) {
	svc, store := pinFixture(t)
	err := svc.Pin(context.Background(), service.PinActionInput{
		WorkspaceID: "ws-1", TargetType: "dm", TargetID: "dm-1", MessageID: "msg-1", ActorUserID: "user-1",
	})
	if err != nil {
		t.Fatalf("DM Pin: %v", err)
	}
	if store.addCalls != 1 || store.lastTargetType != "dm" || store.lastTargetID != "dm-1" {
		t.Fatalf("expected DM target delegation, got type=%q target=%q calls=%d", store.lastTargetType, store.lastTargetID, store.addCalls)
	}
}

func TestPinService_Unpin_TrimsAndDelegatesChannelTarget(t *testing.T) {
	svc, store := pinFixture(t)
	if err := svc.Unpin(context.Background(), pinInput()); err != nil {
		t.Fatalf("Unpin: %v", err)
	}
	if store.removeCalls != 1 || store.lastTargetType != "channel" || store.lastTargetID != "ch-1" || store.lastMessage != "msg-1" || store.lastActor != "user-1" {
		t.Fatalf("expected trimmed unpin delegation, got type=%q target=%q msg=%q actor=%q calls=%d",
			store.lastTargetType, store.lastTargetID, store.lastMessage, store.lastActor, store.removeCalls)
	}
}

func TestPinService_Unpin_DMPropagatesNotFound(t *testing.T) {
	svc, store := pinFixture(t)
	store.removeErr = domain.ErrNotFound
	err := svc.Unpin(context.Background(), service.PinActionInput{
		WorkspaceID: "ws-1", TargetType: "dm", TargetID: "dm-1", MessageID: "msg-1", ActorUserID: "user-1",
	})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	if store.removeCalls != 1 || store.lastTargetType != "dm" || store.lastTargetID != "dm-1" {
		t.Fatalf("expected DM unpin delegation, got type=%q target=%q calls=%d", store.lastTargetType, store.lastTargetID, store.removeCalls)
	}
}

func TestPinService_Unpin_InvalidInput(t *testing.T) {
	svc, store := pinFixture(t)
	err := svc.Unpin(context.Background(), service.PinActionInput{
		WorkspaceID: "ws-1", TargetType: "thread", TargetID: "ch-1", MessageID: "msg-1", ActorUserID: "user-1",
	})
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput, got %v", err)
	}
	if store.removeCalls != 0 {
		t.Fatal("invalid unpin input must not call store")
	}
}

func TestPinService_ListPins_TrimsAndDelegatesDM(t *testing.T) {
	svc, store := pinFixture(t)
	_, err := svc.ListPins(context.Background(), service.ListPinsInput{
		WorkspaceID: " ws-1 ", TargetType: " dm ", TargetID: " dm-1 ", ViewerID: " user-1 ",
	})
	if err != nil {
		t.Fatalf("ListPins: %v", err)
	}
	if store.lastTargetType != "dm" || store.lastTargetID != "dm-1" || store.lastActor != "user-1" {
		t.Fatalf("expected trimmed DM list delegation, got type=%q target=%q viewer=%q", store.lastTargetType, store.lastTargetID, store.lastActor)
	}
}

func TestPinService_ListPins_InvalidInput(t *testing.T) {
	svc, store := pinFixture(t)
	for _, in := range []service.ListPinsInput{
		{TargetType: "channel", TargetID: "ch-1", ViewerID: "user-1"},
		{WorkspaceID: "ws-1", TargetType: "thread", TargetID: "ch-1", ViewerID: "user-1"},
	} {
		if _, err := svc.ListPins(context.Background(), in); !errors.Is(err, domain.ErrInvalidInput) {
			t.Fatalf("input %+v: expected ErrInvalidInput, got %v", in, err)
		}
	}
	if store.lastTargetType != "" {
		t.Fatal("invalid list input must not call store")
	}
}
