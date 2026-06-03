package storage_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	pgxmock "github.com/pashagolub/pgxmock/v2"

	"github.com/nicrepository/nchat/services/auth-service/internal/domain"
	"github.com/nicrepository/nchat/services/auth-service/internal/service"
	"github.com/nicrepository/nchat/services/auth-service/internal/storage"
)

// newLoginStore creates a test login store with real argon2 verifiers.
func newLoginStore(mock pgxmock.PgxPoolIface) *storage.PGXLoginStore {
	return storage.NewPGXLoginStore(mock, service.VerifyPassword, service.RunDummyPasswordVerification)
}

// policyColumns is the shared column list for auth.auth_policy_settings queries.
var policyColumns = []string{
	"min_password_length", "require_uppercase", "require_lowercase",
	"require_number", "require_symbol", "failed_login_limit",
	"failed_login_window_minutes", "failed_login_lockout_minutes",
	"session_idle_timeout_minutes", "max_devices_per_user",
	"password_reset_token_ttl_minutes", "invite_token_ttl_hours",
}

// defaultPolicyRow returns a standard policy row: limit=5, window=15, lockout=15, idle=60, maxDevices=5.
func defaultPolicyRow() *pgxmock.Rows {
	return policyRow(5, 15, 15, 60, 5)
}

func policyRow(limit, windowMinutes, lockoutMinutes, idleMinutes, maxDevices int) *pgxmock.Rows {
	return pgxmock.NewRows(policyColumns).
		AddRow(12, true, true, true, true, limit, windowMinutes, lockoutMinutes, idleMinutes, maxDevices, 60, 72)
}

func mustHashPassword(t *testing.T, password string) string {
	t.Helper()
	hash, err := service.HashPassword(password)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	return hash
}

