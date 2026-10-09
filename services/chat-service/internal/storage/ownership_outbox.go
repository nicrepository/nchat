package storage

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

type ownershipDelivery struct {
	id                            int64
	workspace, kind, conversation string
	attempts                      int
}

// DispatchOwnershipChanges reads committed mutations. Row locks remain held
// during bounded publishing; a crash before commit permits a safe refetch replay.
// Observers report actual publish attempts, even if their database commit fails.
func (s *PGXOwnershipStore) DispatchOwnershipChanges(ctx context.Context, publish func(context.Context, string, string, string) error, observers ...func(bool, error)) error {
	var failures error
	for range 50 {
		found, publishErr, err := s.dispatchOwnershipChange(ctx, publish, observers)
		failures = errors.Join(failures, publishErr)
		if err != nil || !found {
			return errors.Join(failures, err)
		}
	}
	return failures
}

func (s *PGXOwnershipStore) dispatchOwnershipChange(ctx context.Context, publish func(context.Context, string, string, string) error, observers []func(bool, error)) (bool, error, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var e ownershipDelivery
	err = tx.QueryRow(ctx, `SELECT id,workspace_id::text,conversation_kind,conversation_id::text,attempt_count
 FROM chat.ownership_outbox WHERE published_at IS NULL AND next_attempt_at <= now()
 ORDER BY next_attempt_at,id LIMIT 1 FOR UPDATE SKIP LOCKED`).Scan(&e.id, &e.workspace, &e.kind, &e.conversation, &e.attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil, nil
	}
	if err != nil {
		return false, nil, err
	}
	publishCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	publishErr := publish(publishCtx, e.workspace, e.kind, e.conversation)
	cancel()
	for _, observe := range observers {
		observe(e.attempts > 0, publishErr)
	}
	if err := recordOwnershipDelivery(ctx, tx, e, publishErr); err != nil {
		return true, publishErr, err
	}
	return true, publishErr, tx.Commit(ctx)
}

func recordOwnershipDelivery(ctx context.Context, tx pgx.Tx, e ownershipDelivery, publishErr error) error {
	if publishErr == nil {
		_, err := tx.Exec(ctx, `UPDATE chat.ownership_outbox SET published_at=clock_timestamp(),
 attempt_count=attempt_count+1,last_attempt_at=clock_timestamp(),last_failure=NULL WHERE id=$1`, e.id)
		return err
	}
	failure := "publish_failed"
	if errors.Is(publishErr, context.DeadlineExceeded) {
		failure = "publish_timeout"
	}
	// Bound the exponent as well as the delay for indefinitely retryable rows.
	delay := min(1<<min(e.attempts, 6), 60)
	_, err := tx.Exec(ctx, `UPDATE chat.ownership_outbox SET attempt_count=attempt_count+1,
 last_attempt_at=clock_timestamp(),next_attempt_at=clock_timestamp()+make_interval(secs => $2),last_failure=$3 WHERE id=$1`, e.id, delay, failure)
	return err
}

// OwnershipOutboxBacklog includes delayed retries. Each replica sees the shared
// database backlog, so these gauges must not be summed across replicas.
func (s *PGXOwnershipStore) OwnershipOutboxBacklog(ctx context.Context) (pending int64, oldestAge float64, err error) {
	err = s.pool.QueryRow(ctx, `SELECT count(*),
 COALESCE(GREATEST(EXTRACT(EPOCH FROM (now()-min(created_at))),0),0)::double precision
 FROM chat.ownership_outbox WHERE published_at IS NULL`).Scan(&pending, &oldestAge)
	return
}
