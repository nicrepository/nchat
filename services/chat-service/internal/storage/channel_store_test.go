package storage_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
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

func visibleChannelAccessCols() []string {
	return append(channelCols(), "member_channel_id", "member_user_id", "member_role")
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
		WillReturnError(&pgconn.PgError{Code: "23505", ConstraintName: "channels_workspace_slug_unique"})

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

func TestPGXChannelStore_GetChannelByIDInWorkspace_Success(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	now := time.Now()
	mock.ExpectQuery(`SELECT id, workspace_id`).
		WithArgs("ch-1", "ws-1").
		WillReturnRows(pgxmock.NewRows(channelCols()).
			AddRow("ch-1", "ws-1", "", "geral", "Geral", "public", "active", true, 0, "", now, now))

	store := storage.NewPGXChannelStore(mock)
	ch, err := store.GetChannelByIDInWorkspace(context.Background(), "ws-1", "ch-1")
	if err != nil {
		t.Fatalf("GetChannelByIDInWorkspace: %v", err)
	}
	if ch.WorkspaceID != "ws-1" {
		t.Fatalf("expected ws-1, got %q", ch.WorkspaceID)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXChannelStore_GetChannelByIDInWorkspace_NotFound(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery(`SELECT id, workspace_id`).
		WithArgs("ch-other", "ws-1").
		WillReturnRows(pgxmock.NewRows(channelCols()))

	store := storage.NewPGXChannelStore(mock)
	_, err = store.GetChannelByIDInWorkspace(context.Background(), "ws-1", "ch-other")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestPGXChannelStore_GetChannelByIDInWorkspace_DBError(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery(`SELECT id, workspace_id`).
		WithArgs("ch-1", "ws-1").
		WillReturnError(errors.New("connection lost"))

	store := storage.NewPGXChannelStore(mock)
	_, err = store.GetChannelByIDInWorkspace(context.Background(), "ws-1", "ch-1")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestPGXChannelStore_ListVisibleChannelsByUser_ReturnsChannels(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	now := time.Now()
	mock.ExpectQuery(`(?s)FROM chat\.channels c.*JOIN chat\.workspaces w.*w\.status = 'active'.*JOIN chat\.workspace_members wm.*wm\.workspace_id = c\.workspace_id.*wm\.user_id = \$2.*wm\.status = 'active'.*LEFT JOIN chat\.channel_members cm.*cm\.channel_id = c\.id.*cm\.user_id = \$2.*WHERE c\.workspace_id = \$1.*c\.status = 'active'.*c\.is_general = true OR c\.type = 'public' OR cm\.channel_id IS NOT NULL`).
		WithArgs("ws-1", "user-1").
		WillReturnRows(pgxmock.NewRows(visibleChannelAccessCols()).
			AddRow("ch-geral", "ws-1", "", "geral", "Geral", "public", "active", true, 0, "", now, now, "", "", "").
			AddRow("ch-pub", "ws-1", "", "pub", "Public", "public", "active", false, 1, "", now, now, "", "", ""))

	store := storage.NewPGXChannelStore(mock)
	channels, err := store.ListVisibleChannelsByUser(context.Background(), "ws-1", "user-1")
	if err != nil {
		t.Fatalf("ListVisibleChannelsByUser: %v", err)
	}
	if len(channels) != 2 {
		t.Fatalf("expected 2 channels, got %d", len(channels))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXChannelStore_ListVisibleChannelsByUser_NonMemberGetsEmpty(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery(`SELECT c.id, c.workspace_id`).
		WithArgs("ws-1", "non-member").
		WillReturnRows(pgxmock.NewRows(visibleChannelAccessCols()))

	store := storage.NewPGXChannelStore(mock)
	channels, err := store.ListVisibleChannelsByUser(context.Background(), "ws-1", "non-member")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(channels) != 0 {
		t.Fatalf("non-member should get empty list, got %d", len(channels))
	}
}

func TestPGXChannelStore_ListVisibleChannelAccessByUser_ReturnsMembershipWithoutNPlusOne(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	now := time.Now()
	mock.ExpectQuery(`SELECT c.id, c.workspace_id`).
		WithArgs("ws-1", "user-1").
		WillReturnRows(pgxmock.NewRows(visibleChannelAccessCols()).
			AddRow("ch-public", "ws-1", "", "public", "Public", "public", "active", false, 0, "", now, now, "", "", "").
			AddRow("ch-private", "ws-1", "", "private", "Private", "private", "active", false, 1, "", now, now, "ch-private", "user-1", "member"))

	store := storage.NewPGXChannelStore(mock)
	accesses, err := store.ListVisibleChannelAccessByUser(context.Background(), "ws-1", "user-1")
	if err != nil {
		t.Fatalf("ListVisibleChannelAccessByUser: %v", err)
	}
	if len(accesses) != 2 || accesses[0].ChannelMember != nil {
		t.Fatalf("unexpected public access: %+v", accesses)
	}
	if accesses[1].ChannelMember == nil || accesses[1].ChannelMember.UserID != "user-1" {
		t.Fatalf("expected real private membership, got %+v", accesses[1])
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expected one query for all channel memberships: %v", err)
	}
}

func TestPGXChannelStore_ListVisibleChannelsByUser_DBError(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery(`SELECT c.id, c.workspace_id`).
		WithArgs("ws-1", "user-1").
		WillReturnError(errors.New("db unavailable"))

	store := storage.NewPGXChannelStore(mock)
	_, err = store.ListVisibleChannelsByUser(context.Background(), "ws-1", "user-1")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestPGXChannelStore_CreateChannel_GeneralChannelExists(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery(`INSERT INTO chat.channels`).
		WithArgs("ws-1", pgxmock.AnyArg(), "geral2", "Geral 2", "public", true, 0, pgxmock.AnyArg()).
		WillReturnError(&pgconn.PgError{
			Code:           "23505",
			ConstraintName: "idx_channels_one_general_per_workspace",
		})

	store := storage.NewPGXChannelStore(mock)
	_, err = store.CreateChannel(context.Background(), storage.CreateChannelInput{
		WorkspaceID: "ws-1", Slug: "geral2", DisplayName: "Geral 2",
		Type: domain.ChannelTypePublic, IsGeneral: true,
	})
	if !errors.Is(err, domain.ErrGeneralChannelExists) {
		t.Fatalf("expected ErrGeneralChannelExists, got %v", err)
	}
}

func TestPGXChannelStore_CreateChannel_UnknownUniqueViolationIsNotDuplicateSlug(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery(`INSERT INTO chat.channels`).
		WithArgs("ws-1", pgxmock.AnyArg(), "team", "Team", "public", false, 0, pgxmock.AnyArg()).
		WillReturnError(&pgconn.PgError{Code: "23505", ConstraintName: "unexpected_unique_constraint"})

	store := storage.NewPGXChannelStore(mock)
	_, err = store.CreateChannel(context.Background(), storage.CreateChannelInput{
		WorkspaceID: "ws-1", Slug: "team", DisplayName: "Team", Type: domain.ChannelTypePublic,
	})
	if err == nil {
		t.Fatal("expected unique violation error")
	}
	if errors.Is(err, domain.ErrDuplicateSlug) || errors.Is(err, domain.ErrGeneralChannelExists) {
		t.Fatalf("unknown unique constraint must not map to a domain duplicate error: %v", err)
	}
}

func TestPGXChannelStore_CreateCategory_DBError(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery(`INSERT INTO chat.channel_categories`).
		WithArgs("ws-1", "General", 0).
		WillReturnError(errors.New("db error"))

	store := storage.NewPGXChannelStore(mock)
	_, err = store.CreateCategory(context.Background(), storage.CreateCategoryInput{
		WorkspaceID: "ws-1", Name: "General", Position: 0,
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestPGXChannelStore_CreateChannel_DBError(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery(`INSERT INTO chat.channels`).
		WithArgs("ws-1", pgxmock.AnyArg(), "geral", "Geral", "public", false, 0, pgxmock.AnyArg()).
		WillReturnError(errors.New("db error"))

	store := storage.NewPGXChannelStore(mock)
	_, err = store.CreateChannel(context.Background(), storage.CreateChannelInput{
		WorkspaceID: "ws-1", Slug: "geral", DisplayName: "Geral", Type: domain.ChannelTypePublic,
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestPGXChannelStore_GetChannelByID_DBError(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery(`SELECT id, workspace_id`).
		WithArgs("ch-1").
		WillReturnError(errors.New("db error"))

	store := storage.NewPGXChannelStore(mock)
	_, err = store.GetChannelByID(context.Background(), "ch-1")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestPGXChannelStore_ListChannelsByWorkspace_DBError(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery(`SELECT id, workspace_id`).
		WithArgs("ws-1").
		WillReturnError(errors.New("db error"))

	store := storage.NewPGXChannelStore(mock)
	_, err = store.ListChannelsByWorkspace(context.Background(), "ws-1")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestPGXChannelStore_GetCategoryByIDInWorkspace_Success(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	now := time.Now()
	mock.ExpectQuery(`SELECT id, workspace_id, name, position`).
		WithArgs("ws-1", "cat-1").
		WillReturnRows(pgxmock.NewRows([]string{"id", "workspace_id", "name", "position", "created_at", "updated_at"}).
			AddRow("cat-1", "ws-1", "Team", 2, now, now))

	store := storage.NewPGXChannelStore(mock)
	category, err := store.GetCategoryByIDInWorkspace(context.Background(), "ws-1", "cat-1")
	if err != nil {
		t.Fatalf("GetCategoryByIDInWorkspace: %v", err)
	}
	if category.ID != "cat-1" || category.WorkspaceID != "ws-1" {
		t.Fatalf("unexpected category: %+v", category)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXChannelStore_GetCategoryByIDInWorkspace_NotFound(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery(`SELECT id, workspace_id, name, position`).
		WithArgs("ws-1", "cat-other").
		WillReturnRows(pgxmock.NewRows([]string{"id", "workspace_id", "name", "position", "created_at", "updated_at"}))

	store := storage.NewPGXChannelStore(mock)
	_, err = store.GetCategoryByIDInWorkspace(context.Background(), "ws-1", "cat-other")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestPGXChannelStore_GetCategoryByIDInWorkspace_DBError(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery(`SELECT id, workspace_id, name, position`).
		WithArgs("ws-1", "cat-1").
		WillReturnError(errors.New("db unavailable"))

	store := storage.NewPGXChannelStore(mock)
	_, err = store.GetCategoryByIDInWorkspace(context.Background(), "ws-1", "cat-1")
	if err == nil {
		t.Fatal("expected db error")
	}
}

func TestPGXChannelStore_GetVisibleChannelByID_SQLVisibility(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	now := time.Now()
	mock.ExpectQuery(`(?s)FROM chat\.channels c.*JOIN chat\.workspaces w.*w\.status = 'active'.*JOIN chat\.workspace_members wm.*wm\.workspace_id = c\.workspace_id.*wm\.user_id = \$3.*wm\.status = 'active'.*LEFT JOIN chat\.channel_members cm.*cm\.channel_id = c\.id.*cm\.user_id = \$3.*WHERE c\.workspace_id = \$1.*c\.id = \$2.*c\.status = 'active'.*c\.is_general = true OR c\.type = 'public' OR cm\.channel_id IS NOT NULL`).
		WithArgs("ws-1", "ch-1", "user-1").
		WillReturnRows(pgxmock.NewRows(channelCols()).
			AddRow("ch-1", "ws-1", "", "private", "Private", "private", "active", false, 0, "", now, now))

	store := storage.NewPGXChannelStore(mock)
	ch, err := store.GetVisibleChannelByID(context.Background(), "ws-1", "ch-1", "user-1")
	if err != nil {
		t.Fatalf("GetVisibleChannelByID: %v", err)
	}
	if ch.ID != "ch-1" {
		t.Fatalf("unexpected channel: %+v", ch)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXChannelStore_GetVisibleChannelByID_DBError(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery(`FROM chat\.channels c`).
		WithArgs("ws-1", "ch-1", "user-1").
		WillReturnError(errors.New("db unavailable"))

	store := storage.NewPGXChannelStore(mock)
	_, err = store.GetVisibleChannelByID(context.Background(), "ws-1", "ch-1", "user-1")
	if err == nil {
		t.Fatal("expected db error")
	}
}

func TestPGXChannelStore_GetVisibleChannelBySlug_NotFoundForHiddenChannel(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery(`(?s)FROM chat\.channels c.*c\.slug = \$2.*cm\.channel_id IS NOT NULL`).
		WithArgs("ws-1", "private", "user-1").
		WillReturnRows(pgxmock.NewRows(channelCols()))

	store := storage.NewPGXChannelStore(mock)
	_, err = store.GetVisibleChannelBySlug(context.Background(), "ws-1", "private", "user-1")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestPGXChannelStore_UpdateChannel_WorkspaceBound(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	now := time.Now()
	mock.ExpectQuery(`(?s)UPDATE chat\.channels.*WHERE workspace_id = \$1.*id = \$2.*status = 'active'.*is_general = false`).
		WithArgs("ws-1", "ch-1", pgxmock.AnyArg(), "team-updates", "Team Updates", "public", 20).
		WillReturnRows(pgxmock.NewRows(channelCols()).
			AddRow("ch-1", "ws-1", "cat-1", "team-updates", "Team Updates", "public", "active", false, 20, "owner-1", now, now))

	store := storage.NewPGXChannelStore(mock)
	ch, err := store.UpdateChannel(context.Background(), storage.UpdateChannelInput{
		WorkspaceID: "ws-1", ChannelID: "ch-1", CategoryID: "cat-1", Slug: "team-updates",
		DisplayName: "Team Updates", Type: domain.ChannelTypePublic, Position: 20,
	})
	if err != nil {
		t.Fatalf("UpdateChannel: %v", err)
	}
	if ch.Slug != "team-updates" || ch.WorkspaceID != "ws-1" {
		t.Fatalf("unexpected channel: %+v", ch)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXChannelStore_UpdateChannel_PublicToPrivateEnsuresMembershipInTransaction(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	now := time.Now()
	mock.ExpectBegin()
	mock.ExpectQuery(`UPDATE chat\.channels`).
		WithArgs("ws-1", "ch-1", pgxmock.AnyArg(), "team", "Team", "private", 0).
		WillReturnRows(pgxmock.NewRows(channelCols()).
			AddRow("ch-1", "ws-1", "", "team", "Team", "private", "active", false, 0, "owner-1", now, now))
	mock.ExpectExec(`INSERT INTO chat\.channel_members`).
		WithArgs("ch-1", "owner-1", "member").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit()

	store := storage.NewPGXChannelStore(mock)
	ch, err := store.UpdateChannel(context.Background(), storage.UpdateChannelInput{
		WorkspaceID: "ws-1", ChannelID: "ch-1", Slug: "team", DisplayName: "Team",
		Type: domain.ChannelTypePrivate, EnsureMemberUserID: "owner-1",
	})
	if err != nil {
		t.Fatalf("UpdateChannel: %v", err)
	}
	if ch.Type != domain.ChannelTypePrivate {
		t.Fatalf("unexpected channel: %+v", ch)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXChannelStore_UpdateChannel_PublicToPrivateRollsBackWhenMembershipFails(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	now := time.Now()
	mock.ExpectBegin()
	mock.ExpectQuery(`UPDATE chat\.channels`).
		WithArgs("ws-1", "ch-1", pgxmock.AnyArg(), "team", "Team", "private", 0).
		WillReturnRows(pgxmock.NewRows(channelCols()).
			AddRow("ch-1", "ws-1", "", "team", "Team", "private", "active", false, 0, "owner-1", now, now))
	mock.ExpectExec(`INSERT INTO chat\.channel_members`).
		WithArgs("ch-1", "owner-1", "member").
		WillReturnError(errors.New("membership insert failed"))
	mock.ExpectRollback()

	store := storage.NewPGXChannelStore(mock)
	_, err = store.UpdateChannel(context.Background(), storage.UpdateChannelInput{
		WorkspaceID: "ws-1", ChannelID: "ch-1", Slug: "team", DisplayName: "Team",
		Type: domain.ChannelTypePrivate, EnsureMemberUserID: "owner-1",
	})
	if err == nil {
		t.Fatal("expected membership insert error")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXChannelStore_UpdateChannel_BeginErrorWhenEnsuringMembership(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectBegin().WillReturnError(errors.New("begin failed"))

	store := storage.NewPGXChannelStore(mock)
	_, err = store.UpdateChannel(context.Background(), storage.UpdateChannelInput{
		WorkspaceID: "ws-1", ChannelID: "ch-1", Slug: "team", DisplayName: "Team",
		Type: domain.ChannelTypePrivate, EnsureMemberUserID: "owner-1",
	})
	if err == nil {
		t.Fatal("expected begin error")
	}
}

func TestPGXChannelStore_UpdateChannel_DuplicateSlug(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery(`UPDATE chat\.channels`).
		WithArgs("ws-1", "ch-1", pgxmock.AnyArg(), "existing", "Existing", "public", 0).
		WillReturnError(&pgconn.PgError{Code: "23505", ConstraintName: "channels_workspace_slug_unique"})

	store := storage.NewPGXChannelStore(mock)
	_, err = store.UpdateChannel(context.Background(), storage.UpdateChannelInput{
		WorkspaceID: "ws-1", ChannelID: "ch-1", Slug: "existing", DisplayName: "Existing", Type: domain.ChannelTypePublic,
	})
	if !errors.Is(err, domain.ErrDuplicateSlug) {
		t.Fatalf("expected ErrDuplicateSlug, got %v", err)
	}
}

func TestPGXChannelStore_UpdateChannel_NotFound(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery(`UPDATE chat\.channels`).
		WithArgs("ws-1", "missing", pgxmock.AnyArg(), "team", "Team", "public", 0).
		WillReturnRows(pgxmock.NewRows(channelCols()))

	store := storage.NewPGXChannelStore(mock)
	_, err = store.UpdateChannel(context.Background(), storage.UpdateChannelInput{
		WorkspaceID: "ws-1", ChannelID: "missing", Slug: "team", DisplayName: "Team", Type: domain.ChannelTypePublic,
	})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestPGXChannelStore_UpdateChannel_CrossWorkspaceCategoryMapsInvalidInput(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery(`UPDATE chat\.channels`).
		WithArgs("ws-1", "ch-1", pgxmock.AnyArg(), "team", "Team", "public", 0).
		WillReturnError(&pgconn.PgError{Code: "23503", ConstraintName: "channels_workspace_category_fk"})

	store := storage.NewPGXChannelStore(mock)
	_, err = store.UpdateChannel(context.Background(), storage.UpdateChannelInput{
		WorkspaceID: "ws-1", ChannelID: "ch-1", CategoryID: "cat-other", Slug: "team", DisplayName: "Team", Type: domain.ChannelTypePublic,
	})
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput, got %v", err)
	}
}

func TestPGXChannelStore_ArchiveChannel_WorkspaceBoundNoHardDelete(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	now := time.Now()
	mock.ExpectQuery(`(?s)UPDATE chat\.channels.*SET status = 'archived'.*WHERE workspace_id = \$1.*id = \$2.*status = 'active'.*is_general = false`).
		WithArgs("ws-1", "ch-1").
		WillReturnRows(pgxmock.NewRows(channelCols()).
			AddRow("ch-1", "ws-1", "", "team", "Team", "public", "archived", false, 0, "owner-1", now, now))

	store := storage.NewPGXChannelStore(mock)
	ch, err := store.ArchiveChannel(context.Background(), "ws-1", "ch-1")
	if err != nil {
		t.Fatalf("ArchiveChannel: %v", err)
	}
	if ch.Status != domain.ChannelStatusArchived {
		t.Fatalf("expected archived channel, got %+v", ch)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXChannelStore_ArchiveChannel_NotFound(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery(`UPDATE chat\.channels`).
		WithArgs("ws-1", "missing").
		WillReturnRows(pgxmock.NewRows(channelCols()))

	store := storage.NewPGXChannelStore(mock)
	_, err = store.ArchiveChannel(context.Background(), "ws-1", "missing")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestPGXChannelStore_ArchiveChannel_DBError(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery(`UPDATE chat\.channels`).
		WithArgs("ws-1", "ch-1").
		WillReturnError(errors.New("db unavailable"))

	store := storage.NewPGXChannelStore(mock)
	_, err = store.ArchiveChannel(context.Background(), "ws-1", "ch-1")
	if err == nil {
		t.Fatal("expected db error")
	}
}

func TestPGXChannelStore_ArchiveChannel_ConstraintErrorMapsDomain(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery(`UPDATE chat\.channels`).
		WithArgs("ws-1", "ch-1").
		WillReturnError(&pgconn.PgError{Code: "23505", ConstraintName: "channels_workspace_slug_unique"})

	store := storage.NewPGXChannelStore(mock)
	_, err = store.ArchiveChannel(context.Background(), "ws-1", "ch-1")
	if !errors.Is(err, domain.ErrDuplicateSlug) {
		t.Fatalf("expected mapped domain error, got %v", err)
	}
}

// authorizedContextArgs matches the eight placeholders of the authorized-context
// INSERT; the individual values are asserted where they matter.
func authorizedContextArgs() []any {
	args := make([]any, 8)
	for i := range args {
		args[i] = pgxmock.AnyArg()
	}
	return args
}

// ── CreateChannelForActiveMember ──────────────────────────────────────────────
//
// These cover the transactional shape against a mocked pool, deterministically
// and without a database. The authorization semantics themselves — that the
// locked SELECT and the INSERT cannot be interleaved by a concurrent revocation
// — cannot be mocked and are proved against real PostgreSQL in
// channel_store_postgres_test.go.

func TestPGXChannelStore_CreateChannelForActiveMember_PublicCommitsWithoutMembership(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	now := time.Now()
	mock.ExpectBegin()
	mock.ExpectQuery(`WITH authorized_context`).
		WithArgs("ws-1", pgxmock.AnyArg(), "infra", "Infra", "public", false, 0, "user-1").
		WillReturnRows(pgxmock.NewRows(channelCols()).
			AddRow("ch-1", "ws-1", "", "infra", "Infra", "public", "active", false, 0, "user-1", now, now))
	mock.ExpectCommit()

	ch, err := storage.NewPGXChannelStore(mock).CreateChannelForActiveMember(context.Background(), storage.CreateChannelInput{
		WorkspaceID: "ws-1", Slug: "infra", DisplayName: "Infra",
		Type: domain.ChannelTypePublic, CreatedBy: "user-1",
	})
	if err != nil {
		t.Fatalf("CreateChannelForActiveMember: %v", err)
	}
	if ch.ID != "ch-1" || ch.CreatedBy != "user-1" {
		t.Fatalf("unexpected channel: %+v", ch)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXChannelStore_CreateChannelForActiveMember_PrivateSeedsCreatorInSameTx(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	now := time.Now()
	mock.ExpectBegin()
	mock.ExpectQuery(`WITH authorized_context`).
		WithArgs(authorizedContextArgs()...).
		WillReturnRows(pgxmock.NewRows(channelCols()).
			AddRow("ch-1", "ws-1", "", "infra", "Infra", "private", "active", false, 0, "user-1", now, now))
	// The membership uses the created_by the database returned, not the input.
	mock.ExpectExec(`INSERT INTO chat.channel_members`).
		WithArgs("ch-1", "user-1", "member").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit()

	if _, err := storage.NewPGXChannelStore(mock).CreateChannelForActiveMember(context.Background(), storage.CreateChannelInput{
		WorkspaceID: "ws-1", Slug: "infra", DisplayName: "Infra",
		Type: domain.ChannelTypePrivate, CreatedBy: "user-1",
		EnsureCreatorMemberRole: domain.ChannelRoleMember,
	}); err != nil {
		t.Fatalf("CreateChannelForActiveMember: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// No row from the authorized context is a denial, not an internal error, and the
// transaction is rolled back rather than left open.
func TestPGXChannelStore_CreateChannelForActiveMember_NoAuthorizedContextIsForbidden(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectBegin()
	mock.ExpectQuery(`WITH authorized_context`).WithArgs(authorizedContextArgs()...).WillReturnError(pgx.ErrNoRows)
	mock.ExpectRollback()

	_, err = storage.NewPGXChannelStore(mock).CreateChannelForActiveMember(context.Background(), storage.CreateChannelInput{
		WorkspaceID: "ws-1", Slug: "infra", DisplayName: "Infra",
		Type: domain.ChannelTypePublic, CreatedBy: "user-1",
	})
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("error = %v, want ErrForbidden", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// An actorless call cannot be authorized by any row, so it is refused before a
// connection is even taken.
func TestPGXChannelStore_CreateChannelForActiveMember_RequiresAnActor(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	_, err = storage.NewPGXChannelStore(mock).CreateChannelForActiveMember(context.Background(), storage.CreateChannelInput{
		WorkspaceID: "ws-1", Slug: "infra", DisplayName: "Infra", Type: domain.ChannelTypePublic,
	})
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("error = %v, want ErrForbidden", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXChannelStore_CreateChannelForActiveMember_MapsConstraintViolations(t *testing.T) {
	for _, test := range []struct {
		name       string
		constraint string
		want       error
	}{
		{name: "duplicate slug", constraint: "channels_workspace_slug_unique", want: domain.ErrDuplicateSlug},
		{name: "second general", constraint: "idx_channels_one_general_per_workspace", want: domain.ErrGeneralChannelExists},
	} {
		t.Run(test.name, func(t *testing.T) {
			mock, err := pgxmock.NewPool()
			if err != nil {
				t.Fatalf("pgxmock: %v", err)
			}
			defer mock.Close()

			mock.ExpectBegin()
			mock.ExpectQuery(`WITH authorized_context`).
				WithArgs(authorizedContextArgs()...).
				WillReturnError(&pgconn.PgError{Code: "23505", ConstraintName: test.constraint})
			mock.ExpectRollback()

			_, err = storage.NewPGXChannelStore(mock).CreateChannelForActiveMember(context.Background(), storage.CreateChannelInput{
				WorkspaceID: "ws-1", Slug: "infra", DisplayName: "Infra",
				Type: domain.ChannelTypePublic, CreatedBy: "user-1",
			})
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet expectations: %v", err)
			}
		})
	}
}

// A failing secondary insert must not leave the channel behind: the whole
// transaction is rolled back.
func TestPGXChannelStore_CreateChannelForActiveMember_MemberInsertFailureRollsBack(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	now := time.Now()
	mock.ExpectBegin()
	mock.ExpectQuery(`WITH authorized_context`).
		WithArgs(authorizedContextArgs()...).
		WillReturnRows(pgxmock.NewRows(channelCols()).
			AddRow("ch-1", "ws-1", "", "infra", "Infra", "private", "active", false, 0, "user-1", now, now))
	mock.ExpectExec(`INSERT INTO chat.channel_members`).
		WithArgs("ch-1", "user-1", "member").
		WillReturnError(errors.New("boom"))
	mock.ExpectRollback()

	if _, err := storage.NewPGXChannelStore(mock).CreateChannelForActiveMember(context.Background(), storage.CreateChannelInput{
		WorkspaceID: "ws-1", Slug: "infra", DisplayName: "Infra",
		Type: domain.ChannelTypePrivate, CreatedBy: "user-1",
		EnsureCreatorMemberRole: domain.ChannelRoleMember,
	}); err == nil {
		t.Fatal("expected the member insert failure to surface")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXChannelStore_CreateChannelForActiveMember_BeginAndCommitFailures(t *testing.T) {
	t.Run("begin", func(t *testing.T) {
		mock, err := pgxmock.NewPool()
		if err != nil {
			t.Fatalf("pgxmock: %v", err)
		}
		defer mock.Close()
		mock.ExpectBegin().WillReturnError(errors.New("no connection"))

		if _, err := storage.NewPGXChannelStore(mock).CreateChannelForActiveMember(context.Background(), storage.CreateChannelInput{
			WorkspaceID: "ws-1", Slug: "infra", DisplayName: "Infra",
			Type: domain.ChannelTypePublic, CreatedBy: "user-1",
		}); err == nil {
			t.Fatal("expected the begin failure to surface")
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet expectations: %v", err)
		}
	})

	t.Run("commit", func(t *testing.T) {
		mock, err := pgxmock.NewPool()
		if err != nil {
			t.Fatalf("pgxmock: %v", err)
		}
		defer mock.Close()
		now := time.Now()
		mock.ExpectBegin()
		mock.ExpectQuery(`WITH authorized_context`).
			WithArgs(authorizedContextArgs()...).
			WillReturnRows(pgxmock.NewRows(channelCols()).
				AddRow("ch-1", "ws-1", "", "infra", "Infra", "public", "active", false, 0, "user-1", now, now))
		mock.ExpectCommit().WillReturnError(errors.New("commit lost"))
		mock.ExpectRollback()

		if _, err := storage.NewPGXChannelStore(mock).CreateChannelForActiveMember(context.Background(), storage.CreateChannelInput{
			WorkspaceID: "ws-1", Slug: "infra", DisplayName: "Infra",
			Type: domain.ChannelTypePublic, CreatedBy: "user-1",
		}); err == nil {
			t.Fatal("expected the commit failure to surface")
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet expectations: %v", err)
		}
	})
}

// An unexpected database error is not laundered into a denial.
func TestPGXChannelStore_CreateChannelForActiveMember_UnknownErrorIsNotForbidden(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectBegin()
	mock.ExpectQuery(`WITH authorized_context`).WithArgs(authorizedContextArgs()...).WillReturnError(errors.New("connection reset"))
	mock.ExpectRollback()

	_, err = storage.NewPGXChannelStore(mock).CreateChannelForActiveMember(context.Background(), storage.CreateChannelInput{
		WorkspaceID: "ws-1", Slug: "infra", DisplayName: "Infra",
		Type: domain.ChannelTypePublic, CreatedBy: "user-1",
	})
	if err == nil || errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("error = %v, want a non-forbidden failure", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}
