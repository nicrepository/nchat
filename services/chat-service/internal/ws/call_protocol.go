package ws

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
)

const (
	defaultCallStartLimit  = 10
	defaultCallStartWindow = 60
)

var (
	ErrCallFeatureDisabled = errors.New("call feature disabled")
	ErrCallRateLimited     = errors.New("call rate limited")
)

type StartCallCommand struct {
	WorkspaceID string
	RequestID   string
	CallerID    string
	CalleeID    string
	TargetType  TargetType
	TargetID    string
	Type        domain.CallType
}

type CallHandler interface {
	StartCall(context.Context, StartCallCommand) (domain.Call, error)
	TransitionCall(context.Context, string, string, string, ClientMessageType) (domain.Call, error)
	CurrentCall(context.Context, string, string, string) (domain.Call, error)
	RenewCallPresence(context.Context, string, string, string) error
	// LeaveCall releases one participant's own presence in a resource call —
	// distinct from TransitionCall's call.end, which only the resource
	// call's original caller may use to end it for everyone (issue #569).
	// workspaceID, actorID, callID, in that order.
	LeaveCall(context.Context, string, string, string) (domain.Call, error)
}

type CallLimiter interface {
	AllowActionWithLimit(context.Context, string, string, int, int) (bool, error)
}

func WithCallHandler(handler CallHandler) HubOption {
	return func(h *Hub) { h.callHandler = handler }
}

func WithCallLimiter(limiter CallLimiter, maxActions, windowSeconds int) HubOption {
	return func(h *Hub) {
		h.callLimiter = limiter
		h.callStartLimit = maxActions
		h.callStartWindow = windowSeconds
	}
}

func (h *Hub) handleCallMessage(ctx context.Context, c *Client, msg ClientMessage) error {
	if h.callHandler == nil {
		return ErrCallFeatureDisabled
	}
	switch msg.Type {
	case ClientMessageTypeCallStart:
		direct := msg.TargetUserID != "" && msg.TargetType == "" && msg.TargetID == ""
		resource := msg.TargetUserID == "" && (msg.TargetType == TargetTypeChannel || msg.TargetType == TargetTypeDM) && msg.TargetID != ""
		if msg.CallID != "" || msg.MessageID != "" || msg.Emoji != "" || msg.RequestID == "" ||
			(!direct && !resource) || !msg.CallType.Valid() {
			return domain.ErrInvalidInput
		}
		requestID, err := canonicalCallUUID(msg.RequestID)
		if err != nil {
			return domain.ErrInvalidInput
		}
		calleeID, targetID := "", ""
		if direct {
			calleeID, err = canonicalCallUUID(msg.TargetUserID)
		} else {
			targetID, err = canonicalCallUUID(msg.TargetID)
		}
		if err != nil {
			return domain.ErrInvalidInput
		}
		if h.callLimiter == nil {
			return ErrCallFeatureDisabled
		}
		limit, window := h.callStartLimit, h.callStartWindow
		if limit <= 0 {
			limit = defaultCallStartLimit
		}
		if window <= 0 {
			window = defaultCallStartWindow
		}
		allowed, err := h.callLimiter.AllowActionWithLimit(ctx, c.userID, "call_start", limit, window)
		if err != nil {
			return fmt.Errorf("%w: %v", ErrCallFeatureDisabled, err)
		}
		if !allowed {
			return ErrCallRateLimited
		}
		_, err = h.callHandler.StartCall(ctx, StartCallCommand{
			WorkspaceID: c.workspaceID,
			RequestID:   requestID,
			CallerID:    c.userID,
			CalleeID:    calleeID,
			TargetType:  msg.TargetType,
			TargetID:    targetID,
			Type:        msg.CallType,
		})
		return err

	case ClientMessageTypeCallPresence:
		if msg.RequestID != "" || msg.TargetUserID != "" || msg.CallType != "" || msg.TargetType != "" ||
			msg.TargetID != "" || msg.MessageID != "" || msg.Emoji != "" {
			return domain.ErrInvalidInput
		}
		callID, err := canonicalCallUUID(msg.CallID)
		if err != nil {
			return domain.ErrInvalidInput
		}
		return h.callHandler.RenewCallPresence(ctx, c.workspaceID, c.userID, callID)

	case ClientMessageTypeCallAccept, ClientMessageTypeCallDecline, ClientMessageTypeCallCancel, ClientMessageTypeCallEnd:
		if msg.RequestID != "" || msg.TargetUserID != "" || msg.CallType != "" || msg.TargetType != "" ||
			msg.TargetID != "" || msg.MessageID != "" || msg.Emoji != "" {
			return domain.ErrInvalidInput
		}
		callID, err := canonicalCallUUID(msg.CallID)
		if err != nil {
			return domain.ErrInvalidInput
		}
		_, err = h.callHandler.TransitionCall(ctx, c.workspaceID, c.userID, callID, msg.Type)
		return err

	case ClientMessageTypeCallLeave:
		if msg.RequestID != "" || msg.TargetUserID != "" || msg.CallType != "" || msg.TargetType != "" ||
			msg.TargetID != "" || msg.MessageID != "" || msg.Emoji != "" {
			return domain.ErrInvalidInput
		}
		callID, err := canonicalCallUUID(msg.CallID)
		if err != nil {
			return domain.ErrInvalidInput
		}
		call, err := h.callHandler.LeaveCall(ctx, c.workspaceID, c.userID, callID)
		if err != nil {
			return err
		}
		// Addressed to the requesting client only, exactly like call.sync's
		// reply — every other participant learns about a departure that
		// changes nothing for them only if the call itself transitions,
		// which PublishCall (inside the handler) already broadcasts.
		h.sendCallToClient(c, call)
		return nil

	case ClientMessageTypeCallSync:
		if msg.RequestID != "" || msg.TargetUserID != "" || msg.CallType != "" ||
			msg.TargetType != "" || msg.TargetID != "" || msg.MessageID != "" || msg.Emoji != "" {
			return domain.ErrInvalidInput
		}
		callID := ""
		if msg.CallID != "" {
			var err error
			callID, err = canonicalCallUUID(msg.CallID)
			if err != nil {
				return domain.ErrInvalidInput
			}
		}
		call, err := h.callHandler.CurrentCall(ctx, c.workspaceID, c.userID, callID)
		if err != nil {
			return err
		}
		h.sendCallToClient(c, call)
		return nil
	default:
		return domain.ErrInvalidInput
	}
}

