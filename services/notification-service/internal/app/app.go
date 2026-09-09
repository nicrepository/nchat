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
	"github.com/nicrepository/nchat/services/notification-service/internal/service"
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
	// through (issues #742, #746).
	//
	// Web Push is that channel, and it is the only one: a chat notification is
	// delivered to the recipient's registered browsers or it is not delivered at
	// all. The factory returns nil when it cannot be built, which is not a
	// failure path bolted on — it is the same signal issue #742 designed the
	// worker around. A worker with no channel declines to start and says why on
	// the readiness probe, and the outbox keeps accumulating events, because
	// that is precisely what a durable outbox is for.
	newNotificationDeliverer = newWebPushDeliverer
	newNotificationWorker    = func(cfg config.Config, store storage.NotificationOutboxStore,
		deliverer worker.Deliverer, metrics *worker.NotificationMetrics, logger *slog.Logger) backgroundWorker {
		return worker.NewNotificationWorker(cfg.NotificationWorker,
			notificationWorkerDeps(store, deliverer, metrics, logger))
	}
	startNotificationWorker = func(ctx context.Context, w backgroundWorker) {
		w.Start(ctx)
	}

	// newTokenValidator builds the access-token validator the push subscription
	// routes authenticate with (issue #745). A variable so a test can watch the
	// wiring refuse an unusable configuration without owning a real secret.
	newTokenValidator = func(cfg config.Config) (*httpapi.TokenValidator, error) {
		return httpapi.NewTokenValidator(
			cfg.AuthJWTHMACSecret, cfg.AuthJWTIssuer, cfg.AuthJWTAudience)
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
	options := []httpapi.Option{
		httpapi.WithMetrics(obsMetrics),
		httpapi.WithSMTPWorkerProbe(application.SMTPWorkerRunning),
		httpapi.WithNotificationWorkerProbe(application.NotificationWorkerRunning),
	}
	application.Handler = httpapi.NewRouter(cfg, logger,
		append(options, pushSubscriptionOptions(cfg, pool, logger)...)...)
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

// notificationWorkerDeps is what the production notification worker is built
// with, and it exists as a named function so that one of those dependencies is
// assertable: the policy.
//
// Evaluator is named here rather than left to the constructor's default, so the
// production wiring says out loud which authority decides delivery (issue
// #744). There is no other one to pass — the permissive stand-in that used to
// live in the worker package is gone.
func notificationWorkerDeps(
	store storage.NotificationOutboxStore, deliverer worker.Deliverer,
	metrics *worker.NotificationMetrics, logger *slog.Logger,
) worker.NotificationWorkerDeps {
	return worker.NotificationWorkerDeps{
		Store:     store,
		Evaluator: worker.NewPolicyEvaluator(),
		Deliverer: deliverer,
		Metrics:   metrics,
		Logger:    logger,
	}
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
	cfg config.Config, pool storage.Pool, obsMetrics *observability.Metrics, logger *slog.Logger,
) {
	if !cfg.NotificationWorker.Enabled {
		return
	}
	if reason := notificationDisabledReason(cfg, pool); reason != "" {
		logger.Warn("notification worker disabled", "reason", reason)
		return
	}
	// One NotificationMetrics for the worker and the channel it delivers
	// through: both register on the shared registry, and building two would
	// register the same collectors twice.
	metrics := worker.NewNotificationMetrics(obsMetrics)
	deliverer := newNotificationDeliverer(cfg, pool, metrics, logger)
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
			deliverer, metrics, logger),
		startNotificationWorker, cfg.NotificationWorker.ProcessingBudget())
	// No policy version here, deliberately. It belongs to a decision, not to a
	// process: during a rollout two replicas run different rule sets, so a
	// version stamped at startup would answer a question nobody asked and look
	// like the answer to the one that matters. The worker logs it against each
	// notification instead — see NotificationWorker.logDecision.
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

// pushSubscriptionOptions mounts the Web Push subscription routes, or mounts
// nothing and says why (issue #745).
//
// Both dependencies are hard requirements rather than degraded modes. Without a
// database there is no session to validate a caller against and no table to
// write; without a usable signing secret every token would be unverifiable. In
// either case this returns no option at all, so the router never registers the
// routes and a request for them is answered by the catch-all: 404. A surface
// that answered anything else would be one authorising writes it could not
// attribute to anybody.
//
// The refusal is in the log, not in the status code. An operator sees the reason
// here; a client sees a route this build does not serve.
func pushSubscriptionOptions(cfg config.Config, pool storage.Pool, logger *slog.Logger) []httpapi.Option {
	if pool == nil {
		logger.Warn("push subscription api disabled", "reason", "database_not_configured")
		return nil
	}
	validator, err := newTokenValidator(cfg)
	if err != nil {
		// The reason is the category, never the configuration: naming the field
		// that was short or missing would put a fact about the signing secret in
		// a log line.
		logger.Warn("push subscription api disabled", "reason", "access_token_validation_unavailable")
		return nil
	}
	handler := httpapi.NewPushSubscriptionHandler(
		service.NewPushSubscriptions(storage.NewPGXPushSubscriptionStore(pool)))
	logger.Info("push subscription api enabled")
	return []httpapi.Option{httpapi.WithPushSubscriptions(
		validator, storage.NewPGXPrincipalResolver(pool), handler)}
}

// newWebPushDeliverer builds the Web Push channel, or reports why it cannot
// (issue #746).
//
// Configuration is the whole of the decision, and it is made once here rather
// than rediscovered on every send. A deployment with no VAPID keys, or with
// keys that are not keys, gets no channel — so the worker does not start, the
// readiness probe says so, and no half-configured send is ever attempted. The
// reason names the variable and never its value: this function is the last
// place a key could be turned into a log line, and it does not.
func newWebPushDeliverer(
	cfg config.Config, pool storage.Pool,
	metrics *worker.NotificationMetrics, logger *slog.Logger,
) worker.Deliverer {
	if ready, reason := cfg.WebPush.Ready(); !ready {
		logger.Warn("web push delivery disabled", "reason", reason)
		return nil
	}
	if pool == nil {
		logger.Warn("web push delivery disabled", "reason", "database_not_configured")
		return nil
	}
	// The sender's HTTP timeout is the worker's own delivery budget. The
	// delivery context bounds the fan-out to the same figure, so whichever
	// expires first ends the attempt; the client's copy exists so a client
	// without a context could not outlive the pass that owns it.
	sender := worker.NewVAPIDSender(cfg.WebPush,
		time.Duration(cfg.NotificationWorker.DeliveryTimeoutSeconds)*time.Second)
	logger.Info("web push delivery enabled",
		"ttl_seconds", cfg.WebPush.Normalized().TTLSeconds)
	return worker.NewWebPushDeliverer(cfg.WebPush, worker.WebPushDeps{
		Store:   storage.NewPGXPushDeliveryStore(pool),
		Sender:  sender,
		Metrics: metrics,
		Logger:  logger,
	})
}
