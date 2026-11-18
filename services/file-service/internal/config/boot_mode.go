package config

import (
	"errors"
	"time"

	"github.com/nicrepository/nchat/services/file-service/internal/converter"
)

// Empty remains the combined mode for callers constructed before role isolation.
func (c Config) ServesAPI() bool {
	return c.BootMode == "" || c.BootMode == "combined" || c.BootMode == "api"
}

func (c Config) IsWorker() bool {
	return c.BootMode == "scan-worker" || c.BootMode == "preview-worker"
}

func (c Config) validateBootMode() error {
	if !c.ServesAPI() && !c.IsWorker() {
		return errors.New("FILE_BOOT_MODE must be combined, api, scan-worker or preview-worker")
	}
	if !c.IsWorker() {
		return nil
	}
	if !c.UploadsEnabled || !c.MalwareScanRequired || c.LinkPreviewEnabled || c.LinkSafetyEnabled {
		return errors.New("workers require uploads and malware scanning, with link previews and link safety disabled")
	}
	if c.BootMode == "scan-worker" && c.MalwareScannerAddress == "" {
		return errors.New("scan-worker requires FILE_MALWARE_SCANNER_ADDRESS")
	}
	return nil
}

func (c Config) validatePreviewConverter() error {
	if c.BootMode != "preview-worker" {
		return nil
	}
	// The constructor validates locally without opening connections; keep its rules authoritative.
	_, err := converter.NewClient(c.DocumentConverterURL, time.Duration(c.DocumentConverterTimeoutSeconds)*time.Second)
	return err
}
