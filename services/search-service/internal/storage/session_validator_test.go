package storage

import (
	"context"
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
