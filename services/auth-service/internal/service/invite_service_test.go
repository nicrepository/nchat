//nolint:gosec // Test fixtures intentionally use example opaque/password strings.
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
	policy       domain.PolicySettings
	createErr    error
	acceptErr    error
	createResult domain.InviteResult
	acceptResult domain.AcceptInviteResult
	createCalls  int

	hasAdmin            bool
	hasAdminErr         error
	hasAdminWorkspaceID string

	createInput     domain.AdminInviteInput
	createEmail     string
	createTokenHash string
	createPayload   string
	createExpiresAt time.Time
	createLimit     domain.InviteRateLimit

	acceptTokenHash    string
	acceptDisplayName  string
	acceptFullName     string
	acceptPasswordHash string
}

func (f *fakeInviteStore) WorkspaceHasAdmin(_ context.Context, workspaceID string) (bool, error) {
	f.hasAdminWorkspaceID = workspaceID
	return f.hasAdmin, f.hasAdminErr
}

func (f *fakeInviteStore) GetPolicySettings(_ context.Context) (domain.PolicySettings, error) {
	return f.policy, nil
}

func (f *fakeInviteStore) CreateInvite(_ context.Context, input domain.AdminInviteInput, tokenHash string, expiresAt time.Time, encryptedPayload string, limit domain.InviteRateLimit) (domain.InviteResult, error) {
	f.createCalls++
	f.createInput = input
	f.createEmail = input.Email
	f.createTokenHash = tokenHash
	f.createPayload = encryptedPayload
	f.createExpiresAt = expiresAt
	f.createLimit = limit
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

	result, err := svc.CreateInvite(context.Background(), domain.AdminInviteInput{WorkspaceID: testWorkspaceID, ActorID: testActorID, Email: " USER@Example.COM ", DisplayName: " User ", FullName: " User Full "})
	if err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}
	if result.ID != "invite-1" {
		t.Fatalf("unexpected result: %+v", result)
	}
	if store.createEmail != "user@example.com" {
		t.Fatalf("expected normalized email, got %q", store.createEmail)
	}
	if store.createInput.DisplayName != "User" || store.createInput.FullName != "User Full" {
		t.Fatalf("expected trimmed names, got display=%q full=%q", store.createInput.DisplayName, store.createInput.FullName)
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
	if strings.Contains(store.createPayload, "/auth/invites/accept?to"+"ken="+plaintext.Token) {
		t.Fatalf("outbox envelope must not contain full invite link token: %s", store.createPayload)
	}
	if !store.createExpiresAt.After(time.Now().Add(71 * time.Hour)) {
		t.Fatalf("expected invite expiry about 72 hours out, got %s", store.createExpiresAt)
	}
}

func TestInviteService_CreateInviteRejectsDuplicateUser(t *testing.T) {
	manager := newTestTokenManager(t, strings.Repeat("x", 32))
	store := &fakeInviteStore{policy: defaultPolicy(), createErr: domain.ErrAlreadyMember}
	svc := service.NewInviteService(manager, store, service.WithInviteOutboxEncryptor(newTestEmailOutboxEncryptor(t)))

	_, err := svc.CreateInvite(context.Background(), domain.AdminInviteInput{WorkspaceID: testWorkspaceID, ActorID: testActorID, Email: "user@example.com", DisplayName: "User"})
	if !errors.Is(err, domain.ErrAlreadyMember) {
		t.Fatalf("expected ErrAlreadyMember, got %v", err)
	}
}

func TestInviteService_CreateInviteRejectsActivePendingInvite(t *testing.T) {
	manager := newTestTokenManager(t, strings.Repeat("y", 32))
	store := &fakeInviteStore{policy: defaultPolicy(), createErr: domain.ErrInviteAlreadyPending}
	svc := service.NewInviteService(manager, store, service.WithInviteOutboxEncryptor(newTestEmailOutboxEncryptor(t)))

	_, err := svc.CreateInvite(context.Background(), domain.AdminInviteInput{WorkspaceID: testWorkspaceID, ActorID: testActorID, Email: "user@example.com", DisplayName: "User"})
	if !errors.Is(err, domain.ErrInviteAlreadyPending) {
		t.Fatalf("expected ErrInviteAlreadyPending, got %v", err)
	}

}

func TestInviteService_CreateInviteMissingOutboxKeyDisablesBeforeLookup(t *testing.T) {
	manager := newTestTokenManager(t, strings.Repeat("y", 32))
	store := &fakeInviteStore{policy: defaultPolicy()}
	svc := service.NewInviteService(manager, store)

	_, err := svc.CreateInvite(context.Background(), domain.AdminInviteInput{WorkspaceID: testWorkspaceID, ActorID: testActorID, Email: "user@example.com", DisplayName: "User"})
	if !errors.Is(err, domain.ErrEmailOutboxUnavailable) {
		t.Fatalf("expected ErrEmailOutboxUnavailable, got %v", err)
	}
	if store.createCalls != 0 || store.createTokenHash != "" {
		t.Fatalf("missing outbox key must fail before token creation, createCalls=%d tokenHash=%q", store.createCalls, store.createTokenHash)
	}
}

