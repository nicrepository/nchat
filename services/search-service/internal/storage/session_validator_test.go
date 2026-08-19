package storage

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/nicrepository/nchat/services/search-service/internal/domain"
	"github.com/pashagolub/pgxmock/v2"
)

func TestSessionValidatorFailsClosedForMissingSession(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	mock.ExpectQuery("WITH active_session").WithArgs("session", "user").WillReturnError(pgx.ErrNoRows)
	err = NewPGXSessionValidator(mock).ValidateActiveSession(context.Background(), "user", "session")
	if err != domain.ErrUnauthorized {
		t.Fatalf("err=%v", err)
	}
}

func TestSessionValidatorAcceptsActiveSession(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	mock.ExpectQuery("WITH active_session").WithArgs("session", "user").WillReturnRows(
		pgxmock.NewRows([]string{"active"}).AddRow(true),
	)
	if err := NewPGXSessionValidator(mock).ValidateActiveSession(context.Background(), "user", "session"); err != nil {
		t.Fatal(err)
	}
}

func TestSessionValidatorReturnsDatabaseFailure(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	dbErr := errors.New("database unavailable")
	mock.ExpectQuery("WITH active_session").WithArgs("session", "user").WillReturnError(dbErr)
	err = NewPGXSessionValidator(mock).ValidateActiveSession(context.Background(), "user", "session")
	if !errors.Is(err, dbErr) || !strings.Contains(err.Error(), "validate active session") {
		t.Fatalf("err=%v", err)
	}
}
