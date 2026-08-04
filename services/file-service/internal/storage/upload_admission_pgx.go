package storage

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// pgxLockConnPool adapts a pgx pool to LockConnPool.
//
// It is deliberately separate from Pool: every other store borrows a connection
// per statement, while admission control reserves one for the whole upload.
// Keeping that apart makes the connection cost of a slot explicit — see
// config.validateUploadConcurrency, which requires the pool to be large enough
// that admission can never starve ordinary queries.
type pgxLockConnPool struct {
	pool *pgxpool.Pool
}

// LockConnPoolFrom exposes a pool as a source of lock-holding connections.
//
// It reports false for any Pool that is not the pgx implementation. That is not
// a fallback path: admission control is meaningless without a real connection
// to pin a session lock to, so the caller turns a false into a start-up
// failure rather than into an unlimited service.
func LockConnPoolFrom(pool Pool) (LockConnPool, bool) {
	pgxPool, ok := pool.(*pgxpool.Pool)
	if !ok {
		return nil, false
	}
	return &pgxLockConnPool{pool: pgxPool}, true
}

func (p *pgxLockConnPool) Acquire(ctx context.Context) (LockConn, error) {
	conn, err := p.pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire admission connection: %w", err)
	}
	return &pgxLockConn{conn: conn}, nil
}

// pgxLockConn holds session-scoped advisory locks on one reserved connection.
//
// Session-scoped, not transaction-scoped: an upload lasts as long as the client
// takes to send its bytes, and holding a transaction open for that would pin
// the xmin horizon and stall vacuum. Session locks live on the connection and
// are released by PostgreSQL when it closes, which is what makes a crashed
// replica give its slots back without any cleanup job.
type pgxLockConn struct {
	conn *pgxpool.Conn
}

func (c *pgxLockConn) TryLock(ctx context.Context, key int64) (bool, error) {
	var acquired bool
	if err := c.conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, key).Scan(&acquired); err != nil {
		return false, fmt.Errorf("try upload slot: %w", err)
	}
	return acquired, nil
}

// Unlock releases one lock and reports whether the session actually held it.
//
// pg_advisory_unlock returns a boolean, and it is scanned rather than discarded:
// false means this session did not hold that lock, which tells the caller its
// bookkeeping is wrong and the session can no longer be assumed lock-free.
func (c *pgxLockConn) Unlock(ctx context.Context, key int64) (bool, error) {
	if c.conn == nil {
		return false, errors.New("release upload slot: connection already returned")
	}
	var released bool
	if err := c.conn.QueryRow(ctx, `SELECT pg_advisory_unlock($1)`, key).Scan(&released); err != nil {
		return false, fmt.Errorf("release upload slot: %w", err)
	}
	return released, nil
}

// Release returns the connection to the pool.
//
// Valid only once every lock taken on it has been confirmed released: a session
// handed back while still holding an advisory lock keeps that slot out of
// circulation for as long as the pool keeps the connection, with nothing to
// notice or fix it. Discard is the answer whenever that cannot be proven.
func (c *pgxLockConn) Release() {
	if c.conn != nil {
		c.conn.Release()
		c.conn = nil
	}
}

// Discard takes the connection out of the pool and ends the session.
//
// Hijack is what makes this safe rather than a fancy Release: it transfers
// ownership away from the pool *first*, so the connection is already out of
// circulation before anything is attempted on it. Closing afterwards is best
// effort — if it fails, the session is still unreachable from the pool, and
// PostgreSQL drops its advisory locks when the backend eventually goes away.
//
// The nil guard matters: pgxpool panics if a connection is hijacked or released
// twice, and this is exactly the path a double release would take.
func (c *pgxLockConn) Discard(ctx context.Context) error {
	if c.conn == nil {
		return nil
	}
	hijacked := c.conn.Hijack()
	c.conn = nil
	if hijacked == nil {
		return nil
	}
	if err := hijacked.Close(ctx); err != nil {
		return fmt.Errorf("close upload admission connection: %w", err)
	}
	return nil
}
