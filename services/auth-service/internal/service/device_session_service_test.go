package service_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/nicrepository/nchat/services/auth-service/internal/domain"
	"github.com/nicrepository/nchat/services/auth-service/internal/service"
)

// fakeDeviceSessionStore is a minimal fake for DeviceSessionStore.
type fakeDeviceSessionStore struct {
	listSessionsErr  error
	revokeSessionErr error
	revokeAllErr     error
	listDevicesErr   error
	revokeDeviceErr  error
	updateDisplayErr error
	validateErr      error

	lastSessionIDRevoked string
	lastExceptSessionID  string
	lastDeviceIDRevoked  string
	lastValidatedUserID  string
	lastValidatedSID     string
	lastDisplayName      string
	lastIncludeRevoked   bool
	lastLimit            int
}

func (f *fakeDeviceSessionStore) ListSessions(_ context.Context, _ string, includeRevoked bool, limit int) ([]domain.SessionInfo, error) {
	f.lastIncludeRevoked = includeRevoked
	f.lastLimit = limit
	return nil, f.listSessionsErr
}
func (f *fakeDeviceSessionStore) RevokeSession(_ context.Context, sessionID, _ string) error {
	f.lastSessionIDRevoked = sessionID
	return f.revokeSessionErr
}
func (f *fakeDeviceSessionStore) RevokeAllSessionsExcept(_ context.Context, _, exceptSessionID string) error {
	f.lastExceptSessionID = exceptSessionID
	return f.revokeAllErr
}
func (f *fakeDeviceSessionStore) RevokeAllUserSessions(_ context.Context, _ string) error {
	return nil
}
func (f *fakeDeviceSessionStore) ListDevices(_ context.Context, _ string, _ string, includeRevoked bool, limit int) ([]domain.DeviceInfo, domain.DeviceSessionPolicy, error) {
	f.lastIncludeRevoked = includeRevoked
	f.lastLimit = limit
	return nil, domain.DeviceSessionPolicy{}, f.listDevicesErr
}
func (f *fakeDeviceSessionStore) RevokeDevice(_ context.Context, deviceID, _ string) error {
	f.lastDeviceIDRevoked = deviceID
	return f.revokeDeviceErr
}
func (f *fakeDeviceSessionStore) UpdateDeviceDisplayName(_ context.Context, _, _, name string) error {
	f.lastDisplayName = name
	return f.updateDisplayErr
}
func (f *fakeDeviceSessionStore) ValidateActiveSession(_ context.Context, userID, sessionID string) error {
	f.lastValidatedUserID = userID
	f.lastValidatedSID = sessionID
	return f.validateErr
}

// ---- limit clamping ---------------------------------------------------------

func TestDeviceSessionService_ListSessions_ClampsLimit(t *testing.T) {
	store := &fakeDeviceSessionStore{}
	svc := service.NewDeviceSessionService(store)

	_, _ = svc.ListSessions(context.Background(), "user-1", false, 200)
	if store.lastLimit != 100 {
		t.Fatalf("expected limit clamped to 100, got %d", store.lastLimit)
	}
}

func TestDeviceSessionService_ListSessions_DefaultsLimit(t *testing.T) {
	store := &fakeDeviceSessionStore{}
	svc := service.NewDeviceSessionService(store)

	_, _ = svc.ListSessions(context.Background(), "user-1", false, 0)
	if store.lastLimit != 50 {
		t.Fatalf("expected default limit 50, got %d", store.lastLimit)
	}
}

func TestDeviceSessionService_ListDevices_ClampsLimit(t *testing.T) {
	store := &fakeDeviceSessionStore{}
	svc := service.NewDeviceSessionService(store)

	_, _, _ = svc.ListDevices(context.Background(), "user-1", "", false, 200)
	if store.lastLimit != 100 {
		t.Fatalf("expected limit clamped to 100, got %d", store.lastLimit)
	}
}

// ---- display name validation ------------------------------------------------

func TestDeviceSessionService_UpdateDisplayName_ValidatesMinLength(t *testing.T) {
	store := &fakeDeviceSessionStore{}
	svc := service.NewDeviceSessionService(store)

	err := svc.UpdateDeviceDisplayName(context.Background(), "device-1", "user-1", "")
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput for empty name, got %v", err)
	}
}