func TestInviteService_AcceptInviteHashesTokenAndPassword(t *testing.T) {
	manager := newTestTokenManager(t, strings.Repeat("z", 32))
	store := &fakeInviteStore{policy: defaultPolicy(), acceptResult: domain.AcceptInviteResult{UserID: "user-1", Email: "user@example.com", DisplayName: "User", CreatedAt: time.Now()}}
	svc := service.NewInviteService(manager, store)
	submitted := makeTestOpaqueValue("invite-service-valid")

	result, err := svc.AcceptInvite(context.Background(), domain.AcceptInviteInput{Token: submitted, DisplayName: " User ", FullName: " User Full ", Password: "StrongPassword@123"})
	if err != nil {
		t.Fatalf("AcceptInvite: %v", err)
	}
	if result.UserID != "user-1" {
		t.Fatalf("unexpected result: %+v", result)
	}
	if store.acceptTokenHash == "" || store.acceptTokenHash == submitted || strings.Contains(store.acceptTokenHash, submitted) {
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
	submitted := makeTestOpaqueValue("invite-service-weak-password")

	_, err := svc.AcceptInvite(context.Background(), domain.AcceptInviteInput{Token: submitted, DisplayName: "User", Password: "weak"})
	if !errors.Is(err, domain.ErrPasswordPolicy) {
		t.Fatalf("expected ErrPasswordPolicy, got %v", err)
	}
	if store.acceptTokenHash != "" || store.acceptPasswordHash != "" {
		t.Fatal("weak password must not reach invite store")
	}
}

// ── Workspace authority and rate limit (issue #425) ────────────────────────

const (
	testWorkspaceID = "9a8b7c6d-5e4f-4a3b-8c2d-1e0f9a8b7c6d"
	testActorID     = "3f1c2d4e-5a6b-4c8d-9e0f-1a2b3c4d5e6f"
)

// The workspace and actor reach the store untouched. They are the whole
// authority of the operation, so a service that dropped or defaulted either
// would create an invite nobody authorized.
func TestInviteService_CreateInvitePassesWorkspaceAndActorThrough(t *testing.T) {
	manager := newTestTokenManager(t, strings.Repeat("q", 32))
	store := &fakeInviteStore{policy: defaultPolicy(), createResult: domain.InviteResult{ID: "invite-1"}}
	svc := service.NewInviteService(manager, store, service.WithInviteOutboxEncryptor(newTestEmailOutboxEncryptor(t)))

	if _, err := svc.CreateInvite(context.Background(), domain.AdminInviteInput{
		WorkspaceID: testWorkspaceID, ActorID: testActorID,
		Email: "user@example.com", DisplayName: "User",
	}); err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}

	if store.createInput.WorkspaceID != testWorkspaceID {
		t.Fatalf("expected workspace %q, got %q", testWorkspaceID, store.createInput.WorkspaceID)
	}
	if store.createInput.ActorID != testActorID {
		t.Fatalf("expected actor %q, got %q", testActorID, store.createInput.ActorID)
	}
}

// Missing authority is a refusal, never a default workspace.
func TestInviteService_CreateInviteRequiresWorkspaceAndActor(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input domain.AdminInviteInput
	}{
		{"no workspace", domain.AdminInviteInput{ActorID: testActorID, Email: "u@example.com", DisplayName: "U"}},
		{"blank workspace", domain.AdminInviteInput{WorkspaceID: "  ", ActorID: testActorID, Email: "u@example.com", DisplayName: "U"}},
		{"no actor", domain.AdminInviteInput{WorkspaceID: testWorkspaceID, Email: "u@example.com", DisplayName: "U"}},
		{"blank actor", domain.AdminInviteInput{WorkspaceID: testWorkspaceID, ActorID: " ", Email: "u@example.com", DisplayName: "U"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manager := newTestTokenManager(t, strings.Repeat("r", 32))
			store := &fakeInviteStore{policy: defaultPolicy()}
			svc := service.NewInviteService(manager, store, service.WithInviteOutboxEncryptor(newTestEmailOutboxEncryptor(t)))

			_, err := svc.CreateInvite(context.Background(), tc.input)
			if !errors.Is(err, domain.ErrInvalidInput) {
				t.Fatalf("expected ErrInvalidInput, got %v", err)
			}
			if store.createCalls != 0 {
				t.Fatal("an invite without verified authority must not reach the store")
			}
		})
	}
}

