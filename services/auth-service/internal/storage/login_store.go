package storage

import (
	"context"
	"database/sql"
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
	// PasswordChangedAt is when this password was set, and the only input the
	// expiry rule takes from the row. It is zero when the user has no password
	// credential at all, which the caller has already refused by then.
	PasswordChangedAt time.Time
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

// CreateLoginSession runs the full login transaction in three phases:
// prepare (policy, per-email serialization), authenticate, grant.
//
// The phases are separate functions because they answer separate questions and
// fail differently. Authentication either produces a candidate or commits a
// recorded refusal and returns the error a caller may surface; granting either
// produces a session or does the same. Everything below therefore reads as: get
// the policy, take the lock, prove the identity, grant the session, commit.
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

	candidate, err := s.authenticate(ctx, tx, input, policy)
	if err != nil {
		return domain.CreatedLoginSession{}, err
	}

	session, err := grantLoginSession(ctx, tx, candidate.User.ID, input, policy)
	if err != nil {
		return domain.CreatedLoginSession{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return domain.CreatedLoginSession{}, fmt.Errorf("commit tx: %w", err)
	}

	return domain.CreatedLoginSession{Session: session, User: candidate.User}, nil
}

// authenticate proves the request holds a usable credential, or refuses it.
//
// Every refusal here records the attempt and commits before returning, so the
// trail keeps the reason even though the login produced nothing. The error it
// returns is the one the caller surfaces unchanged: generic for anything
// credential-shaped, and specific for an expired password, which is the one
// refusal the person needs to be able to act on.
func (s *PGXLoginStore) authenticate(ctx context.Context, tx pgx.Tx, input domain.CreateSessionInput, policy domain.PolicySettings) (loginCandidate, error) {
	candidate, found, err := selectLoginUser(ctx, tx, input.Email)
	if err != nil {
		return loginCandidate{}, err
	}

	locked, err := loginTemporarilyLocked(ctx, tx, found, candidate.User.ID, input.Email, policy)
	if err != nil {
		return loginCandidate{}, err
	}
	if locked {
		_, err := commitFailedLoginAttempt(ctx, tx, nullableUserID(found, candidate.User.ID), input, "failed_login_limit_exceeded")
		return loginCandidate{}, err
	}

	if !usableCredential(candidate, found) {
		// The dummy verification keeps the response time of an unknown or
		// unusable account indistinguishable from a wrong password.
		s.dummyVerify(input.Password)
		_, err := commitFailedLoginAttempt(ctx, tx, nullableUserID(found, candidate.User.ID), input, "invalid_credentials")
		return loginCandidate{}, err
	}

	ok, verifyErr := s.verifyPassword(input.Password, candidate.PasswordHash)
	if verifyErr != nil || !ok {
		_, err := commitFailedLoginAttempt(ctx, tx, candidate.User.ID, input, "invalid_credentials")
		return loginCandidate{}, err
	}

	// RF-47 password expiry, checked after the password is known to be correct
	// and before anything is granted.
	//
	// It is recorded as a refusal rather than a credential failure, so it lands
	// in the trail with its own reason and does not feed the brute-force
	// counter — repeatedly presenting a *correct* password is not an attack, and
	// locking the account for it would punish the person who owns it.
	if domain.PasswordExpired(candidate.PasswordChangedAt, time.Now(), policy) {
		_, err := commitRefusedLogin(ctx, tx, candidate.User.ID, input, "password_expired", domain.ErrPasswordExpired)
		return loginCandidate{}, err
	}

	return candidate, nil
}

// usableCredential reports whether this row can be authenticated against at all:
// the account exists, is active, is not soft-deleted, and has a password set.
//
// All four are the same answer to the caller — invalid credentials — so they are
// one predicate rather than four branches that must each remember not to say
// which of them applied.
func usableCredential(candidate loginCandidate, found bool) bool {
	return found && candidate.Status == "active" && !candidate.Deleted && candidate.PasswordHash != ""
}

// grantLoginSession creates the session and everything that must exist with it.
//
// It starts by re-validating that the user is still active, taking a row-level
// lock. This serializes with the suspension transaction (which also locks the
// user row via SELECT ... FOR UPDATE). Two outcomes:
//   - If suspension committed first (status != 'active'): abort login; no session created.
//   - If login reaches this point first: hold the lock through session insert; suspension
//     then waits, locks the row, and revokes the newly-created session in its own TX.
//
// The error is the same generic ErrInvalidCredentials used for wrong passwords so that
// callers cannot distinguish "suspended" from "bad credentials".
func grantLoginSession(ctx context.Context, tx pgx.Tx, userID string, input domain.CreateSessionInput, policy domain.PolicySettings) (domain.Session, error) {
	if err := revalidateUserActive(ctx, tx, userID); err != nil {
		return domain.Session{}, err
	}

	deviceID, err := resolveLoginDevice(ctx, tx, userID, input, policy)
	if err != nil {
		return domain.Session{}, commitDeviceRefusal(ctx, tx, userID, input, err)
	}

	if err := recordLoginAttempt(ctx, tx, userID, input.Email, true, "", input.IPAddress, input.UserAgent); err != nil {
		return domain.Session{}, err
	}

	session, err := insertLoginSession(ctx, tx, userID, deviceID, input, policy)
	if err != nil {
		return domain.Session{}, err
	}

	if err := insertInitialRefreshTokenHistory(ctx, tx, session.ID, input.RefreshTokenHash); err != nil {
		return domain.Session{}, err
	}

	if _, err := tx.Exec(ctx, `UPDATE auth.users SET last_login_at = now() WHERE id = $1`, userID); err != nil {
		return domain.Session{}, fmt.Errorf("update last_login_at: %w", err)
	}
	return session, nil
}

// commitDeviceRefusal turns a device-binding failure into the recorded refusal
// it should be, or passes an infrastructure failure through untouched.
//
// Neither refusal counts toward the lockout: the credential was correct, and the
// lockout query only counts credential reasons.
func commitDeviceRefusal(ctx context.Context, tx pgx.Tx, userID string, input domain.CreateSessionInput, cause error) error {
	reason := ""
	switch {
	case errors.Is(cause, errDeviceRevoked):
		reason = "device_revoked"
	case errors.Is(cause, domain.ErrInvalidCredentials):
		reason = "max_devices_exceeded"
	default:
		return cause
	}
	_, err := commitFailedLoginAttempt(ctx, tx, userID, input, reason)
	return err
}

// selectLoginPolicy reads the policy settings from the DB within the given transaction.
func selectLoginPolicy(ctx context.Context, tx pgx.Tx) (domain.PolicySettings, error) {
	var p domain.PolicySettings
	err := tx.QueryRow(ctx, `
		SELECT min_password_length, require_uppercase, require_lowercase,
		       require_number, require_symbol, failed_login_limit,
		       failed_login_window_minutes, failed_login_lockout_minutes,
		       session_idle_timeout_minutes, max_devices_per_user,
		       password_reset_token_ttl_minutes, invite_token_ttl_hours,
		       -- NULL is "passwords do not expire". The column CHECK refuses a
		       -- stored zero, so collapsing NULL to zero here cannot be
		       -- mistaken for a configured expiry.
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
		return domain.PolicySettings{}, fmt.Errorf("get login policy: %w", err)
	}
	return p, nil
}

// selectLoginUser fetches the login candidate (user + password hash) by email.
// Returns found=false when the email does not exist.
func selectLoginUser(ctx context.Context, tx pgx.Tx, email string) (loginCandidate, bool, error) {
	var c loginCandidate
	var deletedAt *time.Time
	// Nullable because the join is outer: a user with no password credential has
	// no password age, and is refused before the expiry rule is ever consulted.
	var passwordChangedAt sql.NullTime
	err := tx.QueryRow(ctx, `
		SELECT u.id, u.email::text, u.display_name,
		       u.status, u.deleted_at,
		       COALESCE(pc.password_hash, ''),
		       COALESCE(pc.must_change_password, false),
		       pc.password_changed_at
		FROM auth.users AS u
		LEFT JOIN auth.user_password_credentials AS pc ON pc.user_id = u.id
		WHERE u.email = $1
		  AND u.auth_source = 'manual'`,
		email,
	).Scan(
		&c.User.ID, &c.User.Email, &c.User.DisplayName,
		&c.Status, &deletedAt,
		&c.PasswordHash, &c.User.MustChangePassword, &passwordChangedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return loginCandidate{}, false, nil
	}
	if err != nil {
		return loginCandidate{}, false, fmt.Errorf("select login user: %w", err)
	}
	c.Deleted = deletedAt != nil
	if passwordChangedAt.Valid {
		c.PasswordChangedAt = passwordChangedAt.Time
	}
	return c, true, nil
}

// errDeviceRevoked is returned by resolveLoginDevice when the matching device exists
// but has been revoked. The caller records a device_revoked failure and returns 401.
var errDeviceRevoked = errors.New("device revoked")

// loginTemporarilyLocked returns true when a credential threshold-crossing group is
// still within the lockout period. Only credential failure reasons are counted; locked-out,
// device-limit, and rate-limit denials must never extend or create a lockout.
// When the user is not found, the check is keyed by email; otherwise by user_id.
func loginTemporarilyLocked(ctx context.Context, tx pgx.Tx, found bool, userID, email string, policy domain.PolicySettings) (bool, error) {
	if policy.FailedLoginLimit <= 0 {
		return false, nil
	}

	// A threshold-crossing group can start as far back as (window + lockout) minutes
	// ago and still keep the account locked now: the 5th failure might have happened
	// up to lockout_minutes ago, and the 1st failure up to window_minutes before that.
	lookbackMinutes := policy.FailedLoginWindowMinutes + policy.FailedLoginLockoutMinutes

	// Only genuine credential failures count toward the brute-force threshold.
	// Non-credential failures (lockout-denied, device errors, rate-limit) must not
	// extend or create a lockout.
	const credentialFilter = `failure_reason IN ('invalid_credentials', 'unknown_user', 'invalid_password')`

	var rows pgx.Rows
	var err error
	if found {
		rows, err = tx.Query(ctx, `
			SELECT created_at FROM auth.login_attempts
			WHERE user_id = $1
			  AND success = false
			  AND `+credentialFilter+`
			  AND created_at >= now() - ($2 * interval '1 minute')
			ORDER BY created_at ASC`,
			userID, lookbackMinutes,
		)
	} else {
		rows, err = tx.Query(ctx, `
			SELECT created_at FROM auth.login_attempts
			WHERE email = $1
			  AND success = false
			  AND `+credentialFilter+`
			  AND created_at >= now() - ($2 * interval '1 minute')
			ORDER BY created_at ASC`,
			email, lookbackMinutes,
		)
	}
	if err != nil {
		return false, fmt.Errorf("check login attempts: %w", err)
	}
	defer rows.Close()

	var failures []time.Time
	for rows.Next() {
		var t time.Time
		if err := rows.Scan(&t); err != nil {
			return false, fmt.Errorf("scan login attempts: %w", err)
		}
		failures = append(failures, t)
	}
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("scan login attempts: %w", err)
	}

	n := policy.FailedLoginLimit
	if len(failures) < n {
		return false, nil
	}

	// failures is sorted ASC: failures[0] is the oldest, failures[len-1] is the newest.
	// Find the most recent group of N consecutive failures whose span fits within the
	// detection window. The threshold crossing time is failures[i+N-1] (the Nth failure
	// in the group — the moment the limit was reached). Lockout is active while
	// now < threshold_crossing + lockout_minutes.
	//
	// We iterate from the newest possible group (i = len-N) downward. Once we find a
	// valid group whose lockout has expired, all earlier groups are also expired (their
	// threshold crossings are older), so we stop.
	window := time.Duration(policy.FailedLoginWindowMinutes) * time.Minute
	lockout := time.Duration(policy.FailedLoginLockoutMinutes) * time.Minute
	now := time.Now()

	for i := len(failures) - n; i >= 0; i-- {
		oldest := failures[i]
		newest := failures[i+n-1]
		if newest.Sub(oldest) > window {
			continue
		}
		// Valid threshold-crossing group: newest is the moment the limit was reached.
		if newest.Add(lockout).After(now) {
			return true, nil
		}
		// This group's lockout expired; no earlier group can have a later threshold
		// crossing, so all remaining groups are also expired.
		break
	}
	return false, nil
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
	return commitRefusedLogin(ctx, tx, userID, input, failureReason, domain.ErrInvalidCredentials)
}

// commitRefusedLogin records the refusal, commits the attempt, and answers with
// the error the caller must surface.
//
// The outcome is a parameter because not every refusal is a credential
// failure: a correct password that has expired is refused with its own error,
// and the person presenting it has to be told which of the two happened or they
// cannot act on it. The recording and the commit are identical either way,
// which is why there is one function and not two.
func commitRefusedLogin(ctx context.Context, tx pgx.Tx, userID any, input domain.CreateSessionInput, failureReason string, outcome error) (domain.CreatedLoginSession, error) {
	if err := recordLoginAttempt(ctx, tx, userID, input.Email, false, failureReason, input.IPAddress, input.UserAgent); err != nil {
		return domain.CreatedLoginSession{}, fmt.Errorf("record failed login attempt: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.CreatedLoginSession{}, fmt.Errorf("commit failed login attempt: %w", err)
	}
	return domain.CreatedLoginSession{}, outcome
}

// resolveLoginDevice returns the device_id to use for the session.
// If no device fingerprint is provided, returns nil (session created without device binding).
// If a fingerprint is provided, looks up the device by (user_id, device_fingerprint_hash)
// regardless of revocation state to avoid duplicate-key errors on a non-partial unique constraint:
//   - Revoked device → returns errDeviceRevoked; caller records a device_revoked failure.
//   - Active device  → updates last_seen_at / last_ip / metadata; returns device id.
//   - No device      → enforces max_devices_per_user against active (non-revoked) devices, then inserts.
func resolveLoginDevice(ctx context.Context, tx pgx.Tx, userID string, input domain.CreateSessionInput, policy domain.PolicySettings) (any, error) {
	if input.DeviceFingerprintHash == "" {
		return nil, nil
	}

	// Look up the device regardless of revocation status.
	// Excluding revoked rows here would allow a subsequent INSERT to hit the unique
	// constraint on (user_id, device_fingerprint_hash), producing a 500.
	var deviceID string
	var revokedAt *time.Time
	err := tx.QueryRow(ctx, `
		SELECT id, revoked_at FROM auth.user_devices
		WHERE user_id = $1 AND device_fingerprint_hash = $2`,
		userID, input.DeviceFingerprintHash,
	).Scan(&deviceID, &revokedAt)

	if err == nil {
		if revokedAt != nil {
			// Revoked device: do not reactivate silently. Let the caller record a
			// device_revoked failure and return 401.
			return nil, errDeviceRevoked
		}
		// Active device — update last seen metadata.
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

// revalidateUserActive re-checks that the user is still active immediately before inserting
// session artifacts. It acquires a row-level lock (FOR UPDATE) so that this check
// serializes correctly with the suspension transaction.
//
// Returns domain.ErrInvalidCredentials (same code used for wrong passwords) if the user
// is no longer active, ensuring callers cannot distinguish "suspended" from "bad credentials".
func revalidateUserActive(ctx context.Context, tx pgx.Tx, userID string) error {
	var id string
	err := tx.QueryRow(ctx, `
		SELECT id FROM auth.users
		WHERE id = $1 AND status = 'active' AND deleted_at IS NULL
		FOR UPDATE`,
		userID,
	).Scan(&id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrInvalidCredentials
		}
		return fmt.Errorf("revalidate user active: %w", err)
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
