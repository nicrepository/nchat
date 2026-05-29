package storage

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// Pool is satisfied by *pgxpool.Pool and pgxmock.PgxPoolIface.
type Pool interface {
	Begin(ctx context.Context) (pgx.Tx, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

type queryer interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}
