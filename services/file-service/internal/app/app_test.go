package app

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/nicrepository/nchat/services/file-service/internal/config"
	"github.com/nicrepository/nchat/services/file-service/internal/domain"
	httpapi "github.com/nicrepository/nchat/services/file-service/internal/http"
	"github.com/nicrepository/nchat/services/file-service/internal/service"
	"github.com/nicrepository/nchat/services/file-service/internal/storage"
)

func randomBase64Key(t *testing.T) string {
	t.Helper()
	key := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return base64.StdEncoding.EncodeToString(key)
}

func healthOnlyConfig() config.Config {
	return config.Config{
		ServiceName: "file-service", Env: "test", Port: 8083,
		ReadHeaderTimeoutSeconds: 5,
	}
}

func uploadsEnabledConfig(t *testing.T) config.Config {
	t.Helper()
	cfg := healthOnlyConfig()
	cfg.UploadsEnabled = true
	cfg.MaxUploadBytes = domain.DefaultMaxUploadBytes
	cfg.DatabaseURL = "postgres://nchat@localhost:5432/nchat"
	cfg.DBConnectTimeoutSeconds = 5
	cfg.AuthJWTHMACSecret = strings.Repeat("s", 32)
	cfg.AuthJWTIssuer = "nchat-auth"
	cfg.AuthJWTAudience = "nchat-api"
	cfg.EncryptionMasterKey = randomBase64Key(t)
	cfg.EncryptionMasterKeyID = "kek-test-active"
	cfg.SeaweedFSFilerURL = "http://seaweedfs-filer:8888"
	cfg.SeaweedFSTimeoutSeconds = 30
	cfg.MalwareScanRequired = true
	cfg.DBMaxConnections = 10
	cfg.UploadMaxConcurrent = 4
	cfg.UploadMaxConcurrentPerUser = 2
	cfg.UploadRetryAfterSeconds = 30
	return cfg
}

type stubPool struct{ closed bool }

func (p *stubPool) QueryRow(context.Context, string, ...any) pgx.Row { return nil }
func (p *stubPool) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, errors.New("query not configured")
}
func (p *stubPool) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}
func (p *stubPool) Ping(context.Context) error { return nil }
func (p *stubPool) Close()                     { p.closed = true }

func openStub(pool *stubPool) func(context.Context, string, int, int) (storage.Pool, error) {
	return func(context.Context, string, int, int) (storage.Pool, error) { return pool, nil }
}

// fenceStub stands in for the PostgreSQL-backed attachment fence, for the same
// reason admissionStub stands in for admission control: both pin a session
// advisory lock to a real connection, and these tests are about wiring.
func fenceStub() func(storage.Pool, *slog.Logger) (storage.AttachmentFencing, error) {
	return func(storage.Pool, *slog.Logger) (storage.AttachmentFencing, error) {
		return noopFence{}, nil
	}
}

type noopFence struct{}

func (noopFence) Acquire(context.Context, string) (service.FenceHandle, error) {
	return noopFenceHandle{}, nil
}

func (noopFence) WithinTransaction(
	context.Context, int64,
	func(storage.TransactionalQuerier) (service.AttachmentLifecycleState, error),
) (service.AttachmentLifecycleState, error) {
	return service.AttachmentLifecycleState{}, errors.New("wiring stub does not run statements")
}

type noopFenceHandle struct{}

func (noopFenceHandle) Release(context.Context) {}

// admissionStub stands in for the PostgreSQL-backed control, which needs a real
// connection to pin a session lock to. Wiring, not admission behaviour, is what
// these tests are about.
func admissionStub() func(storage.Pool, storage.UploadAdmissionLimits, *slog.Logger) (httpapi.UploadAdmission, error) {
	return func(storage.Pool, storage.UploadAdmissionLimits, *slog.Logger) (httpapi.UploadAdmission, error) {
		return noopAdmission{}, nil
	}
}

type noopAdmission struct{}

func (noopAdmission) Acquire(context.Context, string, int64) (func(), error) {
	return func() {}, nil
}

func noopShutdown(context.Context) error { return nil }

func TestNewBuildsAHealthOnlyServiceByDefault(t *testing.T) {
	cfg := healthOnlyConfig()
	application, err := newApp(cfg, appDependencies{tracingShutdown: noopShutdown})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if application.Logger == nil || application.Handler == nil {
		t.Fatalf("expected an initialized app, got %+v", application)
	}
	if application.Config != cfg {
		t.Fatalf("expected config %+v, got %+v", cfg, application.Config)
	}

	response := httptest.NewRecorder()
	application.Handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("expected a healthy service, got %d", response.Code)
	}
	if err := application.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
}

