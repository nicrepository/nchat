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
	leaveInput      storage.LeaveResourceCallInput
	currentCallID   string
	joinInput       storage.JoinResourceCallInput
	syncWorkspaceID string
	syncActorID     string
	syncTargetType  domain.CallTargetType
	syncTargetID    string
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

func (s *adapterCallStore) LeaveResourceCall(_ context.Context, input storage.LeaveResourceCallInput) (storage.TransitionCallResult, error) {
	s.leaveInput = input
	return storage.TransitionCallResult{}, domain.ErrConflict
}

func (s *adapterCallStore) CurrentCallForUser(_ context.Context, _, _, callID string) (domain.Call, error) {
	s.currentCallID = callID
	return domain.Call{}, domain.ErrConflict
}

func (s *adapterCallStore) ExpireDueCalls(context.Context, int) ([]domain.Call, error) {
	return nil, domain.ErrConflict
}

func (s *adapterCallStore) JoinResourceCall(_ context.Context, input storage.JoinResourceCallInput) (domain.Call, error) {
	s.joinInput = input
	return domain.Call{}, domain.ErrConflict
}

func (s *adapterCallStore) ActiveResourceCall(_ context.Context, workspaceID, actorID string, targetType domain.CallTargetType, targetID string) (storage.ActiveResourceCallResult, error) {
	s.syncWorkspaceID, s.syncActorID, s.syncTargetType, s.syncTargetID = workspaceID, actorID, targetType, targetID
	return storage.ActiveResourceCallResult{}, domain.ErrConflict
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

	_, err = adapter.LeaveCall(
		context.Background(), adapterWorkspace, adapterActor, adapterCallID,
	)
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("LeaveCall error = %v", err)
	}
	if store.leaveInput.WorkspaceID != adapterWorkspace ||
		store.leaveInput.ActorID != adapterActor ||
		store.leaveInput.CallID != adapterCallID {
		t.Fatalf("leave input = %+v", store.leaveInput)
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

	_, err = adapter.JoinCall(
		context.Background(), adapterWorkspace, adapterActor, adapterCallID, ws.TargetTypeChannel, adapterTarget,
	)
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("JoinCall error = %v", err)
	}
	if store.joinInput.WorkspaceID != adapterWorkspace || store.joinInput.ActorID != adapterActor ||
		store.joinInput.CallID != adapterCallID || store.joinInput.TargetType != domain.CallTargetChannel ||
		store.joinInput.TargetID != adapterTarget {
		t.Fatalf("join input = %+v", store.joinInput)
	}

	_, _, _, err = adapter.ResourceSync(
		context.Background(), adapterWorkspace, adapterActor, ws.TargetTypeChannel, adapterTarget,
	)
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("ResourceSync error = %v", err)
	}
	if store.syncWorkspaceID != adapterWorkspace || store.syncActorID != adapterActor ||
		store.syncTargetType != domain.CallTargetChannel || store.syncTargetID != adapterTarget {
		t.Fatalf("sync input = workspace=%q actor=%q targetType=%q targetID=%q",
			store.syncWorkspaceID, store.syncActorID, store.syncTargetType, store.syncTargetID)
	}
}
