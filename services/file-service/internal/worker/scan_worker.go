package worker

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/nicrepository/nchat/services/file-service/internal/service"
)

var errScanConcurrency = errors.New("scan concurrency must be between 1 and 4")

const (
	scanDrainJobs   = 32
	scanDrainBudget = time.Second
	scanDrainYield  = 100 * time.Millisecond
)

// Scan runs one synchronous consumer per slot. No job is prefetched.
type Scan struct {
	processor   PreviewProcessor
	logger      *slog.Logger
	concurrency int
	interval    time.Duration
}

// NewMalwareScan preserves one execution slot until capacity is measured.
func NewMalwareScan(processor PreviewProcessor, logger *slog.Logger) *Scan {
	if logger == nil {
		logger = slog.Default()
	}
	return &Scan{processor: processor, logger: logger, concurrency: 1, interval: scanPollInterval}
}

// SetConcurrency refuses unsafe wiring; config also validates the pool budget.
func (w *Scan) SetConcurrency(slots int) error {
	if slots < 1 || slots > 4 {
		return errScanConcurrency
	}
	w.concurrency = slots
	return nil
}

func (w *Scan) Start(ctx context.Context) {
	if w == nil || w.processor == nil {
		return
	}
	var wg sync.WaitGroup
	for i := 0; i < w.concurrency; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); w.consume(ctx) }()
	}
	wg.Wait()
}

func (w *Scan) consume(ctx context.Context) {
	delay := w.interval
	for ctx.Err() == nil {
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		delay = w.drain(ctx)
	}
}

func (w *Scan) drain(ctx context.Context) time.Duration {
	started := time.Now()
	for n := 0; n < scanDrainJobs && ctx.Err() == nil; n++ {
		count, err := w.processor.ProcessDue(ctx)
		if err != nil {
			w.logFailure(ctx, err)
			return w.interval
		}
		if count == 0 {
			return w.interval
		}
		if time.Since(started) >= scanDrainBudget {
			break
		}
	}
	return scanDrainYield
}

func (w *Scan) logFailure(ctx context.Context, cause error) {
	if ctx.Err() == nil && !errors.Is(cause, service.ErrScanQueueStandby) {
		w.logger.LogAttrs(ctx, slog.LevelWarn, "worker pass failed", slog.String("worker", workerNameMalwareScan), slog.String("error_type", "process_due_failed"))
	}
}