func TestNewRefusesAnInvalidConfiguration(t *testing.T) {
	cfg := uploadsEnabledConfig(t)
	cfg.EncryptionMasterKey = ""

	if _, err := newApp(cfg, appDependencies{tracingShutdown: noopShutdown}); err == nil {
		t.Fatal("a service with uploads enabled must not start without a master key")
	}
}

func TestNewRefusesAWeakJWTSecret(t *testing.T) {
	cfg := uploadsEnabledConfig(t)
	cfg.AuthJWTHMACSecret = "short"

	if _, err := newApp(cfg, appDependencies{tracingShutdown: noopShutdown}); err == nil {
		t.Fatal("a weak JWT secret must stop start-up")
	}
}

func TestNewWiresTheAttachmentRoutesWhenUploadsAreEnabled(t *testing.T) {
	pool := &stubPool{}
	application, err := newApp(uploadsEnabledConfig(t), appDependencies{
		openDB: openStub(pool), openAdmission: admissionStub(), openFence: fenceStub(),
		tracingShutdown: noopShutdown,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Without credentials the route must answer 401, not the 503 a disabled
	// feature returns: that difference proves the wiring took effect.
	response := httptest.NewRecorder()
	application.Handler.ServeHTTP(response,
		httptest.NewRequest(http.MethodGet, "/attachments/00000000-0000-4000-8000-000000000000", nil))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 from a wired route, got %d: %s", response.Code, response.Body.String())
	}

	if err := application.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	if !pool.closed {
		t.Fatal("shutdown must close the connection pool")
	}
}

func TestNewFailsWhenTheDatabaseIsUnreachable(t *testing.T) {
	dbErr := errors.New("dial tcp db-primary.internal:5432: connection refused")
	_, err := newApp(uploadsEnabledConfig(t), appDependencies{
		openDB: func(context.Context, string, int, int) (storage.Pool, error) {
			return nil, dbErr
		},
		openAdmission:   admissionStub(),
		tracingShutdown: noopShutdown,
	})
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), "db-primary.internal") {
		t.Fatal("the DSN and driver text must never reach the start-up error")
	}
}

func TestNewFailsWithoutADatabaseOpener(t *testing.T) {
	if _, err := newApp(uploadsEnabledConfig(t), appDependencies{tracingShutdown: noopShutdown}); err == nil {
		t.Fatal("expected an error when no database opener is wired")
	}
}

func TestNewFailsOnAnUnusableStorageClient(t *testing.T) {
	cfg := uploadsEnabledConfig(t)
	// Validate() accepts a zero timeout because Load() never produces one; the
	// store refuses it anyway, so the two checks stay independent.
	cfg.SeaweedFSTimeoutSeconds = 0

	if _, err := newApp(cfg, appDependencies{
		openDB: openStub(&stubPool{}), tracingShutdown: noopShutdown,
	}); err == nil {
		t.Fatal("expected an error for an unusable storage timeout")
	}
}

