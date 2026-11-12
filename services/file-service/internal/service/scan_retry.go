package service

import (
	"context"
	"errors"
	"hash/fnv"
	"time"
)

var ErrScanFencingInactive = errors.New("scan fencing inactive or legacy writer blocked")
var ErrScanQueueStandby = errors.New("scan queue awaits approved cutover")

// ScanRetryStore releases only the current attempt; lost writes recover by lease.
type ScanRetryStore interface {
	RetryScan(context.Context, ScanJob, time.Duration, string) error
}

// ScanTimingObserver is optional to keep existing observers compatible.
type ScanTimingObserver interface {
	ScanStarted(wait time.Duration)
	ScanFinished(elapsed time.Duration)
	ObserveScanBackoff(delay time.Duration, reason string)
}

// RetryDelay uses 10s exponential steps capped at the former 8*330s ceiling.
// Token-derived equal jitter spreads replicas and is reproducible in tests.
func RetryDelay(attempt int, token string) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	if attempt > 10 {
		attempt = 10
	}
	ceiling := 20 * time.Second * time.Duration(1<<uint(attempt-1))
	if ceiling > 2640*time.Second {
		ceiling = 2640 * time.Second
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(token))
	half := ceiling / 2
	return half + time.Duration(h.Sum32())%(half/time.Millisecond+1)*time.Millisecond
}

func (s *MalwareScanService) scheduleRetry(ctx context.Context, job ScanJob, kind string) {
	// An outage also slows fresh claims, not just retries, across all local slots.
	s.cooldownUntil.Store(time.Now().Add(10 * time.Second).UnixNano())
	q, ok := s.queue.(ScanRetryStore)
	if !ok {
		return
	}
	delay := RetryDelay(job.Attempts, job.ClaimToken)
	writeCtx, cancel := context.WithTimeout(ctx, scanResultTimeout)
	defer cancel()
	if err := q.RetryScan(writeCtx, job, delay, kind); err != nil {
		if errors.Is(err, ErrScanSuperseded) {
			s.observeScan(scanResultSuperseded)
		} else {
			s.observeScan("retry_write_failed")
		}
		return
	}
	if o, ok := s.observer.(ScanTimingObserver); ok {
		o.ObserveScanBackoff(delay, kind)
	}
}

type ScanQueueStatistics interface {
	ScanQueueState(context.Context) (int64, int64, int64, error)
}
type ScanQueueStateObserver interface{ SetScanQueueState(eligible, leased int64) }
