package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/nicrepository/nchat/libs/go/platform/httpserver"
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
	// Bounded by whatever the HTTP drain left of the termination budget, not by
	// an unbounded context: a tracing exporter that cannot reach its collector
	// would otherwise hold the process open until the kubelet killed it.
	defer func() {
		ctx, cancel := httpserver.CleanupContext()
		defer cancel()
		_ = shutdown(ctx)
	}()
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
	// The same drain every other slot workload uses. search-service had its own
	// shutdown, which closed the listener the moment SIGTERM arrived — before
	// the cluster had finished removing this Pod from its Service endpoints, so
	// requests still being routed here were refused. Sharing the helper is what
	// makes the drain window a property of the platform rather than of whichever
	// service happened to implement it.
	if err := httpserver.Run(httpServer, logger); err != nil {
		return fmt.Errorf("serve: %w", err)
	}
	// The deferred pool.Close and tracing shutdown run after this returns, so
	// in-flight requests finish against a live database and their spans are
	// flushed rather than dropped.
	return nil
}
