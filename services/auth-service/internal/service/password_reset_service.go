package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/nicrepository/nchat/services/auth-service/internal/domain"
)

const (
	defaultPasswordResetTokenTTLMinutes = 60
	passwordResetActionPath             = "/auth/password/reset"
)

// PasswordResetStore persists password reset state without receiving plaintext passwords or tokens.
type PasswordResetStore interface {
	GetActiveUserForPasswordReset(ctx context.Context, email string) (userID string, found bool, err error)
	GetPolicySettings(ctx context.Context) (domain.PolicySettings, error)
	CreatePasswordResetToken(ctx context.Context, userID, email, tokenHash string, expiresAt time.Time, encryptedPayload string) error
	ResetPasswordTx(ctx context.Context, tokenHash, newPasswordHash string) error
}

// PasswordRecoveryManager is the HTTP-facing password recovery contract.
type PasswordRecoveryManager interface {
	ForgotPassword(ctx context.Context, input domain.ForgotPasswordInput) error
	ResetPassword(ctx context.Context, input domain.ResetPasswordInput) error
}

type PasswordResetOption func(*PasswordResetService)

func WithPasswordResetOutboxEncryptor(encryptor *EmailOutboxEncryptor) PasswordResetOption {
	return func(s *PasswordResetService) {
		s.emailOutbox = encryptor
	}
}

func WithPasswordResetDummyWork(dummyWork func()) PasswordResetOption {
	return func(s *PasswordResetService) {
		if dummyWork != nil {
			s.dummyWork = dummyWork
		}
	}
}

// PasswordResetService implements enumeration-safe password recovery.
type PasswordResetService struct {
	tokens      *TokenManager
	store       PasswordResetStore
	emailOutbox *EmailOutboxEncryptor
	dummyWork   func()
}

func NewPasswordResetService(tokens *TokenManager, store PasswordResetStore, opts ...PasswordResetOption) *PasswordResetService {
	svc := &PasswordResetService{
		tokens:    tokens,
		store:     store,
		dummyWork: func() { RunDummyPasswordVerification("") },
	}
	for _, opt := range opts {
		opt(svc)
	}
	return svc
}

func (s *PasswordResetService) EmailHandoffAvailable() bool {
	return s != nil && s.emailOutbox != nil
}

func (s *PasswordResetService) ForgotPassword(ctx context.Context, input domain.ForgotPasswordInput) error {
	if s.emailOutbox == nil {
		return domain.ErrEmailOutboxUnavailable
	}

	email := domain.NormalizeEmail(input.Email)
	if err := domain.ValidateEmail(email); err != nil {
		return nil
	}

	rawToken, err := s.tokens.GenerateOpaqueToken()
	if err != nil {
		return err
	}
	tokenHash := s.tokens.HashPasswordResetToken(rawToken)
	s.dummyWork()

	userID, found, err := s.store.GetActiveUserForPasswordReset(ctx, email)
	if err != nil {
		return fmt.Errorf("get reset user: %w", err)
	}
	if !found {
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
	expiresAt := time.Now().UTC().Add(time.Duration(resetTTLMinutes) * time.Minute)
	encryptedPayload, err := s.emailOutbox.Encrypt(EmailOutboxPlaintext{
		Kind:       "password_reset",
		Token:      rawToken,
		ActionPath: passwordResetActionPath,
		ToEmail:    email,
		ExpiresAt:  expiresAt,
	})
	if err != nil {
		return fmt.Errorf("encrypt password reset outbox payload: %w", err)
	}

	if err := s.store.CreatePasswordResetToken(ctx, userID, email, tokenHash, expiresAt, encryptedPayload); err != nil {
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
