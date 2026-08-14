package app

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
	platformlog "github.com/nicrepository/nchat/libs/go/platform/log"
	"github.com/nicrepository/nchat/libs/go/platform/observability"
	"github.com/nicrepository/nchat/libs/go/platform/urlsafety"
	"github.com/nicrepository/nchat/services/file-service/internal/config"
	"github.com/nicrepository/nchat/services/file-service/internal/crypto"
	"github.com/nicrepository/nchat/services/file-service/internal/events"
	httpapi "github.com/nicrepository/nchat/services/file-service/internal/http"
	"github.com/nicrepository/nchat/services/file-service/internal/linkpreview"
	"github.com/nicrepository/nchat/services/file-service/internal/preview"
	"github.com/nicrepository/nchat/services/file-service/internal/scanner"
	"github.com/nicrepository/nchat/services/file-service/internal/service"
	"github.com/nicrepository/nchat/services/file-service/internal/storage"
	"github.com/nicrepository/nchat/services/file-service/internal/worker"
)

const (
	uploadRateLimitPerMinute = 20
	uploadRateLimitWindow    = time.Minute

	// linkPreviewRateLimitPerMinute bounds how much outbound fetching one user
	// can cause (RF-10). It is its own budget rather than a share of the upload
	// one because the two protect different things and would otherwise starve
	// each other: this is the control that stops the endpoint being used to
	// sweep the internet, or this deployment's own bandwidth, on request.
	//
	// It is per-process, the same known ceiling the upload limiter carries: N
	// replicas grant N budgets. The cache is what keeps the common case — the
	// same link previewed by everyone in a channel — from reaching the network
	// at all.
	linkPreviewRateLimitPerMinute = 30
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
	// linkPreviewLimiter is stopped by Shutdown like the upload one: it owns a
	// goroutine that sweeps expired windows.
	linkPreviewLimiter *httpapi.UserRateLimiter
	cleanupCancel      context.CancelFunc
	cleanupDone        <-chan struct{}
	scanCancel         context.CancelFunc
	scanDone           <-chan struct{}
	// statusPublisher is the bus connection the scan worker announces verdicts
	// on. Nil whenever no bus is configured, which is a supported deployment:
	// verdicts are still persisted and clients still see them on their next read.
	statusPublisher *events.Publisher
	shutdownOnce    sync.Once
	shutdownErr     error
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
	// Independent of uploads: link previews touch no database, no storage and
	// no key material, so a deployment may run them with uploads switched off.
	if cfg.LinkPreviewEnabled {
		if err := application.wireLinkPreviews(cfg, metrics, &routerDeps); err != nil {
			_ = shutdownTracing(shutdown)
			return nil, err
		}
	}
	application.Handler = httpapi.NewRouter(cfg, logger, routerDeps)
	return application, nil
}

// wireLinkPreviews builds the RF-10 route's dependencies.
//
// The token validator is shared with the attachment routes when those are also
// enabled: it is derived entirely from configuration, so building a second one
// would only mean two objects answering identically.
func (a *App) wireLinkPreviews(
	cfg config.Config, metrics *observability.Metrics, routerDeps *httpapi.RouterDependencies,
) error {
	if routerDeps.TokenValidator == nil {
		validator, err := httpapi.NewTokenValidator(
			cfg.AuthJWTHMACSecret, cfg.AuthJWTIssuer, cfg.AuthJWTAudience,
		)
		if err != nil {
			return err
		}
		routerDeps.TokenValidator = validator
	}
	if routerDeps.Metrics == nil {
		routerDeps.Metrics = httpapi.NewAttachmentMetrics(metrics)
	}
	limiter := httpapi.NewUserRateLimiter(linkPreviewRateLimitPerMinute, uploadRateLimitWindow)
	a.linkPreviewLimiter = limiter
	routerDeps.LinkPreviewRateLimiter = limiter
	previews := linkpreview.NewService(
		time.Duration(cfg.LinkPreviewTimeoutSeconds)*time.Second,
		time.Duration(cfg.LinkPreviewCacheTTLSeconds)*time.Second,
		routerDeps.Metrics,
	)
	// RF-21. Config validation has already refused an enabled check without
	// credentials, so a constructor failure here is not an operator mistake this
	// service can serve through: without the checker every preview would proceed
	// unchecked, which is the one outcome the flag was set to prevent.
	if cfg.LinkSafetyEnabled {
		scanner, err := urlsafety.NewCloudflareScanner(
			cfg.LinkSafetyCloudflareAccount, cfg.LinkSafetyCloudflareToken,
		)
		if err != nil {
			return err
		}
		previews = previews.WithURLSafety(urlsafety.NewService(scanner, urlsafety.NewMetrics(metrics)))
	}
	routerDeps.LinkPreviews = previews
	return nil
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
	a.startMalwareScanWorker(cfg, pool, fence, objects, keys, attachmentMetrics, logger)
	return nil
}

