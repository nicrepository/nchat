package ws

import (
	"context"
	"encoding/json"
	"slices"
)

// Per-recipient delivery decisions at the fan-out (issue #744, review round 6).
//
// # Why the decision cannot be made where the payload is built
//
// One message.created is encoded once and delivered to every subscriber of its
// target, so the payload built by the publisher addresses many people at once.
// A delivery decision does not: mute belongs to one person, and two members of
// the same channel can hold opposite preferences about it. A single decision
// reused for both is not a decision about either.
//
// The recipient *is* known, just later — handleBroadcast already walks the
// subscriptions one at a time and already re-authorises each of them against the
// database. That loop is where a recipient exists, so that is where the policy
// is evaluated.
//
// # What this costs
//
// One extra query per broadcast, not per subscriber: the muted subset of the
// whole subscriber list is read in a single statement before the loop. Against a
// loop that already performs one authorization query per subscriber, that is
// strictly less than what the path already does.
//
// Re-encoding is avoided in the same spirit. Almost every recipient gets the
// decision the publisher already encoded, so their bytes are the shared bytes;
// only a recipient whose decision actually differs is encoded again.

// RecipientPolicy personalises a message event's delivery decision.
//
// Two methods because they are two different kinds of fact: the first reads
// persisted state and can fail, the second is the pure engine call that turns
// that state into a plan. Keeping them apart is what lets the fan-out batch the
// first and still ask the second per recipient.
type RecipientPolicy interface {
	// MutedUsers returns which of userIDs have silenced this target. The caller
	// has already authorised every user it passes in.
	MutedUsers(
		ctx context.Context, workspaceID string, targetType TargetType, targetID string, userIDs []string,
	) ([]string, error)

	// PolicyFor is the central decision for one recipient of one message. It is
	// pure: every fact it needs is an argument.
	PolicyFor(
		payload MessagePayload, recipientID string, preference RecipientPreference,
	) *NotificationPolicyPayload
}

// RecipientPreference is what the fan-out managed to establish about one
// recipient before asking for a decision.
//
// Three states and not a boolean, because "this recipient has expressed no
// preference" and "nobody could find out what this recipient wants" are
// different facts with different safe answers, and a bool has room for only one
// of them. The engine draws the same distinction — see
// notificationpolicy.PreferenceStatus — and this type is what carries it across
// the package boundary.
type RecipientPreference int

const (
	// RecipientPreferenceNone is a completed read that found nothing expressed,
	// which is how this product records "not muted".
	RecipientPreferenceNone RecipientPreference = iota
	// RecipientPreferenceMuted is a completed read that found this conversation
	// silenced.
	RecipientPreferenceMuted
	// RecipientPreferenceUnavailable is a read that did not succeed. Alerts are
	// decided fail-closed for it; the message is not affected.
	RecipientPreferenceUnavailable
)

// WithRecipientPolicy makes message.created decisions per recipient.
//
// Without it the hub delivers the publisher's own encoding to everyone, which is
// the behaviour of a build that has no policy to personalise.
func WithRecipientPolicy(policy RecipientPolicy) HubOption {
	return func(h *Hub) { h.recipientPolicy = policy }
}

// recipientEncodings resolves, once per broadcast, what each recipient should be
// sent — and encodes only the variants that are actually needed.
type recipientEncodings struct {
	base   []byte
	event  Event
	policy RecipientPolicy
	muted  map[string]struct{}
	// unavailable marks a broadcast whose preference read failed. It applies to
	// every recipient of that broadcast, because the read is one statement for
	// the whole subscriber list: if it did not answer, it did not answer for
	// anybody.
	unavailable bool
	encoded     map[RecipientPreference][]byte
}

// newRecipientEncodings reads the muted subset for this broadcast.
//
// # What a failed read must not do
//
// It must not fall back to the published decision. That decision is the one for
// a recipient who expressed no preference, and handing it to everybody after a
// failed read is precisely the assumption there is no evidence for: a recipient
// who had silenced this conversation is alerted anyway, by a fault, and cannot
// tell it from the product ignoring them.
//
// # What it does instead
//
// The broadcast is marked unavailable and every recipient is decided
// fail-closed — by the engine, from a Context that says its preferences could
// not be read, not by a branch here. Nothing about the message changes: the same
// payload, ids, conversation, workspace and origin are delivered, and only the
// alert channels of the decision differ. A preferences outage costs
// notifications, never messages.
func (h *Hub) newRecipientEncodings(
	ctx context.Context, req broadcastReq, recipients []string,
) *recipientEncodings {
	if h.recipientPolicy == nil || req.event.Payload == nil || len(recipients) == 0 {
		return nil
	}
	encodings := &recipientEncodings{
		base:    req.data,
		event:   req.event,
		policy:  h.recipientPolicy,
		muted:   map[string]struct{}{},
		encoded: map[RecipientPreference][]byte{},
	}
	muted, err := h.recipientPolicy.MutedUsers(
		ctx, req.event.WorkspaceID, req.event.TargetType, req.event.TargetID, recipients,
	)
	if err != nil {
		h.logger.WarnContext(ctx,
			"ws: recipient preferences unavailable; alert surfaces are fail-closed for this broadcast",
			"target_type", string(req.event.TargetType),
			"error", err,
		)
		encodings.unavailable = true
		return encodings
	}
	for _, userID := range muted {
		encodings.muted[userID] = struct{}{}
	}
	return encodings
}

