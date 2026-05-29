package storage_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	pgxmock "github.com/pashagolub/pgxmock/v2"

	"github.com/nicrepository/nchat/services/auth-service/internal/domain"
	"github.com/nicrepository/nchat/services/auth-service/internal/storage"
)

const encryptedResetPayload = `{"alg":"AES-256-GCM","key_version":"v1","nonce":"bm9uY2U=","ciphertext":"Y2lwaGVydGV4dA=="}`

func TestPGXPasswordResetStore_GetActiveUserForPasswordReset(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery(`(?s)SELECT id.*status = 'active'.*deleted_at IS NULL.*auth_source = 'manual'`).
		WithArgs("user@example.com").
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow("user-1"))

	store := storage.NewPGXPasswordResetStore(mock)
	userID, found, err := store.GetActiveUserForPasswordReset(context.Background(), "user@example.com")
	if err != nil {
		t.Fatalf("GetActiveUserForPasswordReset: %v", err)
	}
	if !found || userID != "user-1" {
		t.Fatalf("expected active user-1, found=%v id=%q", found, userID)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func expectPasswordResetUserLock(mock pgxmock.PgxPoolIface) {
	mock.ExpectQuery(`(?s)SELECT id.*FROM auth\.users.*FOR UPDATE`).
		WithArgs("user-1", "user@example.com").
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow("user-1"))
}

