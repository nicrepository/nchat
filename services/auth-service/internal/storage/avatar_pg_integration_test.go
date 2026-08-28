package storage_test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nicrepository/nchat/services/auth-service/internal/domain"
	"github.com/nicrepository/nchat/services/auth-service/internal/storage"
)

// applyAuthMigrations resets the auth schema and applies every up migration, so
// the avatar/OIDC integration runs against the real production DDL.
func applyAuthMigrations(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	var db string
	if err := pool.QueryRow(ctx, `SELECT current_database()`).Scan(&db); err != nil {
		t.Fatalf("current database: %v", err)
	}
	if !strings.HasSuffix(db, "_test") {
		t.Fatalf("refusing to run destructive migration on non-test database %q", db)
	}
	if _, err := pool.Exec(ctx, `DROP SCHEMA IF EXISTS auth CASCADE`); err != nil {
		t.Fatalf("reset auth schema: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DROP SCHEMA IF EXISTS auth CASCADE`) })

	_, currentFile, _, _ := runtime.Caller(0)
	root := filepath.Join(filepath.Dir(currentFile), "..", "..", "..", "..")
	dir := filepath.Join(root, "migrations", "auth")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read migrations dir: %v", err)
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".up.sql") {
			continue
		}
		sql, readErr := os.ReadFile(filepath.Join(dir, e.Name())) //nolint:gosec // repo-local migrations
		if readErr != nil {
			t.Fatalf("read %s: %v", e.Name(), readErr)
		}
		if _, execErr := pool.Exec(ctx, string(sql)); execErr != nil {
			t.Fatalf("apply %s: %v", e.Name(), execErr)
		}
	}
}

func connectAuthTestDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("AUTH_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("AUTH_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func insertActiveUser(t *testing.T, pool *pgxpool.Pool, email string) string {
	t.Helper()
	var id string
	err := pool.QueryRow(context.Background(), `
		INSERT INTO auth.users (email, display_name, status, auth_source, email_verified_at)
		VALUES ($1, 'Test User', 'active', 'manual', now())
		RETURNING id::text`, email).Scan(&id)
	if err != nil {
		t.Fatalf("insert user: %v", err)
	}
	return id
}

func TestPGXUserStore_AvatarLifecyclePostgreSQL(t *testing.T) {
	pool := connectAuthTestDB(t)
	applyAuthMigrations(t, pool)
	store := storage.NewPGXUserStore(pool)
	ctx := context.Background()
	userID := insertActiveUser(t, pool, "avatar@example.test")

	// Set returns the empty previous value and persists the new one.
	prev, err := store.SetAvatarURL(ctx, userID, "/api/auth/avatars/1111111111111111.png")
	if err != nil || prev != "" {
		t.Fatalf("first set: prev=%q err=%v", prev, err)
	}
	if got := readAvatar(t, pool, userID); got != "/api/auth/avatars/1111111111111111.png" {
		t.Fatalf("stored avatar mismatch: %q", got)
	}

	// Replace returns the prior file so the caller can delete it.
	prev, err = store.SetAvatarURL(ctx, userID, "/api/auth/avatars/2222222222222222.png")
	if err != nil || prev != "/api/auth/avatars/1111111111111111.png" {
		t.Fatalf("replace: prev=%q err=%v", prev, err)
	}

	// Clear returns the last value and NULLs the column.
	prev, err = store.ClearAvatarURL(ctx, userID)
	if err != nil || prev != "/api/auth/avatars/2222222222222222.png" {
		t.Fatalf("clear: prev=%q err=%v", prev, err)
	}
	if got := readAvatar(t, pool, userID); got != "" {
		t.Fatalf("avatar should be NULL after clear, got %q", got)
	}

	// Clearing an already-empty avatar is idempotent.
	if prev, err = store.ClearAvatarURL(ctx, userID); err != nil || prev != "" {
		t.Fatalf("idempotent clear: prev=%q err=%v", prev, err)
	}
}

// TestPGXUserStore_GetSelfProfilePostgreSQL is the reload evidence: after an
// avatar is persisted, a fresh read (what GET /auth/me does) surfaces it; after
// removal it reads back empty.
func TestPGXUserStore_GetSelfProfilePostgreSQL(t *testing.T) {
	pool := connectAuthTestDB(t)
	applyAuthMigrations(t, pool)
	store := storage.NewPGXUserStore(pool)
	ctx := context.Background()
	userID := insertActiveUser(t, pool, "reload@example.test")

	// No avatar yet.
	profile, err := store.GetSelfProfile(ctx, userID)
	if err != nil || profile.AvatarURL != "" {
		t.Fatalf("initial profile: %+v err=%v", profile, err)
	}

	// Persist an avatar, then re-read as a fresh page load would.
	if _, err := store.SetAvatarURL(ctx, userID, "/api/auth/avatars/reload.png"); err != nil {
		t.Fatalf("set: %v", err)
	}
	profile, err = store.GetSelfProfile(ctx, userID)
	if err != nil || profile.AvatarURL != "/api/auth/avatars/reload.png" {
		t.Fatalf("after set, profile must carry the avatar: %+v err=%v", profile, err)
	}
	if profile.ID != userID || profile.DisplayName != "Test User" {
		t.Fatalf("unexpected identity: %+v", profile)
	}

	// Remove, then re-read: empty again.
	if _, err := store.ClearAvatarURL(ctx, userID); err != nil {
		t.Fatalf("clear: %v", err)
	}
	profile, err = store.GetSelfProfile(ctx, userID)
	if err != nil || profile.AvatarURL != "" {
		t.Fatalf("after clear, avatar must be empty: %+v err=%v", profile, err)
	}

	// A suspended user is not readable.
	if _, err := pool.Exec(ctx, `UPDATE auth.users SET status = 'suspended' WHERE id = $1`, userID); err != nil {
		t.Fatalf("suspend: %v", err)
	}
	if _, err := store.GetSelfProfile(ctx, userID); err != domain.ErrNotFound {
		t.Fatalf("suspended profile must be ErrNotFound, got %v", err)
	}
}

func TestPGXUserStore_AvatarRejectsInactiveUserPostgreSQL(t *testing.T) {
	pool := connectAuthTestDB(t)
	applyAuthMigrations(t, pool)
	store := storage.NewPGXUserStore(pool)
	ctx := context.Background()
	userID := insertActiveUser(t, pool, "suspended@example.test")
	if _, err := pool.Exec(ctx, `UPDATE auth.users SET status = 'suspended' WHERE id = $1`, userID); err != nil {
		t.Fatalf("suspend: %v", err)
	}
	if _, err := store.SetAvatarURL(ctx, userID, "/api/auth/avatars/3333333333333333.png"); err != domain.ErrNotFound {
		t.Fatalf("expected ErrNotFound for suspended user, got %v", err)
	}
}

// TestPGXUserStore_UpdateDisplayNamePostgreSQL runs the real UPDATE...RETURNING
// against the actual auth.users schema (Code Quality Review, ID 7, finding 2):
// a pgxmock-only test can pass with a query the real column types would
// reject, so this proves the SQL text actually executes and persists.
func TestPGXUserStore_UpdateDisplayNamePostgreSQL(t *testing.T) {
	pool := connectAuthTestDB(t)
	applyAuthMigrations(t, pool)
	store := storage.NewPGXUserStore(pool)
	ctx := context.Background()
	userID := insertActiveUser(t, pool, "displayname@example.test")

	_, before := readDisplayNameAndUpdatedAt(t, pool, userID)

	profile, err := store.UpdateDisplayName(ctx, userID, "Ana Lima")
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if profile.ID != userID || profile.DisplayName != "Ana Lima" {
		t.Fatalf("unexpected returned profile: %+v", profile)
	}

	gotName, after := readDisplayNameAndUpdatedAt(t, pool, userID)
	if gotName != "Ana Lima" {
		t.Fatalf("persisted display_name mismatch: got %q", gotName)
	}
	if !after.After(before) {
		t.Fatalf("updated_at must advance: before=%v after=%v", before, after)
	}

	// A fresh read (what GET /auth/me does) surfaces the persisted value.
	reread, err := store.GetSelfProfile(ctx, userID)
	if err != nil || reread.DisplayName != "Ana Lima" {
		t.Fatalf("reload after update: %+v err=%v", reread, err)
	}
}

func TestPGXUserStore_UpdateDisplayNameRejectsSuspendedUserPostgreSQL(t *testing.T) {
	pool := connectAuthTestDB(t)
	applyAuthMigrations(t, pool)
	store := storage.NewPGXUserStore(pool)
	ctx := context.Background()
	userID := insertActiveUser(t, pool, "suspended-name@example.test")
	if _, err := pool.Exec(ctx, `UPDATE auth.users SET status = 'suspended' WHERE id = $1`, userID); err != nil {
		t.Fatalf("suspend: %v", err)
	}

	if _, err := store.UpdateDisplayName(ctx, userID, "Should Not Persist"); err != domain.ErrNotFound {
		t.Fatalf("expected ErrNotFound for suspended user, got %v", err)
	}
	if gotName, _ := readDisplayNameAndUpdatedAt(t, pool, userID); gotName != "Test User" {
		t.Fatalf("suspended user's display_name must not change, got %q", gotName)
	}
}

// TestPGXUserStore_UpdateDisplayNameRejectsDeletedUserPostgreSQL covers a
// soft-deleted row distinctly from a merely suspended one: deleted_at can be
// set independently of status, and the store's WHERE clause checks both.
func TestPGXUserStore_UpdateDisplayNameRejectsDeletedUserPostgreSQL(t *testing.T) {
	pool := connectAuthTestDB(t)
	applyAuthMigrations(t, pool)
	store := storage.NewPGXUserStore(pool)
	ctx := context.Background()
	userID := insertActiveUser(t, pool, "deleted-name@example.test")
	if _, err := pool.Exec(ctx, `UPDATE auth.users SET deleted_at = now() WHERE id = $1`, userID); err != nil {
		t.Fatalf("soft-delete: %v", err)
	}

	if _, err := store.UpdateDisplayName(ctx, userID, "Should Not Persist"); err != domain.ErrNotFound {
		t.Fatalf("expected ErrNotFound for deleted user, got %v", err)
	}
	if gotName, _ := readDisplayNameAndUpdatedAt(t, pool, userID); gotName != "Test User" {
		t.Fatalf("deleted user's display_name must not change, got %q", gotName)
	}
}

// TestPGXUserStore_UpdateProfileFieldsPostgreSQL runs the real CASE-based
// UPDATE against the actual auth.users schema: a pgxmock-only test can pass
// with a CASE expression Postgres would refuse to plan (a type Postgres
// cannot unify across the WHEN/ELSE branches, for instance), so this proves
// the SQL text actually executes and persists.
func TestPGXUserStore_UpdateProfileFieldsPostgreSQL(t *testing.T) {
	pool := connectAuthTestDB(t)
	applyAuthMigrations(t, pool)
	store := storage.NewPGXUserStore(pool)
	ctx := context.Background()
	userID := insertActiveUser(t, pool, "profilefields@example.test")

	jobTitle, bio, timezone, customStatus := "Engenheira", "Focada em backend.", "America/Sao_Paulo", "Em reunião"
	profile, err := store.UpdateProfileFields(ctx, userID, &jobTitle, &bio, &timezone, &customStatus)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if profile.JobTitle != jobTitle || profile.Bio != bio || profile.Timezone != timezone ||
		profile.CustomStatus != customStatus {
		t.Fatalf("unexpected returned profile: %+v", profile)
	}

	reread, err := store.GetSelfProfile(ctx, userID)
	if err != nil || reread.JobTitle != jobTitle || reread.Bio != bio ||
		reread.Timezone != timezone || reread.CustomStatus != customStatus {
		t.Fatalf("reload after update: %+v err=%v", reread, err)
	}
}

// TestPGXUserStore_UpdateProfileFieldsPostgreSQL_PartialUpdateLeavesOthersAlone
// is the real-database proof for the bug the pointer-based signature exists
// to prevent: updating only job_title must leave a previously-set bio,
// timezone and custom_status exactly as they were, not NULL them out.
func TestPGXUserStore_UpdateProfileFieldsPostgreSQL_PartialUpdateLeavesOthersAlone(t *testing.T) {
	pool := connectAuthTestDB(t)
	applyAuthMigrations(t, pool)
	store := storage.NewPGXUserStore(pool)
	ctx := context.Background()
	userID := insertActiveUser(t, pool, "partialfields@example.test")

	bio, timezone, customStatus := "Focada em backend.", "America/Sao_Paulo", "Em reunião"
	if _, err := store.UpdateProfileFields(ctx, userID, nil, &bio, &timezone, &customStatus); err != nil {
		t.Fatalf("seed: %v", err)
	}

	jobTitle := "Engenheira"
	profile, err := store.UpdateProfileFields(ctx, userID, &jobTitle, nil, nil, nil)
	if err != nil {
		t.Fatalf("partial update: %v", err)
	}
	if profile.JobTitle != jobTitle {
		t.Fatalf("expected job_title set, got %q", profile.JobTitle)
	}
	if profile.Bio != bio || profile.Timezone != timezone || profile.CustomStatus != customStatus {
		t.Fatalf("expected untouched fields to survive a partial update, got %+v", profile)
	}
}

func TestPGXUserStore_UpdateProfileFieldsPostgreSQL_CustomStatusOnly(t *testing.T) {
	pool := connectAuthTestDB(t)
	applyAuthMigrations(t, pool)
	store := storage.NewPGXUserStore(pool)
	ctx := context.Background()
	userID := insertActiveUser(t, pool, "customstatus@example.test")

	customStatus := "Em reunião"
	profile, err := store.UpdateProfileFields(ctx, userID, nil, nil, nil, &customStatus)
	if err != nil {
		t.Fatalf("update custom_status: %v", err)
	}
	if profile.CustomStatus != customStatus {
		t.Fatalf("expected custom_status set, got %q", profile.CustomStatus)
	}
}

// A pointer to "" is a request to clear the field, distinct from nil — the
// real-database counterpart to the pgxmock proof in
// TestPGXUserStore_UpdateProfileFields_EmptyStringClearsColumn.
func TestPGXUserStore_UpdateProfileFieldsPostgreSQL_EmptyClearsField(t *testing.T) {
	pool := connectAuthTestDB(t)
	applyAuthMigrations(t, pool)
	store := storage.NewPGXUserStore(pool)
	ctx := context.Background()
	userID := insertActiveUser(t, pool, "clearfields@example.test")

	customStatus := "Em reunião"
	if _, err := store.UpdateProfileFields(ctx, userID, nil, nil, nil, &customStatus); err != nil {
		t.Fatalf("seed: %v", err)
	}

	empty := ""
	profile, err := store.UpdateProfileFields(ctx, userID, nil, nil, nil, &empty)
	if err != nil {
		t.Fatalf("clear: %v", err)
	}
	if profile.CustomStatus != "" {
		t.Fatalf("expected custom_status cleared, got %q", profile.CustomStatus)
	}
}

func TestPGXUserStore_UpdateProfileFieldsRejectsSuspendedUserPostgreSQL(t *testing.T) {
	pool := connectAuthTestDB(t)
	applyAuthMigrations(t, pool)
	store := storage.NewPGXUserStore(pool)
	ctx := context.Background()
	userID := insertActiveUser(t, pool, "suspended-fields@example.test")
	if _, err := pool.Exec(ctx, `UPDATE auth.users SET status = 'suspended' WHERE id = $1`, userID); err != nil {
		t.Fatalf("suspend: %v", err)
	}

	jobTitle := "Should Not Persist"
	if _, err := store.UpdateProfileFields(ctx, userID, &jobTitle, nil, nil, nil); err != domain.ErrNotFound {
		t.Fatalf("expected ErrNotFound for suspended user, got %v", err)
	}
}

func TestPGXUserStore_UpdateProfileFieldsRejectsDeletedUserPostgreSQL(t *testing.T) {
	pool := connectAuthTestDB(t)
	applyAuthMigrations(t, pool)
	store := storage.NewPGXUserStore(pool)
	ctx := context.Background()
	userID := insertActiveUser(t, pool, "deleted-fields@example.test")
	if _, err := pool.Exec(ctx, `UPDATE auth.users SET deleted_at = now() WHERE id = $1`, userID); err != nil {
		t.Fatalf("soft-delete: %v", err)
	}

	jobTitle := "Should Not Persist"
	if _, err := store.UpdateProfileFields(ctx, userID, &jobTitle, nil, nil, nil); err != domain.ErrNotFound {
		t.Fatalf("expected ErrNotFound for deleted user, got %v", err)
	}
}

func readDisplayNameAndUpdatedAt(t *testing.T, pool *pgxpool.Pool, userID string) (string, time.Time) {
	t.Helper()
	var name string
	var updatedAt time.Time
	err := pool.QueryRow(context.Background(),
		`SELECT display_name, updated_at FROM auth.users WHERE id = $1`, userID,
	).Scan(&name, &updatedAt)
	if err != nil {
		t.Fatalf("read display_name/updated_at: %v", err)
	}
	return name, updatedAt
}

// TestPGXOIDCStore_ReloginDoesNotClobberUploadedAvatarPostgreSQL is the security
// evidence for the OIDC precedence rule: a user uploads an avatar, then signs in
// again through OIDC with a (same-origin) picture claim. The stored avatar must
// remain the uploaded one — the login never overwrites it.
func TestPGXOIDCStore_ReloginDoesNotClobberUploadedAvatarPostgreSQL(t *testing.T) {
	pool := connectAuthTestDB(t)
	applyAuthMigrations(t, pool)
	// JIT provisioning enrolls the new user in the default workspace, so the
	// chat schema has to be there for the login under test to succeed.
	applyChatMigrations(t, pool)
	ctx := context.Background()

	oidcStore := storage.NewPGXOIDCStore(pool)
	userStore := storage.NewPGXUserStore(pool)
	build := func(_ domain.Session, user domain.LoginUser) (domain.OIDCExchangeInput, error) {
		return domain.OIDCExchangeInput{
			ID: uuid.NewString(), Provider: "keycloak", CodeHash: uuid.NewString(),
			AccessValueEncrypted: "a", RefreshValueEncrypted: "r", BearerScheme: "Bearer",
			ExpiresIn: 900, User: user, ExpiresAt: time.Now().Add(2 * time.Minute),
		}, nil
	}

	// Provision via OIDC (no avatar from the provider).
	if _, err := oidcStore.CreateOIDCSessionAndExchange(ctx, domain.OIDCSessionInput{
		Provider: "keycloak", Subject: "sub-clobber", Email: "clobber@example.test",
		DisplayName: "Clobber User", RefreshTokenHash: "h1", RefreshExpiresAt: time.Now().Add(time.Hour),
		AutoProvision: true,
	}, build); err != nil {
		t.Fatalf("provision: %v", err)
	}

	var userID string
	if err := pool.QueryRow(ctx, `SELECT id::text FROM auth.users WHERE external_subject = 'sub-clobber'`).Scan(&userID); err != nil {
		t.Fatalf("read user id: %v", err)
	}

	// The user uploads an avatar (operational producer).
	uploaded := "/api/auth/avatars/abcabcabcabcabcd.png"
	if _, err := userStore.SetAvatarURL(ctx, userID, uploaded); err != nil {
		t.Fatalf("set avatar: %v", err)
	}

	// Re-login with a same-origin picture claim that differs from the upload.
	if _, err := oidcStore.CreateOIDCSessionAndExchange(ctx, domain.OIDCSessionInput{
		Provider: "keycloak", Subject: "sub-clobber", Email: "clobber@example.test",
		DisplayName: "Clobber User", AvatarURL: "/api/auth/avatars/ffffffffffffffff.png",
		RefreshTokenHash: "h2", RefreshExpiresAt: time.Now().Add(time.Hour),
	}, build); err != nil {
		t.Fatalf("re-login: %v", err)
	}

	if got := readAvatar(t, pool, userID); got != uploaded {
		t.Fatalf("re-login clobbered the uploaded avatar: got %q, want %q", got, uploaded)
	}
}

func readAvatar(t *testing.T, pool *pgxpool.Pool, userID string) string {
	t.Helper()
	var url *string
	if err := pool.QueryRow(context.Background(), `SELECT avatar_url FROM auth.users WHERE id = $1`, userID).Scan(&url); err != nil {
		t.Fatalf("read avatar: %v", err)
	}
	if url == nil {
		return ""
	}
	return *url
}
