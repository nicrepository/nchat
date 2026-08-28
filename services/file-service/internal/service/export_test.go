package service

import "time"

// PreviewTuning is the worker's timing and budget configuration, exposed so a
// test can assert the relationship between the constants rather than restate
// their values.
//
// The relationship is what matters: a lease shorter than the work it protects
// hands a row that is still being rendered to a second worker. The constants
// themselves are free to change; the inequality between them is not.
type PreviewTuning struct {
	BatchSize           int
	MaxRenderAttempts   int
	JobTimeout          time.Duration
	CompensationTimeout time.Duration
	LeaseMargin         time.Duration
	Lease               time.Duration
}

// ScanTuning is the antimalware worker's timing configuration, exposed for the
// same reason PreviewTuning is: the relationship between the values is the
// safety property, not the values themselves. A lease that does not outlive a
// scan and its verdict write hands a row that is still being streamed to the
// daemon to a second worker.
//
// Unlike PreviewTuning it is read from a *built service* rather than from the
// package's constants, because the budget is the operator's now: asserting the
// constants would prove nothing about what the running worker actually uses.
type ScanTuning struct {
	BatchSize     int
	JobTimeout    time.Duration
	ResultTimeout time.Duration
	LeaseMargin   time.Duration
	Lease         time.Duration
}

// TuningForTest reports the timing this service instance actually runs with.
func (s *MalwareScanService) TuningForTest() ScanTuning {
	return ScanTuning{
		BatchSize:     scanBatchSize,
		JobTimeout:    s.jobTimeout,
		ResultTimeout: scanResultTimeout,
		LeaseMargin:   scanLeaseMargin,
		Lease:         s.lease,
	}
}

// PreviewTuningForTest reports the constants the preview job runs with.
func PreviewTuningForTest() PreviewTuning {
	return PreviewTuning{
		BatchSize:           previewBatchSize,
		MaxRenderAttempts:   previewMaxRenderAttempts,
		JobTimeout:          previewJobTimeout,
		CompensationTimeout: previewCompensationTimeout,
		LeaseMargin:         previewLeaseMargin,
		Lease:               previewLease,
	}
}
