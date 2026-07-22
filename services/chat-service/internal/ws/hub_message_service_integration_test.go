package ws

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/service"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

// ── stateful fake stores ──────────────────────────────────────────────────────

type integChannelStore struct {
	ch domain.Channel
}

func (s *integChannelStore) CreateCategory(_ context.Context, _ storage.CreateCategoryInput) (domain.ChannelCategory, error) {
	return domain.ChannelCategory{}, nil
}
func (s *integChannelStore) CreateChannel(_ context.Context, _ storage.CreateChannelInput) (domain.Channel, error) {
	return domain.Channel{}, nil
}
func (s *integChannelStore) CreateChannelWithMember(_ context.Context, _ storage.CreateChannelInput, _ string, _ domain.ChannelRole) (domain.Channel, error) {
	return domain.Channel{}, nil
}
func (s *integChannelStore) GetCategoryByIDInWorkspace(_ context.Context, _, _ string) (domain.ChannelCategory, error) {
	return domain.ChannelCategory{}, nil
}
func (s *integChannelStore) GetChannelByID(_ context.Context, _ string) (domain.Channel, error) {
	return s.ch, nil
}
func (s *integChannelStore) GetChannelByIDInWorkspace(_ context.Context, _, _ string) (domain.Channel, error) {
	return s.ch, nil
}
func (s *integChannelStore) GetVisibleChannelByID(_ context.Context, _, _, _ string) (domain.Channel, error) {
	return s.ch, nil
}
func (s *integChannelStore) GetVisibleChannelBySlug(_ context.Context, _, _, _ string) (domain.Channel, error) {
	return s.ch, nil
}
func (s *integChannelStore) ListChannelsByWorkspace(_ context.Context, _ string) ([]domain.Channel, error) {
	return nil, nil
}
func (s *integChannelStore) ListVisibleChannelsByUser(_ context.Context, _, _ string) ([]domain.Channel, error) {
	return nil, nil
}
func (s *integChannelStore) UpdateChannel(_ context.Context, _ storage.UpdateChannelInput) (domain.Channel, error) {
	return domain.Channel{}, nil
}
func (s *integChannelStore) ArchiveChannel(_ context.Context, _, _ string) (domain.Channel, error) {
	return domain.Channel{}, nil
}

// integDMStore is a stateful fake that records the pre-seeded conversation and
// allows the test to read it back for validation.
type integDMStore struct {
	conv domain.DMConversation
}

func (s *integDMStore) CreateDirectConversation(_ context.Context, _ storage.CreateDirectConversationInput) (storage.CreateDirectConversationResult, error) {
	return storage.CreateDirectConversationResult{}, nil
}
func (s *integDMStore) CreateGroupConversation(_ context.Context, _ storage.CreateGroupConversationInput) (domain.DMConversation, error) {
	return domain.DMConversation{}, nil
}
func (s *integDMStore) ListVisibleConversationsByUser(_ context.Context, _, _ string) ([]domain.DMConversation, error) {
	return nil, nil
}
func (s *integDMStore) ListVisibleConversationsWithParticipantIDs(_ context.Context, _, _ string) ([]domain.DMConversationWithParticipantIDs, error) {
	return nil, nil
}
func (s *integDMStore) GetVisibleConversationByID(_ context.Context, _, _, _ string) (domain.DMConversation, error) {
	return s.conv, nil
}

// integMessageStore is a stateful fake that records every CreateMessage call.
// Tests use created() to read back all persisted inputs and validate them.
type integMessageStore struct {
	// seed is the prototype used to construct the returned domain.Message.
	// Only the ID field is copied; all other fields are taken from the input.
	seed domain.Message

	mu      sync.Mutex
	created []domain.Message
}

func (s *integMessageStore) EditMessage(_ context.Context, in storage.EditMessageInput) (domain.Message, error) {
	return domain.Message{ID: in.MessageID, WorkspaceID: in.WorkspaceID, SenderID: in.EditorID, BodyText: in.Body, BodyFormat: in.BodyFormat}, nil
}

