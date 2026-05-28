package storage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/nicrepository/nchat/services/auth-service/internal/domain"
)

// PGXPasswordResetStore implements password-reset persistence using auth.password_reset_tokens.
type PGXPasswordResetStore struct {
	pool Pool
}

func NewPGXPasswordResetStore(pool Pool) *PGXPasswordResetStore {
	return &PGXPasswordResetStore{pool: pool}
}

func (s *PGXPasswordResetStore) GetPolicySettings(ctx context.Context) (domain.PolicySettings, error) {
	var p domain.PolicySettings
	err := s.pool.QueryRow(ctx, `
		SELECT min_password_length, require_uppercase, require_lowercase,
		       require_number, require_symbol, failed_login_limit,
		       failed_login_window_minutes, failed_login_lockout_minutes,
		       session_idle_timeout_minutes, max_devices_per_user,
		       password_reset_token_ttl_minutes, invite_token_ttl_hours
		FROM auth.auth_policy_settings
		WHERE id = 1`,
	).Scan(
		&p.MinPasswordLength, &p.RequireUppercase, &p.RequireLowercase,
		&p.RequireNumber, &p.RequireSymbol, &p.FailedLoginLimit,
		&p.FailedLoginWindowMinutes, &p.FailedLoginLockoutMinutes,
		&p.SessionIdleTimeoutMinutes, &p.MaxDevicesPerUser,
		&p.PasswordResetTokenTTLMinutes, &p.InviteTokenTTLHours,
	)
	if err != nil {
		return domain.PolicySettings{}, fmt.Errorf("get policy settings: %w", err)
	}
	return p, nil
}

func (s *PGXPasswordResetStore) GetActiveUserForPasswordReset(ctx context.Context, email string) (string, bool, error) {
	var userID string
	err := s.pool.QueryRow(ctx, `
		SELECT id
		FROM auth.users
		WHERE email = $1
		  AND status = 'active'
		  AND deleted_at IS NULL
		  AND auth_source = 'manual'`,
		email,
	).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("select password reset user: %w", err)
	}
	return userID, true, nil
}

func (s *PGXPasswordResetStore) CreatePasswordResetToken(ctx context.Context, userID, email, tokenHash string, expiresAt time.Time) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var lockedUserID string
	err = tx.QueryRow(ctx, `
		SELECT id
		FROM auth.users
		WHERE id = $1
		  AND email = $2
		  AND status = 'active'
		  AND deleted_at IS NULL
		  AND auth_source = 'manual'
		FOR UPDATE`,
		userID, email,
	).Scan(&lockedUserID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			if commitErr := tx.Commit(ctx); commitErr != nil {
				return fmt.Errorf("commit tx: %w", commitErr)
			}
			return nil
		}
		return fmt.Errorf("lock password reset user: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		UPDATE auth.password_reset_tokens
		SET used_at = now()
		WHERE user_id = $1
		  AND used_at IS NULL`,
		userID,
	); err != nil {
		return fmt.Errorf("supersede password reset tokens: %w", err)
	}

	var resetTokenID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO auth.password_reset_tokens (user_id, token_hash, expires_at)
		VALUES ($1, $2, $3)
		RETURNING id`,
		userID, tokenHash, expiresAt,
	).Scan(&resetTokenID); err != nil {
		return fmt.Errorf("insert password reset token: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO auth.email_outbox
		  (kind, to_email, subject, template_key, reset_token_id, user_id, payload)
		VALUES ('password_reset', $1, 'Reset your NChat password', 'auth.password_reset', $2, $3, '{}'::jsonb)`,
		email, resetTokenID, userID,
	); err != nil {
		return fmt.Errorf("insert password reset outbox: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}
	return nil
}

func (s *PGXPasswordResetStore) ResetPasswordTx(ctx context.Context, tokenHash, newPasswordHash string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var tokenID string
	var userID string
	var used bool
	var expiresAt time.Time
	err = tx.QueryRow(ctx, `
		SELECT id, user_id, used_at IS NOT NULL, expires_at
		FROM auth.password_reset_tokens
		WHERE token_hash = $1
		FOR UPDATE`,
		tokenHash,
	).Scan(&tokenID, &userID, &used, &expiresAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrInvalidToken
		}
		return fmt.Errorf("select password reset token: %w", err)
	}
	if used || !expiresAt.After(time.Now().UTC()) {
		return domain.ErrInvalidToken
	}

	result, err := tx.Exec(ctx, `
		UPDATE auth.user_password_credentials
		SET password_hash = $1,
		    password_changed_at = now(),
		    updated_at = now()
		WHERE user_id = $2`,
		newPasswordHash, userID,
	)
	if err != nil {
		return fmt.Errorf("update password credential: %w", err)
	}
	if result.RowsAffected() != 1 {
		return domain.ErrInvalidToken
	}

	if _, err := tx.Exec(ctx, `
		UPDATE auth.password_reset_tokens
		SET used_at = now()
		WHERE id = $1
		  AND used_at IS NULL`,
		tokenID,
	); err != nil {
		return fmt.Errorf("mark password reset token used: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		UPDATE auth.user_sessions
		SET revoked_at = now(),
		    revoked_reason = 'password_reset'
		WHERE user_id = $1
		  AND revoked_at IS NULL`,
		userID,
	); err != nil {
		return fmt.Errorf("revoke user sessions after password reset: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		UPDATE auth.refresh_token_history
		SET status = 'revoked',
		    revoked_at = now()
		WHERE session_id IN (
		    SELECT id FROM auth.user_sessions WHERE user_id = $1
		)
		  AND status = 'active'`,
		userID,
	); err != nil {
		return fmt.Errorf("revoke refresh token history after password reset: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}
	return nil
}
