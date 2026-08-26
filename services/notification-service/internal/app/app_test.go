package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/nicrepository/nchat/libs/go/platform/emailcrypto"
	"github.com/nicrepository/nchat/services/notification-service/internal/config"
	"github.com/nicrepository/nchat/services/notification-service/internal/storage"
	"github.com/nicrepository/nchat/services/notification-service/internal/worker"
)

func TestNewCreatesApp(t *testing.T) {
	cfg := config.Config{ServiceName: "notification-service", Env: "test", Port: 8084, ReadHeaderTimeoutSeconds: 5}
	app := New(cfg)
	if app == nil || app.Logger == nil || app.Handler == nil {
		t.Fatalf("expected initialized app, got %+v", app)
	}
	if app.Config != cfg {
		t.Fatalf("expected config %+v, got %+v", cfg, app.Config)
	}
}

func TestNew_WithInvalidDatabaseURLStillCreatesApp(t *testing.T) {
	cfg := config.Config{
		ServiceName:              "notification-service",
		Env:                      "test",
		Port:                     8084,
		ReadHeaderTimeoutSeconds: 5,
		DatabaseURL:              "postgres://user@127.0.0.1:1/nchat?sslmode=disable",
		DBConnectTimeoutSeconds:  1,
	}

	app := New(cfg)
	if app == nil || app.Handler == nil {
		t.Fatalf("expected initialized app, got %+v", app)
	}
}

func TestNew_WithInvalidEncryptionKeyStillCreatesApp(t *testing.T) {
	cfg := config.Config{
		ServiceName:              "notification-service",
		Env:                      "test",
		Port:                     8084,
		ReadHeaderTimeoutSeconds: 5,
		AuthEmailOutboxEncKey:    "invalid-key",
	}

	app := New(cfg)
	if app == nil || app.Handler == nil {
		t.Fatalf("expected initialized app, got %+v", app)
	}
}

func TestNew_WithWorkerEnabledButNotReadyStillCreatesApp(t *testing.T) {
	cfg := config.Config{
		ServiceName:              "notification-service",
		Env:                      "test",
		Port:                     8084,
		ReadHeaderTimeoutSeconds: 5,
		SMTPWorkerEnabled:        true,
	}

	app := New(cfg)
	if app == nil || app.Handler == nil {
		t.Fatalf("expected initialized app, got %+v", app)
	}
}

func TestNew_WithWorkerEnabledWithoutDatabaseStillCreatesApp(t *testing.T) {
	cfg := config.Config{
		ServiceName:              "notification-service",
		Env:                      "test",
		Port:                     8084,
		ReadHeaderTimeoutSeconds: 5,
		AuthEmailOutboxEncKey:    validEncryptionKey(),
		SMTPWorkerEnabled:        true,
		SMTPHost:                 "localhost",
		SMTPFrom:                 "no-reply@example.com",
		SMTPTLSMode:              "none",
	}

	app := New(cfg)
	if app == nil || app.Handler == nil {
		t.Fatalf("expected initialized app, got %+v", app)
	}
}

func TestNew_WithWorkerEnabledWithoutDecryptorDoesNotStartWorker(t *testing.T) {
	restoreFactories(t)
	openDB = func(context.Context, string, int) (storage.Pool, error) { return fakePool{}, nil }
	newEncryptor = func(string) (*emailcrypto.Encryptor, error) { return nil, errors.New("invalid key") }

	started := false
	startSMTPWorker = func(context.Context, smtpWorker) { started = true }

	app := New(validWorkerConfig())
	if app == nil || app.Handler == nil {
		t.Fatalf("expected initialized app, got %+v", app)
	}
	if started {
		t.Fatal("expected smtp worker not to start when decryptor is unavailable")
	}
}

