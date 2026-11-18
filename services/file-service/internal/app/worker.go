package app

import (
	"context"
	"log/slog"
	"time"

	"github.com/nicrepository/nchat/services/file-service/internal/config"
	"github.com/nicrepository/nchat/services/file-service/internal/converter"
	"github.com/nicrepository/nchat/services/file-service/internal/crypto"
	httpapi "github.com/nicrepository/nchat/services/file-service/internal/http"
	"github.com/nicrepository/nchat/services/file-service/internal/preview"
	"github.com/nicrepository/nchat/services/file-service/internal/service"
	"github.com/nicrepository/nchat/services/file-service/internal/storage"
)

func (a *App) wireWorker(cfg config.Config, metrics *httpapi.AttachmentMetrics, router *httpapi.RouterDependencies, deps appDependencies) error {
	keys, err := crypto.NewKeyring(cfg.EncryptionMasterKeyID, cfg.EncryptionMasterKey, cfg.EncryptionPreviousKeys)
	if err != nil {
		return err
	}
	objects, err := storage.NewSeaweedFSStore(cfg.SeaweedFSFilerURL, time.Duration(cfg.SeaweedFSTimeoutSeconds)*time.Second)
	if err != nil {
		return err
	}
	if a.pool == nil {
		return errDependenciesUnavailable
	}
	fence, err := deps.attachmentFence(a.pool, a.Logger)
	if err != nil {
		return errDependenciesUnavailable
	}
	router.ReadinessPinger = a.pool
	router.StoragePinger = objects
	if cfg.BootMode == "scan-worker" {
		a.startMalwareScanWorker(cfg, a.pool, fence, objects, keys, metrics, a.Logger)
		if a.scanDone == nil {
			return errDependenciesUnavailable
		}
		return nil
	}
	client, err := converter.NewClient(cfg.DocumentConverterURL, time.Duration(cfg.DocumentConverterTimeoutSeconds)*time.Second)
	if err != nil {
		return err
	}
	a.startPreviewWorker(service.NewPreviewService(storage.NewPGXPreviewStore(a.pool), objects, keys,
		preview.NewWithDocumentConverter(client), fence, storage.NewPGXObjectCleanupStore(a.pool), metrics, a.Logger), a.Logger)
	return nil
}

// StopWorkers cancels acquisition at SIGTERM, before the HTTP drain starts.
// Shutdown subsequently waits for completion before releasing pools and the bus.
func (a *App) StopWorkers(context.Context) error {
	for _, cancel := range []context.CancelFunc{a.scanCancel, a.previewCancel, a.cleanupCancel, a.draftExpiryCancel, a.linkScanCancel} {
		if cancel != nil {
			cancel()
		}
	}
	return nil
}

func (a *App) openPool(cfg config.Config, deps appDependencies) error {
	if !needsDatabase(cfg) {
		return nil
	}
	if deps.openDB == nil {
		return errDependenciesUnavailable
	}
	pool, err := deps.openDB(context.Background(), cfg.DatabaseURL, cfg.DBConnectTimeoutSeconds, cfg.DBMaxConnections)
	if err != nil {
		return errDependenciesUnavailable
	}
	a.pool = pool
	return nil
}

func (deps appDependencies) attachmentFence(pool storage.Pool, logger *slog.Logger) (storage.AttachmentFencing, error) {
	if deps.openFence == nil {
		return nil, errDependenciesUnavailable
	}
	return deps.openFence(pool, logger)
}