// The configured budget must reach the store: it is enforced there, inside the
// creating transaction, so a service that failed to pass it would silently
// disable the limit.
func TestInviteService_CreateInvitePassesRateLimitToStore(t *testing.T) {
	manager := newTestTokenManager(t, strings.Repeat("s", 32))
	store := &fakeInviteStore{policy: defaultPolicy(), createResult: domain.InviteResult{ID: "invite-1"}}
	limit := domain.InviteRateLimit{MaxPerWindow: 7, WindowMinutes: 11}
	svc := service.NewInviteService(manager, store,
		service.WithInviteOutboxEncryptor(newTestEmailOutboxEncryptor(t)),
		service.WithInviteRateLimit(limit))

	if _, err := svc.CreateInvite(context.Background(), domain.AdminInviteInput{
		WorkspaceID: testWorkspaceID, ActorID: testActorID,
		Email: "user@example.com", DisplayName: "User",
	}); err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}

	if store.createLimit != limit {
		t.Fatalf("expected limit %+v to reach the store, got %+v", limit, store.createLimit)
	}
}

func TestInviteService_CreateInvitePropagatesRateLimitError(t *testing.T) {
	manager := newTestTokenManager(t, strings.Repeat("t", 32))
	store := &fakeInviteStore{policy: defaultPolicy(), createErr: domain.ErrInviteRateLimited}
	svc := service.NewInviteService(manager, store, service.WithInviteOutboxEncryptor(newTestEmailOutboxEncryptor(t)))

	_, err := svc.CreateInvite(context.Background(), domain.AdminInviteInput{
		WorkspaceID: testWorkspaceID, ActorID: testActorID,
		Email: "user@example.com", DisplayName: "User",
	})
	if !errors.Is(err, domain.ErrInviteRateLimited) {
		t.Fatalf("expected ErrInviteRateLimited, got %v", err)
	}
}

// ── Bootstrap invites (issue #425) ─────────────────────────────────────────
//
// The bootstrap command exists because nothing else can onboard the first
// person: no HTTP route creates a workspace membership except invite
// acceptance, so a fresh deployment has no owner/admin and every session-scoped
// admin route answers 403.

const bootstrapWorkspaceID = "00000000-0000-0000-0000-000000000001"

func bootstrapService(t *testing.T, store *fakeInviteStore, opts ...service.InviteOption) *service.InviteService {
	t.Helper()
	base := []service.InviteOption{
		service.WithInviteOutboxEncryptor(newTestEmailOutboxEncryptor(t)),
		service.WithBootstrapWorkspace(bootstrapWorkspaceID),
	}
	return service.NewInviteService(newTestTokenManager(t, strings.Repeat("b", 32)), store, append(base, opts...)...)
}

func TestInviteService_CreateBootstrapInviteUsesConfiguredWorkspaceAndSystemIssuer(t *testing.T) {
	store := &fakeInviteStore{policy: defaultPolicy(), createResult: domain.InviteResult{ID: "invite-1"}}
	svc := bootstrapService(t, store)

	if _, err := svc.CreateBootstrapInvite(context.Background(), domain.BootstrapInviteInput{
		Email: " USER@Example.COM ", DisplayName: " User ", FullName: " User Full ",
	}); err != nil {
		t.Fatalf("CreateBootstrapInvite: %v", err)
	}

	if store.createInput.WorkspaceID != bootstrapWorkspaceID {
		t.Fatalf("expected the configured workspace %q, got %q", bootstrapWorkspaceID, store.createInput.WorkspaceID)
	}
	// The issuer is the system identity, which the store turns into a NULL
	// invited_by_user_id. Anything else here would be a fabricated actor.
	if store.createInput.ActorID != domain.BootstrapInviteIssuer {
		t.Fatalf("expected the bootstrap system issuer, got %q", store.createInput.ActorID)
	}
	// The invitee still goes through the same normalisation as a session invite.
	if store.createInput.Email != "user@example.com" || store.createInput.DisplayName != "User" {
		t.Fatalf("unexpected invitee: %+v", store.createInput)
	}
}

// The workspace is checked before anything is minted or written.
func TestInviteService_CreateBootstrapInviteRefusesWhenNotConfigured(t *testing.T) {
	store := &fakeInviteStore{policy: defaultPolicy()}
	svc := service.NewInviteService(newTestTokenManager(t, strings.Repeat("c", 32)), store,
		service.WithInviteOutboxEncryptor(newTestEmailOutboxEncryptor(t)))

	_, err := svc.CreateBootstrapInvite(context.Background(), domain.BootstrapInviteInput{
		Email: "user@example.com", DisplayName: "User",
	})
	if !errors.Is(err, domain.ErrBootstrapUnavailable) {
		t.Fatalf("expected ErrBootstrapUnavailable, got %v", err)
	}
	if store.createCalls != 0 {
		t.Fatal("an unconfigured bootstrap must not reach the store")
	}
}

