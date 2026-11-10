package worker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/nicrepository/nchat/services/file-service/internal/service"
	"io"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type scanProcessorFunc func(context.Context) (int, error)

func (f scanProcessorFunc) ProcessDue(ctx context.Context) (int, error) { return f(ctx) }
func scanTestWorker(p PreviewProcessor) *Scan {
	w := NewMalwareScan(p, slog.New(slog.NewTextHandler(io.Discard, nil)))
	w.interval = time.Millisecond
	return w
}

func TestScanDrainBoundsAndIdle(t *testing.T) {
	var calls int
	w := scanTestWorker(scanProcessorFunc(func(context.Context) (int, error) { calls++; return 1, nil }))
	if delay := w.drain(context.Background()); delay != scanDrainYield || calls != scanDrainJobs {
		t.Fatalf("delay=%s calls=%d", delay, calls)
	}
	for _, testErr := range []error{nil, errors.New("database unavailable")} {
		calls = 0
		w.processor = scanProcessorFunc(func(context.Context) (int, error) { calls++; return 0, testErr })
		if delay := w.drain(context.Background()); delay != w.interval || calls != 1 {
			t.Fatalf("idle/error loop: delay=%s calls=%d", delay, calls)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls = 0
	w.drain(ctx)
	if calls != 0 {
		t.Fatal("claimed after cancellation")
	}
}

func TestScanPoolAdmitsOnlyAvailableSlotsAndJoinsShutdown(t *testing.T) {
	entered := make(chan struct{}, 4)
	var active, maxActive, calls atomic.Int32
	p := scanProcessorFunc(func(ctx context.Context) (int, error) {
		n := active.Add(1)
		defer active.Add(-1)
		calls.Add(1)
		for old := maxActive.Load(); n > old; old = maxActive.Load() {
			if maxActive.CompareAndSwap(old, n) {
				break
			}
		}
		entered <- struct{}{}
		<-ctx.Done()
		return 0, ctx.Err()
	})
	w := scanTestWorker(p)
	if err := w.SetConcurrency(2); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { w.Start(ctx); close(done) }()
	for i := 0; i < 2; i++ {
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("pool did not fill")
		}
	}
	if maxActive.Load() != 2 || calls.Load() != 2 {
		t.Fatal("slot bound violated")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("orphaned worker")
	}
	if active.Load() != 0 || calls.Load() != 2 {
		t.Fatal("shutdown left active or new claims")
	}
}

func TestScanDrainRunsConsecutiveJobsWithoutPollDelay(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	w := scanTestWorker(scanProcessorFunc(func(context.Context) (int, error) {
		calls++
		if calls == 8 {
			cancel()
		}
		return 1, nil
	}))
	w.interval = time.Hour
	w.drain(ctx)
	if calls != 8 {
		t.Fatalf("fast jobs processed=%d", calls)
	}
}

func TestScanWorkerValidationAndEmptyWiring(t *testing.T) {
	var empty *Scan
	empty.Start(context.Background())
	NewMalwareScan(nil, nil).Start(context.Background())
	w := NewMalwareScan(nil, nil)
	for _, slots := range []int{0, -1, 5} {
		if w.SetConcurrency(slots) == nil {
			t.Fatalf("slots=%d accepted", slots)
		}
	}
	if w.SetConcurrency(4) != nil {
		t.Fatal("valid bound refused")
	}
}

// Benchmark isolates scheduler overhead using identical synthetic processor and
// virtual 10s polling. It does not measure ClamAV, storage, PG or real throughput.
func BenchmarkScanScheduler(b *testing.B) {
	for _, mode := range []string{"baseline", "drain"} {
		b.Run(mode, func(b *testing.B) {
			calls := 0
			w := scanTestWorker(scanProcessorFunc(func(context.Context) (int, error) { calls++; return 1, nil }))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if mode == "baseline" {
					_, _ = w.processor.ProcessDue(context.Background())
				} else {
					w.drain(context.Background())
				}
			}
			b.ReportMetric(float64(calls)/float64(b.N), "jobs/round")
		})
	}
}

func TestScanFailureLogNamesWorkerWithoutDriverText(t *testing.T) {
	var output bytes.Buffer
	w := NewMalwareScan(scanProcessorFunc(func(context.Context) (int, error) { return 0, errors.New("private-database-address") }), slog.New(slog.NewTextHandler(&output, nil)))
	w.drain(context.Background())
	if !strings.Contains(output.String(), "malware_scan") || strings.Contains(output.String(), "private-database-address") {
		t.Fatal("scan log lost worker identity or leaked driver text")
	}
}

func TestStandbyIsQuietButPolicyAndConnectionFailuresWarn(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		warn bool
	}{
		{"preparation", fmt.Errorf("claim: %w", service.ErrScanQueueStandby), false},
		{"unexpected policy", service.ErrScanFencingInactive, true},
		{"connection", errors.New("database connection refused"), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			w := NewMalwareScan(scanProcessorFunc(func(context.Context) (int, error) { return 0, tc.err }), slog.New(slog.NewTextHandler(&logs, nil)))
			for i := 0; i < 3; i++ {
				if delay := w.drain(context.Background()); delay != w.interval {
					t.Fatal("standby or error changed polling budget")
				}
			}
			if strings.Contains(logs.String(), "level=WARN") != tc.warn {
				t.Fatalf("unexpected logging: %s", logs.String())
			}
		})
	}
}

func TestStandbyPollingResumesWithoutRestart(t *testing.T) {
	ready := false
	calls := 0
	w := scanTestWorker(scanProcessorFunc(func(context.Context) (int, error) {
		calls++
		if !ready {
			return 0, service.ErrScanQueueStandby
		}
		if calls == 2 {
			return 1, nil
		}
		return 0, nil
	}))
	if delay := w.drain(context.Background()); delay != w.interval {
		t.Fatal("standby stopped recovery polling")
	}
	ready = true
	if delay := w.drain(context.Background()); delay != w.interval || calls != 3 {
		t.Fatalf("same consumer did not resume: delay=%s calls=%d", delay, calls)
	}
}
