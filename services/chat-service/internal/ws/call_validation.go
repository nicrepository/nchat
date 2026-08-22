package ws

import (
	"github.com/google/uuid"
	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
)

func isCallClientMessage(messageType ClientMessageType) bool {
	switch messageType {
	case ClientMessageTypeCallStart, ClientMessageTypeCallAccept, ClientMessageTypeCallDecline,
		ClientMessageTypeCallCancel, ClientMessageTypeCallEnd, ClientMessageTypeCallLeave,
		ClientMessageTypeCallSync, ClientMessageTypeCallPresence,
		ClientMessageTypeCallResourceSync, ClientMessageTypeCallJoin:
		return true
	default:
		return false
	}
}

func isCallEventType(eventType EventType) bool {
	switch eventType {
	case EventTypeCallRinging, EventTypeCallAccepted, EventTypeCallDeclined,
		EventTypeCallCancelled, EventTypeCallTimedOut, EventTypeCallEnded:
		return true
	default:
		return false
	}
}

// canonicalizeCallEvent validates a call lifecycle event arriving over the bus
// (issue #622 follow-up to #546).
//
// DIRECT (event.TargetType == user): unchanged from before #622 — caller_id,
// callee_id and the event's own target_id all validated exactly as they
// always were. A resource call never reaches this branch (TargetTypeUser is
// never the envelope target type for one).
//
// RESOURCE (event.TargetType == channel or dm): the envelope's own target_id
// (already parsed to a canonical UUID by canonicalizeEventIDs) must match the
// call's own persisted TargetID, and the call's TargetType must agree with
// which of the two the envelope claims — this is what closes the original bug
// (canonicalizeCallEvent used to require TargetTypeUser unconditionally,
// which silently dropped every resource event a remote instance published).
// callee_id must be empty: a resource call never carries one (calls_target_check).
func canonicalizeCallEvent(event Event) (Event, bool) {
	if event.Call == nil || event.MessageID != "" ||
		event.Payload != nil || event.MessageUpdate != nil || event.Reaction != nil || event.Pin != nil {
		return Event{}, false
	}
	call := event.Call

	id, err := uuid.Parse(call.ID)
	if err != nil {
		return Event{}, false
	}
	call.ID = id.String()
	requestID, err := uuid.Parse(call.RequestID)
	if err != nil {
		return Event{}, false
	}
	call.RequestID = requestID.String()
	callerID, err := uuid.Parse(call.CallerID)
	if err != nil {
		return Event{}, false
	}
	call.CallerID = callerID.String()

	switch event.TargetType {
	case TargetTypeUser:
		calleeID, err := uuid.Parse(call.CalleeID)
		if err != nil {
			return Event{}, false
		}
		call.CalleeID = calleeID.String()
		if event.TargetID != call.CallerID && event.TargetID != call.CalleeID {
			return Event{}, false
		}
	case TargetTypeChannel, TargetTypeDM:
		if call.CalleeID != "" {
			return Event{}, false
		}
		wantDomainTargetType := domain.CallTargetChannel
		if event.TargetType == TargetTypeDM {
			wantDomainTargetType = domain.CallTargetDM
		}
		if call.TargetType != wantDomainTargetType {
			return Event{}, false
		}
		targetID, err := uuid.Parse(call.TargetID)
		if err != nil {
			return Event{}, false
		}
		call.TargetID = targetID.String()
		if event.TargetID != call.TargetID {
			return Event{}, false
		}
	default:
		return Event{}, false
	}

	if !call.CallType.Valid() || !call.Status.Valid() || call.Version < 1 ||
		callEventStatus(event.Type) != call.Status || call.CreatedAt.IsZero() ||
		call.OccurredAt.IsZero() || call.ExpiresAt.IsZero() {
		return Event{}, false
	}
	return event, true
}

func callEventStatus(eventType EventType) domain.CallStatus {
	switch eventType {
	case EventTypeCallRinging:
		return domain.CallStatusRinging
	case EventTypeCallAccepted:
		return domain.CallStatusActive
	case EventTypeCallDeclined:
		return domain.CallStatusDeclined
	case EventTypeCallCancelled:
		return domain.CallStatusCancelled
	case EventTypeCallTimedOut:
		return domain.CallStatusTimedOut
	case EventTypeCallEnded:
		return domain.CallStatusEnded
	default:
		return ""
	}
}
