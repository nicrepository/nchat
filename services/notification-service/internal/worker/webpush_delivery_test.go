package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/nicrepository/nchat/services/notification-service/internal/config"
	"github.com/nicrepository/nchat/services/notification-service/internal/domain"
)

// Issue #746: the fan-out.
//
// Four things are proved here and nothing else belongs in this file: which
// endpoints are attempted, what each answer does to the two records that track
// them, what the whole thing amounts to for the outbox row, and — the one that
// is a security property rather than a correctness one — that a provider is
// never reached for a notification that should not produce a push.

const (
	testTTL          = time.Hour
	testWorkspaceID  = "22222222-2222-2222-2222-222222222222"
	testRecipientID  = "33333333-3333-3333-3333-333333333333"
	testNotification = "11111111-1111-1111-1111-111111111111"
)

// deliveryFixture is one deliverer with its fakes to hand.
type deliveryFixture struct {
	deliverer *WebPushDeliverer
	store     *fakePushStore
	sender    *fakeSender
	logs      *bytes.Buffer
}

func newDeliveryFixture(t *testing.T, store *fakePushStore, sender *fakeSender) *deliveryFixture {
	t.Helper()
	logs := &bytes.Buffer{}
	return &deliveryFixture{
		deliverer: NewWebPushDeliverer(
			config.WebPushConfig{TTLSeconds: int(testTTL / time.Second)},
			WebPushDeps{
				Store:  store,
				Sender: sender,
				Logger: slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
			}),
		store:  store,
		sender: sender,
		logs:   logs,
	}
}

// fixture is the common shape: some active subscriptions, everything succeeds
// unless a test says otherwise.
func fixture(t *testing.T, subscriptionIDs ...string) *deliveryFixture {
	t.Helper()
	return newDeliveryFixture(t, newFakePushStore().withTargets(subscriptionIDs...), newFakeSender())
}

func eligibleNotification() Notification {
	return Notification{
		ID:          testNotification,
		WorkspaceID: testWorkspaceID,
		RecipientID: testRecipientID,
		EventType:   "mention",
		Priority:    "high",
		SourceType:  "message",
		SourceID:    "44444444-4444-4444-4444-444444444444",
		Origin:      "live",
		Attempt:     1,
		OccurredAt:  time.Now().Add(-time.Minute),
	}
}

func (f *deliveryFixture) deliver(t *testing.T, notification Notification) error {
	t.Helper()
	return f.deliverer.Deliver(context.Background(), notification)
}

// ---------------------------------------------------------------------------
// Expiry
// ---------------------------------------------------------------------------

// An expired notification is refused before the database is read, let alone a
// provider called. It is terminal rather than retried: waiting longer cannot
// make it younger.
func TestAnExpiredNotificationIsNeverSent(t *testing.T) {
	f := fixture(t, "sub-a")
	notification := eligibleNotification()
	notification.OccurredAt = time.Now().Add(-2 * testTTL)

	err := f.deliver(t, notification)

	if !errors.Is(err, ErrPermanentDelivery) {
		t.Fatalf("err = %v, want a permanent failure", err)
	}
	if f.sender.totalCalls() != 0 {
		t.Fatalf("an expired notification produced %d provider calls", f.sender.totalCalls())
	}
	if f.store.ledgerSize() != 0 {
		t.Fatal("an expired notification was recorded as delivered")
	}
}

// The boundary is exclusive: exactly at the TTL there is nothing left to
// deliver, and a hair inside it there is.
func TestTheExpiryBoundaryIsExclusive(t *testing.T) {
	cases := map[string]struct {
		age  time.Duration
		sent bool
	}{
		"well inside":        {age: time.Minute, sent: true},
		"just inside":        {age: testTTL - time.Second, sent: true},
		"exactly at the TTL": {age: testTTL, sent: false},
		"just past":          {age: testTTL + time.Millisecond, sent: false},
		"long past":          {age: 30 * testTTL, sent: false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := fixture(t, "sub-a")
			notification := eligibleNotification()
			notification.OccurredAt = time.Now().Add(-tc.age)

			_ = f.deliver(t, notification)

			if sent := f.sender.totalCalls() > 0; sent != tc.sent {
				t.Fatalf("sent = %v at age %v, want %v", sent, tc.age, tc.sent)
			}
		})
	}
}

// The TTL is measured from the event, not from the attempt, so a notification
// that aged out while being retried is refused by the same arithmetic.
func TestARetryThatOutlivedTheTTLIsRefused(t *testing.T) {
	f := fixture(t, "sub-a")
	notification := eligibleNotification()
	notification.Attempt = 5
	notification.OccurredAt = time.Now().Add(-testTTL - time.Minute)

	if err := f.deliver(t, notification); !errors.Is(err, ErrPermanentDelivery) {
		t.Fatalf("err = %v, want a permanent failure", err)
	}
	if f.sender.totalCalls() != 0 {
		t.Fatalf("a retry past the TTL produced %d provider calls", f.sender.totalCalls())
	}
}

// The remaining validity, not the whole TTL, is what the push service is told:
// a notification with ten minutes left is not worth holding for an hour.
func TestTheRemainingValidityIsWhatIsSent(t *testing.T) {
	store := newFakePushStore().withTarget("sub-a")
	sender := &recordingSender{}
	deliverer := NewWebPushDeliverer(
		config.WebPushConfig{TTLSeconds: int(testTTL / time.Second)},
		WebPushDeps{Store: store, Sender: sender})

	notification := eligibleNotification()
	notification.OccurredAt = time.Now().Add(-50 * time.Minute)

	if err := deliverer.Deliver(context.Background(), notification); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if ttl := sender.last.TTL; ttl <= 9*time.Minute || ttl > 10*time.Minute {
		t.Fatalf("TTL = %v, want about the ten minutes remaining", ttl)
	}
}

