package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

const (
	serviceCallWorkspace = "00000000-0000-4000-8000-000000000011"
	serviceCallRequest   = "00000000-0000-4000-8000-000000000012"
	serviceCallID        = "00000000-0000-4000-8000-000000000013"
	serviceCallCaller    = "00000000-0000-4000-8000-000000000014"
	serviceCallCallee    = "00000000-0000-4000-8000-000000000015"
	serviceCallOutsider  = "00000000-0000-4000-8000-000000000016"
)

type fakeCallStore struct {
	mu               sync.Mutex
	call             domain.Call
	createInput      storage.CreateCallInput
	resourceInput    storage.CreateResourceCallInput
	presenceInput    storage.RenewCallPresenceInput
	transitionInputs []storage.TransitionCallInput
	createCreated    bool
	createErr        error
	transitionResult storage.TransitionCallResult
	transitionErr    error
	currentErr       error
	currentCallID    string
	expired          []domain.Call
	expireErr        error
}

func (f *fakeCallStore) CreateCall(_ context.Context, input storage.CreateCallInput) (domain.Call, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.createInput = input
	return f.call, f.createCreated, f.createErr
}

func (f *fakeCallStore) CreateResourceCall(_ context.Context, input storage.CreateResourceCallInput) (domain.Call, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resourceInput = input
	return f.call, f.createCreated, f.createErr
}

func (f *fakeCallStore) RenewCallPresence(_ context.Context, input storage.RenewCallPresenceInput) error {
	f.presenceInput = input
	return f.createErr
}

func (f *fakeCallStore) TransitionCall(_ context.Context, input storage.TransitionCallInput) (storage.TransitionCallResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.transitionInputs = append(f.transitionInputs, input)
	return f.transitionResult, f.transitionErr
}

func (f *fakeCallStore) CurrentCallForUser(_ context.Context, _, _, callID string) (domain.Call, error) {
	f.currentCallID = callID
	return f.call, f.currentErr
}

func (f *fakeCallStore) ExpireDueCalls(context.Context, int) ([]domain.Call, error) {
	return f.expired, f.expireErr
}

type fakeCallPublisher struct {
	mu    sync.Mutex
	calls []domain.Call
}

func (p *fakeCallPublisher) PublishCall(_ context.Context, call domain.Call) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, call)
}

func serviceCall(status domain.CallStatus, version int64) domain.Call {
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	return domain.Call{
		ID: serviceCallID, WorkspaceID: serviceCallWorkspace, RequestID: serviceCallRequest,
		CallerID: serviceCallCaller, CalleeID: serviceCallCallee,
		Type: domain.CallTypeVideo, Status: status, Version: version,
		CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(30 * time.Second),
	}
}

