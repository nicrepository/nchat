package storage

import (
	"context"
	"errors"
	"math"
	"time"

	"github.com/nicrepository/nchat/services/file-service/internal/domain"
	"github.com/nicrepository/nchat/services/file-service/internal/service"
)

// Match the claim's partial-index predicate. No ordering is needed: a sort
// could materialize the entire backlog before LIMIT. Never publish partial counts:
// queues larger than 10,000 invalidate the snapshot. LIMIT bounds live rows,
// not physical I/O; the context budget also bounds lock/pool waits and execution.
const scanQueueSnapshotQuery = `
 SELECT count(*),
 count(*) FILTER (WHERE wrapped_dek IS NOT NULL AND kek_key_id IS NOT NULL
  AND (scan_next_attempt_at IS NULL OR scan_next_attempt_at<=now())
  AND (scan_lease_until IS NULL OR scan_lease_until<=now())),
 count(*) FILTER (WHERE scan_lease_until>now()),
 count(*) FILTER (WHERE scan_attempts>0 AND scan_next_attempt_at>now()
  AND (scan_lease_until IS NULL OR scan_lease_until<=now())),
 count(*) FILTER (WHERE uploaded_at < now()-interval '5 seconds'),
 count(*) FILTER (WHERE uploaded_at IS NULL OR uploaded_at>now()),
 greatest(0,coalesce(extract(epoch FROM now()-min(uploaded_at)),0))::double precision,
 statement_timestamp(), (SELECT enabled FROM files.scan_queue_policy WHERE id=1)
 FROM (SELECT uploaded_at,wrapped_dek,kek_key_id,scan_next_attempt_at,scan_lease_until,scan_attempts
 FROM files.attachments WHERE status='pending_scan' AND deleted_at IS NULL
 LIMIT 10001) pending`

func (s *PGXScanStore) ScanQueueSnapshot(ctx context.Context) (service.ScanQueueSnapshot, error) {
	if s == nil || s.pool == nil {
		return service.ScanQueueSnapshot{}, domain.ErrDependenciesUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var snapshot service.ScanQueueSnapshot
	var age float64
	err := s.pool.QueryRow(ctx, scanQueueSnapshotQuery).Scan(&snapshot.Depth, &snapshot.Eligible,
		&snapshot.Leased, &snapshot.Backoff, &snapshot.OverFiveSeconds, &snapshot.UnknownFinalization,
		&age, &snapshot.ObservedAt, &snapshot.PolicyEnabled)
	if err != nil {
		return service.ScanQueueSnapshot{}, err
	}
	if snapshot.Depth > 10000 {
		return service.ScanQueueSnapshot{}, errors.New("scan queue snapshot exceeds row budget")
	}
	if age < 0 || math.IsNaN(age) || age >= float64(math.MaxInt64)/float64(time.Second) {
		return service.ScanQueueSnapshot{}, errors.New("invalid scan queue age")
	}
	snapshot.OldestAge = time.Duration(age * float64(time.Second))
	return snapshot, nil
}
