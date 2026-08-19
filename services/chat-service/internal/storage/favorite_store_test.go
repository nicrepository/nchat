package storage_test

import (
	"context"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	pgxmock "github.com/pashagolub/pgxmock/v2"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

// addFavoriteSQL asserts the single-statement shape: access CTE (workspace
// membership + public/private channel or DM membership), active-message
// filter, insert guarded by the CTE, and idempotent conflict handling.
const addFavoriteSQL = `(?s)WITH authorized AS.*chat\.workspace_members wm.*chat\.dm_members dm.*m\.status = 'active'.*chat\.channel_visible_to_user\(c\.id, \$2::uuid\).*dm\.user_id IS NOT NULL.*INSERT INTO chat\.message_favorites.*ON CONFLICT \(user_id, message_id\) DO NOTHING.*SELECT EXISTS`

func favoriteInput() storage.AddFavoriteInput {
	return storage.AddFavoriteInput{WorkspaceID: "ws-1", UserID: "user-1", MessageID: "msg-1"}
}

func TestPGXFavoriteStore_AddFavorite_AuthorizedSucceeds(t *testing.T) {
	mock := newMock(t)
	mock.ExpectQuery(addFavoriteSQL).WithArgs("ws-1", "user-1", "msg-1").
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(true))

	if err := storage.NewPGXFavoriteStore(mock).AddFavorite(context.Background(), favoriteInput()); err != nil {
		t.Fatalf("AddFavorite: %v", err)
	}
	checkExpectations(t, mock)
}

func TestPGXFavoriteStore_AddFavorite_UnauthorizedReturnsErrNotFound(t *testing.T) {
	mock := newMock(t)
	// Missing, deleted, and unauthorized messages all yield authorized=false —
	// a single non-enumerating answer.
	mock.ExpectQuery(addFavoriteSQL).WithArgs("ws-1", "user-1", "msg-1").
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(false))

	err := storage.NewPGXFavoriteStore(mock).AddFavorite(context.Background(), favoriteInput())
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	checkExpectations(t, mock)
}

func TestPGXFavoriteStore_AddFavorite_QueryErrorWrapped(t *testing.T) {
	mock := newMock(t)
	mock.ExpectQuery(addFavoriteSQL).WillReturnError(errors.New("db down"))

	if err := storage.NewPGXFavoriteStore(mock).AddFavorite(context.Background(), favoriteInput()); err == nil {
		t.Fatal("expected error")
	}
}

func TestPGXFavoriteStore_RemoveFavorite_DeletesOwnRowOnly(t *testing.T) {
	mock := newMock(t)
	mock.ExpectExec(`DELETE FROM chat\.message_favorites\s+WHERE user_id = \$1 AND message_id = \$2`).
		WithArgs("user-1", "msg-1").
		WillReturnResult(pgxmock.NewResult("DELETE", 0)) // idempotent: 0 rows is success

	if err := storage.NewPGXFavoriteStore(mock).RemoveFavorite(context.Background(), "user-1", "msg-1"); err != nil {
		t.Fatalf("RemoveFavorite: %v", err)
	}
	checkExpectations(t, mock)
}

func TestPGXFavoriteStore_RemoveFavorite_ExecErrorWrapped(t *testing.T) {
	mock := newMock(t)
	mock.ExpectExec(`DELETE FROM chat\.message_favorites`).WillReturnError(errors.New("db down"))

	if err := storage.NewPGXFavoriteStore(mock).RemoveFavorite(context.Background(), "user-1", "msg-1"); err == nil {
		t.Fatal("expected error")
	}
}

// ── ListFavorites ────────────────────────────────────────────────────────────

func favoriteCols() []string {
	return append(listMessageCols(), "favorited_at")
}

func favoriteRow(id, channelID, dmID string, now, favoritedAt time.Time) []any {
	return append(listMessageRow(id, "ws-1", channelID, dmID, now), favoritedAt)
}

// listFavoritesSQL asserts current-access filtering at list time and that no
// m.status filter drops deleted messages (RF-14: they stay, with placeholder).
const listFavoritesSQL = `(?s)FROM chat\.message_favorites f.*JOIN chat\.messages m.*chat\.workspace_members wm.*WHERE f\.user_id = \$2.*chat\.channel_visible_to_user\(c\.id, \$2::uuid\).*dm\.user_id IS NOT NULL.*ORDER BY f\.created_at DESC, f\.message_id DESC`

