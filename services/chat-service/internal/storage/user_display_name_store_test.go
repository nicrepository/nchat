package storage_test

import (
	"context"
	"errors"
	"testing"

	pgxmock "github.com/pashagolub/pgxmock/v2"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

const testDNUserID = "user-dn-abc"

func TestPGXUserDisplayNameStore_ReturnsName(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery(`SELECT COALESCE\(display_name, ''\) FROM auth\.users WHERE id = \$1`).
		WithArgs(testDNUserID).
		WillReturnRows(pgxmock.NewRows([]string{"display_name"}).AddRow("Ana Souza"))

	store := storage.NewPGXUserDisplayNameStore(mock)
	name, err := store.GetDisplayName(context.Background(), testDNUserID)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if name != "Ana Souza" {
		t.Fatalf("name = %q, want %q", name, "Ana Souza")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXUserDisplayNameStore_NoRows_ReturnsErrNotFound(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery(`SELECT COALESCE\(display_name, ''\) FROM auth\.users WHERE id = \$1`).
		WithArgs(testDNUserID).
		WillReturnRows(pgxmock.NewRows([]string{"display_name"})) // no rows

	store := storage.NewPGXUserDisplayNameStore(mock)
	_, err = store.GetDisplayName(context.Background(), testDNUserID)
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXUserDisplayNameStore_DBError_Propagates(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	dbErr := errors.New("connection timeout")
	mock.ExpectQuery(`SELECT COALESCE\(display_name, ''\) FROM auth\.users WHERE id = \$1`).
		WithArgs(testDNUserID).
		WillReturnError(dbErr)

	store := storage.NewPGXUserDisplayNameStore(mock)
	_, err = store.GetDisplayName(context.Background(), testDNUserID)
	if err == nil {
		t.Fatal("expected error for DB failure, got nil")
	}
	if errors.Is(err, domain.ErrNotFound) {
		t.Fatal("DB error must not be collapsed into ErrNotFound")
	}
	if !errors.Is(err, dbErr) {
		t.Fatalf("expected wrapped dbErr, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}
