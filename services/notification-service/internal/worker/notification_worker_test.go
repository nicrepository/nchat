package worker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nicrepository/nchat/libs/go/platform/notificationevent"
	"github.com/nicrepository/nchat/services/notification-service/internal/config"
	"github.com/nicrepository/nchat/services/notification-service/internal/storage"
)

// Issue #742: what the worker does, not which methods it calls.
//
// Every test drives real passes against the in-memory outbox in
// notification_outbox_fake_test.go, which enforces the same state machine and
// the same lease the database does, and asserts the rows that come out. The one
// thing a fake cannot prove — that SKIP LOCKED makes a claim exclusive — is
// proved against a real PostgreSQL in the storage package instead.

func silentLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// notificationTestConfig is a fast, coherent configuration: the lease still covers a
// pass, so the worker will actually run.
func notificationTestConfig() config.NotificationWorkerConfig {
	return config.NotificationWorkerConfig{
		Enabled:                true,
		PollSeconds:            1,
		BatchSize:              5,
		MaxConcurrency:         5,
		LeaseSeconds:           60,
		MaxAttempts:            3,
		RetryBaseSeconds:       30,
		RetryMaxSeconds:        300,
		DeliveryTimeoutSeconds: 1,
	}.Normalized()
}

// recordingDeliverer remembers every idempotency key it was handed.
type recordingDeliverer struct {
	mu   sync.Mutex
	keys []string
	err  error
	// hold, when set, blocks each delivery until it is closed, so concurrency
	// can be observed rather than inferred.
	hold chan struct{}
	// inFlight tracks simultaneous deliveries; peak is the high-water mark.
	inFlight atomic.Int64
	peak     atomic.Int64
}

func (d *recordingDeliverer) Deliver(ctx context.Context, notification Notification) error {
	current := d.inFlight.Add(1)
	for {
		peak := d.peak.Load()
		if current <= peak || d.peak.CompareAndSwap(peak, current) {
			break
		}
	}
	defer d.inFlight.Add(-1)

	d.mu.Lock()
	d.keys = append(d.keys, notification.IdempotencyKey())
	err := d.err
	d.mu.Unlock()

	if d.hold != nil {
		select {
		case <-d.hold:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return err
}

func (d *recordingDeliverer) delivered() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.keys...)
}

func newTestWorker(
	t *testing.T, outbox *fakeOutbox, deliverer Deliverer, evaluator Evaluator,
) *NotificationWorker {
	t.Helper()
	return NewNotificationWorker(notificationTestConfig(), NotificationWorkerDeps{
		Store:     outbox,
		Evaluator: evaluator,
		Deliverer: deliverer,
		Logger:    silentLogger(),
	})
}

// ---------------------------------------------------------------------------
// The happy path, and the one that must never reach a provider
// ---------------------------------------------------------------------------

func TestWorkerEvaluatesThenDeliversAPendingEvent(t *testing.T) {
	outbox := newFakeOutbox()
	outbox.seedPending("n1")
	deliverer := &recordingDeliverer{}
	worker := newTestWorker(t, outbox, deliverer, nil)

	// One pass is enough: evaluation runs before the claim, so an event a
	// producer wrote is decided and delivered without waiting for a second tick.
	worker.runPass()

	if got := outbox.snapshot("n1").state; got != notificationevent.StateSent {
		t.Fatalf("state = %q, want %q", got, notificationevent.StateSent)
	}
	if keys := deliverer.delivered(); len(keys) != 1 || keys[0] != "n1" {
		t.Fatalf("delivered %v, want exactly [n1]", keys)
	}
}

// A suppressed event is a successful outcome that must never reach a provider.
func TestWorkerNeverDeliversASuppressedEvent(t *testing.T) {
	outbox := newFakeOutbox()
	outbox.seedPending("n1")
	deliverer := &recordingDeliverer{}
	suppressing := EvaluatorFunc(func(context.Context, Notification) (Verdict, error) {
		return Verdict{Deliver: false, SuppressedReason: "quiet_hours"}, nil
	})
	worker := newTestWorker(t, outbox, deliverer, suppressing)

	worker.runPass()
	worker.runPass()

	row := outbox.snapshot("n1")
	if row.state != notificationevent.StateSuppressed {
		t.Fatalf("state = %q, want %q", row.state, notificationevent.StateSuppressed)
	}
	if row.reason != "quiet_hours" {
		t.Fatalf("suppressed reason = %q, want the policy's own", row.reason)
	}
	if keys := deliverer.delivered(); len(keys) != 0 {
		t.Fatalf("a suppressed event was delivered: %v", keys)
	}
}

