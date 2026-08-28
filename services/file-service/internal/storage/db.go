// Package storage contains file-service persistence and object-storage adapters.
package storage

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// OpenDB builds a connection pool and verifies it before the service reports
// ready. The DSN comes from configuration only.
func OpenDB(ctx context.Context, dsn string, connectTimeoutSeconds int) (Pool, error) {
	return OpenDBWithMaxConns(ctx, dsn, connectTimeoutSeconds, 0)
}

// OpenDBWithMaxConns is OpenDB with an explicit pool size.
//
// The size matters because upload admission reserves one connection per
// in-flight transfer: a pool sized at or below the number of upload slots would
// let a fully booked service starve its own authorization and metadata queries.
// A non-positive maxConns leaves the driver default in place.
func OpenDBWithMaxConns(
	ctx context.Context, dsn string, connectTimeoutSeconds, maxConns int,
) (Pool, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse db config: %w", err)
	}
	cfg.ConnConfig.ConnectTimeout = time.Duration(connectTimeoutSeconds) * time.Second
	if maxConns > 0 {
		cfg.MaxConns = int32(maxConns) //nolint:gosec // Bounded by config validation.
	}
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