type recordingSender struct{ last PushMessage }

func (s *recordingSender) Send(_ context.Context, message PushMessage) PushResult {
	s.last = message
	return delivered()
}

// ---------------------------------------------------------------------------
// Fan-out across several browsers
// ---------------------------------------------------------------------------

func TestNoActiveSubscriptionIsTerminalAndSendsNothing(t *testing.T) {
	f := fixture(t)

	err := f.deliver(t, eligibleNotification())

	if !errors.Is(err, ErrPermanentDelivery) {
		t.Fatalf("err = %v, want a permanent failure", err)
	}
	if f.sender.totalCalls() != 0 {
		t.Fatalf("a recipient with no browsers produced %d provider calls", f.sender.totalCalls())
	}
}

func TestOneActiveSubscriptionIsDelivered(t *testing.T) {
	f := fixture(t, "sub-a")

	if err := f.deliver(t, eligibleNotification()); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if f.sender.callsTo("sub-a") != 1 {
		t.Fatalf("sub-a received %d sends, want one", f.sender.callsTo("sub-a"))
	}
	if !f.store.delivered(testNotification, "sub-a") {
		t.Fatal("the delivery was not recorded in the ledger")
	}
}

// Several browsers each get their own attempt, exactly once.
func TestEveryActiveSubscriptionIsAttemptedOnce(t *testing.T) {
	f := fixture(t, "sub-a", "sub-b", "sub-c")

	if err := f.deliver(t, eligibleNotification()); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	for _, id := range []string{"sub-a", "sub-b", "sub-c"} {
		if got := f.sender.callsTo(id); got != 1 {
			t.Fatalf("%s received %d sends, want one", id, got)
		}
		if !f.store.delivered(testNotification, id) {
			t.Fatalf("%s was not recorded in the ledger", id)
		}
	}
}

// The scenario the issue names. A dead endpoint must not take a live one with
// it: A is retired, B is delivered, and the failure of A neither stops nor
// precedes B.
func TestADeadEndpointDoesNotStopALiveOne(t *testing.T) {
	store := newFakePushStore().withTargets("sub-a", "sub-b")
	sender := newFakeSender().answering("sub-a", gone())
	f := newDeliveryFixture(t, store, sender)

	if err := f.deliver(t, eligibleNotification()); err != nil {
		t.Fatalf("Deliver: %v", err)
	}

	if sender.callsTo("sub-b") != 1 {
		t.Fatal("the live endpoint was not attempted")
	}
	if store.statuses["sub-a"] != domain.StatusInvalid {
		t.Fatalf("sub-a status = %q, want invalid", store.statuses["sub-a"])
	}
	if store.statuses["sub-b"] != domain.StatusActive {
		t.Fatal("retiring one subscription retired another")
	}
	if !store.delivered(testNotification, "sub-b") {
		t.Fatal("the live endpoint was not recorded as delivered")
	}
	if store.delivered(testNotification, "sub-a") {
		t.Fatal("a refused endpoint was recorded as delivered")
	}
}

func TestBothRetirementStatusesInvalidate(t *testing.T) {
	for name, answer := range map[string]PushResult{"gone": gone(), "not found": notFound()} {
		t.Run(name, func(t *testing.T) {
			store := newFakePushStore().withTarget("sub-a")
			f := newDeliveryFixture(t, store, newFakeSender().answering("sub-a", answer))

			_ = f.deliver(t, eligibleNotification())

			if store.statuses["sub-a"] != domain.StatusInvalid {
				t.Fatalf("a %s response did not retire the subscription", name)
			}
		})
	}
}

// A transient failure leaves the subscription alone. Unsubscribing a real
// person because a push service had a bad afternoon is the failure mode this
// asserts against.
func TestTransientFailuresDoNotRetireASubscription(t *testing.T) {
	for name, answer := range map[string]PushResult{
		"provider unavailable": unavailable(),
		"rate limited":         rateLimited(0),
		"no response":          {Class: PushTransientFailure},
		"payload refused":      rejected(),
	} {
		t.Run(name, func(t *testing.T) {
			store := newFakePushStore().withTarget("sub-a")
			f := newDeliveryFixture(t, store, newFakeSender().answering("sub-a", answer))

			_ = f.deliver(t, eligibleNotification())

			if store.statuses["sub-a"] != domain.StatusActive {
				t.Fatalf("a %s response retired the subscription", name)
			}
		})
	}
}

func TestAllPermanentFailuresRetireTheNotification(t *testing.T) {
	store := newFakePushStore().withTargets("sub-a", "sub-b")
	f := newDeliveryFixture(t, store,
		newFakeSender().answering("sub-a", gone()).answering("sub-b", rejected()))

	err := f.deliver(t, eligibleNotification())

	if !errors.Is(err, ErrPermanentDelivery) {
		t.Fatalf("err = %v, want a permanent failure", err)
	}
	if f.store.ledgerSize() != 0 {
		t.Fatal("a fan-out that delivered nothing recorded a delivery")
	}
}

func TestAllTransientFailuresAreRetried(t *testing.T) {
	store := newFakePushStore().withTargets("sub-a", "sub-b")
	f := newDeliveryFixture(t, store,
		newFakeSender().answering("sub-a", unavailable()).answering("sub-b", unavailable()))

	err := f.deliver(t, eligibleNotification())

	if err == nil {
		t.Fatal("a fan-out that delivered nothing reported success")
	}
	if errors.Is(err, ErrPermanentDelivery) {
		t.Fatalf("err = %v, want a transient failure", err)
	}
}

