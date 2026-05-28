package storage_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	pgxmock "github.com/pashagolub/pgxmock/v2"

	"github.com/nicrepository/nchat/services/auth-service/internal/domain"
	"github.com/nicrepository/nchat/services/auth-service/internal/storage"
)

func TestPGXUserStore_GetPolicySettings_Success(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery(`SELECT min_password_length`).
		WillReturnRows(pgxmock.NewRows([]string{
			"min_password_length", "require_uppercase", "require_lowercase",
			"require_number", "require_symbol", "failed_login_limit",
			"failed_login_window_minutes", "failed_login_lockout_minutes",
			"session_idle_timeout_minutes", "max_devices_per_user",
		}).AddRow(12, true, true, true, true, 5, 15, 15, 60, 5))

	store := storage.NewPGXUserStore(mock)
	policy, err := store.GetPolicySettings(context.Background())
	if err != nil {
		t.Fatalf("GetPolicySettings: %v", err)
	}
	if policy.MinPasswordLength != 12 {
		t.Fatalf("expected 12, got %d", policy.MinPasswordLength)
	}
	if !policy.RequireUppercase {
		t.Fatal("expected RequireUppercase true")
	}
	if policy.FailedLoginLimit != 5 || policy.FailedLoginWindowMinutes != 15 || policy.FailedLoginLockoutMinutes != 15 {
		t.Fatalf("unexpected failed login policy: %+v", policy)
	}
	if policy.SessionIdleTimeoutMinutes != 60 || policy.MaxDevicesPerUser != 5 {
		t.Fatalf("unexpected session/device policy: %+v", policy)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXUserStore_GetPolicySettings_QueryError(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery(`SELECT min_password_length`).
		WillReturnError(errors.New("connection lost"))

	store := storage.NewPGXUserStore(mock)
	_, err = store.GetPolicySettings(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXUserStore_CreateUser_Success(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	userID := "550e8400-e29b-41d4-a716-446655440000"
	now := time.Now()

	mock.ExpectBegin()
	mock.ExpectQuery(`INSERT INTO auth\.users`).
		WithArgs("user@example.com", "User Name", nil).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "email", "display_name", "full_name", "status", "auth_source",
			"email_verified_at", "created_at", "updated_at",
		}).AddRow(userID, "user@example.com", "User Name", "", "active", "manual", now, now, now))
	mock.ExpectExec(`INSERT INTO auth\.user_password_credentials`).
		WithArgs(userID, "$argon2id$test-hash", true).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit()
	mock.ExpectRollback() // deferred Rollback after Commit returns pgx.ErrTxClosed

	store := storage.NewPGXUserStore(mock)
	input := domain.CreateUserInput{
		Email: "user@example.com", DisplayName: "User Name",
		MustChangePassword: true,
	}
	user, err := store.CreateUser(context.Background(), input, "$argon2id$test-hash")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if user.ID != userID {
		t.Fatalf("expected %q, got %q", userID, user.ID)
	}
	if user.Email != "user@example.com" {
		t.Fatalf("expected user@example.com, got %q", user.Email)
	}
	if user.Status != "active" {
		t.Fatalf("expected active, got %q", user.Status)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXUserStore_CreateUser_WithFullName(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	userID := "550e8400-e29b-41d4-a716-446655440001"
	now := time.Now()

	mock.ExpectBegin()
	mock.ExpectQuery(`INSERT INTO auth\.users`).
		WithArgs("user@example.com", "User Name", "Full Name").
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "email", "display_name", "full_name", "status", "auth_source",
			"email_verified_at", "created_at", "updated_at",
		}).AddRow(userID, "user@example.com", "User Name", "Full Name", "active", "manual", now, now, now))
	mock.ExpectExec(`INSERT INTO auth\.user_password_credentials`).
		WithArgs(userID, "hash", false).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit()
	mock.ExpectRollback()

	store := storage.NewPGXUserStore(mock)
	input := domain.CreateUserInput{
		Email: "user@example.com", DisplayName: "User Name", FullName: "Full Name",
	}
	user, err := store.CreateUser(context.Background(), input, "hash")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if user.FullName != "Full Name" {
		t.Fatalf("expected 'Full Name', got %q", user.FullName)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXUserStore_CreateUser_CredentialInsertError(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	userID := "550e8400-e29b-41d4-a716-446655440002"
	now := time.Now()

	mock.ExpectBegin()
	mock.ExpectQuery(`INSERT INTO auth\.users`).
		WithArgs("user@example.com", "User", nil).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "email", "display_name", "full_name", "status", "auth_source",
			"email_verified_at", "created_at", "updated_at",
		}).AddRow(userID, "user@example.com", "User", "", "active", "manual", now, now, now))
	mock.ExpectExec(`INSERT INTO auth\.user_password_credentials`).
		WithArgs(userID, "hash", false).
		WillReturnError(errors.New("credential insert failed"))
	mock.ExpectRollback()

	store := storage.NewPGXUserStore(mock)
	input := domain.CreateUserInput{Email: "user@example.com", DisplayName: "User"}
	_, err = store.CreateUser(context.Background(), input, "hash")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXUserStore_CreateUser_BeginTxError(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectBegin().WillReturnError(errors.New("begin tx failed"))

	store := storage.NewPGXUserStore(mock)
	input := domain.CreateUserInput{Email: "user@example.com", DisplayName: "User"}
	_, err = store.CreateUser(context.Background(), input, "hash")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXUserStore_CreateUser_DuplicateEmail(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectBegin()
	mock.ExpectQuery(`INSERT INTO auth\.users`).
		WithArgs("dup@example.com", "Dup", nil).
		WillReturnError(&pgconn.PgError{Code: "23505"})
	mock.ExpectRollback()

	store := storage.NewPGXUserStore(mock)
	input := domain.CreateUserInput{Email: "dup@example.com", DisplayName: "Dup"}
	_, err = store.CreateUser(context.Background(), input, "hash")
	if !errors.Is(err, domain.ErrDuplicateEmail) {
		t.Fatalf("expected ErrDuplicateEmail, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}
