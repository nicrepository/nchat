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

func expectInviteCreateGuards(mock pgxmock.PgxPoolIface) {
	mock.ExpectExec(`SELECT pg_advisory_xact_lock`).
		WithArgs("user@example.com").
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectQuery(`SELECT 1 FROM auth\.users`).
		WithArgs("user@example.com").
		WillReturnError(pgx.ErrNoRows)
	mock.ExpectQuery(`SELECT 1 FROM auth\.user_invites`).
		WithArgs("user@example.com").
		WillReturnError(pgx.ErrNoRows)
}

func TestPGXInviteStore_UserExistsByEmail(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery(`SELECT 1 FROM auth\.users`).WithArgs("user@example.com").WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(1))
	store := storage.NewPGXInviteStore(mock)
	exists, err := store.UserExistsByEmail(context.Background(), "user@example.com")
	if err != nil {
		t.Fatalf("UserExistsByEmail: %v", err)
	}
	if !exists {
		t.Fatal("expected existing user")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXInviteStore_ActiveInviteExistsByEmail(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery(`(?s)SELECT 1 FROM auth\.user_invites.*status = 'pending'.*accepted_at IS NULL.*revoked_at IS NULL.*expires_at > now`).
		WithArgs("user@example.com").
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(1))
	store := storage.NewPGXInviteStore(mock)
	exists, err := store.ActiveInviteExistsByEmail(context.Background(), "user@example.com")
	if err != nil {
		t.Fatalf("ActiveInviteExistsByEmail: %v", err)
	}
	if !exists {
		t.Fatal("expected active invite")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXInviteStore_CreateInviteInsertsInviteAndMetadataOnlyOutbox(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	createdAt := time.Now()
	expiresAt := time.Now().Add(72 * time.Hour)
	mock.ExpectBegin()
	expectInviteCreateGuards(mock)
	mock.ExpectQuery(`INSERT INTO auth\.user_invites`).
		WithArgs("user@example.com", "hashed-invite-token", expiresAt).
		WillReturnRows(pgxmock.NewRows([]string{"id", "email", "created_at"}).AddRow("invite-1", "user@example.com", createdAt))
	mock.ExpectExec(`INSERT INTO auth\.email_outbox`).
		WithArgs("user@example.com", "invite-1").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit()
	mock.ExpectRollback()

	store := storage.NewPGXInviteStore(mock)
	result, err := store.CreateInvite(context.Background(), "user@example.com", "User", "User Full", "hashed-invite-token", expiresAt)
	if err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}
	if result.ID != "invite-1" || result.Email != "user@example.com" || !result.CreatedAt.Equal(createdAt) {
		t.Fatalf("unexpected invite result: %+v", result)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXInviteStore_AcceptInviteTxCreatesUserCredentialAndMarksAccepted(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	createdAt := time.Now()
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT id, email::text, accepted_at IS NOT NULL, revoked_at IS NOT NULL, expires_at, status`).
		WithArgs("hashed-token").
		WillReturnRows(pgxmock.NewRows([]string{"id", "email", "accepted", "revoked", "expires_at", "status"}).AddRow("invite-1", "user@example.com", false, false, time.Now().Add(time.Hour), "pending"))
	mock.ExpectQuery(`INSERT INTO auth\.users`).
		WithArgs("user@example.com", "User", "User Full").
		WillReturnRows(pgxmock.NewRows([]string{"id", "email", "display_name", "full_name", "created_at"}).AddRow("user-1", "user@example.com", "User", "User Full", createdAt))
	mock.ExpectExec(`INSERT INTO auth\.user_password_credentials`).
		WithArgs("user-1", "argon2id-hash").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectExec(`UPDATE auth\.user_invites`).
		WithArgs("invite-1", "user-1").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectCommit()
	mock.ExpectRollback()

	store := storage.NewPGXInviteStore(mock)
	result, err := store.AcceptInviteTx(context.Background(), "hashed-token", "User", "User Full", "argon2id-hash")
	if err != nil {
		t.Fatalf("AcceptInviteTx: %v", err)
	}
	if result.UserID != "user-1" || result.Email != "user@example.com" || result.DisplayName != "User" || result.FullName != "User Full" {
		t.Fatalf("unexpected accept result: %+v", result)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXInviteStore_AcceptInviteTxRejectsUnknownExpiredAcceptedAndRevokedTokens(t *testing.T) {
	tests := []struct {
		name string
		row  *pgxmock.Rows
		err  error
	}{
		{name: "unknown", err: pgx.ErrNoRows},
		{name: "expired", row: pgxmock.NewRows([]string{"id", "email", "accepted", "revoked", "expires_at", "status"}).AddRow("invite-1", "user@example.com", false, false, time.Now().Add(-time.Minute), "pending")},
		{name: "accepted", row: pgxmock.NewRows([]string{"id", "email", "accepted", "revoked", "expires_at", "status"}).AddRow("invite-1", "user@example.com", true, false, time.Now().Add(time.Hour), "pending")},
		{name: "revoked", row: pgxmock.NewRows([]string{"id", "email", "accepted", "revoked", "expires_at", "status"}).AddRow("invite-1", "user@example.com", false, true, time.Now().Add(time.Hour), "pending")},
		{name: "non-pending", row: pgxmock.NewRows([]string{"id", "email", "accepted", "revoked", "expires_at", "status"}).AddRow("invite-1", "user@example.com", false, false, time.Now().Add(time.Hour), "accepted")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock, err := pgxmock.NewPool()
			if err != nil {
				t.Fatalf("pgxmock: %v", err)
			}
			defer mock.Close()

			mock.ExpectBegin()
			expect := mock.ExpectQuery(`SELECT id, email::text, accepted_at IS NOT NULL`).WithArgs("hashed-token")
			if tt.err != nil {
				expect.WillReturnError(tt.err)
			} else {
				expect.WillReturnRows(tt.row)
			}
			mock.ExpectRollback()

			store := storage.NewPGXInviteStore(mock)
			_, err = store.AcceptInviteTx(context.Background(), "hashed-token", "User", "User Full", "argon2id-hash")
			if !errors.Is(err, domain.ErrInvalidToken) {
				t.Fatalf("expected ErrInvalidToken, got %v", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet expectations: %v", err)
			}
		})
	}
}

func TestPGXInviteStore_GetPolicySettings(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery(`SELECT min_password_length`).WillReturnRows(defaultPolicyRow())
	store := storage.NewPGXInviteStore(mock)
	policy, err := store.GetPolicySettings(context.Background())
	if err != nil {
		t.Fatalf("GetPolicySettings: %v", err)
	}
	if policy.InviteTokenTTLHours != 72 {
		t.Fatalf("unexpected policy: %+v", policy)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXInviteStore_GetPolicySettingsError(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery(`SELECT min_password_length`).WillReturnError(errors.New("policy failed"))
	store := storage.NewPGXInviteStore(mock)
	_, err = store.GetPolicySettings(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXInviteStore_UserAndInviteExistenceMissingAndErrors(t *testing.T) {
	t.Run("user missing", func(t *testing.T) {
		mock, err := pgxmock.NewPool()
		if err != nil {
			t.Fatalf("pgxmock: %v", err)
		}
		defer mock.Close()
		mock.ExpectQuery(`SELECT 1 FROM auth\.users`).WithArgs("missing@example.com").WillReturnError(pgx.ErrNoRows)
		store := storage.NewPGXInviteStore(mock)
		exists, err := store.UserExistsByEmail(context.Background(), "missing@example.com")
		if err != nil || exists {
			t.Fatalf("expected missing user, exists=%v err=%v", exists, err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet expectations: %v", err)
		}
	})

	t.Run("user query error", func(t *testing.T) {
		mock, err := pgxmock.NewPool()
		if err != nil {
			t.Fatalf("pgxmock: %v", err)
		}
		defer mock.Close()
		mock.ExpectQuery(`SELECT 1 FROM auth\.users`).WithArgs("user@example.com").WillReturnError(errors.New("query failed"))
		store := storage.NewPGXInviteStore(mock)
		_, err = store.UserExistsByEmail(context.Background(), "user@example.com")
		if err == nil {
			t.Fatal("expected error")
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet expectations: %v", err)
		}
	})

	t.Run("active invite missing", func(t *testing.T) {
		mock, err := pgxmock.NewPool()
		if err != nil {
			t.Fatalf("pgxmock: %v", err)
		}
		defer mock.Close()
		mock.ExpectQuery(`SELECT 1 FROM auth\.user_invites`).WithArgs("missing@example.com").WillReturnError(pgx.ErrNoRows)
		store := storage.NewPGXInviteStore(mock)
		exists, err := store.ActiveInviteExistsByEmail(context.Background(), "missing@example.com")
		if err != nil || exists {
			t.Fatalf("expected missing invite, exists=%v err=%v", exists, err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet expectations: %v", err)
		}
	})

	t.Run("active invite query error", func(t *testing.T) {
		mock, err := pgxmock.NewPool()
		if err != nil {
			t.Fatalf("pgxmock: %v", err)
		}
		defer mock.Close()
		mock.ExpectQuery(`SELECT 1 FROM auth\.user_invites`).WithArgs("user@example.com").WillReturnError(errors.New("query failed"))
		store := storage.NewPGXInviteStore(mock)
		_, err = store.ActiveInviteExistsByEmail(context.Background(), "user@example.com")
		if err == nil {
			t.Fatal("expected error")
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet expectations: %v", err)
		}
	})
}

func TestPGXInviteStore_CreateInviteErrors(t *testing.T) {
	expiresAt := time.Now().Add(time.Hour)
	tests := []struct {
		name  string
		setup func(pgxmock.PgxPoolIface)
	}{
		{name: "begin", setup: func(mock pgxmock.PgxPoolIface) { mock.ExpectBegin().WillReturnError(errors.New("begin failed")) }},
		{name: "lock", setup: func(mock pgxmock.PgxPoolIface) {
			mock.ExpectBegin()
			mock.ExpectExec(`SELECT pg_advisory_xact_lock`).WithArgs("user@example.com").WillReturnError(errors.New("lock failed"))
			mock.ExpectRollback()
		}},
		{name: "duplicate user", setup: func(mock pgxmock.PgxPoolIface) {
			mock.ExpectBegin()
			mock.ExpectExec(`SELECT pg_advisory_xact_lock`).WithArgs("user@example.com").WillReturnResult(pgxmock.NewResult("SELECT", 1))
			mock.ExpectQuery(`SELECT 1 FROM auth\.users`).WithArgs("user@example.com").WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(1))
			mock.ExpectRollback()
		}},
		{name: "pending invite", setup: func(mock pgxmock.PgxPoolIface) {
			mock.ExpectBegin()
			mock.ExpectExec(`SELECT pg_advisory_xact_lock`).WithArgs("user@example.com").WillReturnResult(pgxmock.NewResult("SELECT", 1))
			mock.ExpectQuery(`SELECT 1 FROM auth\.users`).WithArgs("user@example.com").WillReturnError(pgx.ErrNoRows)
			mock.ExpectQuery(`SELECT 1 FROM auth\.user_invites`).WithArgs("user@example.com").WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(1))
			mock.ExpectRollback()
		}},
		{name: "insert", setup: func(mock pgxmock.PgxPoolIface) {
			mock.ExpectBegin()
			expectInviteCreateGuards(mock)
			mock.ExpectQuery(`INSERT INTO auth\.user_invites`).WithArgs("user@example.com", "hashed-invite-token", expiresAt).WillReturnError(errors.New("insert failed"))
			mock.ExpectRollback()
		}},
		{name: "outbox", setup: func(mock pgxmock.PgxPoolIface) {
			mock.ExpectBegin()
			expectInviteCreateGuards(mock)
			mock.ExpectQuery(`INSERT INTO auth\.user_invites`).WithArgs("user@example.com", "hashed-invite-token", expiresAt).WillReturnRows(pgxmock.NewRows([]string{"id", "email", "created_at"}).AddRow("invite-1", "user@example.com", time.Now()))
			mock.ExpectExec(`INSERT INTO auth\.email_outbox`).WithArgs("user@example.com", "invite-1").WillReturnError(errors.New("outbox failed"))
			mock.ExpectRollback()
		}},
		{name: "commit", setup: func(mock pgxmock.PgxPoolIface) {
			mock.ExpectBegin()
			expectInviteCreateGuards(mock)
			mock.ExpectQuery(`INSERT INTO auth\.user_invites`).WithArgs("user@example.com", "hashed-invite-token", expiresAt).WillReturnRows(pgxmock.NewRows([]string{"id", "email", "created_at"}).AddRow("invite-1", "user@example.com", time.Now()))
			mock.ExpectExec(`INSERT INTO auth\.email_outbox`).WithArgs("user@example.com", "invite-1").WillReturnResult(pgxmock.NewResult("INSERT", 1))
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
			store := storage.NewPGXInviteStore(mock)
			_, err = store.CreateInvite(context.Background(), "user@example.com", "User", "User Full", "hashed-invite-token", expiresAt)
			if err == nil {
				t.Fatal("expected error")
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet expectations: %v", err)
			}
		})
	}
}

func TestPGXInviteStore_AcceptInviteTxErrors(t *testing.T) {
	validInviteRows := func() *pgxmock.Rows {
		return pgxmock.NewRows([]string{"id", "email", "accepted", "revoked", "expires_at", "status"}).AddRow("invite-1", "user@example.com", false, false, time.Now().Add(time.Hour), "pending")
	}
	tests := []struct {
		name  string
		setup func(pgxmock.PgxPoolIface)
	}{
		{name: "begin", setup: func(mock pgxmock.PgxPoolIface) { mock.ExpectBegin().WillReturnError(errors.New("begin failed")) }},
		{name: "query", setup: func(mock pgxmock.PgxPoolIface) {
			mock.ExpectBegin()
			mock.ExpectQuery(`SELECT id, email::text, accepted_at IS NOT NULL`).WithArgs("hashed-token").WillReturnError(errors.New("query failed"))
			mock.ExpectRollback()
		}},
		{name: "insert user", setup: func(mock pgxmock.PgxPoolIface) {
			mock.ExpectBegin()
			mock.ExpectQuery(`SELECT id, email::text, accepted_at IS NOT NULL`).WithArgs("hashed-token").WillReturnRows(validInviteRows())
			mock.ExpectQuery(`INSERT INTO auth\.users`).WithArgs("user@example.com", "User", "User Full").WillReturnError(errors.New("insert failed"))
			mock.ExpectRollback()
		}},
		{name: "credential", setup: func(mock pgxmock.PgxPoolIface) {
			mock.ExpectBegin()
			mock.ExpectQuery(`SELECT id, email::text, accepted_at IS NOT NULL`).WithArgs("hashed-token").WillReturnRows(validInviteRows())
			mock.ExpectQuery(`INSERT INTO auth\.users`).WithArgs("user@example.com", "User", "User Full").WillReturnRows(pgxmock.NewRows([]string{"id", "email", "display_name", "full_name", "created_at"}).AddRow("user-1", "user@example.com", "User", "User Full", time.Now()))
			mock.ExpectExec(`INSERT INTO auth\.user_password_credentials`).WithArgs("user-1", "argon2id-hash").WillReturnError(errors.New("credential failed"))
			mock.ExpectRollback()
		}},
		{name: "mark accepted", setup: func(mock pgxmock.PgxPoolIface) {
			mock.ExpectBegin()
			mock.ExpectQuery(`SELECT id, email::text, accepted_at IS NOT NULL`).WithArgs("hashed-token").WillReturnRows(validInviteRows())
			mock.ExpectQuery(`INSERT INTO auth\.users`).WithArgs("user@example.com", "User", "User Full").WillReturnRows(pgxmock.NewRows([]string{"id", "email", "display_name", "full_name", "created_at"}).AddRow("user-1", "user@example.com", "User", "User Full", time.Now()))
			mock.ExpectExec(`INSERT INTO auth\.user_password_credentials`).WithArgs("user-1", "argon2id-hash").WillReturnResult(pgxmock.NewResult("INSERT", 1))
			mock.ExpectExec(`UPDATE auth\.user_invites`).WithArgs("invite-1", "user-1").WillReturnError(errors.New("update failed"))
			mock.ExpectRollback()
		}},
		{name: "mark accepted zero", setup: func(mock pgxmock.PgxPoolIface) {
			mock.ExpectBegin()
			mock.ExpectQuery(`SELECT id, email::text, accepted_at IS NOT NULL`).WithArgs("hashed-token").WillReturnRows(validInviteRows())
			mock.ExpectQuery(`INSERT INTO auth\.users`).WithArgs("user@example.com", "User", "User Full").WillReturnRows(pgxmock.NewRows([]string{"id", "email", "display_name", "full_name", "created_at"}).AddRow("user-1", "user@example.com", "User", "User Full", time.Now()))
			mock.ExpectExec(`INSERT INTO auth\.user_password_credentials`).WithArgs("user-1", "argon2id-hash").WillReturnResult(pgxmock.NewResult("INSERT", 1))
			mock.ExpectExec(`UPDATE auth\.user_invites`).WithArgs("invite-1", "user-1").WillReturnResult(pgxmock.NewResult("UPDATE", 0))
			mock.ExpectRollback()
		}},
		{name: "commit", setup: func(mock pgxmock.PgxPoolIface) {
			mock.ExpectBegin()
			mock.ExpectQuery(`SELECT id, email::text, accepted_at IS NOT NULL`).WithArgs("hashed-token").WillReturnRows(validInviteRows())
			mock.ExpectQuery(`INSERT INTO auth\.users`).WithArgs("user@example.com", "User", "User Full").WillReturnRows(pgxmock.NewRows([]string{"id", "email", "display_name", "full_name", "created_at"}).AddRow("user-1", "user@example.com", "User", "User Full", time.Now()))
			mock.ExpectExec(`INSERT INTO auth\.user_password_credentials`).WithArgs("user-1", "argon2id-hash").WillReturnResult(pgxmock.NewResult("INSERT", 1))
			mock.ExpectExec(`UPDATE auth\.user_invites`).WithArgs("invite-1", "user-1").WillReturnResult(pgxmock.NewResult("UPDATE", 1))
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
			store := storage.NewPGXInviteStore(mock)
			_, err = store.AcceptInviteTx(context.Background(), "hashed-token", "User", "User Full", "argon2id-hash")
			if err == nil {
				t.Fatal("expected error")
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet expectations: %v", err)
			}
		})
	}
}
