// Package httpserver runs an HTTP server that survives a termination signal
// long enough to finish what it was doing.
//
// It exists because of Blue/Green (issue #626), but the gap it closes is older:
// a Go process that installs no signal handler is killed outright by SIGTERM,
// so every request in flight fails, no WebSocket is closed deliberately, and
// the application's own Shutdown — which releases database pools, stops the
// chat hub and flushes tracing — never runs at all. Retiring the previous
// release slot is exactly when that happens to every pod at once.
package httpserver

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"
)

// Variables, not constants, only so the tests can exercise the drain path
// without a five-second wait. Nothing in production reassigns them.
var (
	// How long the server keeps accepting requests after the signal.
	//
	// Kubernetes sends SIGTERM and removes the Pod from its Services'
	// endpoints at the same moment, and endpoint removal has to propagate to
	// every kube-proxy and to Traefik. A server that stops accepting
	// immediately therefore refuses connections that the cluster is still
	// sending it. Serving through that window is what turns a scale-down into
	// a drain.
	drainDelay = 5 * time.Second

	// The whole termination budget: the propagation window, the HTTP drain and
	// any subsystem shutting down alongside it, together.
	//
	// One budget, not one per stage. Adding a 5s window to a 45s HTTP drain to a
	// 40s worker drain gave a worst case of 90s against a
	// terminationGracePeriodSeconds of 60, so the kubelet's SIGKILL could land
	// mid-cleanup — exactly the outcome the graceful path exists to avoid.
	// Everything below therefore shares this one deadline, and the 15s that
	// remain are margin for the caller's final cleanup and scheduling jitter.
	shutdownBudget = 45 * time.Second
)

// Options carries optional participation in the process's shutdown.
type Options struct {
	// OnShutdown runs as soon as termination begins, concurrently with the HTTP
	// drain rather than after it, and returns when its subsystem has stopped.
	//
	// It receives the same context as the HTTP shutdown, so a subsystem cannot
	// extend the process's termination past the shared budget. Optional: a
	// service with nothing else to drain leaves it nil and behaves exactly as
	// before.
	OnShutdown func(ctx context.Context) error
}

// Run serves until the server stops on its own or the process is asked to
// terminate, and returns only once in-flight requests have finished or the
// timeout has passed. A clean stop is not an error.
//
// It does not close hijacked connections: WebSockets are owned by the chat
// hub, which the caller shuts down after this returns.
func Run(server *http.Server, logger *slog.Logger) error {
	return RunWithOptions(server, logger, Options{})
}

// RunWithOptions is Run with a subsystem that must drain alongside the HTTP
// server, under the same deadline.
func RunWithOptions(server *http.Server, logger *slog.Logger, opts Options) error {
	return run(server, logger, opts, server.ListenAndServe)
}

// run takes the serving call as an argument so the drain path can be exercised
// against a listener the test owns, rather than a fixed port.
func run(server *http.Server, logger *slog.Logger, opts Options, serve func() error) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	served := make(chan error, 1)
	go func() { served <- serve() }()

	select {
	case err := <-served:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	case <-ctx.Done():
		return drain(server, logger, opts)
	}
}

func drain(server *http.Server, logger *slog.Logger, opts Options) error {
	markShutdownStarted()
	ctx, cancel := context.WithTimeout(context.Background(), shutdownBudget)
	defer cancel()
	logDraining(logger)
	// Started before the propagation window, not after it: a worker must stop
	// taking new work the moment the signal arrives, or it spends the drain
	// claiming jobs the process is about to abandon.
	hook := startHook(ctx, opts)
	waitFor(ctx, drainDelay)
	serverErr := server.Shutdown(ctx)
	return firstError(serverErr, waitForHook(ctx, hook))
}

func logDraining(logger *slog.Logger) {
	if logger == nil {
		return
	}
	logger.Info("termination signal received; draining",
		"drain_seconds", int(drainDelay.Seconds()),
		"shutdown_budget_seconds", int(shutdownBudget.Seconds()))
}

// startHook runs the caller's subsystem shutdown. A nil hook yields a closed
// channel, so the wait below is a no-op for services that have none.
func startHook(ctx context.Context, opts Options) <-chan error {
	result := make(chan error, 1)
	if opts.OnShutdown == nil {
		close(result)
		return result
	}
	go func() {
		defer close(result)
		result <- opts.OnShutdown(ctx)
	}()
	return result
}

func waitForHook(ctx context.Context, hook <-chan error) error {
	select {
	case err := <-hook:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// waitFor sleeps, but never past the shared deadline.
func waitFor(ctx context.Context, d time.Duration) {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
	}
}

func firstError(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

// shutdownStartedAt records when termination began, in Unix nanoseconds, so
// work done after Run returns can be bounded by what is left of the one budget
// rather than starting a fresh, unbounded one.
var shutdownStartedAt atomic.Int64

func markShutdownStarted() {
	shutdownStartedAt.CompareAndSwap(0, time.Now().UnixNano())
}

// minCleanup is the floor for the post-Run cleanup context.
//
// Without it a drain that used the whole budget would hand the caller a context
// that is already expired, so the database pool and the tracing exporter would
// be abandoned rather than closed. Overshooting the budget by this much still
// leaves ample room inside a 60s grace period.
// A var, like the other timings, so tests can exercise the floor without a
// two-second wait. Nothing in production reassigns it.
var minCleanup = 2 * time.Second

// cleanupFallback applies when Run returned without a termination signal — the
// server failed on its own. There is no budget to share, but cleanup must not
// hang either.
var cleanupFallback = 5 * time.Second

// CleanupContext bounds whatever a service does after Run returns — closing
// pools, stopping workers, flushing traces — by the remainder of the process's
// termination budget.
//
// It exists so that every service shares one deadline without growing a second
// signal handler. The alternative each of them had was
// context.Background(), which is unbounded: the HTTP drain could spend most of
// terminationGracePeriodSeconds and the cleanup that followed had no limit at
// all, so the kubelet's SIGKILL arrived in the middle of it.
func CleanupContext() (context.Context, context.CancelFunc) {
	started := shutdownStartedAt.Load()
	if started == 0 {
		return context.WithTimeout(context.Background(), cleanupFallback)
	}
	remaining := shutdownBudget - time.Since(time.Unix(0, started))
	if remaining < minCleanup {
		remaining = minCleanup
	}
	return context.WithTimeout(context.Background(), remaining)
}
