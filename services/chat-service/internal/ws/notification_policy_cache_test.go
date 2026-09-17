package ws

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nicrepository/nchat/libs/go/platform/notificationevent"
	"github.com/nicrepository/nchat/libs/go/platform/notificationpolicy"
	"github.com/nicrepository/nchat/libs/go/platform/workschedule"
	"github.com/nicrepository/nchat/services/chat-service/internal/service"
)

// The encoding cache, against the real policy engine (issue #136).
//
// The suite beside this one drives the fan-out with a fake that answers from a
// fixed set, which is the right tool for asserting routing. It cannot catch
// what this file is about: with a conversation level the decision depends on the
// recipient's own classification of the event, so a cache keyed by anything less
// than the decision hands one recipient another's plan — and which one wins
// depends on the order the fan-out walked the subscriptions.
//
// So these tests use the engine itself, and the mention codec the server uses to
// decide who a message names. Nothing here restates a rule: the plan comes from
// notificationpolicy.Evaluate and the naming from service.NamedRecipients.

const (
	// The codec's own canonical form, so the classification under test is the
	// real one and not a shape invented here.
	cacheMentionedUser = "aaaaaaaa-1111-4111-8111-111111111111"
	cacheReplyAuthor   = "bbbbbbbb-2222-4222-8222-222222222222"
	cacheBystander     = "cccccccc-3333-4333-8333-333333333333"
)

// enginePolicy is the production mapping, narrowed to what this event carries:
// it resolves the per-recipient classification and then asks the real engine.
//
// It is the same shape app.recipientPolicy has — the adapter whose own
// equivalence with the outbox is proved in that package — and it exists here
// because ws cannot import app.
type enginePolicy struct {
	// preferences is what the fan-out established, by recipient.
	preferences map[string]RecipientPreference
	// calls counts Evaluate calls, so a test can state that the engine is asked
	// per recipient rather than once per cache key.
	calls int
}

func (p *enginePolicy) RecipientPreferences(
	_ context.Context, _ string, _ TargetType, _ string, userIDs []string,
) (map[string]RecipientPreference, error) {
	out := map[string]RecipientPreference{}
	for _, id := range userIDs {
		if preference, ok := p.preferences[id]; ok {
			out[id] = preference
		}
	}
	return out, nil
}

func (p *enginePolicy) PolicyFor(
	payload MessagePayload, recipientID string, preference RecipientPreference,
) *NotificationPolicyPayload {
	p.calls++
	named, everyone := service.NamedRecipients(payload.BodyText)
	decision := notificationpolicy.Evaluate(notificationpolicy.Context{
		EventID:      payload.ID,
		WorkspaceID:  payload.WorkspaceID,
		RecipientID:  recipientID,
		EventType:    cacheEventType(payload, recipientID, named, everyone),
		Origin:       notificationevent.OriginLive,
		Conversation: notificationpolicy.ConversationChannel,
		// The real current state, exactly as the production adapter passes it:
		// issue #743 shipped the temporal domain and no writer, and the zero
		// value means "outside working hours" — which would deny every channel
		// and make this suite prove nothing.
		WorkSchedule: workschedule.StateNotConfigured,
		Presence:     notificationpolicy.PresenceConnected,
		Preferences:  cachePreferences(preference),
	})
	verdict := func(allowed bool) string {
		if allowed {
			return NotificationAllow
		}
		return NotificationDeny
	}
	reasons := make([]string, 0, len(decision.Reasons))
	if decision.Suppressed() {
		for _, reason := range decision.Reasons {
			reasons = append(reasons, string(reason))
		}
	}
	if len(reasons) == 0 {
		reasons = nil
	}
	return &NotificationPolicyPayload{
		PolicyVersion: decision.PolicyVersion,
		InApp:         verdict(decision.Channels.InApp),
		Sound:         verdict(decision.Channels.Sound),
		WebPush:       verdict(decision.Channels.WebPush),
		Reasons:       reasons,
		SoundClass:    SoundClassGeneral,
		NamedUserIDs:  named,
		NamesEveryone: everyone,
	}
}

func cacheEventType(
	payload MessagePayload, recipientID string, named []string, everyone bool,
) notificationevent.EventType {
	if recipientID == "" {
		return notificationevent.EventTypeChannelMessage
	}
	for _, id := range named {
		if id == recipientID {
			return notificationevent.EventTypeMention
		}
	}
	if everyone && payload.DMConversationID != "" {
		return notificationevent.EventTypeMention
	}
	// The canonical reply fact, from the persisted parent — not the visual quote.
	if payload.ReplyToSenderID != "" && payload.ReplyToSenderID == recipientID {
		return notificationevent.EventTypeReply
	}
	return notificationevent.EventTypeChannelMessage
}

