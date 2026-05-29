package storage_test

import (
	"context"
	"errors"
	"testing"

	pgxmock "github.com/pashagolub/pgxmock/v2"

	"github.com/nicrepository/nchat/services/notification-service/internal/storage"
)

func TestPGXOutboxStore_ClaimBatch_Success(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT id::text, kind, to_email, subject, payload::text, attempts`).
		WithArgs(5, 10).
		WillReturnRows(pgxmock.NewRows([]string{"id", "kind", "to_email", "subject", "payload", "attempts"}).
			AddRow("row-1", "password_reset", "user@example.com", "Reset your password", "ciphertext", 1))
	mock.ExpectExec(`UPDATE auth\.email_outbox`).
		WithArgs(30, "row-1").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectCommit()
	mock.ExpectRollback()

	store := storage.NewPGXOutboxStore(mock)
	rows, err := store.ClaimBatch(context.Background(), 5, 30, 10)
	if err != nil {
		t.Fatalf("ClaimBatch returned error: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if rows[0].ID != "row-1" || rows[0].Kind != "password_reset" || rows[0].ToEmail != "user@example.com" {
		t.Fatalf("unexpected claimed row: %+v", rows[0])
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXOutboxStore_ClaimBatch_BeginError(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectBegin().WillReturnError(errors.New("begin failed"))

	store := storage.NewPGXOutboxStore(mock)
	_, err = store.ClaimBatch(context.Background(), 5, 30, 10)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXOutboxStore_ClaimBatch_QueryError(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT id::text, kind, to_email, subject, payload::text, attempts`).
		WithArgs(5, 10).
		WillReturnError(errors.New("query failed"))
	mock.ExpectRollback()

	store := storage.NewPGXOutboxStore(mock)
	_, err = store.ClaimBatch(context.Background(), 5, 30, 10)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXOutboxStore_ClaimBatch_ExecError(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT id::text, kind, to_email, subject, payload::text, attempts`).
		WithArgs(5, 10).
		WillReturnRows(pgxmock.NewRows([]string{"id", "kind", "to_email", "subject", "payload", "attempts"}).
			AddRow("row-1", "invite", "invitee@example.com", "Join", "ciphertext", 0))
	mock.ExpectExec(`UPDATE auth\.email_outbox`).
		WithArgs(30, "row-1").
		WillReturnError(errors.New("exec failed"))
	mock.ExpectRollback()

	store := storage.NewPGXOutboxStore(mock)
	_, err = store.ClaimBatch(context.Background(), 5, 30, 10)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXOutboxStore_ClaimBatch_CommitError(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT id::text, kind, to_email, subject, payload::text, attempts`).
		WithArgs(5, 10).
		WillReturnRows(pgxmock.NewRows([]string{"id", "kind", "to_email", "subject", "payload", "attempts"}))
	mock.ExpectCommit().WillReturnError(errors.New("commit failed"))
	mock.ExpectRollback()

	store := storage.NewPGXOutboxStore(mock)
	_, err = store.ClaimBatch(context.Background(), 5, 30, 10)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXOutboxStore_FinaliseSuccess_Success(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectExec(`UPDATE auth\.email_outbox`).
		WithArgs("row-1").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	store := storage.NewPGXOutboxStore(mock)
	if err := store.FinaliseSuccess(context.Background(), "row-1"); err != nil {
		t.Fatalf("FinaliseSuccess returned error: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXOutboxStore_FinaliseSuccess_Error(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectExec(`UPDATE auth\.email_outbox`).
		WithArgs("row-1").
		WillReturnError(errors.New("exec failed"))

	store := storage.NewPGXOutboxStore(mock)
	if err := store.FinaliseSuccess(context.Background(), "row-1"); err == nil {
		t.Fatal("expected error, got nil")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXOutboxStore_FinaliseFailure_Success(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectExec(`UPDATE auth\.email_outbox`).
		WithArgs(2, "send_error", 60, 5, "row-1").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	store := storage.NewPGXOutboxStore(mock)
	if err := store.FinaliseFailure(context.Background(), "row-1", 2, "send_error", 60, 5); err != nil {
		t.Fatalf("FinaliseFailure returned error: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXOutboxStore_FinaliseFailure_Error(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectExec(`UPDATE auth\.email_outbox`).
		WithArgs(2, "send_error", 60, 5, "row-1").
		WillReturnError(errors.New("exec failed"))

	store := storage.NewPGXOutboxStore(mock)
	if err := store.FinaliseFailure(context.Background(), "row-1", 2, "send_error", 60, 5); err == nil {
		t.Fatal("expected error, got nil")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}