// ---------------------------------------------------------------------------
// Partial success
// ---------------------------------------------------------------------------

// The second scenario the issue names, and the one the ledger exists for.
//
// A answers 5xx and B answers 2xx. The notification is retried, because A is
// still owed one — and when it is, B is not in the fan-out and is not sent to
// again, while A is attempted exactly once more.
func TestARetryReachesOnlyTheEndpointStillOwedOne(t *testing.T) {
	store := newFakePushStore().withTargets("sub-a", "sub-b")
	sender := newFakeSender().answering("sub-a", unavailable(), delivered())
	f := newDeliveryFixture(t, store, sender)

	notification := eligibleNotification()
	err := f.deliver(t, notification)
	if err == nil || errors.Is(err, ErrPermanentDelivery) {
		t.Fatalf("err = %v, want a transient failure so the row is retried", err)
	}
	if !store.delivered(testNotification, "sub-b") {
		t.Fatal("the endpoint that succeeded was not recorded")
	}

	notification.Attempt = 2
	if err := f.deliver(t, notification); err != nil {
		t.Fatalf("the retry failed: %v", err)
	}

	if got := sender.callsTo("sub-b"); got != 1 {
		t.Fatalf("sub-b received %d sends across both passes, want one", got)
	}
	if got := sender.callsTo("sub-a"); got != 2 {
		t.Fatalf("sub-a received %d sends, want one per pass", got)
	}
}

// A permanently retired endpoint is out of the fan-out for good — not only for
// the notification that retired it.
func TestARetiredEndpointIsNeverSentToAgain(t *testing.T) {
	store := newFakePushStore().withTargets("sub-a", "sub-b")
	sender := newFakeSender().answering("sub-a", gone()).answering("sub-b", unavailable(), delivered())
	f := newDeliveryFixture(t, store, sender)

	notification := eligibleNotification()
	_ = f.deliver(t, notification)

	notification.Attempt = 2
	_ = f.deliver(t, notification)

	if got := sender.callsTo("sub-a"); got != 1 {
		t.Fatalf("a retired endpoint received %d sends, want the one that retired it", got)
	}
}

// ---------------------------------------------------------------------------
// Idempotency
// ---------------------------------------------------------------------------

// A replay of a fully delivered notification reaches no provider and writes
// nothing. This is the property that stops a crash-loop, a duplicated claim or
// an operator's manual re-run from multiplying pushes.
func TestReplayingADeliveredNotificationSendsNothing(t *testing.T) {
	f := fixture(t, "sub-a", "sub-b")
	notification := eligibleNotification()

	if err := f.deliver(t, notification); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	sizeAfterFirst := f.store.ledgerSize()

	for attempt := 2; attempt <= 4; attempt++ {
		notification.Attempt = attempt
		if err := f.deliver(t, notification); !errors.Is(err, ErrPermanentDelivery) {
			t.Fatalf("replay %d returned %v, want the terminal no-target answer", attempt, err)
		}
	}

	if got := f.sender.totalCalls(); got != 2 {
		t.Fatalf("%d provider calls across four passes, want the two of the first", got)
	}
	if f.store.ledgerSize() != sizeAfterFirst {
		t.Fatalf("replays grew the ledger from %d to %d", sizeAfterFirst, f.store.ledgerSize())
	}
}

// Two notifications to the same browser are two deliveries. The ledger is keyed
// by the pair, not by the subscription, so deduplication is per notification
// and never suppresses a genuinely new one.
func TestTheLedgerDoesNotSuppressADifferentNotification(t *testing.T) {
	f := fixture(t, "sub-a")

	first := eligibleNotification()
	second := eligibleNotification()
	second.ID = "55555555-5555-5555-5555-555555555555"

	if err := f.deliver(t, first); err != nil {
		t.Fatalf("first: %v", err)
	}
	if err := f.deliver(t, second); err != nil {
		t.Fatalf("second: %v", err)
	}

	if got := f.sender.callsTo("sub-a"); got != 2 {
		t.Fatalf("sub-a received %d sends, want one per notification", got)
	}
	if f.store.ledgerSize() != 2 {
		t.Fatalf("ledger holds %d rows, want one per delivery", f.store.ledgerSize())
	}
}

// A failed ledger write is reported to an operator as well as to the worker.
//
// What it must not do is pass for a delivery — that is
// TestALedgerWriteThatNeverLandsIsNotADelivery, and the two together are the
// whole of the rule: the write is visible, and it is not assumed.
func TestAFailedLedgerWriteIsLogged(t *testing.T) {
	store := newFakePushStore().withTarget("sub-a")
	store.ledgerErr = errFakeStore
	f := newDeliveryFixture(t, store, newFakeSender())

	_ = f.deliver(t, eligibleNotification())

	if !strings.Contains(f.logs.String(), "ledger_write_failed") {
		t.Fatalf("the failed ledger write was not reported:\n%s", f.logs)
	}
}

// ---------------------------------------------------------------------------
// Retry-After
// ---------------------------------------------------------------------------

// A rate limiter's own figure is carried back to the worker, and the largest
// across the fan-out wins: satisfying the busiest push service satisfies them
// all.
func TestTheLongestRetryAfterIsCarriedBack(t *testing.T) {
	store := newFakePushStore().withTargets("sub-a", "sub-b", "sub-c")
	f := newDeliveryFixture(t, store, newFakeSender().
		answering("sub-a", rateLimited(30*time.Second)).
		answering("sub-b", rateLimited(5*time.Minute)).
		answering("sub-c", unavailable()))

	err := f.deliver(t, eligibleNotification())

	if got := requestedRetryDelay(err); got != 5*time.Minute {
		t.Fatalf("requested delay = %v, want the longest of the three", got)
	}
}