// startMalwareScanWorker drains the antimalware scan queue (RF-22).
//
// It starts only when a scanner is configured, and that asymmetry is the
// fail-closed reading of an absent one: with no daemon there is nothing that
// could produce a verdict, so a worker would only poll a queue it can never
// drain. Every upload then stays in pending_scan and stays undownloadable,
// which is the correct behaviour for a deployment with no scanner — the gate
// itself is never what a missing address relaxes.
//
// A scanner that cannot be constructed from a validated address is the same
// case: the worker does not start, and no attachment is approved. Nothing here
// can fail the service's start-up, because uploads, listings and downloads of
// already-approved files all keep working without a scan worker.
//
// The status publisher is separately optional. Without a bus the verdict is
// still persisted and still authoritative; clients learn it from their next
// read rather than immediately.
func (a *App) startMalwareScanWorker(
	cfg config.Config,
	pool storage.Pool,
	fence storage.AttachmentFencing,
	objects service.ObjectStore,
	keys *crypto.Keyring,
	metrics *httpapi.AttachmentMetrics,
	logger *slog.Logger,
) {
	if cfg.MalwareScannerAddress == "" {
		logger.LogAttrs(context.Background(), slog.LevelWarn, "malware scan worker not started",
			slog.String("reason", "scanner_not_configured"),
			slog.Bool("uploads_downloadable_without_scan", !cfg.MalwareScanRequired),
		)
		return
	}
	malware, err := scanner.New(
		cfg.MalwareScannerAddress, time.Duration(cfg.MalwareScanTimeoutSeconds)*time.Second,
	)
	if err != nil {
		logger.LogAttrs(context.Background(), slog.LevelError, "malware scan worker not started",
			slog.String("reason", "scanner_unavailable"),
		)
		return
	}

	// Nil when no bus is configured, and passed through as a typed nil would not
	// be: the service checks the interface for nil, so an interface holding a
	// nil *Publisher would pass that check and then fail on every verdict.
	var publisher service.AttachmentStatusPublisher
	if cfg.ValkeyURL != "" {
		instanceID := cfg.WSInstanceID
		if instanceID == "" {
			// Only used for the consumer's echo suppression, so uniqueness is the
			// whole requirement and a random value satisfies it.
			instanceID = "file-" + uuid.New().String()
		}
		concrete, publisherErr := events.NewPublisher(cfg.ValkeyURL, instanceID)
		if publisherErr != nil {
			logger.LogAttrs(context.Background(), slog.LevelWarn, "attachment status bus unavailable",
				slog.String("reason", "publisher_unavailable"),
			)
		} else {
			a.statusPublisher = concrete
			publisher = concrete
		}
	}

	// The verdict store is the fenced one, the same the rest of the service
	// uses. The *worker* holds no fence — it is the verdict's producer, and a
	// session lock held for the length of a large scan would be a lock its own
	// rejection then waits for — but the rejection statement itself must still
	// run under it, so a condemned attachment cannot commit underneath a
	// renderer already holding its plaintext.
	scans := service.NewMalwareScanService(
		storage.NewPGXScanStore(pool),
		storage.NewFencedAttachmentStore(pool, fence),
		objects,
		keys,
		malware,
		publisher,
		metrics,
		// One source of truth for the budget: the same configured value the
		// scanner client dials with also bounds the job and derives the lease.
		// A service that cancelled at its own fixed deadline would make a
		// longer configured timeout unreachable and every large scan a retry.
		time.Duration(cfg.MalwareScanTimeoutSeconds)*time.Second,
		logger,
	)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		worker.NewMalwareScan(scans, logger).Start(ctx)
	}()
	a.scanCancel = cancel
	a.scanDone = done
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
		if a.linkPreviewLimiter != nil {
			a.linkPreviewLimiter.Stop()
		}
		// The order is a correctness requirement, not tidiness. The preview
		// worker holds the pool for the length of a job — a claim, a decrypted
		// read, a terminal write — so closing the pool first would fail those
		// statements and, worse, could fail the write that records a job's
		// outcome, leaving the row to be rendered again after the restart.
		// Both workers hold the pool, so both must be stopped before it closes.
		previewStopped := a.stopPreviewWorker(ctx)
		cleanupStopped := a.stopWorker(ctx, a.cleanupCancel, a.cleanupDone)
		scanStopped := a.stopWorker(ctx, a.scanCancel, a.scanDone)
		if previewStopped && cleanupStopped && scanStopped {
			if a.pool != nil {
				a.pool.Close()
			}
			// After the workers, never before: the publish that follows a
			// verdict runs on a context detached from the job's, so closing the
			// bus first would drop the announcement of a verdict that was
			// already written.
			//
			// Nil whenever no bus is configured, which is a supported
			// deployment. Checked here rather than relied on inside Close, so
			// this reads like every other optional resource above it.
			if a.statusPublisher != nil {
				a.statusPublisher.Close()
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
