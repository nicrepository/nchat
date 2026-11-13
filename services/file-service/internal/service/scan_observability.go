package service

import (
	"context"
	"time"
)

// ScanQueueSnapshot is one bounded, read-only PostgreSQL snapshot, global to
// the database. It must never be added across file-service replicas.
type ScanQueueSnapshot struct {
	Depth, Eligible, Leased, Backoff, OverFiveSeconds, UnknownFinalization int64
	OldestAge                                                              time.Duration
	ObservedAt                                                             time.Time
	PolicyEnabled                                                          bool
}

type ScanQueueSnapshotStore interface {
	ScanQueueSnapshot(context.Context) (ScanQueueSnapshot, error)
}

type ScanQueueSnapshotObserver interface {
	ObserveScanQueueSnapshot(ScanQueueSnapshot, error)
}

// ScanPerformanceObserver is optional; observation cannot authorize a verdict.
// scanner_stream includes storage reads, decryption and daemon backpressure.
type ScanPerformanceObserver interface {
	ObserveScanPhase(phase string, elapsed time.Duration)
	ObserveScanCommitted(result string, elapsed time.Duration)
	ObserveScanPublication(result string)
	ObserveScanFailure(reason string)
}

func (s *MalwareScanService) observeScanPhase(phase string, started time.Time) {
	if o, ok := s.observer.(ScanPerformanceObserver); ok {
		o.ObserveScanPhase(phase, time.Since(started))
	}
}

func (s *MalwareScanService) observeScanCommitted(job ScanJob, result string) {
	if job.QueuedAt.IsZero() {
		return
	}
	elapsed := time.Since(job.QueuedAt)
	if elapsed < 0 {
		return
	}
	if o, ok := s.observer.(ScanPerformanceObserver); ok {
		o.ObserveScanCommitted(result, elapsed)
	}
}