// A transient failure with no Retry-After asks for nothing, so the worker's own
// backoff decides.
func TestATransientFailureWithoutARetryAfterAsksForNothing(t *testing.T) {
	store := newFakePushStore().withTarget("sub-a")
	f := newDeliveryFixture(t, store, newFakeSender().answering("sub-a", unavailable()))

	err := f.deliver(t, eligibleNotification())

	if got := requestedRetryDelay(err); got != 0 {
		t.Fatalf("requested delay = %v, want none", got)
	}
}

// ---------------------------------------------------------------------------
// Database failures
// ---------------------------------------------------------------------------

// A database that cannot answer is a reason to try later, never a reason to
// declare a recipient unreachable — and never a reason to send blind.
func TestAFanOutQueryFailureIsTransientAndSendsNothing(t *testing.T) {
	store := newFakePushStore().withTarget("sub-a")
	store.listErr = errFakeStore
	f := newDeliveryFixture(t, store, newFakeSender())

	err := f.deliver(t, eligibleNotification())

	if err == nil || errors.Is(err, ErrPermanentDelivery) {
		t.Fatalf("err = %v, want a transient failure", err)
	}
	if f.sender.totalCalls() != 0 {
		t.Fatalf("a failed fan-out query produced %d provider calls", f.sender.totalCalls())
	}
}

// A failed history write does not undo a delivery that happened.
//
// last_success_at is a diagnostic column, not a decision: the ledger row is
// what makes the delivery terminal, and it landed. The failure is logged under
// its own category so an operator can tell a lost stamp from a lost ledger row.
func TestAFailedHistoryWriteDoesNotUndoADelivery(t *testing.T) {
	store := newFakePushStore().withTarget("sub-a")
	store.outcomeErr = errFakeStore
	f := newDeliveryFixture(t, store, newFakeSender())

	if err := f.deliver(t, eligibleNotification()); err != nil {
		t.Fatalf("err = %v, want the delivery reported", err)
	}
	if !store.delivered(testNotification, "sub-a") {
		t.Fatal("the ledger row was not written")
	}
	if !strings.Contains(f.logs.String(), "subscription_history_write_failed") {
		t.Fatalf("the failed history write was not reported:\n%s", f.logs)
	}
}

// A subscription that rotated while the attempt was in flight loses the
// compare-and-set. That is ordinary, and it is reported rather than raised.
func TestASubscriptionThatRotatedMidFlightIsReportedNotFailed(t *testing.T) {
	store := newFakePushStore().withTarget("sub-a").rotatesDuring("sub-a")
	f := newDeliveryFixture(t, store, newFakeSender())

	if err := f.deliver(t, eligibleNotification()); err != nil {
		t.Fatalf("err = %v, want the delivery reported", err)
	}
	if !strings.Contains(f.logs.String(), "changed while the attempt was in flight") {
		t.Fatalf("the lost compare-and-set was not reported:\n%s", f.logs)
	}
}

// The generation captured with the target is what the answer is applied
// against, so a late result cannot be written to the endpoint that replaced the
// one it describes.
func TestTheOutcomeIsAppliedToTheGenerationItWasAttemptedAgainst(t *testing.T) {
	store := newFakePushStore().withTarget("sub-a")
	store.targets[0].Generation = 7
	f := newDeliveryFixture(t, store, newFakeSender())

	_ = f.deliver(t, eligibleNotification())

	outcomes := store.recordedOutcomes()
	if len(outcomes) != 1 {
		t.Fatalf("%d lifecycle writes, want one", len(outcomes))
	}
	if outcomes[0].generation != 7 {
		t.Fatalf("generation = %d, want the one the attempt captured", outcomes[0].generation)
	}
}

// ---------------------------------------------------------------------------
// Cancellation
// ---------------------------------------------------------------------------

// A cancelled context stops the fan-out rather than racing the rest against a
// deadline that has already passed. What was not attempted is not recorded, so
// the next claim picks it up.
func TestACancelledContextStopsTheFanOut(t *testing.T) {
	store := newFakePushStore().withTargets("sub-a", "sub-b", "sub-c")
	sender := newFakeSender()
	deliverer := NewWebPushDeliverer(
		config.WebPushConfig{TTLSeconds: int(testTTL / time.Second)},
		WebPushDeps{Store: store, Sender: sender})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := deliverer.Deliver(ctx, eligibleNotification())

	if err == nil || errors.Is(err, ErrPermanentDelivery) {
		t.Fatalf("err = %v, want a transient failure so the work is reclaimed", err)
	}
	if sender.totalCalls() != 0 {
		t.Fatalf("a cancelled fan-out made %d provider calls", sender.totalCalls())
	}
	if store.ledgerSize() != 0 {
		t.Fatal("a cancelled fan-out recorded a delivery")
	}
}

// ---------------------------------------------------------------------------
// Observability
// ---------------------------------------------------------------------------

// The log records what an operator needs to investigate one browser, and none
// of what a push carries. The endpoint and both keys are the capability to
// reach a device; the payload is the message.
func TestAttemptLogsCarryReferencesAndNothingElse(t *testing.T) {
	store := newFakePushStore().withTargets("sub-a", "sub-b")
	f := newDeliveryFixture(t, store,
		newFakeSender().answering("sub-a", gone()).answering("sub-b", rateLimited(time.Minute)))

	_ = f.deliver(t, eligibleNotification())
	logs := f.logs.String()

	for _, required := range []string{
		`"notification_id":"` + testNotification + `"`,
		`"subscription_id":"sub-a"`,
		`"attempt":1`,
		`"result":"permanent_subscription_failure"`,
		`"status_code":410`,
		`"invalidation_reason":"gone"`,
		`"latency_ms":`,
	} {
		if !strings.Contains(logs, required) {
			t.Fatalf("the attempt log is missing %s:\n%s", required, logs)
		}
	}

	for name, forbidden := range map[string]string{
		"endpoint":  "https://push.example.com/subscription/",
		"p256dh":    "p256dh-sub-a",
		"auth key":  "auth-sub-a",
		"recipient": testRecipientID,
		"workspace": testWorkspaceID,
	} {
		if strings.Contains(logs, forbidden) {
			t.Fatalf("the attempt log carries the %s:\n%s", name, logs)
		}
	}
	assertNoPayloadInLogs(t, logs)
}

