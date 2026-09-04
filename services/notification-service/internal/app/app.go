package app

import (
	"context"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/nicrepository/nchat/libs/go/platform/emailcrypto"
	platformlog "github.com/nicrepository/nchat/libs/go/platform/log"
	"github.com/nicrepository/nchat/libs/go/platform/observability"
	"github.com/nicrepository/nchat/services/notification-service/internal/config"
	httpapi "github.com/nicrepository/nchat/services/notification-service/internal/http"
	"github.com/nicrepository/nchat/services/notification-service/internal/storage"
	"github.com/nicrepository/nchat/services/notification-service/internal/worker"
)

var (
	openDB        = storage.OpenDB
	newEncryptor  = emailcrypto.New
	newSMTPSender = worker.NewNetSMTPSender
	newSMTPWorker = func(cfg config.Config, store storage.OutboxStore, decryptor *emailcrypto.Encryptor, sender worker.Sender, logger *slog.Logger) smtpWorker {
		return worker.New(cfg, store, decryptor, sender, logger)
	}
	// Takes the context it should run under, so the App owns the worker's
	// lifetime instead of handing it an unstoppable context.Background().
	startSMTPWorker = func(ctx context.Context, w smtpWorker) {
		w.Start(ctx)
	}

	// newNotificationDeliverer builds the channel the notification worker sends
	// through (issue #742).
	//
	// It returns nil, and that is the honest state of the pipeline rather than an
	// oversight. Web Push, the browser service worker and the e-mail digest are
	// all explicitly out of this issue's scope, so no delivery channel for chat
	// notifications exists yet — and a placeholder that "delivered" to a log line
	// would be a fake in production, claiming recipients were told when nobody
	// was. Until a real adapter lands here the worker declines to start and says
	// why, which is a state an operator can see. Nothing else changes: the outbox
	// keeps accumulating events, because that is precisely what a durable outbox
	// is for.
	newNotificationDeliverer = func(config.Config, *slog.Logger) worker.Deliverer {
		return nil
	}
	newNotificationWorker = func(cfg config.Config, store storage.NotificationOutboxStore,
		deliverer worker.Deliverer, metrics *worker.NotificationMetrics, logger *slog.Logger) backgroundWorker {
		return worker.NewNotificationWorker(cfg.NotificationWorker, worker.NotificationWorkerDeps{
			Store:     store,
			Deliverer: deliverer,
			Metrics:   metrics,
			Logger:    logger,
		})
	}
	startNotificationWorker = func(ctx context.Context, w backgroundWorker) {
		w.Start(ctx)
	}
)

// smtpWorkerDrainBudget bounds the wait for the SMTP worker.
//
// It is that worker's budget specifically, not a universal one. The SMTP pass is
// a single send plus the grace to record it — SMTPProtectedProcessingSeconds,
// fifteen seconds by default and capped well below this — so forty is a
// comfortable ceiling for it and is left exactly as it was.
//
// It used to be applied to every worker, and that was the defect: the
// notification worker's pass budget is derived from its own configuration and a
// valid configuration can exceed forty seconds, so the lifecycle stopped waiting
// while a pass was still legitimately running. Each worker now carries its own
// budget; see workerHandle.drainBudget.
const smtpWorkerDrainBudget = 40 * time.Second

// backgroundWorker is anything whose lifetime the App owns: it runs until the
// context it was given is cancelled, and returning is how it reports that it
// has stopped.
type backgroundWorker interface {
	Start(ctx context.Context)
}

// smtpWorker is the name the SMTP wiring and its tests already use for that
// same contract.
type smtpWorker = backgroundWorker

// workerHandle is one running worker: how to stop it, how to know it stopped,
// and whether it is alive.
//
// A struct rather than three fields per worker on App, because there are now two
// workers and a second copy of stopWorker/workerDone/workerRunning is how the
// two lifetimes drift apart.
type workerHandle struct {
	// name appears in shutdown logs, so a worker that overran is identifiable.
	name string
	// drainBudget is how long this particular worker may legitimately need to
	// finish the work it already holds. It comes from the worker's own
	// configuration — the same function that validates its lease — so the
	// lifecycle and the worker cannot disagree about what a valid pass costs.
	drainBudget time.Duration
	stop        context.CancelFunc
	done        chan struct{}
	running     atomic.Bool
}

