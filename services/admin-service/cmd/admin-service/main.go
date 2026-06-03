package main

import (
	"context"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/nicrepository/nchat/services/admin-service/internal/app"
	"github.com/nicrepository/nchat/services/admin-service/internal/config"
)

func main() {
	cfg := config.Load()
	application := app.New(cfg)
	addr := ":" + strconv.Itoa(cfg.Port)
	httpServer := &http.Server{
		Addr:              addr,
		Handler:           application.Handler,
		ReadHeaderTimeout: time.Duration(cfg.ReadHeaderTimeoutSeconds) * time.Second,
	}

	application.Logger.Info("service starting", "port", cfg.Port)
	serveErr := httpServer.ListenAndServe()
	_ = application.TracingShutdown(context.Background())
	if serveErr != nil && serveErr != http.ErrServerClosed {
		log.Fatalf("%s failed: %v", cfg.ServiceName, serveErr)
	}
}