func TestPGXPasswordResetStore_GetActiveUserForPasswordResetUnknown(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery(`SELECT id`).
		WithArgs("missing@example.com").
		WillReturnError(pgx.ErrNoRows)

	store := storage.NewPGXPasswordResetStore(mock)
	userID, found, err := store.GetActiveUserForPasswordReset(context.Background(), "missing@example.com")
	if err != nil {
		t.Fatalf("GetActiveUserForPasswordReset: %v", err)
	}
	if found || userID != "" {
		t.Fatalf("expected not found, found=%v id=%q", found, userID)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXPasswordResetStore_CreatePasswordResetTokenSupersedesAndEnqueuesEncryptedOutbox(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	expiresAt := time.Now().Add(time.Hour)
	mock.ExpectBegin()
	expectPasswordResetUserLock(mock)
	mock.ExpectExec(`UPDATE auth\.password_reset_tokens`).
		WithArgs("user-1").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectQuery(`INSERT INTO auth\.password_reset_tokens`).
		WithArgs("user-1", "hashed-reset-token", expiresAt).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow("reset-id-1"))
	mock.ExpectExec(`INSERT INTO auth\.email_outbox`).
		WithArgs("user@example.com", "reset-id-1", "user-1", encryptedResetPayload).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit()
	mock.ExpectRollback()

	store := storage.NewPGXPasswordResetStore(mock)
	if err := store.CreatePasswordResetToken(context.Background(), "user-1", "user@example.com", "hashed-reset-token", expiresAt, encryptedResetPayload); err != nil {
		t.Fatalf("CreatePasswordResetToken: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXPasswordResetStore_ResetPasswordTxValidTokenUpdatesCredentialAndRevokesSessions(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT t\.id, t\.user_id, t\.used_at IS NOT NULL, t\.expires_at`).
		WithArgs("hashed-token").
		WillReturnRows(pgxmock.NewRows([]string{"id", "user_id", "used", "expires_at"}).AddRow("reset-1", "user-1", false, time.Now().Add(time.Hour)))
	mock.ExpectExec(`UPDATE auth\.user_password_credentials`).
		WithArgs("argon2id-hash", "user-1").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec(`UPDATE auth\.password_reset_tokens`).
		WithArgs("reset-1").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec(`UPDATE auth\.user_sessions`).
		WithArgs("user-1").
		WillReturnResult(pgxmock.NewResult("UPDATE", 2))
	mock.ExpectExec(`UPDATE auth\.refresh_token_history`).
		WithArgs("user-1").
		WillReturnResult(pgxmock.NewResult("UPDATE", 2))
	mock.ExpectCommit()
	mock.ExpectRollback()

	store := storage.NewPGXPasswordResetStore(mock)
	if err := store.ResetPasswordTx(context.Background(), "hashed-token", "argon2id-hash"); err != nil {
		t.Fatalf("ResetPasswordTx: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXPasswordResetStore_ResetPasswordTxRejectsIneligibleTokenOwners(t *testing.T) {
	for _, name := range []string{"suspended", "deleted", "locked", "non-manual"} {
		t.Run(name, func(t *testing.T) {
			mock, err := pgxmock.NewPool()
			if err != nil {
				t.Fatalf("pgxmock: %v", err)
			}
			defer mock.Close()

			mock.ExpectBegin()
			mock.ExpectQuery(`(?s)JOIN auth\.users AS u.*u\.status = 'active'.*u\.deleted_at IS NULL.*u\.auth_source = 'manual'`).
				WithArgs("hashed-token").
				WillReturnError(pgx.ErrNoRows)
			mock.ExpectRollback()

			store := storage.NewPGXPasswordResetStore(mock)
			err = store.ResetPasswordTx(context.Background(), "hashed-token", "argon2id-hash")
			if !errors.Is(err, domain.ErrInvalidToken) {
				t.Fatalf("expected ErrInvalidToken, got %v", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet expectations: %v", err)
			}
		})
	}
}

func TestPGXPasswordResetStore_ResetPasswordTxRejectsUnknownExpiredAndUsedTokens(t *testing.T) {
	tests := []struct {
		name string
		row  *pgxmock.Rows
		err  error
	}{
		{name: "unknown", err: pgx.ErrNoRows},
		{name: "expired", row: pgxmock.NewRows([]string{"id", "user_id", "used", "expires_at"}).AddRow("reset-1", "user-1", false, time.Now().Add(-time.Minute))},
		{name: "used", row: pgxmock.NewRows([]string{"id", "user_id", "used", "expires_at"}).AddRow("reset-1", "user-1", true, time.Now().Add(time.Hour))},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock, err := pgxmock.NewPool()
			if err != nil {
				t.Fatalf("pgxmock: %v", err)
			}
			defer mock.Close()

			mock.ExpectBegin()
			expect := mock.ExpectQuery(`SELECT t\.id, t\.user_id, t\.used_at IS NOT NULL, t\.expires_at`).WithArgs("hashed-token")
			if tt.err != nil {
				expect.WillReturnError(tt.err)
			} else {
				expect.WillReturnRows(tt.row)
			}
			mock.ExpectRollback()

			store := storage.NewPGXPasswordResetStore(mock)
			err = store.ResetPasswordTx(context.Background(), "hashed-token", "argon2id-hash")
			if !errors.Is(err, domain.ErrInvalidToken) {
				t.Fatalf("expected ErrInvalidToken, got %v", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet expectations: %v", err)
			}
		})
	}
}

func TestPGXPasswordResetStore_GetPolicySettings(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery(`SELECT min_password_length`).WillReturnRows(defaultPolicyRow())
	store := storage.NewPGXPasswordResetStore(mock)
	policy, err := store.GetPolicySettings(context.Background())
	if err != nil {
		t.Fatalf("GetPolicySettings: %v", err)
	}
	if policy.PasswordResetTokenTTLMinutes != 60 || policy.InviteTokenTTLHours != 72 {
		t.Fatalf("unexpected policy: %+v", policy)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXPasswordResetStore_GetPolicySettingsError(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery(`SELECT min_password_length`).WillReturnError(errors.New("policy failed"))
	store := storage.NewPGXPasswordResetStore(mock)
	_, err = store.GetPolicySettings(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXPasswordResetStore_CreatePasswordResetTokenIneligibleUserDoesNotCreateToken(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)SELECT id.*FROM auth\.users.*FOR UPDATE`).WithArgs("user-1", "user@example.com").WillReturnError(pgx.ErrNoRows)
	mock.ExpectCommit()
	mock.ExpectRollback()

	store := storage.NewPGXPasswordResetStore(mock)
	if err := store.CreatePasswordResetToken(context.Background(), "user-1", "user@example.com", "hashed-reset-token", time.Now().Add(time.Hour), encryptedResetPayload); err != nil {
		t.Fatalf("CreatePasswordResetToken: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXPasswordResetStore_CreatePasswordResetTokenErrors(t *testing.T) {
	expiresAt := time.Now().Add(time.Hour)
	tests := []struct {
		name  string
		setup func(pgxmock.PgxPoolIface)
	}{
		{name: "begin", setup: func(mock pgxmock.PgxPoolIface) { mock.ExpectBegin().WillReturnError(errors.New("begin failed")) }},
		{name: "lock", setup: func(mock pgxmock.PgxPoolIface) {
			mock.ExpectBegin()
			mock.ExpectQuery(`(?s)SELECT id.*FROM auth\.users.*FOR UPDATE`).WithArgs("user-1", "user@example.com").WillReturnError(errors.New("lock failed"))
			mock.ExpectRollback()
		}},
		{name: "supersede", setup: func(mock pgxmock.PgxPoolIface) {
			mock.ExpectBegin()
			expectPasswordResetUserLock(mock)
			mock.ExpectExec(`UPDATE auth\.password_reset_tokens`).WithArgs("user-1").WillReturnError(errors.New("update failed"))
			mock.ExpectRollback()
		}},
		{name: "insert token", setup: func(mock pgxmock.PgxPoolIface) {
			mock.ExpectBegin()
			expectPasswordResetUserLock(mock)
			mock.ExpectExec(`UPDATE auth\.password_reset_tokens`).WithArgs("user-1").WillReturnResult(pgxmock.NewResult("UPDATE", 0))
			mock.ExpectQuery(`INSERT INTO auth\.password_reset_tokens`).WithArgs("user-1", "hashed-reset-token", expiresAt).WillReturnError(errors.New("insert failed"))
			mock.ExpectRollback()
		}},
		{name: "outbox", setup: func(mock pgxmock.PgxPoolIface) {
			mock.ExpectBegin()
			expectPasswordResetUserLock(mock)
			mock.ExpectExec(`UPDATE auth\.password_reset_tokens`).WithArgs("user-1").WillReturnResult(pgxmock.NewResult("UPDATE", 0))
			mock.ExpectQuery(`INSERT INTO auth\.password_reset_tokens`).WithArgs("user-1", "hashed-reset-token", expiresAt).WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow("reset-id-1"))
			mock.ExpectExec(`INSERT INTO auth\.email_outbox`).WithArgs("user@example.com", "reset-id-1", "user-1", encryptedResetPayload).WillReturnError(errors.New("outbox failed"))
			mock.ExpectRollback()
		}},
		{name: "commit", setup: func(mock pgxmock.PgxPoolIface) {
			mock.ExpectBegin()
			expectPasswordResetUserLock(mock)
			mock.ExpectExec(`UPDATE auth\.password_reset_tokens`).WithArgs("user-1").WillReturnResult(pgxmock.NewResult("UPDATE", 0))
			mock.ExpectQuery(`INSERT INTO auth\.password_reset_tokens`).WithArgs("user-1", "hashed-reset-token", expiresAt).WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow("reset-id-1"))
			mock.ExpectExec(`INSERT INTO auth\.email_outbox`).WithArgs("user@example.com", "reset-id-1", "user-1", encryptedResetPayload).WillReturnResult(pgxmock.NewResult("INSERT", 1))
			mock.ExpectCommit().WillReturnError(errors.New("commit failed"))
			mock.ExpectRollback()
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock, err := pgxmock.NewPool()
			if err != nil {
				t.Fatalf("pgxmock: %v", err)
			}
			defer mock.Close()
			tt.setup(mock)
			store := storage.NewPGXPasswordResetStore(mock)
			err = store.CreatePasswordResetToken(context.Background(), "user-1", "user@example.com", "hashed-reset-token", expiresAt, encryptedResetPayload)
			if err == nil {
				t.Fatal("expected error")
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet expectations: %v", err)
			}
		})
	}
}

func TestPGXPasswordResetStore_ResetPasswordTxErrors(t *testing.T) {
	validTokenRows := func() *pgxmock.Rows {
		return pgxmock.NewRows([]string{"id", "user_id", "used", "expires_at"}).AddRow("reset-1", "user-1", false, time.Now().Add(time.Hour))
	}
	tests := []struct {
		name  string
		setup func(pgxmock.PgxPoolIface)
	}{
		{name: "begin", setup: func(mock pgxmock.PgxPoolIface) { mock.ExpectBegin().WillReturnError(errors.New("begin failed")) }},
		{name: "query", setup: func(mock pgxmock.PgxPoolIface) {
			mock.ExpectBegin()
			mock.ExpectQuery(`SELECT t\.id, t\.user_id, t\.used_at IS NOT NULL, t\.expires_at`).WithArgs("hashed-token").WillReturnError(errors.New("query failed"))
			mock.ExpectRollback()
		}},
		{name: "credential update", setup: func(mock pgxmock.PgxPoolIface) {
			mock.ExpectBegin()
			mock.ExpectQuery(`SELECT t\.id, t\.user_id, t\.used_at IS NOT NULL, t\.expires_at`).WithArgs("hashed-token").WillReturnRows(validTokenRows())
			mock.ExpectExec(`UPDATE auth\.user_password_credentials`).WithArgs("argon2id-hash", "user-1").WillReturnError(errors.New("credential failed"))
			mock.ExpectRollback()
		}},
		{name: "credential missing", setup: func(mock pgxmock.PgxPoolIface) {
			mock.ExpectBegin()
			mock.ExpectQuery(`SELECT t\.id, t\.user_id, t\.used_at IS NOT NULL, t\.expires_at`).WithArgs("hashed-token").WillReturnRows(validTokenRows())
			mock.ExpectExec(`UPDATE auth\.user_password_credentials`).WithArgs("argon2id-hash", "user-1").WillReturnResult(pgxmock.NewResult("UPDATE", 0))
			mock.ExpectRollback()
		}},
		{name: "mark used", setup: func(mock pgxmock.PgxPoolIface) {
			mock.ExpectBegin()
			mock.ExpectQuery(`SELECT t\.id, t\.user_id, t\.used_at IS NOT NULL, t\.expires_at`).WithArgs("hashed-token").WillReturnRows(validTokenRows())
			mock.ExpectExec(`UPDATE auth\.user_password_credentials`).WithArgs("argon2id-hash", "user-1").WillReturnResult(pgxmock.NewResult("UPDATE", 1))
			mock.ExpectExec(`UPDATE auth\.password_reset_tokens`).WithArgs("reset-1").WillReturnError(errors.New("mark failed"))
			mock.ExpectRollback()
		}},
		{name: "sessions", setup: func(mock pgxmock.PgxPoolIface) {
			mock.ExpectBegin()
			mock.ExpectQuery(`SELECT t\.id, t\.user_id, t\.used_at IS NOT NULL, t\.expires_at`).WithArgs("hashed-token").WillReturnRows(validTokenRows())
			mock.ExpectExec(`UPDATE auth\.user_password_credentials`).WithArgs("argon2id-hash", "user-1").WillReturnResult(pgxmock.NewResult("UPDATE", 1))
			mock.ExpectExec(`UPDATE auth\.password_reset_tokens`).WithArgs("reset-1").WillReturnResult(pgxmock.NewResult("UPDATE", 1))
			mock.ExpectExec(`UPDATE auth\.user_sessions`).WithArgs("user-1").WillReturnError(errors.New("sessions failed"))
			mock.ExpectRollback()
		}},
		{name: "history", setup: func(mock pgxmock.PgxPoolIface) {
			mock.ExpectBegin()
			mock.ExpectQuery(`SELECT t\.id, t\.user_id, t\.used_at IS NOT NULL, t\.expires_at`).WithArgs("hashed-token").WillReturnRows(validTokenRows())
			mock.ExpectExec(`UPDATE auth\.user_password_credentials`).WithArgs("argon2id-hash", "user-1").WillReturnResult(pgxmock.NewResult("UPDATE", 1))
			mock.ExpectExec(`UPDATE auth\.password_reset_tokens`).WithArgs("reset-1").WillReturnResult(pgxmock.NewResult("UPDATE", 1))
			mock.ExpectExec(`UPDATE auth\.user_sessions`).WithArgs("user-1").WillReturnResult(pgxmock.NewResult("UPDATE", 1))
			mock.ExpectExec(`UPDATE auth\.refresh_token_history`).WithArgs("user-1").WillReturnError(errors.New("history failed"))
			mock.ExpectRollback()
		}},
		{name: "commit", setup: func(mock pgxmock.PgxPoolIface) {
			mock.ExpectBegin()
			mock.ExpectQuery(`SELECT t\.id, t\.user_id, t\.used_at IS NOT NULL, t\.expires_at`).WithArgs("hashed-token").WillReturnRows(validTokenRows())
			mock.ExpectExec(`UPDATE auth\.user_password_credentials`).WithArgs("argon2id-hash", "user-1").WillReturnResult(pgxmock.NewResult("UPDATE", 1))
			mock.ExpectExec(`UPDATE auth\.password_reset_tokens`).WithArgs("reset-1").WillReturnResult(pgxmock.NewResult("UPDATE", 1))
			mock.ExpectExec(`UPDATE auth\.user_sessions`).WithArgs("user-1").WillReturnResult(pgxmock.NewResult("UPDATE", 1))
			mock.ExpectExec(`UPDATE auth\.refresh_token_history`).WithArgs("user-1").WillReturnResult(pgxmock.NewResult("UPDATE", 1))
			mock.ExpectCommit().WillReturnError(errors.New("commit failed"))
			mock.ExpectRollback()
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock, err := pgxmock.NewPool()
			if err != nil {
				t.Fatalf("pgxmock: %v", err)
			}
			defer mock.Close()
			tt.setup(mock)
			store := storage.NewPGXPasswordResetStore(mock)
			err = store.ResetPasswordTx(context.Background(), "hashed-token", "argon2id-hash")
			if err == nil {
				t.Fatal("expected error")
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet expectations: %v", err)
			}
		})
	}
}
