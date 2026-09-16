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

// resolveConversationEventTargetUsers resolves display names for a
// member.added/member.removed event's target_users, at write time, inside the
// same transaction as the membership change (issue #685).
//
// This is the one deliberate exception to "the payload carries facts, never a
// name" documented on domain.ConversationEventUser: a target has no other
// column to be resolved from later the way an actor has sender_id, and a
// renderer must never fetch a profile by id on its own. unnest(...) WITH
// ORDINALITY keeps the result ordered exactly like userIDs, which is what
// makes "who was added" deterministic for a batch. LEFT JOIN rather than
// INNER: a user whose account vanished between the membership INSERT and this
// read (a narrow race, not the common case) still gets an entry, with no name
// — the same "Alguém" fallback an unresolved actor already gets, never a
// dropped target.
func resolveConversationEventTargetUsers(
	ctx context.Context, q channelQuerier, userIDs []string,
) ([]domain.ConversationEventUser, error) {
	if len(userIDs) == 0 {
		return nil, nil
	}
	rows, err := q.Query(ctx, `
		SELECT t.user_id::text,
		       COALESCE(NULLIF(BTRIM(u.full_name), ''), NULLIF(BTRIM(u.display_name), ''), '')
		FROM unnest($1::uuid[]) WITH ORDINALITY AS t(user_id, ord)
		LEFT JOIN auth.users u ON u.id = t.user_id
		ORDER BY t.ord`,
		userIDs,
	)
	if err != nil {
		return nil, fmt.Errorf("resolve conversation event target users: %w", err)
	}
	defer rows.Close()

	targets := make([]domain.ConversationEventUser, 0, len(userIDs))
	for rows.Next() {
		var target domain.ConversationEventUser
		if err := rows.Scan(&target.UserID, &target.DisplayName); err != nil {
			return nil, fmt.Errorf("scan conversation event target user: %w", err)
		}
		targets = append(targets, target)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("resolve conversation event target users: %w", err)
	}
	return targets, nil
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
