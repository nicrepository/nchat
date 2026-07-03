package ws

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

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
		MessageID: "msg-1", TargetType: TargetTypeChannel, TargetID: "ch-1",
		Reactions: []ReactionPayload{{Emoji: "👍", Count: 2}},
	}}
	authorizer := &fakeAuthorizer{}
	authorizer.setAccess("user-auth", "ws-auth", TargetTypeChannel, "ch-1", true)
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

	if err := hub.handleClientMessage(context.Background(), c, ClientMessage{
		Type: ClientMessageTypeReactionToggle, MessageID: "msg-1", Emoji: "👍",
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
		if evt.Type != EventTypeReactionUpdated || evt.Reaction == nil || evt.Reaction.MessageID != "msg-1" {
			t.Fatalf("unexpected event: %+v", evt)
		}
	case <-time.After(time.Second):
		t.Fatal("reaction event not broadcast")
	}
}

func TestHub_ReactionToggleStopsWhenRateLimited(t *testing.T) {
	handler := &fakeReactionHandler{}
	hub := NewHub(&fakeAuthorizer{}, newTestLogger(), NopBus{}, "test-reaction-limit",
		WithReactionHandler(handler), WithReactionLimiter(fakeReactionLimiter{allowed: false}))
	t.Cleanup(hub.Shutdown)
	c := newClient("client-1", "user-auth", "ws-auth", &fakeSender{})

	err := hub.handleClientMessage(context.Background(), c, ClientMessage{
		Type: ClientMessageTypeReactionToggle, MessageID: "msg-1", Emoji: "👍",
	})
	if !errors.Is(err, ErrReactionRateLimited) {
		t.Fatalf("expected ErrReactionRateLimited, got %v", err)
	}
	if handler.messageID != "" {
		t.Fatal("rate-limited toggle reached storage")
	}
}

func decodeEvent(raw []byte) (Event, error) {
	var evt Event
	err := json.Unmarshal(raw, &evt)
	return evt, err
}
