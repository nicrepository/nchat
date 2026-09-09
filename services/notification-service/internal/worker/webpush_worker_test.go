package worker

import (
	"context"
	"testing"
	"time"

	"github.com/nicrepository/nchat/libs/go/platform/notificationevent"
	"github.com/nicrepository/nchat/services/notification-service/internal/config"
	"github.com/nicrepository/nchat/services/notification-service/internal/storage"
)

// Issue #746: the worker and the Web Push channel, together.
//
// The tests above this one exercise the fan-out at its own seam. These run a
// real NotificationWorker against the in-memory outbox — which enforces the
// same state machine and the same lease the database does — with the real
// WebPushDeliverer behind it and a counting sender behind that. What they prove
// is the join: that a provider is reached only for events the pipeline
// authorised, and that the outbox row ends where each kind of failure says it
// should.

// webPushWorker wires a worker to a real deliverer over the two fakes.
func webPushWorker(
	t *testing.T, outbox *fakeOutbox, store *fakePushStore, sender PushSender, evaluator Evaluator,
) *NotificationWorker {
	t.Helper()
	deliverer := NewWebPushDeliverer(
		config.WebPushConfig{TTLSeconds: int(testTTL / time.Second)},
		WebPushDeps{Store: store, Sender: sender, Logger: silentLogger()})
	return NewNotificationWorker(notificationTestConfig(), NotificationWorkerDeps{
		Store:     outbox,
		Evaluator: evaluator,
		Deliverer: deliverer,
		Logger:    silentLogger(),
	})
}

// verdict is a policy that answers the same way for everything.
func verdict(v Verdict) Evaluator {
	return EvaluatorFunc(func(context.Context, Notification) (Verdict, error) { return v, nil })
}

func allow() Evaluator { return verdict(Verdict{Deliver: true, PolicyVersion: 1}) }
func deny(reason string) Evaluator {
	return verdict(Verdict{Deliver: false, SuppressedReason: reason, PolicyVersion: 1})
}

// drain runs enough passes to evaluate and then deliver.
func drain(worker *NotificationWorker, passes int) {
	for range passes {
		worker.runPass()
	}
}

// ---------------------------------------------------------------------------
// The provider is unreachable for anything the policy did not allow
// ---------------------------------------------------------------------------

// The guardrail the issue puts first, proved end to end.
//
// A suppressed event never becomes claimable: MarkEvaluated writes 'suppressed',
// which is terminal, and ClaimDue selects only 'eligible', 'retrying' and
// 'processing'. So the delivery layer is not asked to skip these — it is never
// called at all, and the sender's call count is zero however many passes run.
func TestASuppressedEventNeverReachesTheProvider(t *testing.T) {
	for name, reason := range map[string]string{
		"outside work hours":  "outside_work_hours",
		"muted conversation":  "muted",
		"user preference off": "user_preference",
		"conversation open":   "conversation_open",
		"historical import":   "historical_or_imported",
		"push unavailable":    "unsupported_or_unavailable_channel",
	} {
		t.Run(name, func(t *testing.T) {
			outbox := newFakeOutbox()
			outbox.seedPending("event-1")
			store := newFakePushStore().withTargets("sub-a", "sub-b")
			sender := newFakeSender()

			drain(webPushWorker(t, outbox, store, sender, deny(reason)), 4)

			if got := sender.totalCalls(); got != 0 {
				t.Fatalf("a %s event produced %d provider calls", name, got)
			}
			if state := outbox.snapshot("event-1").state; state != notificationevent.StateSuppressed {
				t.Fatalf("state = %q, want suppressed", state)
			}
			if store.ledgerSize() != 0 {
				t.Fatal("a suppressed event was recorded as delivered")
			}
		})
	}
}

// An event the policy allowed is delivered to every browser and the row ends
// sent. This is the control the test above needs to be meaningful: the pipeline
// does reach the provider when it should.
func TestAnAllowedEventReachesEveryBrowser(t *testing.T) {
	outbox := newFakeOutbox()
	outbox.seedPending("event-1")
	store := newFakePushStore().withTargets("sub-a", "sub-b")
	sender := newFakeSender()

	drain(webPushWorker(t, outbox, store, sender, allow()), 2)

	if got := sender.totalCalls(); got != 2 {
		t.Fatalf("%d provider calls, want one per browser", got)
	}
	if state := outbox.snapshot("event-1").state; state != notificationevent.StateSent {
		t.Fatalf("state = %q, want sent", state)
	}
}

// An event that expired before the worker got to it is retired without a
// provider call. The row is terminal, not retried: waiting cannot make it
// younger.
func TestAnExpiredEventIsRetiredWithoutAProviderCall(t *testing.T) {
	outbox := newFakeOutbox()
	seedAged(outbox, "event-1", 4*testTTL)
	store := newFakePushStore().withTarget("sub-a")
	sender := newFakeSender()

	drain(webPushWorker(t, outbox, store, sender, allow()), 3)

	if got := sender.totalCalls(); got != 0 {
		t.Fatalf("an expired event produced %d provider calls", got)
	}
	if state := outbox.snapshot("event-1").state; state != notificationevent.StateFailed {
		t.Fatalf("state = %q, want failed", state)
	}
}

