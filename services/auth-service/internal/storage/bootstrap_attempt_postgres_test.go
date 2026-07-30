//go:build integration

package storage_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/nicrepository/nchat/services/auth-service/internal/domain"
	"github.com/nicrepository/nchat/services/auth-service/internal/storage"
)

// "Distributed" is a claim about two processes sharing one counter, and only a
// real server can settle it: an in-memory double would share a Go map, which is
// precisely the property under test being assumed rather than demonstrated.
// Shared harness helpers live in invite_migration_postgres_test.go.

const (
	bootstrapLimiterKeyA = "bootstrap-admin-token:203.0.113.10"
	bootstrapLimiterKeyB = "bootstrap-admin-token:198.51.100.7"
)

func bootstrapAttemptDatabase(t *testing.T, ctx context.Context) *storage.PGXBootstrapAttemptStore {
	t.Helper()
	conn := connectTestDatabase(t, ctx)
	applyMigrationsBefore008(t, ctx, conn)
	applyInviteScopeUp(t, ctx, conn)
	applyMigrationFile(t, ctx, conn,
		repositoryRoot(t)+"/migrations/auth/000009_bootstrap_auth_attempts.up.sql")
	return storage.NewPGXBootstrapAttemptStore(testPool(t, ctx))
}

// Two stores over two independent pools are two replicas over one database.
// Alternating between them must spend a single budget — the failure this
// replaces is N replicas granting N times the guesses.
func TestBootstrapAttempts_BudgetIsSharedAcrossReplicas(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	replicaOne := bootstrapAttemptDatabase(t, ctx)
	replicaTwo := storage.NewPGXBootstrapAttemptStore(testPool(t, ctx))

	const limit = 5
	window := time.Hour

	for i := 0; i < limit; i++ {
		replica := replicaOne
		if i%2 == 1 {
			replica = replicaTwo
		}
		allowed, err := replica.RecordAttempt(ctx, bootstrapLimiterKeyA, limit, window)
		if err != nil {
			t.Fatalf("attempt %d: %v", i+1, err)
		}
		if !allowed {
			t.Fatalf("attempt %d of %d must stay within the shared budget", i+1, limit)
		}
	}

	// The attempt past the budget is refused whichever replica serves it.
	allowed, err := replicaTwo.RecordAttempt(ctx, bootstrapLimiterKeyA, limit, window)
	if err != nil {
		t.Fatalf("RecordAttempt: %v", err)
	}
	if allowed {
		t.Fatal("the attempt past the shared budget must be refused")
	}

	// A different address is unaffected: exhausting one must not lock out
	// everyone behind another.
	allowed, err = replicaOne.RecordAttempt(ctx, bootstrapLimiterKeyB, limit, window)
	if err != nil {
		t.Fatalf("RecordAttempt: %v", err)
	}
	if !allowed {
		t.Fatal("a different key must have its own budget")
	}
}

// The window is what makes the budget recover. Advancing it deterministically
// — by writing the previous window's row directly rather than waiting — keeps
// the test free of sleeps.
func TestBootstrapAttempts_BudgetRecoversInTheNextWindow(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	store := bootstrapAttemptDatabase(t, ctx)
	conn := connectTestDatabase(t, ctx)

	const limit = 2
	window := time.Hour

	for i := 0; i < limit; i++ {
		if _, err := store.RecordAttempt(ctx, bootstrapLimiterKeyA, limit, window); err != nil {
			t.Fatalf("attempt %d: %v", i+1, err)
		}
	}
	allowed, err := store.RecordAttempt(ctx, bootstrapLimiterKeyA, limit, window)
	if err != nil {
		t.Fatalf("RecordAttempt: %v", err)
	}
	if allowed {
		t.Fatal("the budget must be exhausted before the window is advanced")
	}

	// Move the exhausted counter into the previous window: the next attempt
	// therefore lands in a window with no history, exactly as it would after
	// the clock advanced.
	if _, err := conn.Exec(ctx, `
		UPDATE auth.bootstrap_auth_attempts
		SET window_start = window_start - $2::interval
		WHERE limiter_key = $1`,
		bootstrapLimiterKeyA, window.String(),
	); err != nil {
		t.Fatalf("advance window: %v", err)
	}

	allowed, err = store.RecordAttempt(ctx, bootstrapLimiterKeyA, limit, window)
	if err != nil {
		t.Fatalf("RecordAttempt: %v", err)
	}
	if !allowed {
		t.Fatal("the budget must recover in the next window")
	}
}