func cachePreferences(preference RecipientPreference) notificationpolicy.Preferences {
	switch preference {
	case RecipientPreferenceMuted:
		return notificationpolicy.Preferences{Muted: true}
	case RecipientPreferenceMentionsReplies:
		return notificationpolicy.Preferences{
			ConversationLevel: notificationpolicy.ConversationLevelMentionsReplies,
		}
	case RecipientPreferenceUnavailable:
		return notificationpolicy.Preferences{Status: notificationpolicy.PreferenceStatusUnavailable}
	case RecipientPreferenceNone:
	}
	return notificationpolicy.Preferences{}
}

// engineFixture is a broadcast whose published decision is the one for a
// recipient with no preference, exactly as the publisher builds it.
func engineFixture(t *testing.T, policy *enginePolicy, body, replyToSenderID string) (*Hub, broadcastReq) {
	t.Helper()
	hub := NewHub(NopAuthorizer{}, nil, NopBus{}, "test-instance", WithRecipientPolicy(policy))
	payload := MessagePayload{
		ID: "msg-1", WorkspaceID: "ws-1", ChannelID: "chan-1", Kind: "user",
		BodyText:        body,
		ReplyToSenderID: replyToSenderID,
	}
	payload.NotificationPolicy = policy.PolicyFor(payload, "", RecipientPreferenceNone)
	event := Event{
		SchemaVersion: CurrentEventSchemaVersion, Type: EventTypeMessageCreated,
		WorkspaceID: "ws-1", TargetType: TargetTypeChannel, TargetID: "chan-1",
		MessageID: payload.ID, Payload: &payload,
	}
	data, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return hub, broadcastReq{event: event, data: data}
}

