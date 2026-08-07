package app

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	platformlog "github.com/nicrepository/nchat/libs/go/platform/log"
	"github.com/nicrepository/nchat/libs/go/platform/observability"
	"github.com/nicrepository/nchat/services/file-service/internal/config"
	"github.com/nicrepository/nchat/services/file-service/internal/crypto"
	httpapi "github.com/nicrepository/nchat/services/file-service/internal/http"
	"github.com/nicrepository/nchat/services/file-service/internal/preview"
	"github.com/nicrepository/nchat/services/file-service/internal/service"
	"github.com/nicrepository/nchat/services/file-service/internal/storage"
	"github.com/nicrepository/nchat/services/file-service/internal/worker"
)

const (
	uploadRateLimitPerMinute = 20
	uploadRateLimitWindow    = time.Minute
)

type App struct {
	Config          config.Config
	Logger          *slog.Logger
	Handler         http.Handler
	TracingShutdown observability.ShutdownFunc
	pool            storage.Pool
	rateLimiter     *httpapi.UserRateLimiter
	// previewCancel stops the preview worker and previewDone reports that it
	// has actually returned. Both are needed: cancelling only asks, and closing
	// the pool while a claim is still in flight would fail that query for no
	// reason. Shutdown therefore stops the worker, waits for it, and only then
	// releases what it was using.
	previewCancel context.CancelFunc
	previewDone   <-chan struct{}
	cleanupCancel context.CancelFunc
	cleanupDone   <-chan struct{}
	shutdownOnce  sync.Once
	shutdownErr   error
}

type appDependencies struct {
	openDB          func(context.Context, string, int, int) (storage.Pool, error)
	openAdmission   func(storage.Pool, storage.UploadAdmissionLimits, *slog.Logger) (httpapi.UploadAdmission, error)
	openFence       func(storage.Pool, *slog.Logger) (storage.AttachmentFencing, error)
	tracingShutdown observability.ShutdownFunc
}

// New validates the configuration and wires the service. It fails rather than
// starting with a half-built attachment feature: a missing master key, an
// unreachable database or an invalid storage endpoint is a start-up error, not
// a runtime surprise on the first upload.
func New(cfg config.Config) (*App, error) {
	return newApp(cfg, appDependencies{
		openDB:        storage.OpenDBWithMaxConns,
		openAdmission: newUploadAdmission,
		openFence:     newAttachmentFence,
	})
}

func newApp(cfg config.Config, deps appDependencies) (*App, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	logger := platformlog.New(cfg.ServiceName, cfg.Env)
	obsCfg := observability.LoadConfig(cfg.ServiceName)
	shutdown := deps.tracingShutdown
	if shutdown == nil {
		shutdown, _ = observability.SetupTracing(context.Background(), obsCfg)
	}
	application := &App{Config: cfg, Logger: logger, TracingShutdown: shutdown}

	metrics := observability.NewMetrics(obsCfg)
	routerDeps := httpapi.RouterDependencies{Observability: metrics}
	if cfg.UploadsEnabled {
		if err := application.wireAttachments(cfg, logger, metrics, deps, &routerDeps); err != nil {
			_ = shutdownTracing(shutdown)
			return nil, err
		}
	}
	application.Handler = httpapi.NewRouter(cfg, logger, routerDeps)
	return application, nil
}

