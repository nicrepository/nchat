package ws

import (
	"encoding/json"
	"testing"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
)

func TestDecodeClientMessage_RejectsUnknownFields(t *testing.T) {
	_, err := decodeClientMessage([]byte(`{"type":"reaction.toggle","message_id":"m1","emoji":"👍","user_id":"attacker"}`))
	if err == nil {
		t.Fatal("expected unknown user_id field to be rejected")
	}
}

func TestDecodeClientMessage_SubscribeRejectsClientControlledIdentityAndAliasIDs(t *testing.T) {
	for _, payload := range []string{
		`{"type":"subscribe","target_type":"channel","target_id":"ch-1","user_id":"attacker"}`,
		`{"type":"subscribe","target_type":"channel","target_id":"ch-1","workspace_id":"other"}`,
		`{"type":"subscribe","target_type":"channel","target_id":"ch-1","room_id":"other"}`,
		`{"type":"subscribe","target_type":"channel","target_id":"ch-1","channel_id":"other"}`,
		`{"type":"subscribe","target_type":"dm","target_id":"dm-1","conversation_id":"other"}`,
	} {
		if _, err := decodeClientMessage([]byte(payload)); err == nil {
			t.Fatalf("client-controlled identity or alias ID must be rejected: %s", payload)
		}
	}
}

func TestHandleReactionClientError_RateLimitIsStructuredAndHandled(t *testing.T) {
	c := newClient("client-1", "user-1", "workspace-1", &fakeSender{})
	if !handleReactionClientError(c, ErrReactionRateLimited) {
		t.Fatal("expected rate limit to be handled separately")
	}

	var got struct {
		Type       string `json:"type"`
		Code       string `json:"code"`
		RetryAfter int    `json:"retry_after"`
	}
	if err := json.Unmarshal(<-c.outbox, &got); err != nil {
		t.Fatal(err)
	}
	if got.Type != "error" || got.Code != "rate_limited" || got.RetryAfter != 60 {
		t.Fatalf("unexpected response: %+v", got)
	}
}

func TestHandleReactionClientError_FeatureDisabledUsesNeutralResponse(t *testing.T) {
	c := newClient("client-1", "user-1", "workspace-1", &fakeSender{})
	if !handleReactionClientError(c, ErrReactionFeatureDisabled) {
		t.Fatal("expected disabled feature to be handled separately")
	}

	var got map[string]any
	if err := json.Unmarshal(<-c.outbox, &got); err != nil {
		t.Fatal(err)
	}
	if got["type"] != "error" || got["code"] != "temporarily_unavailable" {
		t.Fatalf("unexpected response: %+v", got)
	}
}

func TestHandleReactionClientError_ReactionLimitIsStructuredAndHandled(t *testing.T) {
	c := newClient("client-1", "user-1", "workspace-1", &fakeSender{})
	if !handleReactionClientError(c, domain.ErrReactionLimitReached) {
		t.Fatal("expected reaction limit to be handled separately")
	}

	var got struct {
		Type  string `json:"type"`
		Code  string `json:"code"`
		Limit int    `json:"limit"`
	}
	if err := json.Unmarshal(<-c.outbox, &got); err != nil {
		t.Fatal(err)
	}
	if got.Type != "error" || got.Code != "reaction_limit_reached" || got.Limit != 5 {
		t.Fatalf("unexpected response: %+v", got)
	}
}

func TestDecodeClientMessage_AcceptsStrictReactionToggle(t *testing.T) {
	msg, err := decodeClientMessage([]byte(`{"type":"reaction.toggle","message_id":"m1","emoji":"👍"}`))
	if err != nil {
		t.Fatalf("decode reaction: %v", err)
	}
	if msg.Type != ClientMessageTypeReactionToggle || msg.MessageID != "m1" || msg.Emoji != "👍" {
		t.Fatalf("unexpected message: %+v", msg)
	}
}
