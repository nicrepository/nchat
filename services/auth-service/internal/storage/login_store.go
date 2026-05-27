package storage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/nicrepository/nchat/services/auth-service/internal/domain"
)

// PasswordVerifier verifies a plaintext password against an Argon2id PHC hash.
// Returns (true, nil) on match, (false, nil) on mismatch, or (false, err) on malformed hash.
type PasswordVerifier func(password, hash string) (bool, error)

// DummyVerifier runs a constant-time dummy Argon2id operation to prevent user enumeration.
type DummyVerifier func(password string)

// loginCandidate holds the user and credential data fetched in a single query.
type loginCandidate struct {
	User         domain.LoginUser
	Status       string
	Deleted      bool
	PasswordHash string //nolint:gosec
}

// PGXLoginStore implements service.LoginStore using a pgx connection pool.
type PGXLoginStore struct {
	pool           Pool
	verifyPassword PasswordVerifier
	dummyVerify    DummyVerifier
}

// NewPGXLoginStore creates a PGXLoginStore backed by the given connection pool.
// verifyPassword and dummyVerify are injected to avoid an import cycle with the service layer.
func NewPGXLoginStore(pool Pool, verifyPassword PasswordVerifier, dummyVerify DummyVerifier) *PGXLoginStore {
	return &PGXLoginStore{pool: pool, verifyPassword: verifyPassword, dummyVerify: dummyVerify}
}

