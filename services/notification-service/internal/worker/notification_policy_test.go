package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/nicrepository/nchat/libs/go/platform/notificationevent"
	"github.com/nicrepository/nchat/libs/go/platform/notificationpolicy"
	"github.com/nicrepository/nchat/libs/go/platform/workschedule"
)

// Issue #744: the adapter between the outbox row and the central policy.
//
// What is proved here is the mapping, and only the mapping: which row fields
// reach the engine, which channel the verdict reads, and that the reason and
// the policy version survive to the boundary. The rules themselves are proved
// in libs/go/platform/notificationpolicy, and a rule asserted here as well
// would be the beginning of the second authority this adapter exists to
// prevent.

func liveNotification() Notification {
	return Notification{
		ID:          "n1",
		WorkspaceID: "ws-1",
		RecipientID: "user-1",
		EventType:   string(notificationevent.EventTypeMention),
		Priority:    string(notificationevent.PriorityHigh),
		Origin:      string(notificationevent.OriginLive),
		SourceType:  string(notificationevent.SourceTypeMessage),
		SourceID:    "msg-1",
	}
}

func evaluate(t *testing.T, notification Notification) Verdict {
	t.Helper()
	verdict, err := NewPolicyEvaluator().Evaluate(context.Background(), notification)
	if err != nil {
		// The engine is a pure function and cannot fail; an error here would
		// mean the adapter invented a failure mode of its own.
		t.Fatalf("Evaluate: %v", err)
	}
	return verdict
}

func TestPolicyEvaluatorDecidesFromTheEventItWasGiven(t *testing.T) {
	cases := []struct {
		name        string
		mutate      func(*Notification)
		wantDeliver bool
		wantReason  string
	}{
		{"a live mention is delivered", nil, true, ""},
		{"an imported event is not", origin(notificationevent.OriginImport),
			false, string(notificationpolicy.ReasonHistoricalOrImported)},
		{"a replayed event is not", origin(notificationevent.OriginReplay),
			false, string(notificationpolicy.ReasonHistoricalOrImported)},
		{"a resynced event is not", origin(notificationevent.OriginResync),
			false, string(notificationpolicy.ReasonHistoricalOrImported)},
		{"an origin the row should not hold is not", origin("wat"),
			false, string(notificationpolicy.ReasonHistoricalOrImported)},
		{"a reaction is silent", eventType(notificationevent.EventTypeReaction),
			false, string(notificationpolicy.ReasonSilentEventType)},
		{"a direct message is delivered", eventType(notificationevent.EventTypeDirectMessage), true, ""},
		{"a call is delivered", eventType(notificationevent.EventTypeCall), true, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			notification := liveNotification()
			if tc.mutate != nil {
				tc.mutate(&notification)
			}
			verdict := evaluate(t, notification)
			if verdict.Deliver != tc.wantDeliver {
				t.Fatalf("Deliver = %v, want %v", verdict.Deliver, tc.wantDeliver)
			}
			if verdict.Reason() != tc.wantReason {
				t.Fatalf("Reason() = %q, want %q", verdict.Reason(), tc.wantReason)
			}
		})
	}
}

func origin(value notificationevent.Origin) func(*Notification) {
	return func(n *Notification) { n.Origin = string(value) }
}

func eventType(value notificationevent.EventType) func(*Notification) {
	return func(n *Notification) { n.EventType = string(value) }
}

// TestPolicyEvaluatorReportsThePolicyVersion is the observability half: a
// verdict has to say which rule set produced it, or a decision recorded today
// cannot be explained after the rules change.
func TestPolicyEvaluatorReportsThePolicyVersion(t *testing.T) {
	for _, notification := range []Notification{liveNotification(), suppressedNotification()} {
		verdict := evaluate(t, notification)
		if verdict.PolicyVersion != notificationpolicy.Version {
			t.Fatalf("PolicyVersion = %d, want %d", verdict.PolicyVersion, notificationpolicy.Version)
		}
	}
}

func suppressedNotification() Notification {
	notification := liveNotification()
	notification.Origin = string(notificationevent.OriginImport)
	return notification
}

// TestPolicyEvaluatorPersistsWhatTheOutboxAccepts checks the contract the
// column enforces: a suppression always carries a reason, a delivery never
// does, and the reason fits.
func TestPolicyEvaluatorPersistsWhatTheOutboxAccepts(t *testing.T) {
	for _, notification := range []Notification{liveNotification(), suppressedNotification()} {
		verdict := evaluate(t, notification)
		state := notificationevent.StateEligible
		if !verdict.Deliver {
			state = notificationevent.StateSuppressed
		}
		if err := notificationevent.ValidateSuppressedReason(state, verdict.Reason()); err != nil {
			t.Fatalf("verdict %+v is not persistable: %v", verdict, err)
		}
	}
}

