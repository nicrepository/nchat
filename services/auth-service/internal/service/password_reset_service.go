package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/nicrepository/nchat/services/auth-service/internal/domain"
)

const defaultPasswordResetTokenTTLMinutes = 60

// PasswordResetStore persists password reset state without receiving plaintext passwords or tokens.
type PasswordResetStore interface {
	GetActiveUserForPasswordReset(ctx context.Context, email string) (userID string, found bool, err error)
	GetPolicySettings(ctx context.Context) (domain.PolicySettings, error)
	CreatePasswordResetToken(ctx context.Context, userID, email, tokenHash string, expiresAt time.Time) error
	ResetPasswordTx(ctx context.Context, tokenHash, newPasswordHash string) error
}

// PasswordRecoveryManager is the HTTP-facing password recovery contract.
type PasswordRecoveryManager interface {
	ForgotPassword(ctx context.Context, input domain.ForgotPasswordInput) error
	ResetPassword(ctx context.Context, input domain.ResetPasswordInput) error
}

// PasswordResetService implements enumeration-safe password recovery.
type PasswordResetService struct {
	tokens *TokenManager
	store  PasswordResetStore
}

func NewPasswordResetService(tokens *TokenManager, store PasswordResetStore) *PasswordResetService {
	return &PasswordResetService{tokens: tokens, store: store}
}

func (s *PasswordResetService) ForgotPassword(ctx context.Context, input domain.ForgotPasswordInput) error {
	email := domain.NormalizeEmail(input.Email)
	if err := domain.ValidateEmail(email); err != nil {
		return nil
	}

	userID, found, err := s.store.GetActiveUserForPasswordReset(ctx, email)
	if err != nil {
		return fmt.Errorf("get reset user: %w", err)
	}
	if !found {
		RunDummyPasswordVerification("")
		return nil
	}

	policy, err := s.store.GetPolicySettings(ctx)
	if err != nil {
		return fmt.Errorf("get policy: %w", err)
	}
	resetTTLMinutes := policy.PasswordResetTokenTTLMinutes
	if resetTTLMinutes <= 0 {
		resetTTLMinutes = defaultPasswordResetTokenTTLMinutes
	}

	rawToken, err := s.tokens.GenerateOpaqueToken()
	if err != nil {
		return err
	}
	tokenHash := s.tokens.HashPasswordResetToken(rawToken)
	expiresAt := time.Now().UTC().Add(time.Duration(resetTTLMinutes) * time.Minute)

	if err := s.store.CreatePasswordResetToken(ctx, userID, email, tokenHash, expiresAt); err != nil {
		return fmt.Errorf("create password reset token: %w", err)
	}
	return nil
}

func (s *PasswordResetService) ResetPassword(ctx context.Context, input domain.ResetPasswordInput) error {
	token := strings.TrimSpace(input.Token)
	if token == "" {
		return fmt.Errorf("%w: token is required", domain.ErrInvalidInput)
	}
	if input.NewPassword == "" {
		return fmt.Errorf("%w: new_password is required", domain.ErrInvalidInput)
	}

	policy, err := s.store.GetPolicySettings(ctx)
	if err != nil {
		return fmt.Errorf("get policy: %w", err)
	}
	if err := domain.ValidatePassword(input.NewPassword, policy); err != nil {
		return err
	}
	passwordHash, err := HashPassword(input.NewPassword)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}

	tokenHash := s.tokens.HashPasswordResetToken(token)
	if err := s.store.ResetPasswordTx(ctx, tokenHash, passwordHash); err != nil {
		return err
	}
	return nil
}
