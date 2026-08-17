package ws

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
)

const testReactionMessageID = "11111111-1111-1111-1111-111111111111"

type fakeReactionHandler struct {
	workspaceID string
	userID      string
	messageID   string
	emoji       string
	result      ReactionUpdate
	err         error
}

func (f *fakeReactionHandler) ToggleReaction(_ context.Context, workspaceID, userID, messageID, emoji string) (ReactionUpdate, error) {
	f.workspaceID, f.userID, f.messageID, f.emoji = workspaceID, userID, messageID, emoji
	return f.result, f.err
}

type fakeReactionLimiter struct {
	allowed bool
	err     error
}

func (f fakeReactionLimiter) Allow(context.Context, string) (bool, error) { return f.allowed, f.err }

func TestHub_ReactionToggleUsesServerIdentityAndBroadcastsAggregate(t *testing.T) {
	handler := &fakeReactionHandler{result: ReactionUpdate{
		MessageID: testReactionMessageID, TargetType: TargetTypeChannel, TargetID: "ch-1",
		Reactions: []ReactionPayload{{Emoji: "👍", Count: 2}},
	}}
	authorizer := &fakeAuthorizer{}
	authorizer.setAccess("user-auth", "ws-auth", TargetTypeChannel, "ch-1", true)
	authorizer.setAccess("user-other", "ws-auth", TargetTypeChannel, "ch-1", true)
	hub := NewHub(authorizer, newTestLogger(), NopBus{}, "test-reaction",
		WithReactionHandler(handler), WithReactionLimiter(fakeReactionLimiter{allowed: true}))
	t.Cleanup(hub.Shutdown)
	c := newClient("client-1", "user-auth", "ws-auth", &fakeSender{})
	if !hub.Register(c) {
		t.Fatal("register")
	}
	if err := hub.Subscribe(context.Background(), c, TargetTypeChannel, "ch-1"); err != nil {
		t.Fatal(err)
	}
	other := newClient("client-2", "user-other", "ws-auth", &fakeSender{})
	if !hub.Register(other) {
		t.Fatal("register second client")
	}
	if err := hub.Subscribe(context.Background(), other, TargetTypeChannel, "ch-1"); err != nil {
		t.Fatal(err)
	}

	if err := hub.handleClientMessage(context.Background(), c, ClientMessage{
		Type: ClientMessageTypeReactionToggle, MessageID: testReactionMessageID, Emoji: "👍",
	}); err != nil {
		t.Fatalf("reaction toggle: %v", err)
	}
	if handler.userID != "user-auth" || handler.workspaceID != "ws-auth" {
		t.Fatalf("client identity not used: %+v", handler)
	}
	select {
	case raw := <-c.outbox:
		evt, err := decodeEvent(raw)
		if err != nil {
			t.Fatal(err)
		}
		if evt.Type != EventTypeReactionUpdated || evt.Reaction == nil || evt.Reaction.MessageID != testReactionMessageID {
			t.Fatalf("unexpected event: %+v", evt)
		}
	case <-time.After(time.Second):
		t.Fatal("reaction event not broadcast")
	}
	select {
	case raw := <-other.outbox:
		evt, err := decodeEvent(raw)
		if err != nil || evt.Reaction == nil || evt.Reaction.MessageID != testReactionMessageID {
			t.Fatalf("unexpected remote event: event=%+v err=%v", evt, err)
		}
	case <-time.After(time.Second):
		t.Fatal("reaction event not broadcast to second user")
	}
}

func TestHub_ReactionToggleStopsWhenRateLimited(t *testing.T) {
	handler := &fakeReactionHandler{}
	hub := NewHub(&fakeAuthorizer{}, newTestLogger(), NopBus{}, "test-reaction-limit",
		WithReactionHandler(handler), WithReactionLimiter(fakeReactionLimiter{allowed: false}))
	t.Cleanup(hub.Shutdown)
	c := newClient("client-1", "user-auth", "ws-auth", &fakeSender{})

	err := hub.handleClientMessage(context.Background(), c, ClientMessage{
		Type: ClientMessageTypeReactionToggle, MessageID: testReactionMessageID, Emoji: "👍",
	})
	if !errors.Is(err, ErrReactionRateLimited) {
		t.Fatalf("expected ErrReactionRateLimited, got %v", err)
	}
	if handler.messageID != "" {
		t.Fatal("rate-limited toggle reached storage")
	}
}