func TestShutdownIsIdempotent(t *testing.T) {
	pool := &stubPool{}
	application, err := newApp(uploadsEnabledConfig(t), appDependencies{
		openDB: openStub(pool), openAdmission: admissionStub(), openFence: fenceStub(),
		tracingShutdown: noopShutdown,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := application.Shutdown(context.Background()); err != nil {
		t.Fatalf("first shutdown: %v", err)
	}
	if err := application.Shutdown(context.Background()); err != nil {
		t.Fatalf("second shutdown: %v", err)
	}

	var nilApp *App
	if err := nilApp.Shutdown(context.Background()); err != nil {
		t.Fatalf("nil shutdown: %v", err)
	}
}

func TestShutdownPropagatesTracingErrors(t *testing.T) {
	tracingErr := errors.New("exporter failed")
	application, err := newApp(healthOnlyConfig(), appDependencies{
		tracingShutdown: func(context.Context) error { return tracingErr },
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := application.Shutdown(context.Background()); !errors.Is(err, tracingErr) {
		t.Fatalf("expected the tracing error, got %v", err)
	}
}

func TestShutdownTracingHelperToleratesNil(t *testing.T) {
	if err := shutdownTracing(nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// Startup, not just Validate: an unsafe combination must stop the process, and
// the readiness endpoint must never come up to serve it.
//
// This is the regression from the review — an absent APP_ENV used to default to
// "development" and grant the malware-scan exception to any deployment that had
// simply forgotten the variable.
func TestNewRefusesToDisableTheScanGateWithoutAnExplicitDevelopmentEnv(t *testing.T) {
	for name, env := range map[string]string{
		"absent":     "",
		"unknown":    "unknown",
		"production": "production",
		"staging":    "staging",
		"typo":       "developmnt",
	} {
		t.Run(name, func(t *testing.T) {
			cfg := uploadsEnabledConfig(t)
			cfg.Env = env
			cfg.MalwareScanRequired = false

			application, err := newApp(cfg, appDependencies{
				openDB: openStub(&stubPool{}), openAdmission: admissionStub(),
				tracingShutdown: noopShutdown,
			})
			if err == nil {
				_ = application.Shutdown(context.Background())
				t.Fatalf("APP_ENV=%q must not allow the scan gate to be disabled", env)
			}
			if application != nil {
				t.Fatal("no application may be returned for an unsafe configuration")
			}
			if !strings.Contains(err.Error(), "FILE_MALWARE_SCAN_REQUIRED") {
				t.Fatalf("expected the scan-policy error, got %v", err)
			}
		})
	}
}

// The safe pairing still starts, so the check above is not passing because the
// service refuses everything.
func TestNewStartsWithTheScanGateDisabledInADeclaredDevelopmentEnv(t *testing.T) {
	cfg := uploadsEnabledConfig(t)
	cfg.Env = "development"
	cfg.MalwareScanRequired = false

	application, err := newApp(cfg, appDependencies{
		openDB: openStub(&stubPool{}), openAdmission: admissionStub(), openFence: fenceStub(),
		tracingShutdown: noopShutdown,
	})
	if err != nil {
		t.Fatalf("a declared development environment must start: %v", err)
	}
	t.Cleanup(func() { _ = application.Shutdown(context.Background()) })

	response := httptest.NewRecorder()
	application.Handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("expected a healthy service, got %d", response.Code)
	}
}

// --- shutdown ordering (RF-31 preview worker) ----------------------------

// The ordinary path: the worker stops, and only then is the pool closed. The
// worker holds the pool for the length of a job, so the reverse order would
// fail the very statement that records a job's outcome.
func TestShutdownStopsThePreviewWorkerBeforeClosingThePool(t *testing.T) {
	pool := &stubPool{}
	application, err := newApp(uploadsEnabledConfig(t), appDependencies{
		openDB: openStub(pool), openAdmission: admissionStub(), openFence: fenceStub(),
		tracingShutdown: noopShutdown,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Observe the order rather than assert it indirectly: the worker's
	// completion channel is what Shutdown waits on, so a pool closed while it is
	// still open would be the bug this test exists for.
	stopped := make(chan struct{})
	realDone := application.previewDone
	application.previewDone = stopped
	closedWhileRunning := false
	go func() {
		<-realDone
		closedWhileRunning = pool.closed
		close(stopped)
	}()

	if err := application.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	if closedWhileRunning {
		t.Fatal("the pool was closed while the preview worker was still running")
	}
	if !pool.closed {
		t.Fatal("the pool must be closed once the worker has stopped")
	}
}

// The bounded case: a worker that will not stop must not take the process with
// it, and must not have its dependencies pulled out from under it either. The
// incomplete shutdown is reported rather than swallowed.
func TestShutdownReportsAWorkerThatOutlivesItsDeadline(t *testing.T) {
	pool := &stubPool{}
	application, err := newApp(uploadsEnabledConfig(t), appDependencies{
		openDB: openStub(pool), openAdmission: admissionStub(), openFence: fenceStub(),
		tracingShutdown: noopShutdown,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// A worker that never returns: the channel is never closed.
	application.previewDone = make(chan struct{})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err = application.Shutdown(ctx)
	if !errors.Is(err, errShutdownIncomplete) {
		t.Fatalf("shutdown error = %v, want errShutdownIncomplete", err)
	}
	if pool.closed {
		t.Fatal("the pool must not be closed while a job may still be using it")
	}
}

// Cancelling is what reaches the job in flight: the render's context descends
// from the worker's, so a shutdown ends a running render instead of waiting for
// its full timeout.
func TestShutdownCancelsTheWorkerContext(t *testing.T) {
	application := &App{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	cancelled := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	application.previewCancel = cancel
	done := make(chan struct{})
	application.previewDone = done
	go func() {
		<-ctx.Done()
		close(cancelled)
		close(done)
	}()

	if err := application.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	select {
	case <-cancelled:
	default:
		t.Fatal("shutdown must cancel the worker's context")
	}
}