// Concurrent attempts must not both observe the last remaining slot: the count
// and the increment are one statement precisely so they cannot.
func TestBootstrapAttempts_ConcurrentAttemptsShareOneCounter(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	replicaOne := bootstrapAttemptDatabase(t, ctx)
	replicaTwo := storage.NewPGXBootstrapAttemptStore(testPool(t, ctx))

	// Budget of one: exactly one of two racing attempts may be allowed.
	const limit = 1
	window := time.Hour

	var allowedOne, allowedTwo bool
	errs := raceTwo(
		func() (err error) {
			allowedOne, err = replicaOne.RecordAttempt(ctx, bootstrapLimiterKeyA, limit, window)
			return err
		},
		func() (err error) {
			allowedTwo, err = replicaTwo.RecordAttempt(ctx, bootstrapLimiterKeyA, limit, window)
			return err
		},
	)
	for _, err := range errs {
		if err != nil {
			t.Fatalf("racing attempt failed: %v", err)
		}
	}

	if allowedOne == allowedTwo {
		t.Fatalf("exactly one racing attempt may be allowed, got %v and %v", allowedOne, allowedTwo)
	}

	var attempts int
	conn := connectTestDatabase(t, ctx)
	if err := conn.QueryRow(ctx,
		`SELECT attempts FROM auth.bootstrap_auth_attempts WHERE limiter_key = $1`,
		bootstrapLimiterKeyA,
	).Scan(&attempts); err != nil {
		t.Fatalf("read counter: %v", err)
	}
	if attempts != 2 {
		t.Fatalf("both attempts must be charged against one counter, got %d", attempts)
	}
}

// The sweep is housekeeping, not correctness: an expired window is never read.
// It exists so the table does not keep one row per (IP, window) forever.
func TestBootstrapAttempts_SweepDiscardsExpiredWindows(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	store := bootstrapAttemptDatabase(t, ctx)
	conn := connectTestDatabase(t, ctx)
	window := time.Hour

	if _, err := store.RecordAttempt(ctx, bootstrapLimiterKeyA, 5, window); err != nil {
		t.Fatalf("RecordAttempt: %v", err)
	}
	if _, err := conn.Exec(ctx, `
		INSERT INTO auth.bootstrap_auth_attempts (limiter_key, window_start, attempts)
		VALUES ($1, now() - interval '30 days', 99)`, bootstrapLimiterKeyB); err != nil {
		t.Fatalf("seed stale window: %v", err)
	}

	if err := store.SweepExpired(ctx, window); err != nil {
		t.Fatalf("SweepExpired: %v", err)
	}

	assertCount(t, ctx, conn, 0,
		`SELECT count(*) FROM auth.bootstrap_auth_attempts WHERE limiter_key = $1`, bootstrapLimiterKeyB)
	assertCount(t, ctx, conn, 1,
		`SELECT count(*) FROM auth.bootstrap_auth_attempts WHERE limiter_key = $1`, bootstrapLimiterKeyA)
}

// The migration is reversible like every other one in this service.
func TestBootstrapAttemptsMigration_Reverses(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	conn := connectTestDatabase(t, ctx)
	applyMigrationsBefore008(t, ctx, conn)
	applyInviteScopeUp(t, ctx, conn)
	root := repositoryRoot(t)
	applyMigrationFile(t, ctx, conn, root+"/migrations/auth/000009_bootstrap_auth_attempts.up.sql")

	if !objectExists(t, ctx, conn, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.tables
			WHERE table_schema = 'auth' AND table_name = 'bootstrap_auth_attempts')`) {
		t.Fatal("expected the counter table after the up")
	}
	if !indexExists(t, ctx, conn, "idx_bootstrap_auth_attempts_window") {
		t.Fatal("expected the sweep index after the up")
	}

	applyMigrationFile(t, ctx, conn, root+"/migrations/auth/000009_bootstrap_auth_attempts.down.sql")

	if objectExists(t, ctx, conn, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.tables
			WHERE table_schema = 'auth' AND table_name = 'bootstrap_auth_attempts')`) {
		t.Fatal("the counter table must be gone after the down")
	}
}

