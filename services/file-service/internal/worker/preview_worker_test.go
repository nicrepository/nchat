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

// attrCapturingHandler records the attributes of every record it is given, so a
// test can assert on one field rather than on a formatted line.
type attrCapturingHandler struct {
	mu    sync.Mutex
	attrs []slog.Attr
	// named is closed once a record carrying the `worker` attribute has been
	// handled. It exists because the line under test is the one thing a
	// cancellation suppresses: a failed pass whose context is already done is
	// shutdown rather than a failure, and poll deliberately logs nothing for it.
	// Waiting for the pass to *start* and cancelling therefore raced the log,
	// and losing that race read the attribute as "" — the CI failure this
	// replaces. Waiting for the record itself cannot race it.
	named chan struct{}
	once  sync.Once
}

func newAttrCapturingHandler() *attrCapturingHandler {
	return &attrCapturingHandler{named: make(chan struct{})}
}

func (h *attrCapturingHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *attrCapturingHandler) Handle(_ context.Context, record slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	named := false
	record.Attrs(func(attr slog.Attr) bool {
		h.attrs = append(h.attrs, attr)
		if attr.Key == "worker" {
			named = true
		}
		return true
	})
	if named {
		h.once.Do(func() { close(h.named) })
	}
	return nil
}

func (h *attrCapturingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *attrCapturingHandler) WithGroup(string) slog.Handler      { return h }

// value returns the last value logged under key, or "" if the key never
// appeared.
func (h *attrCapturingHandler) value(key string) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	found := ""
	for _, attr := range h.attrs {
		if attr.Key == key {
			found = attr.Value.String()
		}
	}
	return found
}

// Several jobs share this loop, so a failure has to say which one failed.
//
// The assertion is on the `worker` attribute alone and not on the message: the
// wording is free to change, but an operator triaging a stuck scan queue must
// never be reading a line that claims a preview failed.
func TestAFailedPassNamesTheWorkerThatFailed(t *testing.T) {
	for _, tc := range []struct {
		name  string
		build func(worker.PreviewProcessor, *slog.Logger) *worker.Preview
		want  string
	}{
		{"preview", worker.NewPreview, "preview"},
		{"cleanup", worker.NewObjectCleanup, "object_cleanup"},
		{"scan", worker.NewMalwareScan, "malware_scan"},
		{"draft expiry", worker.NewDraftExpiry, "draft_expiry"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handler := newAttrCapturingHandler()
			processor := newCountingProcessor()
			processor.err = errors.New("connection refused")

			job := tc.build(processor, slog.New(handler))
			job.SetIntervalForTest(10 * time.Millisecond)

			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan struct{})
			go func() {
				defer close(done)
				job.Start(ctx)
			}()
			// The failed pass has been logged — not merely started. Cancelling on
			// the start of the pass instead would suppress the very line this
			// asserts on whenever it arrived first.
			select {
			case <-handler.named:
			case <-time.After(2 * time.Second):
				cancel()
				<-done
				t.Fatal("the worker never logged a failed pass")
			}
			cancel()
			<-done

			if got := handler.value("worker"); got != tc.want {
				t.Fatalf("worker attribute = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDraftExpiryUsesSafeDefaultLoggerAndRuns(t *testing.T) {
	processor := newCountingProcessor()
	job := worker.NewDraftExpiry(processor, nil)
	job.SetIntervalForTest(10 * time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		job.Start(ctx)
	}()
	select {
	case <-processor.signal:
	case <-time.After(2 * time.Second):
		cancel()
		<-done
		t.Fatal("draft expiry worker never processed a pass")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("draft expiry worker did not stop after cancellation")
	}
	if got := processor.callCount(); got < 1 {
		t.Fatalf("draft expiry passes = %d, want at least one", got)
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
