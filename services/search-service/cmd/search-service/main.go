package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/nicrepository/nchat/libs/go/platform/observability"
	"github.com/nicrepository/nchat/services/search-service/internal/server"
)

const (
	serviceName = "search-service"
	defaultPort = "8086"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	obsCfg := observability.LoadConfig(serviceName)
	shutdown, _ := observability.SetupTracing(context.Background(), obsCfg)

	port := os.Getenv("PORT")
	if port == "" {
		port = defaultPort
	}

	addr := ":" + port
	httpServer := &http.Server{
		Addr:              addr,
		Handler:           server.NewHandler(serviceName),
		ReadHeaderTimeout: 5 * time.Second,
	}

	logger.Info("service starting", "service", serviceName, "port", port)
	serveErr := httpServer.ListenAndServe()
	_ = shutdown(context.Background())
	if serveErr != nil && serveErr != http.ErrServerClosed {
		logger.Error("service failed", "service", serviceName, "error", serveErr)
		os.Exit(1)
	}
}
