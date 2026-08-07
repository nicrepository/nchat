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
