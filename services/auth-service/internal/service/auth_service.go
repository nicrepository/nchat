package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/nicrepository/nchat/services/auth-service/internal/domain"
)

// SessionStore persists refresh token state for auth.user_sessions.
type SessionStore interface {
	RotateRefreshToken(ctx context.Context, oldHash string, newHash string, expiresAt time.Time) (domain.Session, error)
	RevokeRefreshToken(ctx context.Context, hash string) error
}

// AuthSessionManager is the HTTP-facing interface for refresh and logout.
type AuthSessionManager interface {
	Refresh(ctx context.Context, refreshToken string) (domain.TokenPair, error)
	Logout(ctx context.Context, refreshToken string) error
}

// AuthService implements access-token minting and refresh-token rotation.
type AuthService struct {
	tokens *TokenManager
	store  SessionStore
}

func NewAuthService(tokens *TokenManager, store SessionStore) *AuthService {
	return &AuthService{tokens: tokens, store: store}
}

func (s *AuthService) Refresh(ctx context.Context, refreshToken string) (domain.TokenPair, error) {
	refreshToken = strings.TrimSpace(refreshToken)
	if refreshToken == "" {
		return domain.TokenPair{}, fmt.Errorf("%w: refresh_token is required", domain.ErrInvalidInput)
	}

	newRefreshToken, newRefreshHash, refreshExpiresAt, err := s.tokens.GenerateRefreshToken()
	if err != nil {
		return domain.TokenPair{}, err
	}

	oldHash := s.tokens.HashRefreshToken(refreshToken)
	session, err := s.store.RotateRefreshToken(ctx, oldHash, newRefreshHash, refreshExpiresAt)
	if err != nil {
		return domain.TokenPair{}, err
	}

	accessToken, expiresIn, err := s.tokens.GenerateAccessToken(session.UserID, session.ID)
	if err != nil {
		return domain.TokenPair{}, err
	}

	return domain.TokenPair{
		AccessToken:  accessToken,
		RefreshToken: newRefreshToken,
		TokenType:    bearerTokenType,
		ExpiresIn:    expiresIn,
	}, nil
}

func (s *AuthService) Logout(ctx context.Context, refreshToken string) error {
	refreshToken = strings.TrimSpace(refreshToken)
	if refreshToken == "" {
		return fmt.Errorf("%w: refresh_token is required", domain.ErrInvalidInput)
	}
	return s.store.RevokeRefreshToken(ctx, s.tokens.HashRefreshToken(refreshToken))
}
