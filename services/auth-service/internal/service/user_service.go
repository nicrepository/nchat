package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/nicrepository/nchat/services/auth-service/internal/domain"
	"github.com/nicrepository/nchat/services/auth-service/internal/storage"
)

// UserCreator is the interface for creating new users.
type UserCreator interface {
	CreateUser(ctx context.Context, input domain.CreateUserInput) (domain.User, error)
}

// UserStatusManager is the interface for admin status change operations.
type UserStatusManager interface {
	// UpdateUserStatus transitions targetID to newStatus.
	// callerID is the requesting admin's user ID (from JWT); pass "" when identity
	// is unavailable (e.g. AdminBootstrapGuard). Self-deactivation is rejected only
	// when callerID is non-empty and matches targetID.
	// Status update and session revocation (on suspension) are atomic.
	UpdateUserStatus(ctx context.Context, callerID, targetID, newStatus string) (domain.User, error)
}

// SelfProfileReader reads the authenticated user's own minimal profile.
type SelfProfileReader interface {
	GetProfile(ctx context.Context, userID string) (domain.SelfProfile, error)
}

// UserAdmin is the combined interface used by the auth HTTP handlers. It also
// carries the self-profile read so the router can serve GET /auth/me from the
// same user service without a new constructor parameter.
type UserAdmin interface {
	UserCreator
	UserStatusManager
	SelfProfileReader
}

// UserService implements UserCreator and UserStatusManager.
type UserService struct {
	store storage.UserStore
}

// NewUserService creates a UserService backed by the given store.
func NewUserService(store storage.UserStore) *UserService {
	return &UserService{store: store}
}

// GetProfile returns the authenticated user's own minimal profile. The userID
// comes from the session; there is no lookup by any client-supplied identifier.
func (s *UserService) GetProfile(ctx context.Context, userID string) (domain.SelfProfile, error) {
	return s.store.GetSelfProfile(ctx, userID)
}

func (s *UserService) CreateUser(ctx context.Context, input domain.CreateUserInput) (domain.User, error) {
	input.Email = domain.NormalizeEmail(input.Email)
	input.DisplayName = strings.TrimSpace(input.DisplayName)
	input.FullName = strings.TrimSpace(input.FullName)

	if err := domain.ValidateEmail(input.Email); err != nil {
		return domain.User{}, err
	}
	if input.DisplayName == "" {
		return domain.User{}, fmt.Errorf("%w: display_name is required", domain.ErrInvalidInput)
	}
	if input.InitialPassword == "" {
		return domain.User{}, fmt.Errorf("%w: initial_password is required", domain.ErrInvalidInput)
	}

	policy, err := s.store.GetPolicySettings(ctx)
	if err != nil {
		return domain.User{}, fmt.Errorf("get policy: %w", err)
	}

	if err := domain.ValidatePassword(input.InitialPassword, policy); err != nil {
		return domain.User{}, err
	}

	hash, err := HashPassword(input.InitialPassword)
	if err != nil {
		return domain.User{}, fmt.Errorf("hash password: %w", err)
	}

	return s.store.CreateUser(ctx, input, hash)
}

// UpdateUserStatus transitions targetID to newStatus. Status change and session
// revocation (on suspension) are performed atomically by the storage layer.
// Returns ErrForbidden if callerID is non-empty and equals targetID.
// Returns ErrNotFound if the user does not exist.
// Returns ErrStatusTransitionNotAllowed for unsupported transitions.
func (s *UserService) UpdateUserStatus(ctx context.Context, callerID, targetID, newStatus string) (domain.User, error) {
	if callerID != "" && callerID == targetID {
		return domain.User{}, domain.ErrForbidden
	}
	return s.store.UpdateUserStatus(ctx, targetID, newStatus)
}
