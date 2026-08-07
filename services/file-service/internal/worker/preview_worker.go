// Package worker runs file-service's background work.
//
// There is one job today: producing attachment previews (RF-31). It follows the
// shape auth's e-mail outbox worker already established in this repository —
// a ticker, a claimed batch, and a context that ends the loop — rather than
// introducing a scheduler, a queue broker or a goroutine pool for one task.
package worker

import (
	"context"
	"log/slog"
	"time"
)

// previewPollInterval is how often a replica looks for work. It is the whole
// latency budget of the feature: an upload's preview appears at most one poll
// after it finishes, plus the render.
//
// Ten seconds is deliberately unhurried. The claim is a single indexed query
// against a partial index that is empty whenever there is no backlog, so idle
// polling costs almost nothing, and shortening it would only trade database
// round trips for a delay nobody watching a chat window would notice.
const previewPollInterval = 10 * time.Second

// cleanupPollInterval is how often outstanding object cleanups are retried.
//
// Slower than the preview poll because the work is different: a cleanup job
// exists only after something already failed, and the object it removes is
// invisible to users. Retrying it every thirty seconds reclaims storage
// promptly without turning a storage outage into a tight retry loop.
const cleanupPollInterval = 30 * time.Second

// PreviewProcessor is the use case this worker drives. It is an interface so
// the loop can be tested without a database, a renderer or storage.
type PreviewProcessor interface {
	ProcessDue(ctx context.Context) (int, error)
}

// NewObjectCleanup builds the worker that drains the durable cleanup queue
// (SR-002).
//
// It is the same loop as the preview worker's, with a different cadence, and
// deliberately not a second implementation of it: both claim a batch, process
// it, and stop when their context ends.
func NewObjectCleanup(processor PreviewProcessor, logger *slog.Logger) *Preview {
	if logger == nil {
		logger = slog.Default()
	}
	return &Preview{processor: processor, interval: cleanupPollInterval, logger: logger}
}

// Preview polls for attachments awaiting a preview and processes them.
type Preview struct {
	processor PreviewProcessor
	interval  time.Duration
	logger    *slog.Logger
}

// NewPreview builds the worker. A nil logger falls back to the default one, as
// everywhere else in this service.
func NewPreview(processor PreviewProcessor, logger *slog.Logger) *Preview {
	if logger == nil {
		logger = slog.Default()
	}
	return &Preview{processor: processor, interval: previewPollInterval, logger: logger}
}

// Start runs until ctx is cancelled and then returns.
//
// The returning matters: the caller starts this in a goroutine it owns and
// waits for it during shutdown, so there is no background work still holding a
// database connection after Shutdown has returned. Cancellation is also passed
// into the pass itself, so a shutdown interrupts an in-flight batch rather than
// waiting for it.
func (w *Preview) Start(ctx context.Context) {
	if w == nil || w.processor == nil {
		return
	}
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.poll(ctx)
		}
	}
}

// poll runs one pass. A failure is logged and the loop continues: the rows it
// could not claim stay due, so the next tick retries them without any state
// having changed.
func (w *Preview) poll(ctx context.Context) {
	if _, err := w.processor.ProcessDue(ctx); err != nil {
		if ctx.Err() != nil {
			// Shutdown, not a failure.
			return
		}
		// The error itself is not logged: it can carry driver text. The
		// category is what an operator acts on, and the metrics carry the rest.
		w.logger.LogAttrs(ctx, slog.LevelWarn, "preview worker pass failed",
			slog.String("error_type", "process_due_failed"))
	}
}
