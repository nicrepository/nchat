package app

import (
	"context"

	"github.com/nicrepository/nchat/libs/go/platform/notificationevent"
	"github.com/nicrepository/nchat/libs/go/platform/notificationpolicy"
	"github.com/nicrepository/nchat/libs/go/platform/workschedule"
	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/service"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
	"github.com/nicrepository/nchat/services/chat-service/internal/ws"
)

// The realtime half of the policy engine (issue #744).
//
// chat-service is the producer of the realtime event, so it is where the
// decision for that event is made — with the same package notification-service
// uses. Two consumers running one engine is not two policies; two
// implementations of the rules would be, and that is exactly what this replaces:
// the browser used to decide DM/mention classification and the chime rules for
// itself.
//
// Nothing here restates a rule. It maps the message onto the engine's input,
// asks once, and puts the answer on the wire.
//
// The decision is per recipient. The publisher encodes one payload for the whole
// target and cannot know who will receive it, so what it encodes is the decision
// for a recipient with no preferences of their own; the fan-out then re-asks for
// each subscriber it is about to deliver to, with that subscriber's own facts,
// and re-encodes only where the answer differs (ws.RecipientPolicy).
//
// The one per-recipient fact a client still needs for itself — whether it was
// named — travels as data the server derived, not as a rule the client re-runs.

// notificationPolicyFor returns the realtime decision for a message.
//
// It is never nil for a payload this build produces, and that is the whole of
// the rollout contract. A subscriber has to tell three states apart:
//
//	no object at all   a chat-service that predates issue #744 — it cannot
//	                   answer, and its silence is not a denial
//	deny, no reasons   this build, saying the message is not a notifiable event
//	                   at all: a system message, or a removed one. No outbox row
//	                   is written for either, so there is nothing to alert about
//	deny with reasons  this build, saying the policy suppressed it
//
// Returning nil for the second case would have made the first two identical on
// the wire, and a client that reads that absence as a denial goes silent
// against every older server it can still reach during a rolling deploy.
func notificationPolicyFor(
	msg domain.Message, removed bool, recipient recipientFacts,
) *ws.NotificationPolicyPayload {
	if removed || msg.Kind != domain.MessageKindUser {
		return notNotifiable(msg)
	}
	decision := notificationpolicy.Evaluate(realtimeContext(msg, recipient))
	named, everyone := service.NamedRecipients(msg.BodyText)
	inApp, sound, webPush := channelsOnTheWire(decision.Channels)
	return &ws.NotificationPolicyPayload{
		PolicyVersion: decision.PolicyVersion,
		InApp:         inApp,
		Sound:         sound,
		WebPush:       webPush,
		Reasons:       deniedReasons(decision),
		SoundClass:    soundClassFor(msg),
		NamedUserIDs:  named,
		NamesEveryone: everyone,
	}
}

// realtimeContext maps the message and one recipient onto the engine's input.
//
// Each field is either a fact this path actually holds or the state the contract
// defines for not holding it. None of them is a placeholder chosen to get a
// convenient answer:
//
//   - Origin is live: this is the publish of a message that has just been
//     committed. An import or a replay does not travel this path.
//   - WorkSchedule is NotConfigured, because it is — issue #743 shipped the
//     temporal domain and deliberately no writer.
//   - Presence is Connected, which is exactly what this transport knows: there
//     is an open client, because the event is going down its socket, and
//     whether that client is in front of the reader was never observed here.
//     Foreground would claim the second; Unknown would throw away the first and
//     withdraw the surfaces only a live client can execute.
//   - ConversationOpen stays false and is inert: denyConversationOpen requires
//     an observed focus, which Connected is precisely the absence of. The
//     browser applies "I am looking at this one" locally, and may only remove a
//     surface by doing so.
//   - RecipientID and Preferences.Muted are this recipient's own, resolved by
//     the fan-out from chat.conversation_notification_prefs — the same source of
//     truth the notification worker reads. Two members of one channel with
//     opposite preferences get opposite decisions.
//   - Preferences.SoundMode is unset because no server-side source of truth
//     exists for it: it lives in the browser (#136/#729), where it stays a local
//     execution preference that can only take a permitted chime away.
//   - WebPushAvailable is false. This is the in-app path; push is decided by
//     notification-service against the outbox, and claiming it here would put a
//     channel in a plan nothing on this path can deliver.
func realtimeContext(msg domain.Message, recipient recipientFacts) notificationpolicy.Context {
	return notificationpolicy.Context{
		EventID:      msg.ID,
		WorkspaceID:  msg.WorkspaceID,
		RecipientID:  recipient.id,
		EventType:    eventTypeFor(msg),
		Priority:     notificationevent.PriorityNormal,
		Origin:       notificationevent.OriginLive,
		Conversation: conversationKindFor(msg),
		WorkSchedule: workschedule.StateNotConfigured,
		Presence:     notificationpolicy.PresenceConnected,
		Preferences: notificationpolicy.Preferences{
			Status: recipient.status,
			Muted:  recipient.muted,
		},
	}
}

