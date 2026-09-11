package service_test

import (
	"context"
	"errors"
	"testing"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/service"
)

// assertChannelSendForwardsPriority is one whole behaviour: a channel send
// stating this priority reaches storage with it, and answers with it.
//
// A helper rather than a fourth level of nesting, and a named one: the
// assertions are here in full, not hidden behind a generic matcher, and the
// only thing the caller supplies is the value under test.
func assertChannelSendForwardsPriority(t *testing.T, priority domain.MessagePriority) {
	t.Helper()
	channels := &fakeChannelStore{visibleChannel: publicActiveChannel("ws-1", "ch-1")}
	msgs := &fakeMessageStore{createdMessage: domain.Message{ID: "msg-1", Priority: priority}}

	got, err := service.NewMessageService(channels, &fakeDMStore{}, msgs).
		CreateChannelMessage(context.Background(), service.CreateChannelMessageInput{
			WorkspaceID: "ws-1", ChannelID: "ch-1", SenderID: user1, BodyText: "hello",
			Priority: priority,
		})
	if err != nil {
		t.Fatalf("CreateChannelMessage(%q): %v", priority, err)
	}
	if msgs.lastCreateInput.Priority != priority {
		t.Fatalf("storage received Priority %q, want %q", msgs.lastCreateInput.Priority, priority)
	}
	if got.Priority != priority {
		t.Fatalf("returned Priority %q, want %q", got.Priority, priority)
	}
}

// assertDMSendForwardsPriority is the same behaviour on the DM path.
func assertDMSendForwardsPriority(t *testing.T, priority domain.MessagePriority) {
	t.Helper()
	dms := &fakeDMStore{visibleConversation: activeDMConversation("ws-1", "dm-1")}
	msgs := &fakeMessageStore{createdMessage: domain.Message{ID: "msg-1", Priority: priority}}

	got, err := service.NewMessageService(&fakeChannelStore{}, dms, msgs).
		CreateDMMessage(context.Background(), service.CreateDMMessageInput{
			WorkspaceID: "ws-1", ConversationID: "dm-1", SenderID: user1, BodyText: "hello",
			Priority: priority,
		})
	if err != nil {
		t.Fatalf("CreateDMMessage(%q): %v", priority, err)
	}
	if msgs.lastCreateInput.Priority != priority {
		t.Fatalf("storage received Priority %q, want %q", msgs.lastCreateInput.Priority, priority)
	}
	if got.Priority != priority {
		t.Fatalf("returned Priority %q, want %q", got.Priority, priority)
	}
}

// The validated priority must reach the storage input unchanged. Anything that
// quietly rewrote it between the request and the write would be invisible to a
// caller — the response is built from what storage returns, which a fake will
// happily agree with.
func TestMessageService_CreateChannelMessage_ForwardsPriorityToStorage(t *testing.T) {
	for _, priority := range []domain.MessagePriority{
		domain.MessagePriorityStandard,
		domain.MessagePriorityImportant,
		domain.MessagePriorityUrgent,
	} {
		t.Run(string(priority), func(t *testing.T) {
			assertChannelSendForwardsPriority(t, priority)
		})
	}
}

func TestMessageService_CreateDMMessage_ForwardsPriorityToStorage(t *testing.T) {
	for _, priority := range []domain.MessagePriority{
		domain.MessagePriorityStandard,
		domain.MessagePriorityImportant,
		domain.MessagePriorityUrgent,
	} {
		t.Run(string(priority), func(t *testing.T) {
			assertDMSendForwardsPriority(t, priority)
		})
	}
}

// A client that says nothing about priority gets standard — it is not made to
// spell out the default to keep working.
func TestMessageService_CreateMessage_AbsentPriorityBecomesStandard(t *testing.T) {
	channels := &fakeChannelStore{visibleChannel: publicActiveChannel("ws-1", "ch-1")}
	msgs := &fakeMessageStore{createdMessage: domain.Message{ID: "msg-1"}}

	_, err := service.NewMessageService(channels, &fakeDMStore{}, msgs).
		CreateChannelMessage(context.Background(), service.CreateChannelMessageInput{
			WorkspaceID: "ws-1", ChannelID: "ch-1", SenderID: user1, BodyText: "hello",
		})
	if err != nil {
		t.Fatalf("CreateChannelMessage: %v", err)
	}
	if msgs.lastCreateInput.Priority != domain.MessagePriorityStandard {
		t.Fatalf("storage received Priority %q, want standard", msgs.lastCreateInput.Priority)
	}
}

