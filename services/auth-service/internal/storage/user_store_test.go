package storage_test

import (
	"context"
	"errors"
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

// ── Workspace administration (issue #425) ──────────────────────────────────

// The resolver is the tenant boundary for the admin API: it must ask only for
// memberships that are active, in an active workspace, and carry an
// administrative role. A caller matching none of that is forbidden, not empty.
func TestPGXUserStore_GetAdminWorkspaceID_Success(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery(`FROM chat\.workspace_members wm`).
		WithArgs("actor-1").
		WillReturnRows(pgxmock.NewRows([]string{"workspace_id"}).AddRow("ws-1"))

	store := storage.NewPGXUserStore(mock)
	workspaceID, err := store.GetAdminWorkspaceID(context.Background(), "actor-1")
	if err != nil {
		t.Fatalf("GetAdminWorkspaceID: %v", err)
	}
	if workspaceID != "ws-1" {
		t.Fatalf("expected ws-1, got %q", workspaceID)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXUserStore_GetAdminWorkspaceID_NoAdminMembershipIsForbidden(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery(`FROM chat\.workspace_members wm`).
		WithArgs("member-1").
		WillReturnRows(pgxmock.NewRows([]string{"workspace_id"}))

	store := storage.NewPGXUserStore(mock)
	_, err = store.GetAdminWorkspaceID(context.Background(), "member-1")
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("expected ErrForbidden for a non-admin, got %v", err)
	}
}

func TestPGXUserStore_GetAdminWorkspaceID_QueryErrorIsWrapped(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery(`FROM chat\.workspace_members wm`).
		WithArgs("actor-1").
		WillReturnError(errors.New("connection refused"))

	store := storage.NewPGXUserStore(mock)
	_, err = store.GetAdminWorkspaceID(context.Background(), "actor-1")
	if err == nil || errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("an infrastructure failure must not read as forbidden, got %v", err)
	}
}

// ── Workspace user listing, keyset paginated (issues #425, #433) ───────────

func workspaceUserRows() *pgxmock.Rows {
	created := time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC)
	return pgxmock.NewRows([]string{
		"id", "email", "display_name", "full_name", "status", "auth_source", "created_at", "sort_key",
	}).
		AddRow("u1", "alice@example.com", "Alice", "Alice Andrade", "active", "manual", created, "alice").
		AddRow("u2", "bob@example.com", "Bob", "", "suspended", "oidc", created, "bob")
}

// The listing must be scoped by the workspace it was given and by nothing
// else — that argument is what keeps one workspace's admin out of another's
// member list. A nil cursor passes NULL, which the query's `$2 IS NULL` branch
// turns into "start from the beginning".
func TestPGXUserStore_ListWorkspaceUsers_FirstPageScopesToWorkspace(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery(`FROM chat\.workspace_members wm`).
		WithArgs("ws-1", nil, nil, 51).
		WillReturnRows(workspaceUserRows())

	store := storage.NewPGXUserStore(mock)
	users, err := store.ListWorkspaceUsers(context.Background(), "ws-1", 51, nil)
	if err != nil {
		t.Fatalf("ListWorkspaceUsers: %v", err)
	}
	if len(users) != 2 {
		t.Fatalf("expected 2 users, got %d", len(users))
	}
	if users[0].Email != "alice@example.com" || users[0].FullName != "Alice Andrade" {
		t.Fatalf("unexpected first row: %+v", users[0])
	}
	// The sort key travels with the row so the cursor resumes from exactly the
	// position PostgreSQL ordered by.
	if users[0].SortKey != "alice" || users[1].SortKey != "bob" {
		t.Fatalf("expected sort keys to be carried out, got %q/%q", users[0].SortKey, users[1].SortKey)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// A cursor resumes after (sortKey, id) — a row-value comparison, not OFFSET,
// so page N costs the same as page 1 and concurrent inserts cannot shift it.
func TestPGXUserStore_ListWorkspaceUsers_CursorResumesAfterPosition(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery(`\(lower\(coalesce.*u\.id::text\) > \(\$2::text, \$3::text\)`).
		WithArgs("ws-1", "alice", "u1", 51).
		WillReturnRows(workspaceUserRows())

	store := storage.NewPGXUserStore(mock)
	_, err = store.ListWorkspaceUsers(context.Background(), "ws-1", 51, &domain.WorkspaceUserCursor{
		Version: 1, WorkspaceID: "ws-1", SortKey: "alice", UserID: "u1",
	})
	if err != nil {
		t.Fatalf("ListWorkspaceUsers: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// The id tiebreak is what makes the order total: two people with the same
// display name would otherwise have an undefined relative position, and a
// keyset cursor over an unstable order silently skips or repeats rows.
func TestPGXUserStore_ListWorkspaceUsers_OrdersDeterministicallyWithIDTiebreak(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery(`ORDER BY lower\(coalesce\(nullif\(u\.display_name, ''\), u\.email::text\)\), u\.id`).
		WithArgs("ws-1", nil, nil, 51).
		WillReturnRows(workspaceUserRows())

	store := storage.NewPGXUserStore(mock)
	if _, err := store.ListWorkspaceUsers(context.Background(), "ws-1", 51, nil); err != nil {
		t.Fatalf("ListWorkspaceUsers: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// The limit is applied by the database, not by trimming in Go: the whole point
// is never to materialise more than one page plus the lookahead row.
func TestPGXUserStore_ListWorkspaceUsers_AppliesLimitInSQL(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery(`LIMIT \$4`).
		WithArgs("ws-1", nil, nil, 11).
		WillReturnRows(workspaceUserRows())

	store := storage.NewPGXUserStore(mock)
	if _, err := store.ListWorkspaceUsers(context.Background(), "ws-1", 11, nil); err != nil {
		t.Fatalf("ListWorkspaceUsers: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// An empty workspace yields an empty slice, never nil: the handler serialises
// it straight to JSON, and null would reach the client as a distinct value.
func TestPGXUserStore_ListWorkspaceUsers_EmptyIsNonNilSlice(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery(`FROM chat\.workspace_members wm`).
		WithArgs("ws-empty", nil, nil, 51).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "email", "display_name", "full_name", "status", "auth_source", "created_at", "sort_key",
		}))

	store := storage.NewPGXUserStore(mock)
	users, err := store.ListWorkspaceUsers(context.Background(), "ws-empty", 51, nil)
	if err != nil {
		t.Fatalf("ListWorkspaceUsers: %v", err)
	}
	if users == nil || len(users) != 0 {
		t.Fatalf("expected a non-nil empty slice, got %v", users)
	}
}

func TestPGXUserStore_ListWorkspaceUsers_QueryErrorIsReturned(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery(`FROM chat\.workspace_members wm`).
		WithArgs("ws-1", nil, nil, 51).
		WillReturnError(errors.New("relation does not exist"))

	store := storage.NewPGXUserStore(mock)
	if _, err := store.ListWorkspaceUsers(context.Background(), "ws-1", 51, nil); err == nil {
		t.Fatal("expected an error when the query fails")
	}
}
