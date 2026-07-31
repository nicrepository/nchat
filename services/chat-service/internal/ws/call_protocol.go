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
	Type        domain.CallType
}

type CallHandler interface {
	StartCall(context.Context, StartCallCommand) (domain.Call, error)
	TransitionCall(context.Context, string, string, string, ClientMessageType) (domain.Call, error)
	CurrentCall(context.Context, string, string) (domain.Call, error)
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
		if msg.CallID != "" || msg.TargetType != "" || msg.TargetID != "" || msg.MessageID != "" || msg.Emoji != "" ||
			msg.RequestID == "" || msg.TargetUserID == "" || !msg.CallType.Valid() {
			return domain.ErrInvalidInput
		}
		requestID, err := canonicalCallUUID(msg.RequestID)
		if err != nil {
			return domain.ErrInvalidInput
		}
		calleeID, err := canonicalCallUUID(msg.TargetUserID)
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
			Type:        msg.CallType,
		})
		return err

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

	case ClientMessageTypeCallSync:
		if msg.RequestID != "" || msg.CallID != "" || msg.TargetUserID != "" || msg.CallType != "" ||
			msg.TargetType != "" || msg.TargetID != "" || msg.MessageID != "" || msg.Emoji != "" {
			return domain.ErrInvalidInput
		}
		call, err := h.callHandler.CurrentCall(ctx, c.workspaceID, c.userID)
		if err != nil {
			return err
		}
		h.publishCallToUser(ctx, call, c.userID)
		return nil
	default:
		return domain.ErrInvalidInput
	}
}

func (h *Hub) PublishCall(ctx context.Context, call domain.Call) {
	h.publishCallToUser(ctx, call, call.CallerID)
	h.publishCallToUser(ctx, call, call.CalleeID)
}

func (h *Hub) publishCallToUser(ctx context.Context, call domain.Call, userID string) {
	eventType, ok := callEventType(call.Status)
	if !ok {
		return
	}
	event := Event{
		SchemaVersion: CurrentEventSchemaVersion,
		Type:          eventType,
		WorkspaceID:   call.WorkspaceID,
		TargetType:    TargetTypeUser,
		TargetID:      userID,
		Call: &CallEventPayload{
			ID: call.ID, RequestID: call.RequestID, CallerID: call.CallerID, CalleeID: call.CalleeID,
			CallType: call.Type, Status: call.Status, Version: call.Version,
			CreatedAt: call.CreatedAt, OccurredAt: call.UpdatedAt, ExpiresAt: call.ExpiresAt,
			AcceptedAt: call.AcceptedAt, EndedAt: call.EndedAt,
		},
		EventID: uuid.NewString(), SourceInstanceID: h.instanceID, CreatedAt: call.UpdatedAt,
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
		h.logger.WarnContext(ctx, "ws: call bus publish failed", "call_id", call.ID, "event_type", string(eventType))
	}
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
	case errors.Is(callErr, domain.ErrConflict):
		response.Code = "call_invalid_state"
	default:
		return false
	}
	data, err := json.Marshal(response)
	return err == nil && c.enqueue(data)
}