// A policy that suppresses without saying why must not leave the row stuck in
// pending, re-decided on every pass forever.
func TestWorkerRecordsAnUnexplainedSuppression(t *testing.T) {
	outbox := newFakeOutbox()
	outbox.seedPending("n1")
	worker := newTestWorker(t, outbox, &recordingDeliverer{},
		EvaluatorFunc(func(context.Context, Notification) (Verdict, error) {
			return Verdict{Deliver: false}, nil
		}))

	worker.runPass()

	row := outbox.snapshot("n1")
	if row.state != notificationevent.StateSuppressed || row.reason == "" {
		t.Fatalf("row = %+v, want a suppression carrying a reason", row)
	}
}

// A policy that fails leaves the event pending, to be decided again. It must not
// be delivered, and it must not be suppressed on a decision nobody made.
func TestWorkerLeavesAnUndecidedEventPending(t *testing.T) {
	outbox := newFakeOutbox()
	outbox.seedPending("n1")
	deliverer := &recordingDeliverer{}
	worker := newTestWorker(t, outbox, deliverer,
		EvaluatorFunc(func(context.Context, Notification) (Verdict, error) {
			return Verdict{}, errors.New("policy backend unreachable")
		}))

	worker.runPass()

	if got := outbox.snapshot("n1").state; got != notificationevent.StatePending {
		t.Fatalf("state = %q, want it left pending", got)
	}
	if keys := deliverer.delivered(); len(keys) != 0 {
		t.Fatalf("an undecided event was delivered: %v", keys)
	}
}

// ---------------------------------------------------------------------------
// Retry
// ---------------------------------------------------------------------------

func TestWorkerRetriesATransientFailureWithAPersistedSchedule(t *testing.T) {
	outbox := newFakeOutbox()
	outbox.seedEligible("n1")
	worker := newTestWorker(t, outbox,
		&recordingDeliverer{err: errors.New("provider said 503")}, nil)

	worker.runPass()

	row := outbox.snapshot("n1")
	if row.state != notificationevent.StateRetrying {
		t.Fatalf("state = %q, want %q", row.state, notificationevent.StateRetrying)
	}
	if row.lastError != CategoryTransient {
		t.Fatalf("last error = %q, want %q", row.lastError, CategoryTransient)
	}
	if row.event.Attempts != 1 {
		t.Fatalf("attempts = %d, want 1", row.event.Attempts)
	}
	if !row.nextAttemptAt.After(time.Now()) {
		t.Fatal("the next attempt was not scheduled into the future")
	}
}

// A retried event must not be claimable again until its schedule says so; that
// is what stops a failing provider from being hammered.
func TestWorkerHonoursThePersistedRetrySchedule(t *testing.T) {
	outbox := newFakeOutbox()
	outbox.seedEligible("n1")
	deliverer := &recordingDeliverer{err: errors.New("provider said 503")}
	worker := newTestWorker(t, outbox, deliverer, nil)

	worker.runPass()
	worker.runPass()
	worker.runPass()

	if attempts := len(deliverer.delivered()); attempts != 1 {
		t.Fatalf("%d delivery attempts in three passes, want 1 — the backoff was ignored", attempts)
	}
}

func TestWorkerRetiresAPermanentFailureImmediately(t *testing.T) {
	outbox := newFakeOutbox()
	outbox.seedEligible("n1")
	worker := newTestWorker(t, outbox,
		&recordingDeliverer{err: fmt.Errorf("recipient gone: %w", ErrPermanentDelivery)}, nil)

	worker.runPass()

	row := outbox.snapshot("n1")
	if row.state != notificationevent.StateFailed {
		t.Fatalf("state = %q, want %q on the first permanent failure", row.state, notificationevent.StateFailed)
	}
	if row.lastError != CategoryPermanent {
		t.Fatalf("last error = %q, want %q", row.lastError, CategoryPermanent)
	}
}