// recipientFacts is what the fan-out knows about the person a delivery is for.
//
// The zero value is the publisher's position — no recipient resolved yet — and
// is what the broadcast payload is encoded against. It is not a claim that
// nobody is muted; it is the decision for somebody who has expressed no
// preference, which is what such a recipient would be given anyway.
type recipientFacts struct {
	id    string
	muted bool
	// status is the engine's own answer to "were these readable at all". The
	// zero value is resolved, so only a caller whose read failed says otherwise.
	status notificationpolicy.PreferenceStatus
}

// eventTypeFor classifies the event from its target, which is the only
// classification that is the same for every recipient. Priority and the mention
// kind are per recipient and are not asserted here.
func eventTypeFor(msg domain.Message) notificationevent.EventType {
	if msg.ChannelID != "" {
		return notificationevent.EventTypeChannelMessage
	}
	return notificationevent.EventTypeDirectMessage
}

// conversationKindFor reports where the event happened. A group reads as
// ConversationDirect: chat.dm_conversations holds both, the member count is not
// in hand here, and the two are the same to every rule that reads the kind.
func conversationKindFor(msg domain.Message) notificationpolicy.ConversationKind {
	if msg.ChannelID != "" {
		return notificationpolicy.ConversationChannel
	}
	return notificationpolicy.ConversationDirect
}

// soundClassFor is the authoritative class every recipient of this event
// shares. A recipient the message names is mentioned on top of it, which the
// naming fields carry.
func soundClassFor(msg domain.Message) string {
	if msg.ChannelID != "" {
		return ws.SoundClassGeneral
	}
	return ws.SoundClassDirect
}

// notNotifiable is the decision for a message that produces no notification
// event: the same test chat-service's outbox applies before it writes a row.
//
// It carries no reasons, and that absence is what it means — there was no
// policy question to ask, so there is no policy answer to explain. It is not a
// rule this package decided; it is the fact that a system message and a deleted
// one have nothing to notify anybody about.
func notNotifiable(msg domain.Message) *ws.NotificationPolicyPayload {
	return &ws.NotificationPolicyPayload{
		PolicyVersion: notificationpolicy.Version,
		InApp:         ws.NotificationDeny,
		Sound:         ws.NotificationDeny,
		WebPush:       ws.NotificationDeny,
		SoundClass:    soundClassFor(msg),
	}
}

// channelsOnTheWire renders the delivery plan as the three independent
// decisions the wire carries.
//
// One expression per channel, each reading its own field of the plan and
// nothing else. That is the whole guarantee: no channel is derived from a
// neighbour, and none of them is decision.Eligible(), which is a summary of the
// plan and cannot authorise a particular surface. A client that received one
// channel's answer under another channel's name would be back to deciding
// delivery for itself, one surface at a time.
func channelsOnTheWire(c notificationpolicy.Channels) (inApp, sound, webPush string) {
	return channelVerdict(c.InApp), channelVerdict(c.Sound), channelVerdict(c.WebPush)
}

// channelVerdict renders one channel's decision. One function for every
// channel, so a projection cannot say "allow" for a channel the engine denied
// by copying its neighbour.
func channelVerdict(allowed bool) string {
	if allowed {
		return ws.NotificationAllow
	}
	return ws.NotificationDeny
}

