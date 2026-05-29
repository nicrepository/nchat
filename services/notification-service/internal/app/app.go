package app

import (
	"context"
	"log/slog"
	"net/http"
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
	startSMTPWorker = func(w smtpWorker) {
		go w.Start(context.Background())
	}
)

type smtpWorker interface {
	Start(ctx context.Context)
}

type App struct {
	Config          config.Config
	Logger          *slog.Logger
	Handler         http.Handler
	TracingShutdown observability.ShutdownFunc
}

func New(cfg config.Config) *App {
	logger := platformlog.New(cfg.ServiceName, cfg.Env)
	obsCfg := observability.LoadConfig(cfg.ServiceName)
	shutdown, _ := observability.SetupTracing(context.Background(), obsCfg)

	var pool storage.Pool
	if cfg.DatabaseURL != "" {
		ctx, cancel := context.WithTimeout(context.Background(), time.Duration(cfg.DBConnectTimeoutSeconds)*time.Second)
		defer cancel()
		openedPool, err := openDB(ctx, cfg.DatabaseURL, cfg.DBConnectTimeoutSeconds)
		if err != nil {
			logger.Warn("database unavailable; smtp worker disabled", "reason", "open_db_failed")
		} else {
			pool = openedPool
		}
	}

	var decryptor *emailcrypto.Encryptor
	if cfg.AuthEmailOutboxEncKey != "" {
		openedDecryptor, err := newEncryptor(cfg.AuthEmailOutboxEncKey)
		if err != nil {
			logger.Warn("smtp worker disabled", "reason", "invalid_email_outbox_encryption_key")
		} else {
			decryptor = openedDecryptor
		}
	}

	ready, reason := cfg.SMTPWorkerReady()
	if cfg.SMTPWorkerEnabled {
		switch {
		case !ready:
			logger.Warn("smtp worker disabled", "reason", reason)
		case pool == nil:
			logger.Warn("smtp worker disabled", "reason", "database_not_configured")
		case decryptor == nil:
			logger.Warn("smtp worker disabled", "reason", "email_outbox_encryption_unavailable")
		default:
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
			} else {
				startSMTPWorker(newSMTPWorker(cfg, storage.NewPGXOutboxStore(pool), decryptor, sender, logger))
				logger.Info("smtp worker started")
			}
		}
	}

	return &App{Config: cfg, Logger: logger, Handler: httpapi.NewRouter(cfg, logger), TracingShutdown: shutdown}
}
