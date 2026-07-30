package storage_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	pgxmock "github.com/pashagolub/pgxmock/v2"

	"github.com/nicrepository/nchat/services/auth-service/internal/domain"
	"github.com/nicrepository/nchat/services/auth-service/internal/storage"
)

const encryptedInvitePayload = `{"alg":"AES-256-GCM","key_version":"v1","nonce":"bm9uY2U=","ciphertext":"Y2lwaGVydGV4dA=="}`

// Invites are workspace-scoped. Every guard below is keyed by the
// workspace so two tenants can invite the same address independently.
const (
	inviteWorkspaceID      = "9a8b7c6d-5e4f-4a3b-8c2d-1e0f9a8b7c6d"
	inviteOtherWorkspaceID = "11111111-2222-4333-8444-555555555555"
	inviteActorID          = "3f1c2d4e-5a6b-4c8d-9e0f-1a2b3c4d5e6f"
	inviteEmail            = "user@example.com"

	// The lock keys the store builds. Reproduced literally so a change to the
	// key shape shows up as a test failure rather than as two workspaces
	// silently serialising on each other.
	inviteEmailLockKey  = inviteWorkspaceID + "\x1f" + inviteEmail
	inviteBudgetLockKey = "invite-budget\x1f" + inviteWorkspaceID + "\x1f" + inviteActorID
)

// unlimited disables the budget, which skips the counting queries entirely.
var unlimited = domain.InviteRateLimit{}

// inviteNow is the canonical instant a creating transaction is judged against:
// the store uses it to retire lapsed invites and to decide whether one is still
// active. Fixed here so the scripted expectations can match on it.
const lockSeparatorForTest = "\x1f"

var inviteNow = time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)