func TestCallServiceStartValidatesAndPublishesCreatedCall(t *testing.T) {
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	store := &fakeCallStore{call: serviceCall(domain.CallStatusRinging, 1), createCreated: true}
	publisher := &fakeCallPublisher{}
	svc := NewCallService(store, 30*time.Second, func() time.Time { return now }, publisher)

	call, err := svc.Start(context.Background(), StartCallInput{
		WorkspaceID: serviceCallWorkspace,
		RequestID:   serviceCallRequest,
		CallerID:    serviceCallCaller,
		CalleeID:    serviceCallCallee,
		Type:        domain.CallTypeVideo,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if call.Status != domain.CallStatusRinging || len(publisher.calls) != 1 {
		t.Fatalf("unexpected start: call=%+v published=%+v", call, publisher.calls)
	}
	if !store.createInput.ExpiresAt.Equal(now.Add(30 * time.Second)) {
		t.Fatalf("expires_at = %s", store.createInput.ExpiresAt)
	}
	if store.createInput.CallerID != serviceCallCaller || store.createInput.CalleeID != serviceCallCallee {
		t.Fatalf("store identities were not server inputs: %+v", store.createInput)
	}
}

func TestCallServiceCurrentCanonicalizesOptionalCallID(t *testing.T) {
	store := &fakeCallStore{call: serviceCall(domain.CallStatusActive, 1)}
	svc := NewCallService(store, 30*time.Second, time.Now, &fakeCallPublisher{})

	if _, err := svc.Current(context.Background(), serviceCallWorkspace, serviceCallCaller, serviceCallID); err != nil {
		t.Fatalf("Current: %v", err)
	}
	if store.currentCallID != serviceCallID {
		t.Fatalf("call id = %q", store.currentCallID)
	}
	if _, err := svc.Current(context.Background(), serviceCallWorkspace, serviceCallCaller, "not-a-uuid"); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("invalid call id error = %v", err)
	}
}

func TestCallServiceStartRejectsInvalidInputBeforeStorage(t *testing.T) {
	tests := []struct {
		name  string
		input StartCallInput
	}{
		{name: "invalid workspace", input: StartCallInput{WorkspaceID: "bad", RequestID: serviceCallRequest, CallerID: serviceCallCaller, CalleeID: serviceCallCallee, Type: domain.CallTypeAudio}},
		{name: "invalid request", input: StartCallInput{WorkspaceID: serviceCallWorkspace, RequestID: "bad", CallerID: serviceCallCaller, CalleeID: serviceCallCallee, Type: domain.CallTypeAudio}},
		{name: "invalid caller", input: StartCallInput{WorkspaceID: serviceCallWorkspace, RequestID: serviceCallRequest, CallerID: "bad", CalleeID: serviceCallCallee, Type: domain.CallTypeAudio}},
		{name: "invalid callee", input: StartCallInput{WorkspaceID: serviceCallWorkspace, RequestID: serviceCallRequest, CallerID: serviceCallCaller, CalleeID: "bad", Type: domain.CallTypeAudio}},
		{name: "self", input: StartCallInput{WorkspaceID: serviceCallWorkspace, RequestID: serviceCallRequest, CallerID: serviceCallCaller, CalleeID: serviceCallCaller, Type: domain.CallTypeAudio}},
		{name: "invalid type", input: StartCallInput{WorkspaceID: serviceCallWorkspace, RequestID: serviceCallRequest, CallerID: serviceCallCaller, CalleeID: serviceCallCallee, Type: "screen"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &fakeCallStore{}
			_, err := NewCallService(store, 30*time.Second, nil, nil).Start(context.Background(), test.input)
			if !errors.Is(err, domain.ErrInvalidInput) {
				t.Fatalf("error = %v, want invalid input", err)
			}
			if store.createInput.WorkspaceID != "" {
				t.Fatal("storage called for invalid input")
			}
		})
	}
}

func TestCallServiceStartsAuthorizedResourceActiveAndPublishes(t *testing.T) {
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	resource := serviceCall(domain.CallStatusActive, 1)
	resource.CalleeID = ""
	resource.TargetType = domain.CallTargetChannel
	resource.TargetID = serviceCallCallee
	store := &fakeCallStore{call: resource, createCreated: true}
	publisher := &fakeCallPublisher{}
	svc := NewCallService(store, 30*time.Second, func() time.Time { return now }, publisher)

	call, err := svc.Start(context.Background(), StartCallInput{
		WorkspaceID: serviceCallWorkspace,
		RequestID:   serviceCallRequest,
		CallerID:    serviceCallCaller,
		TargetType:  domain.CallTargetChannel,
		TargetID:    serviceCallCallee,
		Type:        domain.CallTypeVideo,
	})
	if err != nil {
		t.Fatalf("Start resource: %v", err)
	}
	if call.Status != domain.CallStatusActive || len(publisher.calls) != 1 {
		t.Fatalf("resource call=%+v published=%d", call, len(publisher.calls))
	}
	if store.resourceInput.CallerID != serviceCallCaller ||
		store.resourceInput.TargetType != domain.CallTargetChannel ||
		store.resourceInput.TargetID != serviceCallCallee {
		t.Fatalf("resource input not canonical: %+v", store.resourceInput)
	}
}

func TestCallServiceRenewsOnlyAuthenticatedResourcePresence(t *testing.T) {
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	store := &fakeCallStore{}
	svc := NewCallService(store, 30*time.Second, func() time.Time { return now }, nil)

	if err := svc.Presence(context.Background(), serviceCallWorkspace, serviceCallCaller, serviceCallID); err != nil {
		t.Fatalf("Presence: %v", err)
	}
	if store.presenceInput.ActorID != serviceCallCaller || store.presenceInput.CallID != serviceCallID ||
		!store.presenceInput.ExpiresAt.Equal(now.Add(30*time.Second)) {
		t.Fatalf("presence input=%+v", store.presenceInput)
	}
}

func TestCallServiceTransitionsUseAuthenticatedActorAndPublishOnlyChanges(t *testing.T) {
	tests := []struct {
		name   string
		run    func(*CallService) (domain.Call, error)
		action storage.CallAction
		status domain.CallStatus
	}{
		{name: "accept", run: func(s *CallService) (domain.Call, error) {
			return s.Accept(context.Background(), serviceCallWorkspace, serviceCallCallee, serviceCallID)
		}, action: storage.CallActionAccept, status: domain.CallStatusActive},
		{name: "decline", run: func(s *CallService) (domain.Call, error) {
			return s.Decline(context.Background(), serviceCallWorkspace, serviceCallCallee, serviceCallID)
		}, action: storage.CallActionDecline, status: domain.CallStatusDeclined},
		{name: "cancel", run: func(s *CallService) (domain.Call, error) {
			return s.Cancel(context.Background(), serviceCallWorkspace, serviceCallCaller, serviceCallID)
		}, action: storage.CallActionCancel, status: domain.CallStatusCancelled},
		{name: "end", run: func(s *CallService) (domain.Call, error) {
			return s.End(context.Background(), serviceCallWorkspace, serviceCallCaller, serviceCallID)
		}, action: storage.CallActionEnd, status: domain.CallStatusEnded},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			call := serviceCall(test.status, 2)
			store := &fakeCallStore{transitionResult: storage.TransitionCallResult{Call: call, Changed: true}}
			publisher := &fakeCallPublisher{}
			got, err := test.run(NewCallService(store, 30*time.Second, nil, publisher))
			if err != nil {
				t.Fatalf("transition: %v", err)
			}
			if got.Status != test.status || len(publisher.calls) != 1 {
				t.Fatalf("unexpected result: call=%+v published=%+v", got, publisher.calls)
			}
			if len(store.transitionInputs) != 1 || store.transitionInputs[0].Action != test.action {
				t.Fatalf("unexpected store input: %+v", store.transitionInputs)
			}
		})
	}

	t.Run("duplicate", func(t *testing.T) {
		call := serviceCall(domain.CallStatusActive, 2)
		store := &fakeCallStore{transitionResult: storage.TransitionCallResult{Call: call}}
		publisher := &fakeCallPublisher{}
		if _, err := NewCallService(store, 30*time.Second, nil, publisher).
			Accept(context.Background(), serviceCallWorkspace, serviceCallCallee, serviceCallID); err != nil {
			t.Fatalf("duplicate accept: %v", err)
		}
		if len(publisher.calls) != 0 {
			t.Fatal("duplicate transition published another event")
		}
	})
}