// A permanent failure is terminal, so nothing may pick it up again.
func TestWorkerNeverRetriesAPermanentFailure(t *testing.T) {
	outbox := newFakeOutbox()
	outbox.seedEligible("n1")
	deliverer := &recordingDeliverer{err: fmt.Errorf("gone: %w", ErrPermanentDelivery)}
	worker := newTestWorker(t, outbox, deliverer, nil)

	for i := 0; i < 5; i++ {
		worker.runPass()
	}

	if attempts := len(deliverer.delivered()); attempts != 1 {
		t.Fatalf("a permanently failed event was attempted %d times", attempts)
	}
}

// Transient failures are bounded. The ceiling is what makes retry a policy
// rather than a loop.
func TestWorkerFailsAnEventThatExhaustsItsAttempts(t *testing.T) {
	outbox := newFakeOutbox()
	clock := &notificationTestClock{now: time.Now()}
	outbox.now = clock.Now
	outbox.seedEligible("n1")
	deliverer := &recordingDeliverer{err: errors.New("provider said 503")}
	worker := newTestWorker(t, outbox, deliverer, nil)

	// Each pass fails once and schedules the next attempt; the clock is moved
	// past that schedule rather than waiting for it.
	for i := 0; i < notificationTestConfig().MaxAttempts; i++ {
		worker.runPass()
		clock.advance(time.Hour)
	}

	row := outbox.snapshot("n1")
	if row.state != notificationevent.StateFailed {
		t.Fatalf("state = %q after exhausting attempts, want %q", row.state, notificationevent.StateFailed)
	}
	if attempts := len(deliverer.delivered()); attempts != notificationTestConfig().MaxAttempts {
		t.Fatalf("%d attempts, want the configured ceiling of %d",
			attempts, notificationTestConfig().MaxAttempts)
	}

	// And nothing picks it up afterwards.
	worker.runPass()
	if attempts := len(deliverer.delivered()); attempts != notificationTestConfig().MaxAttempts {
		t.Fatalf("a failed event was attempted again: %d attempts", attempts)
	}
}

// ---------------------------------------------------------------------------
// Resilience: an abandoned claim
// ---------------------------------------------------------------------------

// A worker that dies mid-delivery leaves a claim nobody finalises. The event
// must not be lost, and it must not be stuck.
func TestWorkerRecoversAnAbandonedClaimWhenItsLeaseExpires(t *testing.T) {
	outbox := newFakeOutbox()
	clock := &notificationTestClock{now: time.Now()}
	outbox.now = clock.Now
	outbox.seedEligible("n1")

	// A first worker claims and disappears without finalising.
	if _, err := outbox.ClaimDue(context.Background(), 5, 3, time.Minute); err != nil {
		t.Fatalf("ClaimDue: %v", err)
	}
	if got := outbox.snapshot("n1").state; got != notificationevent.StateProcessing {
		t.Fatalf("state = %q, want the row claimed", got)
	}

	deliverer := &recordingDeliverer{}
	worker := newTestWorker(t, outbox, deliverer, nil)

	// Still inside the lease: the event belongs to the worker that vanished.
	worker.runPass()
	if keys := deliverer.delivered(); len(keys) != 0 {
		t.Fatalf("a leased event was taken from its holder: %v", keys)
	}

	clock.advance(2 * time.Minute)
	worker.runPass()

	if keys := deliverer.delivered(); len(keys) != 1 {
		t.Fatalf("delivered %v after the lease expired, want the event recovered", keys)
	}
	if got := outbox.snapshot("n1").state; got != notificationevent.StateSent {
		t.Fatalf("state = %q, want %q", got, notificationevent.StateSent)
	}
}

