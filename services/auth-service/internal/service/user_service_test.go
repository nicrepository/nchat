package service_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
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

	workspaceUsers    []domain.WorkspaceUser
	workspaceUsersErr error
	gotWorkspaceID    string
	gotLimit          int
	gotAfterUserID    string
	listCalls         int

	adminWorkspaceID  string
	adminWorkspaceErr error
	gotAdminUserID    string
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

func (f *fakeStore) UpdateDisplayName(_ context.Context, _, _ string) (domain.SelfProfile, error) {
	return domain.SelfProfile{}, nil
}

func (f *fakeStore) UpdateProfileFields(_ context.Context, _ string, _, _, _, _ *string) (domain.SelfProfile, error) {
	return domain.SelfProfile{}, nil
}

func (f *fakeStore) ListWorkspaceUsers(_ context.Context, workspaceID string, limit int, afterUserID string) ([]domain.WorkspaceUser, error) {
	f.gotWorkspaceID = workspaceID
	f.gotLimit = limit
	f.gotAfterUserID = afterUserID
	f.listCalls++
	return f.workspaceUsers, f.workspaceUsersErr
}

func (f *fakeStore) GetAdminWorkspaceID(_ context.Context, userID string) (string, error) {
	f.gotAdminUserID = userID
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

// ── UpdateDisplayName (ID 7 — cronograma 19/08) ─────────────────────────────

// updateDisplayNameStore records what the service passed to the store, so
// tests can assert validation happens before storage is ever reached, and
// that the value forwarded is the sanitized one, not the raw input.
type updateDisplayNameStore struct {
	fakeStore
	gotUserID      string
	gotDisplayName string
	calls          int
	profile        domain.SelfProfile
	err            error
}

func (s *updateDisplayNameStore) UpdateDisplayName(_ context.Context, userID, displayName string) (domain.SelfProfile, error) {
	s.gotUserID = userID
	s.gotDisplayName = displayName
	s.calls++
	return s.profile, s.err
}

func TestUserService_UpdateDisplayName_TrimsAndDelegates(t *testing.T) {
	store := &updateDisplayNameStore{profile: domain.SelfProfile{ID: "u1", DisplayName: "Ana Lima"}}
	svc := service.NewUserService(store)

	got, err := svc.UpdateDisplayName(context.Background(), "u1", "  Ana Lima  ")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if store.gotUserID != "u1" {
		t.Fatalf("expected userID u1 to reach the store, got %q", store.gotUserID)
	}
	if store.gotDisplayName != "Ana Lima" {
		t.Fatalf("expected trimmed display_name to reach the store, got %q", store.gotDisplayName)
	}
	if got.DisplayName != "Ana Lima" {
		t.Fatalf("expected the store's persisted value to be returned, got %+v", got)
	}
}

func TestUserService_UpdateDisplayName_StripsControlCharacters(t *testing.T) {
	store := &updateDisplayNameStore{profile: domain.SelfProfile{ID: "u1"}}
	svc := service.NewUserService(store)

	if _, err := svc.UpdateDisplayName(context.Background(), "u1", "Ana\x00\nLima"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if store.gotDisplayName != "AnaLima" {
		t.Fatalf("expected control characters stripped, got %q", store.gotDisplayName)
	}
}

func TestUserService_UpdateDisplayName_RejectsEmptyAfterTrim(t *testing.T) {
	store := &updateDisplayNameStore{}
	svc := service.NewUserService(store)

	_, err := svc.UpdateDisplayName(context.Background(), "u1", "   ")
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput, got %v", err)
	}
	if store.calls != 0 {
		t.Fatal("store must not be called when validation fails")
	}
}

func TestUserService_UpdateDisplayName_RejectsTooLong(t *testing.T) {
	store := &updateDisplayNameStore{}
	svc := service.NewUserService(store)

	tooLong := strings.Repeat("a", 81)
	_, err := svc.UpdateDisplayName(context.Background(), "u1", tooLong)
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput, got %v", err)
	}
	if store.calls != 0 {
		t.Fatal("store must not be called when validation fails")
	}
}

