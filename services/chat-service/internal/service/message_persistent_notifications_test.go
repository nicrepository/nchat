package service_test

import (
	"context"
	"errors"
	"testing"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/service"
)

// Issue #825, the send side. Four things belong to the service here: the flag
// reaches storage, an unauthorised combination is refused before anything is
// written, the same per-recipient fan-out bound applies whether the rows were
// asked for by #824 or by this issue, and a send that asks for reminders is a
// different send for idempotency purposes.
//
// What the reminders then do is decided by SQL, in both services, and is proved
// against a real database.

func createPersistentChannelMessage(
	t *testing.T, priority domain.MessagePriority, persistent bool, recipients int,
) (*fakeMessageStore, error) {
	t.Helper()
	msgs := &fakeMessageStore{
		createdMessage:            domain.Message{ID: "msg-persistent"},
		acknowledgementRecipients: recipients,
	}
	_, err := service.NewMessageService(
		&fakeChannelStore{visibleChannel: publicActiveChannel("ws-1", "ch-1")}, &fakeDMStore{}, msgs,
	).CreateChannelMessage(context.Background(), service.CreateChannelMessageInput{
		WorkspaceID: "ws-1", ChannelID: "ch-1", SenderID: user1, BodyText: "restart the cluster",
		BodyFormat: domain.MessageBodyFormatV1,
		Priority:   priority, PersistentNotifications: persistent,
	})
	return msgs, err
}

// The flag reaches the statement that writes the message. Without it no schedule
// is written and every other behaviour in this issue is unreachable.
func TestMessageService_CreateChannelMessage_ForwardsPersistentNotifications(t *testing.T) {
	msgs, err := createPersistentChannelMessage(t, domain.MessagePriorityUrgent, true, 3)
	if err != nil {
		t.Fatalf("CreateChannelMessage: %v", err)
	}
	if !msgs.lastCreateInput.PersistentNotifications {
		t.Fatal("the reminder request did not reach storage")
	}
	if msgs.lastCreateInput.Priority != domain.MessagePriorityUrgent {
		t.Fatalf("priority = %q, want urgent", msgs.lastCreateInput.Priority)
	}
}

// Asking for reminders on a message that is not urgent is refused, and refused
// before anything is written. Sending it quietly without reminders would leave
// the author believing their message keeps asking when it has never asked once.
func TestMessageService_CreateChannelMessage_RefusesRemindersOnANonUrgentMessage(t *testing.T) {
	for _, priority := range []domain.MessagePriority{
		"", domain.MessagePriorityStandard, domain.MessagePriorityImportant,
	} {
		msgs, err := createPersistentChannelMessage(t, priority, true, 3)
		if !errors.Is(err, domain.ErrInvalidInput) {
			t.Fatalf("priority %q: err = %v, want ErrInvalidInput", priority, err)
		}
		if msgs.createCalls != 0 {
			t.Fatalf("priority %q: a refused send wrote a message", priority)
		}
	}
}

// The DM path is held to the same rule. Two create paths that validate
// differently is how one of them ends up being the way round the rule.
func TestMessageService_CreateDMMessage_RefusesRemindersOnANonUrgentMessage(t *testing.T) {
	msgs := &fakeMessageStore{createdMessage: domain.Message{ID: "msg-dm"}}
	_, err := service.NewMessageService(
		&fakeChannelStore{}, &fakeDMStore{visibleConversation: ackGroupConversation()}, msgs,
	).CreateDMMessage(context.Background(), service.CreateDMMessageInput{
		WorkspaceID: "ws-1", ConversationID: "group-1", SenderID: user1, BodyText: "hello",
		BodyFormat: domain.MessageBodyFormatV1, PersistentNotifications: true,
	})
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("err = %v, want ErrInvalidInput", err)
	}
	if msgs.createCalls != 0 {
		t.Fatal("a refused DM send wrote a message")
	}
}

// Reminders write the same per-recipient rows acknowledgement does, so they are
// judged against the same ceiling. A send that asked only for reminders and
// slipped past the bound would be the amplification #824 introduced the bound to
// stop, reached by the other door.
func TestMessageService_CreateChannelMessage_RemindersAreBoundedLikeAcknowledgement(t *testing.T) {
	msgs, err := createPersistentChannelMessage(t, domain.MessagePriorityUrgent, true,
		domain.MaxAcknowledgementRecipients+1)
	if !errors.Is(err, domain.ErrAcknowledgementRecipientsExceeded) {
		t.Fatalf("err = %v, want the recipient bound refusal", err)
	}
	if msgs.createCalls != 0 {
		t.Fatal("a send past the bound must write nothing at all")
	}
	if msgs.countAcknowledgementCalls != 1 {
		t.Fatalf("counted %d times, want exactly one measurement", msgs.countAcknowledgementCalls)
	}
}

