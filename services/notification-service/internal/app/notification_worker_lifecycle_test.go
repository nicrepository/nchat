package app

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/nicrepository/nchat/libs/go/platform/notificationevent"
	"github.com/nicrepository/nchat/libs/go/platform/notificationpolicy"
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

// testWebPushConfig is a structurally valid VAPID configuration (issue #746).
//
// The key pair is generated for the test rather than committed. A real private
// key in the repository would be a secret in version control whatever its
// intended use, and a fabricated string would only prove that the validation
// accepts fabrications.
func testWebPushConfig() config.WebPushConfig {
	private, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		panic(err)
	}
	return config.WebPushConfig{
		VAPIDPublicKey:  base64.RawURLEncoding.EncodeToString(private.PublicKey().Bytes()),
		VAPIDPrivateKey: base64.RawURLEncoding.EncodeToString(private.Bytes()),
		VAPIDSubject:    "mailto:ops@example.test",
		TTLSeconds:      3600,
	}
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
		// An enabled worker needs a usable channel to be coherent (issue #746):
		// Web Push is the only one it has, so a fixture without a key pair is a
		// configuration the readiness probe is right to refuse.
		WebPush: testWebPushConfig(),
	}
}

// noopDeliverer stands in for the delivery channel a later issue will bring.
type noopDeliverer struct{}

func (noopDeliverer) Deliver(context.Context, worker.Notification) error { return nil }

