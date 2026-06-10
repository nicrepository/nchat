package storage_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	pgxmock "github.com/pashagolub/pgxmock/v2"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

func channelCols() []string {
	return []string{
		"id", "workspace_id", "category_id", "slug", "display_name",
		"type", "status", "is_general", "position", "created_by",
		"created_at", "updated_at",
	}
}

func TestPGXChannelStore_CreateChannel_Success(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	now := time.Now()
	mock.ExpectQuery(`INSERT INTO chat.channels`).
		WithArgs("ws-1", pgxmock.AnyArg(), "geral", "Geral", "public", true, 0, pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(channelCols()).
			AddRow("ch-1", "ws-1", "", "geral", "Geral", "public", "active", true, 0, "", now, now))

	store := storage.NewPGXChannelStore(mock)
	ch, err := store.CreateChannel(context.Background(), storage.CreateChannelInput{
		WorkspaceID: "ws-1", Slug: "geral", DisplayName: "Geral",
		Type: domain.ChannelTypePublic, IsGeneral: true,
	})
	if err != nil {
		t.Fatalf("CreateChannel: %v", err)
	}
	if ch.Slug != "geral" || !ch.IsGeneral {
		t.Fatalf("unexpected channel: %+v", ch)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXChannelStore_CreateChannel_DuplicateSlug_ReturnsErrDuplicateSlug(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery(`INSERT INTO chat.channels`).
		WithArgs("ws-1", pgxmock.AnyArg(), "geral", "Geral", "public", false, 0, pgxmock.AnyArg()).
		WillReturnError(&pgconn.PgError{Code: "23505"})

	store := storage.NewPGXChannelStore(mock)
	_, err = store.CreateChannel(context.Background(), storage.CreateChannelInput{
		WorkspaceID: "ws-1", Slug: "geral", DisplayName: "Geral", Type: domain.ChannelTypePublic,
	})
	if !errors.Is(err, domain.ErrDuplicateSlug) {
		t.Fatalf("expected ErrDuplicateSlug, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXChannelStore_GetChannelByID_Success(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	now := time.Now()
	mock.ExpectQuery(`SELECT id, workspace_id`).
		WithArgs("ch-1").
		WillReturnRows(pgxmock.NewRows(channelCols()).
			AddRow("ch-1", "ws-1", "", "geral", "Geral", "public", "active", true, 0, "", now, now))

	store := storage.NewPGXChannelStore(mock)
	ch, err := store.GetChannelByID(context.Background(), "ch-1")
	if err != nil {
		t.Fatalf("GetChannelByID: %v", err)
	}
	if ch.ID != "ch-1" {
		t.Fatalf("expected ch-1, got %q", ch.ID)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXChannelStore_GetChannelByID_NotFound(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery(`SELECT id, workspace_id`).
		WithArgs("no-such").
		WillReturnRows(pgxmock.NewRows(channelCols()))

	store := storage.NewPGXChannelStore(mock)
	_, err = store.GetChannelByID(context.Background(), "no-such")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestPGXChannelStore_ListChannelsByWorkspace_Success(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	now := time.Now()
	mock.ExpectQuery(`SELECT id, workspace_id`).
		WithArgs("ws-1").
		WillReturnRows(pgxmock.NewRows(channelCols()).
			AddRow("ch-1", "ws-1", "", "geral", "Geral", "public", "active", true, 0, "", now, now).
			AddRow("ch-2", "ws-1", "", "random", "Random", "public", "active", false, 1, "", now, now))

	store := storage.NewPGXChannelStore(mock)
	channels, err := store.ListChannelsByWorkspace(context.Background(), "ws-1")
	if err != nil {
		t.Fatalf("ListChannelsByWorkspace: %v", err)
	}
	if len(channels) != 2 {
		t.Fatalf("expected 2 channels, got %d", len(channels))
	}
	if !channels[0].IsGeneral {
		t.Fatal("first channel should be #geral")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXChannelStore_ListChannelsByWorkspace_Empty(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery(`SELECT id, workspace_id`).
		WithArgs("ws-1").
		WillReturnRows(pgxmock.NewRows(channelCols()))

	store := storage.NewPGXChannelStore(mock)
	channels, err := store.ListChannelsByWorkspace(context.Background(), "ws-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(channels) != 0 {
		t.Fatalf("expected 0 channels, got %d", len(channels))
	}
}

func TestPGXChannelStore_CreateCategory_Success(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	now := time.Now()
	mock.ExpectQuery(`INSERT INTO chat.channel_categories`).
		WithArgs("ws-1", "General", 0).
		WillReturnRows(pgxmock.NewRows([]string{"id", "workspace_id", "name", "position", "created_at", "updated_at"}).
			AddRow("cat-1", "ws-1", "General", 0, now, now))

	store := storage.NewPGXChannelStore(mock)
	cat, err := store.CreateCategory(context.Background(), storage.CreateCategoryInput{
		WorkspaceID: "ws-1", Name: "General", Position: 0,
	})
	if err != nil {
		t.Fatalf("CreateCategory: %v", err)
	}
	if cat.Name != "General" {
		t.Fatalf("expected General, got %q", cat.Name)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}
