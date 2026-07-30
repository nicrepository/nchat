//go:build integration

package storage_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/nicrepository/nchat/services/auth-service/internal/domain"
	"github.com/nicrepository/nchat/services/auth-service/internal/storage"
)

// Concurrency properties of the invitation transaction, exercised against a
// real server: FOR UPDATE, the workspace advisory lock, the constraints and the
// commit boundary are the things under test, and none of them exists in a mock.
// Shared harness helpers live in invite_migration_postgres_test.go.

// ── Concurrency ────────────────────────────────────────────────────────────

// raceTwo runs both functions from independent pool connections, released
// together by a barrier channel so neither has a head start. No sleeps: the
// synchronisation is the closed channel and the WaitGroup.
func raceTwo(first, second func() error) (errs [2]error) {
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	for i, fn := range []func() error{first, second} {
		go func(i int, fn func() error) {
			defer wg.Done()
			<-start
			errs[i] = fn()
		}(i, fn)
	}
	close(start)
	wg.Wait()
	return errs
}

// Two concurrent acceptances of the same token. Exactly one must succeed; the
// SQL doing the work is FOR UPDATE on the invite row plus the conditional
// UPDATE that consumes it.
func TestAcceptInviteTx_ConcurrentAcceptsProduceOneMembership(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	conn := migratedDatabase(t, ctx)
	seedPolicy(t, ctx, conn)
	workspaceA := pgWorkspaceA
	inviteID := insertInvite(t, ctx, conn, &workspaceA, "racer@example.test", "race-token-hash", domain.InviteKindMember)

	// Independent stores over independent pools: two real connections, not two
	// goroutines sharing one.
	storeOne := storage.NewPGXInviteStore(testPool(t, ctx))
	storeTwo := storage.NewPGXInviteStore(testPool(t, ctx))
	accept := func(s *storage.PGXInviteStore) func() error {
		return func() error {
			_, err := s.AcceptInviteTx(ctx, "race-token-hash", "Racer", "Racer User", "argon2id-hash")
			return err
		}
	}

	errs := raceTwo(accept(storeOne), accept(storeTwo))

	assertExactlyOneWinner(t, errs, domain.ErrInvalidToken)
	assertCount(t, ctx, conn, 1, `SELECT count(*) FROM auth.users WHERE email = 'racer@example.test'`)
	assertCount(t, ctx, conn, 1, `SELECT count(*) FROM chat.workspace_members WHERE workspace_id = $1::uuid`, pgWorkspaceA)
	assertCount(t, ctx, conn, 1, `SELECT count(*) FROM auth.user_invites WHERE id = $1::uuid AND status = 'accepted' AND accepted_at IS NOT NULL`, inviteID)
	assertNoOpenTransactions(t, ctx, conn)
}

// Two concurrent acceptances of two *different* bootstrap invites in one
// workspace. Exactly one owner may result, and the loser must be refused —
// this is the property the workspace advisory lock exists for.
func TestAcceptInviteTx_ConcurrentBootstrapProducesOneOwner(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	conn := migratedDatabase(t, ctx)
	seedPolicy(t, ctx, conn)
	workspaceA := pgWorkspaceA
	insertInvite(t, ctx, conn, &workspaceA, "owner-one@example.test", "bootstrap-hash-1", domain.InviteKindBootstrapOwner)
	insertInvite(t, ctx, conn, &workspaceA, "owner-two@example.test", "bootstrap-hash-2", domain.InviteKindBootstrapOwner)

	storeOne := storage.NewPGXInviteStore(testPool(t, ctx))
	storeTwo := storage.NewPGXInviteStore(testPool(t, ctx))
	accept := func(s *storage.PGXInviteStore, hash, name string) func() error {
		return func() error {
			_, err := s.AcceptInviteTx(ctx, hash, name, name, "argon2id-hash")
			return err
		}
	}

	errs := raceTwo(
		accept(storeOne, "bootstrap-hash-1", "Owner One"),
		accept(storeTwo, "bootstrap-hash-2", "Owner Two"),
	)

	assertExactlyOneWinner(t, errs, domain.ErrInvalidToken)
	assertCount(t, ctx, conn, 1, `SELECT count(*) FROM chat.workspace_members WHERE workspace_id = $1::uuid AND role = 'owner'`, pgWorkspaceA)
	assertCount(t, ctx, conn, 0, `SELECT count(*) FROM chat.workspace_members WHERE workspace_id = $1::uuid AND role = 'member'`, pgWorkspaceA)
	// The loser's invite is either revoked by the winner or left unconsumed;
	// either way no bootstrap invite remains pending.
	assertCount(t, ctx, conn, 0, `SELECT count(*) FROM auth.user_invites WHERE workspace_id = $1::uuid AND invite_kind = 'bootstrap_owner' AND status = 'pending'`, pgWorkspaceA)
	assertCount(t, ctx, conn, 1, `SELECT count(*) FROM auth.user_invites WHERE workspace_id = $1::uuid AND invite_kind = 'bootstrap_owner' AND status = 'accepted'`, pgWorkspaceA)
	assertNoOpenTransactions(t, ctx, conn)

	// The window is closed, which is what the bootstrap route reads.
	store := storage.NewPGXInviteStore(testPool(t, ctx))
	state, err := store.BootstrapWorkspaceState(ctx, pgWorkspaceA)
	if err != nil {
		t.Fatalf("BootstrapWorkspaceState: %v", err)
	}
	hasAdmin := state.Initialized
	if !hasAdmin {
		t.Fatal("after a bootstrap acceptance the workspace must report an administrator")
	}
}