// Enabled, with nothing to deliver through. Claiming events would move them
// into 'processing' and back out with nothing sent, so the worker must not
// start — and since issue #746 that condition is reached by configuration
// rather than by there being no adapter in the tree: no VAPID keys, no channel.
func TestNotificationWorkerDoesNotStartWithoutADeliveryChannel(t *testing.T) {
	restoreFactories(t)
	restoreNotificationFactories(t)
	openDB = func(context.Context, string, int) (storage.Pool, error) { return fakePool{}, nil }

	started := false
	startNotificationWorker = func(context.Context, backgroundWorker) { started = true }

	cfg := notificationWorkerConfig()
	cfg.WebPush = config.WebPushConfig{}
	application := New(cfg)

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
	newNotificationDeliverer = func(config.Config, storage.Pool,
		*worker.NotificationMetrics, *slog.Logger) worker.Deliverer {
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
	newNotificationDeliverer = func(config.Config, storage.Pool,
		*worker.NotificationMetrics, *slog.Logger) worker.Deliverer {
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
	newNotificationDeliverer = func(config.Config, storage.Pool,
		*worker.NotificationMetrics, *slog.Logger) worker.Deliverer {
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
	newNotificationDeliverer = func(config.Config, storage.Pool,
		*worker.NotificationMetrics, *slog.Logger) worker.Deliverer {
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

// An unconfigured deployment gets no delivery channel (issue #746).
//
// The empty config has no VAPID keys, so Web Push cannot be built and the
// factory says so by returning nil — which is what makes the worker decline to
// start rather than claim recipients were told when nobody was. A placeholder
// that "delivered" to a log line would be exactly that claim.
func TestNoDeliveryChannelWithoutWebPushConfiguration(t *testing.T) {
	if newNotificationDeliverer(config.Config{}, fakePool{}, nil, quietTestLogger()) != nil {
		t.Fatal("a delivery channel appeared without VAPID configuration")
	}
}

// Configured keys are not enough on their own: the fan-out reads the database,
// so a deployment without one has no channel either.
func TestNoDeliveryChannelWithoutADatabase(t *testing.T) {
	cfg := config.Config{WebPush: testWebPushConfig()}
	if newNotificationDeliverer(cfg, nil, nil, quietTestLogger()) != nil {
		t.Fatal("a delivery channel appeared without a database behind it")
	}
}

// With both in place the real Web Push channel is built.
func TestWebPushDeliveryChannelIsBuiltWhenConfigured(t *testing.T) {
	cfg := notificationWorkerConfig()
	cfg.WebPush = testWebPushConfig()
	if newNotificationDeliverer(cfg, fakePool{}, nil, quietTestLogger()) == nil {
		t.Fatal("a configured deployment got no delivery channel")
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
	newNotificationDeliverer = func(config.Config, storage.Pool,
		*worker.NotificationMetrics, *slog.Logger) worker.Deliverer {
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

// Issue #744: the wiring names its policy.
//
// The production worker must not be constructed without one, and the one it is
// constructed with must be the central engine rather than anything this service
// decided for itself. Asserting it through the dependencies is what makes that
// checkable without starting a worker and waiting for a tick.
func TestNotificationWorkerIsWiredWithTheCentralPolicy(t *testing.T) {
	deps := notificationWorkerDeps(nil, nil, nil, slog.New(slog.DiscardHandler))
	if deps.Evaluator == nil {
		t.Fatal("the production worker was wired without a policy")
	}

	imported := worker.Notification{
		ID:        "n1",
		EventType: string(notificationevent.EventTypeMention),
		Origin:    string(notificationevent.OriginImport),
	}
	verdict, err := deps.Evaluator.Evaluate(context.Background(), imported)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if verdict.Deliver {
		t.Fatal("the wired policy delivered an imported event")
	}
	if verdict.Reason() != string(notificationpolicy.ReasonHistoricalOrImported) {
		t.Fatalf("reason = %q, want the central policy's own", verdict.Reason())
	}
	if verdict.PolicyVersion != notificationpolicy.Version {
		t.Fatalf("policy version = %d, want %d", verdict.PolicyVersion, notificationpolicy.Version)
	}

	live := imported
	live.Origin = string(notificationevent.OriginLive)
	if verdict, err = deps.Evaluator.Evaluate(context.Background(), live); err != nil || !verdict.Deliver {
		t.Fatalf("Evaluate(live) = (%+v, %v), want a delivery", verdict, err)
	}
}

// A VAPID pair that is not a pair gets no delivery channel (issue #746).
//
// Both keys are individually valid P-256 values, so nothing structural refuses
// them — only deriving the public key from the private one does. Without that
// check the worker started, claimed events, and every push failed at the
// provider as a permanent error, retiring outbox rows for what is a
// configuration mistake.
func TestNoDeliveryChannelForAMismatchedVAPIDPair(t *testing.T) {
	first := testWebPushConfig()
	second := testWebPushConfig()

	mismatched := first
	mismatched.VAPIDPublicKey = second.VAPIDPublicKey

	cfg := notificationWorkerConfig()
	cfg.WebPush = mismatched

	if newNotificationDeliverer(cfg, fakePool{}, nil, quietTestLogger()) != nil {
		t.Fatal("a mismatched VAPID pair produced a delivery channel")
	}
}

// Keys that decode to the right number of bytes but are not usable P-256
// values get no channel either.
func TestNoDeliveryChannelForKeysThatAreNotKeys(t *testing.T) {
	valid := testWebPushConfig()

	cases := map[string]func(*config.WebPushConfig){
		"zero private scalar": func(c *config.WebPushConfig) {
			c.VAPIDPrivateKey = base64.RawURLEncoding.EncodeToString(make([]byte, 32))
		},
		"public point off the curve": func(c *config.WebPushConfig) {
			point := make([]byte, 65)
			point[0] = 0x04
			point[1] = 0x01
			c.VAPIDPublicKey = base64.RawURLEncoding.EncodeToString(point)
		},
	}
	for name, breakIt := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := notificationWorkerConfig()
			cfg.WebPush = valid
			breakIt(&cfg.WebPush)

			if newNotificationDeliverer(cfg, fakePool{}, nil, quietTestLogger()) != nil {
				t.Fatal("an unusable key produced a delivery channel")
			}
		})
	}
}

// An unusable channel does not merely disable the worker quietly — the
// readiness probe reports it, so the pod never goes green with a backlog that
// nothing is draining.
func TestAnUnusableChannelKeepsTheWorkerFromReportingReady(t *testing.T) {
	cfg := notificationWorkerConfig()
	cfg.NotificationWorker.Enabled = true
	cfg.WebPush = config.WebPushConfig{}

	if ready, reason := cfg.NotificationWorkerReady(); ready {
		t.Fatal("an enabled worker with no Web Push configuration reported ready")
	} else if reason == "" {
		t.Fatal("the refusal said nothing")
	}

	cfg.WebPush = testWebPushConfig()
	if ready, reason := cfg.NotificationWorkerReady(); !ready {
		t.Fatalf("a usable channel was refused: %s", reason)
	}
}
