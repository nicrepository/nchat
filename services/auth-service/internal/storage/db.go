package storage

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Bootstrap retry tuning. The first attempt is immediate; subsequent attempts
// back off exponentially up to dbRetryMaxBackoff. The total budget is enforced
// by the caller's context deadline. No jitter yet (single replica today);
// add jitter before scaling to multiple replicas to avoid thundering herd.
const (
	dbRetryInitialBackoff = 500 * time.Millisecond
	dbRetryMaxBackoff     = 5 * time.Second
)

// ErrDBBootstrapFailed is returned when the database could not be reached
// before the bootstrap context expired. It is a static error so no DSN or
// credential detail can leak into logs or process exit messages.
var ErrDBBootstrapFailed = errors.New("database bootstrap failed: retries exhausted")

// OpenDB creates a connection pool, pings the database, and returns it as Pool.
func OpenDB(ctx context.Context, dsn string, connectTimeoutSeconds int) (Pool, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse db config: %w", err)
	}
	cfg.ConnConfig.ConnectTimeout = time.Duration(connectTimeoutSeconds) * time.Second

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("create pool: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, time.Duration(connectTimeoutSeconds)*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping db: %w", err)
	}
	return pool, nil
}

// OpenDBWithRetry calls OpenDB with capped exponential backoff until it
// succeeds or ctx is done. Log entries carry only attempt number, duration,
// and a sanitized reason — never the underlying error, which may embed DSN
// details.
func OpenDBWithRetry(ctx context.Context, dsn string, connectTimeoutSeconds int, logger *slog.Logger) (Pool, error) {
	connect := func(ctx context.Context) (Pool, error) {
		return OpenDB(ctx, dsn, connectTimeoutSeconds)
	}
	return openDBWithRetry(ctx, connect, logger, sleepContext)
}

// openDBWithRetry is the testable core of OpenDBWithRetry: connect and sleep
// are injectable so tests run without real network access or real sleeps.
func openDBWithRetry(ctx context.Context, connect func(context.Context) (Pool, error), logger *slog.Logger, sleep func(context.Context, time.Duration) error) (Pool, error) {
	backoff := dbRetryInitialBackoff
	for attempt := 1; ; attempt++ {
		start := time.Now()
		pool, err := connect(ctx)
		if err == nil {
			if attempt > 1 {
				logger.Info("database connected after retry", "attempt", attempt)
			}
			return pool, nil
		}
		logger.Warn("database connection attempt failed",
			"attempt", attempt,
			"duration_ms", time.Since(start).Milliseconds(),
			"reason", "open_db_failed",
		)
		if sleep(ctx, backoff) != nil {
			return nil, ErrDBBootstrapFailed
		}
		backoff = min(backoff*2, dbRetryMaxBackoff)
	}
}

// sleepContext waits d or until ctx is done, returning ctx.Err() in the
// latter case. No goroutines are spawned; the timer is always stopped.
func sleepContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
