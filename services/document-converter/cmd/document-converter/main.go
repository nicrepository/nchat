package main

import (
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/nicrepository/nchat/libs/go/platform/httpserver"
	"github.com/nicrepository/nchat/services/document-converter/internal/converter"
)

func main() {
	address := os.Getenv("LISTEN_ADDRESS")
	if address == "" {
		address = ":8089"
	}
	workDir := os.Getenv("CONVERTER_WORK_DIR")
	if workDir == "" {
		workDir = "/tmp/document-converter"
	}
	runner, err := converter.NewLibreOfficeRunner("soffice", workDir, 40*time.Second)
	if err != nil {
		slog.Error("document converter failed to start", "error", err)
		os.Exit(1)
	}
	server := &http.Server{
		Addr:              address,
		Handler:           converter.NewHandler(runner),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       45 * time.Second,
		WriteTimeout:      45 * time.Second,
		IdleTimeout:       30 * time.Second,
	}
	logger := slog.Default()
	logger.Info("document converter listening", "address", address)
	// httpserver.Run owns the signal handling and graceful drain end to end —
	// its own defer never escapes into this function, so there is nothing
	// pending here for os.Exit to skip (the exitAfterDefer gosec/staticcheck
	// finding the previous hand-rolled signal.NotifyContext + defer stop() +
	// os.Exit(1) in this same function used to trigger).
	if err := httpserver.Run(server, logger); err != nil {
		logger.Error("document converter stopped", "error", err)
		os.Exit(1)
	}
}
