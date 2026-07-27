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
	mock.ExpectQuery(`SELECT id, slug, name, status, message_rate_limit_per_minute, created_at, updated_at`).
		WithArgs("default").
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "slug", "name", "status", "message_rate_limit_per_minute", "created_at", "updated_at",
		}).AddRow("00000000-0000-0000-0000-000000000001", "default", "NChat", "active", 60, now, now))

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

	mock.ExpectQuery(`SELECT id, slug, name, status, message_rate_limit_per_minute, created_at, updated_at`).
		WithArgs("default").
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "slug", "name", "status", "message_rate_limit_per_minute", "created_at", "updated_at",
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

	mock.ExpectQuery(`SELECT id, slug, name, status, message_rate_limit_per_minute, created_at, updated_at`).
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

func TestPGXWorkspaceStore_GetWorkspaceByID_ReturnsDisabledWorkspace(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	now := time.Now()
	mock.ExpectQuery(`SELECT id, slug, name, status, message_rate_limit_per_minute, created_at, updated_at`).
		WithArgs("ws-disabled").
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "slug", "name", "status", "message_rate_limit_per_minute", "created_at", "updated_at",
		}).AddRow("ws-disabled", "disabled", "Disabled", "disabled", 60, now, now))

	workspace, err := storage.NewPGXWorkspaceStore(mock).GetWorkspaceByID(context.Background(), "ws-disabled")
	if err != nil {
		t.Fatalf("GetWorkspaceByID: %v", err)
	}
	if workspace.Status != domain.WorkspaceStatusDisabled {
		t.Fatalf("expected disabled workspace, got %q", workspace.Status)
	}
}

func TestPGXWorkspaceStore_GetWorkspaceByID_NotFound(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery(`SELECT id, slug, name, status, message_rate_limit_per_minute, created_at, updated_at`).
		WithArgs("missing").
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "slug", "name", "status", "message_rate_limit_per_minute", "created_at", "updated_at",
		}))

	_, err = storage.NewPGXWorkspaceStore(mock).GetWorkspaceByID(context.Background(), "missing")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestPGXWorkspaceStore_GetWorkspaceByID_DBError(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	want := errors.New("database unavailable")
	mock.ExpectQuery(`SELECT id, slug, name, status, message_rate_limit_per_minute, created_at, updated_at`).
		WithArgs("ws-1").
		WillReturnError(want)

	_, err = storage.NewPGXWorkspaceStore(mock).GetWorkspaceByID(context.Background(), "ws-1")
	if !errors.Is(err, want) {
		t.Fatalf("expected database error, got %v", err)
	}
}

func TestPGXWorkspaceStore_UpdateEditWindow_HasAtomicAdminBackstop(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()
	now := time.Now()
	window := 900
	mock.ExpectQuery(`(?s)UPDATE chat\.workspaces.*chat\.workspace_members.*wm\.role IN \('owner', 'admin'\)`).
		WithArgs("ws-1", "admin-1", &window).
		WillReturnRows(pgxmock.NewRows([]string{"id", "slug", "name", "status", "edit_window_seconds", "created_at", "updated_at"}).
			AddRow("ws-1", "default", "NChat", "active", &window, now, now))

	workspace, err := storage.NewPGXWorkspaceStore(mock).UpdateEditWindow(context.Background(), "ws-1", "admin-1", &window)
	if err != nil || workspace.EditWindowSeconds == nil || *workspace.EditWindowSeconds != window {
		t.Fatalf("UpdateEditWindow() = %+v, %v", workspace, err)
	}
}

func TestPGXWorkspaceStore_UpdateEditWindow_UnauthorizedIsForbidden(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()
	mock.ExpectQuery(`UPDATE chat\.workspaces`).
		WithArgs("ws-1", "member-1", (*int)(nil)).
		WillReturnRows(pgxmock.NewRows([]string{"id", "slug", "name", "status", "edit_window_seconds", "created_at", "updated_at"}))

	_, err = storage.NewPGXWorkspaceStore(mock).UpdateEditWindow(context.Background(), "ws-1", "member-1", nil)
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("expected ErrForbidden, got %v", err)
	}
}

func TestPGXWorkspaceStore_UpdateEditWindow_NullDisablesWindow(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()
	now := time.Now()
	mock.ExpectQuery(`UPDATE chat\.workspaces`).
		WithArgs("ws-1", "admin-1", (*int)(nil)).
		WillReturnRows(pgxmock.NewRows([]string{"id", "slug", "name", "status", "edit_window_seconds", "created_at", "updated_at"}).
			AddRow("ws-1", "default", "NChat", "active", nil, now, now))

	workspace, err := storage.NewPGXWorkspaceStore(mock).UpdateEditWindow(context.Background(), "ws-1", "admin-1", nil)
	if err != nil || workspace.EditWindowSeconds != nil {
		t.Fatalf("UpdateEditWindow(nil) = %+v, %v", workspace, err)
	}
}

// ── RF-19 anti-spam policy (issue #419) ──────────────────────────────────────

func TestPGXWorkspaceStore_UpdateMessageRateLimit_HasAtomicAdminBackstop(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()
	now := time.Now()
	// The membership join and the owner/admin predicate are part of the same
	// statement as the write, so authorization cannot be raced apart from it.
	mock.ExpectQuery(`(?s)UPDATE chat\.workspaces.*chat\.workspace_members.*wm\.role IN \('owner', 'admin'\)`).
		WithArgs("ws-1", "admin-1", 30).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "slug", "name", "status", "message_rate_limit_per_minute", "created_at", "updated_at",
		}).AddRow("ws-1", "default", "NChat", "active", 30, now, now))

	workspace, err := storage.NewPGXWorkspaceStore(mock).
		UpdateMessageRateLimit(context.Background(), "ws-1", "admin-1", 30)
	if err != nil {
		t.Fatalf("UpdateMessageRateLimit: %v", err)
	}
	if workspace.MessageRateLimitPerMinute != 30 {
		t.Fatalf("expected 30, got %d", workspace.MessageRateLimitPerMinute)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// A caller who is not an owner/admin of this workspace — including an admin of
// some other workspace — matches no row, which must surface as ErrForbidden
// rather than as a silent no-op success.
func TestPGXWorkspaceStore_UpdateMessageRateLimit_UnauthorizedIsForbidden(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()
	mock.ExpectQuery(`UPDATE chat\.workspaces`).
		WithArgs("ws-1", "member-1", 30).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "slug", "name", "status", "message_rate_limit_per_minute", "created_at", "updated_at",
		}))

	_, err = storage.NewPGXWorkspaceStore(mock).
		UpdateMessageRateLimit(context.Background(), "ws-1", "member-1", 30)
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("expected ErrForbidden, got %v", err)
	}
}

// The database CHECK is the backstop for a value the handler should already
// have rejected; it must surface as an error, never as a clamped write.
func TestPGXWorkspaceStore_UpdateMessageRateLimit_ConstraintViolationSurfaces(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()
	mock.ExpectQuery(`UPDATE chat\.workspaces`).
		WithArgs("ws-1", "admin-1", 9999).
		WillReturnError(errors.New("new row violates check constraint"))

	_, err = storage.NewPGXWorkspaceStore(mock).
		UpdateMessageRateLimit(context.Background(), "ws-1", "admin-1", 9999)
	if err == nil || errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("expected a wrapped database error, got %v", err)
	}
}