func (h *Hub) PublishCall(ctx context.Context, call domain.Call) {
	if call.IsResource() {
		h.publishCallToTarget(ctx, call, TargetType(call.TargetType), call.TargetID)
		return
	}
	h.publishCallToUser(ctx, call, call.CallerID)
	h.publishCallToUser(ctx, call, call.CalleeID)
}

func (h *Hub) publishCallToUser(ctx context.Context, call domain.Call, userID string) {
	h.publishCallToTarget(ctx, call, TargetTypeUser, userID)
}

func (h *Hub) publishCallToTarget(ctx context.Context, call domain.Call, targetType TargetType, targetID string) {
	event, ok := h.newCallEvent(call, targetType, targetID)
	if !ok {
		return
	}
	data, err := json.Marshal(event)
	if err != nil {
		h.logger.ErrorContext(ctx, "ws: marshal call event", "call_id", call.ID, "error", err)
		return
	}
	select {
	case h.bcast <- broadcastReq{event: event, data: data}:
	case <-ctx.Done():
		return
	case <-h.quit:
		return
	}
	if err := h.bus.Publish(ctx, event); err != nil {
		h.logger.WarnContext(ctx, "ws: call bus publish failed", "call_id", call.ID, "event_type", string(event.Type))
	}
}

func (h *Hub) sendCallToClient(c *Client, call domain.Call) {
	targetType, targetID := TargetTypeUser, c.userID
	if call.IsResource() {
		targetType, targetID = TargetType(call.TargetType), call.TargetID
	}
	event, ok := h.newCallEvent(call, targetType, targetID)
	if !ok {
		return
	}
	data, err := json.Marshal(event)
	if err == nil {
		_ = c.enqueue(data)
	}
}

func (h *Hub) newCallEvent(call domain.Call, targetType TargetType, targetID string) (Event, bool) {
	eventType, ok := callEventType(call.Status)
	if !ok {
		return Event{}, false
	}
	return Event{
		SchemaVersion: CurrentEventSchemaVersion,
		Type:          eventType,
		WorkspaceID:   call.WorkspaceID,
		TargetType:    targetType,
		TargetID:      targetID,
		Call: &CallEventPayload{
			ID: call.ID, RequestID: call.RequestID, CallerID: call.CallerID, CalleeID: call.CalleeID,
			TargetType: call.TargetType, TargetID: call.TargetID,
			CallType: call.Type, Status: call.Status, Version: call.Version,
			CreatedAt: call.CreatedAt, OccurredAt: call.UpdatedAt, ExpiresAt: call.ExpiresAt,
			AcceptedAt: call.AcceptedAt, EndedAt: call.EndedAt,
		},
		EventID: uuid.NewString(), SourceInstanceID: h.presenceInstanceID, CreatedAt: call.UpdatedAt,
	}, true
}

func callEventType(status domain.CallStatus) (EventType, bool) {
	switch status {
	case domain.CallStatusRinging:
		return EventTypeCallRinging, true
	case domain.CallStatusActive:
		return EventTypeCallAccepted, true
	case domain.CallStatusDeclined:
		return EventTypeCallDeclined, true
	case domain.CallStatusCancelled:
		return EventTypeCallCancelled, true
	case domain.CallStatusTimedOut:
		return EventTypeCallTimedOut, true
	case domain.CallStatusEnded:
		return EventTypeCallEnded, true
	default:
		return "", false
	}
}

func canonicalCallUUID(value string) (string, error) {
	id, err := uuid.Parse(value)
	if err != nil {
		return "", err
	}
	return id.String(), nil
}

func handleCallClientError(c *Client, operation ClientMessageType, callID string, callErr error) bool {
	response := clientErrorResponse{Type: "call.error", Operation: string(operation), CallID: callID}
	switch {
	case errors.Is(callErr, ErrCallRateLimited):
		response.Code, response.RetryAfter = "call_rate_limited", defaultCallStartWindow
	case errors.Is(callErr, ErrCallFeatureDisabled):
		response.Code = "call_unavailable"
	case errors.Is(callErr, domain.ErrInvalidInput):
		response.Code = "call_invalid"
	case errors.Is(callErr, domain.ErrNotFound), errors.Is(callErr, domain.ErrForbidden):
		response.Code = "call_not_found"
	case errors.Is(callErr, domain.ErrCallParticipantBusy):
		// Must be checked before the generic ErrConflict case below:
		// ErrCallParticipantBusy wraps ErrConflict, so it would otherwise
		// match that broader case first and be misreported as a lifecycle
		// state conflict (issue #575).
		response.Code = "call_participant_busy"
	case errors.Is(callErr, domain.ErrConflict):
		response.Code = "call_invalid_state"
	default:
		return false
	}
	data, err := json.Marshal(response)
	return err == nil && c.enqueue(data)
}
