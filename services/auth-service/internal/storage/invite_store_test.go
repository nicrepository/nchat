package storage_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	pgxmock "github.com/pashagolub/pgxmock/v2"

	"github.com/nicrepository/nchat/services/auth-service/internal/domain"
	"github.com/nicrepository/nchat/services/auth-service/internal/storage"
)

const encryptedInvitePayload = `{"alg":"AES-256-GCM","key_version":"v1","nonce":"bm9uY2U=","ciphertext":"Y2lwaGVydGV4dA=="}`

// Issue #425: invites are workspace-scoped. Every guard below is keyed by the
// workspace so two tenants can invite the same address independently.
const (
	inviteWorkspaceID      = "9a8b7c6d-5e4f-4a3b-8c2d-1e0f9a8b7c6d"
	inviteOtherWorkspaceID = "11111111-2222-4333-8444-555555555555"
	inviteActorID          = "3f1c2d4e-5a6b-4c8d-9e0f-1a2b3c4d5e6f"
	inviteEmail            = "user@example.com"

	// The lock keys the store builds. Reproduced literally so a change to the
	// key shape shows up as a test failure rather than as two workspaces
	// silently serialising on each other.
	inviteEmailLockKey  = inviteWorkspaceID + "\x00" + inviteEmail
	inviteBudgetLockKey = "invite-budget\x00" + inviteWorkspaceID + "\x00" + inviteActorID
)

// unlimited disables the budget, which skips the counting queries entirely.
var unlimited = domain.InviteRateLimit{}

// uniqueViolation is the PostgreSQL error the partial unique index raises when
// a second pending invite for the same (workspace, email) is inserted.
func uniqueViolation() error {
	return &pgconn.PgError{Code: "23505", Message: "duplicate key value violates unique constraint"}
}

func inviteInput() domain.AdminInviteInput {
	return domain.AdminInviteInput{
		WorkspaceID: inviteWorkspaceID,
		ActorID:     inviteActorID,
		Email:       inviteEmail,
		DisplayName: "User",
		FullName:    "User Full",
	}
}

// expectInviteCreateGuards scripts the duplicate/pending checks that run under
// the advisory lock, with the budget disabled.
func expectInviteCreateGuards(mock pgxmock.PgxPoolIface) {
	mock.ExpectExec(`SELECT pg_advisory_xact_lock`).
		WithArgs(inviteEmailLockKey).
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectQuery(`JOIN chat\.workspace_members`).
		WithArgs(inviteWorkspaceID, inviteEmail).
		WillReturnError(pgx.ErrNoRows)
	mock.ExpectQuery(`FROM auth\.user_invites`).
		WithArgs(inviteWorkspaceID, inviteEmail).
		WillReturnError(pgx.ErrNoRows)
}

// expectInviteBudgetPass scripts the email lock, the budget lock and a count
// that leaves room, i.e. everything that runs before the duplicate guards when
// a limit is configured.
func expectInviteBudgetPass(mock pgxmock.PgxPoolIface) {
	mock.ExpectExec(`SELECT pg_advisory_xact_lock`).WithArgs(inviteEmailLockKey).
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectExec(`SELECT pg_advisory_xact_lock`).WithArgs(inviteBudgetLockKey).
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectQuery(`SELECT count\(\*\)`).WithArgs(inviteActorID, inviteWorkspaceID, 10).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(0))
}

func inviteInsertRows(createdAt time.Time) *pgxmock.Rows {
	return pgxmock.NewRows([]string{"id", "email", "workspace_id", "created_at"}).
		AddRow("invite-1", inviteEmail, inviteWorkspaceID, createdAt)
}

// ── Creation ───────────────────────────────────────────────────────────────