// An unknown priority is refused, and refused before anything is written: a
// value we do not understand must not become a message at standard priority,
// because the author asked for something else.
func TestMessageService_CreateMessage_RejectsUnknownPriority(t *testing.T) {
	for _, priority := range []domain.MessagePriority{"critical", "Urgent", "high", "  urgent"} {
		t.Run(string(priority), func(t *testing.T) {
			channels := &fakeChannelStore{visibleChannel: publicActiveChannel("ws-1", "ch-1")}
			msgs := &fakeMessageStore{createdMessage: domain.Message{ID: "msg-1"}}

			_, err := service.NewMessageService(channels, &fakeDMStore{}, msgs).
				CreateChannelMessage(context.Background(), service.CreateChannelMessageInput{
					WorkspaceID: "ws-1", ChannelID: "ch-1", SenderID: user1, BodyText: "hello",
					Priority: priority,
				})
			if !errors.Is(err, domain.ErrInvalidInput) {
				t.Fatalf("CreateChannelMessage(%q) error = %v, want ErrInvalidInput", priority, err)
			}
			if msgs.createCalls != 0 {
				t.Fatalf("a refused priority must write nothing, got %d create calls", msgs.createCalls)
			}
		})
	}
}

// The DM path refuses it on the same terms, and equally before any write.
func TestMessageService_CreateDMMessage_RejectsUnknownPriority(t *testing.T) {
	dms := &fakeDMStore{visibleConversation: activeDMConversation("ws-1", "dm-1")}
	msgs := &fakeMessageStore{createdMessage: domain.Message{ID: "msg-1"}}

	_, err := service.NewMessageService(&fakeChannelStore{}, dms, msgs).
		CreateDMMessage(context.Background(), service.CreateDMMessageInput{
			WorkspaceID: "ws-1", ConversationID: "dm-1", SenderID: user1, BodyText: "hello",
			Priority: "critical",
		})
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("CreateDMMessage error = %v, want ErrInvalidInput", err)
	}
	if msgs.createCalls != 0 {
		t.Fatalf("a refused priority must write nothing, got %d create calls", msgs.createCalls)
	}
}

// Priority is part of what an idempotency key stands for. Reusing a key for the
// same body at a different priority is a different operation, and must not
// replay as the first one — the sender asked for something else and would
// otherwise be told nothing.
func TestMessageService_CreateMessage_PriorityIsPartOfTheOperationIdentity(t *testing.T) {
	fingerprintFor := func(priority domain.MessagePriority) string {
		t.Helper()
		channels := &fakeChannelStore{visibleChannel: publicActiveChannel("ws-1", "ch-1")}
		msgs := &fakeMessageStore{createdMessage: domain.Message{ID: "msg-1"}}
		_, err := service.NewMessageService(channels, &fakeDMStore{}, msgs).
			CreateChannelMessage(context.Background(), service.CreateChannelMessageInput{
				WorkspaceID: "ws-1", ChannelID: "ch-1", SenderID: user1, BodyText: "hello",
				Priority: priority, IdempotencyKey: "key-1",
			})
		if err != nil {
			t.Fatalf("CreateChannelMessage(%q): %v", priority, err)
		}
		return msgs.lastCreateReplayInput.RequestFingerprint
	}

	absent := fingerprintFor("")
	standard := fingerprintFor(domain.MessagePriorityStandard)
	important := fingerprintFor(domain.MessagePriorityImportant)
	urgent := fingerprintFor(domain.MessagePriorityUrgent)

	// Backward compatibility: a send that states nothing and a send that states
	// the default are the same operation, and hash as the identity already did
	// before this field existed — so a key already in flight when this ships
	// still replays instead of becoming a conflict.
	if absent != standard {
		t.Fatal("an omitted priority and an explicit standard must be the same operation")
	}
	for _, tc := range []struct {
		name        string
		fingerprint string
	}{
		{name: "important", fingerprint: important},
		{name: "urgent", fingerprint: urgent},
	} {
		if tc.fingerprint == standard {
			t.Fatalf("a %s send must not share the standard send's identity", tc.name)
		}
	}
	if important == urgent {
		t.Fatal("important and urgent are different operations")
	}
}
