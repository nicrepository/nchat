package ws

import (
	"github.com/google/uuid"
	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
)

func isCallClientMessage(messageType ClientMessageType) bool {
	switch messageType {
	case ClientMessageTypeCallStart, ClientMessageTypeCallAccept, ClientMessageTypeCallDecline,
		ClientMessageTypeCallCancel, ClientMessageTypeCallEnd, ClientMessageTypeCallLeave,
		ClientMessageTypeCallSync, ClientMessageTypeCallPresence:
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

func canonicalizeCallEvent(event Event) (Event, bool) {
	if event.TargetType != TargetTypeUser || event.Call == nil || event.MessageID != "" ||
		event.Payload != nil || event.MessageUpdate != nil || event.Reaction != nil || event.Pin != nil {
		return Event{}, false
	}
	call := event.Call
	ids := []*string{&call.ID, &call.RequestID, &call.CallerID, &call.CalleeID}
	for _, value := range ids {
		id, err := uuid.Parse(*value)
		if err != nil {
			return Event{}, false
		}
		*value = id.String()
	}
	if event.TargetID != call.CallerID && event.TargetID != call.CalleeID {
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
