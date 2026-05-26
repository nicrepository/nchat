package storage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/nicrepository/nchat/services/auth-service/internal/domain"
)

// PGXSessionStore implements refresh-token persistence using auth.user_sessions.
type PGXSessionStore struct {
	pool Pool
}

func NewPGXSessionStore(pool Pool) *PGXSessionStore {
	return &PGXSessionStore{pool: pool}
}

func (s *PGXSessionStore) RotateRefreshToken(ctx context.Context, oldHash string, newHash string, expiresAt time.Time) (domain.Session, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.Session{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var session domain.Session
	err = tx.QueryRow(ctx, `
		SELECT s.id, s.user_id
		FROM auth.user_sessions AS s
		JOIN auth.users AS u ON u.id = s.user_id
		WHERE s.refresh_token_hash = $1
		  AND s.revoked_at IS NULL
		  AND s.idle_expires_at > now()
		  AND (s.absolute_expires_at IS NULL OR s.absolute_expires_at > now())
		  AND u.status = 'active'
		  AND u.deleted_at IS NULL
		FOR UPDATE OF s`,
		oldHash,
	).Scan(&session.ID, &session.UserID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Session{}, domain.ErrInvalidRefreshToken
		}
		return domain.Session{}, fmt.Errorf("select refresh session: %w", err)
	}

	result, err := tx.Exec(ctx, `
		UPDATE auth.user_sessions
		SET refresh_token_hash = $1,
		    idle_expires_at = $2,
		    last_seen_at = now()
		WHERE id = $3
		  AND refresh_token_hash = $4
		  AND revoked_at IS NULL`,
		newHash, expiresAt, session.ID, oldHash,
	)
	if err != nil {
		return domain.Session{}, fmt.Errorf("rotate refresh token: %w", err)
	}
	if result.RowsAffected() != 1 {
		return domain.Session{}, domain.ErrInvalidRefreshToken
	}

	if err := tx.Commit(ctx); err != nil {
		return domain.Session{}, fmt.Errorf("commit tx: %w", err)
	}
	return session, nil
}

func (s *PGXSessionStore) RevokeRefreshToken(ctx context.Context, hash string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	result, err := tx.Exec(ctx, `
		UPDATE auth.user_sessions
		SET revoked_at = now(),
		    revoked_reason = 'logout'
		WHERE refresh_token_hash = $1
		  AND revoked_at IS NULL`,
		hash,
	)
	if err != nil {
		return fmt.Errorf("revoke refresh token: %w", err)
	}
	if result.RowsAffected() != 1 {
		return domain.ErrInvalidRefreshToken
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}
	return nil
}