// The payload never appears in a log line, whole or in part.
func assertNoPayloadInLogs(t *testing.T, logs string) {
	t.Helper()
	payload, err := buildPushPayload(eligibleNotification())
	if err != nil {
		t.Fatalf("buildPushPayload: %v", err)
	}
	var fields map[string]any
	if err := json.Unmarshal(payload, &fields); err != nil {
		t.Fatalf("payload: %v", err)
	}
	// The notification id is deliberately in both; every other payload field is
	// content the log has no business repeating.
	delete(fields, "id")
	for key, value := range fields {
		rendered, _ := json.Marshal(value)
		if strings.Contains(logs, `"`+key+`":`+string(rendered)) {
			t.Fatalf("the log repeats the payload field %q:\n%s", key, logs)
		}
	}
}

// An expired notification is observable as expired, not as a silent nothing.
func TestExpiryIsLogged(t *testing.T) {
	f := fixture(t, "sub-a")
	notification := eligibleNotification()
	notification.OccurredAt = time.Now().Add(-2 * testTTL)

	_ = f.deliver(t, notification)

	if !strings.Contains(f.logs.String(), "expired before web push delivery") {
		t.Fatalf("expiry was not logged:\n%s", f.logs)
	}
}

// ---------------------------------------------------------------------------
// Metrics
// ---------------------------------------------------------------------------

// Every label value comes from the closed sets in this package. A notification,
// a subscription or an endpoint's host as a label would grow the series count
// with traffic and publish an identifier this service keeps private.
func TestPushMetricsUseOnlyClosedLabelValues(t *testing.T) {
	metrics, shared := newTestMetrics(t)
	store := newFakePushStore().withTargets("sub-a", "sub-b")
	deliverer := NewWebPushDeliverer(
		config.WebPushConfig{TTLSeconds: int(testTTL / time.Second)},
		WebPushDeps{
			Store:   store,
			Sender:  newFakeSender().answering("sub-a", gone()),
			Metrics: metrics,
		})

	if err := deliverer.Deliver(context.Background(), eligibleNotification()); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	body := scrape(t, shared)

	for _, required := range []string{
		`nchat_notification_push_attempts_total{result="delivered"} 1`,
		`nchat_notification_push_attempts_total{result="permanent_subscription_failure"} 1`,
		`nchat_notification_push_fanout_total{result="delivered"} 1`,
		"nchat_notification_push_duration_seconds",
	} {
		if !strings.Contains(body, required) {
			t.Fatalf("%s was not exported:\n%s", required, body)
		}
	}
	for _, forbidden := range []string{testNotification, "sub-a", "sub-b", "push.example.com"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("%q appeared as a metric label:\n%s", forbidden, body)
		}
	}
}

// The fan-out counter distinguishes the five shapes a fan-out can take, which
// is what no per-attempt count can express.
func TestTheFanOutCounterNamesTheShapeOfTheResult(t *testing.T) {
	cases := map[string]struct {
		targets []string
		answers map[string]PushResult
		aged    time.Duration
		want    string
	}{
		"everything delivered": {targets: []string{"sub-a"}, want: pushFanOutDelivered},
		"some delivered, some owed a retry": {
			targets: []string{"sub-a", "sub-b"},
			answers: map[string]PushResult{"sub-a": unavailable()},
			want:    pushFanOutPartial,
		},
		"nothing delivered": {
			targets: []string{"sub-a"},
			answers: map[string]PushResult{"sub-a": rejected()},
			want:    pushFanOutFailed,
		},
		"no browsers":     {want: pushFanOutNoTarget},
		"already expired": {targets: []string{"sub-a"}, aged: 2 * testTTL, want: pushFanOutExpired},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			metrics, shared := newTestMetrics(t)
			sender := newFakeSender()
			for id, answer := range tc.answers {
				sender.answering(id, answer)
			}
			deliverer := NewWebPushDeliverer(
				config.WebPushConfig{TTLSeconds: int(testTTL / time.Second)},
				WebPushDeps{
					Store:   newFakePushStore().withTargets(tc.targets...),
					Sender:  sender,
					Metrics: metrics,
				})

			notification := eligibleNotification()
			if tc.aged > 0 {
				notification.OccurredAt = time.Now().Add(-tc.aged)
			}
			_ = deliverer.Deliver(context.Background(), notification)

			want := `nchat_notification_push_fanout_total{result="` + tc.want + `"} 1`
			if body := scrape(t, shared); !strings.Contains(body, want) {
				t.Fatalf("%s was not recorded:\n%s", want, body)
			}
		})
	}
}

// A deliverer built without metrics still works. The worker's own collectors
// are nil-safe for the same reason: an operator turning metrics off must not
// turn deliveries off with them.
func TestDeliveryWorksWithoutMetrics(t *testing.T) {
	deliverer := NewWebPushDeliverer(
		config.WebPushConfig{TTLSeconds: int(testTTL / time.Second)},
		WebPushDeps{Store: newFakePushStore().withTarget("sub-a"), Sender: newFakeSender()})

	if err := deliverer.Deliver(context.Background(), eligibleNotification()); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
}

