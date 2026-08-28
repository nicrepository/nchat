package service

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/nicrepository/nchat/services/file-service/internal/domain"
)

// Cleanup job tuning, in the same shape as the preview job's: a lease derived
// from the work it protects, and a small batch.
const (
	cleanupBatchSize = 5

	// cleanupJobTimeout bounds one delete against storage.
	cleanupJobTimeout = 15 * time.Second

	// cleanupLease is what a claim holds the job for. Derived from the timeout
	// so it outlives the delete it protects by construction.
	cleanupLeaseMargin = 15 * time.Second
	cleanupLease       = cleanupJobTimeout + cleanupLeaseMargin
)

// Compile-time proof that a claim outlives the delete it covers.
const _ = uint(cleanupLease - cleanupJobTimeout)

// Cleanup outcomes. Closed set, used as metric labels and log results.
//
// They are deliberately *not* the preview worker's vocabulary even where the
// word coincides: a cleanup "retry" is a delete that storage refused, while a
// preview "retry" is a render that will be attempted again. Counting both on
// one series would make each unreadable, which is why they have separate
// observers below.
const (
	cleanupResultRemoved    = "removed"
	cleanupResultReferenced = "referenced"
	cleanupResultRetry      = "retry"
)

// ObjectCleanupObserver counts cleanup outcomes.
//
// Separate from PreviewObserver because the two describe different work. The
// preview counter answers "are previews being produced"; this one answers "is
// storage being reclaimed". A dashboard or alert built on either would be wrong
// if the other's results were mixed into it — a burst of cleanup retries during
// a storage outage would read as previews failing to render.
type ObjectCleanupObserver interface {
	ObserveCleanup(result string)
}

// ObjectCleanupJob is one stored object that must be removed.
type ObjectCleanupJob struct {
	ID        string
	ObjectKey string
	// Attempts is this claim's fencing token, exactly as it is for a preview
	// claim: completing the job requires it, so a worker whose lease expired
	// cannot delete the entry a newer attempt is working on.
	Attempts int
}

// ObjectCleanupStore is the durable queue behind the cleanup worker.
type ObjectCleanupStore interface {
	// Enqueue records an object that must be removed. It is idempotent.
	Enqueue(ctx context.Context, objectKey string) error
	ClaimDueCleanups(ctx context.Context, batchSize int, lease time.Duration) ([]ObjectCleanupJob, error)
	Complete(ctx context.Context, jobID string, claimAttempt int) (bool, error)
	// IsObjectReferenced reports whether a live preview points at the key.
	IsObjectReferenced(ctx context.Context, objectKey string) (bool, error)
}

// ObjectCleanupService removes stored objects that nothing references (SR-002).
//
// It is the durable half of the compensation the preview job starts: when a
// preview cannot be published, its object has to go, and when that delete fails
// the key must survive the failure. Everything here is about that survival —
// the queue is in PostgreSQL, so a restart, a storage outage and a crashed
// replica all leave the work exactly where it was.
type ObjectCleanupService struct {
	store    ObjectCleanupStore
	objects  ObjectStore
	observer ObjectCleanupObserver
	logger   *slog.Logger
}

func NewObjectCleanupService(
	store ObjectCleanupStore,
	objects ObjectStore,
	observer ObjectCleanupObserver,
	logger *slog.Logger,
) *ObjectCleanupService {
	if logger == nil {
		logger = slog.Default()
	}
	return &ObjectCleanupService{store: store, objects: objects, observer: observer, logger: logger}
}

// Ready reports whether the worker has what it needs.
func (s *ObjectCleanupService) Ready() bool {
	return s != nil && s.store != nil && s.objects != nil
}