// expectExpireStaleInvites scripts the retirement that now runs under the
// (workspace, email) lock, before the duplicate checks.
func expectExpireStaleInvites(mock pgxmock.PgxPoolIface) {
	mock.ExpectExec(`UPDATE auth\.user_invites`).
		WithArgs(inviteWorkspaceID, inviteEmail, inviteNow).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))
}

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
	expectExpireStaleInvites(mock)
	mock.ExpectQuery(`JOIN chat\.workspace_members`).
		WithArgs(inviteWorkspaceID, inviteEmail).
		WillReturnError(pgx.ErrNoRows)
	mock.ExpectQuery(`FROM auth\.user_invites`).
		WithArgs(inviteWorkspaceID, inviteEmail, inviteNow).
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
	expectExpireStaleInvites(mock)
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
		WithArgs(inviteWorkspaceID, inviteActorID, inviteEmail, "hashed-invite-value", expiresAt, string(domain.InviteKindMember)).
		WillReturnRows(inviteInsertRows(createdAt))
	mock.ExpectExec(`INSERT INTO auth\.email_outbox`).
		WithArgs(inviteEmail, "invite-1", encryptedInvitePayload).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit()
	mock.ExpectRollback()

	store := storage.NewPGXInviteStore(mock)
	result, err := store.CreateInvite(context.Background(), inviteInput(), "hashed-invite-value", inviteNow, expiresAt, encryptedInvitePayload, unlimited)
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
	expectExpireStaleInvites(mock)
	mock.ExpectQuery(`JOIN chat\.workspace_members`).
		WithArgs(inviteWorkspaceID, inviteEmail).
		WillReturnError(pgx.ErrNoRows)
	mock.ExpectQuery(`FROM auth\.user_invites`).
		WithArgs(inviteWorkspaceID, inviteEmail, inviteNow).
		WillReturnError(pgx.ErrNoRows)
	mock.ExpectQuery(`INSERT INTO auth\.user_invites`).
		WithArgs(inviteWorkspaceID, inviteActorID, inviteEmail, "hash", expiresAt, string(domain.InviteKindMember)).
		WillReturnRows(inviteInsertRows(time.Now()))
	mock.ExpectExec(`INSERT INTO auth\.email_outbox`).WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit()
	mock.ExpectRollback()

	store := storage.NewPGXInviteStore(mock)
	if _, err := store.CreateInvite(context.Background(), inviteInput(), "hash", inviteNow, expiresAt, encryptedInvitePayload, unlimited); err != nil {
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
		WithArgs(inviteOtherWorkspaceID + "\x1f" + inviteEmail).
		WillReturnError(errors.New("stop here"))
	mock.ExpectRollback()

	store := storage.NewPGXInviteStore(mock)
	if _, err := store.CreateInvite(context.Background(), input, "hash", inviteNow, expiresAt, encryptedInvitePayload, unlimited); err == nil {
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
	expectExpireStaleInvites(mock)
	mock.ExpectQuery(`JOIN chat\.workspace_members`).
		WithArgs(inviteWorkspaceID, inviteEmail).
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(1))
	mock.ExpectRollback()

	store := storage.NewPGXInviteStore(mock)
	_, err = store.CreateInvite(context.Background(), inviteInput(), "hash", inviteNow, time.Now().Add(time.Hour), encryptedInvitePayload, unlimited)
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
	expectExpireStaleInvites(mock)
	mock.ExpectQuery(`JOIN chat\.workspace_members`).
		WithArgs(inviteWorkspaceID, inviteEmail).
		WillReturnError(pgx.ErrNoRows)
	mock.ExpectQuery(`FROM auth\.user_invites`).
		WithArgs(inviteWorkspaceID, inviteEmail, inviteNow).
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(1))
	mock.ExpectRollback()

	store := storage.NewPGXInviteStore(mock)
	_, err = store.CreateInvite(context.Background(), inviteInput(), "hash", inviteNow, time.Now().Add(time.Hour), encryptedInvitePayload, unlimited)
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
	expectExpireStaleInvites(mock)
	mock.ExpectQuery(`JOIN chat\.workspace_members`).WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).WillReturnError(pgx.ErrNoRows)
	mock.ExpectQuery(`FROM auth\.user_invites`).WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).WillReturnError(pgx.ErrNoRows)
	mock.ExpectQuery(`INSERT INTO auth\.user_invites`).WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).WillReturnRows(inviteInsertRows(time.Now()))
	mock.ExpectExec(`INSERT INTO auth\.email_outbox`).WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit()
	mock.ExpectRollback()

	store := storage.NewPGXInviteStore(mock)
	if _, err := store.CreateInvite(context.Background(), inviteInput(), "hash", inviteNow, expiresAt, encryptedInvitePayload, limit); err != nil {
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
	_, err = store.CreateInvite(context.Background(), inviteInput(), "hash", inviteNow, time.Now().Add(time.Hour), encryptedInvitePayload, limit)
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
		WithArgs(inviteOtherWorkspaceID + "\x1f" + inviteEmail).
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectExec(`SELECT pg_advisory_xact_lock`).
		WithArgs("invite-budget\x1f" + inviteOtherWorkspaceID + "\x1f" + inviteActorID).
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectQuery(`SELECT count\(\*\)`).
		WithArgs(inviteActorID, inviteOtherWorkspaceID, 10).
		WillReturnError(errors.New("stop here"))
	mock.ExpectRollback()

	store := storage.NewPGXInviteStore(mock)
	_, err = store.CreateInvite(context.Background(), input, "hash", inviteNow, time.Now().Add(time.Hour), encryptedInvitePayload,
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
	mock.ExpectQuery(`INSERT INTO auth\.user_invites`).WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).WillReturnRows(inviteInsertRows(time.Now()))
	mock.ExpectExec(`INSERT INTO auth\.email_outbox`).WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit()
	mock.ExpectRollback()

	store := storage.NewPGXInviteStore(mock)
	if _, err := store.CreateInvite(context.Background(), inviteInput(), "hash", inviteNow, time.Now().Add(time.Hour), encryptedInvitePayload, unlimited); err != nil {
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
	mock.ExpectQuery(`INSERT INTO auth\.user_invites`).WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(uniqueViolation())
	mock.ExpectRollback()

	store := storage.NewPGXInviteStore(mock)
	_, err = store.CreateInvite(context.Background(), inviteInput(), "hash", inviteNow, time.Now().Add(time.Hour), encryptedInvitePayload, unlimited)
	if !errors.Is(err, domain.ErrInviteAlreadyPending) {
		t.Fatalf("expected ErrInviteAlreadyPending, got %v", err)
	}
}

// ── Acceptance ─────────────────────────────────────────────────────────────

func acceptInviteRows(workspaceID string) *pgxmock.Rows {
	return acceptInviteRowsOfKind(workspaceID, domain.InviteKindMember)
}

func acceptInviteRowsOfKind(workspaceID string, kind domain.InviteKind) *pgxmock.Rows {
	return pgxmock.NewRows([]string{"id", "email", "workspace_id", "invite_kind", "accepted", "revoked", "expires_at", "status"}).
		AddRow("invite-1", inviteEmail, workspaceID, string(kind), false, false, time.Now().Add(time.Hour), "pending")
}

// expectInviteLookup scripts both reads AcceptInviteTx makes of the invite: the
// unlocked peek that only decides which workspace lock to take, and the
// authoritative FOR UPDATE re-read that everything downstream uses.
func expectInviteLookup(mock pgxmock.PgxPoolIface, arg any, rows func() *pgxmock.Rows) {
	for i := 0; i < 2; i++ {
		mock.ExpectQuery(`SELECT id, email::text, COALESCE\(workspace_id`).
			WithArgs(arg).
			WillReturnRows(rows())
	}
}

func newUserRows(createdAt time.Time) *pgxmock.Rows {
	return pgxmock.NewRows([]string{"id", "email", "display_name", "full_name", "created_at"}).
		AddRow("user-1", inviteEmail, "User", "User Full", createdAt)
}

// expectAcceptThroughMembership scripts a full acceptance for a brand new
// account, up to and including the membership writes.
func expectAcceptThroughMembership(mock pgxmock.PgxPoolIface, createdAt time.Time) {
	expectInviteLookup(mock, "hashed-value", func() *pgxmock.Rows { return acceptInviteRows(inviteWorkspaceID) })
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
		WithArgs(inviteWorkspaceID, "user-1", "member").
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
	expectInviteLookup(mock, "hashed-value", func() *pgxmock.Rows { return acceptInviteRows(inviteOtherWorkspaceID) })
	mock.ExpectExec(`SELECT pg_advisory_xact_lock`).WithArgs(inviteEmail).WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectQuery(`SELECT id, email::text, display_name`).WithArgs(inviteEmail).WillReturnError(pgx.ErrNoRows)
	mock.ExpectQuery(`INSERT INTO auth\.users`).WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).WillReturnRows(newUserRows(time.Now()))
	mock.ExpectExec(`INSERT INTO auth\.user_password_credentials`).WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).WillReturnResult(pgxmock.NewResult("INSERT", 1))
	// The other workspace's ID, not the one used everywhere else in this file.
	mock.ExpectExec(`INSERT INTO chat\.workspace_members`).
		WithArgs(inviteOtherWorkspaceID, "user-1", "member").
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
	expectInviteLookup(mock, "hashed-value", func() *pgxmock.Rows { return acceptInviteRows(inviteWorkspaceID) })
	mock.ExpectExec(`SELECT pg_advisory_xact_lock`).WithArgs(inviteEmail).WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectQuery(`SELECT id, email::text, display_name`).
		WithArgs(inviteEmail).
		WillReturnRows(pgxmock.NewRows([]string{"id", "email", "display_name", "full_name", "created_at"}).
			AddRow("existing-user", inviteEmail, "Existing Name", "Existing Full", time.Now()))
	// No INSERT INTO auth.users and no credential write are scripted.
	mock.ExpectExec(`INSERT INTO chat\.workspace_members`).
		WithArgs(inviteWorkspaceID, "existing-user", "member").
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

