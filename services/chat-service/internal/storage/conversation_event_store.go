package storage

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
)

// ConversationEventInput describes one system message to persist (issue #527).
//
// Exactly one of ChannelID / DMConversationID is set, matching the
// messages_exactly_one_target CHECK. ActorID becomes chat.messages.sender_id, so
// who did it is carried by the same column every other message's author uses and
// is resolved through the same authorized projection — there is deliberately no
// actor *name* field anywhere in this input or in the payload.
type ConversationEventInput struct {
	WorkspaceID      string
	ChannelID        string
	DMConversationID string
	ActorID          string
	Event            domain.ConversationEventType
	Payload          domain.ConversationEventPayload
}

// InsertConversationEvent writes a system message inside the caller's
// transaction and returns the row it created.
//
// It is an internal writer and is deliberately not reachable from any HTTP
// route: there is no endpoint that accepts a kind, an event type or an event
// payload, so a client cannot forge one of these. That is also why the INSERT
// carries no authorization subquery of its own, unlike PGXMessageStore's — the
// transaction calling it has already established the authority for the mutation
// the event describes, and re-deriving the actor's *send* permission here would
// be wrong twice over: it would duplicate a decision already taken, and it would
// refuse the "member left" event, which by construction is written in the same
// transaction that removed the membership it would have checked.
//
// Callers must invoke it inside the transaction that performs the mutation, so a
// rename that rolls back leaves no event claiming it happened, and an event
// never exists without the change it describes.
func InsertConversationEvent(ctx context.Context, q channelQuerier, input ConversationEventInput) (domain.Message, error) {
	if !domain.ValidConversationEventType(input.Event) {
		return domain.Message{}, domain.ErrUnknownConversationEvent
	}
	payload, err := json.Marshal(input.Payload)
	if err != nil {
		return domain.Message{}, fmt.Errorf("marshal conversation event payload: %w", err)
	}

	var message domain.Message
	err = q.QueryRow(ctx, `
		INSERT INTO chat.messages
			(workspace_id, channel_id, dm_conversation_id, sender_id, kind,
			 body_text, event_type, event_payload)
		VALUES ($1, $2, $3, $4, 'system', '', $5, $6)
		RETURNING id, workspace_id, COALESCE(channel_id::text, ''),
		          COALESCE(dm_conversation_id::text, ''), sender_id::text, kind,
		          COALESCE(event_type, ''), created_at`,
		input.WorkspaceID,
		nullableUUID(input.ChannelID),
		nullableUUID(input.DMConversationID),
		input.ActorID,
		string(input.Event),
		payload,
	).Scan(
		&message.ID, &message.WorkspaceID, &message.ChannelID,
		&message.DMConversationID, &message.SenderID, (*string)(&message.Kind),
		&message.EventType, &message.CreatedAt,
	)
	if err != nil {
		return domain.Message{}, fmt.Errorf("insert conversation event: %w", err)
	}
	message.EventPayload = input.Payload
	return message, nil
}

// decodeConversationEvent fills a read message's structured event, if it has
// one (issue #527).
//
// A row whose event_type this build does not recognise is left with no event at
// all rather than surfaced as an unknown one: the client renders nothing for it,
// which is the safe failure for a value written by a newer producer. The
// database CHECK already guarantees the pairing with kind='system', so a user
// message can never arrive here carrying a payload.
func decodeConversationEvent(message *domain.Message, payload []byte) error {
	if message.EventType == "" {
		return nil
	}
	if !domain.ValidConversationEventType(domain.ConversationEventType(message.EventType)) {
		message.EventType = ""
		return nil
	}
	if len(payload) == 0 {
		return nil
	}
	if err := json.Unmarshal(payload, &message.EventPayload); err != nil {
		return fmt.Errorf("decode conversation event payload: %w", err)
	}
	return nil
}
