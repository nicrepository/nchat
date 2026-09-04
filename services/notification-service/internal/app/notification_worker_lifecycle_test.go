package app

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/nicrepository/nchat/services/notification-service/internal/config"
	"github.com/nicrepository/nchat/services/notification-service/internal/storage"
	"github.com/nicrepository/nchat/services/notification-service/internal/worker"
)

// Issue #742: the outbox worker's place in the process lifecycle.
//
// Two things are being defended here. The worker must not start on a
// configuration it cannot honour — including the one it has today, where no
// delivery channel exists — and once it is running the App must own its
// lifetime, so a SIGTERM stops it rather than leaving it claiming events into a
// process that is going away.

// restoreNotificationFactories keeps the package-level seams from leaking
// between tests.
func restoreNotificationFactories(t *testing.T) {
	t.Helper()
	origDeliverer := newNotificationDeliverer
	origWorker := newNotificationWorker
	origStart := startNotificationWorker
	t.Cleanup(func() {
		newNotificationDeliverer = origDeliverer
		newNotificationWorker = origWorker
		startNotificationWorker = origStart
	})
}

func notificationWorkerConfig() config.Config {
	return config.Config{
		ServiceName:              "notification-service",
		Env:                      "test",
		Port:                     8084,
		ReadHeaderTimeoutSeconds: 5,
		DatabaseURL:              "postgres://user@127.0.0.1:1/nchat?sslmode=disable",
		DBConnectTimeoutSeconds:  1,
		NotificationWorker:       config.NotificationWorkerConfig{Enabled: true}.Normalized(),
	}
}

// noopDeliverer stands in for the delivery channel a later issue will bring.
type noopDeliverer struct{}

func (noopDeliverer) Deliver(context.Context, worker.Notification) error { return nil }

// The state the pipeline is actually in: enabled, but with nothing to deliver
// through. Claiming events would move them into 'processing' and back out with
// nothing sent, so the worker must not start.
func TestNotificationWorkerDoesNotStartWithoutADeliveryChannel(t *testing.T) {
	restoreFactories(t)
	restoreNotificationFactories(t)
	openDB = func(context.Context, string, int) (storage.Pool, error) { return fakePool{}, nil }

	started := false
	startNotificationWorker = func(context.Context, backgroundWorker) { started = true }

	application := New(notificationWorkerConfig())

	if started {
		t.Fatal("the worker started with no channel to deliver through")
	}
	if application.NotificationWorkerRunning() {
		t.Fatal("readiness claims a notification pipeline that is not running")
	}
}

func TestNotificationWorkerDoesNotStartWhenDisabled(t *testing.T) {
	restoreFactories(t)
	restoreNotificationFactories(t)
	openDB = func(context.Context, string, int) (storage.Pool, error) { return fakePool{}, nil }
	newNotificationDeliverer = func(config.Config, *slog.Logger) worker.Deliverer {
		return noopDeliverer{}
	}

	started := false
	startNotificationWorker = func(context.Context, backgroundWorker) { started = true }

	cfg := notificationWorkerConfig()
	cfg.NotificationWorker.Enabled = false
	New(cfg)

	if started {
		t.Fatal("a disabled worker started anyway")
	}
}

// Without a database there is no outbox to drain. It is a degraded mode, not a
// start-up failure: the HTTP surface stays up and only the worker is disabled.
func TestNotificationWorkerDoesNotStartWithoutADatabase(t *testing.T) {
	restoreFactories(t)
	restoreNotificationFactories(t)
	newNotificationDeliverer = func(config.Config, *slog.Logger) worker.Deliverer {
		return noopDeliverer{}
	}

	started := false
	startNotificationWorker = func(context.Context, backgroundWorker) { started = true }

	cfg := notificationWorkerConfig()
	cfg.DatabaseURL = ""
	application := New(cfg)

	if started {
		t.Fatal("the worker started with no database")
	}
	if application.Handler == nil {
		t.Fatal("the HTTP surface must survive a disabled worker")
	}
}