func TestPGXLoginStore_CreateLoginSession_Success(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	passwordHash := mustHashPassword(t, "correct-password")
	refreshExpiresAt := time.Now().Add(30 * 24 * time.Hour).UTC().Truncate(time.Millisecond)

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT min_password_length`).
		WillReturnRows(defaultPolicyRow())
	mock.ExpectExec(`SELECT pg_advisory_xact_lock`).
		WithArgs(pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("", 0))
	mock.ExpectQuery(`SELECT u\.id, u\.email::text, u\.display_name`).
		WithArgs("user@example.com").
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "email", "display_name", "status", "deleted_at", "password_hash", "must_change_password",
		}).AddRow("user-1", "user@example.com", "User", "active", nil, passwordHash, true))
	mock.ExpectQuery(`SELECT created_at FROM auth\.login_attempts`).
		WithArgs("user-1", 30).
		WillReturnRows(pgxmock.NewRows([]string{"created_at"}))
	mock.ExpectExec(`INSERT INTO auth\.login_attempts`).
		WithArgs("user-1", "user@example.com", true, nil, "203.0.113.10", "agent").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectQuery(`INSERT INTO auth\.user_sessions`).
		WithArgs("user-1", nil, "refresh-hash", "203.0.113.10", "agent", pgxmock.AnyArg(), refreshExpiresAt).
		WillReturnRows(pgxmock.NewRows([]string{"id", "user_id"}).AddRow("session-1", "user-1"))
	mock.ExpectExec(`INSERT INTO auth\.refresh_token_history`).
		WithArgs("session-1", "refresh-hash").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectExec(`UPDATE auth\.users`).
		WithArgs("user-1").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectCommit()
	mock.ExpectRollback()

	store := newLoginStore(mock)
	input := domain.CreateSessionInput{
		Password:         "correct-password",
		Email:            "user@example.com",
		RefreshTokenHash: "refresh-hash",
		RefreshExpiresAt: refreshExpiresAt,
		IPAddress:        "203.0.113.10",
		UserAgent:        "agent",
	}
	result, err := store.CreateLoginSession(context.Background(), input)
	if err != nil {
		t.Fatalf("CreateLoginSession: %v", err)
	}
	if result.Session.ID != "session-1" || result.Session.UserID != "user-1" {
		t.Fatalf("unexpected session: %+v", result.Session)
	}
	if result.User.Email != "user@example.com" || !result.User.MustChangePassword {
		t.Fatalf("unexpected user: %+v", result.User)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXLoginStore_CreateLoginSession_WrongPassword(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	passwordHash := mustHashPassword(t, "correct-password")

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT min_password_length`).
		WillReturnRows(defaultPolicyRow())
	mock.ExpectExec(`SELECT pg_advisory_xact_lock`).
		WithArgs(pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("", 0))
	mock.ExpectQuery(`SELECT u\.id, u\.email::text, u\.display_name`).
		WithArgs("user@example.com").
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "email", "display_name", "status", "deleted_at", "password_hash", "must_change_password",
		}).AddRow("user-1", "user@example.com", "User", "active", nil, passwordHash, false))
	mock.ExpectQuery(`SELECT created_at FROM auth\.login_attempts`).
		WithArgs("user-1", 30).
		WillReturnRows(pgxmock.NewRows([]string{"created_at"}))
	mock.ExpectExec(`INSERT INTO auth\.login_attempts`).
		WithArgs("user-1", "user@example.com", false, "invalid_credentials", "1.2.3.4", "ua").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit()
	mock.ExpectRollback()

	store := newLoginStore(mock)
	_, err = store.CreateLoginSession(context.Background(), domain.CreateSessionInput{
		Password:  "wrong-password",
		Email:     "user@example.com",
		IPAddress: "1.2.3.4",
		UserAgent: "ua",
	})
	if !errors.Is(err, domain.ErrInvalidCredentials) {
		t.Fatalf("expected ErrInvalidCredentials, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXLoginStore_CreateLoginSession_UnknownEmail(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT min_password_length`).
		WillReturnRows(defaultPolicyRow())
	mock.ExpectExec(`SELECT pg_advisory_xact_lock`).
		WithArgs(pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("", 0))
	mock.ExpectQuery(`SELECT u\.id, u\.email::text, u\.display_name`).
		WithArgs("nobody@example.com").
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "email", "display_name", "status", "deleted_at", "password_hash", "must_change_password",
		}))
	// Lockout check is by email when user is not found.
	mock.ExpectQuery(`SELECT created_at FROM auth\.login_attempts`).
		WithArgs("nobody@example.com", 30).
		WillReturnRows(pgxmock.NewRows([]string{"created_at"}))
	mock.ExpectExec(`INSERT INTO auth\.login_attempts`).
		WithArgs(nil, "nobody@example.com", false, "invalid_credentials", "1.2.3.4", "ua").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit()
	mock.ExpectRollback()

	store := newLoginStore(mock)
	_, err = store.CreateLoginSession(context.Background(), domain.CreateSessionInput{
		Password:  "some-password",
		Email:     "nobody@example.com",
		IPAddress: "1.2.3.4",
		UserAgent: "ua",
	})
	if !errors.Is(err, domain.ErrInvalidCredentials) {
		t.Fatalf("expected ErrInvalidCredentials, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXLoginStore_CreateLoginSession_SuspendedUser(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	passwordHash := mustHashPassword(t, "correct-password")

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT min_password_length`).
		WillReturnRows(defaultPolicyRow())
	mock.ExpectExec(`SELECT pg_advisory_xact_lock`).
		WithArgs(pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("", 0))
	mock.ExpectQuery(`SELECT u\.id, u\.email::text, u\.display_name`).
		WithArgs("user@example.com").
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "email", "display_name", "status", "deleted_at", "password_hash", "must_change_password",
		}).AddRow("user-1", "user@example.com", "User", "suspended", nil, passwordHash, false))
	mock.ExpectQuery(`SELECT created_at FROM auth\.login_attempts`).
		WithArgs("user-1", 30).
		WillReturnRows(pgxmock.NewRows([]string{"created_at"}))
	mock.ExpectExec(`INSERT INTO auth\.login_attempts`).
		WithArgs("user-1", "user@example.com", false, "invalid_credentials", "1.2.3.4", "ua").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit()
	mock.ExpectRollback()

	store := newLoginStore(mock)
	_, err = store.CreateLoginSession(context.Background(), domain.CreateSessionInput{
		Password:  "correct-password",
		Email:     "user@example.com",
		IPAddress: "1.2.3.4",
		UserAgent: "ua",
	})
	if !errors.Is(err, domain.ErrInvalidCredentials) {
		t.Fatalf("expected ErrInvalidCredentials for suspended user, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXLoginStore_CreateLoginSession_TemporaryLockout(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	passwordHash := mustHashPassword(t, "correct-password")

	// Five recent failures (== limit), added in ASC order (oldest first) as the
	// query now returns ORDER BY created_at ASC. Default policy: window=15, lockout=15,
	// so lookback = 30.
	recentFailures := pgxmock.NewRows([]string{"created_at"})
	for i := 4; i >= 0; i-- {
		recentFailures.AddRow(time.Now().Add(-time.Duration(i) * time.Minute))
	}

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT min_password_length`).
		WillReturnRows(defaultPolicyRow())
	mock.ExpectExec(`SELECT pg_advisory_xact_lock`).
		WithArgs(pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("", 0))
	mock.ExpectQuery(`SELECT u\.id, u\.email::text, u\.display_name`).
		WithArgs("user@example.com").
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "email", "display_name", "status", "deleted_at", "password_hash", "must_change_password",
		}).AddRow("user-1", "user@example.com", "User", "active", nil, passwordHash, false))
	mock.ExpectQuery(`SELECT created_at FROM auth\.login_attempts`).
		WithArgs("user-1", 30).
		WillReturnRows(recentFailures)
	mock.ExpectExec(`INSERT INTO auth\.login_attempts`).
		WithArgs("user-1", "user@example.com", false, "failed_login_limit_exceeded", "1.2.3.4", "ua").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit()
	mock.ExpectRollback()

	store := newLoginStore(mock)
	_, err = store.CreateLoginSession(context.Background(), domain.CreateSessionInput{
		Password:  "correct-password",
		Email:     "user@example.com",
		IPAddress: "1.2.3.4",
		UserAgent: "ua",
	})
	if !errors.Is(err, domain.ErrInvalidCredentials) {
		t.Fatalf("expected ErrInvalidCredentials for locked account, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXLoginStore_CreateLoginSession_LockoutExpiresByLockoutPolicy(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	passwordHash := mustHashPassword(t, "correct-password")
	refreshExpiresAt := time.Now().Add(30 * 24 * time.Hour).UTC().Truncate(time.Millisecond)

	// Failures in ASC order (oldest first). Policy: window=60, lockout=1 → lookback=61.
	// Threshold crossing = newest failure (-2 min). lockoutExpiry = -2+1 = -1 min ago → expired.
	failuresWithinWindow := pgxmock.NewRows([]string{"created_at"})
	for i := 6; i >= 2; i-- {
		failuresWithinWindow.AddRow(time.Now().Add(-time.Duration(i) * time.Minute))
	}

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT min_password_length`).
		WillReturnRows(policyRow(5, 60, 1, 60, 5))
	mock.ExpectExec(`SELECT pg_advisory_xact_lock`).
		WithArgs(pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("", 0))
	mock.ExpectQuery(`SELECT u\.id, u\.email::text, u\.display_name`).
		WithArgs("user@example.com").
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "email", "display_name", "status", "deleted_at", "password_hash", "must_change_password",
		}).AddRow("user-1", "user@example.com", "User", "active", nil, passwordHash, false))
	mock.ExpectQuery(`SELECT created_at FROM auth\.login_attempts`).
		WithArgs("user-1", 61).
		WillReturnRows(failuresWithinWindow)
	mock.ExpectExec(`INSERT INTO auth\.login_attempts`).
		WithArgs("user-1", "user@example.com", true, nil, "1.2.3.4", "ua").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectQuery(`INSERT INTO auth\.user_sessions`).
		WithArgs("user-1", nil, "refresh-hash", "1.2.3.4", "ua", pgxmock.AnyArg(), refreshExpiresAt).
		WillReturnRows(pgxmock.NewRows([]string{"id", "user_id"}).AddRow("session-unlocked", "user-1"))
	mock.ExpectExec(`INSERT INTO auth\.refresh_token_history`).
		WithArgs("session-unlocked", "refresh-hash").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectExec(`UPDATE auth\.users`).
		WithArgs("user-1").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectCommit()
	mock.ExpectRollback()

	store := newLoginStore(mock)
	result, err := store.CreateLoginSession(context.Background(), domain.CreateSessionInput{
		Password:         "correct-password",
		Email:            "user@example.com",
		RefreshTokenHash: "refresh-hash",
		RefreshExpiresAt: refreshExpiresAt,
		IPAddress:        "1.2.3.4",
		UserAgent:        "ua",
	})
	if err != nil {
		t.Fatalf("CreateLoginSession: %v", err)
	}
	if result.Session.ID != "session-unlocked" {
		t.Fatalf("unexpected session: %+v", result.Session)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXLoginStore_CreateLoginSession_NoDeviceFingerprint(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	passwordHash := mustHashPassword(t, "pass")
	refreshExpiresAt := time.Now().Add(30 * 24 * time.Hour).UTC().Truncate(time.Millisecond)

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT min_password_length`).WillReturnRows(defaultPolicyRow())
	mock.ExpectExec(`SELECT pg_advisory_xact_lock`).
		WithArgs(pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("", 0))
	mock.ExpectQuery(`SELECT u\.id, u\.email::text, u\.display_name`).
		WithArgs("user@example.com").
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "email", "display_name", "status", "deleted_at", "password_hash", "must_change_password",
		}).AddRow("user-1", "user@example.com", "User", "active", nil, passwordHash, false))
	mock.ExpectQuery(`SELECT created_at FROM auth\.login_attempts`).
		WithArgs("user-1", 30).
		WillReturnRows(pgxmock.NewRows([]string{"created_at"}))
	// No user_devices queries expected — empty fingerprint.
	mock.ExpectExec(`INSERT INTO auth\.login_attempts`).
		WithArgs("user-1", "user@example.com", true, nil, nil, nil). // empty ip/ua are stored as NULL
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectQuery(`INSERT INTO auth\.user_sessions`).
		WithArgs("user-1", nil, "refresh-hash", nil, nil, pgxmock.AnyArg(), refreshExpiresAt).
		WillReturnRows(pgxmock.NewRows([]string{"id", "user_id"}).AddRow("session-2", "user-1"))
	mock.ExpectExec(`INSERT INTO auth\.refresh_token_history`).
		WithArgs("session-2", "refresh-hash").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectExec(`UPDATE auth\.users`).
		WithArgs("user-1").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectCommit()
	mock.ExpectRollback()

	store := newLoginStore(mock)
	_, err = store.CreateLoginSession(context.Background(), domain.CreateSessionInput{
		Password:              "pass",
		Email:                 "user@example.com",
		RefreshTokenHash:      "refresh-hash",
		RefreshExpiresAt:      refreshExpiresAt,
		DeviceFingerprintHash: "", // no fingerprint
	})
	if err != nil {
		t.Fatalf("CreateLoginSession: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXLoginStore_CreateLoginSession_ExistingDeviceUpdated(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	passwordHash := mustHashPassword(t, "pass")
	refreshExpiresAt := time.Now().Add(30 * 24 * time.Hour).UTC().Truncate(time.Millisecond)

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT min_password_length`).WillReturnRows(defaultPolicyRow())
	mock.ExpectExec(`SELECT pg_advisory_xact_lock`).
		WithArgs(pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("", 0))
	mock.ExpectQuery(`SELECT u\.id, u\.email::text, u\.display_name`).
		WithArgs("user@example.com").
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "email", "display_name", "status", "deleted_at", "password_hash", "must_change_password",
		}).AddRow("user-1", "user@example.com", "User", "active", nil, passwordHash, false))
	mock.ExpectQuery(`SELECT created_at FROM auth\.login_attempts`).
		WithArgs("user-1", 30).
		WillReturnRows(pgxmock.NewRows([]string{"created_at"}))
	// Existing active device lookup (returns id + revoked_at=nil).
	mock.ExpectQuery(`SELECT id, revoked_at FROM auth\.user_devices`).
		WithArgs("user-1", "fp-hash").
		WillReturnRows(pgxmock.NewRows([]string{"id", "revoked_at"}).AddRow("device-1", nil))
	// Update existing device.
	mock.ExpectExec(`UPDATE auth\.user_devices`).
		WithArgs("Laptop", "linux", "1.2.3.4", "device-1").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec(`INSERT INTO auth\.login_attempts`).
		WithArgs("user-1", "user@example.com", true, nil, "1.2.3.4", "ua").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectQuery(`INSERT INTO auth\.user_sessions`).
		WithArgs("user-1", "device-1", "refresh-hash", "1.2.3.4", "ua", pgxmock.AnyArg(), refreshExpiresAt).
		WillReturnRows(pgxmock.NewRows([]string{"id", "user_id"}).AddRow("session-3", "user-1"))
	mock.ExpectExec(`INSERT INTO auth\.refresh_token_history`).
		WithArgs("session-3", "refresh-hash").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectExec(`UPDATE auth\.users`).
		WithArgs("user-1").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectCommit()
	mock.ExpectRollback()

	store := newLoginStore(mock)
	result, err := store.CreateLoginSession(context.Background(), domain.CreateSessionInput{
		Password:              "pass",
		Email:                 "user@example.com",
		RefreshTokenHash:      "refresh-hash",
		RefreshExpiresAt:      refreshExpiresAt,
		DeviceFingerprintHash: "fp-hash",
		DeviceName:            "Laptop",
		Platform:              "linux",
		IPAddress:             "1.2.3.4",
		UserAgent:             "ua",
	})
	if err != nil {
		t.Fatalf("CreateLoginSession: %v", err)
	}
	if result.Session.ID != "session-3" {
		t.Fatalf("unexpected session id: %q", result.Session.ID)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXLoginStore_CreateLoginSession_NewDeviceInserted(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	passwordHash := mustHashPassword(t, "pass")
	refreshExpiresAt := time.Now().Add(30 * 24 * time.Hour).UTC().Truncate(time.Millisecond)

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT min_password_length`).WillReturnRows(defaultPolicyRow())
	mock.ExpectExec(`SELECT pg_advisory_xact_lock`).
		WithArgs(pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("", 0))
	mock.ExpectQuery(`SELECT u\.id, u\.email::text, u\.display_name`).
		WithArgs("user@example.com").
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "email", "display_name", "status", "deleted_at", "password_hash", "must_change_password",
		}).AddRow("user-1", "user@example.com", "User", "active", nil, passwordHash, false))
	mock.ExpectQuery(`SELECT created_at FROM auth\.login_attempts`).
		WithArgs("user-1", 30).
		WillReturnRows(pgxmock.NewRows([]string{"created_at"}))
	// No existing device (no rows returned).
	mock.ExpectQuery(`SELECT id, revoked_at FROM auth\.user_devices`).
		WithArgs("user-1", "new-fp").
		WillReturnRows(pgxmock.NewRows([]string{"id", "revoked_at"}))
	mock.ExpectQuery(`SELECT id FROM auth\.users`).
		WithArgs("user-1").
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow("user-1"))
	// Count shows 2 active devices (< max 5).
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM auth\.user_devices`).
		WithArgs("user-1").
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(2))
	// Insert new device.
	mock.ExpectQuery(`INSERT INTO auth\.user_devices`).
		WithArgs("user-1", "new-fp", nil, nil, nil).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow("device-new"))
	mock.ExpectExec(`INSERT INTO auth\.login_attempts`).
		WithArgs("user-1", "user@example.com", true, nil, nil, nil).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectQuery(`INSERT INTO auth\.user_sessions`).
		WithArgs("user-1", "device-new", "refresh-hash", nil, nil, pgxmock.AnyArg(), refreshExpiresAt).
		WillReturnRows(pgxmock.NewRows([]string{"id", "user_id"}).AddRow("session-new", "user-1"))
	mock.ExpectExec(`INSERT INTO auth\.refresh_token_history`).
		WithArgs("session-new", "refresh-hash").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectExec(`UPDATE auth\.users`).
		WithArgs("user-1").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectCommit()
	mock.ExpectRollback()

	store := newLoginStore(mock)
	result, err := store.CreateLoginSession(context.Background(), domain.CreateSessionInput{
		Password:              "pass",
		Email:                 "user@example.com",
		RefreshTokenHash:      "refresh-hash",
		RefreshExpiresAt:      refreshExpiresAt,
		DeviceFingerprintHash: "new-fp",
	})
	if err != nil {
		t.Fatalf("CreateLoginSession: %v", err)
	}
	if result.Session.ID != "session-new" {
		t.Fatalf("unexpected session: %q", result.Session.ID)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXLoginStore_CreateLoginSession_PolicyQueryError(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT min_password_length`).
		WillReturnError(errors.New("db gone"))
	mock.ExpectRollback()

	store := newLoginStore(mock)
	_, err = store.CreateLoginSession(context.Background(), domain.CreateSessionInput{
		Password: "pass", Email: "user@example.com",
	})
	if err == nil {
		t.Fatal("expected error for policy query failure")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXLoginStore_CreateLoginSession_BeginTxError(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectBegin().WillReturnError(errors.New("pool exhausted"))

	store := newLoginStore(mock)
	_, err = store.CreateLoginSession(context.Background(), domain.CreateSessionInput{
		Password: "pass", Email: "user@example.com",
	})
	if err == nil {
		t.Fatal("expected error for begin tx failure")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXLoginStore_CreateLoginSession_FailedAttemptInsertError(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	passwordHash := mustHashPassword(t, "correct-password")

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT min_password_length`).WillReturnRows(defaultPolicyRow())
	mock.ExpectExec(`SELECT pg_advisory_xact_lock`).
		WithArgs(pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("", 0))
	mock.ExpectQuery(`SELECT u\.id, u\.email::text, u\.display_name`).
		WithArgs("user@example.com").
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "email", "display_name", "status", "deleted_at", "password_hash", "must_change_password",
		}).AddRow("user-1", "user@example.com", "User", "active", nil, passwordHash, false))
	mock.ExpectQuery(`SELECT created_at FROM auth\.login_attempts`).
		WithArgs("user-1", 30).
		WillReturnRows(pgxmock.NewRows([]string{"created_at"}))
	mock.ExpectExec(`INSERT INTO auth\.login_attempts`).
		WithArgs("user-1", "user@example.com", false, "invalid_credentials", "1.2.3.4", "ua").
		WillReturnError(errors.New("attempt insert failed"))
	mock.ExpectRollback()

	store := newLoginStore(mock)
	_, err = store.CreateLoginSession(context.Background(), domain.CreateSessionInput{
		Password:  "wrong-password",
		Email:     "user@example.com",
		IPAddress: "1.2.3.4",
		UserAgent: "ua",
	})
	if err == nil || !strings.Contains(err.Error(), "record failed login attempt") {
		t.Fatalf("expected failed-attempt persistence error, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXLoginStore_CreateLoginSession_FailedAttemptCommitError(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	passwordHash := mustHashPassword(t, "correct-password")

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT min_password_length`).WillReturnRows(defaultPolicyRow())
	mock.ExpectExec(`SELECT pg_advisory_xact_lock`).
		WithArgs(pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("", 0))
	mock.ExpectQuery(`SELECT u\.id, u\.email::text, u\.display_name`).
		WithArgs("user@example.com").
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "email", "display_name", "status", "deleted_at", "password_hash", "must_change_password",
		}).AddRow("user-1", "user@example.com", "User", "active", nil, passwordHash, false))
	mock.ExpectQuery(`SELECT created_at FROM auth\.login_attempts`).
		WithArgs("user-1", 30).
		WillReturnRows(pgxmock.NewRows([]string{"created_at"}))
	mock.ExpectExec(`INSERT INTO auth\.login_attempts`).
		WithArgs("user-1", "user@example.com", false, "invalid_credentials", "1.2.3.4", "ua").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit().WillReturnError(errors.New("commit failed"))
	mock.ExpectRollback()

	store := newLoginStore(mock)
	_, err = store.CreateLoginSession(context.Background(), domain.CreateSessionInput{
		Password:  "wrong-password",
		Email:     "user@example.com",
		IPAddress: "1.2.3.4",
		UserAgent: "ua",
	})
	if err == nil || !strings.Contains(err.Error(), "commit failed login attempt") {
		t.Fatalf("expected failed-attempt commit error, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXLoginStore_CreateLoginSession_DeviceLockError(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	passwordHash := mustHashPassword(t, "pass")

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT min_password_length`).WillReturnRows(defaultPolicyRow())
	mock.ExpectExec(`SELECT pg_advisory_xact_lock`).
		WithArgs(pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("", 0))
	mock.ExpectQuery(`SELECT u\.id, u\.email::text, u\.display_name`).
		WithArgs("user@example.com").
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "email", "display_name", "status", "deleted_at", "password_hash", "must_change_password",
		}).AddRow("user-1", "user@example.com", "User", "active", nil, passwordHash, false))
	mock.ExpectQuery(`SELECT created_at FROM auth\.login_attempts`).
		WithArgs("user-1", 30).
		WillReturnRows(pgxmock.NewRows([]string{"created_at"}))
	mock.ExpectQuery(`SELECT id, revoked_at FROM auth\.user_devices`).
		WithArgs("user-1", "new-fp").
		WillReturnRows(pgxmock.NewRows([]string{"id", "revoked_at"}))
	mock.ExpectQuery(`SELECT id FROM auth\.users`).
		WithArgs("user-1").
		WillReturnError(errors.New("lock failed"))
	mock.ExpectRollback()

	store := newLoginStore(mock)
	_, err = store.CreateLoginSession(context.Background(), domain.CreateSessionInput{
		Password:              "pass",
		Email:                 "user@example.com",
		DeviceFingerprintHash: "new-fp",
	})
	if err == nil || !strings.Contains(err.Error(), "lock user for device insert") {
		t.Fatalf("expected device lock error, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXLoginStore_CreateLoginSession_SessionInsertError(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	passwordHash := mustHashPassword(t, "pass")

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT min_password_length`).WillReturnRows(defaultPolicyRow())
	mock.ExpectExec(`SELECT pg_advisory_xact_lock`).
		WithArgs(pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("", 0))
	mock.ExpectQuery(`SELECT u\.id, u\.email::text, u\.display_name`).
		WithArgs("user@example.com").
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "email", "display_name", "status", "deleted_at", "password_hash", "must_change_password",
		}).AddRow("user-1", "user@example.com", "User", "active", nil, passwordHash, false))
	mock.ExpectQuery(`SELECT created_at FROM auth\.login_attempts`).
		WithArgs("user-1", 30).
		WillReturnRows(pgxmock.NewRows([]string{"created_at"}))
	mock.ExpectExec(`INSERT INTO auth\.login_attempts`).
		WithArgs("user-1", "user@example.com", true, nil, nil, nil).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectQuery(`INSERT INTO auth\.user_sessions`).
		WithArgs("user-1", nil, "", nil, nil, pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(errors.New("constraint violation"))
	mock.ExpectRollback()

	store := newLoginStore(mock)
	_, err = store.CreateLoginSession(context.Background(), domain.CreateSessionInput{
		Password: "pass",
		Email:    "user@example.com",
	})
	if err == nil {
		t.Fatal("expected error for session insert failure")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXLoginStore_CreateLoginSession_RefreshHistoryInsertError(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	passwordHash := mustHashPassword(t, "pass")
	refreshExpiresAt := time.Now().Add(30 * 24 * time.Hour).UTC().Truncate(time.Millisecond)

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT min_password_length`).WillReturnRows(defaultPolicyRow())
	mock.ExpectExec(`SELECT pg_advisory_xact_lock`).
		WithArgs(pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("", 0))
	mock.ExpectQuery(`SELECT u\.id, u\.email::text, u\.display_name`).
		WithArgs("user@example.com").
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "email", "display_name", "status", "deleted_at", "password_hash", "must_change_password",
		}).AddRow("user-1", "user@example.com", "User", "active", nil, passwordHash, false))
	mock.ExpectQuery(`SELECT created_at FROM auth\.login_attempts`).
		WithArgs("user-1", 30).
		WillReturnRows(pgxmock.NewRows([]string{"created_at"}))
	mock.ExpectExec(`INSERT INTO auth\.login_attempts`).
		WithArgs("user-1", "user@example.com", true, nil, nil, nil).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectQuery(`INSERT INTO auth\.user_sessions`).
		WithArgs("user-1", nil, "refresh-hash", nil, nil, pgxmock.AnyArg(), refreshExpiresAt).
		WillReturnRows(pgxmock.NewRows([]string{"id", "user_id"}).AddRow("session-err", "user-1"))
	mock.ExpectExec(`INSERT INTO auth\.refresh_token_history`).
		WithArgs("session-err", "refresh-hash").
		WillReturnError(errors.New("history insert failed"))
	mock.ExpectRollback()

	store := newLoginStore(mock)
	_, err = store.CreateLoginSession(context.Background(), domain.CreateSessionInput{
		Password:         "pass",
		Email:            "user@example.com",
		RefreshTokenHash: "refresh-hash",
		RefreshExpiresAt: refreshExpiresAt,
	})
	if err == nil {
		t.Fatal("expected error for history insert failure")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXLoginStore_CreateLoginSession_MaxDevicesExceeded(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	passwordHash := mustHashPassword(t, "pass")

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT min_password_length`).WillReturnRows(defaultPolicyRow())
	mock.ExpectExec(`SELECT pg_advisory_xact_lock`).
		WithArgs(pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("", 0))
	mock.ExpectQuery(`SELECT u\.id, u\.email::text, u\.display_name`).
		WithArgs("user@example.com").
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "email", "display_name", "status", "deleted_at", "password_hash", "must_change_password",
		}).AddRow("user-1", "user@example.com", "User", "active", nil, passwordHash, false))
	mock.ExpectQuery(`SELECT created_at FROM auth\.login_attempts`).
		WithArgs("user-1", 30).
		WillReturnRows(pgxmock.NewRows([]string{"created_at"}))
	// Device not found (new device) — no existing row for this fingerprint.
	mock.ExpectQuery(`SELECT id, revoked_at FROM auth\.user_devices`).
		WithArgs("user-1", "new-fp-hash").
		WillReturnRows(pgxmock.NewRows([]string{"id", "revoked_at"}))
	mock.ExpectQuery(`SELECT id FROM auth\.users`).
		WithArgs("user-1").
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow("user-1"))
	// Count existing active devices — returns max (5).
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM auth\.user_devices`).
		WithArgs("user-1").
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(5))
	mock.ExpectExec(`INSERT INTO auth\.login_attempts`).
		WithArgs("user-1", "user@example.com", false, "max_devices_exceeded", "1.2.3.4", "ua").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit()
	mock.ExpectRollback()

	store := newLoginStore(mock)
	_, err = store.CreateLoginSession(context.Background(), domain.CreateSessionInput{
		Password:              "pass",
		Email:                 "user@example.com",
		RefreshTokenHash:      "refresh-hash",
		DeviceFingerprintHash: "new-fp-hash",
		IPAddress:             "1.2.3.4",
		UserAgent:             "ua",
	})
	if !errors.Is(err, domain.ErrInvalidCredentials) {
		t.Fatalf("expected ErrInvalidCredentials for max devices, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// TestPGXLoginStore_CreateLoginSession_LockoutActiveAfterWindowExpires verifies that a
// lockout remains active when failures are older than the detection window but still
// within the lockout period (window=15 min, lockout=60 min). The lookback must be
// window + lockout = 75 min so threshold-crossing failures remain visible.
// Lockout must not mutate auth.users.status — the mock expectations contain no
// UPDATE auth.users SET status expectation.
func TestPGXLoginStore_CreateLoginSession_LockoutActiveAfterWindowExpires(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	passwordHash := mustHashPassword(t, "correct-password")

	// Five failures at 20-24 minutes ago: outside the 15-min detection window but
	// inside the 60-min lockout period. Rows in ASC order (oldest first), matching
	// ORDER BY created_at ASC from the query. lookback = 15+60 = 75.
	oldFailures := pgxmock.NewRows([]string{"created_at"})
	for i := 24; i >= 20; i-- {
		oldFailures.AddRow(time.Now().Add(-time.Duration(i) * time.Minute))
	}

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT min_password_length`).
		WillReturnRows(policyRow(5, 15, 60, 60, 5)) // limit=5, window=15, lockout=60
	mock.ExpectExec(`SELECT pg_advisory_xact_lock`).
		WithArgs(pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("", 0))
	mock.ExpectQuery(`SELECT u\.id, u\.email::text, u\.display_name`).
		WithArgs("user@example.com").
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "email", "display_name", "status", "deleted_at", "password_hash", "must_change_password",
		}).AddRow("user-1", "user@example.com", "User", "active", nil, passwordHash, false))
	// lookback = window + lockout = 15 + 60 = 75 minutes
	mock.ExpectQuery(`SELECT created_at FROM auth\.login_attempts`).
		WithArgs("user-1", 75).
		WillReturnRows(oldFailures)
	mock.ExpectExec(`INSERT INTO auth\.login_attempts`).
		WithArgs("user-1", "user@example.com", false, "failed_login_limit_exceeded", "1.2.3.4", "ua").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit()
	mock.ExpectRollback()

	store := newLoginStore(mock)
	_, err = store.CreateLoginSession(context.Background(), domain.CreateSessionInput{
		Password:  "correct-password",
		Email:     "user@example.com",
		IPAddress: "1.2.3.4",
		UserAgent: "ua",
	})
	if !errors.Is(err, domain.ErrInvalidCredentials) {
		t.Fatalf("expected ErrInvalidCredentials (lockout active past window), got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// TestPGXLoginStore_CreateLoginSession_LockoutExpiredAfterLockoutMinutes verifies that
// when all failures are older than failed_login_lockout_minutes (i.e. outside the max
// lookback window), the DB returns no rows and the account is no longer locked.
// Policy: window=15, lockout=60. Failures > 60 min ago are excluded by the DB query
// (lookback = 15+60 = 75); mock returns 0 rows → account unlocked → login succeeds.
func TestPGXLoginStore_CreateLoginSession_LockoutExpiredAfterLockoutMinutes(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	passwordHash := mustHashPassword(t, "correct-password")
	refreshExpiresAt := time.Now().Add(30 * 24 * time.Hour).UTC().Truncate(time.Millisecond)

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT min_password_length`).
		WillReturnRows(policyRow(5, 15, 60, 60, 5)) // limit=5, window=15, lockout=60
	mock.ExpectExec(`SELECT pg_advisory_xact_lock`).
		WithArgs(pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("", 0))
	mock.ExpectQuery(`SELECT u\.id, u\.email::text, u\.display_name`).
		WithArgs("user@example.com").
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "email", "display_name", "status", "deleted_at", "password_hash", "must_change_password",
		}).AddRow("user-1", "user@example.com", "User", "active", nil, passwordHash, false))
	// All prior failures are > 75 min ago — the DB returns no rows (lookback = 75).
	mock.ExpectQuery(`SELECT created_at FROM auth\.login_attempts`).
		WithArgs("user-1", 75).
		WillReturnRows(pgxmock.NewRows([]string{"created_at"}))
	mock.ExpectExec(`INSERT INTO auth\.login_attempts`).
		WithArgs("user-1", "user@example.com", true, nil, "1.2.3.4", "ua").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectQuery(`INSERT INTO auth\.user_sessions`).
		WithArgs("user-1", nil, "refresh-hash", "1.2.3.4", "ua", pgxmock.AnyArg(), refreshExpiresAt).
		WillReturnRows(pgxmock.NewRows([]string{"id", "user_id"}).AddRow("session-expired-lock", "user-1"))
	mock.ExpectExec(`INSERT INTO auth\.refresh_token_history`).
		WithArgs("session-expired-lock", "refresh-hash").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectExec(`UPDATE auth\.users`).
		WithArgs("user-1").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectCommit()
	mock.ExpectRollback()

	store := newLoginStore(mock)
	result, err := store.CreateLoginSession(context.Background(), domain.CreateSessionInput{
		Password:         "correct-password",
		Email:            "user@example.com",
		RefreshTokenHash: "refresh-hash",
		RefreshExpiresAt: refreshExpiresAt,
		IPAddress:        "1.2.3.4",
		UserAgent:        "ua",
	})
	if err != nil {
		t.Fatalf("expected successful login after lockout expired, got %v", err)
	}
	if result.Session.ID != "session-expired-lock" {
		t.Fatalf("unexpected session: %+v", result.Session)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// TestPGXLoginStore_CreateLoginSession_LoginLockAcquireError verifies that a failure to
// acquire the per-email advisory lock propagates as an error.
func TestPGXLoginStore_CreateLoginSession_LoginLockAcquireError(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT min_password_length`).WillReturnRows(defaultPolicyRow())
	mock.ExpectExec(`SELECT pg_advisory_xact_lock`).
		WithArgs(pgxmock.AnyArg()).
		WillReturnError(errors.New("advisory lock failed"))
	mock.ExpectRollback()

	store := newLoginStore(mock)
	_, err = store.CreateLoginSession(context.Background(), domain.CreateSessionInput{
		Password: "pass", Email: "user@example.com",
	})
	if err == nil || !strings.Contains(err.Error(), "acquire login lock") {
		t.Fatalf("expected acquire login lock error, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// TestPGXLoginStore_CreateLoginSession_RevokedDeviceReturnsFailure verifies that when
// the device lookup finds a row with revoked_at IS NOT NULL, the login fails with
// ErrInvalidCredentials and records a device_revoked failure reason. No duplicate INSERT
// is attempted, which avoids a unique-constraint violation on (user_id, device_fingerprint_hash).
func TestPGXLoginStore_CreateLoginSession_RevokedDeviceReturnsFailure(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	passwordHash := mustHashPassword(t, "pass")
	revokedAt := time.Now().Add(-24 * time.Hour)

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT min_password_length`).WillReturnRows(defaultPolicyRow())
	mock.ExpectExec(`SELECT pg_advisory_xact_lock`).
		WithArgs(pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("", 0))
	mock.ExpectQuery(`SELECT u\.id, u\.email::text, u\.display_name`).
		WithArgs("user@example.com").
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "email", "display_name", "status", "deleted_at", "password_hash", "must_change_password",
		}).AddRow("user-1", "user@example.com", "User", "active", nil, passwordHash, false))
	mock.ExpectQuery(`SELECT created_at FROM auth\.login_attempts`).
		WithArgs("user-1", 30).
		WillReturnRows(pgxmock.NewRows([]string{"created_at"}))
	// Device lookup returns the revoked device (revoked_at is NOT NULL).
	// No revoked_at IS NULL filter, so we find it and detect revocation in Go.
	mock.ExpectQuery(`SELECT id, revoked_at FROM auth\.user_devices`).
		WithArgs("user-1", "fp-was-revoked").
		WillReturnRows(pgxmock.NewRows([]string{"id", "revoked_at"}).AddRow("device-revoked-1", &revokedAt))
	// No INSERT into user_devices — no duplicate row attempted.
	mock.ExpectExec(`INSERT INTO auth\.login_attempts`).
		WithArgs("user-1", "user@example.com", false, "device_revoked", "1.2.3.4", "ua").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit()
	mock.ExpectRollback()

	store := newLoginStore(mock)
	_, err = store.CreateLoginSession(context.Background(), domain.CreateSessionInput{
		Password:              "pass",
		Email:                 "user@example.com",
		RefreshTokenHash:      "refresh-hash",
		DeviceFingerprintHash: "fp-was-revoked",
		IPAddress:             "1.2.3.4",
		UserAgent:             "ua",
	})
	if !errors.Is(err, domain.ErrInvalidCredentials) {
		t.Fatalf("expected ErrInvalidCredentials for revoked device, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// TestPGXLoginStore_CreateLoginSession_SparseSingleGroupLockout verifies that 5 credential
// failures spread non-uniformly within the detection window still trigger a lockout.
// The threshold crossing is the 5th (newest) failure; lockout expires from that moment.
func TestPGXLoginStore_CreateLoginSession_SparseSingleGroupLockout(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	passwordHash := mustHashPassword(t, "correct-password")

	// Sparse failures at -14, -10, -5, -3, -1 min ago (span = 13 min ≤ window=15).
	// ASC order (oldest first). lookback = window+lockout = 30.
	sparseFailures := pgxmock.NewRows([]string{"created_at"})
	for _, minutesAgo := range []int{14, 10, 5, 3, 1} {
		sparseFailures.AddRow(time.Now().Add(-time.Duration(minutesAgo) * time.Minute))
	}

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT min_password_length`).WillReturnRows(defaultPolicyRow()) // window=15, lockout=15
	mock.ExpectExec(`SELECT pg_advisory_xact_lock`).
		WithArgs(pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("", 0))
	mock.ExpectQuery(`SELECT u\.id, u\.email::text, u\.display_name`).
		WithArgs("user@example.com").
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "email", "display_name", "status", "deleted_at", "password_hash", "must_change_password",
		}).AddRow("user-1", "user@example.com", "User", "active", nil, passwordHash, false))
	mock.ExpectQuery(`SELECT created_at FROM auth\.login_attempts`).
		WithArgs("user-1", 30).
		WillReturnRows(sparseFailures)
	mock.ExpectExec(`INSERT INTO auth\.login_attempts`).
		WithArgs("user-1", "user@example.com", false, "failed_login_limit_exceeded", "1.2.3.4", "ua").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit()
	mock.ExpectRollback()

	store := newLoginStore(mock)
	_, err = store.CreateLoginSession(context.Background(), domain.CreateSessionInput{
		Password:  "correct-password",
		Email:     "user@example.com",
		IPAddress: "1.2.3.4",
		UserAgent: "ua",
	})
	if !errors.Is(err, domain.ErrInvalidCredentials) {
		t.Fatalf("expected ErrInvalidCredentials (sparse failures within window = locked), got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// TestRF49_UnknownEmailPathGeneric verifies that when the email is not found, the store
// calls dummyVerify (to prevent user-enumeration timing attacks) and returns
// ErrInvalidCredentials — indistinguishable from a wrong-password failure.
// RF-49: unknown-email path must exercise the dummy verifier exactly once.
func TestRF49_UnknownEmailPathGeneric(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	var dummyCalls int
	trackingDummy := func(_ string) { dummyCalls++ }

	// Policy limit=2, window=5, lockout=10 → lookback=15.
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT min_password_length`).
		WillReturnRows(policyRow(2, 5, 10, 60, 5))
	mock.ExpectExec(`SELECT pg_advisory_xact_lock`).
		WithArgs(pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("", 0))
	// User lookup: email not found → empty rows.
	mock.ExpectQuery(`SELECT u\.id, u\.email::text, u\.display_name`).
		WithArgs("ghost@example.com").
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "email", "display_name", "status", "deleted_at", "password_hash", "must_change_password",
		}))
	// Lockout check is keyed by email when user is not found; lookback = 5+10 = 15.
	mock.ExpectQuery(`SELECT created_at FROM auth\.login_attempts`).
		WithArgs("ghost@example.com", 15).
		WillReturnRows(pgxmock.NewRows([]string{"created_at"}))
	mock.ExpectExec(`INSERT INTO auth\.login_attempts`).
		WithArgs(nil, "ghost@example.com", false, "invalid_credentials", "1.2.3.4", "ua").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit()
	mock.ExpectRollback()

	store := storage.NewPGXLoginStore(mock, service.VerifyPassword, trackingDummy)
	_, err = store.CreateLoginSession(context.Background(), domain.CreateSessionInput{
		Password:  "any-password",
		Email:     "ghost@example.com",
		IPAddress: "1.2.3.4",
		UserAgent: "ua",
	})
	if !errors.Is(err, domain.ErrInvalidCredentials) {
		t.Fatalf("expected ErrInvalidCredentials for unknown email, got %v", err)
	}
	if dummyCalls != 1 {
		t.Fatalf("expected dummyVerify called once, got %d", dummyCalls)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// TestPGXLoginStore_CreateLoginSession_NonCredentialFailuresDoNotTriggerLockout verifies
// that failed_login_limit_exceeded, max_devices_exceeded, and similar non-credential failure
// reasons do not count toward the brute-force threshold. The DB query filters by
// failure_reason, so mock returning 0 rows represents a DB that contains only non-credential
// failures — the account is not locked and login proceeds normally.
func TestPGXLoginStore_CreateLoginSession_NonCredentialFailuresDoNotTriggerLockout(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	passwordHash := mustHashPassword(t, "pass")
	refreshExpiresAt := time.Now().Add(30 * 24 * time.Hour).UTC().Truncate(time.Millisecond)

	// Policy with limit=1: even one credential failure would lock the account.
	// DB returns 0 credential failures (all entries are non-credential type and filtered out).
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT min_password_length`).WillReturnRows(policyRow(1, 15, 15, 60, 5))
	mock.ExpectExec(`SELECT pg_advisory_xact_lock`).
		WithArgs(pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("", 0))
	mock.ExpectQuery(`SELECT u\.id, u\.email::text, u\.display_name`).
		WithArgs("user@example.com").
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "email", "display_name", "status", "deleted_at", "password_hash", "must_change_password",
		}).AddRow("user-1", "user@example.com", "User", "active", nil, passwordHash, false))
	// lookback = 15+15 = 30; credential filter returns 0 rows despite non-credential failures in DB.
	mock.ExpectQuery(`SELECT created_at FROM auth\.login_attempts`).
		WithArgs("user-1", 30).
		WillReturnRows(pgxmock.NewRows([]string{"created_at"}))
	mock.ExpectExec(`INSERT INTO auth\.login_attempts`).
		WithArgs("user-1", "user@example.com", true, nil, "1.2.3.4", "ua").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectQuery(`INSERT INTO auth\.user_sessions`).
		WithArgs("user-1", nil, "refresh-hash", "1.2.3.4", "ua", pgxmock.AnyArg(), refreshExpiresAt).
		WillReturnRows(pgxmock.NewRows([]string{"id", "user_id"}).AddRow("session-nc", "user-1"))
	mock.ExpectExec(`INSERT INTO auth\.refresh_token_history`).
		WithArgs("session-nc", "refresh-hash").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectExec(`UPDATE auth\.users`).
		WithArgs("user-1").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectCommit()
	mock.ExpectRollback()

	store := newLoginStore(mock)
	result, err := store.CreateLoginSession(context.Background(), domain.CreateSessionInput{
		Password:         "pass",
		Email:            "user@example.com",
		RefreshTokenHash: "refresh-hash",
		RefreshExpiresAt: refreshExpiresAt,
		IPAddress:        "1.2.3.4",
		UserAgent:        "ua",
	})
	if err != nil {
		t.Fatalf("expected success (non-credential failures filtered), got %v", err)
	}
	if result.Session.ID != "session-nc" {
		t.Fatalf("unexpected session: %+v", result.Session)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}
