package main

import (
	"context"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/nicrepository/nchat/services/file-service/internal/app"
	"github.com/nicrepository/nchat/services/file-service/internal/config"
)

func main() {
	cfg := config.Load()
	if err := cfg.Validate(); err != nil {
		log.Fatalf("%s configuration invalid: %v", cfg.ServiceName, err)
	}
	application, err := app.New(cfg)
	if err != nil {
		log.Fatalf("%s initialization failed: %v", cfg.ServiceName, err)
	}
	addr := ":" + strconv.Itoa(cfg.Port)
	httpServer := &http.Server{
		Addr:              addr,
		Handler:           application.Handler,
		ReadHeaderTimeout: time.Duration(cfg.ReadHeaderTimeoutSeconds) * time.Second,
		ReadTimeout:       time.Duration(cfg.ReadTimeoutSeconds) * time.Second,
		WriteTimeout:      time.Duration(cfg.WriteTimeoutSeconds) * time.Second,
	}

	application.Logger.Info("service starting", "port", cfg.Port)
	serveErr := httpServer.ListenAndServe()
	_ = application.Shutdown(context.Background())
	if serveErr != nil && serveErr != http.ErrServerClosed {
		log.Fatalf("%s failed: %v", cfg.ServiceName, serveErr)
	}
}
