package storage

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Pool is satisfied by *pgxpool.Pool and pgxmock.PgxPoolIface.
type Pool interface {
	Begin(ctx context.Context) (pgx.Tx, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

type queryer interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// txQueryer is the subset of pgx.Tx used by the helpers that make up a
// multi-statement transaction. Taking it as a parameter is deliberate: it
// forces every such helper to be handed the transaction it runs in, so none can
// quietly open its own or reach for the pool. Committing and rolling back stay
// with the function that began the transaction.
type txQueryer interface {
	queryer
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}