// deliveredPlan is what one recipient's bytes actually tell their client.
func deliveredPlan(t *testing.T, encodings *recipientEncodings, userID string) NotificationPolicyPayload {
	t.Helper()
	bytes := encodings.bytesFor(userID, encodings.base)
	var decoded Event
	if err := json.Unmarshal(bytes, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.Payload == nil || decoded.Payload.NotificationPolicy == nil {
		t.Fatal("the delivered event carried no decision")
	}
	return *decoded.Payload.NotificationPolicy
}

// mentionBody names one user, in the codec's canonical form.
func mentionBody(userID string) string {
	return "@[Alguem](mention:user:" + userID + ") olha isso"
}

// The headline case, in both orders. Two recipients who expressed the *same*
// preference are owed opposite plans, because only one of them was named — so a
// cache that could reuse one's bytes for the other would make delivery depend on
// the order the fan-out happened to walk the subscriptions.
func TestNarrowedRecipientsWithDifferentClassificationsGetTheirOwnPlan(t *testing.T) {
	for _, order := range [][]string{
		{cacheMentionedUser, cacheBystander},
		{cacheBystander, cacheMentionedUser},
	} {
		t.Run(order[0], func(t *testing.T) {
			policy := &enginePolicy{preferences: map[string]RecipientPreference{
				cacheMentionedUser: RecipientPreferenceMentionsReplies,
				cacheBystander:     RecipientPreferenceMentionsReplies,
			}}
			hub, req := engineFixture(t, policy, mentionBody(cacheMentionedUser), "")

			encodings := hub.newRecipientEncodings(context.Background(), req, order)
			if encodings == nil {
				t.Fatal("no per-recipient encodings were resolved")
			}

			// Resolved in the order given, which is the whole point.
			plans := map[string]NotificationPolicyPayload{}
			for _, userID := range order {
				plans[userID] = deliveredPlan(t, encodings, userID)
			}

			mentioned := plans[cacheMentionedUser]
			if mentioned.InApp != NotificationAllow || mentioned.Sound != NotificationAllow {
				t.Fatalf("the named recipient lost a surface: %+v", mentioned)
			}
			if len(mentioned.Reasons) != 0 {
				t.Fatalf("the named recipient's delivery explained itself: %v", mentioned.Reasons)
			}
			bystander := plans[cacheBystander]
			if bystander.InApp != NotificationDeny || bystander.Sound != NotificationDeny {
				t.Fatalf("an ordinary message still interrupted: %+v", bystander)
			}
			if len(bystander.Reasons) != 1 ||
				bystander.Reasons[0] != string(notificationpolicy.ReasonConversationLevel) {
				t.Fatalf("reasons = %v, want exactly [%s]",
					bystander.Reasons, notificationpolicy.ReasonConversationLevel)
			}
		})
	}
}

// The same property for a reply, which is the other classification a level
// keeps — and the one that reads the canonical parent fact rather than the
// visual quote.
func TestNarrowedRecipientsAreClassifiedByTheCanonicalReplyFact(t *testing.T) {
	for _, order := range [][]string{
		{cacheReplyAuthor, cacheBystander},
		{cacheBystander, cacheReplyAuthor},
	} {
		t.Run(order[0], func(t *testing.T) {
			policy := &enginePolicy{preferences: map[string]RecipientPreference{
				cacheReplyAuthor: RecipientPreferenceMentionsReplies,
				cacheBystander:   RecipientPreferenceMentionsReplies,
			}}
			// No mention in the body and no visual quote on the payload: the
			// only thing that makes this a reply is the persisted parent's
			// author.
			hub, req := engineFixture(t, policy, "respondi acima", cacheReplyAuthor)

			encodings := hub.newRecipientEncodings(context.Background(), req, order)
			plans := map[string]NotificationPolicyPayload{}
			for _, userID := range order {
				plans[userID] = deliveredPlan(t, encodings, userID)
			}

			answered := plans[cacheReplyAuthor]
			if answered.InApp != NotificationAllow || answered.Sound != NotificationAllow {
				t.Fatalf("the answered recipient lost a surface: %+v", answered)
			}
			bystander := plans[cacheBystander]
			if bystander.InApp != NotificationDeny {
				t.Fatalf("a bystander of the reply still interrupted: %+v", bystander)
			}
		})
	}
}

// Reuse is still reuse: recipients owed the same plan share the bytes, whether
// they arrived at it from the same preference or from different ones. Asserted
// on pointer identity of the slice, because sharing the allocation is the
// saving the cache exists for.
func TestRecipientsOwedTheSamePlanShareTheirEncoding(t *testing.T) {
	const otherBystander = "dddddddd-4444-4444-8444-444444444444"
	policy := &enginePolicy{preferences: map[string]RecipientPreference{
		// Two narrowed bystanders — same preference, same classification.
		cacheBystander: RecipientPreferenceMentionsReplies,
		otherBystander: RecipientPreferenceMentionsReplies,
		// ...and one muted recipient, whose plan is a different suppression.
		cacheReplyAuthor: RecipientPreferenceMuted,
	}}
	hub, req := engineFixture(t, policy, mentionBody(cacheMentionedUser), "")

	encodings := hub.newRecipientEncodings(context.Background(), req,
		[]string{cacheBystander, otherBystander, cacheReplyAuthor})

	first := encodings.forRecipient(cacheBystander)
	second := encodings.forRecipient(otherBystander)
	if &first[0] != &second[0] {
		t.Fatal("two recipients owed the same plan were encoded twice")
	}
	muted := encodings.forRecipient(cacheReplyAuthor)
	if &muted[0] == &first[0] {
		t.Fatal("a muted recipient was handed the narrowed recipients' bytes")
	}
	// The distinct reasons are what make them distinct plans.
	if plan := deliveredPlan(t, encodings, cacheReplyAuthor); len(plan.Reasons) != 1 ||
		plan.Reasons[0] != string(notificationpolicy.ReasonMuted) {
		t.Fatalf("the muted recipient's reasons = %v, want [muted]", plan.Reasons)
	}
}

// A recipient whose plan is the published one is handed the published bytes,
// which is the case almost every recipient of every message falls into.
func TestUnnarrowedRecipientsKeepThePublishedBytes(t *testing.T) {
	policy := &enginePolicy{preferences: map[string]RecipientPreference{}}
	hub, req := engineFixture(t, policy, "bom dia", "")

	encodings := hub.newRecipientEncodings(context.Background(), req,
		[]string{cacheBystander, cacheMentionedUser})

	for _, userID := range []string{cacheBystander, cacheMentionedUser} {
		// The published allocation itself, not a copy of it: a recipient whose
		// plan is the published one costs no marshalling at all.
		if bytes := encodings.forRecipient(userID); &bytes[0] != &encodings.base[0] {
			t.Fatalf("%s was re-encoded for the decision the publisher already made", userID)
		}
	}
}

// The engine is asked once per recipient, not once per distinct plan: it is the
// only thing that can tell whether two recipients are owed the same plan, so
// asking it is what the cache is built on top of rather than around.
func TestTheEngineIsAskedForEveryRecipient(t *testing.T) {
	policy := &enginePolicy{preferences: map[string]RecipientPreference{
		cacheMentionedUser: RecipientPreferenceMentionsReplies,
		cacheBystander:     RecipientPreferenceMentionsReplies,
	}}
	hub, req := engineFixture(t, policy, mentionBody(cacheMentionedUser), "")
	// The fixture's own published decision is one call.
	before := policy.calls

	encodings := hub.newRecipientEncodings(context.Background(), req,
		[]string{cacheMentionedUser, cacheBystander})
	encodings.forRecipient(cacheMentionedUser)
	encodings.forRecipient(cacheBystander)

	if policy.calls-before != 2 {
		t.Fatalf("the engine was asked %d times for 2 recipients", policy.calls-before)
	}
}