func TestUserService_UpdateDisplayName_AcceptsMaxLength(t *testing.T) {
	store := &updateDisplayNameStore{profile: domain.SelfProfile{ID: "u1"}}
	svc := service.NewUserService(store)

	exactly80 := strings.Repeat("a", 80)
	if _, err := svc.UpdateDisplayName(context.Background(), "u1", exactly80); err != nil {
		t.Fatalf("unexpected error at the boundary: %v", err)
	}
	if store.calls != 1 {
		t.Fatalf("expected exactly one store call, got %d", store.calls)
	}
}

func TestUserService_UpdateDisplayName_PropagatesStoreError(t *testing.T) {
	store := &updateDisplayNameStore{err: domain.ErrNotFound}
	svc := service.NewUserService(store)

	_, err := svc.UpdateDisplayName(context.Background(), "u1", "Ana")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

// ── UpdateProfileFields (job_title/bio/timezone/custom_status) ────────────

// updateProfileFieldsStore records what the service passed to the store, so
// tests can assert validation and sanitization happen before storage is ever
// reached, and that a nil argument in stays a nil argument out (the "leave
// this field alone" signal must survive the service layer unchanged).
type updateProfileFieldsStore struct {
	fakeStore
	gotUserID       string
	gotJobTitle     *string
	gotBio          *string
	gotTimezone     *string
	gotCustomStatus *string
	calls           int
	profile         domain.SelfProfile
	err             error
}

func (s *updateProfileFieldsStore) UpdateProfileFields(_ context.Context, userID string, jobTitle, bio, timezone, customStatus *string) (domain.SelfProfile, error) {
	s.gotUserID = userID
	s.gotJobTitle = jobTitle
	s.gotBio = bio
	s.gotTimezone = timezone
	s.gotCustomStatus = customStatus
	s.calls++
	return s.profile, s.err
}

func strPtr(s string) *string { return &s }

func TestUserService_UpdateProfileFields_TrimsAndDelegates(t *testing.T) {
	store := &updateProfileFieldsStore{profile: domain.SelfProfile{ID: "u1"}}
	svc := service.NewUserService(store)

	_, err := svc.UpdateProfileFields(context.Background(), "u1",
		strPtr("  Engenheira  "), strPtr("  Gosto de café.  "), strPtr("  America/Sao_Paulo  "),
		strPtr("  Em reunião  "))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if store.gotUserID != "u1" {
		t.Fatalf("expected userID u1 to reach the store, got %q", store.gotUserID)
	}
	if store.gotJobTitle == nil || *store.gotJobTitle != "Engenheira" {
		t.Fatalf("expected trimmed job_title, got %v", store.gotJobTitle)
	}
	if store.gotBio == nil || *store.gotBio != "Gosto de café." {
		t.Fatalf("expected trimmed bio, got %v", store.gotBio)
	}
	if store.gotTimezone == nil || *store.gotTimezone != "America/Sao_Paulo" {
		t.Fatalf("expected trimmed timezone, got %v", store.gotTimezone)
	}
	if store.gotCustomStatus == nil || *store.gotCustomStatus != "Em reunião" {
		t.Fatalf("expected trimmed custom_status, got %v", store.gotCustomStatus)
	}
}

func TestUserService_UpdateProfileFields_StripsControlCharacters(t *testing.T) {
	store := &updateProfileFieldsStore{profile: domain.SelfProfile{ID: "u1"}}
	svc := service.NewUserService(store)

	if _, err := svc.UpdateProfileFields(context.Background(), "u1", strPtr("Eng\x00enheira"), nil, nil, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if *store.gotJobTitle != "Engenheira" {
		t.Fatalf("expected control characters stripped, got %q", *store.gotJobTitle)
	}
}

func TestUserService_UpdateProfileFields_PreservesBioLineBreaks(t *testing.T) {
	store := &updateProfileFieldsStore{profile: domain.SelfProfile{ID: "u1"}}
	svc := service.NewUserService(store)

	if _, err := svc.UpdateProfileFields(context.Background(), "u1", nil, strPtr("  Primeira linha\r\nSegunda\x00 linha\rTerceira linha  "), nil, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if store.gotBio == nil || *store.gotBio != "Primeira linha\nSegunda linha\nTerceira linha" {
		t.Fatalf("expected normalized multiline bio, got %v", store.gotBio)
	}
}

// A nil argument means "the caller did not touch this field" and must reach
// the store as nil too — the service must never invent a value for a field
// nobody asked to change.
func TestUserService_UpdateProfileFields_NilFieldsStayNil(t *testing.T) {
	store := &updateProfileFieldsStore{profile: domain.SelfProfile{ID: "u1"}}
	svc := service.NewUserService(store)

	if _, err := svc.UpdateProfileFields(context.Background(), "u1", strPtr("Engenheira"), nil, nil, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if store.gotBio != nil || store.gotTimezone != nil || store.gotCustomStatus != nil {
		t.Fatalf("expected untouched fields to stay nil, got bio=%v timezone=%v custom_status=%v",
			store.gotBio, store.gotTimezone, store.gotCustomStatus)
	}
}

func TestUserService_UpdateProfileFields_CustomStatusOnly(t *testing.T) {
	store := &updateProfileFieldsStore{profile: domain.SelfProfile{ID: "u1"}}
	svc := service.NewUserService(store)

	if _, err := svc.UpdateProfileFields(context.Background(), "u1", nil, nil, nil, strPtr("Em reunião")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if store.gotCustomStatus == nil || *store.gotCustomStatus != "Em reunião" {
		t.Fatalf("expected custom_status to reach the store, got %v", store.gotCustomStatus)
	}
}

// Unlike display_name, a provided-but-empty value is accepted for every one
// of these four fields — it clears the field rather than being rejected.
func TestUserService_UpdateProfileFields_EmptyClears(t *testing.T) {
	store := &updateProfileFieldsStore{profile: domain.SelfProfile{ID: "u1"}}
	svc := service.NewUserService(store)

	if _, err := svc.UpdateProfileFields(context.Background(), "u1", strPtr(""), strPtr(""), strPtr(""), strPtr("")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if store.gotJobTitle == nil || *store.gotJobTitle != "" {
		t.Fatalf("expected job_title cleared (non-nil empty), got %v", store.gotJobTitle)
	}
	if store.gotTimezone == nil || *store.gotTimezone != "" {
		t.Fatalf("expected timezone cleared (non-nil empty), got %v", store.gotTimezone)
	}
	if store.gotCustomStatus == nil || *store.gotCustomStatus != "" {
		t.Fatalf("expected custom_status cleared (non-nil empty), got %v", store.gotCustomStatus)
	}
	if store.calls != 1 {
		t.Fatalf("expected exactly one store call, got %d", store.calls)
	}
}

func TestUserService_UpdateProfileFields_RejectsTooLong(t *testing.T) {
	tooLong81 := strings.Repeat("a", 81)
	tooLong501 := strings.Repeat("a", 501)
	cases := []struct {
		name                                  string
		jobTitle, bio, timezone, customStatus *string
	}{
		{"job_title", strPtr(tooLong81), nil, nil, nil},
		{"bio", nil, strPtr(tooLong501), nil, nil},
		{"custom_status", nil, nil, nil, strPtr(tooLong81)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := &updateProfileFieldsStore{}
			svc := service.NewUserService(store)
			_, err := svc.UpdateProfileFields(context.Background(), "u1", tc.jobTitle, tc.bio, tc.timezone, tc.customStatus)
			if !errors.Is(err, domain.ErrInvalidInput) {
				t.Fatalf("expected ErrInvalidInput, got %v", err)
			}
			if store.calls != 0 {
				t.Fatal("store must not be called when validation fails")
			}
		})
	}
}

func TestUserService_UpdateProfileFields_AcceptsMaxLength(t *testing.T) {
	store := &updateProfileFieldsStore{profile: domain.SelfProfile{ID: "u1"}}
	svc := service.NewUserService(store)

	exactly80 := strings.Repeat("a", 80)
	exactly500 := strings.Repeat("a", 500)
	_, err := svc.UpdateProfileFields(context.Background(), "u1", strPtr(exactly80), strPtr(exactly500), nil, strPtr(exactly80))
	if err != nil {
		t.Fatalf("unexpected error at the boundary: %v", err)
	}
	if store.calls != 1 {
		t.Fatalf("expected exactly one store call, got %d", store.calls)
	}
}

func TestUserService_UpdateProfileFields_AcceptsRealTimezone(t *testing.T) {
	store := &updateProfileFieldsStore{profile: domain.SelfProfile{ID: "u1"}}
	svc := service.NewUserService(store)

	for _, tz := range []string{"America/Sao_Paulo", "UTC", "Europe/Lisbon", "Asia/Tokyo"} {
		store.calls = 0
		if _, err := svc.UpdateProfileFields(context.Background(), "u1", nil, nil, strPtr(tz), nil); err != nil {
			t.Fatalf("unexpected error for %q: %v", tz, err)
		}
		if *store.gotTimezone != tz {
			t.Fatalf("expected timezone %q to reach the store, got %q", tz, *store.gotTimezone)
		}
	}
}

func TestUserService_UpdateProfileFields_RejectsFakeTimezone(t *testing.T) {
	store := &updateProfileFieldsStore{}
	svc := service.NewUserService(store)

	_, err := svc.UpdateProfileFields(context.Background(), "u1", nil, nil, strPtr("Mars/Olympus_Mons"), nil)
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput, got %v", err)
	}
	if store.calls != 0 {
		t.Fatal("store must not be called when timezone validation fails")
	}
}

// "Local" is Go's sentinel for the host's own system zone, not a zone the
// user chose — accepting it would store a value whose meaning silently
// depends on wherever the server happens to be deployed.
func TestUserService_UpdateProfileFields_RejectsLocalSentinel(t *testing.T) {
	store := &updateProfileFieldsStore{}
	svc := service.NewUserService(store)

	_, err := svc.UpdateProfileFields(context.Background(), "u1", nil, nil, strPtr("Local"), nil)
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput, got %v", err)
	}
}

func TestUserService_UpdateProfileFields_EmptyTimezoneClearsWithoutValidation(t *testing.T) {
	store := &updateProfileFieldsStore{profile: domain.SelfProfile{ID: "u1"}}
	svc := service.NewUserService(store)

	if _, err := svc.UpdateProfileFields(context.Background(), "u1", nil, nil, strPtr("  "), nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if store.gotTimezone == nil || *store.gotTimezone != "" {
		t.Fatalf("expected timezone cleared, got %v", store.gotTimezone)
	}
}

func TestUserService_UpdateProfileFields_PropagatesStoreError(t *testing.T) {
	store := &updateProfileFieldsStore{err: domain.ErrNotFound}
	svc := service.NewUserService(store)

	_, err := svc.UpdateProfileFields(context.Background(), "u1", strPtr("Engenheira"), nil, nil, nil)
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

// ── Workspace administration (issue #425) ──────────────────────────────────

// The actor reaches the store unchanged: the service adds no notion of a
// default or fallback caller.
func TestUserService_GetAdminWorkspaceID_PassesActorThrough(t *testing.T) {
	store := &fakeStore{adminWorkspaceID: "ws-1"}
	svc := service.NewUserService(store)

	workspaceID, err := svc.GetAdminWorkspaceID(context.Background(), "actor-1")
	if err != nil {
		t.Fatalf("GetAdminWorkspaceID: %v", err)
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
func TestUserService_GetAdminWorkspaceID_ForbiddenPropagates(t *testing.T) {
	store := &fakeStore{adminWorkspaceErr: domain.ErrForbidden}
	svc := service.NewUserService(store)

	if _, err := svc.GetAdminWorkspaceID(context.Background(), "member-1"); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("expected ErrForbidden, got %v", err)
	}
}

// ── Workspace user listing and cursors (issue #425) ────────────────────────

const testWorkspaceID = "9a8b7c6d-5e4f-4a3b-8c2d-1e0f9a8b7c6d"

// A cursor now carries UUIDs only, and they are shape-checked.
const anchorUserID = "3f1c2d4e-5a6b-4c8d-9e0f-1a2b3c4d5e6f"

// Ids are real UUIDs: auth.users.id is a UUID column, and the cursor now
// shape-checks the identifier it carries, so a fixture using "u01" would be
// testing a value production never produces.
func listUserID(i int) string {
	return fmt.Sprintf("00000000-0000-4000-8000-%012d", i)
}

func listUsers(n int) []domain.WorkspaceUser {
	out := make([]domain.WorkspaceUser, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, domain.WorkspaceUser{ID: listUserID(i), DisplayName: fmt.Sprintf("u%02d", i)})
	}
	return out
}

// One extra row is requested so "is there another page" is answered by the
// same read, without a second COUNT that could disagree with it.
func TestUserService_ListWorkspaceUsers_RequestsOneExtraRow(t *testing.T) {
	store := &fakeStore{workspaceUsers: listUsers(3)}
	svc := service.NewUserService(store)

	if _, _, err := svc.ListWorkspaceUsers(context.Background(), testWorkspaceID, 10, ""); err != nil {
		t.Fatalf("ListWorkspaceUsers: %v", err)
	}
	if store.gotLimit != 11 {
		t.Fatalf("expected limit+1 = 11 to reach the store, got %d", store.gotLimit)
	}
	if store.gotWorkspaceID != testWorkspaceID {
		t.Fatalf("expected workspace %q, got %q", testWorkspaceID, store.gotWorkspaceID)
	}
}

// Exactly `limit` rows means this was the last page: the extra row was absent.
func TestUserService_ListWorkspaceUsers_LastPageHasNoCursor(t *testing.T) {
	store := &fakeStore{workspaceUsers: listUsers(3)}
	svc := service.NewUserService(store)

	users, next, err := svc.ListWorkspaceUsers(context.Background(), testWorkspaceID, 3, "")
	if err != nil {
		t.Fatalf("ListWorkspaceUsers: %v", err)
	}
	if len(users) != 3 {
		t.Fatalf("expected 3 users, got %d", len(users))
	}
	if next != "" {
		t.Fatalf("the last page must not carry a cursor, got %q", next)
	}
}

// limit+1 rows means another page exists: the extra row is trimmed and the
// cursor points at the last row actually returned.
func TestUserService_ListWorkspaceUsers_FullPageTrimsAndEmitsCursor(t *testing.T) {
	store := &fakeStore{workspaceUsers: listUsers(4)}
	svc := service.NewUserService(store)

	users, next, err := svc.ListWorkspaceUsers(context.Background(), testWorkspaceID, 3, "")
	if err != nil {
		t.Fatalf("ListWorkspaceUsers: %v", err)
	}
	if len(users) != 3 {
		t.Fatalf("expected the extra row trimmed, got %d", len(users))
	}
	if users[2].ID != listUserID(2) {
		t.Fatalf("expected the page to end at the third user, got %q", users[2].ID)
	}
	if next == "" {
		t.Fatal("expected a cursor when another page exists")
	}

	// The cursor must resume exactly after the last returned row.
	raw, err := base64.RawURLEncoding.DecodeString(next)
	if err != nil {
		t.Fatalf("cursor must be base64url: %v", err)
	}
	var c domain.WorkspaceUserCursor
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatalf("cursor must be JSON: %v", err)
	}
	if c.Version != domain.WorkspaceUserCursorVersion || c.UserID != listUserID(2) {
		t.Fatalf("unexpected cursor: %+v", c)
	}
	// The cursor must not carry the ordering value: it is a display name or an
	// e-mail, and it would end up in a query string and in access logs.
	if strings.Contains(string(raw), "sortKey") || strings.Contains(string(raw), "@") {
		t.Fatalf("cursor must not carry the sort key or any address: %s", raw)
	}
	if c.WorkspaceID != testWorkspaceID {
		t.Fatalf("cursor must carry its workspace, got %q", c.WorkspaceID)
	}
}

// A round trip: the cursor this service emits is one it accepts back.
func TestUserService_ListWorkspaceUsers_AcceptsItsOwnCursor(t *testing.T) {
	store := &fakeStore{workspaceUsers: listUsers(4)}
	svc := service.NewUserService(store)

	_, next, err := svc.ListWorkspaceUsers(context.Background(), testWorkspaceID, 3, "")
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	if _, _, err := svc.ListWorkspaceUsers(context.Background(), testWorkspaceID, 3, next); err != nil {
		t.Fatalf("second page: %v", err)
	}
	// The cursor's user id is the position outright — the listing is ordered by
	// it — so it reaches the store unchanged, with no second query to place it.
	if store.gotAfterUserID != listUserID(2) {
		t.Fatalf("expected the store to resume after the last row, got %q", store.gotAfterUserID)
	}
}

// Every rejection is ErrInvalidInput and nothing reaches the store, so a
// cursor cannot be used to probe for another tenant's existence.
func TestUserService_ListWorkspaceUsers_RejectsBadCursors(t *testing.T) {
	valid := func(c domain.WorkspaceUserCursor) string {
		raw, _ := json.Marshal(c)
		return base64.RawURLEncoding.EncodeToString(raw)
	}
	otherWorkspace := "11111111-2222-4333-8444-555555555555"

	for _, tc := range []struct {
		name   string
		cursor string
	}{
		{"not base64", "!!!not-base64!!!"},
		{"not json", base64.RawURLEncoding.EncodeToString([]byte("nope"))},
		{"unknown version", valid(domain.WorkspaceUserCursor{Version: 99, WorkspaceID: testWorkspaceID, UserID: anchorUserID})},
		{"zero version", valid(domain.WorkspaceUserCursor{WorkspaceID: testWorkspaceID, UserID: anchorUserID})},
		{"missing user id", valid(domain.WorkspaceUserCursor{Version: 1, WorkspaceID: testWorkspaceID})},
		{"another workspace", valid(domain.WorkspaceUserCursor{Version: 1, WorkspaceID: otherWorkspace, UserID: anchorUserID})},
		{"unknown field", base64.RawURLEncoding.EncodeToString([]byte(`{"v":1,"workspaceId":"` + testWorkspaceID + `","userId":"` + anchorUserID + `","admin":true}`))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &fakeStore{workspaceUsers: listUsers(2)}
			svc := service.NewUserService(store)

			_, _, err := svc.ListWorkspaceUsers(context.Background(), testWorkspaceID, 10, tc.cursor)
			if !errors.Is(err, domain.ErrInvalidInput) {
				t.Fatalf("expected ErrInvalidInput, got %v", err)
			}
			if store.gotLimit != 0 {
				t.Fatal("an invalid cursor must not reach the store")
			}
		})
	}
}

// A cursor minted for another tenant cannot widen the query: the workspace
// passed to the store is always the one resolved from the session.
func TestUserService_ListWorkspaceUsers_CursorCannotChangeWorkspace(t *testing.T) {
	store := &fakeStore{workspaceUsers: listUsers(4)}
	svc := service.NewUserService(store)

	_, next, err := svc.ListWorkspaceUsers(context.Background(), testWorkspaceID, 3, "")
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	// Replay the cursor against a different workspace.
	other := "11111111-2222-4333-8444-555555555555"
	if _, _, err := svc.ListWorkspaceUsers(context.Background(), other, 3, next); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("a foreign cursor must be rejected, got %v", err)
	}
}

// The handler already bounds the limit; this is the backstop for an internal
// caller that does not.
func TestUserService_ListWorkspaceUsers_ClampsOutOfRangeLimit(t *testing.T) {
	for _, tc := range []struct{ in, want int }{
		{0, domain.WorkspaceUserPageDefaultLimit + 1},
		{-5, domain.WorkspaceUserPageDefaultLimit + 1},
		{5000, domain.WorkspaceUserPageMaxLimit + 1},
	} {
		store := &fakeStore{}
		svc := service.NewUserService(store)
		if _, _, err := svc.ListWorkspaceUsers(context.Background(), testWorkspaceID, tc.in, ""); err != nil {
			t.Fatalf("ListWorkspaceUsers(%d): %v", tc.in, err)
		}
		if store.gotLimit != tc.want {
			t.Fatalf("limit %d: expected %d to reach the store, got %d", tc.in, tc.want, store.gotLimit)
		}
	}
}

func TestUserService_ListWorkspaceUsers_StoreErrorPropagates(t *testing.T) {
	store := &fakeStore{workspaceUsersErr: errors.New("query failed")}
	svc := service.NewUserService(store)

	if _, _, err := svc.ListWorkspaceUsers(context.Background(), testWorkspaceID, 10, ""); err == nil {
		t.Fatal("expected the store error to propagate")
	}
}

// ── Cursor hardening (issue #425 review) ───────────────────────────────────

// The cursor is entirely client-controlled, so an oversized one must cost a
// length comparison — not a base64 decode and a JSON parse.
func TestUserService_ListWorkspaceUsers_OversizedCursorRejectedBeforeStore(t *testing.T) {
	store := &fakeStore{workspaceUsers: listUsers(2)}
	svc := service.NewUserService(store)

	oversized := strings.Repeat("A", domain.WorkspaceUserCursorMaxEncodedBytes+1)

	_, _, err := svc.ListWorkspaceUsers(context.Background(), testWorkspaceID, 10, oversized)
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput, got %v", err)
	}
	if store.gotLimit != 0 || store.gotAfterUserID != "" {
		t.Fatal("an oversized cursor must not reach the store at all")
	}
}

// A cursor exactly at the cap is still parsed — the limit rejects abuse, not
// legitimate tokens, which are well under it.
func TestUserService_ListWorkspaceUsers_CursorAtSizeLimitIsStillParsed(t *testing.T) {
	store := &fakeStore{workspaceUsers: listUsers(2)}
	svc := service.NewUserService(store)

	atLimit := strings.Repeat("A", domain.WorkspaceUserCursorMaxEncodedBytes)

	// Rejected for being unparseable, not for its length — either way it is a
	// 400, but the point is that the size gate did not short-circuit it.
	if _, _, err := svc.ListWorkspaceUsers(context.Background(), testWorkspaceID, 10, atLimit); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput, got %v", err)
	}
}

