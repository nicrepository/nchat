package storage_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	pgxmock "github.com/pashagolub/pgxmock/v2"

	"github.com/nicrepository/nchat/services/auth-service/internal/domain"
	"github.com/nicrepository/nchat/services/auth-service/internal/storage"
)

func TestPGXUserStore_SetAvatarURL_ReturnsPreviousValue(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	// The CTE returns the OLD avatar_url; the arg is the new value.
	mock.ExpectQuery(`WITH target AS.*UPDATE auth\.users`).
		WithArgs("user-1", "/api/auth/avatars/new.png").
		WillReturnRows(pgxmock.NewRows([]string{"avatar_url"}).AddRow(strptrAvatar("/api/auth/avatars/old.png")))

	prev, err := storage.NewPGXUserStore(mock).SetAvatarURL(context.Background(), "user-1", "/api/auth/avatars/new.png")
	if err != nil {
		t.Fatalf("set: %v", err)
	}
	if prev != "/api/auth/avatars/old.png" {
		t.Fatalf("expected previous value, got %q", prev)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet: %v", err)
	}
}

func TestPGXUserStore_SetAvatarURL_NoPreviousReturnsEmpty(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery(`WITH target AS.*UPDATE auth\.users`).
		WithArgs("user-1", "/api/auth/avatars/new.png").
		WillReturnRows(pgxmock.NewRows([]string{"avatar_url"}).AddRow(nil))

	prev, err := storage.NewPGXUserStore(mock).SetAvatarURL(context.Background(), "user-1", "/api/auth/avatars/new.png")
	if err != nil || prev != "" {
		t.Fatalf("expected empty previous, got %q err=%v", prev, err)
	}
}

func TestPGXUserStore_SetAvatarURL_InactiveReturnsNotFound(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	// No active row → the CTE is empty → the UPDATE returns no rows.
	mock.ExpectQuery(`WITH target AS.*UPDATE auth\.users`).
		WithArgs("user-1", "/api/auth/avatars/new.png").
		WillReturnError(pgx.ErrNoRows)

	if _, err := storage.NewPGXUserStore(mock).SetAvatarURL(context.Background(), "user-1", "/api/auth/avatars/new.png"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestPGXUserStore_ClearAvatarURL_NullsColumnAndReturnsPrevious(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	// Clearing passes a nil second argument (SQL NULL).
	mock.ExpectQuery(`WITH target AS.*UPDATE auth\.users`).
		WithArgs("user-1", nil).
		WillReturnRows(pgxmock.NewRows([]string{"avatar_url"}).AddRow(strptrAvatar("/api/auth/avatars/old.png")))

	prev, err := storage.NewPGXUserStore(mock).ClearAvatarURL(context.Background(), "user-1")
	if err != nil || prev != "/api/auth/avatars/old.png" {
		t.Fatalf("expected old value, got %q err=%v", prev, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet: %v", err)
	}
}

func TestPGXUserStore_SwapAvatarURL_PropagatesDBError(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery(`WITH target AS.*UPDATE auth\.users`).
		WithArgs("user-1", nil).
		WillReturnError(errors.New("connection reset"))

	if _, err := storage.NewPGXUserStore(mock).ClearAvatarURL(context.Background(), "user-1"); err == nil || errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected a wrapped db error, got %v", err)
	}
}

func strptrAvatar(s string) *string { return &s }

func TestPGXUserStore_GetSelfProfile_ReturnsAvatar(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery(`SELECT id, display_name, avatar_url\s+FROM auth\.users\s+WHERE id = \$1\s+AND status = 'active'\s+AND deleted_at IS NULL`).
		WithArgs("user-1").
		WillReturnRows(pgxmock.NewRows([]string{"id", "display_name", "avatar_url"}).
			AddRow("user-1", "Ana", strptrAvatar("/api/auth/avatars/x.png")))

	got, err := storage.NewPGXUserStore(mock).GetSelfProfile(context.Background(), "user-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.ID != "user-1" || got.DisplayName != "Ana" || got.AvatarURL != "/api/auth/avatars/x.png" {
		t.Fatalf("unexpected profile: %+v", got)
	}
}

func TestPGXUserStore_GetSelfProfile_NullAvatarIsEmpty(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery(`SELECT id, display_name, avatar_url`).
		WithArgs("user-1").
		WillReturnRows(pgxmock.NewRows([]string{"id", "display_name", "avatar_url"}).
			AddRow("user-1", "Ana", nil))

	got, err := storage.NewPGXUserStore(mock).GetSelfProfile(context.Background(), "user-1")
	if err != nil || got.AvatarURL != "" {
		t.Fatalf("expected empty avatar, got %q err=%v", got.AvatarURL, err)
	}
}

func TestPGXUserStore_GetSelfProfile_NotFound(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery(`SELECT id, display_name, avatar_url`).
		WithArgs("user-1").
		WillReturnError(pgx.ErrNoRows)

	if _, err := storage.NewPGXUserStore(mock).GetSelfProfile(context.Background(), "user-1"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestPGXUserStore_UpdateDisplayName_ReturnsPersistedProfile(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery(`UPDATE auth\.users\s+SET display_name = \$2, updated_at = now\(\)\s+WHERE id = \$1\s+AND status = 'active'\s+AND deleted_at IS NULL\s+RETURNING id, display_name, avatar_url`).
		WithArgs("user-1", "Ana Lima").
		WillReturnRows(pgxmock.NewRows([]string{"id", "display_name", "avatar_url"}).
			AddRow("user-1", "Ana Lima", strptrAvatar("/api/auth/avatars/x.png")))

	got, err := storage.NewPGXUserStore(mock).UpdateDisplayName(context.Background(), "user-1", "Ana Lima")
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if got.ID != "user-1" || got.DisplayName != "Ana Lima" || got.AvatarURL != "/api/auth/avatars/x.png" {
		t.Fatalf("unexpected profile: %+v", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet: %v", err)
	}
}

func TestPGXUserStore_UpdateDisplayName_NullAvatarIsEmpty(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery(`UPDATE auth\.users`).
		WithArgs("user-1", "Ana").
		WillReturnRows(pgxmock.NewRows([]string{"id", "display_name", "avatar_url"}).
			AddRow("user-1", "Ana", nil))

	got, err := storage.NewPGXUserStore(mock).UpdateDisplayName(context.Background(), "user-1", "Ana")
	if err != nil || got.AvatarURL != "" {
		t.Fatalf("expected empty avatar, got %q err=%v", got.AvatarURL, err)
	}
}

// Inactive, deleted, or nonexistent users match no row: the WHERE clause
// excludes them, so the UPDATE affects nothing and RETURNING yields ErrNoRows.
func TestPGXUserStore_UpdateDisplayName_InactiveOrMissingReturnsNotFound(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery(`UPDATE auth\.users`).
		WithArgs("user-1", "Ana").
		WillReturnError(pgx.ErrNoRows)

	if _, err := storage.NewPGXUserStore(mock).UpdateDisplayName(context.Background(), "user-1", "Ana"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestPGXUserStore_UpdateDisplayName_PropagatesDBError(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery(`UPDATE auth\.users`).
		WithArgs("user-1", "Ana").
		WillReturnError(errors.New("connection reset"))

	if _, err := storage.NewPGXUserStore(mock).UpdateDisplayName(context.Background(), "user-1", "Ana"); err == nil || errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected a wrapped db error, got %v", err)
	}
}