// A lease that cannot cover a batch of deliveries is refused before anything
// starts, so the failure is a configuration message rather than a duplicated
// notification.
func TestNotificationWorkerDoesNotStartOnALeaseItCannotHonour(t *testing.T) {
	restoreFactories(t)
	restoreNotificationFactories(t)
	openDB = func(context.Context, string, int) (storage.Pool, error) { return fakePool{}, nil }
	newNotificationDeliverer = func(config.Config, *slog.Logger) worker.Deliverer {
		return noopDeliverer{}
	}

	started := false
	startNotificationWorker = func(context.Context, backgroundWorker) { started = true }

	cfg := notificationWorkerConfig()
	cfg.NotificationWorker.BatchSize = 200
	cfg.NotificationWorker.MaxConcurrency = 1
	cfg.NotificationWorker.DeliveryTimeoutSeconds = 120
	cfg.NotificationWorker.LeaseSeconds = 60
	New(cfg)

	if started {
		t.Fatal("the worker started on a lease that cannot cover a batch")
	}
}

// With a channel present the worker runs, readiness sees it, and shutdown stops
// it. This is the shape the next issue's adapter drops into.
func TestNotificationWorkerRunsAndIsStoppedByShutdown(t *testing.T) {
	restoreFactories(t)
	restoreNotificationFactories(t)
	openDB = func(context.Context, string, int) (storage.Pool, error) { return fakePool{}, nil }
	newNotificationDeliverer = func(config.Config, *slog.Logger) worker.Deliverer {
		return noopDeliverer{}
	}

	starter := newFakeSMTPWorkerStarter()
	newNotificationWorker = func(config.Config, storage.NotificationOutboxStore,
		worker.Deliverer, *worker.NotificationMetrics, *slog.Logger) backgroundWorker {
		return starter
	}

	application := New(notificationWorkerConfig())

	select {
	case <-starter.started:
	case <-time.After(2 * time.Second):
		t.Fatal("the worker never started")
	}
	if !application.NotificationWorkerRunning() {
		t.Fatal("readiness cannot see a running worker")
	}

	if err := application.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if starter.ctx.Err() == nil {
		t.Fatal("shutdown did not cancel the worker's context")
	}
	if application.NotificationWorkerRunning() {
		t.Fatal("readiness still claims a stopped worker")
	}
}

// Both workers stop on one shutdown, and neither is left claiming while the
// other drains.
func TestShutdownStopsEveryWorker(t *testing.T) {
	smtp := newFakeSMTPWorkerStarter()
	notification := newFakeSMTPWorkerStarter()
	application := &App{Logger: quietTestLogger()}
	application.runWorker(smtp)
	application.notification = application.launchWorker("notification", notification,
		func(ctx context.Context, w backgroundWorker) { w.Start(ctx) },
		config.NotificationWorkerConfig{}.ProcessingBudget())

	<-smtp.started
	<-notification.started

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := application.StopWorker(ctx); err != nil {
		t.Fatalf("StopWorker: %v", err)
	}
	if smtp.ctx.Err() == nil || notification.ctx.Err() == nil {
		t.Fatal("a worker was left running after shutdown")
	}
}

// notificationDisabledReason must name the cause rather than return one opaque
// failure, because the reason is what an operator reads in the log line.
func TestNotificationDisabledReasonNamesTheCause(t *testing.T) {
	enabled := notificationWorkerConfig()

	if reason := notificationDisabledReason(enabled, fakePool{}); reason != "" {
		t.Fatalf("reason = %q, want the worker permitted", reason)
	}
	if reason := notificationDisabledReason(enabled, nil); reason != "database_not_configured" {
		t.Fatalf("reason = %q, want the missing database named", reason)
	}

	noDatabase := enabled
	noDatabase.DatabaseURL = ""
	if reason := notificationDisabledReason(noDatabase, fakePool{}); reason == "" {
		t.Fatal("an unusable configuration produced no reason")
	}
}