// A legacy invite predating the workspace binding names no workspace, so there
// is no membership it could produce. Migration auth/000008 leaves such rows
// untouched rather than revoking them, which makes refusing them this layer's
// job.
func TestPGXInviteStore_AcceptInviteTxRejectsLegacyUnscopedInvite(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectBegin()
	expectInviteLookup(mock, "hashed-value", func() *pgxmock.Rows { return acceptInviteRows("") })
	mock.ExpectRollback()

	store := storage.NewPGXInviteStore(mock)
	_, err = store.AcceptInviteTx(context.Background(), "hashed-value", "User", "User Full", "argon2id-hash")
	if !errors.Is(err, domain.ErrInviteWorkspaceMissing) {
		t.Fatalf("expected ErrInviteWorkspaceMissing, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXInviteStore_AcceptInviteTxRejectsUnknownExpiredAcceptedAndRevokedTokens(t *testing.T) {
	rows := func(accepted, revoked bool, expires time.Time, status string) *pgxmock.Rows {
		return pgxmock.NewRows([]string{"id", "email", "workspace_id", "invite_kind", "accepted", "revoked", "expires_at", "status"}).
			AddRow("invite-1", inviteEmail, inviteWorkspaceID, string(domain.InviteKindMember), accepted, revoked, expires, status)
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
			mock.ExpectQuery(`FROM auth\.user_invites`).WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).WillReturnError(errors.New("query failed"))
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
			_, err = store.CreateInvite(context.Background(), inviteInput(), "hash", inviteNow, time.Now().Add(time.Hour), encryptedInvitePayload,
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
			mock.ExpectQuery(`INSERT INTO auth\.user_invites`).WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).WillReturnError(errors.New("insert failed"))
			mock.ExpectRollback()
		}},
		{name: "outbox", setup: func(mock pgxmock.PgxPoolIface) {
			mock.ExpectBegin()
			expectInviteCreateGuards(mock)
			mock.ExpectQuery(`INSERT INTO auth\.user_invites`).WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).WillReturnRows(inviteInsertRows(time.Now()))
			mock.ExpectExec(`INSERT INTO auth\.email_outbox`).WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).WillReturnError(errors.New("outbox failed"))
			mock.ExpectRollback()
		}},
		{name: "commit", setup: func(mock pgxmock.PgxPoolIface) {
			mock.ExpectBegin()
			expectInviteCreateGuards(mock)
			mock.ExpectQuery(`INSERT INTO auth\.user_invites`).WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).WillReturnRows(inviteInsertRows(time.Now()))
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
			_, err = store.CreateInvite(context.Background(), inviteInput(), "hashed-invite-value", inviteNow, expiresAt, encryptedInvitePayload, unlimited)
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
			expectInviteLookup(mock, pgxmock.AnyArg(), func() *pgxmock.Rows { return acceptInviteRows(inviteWorkspaceID) })
			mock.ExpectExec(`SELECT pg_advisory_xact_lock`).WithArgs(pgxmock.AnyArg()).WillReturnResult(pgxmock.NewResult("SELECT", 1))
			mock.ExpectQuery(`SELECT id, email::text, display_name`).WithArgs(pgxmock.AnyArg()).WillReturnError(errors.New("lookup failed"))
			mock.ExpectRollback()
		}},
		{name: "insert user", setup: func(mock pgxmock.PgxPoolIface) {
			mock.ExpectBegin()
			expectInviteLookup(mock, pgxmock.AnyArg(), func() *pgxmock.Rows { return acceptInviteRows(inviteWorkspaceID) })
			mock.ExpectExec(`SELECT pg_advisory_xact_lock`).WithArgs(pgxmock.AnyArg()).WillReturnResult(pgxmock.NewResult("SELECT", 1))
			mock.ExpectQuery(`SELECT id, email::text, display_name`).WithArgs(pgxmock.AnyArg()).WillReturnError(pgx.ErrNoRows)
			mock.ExpectQuery(`INSERT INTO auth\.users`).WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).WillReturnError(errors.New("insert failed"))
			mock.ExpectRollback()
		}},
		{name: "credential", setup: func(mock pgxmock.PgxPoolIface) {
			mock.ExpectBegin()
			expectInviteLookup(mock, pgxmock.AnyArg(), func() *pgxmock.Rows { return acceptInviteRows(inviteWorkspaceID) })
			mock.ExpectExec(`SELECT pg_advisory_xact_lock`).WithArgs(pgxmock.AnyArg()).WillReturnResult(pgxmock.NewResult("SELECT", 1))
			mock.ExpectQuery(`SELECT id, email::text, display_name`).WithArgs(pgxmock.AnyArg()).WillReturnError(pgx.ErrNoRows)
			mock.ExpectQuery(`INSERT INTO auth\.users`).WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).WillReturnRows(newUserRows(time.Now()))
			mock.ExpectExec(`INSERT INTO auth\.user_password_credentials`).WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).WillReturnError(errors.New("credential failed"))
			mock.ExpectRollback()
		}},
		{name: "workspace membership", setup: func(mock pgxmock.PgxPoolIface) {
			mock.ExpectBegin()
			expectInviteLookup(mock, pgxmock.AnyArg(), func() *pgxmock.Rows { return acceptInviteRows(inviteWorkspaceID) })
			mock.ExpectExec(`SELECT pg_advisory_xact_lock`).WithArgs(pgxmock.AnyArg()).WillReturnResult(pgxmock.NewResult("SELECT", 1))
			mock.ExpectQuery(`SELECT id, email::text, display_name`).WithArgs(pgxmock.AnyArg()).WillReturnError(pgx.ErrNoRows)
			mock.ExpectQuery(`INSERT INTO auth\.users`).WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).WillReturnRows(newUserRows(time.Now()))
			mock.ExpectExec(`INSERT INTO auth\.user_password_credentials`).WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).WillReturnResult(pgxmock.NewResult("INSERT", 1))
			mock.ExpectExec(`INSERT INTO chat\.workspace_members`).WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).WillReturnError(errors.New("membership failed"))
			mock.ExpectRollback()
		}},
		{name: "general channel membership", setup: func(mock pgxmock.PgxPoolIface) {
			mock.ExpectBegin()
			expectInviteLookup(mock, pgxmock.AnyArg(), func() *pgxmock.Rows { return acceptInviteRows(inviteWorkspaceID) })
			mock.ExpectExec(`SELECT pg_advisory_xact_lock`).WithArgs(pgxmock.AnyArg()).WillReturnResult(pgxmock.NewResult("SELECT", 1))
			mock.ExpectQuery(`SELECT id, email::text, display_name`).WithArgs(pgxmock.AnyArg()).WillReturnError(pgx.ErrNoRows)
			mock.ExpectQuery(`INSERT INTO auth\.users`).WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).WillReturnRows(newUserRows(time.Now()))
			mock.ExpectExec(`INSERT INTO auth\.user_password_credentials`).WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).WillReturnResult(pgxmock.NewResult("INSERT", 1))
			mock.ExpectExec(`INSERT INTO chat\.workspace_members`).WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).WillReturnResult(pgxmock.NewResult("INSERT", 1))
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

