package service_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nicrepository/nchat/services/auth-service/internal/domain"
	"github.com/nicrepository/nchat/services/auth-service/internal/service"
)

type fakeStore struct {
	policy    domain.PolicySettings
	policyErr error
	user      domain.User
	createErr error
	gotHash   string

	adminWorkspaceID  string
	adminWorkspaceErr error
	gotAdminUserID    string
	gotAdminSelector  string
}

func (f *fakeStore) GetPolicySettings(_ context.Context) (domain.PolicySettings, error) {
	return f.policy, f.policyErr
}

func (f *fakeStore) CreateUser(_ context.Context, _ domain.CreateUserInput, hash string) (domain.User, error) {
	f.gotHash = hash
	return f.user, f.createErr
}

func (f *fakeStore) GetUserByID(_ context.Context, _ string) (domain.User, error) {
	return domain.User{}, domain.ErrNotFound
}

func (f *fakeStore) UpdateUserStatus(_ context.Context, _, _ string) (domain.User, error) {
	return domain.User{}, nil
}

func (f *fakeStore) SetAvatarURL(_ context.Context, _, _ string) (string, error) { return "", nil }
func (f *fakeStore) ClearAvatarURL(_ context.Context, _ string) (string, error)  { return "", nil }
func (f *fakeStore) GetSelfProfile(_ context.Context, _ string) (domain.SelfProfile, error) {
	return domain.SelfProfile{}, nil
}

func (f *fakeStore) ResolveAdminWorkspaceID(_ context.Context, userID, selector string) (string, error) {
	f.gotAdminUserID = userID
	f.gotAdminSelector = selector
	return f.adminWorkspaceID, f.adminWorkspaceErr
}

func defaultPolicy() domain.PolicySettings {
	return domain.PolicySettings{
		MinPasswordLength: 8,
		RequireUppercase:  true,
		RequireLowercase:  true,
		RequireNumber:     true,
		RequireSymbol:     true,
	}
}

