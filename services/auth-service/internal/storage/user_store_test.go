package storage_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	pgxmock "github.com/pashagolub/pgxmock/v2"

	"github.com/nicrepository/nchat/services/auth-service/internal/domain"
	"github.com/nicrepository/nchat/services/auth-service/internal/storage"
)

func TestPGXUserStore_GetPolicySettings_Success(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery(`SELECT min_password_length`).
		WillReturnRows(pgxmock.NewRows([]string{
			"min_password_length", "require_uppercase", "require_lowercase",
			"require_number", "require_symbol", "failed_login_limit",
			"failed_login_window_minutes", "failed_login_lockout_minutes",
			"session_idle_timeout_minutes", "max_devices_per_user",
			"password_reset_token_ttl_minutes", "invite_token_ttl_hours",
		}).AddRow(12, true, true, true, true, 5, 15, 15, 60, 5, 60, 72))

	store := storage.NewPGXUserStore(mock)
	policy, err := store.GetPolicySettings(context.Background())
	if err != nil {
		t.Fatalf("GetPolicySettings: %v", err)
	}
	if policy.MinPasswordLength != 12 {
		t.Fatalf("expected 12, got %d", policy.MinPasswordLength)
	}
	if !policy.RequireUppercase {
		t.Fatal("expected RequireUppercase true")
	}
	if policy.FailedLoginLimit != 5 || policy.FailedLoginWindowMinutes != 15 || policy.FailedLoginLockoutMinutes != 15 {
		t.Fatalf("unexpected failed login policy: %+v", policy)
	}
	if policy.SessionIdleTimeoutMinutes != 60 || policy.MaxDevicesPerUser != 5 {
		t.Fatalf("unexpected session/device policy: %+v", policy)
	}
	if policy.PasswordResetTokenTTLMinutes != 60 || policy.InviteTokenTTLHours != 72 {
		t.Fatalf("unexpected recovery/invite policy: %+v", policy)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXUserStore_GetPolicySettings_QueryError(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery(`SELECT min_password_length`).
		WillReturnError(errors.New("connection lost"))

	store := storage.NewPGXUserStore(mock)
	_, err = store.GetPolicySettings(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXUserStore_CreateUser_Success(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	userID := "550e8400-e29b-41d4-a716-446655440000"
	now := time.Now()

	mock.ExpectBegin()
	mock.ExpectQuery(`INSERT INTO auth\.users`).
		WithArgs("user@example.com", "User Name", nil).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "email", "display_name", "full_name", "status", "auth_source",
			"email_verified_at", "created_at", "updated_at",
		}).AddRow(userID, "user@example.com", "User Name", "", "active", "manual", now, now, now))
	mock.ExpectExec(`INSERT INTO auth\.user_password_credentials`).
		WithArgs(userID, "$argon2id$test-hash", true).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit()
	mock.ExpectRollback() // deferred Rollback after Commit returns pgx.ErrTxClosed

	store := storage.NewPGXUserStore(mock)
	input := domain.CreateUserInput{
		Email: "user@example.com", DisplayName: "User Name",
		MustChangePassword: true,
	}
	user, err := store.CreateUser(context.Background(), input, "$argon2id$test-hash")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if user.ID != userID {
		t.Fatalf("expected %q, got %q", userID, user.ID)
	}
	if user.Email != "user@example.com" {
		t.Fatalf("expected user@example.com, got %q", user.Email)
	}
	if user.Status != "active" {
		t.Fatalf("expected active, got %q", user.Status)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXUserStore_CreateUser_WithFullName(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	userID := "550e8400-e29b-41d4-a716-446655440001"
	now := time.Now()

	mock.ExpectBegin()
	mock.ExpectQuery(`INSERT INTO auth\.users`).
		WithArgs("user@example.com", "User Name", "Full Name").
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "email", "display_name", "full_name", "status", "auth_source",
			"email_verified_at", "created_at", "updated_at",
		}).AddRow(userID, "user@example.com", "User Name", "Full Name", "active", "manual", now, now, now))
	mock.ExpectExec(`INSERT INTO auth\.user_password_credentials`).
		WithArgs(userID, "hash", false).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit()
	mock.ExpectRollback()

	store := storage.NewPGXUserStore(mock)
	input := domain.CreateUserInput{
		Email: "user@example.com", DisplayName: "User Name", FullName: "Full Name",
	}
	user, err := store.CreateUser(context.Background(), input, "hash")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if user.FullName != "Full Name" {
		t.Fatalf("expected 'Full Name', got %q", user.FullName)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXUserStore_CreateUser_CredentialInsertError(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	userID := "550e8400-e29b-41d4-a716-446655440002"
	now := time.Now()

	mock.ExpectBegin()
	mock.ExpectQuery(`INSERT INTO auth\.users`).
		WithArgs("user@example.com", "User", nil).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "email", "display_name", "full_name", "status", "auth_source",
			"email_verified_at", "created_at", "updated_at",
		}).AddRow(userID, "user@example.com", "User", "", "active", "manual", now, now, now))
	mock.ExpectExec(`INSERT INTO auth\.user_password_credentials`).
		WithArgs(userID, "hash", false).
		WillReturnError(errors.New("credential insert failed"))
	mock.ExpectRollback()

	store := storage.NewPGXUserStore(mock)
	input := domain.CreateUserInput{Email: "user@example.com", DisplayName: "User"}
	_, err = store.CreateUser(context.Background(), input, "hash")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXUserStore_CreateUser_BeginTxError(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectBegin().WillReturnError(errors.New("begin tx failed"))

	store := storage.NewPGXUserStore(mock)
	input := domain.CreateUserInput{Email: "user@example.com", DisplayName: "User"}
	_, err = store.CreateUser(context.Background(), input, "hash")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXUserStore_CreateUser_DuplicateEmail(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectBegin()
	mock.ExpectQuery(`INSERT INTO auth\.users`).
		WithArgs("dup@example.com", "Dup", nil).
		WillReturnError(&pgconn.PgError{Code: "23505"})
	mock.ExpectRollback()

	store := storage.NewPGXUserStore(mock)
	input := domain.CreateUserInput{Email: "dup@example.com", DisplayName: "Dup"}
	_, err = store.CreateUser(context.Background(), input, "hash")
	if !errors.Is(err, domain.ErrDuplicateEmail) {
		t.Fatalf("expected ErrDuplicateEmail, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func userRow() *pgxmock.Rows {
	return pgxmock.NewRows([]string{
		"id", "email", "display_name", "full_name", "status", "auth_source",
		"email_verified_at", "created_at", "updated_at",
	}).AddRow("uid-1", "u@example.com", "User", "", "active", "manual",
		time.Now(), time.Now(), time.Now())
}

func TestPGXUserStore_GetUserByID_Success(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery(`SELECT id, email`).
		WithArgs("uid-1").
		WillReturnRows(userRow())

	store := storage.NewPGXUserStore(mock)
	user, err := store.GetUserByID(context.Background(), "uid-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if user.ID != "uid-1" {
		t.Fatalf("expected uid-1, got %q", user.ID)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXUserStore_GetUserByID_NotFound(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery(`SELECT id, email`).
		WithArgs("no-such").
		WillReturnRows(pgxmock.NewRows([]string{"id", "email", "display_name", "full_name", "status", "auth_source", "email_verified_at", "created_at", "updated_at"})) // empty result set

	store := storage.NewPGXUserStore(mock)
	_, err = store.GetUserByID(context.Background(), "no-such")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXUserStore_UpdateUserStatus_Suspend_Atomic(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	// Atomic: BEGIN, lock+read status, UPDATE users, revoke sessions CTE,
	// invalidate OIDC exchange codes, COMMIT — all or nothing.
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT status FROM auth\.users`).
		WithArgs("uid-1").
		WillReturnRows(pgxmock.NewRows([]string{"status"}).AddRow("active"))
	mock.ExpectQuery(`UPDATE auth\.users`).
		WithArgs("uid-1", "suspended").
		WillReturnRows(userRow())
	mock.ExpectExec(`WITH revoked AS`).
		WithArgs("uid-1").
		WillReturnResult(pgxmock.NewResult("UPDATE", 2))
	mock.ExpectExec(`UPDATE auth\.oidc_exchange_codes`).
		WithArgs("uid-1").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectCommit()
	mock.ExpectRollback()

	store := storage.NewPGXUserStore(mock)
	user, err := store.UpdateUserStatus(context.Background(), "uid-1", "suspended")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if user.ID != "uid-1" {
		t.Fatalf("expected uid-1, got %q", user.ID)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXUserStore_UpdateUserStatus_Activate_NoRevocationNoOIDCInvalidation(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	// Activation: no session revocation and no OIDC exchange code invalidation.
	// A code previously invalidated by suspension retains its used_at marker.
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT status FROM auth\.users`).
		WithArgs("uid-1").
		WillReturnRows(pgxmock.NewRows([]string{"status"}).AddRow("suspended"))
	mock.ExpectQuery(`UPDATE auth\.users`).
		WithArgs("uid-1", "active").
		WillReturnRows(userRow())
	mock.ExpectCommit()
	mock.ExpectRollback()

	store := storage.NewPGXUserStore(mock)
	user, err := store.UpdateUserStatus(context.Background(), "uid-1", "active")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if user.ID != "uid-1" {
		t.Fatalf("expected uid-1, got %q", user.ID)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXUserStore_UpdateUserStatus_NotFound(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT status FROM auth\.users`).
		WithArgs("no-id").
		WillReturnRows(pgxmock.NewRows([]string{"status"})) // empty = not found
	mock.ExpectRollback()

	store := storage.NewPGXUserStore(mock)
	_, err = store.UpdateUserStatus(context.Background(), "no-id", "suspended")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXUserStore_UpdateUserStatus_InvalidTransition_Rejected(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	// active→active is invalid; status is read under lock then rejected
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT status FROM auth\.users`).
		WithArgs("uid-1").
		WillReturnRows(pgxmock.NewRows([]string{"status"}).AddRow("active"))
	mock.ExpectRollback()

	store := storage.NewPGXUserStore(mock)
	_, err = store.UpdateUserStatus(context.Background(), "uid-1", "active")
	if !errors.Is(err, domain.ErrStatusTransitionNotAllowed) {
		t.Fatalf("expected ErrStatusTransitionNotAllowed, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXUserStore_UpdateUserStatus_RevocationFailure_RollsBackStatus(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	// Status update succeeds but session revocation fails → entire TX rolls back.
	// No partial state: user remains active.
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT status FROM auth\.users`).
		WithArgs("uid-1").
		WillReturnRows(pgxmock.NewRows([]string{"status"}).AddRow("active"))
	mock.ExpectQuery(`UPDATE auth\.users`).
		WithArgs("uid-1", "suspended").
		WillReturnRows(userRow())
	mock.ExpectExec(`WITH revoked AS`).
		WithArgs("uid-1").
		WillReturnError(errors.New("revocation db error"))
	mock.ExpectRollback()

	store := storage.NewPGXUserStore(mock)
	_, err = store.UpdateUserStatus(context.Background(), "uid-1", "suspended")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// TestPGXUserStore_UpdateUserStatus_OIDCInvalidationFailure_RollsBackAll verifies that
// if OIDC exchange code invalidation fails, the entire transaction rolls back —
// user status is not changed and sessions are not revoked.
func TestPGXUserStore_UpdateUserStatus_OIDCInvalidationFailure_RollsBackAll(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT status FROM auth\.users`).
		WithArgs("uid-1").
		WillReturnRows(pgxmock.NewRows([]string{"status"}).AddRow("active"))
	mock.ExpectQuery(`UPDATE auth\.users`).
		WithArgs("uid-1", "suspended").
		WillReturnRows(userRow())
	mock.ExpectExec(`WITH revoked AS`).
		WithArgs("uid-1").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec(`UPDATE auth\.oidc_exchange_codes`).
		WithArgs("uid-1").
		WillReturnError(errors.New("oidc invalidation db error"))
	mock.ExpectRollback()

	store := storage.NewPGXUserStore(mock)
	_, err = store.UpdateUserStatus(context.Background(), "uid-1", "suspended")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations (all three operations must roll back): %v", err)
	}
}

// TestPGXUserStore_SuspendOIDCExchangeLifecycle is a regression test for the
// OIDC exchange code invalidation on suspension scenario:
//
//  1. A pending exchange code exists for the user (used_at IS NULL).
//  2. User is suspended → suspension TX sets used_at on the exchange code.
//  3. Exchange code cannot be consumed while user is suspended
//     (ConsumeExchange checks used_at IS NULL AND user.status='active').
//  4. User is reactivated → activation TX does NOT reset used_at.
//  5. The same exchange code is still rejected because used_at was set in step 2.
//
// This test verifies steps 2 and 4 at the storage level using mock expectations.
func TestPGXUserStore_SuspendOIDCExchangeLifecycle(t *testing.T) {
	t.Run("step2_suspension_sets_used_at_on_exchange_code", func(t *testing.T) {
		mock, err := pgxmock.NewPool()
		if err != nil {
			t.Fatalf("pgxmock: %v", err)
		}
		defer mock.Close()

		mock.ExpectBegin()
		mock.ExpectQuery(`SELECT status FROM auth\.users`).
			WithArgs("user-abc").
			WillReturnRows(pgxmock.NewRows([]string{"status"}).AddRow("active"))
		mock.ExpectQuery(`UPDATE auth\.users`).
			WithArgs("user-abc", "suspended").
			WillReturnRows(pgxmock.NewRows([]string{
				"id", "email", "display_name", "full_name", "status", "auth_source",
				"email_verified_at", "created_at", "updated_at",
			}).AddRow("user-abc", "u@example.com", "User", "", "suspended", "manual",
				time.Now(), time.Now(), time.Now()))
		mock.ExpectExec(`WITH revoked AS`).
			WithArgs("user-abc").
			WillReturnResult(pgxmock.NewResult("UPDATE", 1))
		// Verify the exchange code invalidation SQL is executed with the user's ID.
		mock.ExpectExec(`UPDATE auth\.oidc_exchange_codes`).
			WithArgs("user-abc").
			WillReturnResult(pgxmock.NewResult("UPDATE", 1)) // 1 code invalidated
		mock.ExpectCommit()
		mock.ExpectRollback()

		store := storage.NewPGXUserStore(mock)
		user, err := store.UpdateUserStatus(context.Background(), "user-abc", "suspended")
		if err != nil {
			t.Fatalf("suspension error: %v", err)
		}
		if user.Status != "suspended" {
			t.Fatalf("expected suspended, got %q", user.Status)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("suspension did not run OIDC invalidation: %v", err)
		}
	})

	t.Run("step4_activation_does_not_reset_used_at", func(t *testing.T) {
		mock, err := pgxmock.NewPool()
		if err != nil {
			t.Fatalf("pgxmock: %v", err)
		}
		defer mock.Close()

		// Activation TX must NOT include any UPDATE to oidc_exchange_codes.
		mock.ExpectBegin()
		mock.ExpectQuery(`SELECT status FROM auth\.users`).
			WithArgs("user-abc").
			WillReturnRows(pgxmock.NewRows([]string{"status"}).AddRow("suspended"))
		mock.ExpectQuery(`UPDATE auth\.users`).
			WithArgs("user-abc", "active").
			WillReturnRows(pgxmock.NewRows([]string{
				"id", "email", "display_name", "full_name", "status", "auth_source",
				"email_verified_at", "created_at", "updated_at",
			}).AddRow("user-abc", "u@example.com", "User", "", "active", "manual",
				time.Now(), time.Now(), time.Now()))
		mock.ExpectCommit()
		mock.ExpectRollback()

		store := storage.NewPGXUserStore(mock)
		user, err := store.UpdateUserStatus(context.Background(), "user-abc", "active")
		if err != nil {
			t.Fatalf("activation error: %v", err)
		}
		if user.Status != "active" {
			t.Fatalf("expected active, got %q", user.Status)
		}
		// ExpectationsWereMet verifies NO oidc_exchange_codes UPDATE was sent.
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("activation unexpectedly touched oidc_exchange_codes: %v", err)
		}
	})
}

// ── Workspace administration ──────────────────────────────────

// The resolver is the tenant boundary for the admin API: it must ask only for
// memberships that are active, in an active workspace, and carry an
// administrative role. A caller matching none of that is forbidden, not empty.
func TestPGXUserStore_ResolveAdminWorkspaceID_SingleAdminMembership(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery(`FROM chat\.workspace_members wm`).
		WithArgs("actor-1").
		WillReturnRows(pgxmock.NewRows([]string{"workspace_id"}).AddRow("ws-1"))

	store := storage.NewPGXUserStore(mock)
	workspaceID, err := store.ResolveAdminWorkspaceID(context.Background(), "actor-1", "")
	if err != nil {
		t.Fatalf("ResolveAdminWorkspaceID: %v", err)
	}
	if workspaceID != "ws-1" {
		t.Fatalf("expected ws-1, got %q", workspaceID)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXUserStore_ResolveAdminWorkspaceID_NoAdminMembershipIsForbidden(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery(`FROM chat\.workspace_members wm`).
		WithArgs("member-1").
		WillReturnRows(pgxmock.NewRows([]string{"workspace_id"}))

	store := storage.NewPGXUserStore(mock)
	_, err = store.ResolveAdminWorkspaceID(context.Background(), "member-1", "")
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("expected ErrForbidden for a non-admin, got %v", err)
	}
}

// The behaviour this replaced picked the oldest membership. Several
// administered workspaces must now be an error the caller has to resolve, not
// a silent choice of tenant.
func TestPGXUserStore_ResolveAdminWorkspaceID_MultipleRequiresSelection(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery(`FROM chat\.workspace_members wm`).
		WithArgs("actor-1").
		WillReturnRows(pgxmock.NewRows([]string{"workspace_id"}).AddRow("ws-1").AddRow("ws-2"))

	store := storage.NewPGXUserStore(mock)
	workspaceID, err := store.ResolveAdminWorkspaceID(context.Background(), "actor-1", "")
	if !errors.Is(err, domain.ErrWorkspaceSelectionRequired) {
		t.Fatalf("expected ErrWorkspaceSelectionRequired, got %v", err)
	}
	if workspaceID != "" {
		t.Fatalf("a refused resolution must yield no workspace, got %q", workspaceID)
	}
}

// The unselected query must not order by anything: ordering plus a limit is
// exactly the "oldest wins" tenant choice this replaced. Asserted on the SQL
// the store actually issues, because a regex over the query text is the only
// place this invariant is observable.
func TestPGXUserStore_ResolveAdminWorkspaceID_QueryDoesNotOrderToPickATenant(t *testing.T) {
	var issued []string
	mock, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(
		pgxmock.QueryMatcherFunc(func(_, actualSQL string) error {
			issued = append(issued, actualSQL)
			return nil
		}),
	))
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery(``).
		WithArgs("actor-1").
		WillReturnRows(pgxmock.NewRows([]string{"workspace_id"}).AddRow("ws-1"))

	store := storage.NewPGXUserStore(mock)
	if _, err := store.ResolveAdminWorkspaceID(context.Background(), "actor-1", ""); err != nil {
		t.Fatalf("ResolveAdminWorkspaceID: %v", err)
	}
	if len(issued) != 1 {
		t.Fatalf("expected one query, got %d", len(issued))
	}
	if strings.Contains(strings.ToUpper(issued[0]), "ORDER BY") {
		t.Fatalf("tenant resolution must not order rows to pick one:\n%s", issued[0])
	}
	if !strings.Contains(issued[0], "LIMIT 2") {
		t.Fatalf("expected the two-row probe that distinguishes 0/1/many:\n%s", issued[0])
	}
}

// The selector narrows; it never authorizes. It is passed to a query carrying
// the same membership predicate, so it can only ever match a workspace the
// caller already administers.
func TestPGXUserStore_ResolveAdminWorkspaceID_SelectorIsCheckedAgainstMembership(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery(`wm\.workspace_id = \$2::uuid`).
		WithArgs("actor-1", "ws-2").
		WillReturnRows(pgxmock.NewRows([]string{"workspace_id"}).AddRow("ws-2"))

	store := storage.NewPGXUserStore(mock)
	workspaceID, err := store.ResolveAdminWorkspaceID(context.Background(), "actor-1", "ws-2")
	if err != nil {
		t.Fatalf("ResolveAdminWorkspaceID: %v", err)
	}
	if workspaceID != "ws-2" {
		t.Fatalf("expected ws-2, got %q", workspaceID)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// A selector naming a workspace the caller does not administer — including one
// that does not exist — is forbidden, with no hint of which it was.
func TestPGXUserStore_ResolveAdminWorkspaceID_UnmatchedSelectorIsForbidden(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery(`wm\.workspace_id = \$2::uuid`).
		WithArgs("actor-1", "ws-9").
		WillReturnRows(pgxmock.NewRows([]string{"workspace_id"}))

	store := storage.NewPGXUserStore(mock)
	_, err = store.ResolveAdminWorkspaceID(context.Background(), "actor-1", "ws-9")
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("expected ErrForbidden, got %v", err)
	}
}

func TestPGXUserStore_ResolveAdminWorkspaceID_QueryErrorIsWrapped(t *testing.T) {
	for _, tt := range []struct {
		name     string
		selector string
	}{
		{name: "unselected", selector: ""},
		{name: "selected", selector: "ws-2"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			mock, err := pgxmock.NewPool()
			if err != nil {
				t.Fatalf("pgxmock: %v", err)
			}
			defer mock.Close()

			mock.ExpectQuery(`FROM chat\.workspace_members wm`).
				WillReturnError(errors.New("connection refused"))

			store := storage.NewPGXUserStore(mock)
			_, err = store.ResolveAdminWorkspaceID(context.Background(), "actor-1", tt.selector)
			if err == nil || errors.Is(err, domain.ErrForbidden) || errors.Is(err, domain.ErrWorkspaceSelectionRequired) {
				t.Fatalf("an infrastructure failure must not read as a decision, got %v", err)
			}
		})
	}
}