// ── Bootstrap issuer and lifecycle ────────────────────────────

func TestPGXInviteStore_BootstrapWorkspaceState(t *testing.T) {
	for _, tc := range []struct {
		name     string
		rows     *pgxmock.Rows
		err      error
		want     domain.BootstrapWorkspaceState
		wantOpen bool
	}{
		{
			name:     "active and uninitialized",
			rows:     pgxmock.NewRows([]string{"active", "initialized"}).AddRow(true, false),
			want:     domain.BootstrapWorkspaceState{Exists: true, Active: true},
			wantOpen: true,
		},
		{
			name: "active with an administrator",
			rows: pgxmock.NewRows([]string{"active", "initialized"}).AddRow(true, true),
			want: domain.BootstrapWorkspaceState{Exists: true, Active: true, Initialized: true},
		},
		// The finding: an archived workspace that already has an owner must
		// still report as initialized, or the window reopens.
		{
			name: "archived with an administrator",
			rows: pgxmock.NewRows([]string{"active", "initialized"}).AddRow(false, true),
			want: domain.BootstrapWorkspaceState{Exists: true, Initialized: true},
		},
		{
			name: "archived and uninitialized",
			rows: pgxmock.NewRows([]string{"active", "initialized"}).AddRow(false, false),
			want: domain.BootstrapWorkspaceState{Exists: true},
		},
		{name: "missing workspace", err: pgx.ErrNoRows},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mock, err := pgxmock.NewPool()
			if err != nil {
				t.Fatalf("pgxmock: %v", err)
			}
			defer mock.Close()

			expect := mock.ExpectQuery(`FROM chat\.workspaces w`).WithArgs(inviteWorkspaceID)
			if tc.err != nil {
				expect.WillReturnError(tc.err)
			} else {
				expect.WillReturnRows(tc.rows)
			}

			store := storage.NewPGXInviteStore(mock)
			got, err := store.BootstrapWorkspaceState(context.Background(), inviteWorkspaceID)
			if err != nil {
				t.Fatalf("BootstrapWorkspaceState: %v", err)
			}
			if got != tc.want {
				t.Fatalf("expected %+v, got %+v", tc.want, got)
			}
			if got.BootstrapOpen() != tc.wantOpen {
				t.Fatalf("expected BootstrapOpen=%v for %+v", tc.wantOpen, got)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet expectations: %v", err)
			}
		})
	}
}

// The membership probe must not be filtered by the workspace's own status.
// Joining on an active workspace is what made an archived one look
// uninitialized and reopened a window that had already closed.
func TestPGXInviteStore_BootstrapStateCountsAdminsRegardlessOfWorkspaceStatus(t *testing.T) {
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

	mock.ExpectQuery(``).WithArgs(inviteWorkspaceID).
		WillReturnRows(pgxmock.NewRows([]string{"active", "initialized"}).AddRow(false, true))

	store := storage.NewPGXInviteStore(mock)
	if _, err := store.BootstrapWorkspaceState(context.Background(), inviteWorkspaceID); err != nil {
		t.Fatalf("BootstrapWorkspaceState: %v", err)
	}
	if len(issued) != 1 {
		t.Fatalf("expected one query, got %d", len(issued))
	}
	// The membership subquery keys on the workspace id alone. A status
	// predicate applied to the members join is the regression.
	_, memberClause, found := strings.Cut(issued[0], "FROM chat.workspace_members")
	if !found {
		t.Fatalf("expected a membership subquery in:\n%s", issued[0])
	}
	memberClause, _, found = strings.Cut(memberClause, ")")
	if !found {
		t.Fatalf("expected the membership subquery to be closed in:\n%s", issued[0])
	}
	if strings.Contains(memberClause, "w.status") {
		t.Fatalf("the membership probe must not depend on the workspace status:\n%s", memberClause)
	}
}

