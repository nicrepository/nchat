package storage

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/nicrepository/nchat/services/auth-service/internal/domain"
)

const pgCodeUniqueViolation = "23505"

// UserStore is the persistence interface for user operations.
type UserStore interface {
	CreateUser(ctx context.Context, input domain.CreateUserInput, passwordHash string) (domain.User, error)
	GetPolicySettings(ctx context.Context) (domain.PolicySettings, error)
	GetUserByID(ctx context.Context, id string) (domain.User, error)
	UpdateUserStatus(ctx context.Context, id, status string) (domain.User, error)
}

// PGXUserStore implements UserStore using a pgx connection pool.
type PGXUserStore struct {
	pool Pool
}

func NewPGXUserStore(pool Pool) *PGXUserStore {
	return &PGXUserStore{pool: pool}
}

func (s *PGXUserStore) GetPolicySettings(ctx context.Context) (domain.PolicySettings, error) {
	var p domain.PolicySettings
	err := s.pool.QueryRow(ctx, `
		SELECT min_password_length, require_uppercase, require_lowercase,
		       require_number, require_symbol, failed_login_limit,
		       failed_login_window_minutes, failed_login_lockout_minutes,
		       session_idle_timeout_minutes, max_devices_per_user,
		       password_reset_token_ttl_minutes, invite_token_ttl_hours
		FROM auth.auth_policy_settings
		WHERE id = 1`).Scan(
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

func (s *PGXUserStore) CreateUser(ctx context.Context, input domain.CreateUserInput, passwordHash string) (domain.User, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.User{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var u domain.User
	err = tx.QueryRow(ctx, `
		INSERT INTO auth.users (email, display_name, full_name, status, auth_source, email_verified_at)
		VALUES ($1, $2, $3, 'active', 'manual', now())
		RETURNING id, email::text, display_name, COALESCE(full_name, ''), status, auth_source,
		          email_verified_at, created_at, updated_at`,
		input.Email, input.DisplayName, nullableString(input.FullName),
	).Scan(
		&u.ID, &u.Email, &u.DisplayName, &u.FullName, &u.Status, &u.AuthSource,
		&u.EmailVerifiedAt, &u.CreatedAt, &u.UpdatedAt,
	)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgCodeUniqueViolation {
			return domain.User{}, domain.ErrDuplicateEmail
		}
		return domain.User{}, fmt.Errorf("insert user: %w", err)
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO auth.user_password_credentials (user_id, password_hash, must_change_password)
		VALUES ($1, $2, $3)`,
		u.ID, passwordHash, input.MustChangePassword,
	)
	if err != nil {
		return domain.User{}, fmt.Errorf("insert password credential: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return domain.User{}, fmt.Errorf("commit tx: %w", err)
	}
	return u, nil
}

func (s *PGXUserStore) GetUserByID(ctx context.Context, id string) (domain.User, error) {
	var u domain.User
	err := s.pool.QueryRow(ctx, `
		SELECT id, email::text, display_name, COALESCE(full_name, ''), status, auth_source,
		       email_verified_at, created_at, updated_at
		FROM auth.users
		WHERE id = $1
		  AND deleted_at IS NULL`,
		id,
	).Scan(&u.ID, &u.Email, &u.DisplayName, &u.FullName, &u.Status, &u.AuthSource,
		&u.EmailVerifiedAt, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.User{}, domain.ErrNotFound
		}
		return domain.User{}, fmt.Errorf("get user by id: %w", err)
	}
	return u, nil
}

func (s *PGXUserStore) UpdateUserStatus(ctx context.Context, id, newStatus string) (domain.User, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.User{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// Lock the user row and read current status to validate the transition atomically.
	var currentStatus string
	err = tx.QueryRow(ctx, `
		SELECT status FROM auth.users
		WHERE id = $1 AND deleted_at IS NULL
		FOR UPDATE`,
		id,
	).Scan(&currentStatus)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.User{}, domain.ErrNotFound
		}
		return domain.User{}, fmt.Errorf("lock user for status update: %w", err)
	}

	if err := domain.ValidateStatusTransition(currentStatus, newStatus); err != nil {
		return domain.User{}, err
	}

	var u domain.User
	err = tx.QueryRow(ctx, `
		UPDATE auth.users
		SET status = $2, updated_at = now()
		WHERE id = $1
		  AND deleted_at IS NULL
		RETURNING id, email::text, display_name, COALESCE(full_name, ''), status, auth_source,
		          email_verified_at, created_at, updated_at`,
		id, newStatus,
	).Scan(&u.ID, &u.Email, &u.DisplayName, &u.FullName, &u.Status, &u.AuthSource,
		&u.EmailVerifiedAt, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		return domain.User{}, fmt.Errorf("update user status: %w", err)
	}

	// Revoke all active sessions and refresh tokens in the same transaction so that
	// a suspension always produces a consistent state (status suspended + no active sessions).
	// Also invalidate pending OIDC exchange codes for the user, because a code created before
	// suspension could otherwise be consumed after reactivation to return pre-suspension tokens.
	if newStatus == "suspended" {
		if _, err := tx.Exec(ctx, `
			WITH revoked AS (
			    UPDATE auth.user_sessions
			    SET revoked_at = now(), revoked_reason = 'admin_suspension'
			    WHERE user_id = $1 AND revoked_at IS NULL
			    RETURNING id
			)
			UPDATE auth.refresh_token_history
			SET status = 'revoked', revoked_at = now()
			WHERE session_id IN (SELECT id FROM revoked)
			  AND status = 'active'`,
			id,
		); err != nil {
			return domain.User{}, fmt.Errorf("revoke sessions on suspension: %w", err)
		}

		// Invalidate any pending OIDC exchange codes for this user.
		// user_json->>'id' stores the user UUID as text; no migration required.
		// Activation intentionally does NOT reset used_at — a code invalidated by
		// suspension cannot be replayed even if the user is later reactivated.
		if _, err := tx.Exec(ctx, `
			UPDATE auth.oidc_exchange_codes
			SET used_at = now()
			WHERE used_at IS NULL
			  AND expires_at > now()
			  AND user_json->>'id' = $1`,
			id,
		); err != nil {
			return domain.User{}, fmt.Errorf("invalidate oidc exchange codes on suspension: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return domain.User{}, fmt.Errorf("commit tx: %w", err)
	}
	return u, nil
}

func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}
