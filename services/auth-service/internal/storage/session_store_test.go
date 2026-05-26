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

func TestPGXSessionStore_RotateRefreshToken_Success(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	expiresAt := time.Now().Add(30 * 24 * time.Hour)
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT s\.id, s\.user_id`).
		WithArgs("old-hash").
		WillReturnRows(pgxmock.NewRows([]string{"id", "user_id"}).AddRow("session-1", "user-1"))
	mock.ExpectExec(`UPDATE auth\.user_sessions`).
		WithArgs("new-hash", expiresAt, "session-1", "old-hash").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectCommit()
	mock.ExpectRollback()

	store := storage.NewPGXSessionStore(mock)
	session, err := store.RotateRefreshToken(context.Background(), "old-hash", "new-hash", expiresAt)
	if err != nil {
		t.Fatalf("RotateRefreshToken: %v", err)
	}
	if session.ID != "session-1" || session.UserID != "user-1" {
		t.Fatalf("unexpected session: %+v", session)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXSessionStore_RotateRefreshToken_NotFoundRejected(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT s\.id, s\.user_id`).
		WithArgs("missing-hash").
		WillReturnError(pgx.ErrNoRows)
	mock.ExpectRollback()

	store := storage.NewPGXSessionStore(mock)
	_, err = store.RotateRefreshToken(context.Background(), "missing-hash", "new-hash", time.Now())
	if !errors.Is(err, domain.ErrInvalidRefreshToken) {
		t.Fatalf("expected ErrInvalidRefreshToken, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXSessionStore_LogoutRevokesToken(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE auth\.user_sessions`).
		WithArgs("token-hash").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectCommit()
	mock.ExpectRollback()

	store := storage.NewPGXSessionStore(mock)
	if err := store.RevokeRefreshToken(context.Background(), "token-hash"); err != nil {
		t.Fatalf("RevokeRefreshToken: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXSessionStore_LogoutRejectsUnknownToken(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE auth\.user_sessions`).
		WithArgs("unknown-hash").
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	mock.ExpectRollback()

	store := storage.NewPGXSessionStore(mock)
	err = store.RevokeRefreshToken(context.Background(), "unknown-hash")
	if !errors.Is(err, domain.ErrInvalidRefreshToken) {
		t.Fatalf("expected ErrInvalidRefreshToken, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXSessionStore_RotateRefreshToken_BeginError(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectBegin().WillReturnError(errors.New("begin failed"))

	store := storage.NewPGXSessionStore(mock)
	_, err = store.RotateRefreshToken(context.Background(), "old-hash", "new-hash", time.Now())
	if err == nil {
		t.Fatal("expected error")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXSessionStore_RotateRefreshToken_SelectError(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT s\.id, s\.user_id`).
		WithArgs("old-hash").
		WillReturnError(errors.New("select failed"))
	mock.ExpectRollback()

	store := storage.NewPGXSessionStore(mock)
	_, err = store.RotateRefreshToken(context.Background(), "old-hash", "new-hash", time.Now())
	if err == nil {
		t.Fatal("expected error")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXSessionStore_RotateRefreshToken_UpdateError(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	expiresAt := time.Now().Add(30 * 24 * time.Hour)
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT s\.id, s\.user_id`).
		WithArgs("old-hash").
		WillReturnRows(pgxmock.NewRows([]string{"id", "user_id"}).AddRow("session-1", "user-1"))
	mock.ExpectExec(`UPDATE auth\.user_sessions`).
		WithArgs("new-hash", expiresAt, "session-1", "old-hash").
		WillReturnError(errors.New("update failed"))
	mock.ExpectRollback()

	store := storage.NewPGXSessionStore(mock)
	_, err = store.RotateRefreshToken(context.Background(), "old-hash", "new-hash", expiresAt)
	if err == nil {
		t.Fatal("expected error")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXSessionStore_RotateRefreshToken_UpdateZeroRows(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	expiresAt := time.Now().Add(30 * 24 * time.Hour)
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT s\.id, s\.user_id`).
		WithArgs("old-hash").
		WillReturnRows(pgxmock.NewRows([]string{"id", "user_id"}).AddRow("session-1", "user-1"))
	mock.ExpectExec(`UPDATE auth\.user_sessions`).
		WithArgs("new-hash", expiresAt, "session-1", "old-hash").
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	mock.ExpectRollback()

	store := storage.NewPGXSessionStore(mock)
	_, err = store.RotateRefreshToken(context.Background(), "old-hash", "new-hash", expiresAt)
	if !errors.Is(err, domain.ErrInvalidRefreshToken) {
		t.Fatalf("expected ErrInvalidRefreshToken, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXSessionStore_RotateRefreshToken_CommitError(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	expiresAt := time.Now().Add(30 * 24 * time.Hour)
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT s\.id, s\.user_id`).
		WithArgs("old-hash").
		WillReturnRows(pgxmock.NewRows([]string{"id", "user_id"}).AddRow("session-1", "user-1"))
	mock.ExpectExec(`UPDATE auth\.user_sessions`).
		WithArgs("new-hash", expiresAt, "session-1", "old-hash").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectCommit().WillReturnError(errors.New("commit failed"))
	mock.ExpectRollback()

	store := storage.NewPGXSessionStore(mock)
	_, err = store.RotateRefreshToken(context.Background(), "old-hash", "new-hash", expiresAt)
	if err == nil {
		t.Fatal("expected error")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXSessionStore_RevokeRefreshToken_BeginError(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectBegin().WillReturnError(errors.New("begin failed"))

	store := storage.NewPGXSessionStore(mock)
	err = store.RevokeRefreshToken(context.Background(), "token-hash")
	if err == nil {
		t.Fatal("expected error")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXSessionStore_RevokeRefreshToken_ExecError(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE auth\.user_sessions`).
		WithArgs("token-hash").
		WillReturnError(errors.New("exec failed"))
	mock.ExpectRollback()

	store := storage.NewPGXSessionStore(mock)
	err = store.RevokeRefreshToken(context.Background(), "token-hash")
	if err == nil {
		t.Fatal("expected error")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXSessionStore_RevokeRefreshToken_CommitError(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE auth\.user_sessions`).
		WithArgs("token-hash").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectCommit().WillReturnError(errors.New("commit failed"))
	mock.ExpectRollback()

	store := storage.NewPGXSessionStore(mock)
	err = store.RevokeRefreshToken(context.Background(), "token-hash")
	if err == nil {
		t.Fatal("expected error")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}
