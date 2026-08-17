// Package worker runs file-service's background work.
//
// Three jobs share one loop: attachment previews (RF-31), object cleanup
// (SR-002) and antimalware scanning (RF-22). Each names itself in its log
// lines, so the shared loop stays one implementation without its failures
// becoming indistinguishable. It follows the
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

// scanPollInterval is how often outstanding antimalware scans are claimed
// (RF-22).
//
// The same cadence as the preview poll, and for the same reason: it is the
// latency budget of the feature. A fresh upload is not downloadable until the
// scan approves it, so this interval is what a user waits before their own file
// becomes usable — and the claim is one indexed query against a partial index
// that is empty whenever there is no backlog, so idle polling costs almost
// nothing.
//
// Retries are not paced by this. A scan that failed is rescheduled by its own
// claim, which pushes the next attempt out by a lease that grows with the
// attempt count, so a daemon outage does not turn this ticker into a retry
// storm against clamd.
const scanPollInterval = 10 * time.Second

// PreviewProcessor is the use case this worker drives. It is an interface so
// the loop can be tested without a database, a renderer or storage.
type PreviewProcessor interface {
	ProcessDue(ctx context.Context) (int, error)
}

// Worker names, used as the `worker` log attribute. A closed set decided here,
// so the identity in a log line can only ever be one of the three loops that
// actually run — the same discipline the scan results follow.
const (
	workerNamePreview       = "preview"
	workerNameObjectCleanup = "object_cleanup"
	workerNameMalwareScan   = "malware_scan"
	workerNameLinkScan      = "link_scan"
)

// linkScanPollInterval is how often outstanding RF-21 URL scans are advanced.
//
// The same cadence as the preview and antimalware polls, and for the same
// reason: it is the latency budget of the feature. A link preview is withheld
// until its URL has a verdict, so this is what somebody waits before their card
// renders — and it matches the fastest cadence Cloudflare recommends polling a
// scan at.
const linkScanPollInterval = 10 * time.Second

// NewLinkScan builds the worker that drains the RF-21 URL scan queue.
//
// The fourth job on the same loop, and deliberately not a fourth implementation
// of it: all of them claim a batch, process it, and stop when their context
// ends.
func NewLinkScan(processor PreviewProcessor, logger *slog.Logger) *Preview {
	if logger == nil {
		logger = slog.Default()
	}
	return &Preview{
		processor: processor, interval: linkScanPollInterval,
		name: workerNameLinkScan, logger: logger,
	}
}

// NewMalwareScan builds the worker that drains the antimalware scan queue
// (RF-22).
//
// It is the same loop as the preview worker's, with its own cadence, and
// deliberately not a third implementation of it: all three claim a batch,
// process it, and stop when their context ends.
func NewMalwareScan(processor PreviewProcessor, logger *slog.Logger) *Preview {
	if logger == nil {
		logger = slog.Default()
	}
	return &Preview{
		processor: processor, interval: scanPollInterval,
		name: workerNameMalwareScan, logger: logger,
	}
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
	return &Preview{
		processor: processor, interval: cleanupPollInterval,
		name: workerNameObjectCleanup, logger: logger,
	}
}

// Preview polls for work and processes it. Three jobs share it — previews,
// object cleanup and antimalware scans — so it carries the name it runs under:
// a failure logged without one is indistinguishable between the three, which is
// precisely when an operator needs to tell them apart.
type Preview struct {
	processor PreviewProcessor
	interval  time.Duration
	name      string
	logger    *slog.Logger
}

// NewPreview builds the worker. A nil logger falls back to the default one, as
// everywhere else in this service.
func NewPreview(processor PreviewProcessor, logger *slog.Logger) *Preview {
	if logger == nil {
		logger = slog.Default()
	}
	return &Preview{
		processor: processor, interval: previewPollInterval,
		name: workerNamePreview, logger: logger,
	}
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
		w.logger.LogAttrs(ctx, slog.LevelWarn, "worker pass failed",
			slog.String("worker", w.name),
			slog.String("error_type", "process_due_failed"))
	}
}
