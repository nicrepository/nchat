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

type fakeInviteStore struct {
	userExists         bool
	activeInvite       bool
	policy             domain.PolicySettings
	createErr          error
	acceptErr          error
	createResult       domain.InviteResult
	acceptResult       domain.AcceptInviteResult
	checkedUserCalls   int
	checkedInviteCalls int
	checkedUserEmail   string
	checkedInviteEmail string

	createEmail       string
	createDisplayName string
	createFullName    string
	createTokenHash   string
	createPayload     string
	createExpiresAt   time.Time

	acceptTokenHash    string
	acceptDisplayName  string
	acceptFullName     string
	acceptPasswordHash string
}

func (f *fakeInviteStore) UserExistsByEmail(_ context.Context, email string) (bool, error) {
	f.checkedUserCalls++
	f.checkedUserEmail = email
	return f.userExists, nil
}

func (f *fakeInviteStore) ActiveInviteExistsByEmail(_ context.Context, email string) (bool, error) {
	f.checkedInviteCalls++
	f.checkedInviteEmail = email
	return f.activeInvite, nil
}

func (f *fakeInviteStore) GetPolicySettings(_ context.Context) (domain.PolicySettings, error) {
	return f.policy, nil
}

func (f *fakeInviteStore) CreateInvite(_ context.Context, email, displayName, fullName, tokenHash string, expiresAt time.Time, encryptedPayload string) (domain.InviteResult, error) {
	f.createEmail = email
	f.createDisplayName = displayName
	f.createFullName = fullName
	f.createTokenHash = tokenHash
	f.createPayload = encryptedPayload
	f.createExpiresAt = expiresAt
	return f.createResult, f.createErr
}

func (f *fakeInviteStore) AcceptInviteTx(_ context.Context, tokenHash, displayName, fullName, passwordHash string) (domain.AcceptInviteResult, error) {
	f.acceptTokenHash = tokenHash
	f.acceptDisplayName = displayName
	f.acceptFullName = fullName
	f.acceptPasswordHash = passwordHash
	return f.acceptResult, f.acceptErr
}

func TestInviteService_EmailHandoffAvailableReflectsEncryptor(t *testing.T) {
	manager := newTestTokenManager(t, strings.Repeat("i", 32))
	store := &fakeInviteStore{policy: defaultPolicy()}
	if service.NewInviteService(manager, store).EmailHandoffAvailable() {
		t.Fatal("expected handoff unavailable without encryptor")
	}
	if !service.NewInviteService(manager, store, service.WithInviteOutboxEncryptor(newTestEmailOutboxEncryptor(t))).EmailHandoffAvailable() {
		t.Fatal("expected handoff available with encryptor")
	}
}

func TestInviteService_CreateInviteCreatesHashedToken(t *testing.T) {
	manager := newTestTokenManager(t, strings.Repeat("w", 32))
	encryptor := newTestEmailOutboxEncryptor(t)
	store := &fakeInviteStore{policy: defaultPolicy(), createResult: domain.InviteResult{ID: "invite-1", Email: "user@example.com", CreatedAt: time.Now()}}
	svc := service.NewInviteService(manager, store, service.WithInviteOutboxEncryptor(encryptor))

	result, err := svc.CreateInvite(context.Background(), domain.AdminInviteInput{Email: " USER@Example.COM ", DisplayName: " User ", FullName: " User Full "})
	if err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}
	if result.ID != "invite-1" {
		t.Fatalf("unexpected result: %+v", result)
	}
	if store.checkedUserEmail != "user@example.com" || store.checkedInviteEmail != "user@example.com" || store.createEmail != "user@example.com" {
		t.Fatalf("expected normalized email, got user=%q invite=%q create=%q", store.checkedUserEmail, store.checkedInviteEmail, store.createEmail)
	}
	if store.createDisplayName != "User" || store.createFullName != "User Full" {
		t.Fatalf("expected trimmed names, got display=%q full=%q", store.createDisplayName, store.createFullName)
	}
	if store.createTokenHash == "" || len(store.createTokenHash) != 64 || strings.Contains(store.createTokenHash, "user@example.com") {
		t.Fatalf("expected hashed invite token, got %q", store.createTokenHash)
	}
	plaintext, err := encryptor.Decrypt(store.createPayload)
	if err != nil {
		t.Fatalf("decrypt invite outbox payload: %v", err)
	}
	if plaintext.Kind != "invite" || plaintext.ToEmail != "user@example.com" || plaintext.ActionPath != "/auth/invites/accept" {
		t.Fatalf("unexpected encrypted payload: %+v", plaintext)
	}
	if plaintext.Token == "" {
		t.Fatal("expected encrypted payload to contain invite token")
	}
	if strings.Contains(store.createPayload, plaintext.Token) {
		t.Fatalf("outbox envelope must not contain raw invite token: %s", store.createPayload)
	}
	if strings.Contains(store.createPayload, "/auth/invites/accept?token="+plaintext.Token) {
		t.Fatalf("outbox envelope must not contain full invite link token: %s", store.createPayload)
	}
	if !store.createExpiresAt.After(time.Now().Add(71 * time.Hour)) {
		t.Fatalf("expected invite expiry about 72 hours out, got %s", store.createExpiresAt)
	}
}

