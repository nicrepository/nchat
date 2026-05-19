package main

import (
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/nicrepository/nchat/services/search-service/internal/server"
)

const (
	serviceName = "search-service"
	defaultPort = "8086"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

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
	if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		logger.Error("service failed", "service", serviceName, "error", err)
		os.Exit(1)
	}
}
