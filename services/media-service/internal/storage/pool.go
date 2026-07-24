package storage

import (
	"context"

	"github.com/jackc/pgx/v5"
)

type Pool interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Ping(ctx context.Context) error
	Close()
}