// A failure to determine the state must surface as an error, never as an open
// window — that would reopen the bootstrap on a transient outage.
func TestPGXInviteStore_BootstrapWorkspaceStateQueryErrorIsReturned(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery(`FROM chat\.workspaces w`).
		WithArgs(inviteWorkspaceID).
		WillReturnError(errors.New("connection refused"))

	store := storage.NewPGXInviteStore(mock)
	if _, err := store.BootstrapWorkspaceState(context.Background(), inviteWorkspaceID); err == nil {
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
	input.Kind = domain.InviteKindBootstrapOwner
	expiresAt := time.Now().Add(time.Hour)

	mock.ExpectBegin()
	expectInviteCreateGuards(mock)
	// nil, not "": the column is a UUID and the empty string is not a UUID.
	mock.ExpectQuery(`INSERT INTO auth\.user_invites`).
		WithArgs(inviteWorkspaceID, nil, inviteEmail, "hash", expiresAt, string(domain.InviteKindBootstrapOwner)).
		WillReturnRows(inviteInsertRows(time.Now()))
	mock.ExpectExec(`INSERT INTO auth\.email_outbox`).WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit()
	mock.ExpectRollback()

	store := storage.NewPGXInviteStore(mock)
	if _, err := store.CreateInvite(context.Background(), input, "hash", inviteNow, expiresAt, encryptedInvitePayload, unlimited); err != nil {
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
		WithArgs("invite-budget\x1f" + inviteWorkspaceID + "\x1f").
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	// nil is passed for the issuer, and the count is at the limit.
	mock.ExpectQuery(`IS NOT DISTINCT FROM`).
		WithArgs(nil, inviteWorkspaceID, 10).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(2))
	mock.ExpectRollback()

	store := storage.NewPGXInviteStore(mock)
	_, err = store.CreateInvite(context.Background(), input, "hash", inviteNow, time.Now().Add(time.Hour), encryptedInvitePayload, limit)
	if !errors.Is(err, domain.ErrInviteRateLimited) {
		t.Fatalf("expected ErrInviteRateLimited for an exhausted bootstrap budget, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// ── Bootstrap acceptance ───────────────────────────────────────────────────
//
// A bootstrap invite is the only one that creates an owner, and accepting it is
// what closes the bootstrap window. The scripts below pin the statement order
// that makes that safe: the workspace lock is taken before the invite row is
// locked (so two bootstrap acceptances queue instead of deadlocking), the
// "already has an administrator" check runs inside the lock, and the surviving
// pending bootstrap invites are revoked in the same transaction.

const bootstrapWorkspaceLockKey = "workspace-bootstrap\x1f" + inviteWorkspaceID

func bootstrapInviteRows() *pgxmock.Rows {
	return acceptInviteRowsOfKind(inviteWorkspaceID, domain.InviteKindBootstrapOwner)
}

func TestPGXInviteStore_AcceptInviteTxBootstrapCreatesOwnerAndRevokesSiblings(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectBegin()
	// The unlocked peek comes first and exists only to learn the workspace.
	mock.ExpectQuery(`SELECT id, email::text, COALESCE\(workspace_id`).
		WithArgs("hashed-value").WillReturnRows(bootstrapInviteRows())
	// Then the workspace lock, *before* the row is locked.
	mock.ExpectExec(`SELECT pg_advisory_xact_lock`).
		WithArgs(bootstrapWorkspaceLockKey).WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectQuery(`SELECT id, email::text, COALESCE\(workspace_id`).
		WithArgs("hashed-value").WillReturnRows(bootstrapInviteRows())
	// The lifecycle check, inside the lock: no administrator yet.
	mock.ExpectQuery(`FROM chat\.workspaces w`).
		WithArgs(inviteWorkspaceID).
		WillReturnRows(pgxmock.NewRows([]string{"active", "initialized"}).AddRow(true, false))
	mock.ExpectExec(`SELECT pg_advisory_xact_lock`).
		WithArgs(inviteEmail).WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectQuery(`SELECT id, email::text, display_name`).
		WithArgs(inviteEmail).WillReturnError(pgx.ErrNoRows)
	mock.ExpectQuery(`INSERT INTO auth\.users`).
		WithArgs(inviteEmail, "User", "User Full").WillReturnRows(newUserRows(time.Now()))
	mock.ExpectExec(`INSERT INTO auth\.user_password_credentials`).
		WithArgs("user-1", "argon2id-hash").WillReturnResult(pgxmock.NewResult("INSERT", 1))
	// 'owner', not 'member' — this is the whole point of the bootstrap kind.
	mock.ExpectExec(`INSERT INTO chat\.workspace_members`).
		WithArgs(inviteWorkspaceID, "user-1", "owner").WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectExec(`INSERT INTO chat\.channel_members`).
		WithArgs(inviteWorkspaceID, "user-1").WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectExec(`UPDATE auth\.user_invites`).
		WithArgs("invite-1", "user-1").WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	// Same transaction: the siblings become unusable with the owner's creation.
	mock.ExpectExec(`UPDATE auth\.user_invites`).
		WithArgs(inviteWorkspaceID, "invite-1").WillReturnResult(pgxmock.NewResult("UPDATE", 1))
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

// Once the workspace has an administrator the bootstrap invite is refused, and
// the refusal writes nothing: no identity, no membership, and the invite is not
// marked accepted. This is the branch that stops a race producing a second
// owner.
func TestPGXInviteStore_AcceptInviteTxBootstrapRefusedOnceInitialized(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT id, email::text, COALESCE\(workspace_id`).
		WithArgs("hashed-value").WillReturnRows(bootstrapInviteRows())
	mock.ExpectExec(`SELECT pg_advisory_xact_lock`).
		WithArgs(bootstrapWorkspaceLockKey).WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectQuery(`SELECT id, email::text, COALESCE\(workspace_id`).
		WithArgs("hashed-value").WillReturnRows(bootstrapInviteRows())
	mock.ExpectQuery(`FROM chat\.workspaces w`).
		WithArgs(inviteWorkspaceID).
		WillReturnRows(pgxmock.NewRows([]string{"active", "initialized"}).AddRow(true, true))
	// No identity, membership or acceptance is scripted: reaching any of them
	// would fail this test.
	mock.ExpectRollback()

	store := storage.NewPGXInviteStore(mock)
	_, err = store.AcceptInviteTx(context.Background(), "hashed-value", "User", "User Full", "argon2id-hash")
	if !errors.Is(err, domain.ErrInvalidToken) {
		t.Fatalf("expected ErrInvalidToken once the window is closed, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// An ordinary invite takes no workspace lock and runs no lifecycle check: the
// bootstrap machinery must not touch the path every normal onboarding uses.
func TestPGXInviteStore_AcceptInviteTxMemberInviteTakesNoWorkspaceLock(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectBegin()
	expectAcceptThroughMembership(mock, time.Now())
	mock.ExpectExec(`UPDATE auth\.user_invites`).
		WithArgs("invite-1", "user-1").WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectCommit()
	mock.ExpectRollback()

	store := storage.NewPGXInviteStore(mock)
	if _, err := store.AcceptInviteTx(context.Background(), "hashed-value", "User", "User Full", "argon2id-hash"); err != nil {
		t.Fatalf("AcceptInviteTx: %v", err)
	}
	// expectAcceptThroughMembership scripts neither the workspace lock nor the
	// admin probe nor the sibling revoke; an unmet or surplus statement fails.
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPGXInviteStore_AcceptInviteTxBootstrapErrors(t *testing.T) {
	tests := []struct {
		name  string
		setup func(mock pgxmock.PgxPoolIface)
	}{
		{name: "workspace lock", setup: func(mock pgxmock.PgxPoolIface) {
			mock.ExpectBegin()
			mock.ExpectQuery(`SELECT id, email::text, COALESCE\(workspace_id`).
				WithArgs(pgxmock.AnyArg()).WillReturnRows(bootstrapInviteRows())
			mock.ExpectExec(`SELECT pg_advisory_xact_lock`).
				WithArgs(bootstrapWorkspaceLockKey).WillReturnError(errors.New("lock failed"))
			mock.ExpectRollback()
		}},
		{name: "lifecycle probe", setup: func(mock pgxmock.PgxPoolIface) {
			mock.ExpectBegin()
			mock.ExpectQuery(`SELECT id, email::text, COALESCE\(workspace_id`).
				WithArgs(pgxmock.AnyArg()).WillReturnRows(bootstrapInviteRows())
			mock.ExpectExec(`SELECT pg_advisory_xact_lock`).
				WithArgs(bootstrapWorkspaceLockKey).WillReturnResult(pgxmock.NewResult("SELECT", 1))
			mock.ExpectQuery(`SELECT id, email::text, COALESCE\(workspace_id`).
				WithArgs(pgxmock.AnyArg()).WillReturnRows(bootstrapInviteRows())
			mock.ExpectQuery(`FROM chat\.workspaces w`).
				WithArgs(inviteWorkspaceID).WillReturnError(errors.New("probe failed"))
			mock.ExpectRollback()
		}},
		// A failure to revoke the siblings must roll back the owner too:
		// committing here would leave a workspace initialized while another
		// bootstrap invite stayed pending.
		{name: "sibling revoke", setup: func(mock pgxmock.PgxPoolIface) {
			mock.ExpectBegin()
			mock.ExpectQuery(`SELECT id, email::text, COALESCE\(workspace_id`).
				WithArgs(pgxmock.AnyArg()).WillReturnRows(bootstrapInviteRows())
			mock.ExpectExec(`SELECT pg_advisory_xact_lock`).
				WithArgs(bootstrapWorkspaceLockKey).WillReturnResult(pgxmock.NewResult("SELECT", 1))
			mock.ExpectQuery(`SELECT id, email::text, COALESCE\(workspace_id`).
				WithArgs(pgxmock.AnyArg()).WillReturnRows(bootstrapInviteRows())
			mock.ExpectQuery(`FROM chat\.workspaces w`).
				WithArgs(inviteWorkspaceID).
				WillReturnRows(pgxmock.NewRows([]string{"active", "initialized"}).AddRow(true, false))
			mock.ExpectExec(`SELECT pg_advisory_xact_lock`).
				WithArgs(inviteEmail).WillReturnResult(pgxmock.NewResult("SELECT", 1))
			mock.ExpectQuery(`SELECT id, email::text, display_name`).
				WithArgs(inviteEmail).WillReturnError(pgx.ErrNoRows)
			mock.ExpectQuery(`INSERT INTO auth\.users`).
				WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).WillReturnRows(newUserRows(time.Now()))
			mock.ExpectExec(`INSERT INTO auth\.user_password_credentials`).
				WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).WillReturnResult(pgxmock.NewResult("INSERT", 1))
			mock.ExpectExec(`INSERT INTO chat\.workspace_members`).
				WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).WillReturnResult(pgxmock.NewResult("INSERT", 1))
			mock.ExpectExec(`INSERT INTO chat\.channel_members`).
				WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).WillReturnResult(pgxmock.NewResult("INSERT", 1))
			mock.ExpectExec(`UPDATE auth\.user_invites`).
				WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
			mock.ExpectExec(`UPDATE auth\.user_invites`).
				WithArgs(inviteWorkspaceID, pgxmock.AnyArg()).WillReturnError(errors.New("revoke failed"))
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
			if _, err := store.AcceptInviteTx(context.Background(), "hashed-value", "User", "User Full", "argon2id-hash"); err == nil {
				t.Fatal("expected error")
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet expectations: %v", err)
			}
		})
	}
}

// ── Reinvite after expiry ──────────────────────────────────────────────────
//
// The service treated an invite as usable only while expires_at was ahead, but
// the partial unique index keys on status alone. A lapsed row therefore kept
// occupying the slot and re-inviting the address failed with a conflict that no
// amount of waiting cleared. The store now retires those rows, under the same
// lock, before the check that would reject on them.

// expectReinviteThroughOutbox scripts a creation that survives the guards and
// writes both rows, with the retirement reporting n rows updated.
func expectReinviteThroughOutbox(mock pgxmock.PgxPoolIface, expired int64) {
	mock.ExpectExec(`SELECT pg_advisory_xact_lock`).
		WithArgs(inviteEmailLockKey).
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectExec(`UPDATE auth\.user_invites`).
		WithArgs(inviteWorkspaceID, inviteEmail, inviteNow).
		WillReturnResult(pgxmock.NewResult("UPDATE", expired))
	mock.ExpectQuery(`JOIN chat\.workspace_members`).
		WithArgs(inviteWorkspaceID, inviteEmail).
		WillReturnError(pgx.ErrNoRows)
	mock.ExpectQuery(`FROM auth\.user_invites`).
		WithArgs(inviteWorkspaceID, inviteEmail, inviteNow).
		WillReturnError(pgx.ErrNoRows)
	mock.ExpectQuery(`INSERT INTO auth\.user_invites`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(inviteInsertRows(time.Now()))
	mock.ExpectExec(`INSERT INTO auth\.email_outbox`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
}

// The retirement runs under the lock and before the duplicate checks, so the
// row it retires cannot be the one that rejects the request.
func TestPGXInviteStore_CreateInviteExpiresLapsedInviteBeforeInserting(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectBegin()
	expectReinviteThroughOutbox(mock, 1)
	mock.ExpectCommit()
	mock.ExpectRollback()

	store := storage.NewPGXInviteStore(mock)
	if _, err := store.CreateInvite(context.Background(), inviteInput(), "hash", inviteNow, inviteNow.Add(time.Hour), encryptedInvitePayload, unlimited); err != nil {
		t.Fatalf("re-inviting after a lapsed invite must succeed: %v", err)
	}
	// pgxmock is ordered, so this also asserts the sequence: lock, retire,
	// check, insert, outbox, commit.
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// A live invite is not retired — the UPDATE matches nothing — and the pending
// check still rejects, writing no second invite and no second outbox row.
func TestPGXInviteStore_CreateInviteStillRejectsWhileInviteIsLive(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectBegin()
	mock.ExpectExec(`SELECT pg_advisory_xact_lock`).
		WithArgs(inviteEmailLockKey).
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectExec(`UPDATE auth\.user_invites`).
		WithArgs(inviteWorkspaceID, inviteEmail, inviteNow).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	mock.ExpectQuery(`JOIN chat\.workspace_members`).
		WithArgs(inviteWorkspaceID, inviteEmail).
		WillReturnError(pgx.ErrNoRows)
	mock.ExpectQuery(`FROM auth\.user_invites`).
		WithArgs(inviteWorkspaceID, inviteEmail, inviteNow).
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(1))
	// No INSERT and no outbox are scripted: reaching either fails this test.
	mock.ExpectRollback()

	store := storage.NewPGXInviteStore(mock)
	_, err = store.CreateInvite(context.Background(), inviteInput(), "hash", inviteNow, inviteNow.Add(time.Hour), encryptedInvitePayload, unlimited)
	if !errors.Is(err, domain.ErrInviteAlreadyPending) {
		t.Fatalf("expected ErrInviteAlreadyPending, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// The retirement is scoped to one (workspace, address) pair. A global sweep
// would let one admin's invitation rewrite unrelated tenants' rows.
func TestPGXInviteStore_CreateInviteExpiryIsScopedToWorkspaceAndEmail(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	input := inviteInput()
	input.WorkspaceID = inviteOtherWorkspaceID

	mock.ExpectBegin()
	mock.ExpectExec(`SELECT pg_advisory_xact_lock`).
		WithArgs(inviteOtherWorkspaceID + lockSeparatorForTest + inviteEmail).
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	// The workspace and address reaching the UPDATE are this request's, never
	// a wildcard.
	mock.ExpectExec(`UPDATE auth\.user_invites`).
		WithArgs(inviteOtherWorkspaceID, inviteEmail, inviteNow).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	mock.ExpectQuery(`JOIN chat\.workspace_members`).
		WithArgs(inviteOtherWorkspaceID, inviteEmail).
		WillReturnError(pgx.ErrNoRows)
	mock.ExpectQuery(`FROM auth\.user_invites`).
		WithArgs(inviteOtherWorkspaceID, inviteEmail, inviteNow).
		WillReturnError(pgx.ErrNoRows)
	mock.ExpectQuery(`INSERT INTO auth\.user_invites`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(inviteInsertRows(time.Now()))
	mock.ExpectExec(`INSERT INTO auth\.email_outbox`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit()
	mock.ExpectRollback()

	store := storage.NewPGXInviteStore(mock)
	if _, err := store.CreateInvite(context.Background(), input, "hash", inviteNow, inviteNow.Add(time.Hour), encryptedInvitePayload, unlimited); err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// A failure anywhere after the retirement rolls it back with everything else:
// an invite must never be retired by a request that then failed to replace it.
func TestPGXInviteStore_CreateInviteRollsBackExpiryOnLaterFailure(t *testing.T) {
	for _, tt := range []struct {
		name  string
		setup func(mock pgxmock.PgxPoolIface)
	}{
		{name: "expiry itself fails", setup: func(mock pgxmock.PgxPoolIface) {
			mock.ExpectBegin()
			mock.ExpectExec(`SELECT pg_advisory_xact_lock`).
				WithArgs(inviteEmailLockKey).
				WillReturnResult(pgxmock.NewResult("SELECT", 1))
			mock.ExpectExec(`UPDATE auth\.user_invites`).
				WithArgs(inviteWorkspaceID, inviteEmail, inviteNow).
				WillReturnError(errors.New("update failed"))
			mock.ExpectRollback()
		}},
		{name: "invite insert fails", setup: func(mock pgxmock.PgxPoolIface) {
			mock.ExpectBegin()
			mock.ExpectExec(`SELECT pg_advisory_xact_lock`).
				WithArgs(inviteEmailLockKey).
				WillReturnResult(pgxmock.NewResult("SELECT", 1))
			mock.ExpectExec(`UPDATE auth\.user_invites`).
				WithArgs(inviteWorkspaceID, inviteEmail, inviteNow).
				WillReturnResult(pgxmock.NewResult("UPDATE", 1))
			mock.ExpectQuery(`JOIN chat\.workspace_members`).
				WithArgs(inviteWorkspaceID, inviteEmail).
				WillReturnError(pgx.ErrNoRows)
			mock.ExpectQuery(`FROM auth\.user_invites`).
				WithArgs(inviteWorkspaceID, inviteEmail, inviteNow).
				WillReturnError(pgx.ErrNoRows)
			mock.ExpectQuery(`INSERT INTO auth\.user_invites`).
				WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
				WillReturnError(errors.New("insert failed"))
			mock.ExpectRollback()
		}},
		{name: "outbox fails", setup: func(mock pgxmock.PgxPoolIface) {
			mock.ExpectBegin()
			expectReinviteThroughOutboxUpToInsert(mock)
			mock.ExpectExec(`INSERT INTO auth\.email_outbox`).
				WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
				WillReturnError(errors.New("outbox failed"))
			mock.ExpectRollback()
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			mock, err := pgxmock.NewPool()
			if err != nil {
				t.Fatalf("pgxmock: %v", err)
			}
			defer mock.Close()
			tt.setup(mock)

			store := storage.NewPGXInviteStore(mock)
			if _, err := store.CreateInvite(context.Background(), inviteInput(), "hash", inviteNow, inviteNow.Add(time.Hour), encryptedInvitePayload, unlimited); err == nil {
				t.Fatal("expected error")
			}
			// The scripted rollback is the assertion: no commit is scripted, so
			// committing would fail the expectations.
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet expectations: %v", err)
			}
		})
	}
}

func expectReinviteThroughOutboxUpToInsert(mock pgxmock.PgxPoolIface) {
	mock.ExpectExec(`SELECT pg_advisory_xact_lock`).
		WithArgs(inviteEmailLockKey).
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectExec(`UPDATE auth\.user_invites`).
		WithArgs(inviteWorkspaceID, inviteEmail, inviteNow).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectQuery(`JOIN chat\.workspace_members`).
		WithArgs(inviteWorkspaceID, inviteEmail).
		WillReturnError(pgx.ErrNoRows)
	mock.ExpectQuery(`FROM auth\.user_invites`).
		WithArgs(inviteWorkspaceID, inviteEmail, inviteNow).
		WillReturnError(pgx.ErrNoRows)
	mock.ExpectQuery(`INSERT INTO auth\.user_invites`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(inviteInsertRows(time.Now()))
}

// The finding, at the acceptance boundary: an archived workspace that already
// has an owner must refuse a bootstrap accept. Before the fix the state probe
// filtered on an active workspace, so this read as "uninitialized" and the
// accept created a second owner that took effect on reactivation.
func TestPGXInviteStore_AcceptInviteTxBootstrapRefusedOnArchivedWorkspace(t *testing.T) {
	for _, tt := range []struct {
		name        string
		active      bool
		initialized bool
	}{
		{name: "archived with an owner", active: false, initialized: true},
		// Even with no owner yet: granting ownership of a workspace nobody is
		// running would take effect the moment somebody reactivates it.
		{name: "archived without an owner", active: false, initialized: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			mock, err := pgxmock.NewPool()
			if err != nil {
				t.Fatalf("pgxmock: %v", err)
			}
			defer mock.Close()

			mock.ExpectBegin()
			mock.ExpectQuery(`SELECT id, email::text, COALESCE\(workspace_id`).
				WithArgs("hashed-value").WillReturnRows(bootstrapInviteRows())
			mock.ExpectExec(`SELECT pg_advisory_xact_lock`).
				WithArgs(bootstrapWorkspaceLockKey).WillReturnResult(pgxmock.NewResult("SELECT", 1))
			mock.ExpectQuery(`SELECT id, email::text, COALESCE\(workspace_id`).
				WithArgs("hashed-value").WillReturnRows(bootstrapInviteRows())
			mock.ExpectQuery(`FROM chat\.workspaces w`).
				WithArgs(inviteWorkspaceID).
				WillReturnRows(pgxmock.NewRows([]string{"active", "initialized"}).AddRow(tt.active, tt.initialized))
			// No identity, membership or acceptance is scripted: reaching any
			// of them fails this test.
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

// A workspace that no longer exists is not an open window either.
func TestPGXInviteStore_AcceptInviteTxBootstrapRefusedOnMissingWorkspace(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT id, email::text, COALESCE\(workspace_id`).
		WithArgs("hashed-value").WillReturnRows(bootstrapInviteRows())
	mock.ExpectExec(`SELECT pg_advisory_xact_lock`).
		WithArgs(bootstrapWorkspaceLockKey).WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectQuery(`SELECT id, email::text, COALESCE\(workspace_id`).
		WithArgs("hashed-value").WillReturnRows(bootstrapInviteRows())
	mock.ExpectQuery(`FROM chat\.workspaces w`).
		WithArgs(inviteWorkspaceID).WillReturnError(pgx.ErrNoRows)
	mock.ExpectRollback()

	store := storage.NewPGXInviteStore(mock)
	_, err = store.AcceptInviteTx(context.Background(), "hashed-value", "User", "User Full", "argon2id-hash")
	if !errors.Is(err, domain.ErrInvalidToken) {
		t.Fatalf("expected ErrInvalidToken, got %v", err)
	}
}