func TestHub_ReactionLimitDoesNotBroadcastRejectedToggle(t *testing.T) {
	handler := &fakeReactionHandler{err: domain.ErrReactionLimitReached}
	hub := NewHub(&fakeAuthorizer{}, newTestLogger(), NopBus{}, "test-reaction-limit-rejected",
		WithReactionHandler(handler), WithReactionLimiter(fakeReactionLimiter{allowed: true}))
	t.Cleanup(hub.Shutdown)
	client := newClient("client-1", "user-auth", "ws-auth", &fakeSender{})

	err := hub.handleClientMessage(context.Background(), client, ClientMessage{
		Type: ClientMessageTypeReactionToggle, MessageID: testReactionMessageID, Emoji: "👍",
	})
	if !errors.Is(err, domain.ErrReactionLimitReached) {
		t.Fatalf("expected reaction limit error, got %v", err)
	}
	select {
	case event := <-client.outbox:
		t.Fatalf("rejected reaction broadcast %s", event)
	default:
	}
}

func TestHub_ReactionTogglePropagatesDependencyErrors(t *testing.T) {
	want := errors.New("dependency failed")
	for _, tt := range []struct {
		name    string
		limiter fakeReactionLimiter
		handler *fakeReactionHandler
	}{
		{name: "limiter", limiter: fakeReactionLimiter{err: want}, handler: &fakeReactionHandler{}},
		{name: "handler", limiter: fakeReactionLimiter{allowed: true}, handler: &fakeReactionHandler{err: want}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			hub := NewHub(&fakeAuthorizer{}, newTestLogger(), NopBus{}, "test-reaction-error",
				WithReactionHandler(tt.handler), WithReactionLimiter(tt.limiter))
			t.Cleanup(hub.Shutdown)
			client := newClient("client-1", "user-auth", "ws-auth", &fakeSender{})

			err := hub.handleClientMessage(t.Context(), client, ClientMessage{
				Type: ClientMessageTypeReactionToggle, MessageID: testReactionMessageID, Emoji: "👍",
			})
			if !errors.Is(err, want) {
				t.Fatalf("expected %v, got %v", want, err)
			}
		})
	}
}

func TestHub_ReactionToggleFeatureDisabledPrecedesPayloadValidation(t *testing.T) {
	hub := NewHub(&fakeAuthorizer{}, newTestLogger(), NopBus{}, "test-reaction-disabled")
	t.Cleanup(hub.Shutdown)
	c := newClient("client-1", "user-auth", "ws-auth", &fakeSender{})

	err := hub.handleClientMessage(context.Background(), c, ClientMessage{
		Type: ClientMessageTypeReactionToggle, MessageID: "not-a-uuid",
	})
	if !errors.Is(err, ErrReactionFeatureDisabled) {
		t.Fatalf("expected ErrReactionFeatureDisabled, got %v", err)
	}
}

func TestHub_ReactionToggleRejectsInvalidMessageUUIDBeforeHandler(t *testing.T) {
	handler := &fakeReactionHandler{}
	hub := NewHub(&fakeAuthorizer{}, newTestLogger(), NopBus{}, "test-reaction-invalid-uuid",
		WithReactionHandler(handler), WithReactionLimiter(fakeReactionLimiter{allowed: true}))
	t.Cleanup(hub.Shutdown)
	c := newClient("client-1", "user-auth", "ws-auth", &fakeSender{})

	err := hub.handleClientMessage(context.Background(), c, ClientMessage{
		Type: ClientMessageTypeReactionToggle, MessageID: "not-a-uuid", Emoji: "👍",
	})
	if err == nil {
		t.Fatal("expected invalid message_id format error")
	}
	if handler.messageID != "" {
		t.Fatal("invalid UUID reached reaction handler")
	}
}

func TestValidateReactionToggle_ReportsInvalidField(t *testing.T) {
	tests := []struct {
		name string
		msg  ClientMessage
		want string
	}{
		{name: "message id required", msg: ClientMessage{Emoji: "👍"}, want: "ws: reaction toggle: message_id required"},
		{name: "emoji required", msg: ClientMessage{MessageID: testReactionMessageID}, want: "ws: reaction toggle: emoji required"},
		{name: "unexpected target type", msg: ClientMessage{MessageID: testReactionMessageID, Emoji: "👍", TargetType: TargetTypeChannel}, want: "ws: reaction toggle: unexpected target fields"},
		{name: "unexpected target id", msg: ClientMessage{MessageID: testReactionMessageID, Emoji: "👍", TargetID: "channel-id"}, want: "ws: reaction toggle: unexpected target fields"},
		{name: "invalid message id", msg: ClientMessage{MessageID: "not-a-uuid", Emoji: "👍"}, want: "ws: reaction toggle: invalid message_id format"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := validateReactionToggle(tt.msg); err == nil || err.Error() != tt.want {
				t.Fatalf("expected %q, got %v", tt.want, err)
			}
		})
	}
}

func decodeEvent(raw []byte) (Event, error) {
	var evt Event
	err := json.Unmarshal(raw, &evt)
	return evt, err
}