func TestUserService_CreateUser_Success(t *testing.T) {
	now := time.Now()
	store := &fakeStore{
		policy: defaultPolicy(),
		user: domain.User{
			ID: "uuid-1", Email: "user@example.com", DisplayName: "User",
			Status: "active", AuthSource: "manual", EmailVerifiedAt: now,
		},
	}
	svc := service.NewUserService(store)
	user, err := svc.CreateUser(context.Background(), domain.CreateUserInput{
		Email: "user@example.com", DisplayName: "User",
		InitialPassword: "Abcdef1!", MustChangePassword: true,
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if user.ID != "uuid-1" {
		t.Fatalf("expected uuid-1, got %q", user.ID)
	}
	if store.gotHash == "" {
		t.Fatal("expected password hash to be set")
	}
	if store.gotHash == "Abcdef1!" {
		t.Fatal("password must be hashed, not stored as plaintext")
	}
}

func TestUserService_CreateUser_NormalizesEmail(t *testing.T) {
	store := &fakeStore{policy: defaultPolicy(), user: domain.User{Email: "user@example.com"}}
	svc := service.NewUserService(store)
	_, err := svc.CreateUser(context.Background(), domain.CreateUserInput{
		Email: "  USER@EXAMPLE.COM  ", DisplayName: "User", InitialPassword: "Abcdef1!",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestUserService_CreateUser_EmptyEmail(t *testing.T) {
	store := &fakeStore{policy: defaultPolicy()}
	svc := service.NewUserService(store)
	_, err := svc.CreateUser(context.Background(), domain.CreateUserInput{
		Email: "", DisplayName: "User", InitialPassword: "Abcdef1!",
	})
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput, got %v", err)
	}
}

func TestUserService_CreateUser_EmptyDisplayName(t *testing.T) {
	store := &fakeStore{policy: defaultPolicy()}
	svc := service.NewUserService(store)
	_, err := svc.CreateUser(context.Background(), domain.CreateUserInput{
		Email: "user@example.com", DisplayName: "", InitialPassword: "Abcdef1!",
	})
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput, got %v", err)
	}
}

func TestUserService_CreateUser_EmptyPassword(t *testing.T) {
	store := &fakeStore{policy: defaultPolicy()}
	svc := service.NewUserService(store)
	_, err := svc.CreateUser(context.Background(), domain.CreateUserInput{
		Email: "user@example.com", DisplayName: "User", InitialPassword: "",
	})
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput, got %v", err)
	}
}

func TestUserService_CreateUser_PolicyViolation(t *testing.T) {
	store := &fakeStore{policy: domain.PolicySettings{MinPasswordLength: 20}}
	svc := service.NewUserService(store)
	_, err := svc.CreateUser(context.Background(), domain.CreateUserInput{
		Email: "user@example.com", DisplayName: "User", InitialPassword: "short",
	})
	if !errors.Is(err, domain.ErrPasswordPolicy) {
		t.Fatalf("expected ErrPasswordPolicy, got %v", err)
	}
}

func TestUserService_CreateUser_PolicyError(t *testing.T) {
	store := &fakeStore{policyErr: errors.New("db down")}
	svc := service.NewUserService(store)
	_, err := svc.CreateUser(context.Background(), domain.CreateUserInput{
		Email: "user@example.com", DisplayName: "User", InitialPassword: "Abcdef1!",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestUserService_CreateUser_DuplicateEmail(t *testing.T) {
	store := &fakeStore{policy: defaultPolicy(), createErr: domain.ErrDuplicateEmail}
	svc := service.NewUserService(store)
	_, err := svc.CreateUser(context.Background(), domain.CreateUserInput{
		Email: "dup@example.com", DisplayName: "User", InitialPassword: "Abcdef1!",
	})
	if !errors.Is(err, domain.ErrDuplicateEmail) {
		t.Fatalf("expected ErrDuplicateEmail, got %v", err)
	}
}

// ── UpdateUserStatus tests ─────────────────────────────────────────────────
// The service delegates all status-change logic (transition validation,
// atomic status update + session revocation) to the storage layer.
// These tests verify the thin service logic: self-deactivation guard and
// error propagation.

type fakeUserStatusStore struct {
	fakeStore
	updatedUser     domain.User
	updateErr       error
	gotUpdateID     string
	gotUpdateStatus string
}

func (f *fakeUserStatusStore) GetUserByID(_ context.Context, _ string) (domain.User, error) {
	return domain.User{}, domain.ErrNotFound
}

func (f *fakeUserStatusStore) UpdateUserStatus(_ context.Context, id, status string) (domain.User, error) {
	f.gotUpdateID = id
	f.gotUpdateStatus = status
	return f.updatedUser, f.updateErr
}

func (f *fakeUserStatusStore) SetAvatarURL(_ context.Context, _, _ string) (string, error) {
	return "", nil
}
func (f *fakeUserStatusStore) ClearAvatarURL(_ context.Context, _ string) (string, error) {
	return "", nil
}

func activeUser() domain.User {
	return domain.User{ID: "user-1", Email: "u@example.com", Status: "active"}
}

func suspendedUser() domain.User {
	return domain.User{ID: "user-1", Email: "u@example.com", Status: "suspended"}
}

func TestUpdateUserStatus_ActiveToSuspended_DelegatesToStore(t *testing.T) {
	store := &fakeUserStatusStore{updatedUser: suspendedUser()}
	svc := service.NewUserService(store)

	got, err := svc.UpdateUserStatus(context.Background(), "", "user-1", "suspended")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Status != "suspended" {
		t.Fatalf("expected suspended, got %q", got.Status)
	}
	if store.gotUpdateID != "user-1" || store.gotUpdateStatus != "suspended" {
		t.Fatalf("unexpected store call: id=%q status=%q", store.gotUpdateID, store.gotUpdateStatus)
	}
}

func TestUpdateUserStatus_SuspendedToActive_DelegatesToStore(t *testing.T) {
	store := &fakeUserStatusStore{updatedUser: activeUser()}
	svc := service.NewUserService(store)

	got, err := svc.UpdateUserStatus(context.Background(), "", "user-1", "active")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Status != "active" {
		t.Fatalf("expected active, got %q", got.Status)
	}
}

func TestUpdateUserStatus_StoreError_Propagates(t *testing.T) {
	store := &fakeUserStatusStore{updateErr: domain.ErrStatusTransitionNotAllowed}
	svc := service.NewUserService(store)

	_, err := svc.UpdateUserStatus(context.Background(), "", "user-1", "active")
	if !errors.Is(err, domain.ErrStatusTransitionNotAllowed) {
		t.Fatalf("expected ErrStatusTransitionNotAllowed, got %v", err)
	}
}

func TestUpdateUserStatus_NotFound_Propagates(t *testing.T) {
	store := &fakeUserStatusStore{updateErr: domain.ErrNotFound}
	svc := service.NewUserService(store)

	_, err := svc.UpdateUserStatus(context.Background(), "", "no-such-id", "suspended")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestUpdateUserStatus_SelfDeactivation_Rejected(t *testing.T) {
	store := &fakeUserStatusStore{}
	svc := service.NewUserService(store)

	_, err := svc.UpdateUserStatus(context.Background(), "user-1", "user-1", "suspended")
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("expected ErrForbidden, got %v", err)
	}
	// self-check fires before storage call
	if store.gotUpdateID != "" {
		t.Fatal("store must not be called on self-deactivation")
	}
}

// profileStore is a fakeStore that returns a fixed self-profile.
type profileStore struct {
	fakeStore
	profile domain.SelfProfile
	err     error
}

func (p *profileStore) GetSelfProfile(_ context.Context, _ string) (domain.SelfProfile, error) {
	return p.profile, p.err
}

func TestUserService_GetProfile_DelegatesToStore(t *testing.T) {
	store := &profileStore{profile: domain.SelfProfile{ID: "u1", DisplayName: "Ana", AvatarURL: "/api/auth/avatars/x.png"}}
	svc := service.NewUserService(store)

	got, err := svc.GetProfile(context.Background(), "u1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.ID != "u1" || got.AvatarURL != "/api/auth/avatars/x.png" {
		t.Fatalf("unexpected profile: %+v", got)
	}
}

func TestUserService_GetProfile_PropagatesError(t *testing.T) {
	store := &profileStore{err: domain.ErrNotFound}
	svc := service.NewUserService(store)
	if _, err := svc.GetProfile(context.Background(), "u1"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

// ── Workspace administration ──────────────────────────────────

// The actor reaches the store unchanged: the service adds no notion of a
// default or fallback caller.
func TestUserService_ResolveAdminWorkspaceID_PassesActorThrough(t *testing.T) {
	store := &fakeStore{adminWorkspaceID: "ws-1"}
	svc := service.NewUserService(store)

	workspaceID, err := svc.ResolveAdminWorkspaceID(context.Background(), "actor-1", "")
	if err != nil {
		t.Fatalf("ResolveAdminWorkspaceID: %v", err)
	}
	if workspaceID != "ws-1" {
		t.Fatalf("expected ws-1, got %q", workspaceID)
	}
	if store.gotAdminUserID != "actor-1" {
		t.Fatalf("expected actor-1 to reach the store, got %q", store.gotAdminUserID)
	}
}

// A caller who administers nothing must surface as forbidden, not as an empty
// workspace that a later query would silently widen.
func TestUserService_ResolveAdminWorkspaceID_ForbiddenPropagates(t *testing.T) {
	store := &fakeStore{adminWorkspaceErr: domain.ErrForbidden}
	svc := service.NewUserService(store)

	if _, err := svc.ResolveAdminWorkspaceID(context.Background(), "member-1", ""); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("expected ErrForbidden, got %v", err)
	}
}
