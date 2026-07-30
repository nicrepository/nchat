package storage

import (
	"context"
	"fmt"
	"time"
)

// PGXBootstrapAttemptStore counts bootstrap-credential attempts in PostgreSQL,
// so the budget is shared by every replica.
//
// The in-process limiters elsewhere in this service are complementary ceilings
// on high-volume endpoints, where a per-pod approximation is an acceptable
// trade for not touching the database on every request. This one guards a
// pre-shared secret that can mint an invite conferring workspace ownership: an
// attacker who gets N times the budget because the deployment runs N replicas,
// and a fresh budget on every restart, is the whole failure being fixed. The
// volume is failed guesses against one endpoint, so the cost of one upsert per
// attempt is not worth optimising away.
type PGXBootstrapAttemptStore struct {
	pool Pool
}

func NewPGXBootstrapAttemptStore(pool Pool) *PGXBootstrapAttemptStore {
	return &PGXBootstrapAttemptStore{pool: pool}
}

// RecordAttempt charges one attempt against key and reports whether it stayed
// inside limit for the current window.
//
// The count and the increment are one statement: an upsert that returns the
// post-increment value. Read-then-write would let two replicas both observe the
// last remaining attempt, which on this endpoint means two extra guesses per
// concurrent request.
//
// The window is fixed rather than sliding, truncated to a multiple of the
// window length, so an attempt writes one row per (key, window) and there is no
// per-attempt row to accumulate. The trade is the usual fixed-window one: an
// attacker timing guesses around a boundary can get up to 2×limit across two
// adjacent windows. For a bound whose purpose is to turn an unlimited online
// guessing attack into a rate-limited one, that constant does not matter.
//
// Every attempt is charged, successful or not. A correct credential does not
// reset or refund the budget: if it is being replayed by somebody who stole it,
// the limit is exactly what should still apply.
func (s *PGXBootstrapAttemptStore) RecordAttempt(ctx context.Context, key string, limit int, window time.Duration) (bool, error) {
	if limit <= 0 || window <= 0 {
		return false, fmt.Errorf("bootstrap attempt limiter misconfigured")
	}

	windowStart := time.Now().UTC().Truncate(window)
	var attempts int
	err := s.pool.QueryRow(ctx, `
		INSERT INTO auth.bootstrap_auth_attempts (limiter_key, window_start, attempts, updated_at)
		VALUES ($1, $2, 1, now())
		ON CONFLICT (limiter_key, window_start) DO UPDATE
		SET attempts   = auth.bootstrap_auth_attempts.attempts + 1,
		    updated_at = now()
		RETURNING attempts`,
		key, windowStart,
	).Scan(&attempts)
	if err != nil {
		return false, fmt.Errorf("record bootstrap attempt: %w", err)
	}
	return attempts <= limit, nil
}

// SweepExpired discards windows that have already ended. Nothing depends on it
// running: an expired row is never read, because RecordAttempt only ever
// touches the current window. It exists so the table does not keep one row per
// (IP, window) indefinitely.
func (s *PGXBootstrapAttemptStore) SweepExpired(ctx context.Context, window time.Duration) error {
	if window <= 0 {
		return nil
	}
	cutoff := time.Now().UTC().Add(-2 * window)
	if _, err := s.pool.Exec(ctx,
		`DELETE FROM auth.bootstrap_auth_attempts WHERE window_start < $1`, cutoff,
	); err != nil {
		return fmt.Errorf("sweep bootstrap attempts: %w", err)
	}
	return nil
}
