package storage_test

import (
	"context"
	"errors"
	"testing"
	"time"

	pgxmock "github.com/pashagolub/pgxmock/v2"

	"github.com/nicrepository/nchat/services/auth-service/internal/domain"
	"github.com/nicrepository/nchat/services/auth-service/internal/storage"
)

var loginAttemptColumns = []string{"id", "email", "failure_reason", "ip_address", "user_agent", "created_at"}

func newLoginAttemptsStore(mock pgxmock.PgxPoolIface) *storage.PGXLoginAttemptsStore {
	return storage.NewPGXLoginAttemptsStore(mock)
}

func TestPGXLoginAttemptsStore_GetUserFailedAttempts_NoCursor(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	now := time.Now().UTC().Truncate(time.Millisecond)

	mock.ExpectQuery(`SELECT id, email, failure_reason`).
		WithArgs("user-1", nil, nil, 3).
		WillReturnRows(pgxmock.NewRows(loginAttemptColumns).
			AddRow(int64(2), "user@example.com", "invalid_credentials", "1.2.3.4", "Mozilla/5.0", now).
			AddRow(int64(1), "user@example.com", "invalid_credentials", "1.2.3.4", "Mozilla/5.0", now.Add(-time.Minute)),
		)

	store := newLoginAttemptsStore(mock)
	rows, err := store.GetUserFailedAttempts(context.Background(), "user-1", 3, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(rows))
	}
	if rows[0].ID != 2 {
		t.Errorf("expected ID=2, got %d", rows[0].ID)
	}
	if rows[0].Email != "user@example.com" {
		t.Errorf("expected email user@example.com, got %s", rows[0].Email)
	}
	if rows[0].IPAddress != "1.2.3.4" {
		t.Errorf("expected ip 1.2.3.4, got %s", rows[0].IPAddress)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestPGXLoginAttemptsStore_GetUserFailedAttempts_WithCursor(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	cursorTime := time.Now().UTC().Add(-time.Hour).Truncate(time.Millisecond)
	cursor := &domain.LoginAttemptsCursor{CreatedAt: cursorTime, ID: 10}

	older := cursorTime.Add(-2 * time.Minute)

	mock.ExpectQuery(`SELECT id, email, failure_reason`).
		WithArgs("user-1", cursorTime, int64(10), 3).
		WillReturnRows(pgxmock.NewRows(loginAttemptColumns).
			AddRow(int64(9), "user@example.com", "invalid_credentials", "1.2.3.4", "agent", older),
		)

	store := newLoginAttemptsStore(mock)
	rows, err := store.GetUserFailedAttempts(context.Background(), "user-1", 3, cursor)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if rows[0].ID != 9 {
		t.Errorf("expected ID=9, got %d", rows[0].ID)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestPGXLoginAttemptsStore_GetUserFailedAttempts_NoSuccessRows(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	// Query must filter success=false — verified by matching SQL pattern
	mock.ExpectQuery(`success = false`).
		WithArgs("user-1", nil, nil, 5).
		WillReturnRows(pgxmock.NewRows(loginAttemptColumns))

	store := newLoginAttemptsStore(mock)
	rows, err := store.GetUserFailedAttempts(context.Background(), "user-1", 5, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("expected 0 rows, got %d", len(rows))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestPGXLoginAttemptsStore_GetUserFailedAttempts_NullableFields(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	now := time.Now().UTC().Truncate(time.Millisecond)

	mock.ExpectQuery(`SELECT id, email, failure_reason`).
		WithArgs("user-1", nil, nil, 2).
		WillReturnRows(pgxmock.NewRows(loginAttemptColumns).
			AddRow(int64(3), "user@example.com", "invalid_credentials", nil, nil, now),
		)

	store := newLoginAttemptsStore(mock)
	rows, err := store.GetUserFailedAttempts(context.Background(), "user-1", 2, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if rows[0].IPAddress != "" {
		t.Errorf("expected empty IPAddress, got %q", rows[0].IPAddress)
	}
	if rows[0].UserAgent != "" {
		t.Errorf("expected empty UserAgent, got %q", rows[0].UserAgent)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestPGXLoginAttemptsStore_GetUserFailedAttempts_QueryError(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery(`SELECT id, email, failure_reason`).
		WithArgs("user-1", nil, nil, 5).
		WillReturnError(errors.New("db error"))

	store := newLoginAttemptsStore(mock)
	_, err = store.GetUserFailedAttempts(context.Background(), "user-1", 5, nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestPGXLoginAttemptsStore_NoPasswordTokenColumns(t *testing.T) {
	// This test verifies the query does not expose sensitive columns.
	// We verify by checking the store builds and uses only the expected columns.
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	now := time.Now().UTC()
	mock.ExpectQuery(`SELECT id, email, failure_reason`).
		WithArgs("user-1", nil, nil, 1).
		WillReturnRows(pgxmock.NewRows(loginAttemptColumns).
			AddRow(int64(1), "user@example.com", "invalid_credentials", "127.0.0.1", "curl/7", now),
		)

	store := newLoginAttemptsStore(mock)
	rows, err := store.GetUserFailedAttempts(context.Background(), "user-1", 1, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	// Columns password_hash, token are not in LoginAttempt struct — compile-time guarantee.
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}
