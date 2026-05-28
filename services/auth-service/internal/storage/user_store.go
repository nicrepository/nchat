package storage

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/nicrepository/nchat/services/auth-service/internal/domain"
)

const pgCodeUniqueViolation = "23505"

// UserStore is the persistence interface for user operations.
type UserStore interface {
	CreateUser(ctx context.Context, input domain.CreateUserInput, passwordHash string) (domain.User, error)
	GetPolicySettings(ctx context.Context) (domain.PolicySettings, error)
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
		       session_idle_timeout_minutes, max_devices_per_user
		FROM auth.auth_policy_settings
		WHERE id = 1`).Scan(
		&p.MinPasswordLength, &p.RequireUppercase, &p.RequireLowercase,
		&p.RequireNumber, &p.RequireSymbol, &p.FailedLoginLimit,
		&p.FailedLoginWindowMinutes, &p.FailedLoginLockoutMinutes,
		&p.SessionIdleTimeoutMinutes, &p.MaxDevicesPerUser,
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

func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}