func (s *integMessageStore) DeleteMessage(_ context.Context, in storage.DeleteMessageInput) (domain.Message, bool, error) {
	return domain.Message{ID: in.MessageID, WorkspaceID: in.WorkspaceID, SenderID: in.RequesterID, Kind: domain.MessageKindUser, Status: domain.MessageStatusDeleted}, true, nil
}

func (s *integMessageStore) ListMessageEditHistory(context.Context, storage.ListMessageEditHistoryInput) ([]domain.MessageEditHistory, error) {
	return nil, nil
}

func (s *integMessageStore) ResolveMentionLabels(_ context.Context, _ string, _, _ []string) (map[string]string, error) {
	return map[string]string{}, nil
}

func (s *integMessageStore) ResolveAuthorizedMentionLabels(_ context.Context, _, _, _ string, _, _ []string) (map[string]string, error) {
	return map[string]string{}, nil
}

// CreateMessage records the input and returns a domain.Message derived from seed + input.
func (s *integMessageStore) CreateMessage(_ context.Context, in storage.CreateMessageInput) (domain.Message, error) {
	now := time.Now().UTC()
	msg := domain.Message{
		ID:               s.seed.ID,
		WorkspaceID:      in.WorkspaceID,
		ChannelID:        in.ChannelID,
		DMConversationID: in.DMConversationID,
		SenderID:         in.SenderID,
		BodyText:         in.BodyText,
		Kind:             domain.MessageKindUser,
		Status:           domain.MessageStatusActive,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	s.mu.Lock()
	s.created = append(s.created, msg)
	s.mu.Unlock()
	return msg, nil
}

// listCreated returns a copy of all messages recorded by CreateMessage.
func (s *integMessageStore) listCreated() []domain.Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]domain.Message, len(s.created))
	copy(out, s.created)
	return out
}

func (s *integMessageStore) GetMessageByIDInWorkspace(_ context.Context, _, _, _ string) (domain.Message, error) {
	return s.seed, nil
}
func (s *integMessageStore) ValidateRefMessageInTarget(_ context.Context, _, _, _, _, _ string) error {
	return nil
}
func (s *integMessageStore) ResolveMessageReferences(_ context.Context, _, _ string, _ []string) (map[string]domain.MessageReference, error) {
	return map[string]domain.MessageReference{}, nil
}

func (s *integMessageStore) ListReferencedMessageIDs(_ context.Context, _, _, _, _ string, _ []string) (map[string]string, error) {
	return map[string]string{}, nil
}
func (s *integMessageStore) ListChannelMessages(_ context.Context, _ storage.ListChannelMessagesInput) (storage.ListMessagesResult, error) {
	return storage.ListMessagesResult{}, nil
}
func (s *integMessageStore) ListDMMessages(_ context.Context, _ storage.ListDMMessagesInput) (storage.ListMessagesResult, error) {
	return storage.ListMessagesResult{}, nil
}

// ── hubPublisherAdapter ───────────────────────────────────────────────────────

type hubPublisherAdapter struct{ hub *Hub }

func (a *hubPublisherAdapter) PublishMessageCreated(ctx context.Context, workspaceID, targetType, targetID string, msg domain.Message) {
	now := time.Now().UTC()
	if msg.CreatedAt.IsZero() {
		msg.CreatedAt = now
	}
	if msg.UpdatedAt.IsZero() {
		msg.UpdatedAt = now
	}
	payload := MessagePayload{
		ID:               msg.ID,
		WorkspaceID:      msg.WorkspaceID,
		ChannelID:        msg.ChannelID,
		DMConversationID: msg.DMConversationID,
		SenderID:         msg.SenderID,
		Kind:             string(msg.Kind),
		BodyText:         msg.BodyText,
		Status:           string(msg.Status),
		CreatedAt:        msg.CreatedAt,
		UpdatedAt:        msg.UpdatedAt,
	}
	a.hub.PublishMessageCreated(ctx, workspaceID, TargetType(targetType), targetID, payload)
}

