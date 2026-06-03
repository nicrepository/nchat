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

func (s *PGXSessionStore) RotateRefreshToken(ctx context.Context, oldHash string, newHash string) (domain.Session, error) {
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
		JOIN auth.refresh_token_history AS h
		  ON h.session_id = s.id
		 AND h.refresh_token_hash = s.refresh_token_hash
		 AND h.status = 'active'
		WHERE s.refresh_token_hash = $1
		  AND h.refresh_token_hash = $1
		  AND s.revoked_at IS NULL
		  AND s.idle_expires_at > now()
		  AND (s.absolute_expires_at IS NULL OR s.absolute_expires_at > now())
		  AND u.status = 'active'
		  AND u.deleted_at IS NULL
		FOR UPDATE OF s, h`,
		oldHash,
	).Scan(&session.ID, &session.UserID)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			return domain.Session{}, fmt.Errorf("select refresh session: %w", err)
		}
		revoked, revokeErr := revokeSessionForReusedToken(ctx, tx, oldHash)
		if revokeErr != nil {
			return domain.Session{}, revokeErr
		}
		if revoked {
			if err := tx.Commit(ctx); err != nil {
				return domain.Session{}, fmt.Errorf("commit tx: %w", err)
			}
		}
		return domain.Session{}, domain.ErrInvalidRefreshToken
	}

	var idleTimeoutMinutes int
	if err := tx.QueryRow(ctx, `
		SELECT session_idle_timeout_minutes
		FROM auth.auth_policy_settings
		WHERE id = 1`,
	).Scan(&idleTimeoutMinutes); err != nil {
		return domain.Session{}, fmt.Errorf("get session idle timeout policy: %w", err)
	}
	idleExpiresAt := time.Now().UTC().Add(time.Duration(idleTimeoutMinutes) * time.Minute)

	result, err := tx.Exec(ctx, `
		UPDATE auth.refresh_token_history
		SET status = 'rotated',
		    rotated_at = now()
		WHERE session_id = $1
		  AND refresh_token_hash = $2
		  AND status = 'active'`,
		session.ID, oldHash,
	)
	if err != nil {
		return domain.Session{}, fmt.Errorf("mark refresh token rotated: %w", err)
	}
	if result.RowsAffected() != 1 {
		return domain.Session{}, domain.ErrInvalidRefreshToken
	}

	result, err = tx.Exec(ctx, `
		UPDATE auth.user_sessions
		SET refresh_token_hash = $1,
		    idle_expires_at = $2,
		    last_seen_at = now()
		WHERE id = $3
		  AND refresh_token_hash = $4
		  AND revoked_at IS NULL`,
		newHash, idleExpiresAt, session.ID, oldHash,
	)
	if err != nil {
		return domain.Session{}, fmt.Errorf("rotate refresh token: %w", err)
	}
	if result.RowsAffected() != 1 {
		return domain.Session{}, domain.ErrInvalidRefreshToken
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO auth.refresh_token_history (session_id, refresh_token_hash, status)
		VALUES ($1, $2, 'active')`,
		session.ID, newHash,
	); err != nil {
		return domain.Session{}, fmt.Errorf("insert refresh token history: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return domain.Session{}, fmt.Errorf("commit tx: %w", err)
	}
	return session, nil
}

func revokeSessionForReusedToken(ctx context.Context, tx pgx.Tx, hash string) (bool, error) {
	var sessionID string
	err := tx.QueryRow(ctx, `
		SELECT h.session_id
		FROM auth.refresh_token_history AS h
		WHERE h.refresh_token_hash = $1
		  AND h.status = 'rotated'
		FOR UPDATE OF h`,
		hash,
	).Scan(&sessionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("select reused refresh token: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		UPDATE auth.refresh_token_history
		SET status = 'reused',
		    reused_at = now()
		WHERE refresh_token_hash = $1
		  AND status = 'rotated'`,
		hash,
	); err != nil {
		return false, fmt.Errorf("mark refresh token reused: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		UPDATE auth.user_sessions
		SET revoked_at = now(),
		    revoked_reason = 'refresh_token_reuse_detected'
		WHERE id = $1
		  AND revoked_at IS NULL`,
		sessionID,
	); err != nil {
		return false, fmt.Errorf("revoke reused refresh session: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		UPDATE auth.refresh_token_history
		SET status = 'revoked',
		    revoked_at = now()
		WHERE session_id = $1
		  AND status = 'active'`,
		sessionID,
	); err != nil {
		return false, fmt.Errorf("revoke active refresh token history: %w", err)
	}
	return true, nil
}

func (s *PGXSessionStore) RevokeRefreshToken(ctx context.Context, hash string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var sessionID string
	err = tx.QueryRow(ctx, `
		SELECT s.id
		FROM auth.user_sessions AS s
		JOIN auth.refresh_token_history AS h
		  ON h.session_id = s.id
		 AND h.refresh_token_hash = s.refresh_token_hash
		 AND h.status = 'active'
		WHERE s.refresh_token_hash = $1
		  AND h.refresh_token_hash = $1
		  AND s.revoked_at IS NULL
		FOR UPDATE OF s, h`,
		hash,
	).Scan(&sessionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrInvalidRefreshToken
		}
		return fmt.Errorf("select refresh session for logout: %w", err)
	}

	result, err := tx.Exec(ctx, `
		UPDATE auth.user_sessions
		SET revoked_at = now(),
		    revoked_reason = 'logout'
		WHERE id = $1
		  AND revoked_at IS NULL`,
		sessionID,
	)
	if err != nil {
		return fmt.Errorf("revoke refresh token: %w", err)
	}
	if result.RowsAffected() != 1 {
		return domain.ErrInvalidRefreshToken
	}

	if _, err := tx.Exec(ctx, `
		UPDATE auth.refresh_token_history
		SET status = 'revoked',
		    revoked_at = now()
		WHERE session_id = $1
		  AND status = 'active'`,
		sessionID,
	); err != nil {
		return fmt.Errorf("revoke refresh token history: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}
	return nil
}
