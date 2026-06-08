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
	svc := service.NewUserService(store, nil)
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
	svc := service.NewUserService(store, nil)
	_, err := svc.CreateUser(context.Background(), domain.CreateUserInput{
		Email: "  USER@EXAMPLE.COM  ", DisplayName: "User", InitialPassword: "Abcdef1!",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestUserService_CreateUser_EmptyEmail(t *testing.T) {
	store := &fakeStore{policy: defaultPolicy()}
	svc := service.NewUserService(store, nil)
	_, err := svc.CreateUser(context.Background(), domain.CreateUserInput{
		Email: "", DisplayName: "User", InitialPassword: "Abcdef1!",
	})
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput, got %v", err)
	}
}

func TestUserService_CreateUser_EmptyDisplayName(t *testing.T) {
	store := &fakeStore{policy: defaultPolicy()}
	svc := service.NewUserService(store, nil)
	_, err := svc.CreateUser(context.Background(), domain.CreateUserInput{
		Email: "user@example.com", DisplayName: "", InitialPassword: "Abcdef1!",
	})
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput, got %v", err)
	}
}

func TestUserService_CreateUser_EmptyPassword(t *testing.T) {
	store := &fakeStore{policy: defaultPolicy()}
	svc := service.NewUserService(store, nil)
	_, err := svc.CreateUser(context.Background(), domain.CreateUserInput{
		Email: "user@example.com", DisplayName: "User", InitialPassword: "",
	})
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput, got %v", err)
	}
}

func TestUserService_CreateUser_PolicyViolation(t *testing.T) {
	store := &fakeStore{policy: domain.PolicySettings{MinPasswordLength: 20}}
	svc := service.NewUserService(store, nil)
	_, err := svc.CreateUser(context.Background(), domain.CreateUserInput{
		Email: "user@example.com", DisplayName: "User", InitialPassword: "short",
	})
	if !errors.Is(err, domain.ErrPasswordPolicy) {
		t.Fatalf("expected ErrPasswordPolicy, got %v", err)
	}
}

func TestUserService_CreateUser_PolicyError(t *testing.T) {
	store := &fakeStore{policyErr: errors.New("db down")}
	svc := service.NewUserService(store, nil)
	_, err := svc.CreateUser(context.Background(), domain.CreateUserInput{
		Email: "user@example.com", DisplayName: "User", InitialPassword: "Abcdef1!",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestUserService_CreateUser_DuplicateEmail(t *testing.T) {
	store := &fakeStore{policy: defaultPolicy(), createErr: domain.ErrDuplicateEmail}
	svc := service.NewUserService(store, nil)
	_, err := svc.CreateUser(context.Background(), domain.CreateUserInput{
		Email: "dup@example.com", DisplayName: "User", InitialPassword: "Abcdef1!",
	})
	if !errors.Is(err, domain.ErrDuplicateEmail) {
		t.Fatalf("expected ErrDuplicateEmail, got %v", err)
	}
}

// ── UpdateUserStatus tests ─────────────────────────────────────────────────

type fakeUserStatusStore struct {
	fakeStore
	getUser         domain.User
	getUserErr      error
	updatedUser     domain.User
	updateErr       error
	gotUpdateID     string
	gotUpdateStatus string
}

func (f *fakeUserStatusStore) GetUserByID(_ context.Context, id string) (domain.User, error) {
	return f.getUser, f.getUserErr
}

func (f *fakeUserStatusStore) UpdateUserStatus(_ context.Context, id, status string) (domain.User, error) {
	f.gotUpdateID = id
	f.gotUpdateStatus = status
	return f.updatedUser, f.updateErr
}

type fakeRevoker struct {
	called bool
	err    error
}

func (f *fakeRevoker) RevokeAllUserSessions(_ context.Context, _ string) error {
	f.called = true
	return f.err
}

func activeUser() domain.User {
	return domain.User{ID: "user-1", Email: "u@example.com", Status: "active"}
}

func suspendedUser() domain.User {
	return domain.User{ID: "user-1", Email: "u@example.com", Status: "suspended"}
}

func TestUpdateUserStatus_ActiveToSuspended_Succeeds(t *testing.T) {
	store := &fakeUserStatusStore{
		getUser:     activeUser(),
		updatedUser: suspendedUser(),
	}
	revoker := &fakeRevoker{}
	svc := service.NewUserService(store, revoker)

	got, err := svc.UpdateUserStatus(context.Background(), "", "user-1", "suspended")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Status != "suspended" {
		t.Fatalf("expected suspended, got %q", got.Status)
	}
	if !revoker.called {
		t.Fatal("expected sessions to be revoked on suspension")
	}
}

func TestUpdateUserStatus_SuspendedToActive_Succeeds(t *testing.T) {
	store := &fakeUserStatusStore{
		getUser:     suspendedUser(),
		updatedUser: activeUser(),
	}
	revoker := &fakeRevoker{}
	svc := service.NewUserService(store, revoker)

	got, err := svc.UpdateUserStatus(context.Background(), "", "user-1", "active")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Status != "active" {
		t.Fatalf("expected active, got %q", got.Status)
	}
	if revoker.called {
		t.Fatal("expected sessions NOT to be revoked on activation")
	}
}

func TestUpdateUserStatus_InvalidTransition_Rejected(t *testing.T) {
	store := &fakeUserStatusStore{getUser: activeUser()}
	svc := service.NewUserService(store, nil)

	_, err := svc.UpdateUserStatus(context.Background(), "", "user-1", "active")
	if !errors.Is(err, domain.ErrStatusTransitionNotAllowed) {
		t.Fatalf("expected ErrStatusTransitionNotAllowed, got %v", err)
	}
}

func TestUpdateUserStatus_UnknownUser_ReturnsNotFound(t *testing.T) {
	store := &fakeUserStatusStore{getUserErr: domain.ErrNotFound}
	svc := service.NewUserService(store, nil)

	_, err := svc.UpdateUserStatus(context.Background(), "", "no-such-id", "suspended")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestUpdateUserStatus_SelfDeactivation_Rejected(t *testing.T) {
	store := &fakeUserStatusStore{getUser: activeUser()}
	svc := service.NewUserService(store, nil)

	_, err := svc.UpdateUserStatus(context.Background(), "user-1", "user-1", "suspended")
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("expected ErrForbidden, got %v", err)
	}
}

func TestUpdateUserStatus_NilRevoker_SuspendSucceeds(t *testing.T) {
	store := &fakeUserStatusStore{
		getUser:     activeUser(),
		updatedUser: suspendedUser(),
	}
	svc := service.NewUserService(store, nil)

	got, err := svc.UpdateUserStatus(context.Background(), "", "user-1", "suspended")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Status != "suspended" {
		t.Fatalf("expected suspended, got %q", got.Status)
	}
}

func TestUpdateUserStatus_RevokerError_PropagatesError(t *testing.T) {
	store := &fakeUserStatusStore{
		getUser:     activeUser(),
		updatedUser: suspendedUser(),
	}
	revoker := &fakeRevoker{err: errors.New("db error")}
	svc := service.NewUserService(store, revoker)

	_, err := svc.UpdateUserStatus(context.Background(), "", "user-1", "suspended")
	if err == nil {
		t.Fatal("expected error from revoker, got nil")
	}
}
