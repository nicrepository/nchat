//go:build integration

package storage_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nicrepository/nchat/services/auth-service/internal/domain"
	"github.com/nicrepository/nchat/services/auth-service/internal/storage"
)

// Whether a lapsed invite blocks a new one is a question about a partial unique
// index and a transaction, so only a real server can answer it: the index is
// the thing that was disagreeing with the service, and a mock would replay
// whichever belief the test author held.
// Shared harness helpers live in invite_migration_postgres_test.go.

const reinviteEmail = "reinvite@example.test"

func reinviteDatabase(t *testing.T, ctx context.Context) *storage.PGXInviteStore {
	t.Helper()
	conn := migratedDatabase(t, ctx)
	seedPolicy(t, ctx, conn)
	return storage.NewPGXInviteStore(testPool(t, ctx))
}

// insertLapsedInvite writes a pending invite whose TTL has already run out —
// the state the partial unique index used to treat as occupying the slot.
func insertLapsedInvite(t *testing.T, ctx context.Context, workspaceID, email, tokenHash string) string {
	t.Helper()
	conn := connectTestDatabase(t, ctx)
	var id string
	if err := conn.QueryRow(ctx, `
		INSERT INTO auth.user_invites (workspace_id, email, token_hash, status, expires_at, invite_kind)
		VALUES ($1::uuid, $2, $3, 'pending', now() - interval '1 hour', 'member')
		RETURNING id::text`,
		workspaceID, email, tokenHash,
	).Scan(&id); err != nil {
		t.Fatalf("seed lapsed invite: %v", err)
	}
	return id
}

func createInvite(ctx context.Context, store *storage.PGXInviteStore, workspaceID, email, tokenHash string) (domain.InviteResult, error) {
	now := time.Now().UTC()
	return store.CreateInvite(ctx, domain.AdminInviteInput{
		WorkspaceID: workspaceID, Email: email, DisplayName: "Reinvited",
		Kind: domain.InviteKindMember,
	}, tokenHash, now, now.Add(48*time.Hour), `{"alg":"AES-256-GCM","key_version":"v1","nonce":"bm9uY2U=","ciphertext":"Y2lwaGVydGV4dA=="}`, domain.InviteRateLimit{})
}

// The finding: a lapsed invite blocked its own replacement, and no amount of
// waiting cleared it because nothing ever moved the row out of 'pending'.
func TestReinvite_LapsedInviteIsRetiredAndReplaced(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	store := reinviteDatabase(t, ctx)
	conn := connectTestDatabase(t, ctx)
	oldID := insertLapsedInvite(t, ctx, pgWorkspaceA, reinviteEmail, "lapsed-token-hash")

	result, err := createInvite(ctx, store, pgWorkspaceA, reinviteEmail, "fresh-token-hash")
	if err != nil {
		t.Fatalf("re-inviting after the TTL lapsed must succeed: %v", err)
	}

	var oldStatus string
	if err := conn.QueryRow(ctx, `SELECT status FROM auth.user_invites WHERE id = $1::uuid`, oldID).Scan(&oldStatus); err != nil {
		t.Fatalf("read the retired invite: %v", err)
	}
	if oldStatus != "expired" {
		t.Fatalf("the lapsed invite must be retired, status is %q", oldStatus)
	}

	// Exactly one live invite, and it is the new one.
	assertCount(t, ctx, conn, 1,
		`SELECT count(*) FROM auth.user_invites WHERE workspace_id = $1::uuid AND email = $2 AND status = 'pending'`,
		pgWorkspaceA, reinviteEmail)
	assertCount(t, ctx, conn, 1,
		`SELECT count(*) FROM auth.user_invites WHERE id = $1::uuid AND status = 'pending'`, result.ID)
	// One new outbox row: the retirement must not have queued anything itself.
	assertCount(t, ctx, conn, 1, `SELECT count(*) FROM auth.email_outbox WHERE invite_id = $1::uuid`, result.ID)
	assertCount(t, ctx, conn, 1, `SELECT count(*) FROM auth.email_outbox`)
	// The retired row keeps its own token; the new invite has a different one,
	// so the replacement is a genuinely new credential.
	assertCount(t, ctx, conn, 1,
		`SELECT count(*) FROM auth.user_invites WHERE id = $1::uuid AND token_hash = 'fresh-token-hash'`, result.ID)
	assertNoOpenTransactions(t, ctx, conn)
}

// The new invite is an ordinary one: it goes on to be accepted normally.
func TestReinvite_ReplacementInviteCompletesNormally(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	store := reinviteDatabase(t, ctx)
	conn := connectTestDatabase(t, ctx)
	insertLapsedInvite(t, ctx, pgWorkspaceA, reinviteEmail, "lapsed-token-hash")

	if _, err := createInvite(ctx, store, pgWorkspaceA, reinviteEmail, "fresh-token-hash"); err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}
	if _, err := store.AcceptInviteTx(ctx, "fresh-token-hash", "Reinvited", "Reinvited User", "argon2id-hash"); err != nil {
		t.Fatalf("the replacement invite must be acceptable: %v", err)
	}

	assertCount(t, ctx, conn, 1,
		`SELECT count(*) FROM chat.workspace_members wm JOIN auth.users u ON u.id = wm.user_id
		 WHERE wm.workspace_id = $1::uuid AND u.email = $2 AND wm.role = 'member'`,
		pgWorkspaceA, reinviteEmail)
	// The retired invite stays retired — it is not resurrected by the accept.
	assertCount(t, ctx, conn, 1,
		`SELECT count(*) FROM auth.user_invites WHERE token_hash = 'lapsed-token-hash' AND status = 'expired'`)
}

