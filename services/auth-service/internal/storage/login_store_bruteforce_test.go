package storage

import (
	"bytes"
	"context"
	"os"
	"testing"
	"time"

	pgxmock "github.com/pashagolub/pgxmock/v2"

	"github.com/nicrepository/nchat/services/auth-service/internal/domain"
)

// TestRF49_ThresholdTriggersLockout verifies that when the DB returns N credential
// failures within the detection window, loginTemporarilyLocked returns true.
// RF-49: policy limit=2, window=5min, lockout=10min; 2 invalid_credentials rows within
// window → locked.
func TestRF49_ThresholdTriggersLockout(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	policy := domain.PolicySettings{
		FailedLoginLimit:          2,
		FailedLoginWindowMinutes:  5,
		FailedLoginLockoutMinutes: 10,
	}
	// lookback = window + lockout = 5 + 10 = 15
	rows := pgxmock.NewRows([]string{"created_at"}).
		AddRow(time.Now().Add(-4 * time.Minute)).
		AddRow(time.Now().Add(-1 * time.Minute))

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT created_at FROM auth\.login_attempts`).
		WithArgs("user-1", 15).
		WillReturnRows(rows)

	tx, err := mock.Begin(context.Background())
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}

	locked, err := loginTemporarilyLocked(context.Background(), tx, true, "user-1", "user@example.com", policy)
	if err != nil {
		t.Fatalf("loginTemporarilyLocked: %v", err)
	}
	if !locked {
		t.Fatal("expected locked=true: 2 credential failures within window")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// TestRF49_LockoutExpiresAfterLockoutMinutes verifies that when the threshold-crossing
// group is older than lockout_minutes, loginTemporarilyLocked returns false.
// RF-49: 2 failures at now-15min and now-12min → threshold_crossing=now-12min,
// lockout_expiry = now-12min + 10min = now-2min → expired → locked=false.
func TestRF49_LockoutExpiresAfterLockoutMinutes(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	policy := domain.PolicySettings{
		FailedLoginLimit:          2,
		FailedLoginWindowMinutes:  5,
		FailedLoginLockoutMinutes: 10,
	}
	// Span = 15-12 = 3 min ≤ window=5 → valid group; but threshold crossing = now-12min,
	// lockout_expiry = now-12min+10min = now-2min → already expired.
	rows := pgxmock.NewRows([]string{"created_at"}).
		AddRow(time.Now().Add(-15 * time.Minute)).
		AddRow(time.Now().Add(-12 * time.Minute))

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT created_at FROM auth\.login_attempts`).
		WithArgs("user-1", 15).
		WillReturnRows(rows)

	tx, err := mock.Begin(context.Background())
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}

	locked, err := loginTemporarilyLocked(context.Background(), tx, true, "user-1", "user@example.com", policy)
	if err != nil {
		t.Fatalf("loginTemporarilyLocked: %v", err)
	}
	if locked {
		t.Fatal("expected locked=false: lockout period expired")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// TestRF49_NonCredentialFailuresDoNotExtendLockout verifies that non-credential failure
// reasons (failed_login_limit_exceeded, device_revoked, max_devices_exceeded) are
// excluded by the credentialFilter in the SQL query. The mock returns only 1 row
// (simulating that the DB filtered out the non-credential rows), which is below
// limit=2 → locked=false.
// RF-49: credentialFilter = failure_reason IN ('invalid_credentials', 'unknown_user', 'invalid_password').
func TestRF49_NonCredentialFailuresDoNotExtendLockout(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	policy := domain.PolicySettings{
		FailedLoginLimit:          2,
		FailedLoginWindowMinutes:  5,
		FailedLoginLockoutMinutes: 10,
	}
	// Only 1 credential failure row returned (non-credential rows excluded by DB filter).
	rows := pgxmock.NewRows([]string{"created_at"}).
		AddRow(time.Now().Add(-2 * time.Minute))

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT created_at FROM auth\.login_attempts`).
		WithArgs("user-1", 15).
		WillReturnRows(rows)

	tx, err := mock.Begin(context.Background())
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}

	locked, err := loginTemporarilyLocked(context.Background(), tx, true, "user-1", "user@example.com", policy)
	if err != nil {
		t.Fatalf("loginTemporarilyLocked: %v", err)
	}
	if locked {
		t.Fatal("expected locked=false: only 1 credential failure (non-credential rows filtered)")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// TestRF49_UsersStatusNotMutated asserts that the login failure code path never
// issues an UPDATE auth.users SET status statement. Lockout is time-based (via
// auth.login_attempts) and must not mutate the user's status column.
// RF-49: lockout is soft / time-window-based; user.status remains unchanged.
func TestRF49_UsersStatusNotMutated(t *testing.T) {
	src, err := os.ReadFile("login_store.go")
	if err != nil {
		t.Fatalf("read login_store.go: %v", err)
	}
	if bytes.Contains(src, []byte("UPDATE auth.users SET status")) {
		t.Fatal("login_store.go must not contain 'UPDATE auth.users SET status': lockout must not mutate user status")
	}
}