// Once the workspace has an administrator the browser endpoint can do the job,
// and a pre-shared credential able to inject invites into a live workspace is a
// standing risk with no remaining purpose.
func TestInviteService_CreateBootstrapInviteRefusesAfterWorkspaceHasAdmin(t *testing.T) {
	store := &fakeInviteStore{policy: defaultPolicy(), hasAdmin: true}
	svc := bootstrapService(t, store)

	_, err := svc.CreateBootstrapInvite(context.Background(), domain.BootstrapInviteInput{
		Email: "user@example.com", DisplayName: "User",
	})
	if !errors.Is(err, domain.ErrBootstrapUnavailable) {
		t.Fatalf("expected ErrBootstrapUnavailable, got %v", err)
	}
	if store.createCalls != 0 {
		t.Fatal("an initialized workspace must not receive a bootstrap invite")
	}
	if store.hasAdminWorkspaceID != bootstrapWorkspaceID {
		t.Fatalf("the lifecycle check must ask about the configured workspace, got %q", store.hasAdminWorkspaceID)
	}
}

// A failure to determine the state must not be read as "still uninitialized".
func TestInviteService_CreateBootstrapInviteFailsClosedOnLifecycleCheckError(t *testing.T) {
	store := &fakeInviteStore{policy: defaultPolicy(), hasAdminErr: errors.New("connection refused")}
	svc := bootstrapService(t, store)

	_, err := svc.CreateBootstrapInvite(context.Background(), domain.BootstrapInviteInput{
		Email: "user@example.com", DisplayName: "User",
	})
	if err == nil {
		t.Fatal("expected an error when the lifecycle check fails")
	}
	if store.createCalls != 0 {
		t.Fatal("an undetermined workspace state must not create an invite")
	}
}

// The bootstrap path spends the same budget as the session path.
func TestInviteService_CreateBootstrapInvitePassesRateLimitToStore(t *testing.T) {
	store := &fakeInviteStore{policy: defaultPolicy(), createResult: domain.InviteResult{ID: "invite-1"}}
	limit := domain.InviteRateLimit{MaxPerWindow: 3, WindowMinutes: 15}
	svc := bootstrapService(t, store, service.WithInviteRateLimit(limit))

	if _, err := svc.CreateBootstrapInvite(context.Background(), domain.BootstrapInviteInput{
		Email: "user@example.com", DisplayName: "User",
	}); err != nil {
		t.Fatalf("CreateBootstrapInvite: %v", err)
	}
	if store.createLimit != limit {
		t.Fatalf("expected limit %+v to reach the store, got %+v", limit, store.createLimit)
	}
}

func TestInviteService_CreateBootstrapInviteRejectsInvalidInvitee(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input domain.BootstrapInviteInput
	}{
		{"invalid email", domain.BootstrapInviteInput{Email: "not-an-email", DisplayName: "User"}},
		{"missing display name", domain.BootstrapInviteInput{Email: "user@example.com"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &fakeInviteStore{policy: defaultPolicy()}
			svc := bootstrapService(t, store)

			if _, err := svc.CreateBootstrapInvite(context.Background(), tc.input); err == nil {
				t.Fatal("expected a validation error")
			}
			if store.createCalls != 0 {
				t.Fatal("an invalid invitee must not reach the store")
			}
		})
	}
}

func TestInviteService_CreateBootstrapInviteRequiresEmailHandoff(t *testing.T) {
	store := &fakeInviteStore{policy: defaultPolicy()}
	svc := service.NewInviteService(newTestTokenManager(t, strings.Repeat("d", 32)), store,
		service.WithBootstrapWorkspace(bootstrapWorkspaceID))

	_, err := svc.CreateBootstrapInvite(context.Background(), domain.BootstrapInviteInput{
		Email: "user@example.com", DisplayName: "User",
	})
	if !errors.Is(err, domain.ErrEmailOutboxUnavailable) {
		t.Fatalf("expected ErrEmailOutboxUnavailable, got %v", err)
	}
}

// The session command keeps demanding a real actor: the bootstrap issuer is not
// a value a session-scoped caller may adopt.
func TestInviteService_CreateInviteStillRejectsBootstrapIssuer(t *testing.T) {
	store := &fakeInviteStore{policy: defaultPolicy()}
	svc := bootstrapService(t, store)

	_, err := svc.CreateInvite(context.Background(), domain.AdminInviteInput{
		WorkspaceID: testWorkspaceID,
		ActorID:     domain.BootstrapInviteIssuer,
		Email:       "user@example.com",
		DisplayName: "User",
	})
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput, got %v", err)
	}
	if store.createCalls != 0 {
		t.Fatal("a session invite without an actor must not reach the store")
	}
}