func TestCallServicePublishesTimeoutMaterializedByLateCommand(t *testing.T) {
	timedOut := serviceCall(domain.CallStatusTimedOut, 2)
	store := &fakeCallStore{
		transitionResult: storage.TransitionCallResult{Call: timedOut, Changed: true, TimedOut: true},
		transitionErr:    domain.ErrConflict,
	}
	publisher := &fakeCallPublisher{}
	_, err := NewCallService(store, 30*time.Second, nil, publisher).
		Accept(context.Background(), serviceCallWorkspace, serviceCallCallee, serviceCallID)
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("error = %v, want conflict", err)
	}
	if len(publisher.calls) != 1 || publisher.calls[0].Status != domain.CallStatusTimedOut {
		t.Fatalf("timeout not published: %+v", publisher.calls)
	}
}

func TestCallServiceExpireDuePublishesEachWinnerOnce(t *testing.T) {
	first := serviceCall(domain.CallStatusTimedOut, 2)
	second := first
	second.ID = "00000000-0000-4000-8000-000000000017"
	store := &fakeCallStore{expired: []domain.Call{first, second}}
	publisher := &fakeCallPublisher{}
	svc := NewCallService(store, 30*time.Second, nil, publisher)

	if err := svc.ExpireDue(context.Background()); err != nil {
		t.Fatalf("ExpireDue: %v", err)
	}
	if len(publisher.calls) != 2 {
		t.Fatalf("published calls = %d, want 2", len(publisher.calls))
	}
}

