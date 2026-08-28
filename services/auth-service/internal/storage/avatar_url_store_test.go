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

	mock.ExpectQuery(`SELECT id, display_name, avatar_url, job_title, bio, timezone, custom_status\s+FROM auth\.users\s+WHERE id = \$1\s+AND status = 'active'\s+AND deleted_at IS NULL`).
		WithArgs("user-1").
		WillReturnRows(pgxmock.NewRows([]string{"id", "display_name", "avatar_url", "job_title", "bio", "timezone", "custom_status"}).
			AddRow("user-1", "Ana", strptrAvatar("/api/auth/avatars/x.png"),
				strptrAvatar("Engenheira"), strptrAvatar("Focada em backend."), strptrAvatar("America/Sao_Paulo"), strptrAvatar("Em reunião")))

	got, err := storage.NewPGXUserStore(mock).GetSelfProfile(context.Background(), "user-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.ID != "user-1" || got.DisplayName != "Ana" || got.AvatarURL != "/api/auth/avatars/x.png" {
		t.Fatalf("unexpected profile: %+v", got)
	}
	if got.JobTitle != "Engenheira" || got.Bio != "Focada em backend." ||
		got.Timezone != "America/Sao_Paulo" || got.CustomStatus != "Em reunião" {
		t.Fatalf("unexpected profile fields: %+v", got)
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
		WillReturnRows(pgxmock.NewRows([]string{"id", "display_name", "avatar_url", "job_title", "bio", "timezone", "custom_status"}).
			AddRow("user-1", "Ana", nil, nil, nil, nil, nil))

	got, err := storage.NewPGXUserStore(mock).GetSelfProfile(context.Background(), "user-1")
	if err != nil || got.AvatarURL != "" {
		t.Fatalf("expected empty avatar, got %q err=%v", got.AvatarURL, err)
	}
	if got.JobTitle != "" || got.Bio != "" || got.Timezone != "" || got.CustomStatus != "" {
		t.Fatalf("expected empty optional fields, got %+v", got)
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

	mock.ExpectQuery(`UPDATE auth\.users\s+SET display_name = \$2, updated_at = now\(\)\s+WHERE id = \$1\s+AND status = 'active'\s+AND deleted_at IS NULL\s+RETURNING id, display_name, avatar_url, job_title, bio, timezone, custom_status`).
		WithArgs("user-1", "Ana Lima").
		WillReturnRows(pgxmock.NewRows([]string{"id", "display_name", "avatar_url", "job_title", "bio", "timezone", "custom_status"}).
			AddRow("user-1", "Ana Lima", strptrAvatar("/api/auth/avatars/x.png"), nil, nil, nil, nil))

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

// UpdateDisplayName's SET clause names only display_name; this confirms the
// other four optional columns come back exactly as they already were,
// proving this write cannot be the source of the cross-field clobbering that
// motivated giving job_title/bio/timezone/custom_status their own,
// independent write path (UpdateProfileFields).
func TestPGXUserStore_UpdateDisplayName_LeavesProfileFieldsUntouched(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery(`UPDATE auth\.users\s+SET display_name = \$2, updated_at = now\(\)`).
		WithArgs("user-1", "Ana Lima").
		WillReturnRows(pgxmock.NewRows([]string{"id", "display_name", "avatar_url", "job_title", "bio", "timezone", "custom_status"}).
			AddRow("user-1", "Ana Lima", nil, strptrAvatar("Engenheira"), strptrAvatar("Bio."), strptrAvatar("UTC"), strptrAvatar("Ocupada")))

	got, err := storage.NewPGXUserStore(mock).UpdateDisplayName(context.Background(), "user-1", "Ana Lima")
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if got.JobTitle != "Engenheira" || got.Bio != "Bio." || got.Timezone != "UTC" || got.CustomStatus != "Ocupada" {
		t.Fatalf("expected pre-existing profile fields to survive a display-name-only write, got %+v", got)
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
		WillReturnRows(pgxmock.NewRows([]string{"id", "display_name", "avatar_url", "job_title", "bio", "timezone", "custom_status"}).
			AddRow("user-1", "Ana", nil, nil, nil, nil, nil))

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

// ── UpdateProfileFields ──────────────────────────────────────────────────

func TestPGXUserStore_UpdateProfileFields_SetsAllFieldsWhenProvided(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery(`UPDATE auth\.users\s+SET job_title\s+= CASE WHEN \$6 THEN \$2 ELSE job_title END,\s+bio\s+= CASE WHEN \$7 THEN \$3 ELSE bio END,\s+timezone\s+= CASE WHEN \$8 THEN \$4 ELSE timezone END,\s+custom_status = CASE WHEN \$9 THEN \$5 ELSE custom_status END`).
		WithArgs("user-1", "Engenheira", "Focada em backend.", "America/Sao_Paulo", "Em reunião", true, true, true, true).
		WillReturnRows(pgxmock.NewRows([]string{"id", "display_name", "avatar_url", "job_title", "bio", "timezone", "custom_status"}).
			AddRow("user-1", "Ana", nil, strptrAvatar("Engenheira"), strptrAvatar("Focada em backend."), strptrAvatar("America/Sao_Paulo"), strptrAvatar("Em reunião")))

	got, err := storage.NewPGXUserStore(mock).UpdateProfileFields(context.Background(), "user-1",
		strptrAvatar("Engenheira"), strptrAvatar("Focada em backend."), strptrAvatar("America/Sao_Paulo"), strptrAvatar("Em reunião"))
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if got.JobTitle != "Engenheira" || got.Bio != "Focada em backend." ||
		got.Timezone != "America/Sao_Paulo" || got.CustomStatus != "Em reunião" {
		t.Fatalf("unexpected profile: %+v", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet: %v", err)
	}
}

// A nil argument must reach the query as a false "provided" flag with a nil
// value — this is the SQL-level proof that a field the caller never
// mentioned is passed through as "leave it alone," not as "clear it."
func TestPGXUserStore_UpdateProfileFields_NilArgumentsAreNotProvided(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery(`UPDATE auth\.users`).
		WithArgs("user-1", nil, nil, nil, nil, false, false, false, false).
		WillReturnRows(pgxmock.NewRows([]string{"id", "display_name", "avatar_url", "job_title", "bio", "timezone", "custom_status"}).
			AddRow("user-1", "Ana", nil, strptrAvatar("Engenheira"), nil, nil, nil))

	got, err := storage.NewPGXUserStore(mock).UpdateProfileFields(context.Background(), "user-1", nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	// The row read back has the pre-existing job_title, proving the query
	// left it untouched rather than clearing it.
	if got.JobTitle != "Engenheira" {
		t.Fatalf("expected pre-existing job_title to survive an all-nil call, got %q", got.JobTitle)
	}
}

// A single field can be provided without the other three being affected —
// the "provided" flags are independent per field.
func TestPGXUserStore_UpdateProfileFields_OneFieldProvided(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery(`UPDATE auth\.users`).
		WithArgs("user-1", "Engenheira", nil, nil, nil, true, false, false, false).
		WillReturnRows(pgxmock.NewRows([]string{"id", "display_name", "avatar_url", "job_title", "bio", "timezone", "custom_status"}).
			AddRow("user-1", "Ana", nil, strptrAvatar("Engenheira"), nil, nil, nil))

	_, err = storage.NewPGXUserStore(mock).UpdateProfileFields(context.Background(), "user-1", strptrAvatar("Engenheira"), nil, nil, nil)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet: %v", err)
	}
}

func TestPGXUserStore_UpdateProfileFields_CustomStatusOnly(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery(`UPDATE auth\.users`).
		WithArgs("user-1", nil, nil, nil, "Em reunião", false, false, false, true).
		WillReturnRows(pgxmock.NewRows([]string{"id", "display_name", "avatar_url", "job_title", "bio", "timezone", "custom_status"}).
			AddRow("user-1", "Ana", nil, nil, nil, nil, strptrAvatar("Em reunião")))

	got, err := storage.NewPGXUserStore(mock).UpdateProfileFields(context.Background(), "user-1", nil, nil, nil, strptrAvatar("Em reunião"))
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if got.CustomStatus != "Em reunião" {
		t.Fatalf("expected custom_status set, got %q", got.CustomStatus)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet: %v", err)
	}
}

// A pointer to "" is provided (clears the column), distinct from nil (leaves
// it alone) — both are represented as NULL SQL parameters, but with a
// different "provided" boolean, which is what the store must get right.
func TestPGXUserStore_UpdateProfileFields_EmptyStringClearsColumn(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	empty := ""
	mock.ExpectQuery(`UPDATE auth\.users`).
		WithArgs("user-1", nil, nil, nil, nil, false, false, false, true).
		WillReturnRows(pgxmock.NewRows([]string{"id", "display_name", "avatar_url", "job_title", "bio", "timezone", "custom_status"}).
			AddRow("user-1", "Ana", nil, nil, nil, nil, nil))

	got, err := storage.NewPGXUserStore(mock).UpdateProfileFields(context.Background(), "user-1", nil, nil, nil, &empty)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if got.CustomStatus != "" {
		t.Fatalf("expected custom_status cleared, got %q", got.CustomStatus)
	}
}

// Inactive, deleted, or nonexistent users match no row: the WHERE clause
// excludes them, so the UPDATE affects nothing and RETURNING yields ErrNoRows.
func TestPGXUserStore_UpdateProfileFields_InactiveOrMissingReturnsNotFound(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery(`UPDATE auth\.users`).
		WithArgs("user-1", "Engenheira", nil, nil, nil, true, false, false, false).
		WillReturnError(pgx.ErrNoRows)

	if _, err := storage.NewPGXUserStore(mock).UpdateProfileFields(context.Background(), "user-1", strptrAvatar("Engenheira"), nil, nil, nil); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestPGXUserStore_UpdateProfileFields_PropagatesDBError(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery(`UPDATE auth\.users`).
		WithArgs("user-1", "Engenheira", nil, nil, nil, true, false, false, false).
		WillReturnError(errors.New("connection reset"))

	if _, err := storage.NewPGXUserStore(mock).UpdateProfileFields(context.Background(), "user-1", strptrAvatar("Engenheira"), nil, nil, nil); err == nil || errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected a wrapped db error, got %v", err)
	}
}