// An event that kills its worker every time still has to stop. Attempts are
// counted at claim time precisely so a claim nobody finalises still burns one.
func TestWorkerRetiresAClaimNobodyEverFinalises(t *testing.T) {
	outbox := newFakeOutbox()
	clock := &notificationTestClock{now: time.Now()}
	outbox.now = clock.Now
	outbox.seedEligible("n1")
	worker := newTestWorker(t, outbox, &recordingDeliverer{}, nil)

	// Simulate the worker dying immediately after every claim.
	for i := 0; i < notificationTestConfig().MaxAttempts; i++ {
		if _, err := outbox.ClaimDue(context.Background(), 5, notificationTestConfig().MaxAttempts, time.Minute); err != nil {
			t.Fatalf("ClaimDue: %v", err)
		}
		clock.advance(2 * time.Minute)
	}

	worker.runPass()

	row := outbox.snapshot("n1")
	if row.state != notificationevent.StateFailed {
		t.Fatalf("state = %q, want an exhausted claim retired as %q",
			row.state, notificationevent.StateFailed)
	}
	if row.lastError != "attempts_exhausted" {
		t.Fatalf("last error = %q, want it to say the attempts ran out", row.lastError)
	}
}

// ---------------------------------------------------------------------------
// Idempotency
// ---------------------------------------------------------------------------

// The identity handed to an adapter is the notification's, not the attempt's.
func TestWorkerPresentsTheSameIdempotencyKeyOnEveryAttempt(t *testing.T) {
	outbox := newFakeOutbox()
	clock := &notificationTestClock{now: time.Now()}
	outbox.now = clock.Now
	outbox.seedEligible("n1")
	deliverer := &recordingDeliverer{err: errors.New("provider said 503")}
	worker := newTestWorker(t, outbox, deliverer, nil)

	for i := 0; i < 3; i++ {
		worker.runPass()
		clock.advance(time.Hour)
	}

	keys := deliverer.delivered()
	if len(keys) < 2 {
		t.Fatalf("only %d attempts were made", len(keys))
	}
	for _, key := range keys {
		if key != "n1" {
			t.Fatalf("attempt carried key %q, want the stable notification id", key)
		}
	}
}

// idempotentDeliverer is an adapter that deduplicates on the key itself — the
// contract Deliverer states for providers that offer no idempotency of their
// own. Given a stable key, retries must collapse into one logical delivery.
type idempotentDeliverer struct {
	mu        sync.Mutex
	seen      map[string]struct{}
	logical   int
	failUntil int
	attempts  int
}

func (d *idempotentDeliverer) deliverKeyed(key string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.attempts++
	if d.attempts <= d.failUntil {
		return errors.New("provider said 503")
	}
	if _, done := d.seen[key]; done {
		return nil
	}
	d.seen[key] = struct{}{}
	d.logical++
	return nil
}

func TestWorkerRetryProducesOneLogicalDeliveryAtAnIdempotentAdapter(t *testing.T) {
	adapter := &idempotentDeliverer{seen: map[string]struct{}{}, failUntil: 2}
	outbox := newFakeOutbox()
	clock := &notificationTestClock{now: time.Now()}
	outbox.now = clock.Now
	outbox.seedEligible("n1")

	worker := newTestWorker(t, outbox, DelivererFunc(func(_ context.Context, n Notification) error {
		return adapter.deliverKeyed(n.IdempotencyKey())
	}), nil)

	for i := 0; i < 3; i++ {
		worker.runPass()
		clock.advance(time.Hour)
	}

	if adapter.attempts < 3 {
		t.Fatalf("%d attempts, want the failures to have been retried", adapter.attempts)
	}
	if adapter.logical != 1 {
		t.Fatalf("%d logical deliveries across %d attempts, want exactly 1",
			adapter.logical, adapter.attempts)
	}
	if got := outbox.snapshot("n1").state; got != notificationevent.StateSent {
		t.Fatalf("state = %q, want %q", got, notificationevent.StateSent)
	}
}

