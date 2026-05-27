package service_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nicrepository/nchat/services/auth-service/internal/domain"
	"github.com/nicrepository/nchat/services/auth-service/internal/service"
)

type fakeLoginStore struct {
	input  domain.CreateSessionInput
	result domain.CreatedLoginSession
	err    error
}

func (f *fakeLoginStore) CreateLoginSession(_ context.Context, input domain.CreateSessionInput) (domain.CreatedLoginSession, error) {
	f.input = input
	return f.result, f.err
}

func TestLoginService_LoginCreatesSessionWithHashedRefreshAndDeviceFingerprint(t *testing.T) {
	manager := newTestTokenManager(t, strings.Repeat("l", 32))
	store := &fakeLoginStore{result: domain.CreatedLoginSession{
		Session: domain.Session{ID: "session-1", UserID: "user-1"},
		User:    domain.LoginUser{ID: "user-1", Email: "user@example.com", DisplayName: "User", MustChangePassword: true},
	}}
	login := service.NewLoginService(manager, store)

	result, err := login.Login(context.Background(), domain.LoginInput{
		Email: "  USER@EXAMPLE.COM  ", Password: "ChangeMe@123",
		DeviceFingerprint: "raw-device", DeviceName: "Laptop", Platform: "linux",
		IPAddress: "203.0.113.10", UserAgent: "test-agent",
	})
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if store.input.Email != "user@example.com" {
		t.Fatalf("expected normalized email, got %q", store.input.Email)
	}
	if store.input.RefreshTokenHash == "" || store.input.RefreshTokenHash == result.RefreshToken {
		t.Fatal("refresh token must be hashed before storage")
	}
	if store.input.DeviceFingerprintHash == "" || store.input.DeviceFingerprintHash == "raw-device" {
		t.Fatal("device fingerprint must be hashed before storage")
	}
	if result.User.Email != "user@example.com" || !result.User.MustChangePassword {
		t.Fatalf("unexpected safe user: %+v", result.User)
	}
	if result.AccessToken == "" || result.RefreshToken == "" || result.TokenType != "Bearer" || result.ExpiresIn != 900 {
		t.Fatalf("unexpected token result: %+v", result)
	}
}

func TestLoginService_LoginRequiresEmailAndPassword(t *testing.T) {
	manager := newTestTokenManager(t, strings.Repeat("m", 32))
	login := service.NewLoginService(manager, &fakeLoginStore{})
	if _, err := login.Login(context.Background(), domain.LoginInput{Password: "x"}); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("expected invalid input for missing email, got %v", err)
	}
	if _, err := login.Login(context.Background(), domain.LoginInput{Email: "user@example.com"}); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("expected invalid input for missing password, got %v", err)
	}
}

func TestLoginService_LoginPropagatesInvalidCredentials(t *testing.T) {
	manager := newTestTokenManager(t, strings.Repeat("o", 32))
	login := service.NewLoginService(manager, &fakeLoginStore{err: domain.ErrInvalidCredentials})
	_, err := login.Login(context.Background(), domain.LoginInput{Email: "user@example.com", Password: "ChangeMe@123"})
	if !errors.Is(err, domain.ErrInvalidCredentials) {
		t.Fatalf("expected invalid credentials, got %v", err)
	}
}

var _ = time.Time{}