// A payload this build cannot produce correctly is a defect in this service,
// not something a provider or a recipient did. It fails identically on every
// attempt, so it is terminal and no provider is called.
func TestAnUnbuildablePayloadIsTerminalAndSendsNothing(t *testing.T) {
	f := fixture(t, "sub-a")
	notification := eligibleNotification()
	notification.SourceID = strings.Repeat("x", maxPushPayloadBytes)

	err := f.deliver(t, notification)

	if !errors.Is(err, ErrPermanentDelivery) {
		t.Fatalf("err = %v, want a permanent failure", err)
	}
	if f.sender.totalCalls() != 0 {
		t.Fatalf("an unbuildable payload produced %d provider calls", f.sender.totalCalls())
	}
}

// A recipient with no browsers is the common terminal ending in a deployment
// where few people have enabled push. It is observable rather than a silent
// "failed" on the outbox row.
func TestHavingNoBrowsersIsLogged(t *testing.T) {
	f := fixture(t)

	_ = f.deliver(t, eligibleNotification())

	if !strings.Contains(f.logs.String(), "no deliverable push subscription") {
		t.Fatalf("an empty fan-out was not reported:\n%s", f.logs)
	}
}

// ---------------------------------------------------------------------------
// A terminal answer is not terminal until it has been written down
// ---------------------------------------------------------------------------

// The finding, exactly as reported: A is delivered, its ledger write fails, and
// B is owed a retry.
//
// Before the fix the failed write was logged and discarded, so A counted as
// delivered while no ledger row existed — and every retry driven by B sent to A
// again. Now the write is retried, not the push: A is attempted once, lands in
// the ledger, and the retry reaches only B.
func TestADeliveredEndpointSurvivesALedgerBlipAndIsNotResent(t *testing.T) {
	store := newFakePushStore().withTargets("sub-a", "sub-b").failLedgerOnce()
	sender := newFakeSender().answering("sub-b", unavailable(), delivered())
	f := newDeliveryFixture(t, store, sender)

	notification := eligibleNotification()
	err := f.deliver(t, notification)

	// B is still owed one, so the event retries — but not because of A.
	if err == nil || errors.Is(err, ErrPermanentDelivery) {
		t.Fatalf("err = %v, want a transient failure so the row is retried", err)
	}
	if !errors.Is(err, errPushTransient) {
		t.Fatalf("err = %v, want the transient provider failure, not a storage error", err)
	}

	// The write was retried; the push was not.
	if got := sender.callsTo("sub-a"); got != 1 {
		t.Fatalf("the provider was called %d times for sub-a, want once", got)
	}
	if got := store.ledgerAttempts(); got != 2 {
		t.Fatalf("%d ledger writes, want the failure and its retry", got)
	}
	if !store.delivered(testNotification, "sub-a") {
		t.Fatal("the recovered write did not record the delivery")
	}

	// The retry of the event reaches only the endpoint still owed one.
	notification.Attempt = 2
	if err := f.deliver(t, notification); err != nil {
		t.Fatalf("the retry failed: %v", err)
	}
	if got := sender.callsTo("sub-a"); got != 1 {
		t.Fatalf("the retry re-sent to a delivered endpoint %d times", got-1)
	}
	if got := sender.callsTo("sub-b"); got != 2 {
		t.Fatalf("sub-b was called %d times, want one per pass", got)
	}
}

// A ledger write that never lands must not be reported as a delivery.
//
// The event goes back for another pass rather than being declared sent on the
// provider's word alone, and the error names a storage failure rather than
// pretending the push service was unavailable.
func TestALedgerWriteThatNeverLandsIsNotADelivery(t *testing.T) {
	store := newFakePushStore().withTargets("sub-a", "sub-b")
	store.ledgerErr = errFakeStore
	sender := newFakeSender()
	f := newDeliveryFixture(t, store, sender)

	err := f.deliver(t, eligibleNotification())

	if err == nil {
		t.Fatal("an unrecorded delivery was reported as success")
	}
	if !errors.Is(err, errUnrecordedTransition) {
		t.Fatalf("err = %v, want the unrecorded-transition error", err)
	}
	if errors.Is(err, ErrPermanentDelivery) {
		t.Fatal("an unrecorded transition was reported as permanent; the state is recoverable")
	}
	// The fan-out stopped rather than making more external calls while the
	// internal state is known to disagree with what the provider accepted.
	if got := sender.totalCalls(); got != 1 {
		t.Fatalf("%d provider calls, want the fan-out to stop at the first unrecorded one", got)
	}
	if store.ledgerAttempts() != terminalWriteAttempts {
		t.Fatalf("%d ledger writes, want the bounded retry to be exhausted",
			store.ledgerAttempts())
	}
}

// The finding's second scenario: the push service retires an endpoint and the
// invalidation write fails.
//
// Before the fix the event could end as a permanent failure while the
// subscription stayed active, so every future notification tried the dead
// endpoint again. Now the write is recovered and the subscription really is
// retired, with one provider call.
func TestARetirementSurvivesALifecycleBlip(t *testing.T) {
	store := newFakePushStore().withTarget("sub-a").failLifecycleOnce()
	sender := newFakeSender().answering("sub-a", gone())
	f := newDeliveryFixture(t, store, sender)

	err := f.deliver(t, eligibleNotification())

	if !errors.Is(err, ErrPermanentDelivery) {
		t.Fatalf("err = %v, want the permanent failure once the retirement landed", err)
	}
	if got := sender.callsTo("sub-a"); got != 1 {
		t.Fatalf("the provider was called %d times, want once", got)
	}
	if got := store.lifecycleAttempts(); got != 2 {
		t.Fatalf("%d lifecycle writes, want the failure and its retry", got)
	}
	if got := store.status("sub-a"); got != domain.StatusInvalid {
		t.Fatalf("subscription status = %q, want invalid", got)
	}

	// A later notification does not reach the retired endpoint.
	later := eligibleNotification()
	later.ID = "99999999-9999-9999-9999-999999999999"
	_ = f.deliver(t, later)
	if got := sender.callsTo("sub-a"); got != 1 {
		t.Fatalf("a retired endpoint received %d further sends", got-1)
	}
}