// ── Bootstrap window and workspace status ──────────────────────────────────
//
// Archiving a workspace that already had an owner used to make it read as
// uninitialized, reopening a window that had closed: the bootstrap credential
// could mint a bootstrap_owner invite whose acceptance granted ownership that
// materialised on reactivation. These run against real rows because the bug was
// in a join, and a join is exactly what a mock cannot check.

func archiveWorkspace(t *testing.T, ctx context.Context, conn *pgx.Conn, workspaceID string) {
	t.Helper()
	if _, err := conn.Exec(ctx,
		`UPDATE chat.workspaces SET status = 'disabled' WHERE id = $1::uuid`, workspaceID); err != nil {
		t.Fatalf("archive workspace: %v", err)
	}
}

func makeWorkspaceOwner(t *testing.T, ctx context.Context, conn *pgx.Conn, workspaceID, email string) {
	t.Helper()
	var userID string
	if err := conn.QueryRow(ctx, `
		INSERT INTO auth.users (email, display_name, status) VALUES ($1, 'Owner', 'active')
		RETURNING id::text`, email).Scan(&userID); err != nil {
		t.Fatalf("seed owner user: %v", err)
	}
	if _, err := conn.Exec(ctx, `
		INSERT INTO chat.workspace_members (workspace_id, user_id, role, status)
		VALUES ($1::uuid, $2::uuid, 'owner', 'active')`, workspaceID, userID); err != nil {
		t.Fatalf("seed owner membership: %v", err)
	}
}

// An archived workspace that already has an owner stays initialized, so the
// window stays shut.
func TestBootstrapWorkspaceState_ArchivingDoesNotReopenTheWindow(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	conn := migratedDatabase(t, ctx)
	store := storage.NewPGXInviteStore(testPool(t, ctx))
	makeWorkspaceOwner(t, ctx, conn, pgWorkspaceA, "owner-a@example.test")

	state, err := store.BootstrapWorkspaceState(ctx, pgWorkspaceA)
	if err != nil {
		t.Fatalf("BootstrapWorkspaceState: %v", err)
	}
	if !state.Initialized || state.BootstrapOpen() {
		t.Fatalf("an owned workspace must be initialized and shut, got %+v", state)
	}

	archiveWorkspace(t, ctx, conn, pgWorkspaceA)

	state, err = store.BootstrapWorkspaceState(ctx, pgWorkspaceA)
	if err != nil {
		t.Fatalf("BootstrapWorkspaceState: %v", err)
	}
	if !state.Initialized {
		t.Fatal("archiving must not un-initialize a workspace that has an owner")
	}
	if state.Active {
		t.Fatal("an archived workspace must not report as active")
	}
	if state.BootstrapOpen() {
		t.Fatal("the bootstrap window must stay shut for an archived, owned workspace")
	}
}