// A live invite still blocks, and the rejected attempt writes nothing.
func TestReinvite_LiveInviteStillBlocks(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	store := reinviteDatabase(t, ctx)
	conn := connectTestDatabase(t, ctx)

	if _, err := createInvite(ctx, store, pgWorkspaceA, reinviteEmail, "live-token-hash"); err != nil {
		t.Fatalf("first invite: %v", err)
	}

	_, err := createInvite(ctx, store, pgWorkspaceA, reinviteEmail, "second-token-hash")
	if !errors.Is(err, domain.ErrInviteAlreadyPending) {
		t.Fatalf("expected ErrInviteAlreadyPending while the invite is live, got %v", err)
	}
	assertCount(t, ctx, conn, 1,
		`SELECT count(*) FROM auth.user_invites WHERE workspace_id = $1::uuid AND email = $2`,
		pgWorkspaceA, reinviteEmail)
	assertCount(t, ctx, conn, 1, `SELECT count(*) FROM auth.email_outbox`)
}

// The retirement is scoped: another workspace's invite for the same address,
// and this workspace's invite for another address, are both left alone.
func TestReinvite_RetirementDoesNotReachOtherRows(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	store := reinviteDatabase(t, ctx)
	conn := connectTestDatabase(t, ctx)

	otherWorkspace := insertLapsedInvite(t, ctx, pgWorkspaceB, reinviteEmail, "other-workspace-hash")
	otherEmail := insertLapsedInvite(t, ctx, pgWorkspaceA, "someone-else@example.test", "other-email-hash")
	insertLapsedInvite(t, ctx, pgWorkspaceA, reinviteEmail, "lapsed-token-hash")

	if _, err := createInvite(ctx, store, pgWorkspaceA, reinviteEmail, "fresh-token-hash"); err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}

	for _, id := range []string{otherWorkspace, otherEmail} {
		var status string
		if err := conn.QueryRow(ctx, `SELECT status FROM auth.user_invites WHERE id = $1::uuid`, id).Scan(&status); err != nil {
			t.Fatalf("read untouched invite: %v", err)
		}
		if status != "pending" {
			t.Fatalf("an unrelated invite must not be retired, status is %q", status)
		}
	}
}

// Accepted and revoked rows are never rewritten, whatever their expires_at.
func TestReinvite_RetirementLeavesSettledRowsAlone(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	store := reinviteDatabase(t, ctx)
	conn := connectTestDatabase(t, ctx)

	if _, err := conn.Exec(ctx, `
		INSERT INTO auth.user_invites (workspace_id, email, token_hash, status, expires_at, accepted_at, invite_kind)
		VALUES ($1::uuid, $2, 'settled-accepted', 'accepted', now() - interval '2 hours', now() - interval '3 hours', 'member'),
		       ($1::uuid, $2, 'settled-revoked',  'revoked',  now() - interval '2 hours', NULL, 'member')`,
		pgWorkspaceA, reinviteEmail); err != nil {
		t.Fatalf("seed settled invites: %v", err)
	}

	if _, err := createInvite(ctx, store, pgWorkspaceA, reinviteEmail, "fresh-token-hash"); err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}

	assertCount(t, ctx, conn, 1,
		`SELECT count(*) FROM auth.user_invites WHERE token_hash = 'settled-accepted' AND status = 'accepted' AND accepted_at IS NOT NULL`)
	assertCount(t, ctx, conn, 1,
		`SELECT count(*) FROM auth.user_invites WHERE token_hash = 'settled-revoked' AND status = 'revoked'`)
}

// Two admins re-inviting the same lapsed address at the same instant: the lock
// serialises them, so one creates the replacement and the other finds it live.
func TestReinvite_ConcurrentReinvitesProduceOneInvite(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	conn := migratedDatabase(t, ctx)
	seedPolicy(t, ctx, conn)
	insertLapsedInvite(t, ctx, pgWorkspaceA, reinviteEmail, "lapsed-token-hash")

	// Independent pools: two real connections, not two goroutines sharing one.
	storeOne := storage.NewPGXInviteStore(testPool(t, ctx))
	storeTwo := storage.NewPGXInviteStore(testPool(t, ctx))

	errs := raceTwo(
		func() error {
			_, err := createInvite(ctx, storeOne, pgWorkspaceA, reinviteEmail, "race-token-one")
			return err
		},
		func() error {
			_, err := createInvite(ctx, storeTwo, pgWorkspaceA, reinviteEmail, "race-token-two")
			return err
		},
	)

	assertExactlyOneWinner(t, errs, domain.ErrInviteAlreadyPending)
	assertCount(t, ctx, conn, 1,
		`SELECT count(*) FROM auth.user_invites WHERE workspace_id = $1::uuid AND email = $2 AND status = 'pending'`,
		pgWorkspaceA, reinviteEmail)
	// One replacement means one e-mail; the loser must not have queued a second.
	assertCount(t, ctx, conn, 1, `SELECT count(*) FROM auth.email_outbox`)
	assertCount(t, ctx, conn, 1,
		`SELECT count(*) FROM auth.user_invites WHERE token_hash = 'lapsed-token-hash' AND status = 'expired'`)
	assertNoOpenTransactions(t, ctx, conn)
}
