package service

import (
	"context"
	"fmt"
	"strings"
	"unicode"

	"github.com/nicrepository/nchat/services/auth-service/internal/domain"
)

const (
	deviceDisplayNameMinLen = 1
	deviceDisplayNameMaxLen = 80
)

// DeviceSessionStore is the persistence interface for DeviceSessionService.
type DeviceSessionStore interface {
	ListSessions(ctx context.Context, userID string, includeRevoked bool, limit int) ([]domain.SessionInfo, error)
	RevokeSession(ctx context.Context, sessionID, userID string) error
	RevokeAllSessionsExcept(ctx context.Context, userID, exceptSessionID string) error
	RevokeAllUserSessions(ctx context.Context, userID string) error
	ListDevices(ctx context.Context, userID, currentSessionID string, includeRevoked bool, limit int) ([]domain.DeviceInfo, domain.DeviceSessionPolicy, error)
	RevokeDevice(ctx context.Context, deviceID, userID string) error
	UpdateDeviceDisplayName(ctx context.Context, deviceID, userID, name string) error
	ValidateActiveSession(ctx context.Context, userID, sessionID string) error
}

// DeviceSessionManager is the HTTP-facing interface for device/session management.
type DeviceSessionManager interface {
	ListSessions(ctx context.Context, userID string, includeRevoked bool, limit int) ([]domain.SessionInfo, error)
	RevokeSession(ctx context.Context, sessionID, userID string) error
	RevokeAllSessionsExcept(ctx context.Context, userID, exceptSessionID string) error
	ListDevices(ctx context.Context, userID, currentSessionID string, includeRevoked bool, limit int) ([]domain.DeviceInfo, domain.DeviceSessionPolicy, error)
	RevokeDevice(ctx context.Context, deviceID, userID string) error
	UpdateDeviceDisplayName(ctx context.Context, deviceID, userID, name string) error
	ValidateActiveSession(ctx context.Context, userID, sessionID string) error
}

// DeviceSessionService implements DeviceSessionManager.
type DeviceSessionService struct {
	store DeviceSessionStore
}

// NewDeviceSessionService creates a DeviceSessionService backed by the given store.
func NewDeviceSessionService(store DeviceSessionStore) *DeviceSessionService {
	return &DeviceSessionService{store: store}
}

func (s *DeviceSessionService) ListSessions(ctx context.Context, userID string, includeRevoked bool, limit int) ([]domain.SessionInfo, error) {
	limit = clampDeviceSessionLimit(limit)
	return s.store.ListSessions(ctx, userID, includeRevoked, limit)
}

func (s *DeviceSessionService) RevokeSession(ctx context.Context, sessionID, userID string) error {
	return s.store.RevokeSession(ctx, sessionID, userID)
}

func (s *DeviceSessionService) RevokeAllSessionsExcept(ctx context.Context, userID, exceptSessionID string) error {
	return s.store.RevokeAllSessionsExcept(ctx, userID, exceptSessionID)
}

func (s *DeviceSessionService) RevokeAllUserSessions(ctx context.Context, userID string) error {
	return s.store.RevokeAllUserSessions(ctx, userID)
}

func (s *DeviceSessionService) ValidateActiveSession(ctx context.Context, userID, sessionID string) error {
	return s.store.ValidateActiveSession(ctx, userID, sessionID)
}

func (s *DeviceSessionService) ListDevices(ctx context.Context, userID, currentSessionID string, includeRevoked bool, limit int) ([]domain.DeviceInfo, domain.DeviceSessionPolicy, error) {
	limit = clampDeviceSessionLimit(limit)
	return s.store.ListDevices(ctx, userID, currentSessionID, includeRevoked, limit)
}

func (s *DeviceSessionService) RevokeDevice(ctx context.Context, deviceID, userID string) error {
	return s.store.RevokeDevice(ctx, deviceID, userID)
}

// UpdateDeviceDisplayName validates and sanitizes the display name, then delegates.
// Returns ErrInvalidInput if name (after trim and strip) is empty or exceeds 80 chars.
func (s *DeviceSessionService) UpdateDeviceDisplayName(ctx context.Context, deviceID, userID, name string) error {
	name = sanitizeDisplayName(name)
	if len([]rune(name)) < deviceDisplayNameMinLen {
		return fmt.Errorf("%w: display_name must be at least 1 character", domain.ErrInvalidInput)
	}
	if len([]rune(name)) > deviceDisplayNameMaxLen {
		return fmt.Errorf("%w: display_name must be at most 80 characters", domain.ErrInvalidInput)
	}
	return s.store.UpdateDeviceDisplayName(ctx, deviceID, userID, name)
}

// sanitizeDisplayName trims whitespace and removes control characters (NUL, CR, LF, etc.).
func sanitizeDisplayName(name string) string {
	name = strings.TrimSpace(name)
	var sb strings.Builder
	for _, r := range name {
		if unicode.IsControl(r) {
			continue
		}
		sb.WriteRune(r)
	}
	return sb.String()
}

// clampDeviceSessionLimit clamps limit to [1, 100]; 0 or negative defaults to 50.
func clampDeviceSessionLimit(limit int) int {
	if limit <= 0 {
		return 50
	}
	if limit > 100 {
		return 100
	}
	return limit
}
