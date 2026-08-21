package service

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

type callStore interface {
	CreateCall(context.Context, storage.CreateCallInput) (domain.Call, bool, error)
	CreateResourceCall(context.Context, storage.CreateResourceCallInput) (domain.Call, bool, error)
	RenewCallPresence(context.Context, storage.RenewCallPresenceInput) error
	TransitionCall(context.Context, storage.TransitionCallInput) (storage.TransitionCallResult, error)
	LeaveResourceCall(context.Context, storage.LeaveResourceCallInput) (storage.TransitionCallResult, error)
	CurrentCallForUser(context.Context, string, string, string) (domain.Call, error)
	ExpireDueCalls(context.Context, int) ([]domain.Call, error)
	// JoinResourceCall admits an actor into an already-existing, active
	// resource call (issue #622) — see storage.PGXCallStore.JoinResourceCall.
	JoinResourceCall(context.Context, storage.JoinResourceCallInput) (domain.Call, error)
	// ActiveResourceCall answers call.resource.sync (issue #622) — see
	// storage.PGXCallStore.ActiveResourceCall.
	ActiveResourceCall(ctx context.Context, workspaceID, actorID string, targetType domain.CallTargetType, targetID string) (storage.ActiveResourceCallResult, error)
}

type CallEventPublisher interface {
	PublishCall(context.Context, domain.Call)
}

type CallService struct {
	store     callStore
	timeout   time.Duration
	now       func() time.Time
	publisher CallEventPublisher
}

type StartCallInput struct {
	WorkspaceID string
	RequestID   string
	CallerID    string
	CalleeID    string
	TargetType  domain.CallTargetType
	TargetID    string
	Type        domain.CallType
}

func NewCallService(store callStore, timeout time.Duration, now func() time.Time, publisher CallEventPublisher) *CallService {
	if now == nil {
		now = time.Now
	}
	return &CallService{store: store, timeout: timeout, now: now, publisher: publisher}
}

func (s *CallService) Start(ctx context.Context, input StartCallInput) (domain.Call, error) {
	workspaceID, err := canonicalUUID(input.WorkspaceID)
	if err != nil {
		return domain.Call{}, domain.ErrInvalidInput
	}
	requestID, err := canonicalUUID(input.RequestID)
	if err != nil {
		return domain.Call{}, domain.ErrInvalidInput
	}
	callerID, err := canonicalUUID(input.CallerID)
	if err != nil {
		return domain.Call{}, domain.ErrInvalidInput
	}
	if !input.Type.Valid() || s == nil || s.store == nil || s.timeout <= 0 {
		return domain.Call{}, domain.ErrInvalidInput
	}
	if input.CalleeID == "" {
		if input.TargetType != domain.CallTargetChannel && input.TargetType != domain.CallTargetDM {
			return domain.Call{}, domain.ErrInvalidInput
		}
		targetID, targetErr := canonicalUUID(input.TargetID)
		if targetErr != nil {
			return domain.Call{}, domain.ErrInvalidInput
		}
		call, created, createErr := s.store.CreateResourceCall(ctx, storage.CreateResourceCallInput{
			WorkspaceID: workspaceID,
			RequestID:   requestID,
			CallerID:    callerID,
			TargetType:  input.TargetType,
			TargetID:    targetID,
			Type:        input.Type,
			ExpiresAt:   s.now().UTC().Add(s.timeout),
		})
		if createErr != nil {
			return domain.Call{}, createErr
		}
		// Re-publishing an existing resource call lets a late starter reconcile
		// without manufacturing a second lifecycle row; clients discard its same version.
		if created || call.Status == domain.CallStatusActive {
			s.publish(ctx, call)
		}
		return call, nil
	}
	if input.TargetType != "" || input.TargetID != "" {
		return domain.Call{}, domain.ErrInvalidInput
	}
	calleeID, err := canonicalUUID(input.CalleeID)
	if err != nil || callerID == calleeID {
		return domain.Call{}, domain.ErrInvalidInput
	}

	call, created, err := s.store.CreateCall(ctx, storage.CreateCallInput{
		WorkspaceID: workspaceID,
		RequestID:   requestID,
		CallerID:    callerID,
		CalleeID:    calleeID,
		Type:        input.Type,
		ExpiresAt:   s.now().UTC().Add(s.timeout),
	})
	if err != nil {
		return domain.Call{}, err
	}
	if created {
		s.publish(ctx, call)
	}
	return call, nil
}