func TestCallServiceCoversResourceDirectAndLifecycleErrorPaths(t *testing.T) {
	sentinel := errors.New("call store failure")

	t.Run("resource requires supported target type", func(t *testing.T) {
		store := &fakeCallStore{}
		svc := NewCallService(store, 30*time.Second, nil, nil)

		_, err := svc.Start(context.Background(), StartCallInput{
			WorkspaceID: serviceCallWorkspace,
			RequestID:   serviceCallRequest,
			CallerID:    serviceCallCaller,
			TargetType:  domain.CallTargetUser,
			TargetID:    serviceCallCallee,
			Type:        domain.CallTypeAudio,
		})
		if !errors.Is(err, domain.ErrInvalidInput) {
			t.Fatalf("error = %v, want invalid input", err)
		}
	})

	t.Run("resource requires canonical target id", func(t *testing.T) {
		store := &fakeCallStore{}
		svc := NewCallService(store, 30*time.Second, nil, nil)

		_, err := svc.Start(context.Background(), StartCallInput{
			WorkspaceID: serviceCallWorkspace,
			RequestID:   serviceCallRequest,
			CallerID:    serviceCallCaller,
			TargetType:  domain.CallTargetChannel,
			TargetID:    "not-a-uuid",
			Type:        domain.CallTypeAudio,
		})
		if !errors.Is(err, domain.ErrInvalidInput) {
			t.Fatalf("error = %v, want invalid input", err)
		}
	})

	t.Run("resource propagates storage failure", func(t *testing.T) {
		store := &fakeCallStore{createErr: sentinel}
		svc := NewCallService(store, 30*time.Second, nil, nil)

		_, err := svc.Start(context.Background(), StartCallInput{
			WorkspaceID: serviceCallWorkspace,
			RequestID:   serviceCallRequest,
			CallerID:    serviceCallCaller,
			TargetType:  domain.CallTargetChannel,
			TargetID:    serviceCallCallee,
			Type:        domain.CallTypeAudio,
		})
		if !errors.Is(err, sentinel) {
			t.Fatalf("error = %v, want storage failure", err)
		}
	})

	t.Run("direct call rejects resource metadata", func(t *testing.T) {
		store := &fakeCallStore{}
		svc := NewCallService(store, 30*time.Second, nil, nil)

		_, err := svc.Start(context.Background(), StartCallInput{
			WorkspaceID: serviceCallWorkspace,
			RequestID:   serviceCallRequest,
			CallerID:    serviceCallCaller,
			CalleeID:    serviceCallCallee,
			TargetType:  domain.CallTargetChannel,
			TargetID:    serviceCallCallee,
			Type:        domain.CallTypeAudio,
		})
		if !errors.Is(err, domain.ErrInvalidInput) {
			t.Fatalf("error = %v, want invalid input", err)
		}
	})

	t.Run("direct call propagates storage failure", func(t *testing.T) {
		store := &fakeCallStore{createErr: sentinel}
		svc := NewCallService(store, 30*time.Second, nil, nil)

		_, err := svc.Start(context.Background(), StartCallInput{
			WorkspaceID: serviceCallWorkspace,
			RequestID:   serviceCallRequest,
			CallerID:    serviceCallCaller,
			CalleeID:    serviceCallCallee,
			Type:        domain.CallTypeAudio,
		})
		if !errors.Is(err, sentinel) {
			t.Fatalf("error = %v, want storage failure", err)
		}
	})

	t.Run("presence validates every identity", func(t *testing.T) {
		store := &fakeCallStore{}
		svc := NewCallService(store, 30*time.Second, nil, nil)

		tests := []struct {
			name        string
			workspaceID string
			actorID     string
			callID      string
		}{
			{"workspace", "bad", serviceCallCaller, serviceCallID},
			{"actor", serviceCallWorkspace, "bad", serviceCallID},
			{"call", serviceCallWorkspace, serviceCallCaller, "bad"},
		}
		for _, test := range tests {
			t.Run(test.name, func(t *testing.T) {
				err := svc.Presence(
					context.Background(),
					test.workspaceID,
					test.actorID,
					test.callID,
				)
				if !errors.Is(err, domain.ErrInvalidInput) {
					t.Fatalf("error = %v, want invalid input", err)
				}
			})
		}
	})

	t.Run("current validates workspace and actor", func(t *testing.T) {
		store := &fakeCallStore{}
		svc := NewCallService(store, 30*time.Second, nil, nil)

		if _, err := svc.Current(
			context.Background(), "bad", serviceCallCaller, "",
		); !errors.Is(err, domain.ErrInvalidInput) {
			t.Fatalf("invalid workspace error = %v", err)
		}

		if _, err := svc.Current(
			context.Background(), serviceCallWorkspace, "bad", "",
		); !errors.Is(err, domain.ErrInvalidInput) {
			t.Fatalf("invalid actor error = %v", err)
		}
	})

	t.Run("expiry rejects unavailable store", func(t *testing.T) {
		svc := NewCallService(nil, 30*time.Second, nil, nil)
		if err := svc.ExpireDue(context.Background()); !errors.Is(err, domain.ErrInvalidInput) {
			t.Fatalf("error = %v, want invalid input", err)
		}
	})

	t.Run("expiry propagates storage failure", func(t *testing.T) {
		store := &fakeCallStore{expireErr: sentinel}
		svc := NewCallService(store, 30*time.Second, nil, nil)

		if err := svc.ExpireDue(context.Background()); !errors.Is(err, sentinel) {
			t.Fatalf("error = %v, want storage failure", err)
		}
	})

	t.Run("transition validates every identity", func(t *testing.T) {
		store := &fakeCallStore{}
		svc := NewCallService(store, 30*time.Second, nil, nil)

		tests := []struct {
			name        string
			workspaceID string
			actorID     string
			callID      string
		}{
			{"workspace", "bad", serviceCallCaller, serviceCallID},
			{"actor", serviceCallWorkspace, "bad", serviceCallID},
			{"call", serviceCallWorkspace, serviceCallCaller, "bad"},
		}
		for _, test := range tests {
			t.Run(test.name, func(t *testing.T) {
				_, err := svc.Accept(
					context.Background(),
					test.workspaceID,
					test.actorID,
					test.callID,
				)
				if !errors.Is(err, domain.ErrInvalidInput) {
					t.Fatalf("error = %v, want invalid input", err)
				}
			})
		}
	})
}
