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

// DistributedRateLimitRequest is one charge against a shared counter.
//
// Namespace keeps unrelated controls apart in a table they share: a bootstrap
// guess and an invite request from the same address must not spend each other's
// allowance. Subject is what the limit is per — an already-normalised client IP
// for the limits here — and never the thing being guessed or invited, which
// would hand each attempt its own budget.
//
// Now is supplied rather than read here so a caller can test window rollover
// without waiting for one.
type DistributedRateLimitRequest struct {
	Namespace string
	Subject   string
	Limit     int
	Window    time.Duration
	Now       time.Time
}

// DistributedRateLimitResult reports the decision and, when refused, how long
// the current window still has to run.
type DistributedRateLimitResult struct {
	Allowed    bool
	RetryAfter time.Duration
}

// Allow charges one request against (namespace, subject) and reports whether it
// stayed inside the budget for the current window.
//
// This is the shared counter behind every rate limit in this service that has
// to hold across replicas. It is one statement — an upsert returning the
// post-increment value — because read-then-write lets two replicas both observe
// the last remaining slot, which is exactly the multi-replica hole these limits
// exist to close. Restarts do not reset it either: the state is in the
// database, not in the process.
//
// The window is fixed rather than sliding, truncated to a multiple of its own
// length, so a request writes one row per (key, window) with no per-request row
// to accumulate. The usual fixed-window trade applies — a caller timing
// requests around a boundary can get up to 2×limit across two adjacent windows
// — which is acceptable for a ceiling whose job is to bound abuse rather than
// to meter precisely.
func (s *PGXBootstrapAttemptStore) Allow(ctx context.Context, req DistributedRateLimitRequest) (DistributedRateLimitResult, error) {
	if req.Limit <= 0 || req.Window <= 0 {
		return DistributedRateLimitResult{}, fmt.Errorf("distributed rate limiter misconfigured")
	}
	now := req.Now
	if now.IsZero() {
		now = time.Now()
	}
	now = now.UTC()

	windowStart := now.Truncate(req.Window)
	// An empty namespace composes to the bare subject, not to ":"+subject: the
	// bootstrap limiter namespaces its own key and must keep producing exactly
	// the key it produced before this method existed, or an upgrade would hand
	// every address a fresh allowance.
	key := req.Subject
	if req.Namespace != "" {
		key = req.Namespace + ":" + req.Subject
	}

	var used int
	err := s.pool.QueryRow(ctx, `
		INSERT INTO auth.bootstrap_auth_attempts (limiter_key, window_start, attempts, updated_at)
		VALUES ($1, $2, 1, now())
		ON CONFLICT (limiter_key, window_start) DO UPDATE
		SET attempts   = auth.bootstrap_auth_attempts.attempts + 1,
		    updated_at = now()
		RETURNING attempts`,
		key, windowStart,
	).Scan(&used)
	if err != nil {
		return DistributedRateLimitResult{}, fmt.Errorf("record rate limit attempt: %w", err)
	}
	if used <= req.Limit {
		return DistributedRateLimitResult{Allowed: true}, nil
	}
	return DistributedRateLimitResult{RetryAfter: windowStart.Add(req.Window).Sub(now)}, nil
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
// The caller already namespaces its key, so Namespace is left empty here and
// the composed key is unchanged from before Allow existed.
func (s *PGXBootstrapAttemptStore) RecordAttempt(ctx context.Context, key string, limit int, window time.Duration) (bool, error) {
	result, err := s.Allow(ctx, DistributedRateLimitRequest{
		Subject: key,
		Limit:   limit,
		Window:  window,
	})
	if err != nil {
		return false, err
	}
	return result.Allowed, nil
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
