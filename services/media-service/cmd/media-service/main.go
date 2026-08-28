package main

import (
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/nicrepository/nchat/libs/go/platform/httpserver"
	"github.com/nicrepository/nchat/services/media-service/internal/app"
	"github.com/nicrepository/nchat/services/media-service/internal/config"
)

// shutdownWithBudget gives cleanup the remainder of the process's termination
// budget instead of an unbounded context.
//
// The HTTP drain can consume most of terminationGracePeriodSeconds; whatever it
// leaves is all the time this has, and a context.Background() here meant the
// kubelet could SIGKILL the process mid-cleanup. Its own function because main
// ends in log.Fatalf, which exits without running deferred calls.
func shutdownWithBudget(application *app.App) {
	ctx, cancel := httpserver.CleanupContext()
	defer cancel()
	_ = application.Shutdown(ctx)
}

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
	serveErr := httpserver.Run(httpServer, application.Logger)
	shutdownWithBudget(application)
	if serveErr != nil && serveErr != http.ErrServerClosed {
		log.Fatalf("%s failed: %v", cfg.ServiceName, serveErr)
	}
}