// CreateLoginSession runs the full login transaction:
// policy fetch → user + credential fetch → lockout check → password verify →
// optional device upsert → session insert → token history insert → last-login update.
func (s *PGXLoginStore) CreateLoginSession(ctx context.Context, input domain.CreateSessionInput) (domain.CreatedLoginSession, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.CreatedLoginSession{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	policy, err := selectLoginPolicy(ctx, tx)
	if err != nil {
		return domain.CreatedLoginSession{}, err
	}

	candidate, found, err := selectLoginUser(ctx, tx, input.Email)
	if err != nil {
		return domain.CreatedLoginSession{}, err
	}

	locked, err := loginTemporarilyLocked(ctx, tx, found, candidate.User.ID, input.Email, policy)
	if err != nil {
		return domain.CreatedLoginSession{}, err
	}
	if locked {
		_ = recordLoginAttempt(ctx, tx, nullableUserID(found, candidate.User.ID), input.Email, false, "failed_login_limit_exceeded", input.IPAddress, input.UserAgent)
		return domain.CreatedLoginSession{}, domain.ErrInvalidCredentials
	}

	if !found || candidate.Status != "active" || candidate.Deleted || candidate.PasswordHash == "" {
		if !found {
			s.dummyVerify(input.Password)
		}
		_ = recordLoginAttempt(ctx, tx, nullableUserID(found, candidate.User.ID), input.Email, false, "invalid_credentials", input.IPAddress, input.UserAgent)
		return domain.CreatedLoginSession{}, domain.ErrInvalidCredentials
	}

	ok, verifyErr := s.verifyPassword(input.Password, candidate.PasswordHash)
	if verifyErr != nil || !ok {
		_ = recordLoginAttempt(ctx, tx, candidate.User.ID, input.Email, false, "invalid_credentials", input.IPAddress, input.UserAgent)
		return domain.CreatedLoginSession{}, domain.ErrInvalidCredentials
	}

	deviceID, err := resolveLoginDevice(ctx, tx, candidate.User.ID, input, policy)
	if err != nil {
		if errors.Is(err, domain.ErrInvalidCredentials) {
			_ = recordLoginAttempt(ctx, tx, candidate.User.ID, input.Email, false, "invalid_credentials", input.IPAddress, input.UserAgent)
		}
		return domain.CreatedLoginSession{}, err
	}

	if err := recordLoginAttempt(ctx, tx, candidate.User.ID, input.Email, true, "", input.IPAddress, input.UserAgent); err != nil {
		return domain.CreatedLoginSession{}, err
	}

	session, err := insertLoginSession(ctx, tx, candidate.User.ID, deviceID, input, policy)
	if err != nil {
		return domain.CreatedLoginSession{}, err
	}

	if err := insertInitialRefreshTokenHistory(ctx, tx, session.ID, input.RefreshTokenHash); err != nil {
		return domain.CreatedLoginSession{}, err
	}

	if _, err := tx.Exec(ctx, `UPDATE auth.users SET last_login_at = now() WHERE id = $1`, candidate.User.ID); err != nil {
		return domain.CreatedLoginSession{}, fmt.Errorf("update last_login_at: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return domain.CreatedLoginSession{}, fmt.Errorf("commit tx: %w", err)
	}

	return domain.CreatedLoginSession{Session: session, User: candidate.User}, nil
}

// selectLoginPolicy reads the policy settings from the DB within the given transaction.
func selectLoginPolicy(ctx context.Context, tx pgx.Tx) (domain.PolicySettings, error) {
	var p domain.PolicySettings
	err := tx.QueryRow(ctx, `
		SELECT min_password_length, require_uppercase, require_lowercase,
		       require_number, require_symbol, failed_login_limit,
		       failed_login_window_minutes, failed_login_lockout_minutes,
		       session_idle_timeout_minutes, max_devices_per_user
		FROM auth.auth_policy_settings
		WHERE id = 1`,
	).Scan(
		&p.MinPasswordLength, &p.RequireUppercase, &p.RequireLowercase,
		&p.RequireNumber, &p.RequireSymbol, &p.FailedLoginLimit,
		&p.FailedLoginWindowMinutes, &p.FailedLoginLockoutMinutes,
		&p.SessionIdleTimeoutMinutes, &p.MaxDevicesPerUser,
	)
	if err != nil {
		return domain.PolicySettings{}, fmt.Errorf("get login policy: %w", err)
	}
	return p, nil
}

// selectLoginUser fetches the login candidate (user + password hash) by email.
// Returns found=false when the email does not exist.
func selectLoginUser(ctx context.Context, tx pgx.Tx, email string) (loginCandidate, bool, error) {
	var c loginCandidate
	var deletedAt *time.Time
	err := tx.QueryRow(ctx, `
		SELECT u.id, u.email::text, u.display_name,
		       u.status, u.deleted_at,
		       COALESCE(pc.password_hash, ''),
		       COALESCE(pc.must_change_password, false)
		FROM auth.users AS u
		LEFT JOIN auth.user_password_credentials AS pc ON pc.user_id = u.id
		WHERE u.email = $1`,
		email,
	).Scan(
		&c.User.ID, &c.User.Email, &c.User.DisplayName,
		&c.Status, &deletedAt,
		&c.PasswordHash, &c.User.MustChangePassword,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return loginCandidate{}, false, nil
	}
	if err != nil {
		return loginCandidate{}, false, fmt.Errorf("select login user: %w", err)
	}
	c.Deleted = deletedAt != nil
	return c, true, nil
}

// loginTemporarilyLocked returns true when the number of recent failed login attempts
// reaches or exceeds the policy limit within the failure window.
// When the user is not found, the check is keyed by email; otherwise by user_id.
func loginTemporarilyLocked(ctx context.Context, tx pgx.Tx, found bool, userID, email string, policy domain.PolicySettings) (bool, error) {
	if policy.FailedLoginLimit <= 0 {
		return false, nil
	}

	var rows pgx.Rows
	var err error
	if found {
		rows, err = tx.Query(ctx, `
			SELECT created_at FROM auth.login_attempts
			WHERE user_id = $1
			  AND success = false
			  AND created_at > now() - ($2 * interval '1 minute')`,
			userID, policy.FailedLoginWindowMinutes,
		)
	} else {
		rows, err = tx.Query(ctx, `
			SELECT created_at FROM auth.login_attempts
			WHERE email = $1
			  AND success = false
			  AND created_at > now() - ($2 * interval '1 minute')`,
			email, policy.FailedLoginWindowMinutes,
		)
	}
	if err != nil {
		return false, fmt.Errorf("check login attempts: %w", err)
	}
	defer rows.Close()

	count := 0
	for rows.Next() {
		count++
	}
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("scan login attempts: %w", err)
	}
	return count >= policy.FailedLoginLimit, nil
}

// recordLoginAttempt inserts a row into auth.login_attempts.
// failureReason should be empty string for successful attempts.
func recordLoginAttempt(ctx context.Context, tx pgx.Tx, userID any, email string, success bool, failureReason, ipAddress, userAgent string) error {
	var reason any
	if failureReason != "" {
		reason = failureReason
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO auth.login_attempts (user_id, email, success, failure_reason, ip_address, user_agent)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		userID, email, success, reason, ipAddress, userAgent,
	)
	return err
}

// resolveLoginDevice returns the device_id to use for the session.
// If no device fingerprint is provided, returns nil.
// If a fingerprint is provided, upserts the device and returns its id.
// Returns ErrInvalidCredentials if max devices are exceeded.
func resolveLoginDevice(ctx context.Context, tx pgx.Tx, userID string, input domain.CreateSessionInput, policy domain.PolicySettings) (any, error) {
	if input.DeviceFingerprintHash == "" {
		return nil, nil
	}

	// Try to find existing device by fingerprint.
	var deviceID string
	err := tx.QueryRow(ctx, `
		SELECT id FROM auth.user_devices
		WHERE user_id = $1 AND device_fingerprint_hash = $2`,
		userID, input.DeviceFingerprintHash,
	).Scan(&deviceID)

	if err == nil {
		// Device exists — update last seen metadata.
		_, updateErr := tx.Exec(ctx, `
			UPDATE auth.user_devices
			SET display_name = $1, platform = $2, last_ip = $3, last_seen_at = now()
			WHERE id = $4`,
			nullableString(input.DeviceName), nullableString(input.Platform), nullableString(input.IPAddress), deviceID,
		)
		if updateErr != nil {
			return nil, fmt.Errorf("update device: %w", updateErr)
		}
		return deviceID, nil
	}

	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("select device: %w", err)
	}

	// New device — check against policy.
	var deviceCount int
	if countErr := tx.QueryRow(ctx, `
		SELECT COUNT(*) FROM auth.user_devices
		WHERE user_id = $1 AND revoked_at IS NULL`,
		userID,
	).Scan(&deviceCount); countErr != nil {
		return nil, fmt.Errorf("count devices: %w", countErr)
	}
	if deviceCount >= policy.MaxDevicesPerUser {
		return nil, domain.ErrInvalidCredentials
	}

	// Insert new device.
	var newDeviceID string
	if insertErr := tx.QueryRow(ctx, `
		INSERT INTO auth.user_devices (user_id, device_fingerprint_hash, display_name, platform, last_ip)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id`,
		userID, input.DeviceFingerprintHash,
		nullableString(input.DeviceName), nullableString(input.Platform), nullableString(input.IPAddress),
	).Scan(&newDeviceID); insertErr != nil {
		return nil, fmt.Errorf("insert device: %w", insertErr)
	}
	return newDeviceID, nil
}