// ---------------------------------------------------------------------------
// How each failure lands in the outbox
// ---------------------------------------------------------------------------

// A retired endpoint is terminal for the row when it is the only one, and the
// worker does not insist on it: no retry is scheduled and nothing is attempted
// again.
func TestARetiredEndpointRetiresTheRowAndIsNotInsistedOn(t *testing.T) {
	outbox := newFakeOutbox()
	outbox.seedPending("event-1")
	store := newFakePushStore().withTarget("sub-a")
	sender := newFakeSender().answering("sub-a", gone())

	drain(webPushWorker(t, outbox, store, sender, allow()), 6)

	if got := sender.callsTo("sub-a"); got != 1 {
		t.Fatalf("a retired endpoint was attempted %d times, want once", got)
	}
	row := outbox.snapshot("event-1")
	if row.state != notificationevent.StateFailed {
		t.Fatalf("state = %q, want failed", row.state)
	}
	if row.event.Attempts != 1 {
		t.Fatalf("attempts = %d, want the single attempt", row.event.Attempts)
	}
}

// A push service having a bad afternoon feeds the worker's retry ladder, and
// the ladder ends: the event reaches the attempt ceiling and is retired rather
// than retried forever.
func TestAProviderOutageRetriesUpToTheCeiling(t *testing.T) {
	outbox := newFakeOutbox()
	outbox.seedPending("event-1")
	store := newFakePushStore().withTarget("sub-a")
	sender := newFakeSender().answering("sub-a", unavailable())

	worker := webPushWorker(t, outbox, store, sender, allow())
	cfg := notificationTestConfig()

	worker.runPass() // evaluate, then claim and fail the first attempt
	for range cfg.MaxAttempts + 3 {
		makeDue(outbox, "event-1")
		worker.runPass()
	}

	row := outbox.snapshot("event-1")
	if row.state != notificationevent.StateFailed {
		t.Fatalf("state = %q, want failed after the attempts ran out", row.state)
	}
	if row.event.Attempts > cfg.MaxAttempts {
		t.Fatalf("attempts = %d, over the ceiling of %d", row.event.Attempts, cfg.MaxAttempts)
	}
	if sender.callsTo("sub-a") > cfg.MaxAttempts {
		t.Fatalf("the provider was called %d times, over the attempt ceiling",
			sender.callsTo("sub-a"))
	}
}

// A rate limiter's Retry-After survives the whole path: the sender normalises
// it, the fan-out carries the largest, and the worker schedules against it
// rather than against its own backoff alone.
func TestARateLimitSchedulesAgainstItsRetryAfter(t *testing.T) {
	outbox := newFakeOutbox()
	outbox.seedPending("event-1")
	store := newFakePushStore().withTarget("sub-a")
	sender := newFakeSender().answering("sub-a", rateLimited(4*time.Minute))

	drain(webPushWorker(t, outbox, store, sender, allow()), 2)

	row := outbox.snapshot("event-1")
	if row.state != notificationevent.StateRetrying {
		t.Fatalf("state = %q, want retrying", row.state)
	}
	// The configured backoff at attempt one is thirty seconds before jitter, so
	// a next attempt minutes away can only have come from the header.
	if wait := time.Until(row.nextAttemptAt); wait < 3*time.Minute {
		t.Fatalf("next attempt is in %v, want the rate limiter's four minutes", wait)
	}
}

// Partial success across a whole worker pass: one browser is delivered, one is
// owed a retry, and the retry reaches only the one still owed.
func TestPartialSuccessRetriesOnlyWhatIsStillOwed(t *testing.T) {
	outbox := newFakeOutbox()
	outbox.seedPending("event-1")
	store := newFakePushStore().withTargets("sub-a", "sub-b")
	sender := newFakeSender().answering("sub-a", unavailable(), delivered())

	worker := webPushWorker(t, outbox, store, sender, allow())
	drain(worker, 2)

	if outbox.snapshot("event-1").state != notificationevent.StateRetrying {
		t.Fatalf("state = %q, want retrying while one browser is still owed",
			outbox.snapshot("event-1").state)
	}

	makeDue(outbox, "event-1")
	worker.runPass()

	if got := sender.callsTo("sub-b"); got != 1 {
		t.Fatalf("the delivered browser was sent to %d times, want once", got)
	}
	if got := sender.callsTo("sub-a"); got != 2 {
		t.Fatalf("the retried browser was sent to %d times, want once per pass", got)
	}
	if outbox.snapshot("event-1").state != notificationevent.StateSent {
		t.Fatalf("state = %q, want sent once every browser has it",
			outbox.snapshot("event-1").state)
	}
}