// TestPolicyContextStatesWhatItDoesNotKnow pins the honest half of the mapping.
// These are not gaps to be filled in silently later: each one is a state the
// engine has a documented answer for, and changing one changes decisions.
func TestPolicyContextStatesWhatItDoesNotKnow(t *testing.T) {
	c := policyContext(liveNotification())

	if c.Presence != notificationpolicy.PresenceUnknown {
		t.Fatalf("Presence = %q, want unknown: an outbox row carries no session", c.Presence)
	}
	if c.WorkSchedule != workschedule.StateNotConfigured {
		t.Fatalf("WorkSchedule = %q, want not_configured: no schedule is persisted yet", c.WorkSchedule)
	}
	// Mute is resolved from the row (see TestPolicyContextCarriesTheResolvedMute);
	// the other two preferences have no server-side source of truth at all, so
	// they must stay at the zero value the engine documents rather than be
	// guessed at here.
	if c.Preferences.Disabled || c.Preferences.SoundMode != "" {
		t.Fatalf("Preferences = %+v, want only the mute this row resolves", c.Preferences)
	}
	if !c.WebPushAvailable {
		t.Fatalf("WebPushAvailable = false, but a worker without a delivery channel never starts")
	}
	if c.EventID != "n1" || c.WorkspaceID != "ws-1" || c.RecipientID != "user-1" {
		t.Fatalf("identity = %+v, want the row's own", c)
	}
}

// Issue #744: a decision has to be correlatable with the notification it was
// about and with the rule set that produced it.
//
// The version is asserted to come from the verdict rather than from anything
// the process knows about itself: during a rollout two replicas run different
// rule sets, and a version stamped by the build cannot answer "which rules
// decided this notification".

func decisionLog(t *testing.T, outbox *fakeOutbox, evaluator Evaluator) []map[string]any {
	t.Helper()
	var captured bytes.Buffer
	worker := NewNotificationWorker(notificationTestConfig(), NotificationWorkerDeps{
		Store:     outbox,
		Evaluator: evaluator,
		Deliverer: &recordingDeliverer{},
		Logger:    slog.New(slog.NewJSONHandler(&captured, nil)),
	})
	worker.runPass()

	var lines []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(captured.String()), "\n") {
		entry := map[string]any{}
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("log line %q is not structured: %v", line, err)
		}
		if entry["msg"] == "notification policy decision" {
			lines = append(lines, entry)
		}
	}
	return lines
}

func TestEveryDecisionIsCorrelatableWithItsNotification(t *testing.T) {
	cases := []decisionLogCase{
		{"a delivery keeps its version", notificationevent.OriginLive, "eligible", ""},
		{"and so does a suppression", notificationevent.OriginImport, "suppressed",
			string(notificationpolicy.ReasonHistoricalOrImported)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { tc.check(t) })
	}
}

type decisionLogCase struct {
	name       string
	origin     notificationevent.Origin
	wantState  string
	wantReason string
}

func (tc decisionLogCase) check(t *testing.T) {
	t.Helper()
	outbox := newFakeOutbox()
	outbox.seedPending("n1").event.Origin = string(tc.origin)

	lines := decisionLog(t, outbox, NewPolicyEvaluator())
	if len(lines) != 1 {
		t.Fatalf("got %d decision logs, want exactly one per decision", len(lines))
	}
	assertDecisionEntry(t, lines[0], tc)
}

func assertDecisionEntry(t *testing.T, entry map[string]any, tc decisionLogCase) {
	t.Helper()
	if entry["notification_id"] != "n1" {
		t.Fatalf("notification_id = %v, want the event's own", entry["notification_id"])
	}
	if entry["policy_version"] != float64(notificationpolicy.Version) {
		t.Fatalf("policy_version = %v, want %d", entry["policy_version"], notificationpolicy.Version)
	}
	if entry["state"] != tc.wantState {
		t.Fatalf("state = %v, want %q", entry["state"], tc.wantState)
	}
	if entry["reason"] != tc.wantReason {
		t.Fatalf("reason = %v, want %q", entry["reason"], tc.wantReason)
	}
}