// deniedReasons renders the reasons a suppression rests on.
//
// Only a suppression has any: a decision that still allows a surface was not
// suppressed, it was routed, and shipping an explanation of the surfaces it
// skipped on every message would be payload nobody reads.
func deniedReasons(decision notificationpolicy.Decision) []string {
	if !decision.Suppressed() {
		return nil
	}
	reasons := make([]string, len(decision.Reasons))
	for i, reason := range decision.Reasons {
		reasons[i] = string(reason)
	}
	return reasons
}

// recipientPolicy is the ws.RecipientPolicy the hub personalises deliveries
// with: the mute read on one side, the engine call on the other.
//
// It holds a store and no rules. Nothing in this type decides what a mute means
// — it reports the state and hands it to the same Evaluate every other consumer
// calls.
type recipientPolicy struct {
	prefs storage.NotificationPrefStore
}

// MutedUsers reports which of the given recipients silenced this target.
func (p recipientPolicy) MutedUsers(
	ctx context.Context, workspaceID string, targetType ws.TargetType, targetID string, userIDs []string,
) ([]string, error) {
	kind, ok := prefTargetKind(targetType)
	if !ok {
		// A target kind this preference table does not describe. Reporting
		// nobody muted is the honest answer, not a failure: there is no
		// preference for a target that cannot carry one.
		return nil, nil
	}
	return p.prefs.FilterMutedUsers(ctx, workspaceID, kind, targetID, userIDs)
}

// PolicyFor is the central decision for one recipient, from the same engine and
// the same mapping the published payload used.
func (p recipientPolicy) PolicyFor(
	payload ws.MessagePayload, recipientID string, preference ws.RecipientPreference,
) *ws.NotificationPolicyPayload {
	return notificationPolicyFor(
		wsPayloadToDomainMessage(payload),
		payload.IsRemoved,
		recipientFactsFrom(recipientID, preference),
	)
}

// recipientFactsFrom translates what the fan-out established into the engine's
// own vocabulary.
//
// The unavailable case is passed through as a status rather than collapsed into
// muted: the engine has a rule for "these preferences could not be read", and
// borrowing the mute rule for it would record a choice the recipient never made.
func recipientFactsFrom(recipientID string, preference ws.RecipientPreference) recipientFacts {
	facts := recipientFacts{id: recipientID}
	switch preference {
	case ws.RecipientPreferenceMuted:
		facts.muted = true
	case ws.RecipientPreferenceUnavailable:
		facts.status = notificationpolicy.PreferenceStatusUnavailable
	case ws.RecipientPreferenceNone:
	}
	return facts
}

// prefTargetKind maps a subscription target onto the preference table's own
// vocabulary. Declared here rather than shared, so neither package depends on
// the other's strings.
func prefTargetKind(targetType ws.TargetType) (string, bool) {
	switch targetType {
	case ws.TargetTypeChannel:
		return storage.NotificationPrefTargetChannel, true
	case ws.TargetTypeDM:
		return storage.NotificationPrefTargetDM, true
	default:
		return "", false
	}
}

// wsPayloadToDomainMessage recovers the few message facts the policy reads from
// the payload the publisher already built.
//
// Only the fields the engine's inputs are derived from — the target, the kind,
// the identity — so this is a projection back onto the same facts rather than a
// second source for them. The body is carried because the naming codec reads it,
// and it is the same body the payload already holds.
func wsPayloadToDomainMessage(payload ws.MessagePayload) domain.Message {
	return domain.Message{
		ID:               payload.ID,
		WorkspaceID:      payload.WorkspaceID,
		ChannelID:        payload.ChannelID,
		DMConversationID: payload.DMConversationID,
		SenderID:         payload.SenderID,
		Kind:             domain.MessageKind(payload.Kind),
		BodyText:         payload.BodyText,
	}
}

// withRecipientPolicyOption adds the per-recipient decision to the hub's
// options when there is a preference store to resolve it from (issue #744).
//
// A function rather than a conditional at the call site: app.New is already the
// longest function in this package and every branch added there is one more
// path through the one place a deployment is assembled.
//
// Without a store the option is omitted, and the hub delivers the published
// decision to everyone — the behaviour of a deployment with no database, whose
// message routes answer 503 long before this matters.
func withRecipientPolicyOption(
	options []ws.HubOption, prefs storage.NotificationPrefStore,
) []ws.HubOption {
	if prefs == nil {
		return options
	}
	return append(options, ws.WithRecipientPolicy(recipientPolicy{prefs: prefs}))
}