// Two workers sharing one outbox must not both deliver the same event: the
// claim is exclusive, so the second finds nothing.
func TestTwoWorkersDoNotDeliverTheSameEvent(t *testing.T) {
	outbox := newFakeOutbox()
	outbox.seedEligible("n1")
	deliverer := &recordingDeliverer{}
	first := newTestWorker(t, outbox, deliverer, nil)
	second := newTestWorker(t, outbox, deliverer, nil)

	var started sync.WaitGroup
	started.Add(2)
	for _, w := range []*NotificationWorker{first, second} {
		go func() {
			defer started.Done()
			w.runPass()
		}()
	}
	started.Wait()

	if keys := deliverer.delivered(); len(keys) != 1 {
		t.Fatalf("the event was delivered %d times: %v", len(keys), keys)
	}
}

// ---------------------------------------------------------------------------
// Backpressure
// ---------------------------------------------------------------------------

func TestWorkerNeverClaimsMoreThanOneBatch(t *testing.T) {
	outbox := newFakeOutbox()
	for i := 0; i < 25; i++ {
		outbox.seedEligible(fmt.Sprintf("n%02d", i))
	}
	deliverer := &recordingDeliverer{}
	worker := newTestWorker(t, outbox, deliverer, nil)

	worker.runPass()

	if delivered := len(deliverer.delivered()); delivered != notificationTestConfig().BatchSize {
		t.Fatalf("one pass delivered %d events, want the batch size of %d",
			delivered, notificationTestConfig().BatchSize)
	}
}

func TestWorkerRespectsItsConcurrencyLimit(t *testing.T) {
	outbox := newFakeOutbox()
	for i := 0; i < 12; i++ {
		outbox.seedEligible(fmt.Sprintf("n%02d", i))
	}

	cfg := notificationTestConfig()
	cfg.BatchSize = 12
	cfg.MaxConcurrency = 3
	// The lease has to cover four sequential waves for the worker to run at all.
	cfg.LeaseSeconds = 120
	deliverer := &recordingDeliverer{hold: make(chan struct{})}
	worker := NewNotificationWorker(cfg, NotificationWorkerDeps{
		Store: outbox, Deliverer: deliverer, Logger: silentLogger(),
	})

	done := make(chan struct{})
	go func() {
		defer close(done)
		worker.runPass()
	}()

	// Let the first wave fill up, then release everything.
	waitUntil(t, func() bool { return deliverer.inFlight.Load() >= int64(cfg.MaxConcurrency) })
	close(deliverer.hold)
	<-done

	if peak := deliverer.peak.Load(); peak > int64(cfg.MaxConcurrency) {
		t.Fatalf("%d deliveries were in flight at once, past the limit of %d",
			peak, cfg.MaxConcurrency)
	}
	if delivered := len(deliverer.delivered()); delivered != 12 {
		t.Fatalf("delivered %d of 12 events", delivered)
	}
}

// An empty queue must cost one claim per tick, not a spin. This is the
// difference between a poll interval and a busy loop.
func TestIdleWorkerDoesNotPollAggressively(t *testing.T) {
	outbox := newFakeOutbox()
	worker := newTestWorker(t, outbox, &recordingDeliverer{}, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		worker.Start(ctx)
	}()

	// Two ticks' worth of an empty queue. A worker that spun would run thousands
	// of claims in that window.
	time.Sleep(250 * time.Millisecond)
	cancel()
	<-stopped

	if claims := outbox.claimCount(); claims > 2 {
		t.Fatalf("%d claims against an empty queue in 250ms; the worker is spinning", claims)
	}
}

// A database that is refusing must not become a tight retry loop against itself.
func TestWorkerDoesNotSpinWhenTheStoreIsUnavailable(t *testing.T) {
	outbox := newFakeOutbox()
	outbox.fail("claim", errStoreUnavailable)
	outbox.fail("list", errStoreUnavailable)
	outbox.fail("backlog", errStoreUnavailable)
	outbox.fail("exhaust", errStoreUnavailable)
	worker := newTestWorker(t, outbox, &recordingDeliverer{}, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		worker.Start(ctx)
	}()

	time.Sleep(250 * time.Millisecond)
	cancel()
	<-stopped

	if claims := outbox.claimCount(); claims > 2 {
		t.Fatalf("%d claims in 250ms against a failing store; the worker is spinning", claims)
	}
}