// policyTestVersion is a rule set no build of this service has or could have.
//
// That is the whole of its job. A fixture that reused notificationpolicy.Version
// would pass just as well against a worker that ignored the verdict and stamped
// the constant itself, and the question this test exists to answer — "which
// rules decided this notification, on a replica that may not be running mine?"
// — would still be unanswered.
const policyTestVersion = 987

type verdictLogCase struct {
	name       string
	verdict    Verdict
	wantState  string
	wantReason string
}

// TestTheLoggedVersionIsTheVerdictsOwn drives the real evaluation path with a
// policy that answers with a version this build does not have, and checks the
// whole record the worker emitted for it.
//
// Both outcomes are covered on purpose. A version recorded only for
// suppressions would leave every delivered notification untraceable, which is
// most of them.
func TestTheLoggedVersionIsTheVerdictsOwn(t *testing.T) {
	if policyTestVersion == notificationpolicy.Version {
		t.Fatal("the fixture version must differ from this build's, or it proves nothing")
	}
	cases := []verdictLogCase{
		{
			name:      "an allowed decision carries the version too",
			verdict:   Verdict{Deliver: true, PolicyVersion: policyTestVersion},
			wantState: string(notificationevent.StateEligible),
		},
		{
			name: "a suppressed decision carries the version and its reason",
			verdict: Verdict{
				SuppressedReason: string(notificationpolicy.ReasonOutsideWorkHours),
				PolicyVersion:    policyTestVersion,
			},
			wantState:  string(notificationevent.StateSuppressed),
			wantReason: string(notificationpolicy.ReasonOutsideWorkHours),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { tc.check(t) })
	}
}

func (tc verdictLogCase) check(t *testing.T) {
	t.Helper()
	outbox := newFakeOutbox()
	outbox.seedPending("n1")

	lines := decisionLog(t, outbox, EvaluatorFunc(func(context.Context, Notification) (Verdict, error) {
		return tc.verdict, nil
	}))
	if len(lines) != 1 {
		t.Fatalf("got %d decision logs, want one per decision", len(lines))
	}
	assertVerdictEntry(t, lines[0], tc)
}

func assertVerdictEntry(t *testing.T, entry map[string]any, tc verdictLogCase) {
	t.Helper()
	if entry["notification_id"] != "n1" {
		t.Fatalf("notification_id = %v, want the event's own", entry["notification_id"])
	}
	if entry["policy_version"] != float64(policyTestVersion) {
		t.Fatalf("policy_version = %v, want the verdict's %d", entry["policy_version"], policyTestVersion)
	}
	if entry["policy_version"] == float64(notificationpolicy.Version) {
		t.Fatal("policy_version is this build's own rule set, not the one that decided")
	}
	if entry["state"] != tc.wantState {
		t.Fatalf("state = %v, want %q", entry["state"], tc.wantState)
	}
	if entry["reason"] != tc.wantReason {
		t.Fatalf("reason = %v, want %q", entry["reason"], tc.wantReason)
	}
}

// TestTheDecisionRecordCarriesNothingElse is the privacy half: the event is a
// correlation record, not a dump of what was evaluated. Every field is an
// identifier or a closed vocabulary, and a field added later has to be a
// deliberate change to this list.
func TestTheDecisionRecordCarriesNothingElse(t *testing.T) {
	outbox := newFakeOutbox()
	outbox.seedPending("n1")

	lines := decisionLog(t, outbox, NewPolicyEvaluator())
	if len(lines) != 1 {
		t.Fatalf("got %d decision logs, want one", len(lines))
	}
	allowed := map[string]bool{
		"time": true, "level": true, "msg": true,
		"notification_id": true, "policy_version": true,
		"state": true, "reason": true, "worker_id": true,
	}
	for field := range lines[0] {
		if !allowed[field] {
			t.Fatalf("the decision record carries an unexpected field %q", field)
		}
	}
}

// A decision whose write failed is still a decision that was produced, and it
// is the one an operator most needs to see: the event stays pending and will be
// decided again, so without this the only trace of what the policy said would
// be the trace that was lost.
func TestADecisionSurvivesAFailedWrite(t *testing.T) {
	outbox := newFakeOutbox()
	outbox.seedPending("n1")
	outbox.fail("evaluate", errors.New("conflict"))

	lines := decisionLog(t, outbox, NewPolicyEvaluator())
	if len(lines) != 1 {
		t.Fatalf("got %d decision logs, want the decision that was produced", len(lines))
	}
	if lines[0]["notification_id"] != "n1" ||
		lines[0]["policy_version"] != float64(notificationpolicy.Version) {
		t.Fatalf("decision log = %v, want it correlated with the notification", lines[0])
	}
}

