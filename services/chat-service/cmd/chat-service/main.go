package main

import (
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/nicrepository/nchat/services/chat-service/internal/app"
	"github.com/nicrepository/nchat/services/chat-service/internal/config"
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
	if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("%s failed: %v", cfg.ServiceName, err)
	}
}