func TestInviteService_CreateInviteRejectsDuplicateUser(t *testing.T) {
	manager := newTestTokenManager(t, strings.Repeat("x", 32))
	store := &fakeInviteStore{userExists: true, policy: defaultPolicy()}
	svc := service.NewInviteService(manager, store, service.WithInviteOutboxEncryptor(newTestEmailOutboxEncryptor(t)))

	_, err := svc.CreateInvite(context.Background(), domain.AdminInviteInput{Email: "user@example.com", DisplayName: "User"})
	if !errors.Is(err, domain.ErrDuplicateEmail) {
		t.Fatalf("expected ErrDuplicateEmail, got %v", err)
	}
	if store.createTokenHash != "" {
		t.Fatal("duplicate user must not create invite token")
	}
}

func TestInviteService_CreateInviteRejectsActivePendingInvite(t *testing.T) {
	manager := newTestTokenManager(t, strings.Repeat("y", 32))
	store := &fakeInviteStore{activeInvite: true, policy: defaultPolicy()}
	svc := service.NewInviteService(manager, store, service.WithInviteOutboxEncryptor(newTestEmailOutboxEncryptor(t)))

	_, err := svc.CreateInvite(context.Background(), domain.AdminInviteInput{Email: "user@example.com", DisplayName: "User"})
	if !errors.Is(err, domain.ErrInviteAlreadyPending) {
		t.Fatalf("expected ErrInviteAlreadyPending, got %v", err)
	}
	if store.createTokenHash != "" {
		t.Fatal("pending invite must not rotate token in this PR")
	}
}

func TestInviteService_CreateInviteMissingOutboxKeyDisablesBeforeLookup(t *testing.T) {
	manager := newTestTokenManager(t, strings.Repeat("y", 32))
	store := &fakeInviteStore{policy: defaultPolicy()}
	svc := service.NewInviteService(manager, store)

	_, err := svc.CreateInvite(context.Background(), domain.AdminInviteInput{Email: "user@example.com", DisplayName: "User"})
	if !errors.Is(err, domain.ErrEmailOutboxUnavailable) {
		t.Fatalf("expected ErrEmailOutboxUnavailable, got %v", err)
	}
	if store.checkedUserCalls != 0 || store.checkedInviteCalls != 0 || store.createTokenHash != "" {
		t.Fatalf("missing outbox key must fail before lookup/token creation, userChecks=%d inviteChecks=%d tokenHash=%q", store.checkedUserCalls, store.checkedInviteCalls, store.createTokenHash)
	}
}

func TestInviteService_AcceptInviteHashesTokenAndPassword(t *testing.T) {
	manager := newTestTokenManager(t, strings.Repeat("z", 32))
	store := &fakeInviteStore{policy: defaultPolicy(), acceptResult: domain.AcceptInviteResult{UserID: "user-1", Email: "user@example.com", DisplayName: "User", CreatedAt: time.Now()}}
	svc := service.NewInviteService(manager, store)

	result, err := svc.AcceptInvite(context.Background(), domain.AcceptInviteInput{Token: "raw-invite-token", DisplayName: " User ", FullName: " User Full ", Password: "StrongPassword@123"})
	if err != nil {
		t.Fatalf("AcceptInvite: %v", err)
	}
	if result.UserID != "user-1" {
		t.Fatalf("unexpected result: %+v", result)
	}
	if store.acceptTokenHash == "" || store.acceptTokenHash == "raw-invite-token" || strings.Contains(store.acceptTokenHash, "raw-invite-token") {
		t.Fatalf("expected hashed invite token, got %q", store.acceptTokenHash)
	}
	if !strings.HasPrefix(store.acceptPasswordHash, "$argon2id$") || strings.Contains(store.acceptPasswordHash, "StrongPassword") {
		t.Fatalf("expected Argon2id password hash, got %q", store.acceptPasswordHash)
	}
	if store.acceptDisplayName != "User" || store.acceptFullName != "User Full" {
		t.Fatalf("expected trimmed names, got display=%q full=%q", store.acceptDisplayName, store.acceptFullName)
	}
}

func TestInviteService_AcceptInviteWeakPasswordRejected(t *testing.T) {
	manager := newTestTokenManager(t, strings.Repeat("a1", 16))
	store := &fakeInviteStore{policy: defaultPolicy()}
	svc := service.NewInviteService(manager, store)

	_, err := svc.AcceptInvite(context.Background(), domain.AcceptInviteInput{Token: "raw-invite-token", DisplayName: "User", Password: "weak"})
	if !errors.Is(err, domain.ErrPasswordPolicy) {
		t.Fatalf("expected ErrPasswordPolicy, got %v", err)
	}
	if store.acceptTokenHash != "" || store.acceptPasswordHash != "" {
		t.Fatal("weak password must not reach invite store")
	}
}
