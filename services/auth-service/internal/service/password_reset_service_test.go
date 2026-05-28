//nolint:gosec // Test fixtures intentionally use example token/password strings.
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

type fakePasswordResetStore struct {
	activeUserID string
	activeFound  bool
	policy       domain.PolicySettings
	policyErr    error
	createErr    error
	resetErr     error

	lookupEmail  string
	createCalls  int
	createUserID string
	createEmail  string
	createHash   string
	createExpiry time.Time

	resetTokenHash    string
	resetPasswordHash string
}

func (f *fakePasswordResetStore) GetActiveUserForPasswordReset(_ context.Context, email string) (string, bool, error) {
	f.lookupEmail = email
	return f.activeUserID, f.activeFound, nil
}

func (f *fakePasswordResetStore) GetPolicySettings(_ context.Context) (domain.PolicySettings, error) {
	return f.policy, f.policyErr
}

func (f *fakePasswordResetStore) CreatePasswordResetToken(_ context.Context, userID, email, tokenHash string, expiresAt time.Time) error {
	f.createCalls++
	f.createUserID = userID
	f.createEmail = email
	f.createHash = tokenHash
	f.createExpiry = expiresAt
	return f.createErr
}

func (f *fakePasswordResetStore) ResetPasswordTx(_ context.Context, tokenHash, newPasswordHash string) error {
	f.resetTokenHash = tokenHash
	f.resetPasswordHash = newPasswordHash
	return f.resetErr
}

func TestPasswordResetService_ForgotPasswordKnownActiveUserCreatesHashedToken(t *testing.T) {
	manager := newTestTokenManager(t, strings.Repeat("q", 32))
	store := &fakePasswordResetStore{activeUserID: "user-1", activeFound: true, policy: defaultPolicy()}
	svc := service.NewPasswordResetService(manager, store)

	if err := svc.ForgotPassword(context.Background(), domain.ForgotPasswordInput{Email: "  USER@Example.COM  "}); err != nil {
		t.Fatalf("ForgotPassword: %v", err)
	}
	if store.lookupEmail != "user@example.com" || store.createEmail != "user@example.com" {
		t.Fatalf("expected normalized email, lookup=%q create=%q", store.lookupEmail, store.createEmail)
	}
	if store.createCalls != 1 || store.createUserID != "user-1" {
		t.Fatalf("expected one reset token for user-1, got calls=%d user=%q", store.createCalls, store.createUserID)
	}
	if store.createHash == "" || len(store.createHash) != 64 {
		t.Fatalf("expected HMAC-SHA-256 token hash, got %q", store.createHash)
	}
	if strings.Contains(store.createHash, "USER") || strings.Contains(store.createHash, "user@example.com") {
		t.Fatalf("token hash must not contain email or raw token material: %q", store.createHash)
	}
	if !store.createExpiry.After(time.Now().Add(59 * time.Minute)) {
		t.Fatalf("expected reset token expiry about 60 minutes out, got %s", store.createExpiry)
	}
}

func TestPasswordResetService_ForgotPasswordUnknownUserGenericSuccessNoToken(t *testing.T) {
	manager := newTestTokenManager(t, strings.Repeat("r", 32))
	store := &fakePasswordResetStore{activeFound: false, policy: defaultPolicy()}
	svc := service.NewPasswordResetService(manager, store)

	if err := svc.ForgotPassword(context.Background(), domain.ForgotPasswordInput{Email: "missing@example.com"}); err != nil {
		t.Fatalf("ForgotPassword should be generic success: %v", err)
	}
	if store.createCalls != 0 || store.createHash != "" {
		t.Fatalf("unknown user must not create token, calls=%d hash=%q", store.createCalls, store.createHash)
	}
}

func TestPasswordResetService_ForgotPasswordInvalidEmailGenericSuccessNoToken(t *testing.T) {
	manager := newTestTokenManager(t, strings.Repeat("s", 32))
	store := &fakePasswordResetStore{activeFound: true, activeUserID: "user-1", policy: defaultPolicy()}
	svc := service.NewPasswordResetService(manager, store)

	if err := svc.ForgotPassword(context.Background(), domain.ForgotPasswordInput{Email: "not-an-email"}); err != nil {
		t.Fatalf("ForgotPassword should hide invalid email: %v", err)
	}
	if store.lookupEmail != "" || store.createCalls != 0 {
		t.Fatalf("invalid email must not lookup/create token, lookup=%q calls=%d", store.lookupEmail, store.createCalls)
	}
}

func TestPasswordResetService_ResetPasswordValidTokenHashesPassword(t *testing.T) {
	manager := newTestTokenManager(t, strings.Repeat("t", 32))
	store := &fakePasswordResetStore{policy: defaultPolicy()}
	svc := service.NewPasswordResetService(manager, store)

	err := svc.ResetPassword(context.Background(), domain.ResetPasswordInput{Token: "raw-reset-token", NewPassword: "NewStrongPassword@123"})
	if err != nil {
		t.Fatalf("ResetPassword: %v", err)
	}
	if store.resetTokenHash == "" || store.resetTokenHash == "raw-reset-token" || strings.Contains(store.resetTokenHash, "raw-reset-token") {
		t.Fatalf("expected hashed reset token, got %q", store.resetTokenHash)
	}
	if store.resetPasswordHash == "" || store.resetPasswordHash == "NewStrongPassword@123" || strings.Contains(store.resetPasswordHash, "NewStrongPassword") {
		t.Fatalf("expected Argon2id password hash, got %q", store.resetPasswordHash)
	}
	if !strings.HasPrefix(store.resetPasswordHash, "$argon2id$") {
		t.Fatalf("expected Argon2id PHC hash, got %q", store.resetPasswordHash)
	}
}

func TestPasswordResetService_ResetPasswordWeakPasswordRejected(t *testing.T) {
	manager := newTestTokenManager(t, strings.Repeat("u", 32))
	store := &fakePasswordResetStore{policy: defaultPolicy()}
	svc := service.NewPasswordResetService(manager, store)

	err := svc.ResetPassword(context.Background(), domain.ResetPasswordInput{Token: "raw-reset-token", NewPassword: "weak"})
	if !errors.Is(err, domain.ErrPasswordPolicy) {
		t.Fatalf("expected ErrPasswordPolicy, got %v", err)
	}
	if store.resetTokenHash != "" || store.resetPasswordHash != "" {
		t.Fatal("weak password must not reach reset store")
	}
}

func TestPasswordResetService_ResetPasswordInvalidTokenPropagatesGenericError(t *testing.T) {
	manager := newTestTokenManager(t, strings.Repeat("v", 32))
	store := &fakePasswordResetStore{policy: defaultPolicy(), resetErr: domain.ErrInvalidToken}
	svc := service.NewPasswordResetService(manager, store)

	err := svc.ResetPassword(context.Background(), domain.ResetPasswordInput{Token: "bad-token", NewPassword: "NewStrongPassword@123"})
	if !errors.Is(err, domain.ErrInvalidToken) {
		t.Fatalf("expected ErrInvalidToken, got %v", err)
	}
}