func TestPGXInviteStore_CreateInviteInsertsInviteAndEncryptedOutbox(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	createdAt := time.Now()
	expiresAt := time.Now().Add(72 * time.Hour)
	mock.ExpectBegin()
	expectInviteCreateGuards(mock)
	mock.ExpectQuery(`INSERT INTO auth\.user_invites`).
		WithArgs(inviteWorkspaceID, inviteActorID, inviteEmail, "hashed-invite-value", expiresAt).
		WillReturnRows(inviteInsertRows(createdAt))
	mock.ExpectExec(`INSERT INTO auth\.email_outbox`).
		WithArgs(inviteEmail, "invite-1", encryptedInvitePayload).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit()
	mock.ExpectRollback()

	store := storage.NewPGXInviteStore(mock)
	result, err := store.CreateInvite(context.Background(), inviteInput(), "hashed-invite-value", expiresAt, encryptedInvitePayload, unlimited)
	if err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}
	if result.ID != "invite-1" || result.Email != inviteEmail || !result.CreatedAt.Equal(createdAt) {
		t.Fatalf("unexpected invite result: %+v", result)
	}
	// The persisted workspace is what the acceptance will turn into a
	// membership, so it must round-trip.
	if result.WorkspaceID != inviteWorkspaceID {
		t.Fatalf("expected workspace %q on the result, got %q", inviteWorkspaceID, result.WorkspaceID)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// Being a member of *another* workspace must not block the invite: identity is
// global, onboarding is per workspace.
func TestPGXInviteStore_CreateInviteScopesGuardsToWorkspace(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	expiresAt := time.Now().Add(time.Hour)
	mock.ExpectBegin()
	// Both guard queries must be given the workspace as their first argument.
	mock.ExpectExec(`SELECT pg_advisory_xact_lock`).
		WithArgs(inviteEmailLockKey).
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectQuery(`JOIN chat\.workspace_members`).
		WithArgs(inviteWorkspaceID, inviteEmail).
		WillReturnError(pgx.ErrNoRows)
	mock.ExpectQuery(`FROM auth\.user_invites`).
		WithArgs(inviteWorkspaceID, inviteEmail).
		WillReturnError(pgx.ErrNoRows)
	mock.ExpectQuery(`INSERT INTO auth\.user_invites`).
		WithArgs(inviteWorkspaceID, inviteActorID, inviteEmail, "hash", expiresAt).
		WillReturnRows(inviteInsertRows(time.Now()))
	mock.ExpectExec(`INSERT INTO auth\.email_outbox`).WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit()
	mock.ExpectRollback()

	store := storage.NewPGXInviteStore(mock)
	if _, err := store.CreateInvite(context.Background(), inviteInput(), "hash", expiresAt, encryptedInvitePayload, unlimited); err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// A different workspace builds a different advisory-lock key, which is what
// keeps concurrent onboarding in two tenants from serialising.
func TestPGXInviteStore_CreateInviteLockKeyIncludesWorkspace(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	input := inviteInput()
	input.WorkspaceID = inviteOtherWorkspaceID
	expiresAt := time.Now().Add(time.Hour)

	mock.ExpectBegin()
	mock.ExpectExec(`SELECT pg_advisory_xact_lock`).
		WithArgs(inviteOtherWorkspaceID + "\x00" + inviteEmail).
		WillReturnError(errors.New("stop here"))
	mock.ExpectRollback()

	store := storage.NewPGXInviteStore(mock)
	if _, err := store.CreateInvite(context.Background(), input, "hash", expiresAt, encryptedInvitePayload, unlimited); err == nil {
		t.Fatal("expected the scripted lock failure")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXInviteStore_CreateInviteRejectsExistingMember(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectBegin()
	mock.ExpectExec(`SELECT pg_advisory_xact_lock`).
		WithArgs(inviteEmailLockKey).
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectQuery(`JOIN chat\.workspace_members`).
		WithArgs(inviteWorkspaceID, inviteEmail).
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(1))
	mock.ExpectRollback()

	store := storage.NewPGXInviteStore(mock)
	_, err = store.CreateInvite(context.Background(), inviteInput(), "hash", time.Now().Add(time.Hour), encryptedInvitePayload, unlimited)
	if !errors.Is(err, domain.ErrAlreadyMember) {
		t.Fatalf("expected ErrAlreadyMember, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXInviteStore_CreateInviteRejectsPendingInviteInSameWorkspace(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectBegin()
	mock.ExpectExec(`SELECT pg_advisory_xact_lock`).
		WithArgs(inviteEmailLockKey).
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectQuery(`JOIN chat\.workspace_members`).
		WithArgs(inviteWorkspaceID, inviteEmail).
		WillReturnError(pgx.ErrNoRows)
	mock.ExpectQuery(`FROM auth\.user_invites`).
		WithArgs(inviteWorkspaceID, inviteEmail).
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(1))
	mock.ExpectRollback()

	store := storage.NewPGXInviteStore(mock)
	_, err = store.CreateInvite(context.Background(), inviteInput(), "hash", time.Now().Add(time.Hour), encryptedInvitePayload, unlimited)
	if !errors.Is(err, domain.ErrInviteAlreadyPending) {
		t.Fatalf("expected ErrInviteAlreadyPending, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// ── Rate limit ─────────────────────────────────────────────────────────────

func TestPGXInviteStore_CreateInviteCountsBudgetPerActorAndWorkspace(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	limit := domain.InviteRateLimit{MaxPerWindow: 5, WindowMinutes: 10}
	expiresAt := time.Now().Add(time.Hour)

	mock.ExpectBegin()
	mock.ExpectExec(`SELECT pg_advisory_xact_lock`).
		WithArgs(inviteEmailLockKey).
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectExec(`SELECT pg_advisory_xact_lock`).
		WithArgs(inviteBudgetLockKey).
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	// The count is keyed by actor AND workspace, over the configured window.
	mock.ExpectQuery(`SELECT count\(\*\)`).
		WithArgs(inviteActorID, inviteWorkspaceID, 10).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(4))
	mock.ExpectQuery(`JOIN chat\.workspace_members`).WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).WillReturnError(pgx.ErrNoRows)
	mock.ExpectQuery(`FROM auth\.user_invites`).WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).WillReturnError(pgx.ErrNoRows)
	mock.ExpectQuery(`INSERT INTO auth\.user_invites`).WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).WillReturnRows(inviteInsertRows(time.Now()))
	mock.ExpectExec(`INSERT INTO auth\.email_outbox`).WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit()
	mock.ExpectRollback()

	store := storage.NewPGXInviteStore(mock)
	if _, err := store.CreateInvite(context.Background(), inviteInput(), "hash", expiresAt, encryptedInvitePayload, limit); err != nil {
		t.Fatalf("CreateInvite below the limit must succeed: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// At the limit nothing is written: no invite row and no outbox entry, so no
// e-mail is ever queued for a rejected request.
func TestPGXInviteStore_CreateInviteOverBudgetWritesNothing(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	limit := domain.InviteRateLimit{MaxPerWindow: 5, WindowMinutes: 10}

	mock.ExpectBegin()
	mock.ExpectExec(`SELECT pg_advisory_xact_lock`).WithArgs(inviteEmailLockKey).WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectExec(`SELECT pg_advisory_xact_lock`).WithArgs(inviteBudgetLockKey).WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectQuery(`SELECT count\(\*\)`).
		WithArgs(inviteActorID, inviteWorkspaceID, 10).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(5))
	// No INSERT of any kind is scripted; ExpectationsWereMet plus pgxmock's
	// ordered matching fail the test if the store attempts one.
	mock.ExpectRollback()

	store := storage.NewPGXInviteStore(mock)
	_, err = store.CreateInvite(context.Background(), inviteInput(), "hash", time.Now().Add(time.Hour), encryptedInvitePayload, limit)
	if !errors.Is(err, domain.ErrInviteRateLimited) {
		t.Fatalf("expected ErrInviteRateLimited, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// The budget lock is keyed by (workspace, actor): a second workspace has its
// own budget, so an admin of A cannot exhaust the allowance of an admin of B.
func TestPGXInviteStore_CreateInviteBudgetKeyIsPerWorkspaceAndActor(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	input := inviteInput()
	input.WorkspaceID = inviteOtherWorkspaceID

	mock.ExpectBegin()
	mock.ExpectExec(`SELECT pg_advisory_xact_lock`).
		WithArgs(inviteOtherWorkspaceID + "\x00" + inviteEmail).
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectExec(`SELECT pg_advisory_xact_lock`).
		WithArgs("invite-budget\x00" + inviteOtherWorkspaceID + "\x00" + inviteActorID).
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectQuery(`SELECT count\(\*\)`).
		WithArgs(inviteActorID, inviteOtherWorkspaceID, 10).
		WillReturnError(errors.New("stop here"))
	mock.ExpectRollback()

	store := storage.NewPGXInviteStore(mock)
	_, err = store.CreateInvite(context.Background(), input, "hash", time.Now().Add(time.Hour), encryptedInvitePayload,
		domain.InviteRateLimit{MaxPerWindow: 5, WindowMinutes: 10})
	if err == nil || errors.Is(err, domain.ErrInviteRateLimited) {
		t.Fatalf("expected the scripted count failure, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// A zero budget means "unlimited" and must skip the counting entirely, so the
// tests above that omit those queries stay honest.
func TestPGXInviteStore_CreateInviteSkipsBudgetWhenDisabled(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectBegin()
	expectInviteCreateGuards(mock)
	mock.ExpectQuery(`INSERT INTO auth\.user_invites`).WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).WillReturnRows(inviteInsertRows(time.Now()))
	mock.ExpectExec(`INSERT INTO auth\.email_outbox`).WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit()
	mock.ExpectRollback()

	store := storage.NewPGXInviteStore(mock)
	if _, err := store.CreateInvite(context.Background(), inviteInput(), "hash", time.Now().Add(time.Hour), encryptedInvitePayload, unlimited); err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// The partial unique index is the backstop behind the pending check.
func TestPGXInviteStore_CreateInviteMapsUniqueViolationToPending(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectBegin()
	expectInviteCreateGuards(mock)
	mock.ExpectQuery(`INSERT INTO auth\.user_invites`).WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(uniqueViolation())
	mock.ExpectRollback()

	store := storage.NewPGXInviteStore(mock)
	_, err = store.CreateInvite(context.Background(), inviteInput(), "hash", time.Now().Add(time.Hour), encryptedInvitePayload, unlimited)
	if !errors.Is(err, domain.ErrInviteAlreadyPending) {
		t.Fatalf("expected ErrInviteAlreadyPending, got %v", err)
	}
}

// ── Acceptance ─────────────────────────────────────────────────────────────

func acceptInviteRows(workspaceID string) *pgxmock.Rows {
	return pgxmock.NewRows([]string{"id", "email", "workspace_id", "accepted", "revoked", "expires_at", "status"}).
		AddRow("invite-1", inviteEmail, workspaceID, false, false, time.Now().Add(time.Hour), "pending")
}

func newUserRows(createdAt time.Time) *pgxmock.Rows {
	return pgxmock.NewRows([]string{"id", "email", "display_name", "full_name", "created_at"}).
		AddRow("user-1", inviteEmail, "User", "User Full", createdAt)
}

// expectAcceptThroughMembership scripts a full acceptance for a brand new
// account, up to and including the membership writes.
func expectAcceptThroughMembership(mock pgxmock.PgxPoolIface, createdAt time.Time) {
	mock.ExpectQuery(`SELECT id, email::text, COALESCE\(workspace_id`).
		WithArgs("hashed-value").
		WillReturnRows(acceptInviteRows(inviteWorkspaceID))
	mock.ExpectExec(`SELECT pg_advisory_xact_lock`).
		WithArgs(inviteEmail).
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectQuery(`SELECT id, email::text, display_name`).
		WithArgs(inviteEmail).
		WillReturnError(pgx.ErrNoRows)
	mock.ExpectQuery(`INSERT INTO auth\.users`).
		WithArgs(inviteEmail, "User", "User Full").
		WillReturnRows(newUserRows(createdAt))
	mock.ExpectExec(`INSERT INTO auth\.user_password_credentials`).
		WithArgs("user-1", "argon2id-hash").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectExec(`INSERT INTO chat\.workspace_members`).
		WithArgs(inviteWorkspaceID, "user-1").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectExec(`INSERT INTO chat\.channel_members`).
		WithArgs(inviteWorkspaceID, "user-1").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
}

func TestPGXInviteStore_AcceptInviteTxCreatesUserCredentialMembershipAndMarksAccepted(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	createdAt := time.Now()
	mock.ExpectBegin()
	expectAcceptThroughMembership(mock, createdAt)
	mock.ExpectExec(`UPDATE auth\.user_invites`).
		WithArgs("invite-1", "user-1").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectCommit()
	mock.ExpectRollback()

	store := storage.NewPGXInviteStore(mock)
	result, err := store.AcceptInviteTx(context.Background(), "hashed-value", "User", "User Full", "argon2id-hash")
	if err != nil {
		t.Fatalf("AcceptInviteTx: %v", err)
	}
	if result.UserID != "user-1" || result.Email != inviteEmail || result.DisplayName != "User" || result.FullName != "User Full" {
		t.Fatalf("unexpected accept result: %+v", result)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// The membership goes to the workspace recorded on the invite. The accepting
// client sends only a token, so this is the sole source of that decision.
func TestPGXInviteStore_AcceptInviteTxCreatesMembershipInInviteWorkspace(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT id, email::text, COALESCE\(workspace_id`).
		WithArgs("hashed-value").
		WillReturnRows(acceptInviteRows(inviteOtherWorkspaceID))
	mock.ExpectExec(`SELECT pg_advisory_xact_lock`).WithArgs(inviteEmail).WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectQuery(`SELECT id, email::text, display_name`).WithArgs(inviteEmail).WillReturnError(pgx.ErrNoRows)
	mock.ExpectQuery(`INSERT INTO auth\.users`).WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).WillReturnRows(newUserRows(time.Now()))
	mock.ExpectExec(`INSERT INTO auth\.user_password_credentials`).WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).WillReturnResult(pgxmock.NewResult("INSERT", 1))
	// The other workspace's ID, not the one used everywhere else in this file.
	mock.ExpectExec(`INSERT INTO chat\.workspace_members`).
		WithArgs(inviteOtherWorkspaceID, "user-1").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectExec(`INSERT INTO chat\.channel_members`).
		WithArgs(inviteOtherWorkspaceID, "user-1").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectExec(`UPDATE auth\.user_invites`).WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectCommit()
	mock.ExpectRollback()

	store := storage.NewPGXInviteStore(mock)
	if _, err := store.AcceptInviteTx(context.Background(), "hashed-value", "User", "User Full", "argon2id-hash"); err != nil {
		t.Fatalf("AcceptInviteTx: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// An address that already has an account gets a membership, not a second
// account — and keeps its existing password. Honouring the submitted password
// here would make any invite to a known address a password reset.
func TestPGXInviteStore_AcceptInviteTxReusesExistingIdentityWithoutTouchingCredentials(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT id, email::text, COALESCE\(workspace_id`).
		WithArgs("hashed-value").
		WillReturnRows(acceptInviteRows(inviteWorkspaceID))
	mock.ExpectExec(`SELECT pg_advisory_xact_lock`).WithArgs(inviteEmail).WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectQuery(`SELECT id, email::text, display_name`).
		WithArgs(inviteEmail).
		WillReturnRows(pgxmock.NewRows([]string{"id", "email", "display_name", "full_name", "created_at"}).
			AddRow("existing-user", inviteEmail, "Existing Name", "Existing Full", time.Now()))
	// No INSERT INTO auth.users and no credential write are scripted.
	mock.ExpectExec(`INSERT INTO chat\.workspace_members`).
		WithArgs(inviteWorkspaceID, "existing-user").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectExec(`INSERT INTO chat\.channel_members`).
		WithArgs(inviteWorkspaceID, "existing-user").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectExec(`UPDATE auth\.user_invites`).
		WithArgs("invite-1", "existing-user").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectCommit()
	mock.ExpectRollback()

	store := storage.NewPGXInviteStore(mock)
	result, err := store.AcceptInviteTx(context.Background(), "hashed-value", "New Label", "New Full", "argon2id-hash")
	if err != nil {
		t.Fatalf("AcceptInviteTx: %v", err)
	}
	if result.UserID != "existing-user" {
		t.Fatalf("expected the existing identity to be reused, got %q", result.UserID)
	}
	// The inviter's labelling must not overwrite the name the person already has.
	if result.DisplayName != "Existing Name" {
		t.Fatalf("expected the existing display name to be kept, got %q", result.DisplayName)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// A pending invite with no workspace cannot produce a membership, so it is
// refused rather than honoured against some default.
func TestPGXInviteStore_AcceptInviteTxRejectsInviteWithoutWorkspace(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT id, email::text, COALESCE\(workspace_id`).
		WithArgs("hashed-value").
		WillReturnRows(acceptInviteRows(""))
	mock.ExpectRollback()

	store := storage.NewPGXInviteStore(mock)
	_, err = store.AcceptInviteTx(context.Background(), "hashed-value", "User", "User Full", "argon2id-hash")
	if !errors.Is(err, domain.ErrInvalidToken) {
		t.Fatalf("expected ErrInvalidToken, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXInviteStore_AcceptInviteTxRejectsUnknownExpiredAcceptedAndRevokedTokens(t *testing.T) {
	rows := func(accepted, revoked bool, expires time.Time, status string) *pgxmock.Rows {
		return pgxmock.NewRows([]string{"id", "email", "workspace_id", "accepted", "revoked", "expires_at", "status"}).
			AddRow("invite-1", inviteEmail, inviteWorkspaceID, accepted, revoked, expires, status)
	}
	tests := []struct {
		name string
		row  *pgxmock.Rows
		err  error
	}{
		{name: "unknown", err: pgx.ErrNoRows},
		{name: "expired", row: rows(false, false, time.Now().Add(-time.Minute), "pending")},
		{name: "accepted", row: rows(true, false, time.Now().Add(time.Hour), "pending")},
		{name: "revoked", row: rows(false, true, time.Now().Add(time.Hour), "pending")},
		{name: "non-pending", row: rows(false, false, time.Now().Add(time.Hour), "accepted")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock, err := pgxmock.NewPool()
			if err != nil {
				t.Fatalf("pgxmock: %v", err)
			}
			defer mock.Close()

			mock.ExpectBegin()
			expect := mock.ExpectQuery(`SELECT id, email::text, COALESCE\(workspace_id`).WithArgs("hashed-value")
			if tt.err != nil {
				expect.WillReturnError(tt.err)
			} else {
				expect.WillReturnRows(tt.row)
			}
			mock.ExpectRollback()

			store := storage.NewPGXInviteStore(mock)
			_, err = store.AcceptInviteTx(context.Background(), "hashed-value", "User", "User Full", "argon2id-hash")
			if !errors.Is(err, domain.ErrInvalidToken) {
				t.Fatalf("expected ErrInvalidToken, got %v", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet expectations: %v", err)
			}
		})
	}
}

// ── Policy ─────────────────────────────────────────────────────────────────

func TestPGXInviteStore_GetPolicySettings(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery(`SELECT min_password_length`).WillReturnRows(defaultPolicyRow())
	store := storage.NewPGXInviteStore(mock)
	policy, err := store.GetPolicySettings(context.Background())
	if err != nil {
		t.Fatalf("GetPolicySettings: %v", err)
	}
	if policy.InviteTokenTTLHours != 72 {
		t.Fatalf("unexpected policy: %+v", policy)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXInviteStore_GetPolicySettingsError(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery(`SELECT min_password_length`).WillReturnError(errors.New("policy failed"))
	store := storage.NewPGXInviteStore(mock)
	if _, err = store.GetPolicySettings(context.Background()); err == nil {
		t.Fatal("expected error")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// ── Guard query failures ───────────────────────────────────────────────────

func TestPGXInviteStore_CreateInviteGuardQueryErrors(t *testing.T) {
	tests := []struct {
		name  string
		setup func(pgxmock.PgxPoolIface)
	}{
		{name: "member lookup", setup: func(mock pgxmock.PgxPoolIface) {
			mock.ExpectBegin()
			expectInviteBudgetPass(mock)
			mock.ExpectQuery(`JOIN chat\.workspace_members`).WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).WillReturnError(errors.New("query failed"))
			mock.ExpectRollback()
		}},
		{name: "pending lookup", setup: func(mock pgxmock.PgxPoolIface) {
			mock.ExpectBegin()
			expectInviteBudgetPass(mock)
			mock.ExpectQuery(`JOIN chat\.workspace_members`).WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).WillReturnError(pgx.ErrNoRows)
			mock.ExpectQuery(`FROM auth\.user_invites`).WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).WillReturnError(errors.New("query failed"))
			mock.ExpectRollback()
		}},
		{name: "budget count", setup: func(mock pgxmock.PgxPoolIface) {
			mock.ExpectBegin()
			mock.ExpectExec(`SELECT pg_advisory_xact_lock`).WithArgs(inviteEmailLockKey).WillReturnResult(pgxmock.NewResult("SELECT", 1))
			mock.ExpectExec(`SELECT pg_advisory_xact_lock`).WithArgs(inviteBudgetLockKey).WillReturnResult(pgxmock.NewResult("SELECT", 1))
			mock.ExpectQuery(`SELECT count\(\*\)`).WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).WillReturnError(errors.New("count failed"))
			mock.ExpectRollback()
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock, err := pgxmock.NewPool()
			if err != nil {
				t.Fatalf("pgxmock: %v", err)
			}
			defer mock.Close()
			tt.setup(mock)
			store := storage.NewPGXInviteStore(mock)
			_, err = store.CreateInvite(context.Background(), inviteInput(), "hash", time.Now().Add(time.Hour), encryptedInvitePayload,
				domain.InviteRateLimit{MaxPerWindow: 5, WindowMinutes: 10})
			if err == nil {
				t.Fatal("expected error")
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet expectations: %v", err)
			}
		})
	}
}

func TestPGXInviteStore_CreateInviteErrors(t *testing.T) {
	expiresAt := time.Now().Add(time.Hour)
	tests := []struct {
		name  string
		setup func(pgxmock.PgxPoolIface)
	}{
		{name: "begin", setup: func(mock pgxmock.PgxPoolIface) { mock.ExpectBegin().WillReturnError(errors.New("begin failed")) }},
		{name: "lock", setup: func(mock pgxmock.PgxPoolIface) {
			mock.ExpectBegin()
			mock.ExpectExec(`SELECT pg_advisory_xact_lock`).WithArgs(inviteEmailLockKey).WillReturnError(errors.New("lock failed"))
			mock.ExpectRollback()
		}},
		{name: "insert", setup: func(mock pgxmock.PgxPoolIface) {
			mock.ExpectBegin()
			expectInviteCreateGuards(mock)
			mock.ExpectQuery(`INSERT INTO auth\.user_invites`).WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).WillReturnError(errors.New("insert failed"))
			mock.ExpectRollback()
		}},
		{name: "outbox", setup: func(mock pgxmock.PgxPoolIface) {
			mock.ExpectBegin()
			expectInviteCreateGuards(mock)
			mock.ExpectQuery(`INSERT INTO auth\.user_invites`).WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).WillReturnRows(inviteInsertRows(time.Now()))
			mock.ExpectExec(`INSERT INTO auth\.email_outbox`).WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).WillReturnError(errors.New("outbox failed"))
			mock.ExpectRollback()
		}},
		{name: "commit", setup: func(mock pgxmock.PgxPoolIface) {
			mock.ExpectBegin()
			expectInviteCreateGuards(mock)
			mock.ExpectQuery(`INSERT INTO auth\.user_invites`).WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).WillReturnRows(inviteInsertRows(time.Now()))
			mock.ExpectExec(`INSERT INTO auth\.email_outbox`).WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).WillReturnResult(pgxmock.NewResult("INSERT", 1))
			mock.ExpectCommit().WillReturnError(errors.New("commit failed"))
			mock.ExpectRollback()
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock, err := pgxmock.NewPool()
			if err != nil {
				t.Fatalf("pgxmock: %v", err)
			}
			defer mock.Close()
			tt.setup(mock)
			store := storage.NewPGXInviteStore(mock)
			_, err = store.CreateInvite(context.Background(), inviteInput(), "hashed-invite-value", expiresAt, encryptedInvitePayload, unlimited)
			if err == nil {
				t.Fatal("expected error")
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet expectations: %v", err)
			}
		})
	}
}

// Any failure after the invite row is read must roll the whole thing back: an
// accepted invite without a membership, or a membership without the invite
// being consumed, are both states the flow must never leave behind.
func TestPGXInviteStore_AcceptInviteTxErrors(t *testing.T) {
	tests := []struct {
		name  string
		setup func(pgxmock.PgxPoolIface)
	}{
		{name: "begin", setup: func(mock pgxmock.PgxPoolIface) { mock.ExpectBegin().WillReturnError(errors.New("begin failed")) }},
		{name: "query", setup: func(mock pgxmock.PgxPoolIface) {
			mock.ExpectBegin()
			mock.ExpectQuery(`SELECT id, email::text, COALESCE\(workspace_id`).WithArgs(pgxmock.AnyArg()).WillReturnError(errors.New("query failed"))
			mock.ExpectRollback()
		}},
		{name: "identity lookup", setup: func(mock pgxmock.PgxPoolIface) {
			mock.ExpectBegin()
			mock.ExpectQuery(`SELECT id, email::text, COALESCE\(workspace_id`).WithArgs(pgxmock.AnyArg()).WillReturnRows(acceptInviteRows(inviteWorkspaceID))
			mock.ExpectExec(`SELECT pg_advisory_xact_lock`).WithArgs(pgxmock.AnyArg()).WillReturnResult(pgxmock.NewResult("SELECT", 1))
			mock.ExpectQuery(`SELECT id, email::text, display_name`).WithArgs(pgxmock.AnyArg()).WillReturnError(errors.New("lookup failed"))
			mock.ExpectRollback()
		}},
		{name: "insert user", setup: func(mock pgxmock.PgxPoolIface) {
			mock.ExpectBegin()
			mock.ExpectQuery(`SELECT id, email::text, COALESCE\(workspace_id`).WithArgs(pgxmock.AnyArg()).WillReturnRows(acceptInviteRows(inviteWorkspaceID))
			mock.ExpectExec(`SELECT pg_advisory_xact_lock`).WithArgs(pgxmock.AnyArg()).WillReturnResult(pgxmock.NewResult("SELECT", 1))
			mock.ExpectQuery(`SELECT id, email::text, display_name`).WithArgs(pgxmock.AnyArg()).WillReturnError(pgx.ErrNoRows)
			mock.ExpectQuery(`INSERT INTO auth\.users`).WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).WillReturnError(errors.New("insert failed"))
			mock.ExpectRollback()
		}},
		{name: "credential", setup: func(mock pgxmock.PgxPoolIface) {
			mock.ExpectBegin()
			mock.ExpectQuery(`SELECT id, email::text, COALESCE\(workspace_id`).WithArgs(pgxmock.AnyArg()).WillReturnRows(acceptInviteRows(inviteWorkspaceID))
			mock.ExpectExec(`SELECT pg_advisory_xact_lock`).WithArgs(pgxmock.AnyArg()).WillReturnResult(pgxmock.NewResult("SELECT", 1))
			mock.ExpectQuery(`SELECT id, email::text, display_name`).WithArgs(pgxmock.AnyArg()).WillReturnError(pgx.ErrNoRows)
			mock.ExpectQuery(`INSERT INTO auth\.users`).WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).WillReturnRows(newUserRows(time.Now()))
			mock.ExpectExec(`INSERT INTO auth\.user_password_credentials`).WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).WillReturnError(errors.New("credential failed"))
			mock.ExpectRollback()
		}},
		{name: "workspace membership", setup: func(mock pgxmock.PgxPoolIface) {
			mock.ExpectBegin()
			mock.ExpectQuery(`SELECT id, email::text, COALESCE\(workspace_id`).WithArgs(pgxmock.AnyArg()).WillReturnRows(acceptInviteRows(inviteWorkspaceID))
			mock.ExpectExec(`SELECT pg_advisory_xact_lock`).WithArgs(pgxmock.AnyArg()).WillReturnResult(pgxmock.NewResult("SELECT", 1))
			mock.ExpectQuery(`SELECT id, email::text, display_name`).WithArgs(pgxmock.AnyArg()).WillReturnError(pgx.ErrNoRows)
			mock.ExpectQuery(`INSERT INTO auth\.users`).WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).WillReturnRows(newUserRows(time.Now()))
			mock.ExpectExec(`INSERT INTO auth\.user_password_credentials`).WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).WillReturnResult(pgxmock.NewResult("INSERT", 1))
			mock.ExpectExec(`INSERT INTO chat\.workspace_members`).WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).WillReturnError(errors.New("membership failed"))
			mock.ExpectRollback()
		}},
		{name: "general channel membership", setup: func(mock pgxmock.PgxPoolIface) {
			mock.ExpectBegin()
			mock.ExpectQuery(`SELECT id, email::text, COALESCE\(workspace_id`).WithArgs(pgxmock.AnyArg()).WillReturnRows(acceptInviteRows(inviteWorkspaceID))
			mock.ExpectExec(`SELECT pg_advisory_xact_lock`).WithArgs(pgxmock.AnyArg()).WillReturnResult(pgxmock.NewResult("SELECT", 1))
			mock.ExpectQuery(`SELECT id, email::text, display_name`).WithArgs(pgxmock.AnyArg()).WillReturnError(pgx.ErrNoRows)
			mock.ExpectQuery(`INSERT INTO auth\.users`).WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).WillReturnRows(newUserRows(time.Now()))
			mock.ExpectExec(`INSERT INTO auth\.user_password_credentials`).WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).WillReturnResult(pgxmock.NewResult("INSERT", 1))
			mock.ExpectExec(`INSERT INTO chat\.workspace_members`).WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).WillReturnResult(pgxmock.NewResult("INSERT", 1))
			mock.ExpectExec(`INSERT INTO chat\.channel_members`).WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).WillReturnError(errors.New("channel failed"))
			mock.ExpectRollback()
		}},
		{name: "mark accepted", setup: func(mock pgxmock.PgxPoolIface) {
			mock.ExpectBegin()
			expectAcceptThroughMembership(mock, time.Now())
			mock.ExpectExec(`UPDATE auth\.user_invites`).WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).WillReturnError(errors.New("update failed"))
			mock.ExpectRollback()
		}},
		// Zero rows updated means another transaction consumed the invite
		// first. Rolling back is what stops a concurrent double-accept from
		// producing two memberships off one token.
		{name: "mark accepted zero", setup: func(mock pgxmock.PgxPoolIface) {
			mock.ExpectBegin()
			expectAcceptThroughMembership(mock, time.Now())
			mock.ExpectExec(`UPDATE auth\.user_invites`).WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).WillReturnResult(pgxmock.NewResult("UPDATE", 0))
			mock.ExpectRollback()
		}},
		{name: "commit", setup: func(mock pgxmock.PgxPoolIface) {
			mock.ExpectBegin()
			expectAcceptThroughMembership(mock, time.Now())
			mock.ExpectExec(`UPDATE auth\.user_invites`).WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
			mock.ExpectCommit().WillReturnError(errors.New("commit failed"))
			mock.ExpectRollback()
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock, err := pgxmock.NewPool()
			if err != nil {
				t.Fatalf("pgxmock: %v", err)
			}
			defer mock.Close()
			tt.setup(mock)
			store := storage.NewPGXInviteStore(mock)
			_, err = store.AcceptInviteTx(context.Background(), "hashed-value", "User", "User Full", "argon2id-hash")
			if err == nil {
				t.Fatal("expected error")
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet expectations: %v", err)
			}
		})
	}
}

// ── Bootstrap issuer and lifecycle (issue #425) ────────────────────────────

func TestPGXInviteStore_WorkspaceHasAdmin(t *testing.T) {
	for _, tc := range []struct {
		name string
		rows *pgxmock.Rows
		err  error
		want bool
	}{
		{name: "has an admin", rows: pgxmock.NewRows([]string{"exists"}).AddRow(1), want: true},
		{name: "has none", err: pgx.ErrNoRows, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mock, err := pgxmock.NewPool()
			if err != nil {
				t.Fatalf("pgxmock: %v", err)
			}
			defer mock.Close()

			expect := mock.ExpectQuery(`FROM chat\.workspace_members wm`).WithArgs(inviteWorkspaceID)
			if tc.err != nil {
				expect.WillReturnError(tc.err)
			} else {
				expect.WillReturnRows(tc.rows)
			}

			store := storage.NewPGXInviteStore(mock)
			got, err := store.WorkspaceHasAdmin(context.Background(), inviteWorkspaceID)
			if err != nil {
				t.Fatalf("WorkspaceHasAdmin: %v", err)
			}
			if got != tc.want {
				t.Fatalf("expected %v, got %v", tc.want, got)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet expectations: %v", err)
			}
		})
	}
}

// A failure to determine the state must surface as an error, never as "no
// admin" — that would reopen the bootstrap window on a transient outage.
func TestPGXInviteStore_WorkspaceHasAdminQueryErrorIsReturned(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery(`FROM chat\.workspace_members wm`).
		WithArgs(inviteWorkspaceID).
		WillReturnError(errors.New("connection refused"))

	store := storage.NewPGXInviteStore(mock)
	if _, err := store.WorkspaceHasAdmin(context.Background(), inviteWorkspaceID); err == nil {
		t.Fatal("expected the query failure to propagate")
	}
}

// A bootstrap invite stores NULL in invited_by_user_id. That NULL is the audit
// record for "issued by the credential, no human actor".
func TestPGXInviteStore_CreateInviteStoresNullIssuerForBootstrap(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	input := inviteInput()
	input.ActorID = domain.BootstrapInviteIssuer
	expiresAt := time.Now().Add(time.Hour)

	mock.ExpectBegin()
	expectInviteCreateGuards(mock)
	// nil, not "": the column is a UUID and the empty string is not a UUID.
	mock.ExpectQuery(`INSERT INTO auth\.user_invites`).
		WithArgs(inviteWorkspaceID, nil, inviteEmail, "hash", expiresAt).
		WillReturnRows(inviteInsertRows(time.Now()))
	mock.ExpectExec(`INSERT INTO auth\.email_outbox`).WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit()
	mock.ExpectRollback()

	store := storage.NewPGXInviteStore(mock)
	if _, err := store.CreateInvite(context.Background(), input, "hash", expiresAt, encryptedInvitePayload, unlimited); err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// The budget must still bind for a NULL issuer. `invited_by_user_id = NULL` is
// unknown for every row, so a plain `=` would count zero and hand the bootstrap
// credential an unlimited allowance.
func TestPGXInviteStore_CreateInviteBudgetBindsForBootstrapIssuer(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	input := inviteInput()
	input.ActorID = domain.BootstrapInviteIssuer
	limit := domain.InviteRateLimit{MaxPerWindow: 2, WindowMinutes: 10}

	mock.ExpectBegin()
	mock.ExpectExec(`SELECT pg_advisory_xact_lock`).WithArgs(inviteEmailLockKey).
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectExec(`SELECT pg_advisory_xact_lock`).
		WithArgs("invite-budget\x00" + inviteWorkspaceID + "\x00").
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	// nil is passed for the issuer, and the count is at the limit.
	mock.ExpectQuery(`IS NOT DISTINCT FROM`).
		WithArgs(nil, inviteWorkspaceID, 10).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(2))
	mock.ExpectRollback()

	store := storage.NewPGXInviteStore(mock)
	_, err = store.CreateInvite(context.Background(), input, "hash", time.Now().Add(time.Hour), encryptedInvitePayload, limit)
	if !errors.Is(err, domain.ErrInviteRateLimited) {
		t.Fatalf("expected ErrInviteRateLimited for an exhausted bootstrap budget, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}