// A worker restarting mid-flight does not duplicate what was already
// delivered. The ledger is the state that survives, and the second worker's
// fan-out excludes what the first one recorded.
func TestARestartDoesNotResendWhatWasAlreadyDelivered(t *testing.T) {
	outbox := newFakeOutbox()
	outbox.seedPending("event-1")
	store := newFakePushStore().withTargets("sub-a", "sub-b")
	sender := newFakeSender().answering("sub-a", unavailable(), delivered())

	drain(webPushWorker(t, outbox, store, sender, allow()), 2)

	// A different worker instance, as a restart produces. Only the fakes
	// standing in for the database carry anything across.
	makeDue(outbox, "event-1")
	webPushWorker(t, outbox, store, sender, allow()).runPass()

	if got := sender.callsTo("sub-b"); got != 1 {
		t.Fatalf("a restart re-sent to a delivered browser %d times", got-1)
	}
}

// makeDue brings a retrying row forward so the next pass claims it, which is
// what waiting out the backoff would do.
func makeDue(outbox *fakeOutbox, id string) {
	outbox.mu.Lock()
	defer outbox.mu.Unlock()
	if row, ok := outbox.rows[id]; ok {
		row.nextAttemptAt = time.Now().Add(-time.Second)
	}
}

// seedAged adds a pending event that happened some time ago, which is how an
// event outlives its own TTL without a test waiting for one.
func seedAged(outbox *fakeOutbox, id string, age time.Duration) {
	outbox.seedPending(id)
	outbox.mu.Lock()
	defer outbox.mu.Unlock()
	outbox.rows[id].event.OccurredAt = time.Now().Add(-age)
}

var _ storage.NotificationOutboxStore = (*fakeOutbox)(nil)

// ---------------------------------------------------------------------------
// The real duplication bound when a delivery cannot be written down
// ---------------------------------------------------------------------------

// A ledger that never accepts a write costs one duplicate push per remaining
// outbox attempt — and this test exists to state that number honestly rather
// than let the documentation imply a smaller one.
//
// The provider accepts every time. MarkDelivered fails every time, including
// through the bounded write retry. Nothing durable ever records the accept, so
// no later pass can know it happened, and each retry sends again. What bounds
// it is the worker's own attempt ceiling, not any guarantee Web Push offers.
func TestAnUnrecordableDeliveryIsResentOncePerRemainingAttempt(t *testing.T) {
	outbox := newFakeOutbox()
	outbox.seedPending("event-1")
	store := newFakePushStore().withTarget("sub-a")
	store.ledgerErr = errFakeStore
	sender := newFakeSender()

	worker := webPushWorker(t, outbox, store, sender, allow())
	cfg := notificationTestConfig()

	// One pass evaluates and takes the first attempt; each later pass is a
	// retry brought forward past its backoff.
	worker.runPass()
	for range cfg.MaxAttempts + 2 {
		makeDue(outbox, "event-1")
		worker.runPass()
	}

	row := outbox.snapshot("event-1")
	if row.state != notificationevent.StateFailed {
		t.Fatalf("state = %q, want failed once the attempts ran out", row.state)
	}
	if row.event.Attempts > cfg.MaxAttempts {
		t.Fatalf("attempts = %d, over the ceiling of %d", row.event.Attempts, cfg.MaxAttempts)
	}

	// One send per attempt: the duplication is real, and it is bounded by the
	// retry budget rather than by anything the provider promises.
	sends := sender.callsTo("sub-a")
	if sends != row.event.Attempts {
		t.Fatalf("%d sends across %d attempts, want one per attempt",
			sends, row.event.Attempts)
	}
	if sends > cfg.MaxAttempts {
		t.Fatalf("%d sends, over the attempt ceiling of %d — unbounded amplification",
			sends, cfg.MaxAttempts)
	}
	if store.ledgerSize() != 0 {
		t.Fatal("a ledger that refused every write recorded a delivery")
	}
}

// The contrast that makes the bound above meaningful: a storage blip recovered
// inside one execution costs no duplicate at all. The provider is called once
// and the event completes.
func TestARecoveredLedgerBlipCostsNoDuplicate(t *testing.T) {
	outbox := newFakeOutbox()
	outbox.seedPending("event-1")
	store := newFakePushStore().withTarget("sub-a").failLedgerOnce()
	sender := newFakeSender()

	worker := webPushWorker(t, outbox, store, sender, allow())
	worker.runPass()
	makeDue(outbox, "event-1")
	worker.runPass()

	if got := sender.callsTo("sub-a"); got != 1 {
		t.Fatalf("%d sends, want one — the write was retried, not the push", got)
	}
	if outbox.snapshot("event-1").state != notificationevent.StateSent {
		t.Fatalf("state = %q, want sent", outbox.snapshot("event-1").state)
	}
}
