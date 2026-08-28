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
)

// workerShutdownTimeout bounds the wait for the SMTP worker when the caller
// supplies no deadline of its own. When it does — which is the normal path,
// since httpserver hands every subsystem the one process-wide termination
// budget — that deadline wins, because a child budget that outlived the
// process's own would be the thing the kubelet interrupts.
const workerShutdownTimeout = 40 * time.Second

type smtpWorker interface {
	Start(ctx context.Context)
}

type App struct {
	Config          config.Config
	Logger          *slog.Logger
	Handler         http.Handler
	TracingShutdown observability.ShutdownFunc

	// The SMTP worker's lifetime, owned here rather than left running on a
	// context nothing can cancel. stopWorker is nil when no worker was started,
	// which is the normal case in every environment that has SMTP disabled.
	stopWorker context.CancelFunc
	workerDone chan struct{}

	// Whether the SMTP worker goroutine is currently running. Read by readiness
	// and written by the goroutine, so it is atomic: a plain bool here is a data
	// race, and the answer it gives decides whether the pod claims it can send
	// mail.
	workerRunning atomic.Bool
}

// SMTPWorkerRunning reports whether the SMTP worker goroutine is alive.
//
// A worker can stop without the process stopping — it refuses to run on a
// configuration whose lease cannot cover a delivery, and it returns if its
// context is cancelled. Readiness has to see that, or the pod goes on
// advertising a mail capability that nothing is serving.
func (a *App) SMTPWorkerRunning() bool {
	return a.workerRunning.Load()
}

// Shutdown stops the SMTP worker, waits for the pass it is in to finish, and
// then releases tracing.
//
// The order matters. The worker writes the outcome of a delivery through the
// database, so tearing anything down before it has stopped is what turns a
// graceful shutdown into a duplicated email on the next poll. If the worker
// does not stop within workerShutdownTimeout the process gives up waiting and
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

// StopWorker asks the SMTP worker to stop claiming and waits for the delivery it
// is in the middle of, within whichever deadline arrives first.
//
// Exported so it can be handed to httpserver as a shutdown hook: the worker then
// stops the moment SIGTERM arrives and drains alongside the HTTP server rather
// than after it, which is what keeps the two budgets from being added together.
func (a *App) StopWorker(ctx context.Context) error {
	if a.stopWorker == nil {
		return nil
	}
	a.stopWorker()
	return a.awaitWorker(ctx)
}

func (a *App) awaitWorker(ctx context.Context) error {
	fallback := time.NewTimer(workerShutdownTimeout)
	defer fallback.Stop()
	select {
	case <-a.workerDone:
		a.Logger.Info("smtp worker stopped")
		return nil
	case <-ctx.Done():
		a.Logger.Warn("smtp worker did not stop before the process deadline")
		return ctx.Err()
	case <-fallback.C:
		a.Logger.Warn("smtp worker did not stop in time",
			"timeout_seconds", int(workerShutdownTimeout.Seconds()))
		return context.DeadlineExceeded
	}
}

func New(cfg config.Config) *App {
	logger := platformlog.New(cfg.ServiceName, cfg.Env)
	application := &App{Logger: logger}
	obsCfg := observability.LoadConfig(cfg.ServiceName)
	shutdown, _ := observability.SetupTracing(context.Background(), obsCfg)

	pool := openPool(cfg, logger)
	decryptor := openDecryptor(cfg, logger)
	application.startSMTPWorker(cfg, pool, decryptor, logger)

	application.Config = cfg
	application.Handler = httpapi.NewRouter(cfg, logger,
		httpapi.WithSMTPWorkerProbe(application.SMTPWorkerRunning))
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

// runWorker starts the worker on a cancellable context and records how to stop
// it and how to know it has stopped.
func (a *App) runWorker(w smtpWorker) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	a.stopWorker = cancel
	a.workerDone = done
	a.workerRunning.Store(true)
	go func() {
		defer close(done)
		// Whatever ends the worker — shutdown, or a refusal to run at all —
		// readiness stops claiming the capability from this moment.
		defer a.workerRunning.Store(false)
		startSMTPWorker(ctx, w)
	}()
}