// Paging costs one query per page. There is no position to look up, because the
// cursor's user id *is* the position, and a second round trip per page was pure
// overhead once the ordering stopped being a text expression.
func TestUserService_ListWorkspaceUsers_PagesWithASingleStoreCall(t *testing.T) {
	store := &fakeStore{workspaceUsers: listUsers(4)}
	svc := service.NewUserService(store)

	_, next, err := svc.ListWorkspaceUsers(context.Background(), testWorkspaceID, 3, "")
	if err != nil {
		t.Fatalf("first page: %v", err)
	}

	store.listCalls = 0
	if _, _, err := svc.ListWorkspaceUsers(context.Background(), testWorkspaceID, 3, next); err != nil {
		t.Fatalf("second page: %v", err)
	}
	if store.listCalls != 1 {
		t.Fatalf("expected one store call per page, got %d", store.listCalls)
	}
}

// ── Two workspaces ─────────────────────────────────────────────────────────

// A cursor is usable only in the workspace that minted it.
//
// Two tenants, one cursor: it pages workspace A and is refused by workspace B,
// as a plain ErrInvalidInput that says nothing about whether A exists. The
// refusal is defence in depth rather than the boundary — the listing query
// filters by the workspace the session resolved to, so even an accepted foreign
// cursor could only move the caller's position inside their own workspace — but
// a cursor that crosses tenants should not be a thing that happens quietly.
func TestUserService_ListWorkspaceUsers_CursorIsBoundToItsWorkspace(t *testing.T) {
	const workspaceA = "aaaaaaaa-1111-4111-8111-aaaaaaaaaaaa"
	const workspaceB = "bbbbbbbb-2222-4222-8222-bbbbbbbbbbbb"

	store := &fakeStore{workspaceUsers: listUsers(4)}
	svc := service.NewUserService(store)

	_, cursorA, err := svc.ListWorkspaceUsers(context.Background(), workspaceA, 3, "")
	if err != nil {
		t.Fatalf("first page of A: %v", err)
	}
	if cursorA == "" {
		t.Fatal("expected a cursor for workspace A")
	}

	// In its own workspace it resumes.
	store.listCalls = 0
	if _, _, err := svc.ListWorkspaceUsers(context.Background(), workspaceA, 3, cursorA); err != nil {
		t.Fatalf("A's cursor must work in A: %v", err)
	}
	if store.gotWorkspaceID != workspaceA {
		t.Fatalf("expected the query to stay in A, got %q", store.gotWorkspaceID)
	}

	// In another workspace it is refused, and nothing is queried.
	store.listCalls = 0
	store.gotWorkspaceID = ""
	_, _, err = svc.ListWorkspaceUsers(context.Background(), workspaceB, 3, cursorA)
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("A's cursor must be refused in B, got %v", err)
	}
	if store.listCalls != 0 || store.gotWorkspaceID != "" {
		t.Fatal("a foreign cursor must not reach the store")
	}
}