// A retirement that never lands is not a retirement.
//
// The subscription is still active, so declaring the endpoint finished would
// leave the two records disagreeing with nothing to reconcile them. The error
// is a storage error and the event is retryable.
func TestARetirementThatNeverLandsIsReportedNotAssumed(t *testing.T) {
	store := newFakePushStore().withTarget("sub-a")
	store.outcomeErr = errFakeStore
	f := newDeliveryFixture(t, store, newFakeSender().answering("sub-a", gone()))

	err := f.deliver(t, eligibleNotification())

	if !errors.Is(err, errUnrecordedTransition) {
		t.Fatalf("err = %v, want the unrecorded-transition error", err)
	}
	if errors.Is(err, ErrPermanentDelivery) {
		t.Fatal("an unrecorded retirement was reported as permanent")
	}
	if got := store.status("sub-a"); got != domain.StatusActive {
		t.Fatalf("status = %q; the fake must not retire what the write refused", got)
	}
}

// Only the two transitions that carry terminal meaning gate the result.
//
// A success stamp or a failure count that does not land costs a stale
// diagnostic column and nothing else. Reporting it as an error would turn a
// retryable attempt into a storage failure and buy nothing.
func TestBestEffortHistoryWritesDoNotGateTheResult(t *testing.T) {
	t.Run("a delivery whose success stamp fails is still a delivery", func(t *testing.T) {
		store := newFakePushStore().withTarget("sub-a")
		store.outcomeErr = errFakeStore
		f := newDeliveryFixture(t, store, newFakeSender())

		if err := f.deliver(t, eligibleNotification()); err != nil {
			t.Fatalf("err = %v, want the delivery reported", err)
		}
		if !store.delivered(testNotification, "sub-a") {
			t.Fatal("the ledger row was not written")
		}
	})

	t.Run("a transient attempt whose failure count fails is still transient", func(t *testing.T) {
		store := newFakePushStore().withTarget("sub-a")
		store.outcomeErr = errFakeStore
		f := newDeliveryFixture(t, store, newFakeSender().answering("sub-a", unavailable()))

		err := f.deliver(t, eligibleNotification())
		if errors.Is(err, errUnrecordedTransition) {
			t.Fatalf("err = %v, want the provider failure rather than a storage error", err)
		}
		if err == nil || errors.Is(err, ErrPermanentDelivery) {
			t.Fatalf("err = %v, want a transient failure", err)
		}
	})
}

// The write retry is bounded, and the bound is attempts rather than time.
func TestTheWriteRetryIsBounded(t *testing.T) {
	calls := 0
	err := retryWrite(context.Background(), func(context.Context) error {
		calls++
		return errFakeStore
	})

	if !errors.Is(err, errFakeStore) {
		t.Fatalf("err = %v, want the write's own failure", err)
	}
	if calls != terminalWriteAttempts {
		t.Fatalf("%d attempts, want %d", calls, terminalWriteAttempts)
	}
}

// A write that recovers stops being retried.
func TestTheWriteRetryStopsAtTheFirstSuccess(t *testing.T) {
	calls := 0
	err := retryWrite(context.Background(), func(context.Context) error {
		calls++
		if calls == 1 {
			return errFakeStore
		}
		return nil
	})

	if err != nil {
		t.Fatalf("err = %v, want the recovered write reported as success", err)
	}
	if calls != 2 {
		t.Fatalf("%d attempts, want the failure and its retry", calls)
	}
}

// A cancelled context ends the retry immediately: it does not sleep past a
// deadline that has already passed, and it does not spin through the remaining
// attempts either.
func TestTheWriteRetryStopsAtCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	calls := 0
	started := time.Now()
	err := retryWrite(ctx, func(context.Context) error {
		calls++
		return errFakeStore
	})
	elapsed := time.Since(started)

	if !errors.Is(err, errFakeStore) {
		t.Fatalf("err = %v, want the write's own failure", err)
	}
	if calls != 1 {
		t.Fatalf("%d attempts under a cancelled context, want one", calls)
	}
	if elapsed >= terminalWriteBackoff {
		t.Fatalf("the retry waited %v past a cancelled context", elapsed)
	}
}

