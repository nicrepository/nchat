package worker

import (
	"context"

	"github.com/nicrepository/nchat/libs/go/platform/notificationevent"
	"github.com/nicrepository/nchat/libs/go/platform/notificationpolicy"
	"github.com/nicrepository/nchat/libs/go/platform/workschedule"
)

// The policy adapter (issue #744).
//
// This file is the whole of what the worker knows about notification policy: it
// turns an outbox row into the vocabulary libs/go/platform/notificationpolicy
// speaks, asks it once, and turns the answer back into the Verdict this package
// already had. No rule is restated here, and none may be — a condition written
// in this file would be a second authority, which is the thing issue #744
// exists to end.
//
// The dependency runs one way only: worker imports notificationpolicy, never
// the reverse. The engine is a pure function of its argument and knows nothing
// about outboxes, claims, leases or delivery.

// NewPolicyEvaluator returns the notification policy as the worker's Evaluator.
//
// It is what NewNotificationWorker uses when a caller supplies no Evaluator of
// its own, and what the app wiring passes explicitly. There is deliberately no
// permissive alternative: a worker that decided everything was deliverable
// because nobody handed it a policy would be exactly the parallel authority
// this adapter replaces.
func NewPolicyEvaluator() Evaluator {
	return EvaluatorFunc(func(_ context.Context, notification Notification) (Verdict, error) {
		return policyVerdict(notificationpolicy.Evaluate(policyContext(notification))), nil
	})
}

// policyContext maps one outbox row onto the engine's input.
//
// Only what the row actually holds is filled in. Everything else is left at the
// state the contract already has for "not known here", and that is a statement
// rather than a gap:
//
//   - Presence is Unknown, because an outbox row carries no session and this
//     process has no registry of them. The engine's surface for an unknown
//     presence is push only, which is exactly what this worker delivers.
//   - WorkSchedule is NotConfigured, because it is: issue #743 shipped the
//     temporal domain and deliberately shipped no writer, so there is no
//     schedule to read. It is the real current state, not a default invented
//     here, and the engine's documented answer for it is that it does not
//     suppress.
//   - Preferences.Muted is the recipient's own mute preference, resolved from
//     chat.conversation_notification_prefs by the outbox projection that read
//     this row — one statement per batch, never one per event. The engine still
//     owns what a mute *does*: nothing in this package or in storage suppresses
//     anything, they only report the state. Absence of a preference row is
//     false, which is what that table already means by it.
//   - Preferences.Disabled and Preferences.SoundMode are unset, because neither
//     has a server-side source of truth: the chime preference lives in the
//     browser, and there is no global off switch. Both are inert here anyway —
//     the chime is never on the surface an unknown presence admits.
//   - Conversation is unset, and is provably inert for this consumer: the only
//     rule that reads it is the chime preference, and the chime is never on the
//     surface an unknown presence admits.
//
// WebPushAvailable is true, and that is a structural fact rather than a guess:
// app.startNotificationWorker refuses to start a worker with no Deliverer, so a
// worker that is evaluating anything has a channel to deliver through.
func policyContext(notification Notification) notificationpolicy.Context {
	return notificationpolicy.Context{
		EventID:          notification.ID,
		WorkspaceID:      notification.WorkspaceID,
		RecipientID:      notification.RecipientID,
		EventType:        notificationevent.EventType(notification.EventType),
		Priority:         notificationevent.Priority(notification.Priority),
		Origin:           notificationevent.Origin(notification.Origin),
		WorkSchedule:     workschedule.StateNotConfigured,
		Presence:         notificationpolicy.PresenceUnknown,
		Preferences:      notificationpolicy.Preferences{Muted: notification.Muted},
		WebPushAvailable: true,
	}
}

// policyVerdict maps the delivery plan onto the one channel this worker owns.
//
// Deliver is the push channel specifically, not the plan's overall eligibility,
// because this worker delivers push and nothing else. Under the context above
// the two coincide — an unknown presence puts nothing but push on the surface —
// and reading the channel is what keeps that a coincidence rather than an
// assumption a later consumer could break silently.
func policyVerdict(decision notificationpolicy.Decision) Verdict {
	return Verdict{
		Deliver:          decision.Channels.WebPush,
		SuppressedReason: decision.SuppressedReason(),
		PolicyVersion:    decision.PolicyVersion,
	}
}