// isRunning is nil-safe: a worker that was never started is not running, and
// readiness asks that question before anything has been built.
func (h *workerHandle) isRunning() bool {
	return h != nil && h.running.Load()
}

type App struct {
	Config          config.Config
	Logger          *slog.Logger
	Handler         http.Handler
	TracingShutdown observability.ShutdownFunc

	// The background workers' lifetimes, owned here rather than left running on
	// a context nothing can cancel. Either is nil when that worker was not
	// started, which is the normal case in every environment that has it
	// disabled — and both are disabled by default.
	smtp         *workerHandle
	notification *workerHandle
}

// SMTPWorkerRunning reports whether the SMTP worker goroutine is alive.
//
// A worker can stop without the process stopping — it refuses to run on a
// configuration whose lease cannot cover a delivery, and it returns if its
// context is cancelled. Readiness has to see that, or the pod goes on
// advertising a mail capability that nothing is serving.
func (a *App) SMTPWorkerRunning() bool {
	return a.smtp.isRunning()
}

// NotificationWorkerRunning reports whether the notification outbox worker is
// alive, for the same reason its SMTP counterpart does.
//
// The worker refuses to run on a lease that cannot cover a batch of deliveries,
// and it returns when its context is cancelled. Readiness has to see that, or
// the pod goes on advertising a notification pipeline nothing is draining.
func (a *App) NotificationWorkerRunning() bool {
	return a.notification.isRunning()
}

// Shutdown stops the SMTP worker, waits for the pass it is in to finish, and
// then releases tracing.
//
// The order matters. The worker writes the outcome of a delivery through the
// database, so tearing anything down before it has stopped is what turns a
// graceful shutdown into a duplicated email on the next poll. If the worker
// does not stop within its own drain budget the process gives up waiting and
// says so: a stuck worker must not stop the pod from terminating.
func (a *App) Shutdown(ctx context.Context) error {
	// Tracing is released even when the worker overran, so a stuck worker does
	// not also cost the spans; the worker's failure is what gets reported,
	// because it is the one that can mean a message was left unfinalised.
	workerErr := a.StopWorker(ctx)
	if a.TracingShutdown == nil {
		return workerErr
	}
	tracingErr := a.TracingShutdown(ctx)
	if workerErr != nil {
		return workerErr
	}
	return tracingErr
}