// insertLoginSession creates the auth.user_sessions row and returns the new session.
func insertLoginSession(ctx context.Context, tx pgx.Tx, userID string, deviceID any, input domain.CreateSessionInput, policy domain.PolicySettings) (domain.Session, error) {
	idleExpiresAt := time.Now().Add(time.Duration(policy.SessionIdleTimeoutMinutes) * time.Minute)

	var session domain.Session
	err := tx.QueryRow(ctx, `
		INSERT INTO auth.user_sessions
		  (user_id, device_id, refresh_token_hash, ip_address, user_agent, idle_expires_at, absolute_expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING id, user_id`,
		userID, deviceID, input.RefreshTokenHash,
		nullableString(input.IPAddress), nullableString(input.UserAgent),
		idleExpiresAt, input.RefreshExpiresAt,
	).Scan(&session.ID, &session.UserID)
	if err != nil {
		return domain.Session{}, fmt.Errorf("insert session: %w", err)
	}
	return session, nil
}

// insertInitialRefreshTokenHistory records the first refresh token in the rotation history.
func insertInitialRefreshTokenHistory(ctx context.Context, tx pgx.Tx, sessionID, refreshTokenHash string) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO auth.refresh_token_history (session_id, refresh_token_hash)
		VALUES ($1, $2)`,
		sessionID, refreshTokenHash,
	)
	if err != nil {
		return fmt.Errorf("insert refresh token history: %w", err)
	}
	return nil
}

// nullableUserID returns the userID string as-is if found, or nil if not found.
func nullableUserID(found bool, userID string) any {
	if !found {
		return nil
	}
	return userID
}