func (a *App) wireAttachments(
	cfg config.Config,
	logger *slog.Logger,
	metrics *observability.Metrics,
	deps appDependencies,
	routerDeps *httpapi.RouterDependencies,
) error {
	validator, err := httpapi.NewTokenValidator(cfg.AuthJWTHMACSecret, cfg.AuthJWTIssuer, cfg.AuthJWTAudience)
	if err != nil {
		return err
	}
	keys, err := crypto.NewKeyring(
		cfg.EncryptionMasterKeyID, cfg.EncryptionMasterKey, cfg.EncryptionPreviousKeys,
	)
	if err != nil {
		return err
	}
	objects, err := storage.NewSeaweedFSStore(
		cfg.SeaweedFSFilerURL, time.Duration(cfg.SeaweedFSTimeoutSeconds)*time.Second,
	)
	if err != nil {
		return err
	}
	if deps.openDB == nil || deps.openAdmission == nil || deps.openFence == nil {
		return errDependenciesUnavailable
	}
	pool, err := deps.openDB(
		context.Background(), cfg.DatabaseURL, cfg.DBConnectTimeoutSeconds, cfg.DBMaxConnections,
	)
	if err != nil {
		// The DSN and the driver message never reach the caller or the log.
		return errDependenciesUnavailable
	}
	// Cluster-wide upload admission. It needs connections it can reserve for the
	// duration of a transfer, which the per-statement Pool interface cannot
	// express, so it is wired from the concrete pool. A pool that cannot supply
	// them leaves admission unwired, and the routes then answer 503 rather than
	// accepting uncounted uploads.
	admission, err := deps.openAdmission(pool, storage.UploadAdmissionLimits{
		Global:  cfg.UploadMaxConcurrent,
		PerUser: cfg.UploadMaxConcurrentPerUser,
	}, logger)
	if err != nil {
		pool.Close()
		return errDependenciesUnavailable
	}

	attachmentMetrics := httpapi.NewAttachmentMetrics(metrics)
	limiter := httpapi.NewUserRateLimiter(uploadRateLimitPerMinute, uploadRateLimitWindow)
	a.pool = pool
	a.rateLimiter = limiter

	routerDeps.TokenValidator = validator
	routerDeps.Attachments = service.NewAttachmentService(
		storage.NewPGXDestinationAuthorizer(pool),
		storage.NewPGXAttachmentStore(pool),
		objects,
		keys,
		cfg.MaxUploadBytes,
		cfg.MalwareScanRequired,
		attachmentMetrics,
		logger,
	)
	routerDeps.Admission = admission
	routerDeps.RateLimiter = limiter
	routerDeps.ReadinessPinger = pool
	routerDeps.StoragePinger = objects
	routerDeps.Metrics = attachmentMetrics

	fence, err := deps.openFence(pool, logger)
	if err != nil {
		pool.Close()
		return errDependenciesUnavailable
	}

	cleanupStore := storage.NewPGXObjectCleanupStore(pool)
	a.startPreviewWorker(service.NewPreviewService(
		storage.NewPGXPreviewStore(pool),
		objects,
		keys,
		preview.New(),
		fence,
		cleanupStore,
		attachmentMetrics,
		logger,
	), logger)
	a.startCleanupWorker(service.NewObjectCleanupService(
		cleanupStore, objects, attachmentMetrics, logger,
	), logger)
	return nil
}

// startPreviewWorker runs preview generation for as long as the app lives.
//
// The goroutine is owned, not fired and forgotten: its context is cancelled by
// Shutdown and its completion is observable, so the process never exits with a
// render still holding a database connection. It is wired here, inside the
// attachment wiring, so a deployment with uploads disabled starts no worker at
// all — there would be nothing for it to do and no dependencies for it to use.
func (a *App) startPreviewWorker(previews *service.PreviewService, logger *slog.Logger) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		worker.NewPreview(previews, logger).Start(ctx)
	}()
	a.previewCancel = cancel
	a.previewDone = done
}

// startCleanupWorker drains the durable object-cleanup queue (SR-002).
//
// It is owned exactly like the preview worker: cancelled by Shutdown, waited
// for before the pool closes, so a delete in flight never loses its connection
// underneath it.
func (a *App) startCleanupWorker(cleanups *service.ObjectCleanupService, logger *slog.Logger) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		worker.NewObjectCleanup(cleanups, logger).Start(ctx)
	}()
	a.cleanupCancel = cancel
	a.cleanupDone = done
}

func (a *App) Shutdown(ctx context.Context) error {
	if a == nil {
		return nil
	}
	a.shutdownOnce.Do(func() {
		var failures []error
		if a.rateLimiter != nil {
			a.rateLimiter.Stop()
		}
		// The order is a correctness requirement, not tidiness. The preview
		// worker holds the pool for the length of a job — a claim, a decrypted
		// read, a terminal write — so closing the pool first would fail those
		// statements and, worse, could fail the write that records a job's
		// outcome, leaving the row to be rendered again after the restart.
		// Both workers hold the pool, so both must be stopped before it closes.
		previewStopped := a.stopPreviewWorker(ctx)
		cleanupStopped := a.stopWorker(ctx, a.cleanupCancel, a.cleanupDone)
		if previewStopped && cleanupStopped {
			if a.pool != nil {
				a.pool.Close()
			}
		} else {
			// The worker did not stop inside the grace period. Closing the pool
			// now would pull it out from under work that is still running, so
			// it is deliberately left open: the process is about to exit and
			// the operating system will reclaim the sockets, which is the
			// lesser of the two failures. Saying so is the point — an
			// incomplete shutdown must be visible in the exit path, not
			// swallowed by a function that reports success either way.
			a.Logger.LogAttrs(ctx, slog.LevelError, "shutdown incomplete",
				slog.String("reason", "preview_worker_still_running"),
				slog.Bool("pool_closed", false),
			)
			failures = append(failures, errShutdownIncomplete)
		}
		if a.TracingShutdown != nil {
			if err := a.TracingShutdown(ctx); err != nil {
				failures = append(failures, err)
			}
		}
		// Join of nothing is nil, so the ordinary path still reports success.
		a.shutdownErr = errors.Join(failures...)
	})
	return a.shutdownErr
}

