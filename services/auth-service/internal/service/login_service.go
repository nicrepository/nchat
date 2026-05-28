package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/nicrepository/nchat/services/auth-service/internal/domain"
)

// LoginStore is the persistence interface for creating login sessions.
type LoginStore interface {
	CreateLoginSession(ctx context.Context, input domain.CreateSessionInput) (domain.CreatedLoginSession, error)
}

// LoginManager is the HTTP-facing interface for the login endpoint.
type LoginManager interface {
	Login(ctx context.Context, input domain.LoginInput) (domain.LoginResult, error)
}

// LoginService implements the email/password login flow.
type LoginService struct {
	tokens *TokenManager
	store  LoginStore
}

// NewLoginService creates a LoginService backed by the given token manager and login store.
func NewLoginService(tokens *TokenManager, store LoginStore) *LoginService {
	return &LoginService{tokens: tokens, store: store}
}

// Login validates the input, delegates credential verification to the store, and returns
// a LoginResult with access/refresh tokens. Sensitive fields are hashed before storage.
func (s *LoginService) Login(ctx context.Context, input domain.LoginInput) (domain.LoginResult, error) {
	input.Email = domain.NormalizeEmail(input.Email)
	input.DeviceFingerprint = strings.TrimSpace(input.DeviceFingerprint)
	input.DeviceName = strings.TrimSpace(input.DeviceName)
	input.Platform = strings.TrimSpace(input.Platform)

	if err := domain.ValidateEmail(input.Email); err != nil {
		return domain.LoginResult{}, err
	}
	if input.Password == "" {
		return domain.LoginResult{}, fmt.Errorf("%w: password is required", domain.ErrInvalidInput)
	}

	refreshToken, refreshHash, refreshExpiresAt, err := s.tokens.GenerateRefreshToken()
	if err != nil {
		return domain.LoginResult{}, err
	}

	sessionInput := domain.CreateSessionInput{
		Password:              input.Password,
		Email:                 input.Email,
		RefreshTokenHash:      refreshHash,
		RefreshExpiresAt:      refreshExpiresAt,
		DeviceFingerprintHash: s.tokens.HashDeviceFingerprint(input.DeviceFingerprint),
		DeviceName:            input.DeviceName,
		Platform:              input.Platform,
		IPAddress:             input.IPAddress,
		UserAgent:             input.UserAgent,
	}

	created, err := s.store.CreateLoginSession(ctx, sessionInput)
	if err != nil {
		return domain.LoginResult{}, err
	}

	accessToken, expiresIn, err := s.tokens.GenerateAccessToken(created.Session.UserID, created.Session.ID)
	if err != nil {
		return domain.LoginResult{}, err
	}

	return domain.LoginResult{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		TokenType:    bearerTokenType,
		ExpiresIn:    expiresIn,
		User:         created.User,
	}, nil
}
