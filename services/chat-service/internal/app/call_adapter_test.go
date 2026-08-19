package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/service"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
	"github.com/nicrepository/nchat/services/chat-service/internal/ws"
)

const (
	adapterWorkspace = "00000000-0000-4000-8000-000000000021"
	adapterRequest   = "00000000-0000-4000-8000-000000000022"
	adapterCallID    = "00000000-0000-4000-8000-000000000023"
	adapterActor     = "00000000-0000-4000-8000-000000000024"
	adapterTarget    = "00000000-0000-4000-8000-000000000025"
)

type adapterCallStore struct {
	resourceInput   storage.CreateResourceCallInput
	presenceInput   storage.RenewCallPresenceInput
	transitionInput storage.TransitionCallInput
	currentCallID   string
}

func (s *adapterCallStore) CreateCall(context.Context, storage.CreateCallInput) (domain.Call, bool, error) {
	return domain.Call{}, false, domain.ErrConflict
}

func (s *adapterCallStore) CreateResourceCall(_ context.Context, input storage.CreateResourceCallInput) (domain.Call, bool, error) {
	s.resourceInput = input
	return domain.Call{}, false, domain.ErrConflict
}

func (s *adapterCallStore) RenewCallPresence(_ context.Context, input storage.RenewCallPresenceInput) error {
	s.presenceInput = input
	return domain.ErrConflict
}

func (s *adapterCallStore) TransitionCall(_ context.Context, input storage.TransitionCallInput) (storage.TransitionCallResult, error) {
	s.transitionInput = input
	return storage.TransitionCallResult{}, domain.ErrConflict
}

func (s *adapterCallStore) CurrentCallForUser(_ context.Context, _, _, callID string) (domain.Call, error) {
	s.currentCallID = callID
	return domain.Call{}, domain.ErrConflict
}

func (s *adapterCallStore) ExpireDueCalls(context.Context, int) ([]domain.Call, error) {
	return nil, domain.ErrConflict
}

func TestCallHandlerAdapterDelegatesResourceLifecycle(t *testing.T) {
	store := &adapterCallStore{}
	svc := service.NewCallService(store, 30*time.Second, nil, nil)
	adapter := &callHandlerAdapter{service: svc}

	_, err := adapter.StartCall(context.Background(), ws.StartCallCommand{
		WorkspaceID: adapterWorkspace,
		RequestID:   adapterRequest,
		CallerID:    adapterActor,
		TargetType:  ws.TargetTypeChannel,
		TargetID:    adapterTarget,
		Type:        domain.CallTypeVideo,
	})
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("StartCall error = %v", err)
	}
	if store.resourceInput.CallerID != adapterActor ||
		store.resourceInput.TargetType != domain.CallTargetChannel ||
		store.resourceInput.TargetID != adapterTarget {
		t.Fatalf("resource input = %+v", store.resourceInput)
	}

	err = adapter.RenewCallPresence(
		context.Background(), adapterWorkspace, adapterActor, adapterCallID,
	)
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("RenewCallPresence error = %v", err)
	}
	if store.presenceInput.ActorID != adapterActor ||
		store.presenceInput.CallID != adapterCallID {
		t.Fatalf("presence input = %+v", store.presenceInput)
	}

	actions := []struct {
		message ws.ClientMessageType
		want    storage.CallAction
	}{
		{ws.ClientMessageTypeCallAccept, storage.CallActionAccept},
		{ws.ClientMessageTypeCallDecline, storage.CallActionDecline},
		{ws.ClientMessageTypeCallCancel, storage.CallActionCancel},
		{ws.ClientMessageTypeCallEnd, storage.CallActionEnd},
	}
	for _, action := range actions {
		_, err = adapter.TransitionCall(
			context.Background(),
			adapterWorkspace,
			adapterActor,
			adapterCallID,
			action.message,
		)
		if !errors.Is(err, domain.ErrConflict) {
			t.Fatalf("%s error = %v", action.message, err)
		}
		if store.transitionInput.Action != action.want {
			t.Fatalf("%s action = %q, want %q",
				action.message, store.transitionInput.Action, action.want)
		}
	}

	_, err = adapter.TransitionCall(
		context.Background(),
		adapterWorkspace,
		adapterActor,
		adapterCallID,
		ws.ClientMessageType("invalid-call-action"),
	)
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("invalid transition error = %v", err)
	}

	_, err = adapter.CurrentCall(
		context.Background(), adapterWorkspace, adapterActor, adapterCallID,
	)
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("CurrentCall error = %v", err)
	}
	if store.currentCallID != adapterCallID {
		t.Fatalf("current call id = %q", store.currentCallID)
	}
}