func TestDeviceSessionService_UpdateDisplayName_ValidatesMaxLength(t *testing.T) {
	store := &fakeDeviceSessionStore{}
	svc := service.NewDeviceSessionService(store)

	longName := strings.Repeat("a", 81)
	err := svc.UpdateDeviceDisplayName(context.Background(), "device-1", "user-1", longName)
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput for name >80 chars, got %v", err)
	}
}

func TestDeviceSessionService_UpdateDisplayName_StripsControlChars(t *testing.T) {
	store := &fakeDeviceSessionStore{}
	svc := service.NewDeviceSessionService(store)

	_ = svc.UpdateDeviceDisplayName(context.Background(), "device-1", "user-1", "My\x00Phone\r\n")
	if store.lastDisplayName != "MyPhone" {
		t.Fatalf("expected control chars stripped, got %q", store.lastDisplayName)
	}
}

func TestDeviceSessionService_UpdateDisplayName_TrimsWhitespace(t *testing.T) {
	store := &fakeDeviceSessionStore{}
	svc := service.NewDeviceSessionService(store)

	_ = svc.UpdateDeviceDisplayName(context.Background(), "device-1", "user-1", "  My Phone  ")
	if store.lastDisplayName != "My Phone" {
		t.Fatalf("expected trimmed name, got %q", store.lastDisplayName)
	}
}

func TestDeviceSessionService_UpdateDisplayName_ValidName_Delegates(t *testing.T) {
	store := &fakeDeviceSessionStore{}
	svc := service.NewDeviceSessionService(store)

	if err := svc.UpdateDeviceDisplayName(context.Background(), "device-1", "user-1", "My Phone"); err != nil {
		t.Fatalf("expected no error for valid name, got %v", err)
	}
	if store.lastDisplayName != "My Phone" {
		t.Fatalf("expected 'My Phone' passed to store, got %q", store.lastDisplayName)
	}
}

// ---- delegation -------------------------------------------------------------

func TestDeviceSessionService_RevokeSession_Delegates(t *testing.T) {
	store := &fakeDeviceSessionStore{}
	svc := service.NewDeviceSessionService(store)

	_ = svc.RevokeSession(context.Background(), "session-abc", "user-1")
	if store.lastSessionIDRevoked != "session-abc" {
		t.Fatalf("expected session-abc delegated, got %q", store.lastSessionIDRevoked)
	}
}

func TestDeviceSessionService_RevokeDevice_Delegates(t *testing.T) {
	store := &fakeDeviceSessionStore{}
	svc := service.NewDeviceSessionService(store)

	_ = svc.RevokeDevice(context.Background(), "device-abc", "user-1")
	if store.lastDeviceIDRevoked != "device-abc" {
		t.Fatalf("expected device-abc delegated, got %q", store.lastDeviceIDRevoked)
	}
}

func TestDeviceSessionService_ValidateActiveSession_Delegates(t *testing.T) {
	store := &fakeDeviceSessionStore{}
	svc := service.NewDeviceSessionService(store)

	if err := svc.ValidateActiveSession(context.Background(), "user-1", "session-1"); err != nil {
		t.Fatalf("ValidateActiveSession: %v", err)
	}
	if store.lastValidatedUserID != "user-1" || store.lastValidatedSID != "session-1" {
		t.Fatalf("validated (%q, %q), want (user-1, session-1)", store.lastValidatedUserID, store.lastValidatedSID)
	}
}

func TestDeviceSessionService_RevokeAllSessionsExcept_Delegates(t *testing.T) {
	store := &fakeDeviceSessionStore{}
	svc := service.NewDeviceSessionService(store)

	if err := svc.RevokeAllSessionsExcept(context.Background(), "user-1", "session-current"); err != nil {
		t.Fatalf("RevokeAllSessionsExcept: %v", err)
	}
	if store.lastExceptSessionID != "session-current" {
		t.Fatalf("expected session-current delegated, got %q", store.lastExceptSessionID)
	}
}

func TestDeviceSessionService_RevokeAllUserSessions_Delegates(t *testing.T) {
store := &fakeDeviceSessionStore{}
svc := service.NewDeviceSessionService(store)

if err := svc.RevokeAllUserSessions(context.Background(), "user-1"); err != nil {
t.Fatalf("RevokeAllUserSessions: %v", err)
}
}