// ---------------------------------------------------------------------------
// Mute, from the outbox row to the decision (issue #744)
// ---------------------------------------------------------------------------

// The adapter has to hand the resolved mute to the engine. Asserted on the
// Context the adapter builds, because that is the mapping this file owns: what
// the mute then *does* is the engine's rule, proved below through the worker.
func TestPolicyContextCarriesTheResolvedMute(t *testing.T) {
	if policyContext(liveNotification()).Preferences.Muted {
		t.Fatal("an unmuted row produced a muted context")
	}
	muted := liveNotification()
	muted.Muted = true
	if !policyContext(muted).Preferences.Muted {
		t.Fatal("the resolved mute did not reach the engine's input")
	}
}

// A muted conversation must reach the engine as a suppression with the reason
// the operator will read months later, and it must take the push channel with
// it — this worker's only channel.
func TestPolicyEvaluatorSuppressesAMutedConversation(t *testing.T) {
	muted := liveNotification()
	muted.Muted = true
	verdict := evaluate(t, muted)

	if verdict.Deliver {
		t.Fatal("a muted conversation was still eligible for push")
	}
	if verdict.SuppressedReason != string(notificationpolicy.ReasonMuted) {
		t.Fatalf("reason = %q, want %q", verdict.SuppressedReason, notificationpolicy.ReasonMuted)
	}
	if verdict.PolicyVersion != notificationpolicy.Version {
		t.Fatalf("policy version = %d, want %d", verdict.PolicyVersion, notificationpolicy.Version)
	}
	// The decision is the engine's, not the adapter's: the same row without the
	// preference is delivered, so nothing here suppresses on its own account.
	if !evaluate(t, liveNotification()).Deliver {
		t.Fatal("the adapter suppressed an event the engine would have delivered")
	}
}

// The real path, end to end: a pending row carrying the mute the projection
// resolved is decided by the production evaluator, recorded as suppressed with
// the reason "muted", and never handed to a provider.
//
// This is deliberately not an assertion about notificationpolicy — that package
// proves its own rules. What it proves is the assembly: the row's mute survives
// storage.NotificationEvent, notificationFrom, policyContext and Evaluate, and
// arrives in the column an operator queries.
func TestWorkerSuppressesAMutedConversationEndToEnd(t *testing.T) {
	outbox := newFakeOutbox()
	outbox.seedPendingMuted("muted")
	outbox.seedPending("unmuted")
	deliverer := &recordingDeliverer{}
	// nil evaluator: the worker resolves it to NewPolicyEvaluator(), so this
	// runs the same authority production runs.
	worker := newTestWorker(t, outbox, deliverer, nil)

	worker.runPass()
	worker.runPass()

	muted := outbox.snapshot("muted")
	if muted.state != notificationevent.StateSuppressed {
		t.Fatalf("muted state = %q, want %q", muted.state, notificationevent.StateSuppressed)
	}
	if muted.reason != string(notificationpolicy.ReasonMuted) {
		t.Fatalf("muted reason = %q, want %q", muted.reason, notificationpolicy.ReasonMuted)
	}

	// Isolation, at the level that matters operationally: the recipient who did
	// not mute is unaffected by the one who did.
	if got := outbox.snapshot("unmuted").state; got != notificationevent.StateSent {
		t.Fatalf("unmuted state = %q, want %q", got, notificationevent.StateSent)
	}
	if keys := deliverer.delivered(); len(keys) != 1 || keys[0] != "unmuted" {
		t.Fatalf("delivered %v, want exactly [unmuted]", keys)
	}
}

// The decision log has to stay correlatable for a suppression the mute caused:
// the notification, the version that decided it, and the reason.
func TestWorkerLogsTheMuteDecision(t *testing.T) {
	outbox := newFakeOutbox()
	outbox.seedPendingMuted("muted")

	// nil evaluator, so this reads the log the production policy produces.
	entries := decisionLog(t, outbox, nil)
	if len(entries) != 1 {
		t.Fatalf("got %d decision logs, want exactly 1", len(entries))
	}
	want := map[string]any{
		"notification_id": "muted",
		"state":           string(notificationevent.StateSuppressed),
		"reason":          string(notificationpolicy.ReasonMuted),
		"policy_version":  float64(notificationpolicy.Version),
	}
	for field, expected := range want {
		if entries[0][field] != expected {
			t.Fatalf("%s = %v, want %v", field, entries[0][field], expected)
		}
	}
}