// The exploit path end to end: with the workspace archived, accepting a
// bootstrap invite must write nothing, so reactivating it later confers no
// ownership.
func TestAcceptInviteTx_BootstrapRefusedOnArchivedWorkspaceAndReactivationGrantsNothing(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	conn := migratedDatabase(t, ctx)
	seedPolicy(t, ctx, conn)
	store := storage.NewPGXInviteStore(testPool(t, ctx))

	makeWorkspaceOwner(t, ctx, conn, pgWorkspaceA, "owner-a@example.test")
	workspaceA := pgWorkspaceA
	insertInvite(t, ctx, conn, &workspaceA, "attacker@example.test", "archived-bootstrap-hash", domain.InviteKindBootstrapOwner)
	archiveWorkspace(t, ctx, conn, pgWorkspaceA)

	_, err := store.AcceptInviteTx(ctx, "archived-bootstrap-hash", "Attacker", "Attacker", "argon2id-hash")
	if !errors.Is(err, domain.ErrInvalidToken) {
		t.Fatalf("expected ErrInvalidToken on an archived workspace, got %v", err)
	}

	// Nothing was written: no identity, no membership, invite not consumed.
	assertCount(t, ctx, conn, 0, `SELECT count(*) FROM auth.users WHERE email = 'attacker@example.test'`)
	assertCount(t, ctx, conn, 1,
		`SELECT count(*) FROM chat.workspace_members WHERE workspace_id = $1::uuid AND role = 'owner'`, pgWorkspaceA)
	assertCount(t, ctx, conn, 1,
		`SELECT count(*) FROM auth.user_invites WHERE token_hash = 'archived-bootstrap-hash' AND status = 'pending'`)

	// Reactivating the workspace confers nothing on the invitee, and the
	// invite is still refused because the workspace has an owner.
	if _, err := conn.Exec(ctx,
		`UPDATE chat.workspaces SET status = 'active' WHERE id = $1::uuid`, pgWorkspaceA); err != nil {
		t.Fatalf("reactivate workspace: %v", err)
	}
	assertCount(t, ctx, conn, 1,
		`SELECT count(*) FROM chat.workspace_members WHERE workspace_id = $1::uuid AND role = 'owner'`, pgWorkspaceA)

	_, err = store.AcceptInviteTx(ctx, "archived-bootstrap-hash", "Attacker", "Attacker", "argon2id-hash")
	if !errors.Is(err, domain.ErrInvalidToken) {
		t.Fatalf("expected ErrInvalidToken after reactivation, got %v", err)
	}
	assertCount(t, ctx, conn, 0, `SELECT count(*) FROM auth.users WHERE email = 'attacker@example.test'`)
	assertNoOpenTransactions(t, ctx, conn)
}

// An archived workspace with no owner is refused too: the grant would take
// effect the moment somebody reactivates it.
func TestAcceptInviteTx_BootstrapRefusedOnArchivedUninitializedWorkspace(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	conn := migratedDatabase(t, ctx)
	seedPolicy(t, ctx, conn)
	store := storage.NewPGXInviteStore(testPool(t, ctx))

	workspaceB := pgWorkspaceB
	insertInvite(t, ctx, conn, &workspaceB, "first@example.test", "archived-empty-hash", domain.InviteKindBootstrapOwner)
	archiveWorkspace(t, ctx, conn, pgWorkspaceB)

	_, err := store.AcceptInviteTx(ctx, "archived-empty-hash", "First", "First", "argon2id-hash")
	if !errors.Is(err, domain.ErrInvalidToken) {
		t.Fatalf("expected ErrInvalidToken, got %v", err)
	}
	assertCount(t, ctx, conn, 0,
		`SELECT count(*) FROM chat.workspace_members WHERE workspace_id = $1::uuid`, pgWorkspaceB)
	assertCount(t, ctx, conn, 0, `SELECT count(*) FROM auth.users WHERE email = 'first@example.test'`)
}

// The ordinary path is unaffected: an active, uninitialized workspace still
// bootstraps normally.
func TestAcceptInviteTx_BootstrapStillWorksOnActiveUninitializedWorkspace(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	conn := migratedDatabase(t, ctx)
	seedPolicy(t, ctx, conn)
	store := storage.NewPGXInviteStore(testPool(t, ctx))

	workspaceA := pgWorkspaceA
	insertInvite(t, ctx, conn, &workspaceA, "first@example.test", "healthy-bootstrap-hash", domain.InviteKindBootstrapOwner)

	if _, err := store.AcceptInviteTx(ctx, "healthy-bootstrap-hash", "First", "First", "argon2id-hash"); err != nil {
		t.Fatalf("bootstrap on an active workspace must still work: %v", err)
	}
	assertCount(t, ctx, conn, 1,
		`SELECT count(*) FROM chat.workspace_members WHERE workspace_id = $1::uuid AND role = 'owner'`, pgWorkspaceA)

	state, err := store.BootstrapWorkspaceState(ctx, pgWorkspaceA)
	if err != nil {
		t.Fatalf("BootstrapWorkspaceState: %v", err)
	}
	if !state.Initialized || state.BootstrapOpen() {
		t.Fatalf("the window must close on the first owner, got %+v", state)
	}
}

