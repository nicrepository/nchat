package storage_test

import (
	"context"
	"errors"
	"testing"
	"time"

	pgxmock "github.com/pashagolub/pgxmock/v2"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

func TestPGXWorkspaceStore_GetDefaultWorkspace_Success(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	now := time.Now()
	mock.ExpectQuery(`SELECT id, slug, name, status, created_at, updated_at`).
		WithArgs("default").
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "slug", "name", "status", "created_at", "updated_at",
		}).AddRow("00000000-0000-0000-0000-000000000001", "default", "NChat", "active", now, now))

	store := storage.NewPGXWorkspaceStore(mock)
	ws, err := store.GetDefaultWorkspace(context.Background())
	if err != nil {
		t.Fatalf("GetDefaultWorkspace: %v", err)
	}
	if ws.Slug != "default" {
		t.Fatalf("expected default, got %q", ws.Slug)
	}
	if ws.Status != domain.WorkspaceStatusActive {
		t.Fatalf("expected active, got %q", ws.Status)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXWorkspaceStore_GetDefaultWorkspace_NotFound(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery(`SELECT id, slug, name, status, created_at, updated_at`).
		WithArgs("default").
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "slug", "name", "status", "created_at", "updated_at",
		}))

	store := storage.NewPGXWorkspaceStore(mock)
	_, err = store.GetDefaultWorkspace(context.Background())
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXWorkspaceStore_GetDefaultWorkspace_DBError(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery(`SELECT id, slug, name, status, created_at, updated_at`).
		WithArgs("default").
		WillReturnError(errors.New("connection lost"))

	store := storage.NewPGXWorkspaceStore(mock)
	_, err = store.GetDefaultWorkspace(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}
