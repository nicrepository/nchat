package storage_test

import (
	"context"
	"errors"
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
}

// defaultPolicyRow returns a standard policy row: limit=5, window=15, lockout=15, idle=60, maxDevices=5.
func defaultPolicyRow() *pgxmock.Rows {
	return pgxmock.NewRows(policyColumns).
		AddRow(12, true, true, true, true, 5, 15, 15, 60, 5)
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
	mock.ExpectQuery(`SELECT u\.id, u\.email::text, u\.display_name`).
		WithArgs("user@example.com").
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "email", "display_name", "status", "deleted_at", "password_hash", "must_change_password",
		}).AddRow("user-1", "user@example.com", "User", "active", nil, passwordHash, true))
	mock.ExpectQuery(`SELECT created_at FROM auth\.login_attempts`).
		WithArgs("user-1", 15).
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
	mock.ExpectQuery(`SELECT u\.id, u\.email::text, u\.display_name`).
		WithArgs("user@example.com").
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "email", "display_name", "status", "deleted_at", "password_hash", "must_change_password",
		}).AddRow("user-1", "user@example.com", "User", "active", nil, passwordHash, false))
	mock.ExpectQuery(`SELECT created_at FROM auth\.login_attempts`).
		WithArgs("user-1", 15).
		WillReturnRows(pgxmock.NewRows([]string{"created_at"}))
	mock.ExpectExec(`INSERT INTO auth\.login_attempts`).
		WithArgs("user-1", "user@example.com", false, "invalid_credentials", "1.2.3.4", "ua").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
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
	mock.ExpectQuery(`SELECT u\.id, u\.email::text, u\.display_name`).
		WithArgs("nobody@example.com").
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "email", "display_name", "status", "deleted_at", "password_hash", "must_change_password",
		}))
	// Lockout check is by email when user is not found.
	mock.ExpectQuery(`SELECT created_at FROM auth\.login_attempts`).
		WithArgs("nobody@example.com", 15).
		WillReturnRows(pgxmock.NewRows([]string{"created_at"}))
	mock.ExpectExec(`INSERT INTO auth\.login_attempts`).
		WithArgs(nil, "nobody@example.com", false, "invalid_credentials", "1.2.3.4", "ua").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
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
	mock.ExpectQuery(`SELECT u\.id, u\.email::text, u\.display_name`).
		WithArgs("user@example.com").
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "email", "display_name", "status", "deleted_at", "password_hash", "must_change_password",
		}).AddRow("user-1", "user@example.com", "User", "suspended", nil, passwordHash, false))
	mock.ExpectQuery(`SELECT created_at FROM auth\.login_attempts`).
		WithArgs("user-1", 15).
		WillReturnRows(pgxmock.NewRows([]string{"created_at"}))
	mock.ExpectExec(`INSERT INTO auth\.login_attempts`).
		WithArgs("user-1", "user@example.com", false, "invalid_credentials", "1.2.3.4", "ua").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
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

	// Five recent failures (== limit) means locked.
	recentFailures := pgxmock.NewRows([]string{"created_at"})
	for i := 0; i < 5; i++ {
		recentFailures.AddRow(time.Now().Add(-time.Duration(i) * time.Minute))
	}

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT min_password_length`).
		WillReturnRows(defaultPolicyRow())
	mock.ExpectQuery(`SELECT u\.id, u\.email::text, u\.display_name`).
		WithArgs("user@example.com").
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "email", "display_name", "status", "deleted_at", "password_hash", "must_change_password",
		}).AddRow("user-1", "user@example.com", "User", "active", nil, passwordHash, false))
	mock.ExpectQuery(`SELECT created_at FROM auth\.login_attempts`).
		WithArgs("user-1", 15).
		WillReturnRows(recentFailures)
	mock.ExpectExec(`INSERT INTO auth\.login_attempts`).
		WithArgs("user-1", "user@example.com", false, "failed_login_limit_exceeded", "1.2.3.4", "ua").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
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
	mock.ExpectQuery(`SELECT u\.id, u\.email::text, u\.display_name`).
		WithArgs("user@example.com").
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "email", "display_name", "status", "deleted_at", "password_hash", "must_change_password",
		}).AddRow("user-1", "user@example.com", "User", "active", nil, passwordHash, false))
	mock.ExpectQuery(`SELECT created_at FROM auth\.login_attempts`).
		WithArgs("user-1", 15).
		WillReturnRows(pgxmock.NewRows([]string{"created_at"}))
	// No user_devices queries expected — empty fingerprint.
	mock.ExpectExec(`INSERT INTO auth\.login_attempts`).
		WithArgs("user-1", "user@example.com", true, nil, "", ""). // ip/ua are passed as-is (empty strings)
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
	mock.ExpectQuery(`SELECT u\.id, u\.email::text, u\.display_name`).
		WithArgs("user@example.com").
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "email", "display_name", "status", "deleted_at", "password_hash", "must_change_password",
		}).AddRow("user-1", "user@example.com", "User", "active", nil, passwordHash, false))
	mock.ExpectQuery(`SELECT created_at FROM auth\.login_attempts`).
		WithArgs("user-1", 15).
		WillReturnRows(pgxmock.NewRows([]string{"created_at"}))
	// Existing device lookup.
	mock.ExpectQuery(`SELECT id FROM auth\.user_devices`).
		WithArgs("user-1", "fp-hash").
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow("device-1"))
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
	mock.ExpectQuery(`SELECT u\.id, u\.email::text, u\.display_name`).
		WithArgs("user@example.com").
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "email", "display_name", "status", "deleted_at", "password_hash", "must_change_password",
		}).AddRow("user-1", "user@example.com", "User", "active", nil, passwordHash, false))
	mock.ExpectQuery(`SELECT created_at FROM auth\.login_attempts`).
		WithArgs("user-1", 15).
		WillReturnRows(pgxmock.NewRows([]string{"created_at"}))
	// No existing device.
	mock.ExpectQuery(`SELECT id FROM auth\.user_devices`).
		WithArgs("user-1", "new-fp").
		WillReturnRows(pgxmock.NewRows([]string{"id"}))
	// Count shows 2 active devices (< max 5).
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM auth\.user_devices`).
		WithArgs("user-1").
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(2))
	// Insert new device.
	mock.ExpectQuery(`INSERT INTO auth\.user_devices`).
		WithArgs("user-1", "new-fp", nil, nil, nil).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow("device-new"))
	mock.ExpectExec(`INSERT INTO auth\.login_attempts`).
		WithArgs("user-1", "user@example.com", true, nil, "", "").
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

