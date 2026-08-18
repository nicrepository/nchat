package main

import (
	"context"
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

	cfg := config.Load()
	if err := cfg.Validate(); err != nil {
		logger.Error("invalid configuration", "error", err)
		os.Exit(1)
	}
	obsCfg := observability.LoadConfig(serviceName)
	shutdown, _ := observability.SetupTracing(context.Background(), obsCfg)
	pool, err := storage.OpenDB(context.Background(), cfg.DatabaseURL, cfg.DBConnectTimeoutSeconds)
	if err != nil {
		logger.Error("database unavailable", "error", err)
		_ = shutdown(context.Background())
		os.Exit(1)
	}
	defer pool.Close()
	tokens, err := server.NewTokenValidator(cfg.AuthJWTHMACSecret, cfg.AuthJWTIssuer, cfg.AuthJWTAudience)
	if err != nil {
		logger.Error("auth configuration invalid", "error", err)
		_ = shutdown(context.Background())
		os.Exit(1)
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
			logger.Error("service failed", "service", serviceName, "error", serveErr)
			os.Exit(1)
		}
	case <-sigCtx.Done():
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(ctx); err != nil {
			logger.Error("shutdown failed", "error", err)
		}
	}
	_ = shutdown(context.Background())
}