func TestNew_WithInvalidSMTPSenderDoesNotStartWorker(t *testing.T) {
	restoreFactories(t)
	openDB = func(context.Context, string, int) (storage.Pool, error) { return fakePool{}, nil }
	newEncryptor = emailcrypto.New
	newSMTPSender = func(string, int, string, string, string, string, int) (*worker.NetSMTPSender, error) {
		return nil, errors.New("invalid sender")
	}

	started := false
	startSMTPWorker = func(context.Context, smtpWorker) { started = true }

	app := New(validWorkerConfig())
	if app == nil || app.Handler == nil {
		t.Fatalf("expected initialized app, got %+v", app)
	}
	if started {
		t.Fatal("expected smtp worker not to start when sender creation fails")
	}
}

func TestNew_WithReadyWorkerStartsSMTPWorker(t *testing.T) {
	restoreFactories(t)
	openDB = func(context.Context, string, int) (storage.Pool, error) { return fakePool{}, nil }
	newEncryptor = emailcrypto.New
	newSMTPSender = func(string, int, string, string, string, string, int) (*worker.NetSMTPSender, error) {
		return &worker.NetSMTPSender{}, nil
	}

	starter := newFakeSMTPWorkerStarter()
	newSMTPWorker = func(config.Config, storage.OutboxStore, *emailcrypto.Encryptor, worker.Sender, *slog.Logger) smtpWorker {
		return starter
	}
	startSMTPWorker = func(ctx context.Context, w smtpWorker) { w.Start(ctx) }

	app := New(validWorkerConfig())
	if app == nil || app.Handler == nil {
		t.Fatalf("expected initialized app, got %+v", app)
	}
	select {
	case <-starter.started:
	case <-time.After(2 * time.Second):
		t.Fatal("expected smtp worker to start")
	}
	// Shutdown must cancel the worker's context and wait for it to return.
	if err := app.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if starter.ctx.Err() == nil {
		t.Fatal("Shutdown did not cancel the worker context")
	}
}

type fakePool struct{}

func (fakePool) Begin(context.Context) (pgx.Tx, error)                   { return nil, nil }
func (fakePool) QueryRow(context.Context, string, ...any) pgx.Row        { return nil }
func (fakePool) Query(context.Context, string, ...any) (pgx.Rows, error) { return nil, nil }
func (fakePool) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}

// The worker now runs on a goroutine the App owns, so "did it start" is only
// answerable once. The channel is what makes the assertion deterministic
// instead of a race against the scheduler.
type fakeSMTPWorkerStarter struct {
	started chan struct{}
	ctx     context.Context
}

func newFakeSMTPWorkerStarter() *fakeSMTPWorkerStarter {
	return &fakeSMTPWorkerStarter{started: make(chan struct{})}
}

func (f *fakeSMTPWorkerStarter) Start(ctx context.Context) {
	f.ctx = ctx
	close(f.started)
	<-ctx.Done()
}

func restoreFactories(t *testing.T) {
	t.Helper()
	origOpenDB := openDB
	origNewEncryptor := newEncryptor
	origNewSMTPSender := newSMTPSender
	origNewSMTPWorker := newSMTPWorker
	origStartSMTPWorker := startSMTPWorker
	t.Cleanup(func() {
		openDB = origOpenDB
		newEncryptor = origNewEncryptor
		newSMTPSender = origNewSMTPSender
		newSMTPWorker = origNewSMTPWorker
		startSMTPWorker = origStartSMTPWorker
	})
}

func validWorkerConfig() config.Config {
	return config.Config{
		ServiceName:              "notification-service",
		Env:                      "test",
		Port:                     8084,
		ReadHeaderTimeoutSeconds: 5,
		DatabaseURL:              "postgres://user@127.0.0.1:1/nchat?sslmode=disable",
		DBConnectTimeoutSeconds:  1,
		AuthEmailOutboxEncKey:    validEncryptionKey(),
		SMTPWorkerEnabled:        true,
		SMTPHost:                 "localhost",
		SMTPPort:                 2525,
		SMTPFrom:                 "no-reply@example.com",
		SMTPTLSMode:              "none",
		SMTPTimeoutSeconds:       1,
	}
}

func validEncryptionKey() string {
	return base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32))
}

func quietTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