func TestPGXLoginStore_CreateLoginSession_SessionInsertError(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	passwordHash := mustHashPassword(t, "pass")

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT min_password_length`).WillReturnRows(defaultPolicyRow())
	mock.ExpectQuery(`SELECT u\.id, u\.email::text, u\.display_name`).
		WithArgs("user@example.com").
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "email", "display_name", "status", "deleted_at", "password_hash", "must_change_password",
		}).AddRow("user-1", "user@example.com", "User", "active", nil, passwordHash, false))
	mock.ExpectQuery(`SELECT created_at FROM auth\.login_attempts`).
		WithArgs("user-1", 15).
		WillReturnRows(pgxmock.NewRows([]string{"created_at"}))
	mock.ExpectExec(`INSERT INTO auth\.login_attempts`).
		WithArgs("user-1", "user@example.com", true, nil, "", "").
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
	mock.ExpectQuery(`SELECT u\.id, u\.email::text, u\.display_name`).
		WithArgs("user@example.com").
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "email", "display_name", "status", "deleted_at", "password_hash", "must_change_password",
		}).AddRow("user-1", "user@example.com", "User", "active", nil, passwordHash, false))
	mock.ExpectQuery(`SELECT created_at FROM auth\.login_attempts`).
		WithArgs("user-1", 15).
		WillReturnRows(pgxmock.NewRows([]string{"created_at"}))
	mock.ExpectExec(`INSERT INTO auth\.login_attempts`).
		WithArgs("user-1", "user@example.com", true, nil, "", "").
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
	mock.ExpectQuery(`SELECT u\.id, u\.email::text, u\.display_name`).
		WithArgs("user@example.com").
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "email", "display_name", "status", "deleted_at", "password_hash", "must_change_password",
		}).AddRow("user-1", "user@example.com", "User", "active", nil, passwordHash, false))
	mock.ExpectQuery(`SELECT created_at FROM auth\.login_attempts`).
		WithArgs("user-1", 15).
		WillReturnRows(pgxmock.NewRows([]string{"created_at"}))
	// Device not found (new device).
	mock.ExpectQuery(`SELECT id FROM auth\.user_devices`).
		WithArgs("user-1", "new-fp-hash").
		WillReturnRows(pgxmock.NewRows([]string{"id"}))
	// Count existing active devices — returns max (5).
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM auth\.user_devices`).
		WithArgs("user-1").
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(5))
	mock.ExpectExec(`INSERT INTO auth\.login_attempts`).
		WithArgs("user-1", "user@example.com", false, "invalid_credentials", "1.2.3.4", "ua").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
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
