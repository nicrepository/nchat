package storage_test

import (
	"context"
	"errors"
	"testing"

	pgxmock "github.com/pashagolub/pgxmock/v2"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

const (
	testSVUserID    = "user-sv-abc"
	testSVSessionID = "b1e2c3d4-0000-0000-0000-000000000001"
)

func TestPGXSessionValidator_ActiveSession_ReturnsNil(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery(`(?s)WITH active_session AS.*FROM auth\.user_sessions AS s.*JOIN auth\.users AS u.*SELECT true FROM active_session`).
		WithArgs(testSVSessionID, testSVUserID).
		WillReturnRows(pgxmock.NewRows([]string{"active"}).AddRow(true))

	sv := storage.NewPGXSessionValidator(mock)
	if err := sv.ValidateActiveSession(context.Background(), testSVUserID, testSVSessionID); err != nil {
		t.Fatalf("expected nil for active session, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXSessionValidator_NoRows_ReturnsErrInvalidToken(t *testing.T) {
	// Covers: revoked session, expired session, deleted/suspended user.
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery(`(?s)WITH active_session AS.*FROM auth\.user_sessions AS s.*SELECT true FROM active_session`).
		WithArgs(testSVSessionID, testSVUserID).
		WillReturnRows(pgxmock.NewRows([]string{"active"})) // no rows

	sv := storage.NewPGXSessionValidator(mock)
	err = sv.ValidateActiveSession(context.Background(), testSVUserID, testSVSessionID)
	if !errors.Is(err, domain.ErrInvalidToken) {
		t.Fatalf("expected ErrInvalidToken for no-rows, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXSessionValidator_DBError_Propagates(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	dbErr := errors.New("connection timeout")
	mock.ExpectQuery(`(?s)WITH active_session AS.*FROM auth\.user_sessions AS s.*SELECT true FROM active_session`).
		WithArgs(testSVSessionID, testSVUserID).
		WillReturnError(dbErr)

	sv := storage.NewPGXSessionValidator(mock)
	err = sv.ValidateActiveSession(context.Background(), testSVUserID, testSVSessionID)
	if err == nil {
		t.Fatal("expected error for DB failure, got nil")
	}
	if errors.Is(err, domain.ErrInvalidToken) {
		t.Fatal("DB error must not be collapsed into ErrInvalidToken")
	}
	if !errors.Is(err, dbErr) {
		t.Fatalf("expected wrapped dbErr, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}