// Exactly the bound is admitted. The ceiling is a limit, not a limit minus one.
func TestMessageService_CreateChannelMessage_RemindersAdmitExactlyTheBound(t *testing.T) {
	if _, err := createPersistentChannelMessage(t, domain.MessagePriorityUrgent, true,
		domain.MaxAcknowledgementRecipients); err != nil {
		t.Fatalf("a send at the bound must be admitted, got %v", err)
	}
}

// An urgent message that asked for neither confirmation nor reminders never
// measures its conversation. The guard is what keeps the bound from putting a
// member count on the hot path of every send, and urgency alone is not a reason
// to spend one.
func TestMessageService_CreateChannelMessage_UrgentWithoutRemindersNeverCountsRecipients(t *testing.T) {
	msgs, err := createPersistentChannelMessage(t, domain.MessagePriorityUrgent, false, 1000)
	if err != nil {
		t.Fatalf("CreateChannelMessage: %v", err)
	}
	if msgs.countAcknowledgementCalls != 0 {
		t.Fatal("a send that asks for nothing must not measure the conversation")
	}
}

// The same urgent text sent once quietly and once with reminders are two
// different sends. Reusing one key across them must conflict rather than replay
// the silent one.
func TestMessageService_CreateChannelMessage_RemindersChangeTheReplayIdentity(t *testing.T) {
	fingerprint := func(persistent bool) string {
		msgs, err := createPersistentChannelMessageWithKey(t, persistent)
		if err != nil {
			t.Fatalf("CreateChannelMessage: %v", err)
		}
		return msgs.lastCreateInput.RequestFingerprint
	}
	quiet, reminding := fingerprint(false), fingerprint(true)
	if quiet == "" || reminding == "" {
		t.Fatal("both sends must record a fingerprint for the key they carried")
	}
	if quiet == reminding {
		t.Fatal("an urgent message with reminders must not replay one sent without them")
	}
}

func createPersistentChannelMessageWithKey(t *testing.T, persistent bool) (*fakeMessageStore, error) {
	t.Helper()
	msgs := &fakeMessageStore{createdMessage: domain.Message{ID: "msg-fp"}}
	_, err := service.NewMessageService(
		&fakeChannelStore{visibleChannel: publicActiveChannel("ws-1", "ch-1")}, &fakeDMStore{}, msgs,
	).CreateChannelMessage(context.Background(), service.CreateChannelMessageInput{
		WorkspaceID: "ws-1", ChannelID: "ch-1", SenderID: user1, BodyText: "same urgent text",
		BodyFormat: domain.MessageBodyFormatV1, IdempotencyKey: "key-825",
		Priority: domain.MessagePriorityUrgent, PersistentNotifications: persistent,
	})
	return msgs, err
}

// The compatibility half: a send that asks for no reminders hashes exactly as it
// did before this field existed, so a key already in flight when this ships
// still replays instead of becoming a conflict.
func TestMessageService_CreateChannelMessage_SendWithoutRemindersKeepsItsReplayIdentity(t *testing.T) {
	fingerprint := func(input service.CreateChannelMessageInput) string {
		msgs := &fakeMessageStore{createdMessage: domain.Message{ID: "msg-compat-825"}}
		if _, err := service.NewMessageService(
			&fakeChannelStore{visibleChannel: publicActiveChannel("ws-1", "ch-1")}, &fakeDMStore{}, msgs,
		).CreateChannelMessage(context.Background(), input); err != nil {
			t.Fatalf("CreateChannelMessage: %v", err)
		}
		return msgs.lastCreateInput.RequestFingerprint
	}
	base := service.CreateChannelMessageInput{
		WorkspaceID: "ws-1", ChannelID: "ch-1", SenderID: user1, BodyText: "urgent text",
		BodyFormat: domain.MessageBodyFormatV1, IdempotencyKey: "key-compat",
		Priority: domain.MessagePriorityUrgent,
	}
	stated := base
	stated.PersistentNotifications = false

	if fingerprint(base) != fingerprint(stated) {
		t.Fatal("stating the flag as false must hash identically to omitting it")
	}
}
