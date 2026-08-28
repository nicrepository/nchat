package main

import (
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/nicrepository/nchat/libs/go/platform/httpserver"
	"github.com/nicrepository/nchat/services/notification-service/internal/app"
	"github.com/nicrepository/nchat/services/notification-service/internal/config"
)

// shutdownApp keeps the cleanup context in its own scope: main ends in
// log.Fatalf on a serve error, which exits the process without running deferred
// calls, so a cancel deferred there would never fire.
//
// Tracing last, and briefly: the worker has already stopped by the time this
// runs, so it only has to flush. It is what the shutdown budget leaves room for.
func shutdownApp(application *app.App) {
	// The same remaining-budget context every other service uses, so the
	// termination budget lives in one place rather than a constant per service.
	ctx, cancel := httpserver.CleanupContext()
	defer cancel()
	_ = application.Shutdown(ctx)
}

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
	// The worker drains alongside the HTTP server, not after it.
	//
	// Running them in sequence added their budgets together — a 5s propagation
	// window plus a 45s HTTP drain plus a 40s worker wait is 90s against a
	// terminationGracePeriodSeconds of 60, so the kubelet could SIGKILL the
	// process mid-cleanup. As a shutdown hook the worker stops claiming the
	// moment the signal arrives and shares the one termination budget.
	serveErr := httpserver.RunWithOptions(httpServer, application.Logger, httpserver.Options{
		OnShutdown: application.StopWorker,
	})
	shutdownApp(application)
	if serveErr != nil && serveErr != http.ErrServerClosed {
		log.Fatalf("%s failed: %v", cfg.ServiceName, serveErr)
	}
}
