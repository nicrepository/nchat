package storage

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
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

	// Serialize the lockout-check + attempt-insert per email so concurrent failed
	// logins cannot both pass the check before either records a failure.
	// pg_advisory_xact_lock is transaction-scoped and auto-released on commit/rollback.
	// The key is a deterministic FNV-1a hash of the normalized email — no raw value stored.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, emailAdvisoryKey(input.Email)); err != nil {
		return domain.CreatedLoginSession{}, fmt.Errorf("acquire login lock: %w", err)
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
		return commitFailedLoginAttempt(ctx, tx, nullableUserID(found, candidate.User.ID), input, "failed_login_limit_exceeded")
	}

	if !found || candidate.Status != "active" || candidate.Deleted || candidate.PasswordHash == "" {
		s.dummyVerify(input.Password)
		return commitFailedLoginAttempt(ctx, tx, nullableUserID(found, candidate.User.ID), input, "invalid_credentials")
	}

	ok, verifyErr := s.verifyPassword(input.Password, candidate.PasswordHash)
	if verifyErr != nil || !ok {
		return commitFailedLoginAttempt(ctx, tx, candidate.User.ID, input, "invalid_credentials")
	}

	deviceID, err := resolveLoginDevice(ctx, tx, candidate.User.ID, input, policy)
	if err != nil {
		if errors.Is(err, domain.ErrInvalidCredentials) {
			return commitFailedLoginAttempt(ctx, tx, candidate.User.ID, input, "max_devices_exceeded")
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
		WHERE u.email = $1
		  AND u.auth_source = 'manual'`,
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

	// Look back as far as the lockout can last so that failures which triggered
	// a lockout remain visible even after the shorter detection window expires.
	lookbackMinutes := policy.FailedLoginWindowMinutes
	if policy.FailedLoginLockoutMinutes > lookbackMinutes {
		lookbackMinutes = policy.FailedLoginLockoutMinutes
	}

	var rows pgx.Rows
	var err error
	if found {
		rows, err = tx.Query(ctx, `
			SELECT created_at FROM auth.login_attempts
			WHERE user_id = $1
			  AND success = false
			  AND created_at >= now() - ($2 * interval '1 minute')
			ORDER BY created_at DESC
			LIMIT $3`,
			userID, lookbackMinutes, policy.FailedLoginLimit,
		)
	} else {
		rows, err = tx.Query(ctx, `
			SELECT created_at FROM auth.login_attempts
			WHERE email = $1
			  AND success = false
			  AND created_at >= now() - ($2 * interval '1 minute')
			ORDER BY created_at DESC
			LIMIT $3`,
			email, lookbackMinutes, policy.FailedLoginLimit,
		)
	}
	if err != nil {
		return false, fmt.Errorf("check login attempts: %w", err)
	}
	defer rows.Close()

	failures := make([]time.Time, 0, policy.FailedLoginLimit)
	for rows.Next() {
		var createdAt time.Time
		if err := rows.Scan(&createdAt); err != nil {
			return false, fmt.Errorf("scan login attempts: %w", err)
		}
		failures = append(failures, createdAt)
	}
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("scan login attempts: %w", err)
	}
	if len(failures) < policy.FailedLoginLimit {
		return false, nil
	}
	// failures is DESC: [0] is most recent, [limit-1] is oldest.
	// The N most recent failures must span <= the detection window to represent a
	// valid threshold crossing; without this, sparse failures beyond the window
	// would falsely appear as a lockout.
	window := time.Duration(policy.FailedLoginWindowMinutes) * time.Minute
	if failures[0].Sub(failures[policy.FailedLoginLimit-1]) > window {
		return false, nil
	}
	thresholdCrossing := failures[policy.FailedLoginLimit-1]
	lockoutExpiresAt := thresholdCrossing.Add(time.Duration(policy.FailedLoginLockoutMinutes) * time.Minute)
	return lockoutExpiresAt.After(time.Now()), nil
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
		userID, email, success, reason, nullableString(ipAddress), nullableString(userAgent),
	)
	return err
}

func commitFailedLoginAttempt(ctx context.Context, tx pgx.Tx, userID any, input domain.CreateSessionInput, failureReason string) (domain.CreatedLoginSession, error) {
	if err := recordLoginAttempt(ctx, tx, userID, input.Email, false, failureReason, input.IPAddress, input.UserAgent); err != nil {
		return domain.CreatedLoginSession{}, fmt.Errorf("record failed login attempt: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.CreatedLoginSession{}, fmt.Errorf("commit failed login attempt: %w", err)
	}
	return domain.CreatedLoginSession{}, domain.ErrInvalidCredentials
}

// resolveLoginDevice returns the device_id to use for the session.
// If no device fingerprint is provided, returns nil.
// If a fingerprint is provided, upserts the device and returns its id.
// Returns ErrInvalidCredentials if max devices are exceeded.
func resolveLoginDevice(ctx context.Context, tx pgx.Tx, userID string, input domain.CreateSessionInput, policy domain.PolicySettings) (any, error) {
	if input.DeviceFingerprintHash == "" {
		return nil, nil
	}

	// Try to find existing non-revoked device by fingerprint.
	// Revoked devices are excluded so they are not silently reactivated.
	var deviceID string
	err := tx.QueryRow(ctx, `
		SELECT id FROM auth.user_devices
		WHERE user_id = $1 AND device_fingerprint_hash = $2 AND revoked_at IS NULL`,
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

	// New device — serialize count and insert so concurrent logins cannot bypass the max-device policy.
	if err := lockUserForDeviceInsert(ctx, tx, userID); err != nil {
		return nil, err
	}

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

func lockUserForDeviceInsert(ctx context.Context, tx pgx.Tx, userID string) error {
	var lockedID string
	if err := tx.QueryRow(ctx, `
		SELECT id FROM auth.users
		WHERE id = $1
		FOR UPDATE`,
		userID,
	).Scan(&lockedID); err != nil {
		return fmt.Errorf("lock user for device insert: %w", err)
	}
	return nil
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
		INSERT INTO auth.refresh_token_history (session_id, refresh_token_hash, status)
		VALUES ($1, $2, 'active')`,
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

// emailAdvisoryKey returns a deterministic int64 advisory lock key for the given
// (already-normalized) email. FNV-1a gives a stable, compact hash with no raw
// email exposed in the advisory lock table.
func emailAdvisoryKey(email string) int64 {
	h := fnv.New64a()
	h.Write([]byte(email))
	return int64(h.Sum64()) //nolint:gosec // G115: intentional uint64→int64 reinterpret for advisory lock key; wrap is acceptable
}
