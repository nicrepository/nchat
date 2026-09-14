package service

import (
	"slices"

	"github.com/nicrepository/nchat/libs/go/platform/notificationevent"
	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
)

// The canonical classification of a message event, per recipient (issue #136).
//
// # Why it lives here
//
// Two paths have to agree about what one message *is* for one person:
//
//	the push path      chat.notification_outbox.kind, written by the same
//	                   statement that creates the message
//	the realtime path  the per-recipient decision the fan-out makes
//
// They are two different mechanisms — SQL and Go — so agreement cannot be
// assumed, it has to be tested. Keeping the Go half in this package, beside the
// mention codec it reads, is what lets the storage suite compare the two against
// a real PostgreSQL for the same message and the same recipient.
//
// # What it may read
//
// Only facts the server derived. `NamedRecipients` is the same mention codec
// that decides who chat.message_mentions rows are written for, and
// `ReplyToSenderID` is the persisted parent's author — the same column the
// outbox's reply rule reads as parent.sender_id. There is no substring search
// for "@" here, and nothing a client sent takes part.

// NotificationEventTypeFor classifies a message for one recipient.
//
// The precedence is the one chat.notification_outbox's own recipient CTE uses
// (rank 1 named, rank 2 answered, rank 3 present), so one message is classified
// the same way whichever path is asking. Exactly one classification per
// recipient, and it is the strongest that applies.
//
// `named` and `everyone` are passed in rather than derived here because the
// caller resolves them once per message and asks this once per recipient; a
// fan-out must not re-parse a body per subscriber.
//
// With no recipient — the publisher's own broadcast encoding, before anyone is
// resolved — the answer is the one every recipient shares.
func NotificationEventTypeFor(
	msg domain.Message, recipientID string, named []string, everyone bool,
) notificationevent.EventType {
	if recipientID == "" {
		return NotificationEventTypeForTarget(msg)
	}
	// @all counts as a mention only in a DM or group, which is the scope issue
	// #776 gave it and the scope the outbox enforces with `dc.type = 'group'`.
	// Treating a channel-wide @all as a personal mention here would let the
	// realtime path allow an alert the push path calls an ordinary message.
	if slices.Contains(named, recipientID) || (everyone && msg.DMConversationID != "") {
		return notificationevent.EventTypeMention
	}
	// The canonical reply fact, never the quoted preview beside it: that DTO is
	// shaped by what a reader may see, so a deleted parent or a withheld body
	// would have turned a reply into an ordinary message here while the outbox
	// still called it a reply.
	if msg.ReplyToSenderID != "" && msg.ReplyToSenderID == recipientID {
		return notificationevent.EventTypeReply
	}
	return NotificationEventTypeForTarget(msg)
}

// NotificationEventTypeForTarget classifies a message from its target alone:
// the classification every recipient of it shares.
func NotificationEventTypeForTarget(msg domain.Message) notificationevent.EventType {
	if msg.ChannelID != "" {
		return notificationevent.EventTypeChannelMessage
	}
	return notificationevent.EventTypeDirectMessage
}