// preferenceFor is what the fan-out established about one recipient.
func (e *recipientEncodings) preferenceFor(userID string) RecipientPreference {
	if e.unavailable {
		return RecipientPreferenceUnavailable
	}
	if _, muted := e.muted[userID]; muted {
		return RecipientPreferenceMuted
	}
	return RecipientPreferenceNone
}

// bytesFor returns what this recipient is sent: their own encoding when their
// decision differs, and the published bytes otherwise.
//
// Nil-safe on the receiver, so the delivery loop reads the same whether or not
// this build personalises anything.
func (e *recipientEncodings) bytesFor(userID string, published []byte) []byte {
	if personalised := e.forRecipient(userID); personalised != nil {
		return personalised
	}
	return published
}

// forRecipient returns this recipient's own encoding, or nil when there is
// nothing to personalise with.
func (e *recipientEncodings) forRecipient(userID string) []byte {
	if e == nil {
		return nil
	}
	preference := e.preferenceFor(userID)
	if encoded, ok := e.encoded[preference]; ok {
		return encoded
	}
	encoded := e.encode(userID, preference)
	e.encoded[preference] = encoded
	return encoded
}

// encode renders one recipient's event, reusing the published bytes when the
// decision it carries is already the right one.
//
// The comparison is against the decision, never against the recipient: two
// recipients whose facts produce the same plan get the same bytes because the
// plan is the same, not because anything assumed they were alike.
func (e *recipientEncodings) encode(userID string, preference RecipientPreference) []byte {
	payload := *e.event.Payload
	decision := e.policy.PolicyFor(payload, userID, preference)
	if samePolicy(decision, payload.NotificationPolicy) {
		return e.base
	}
	payload.NotificationPolicy = decision
	event := e.event
	event.Payload = &payload
	data, err := json.Marshal(event)
	if err != nil {
		// Unreachable for a payload that already encoded once; falling back to
		// the published bytes keeps the message deliverable either way.
		return e.base
	}
	return data
}

// samePolicy reports whether two decisions are the same plan.
func samePolicy(a, b *NotificationPolicyPayload) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.PolicyVersion == b.PolicyVersion &&
		a.InApp == b.InApp && a.Sound == b.Sound && a.WebPush == b.WebPush &&
		a.SoundClass == b.SoundClass && a.NamesEveryone == b.NamesEveryone &&
		slices.Equal(a.Reasons, b.Reasons) &&
		slices.Equal(a.NamedUserIDs, b.NamedUserIDs)
}

// recipientEncodingsFor resolves the per-recipient encodings for one broadcast,
// on a context of the hub's own: the decision belongs to the broadcast, not to
// any one subscriber's connection.
func (h *Hub) recipientEncodingsFor(
	req broadcastReq, subscriptions []broadcastSubscription,
) *recipientEncodings {
	if h.recipientPolicy == nil || req.event.Payload == nil {
		return nil
	}
	// One entry per person, in the order they were first seen: a set answers
	// "have we got this one already" in constant time, and the slice keeps the
	// order the preference read and the decisions are made in. Scanning the
	// slice for membership instead made the whole loop quadratic in the number
	// of subscribers, on the broadcast path.
	recipients := make([]string, 0, len(subscriptions))
	seen := make(map[string]struct{}, len(subscriptions))
	for _, subscription := range subscriptions {
		userID := subscription.client.userID
		if userID == "" {
			continue
		}
		if _, already := seen[userID]; already {
			continue
		}
		seen[userID] = struct{}{}
		recipients = append(recipients, userID)
	}
	ctx, cancel := context.WithTimeout(context.Background(), broadcastAuthTimeout)
	defer cancel()
	return h.newRecipientEncodings(ctx, req, recipients)
}