// Once the workspace has an owner, a bootstrap invite that somehow remained
// pending still cannot be accepted.
func TestAcceptInviteTx_BootstrapRefusedAfterInitialization(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	conn := migratedDatabase(t, ctx)
	seedPolicy(t, ctx, conn)
	workspaceA := pgWorkspaceA
	insertInvite(t, ctx, conn, &workspaceA, "owner-one@example.test", "bootstrap-hash-1", domain.InviteKindBootstrapOwner)

	store := storage.NewPGXInviteStore(testPool(t, ctx))
	if _, err := store.AcceptInviteTx(ctx, "bootstrap-hash-1", "Owner One", "Owner One", "argon2id-hash"); err != nil {
		t.Fatalf("first bootstrap acceptance: %v", err)
	}

	// A second bootstrap invite issued afterwards (which the service would
	// refuse to create, but the database can hold) must not be acceptable.
	insertInvite(t, ctx, conn, &workspaceA, "owner-two@example.test", "bootstrap-hash-2", domain.InviteKindBootstrapOwner)
	_, err := store.AcceptInviteTx(ctx, "bootstrap-hash-2", "Owner Two", "Owner Two", "argon2id-hash")
	if !errors.Is(err, domain.ErrInvalidToken) {
		t.Fatalf("expected ErrInvalidToken after initialization, got %v", err)
	}
	assertCount(t, ctx, conn, 1, `SELECT count(*) FROM chat.workspace_members WHERE workspace_id = $1::uuid AND role = 'owner'`, pgWorkspaceA)
	assertCount(t, ctx, conn, 0, `SELECT count(*) FROM auth.users WHERE email = 'owner-two@example.test'`)
}

// An ordinary invite keeps creating a plain member, in a workspace the
// bootstrap kind never touched.
func TestAcceptInviteTx_MemberInviteStillCreatesMember(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	conn := migratedDatabase(t, ctx)
	seedPolicy(t, ctx, conn)
	workspaceB := pgWorkspaceB
	insertInvite(t, ctx, conn, &workspaceB, "member@example.test", "member-hash", domain.InviteKindMember)

	store := storage.NewPGXInviteStore(testPool(t, ctx))
	if _, err := store.AcceptInviteTx(ctx, "member-hash", "Member", "Member User", "argon2id-hash"); err != nil {
		t.Fatalf("AcceptInviteTx: %v", err)
	}

	var role string
	if err := conn.QueryRow(ctx, `
		SELECT role FROM chat.workspace_members WHERE workspace_id = $1::uuid`, pgWorkspaceB).Scan(&role); err != nil {
		t.Fatalf("read membership: %v", err)
	}
	if role != "member" {
		t.Fatalf("an ordinary invite must confer 'member', got %q", role)
	}
	state, err := store.BootstrapWorkspaceState(ctx, pgWorkspaceB)
	if err != nil {
		t.Fatalf("BootstrapWorkspaceState: %v", err)
	}
	hasAdmin := state.Initialized
	if hasAdmin {
		t.Fatal("an ordinary invite must not close the bootstrap window")
	}
}
