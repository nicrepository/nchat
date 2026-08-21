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
	// A configured Admin API that cannot be built is a startup failure, not a
	// degraded mode: nothing reopens the database later, and a readiness probe
	// that never passes does not restart a container. Exiting non-zero is what
	// gives the orchestrator something to act on.
	application, err := app.New(cfg)
	if err != nil {
		log.Fatalf("%s failed to start: %v", cfg.ServiceName, err)
	}
	addr := ":" + strconv.Itoa(cfg.Port)
	httpServer := &http.Server{
		Addr:              addr,
		Handler:           application.Handler,
		ReadHeaderTimeout: time.Duration(cfg.ReadHeaderTimeoutSeconds) * time.Second,
	}

	application.Logger.Info("service starting", "port", cfg.Port, "admin_api", cfg.AdminAPIEnabled())
	serveErr := httpServer.ListenAndServe()
	// Shutdown releases the database pool as well as the tracer, so a restart
	// does not leave a connection behind.
	_ = application.Shutdown(context.Background())
	if serveErr != nil && serveErr != http.ErrServerClosed {
		log.Fatalf("%s failed: %v", cfg.ServiceName, serveErr)
	}
}
