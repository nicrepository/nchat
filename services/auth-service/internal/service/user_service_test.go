package service_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
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
	gotCursor         *domain.WorkspaceUserCursor

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

func (f *fakeStore) ListWorkspaceUsers(_ context.Context, workspaceID string, limit int, cursor *domain.WorkspaceUserCursor) ([]domain.WorkspaceUser, error) {
	f.gotWorkspaceID = workspaceID
	f.gotLimit = limit
	f.gotCursor = cursor
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

func listUsers(n int) []domain.WorkspaceUser {
	out := make([]domain.WorkspaceUser, 0, n)
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("u%02d", i)
		out = append(out, domain.WorkspaceUser{ID: id, DisplayName: id, SortKey: id})
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
	if users[2].ID != "u02" {
		t.Fatalf("expected the page to end at u02, got %q", users[2].ID)
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
	if c.Version != domain.WorkspaceUserCursorVersion || c.UserID != "u02" || c.SortKey != "u02" {
		t.Fatalf("unexpected cursor: %+v", c)
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
	if store.gotCursor == nil || store.gotCursor.UserID != "u02" {
		t.Fatalf("expected the decoded cursor to reach the store, got %+v", store.gotCursor)
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
		{"unknown version", valid(domain.WorkspaceUserCursor{Version: 99, WorkspaceID: testWorkspaceID, UserID: "u1"})},
		{"zero version", valid(domain.WorkspaceUserCursor{WorkspaceID: testWorkspaceID, UserID: "u1"})},
		{"missing user id", valid(domain.WorkspaceUserCursor{Version: 1, WorkspaceID: testWorkspaceID})},
		{"another workspace", valid(domain.WorkspaceUserCursor{Version: 1, WorkspaceID: otherWorkspace, UserID: "u1"})},
		{"unknown field", base64.RawURLEncoding.EncodeToString([]byte(`{"v":1,"workspaceId":"` + testWorkspaceID + `","userId":"u1","admin":true}`))},
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
