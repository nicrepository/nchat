package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/nicrepository/nchat/services/auth-service/internal/domain"
	"github.com/nicrepository/nchat/services/auth-service/internal/storage"
)

// UserCreator is the interface the HTTP handler depends on.
type UserCreator interface {
	CreateUser(ctx context.Context, input domain.CreateUserInput) (domain.User, error)
}

// UserService implements UserCreator.
type UserService struct {
	store storage.UserStore
}

func NewUserService(store storage.UserStore) *UserService {
	return &UserService{store: store}
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