// stopPreviewWorker cancels the worker and waits for it to return, reporting
// whether it actually stopped.
//
// Cancelling comes first and does two things at once: it ends the polling loop
// so no new row is claimed, and it reaches the job in flight — the render's
// context descends from this one, so an in-progress PDF sandbox is closed
// rather than run to completion. The writes that record a job's outcome are
// detached from it deliberately, so cancelling never leaves a rendered preview
// unrecorded.
//
// The wait is bounded by the shutdown context the caller owns, because a worker
// that will not stop must not hold the process open forever. The boolean is
// what stops that bound from becoming silent data loss: the caller uses it to
// decide whether the pool may be closed, and an unreported false would put the
// service back where it started, closing dependencies out from under live work.
func (a *App) stopPreviewWorker(ctx context.Context) bool {
	return a.stopWorker(ctx, a.previewCancel, a.previewDone)
}

// stopWorker cancels one worker and waits for it, reporting whether it stopped.
func (a *App) stopWorker(ctx context.Context, cancel context.CancelFunc, done <-chan struct{}) bool {
	if cancel == nil {
		return true
	}
	cancel()
	if done == nil {
		return true
	}
	select {
	case <-done:
		return true
	case <-ctx.Done():
		return false
	}
}

var (
	errDependenciesUnavailable = errors.New("attachment dependencies unavailable")
	// errShutdownIncomplete is returned when the preview worker outlived the
	// shutdown deadline. It is an error rather than a log line alone because
	// the process is about to exit with dependencies still in use, and whoever
	// called Shutdown is the only one who can still act on that.
	errShutdownIncomplete = errors.New("shutdown incomplete: preview worker did not stop in time")
)

func shutdownTracing(shutdown observability.ShutdownFunc) error {
	if shutdown == nil {
		return nil
	}
	return shutdown(context.Background())
}

// newAttachmentFence builds the PostgreSQL-backed attachment fence (RF-31,
// SR-001).
//
// Like admission control it needs the concrete pool: the fence is a session
// advisory lock held on one connection for the length of a render, and the
// per-statement storage.Pool interface has no way to lend one out. A pool that
// cannot supply one therefore cannot run the preview worker safely, and saying
// so here keeps the failure at start-up rather than turning into a render that
// nothing serialises.
func newAttachmentFence(pool storage.Pool, logger *slog.Logger) (storage.AttachmentFencing, error) {
	lockPool, ok := storage.LockConnPoolFrom(pool)
	if !ok {
		return nil, errDependenciesUnavailable
	}
	txPool, ok := storage.TransactionPoolFrom(pool)
	if !ok {
		return nil, errDependenciesUnavailable
	}
	return storage.NewPGXAttachmentFence(lockPool, txPool, logger), nil
}

// newUploadAdmission builds the PostgreSQL-backed admission control.
//
// It needs the concrete pool: a slot is a session advisory lock held on a
// connection reserved for the whole upload, and the per-statement storage.Pool
// interface has no way to lend one out. A pool that is not the pgx
// implementation therefore cannot support admission, and saying so here keeps
// the failure at start-up rather than on the first upload.
func newUploadAdmission(
	pool storage.Pool, limits storage.UploadAdmissionLimits, logger *slog.Logger,
) (httpapi.UploadAdmission, error) {
	lockPool, ok := storage.LockConnPoolFrom(pool)
	if !ok {
		return nil, errDependenciesUnavailable
	}
	return storage.NewPGXUploadAdmission(lockPool, limits, logger), nil
}