// ── Bootstrap issuance races bootstrap acceptance ──────────────────────────
//
// Issuance validated the window on one connection and persisted on another, so
// an acceptance committing in between produced a 201, a bootstrap_owner invite
// and an outbox row for a credential that could never be accepted. Both paths
// now take the same workspace lock, so whichever commits first is seen by the
// other. Real connections, because the whole claim is about two transactions.

// issueBootstrapInvite mints a bootstrap_owner invite the way the service does:
// workspace from configuration, no human actor, kind fixed server-side.
func issueBootstrapInvite(ctx context.Context, store *storage.PGXInviteStore, workspaceID, email, tokenHash string) error {
	now := time.Now().UTC()
	_, err := store.CreateInvite(ctx, domain.AdminInviteInput{
		WorkspaceID: workspaceID,
		ActorID:     domain.BootstrapInviteIssuer,
		Email:       email,
		DisplayName: "Second Owner",
		Kind:        domain.InviteKindBootstrapOwner,
	}, tokenHash, now, now.Add(48*time.Hour),
		`{"alg":"AES-256-GCM","key_version":"v1","nonce":"bm9uY2U=","ciphertext":"Y2lwaGVydGV4dA=="}`,
		domain.InviteRateLimit{})
	return err
}

// Whichever of the two commits first, the outcome must be a lifecycle with no
// loose ends: exactly one owner, and no bootstrap_owner invite left pending —
// because a pending one is precisely an invite that can never be accepted.
func TestBootstrapIssuanceRacesAcceptance_LeavesNoUnusableInvite(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	conn := migratedDatabase(t, ctx)
	seedPolicy(t, ctx, conn)

	// One workspace per round, so several interleavings are exercised without
	// re-running the migrations each time.
	rounds := []string{
		"a0000000-0000-4000-8000-0000000000a1",
		"a0000000-0000-4000-8000-0000000000a2",
		"a0000000-0000-4000-8000-0000000000a3",
		"a0000000-0000-4000-8000-0000000000a4",
		"a0000000-0000-4000-8000-0000000000a5",
	}
	for i, workspaceID := range rounds {
		seedWorkspace(t, ctx, conn, workspaceID, fmt.Sprintf("ws-race-%d", i), "active")
	}

	accepting := storage.NewPGXInviteStore(testPool(t, ctx))
	issuing := storage.NewPGXInviteStore(testPool(t, ctx))

	for i, workspaceID := range rounds {
		t.Run(fmt.Sprintf("round-%d", i), func(t *testing.T) {
			acceptedToken := fmt.Sprintf("race-accept-%d", i)
			issuedToken := fmt.Sprintf("race-issue-%d", i)
			ws := workspaceID
			insertInvite(t, ctx, conn, &ws, fmt.Sprintf("owner-%d@example.test", i), acceptedToken, domain.InviteKindBootstrapOwner)

			var issueErr error
			errs := raceTwo(
				func() error {
					_, err := accepting.AcceptInviteTx(ctx, acceptedToken, "Owner", "Owner", "argon2id-hash")
					return err
				},
				func() error {
					// Recorded rather than returned: refusal is a legitimate
					// outcome of this race, not a test failure.
					issueErr = issueBootstrapInvite(ctx, issuing, workspaceID, fmt.Sprintf("second-%d@example.test", i), issuedToken)
					return nil
				},
			)
			for _, err := range errs {
				if err != nil {
					t.Fatalf("the acceptance must succeed: %v", err)
				}
			}

			// Exactly one owner, whichever way the race went.
			assertCount(t, ctx, conn, 1,
				`SELECT count(*) FROM chat.workspace_members WHERE workspace_id = $1::uuid AND role = 'owner'`, workspaceID)

			switch {
			case issueErr == nil:
				// Issuance won the lock and committed first. The acceptance
				// then revoked it as a sibling, so it is not left pending.
				assertCount(t, ctx, conn, 1,
					`SELECT count(*) FROM auth.user_invites WHERE token_hash = $1 AND status = 'revoked'`, issuedToken)
			case errors.Is(issueErr, domain.ErrBootstrapUnavailable):
				// The acceptance won: issuance refused and wrote nothing.
				assertCount(t, ctx, conn, 0,
					`SELECT count(*) FROM auth.user_invites WHERE token_hash = $1`, issuedToken)
				assertCount(t, ctx, conn, 0,
					`SELECT count(*) FROM auth.email_outbox eo
					 JOIN auth.user_invites ui ON ui.id = eo.invite_id
					 WHERE ui.token_hash = $1`, issuedToken)
			default:
				t.Fatalf("unexpected issuance failure: %v", issueErr)
			}

			// The invariant the finding is about: nothing bootstrap-shaped is
			// left pending, so no e-mail promises a credential that is dead.
			assertCount(t, ctx, conn, 0,
				`SELECT count(*) FROM auth.user_invites
				 WHERE workspace_id = $1::uuid AND invite_kind = 'bootstrap_owner' AND status = 'pending'`,
				workspaceID)
			assertNoOpenTransactions(t, ctx, conn)
		})
	}
}

