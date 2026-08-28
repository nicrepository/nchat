package storage

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Pool is the subset of pgxpool.Pool this service uses. Narrowing it keeps the
// stores testable against a mock without pulling a real connection into unit
// tests.
type Pool interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	// Begin opens a transaction. The management store needs it for the
	// mutations whose invariant spans more than one statement — a status
	// transition validated under a row lock, a role revocation that must not
	// leave the platform without an administrator — where SELECT-then-UPDATE
	// across two round trips would let two concurrent requests both pass a
	// check that only one of them may.
	Begin(ctx context.Context) (pgx.Tx, error)
	Ping(ctx context.Context) error
	Close()
}