// A finalisation that lands after the lease was lost must not be treated as a
// fault, and must not corrupt the row the other worker now owns.
func TestWorkerToleratesLosingAClaimMidDelivery(t *testing.T) {
	outbox := newFakeOutbox()
	outbox.seedEligible("n1")
	outbox.fail("finalise", storageConflict())
	worker := newTestWorker(t, outbox, &recordingDeliverer{}, nil)

	worker.runPass()

	if got := outbox.snapshot("n1").state; got != notificationevent.StateProcessing {
		t.Fatalf("state = %q, want the row left to its lease", got)
	}
}

// ---------------------------------------------------------------------------
// Shutdown
// ---------------------------------------------------------------------------

// Cancelling the context stops the worker claiming anything new, and the
// goroutine ends. Anything it was holding stays recoverable through its lease.
func TestWorkerStopsClaimingOnCancellation(t *testing.T) {
	outbox := newFakeOutbox()
	for i := 0; i < 5; i++ {
		outbox.seedEligible(fmt.Sprintf("n%d", i))
	}
	worker := newTestWorker(t, outbox, &recordingDeliverer{}, nil)

	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		worker.Start(ctx)
	}()

	cancel()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("the worker did not return after its context was cancelled")
	}

	claimsAtStop := outbox.claimCount()
	time.Sleep(50 * time.Millisecond)
	if outbox.claimCount() != claimsAtStop {
		t.Fatal("the worker claimed again after it had stopped")
	}
}

// A lease that cannot cover a batch of deliveries is the configuration that
// lets two workers hold one event. The worker refuses to run on it rather than
// duplicating deliveries quietly.
func TestWorkerRefusesToRunOnALeaseItCannotHonour(t *testing.T) {
	cfg := notificationTestConfig()
	cfg.BatchSize = 200
	cfg.MaxConcurrency = 1
	cfg.DeliveryTimeoutSeconds = 120
	cfg.LeaseSeconds = 60
	outbox := newFakeOutbox()
	outbox.seedEligible("n1")
	worker := NewNotificationWorker(cfg, NotificationWorkerDeps{
		Store: outbox, Deliverer: &recordingDeliverer{}, Logger: silentLogger(),
	})

	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		worker.Start(context.Background())
	}()

	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("the worker started on a lease it cannot honour")
	}
	if claims := outbox.claimCount(); claims != 0 {
		t.Fatalf("%d claims were made before the worker refused", claims)
	}
}

// ---------------------------------------------------------------------------
// Observability
// ---------------------------------------------------------------------------

// The backlog gauge is read every pass, and a failure to read it must not stop
// the pass: not knowing the depth is no reason to stop draining.
func TestWorkerKeepsDeliveringWhenTheBacklogGaugeFails(t *testing.T) {
	outbox := newFakeOutbox()
	outbox.seedEligible("n1")
	outbox.fail("backlog", errStoreUnavailable)
	deliverer := &recordingDeliverer{}
	worker := newTestWorker(t, outbox, deliverer, nil)

	worker.runPass()

	if keys := deliverer.delivered(); len(keys) != 1 {
		t.Fatalf("delivered %v, want the pass to have continued", keys)
	}
}

// A nil metrics set is the metrics-disabled configuration. Nothing may panic.
func TestWorkerRunsWithoutMetrics(t *testing.T) {
	outbox := newFakeOutbox()
	outbox.seedPending("n1")
	worker := NewNotificationWorker(notificationTestConfig(), NotificationWorkerDeps{
		Store: outbox, Deliverer: &recordingDeliverer{}, Logger: silentLogger(),
	})

	worker.runPass()
	worker.runPass()

	if got := outbox.snapshot("n1").state; got != notificationevent.StateSent {
		t.Fatalf("state = %q, want %q", got, notificationevent.StateSent)
	}
}

