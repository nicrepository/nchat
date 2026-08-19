package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/nicrepository/nchat/libs/go/platform/observability"
	"github.com/nicrepository/nchat/services/search-service/internal/config"
	"github.com/nicrepository/nchat/services/search-service/internal/server"
	"github.com/nicrepository/nchat/services/search-service/internal/service"
	"github.com/nicrepository/nchat/services/search-service/internal/storage"
)

const (
	serviceName = "search-service"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(logger); err != nil {
		logger.Error("service stopped", "service", serviceName, "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	cfg := config.Load()
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("invalid configuration: %w", err)
	}
	obsCfg := observability.LoadConfig(serviceName)
	shutdown, _ := observability.SetupTracing(context.Background(), obsCfg)
	defer func() { _ = shutdown(context.Background()) }()
	pool, err := storage.OpenDB(context.Background(), cfg.DatabaseURL, cfg.DBConnectTimeoutSeconds)
	if err != nil {
		return fmt.Errorf("database unavailable: %w", err)
	}
	defer pool.Close()
	tokens, err := server.NewTokenValidator(cfg.AuthJWTHMACSecret, cfg.AuthJWTIssuer, cfg.AuthJWTAudience)
	if err != nil {
		return fmt.Errorf("auth configuration invalid: %w", err)
	}
	searcher := service.New(storage.NewPGXSearchStore(pool))
	handler := server.NewHandlerWithDependencies(serviceName, server.Dependencies{Search: searcher, Tokens: tokens, Sessions: storage.NewPGXSessionValidator(pool), ReadinessPinger: pool})
	port := strconv.Itoa(cfg.Port)
	addr := ":" + port
	httpServer := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: time.Duration(cfg.ReadHeaderTimeoutSeconds) * time.Second,
	}

	logger.Info("service starting", "service", serviceName, "port", port)
	errCh := make(chan error, 1)
	go func() { errCh <- httpServer.ListenAndServe() }()
	sigCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	select {
	case serveErr := <-errCh:
		if serveErr != nil && serveErr != http.ErrServerClosed {
			return fmt.Errorf("serve: %w", serveErr)
		}
	case <-sigCtx.Done():
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(ctx); err != nil {
			return fmt.Errorf("shutdown: %w", err)
		}
	}
	return nil
}