// ProcessDue claims and processes one batch of outstanding cleanups.
func (s *ObjectCleanupService) ProcessDue(ctx context.Context) (int, error) {
	if !s.Ready() {
		return 0, domain.ErrDependenciesUnavailable
	}
	expired := 0
	if reaper, ok := s.store.(interface {
		ExpireDuePreviews(context.Context, int) (int, error)
	}); ok {
		var err error
		expired, err = reaper.ExpireDuePreviews(ctx, 50)
		if err != nil {
			return 0, fmt.Errorf("expire due previews: %w", err)
		}
	}
	jobs, err := s.store.ClaimDueCleanups(ctx, cleanupBatchSize, cleanupLease)
	if err != nil {
		return 0, fmt.Errorf("claim due cleanups: %w", err)
	}
	processed := expired
	for _, job := range jobs {
		if ctx.Err() != nil {
			// The lease still holds, so untouched jobs simply become due again.
			return processed, ctx.Err()
		}
		s.process(ctx, job)
		processed++
	}
	return processed, nil
}

// process removes one object, or leaves the job for another attempt.
//
// There is no attempt ceiling and no terminal failure state, deliberately. A
// job exists because an object exists; giving up would not remove the object,
// it would only stop anyone from knowing about it — which is the failure this
// queue was built to end. A job that cannot be completed keeps costing one
// delete per lease until storage accepts it.
func (s *ObjectCleanupService) process(ctx context.Context, job ObjectCleanupJob) {
	jobCtx, cancel := context.WithTimeout(ctx, cleanupJobTimeout)
	defer cancel()

	started := time.Now()

	// A key that a live preview points at must never be deleted. That can only
	// happen to a stale job — the key belongs to one preview object, and a
	// published one is not garbage — so the check is cheap insurance against
	// this worker becoming the thing that breaks a working preview.
	referenced, err := s.store.IsObjectReferenced(jobCtx, job.ObjectKey)
	if err != nil {
		s.observe(cleanupResultRetry)
		s.log(ctx, slog.LevelWarn, job, cleanupResultRetry, started)
		return
	}
	if referenced {
		// Nothing to remove: the object has an owner. The job is finished
		// because it is wrong, not because it succeeded.
		if _, err := s.store.Complete(jobCtx, job.ID, job.Attempts); err != nil {
			s.observe(cleanupResultRetry)
			s.log(ctx, slog.LevelWarn, job, cleanupResultRetry, started)
			return
		}
		s.observe(cleanupResultReferenced)
		s.log(ctx, slog.LevelWarn, job, cleanupResultReferenced, started)
		return
	}

	// Delete is idempotent in the storage client: an object that is already
	// gone reports success, which is exactly right for a retry that follows a
	// delete that actually worked but whose acknowledgement was lost.
	if err := s.objects.Delete(jobCtx, job.ObjectKey); err != nil {
		s.observe(cleanupResultRetry)
		s.log(ctx, slog.LevelWarn, job, cleanupResultRetry, started)
		return
	}
	// Only now, with the object provably gone, is the job forgotten.
	if _, err := s.store.Complete(jobCtx, job.ID, job.Attempts); err != nil {
		// The object is gone but the row remains: the next attempt finds the
		// object already absent, deletes nothing, and completes. Duplicated
		// work, never a lost object.
		s.observe(cleanupResultRetry)
		s.log(ctx, slog.LevelWarn, job, cleanupResultRetry, started)
		return
	}
	s.observe(cleanupResultRemoved)
	s.log(ctx, slog.LevelInfo, job, cleanupResultRemoved, started)
}

func (s *ObjectCleanupService) observe(result string) {
	if s.observer != nil {
		s.observer.ObserveCleanup(result)
	}
}

// log records the operational facts only. The object key is a server-generated
// path with no user input in it, and it is still not logged: the job id is
// enough to correlate, and a key names a stored object.
func (s *ObjectCleanupService) log(
	ctx context.Context, level slog.Level, job ObjectCleanupJob, result string, started time.Time,
) {
	s.logger.LogAttrs(ctx, level, "object cleanup completed",
		slog.String("job_id", job.ID),
		slog.String("result", result),
		slog.Int("attempt", job.Attempts),
		slog.Int64("duration_ms", time.Since(started).Milliseconds()),
	)
}