// StopWorker asks every background worker to stop claiming and waits for the
// work each is in the middle of, within whichever deadline arrives first.
//
// Exported so it can be handed to httpserver as a shutdown hook: the workers then
// stop the moment SIGTERM arrives and drain alongside the HTTP server rather
// than after it, which is what keeps the two budgets from being added together.
//
// Every worker is told to stop before any of them is waited on. Doing it in
// sequence would let the second worker keep claiming for as long as the first
// took to drain, which is the opposite of what a shutdown is for.
func (a *App) StopWorker(ctx context.Context) error {
	handles := a.workers()
	for _, handle := range handles {
		handle.stop()
	}

	var firstErr error
	for _, handle := range handles {
		if err := a.awaitWorker(ctx, handle); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// workers returns the handles that exist, in shutdown order.
func (a *App) workers() []*workerHandle {
	handles := make([]*workerHandle, 0, 2)
	for _, handle := range []*workerHandle{a.smtp, a.notification} {
		if handle != nil {
			handles = append(handles, handle)
		}
	}
	return handles
}

// effectiveDrainBudget is how long to wait for one worker: its own budget, never
// extended past the deadline the caller gave.
//
// A pure function, because the property that matters is arithmetic and has to be
// checkable without running a shutdown for a minute. The two halves:
//
//   - the worker's budget is the floor of what a legitimate pass may need, so it
//     must not be truncated by a constant that knows nothing about the
//     configuration. A 55s budget waits 55s, not 40;
//   - the caller's deadline is the hard ceiling. httpserver hands every
//     subsystem the one process-wide termination budget, and a child that
//     outlived it would be the thing the kubelet interrupts, so the smaller of
//     the two always wins.
func effectiveDrainBudget(ctx context.Context, budget time.Duration) time.Duration {
	if budget <= 0 {
		budget = smtpWorkerDrainBudget
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		return budget
	}
	if remaining := time.Until(deadline); remaining < budget {
		return remaining
	}
	return budget
}

func (a *App) awaitWorker(ctx context.Context, handle *workerHandle) error {
	budget := effectiveDrainBudget(ctx, handle.drainBudget)
	fallback := time.NewTimer(budget)
	defer fallback.Stop()
	select {
	case <-handle.done:
		a.Logger.Info("worker stopped", "worker", handle.name)
		return nil
	case <-ctx.Done():
		a.Logger.Warn("worker did not stop before the process deadline", "worker", handle.name)
		return ctx.Err()
	case <-fallback.C:
		a.Logger.Warn("worker did not stop in time", "worker", handle.name,
			"timeout_seconds", int(budget.Seconds()))
		return context.DeadlineExceeded
	}
}

func New(cfg config.Config) *App {
	logger := platformlog.New(cfg.ServiceName, cfg.Env)
	application := &App{Logger: logger}
	obsCfg := observability.LoadConfig(cfg.ServiceName)
	shutdown, _ := observability.SetupTracing(context.Background(), obsCfg)

	// One registry for the whole process, built here rather than inside the
	// router: the notification worker registers its collectors during wiring,
	// which happens before the router exists. The router serves this exact
	// object.
	obsMetrics := observability.NewMetrics(obsCfg)

	pool := openPool(cfg, logger)
	decryptor := openDecryptor(cfg, logger)
	application.startSMTPWorker(cfg, pool, decryptor, logger)
	application.startNotificationWorker(cfg, pool, obsMetrics, logger)

	application.Config = cfg
	application.Handler = httpapi.NewRouter(cfg, logger,
		httpapi.WithMetrics(obsMetrics),
		httpapi.WithSMTPWorkerProbe(application.SMTPWorkerRunning),
		httpapi.WithNotificationWorkerProbe(application.NotificationWorkerRunning))
	application.TracingShutdown = shutdown
	return application
}

// openPool returns the database pool, or nil when the service is configured
// without one or cannot reach it. Both are degraded modes, not start-up
// failures: the HTTP surface stays up and only the SMTP worker is disabled.
func openPool(cfg config.Config, logger *slog.Logger) storage.Pool {
	if cfg.DatabaseURL == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(),
		time.Duration(cfg.DBConnectTimeoutSeconds)*time.Second)
	defer cancel()
	pool, err := openDB(ctx, cfg.DatabaseURL, cfg.DBConnectTimeoutSeconds)
	if err != nil {
		logger.Warn("database unavailable; smtp worker disabled", "reason", "open_db_failed")
		return nil
	}
	return pool
}

// openDecryptor returns the outbox decryptor, or nil when no key is configured
// or the configured key is unusable.
func openDecryptor(cfg config.Config, logger *slog.Logger) *emailcrypto.Encryptor {
	if cfg.AuthEmailOutboxEncKey == "" {
		return nil
	}
	decryptor, err := newEncryptor(cfg.AuthEmailOutboxEncKey)
	if err != nil {
		logger.Warn("smtp worker disabled", "reason", "invalid_email_outbox_encryption_key")
		return nil
	}
	return decryptor
}

// smtpDisabledReason names why the SMTP worker cannot run, or "" when it can.
// Every dependency it needs is optional at the process level, so each absence is
// a distinct, logged reason rather than one opaque failure.
func smtpDisabledReason(cfg config.Config, pool storage.Pool, decryptor *emailcrypto.Encryptor) string {
	if ready, reason := cfg.SMTPWorkerReady(); !ready {
		return reason
	}
	if pool == nil {
		return "database_not_configured"
	}
	if decryptor == nil {
		return "email_outbox_encryption_unavailable"
	}
	return ""
}

// startSMTPWorker owns the whole decision: whether the worker may run, building
// its sender, and starting it. Keeping it out of New is what keeps New a
// sequence of steps rather than a nest of conditions.
func (a *App) startSMTPWorker(cfg config.Config, pool storage.Pool, decryptor *emailcrypto.Encryptor, logger *slog.Logger) {
	if !cfg.SMTPWorkerEnabled {
		return
	}
	if reason := smtpDisabledReason(cfg, pool, decryptor); reason != "" {
		logger.Warn("smtp worker disabled", "reason", reason)
		return
	}
	sender, err := newSMTPSender(
		cfg.SMTPHost,
		cfg.SMTPPort,
		cfg.SMTPUsername,
		cfg.SMTPPassword,
		cfg.SMTPFrom,
		cfg.SMTPTLSMode,
		cfg.SMTPTimeoutSeconds,
	)
	if err != nil {
		logger.Warn("smtp worker disabled", "reason", "smtp_sender_invalid")
		return
	}
	a.runWorker(newSMTPWorker(cfg, storage.NewPGXOutboxStore(pool), decryptor, sender, logger))
	logger.Info("smtp worker started")
}

// notificationDisabledReason names why the notification outbox worker cannot
// run, or "" when it can. Each absence is its own logged reason rather than one
// opaque failure, exactly as the SMTP worker's is.
func notificationDisabledReason(cfg config.Config, pool storage.Pool) string {
	if ready, reason := cfg.NotificationWorkerReady(); !ready {
		return reason
	}
	if pool == nil {
		return "database_not_configured"
	}
	return ""
}

// startNotificationWorker owns the decision to run the outbox worker: whether it
// may, what it delivers through, and starting it.
func (a *App) startNotificationWorker(
	cfg config.Config, pool storage.Pool, metrics *observability.Metrics, logger *slog.Logger,
) {
	if !cfg.NotificationWorker.Enabled {
		return
	}
	if reason := notificationDisabledReason(cfg, pool); reason != "" {
		logger.Warn("notification worker disabled", "reason", reason)
		return
	}
	deliverer := newNotificationDeliverer(cfg, logger)
	if deliverer == nil {
		// No channel to deliver through. Claiming events would move them into
		// 'processing' and back out again with nothing sent, so the worker does
		// not start and the backlog is left intact for the release that can
		// drain it.
		logger.Warn("notification worker disabled", "reason", "delivery_channel_unavailable")
		return
	}
	// The drain budget is the worker's own ProcessingBudget: exactly the window
	// its protected pass context may use, so shutdown waits for as long as a
	// pass is entitled to run and not a second less.
	a.notification = a.launchWorker("notification",
		newNotificationWorker(cfg, storage.NewPGXNotificationOutboxStore(pool),
			deliverer, worker.NewNotificationMetrics(metrics), logger),
		startNotificationWorker, cfg.NotificationWorker.ProcessingBudget())
	logger.Info("notification worker started")
}

// runWorker starts the SMTP worker and records its handle.
func (a *App) runWorker(w smtpWorker) {
	a.smtp = a.launchWorker("smtp", w, startSMTPWorker, smtpWorkerDrainBudget)
}

// launchWorker starts a worker on a cancellable context and returns the handle
// that stops it and reports whether it is alive.
func (a *App) launchWorker(
	name string, w backgroundWorker, start func(context.Context, backgroundWorker),
	drainBudget time.Duration,
) *workerHandle {
	ctx, cancel := context.WithCancel(context.Background())
	handle := &workerHandle{
		name: name, drainBudget: drainBudget, stop: cancel, done: make(chan struct{}),
	}
	handle.running.Store(true)
	go func() {
		defer close(handle.done)
		// Whatever ends the worker — shutdown, or a refusal to run at all —
		// readiness stops claiming the capability from this moment.
		defer handle.running.Store(false)
		start(ctx, w)
	}()
	return handle
}