func TestPGXFavoriteStore_ListFavorites_ReturnsNewestFirstWithDeletedRetained(t *testing.T) {
	mock := newMock(t)
	now := time.Now()
	favoritedAt := now.Add(-time.Hour)
	mock.ExpectQuery(listFavoritesSQL).
		WithArgs("ws-1", "user-1", 51).
		WillReturnRows(pgxmock.NewRows(favoriteCols()).
			AddRow(favoriteRow("msg-1", "ch-1", "", now, favoritedAt)...).
			AddRow(favoriteRow("msg-2", "", "dm-1", now, favoritedAt.Add(-time.Minute))...))

	result, err := storage.NewPGXFavoriteStore(mock).ListFavorites(context.Background(), storage.ListFavoritesInput{
		WorkspaceID: "ws-1", UserID: "user-1",
	})
	if err != nil {
		t.Fatalf("ListFavorites: %v", err)
	}
	if len(result.Favorites) != 2 || result.NextCursor != nil {
		t.Fatalf("expected 2 favorites without next page, got %+v", result)
	}
	if result.Favorites[0].Message.ID != "msg-1" || !result.Favorites[0].FavoritedAt.Equal(favoritedAt) {
		t.Fatalf("unexpected first favorite: %+v", result.Favorites[0])
	}
	if result.Favorites[1].Message.DMConversationID != "dm-1" {
		t.Fatalf("expected DM favorite, got %+v", result.Favorites[1])
	}
	checkExpectations(t, mock)
}

func TestPGXFavoriteStore_ListFavorites_SQLDoesNotFilterDeletedMessages(t *testing.T) {
	mock := newMock(t)
	// RF-14 regression guard: the listing must not filter on m.status —
	// deleted messages stay in the favorites list with the placeholder.
	mock.ExpectQuery(`FROM chat\.message_favorites f`).
		WithArgs("ws-1", "user-1", 51).
		WillReturnRows(pgxmock.NewRows(favoriteCols()))

	var capturedSQL string
	pool := &sqlCapturingPool{Pool: mock, captured: &capturedSQL}
	if _, err := storage.NewPGXFavoriteStore(pool).ListFavorites(context.Background(), storage.ListFavoritesInput{
		WorkspaceID: "ws-1", UserID: "user-1",
	}); err != nil {
		t.Fatalf("ListFavorites: %v", err)
	}
	// "m.status" appears as a scanned column and wm./dm. aliases also end in
	// "m"; only a comparison on the message alias itself would filter.
	if regexp.MustCompile(`\bm\.status\s*=`).MatchString(capturedSQL) {
		t.Fatalf("favorites listing must not filter on m.status (RF-14):\n%s", capturedSQL)
	}
	checkExpectations(t, mock)
}

// sqlCapturingPool records the SQL passed to Query for content assertions.
type sqlCapturingPool struct {
	storage.Pool
	captured *string
}

func (p *sqlCapturingPool) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	*p.captured = sql
	return p.Pool.Query(ctx, sql, args...)
}

func TestPGXFavoriteStore_ListFavorites_HasNextPage_SetsCursorFromFavoritedAt(t *testing.T) {
	mock := newMock(t)
	now := time.Now()
	oldest := now.Add(-3 * time.Hour)
	rows := pgxmock.NewRows(favoriteCols())
	for _, fav := range []struct {
		id string
		at time.Time
	}{{"msg-1", now}, {"msg-2", now.Add(-time.Hour)}, {"msg-3", oldest}} {
		rows.AddRow(favoriteRow(fav.id, "ch-1", "", now, fav.at)...)
	}
	mock.ExpectQuery(listFavoritesSQL).
		WithArgs("ws-1", "user-1", 3). // limit 2 → fetches 3 to detect next page
		WillReturnRows(rows)

	result, err := storage.NewPGXFavoriteStore(mock).ListFavorites(context.Background(), storage.ListFavoritesInput{
		WorkspaceID: "ws-1", UserID: "user-1", Limit: 2,
	})
	if err != nil {
		t.Fatalf("ListFavorites: %v", err)
	}
	if len(result.Favorites) != 2 {
		t.Fatalf("expected trimmed page of 2, got %d", len(result.Favorites))
	}
	if result.NextCursor == nil || result.NextCursor.ID != "msg-2" {
		t.Fatalf("expected cursor at msg-2, got %+v", result.NextCursor)
	}
	if !result.NextCursor.CreatedAt.Equal(now.Add(-time.Hour)) {
		t.Fatalf("cursor must use favorited_at, got %+v", result.NextCursor)
	}
	checkExpectations(t, mock)
}

