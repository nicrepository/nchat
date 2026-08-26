package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

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
	runner := converter.NewLibreOfficeRunner("soffice", workDir, 40*time.Second)
	server := &http.Server{
		Addr:              address,
		Handler:           converter.NewHandler(runner),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       45 * time.Second,
		WriteTimeout:      45 * time.Second,
		IdleTimeout:       30 * time.Second,
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	slog.Info("document converter listening", "address", address)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("document converter stopped", "error", err)
		os.Exit(1)
	}
}
