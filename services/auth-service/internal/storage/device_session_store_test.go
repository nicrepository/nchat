package storage_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	pgxmock "github.com/pashagolub/pgxmock/v2"

	"github.com/nicrepository/nchat/services/auth-service/internal/domain"
	"github.com/nicrepository/nchat/services/auth-service/internal/storage"
)

// ---- helpers ----------------------------------------------------------------

func newMockPool(t *testing.T) pgxmock.PgxPoolIface {
	t.Helper()
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	return mock
}

// ---- ListSessions -----------------------------------------------------------

func TestPGXDeviceSessionStore_ListSessions_ReturnsMappedRows(t *testing.T) {
	mock := newMockPool(t)
	defer mock.Close()

	now := time.Now().UTC().Truncate(time.Second)
	deviceID := "device-uuid-1"

	mock.ExpectQuery(`SELECT id, device_id`).
		WithArgs("user-1", false, 50).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "device_id", "created_at", "last_seen_at",
			"idle_expires_at", "absolute_expires_at", "revoked_at",
			"ip_address", "user_agent",
		}).AddRow(
			"session-1", &deviceID, now, now,
			now.Add(time.Hour), nil, nil,
			"192.168.1.1", "Mozilla/5.0",
		))

	store := storage.NewPGXDeviceSessionStore(mock)
	sessions, err := store.ListSessions(context.Background(), "user-1", false, 50)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(sessions))
	}
	if sessions[0].ID != "session-1" {
		t.Fatalf("expected ID 'session-1', got %q", sessions[0].ID)
	}
	if sessions[0].DeviceID == nil || *sessions[0].DeviceID != "device-uuid-1" {
		t.Fatalf("expected DeviceID 'device-uuid-1', got %v", sessions[0].DeviceID)
	}
	if sessions[0].IPAddress != "192.168.1.1" {
		t.Fatalf("expected IPAddress '192.168.1.1', got %q", sessions[0].IPAddress)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXDeviceSessionStore_ListSessions_IncludeRevoked(t *testing.T) {
	mock := newMockPool(t)
	defer mock.Close()

	mock.ExpectQuery(`SELECT id, device_id`).
		WithArgs("user-1", true, 10).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "device_id", "created_at", "last_seen_at",
			"idle_expires_at", "absolute_expires_at", "revoked_at",
			"ip_address", "user_agent",
		}))

	store := storage.NewPGXDeviceSessionStore(mock)
	_, err := store.ListSessions(context.Background(), "user-1", true, 10)
	if err != nil {
		t.Fatalf("ListSessions include_revoked: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// ---- RevokeSession ----------------------------------------------------------

func TestPGXDeviceSessionStore_RevokeSession_RevokesSessionAndHistory(t *testing.T) {
	mock := newMockPool(t)
	defer mock.Close()

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT id FROM auth\.user_sessions`).
		WithArgs("session-1", "user-1").
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow("session-1"))
	mock.ExpectExec(`UPDATE auth\.user_sessions`).
		WithArgs("session-1", "user-1").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec(`UPDATE auth\.refresh_token_history`).
		WithArgs("session-1").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectCommit()
	mock.ExpectRollback()

	store := storage.NewPGXDeviceSessionStore(mock)
	if err := store.RevokeSession(context.Background(), "session-1", "user-1"); err != nil {
		t.Fatalf("RevokeSession: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXDeviceSessionStore_RevokeSession_NotFound_ReturnsErrNotFound(t *testing.T) {
	mock := newMockPool(t)
	defer mock.Close()

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT id FROM auth\.user_sessions`).
		WithArgs("no-such-session", "user-1").
		WillReturnError(pgx.ErrNoRows)
	mock.ExpectRollback()

	store := storage.NewPGXDeviceSessionStore(mock)
	err := store.RevokeSession(context.Background(), "no-such-session", "user-1")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

// ---- RevokeAllSessionsExcept ------------------------------------------------

func TestPGXDeviceSessionStore_RevokeAllSessionsExcept_RunsCTE(t *testing.T) {
	mock := newMockPool(t)
	defer mock.Close()

	mock.ExpectBegin()
	mock.ExpectExec(`WITH revoked AS`).
		WithArgs("user-1", "current-session").
		WillReturnResult(pgxmock.NewResult("UPDATE", 2))
	mock.ExpectCommit()
	mock.ExpectRollback()

	store := storage.NewPGXDeviceSessionStore(mock)
	if err := store.RevokeAllSessionsExcept(context.Background(), "user-1", "current-session"); err != nil {
		t.Fatalf("RevokeAllSessionsExcept: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// ---- ValidateActiveSession --------------------------------------------------

func TestPGXDeviceSessionStore_ValidateActiveSession_RequiresActiveSessionAndUser(t *testing.T) {
	mock := newMockPool(t)
	defer mock.Close()

	mock.ExpectQuery(`FROM auth\.user_sessions AS s\s+JOIN auth\.users AS u`).
		WithArgs("session-1", "user-1").
		WillReturnRows(pgxmock.NewRows([]string{"active"}).AddRow(true))

	store := storage.NewPGXDeviceSessionStore(mock)
	if err := store.ValidateActiveSession(context.Background(), "user-1", "session-1"); err != nil {
		t.Fatalf("ValidateActiveSession: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXDeviceSessionStore_ValidateActiveSession_InvalidCurrentSessionReturnsErrInvalidToken(t *testing.T) {
	mock := newMockPool(t)
	defer mock.Close()

	mock.ExpectQuery(`FROM auth\.user_sessions AS s`).
		WithArgs("revoked-or-expired-session", "user-1").
		WillReturnError(pgx.ErrNoRows)

	store := storage.NewPGXDeviceSessionStore(mock)
	err := store.ValidateActiveSession(context.Background(), "user-1", "revoked-or-expired-session")
	if !errors.Is(err, domain.ErrInvalidToken) {
		t.Fatalf("expected ErrInvalidToken for invalid current session, got %v", err)
	}
}

func TestPGXDeviceSessionStore_ValidateActiveSession_DBErrorPropagates(t *testing.T) {
	mock := newMockPool(t)
	defer mock.Close()

	mock.ExpectQuery(`FROM auth\.user_sessions AS s`).
		WithArgs("session-1", "user-1").
		WillReturnError(errors.New("db down"))

	store := storage.NewPGXDeviceSessionStore(mock)
	err := store.ValidateActiveSession(context.Background(), "user-1", "session-1")
	if err == nil || !strings.Contains(err.Error(), "validate active session") {
		t.Fatalf("expected wrapped validate active session error, got %v", err)
	}
}

// ---- ListDevices ------------------------------------------------------------

func TestPGXDeviceSessionStore_ListDevices_ReturnsMappedRows(t *testing.T) {
	mock := newMockPool(t)
	defer mock.Close()

	now := time.Now().UTC().Truncate(time.Second)

	mock.ExpectQuery(`SELECT d\.id`).
		WithArgs("user-1", "", false, 50).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "display_name", "platform", "last_ip",
			"first_seen_at", "last_seen_at", "revoked_at",
			"session_count", "current",
		}).AddRow(
			"device-1", nil, nil, "10.0.0.1",
			now, now, nil,
			2, false,
		))
	mock.ExpectQuery(`SELECT max_devices_per_user`).
		WillReturnRows(pgxmock.NewRows([]string{"max_devices_per_user"}).AddRow(5))

	store := storage.NewPGXDeviceSessionStore(mock)
	devices, policy, err := store.ListDevices(context.Background(), "user-1", "", false, 50)
	if err != nil {
		t.Fatalf("ListDevices: %v", err)
	}
	if len(devices) != 1 {
		t.Fatalf("expected 1 device, got %d", len(devices))
	}
	if devices[0].ID != "device-1" {
		t.Fatalf("expected ID 'device-1', got %q", devices[0].ID)
	}
	if devices[0].SessionCount != 2 {
		t.Fatalf("expected SessionCount 2, got %d", devices[0].SessionCount)
	}
	if policy.MaxDevicesPerUser != 5 {
		t.Fatalf("expected MaxDevicesPerUser 5, got %d", policy.MaxDevicesPerUser)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXDeviceSessionStore_ListDevices_EmptySessionID_NeverCastsUUID(t *testing.T) {
	mock := newMockPool(t)
	defer mock.Close()

	now := time.Now().UTC().Truncate(time.Second)

	mock.ExpectQuery(`SELECT d\.id`).
		WithArgs("user-1", "", false, 50).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "display_name", "platform", "last_ip",
			"first_seen_at", "last_seen_at", "revoked_at",
			"session_count", "current",
		}).AddRow("device-2", nil, nil, "", now, now, nil, 0, false))
	mock.ExpectQuery(`SELECT max_devices_per_user`).
		WillReturnRows(pgxmock.NewRows([]string{"max_devices_per_user"}).AddRow(5))

	store := storage.NewPGXDeviceSessionStore(mock)
	devices, _, err := store.ListDevices(context.Background(), "user-1", "", false, 50)
	if err != nil {
		t.Fatalf("ListDevices empty sid: %v", err)
	}
	if devices[0].Current {
		t.Fatal("expected current=false when session ID is empty")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXDeviceSessionStore_ListDevices_CurrentExpressionIsNullSafe(t *testing.T) {
	mock := newMockPool(t)
	defer mock.Close()

	mock.ExpectQuery(`COALESCE\(d\.id = \(SELECT device_id`).
		WithArgs("user-1", "123e4567-e89b-12d3-a456-426614174000", false, 50).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "display_name", "platform", "last_ip",
			"first_seen_at", "last_seen_at", "revoked_at",
			"session_count", "current",
		}))
	mock.ExpectQuery(`SELECT max_devices_per_user`).
		WillReturnRows(pgxmock.NewRows([]string{"max_devices_per_user"}).AddRow(5))

	store := storage.NewPGXDeviceSessionStore(mock)
	if _, _, err := store.ListDevices(context.Background(), "user-1", "123e4567-e89b-12d3-a456-426614174000", false, 50); err != nil {
		t.Fatalf("ListDevices: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// ---- RevokeDevice -----------------------------------------------------------

func TestPGXDeviceSessionStore_RevokeDevice_UsesCTE(t *testing.T) {
	mock := newMockPool(t)
	defer mock.Close()

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT id FROM auth\.user_devices`).
		WithArgs("device-1", "user-1").
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow("device-1"))
	mock.ExpectExec(`WITH revoked_device AS`).
		WithArgs("device-1", "user-1").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectCommit()
	mock.ExpectRollback()

	store := storage.NewPGXDeviceSessionStore(mock)
	if err := store.RevokeDevice(context.Background(), "device-1", "user-1"); err != nil {
		t.Fatalf("RevokeDevice: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXDeviceSessionStore_RevokeDevice_NotFound_ReturnsErrNotFound(t *testing.T) {
	mock := newMockPool(t)
	defer mock.Close()

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT id FROM auth\.user_devices`).
		WithArgs("no-device", "user-1").
		WillReturnError(pgx.ErrNoRows)
	mock.ExpectRollback()

	store := storage.NewPGXDeviceSessionStore(mock)
	err := store.RevokeDevice(context.Background(), "no-device", "user-1")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

// ---- UpdateDeviceDisplayName ------------------------------------------------

func TestPGXDeviceSessionStore_UpdateDeviceDisplayName_UpdatesActiveDevice(t *testing.T) {
	mock := newMockPool(t)
	defer mock.Close()

	mock.ExpectExec(`UPDATE auth\.user_devices SET display_name`).
		WithArgs("My Phone", "device-1", "user-1").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	store := storage.NewPGXDeviceSessionStore(mock)
	if err := store.UpdateDeviceDisplayName(context.Background(), "device-1", "user-1", "My Phone"); err != nil {
		t.Fatalf("UpdateDeviceDisplayName: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXDeviceSessionStore_UpdateDeviceDisplayName_RevokedOrCrossUser_ReturnsErrNotFound(t *testing.T) {
	mock := newMockPool(t)
	defer mock.Close()

	mock.ExpectExec(`UPDATE auth\.user_devices SET display_name`).
		WithArgs("name", "device-x", "user-1").
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))

	store := storage.NewPGXDeviceSessionStore(mock)
	err := store.UpdateDeviceDisplayName(context.Background(), "device-x", "user-1", "name")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestPGXDeviceSessionStore_ListDevices_PolicyErrorPropagates(t *testing.T) {
	mock := newMockPool(t)
	defer mock.Close()

	mock.ExpectQuery(`SELECT d\.id`).
		WithArgs("user-1", "", false, 50).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "display_name", "platform", "last_ip",
			"first_seen_at", "last_seen_at", "revoked_at",
			"session_count", "current",
		}))
	mock.ExpectQuery(`SELECT max_devices_per_user`).
		WillReturnError(errors.New("policy unavailable"))

	store := storage.NewPGXDeviceSessionStore(mock)
	_, _, err := store.ListDevices(context.Background(), "user-1", "", false, 50)
	if err == nil || !strings.Contains(err.Error(), "get device policy") {
		t.Fatalf("expected policy error, got %v", err)
	}
}

func TestPGXDeviceSessionStore_UpdateDeviceDisplayName_DBErrorPropagates(t *testing.T) {
	mock := newMockPool(t)
	defer mock.Close()

	mock.ExpectExec(`UPDATE auth\.user_devices SET display_name`).
		WithArgs("name", "device-1", "user-1").
		WillReturnError(errors.New("db down"))

	store := storage.NewPGXDeviceSessionStore(mock)
	err := store.UpdateDeviceDisplayName(context.Background(), "device-1", "user-1", "name")
	if err == nil || !strings.Contains(err.Error(), "update device display name") {
		t.Fatalf("expected update error, got %v", err)
	}
}

// ---- RevokeAllUserSessions --------------------------------------------------

func TestPGXDeviceSessionStore_RevokeAllUserSessions_RunsCTE(t *testing.T) {
mock := newMockPool(t)
defer mock.Close()

mock.ExpectBegin()
mock.ExpectExec(`WITH revoked AS`).
WithArgs("user-1").
WillReturnResult(pgxmock.NewResult("UPDATE", 3))
mock.ExpectCommit()
mock.ExpectRollback()

store := storage.NewPGXDeviceSessionStore(mock)
if err := store.RevokeAllUserSessions(context.Background(), "user-1"); err != nil {
t.Fatalf("RevokeAllUserSessions: %v", err)
}
if err := mock.ExpectationsWereMet(); err != nil {
t.Fatalf("unmet expectations: %v", err)
}
}