// A worker built without a logger or an evaluator must still be usable: the
// constructor supplies the defaults rather than leaving a nil to dereference.
func TestNewNotificationWorkerSuppliesItsDefaults(t *testing.T) {
	outbox := newFakeOutbox()
	outbox.seedPending("n1")
	worker := NewNotificationWorker(config.NotificationWorkerConfig{}, NotificationWorkerDeps{
		Store: outbox, Deliverer: &recordingDeliverer{},
	})

	worker.runPass()

	// The default policy suppresses nothing, so the event is decided and
	// delivered in the one pass.
	if got := outbox.snapshot("n1").state; got != notificationevent.StateSent {
		t.Fatalf("state = %q, want the default policy to have delivered it", got)
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// DelivererFunc adapts a function to Deliverer, for tests that need one line.
type DelivererFunc func(ctx context.Context, notification Notification) error

// Deliver calls f.
func (f DelivererFunc) Deliver(ctx context.Context, notification Notification) error {
	return f(ctx, notification)
}

// notificationTestClock is the injectable now the fake outbox reads, so a lease can be
// expired and a retry schedule reached without sleeping through either.
type notificationTestClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *notificationTestClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *notificationTestClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// waitUntil blocks until condition holds, or fails the test.
func waitUntil(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition was never met")
}

// storageConflict is the error the store returns when a compare-and-set matched
// nothing.
func storageConflict() error { return storage.ErrNotificationStateConflict }

// A pass where every database call fails must degrade rather than crash: it
// records nothing, delivers nothing, and comes back next tick.
func TestWorkerSurvivesAPassWhereEveryStoreCallFails(t *testing.T) {
	outbox := newFakeOutbox()
	outbox.seedPending("n1")
	for _, operation := range []string{"backlog", "exhaust", "list", "claim"} {
		outbox.fail(operation, errStoreUnavailable)
	}
	deliverer := &recordingDeliverer{}
	worker := newTestWorker(t, outbox, deliverer, nil)

	worker.runPass()

	if keys := deliverer.delivered(); len(keys) != 0 {
		t.Fatalf("delivered %v while the store was refusing", keys)
	}
	if got := outbox.snapshot("n1").state; got != notificationevent.StatePending {
		t.Fatalf("state = %q, want the event untouched", got)
	}
}

// A decision the store refuses to record must leave the event pending, to be
// decided again — never delivered on a verdict nobody wrote down.
func TestWorkerLeavesAnUnrecordableDecisionPending(t *testing.T) {
	outbox := newFakeOutbox()
	outbox.seedPending("n1")
	outbox.fail("evaluate", errStoreUnavailable)
	deliverer := &recordingDeliverer{}
	worker := newTestWorker(t, outbox, deliverer, nil)

	worker.runPass()

	if got := outbox.snapshot("n1").state; got != notificationevent.StatePending {
		t.Fatalf("state = %q, want the event left pending", got)
	}
	if keys := deliverer.delivered(); len(keys) != 0 {
		t.Fatalf("delivered %v on a decision that was never recorded", keys)
	}
}

// The exhausted-claim reaper reports how many it retired, and a failure to run
// it must not stop the pass.
func TestWorkerContinuesWhenTheReaperFails(t *testing.T) {
	outbox := newFakeOutbox()
	outbox.seedEligible("n1")
	outbox.fail("exhaust", errStoreUnavailable)
	deliverer := &recordingDeliverer{}
	worker := newTestWorker(t, outbox, deliverer, nil)

	worker.runPass()

	if keys := deliverer.delivered(); len(keys) != 1 {
		t.Fatalf("delivered %v, want the pass to have continued past the reaper", keys)
	}
}

// A finalisation that fails for a reason other than a lost race is a fault, not
// contention, and must not be mistaken for one.
func TestWorkerReportsAFinalisationFaultDistinctly(t *testing.T) {
	outbox := newFakeOutbox()
	outbox.seedEligible("n1")
	outbox.fail("finalise", errStoreUnavailable)
	worker := newTestWorker(t, outbox, &recordingDeliverer{}, nil)

	worker.runPass()

	// The row keeps its claim, so its lease is what recovers it — the same
	// outcome as a crash, which is exactly right.
	if got := outbox.snapshot("n1").state; got != notificationevent.StateProcessing {
		t.Fatalf("state = %q, want the claim left to its lease", got)
	}
}

// A worker whose lease expired mid-delivery must not record its outcome over the
// claim that superseded it, and must not report a delivery that never landed.
func TestWorkerDoesNotFinaliseAClaimItNoLongerOwns(t *testing.T) {
	outbox := newFakeOutbox()
	clock := &notificationTestClock{now: time.Now()}
	outbox.now = clock.Now
	outbox.seedEligible("n1")

	// This worker claims generation 1 and is about to deliver.
	stale := newTestWorker(t, outbox, DelivererFunc(func(context.Context, Notification) error {
		// While the delivery is in flight the lease lapses and another worker
		// reclaims the row, taking it to generation 2.
		clock.advance(2 * time.Hour)
		if _, err := outbox.ClaimDue(context.Background(), 1, 5, time.Hour); err != nil {
			t.Errorf("reclaim: %v", err)
		}
		return nil
	}), nil)

	stale.runPass()

	row := outbox.snapshot("n1")
	if row.state != notificationevent.StateProcessing {
		t.Fatalf("state = %q, want the superseding claim untouched", row.state)
	}
	if row.event.Attempts != 2 {
		t.Fatalf("attempts = %d, want the new generation intact", row.event.Attempts)
	}
}

// The stale finalisation is counted as a lost claim, never as a delivery: a
// metric that reported it as delivered would publish a delivery the outbox
// never accepted.
func TestWorkerCountsAStaleFinalisationAsALostClaim(t *testing.T) {
	metrics, shared := newTestMetrics(t)
	outbox := newFakeOutbox()
	outbox.seedEligible("n1")
	outbox.fail("finalise", storageConflict())
	worker := NewNotificationWorker(notificationTestConfig(), NotificationWorkerDeps{
		Store:     outbox,
		Deliverer: &recordingDeliverer{},
		Metrics:   metrics,
		Logger:    silentLogger(),
	})

	worker.runPass()

	body := scrape(t, shared)
	if !strings.Contains(body, `result="`+resultLeaseLost+`"`) {
		t.Fatalf("a lost claim was not counted:\n%s", body)
	}
	if strings.Contains(body, `result="`+resultDelivered+`"`) {
		t.Fatalf("a delivery the outbox never accepted was counted as delivered:\n%s", body)
	}
}

// A pass outlasts the poll interval by design, so when it returns the ticker
// already has a tick waiting. Both select cases are then ready and Go chooses at
// random — so a worker already told to stop could take one more batch of claims
// into a terminating process.
//
// The pass is held open for longer than the poll interval, which is what
// guarantees a tick is queued when it finishes; cancellation lands while it is
// still running. With the guard the worker returns without claiming again, every
// time. Without it the choice is a coin flip, so the test fails about half the
// time and reliably under -count.
func TestWorkerTakesNoNewClaimsAfterAPendingTick(t *testing.T) {
	outbox := newFakeOutbox()
	for i := 0; i < 20; i++ {
		outbox.seedEligible(fmt.Sprintf("n%02d", i))
	}

	cfg := notificationTestConfig()
	cfg.PollSeconds = 1
	cfg.BatchSize = 1
	cfg.MaxConcurrency = 1
	pollInterval := time.Duration(cfg.PollSeconds) * time.Second

	delivering := make(chan struct{}, 1)
	release := make(chan struct{})
	worker := NewNotificationWorker(cfg, NotificationWorkerDeps{
		Store: outbox,
		Deliverer: DelivererFunc(func(context.Context, Notification) error {
			delivering <- struct{}{}
			<-release
			return nil
		}),
		Logger: silentLogger(),
	})

	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		worker.Start(ctx)
	}()

	<-delivering // the first pass is in flight

	// Hold it past one whole poll interval, so the ticker queues a tick that is
	// waiting the moment the pass returns.
	time.Sleep(pollInterval + 200*time.Millisecond)
	cancel()
	close(release)

	select {
	case <-stopped:
	case <-time.After(10 * time.Second):
		t.Fatal("the worker did not return after cancellation")
	}

	// Exactly one claim: the pass that was already running. A second means the
	// queued tick started another pass after the worker had been cancelled.
	if claims := outbox.claimCount(); claims != 1 {
		t.Fatalf("%d claims were taken; a pending tick started another pass "+
			"after the worker was told to stop", claims)
	}
}