func (s *CallService) Presence(ctx context.Context, workspaceID, actorID, callID string) error {
	workspaceID, err := canonicalUUID(workspaceID)
	if err != nil {
		return domain.ErrInvalidInput
	}
	actorID, err = canonicalUUID(actorID)
	if err != nil {
		return domain.ErrInvalidInput
	}
	callID, err = canonicalUUID(callID)
	if err != nil || s == nil || s.store == nil || s.timeout <= 0 {
		return domain.ErrInvalidInput
	}
	return s.store.RenewCallPresence(ctx, storage.RenewCallPresenceInput{
		WorkspaceID: workspaceID,
		CallID:      callID,
		ActorID:     actorID,
		ExpiresAt:   s.now().UTC().Add(s.timeout),
	})
}

func (s *CallService) Accept(ctx context.Context, workspaceID, actorID, callID string) (domain.Call, error) {
	return s.transition(ctx, workspaceID, actorID, callID, storage.CallActionAccept)
}

func (s *CallService) Decline(ctx context.Context, workspaceID, actorID, callID string) (domain.Call, error) {
	return s.transition(ctx, workspaceID, actorID, callID, storage.CallActionDecline)
}

func (s *CallService) Cancel(ctx context.Context, workspaceID, actorID, callID string) (domain.Call, error) {
	return s.transition(ctx, workspaceID, actorID, callID, storage.CallActionCancel)
}

func (s *CallService) End(ctx context.Context, workspaceID, actorID, callID string) (domain.Call, error) {
	return s.transition(ctx, workspaceID, actorID, callID, storage.CallActionEnd)
}

// Leave releases actorID's own participation in a resource call — never the
// resource call's original caller-only call.end, and never RF-23's direct-call
// lifecycle (issue #569). The call itself only changes (and is only
// published) when actorID was its last active participant.
func (s *CallService) Leave(ctx context.Context, workspaceID, actorID, callID string) (domain.Call, error) {
	workspaceID, err := canonicalUUID(workspaceID)
	if err != nil {
		return domain.Call{}, domain.ErrInvalidInput
	}
	actorID, err = canonicalUUID(actorID)
	if err != nil {
		return domain.Call{}, domain.ErrInvalidInput
	}
	callID, err = canonicalUUID(callID)
	if err != nil || s == nil || s.store == nil {
		return domain.Call{}, domain.ErrInvalidInput
	}
	result, err := s.store.LeaveResourceCall(ctx, storage.LeaveResourceCallInput{
		WorkspaceID: workspaceID,
		CallID:      callID,
		ActorID:     actorID,
	})
	if result.Changed {
		s.publish(ctx, result.Call)
	}
	return result.Call, err
}

// JoinCallInput identifies an explicit call.join (issue #622): the actor's
// claim of which call and which target it believes that call belongs to.
type JoinCallInput struct {
	WorkspaceID string
	ActorID     string
	CallID      string
	TargetType  domain.CallTargetType
	TargetID    string
}

// Join admits ActorID into an already-known, active resource call — issue
// #622's call.join. It never creates a call and never publishes: joining
// changes nothing observable for any other participant (only the lease
// table), so there is nothing for PublishCall to broadcast — unlike Start,
// Leave and transition, which all mutate the call's own lifecycle row.
func (s *CallService) Join(ctx context.Context, input JoinCallInput) (domain.Call, error) {
	workspaceID, err := canonicalUUID(input.WorkspaceID)
	if err != nil {
		return domain.Call{}, domain.ErrInvalidInput
	}
	actorID, err := canonicalUUID(input.ActorID)
	if err != nil {
		return domain.Call{}, domain.ErrInvalidInput
	}
	callID, err := canonicalUUID(input.CallID)
	if err != nil {
		return domain.Call{}, domain.ErrInvalidInput
	}
	targetID, err := canonicalUUID(input.TargetID)
	if err != nil || s == nil || s.store == nil || s.timeout <= 0 ||
		(input.TargetType != domain.CallTargetChannel && input.TargetType != domain.CallTargetDM) {
		return domain.Call{}, domain.ErrInvalidInput
	}
	return s.store.JoinResourceCall(ctx, storage.JoinResourceCallInput{
		WorkspaceID: workspaceID,
		CallID:      callID,
		ActorID:     actorID,
		TargetType:  input.TargetType,
		TargetID:    targetID,
		ExpiresAt:   s.now().UTC().Add(s.timeout),
	})
}