// ── helpers ───────────────────────────────────────────────────────────────────

func assertEventPayload(t *testing.T, evt Event, wantType EventType, wantTargetType TargetType, wantTargetID, wantMsgID, wantBody string) {
	t.Helper()
	if evt.Type != wantType {
		t.Errorf("event.Type: got %q want %q", evt.Type, wantType)
	}
	if evt.TargetType != wantTargetType {
		t.Errorf("event.TargetType: got %q want %q", evt.TargetType, wantTargetType)
	}
	if evt.TargetID != wantTargetID {
		t.Errorf("event.TargetID: got %q want %q", evt.TargetID, wantTargetID)
	}
	if evt.Payload == nil {
		t.Fatal("event.Payload must not be nil")
		return
	}
	if evt.Payload.ID != wantMsgID {
		t.Errorf("payload.ID: got %q want %q", evt.Payload.ID, wantMsgID)
	}
	if evt.Payload.BodyText != wantBody {
		t.Errorf("payload.BodyText: got %q want %q", evt.Payload.BodyText, wantBody)
	}
}

func drainOneEvent(t *testing.T, c *Client) Event {
	t.Helper()
	var raw []byte
	select {
	case raw = <-c.outbox:
	default:
		t.Fatal("outbox empty — expected 1 event")
	}
	var evt Event
	if err := json.Unmarshal(raw, &evt); err != nil {
		t.Fatalf("json.Unmarshal event: %v", err)
	}
	return evt
}

// ── integration tests ─────────────────────────────────────────────────────────

// TestMessageServiceIntegration_CreateChannelMessage verifies the full
// service → store → publisher → hub → subscriber delivery path for a channel message:
//
//  1. CreateChannelMessage persists via the stateful fake store.
//  2. store.listCreated() confirms the input was recorded (persistence gate).
//  3. The hub fans out to the subscribed client.
//  4. The event payload carries the correct ID, targetType, targetID, and body.
func TestMessageServiceIntegration_CreateChannelMessage(t *testing.T) {
	const (
		wsID   = "ws-integ-ch"
		chID   = "ch-integ"
		userID = "user-integ-ch"
		msgID  = "msg-integ-ch-001"
		body   = "hello channel integration"
	)

	channels := &integChannelStore{ch: domain.Channel{
		ID: chID, WorkspaceID: wsID, Status: domain.ChannelStatusActive,
	}}
	messages := &integMessageStore{seed: domain.Message{ID: msgID}}

	auth := &fakeAuthorizer{}
	auth.setAccess(userID, wsID, TargetTypeChannel, chID, true)

	hub := NewHub(auth, newTestLogger(), NopBus{}, "test-integ-ch")
	defer hub.Shutdown()

	sub := newClient("integ-ch-sub", userID, wsID, &fakeSender{})
	registerInRunningHub(t, hub, sub)
	if err := hub.Subscribe(context.Background(), sub, TargetTypeChannel, chID); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	svc := service.NewMessageService(channels, &integDMStore{}, messages)
	svc.SetPublisher(&hubPublisherAdapter{hub: hub})

	if _, err := svc.CreateChannelMessage(context.Background(), service.CreateChannelMessageInput{
		WorkspaceID: wsID,
		ChannelID:   chID,
		SenderID:    userID,
		BodyText:    body,
	}); err != nil {
		t.Fatalf("CreateChannelMessage: %v", err)
	}

	// Persistence gate: store must have recorded exactly one message.
	persisted := messages.listCreated()
	if len(persisted) != 1 {
		t.Fatalf("store.listCreated(): got %d records, want 1", len(persisted))
	}
	if persisted[0].ChannelID != chID {
		t.Errorf("persisted.ChannelID: got %q want %q", persisted[0].ChannelID, chID)
	}
	if persisted[0].BodyText != body {
		t.Errorf("persisted.BodyText: got %q want %q", persisted[0].BodyText, body)
	}

	// Delivery gate.
	eventually(t, func() bool { return len(sub.outbox) == 1 }, 3*time.Second,
		"subscriber must receive 1 message.created event")

	assertEventPayload(t, drainOneEvent(t, sub), EventTypeMessageCreated, TargetTypeChannel, chID, msgID, body)

	t.Logf("TestMessageServiceIntegration_CreateChannelMessage: msgID=%q — OK", msgID)
}

