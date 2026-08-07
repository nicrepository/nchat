package worker_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/nicrepository/nchat/services/file-service/internal/worker"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// countingProcessor records passes and can report a failure, so the loop's own
// behaviour is observable without a database.
type countingProcessor struct {
	mu sync.Mutex

	calls int
	err   error
	// signal is closed after the first pass, so a test can wait for the loop
	// to have actually run instead of sleeping for a fixed time.
	signal chan struct{}
	// cancelledCalls counts passes that arrived with an already-cancelled
	// context, which must never happen after Start has returned.
	cancelledCalls int
}

func newCountingProcessor() *countingProcessor {
	return &countingProcessor{signal: make(chan struct{})}
}

func (p *countingProcessor) ProcessDue(ctx context.Context) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	if ctx.Err() != nil {
		p.cancelledCalls++
	}
	if p.calls == 1 {
		close(p.signal)
	}
	return 0, p.err
}

func (p *countingProcessor) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

// startWorker runs the loop with a short interval so a test does not wait for
// the production poll, and returns a channel that closes when it has stopped.
func startWorker(t *testing.T, processor worker.PreviewProcessor) (context.CancelFunc, <-chan struct{}) {
	t.Helper()
	preview := worker.NewPreview(processor, discardLogger())
	preview.SetIntervalForTest(10 * time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		preview.Start(ctx)
	}()
	return cancel, done
}

func TestStartPollsUntilItIsCancelled(t *testing.T) {
	processor := newCountingProcessor()
	cancel, done := startWorker(t, processor)
	defer cancel()

	select {
	case <-processor.signal:
	case <-time.After(2 * time.Second):
		t.Fatal("the worker never ran a pass")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the worker did not return after cancellation")
	}

	// The loop must be finished for good: nothing may run after Start returned,
	// which is what lets Shutdown close the pool safely.
	before := processor.callCount()
	time.Sleep(50 * time.Millisecond)
	if after := processor.callCount(); after != before {
		t.Fatalf("the worker ran %d more passes after returning", after-before)
	}
}

// A failing pass must not end the loop: the rows it could not claim are still
// due, and the next tick retries them.
func TestStartKeepsPollingAfterAFailedPass(t *testing.T) {
	processor := newCountingProcessor()
	processor.err = errors.New("connection refused")
	cancel, done := startWorker(t, processor)
	defer func() {
		cancel()
		<-done
	}()

	select {
	case <-processor.signal:
	case <-time.After(2 * time.Second):
		t.Fatal("the worker never ran a pass")
	}

	deadline := time.After(2 * time.Second)
	for processor.callCount() < 3 {
		select {
		case <-deadline:
			t.Fatalf("the worker stopped after %d passes", processor.callCount())
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func TestStartReturnsImmediatelyWithoutAProcessor(t *testing.T) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		worker.NewPreview(nil, discardLogger()).Start(context.Background())
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("a worker with nothing to drive must not block")
	}
}

// The cleanup worker is the same loop on a slower cadence, so it has to stop
// the same way: cancelled, and returned from.
func TestObjectCleanupWorkerStopsWhenCancelled(t *testing.T) {
	processor := newCountingProcessor()
	cleanup := worker.NewObjectCleanup(processor, discardLogger())
	cleanup.SetIntervalForTest(10 * time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		cleanup.Start(ctx)
	}()

	select {
	case <-processor.signal:
	case <-time.After(2 * time.Second):
		t.Fatal("the cleanup worker never ran a pass")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the cleanup worker did not return after cancellation")
	}
}
