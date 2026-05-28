package service_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/nicrepository/nchat/services/auth-service/internal/domain"
	"github.com/nicrepository/nchat/services/auth-service/internal/service"
)

type fakeSessionStore struct {
	session domain.Session
	err     error

	rotateOldHash string
	rotateNewHash string
	revokeHash    string
}

func (f *fakeSessionStore) RotateRefreshToken(_ context.Context, oldHash string, newHash string) (domain.Session, error) {
	f.rotateOldHash = oldHash
	f.rotateNewHash = newHash
	return f.session, f.err
}

func (f *fakeSessionStore) RevokeRefreshToken(_ context.Context, hash string) error {
	f.revokeHash = hash
	return f.err
}

func TestAuthService_RefreshRotatesToken(t *testing.T) {
	manager := newTestTokenManager(t, strings.Repeat("f", 32))
	store := &fakeSessionStore{session: domain.Session{ID: "session-456", UserID: "user-123"}}
	auth := service.NewAuthService(manager, store)

	pair, err := auth.Refresh(context.Background(), "old-refresh-token")
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	if store.rotateOldHash == "" {
		t.Fatal("expected old refresh token hash")
	}
	if store.rotateOldHash == "old-refresh-token" {
		t.Fatal("old refresh token must be hashed before storage lookup")
	}
	if store.rotateNewHash == "" {
		t.Fatal("expected new refresh token hash")
	}
	if store.rotateNewHash == pair.RefreshToken {
		t.Fatal("new refresh token hash must not equal raw response token")
	}
	if store.rotateOldHash == store.rotateNewHash {
		t.Fatal("refresh must rotate to a new token hash")
	}
	if pair.TokenType != "Bearer" {
		t.Fatalf("expected Bearer token type, got %q", pair.TokenType)
	}
	if pair.ExpiresIn != 900 {
		t.Fatalf("expected expires_in 900, got %d", pair.ExpiresIn)
	}
	if pair.AccessToken == "" || pair.RefreshToken == "" {
		t.Fatalf("expected access and refresh tokens, got %+v", pair)
	}
	claims, err := manager.ValidateAccessToken(pair.AccessToken)
	if err != nil {
		t.Fatalf("ValidateAccessToken: %v", err)
	}
	if claims.Subject != "user-123" || claims.SessionID != "session-456" {
		t.Fatalf("unexpected claims: %+v", claims)
	}
}

func TestAuthService_RefreshRejectsIdleExpiredSession(t *testing.T) {
	assertRefreshRejectsInvalidSession(t, "idle-expired-token")
}

func TestAuthService_RefreshRejectsAbsoluteExpiredSession(t *testing.T) {
	assertRefreshRejectsInvalidSession(t, "absolute-expired-token")
}

func TestAuthService_RefreshRejectsRevokedSession(t *testing.T) {
	assertRefreshRejectsInvalidSession(t, "revoked-token")
}

func assertRefreshRejectsInvalidSession(t *testing.T, refreshToken string) {
	t.Helper()
	manager := newTestTokenManager(t, strings.Repeat("g", 32))
	store := &fakeSessionStore{err: domain.ErrInvalidRefreshToken}
	auth := service.NewAuthService(manager, store)

	_, err := auth.Refresh(context.Background(), refreshToken)
	if !errors.Is(err, domain.ErrInvalidRefreshToken) {
		t.Fatalf("expected ErrInvalidRefreshToken, got %v", err)
	}
}

func TestAuthService_LogoutRevokesRefreshToken(t *testing.T) {
	manager := newTestTokenManager(t, strings.Repeat("h", 32))
	store := &fakeSessionStore{}
	auth := service.NewAuthService(manager, store)

	if err := auth.Logout(context.Background(), "raw-refresh-token"); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if store.revokeHash == "" {
		t.Fatal("expected revoke hash")
	}
	if store.revokeHash == "raw-refresh-token" {
		t.Fatal("logout must hash refresh token before revoking")
	}
}

func TestAuthService_RefreshRequiresToken(t *testing.T) {
	manager := newTestTokenManager(t, strings.Repeat("i", 32))
	store := &fakeSessionStore{}
	auth := service.NewAuthService(manager, store)

	_, err := auth.Refresh(context.Background(), "")
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput, got %v", err)
	}
}

func TestAuthService_LogoutRequiresToken(t *testing.T) {
	manager := newTestTokenManager(t, strings.Repeat("n", 32))
	store := &fakeSessionStore{}
	auth := service.NewAuthService(manager, store)

	err := auth.Logout(context.Background(), "")
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput, got %v", err)
	}
}