// Sequentially, after the window has closed, issuance is simply refused — and
// leaves no invite and no outbox row behind.
func TestBootstrapIssuance_RefusedOnceWindowClosed(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	conn := migratedDatabase(t, ctx)
	seedPolicy(t, ctx, conn)
	store := storage.NewPGXInviteStore(testPool(t, ctx))

	workspaceA := pgWorkspaceA
	insertInvite(t, ctx, conn, &workspaceA, "owner@example.test", "closing-token", domain.InviteKindBootstrapOwner)
	if _, err := store.AcceptInviteTx(ctx, "closing-token", "Owner", "Owner", "argon2id-hash"); err != nil {
		t.Fatalf("first acceptance: %v", err)
	}

	err := issueBootstrapInvite(ctx, store, pgWorkspaceA, "late@example.test", "late-token")
	if !errors.Is(err, domain.ErrBootstrapUnavailable) {
		t.Fatalf("expected ErrBootstrapUnavailable once the window is shut, got %v", err)
	}
	// The refusal writes nothing: no invite row, and so no e-mail promising a
	// credential that could never be accepted. The seeded invite above was
	// inserted directly, so the outbox is empty either way.
	assertCount(t, ctx, conn, 0, `SELECT count(*) FROM auth.user_invites WHERE token_hash = 'late-token'`)
	assertCount(t, ctx, conn, 0, `SELECT count(*) FROM auth.email_outbox`)
}

// Two concurrent issuances into a still-open workspace must not deadlock on the
// workspace lock they now share, and both are legitimate: the window is open
// for each.
func TestBootstrapIssuance_ConcurrentIssuancesDoNotDeadlock(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	conn := migratedDatabase(t, ctx)
	seedPolicy(t, ctx, conn)
	storeOne := storage.NewPGXInviteStore(testPool(t, ctx))
	storeTwo := storage.NewPGXInviteStore(testPool(t, ctx))

	errs := raceTwo(
		func() error {
			return issueBootstrapInvite(ctx, storeOne, pgWorkspaceA, "one@example.test", "issue-one")
		},
		func() error {
			return issueBootstrapInvite(ctx, storeTwo, pgWorkspaceA, "two@example.test", "issue-two")
		},
	)
	for _, err := range errs {
		if err != nil {
			t.Fatalf("both issuances are valid while the window is open: %v", err)
		}
	}

	assertCount(t, ctx, conn, 2,
		`SELECT count(*) FROM auth.user_invites WHERE workspace_id = $1::uuid AND invite_kind = 'bootstrap_owner' AND status = 'pending'`,
		pgWorkspaceA)
	assertNoOpenTransactions(t, ctx, conn)

	// The first acceptance still closes the window and retires both siblings.
	if _, err := storeOne.AcceptInviteTx(ctx, "issue-one", "One", "One", "argon2id-hash"); err != nil {
		t.Fatalf("acceptance: %v", err)
	}
	assertCount(t, ctx, conn, 0,
		`SELECT count(*) FROM auth.user_invites WHERE workspace_id = $1::uuid AND invite_kind = 'bootstrap_owner' AND status = 'pending'`,
		pgWorkspaceA)
}
