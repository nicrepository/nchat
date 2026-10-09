package storage

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/nicrepository/nchat/libs/go/platform/conversationownership"
)

func ownershipSuccessionSession(tx pgx.Tx) conversationownership.Session {
	return conversationownership.Session{
		Query: func(ctx context.Context, sql string, args ...any) (conversationownership.Rows, error) {
			return tx.Query(ctx, sql, args...)
		},
		Exec: func(ctx context.Context, sql string, args ...any) error {
			_, err := tx.Exec(ctx, sql, args...)
			return err
		},
	}
}