// The default factory is deliberately empty: no chat notification channel
// exists yet, and a placeholder that "delivered" to a log line would claim
// recipients were told when nobody was.
func TestNoDeliveryChannelIsRegisteredYet(t *testing.T) {
	if newNotificationDeliverer(config.Config{}, quietTestLogger()) != nil {
		t.Fatal("a delivery channel appeared without an adapter to back it")
	}
}

// ---------------------------------------------------------------------------
// Shutdown waits for the budget the configuration actually allows
// ---------------------------------------------------------------------------

// The configuration the review named: valid, and budgeting more than the fixed
// forty seconds the lifecycle used to wait.
//
// This is the whole defect in one assertion. The lease covers the pass, so the
// worker accepts the configuration and will run passes of up to 55s — while the
// old lifecycle stopped waiting at 40s, 15s before the pass was entitled to
// finish. A process exiting in that window could leave a delivery the provider
// had already accepted unrecorded.
func TestValidConfigurationCanOutlastTheOldFixedShutdownTimeout(t *testing.T) {
	cfg := config.NotificationWorkerConfig{
		Enabled:                true,
		BatchSize:              5,
		MaxConcurrency:         1,
		DeliveryTimeoutSeconds: 10,
		LeaseSeconds:           60,
	}.Normalized()

	if !cfg.LeaseCoversProcessing() {
		t.Fatal("the configuration under test must be one the worker accepts")
	}
	budget := cfg.ProcessingBudget()
	// 5 waves of 10s, plus the 5s reserved for recording the outcomes.
	if budget != 55*time.Second {
		t.Fatalf("processing budget = %s, want 55s", budget)
	}
	if budget <= smtpWorkerDrainBudget {
		t.Fatalf("processing budget %s no longer exceeds the old fixed %s timeout; "+
			"this test has stopped covering the regression it exists for",
			budget, smtpWorkerDrainBudget)
	}
}

// The pure resolver, which is where "wait for the real budget, never past the
// caller" is decided. Checkable without running a shutdown for a minute.
func TestEffectiveDrainBudgetPrefersTheWorkersOwnBudget(t *testing.T) {
	const budget = 55 * time.Second

	tests := map[string]struct {
		callerDeadline time.Duration // 0 means no deadline
		wantAtLeast    time.Duration
		wantAtMost     time.Duration
	}{
		// The regression: a caller with room to spare must not shrink a 55s
		// budget to the old 40s constant.
		"caller has room": {90 * time.Second, 54 * time.Second, budget},
		// The caller's deadline is the hard ceiling and wins when it is nearer.
		"caller is nearer": {10 * time.Second, 0, 10 * time.Second},
		// No deadline at all: the worker's own budget bounds the wait.
		"caller has no deadline": {0, 54 * time.Second, budget},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			if tc.callerDeadline > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, tc.callerDeadline)
				defer cancel()
			}

			got := effectiveDrainBudget(ctx, budget)
			if got < tc.wantAtLeast || got > tc.wantAtMost {
				t.Fatalf("effective budget = %s, want within [%s, %s]",
					got, tc.wantAtLeast, tc.wantAtMost)
			}
		})
	}
}

// A handle built without a budget still gets a bounded wait rather than none.
func TestEffectiveDrainBudgetFallsBackForAnUnsetBudget(t *testing.T) {
	if got := effectiveDrainBudget(context.Background(), 0); got != smtpWorkerDrainBudget {
		t.Fatalf("effective budget = %s, want the fallback %s", got, smtpWorkerDrainBudget)
	}
}