func TestPGXFavoriteStore_ListFavorites_WithBeforeCursorPassesKeysetArgs(t *testing.T) {
	mock := newMock(t)
	now := time.Now()
	cursor := storage.MessageCursor{CreatedAt: now.Add(-time.Hour), ID: "msg-9"}
	mock.ExpectQuery(`(?s)\(f\.created_at, f\.message_id\) < \(\$3, \$4::uuid\)`).
		WithArgs("ws-1", "user-1", cursor.CreatedAt, cursor.ID, 51).
		WillReturnRows(pgxmock.NewRows(favoriteCols()))

	result, err := storage.NewPGXFavoriteStore(mock).ListFavorites(context.Background(), storage.ListFavoritesInput{
		WorkspaceID: "ws-1", UserID: "user-1", BeforeCursor: &cursor,
	})
	if err != nil {
		t.Fatalf("ListFavorites with cursor: %v", err)
	}
	if len(result.Favorites) != 0 || result.NextCursor != nil {
		t.Fatalf("expected empty page, got %+v", result)
	}
	checkExpectations(t, mock)
}

func TestPGXFavoriteStore_ListFavorites_WithEditedAt_ScansTimestamps(t *testing.T) {
	mock := newMock(t)
	now := time.Now()
	editedAt := now.Add(-time.Minute)
	deletedAt := now.Add(-30 * time.Second)
	row := []any{
		"msg-e", "ws-1", "ch-1", "", "user-1",
		"user", "edited", "v1", "deleted",
		"", "", "",
		&editedAt, 1, &deletedAt,
		now, now,
		"",
		"Test User", "test@example.com", true,
		now.Add(-time.Hour),
	}
	mock.ExpectQuery(listFavoritesSQL).
		WithArgs("ws-1", "user-1", 51).
		WillReturnRows(pgxmock.NewRows(favoriteCols()).AddRow(row...))

	result, err := storage.NewPGXFavoriteStore(mock).ListFavorites(context.Background(), storage.ListFavoritesInput{
		WorkspaceID: "ws-1", UserID: "user-1",
	})
	if err != nil {
		t.Fatalf("ListFavorites: %v", err)
	}
	msg := result.Favorites[0].Message
	if msg.EditedAt.IsZero() || msg.DeletedAt.IsZero() || msg.Status != domain.MessageStatusDeleted {
		t.Fatalf("expected scanned timestamps and deleted status, got %+v", msg)
	}
	checkExpectations(t, mock)
}

func TestPGXFavoriteStore_ListFavorites_QueryErrorWrapped(t *testing.T) {
	mock := newMock(t)
	mock.ExpectQuery(listFavoritesSQL).WillReturnError(errors.New("db down"))

	if _, err := storage.NewPGXFavoriteStore(mock).ListFavorites(context.Background(), storage.ListFavoritesInput{
		WorkspaceID: "ws-1", UserID: "user-1",
	}); err == nil {
		t.Fatal("expected error")
	}
}

func TestPGXFavoriteStore_ListFavorites_ScanErrorWrapped(t *testing.T) {
	mock := newMock(t)
	rows := pgxmock.NewRows(favoriteCols()).
		AddRow(favoriteRow("msg-1", "ch-1", "", time.Now(), time.Now())...).
		RowError(0, errors.New("broken row"))
	mock.ExpectQuery(listFavoritesSQL).
		WithArgs("ws-1", "user-1", 51).
		WillReturnRows(rows)

	if _, err := storage.NewPGXFavoriteStore(mock).ListFavorites(context.Background(), storage.ListFavoritesInput{
		WorkspaceID: "ws-1", UserID: "user-1",
	}); err == nil {
		t.Fatal("expected error")
	}
}
