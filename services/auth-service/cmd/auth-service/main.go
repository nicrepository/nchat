package main

import (
	"context"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/nicrepository/nchat/services/auth-service/internal/app"
	"github.com/nicrepository/nchat/services/auth-service/internal/config"
)

func main() {
	cfg := config.Load()
	application, err := app.New(cfg)
	if err != nil {
		// err is a static, sanitized bootstrap error — safe to log.
		log.Fatalf("%s bootstrap failed: %v", cfg.ServiceName, err)
	}

	addr := ":" + strconv.Itoa(cfg.Port)
	httpServer := &http.Server{
		Addr:              addr,
		Handler:           application.Handler,
		ReadHeaderTimeout: time.Duration(cfg.ReadHeaderTimeoutSeconds) * time.Second,
	}

	application.Logger.Info("service starting", "port", cfg.Port)
	serveErr := httpServer.ListenAndServe()
	_ = application.Shutdown(context.Background())
	if serveErr != nil && serveErr != http.ErrServerClosed {
		log.Fatalf("%s failed: %v", cfg.ServiceName, serveErr)
	}
}