// The notification worker's handle carries its configuration's budget, not a
// constant. This is the wiring the pure function above depends on.
func TestNotificationWorkerHandleCarriesItsConfiguredBudget(t *testing.T) {
	restoreFactories(t)
	restoreNotificationFactories(t)
	openDB = func(context.Context, string, int) (storage.Pool, error) { return fakePool{}, nil }
	newNotificationDeliverer = func(config.Config, *slog.Logger) worker.Deliverer {
		return noopDeliverer{}
	}
	starter := newFakeSMTPWorkerStarter()
	newNotificationWorker = func(config.Config, storage.NotificationOutboxStore,
		worker.Deliverer, *worker.NotificationMetrics, *slog.Logger) backgroundWorker {
		return starter
	}

	cfg := notificationWorkerConfig()
	cfg.NotificationWorker.BatchSize = 5
	cfg.NotificationWorker.MaxConcurrency = 1
	cfg.NotificationWorker.DeliveryTimeoutSeconds = 10
	cfg.NotificationWorker.LeaseSeconds = 60
	cfg.NotificationWorker = cfg.NotificationWorker.Normalized()

	application := New(cfg)
	t.Cleanup(func() { _ = application.StopWorker(context.Background()) })
	<-starter.started

	want := cfg.NotificationWorker.ProcessingBudget()
	if got := application.notification.drainBudget; got != want {
		t.Fatalf("drain budget = %s, want the configured %s", got, want)
	}
	if application.notification.drainBudget <= smtpWorkerDrainBudget {
		t.Fatal("the handle is still bounded by the old fixed timeout")
	}
}

// blockingWorker stands in for a pass that is mid-delivery when shutdown begins:
// it signals that it started, blocks until released, and only then returns.
//
// Synchronised entirely by channels — the property under test is ordering, and a
// sleep would be both slower and weaker.
type blockingWorker struct {
	started  chan struct{}
	release  chan struct{}
	finished chan struct{}
}

func newBlockingWorker() *blockingWorker {
	return &blockingWorker{
		started:  make(chan struct{}),
		release:  make(chan struct{}),
		finished: make(chan struct{}),
	}
}

// Start models the worker's real shape: cancellation stops it taking new work,
// but the pass already in flight runs to completion on its own protected
// context, which is exactly what shutdown has to wait for.
func (b *blockingWorker) Start(ctx context.Context) {
	close(b.started)
	<-ctx.Done() // told to stop claiming
	<-b.release  // the pass in flight finishes on its own terms
	close(b.finished)
}

// Shutdown must not report the worker stopped while a pass is still running,
// and must return as soon as it finishes.
func TestShutdownWaitsForThePassInFlight(t *testing.T) {
	blocked := newBlockingWorker()
	application := &App{Logger: quietTestLogger()}
	application.notification = application.launchWorker("notification", blocked,
		func(ctx context.Context, w backgroundWorker) { w.Start(ctx) },
		55*time.Second)

	<-blocked.started

	// A caller deadline far beyond the budget, so nothing but the worker itself
	// decides when this returns.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- application.StopWorker(ctx) }()

	// While the pass holds, shutdown must still be waiting. The short guard is a
	// deadlock detector, not the synchronisation.
	select {
	case err := <-done:
		t.Fatalf("shutdown returned %v while the pass was still in flight", err)
	case <-time.After(100 * time.Millisecond):
	}

	close(blocked.release)
	<-blocked.finished

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("StopWorker returned %v after a clean drain", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown did not return once the pass had finished")
	}
}

// The budget is not a licence to ignore the process's own deadline: a caller
// with a short deadline still interrupts the wait, and shutdown says so rather
// than pretending the pass finished.
func TestShutdownStillHonoursAShorterCallerDeadline(t *testing.T) {
	blocked := newBlockingWorker()
	application := &App{Logger: quietTestLogger()}
	application.notification = application.launchWorker("notification", blocked,
		func(ctx context.Context, w backgroundWorker) { w.Start(ctx) },
		55*time.Second)
	<-blocked.started
	// Released at the end so the goroutine does not outlive the test.
	t.Cleanup(func() { close(blocked.release) })

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := application.StopWorker(ctx)
	elapsed := time.Since(start)

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("StopWorker returned %v, want the caller's deadline to be reported", err)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("StopWorker waited %s, ignoring the caller's 50ms deadline", elapsed)
	}
}