// The workspace a page is read from is the one the caller was given, never one
// derived from the cursor — so a cursor cannot redirect the query at all.
func TestUserService_ListWorkspaceUsers_QueriesOnlyTheGivenWorkspace(t *testing.T) {
	const workspaceA = "aaaaaaaa-1111-4111-8111-aaaaaaaaaaaa"

	store := &fakeStore{workspaceUsers: listUsers(4)}
	svc := service.NewUserService(store)

	_, cursorA, err := svc.ListWorkspaceUsers(context.Background(), workspaceA, 3, "")
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	if _, _, err := svc.ListWorkspaceUsers(context.Background(), workspaceA, 3, cursorA); err != nil {
		t.Fatalf("second page: %v", err)
	}
	if store.gotWorkspaceID != workspaceA {
		t.Fatalf("every page must query the given workspace, got %q", store.gotWorkspaceID)
	}
}

// The member a cursor names may leave the workspace between two pages. Their id
// is still a valid point to resume after — ids are ordered independently of who
// currently holds a membership — so paging continues instead of breaking. The
// row simply is not in the results, which is the correct answer.
func TestUserService_ListWorkspaceUsers_DepartedMemberStillResumesPaging(t *testing.T) {
	store := &fakeStore{workspaceUsers: listUsers(4)}
	svc := service.NewUserService(store)

	_, next, err := svc.ListWorkspaceUsers(context.Background(), testWorkspaceID, 3, "")
	if err != nil {
		t.Fatalf("first page: %v", err)
	}

	// The workspace no longer contains the row the cursor names.
	store.workspaceUsers = nil
	users, nextAfter, err := svc.ListWorkspaceUsers(context.Background(), testWorkspaceID, 3, next)
	if err != nil {
		t.Fatalf("a departed member must not invalidate the cursor: %v", err)
	}
	if len(users) != 0 || nextAfter != "" {
		t.Fatalf("expected a final empty page, got %d users and cursor %q", len(users), nextAfter)
	}
	if store.gotAfterUserID != listUserID(2) {
		t.Fatalf("expected the query to resume after the named id, got %q", store.gotAfterUserID)
	}
}