// pause reports whether it actually waited, which is what lets the retry tell a
// backoff from a cancellation.
func TestPauseReportsWhetherItWaited(t *testing.T) {
	if !pause(context.Background(), time.Millisecond) {
		t.Fatal("a completed wait reported a cancellation")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if pause(ctx, time.Hour) {
		t.Fatal("a cancelled wait reported that it completed")
	}
}

// A fan-out abandoned for an unrecorded transition is counted apart from one
// the push services failed: the cause is this deployment's database, and the
// two want different alerts.
func TestAnUnrecordedTransitionIsCountedSeparately(t *testing.T) {
	metrics, shared := newTestMetrics(t)
	store := newFakePushStore().withTarget("sub-a")
	store.ledgerErr = errFakeStore
	deliverer := NewWebPushDeliverer(
		config.WebPushConfig{TTLSeconds: int(testTTL / time.Second)},
		WebPushDeps{Store: store, Sender: newFakeSender(), Metrics: metrics})

	_ = deliverer.Deliver(context.Background(), eligibleNotification())

	want := `nchat_notification_push_fanout_total{result="unrecorded"} 1`
	if body := scrape(t, shared); !strings.Contains(body, want) {
		t.Fatalf("%s was not recorded:\n%s", want, body)
	}
}

// ---------------------------------------------------------------------------
// A 410 about an endpoint the browser has already replaced
// ---------------------------------------------------------------------------

// The finding, exactly as reported.
//
// The worker reads generation 1, the browser re-registers, and the push service
// answers 410 about the endpoint that generation 1 held. The retirement
// compare-and-set matches nothing — and before the fix that was read as
// "subscription is dead", so the event was retired and the live endpoint on
// generation 2 was never told anything.
func TestAStale410DoesNotRetireARotatedSubscription(t *testing.T) {
	store := newFakePushStore().withTarget("sub-a").rotatesDuring("sub-a")
	sender := newFakeSender().answering("sub-a", gone(), delivered())
	f := newDeliveryFixture(t, store, sender)

	notification := eligibleNotification()
	err := f.deliver(t, notification)

	// Not terminal: a live endpoint is still owed this notification.
	if errors.Is(err, ErrPermanentDelivery) {
		t.Fatalf("err = %v, want the event kept eligible for re-evaluation", err)
	}
	if err == nil {
		t.Fatal("err = nil, want the event returned for another pass")
	}
	// The subscription that rotated is still deliverable.
	if got := store.status("sub-a"); got != domain.StatusActive {
		t.Fatalf("status = %q, want the rotated subscription left active", got)
	}
	if store.delivered(testNotification, "sub-a") {
		t.Fatal("a retirement was recorded as a delivery")
	}
	if !strings.Contains(f.logs.String(), "subscription_generation_changed") {
		t.Fatalf("the superseded retirement was not reported:\n%s", f.logs)
	}

	// The next pass reads the new generation and delivers to it.
	notification.Attempt = 2
	if err := f.deliver(t, notification); err != nil {
		t.Fatalf("the retry failed: %v", err)
	}
	if got := sender.callsTo("sub-a"); got != 2 {
		t.Fatalf("%d sends, want the stale 410 and then the new endpoint", got)
	}
	if !store.delivered(testNotification, "sub-a") {
		t.Fatal("the replacement endpoint never received the notification")
	}
}

// The same race with a second browser in the fan-out.
//
// A rotates and answers 410 about its old endpoint; B is delivered. B must be
// durably done and must not be sent to again, while the event stays eligible so
// A's replacement gets its turn.
func TestARotatedEndpointDoesNotCostTheOtherBrowsersTheirDelivery(t *testing.T) {
	store := newFakePushStore().withTargets("sub-a", "sub-b").rotatesDuring("sub-a")
	sender := newFakeSender().answering("sub-a", gone(), delivered())
	f := newDeliveryFixture(t, store, sender)

	notification := eligibleNotification()
	if err := f.deliver(t, notification); err == nil || errors.Is(err, ErrPermanentDelivery) {
		t.Fatalf("err = %v, want the event kept eligible", err)
	}
	if !store.delivered(testNotification, "sub-b") {
		t.Fatal("the browser that succeeded was not recorded")
	}

	notification.Attempt = 2
	if err := f.deliver(t, notification); err != nil {
		t.Fatalf("the retry failed: %v", err)
	}

	if got := sender.callsTo("sub-b"); got != 1 {
		t.Fatalf("sub-b was sent to %d times, want once", got)
	}
	if got := sender.callsTo("sub-a"); got != 2 {
		t.Fatalf("sub-a was sent to %d times, want once per pass", got)
	}
	if store.status("sub-b") != domain.StatusActive {
		t.Fatal("retiring nothing on sub-a disturbed sub-b")
	}
}

// A 410 that really did retire its subscription is still terminal. Without this
// the fix would have traded one bug for its opposite.
func TestA410ThatAppliesIsStillTerminal(t *testing.T) {
	store := newFakePushStore().withTarget("sub-a")
	sender := newFakeSender().answering("sub-a", gone())
	f := newDeliveryFixture(t, store, sender)

	if err := f.deliver(t, eligibleNotification()); !errors.Is(err, ErrPermanentDelivery) {
		t.Fatalf("err = %v, want a permanent failure", err)
	}
	if got := store.status("sub-a"); got != domain.StatusInvalid {
		t.Fatalf("status = %q, want invalid", got)
	}
	if got := sender.callsTo("sub-a"); got != 1 {
		t.Fatalf("%d sends, want one", got)
	}
}

// A 410 for a subscription that is already inactive, or gone entirely, has
// nothing live behind it: terminal, and no further provider call.
func TestA410ForASubscriptionWithNoLiveEndpointIsTerminal(t *testing.T) {
	// Each change lands between the fan-out reading its targets and the answer
	// being recorded, which is where the real race is.
	cases := map[string]func(*fakePushStore) *fakePushStore{
		"disabled by its owner mid-attempt": func(s *fakePushStore) *fakePushStore {
			return s.disabledDuring("sub-a")
		},
		"deleted mid-attempt": func(s *fakePushStore) *fakePushStore {
			return s.deletedDuring("sub-a")
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			store := mutate(newFakePushStore().withTarget("sub-a"))
			sender := newFakeSender().answering("sub-a", gone())
			f := newDeliveryFixture(t, store, sender)

			if err := f.deliver(t, eligibleNotification()); !errors.Is(err, ErrPermanentDelivery) {
				t.Fatalf("err = %v, want a permanent failure", err)
			}
			if got := sender.callsTo("sub-a"); got != 1 {
				t.Fatalf("%d sends, want one", got)
			}
		})
	}
}
