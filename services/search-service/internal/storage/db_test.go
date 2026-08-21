package storage

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pashagolub/pgxmock/v2"
)

func TestOpenDBRejectsInvalidDSN(t *testing.T) {
	if _, err := OpenDB(context.Background(), "://bad", 1); err == nil {
		t.Fatal("invalid DSN accepted")
	}
}

func TestOpenDBCoversCreationPingAndSuccess(t *testing.T) {
	originalNew := newPool
	defer func() { newPool = originalNew }()
	cfg, err := pgxpool.ParseConfig("postgres://localhost/nchat")
	if err != nil {
		t.Fatal(err)
	}
	creationErr := errors.New("create failed")
	newPool = func(context.Context, *pgxpool.Config) (Pool, error) { return nil, creationErr }
	if _, err := OpenDB(context.Background(), cfg.ConnString(), 1); !errors.Is(err, creationErr) {
		t.Fatalf("creation err=%v", err)
	}
	mock, err := pgxmock.NewPool(pgxmock.MonitorPingsOption(true))
	if err != nil {
		t.Fatal(err)
	}
	mock.ExpectPing().WillReturnError(errors.New("ping failed"))
	mock.ExpectClose()
	newPool = func(context.Context, *pgxpool.Config) (Pool, error) { return mock, nil }
	if _, err := OpenDB(context.Background(), cfg.ConnString(), 1); err == nil {
		t.Fatal("ping failure accepted")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
	success, err := pgxmock.NewPool(pgxmock.MonitorPingsOption(true))
	if err != nil {
		t.Fatal(err)
	}
	success.ExpectPing()
	newPool = func(context.Context, *pgxpool.Config) (Pool, error) { return success, nil }
	pool, err := OpenDB(context.Background(), cfg.ConnString(), 1)
	if err != nil || pool == nil {
		t.Fatalf("pool=%v err=%v", pool, err)
	}
	success.Close()
}
