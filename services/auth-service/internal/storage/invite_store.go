package storage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/nicrepository/nchat/services/auth-service/internal/domain"
)

// PGXInviteStore implements invite persistence using auth.user_invites.
type PGXInviteStore struct {
	pool Pool
}

func NewPGXInviteStore(pool Pool) *PGXInviteStore {
	return &PGXInviteStore{pool: pool}
}

func (s *PGXInviteStore) GetPolicySettings(ctx context.Context) (domain.PolicySettings, error) {
	var p domain.PolicySettings
	err := s.pool.QueryRow(ctx, `
		SELECT min_password_length, require_uppercase, require_lowercase,
		       require_number, require_symbol, failed_login_limit,
		       failed_login_window_minutes, failed_login_lockout_minutes,
		       session_idle_timeout_minutes, max_devices_per_user,
		       password_reset_token_ttl_minutes, invite_token_ttl_hours,
		       COALESCE(password_expiration_days, 0)
		FROM auth.auth_policy_settings
		WHERE id = 1`,
	).Scan(
		&p.MinPasswordLength, &p.RequireUppercase, &p.RequireLowercase,
		&p.RequireNumber, &p.RequireSymbol, &p.FailedLoginLimit,
		&p.FailedLoginWindowMinutes, &p.FailedLoginLockoutMinutes,
		&p.SessionIdleTimeoutMinutes, &p.MaxDevicesPerUser,
		&p.PasswordResetTokenTTLMinutes, &p.InviteTokenTTLHours,
		&p.PasswordExpirationDays,
	)
	if err != nil {
		return domain.PolicySettings{}, fmt.Errorf("get policy settings: %w", err)
	}
	return p, nil
}

func (s *PGXInviteStore) UserExistsByEmail(ctx context.Context, email string) (bool, error) {
	return userExistsByEmailTx(ctx, s.pool, email)
}

func (s *PGXInviteStore) ActiveInviteExistsByEmail(ctx context.Context, email string) (bool, error) {
	return activeInviteExistsByEmailTx(ctx, s.pool, email)
}

func userExistsByEmailTx(ctx context.Context, q queryer, email string) (bool, error) {
	var exists int
	err := q.QueryRow(ctx, `
		SELECT 1 FROM auth.users
		WHERE email = $1
		LIMIT 1`,
		email,
	).Scan(&exists)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("check user exists: %w", err)
	}
	return true, nil
}

func activeInviteExistsByEmailTx(ctx context.Context, q queryer, email string) (bool, error) {
	var exists int
	err := q.QueryRow(ctx, `
		SELECT 1 FROM auth.user_invites
		WHERE email = $1
		  AND status = 'pending'
		  AND accepted_at IS NULL
		  AND revoked_at IS NULL
		  AND expires_at > now()
		LIMIT 1`,
		email,
	).Scan(&exists)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("check active invite exists: %w", err)
	}
	return true, nil
}

func (s *PGXInviteStore) CreateInvite(ctx context.Context, email, displayName, fullName, tokenHash string, expiresAt time.Time, encryptedPayload string) (domain.InviteResult, error) {
	_ = displayName
	_ = fullName
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.InviteResult{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1)::bigint)`, email); err != nil {
		return domain.InviteResult{}, fmt.Errorf("lock invite email: %w", err)
	}

	exists, err := userExistsByEmailTx(ctx, tx, email)
	if err != nil {
		return domain.InviteResult{}, err
	}
	if exists {
		return domain.InviteResult{}, domain.ErrDuplicateEmail
	}

	pending, err := activeInviteExistsByEmailTx(ctx, tx, email)
	if err != nil {
		return domain.InviteResult{}, err
	}
	if pending {
		return domain.InviteResult{}, domain.ErrInviteAlreadyPending
	}

	var result domain.InviteResult
	err = tx.QueryRow(ctx, `
		INSERT INTO auth.user_invites (email, token_hash, status, expires_at)
		VALUES ($1, $2, 'pending', $3)
		RETURNING id, email::text, created_at`,
		email, tokenHash, expiresAt,
	).Scan(&result.ID, &result.Email, &result.CreatedAt)
	if err != nil {
		return domain.InviteResult{}, fmt.Errorf("insert invite: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO auth.email_outbox
		  (kind, to_email, subject, template_key, invite_id, payload)
		VALUES ('invite', $1, 'You have been invited to NChat', 'auth.invite', $2, $3::jsonb)`,
		email, result.ID, encryptedPayload,
	); err != nil {
		return domain.InviteResult{}, fmt.Errorf("insert invite outbox: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return domain.InviteResult{}, fmt.Errorf("commit tx: %w", err)
	}
	return result, nil
}

func (s *PGXInviteStore) AcceptInviteTx(ctx context.Context, tokenHash, displayName, fullName, passwordHash string) (domain.AcceptInviteResult, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.AcceptInviteResult{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var inviteID string
	var email string
	var accepted bool
	var revoked bool
	var expiresAt time.Time
	var status string
	err = tx.QueryRow(ctx, `
		SELECT id, email::text, accepted_at IS NOT NULL, revoked_at IS NOT NULL, expires_at, status
		FROM auth.user_invites
		WHERE token_hash = $1
		FOR UPDATE`,
		tokenHash,
	).Scan(&inviteID, &email, &accepted, &revoked, &expiresAt, &status)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.AcceptInviteResult{}, domain.ErrInvalidToken
		}
		return domain.AcceptInviteResult{}, fmt.Errorf("select invite token: %w", err)
	}
	if accepted || revoked || status != "pending" || !expiresAt.After(time.Now().UTC()) {
		return domain.AcceptInviteResult{}, domain.ErrInvalidToken
	}

	var result domain.AcceptInviteResult
	err = tx.QueryRow(ctx, `
		INSERT INTO auth.users (email, display_name, full_name, status, auth_source, email_verified_at)
		VALUES ($1, $2, $3, 'active', 'manual', now())
		RETURNING id, email::text, display_name, COALESCE(full_name, ''), created_at`,
		email, displayName, nullableString(fullName),
	).Scan(&result.UserID, &result.Email, &result.DisplayName, &result.FullName, &result.CreatedAt)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgCodeUniqueViolation {
			return domain.AcceptInviteResult{}, domain.ErrDuplicateEmail
		}
		return domain.AcceptInviteResult{}, fmt.Errorf("insert invited user: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO auth.user_password_credentials (user_id, password_hash)
		VALUES ($1, $2)`,
		result.UserID, passwordHash,
	); err != nil {
		return domain.AcceptInviteResult{}, fmt.Errorf("insert invited user credential: %w", err)
	}

	update, err := tx.Exec(ctx, `
		UPDATE auth.user_invites
		SET accepted_at = now(),
		    accepted_by_user_id = $2,
		    status = 'accepted',
		    updated_at = now()
		WHERE id = $1
		  AND accepted_at IS NULL
		  AND revoked_at IS NULL
		  AND status = 'pending'`,
		inviteID, result.UserID,
	)
	if err != nil {
		return domain.AcceptInviteResult{}, fmt.Errorf("mark invite accepted: %w", err)
	}
	if update.RowsAffected() != 1 {
		return domain.AcceptInviteResult{}, domain.ErrInvalidToken
	}

	if err := tx.Commit(ctx); err != nil {
		return domain.AcceptInviteResult{}, fmt.Errorf("commit tx: %w", err)
	}
	return result, nil
}
