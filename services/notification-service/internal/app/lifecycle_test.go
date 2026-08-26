package app

import (
	"context"
	"errors"
	"testing"
	"time"
)

// A worker that is already stopped, or was never started, must not delay
// shutdown at all.
func TestStopWorkerIsImmediateWhenNoWorkerRuns(t *testing.T) {
	application := &App{Logger: quietTestLogger()}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := application.StopWorker(ctx); err != nil {
		t.Fatalf("StopWorker: %v", err)
	}
}

// The composite case: the worker is mid-delivery when shutdown begins. It must
// stop claiming immediately, finish what it holds, and return inside the
// deadline the process gave it.
func TestStopWorkerWaitsForWorkInFlightWithinTheDeadline(t *testing.T) {
	worker := newFakeSMTPWorkerStarter()
	application := &App{Logger: quietTestLogger()}
	application.runWorker(worker)

	select {
	case <-worker.started:
	case <-time.After(2 * time.Second):
		t.Fatal("worker never started")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- application.StopWorker(ctx) }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("StopWorker returned %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("StopWorker did not return once the worker stopped")
	}
	if worker.ctx.Err() == nil {
		t.Fatal("the worker was never told to stop")
	}
}

// A worker that overruns must not hold the process past the deadline it was
// given. The process deadline wins over the worker's own fallback.
func TestStopWorkerRespectsTheCallersDeadline(t *testing.T) {
	worker := newStuckWorker()
	application := &App{Logger: quietTestLogger()}
	application.runWorker(worker)
	<-worker.started
	t.Cleanup(func() { close(worker.release) })

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := application.StopWorker(ctx)
	elapsed := time.Since(start)

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("StopWorker returned %v, want a deadline error", err)
	}
	// workerShutdownTimeout is 40s; honouring it here instead of the caller's
	// deadline is precisely the bug this guards.
	if elapsed > 5*time.Second {
		t.Fatalf("StopWorker waited %s, ignoring the caller's 50ms deadline", elapsed)
	}
}

// Shutdown must still flush tracing after the worker has stopped.
func TestShutdownStopsTheWorkerThenReleasesTracing(t *testing.T) {
	worker := newFakeSMTPWorkerStarter()
	tracingReleased := false
	application := &App{Logger: quietTestLogger()}
	application.runWorker(worker)
	<-worker.started
	application.TracingShutdown = func(context.Context) error {
		if worker.ctx.Err() == nil {
			t.Error("tracing was released before the worker stopped")
		}
		tracingReleased = true
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := application.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if !tracingReleased {
		t.Fatal("tracing was never released")
	}
}

// stuckWorker never returns until the test lets it, standing in for a delivery
// that outlives the budget.
type stuckWorker struct {
	started chan struct{}
	release chan struct{}
	ctx     context.Context
}

func newStuckWorker() *stuckWorker {
	return &stuckWorker{started: make(chan struct{}), release: make(chan struct{})}
}

func (s *stuckWorker) Start(ctx context.Context) {
	s.ctx = ctx
	close(s.started)
	<-s.release
}
