package storage_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	pgxmock "github.com/pashagolub/pgxmock/v2"

	"github.com/nicrepository/nchat/services/auth-service/internal/domain"
	"github.com/nicrepository/nchat/services/auth-service/internal/storage"
)

func expectSessionIdlePolicy(mock pgxmock.PgxPoolIface, minutes int) {
	mock.ExpectQuery(`SELECT session_idle_timeout_minutes`).
		WillReturnRows(pgxmock.NewRows([]string{"session_idle_timeout_minutes"}).AddRow(minutes))
}

func TestPGXSessionStore_RotateRefreshToken_SuccessRecordsHistory(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT s\.id, s\.user_id`).
		WithArgs("old-hash").
		WillReturnRows(pgxmock.NewRows([]string{"id", "user_id"}).AddRow("session-1", "user-1"))
	expectSessionIdlePolicy(mock, 60)
	mock.ExpectExec(`UPDATE auth\.refresh_token_history`).
		WithArgs("session-1", "old-hash").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec(`UPDATE auth\.user_sessions`).
		WithArgs("new-hash", pgxmock.AnyArg(), "session-1", "old-hash").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec(`INSERT INTO auth\.refresh_token_history`).
		WithArgs("session-1", "new-hash").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit()
	mock.ExpectRollback()

	store := storage.NewPGXSessionStore(mock)
	session, err := store.RotateRefreshToken(context.Background(), "old-hash", "new-hash")
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

func TestPGXSessionStore_RotateRefreshToken_ReusedTokenRevokesSessionFamily(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT s\.id, s\.user_id`).
		WithArgs("old-hash").
		WillReturnError(pgx.ErrNoRows)
	mock.ExpectQuery(`SELECT h\.session_id`).
		WithArgs("old-hash").
		WillReturnRows(pgxmock.NewRows([]string{"session_id"}).AddRow("session-1"))
	mock.ExpectExec(`UPDATE auth\.refresh_token_history`).
		WithArgs("old-hash").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec(`UPDATE auth\.user_sessions`).
		WithArgs("session-1").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec(`UPDATE auth\.refresh_token_history`).
		WithArgs("session-1").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectCommit()
	mock.ExpectRollback()

	store := storage.NewPGXSessionStore(mock)
	_, err = store.RotateRefreshToken(context.Background(), "old-hash", "new-hash")
	if !errors.Is(err, domain.ErrInvalidRefreshToken) {
		t.Fatalf("expected ErrInvalidRefreshToken, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXSessionStore_RotateRefreshToken_ActiveTokenAfterFamilyRevocationRejected(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT s\.id, s\.user_id`).
		WithArgs("old-hash").
		WillReturnError(pgx.ErrNoRows)
	mock.ExpectQuery(`SELECT h\.session_id`).
		WithArgs("old-hash").
		WillReturnRows(pgxmock.NewRows([]string{"session_id"}).AddRow("session-1"))
	mock.ExpectExec(`UPDATE auth\.refresh_token_history`).
		WithArgs("old-hash").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec(`UPDATE auth\.user_sessions`).
		WithArgs("session-1").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec(`UPDATE auth\.refresh_token_history`).
		WithArgs("session-1").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectCommit()
	mock.ExpectRollback()

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT s\.id, s\.user_id`).
		WithArgs("active-hash").
		WillReturnError(pgx.ErrNoRows)
	mock.ExpectQuery(`SELECT h\.session_id`).
		WithArgs("active-hash").
		WillReturnError(pgx.ErrNoRows)
	mock.ExpectRollback()

	store := storage.NewPGXSessionStore(mock)
	_, err = store.RotateRefreshToken(context.Background(), "old-hash", "new-hash")
	if !errors.Is(err, domain.ErrInvalidRefreshToken) {
		t.Fatalf("expected ErrInvalidRefreshToken for reused token, got %v", err)
	}
	_, err = store.RotateRefreshToken(context.Background(), "active-hash", "newer-hash")
	if !errors.Is(err, domain.ErrInvalidRefreshToken) {
		t.Fatalf("expected ErrInvalidRefreshToken for active token after revocation, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXSessionStore_RotateRefreshToken_UnknownTokenRejected(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT s\.id, s\.user_id`).
		WithArgs("missing-hash").
		WillReturnError(pgx.ErrNoRows)
	mock.ExpectQuery(`SELECT h\.session_id`).
		WithArgs("missing-hash").
		WillReturnError(pgx.ErrNoRows)
	mock.ExpectRollback()

	store := storage.NewPGXSessionStore(mock)
	_, err = store.RotateRefreshToken(context.Background(), "missing-hash", "new-hash")
	if !errors.Is(err, domain.ErrInvalidRefreshToken) {
		t.Fatalf("expected ErrInvalidRefreshToken, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXSessionStore_LogoutRevokesSessionAndTokenHistory(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT s\.id`).
		WithArgs("token-hash").
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow("session-1"))
	mock.ExpectExec(`UPDATE auth\.user_sessions`).
		WithArgs("session-1").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec(`UPDATE auth\.refresh_token_history`).
		WithArgs("session-1").
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
	mock.ExpectQuery(`SELECT s\.id`).
		WithArgs("unknown-hash").
		WillReturnError(pgx.ErrNoRows)
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
	_, err = store.RotateRefreshToken(context.Background(), "old-hash", "new-hash")
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

func TestPGXSessionStore_RotateRefreshToken_QueryError(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT s\.id, s\.user_id`).
		WithArgs("old-hash").
		WillReturnError(errors.New("db down"))
	mock.ExpectRollback()

	store := storage.NewPGXSessionStore(mock)
	_, err = store.RotateRefreshToken(context.Background(), "old-hash", "new-hash")
	if err == nil {
		t.Fatal("expected error")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXSessionStore_RotateRefreshToken_RotatedHistoryRowsAffectedZero(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT s\.id, s\.user_id`).
		WithArgs("old-hash").
		WillReturnRows(pgxmock.NewRows([]string{"id", "user_id"}).AddRow("session-1", "user-1"))
	expectSessionIdlePolicy(mock, 60)
	mock.ExpectExec(`UPDATE auth\.refresh_token_history`).
		WithArgs("session-1", "old-hash").
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	mock.ExpectRollback()

	store := storage.NewPGXSessionStore(mock)
	_, err = store.RotateRefreshToken(context.Background(), "old-hash", "new-hash")
	if !errors.Is(err, domain.ErrInvalidRefreshToken) {
		t.Fatalf("expected ErrInvalidRefreshToken, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXSessionStore_RotateRefreshToken_InsertHistoryError(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT s\.id, s\.user_id`).
		WithArgs("old-hash").
		WillReturnRows(pgxmock.NewRows([]string{"id", "user_id"}).AddRow("session-1", "user-1"))
	expectSessionIdlePolicy(mock, 60)
	mock.ExpectExec(`UPDATE auth\.refresh_token_history`).
		WithArgs("session-1", "old-hash").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec(`UPDATE auth\.user_sessions`).
		WithArgs("new-hash", pgxmock.AnyArg(), "session-1", "old-hash").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec(`INSERT INTO auth\.refresh_token_history`).
		WithArgs("session-1", "new-hash").
		WillReturnError(errors.New("insert failed"))
	mock.ExpectRollback()

	store := storage.NewPGXSessionStore(mock)
	_, err = store.RotateRefreshToken(context.Background(), "old-hash", "new-hash")
	if err == nil {
		t.Fatal("expected error")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXSessionStore_RotateRefreshToken_ReusedTokenCommitError(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT s\.id, s\.user_id`).
		WithArgs("old-hash").
		WillReturnError(pgx.ErrNoRows)
	mock.ExpectQuery(`SELECT h\.session_id`).
		WithArgs("old-hash").
		WillReturnRows(pgxmock.NewRows([]string{"session_id"}).AddRow("session-1"))
	mock.ExpectExec(`UPDATE auth\.refresh_token_history`).
		WithArgs("old-hash").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec(`UPDATE auth\.user_sessions`).
		WithArgs("session-1").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec(`UPDATE auth\.refresh_token_history`).
		WithArgs("session-1").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectCommit().WillReturnError(errors.New("commit failed"))
	mock.ExpectRollback()

	store := storage.NewPGXSessionStore(mock)
	_, err = store.RotateRefreshToken(context.Background(), "old-hash", "new-hash")
	if err == nil {
		t.Fatal("expected error")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXSessionStore_RevokeRefreshToken_QueryError(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT s\.id`).
		WithArgs("token-hash").
		WillReturnError(errors.New("db down"))
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
	mock.ExpectQuery(`SELECT s\.id`).
		WithArgs("token-hash").
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow("session-1"))
	mock.ExpectExec(`UPDATE auth\.user_sessions`).
		WithArgs("session-1").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec(`UPDATE auth\.refresh_token_history`).
		WithArgs("session-1").
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

func TestPGXSessionStore_RotateRefreshToken_ReusedTokenMarkReusedError(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT s\.id, s\.user_id`).
		WithArgs("old-hash").
		WillReturnError(pgx.ErrNoRows)
	mock.ExpectQuery(`SELECT h\.session_id`).
		WithArgs("old-hash").
		WillReturnRows(pgxmock.NewRows([]string{"session_id"}).AddRow("session-1"))
	mock.ExpectExec(`UPDATE auth\.refresh_token_history`).
		WithArgs("old-hash").
		WillReturnError(errors.New("update failed"))
	mock.ExpectRollback()

	store := storage.NewPGXSessionStore(mock)
	_, err = store.RotateRefreshToken(context.Background(), "old-hash", "new-hash")
	if err == nil {
		t.Fatal("expected error")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXSessionStore_RotateRefreshToken_ReusedTokenRevokeSessionError(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT s\.id, s\.user_id`).
		WithArgs("old-hash").
		WillReturnError(pgx.ErrNoRows)
	mock.ExpectQuery(`SELECT h\.session_id`).
		WithArgs("old-hash").
		WillReturnRows(pgxmock.NewRows([]string{"session_id"}).AddRow("session-1"))
	mock.ExpectExec(`UPDATE auth\.refresh_token_history`).
		WithArgs("old-hash").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec(`UPDATE auth\.user_sessions`).
		WithArgs("session-1").
		WillReturnError(errors.New("revoke failed"))
	mock.ExpectRollback()

	store := storage.NewPGXSessionStore(mock)
	_, err = store.RotateRefreshToken(context.Background(), "old-hash", "new-hash")
	if err == nil {
		t.Fatal("expected error")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXSessionStore_RotateRefreshToken_ReusedTokenRevokeActiveHistoryError(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT s\.id, s\.user_id`).
		WithArgs("old-hash").
		WillReturnError(pgx.ErrNoRows)
	mock.ExpectQuery(`SELECT h\.session_id`).
		WithArgs("old-hash").
		WillReturnRows(pgxmock.NewRows([]string{"session_id"}).AddRow("session-1"))
	mock.ExpectExec(`UPDATE auth\.refresh_token_history`).
		WithArgs("old-hash").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec(`UPDATE auth\.user_sessions`).
		WithArgs("session-1").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec(`UPDATE auth\.refresh_token_history`).
		WithArgs("session-1").
		WillReturnError(errors.New("history revoke failed"))
	mock.ExpectRollback()

	store := storage.NewPGXSessionStore(mock)
	_, err = store.RotateRefreshToken(context.Background(), "old-hash", "new-hash")
	if err == nil {
		t.Fatal("expected error")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXSessionStore_RevokeRefreshToken_HistoryUpdateError(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT s\.id`).
		WithArgs("token-hash").
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow("session-1"))
	mock.ExpectExec(`UPDATE auth\.user_sessions`).
		WithArgs("session-1").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec(`UPDATE auth\.refresh_token_history`).
		WithArgs("session-1").
		WillReturnError(errors.New("history update failed"))
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
