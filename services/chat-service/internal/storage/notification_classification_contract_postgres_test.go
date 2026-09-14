package storage_test

import (
	"testing"

	"github.com/nicrepository/nchat/libs/go/platform/notificationevent"
	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/service"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

// The classification contract between the two consumers (issue #136).
//
// A conversation level draws its line between "I was named or answered" and
// "somebody posted", so the two paths that deliver notifications have to agree
// about which side a message falls on for a given person:
//
//	push      chat.notification_outbox.kind, written in SQL by the statement
//	          that creates the message
//	realtime  service.NotificationEventTypeFor, evaluated per recipient by the
//	          fan-out
//
// They are two mechanisms, so agreement is a property to prove rather than
// assume: a divergence would mean the same message interrupts somebody through
// one channel and is suppressed on the other. These tests drive the real
// statement against a real PostgreSQL and compare its answer, per recipient,
// with the Go classifier's.

// realtimeClass is what the fan-out would call this message for one recipient.
//
// It resolves the naming through the same codec the server uses, exactly as the
// realtime adapter does.
func realtimeClass(msg domain.Message, recipientID string) notificationevent.EventType {
	named, everyone := service.NamedRecipients(msg.BodyText)
	return service.NotificationEventTypeFor(msg, recipientID, named, everyone)
}

// outboxClass is what the outbox statement persisted for one recipient, and
// whether it produced a row for them at all.
func outboxClass(rows []outboxRow, recipientID string) (notificationevent.EventType, bool) {
	for _, row := range rows {
		if row.Recipient == recipientID {
			return notificationevent.EventType(row.Kind), true
		}
	}
	return "", false
}

// A group message that names one member and answers another: one message, three
// recipients, three different classifications — and the two paths have to agree
// on all of them.
func TestRealtimeAndOutboxClassifyTheSameMessageIdenticallyPostgreSQL(t *testing.T) {
	pool := seedNotificationFixture(t)
	store := storage.NewPGXMessageStore(pool)

	// notifyThird wrote the parent, so the reply is aimed at them.
	parent := mustCreate(t, store, storage.CreateMessageInput{
		WorkspaceID:      notifyWorkspace,
		DMConversationID: notifyConversation,
		SenderID:         notifyThird,
		BodyText:         "the message being answered",
		BodyFormat:       domain.MessageBodyFormatV3,
	})
	// ...and the body names notifyPeer, in the codec's canonical form.
	//
	// MentionedUserIDs carries the same set, because that is how the two halves
	// stay one fact in production: MessageService.resolveOutgoingMentions runs
	// extractMentionIDs over the body — the same codec NamedRecipients uses —
	// and hands the result to this input. Supplying one without the other would
	// be a state the service cannot produce, and the test would be comparing
	// the paths against an inconsistency of its own making.
	msg := mustCreate(t, store, storage.CreateMessageInput{
		WorkspaceID:      notifyWorkspace,
		DMConversationID: notifyConversation,
		SenderID:         notifyAuthor,
		BodyText:         "@[Peer](mention:user:" + notifyPeer + ") veja",
		BodyFormat:       domain.MessageBodyFormatV3,
		ParentMessageID:  parent.ID,
		MentionedUserIDs: []string{notifyPeer},
	})

	// The store filled the canonical reply fact from the persisted parent.
	if msg.ReplyToSenderID != notifyThird {
		t.Fatalf("ReplyToSenderID = %q, want the parent's author", msg.ReplyToSenderID)
	}

	rows := readOutbox(t, pool, msg.ID)
	if len(rows) == 0 {
		t.Fatal("the message produced no notifications to compare against")
	}
	for _, recipient := range []struct {
		name   string
		userID string
		want   notificationevent.EventType
	}{
		{name: "the person the message names", userID: notifyPeer,
			want: notificationevent.EventTypeMention},
		{name: "the author of the message being answered", userID: notifyThird,
			want: notificationevent.EventTypeReply},
	} {
		t.Run(recipient.name, func(t *testing.T) {
			persisted, found := outboxClass(rows, recipient.userID)
			if !found {
				t.Fatalf("the outbox produced no row for %s", recipient.userID)
			}
			if persisted != recipient.want {
				t.Fatalf("the outbox classified this as %q, want %q", persisted, recipient.want)
			}
			if live := realtimeClass(msg, recipient.userID); live != persisted {
				t.Fatalf("realtime says %q and the outbox says %q for the same message and recipient",
					live, persisted)
			}
		})
	}

	// The author is never a recipient of their own message, on either path.
	if _, found := outboxClass(rows, notifyAuthor); found {
		t.Fatal("the outbox notified the author of their own message")
	}
}

// The case the visual quote could not have carried: a reply whose parent was
// deleted, so nothing a reader may see says it answers anybody.
//
// This is the divergence the finding was about. The preview is withheld by the
// presentation rules, and the classification must survive that — on the
// realtime path as it always did on the push path, which reads the column.
func TestAReplyStaysAReplyWithoutAVisibleQuotePostgreSQL(t *testing.T) {
	pool := seedNotificationFixture(t)
	store := storage.NewPGXMessageStore(pool)

	parent := mustCreate(t, store, storage.CreateMessageInput{
		WorkspaceID:      notifyWorkspace,
		DMConversationID: notifyConversation,
		SenderID:         notifyThird,
		BodyText:         "the message being answered",
		BodyFormat:       domain.MessageBodyFormatV3,
	})
	msg := mustCreate(t, store, storage.CreateMessageInput{
		WorkspaceID:      notifyWorkspace,
		DMConversationID: notifyConversation,
		SenderID:         notifyAuthor,
		BodyText:         "respondi",
		BodyFormat:       domain.MessageBodyFormatV3,
		ParentMessageID:  parent.ID,
	})

	rows := readOutbox(t, pool, msg.ID)
	persisted, found := outboxClass(rows, notifyThird)
	if !found || persisted != notificationevent.EventTypeReply {
		t.Fatalf("the outbox classified this as (%q, found=%v), want a reply", persisted, found)
	}

	// Now strip every trace of the preview, which is what a removed or withheld
	// parent does to it, and keep the canonical fact.
	withoutPreview := msg
	withoutPreview.Quoted = nil
	if live := realtimeClass(withoutPreview, notifyThird); live != notificationevent.EventTypeReply {
		t.Fatalf("realtime classified a reply with no visible quote as %q", live)
	}
	// ...and with the canonical fact gone too, it is honestly an ordinary
	// message: the classifier reads one authority and does not guess.
	withoutFact := withoutPreview
	withoutFact.ReplyToSenderID = ""
	if live := realtimeClass(withoutFact, notifyThird); live != notificationevent.EventTypeDirectMessage {
		t.Fatalf("realtime invented a classification with no fact to read: %q", live)
	}
}

// An ordinary message in a group: every recipient is classified the same way by
// both paths, and nobody is upgraded to a mention by being present.
func TestAnOrdinaryGroupMessageClassifiesIdenticallyPostgreSQL(t *testing.T) {
	pool := seedNotificationFixture(t)
	store := storage.NewPGXMessageStore(pool)

	msg := mustCreate(t, store, storage.CreateMessageInput{
		WorkspaceID:      notifyWorkspace,
		DMConversationID: notifyConversation,
		SenderID:         notifyAuthor,
		BodyText:         "bom dia a todos",
		BodyFormat:       domain.MessageBodyFormatV3,
	})

	rows := readOutbox(t, pool, msg.ID)
	if len(rows) == 0 {
		t.Fatal("an ordinary group message produced no notifications")
	}
	for _, row := range rows {
		persisted := notificationevent.EventType(row.Kind)
		if persisted != notificationevent.EventTypeDirectMessage {
			t.Fatalf("the outbox classified an ordinary group message as %q", persisted)
		}
		if live := realtimeClass(msg, row.Recipient); live != persisted {
			t.Fatalf("realtime says %q and the outbox says %q for %s",
				live, persisted, row.Recipient)
		}
	}
}