// TestMessageServiceIntegration_CreateDMMessage verifies the full
// service → store → publisher → hub → subscriber delivery path for a DM message:
//
//  1. CreateDMMessage persists via the stateful fake store.
//  2. store.listCreated() confirms the DM conversation ID was recorded.
//  3. Both DM participants receive the event; a non-member receives nothing.
func TestMessageServiceIntegration_CreateDMMessage(t *testing.T) {
	const (
		wsID  = "ws-integ-dm"
		dmID  = "dm-integ"
		alice = "user-integ-alice"
		bob   = "user-integ-bob"
		eve   = "user-integ-eve" // non-member
		msgID = "msg-integ-dm-001"
		body  = "hello DM integration"
	)

	dm := domain.DMConversation{
		ID:          dmID,
		WorkspaceID: wsID,
		Status:      domain.DMConversationStatusActive,
	}
	dms := &integDMStore{conv: dm}
	messages := &integMessageStore{seed: domain.Message{ID: msgID}}

	auth := &fakeAuthorizer{}
	auth.setAccess(alice, wsID, TargetTypeDM, dmID, true)
	auth.setAccess(bob, wsID, TargetTypeDM, dmID, true)
	// eve has no access.

	hub := NewHub(auth, newTestLogger(), NopBus{}, "test-integ-dm")
	defer hub.Shutdown()

	aliceSub := newClient("integ-dm-alice", alice, wsID, &fakeSender{})
	bobSub := newClient("integ-dm-bob", bob, wsID, &fakeSender{})
	eveSub := newClient("integ-dm-eve", eve, wsID, &fakeSender{})

	for _, c := range []*Client{aliceSub, bobSub, eveSub} {
		registerInRunningHub(t, hub, c)
	}
	for _, c := range []*Client{aliceSub, bobSub} {
		if err := hub.Subscribe(context.Background(), c, TargetTypeDM, dmID); err != nil {
			t.Fatalf("subscribe %s: %v", c.id, err)
		}
	}
	// eve subscribe must be denied.
	if err := hub.Subscribe(context.Background(), eveSub, TargetTypeDM, dmID); err == nil {
		t.Fatal("non-member eve must not be allowed to subscribe to DM")
	}

	svc := service.NewMessageService(&integChannelStore{}, dms, messages)
	svc.SetPublisher(&hubPublisherAdapter{hub: hub})

	if _, err := svc.CreateDMMessage(context.Background(), service.CreateDMMessageInput{
		WorkspaceID:    wsID,
		ConversationID: dmID,
		SenderID:       alice,
		BodyText:       body,
	}); err != nil {
		t.Fatalf("CreateDMMessage: %v", err)
	}

	// Persistence gate.
	persisted := messages.listCreated()
	if len(persisted) != 1 {
		t.Fatalf("store.listCreated(): got %d records, want 1", len(persisted))
	}
	if persisted[0].DMConversationID != dmID {
		t.Errorf("persisted.DMConversationID: got %q want %q", persisted[0].DMConversationID, dmID)
	}

	// Delivery to alice and bob.
	for _, c := range []*Client{aliceSub, bobSub} {
		c := c
		eventually(t, func() bool { return len(c.outbox) == 1 }, 3*time.Second,
			c.id+" must receive 1 DM message.created event")
		assertEventPayload(t, drainOneEvent(t, c), EventTypeMessageCreated, TargetTypeDM, dmID, msgID, body)
	}

	// eve must receive nothing.
	if n := len(eveSub.outbox); n != 0 {
		t.Fatalf("non-member eve must receive 0 DM events, got %d", n)
	}

	t.Logf("TestMessageServiceIntegration_CreateDMMessage: msgID=%q — OK", msgID)
}