// ResourceSync answers call.resource.sync (issue #622): the authoritative
// active call of one channel/group-DM target, or found=false — for both "no
// active call" and "not authorized for this target", deliberately
// indistinguishable here (see storage.ActiveResourceCallResult). Creates no
// lease, issues no token, mutates nothing.
func (s *CallService) ResourceSync(ctx context.Context, workspaceID, actorID string, targetType domain.CallTargetType, targetID string) (domain.Call, bool, time.Time, error) {
	workspaceID, err := canonicalUUID(workspaceID)
	if err != nil {
		return domain.Call{}, false, time.Time{}, domain.ErrInvalidInput
	}
	actorID, err = canonicalUUID(actorID)
	if err != nil {
		return domain.Call{}, false, time.Time{}, domain.ErrInvalidInput
	}
	targetID, err = canonicalUUID(targetID)
	if err != nil || s == nil || s.store == nil ||
		(targetType != domain.CallTargetChannel && targetType != domain.CallTargetDM) {
		return domain.Call{}, false, time.Time{}, domain.ErrInvalidInput
	}
	result, err := s.store.ActiveResourceCall(ctx, workspaceID, actorID, targetType, targetID)
	if err != nil {
		return domain.Call{}, false, time.Time{}, err
	}
	return result.Call, result.Found, result.ObservedAt, nil
}

func (s *CallService) Current(ctx context.Context, workspaceID, actorID, callID string) (domain.Call, error) {
	workspaceID, err := canonicalUUID(workspaceID)
	if err != nil {
		return domain.Call{}, domain.ErrInvalidInput
	}
	actorID, err = canonicalUUID(actorID)
	if err != nil || s == nil || s.store == nil {
		return domain.Call{}, domain.ErrInvalidInput
	}
	if callID != "" {
		callID, err = canonicalUUID(callID)
		if err != nil {
			return domain.Call{}, domain.ErrInvalidInput
		}
	}
	return s.store.CurrentCallForUser(ctx, workspaceID, actorID, callID)
}

func (s *CallService) ExpireDue(ctx context.Context) error {
	if s == nil || s.store == nil {
		return domain.ErrInvalidInput
	}
	calls, err := s.store.ExpireDueCalls(ctx, 100)
	if err != nil {
		return err
	}
	for _, call := range calls {
		s.publish(ctx, call)
	}
	return nil
}

func (s *CallService) transition(ctx context.Context, workspaceID, actorID, callID string, action storage.CallAction) (domain.Call, error) {
	workspaceID, err := canonicalUUID(workspaceID)
	if err != nil {
		return domain.Call{}, domain.ErrInvalidInput
	}
	actorID, err = canonicalUUID(actorID)
	if err != nil {
		return domain.Call{}, domain.ErrInvalidInput
	}
	callID, err = canonicalUUID(callID)
	if err != nil || s == nil || s.store == nil {
		return domain.Call{}, domain.ErrInvalidInput
	}
	result, transitionErr := s.store.TransitionCall(ctx, storage.TransitionCallInput{
		WorkspaceID: workspaceID,
		CallID:      callID,
		ActorID:     actorID,
		Action:      action,
	})
	if result.Changed {
		s.publish(ctx, result.Call)
	}
	return result.Call, transitionErr
}

func (s *CallService) publish(ctx context.Context, call domain.Call) {
	if s.publisher != nil {
		s.publisher.PublishCall(ctx, call)
	}
}

func canonicalUUID(value string) (string, error) {
	parsed, err := uuid.Parse(value)
	if err != nil {
		return "", err
	}
	return parsed.String(), nil
}