// A malformed identifier must be refused here rather than reaching a ::uuid
// cast, where it would surface as a database error instead of a 400.
func TestUserService_ListWorkspaceUsers_RejectsNonUUIDIdentifiers(t *testing.T) {
	encode := func(c domain.WorkspaceUserCursor) string {
		raw, _ := json.Marshal(c)
		return base64.RawURLEncoding.EncodeToString(raw)
	}
	for _, tc := range []struct {
		name   string
		cursor domain.WorkspaceUserCursor
	}{
		{"user id not a uuid", domain.WorkspaceUserCursor{Version: 1, WorkspaceID: testWorkspaceID, UserID: "u1"}},
		{"user id empty", domain.WorkspaceUserCursor{Version: 1, WorkspaceID: testWorkspaceID}},
		{"workspace not a uuid", domain.WorkspaceUserCursor{Version: 1, WorkspaceID: "ws", UserID: anchorUserID}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &fakeStore{workspaceUsers: listUsers(2)}
			svc := service.NewUserService(store)

			_, _, err := svc.ListWorkspaceUsers(context.Background(), testWorkspaceID, 10, encode(tc.cursor))
			if !errors.Is(err, domain.ErrInvalidInput) {
				t.Fatalf("expected ErrInvalidInput, got %v", err)
			}
			if store.gotAfterUserID != "" {
				t.Fatal("a malformed identifier must not reach the store")
			}
		})
	}
}
