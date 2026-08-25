package storage_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

// Group rename and self-leave under contention (issue #527, security review).
//
// Two properties, both of them lock interactions and therefore both against a
// real PostgreSQL:
//
//  1. a rename must be serialized with a revocation of the actor's *workspace*
//     membership, not merely ordered after a read of it;
//  2. two self-leaves must produce one departure and one domain refusal — never
//     a deadlock, which is what a shared lock upgraded to an exclusive one at
//     UPDATE time produces.
//
// Coordination is by lock state, never by sleeping: each test parks a
// transaction in a known blocked state and waits for the database itself to
// report the wait before releasing it.

// blockedBackendTimeout bounds every wait below.
//
// It is a watchdog, not a synchronisation mechanism: the tests proceed the
// instant the database reports the wait, and this only decides how long a
// *missing* wait takes to be reported. A missing wait is the failure — it means
// the two transactions were never serialized — so it must fail with that
// sentence rather than hang until the package timeout.
const blockedBackendTimeout = 20 * time.Second

// waitForBlockedBackends blocks until `want` backends of this database are
// waiting on a lock.
//
// The single-backend helper in channel_store_postgres_test.go is not enough for
// the two-leave test: it would return as soon as the first goroutine parked, and
// the point of that test is that *both* are queued before either is let go.
func waitForBlockedBackends(t *testing.T, pool *pgxpool.Pool, want int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), blockedBackendTimeout)
	defer cancel()
	for {
		var blocked int
		err := pool.QueryRow(ctx, `
			SELECT count(*) FROM pg_stat_activity
			WHERE datname = current_database()
			  AND wait_event_type = 'Lock'`).Scan(&blocked)
		if ctx.Err() != nil {
			t.Fatalf("fewer than %d backends ever blocked on a lock: the operations are not serialized", want)
		}
		if err != nil {
			t.Fatalf("poll for blocked backends: %v", err)
		}
		if blocked >= want {
			return
		}
	}
}

// failOnDeadlock rejects the one error this whole exercise exists to remove.
//
// A losing transaction must lose for a domain reason. PostgreSQL aborting it
// with 40P01 is not a refusal, it is the server breaking a tie between two
// transactions that asked for locks in an order it could not satisfy — and it
// surfaces as a 500.
func failOnDeadlock(t *testing.T, err error) {
	t.Helper()
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "40P01" {
		t.Fatalf("deadlock_detected (40P01): %v", err)
	}
}

// ── Rename versus workspace revocation: the revocation gets there first ──────
//
// The defect: the rename re-derived the actor's participation under a lock but
// read chat.workspace_members through an unlocked join, so a suspension that
// committed in between was observed too late — the rename went on to write using
// authority that no longer existed.
//
// With the membership row held FOR SHARE, the revoking UPDATE (FOR NO KEY
// UPDATE) and the rename cannot overlap. Here the revocation holds it first, so
// the rename parks on it, re-evaluates when the revocation commits, finds no
// active membership, and refuses. Nothing is written — which is also why no
// realtime event is published: the handler publishes only after the store
// returns successfully.
func TestPGXDMStoreRenameGroupRefusesAfterConcurrentWorkspaceRevocationPostgreSQL(t *testing.T) {
	pool := newChannelAuthzPool(t)
	seedConversationAdminFixtures(t, pool)
	store := storage.NewPGXDMStore(pool)

	revoke, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatalf("begin revocation: %v", err)
	}
	defer func() { _ = revoke.Rollback(context.Background()) }()
	// The real revocation shape: suspending a workspace membership is an UPDATE
	// of a non-key column, which takes FOR NO KEY UPDATE.
	if _, err := revoke.Exec(t.Context(), `
		UPDATE chat.workspace_members SET status = 'suspended'
		WHERE workspace_id = $1 AND user_id = $2`,
		chanWorkspace, chanMember,
	); err != nil {
		t.Fatalf("suspend workspace membership: %v", err)
	}

	renamed := make(chan error, 1)
	go func() {
		_, err := store.RenameGroupConversation(context.Background(), storage.RenameGroupInput{
			WorkspaceID:    chanWorkspace,
			ConversationID: adminGroupID,
			CallerID:       chanMember,
			Title:          "Piloto NChat",
		})
		renamed <- err
	}()

	// The rename is parked on the membership row the revocation holds. This is
	// the evidence that the two are serialized rather than merely racing.
	waitForBlockedBackends(t, pool, 1)

	if err := revoke.Commit(t.Context()); err != nil {
		t.Fatalf("commit revocation: %v", err)
	}

	err = <-renamed
	failOnDeadlock(t, err)
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("rename error = %v, want ErrForbidden after the actor lost workspace access", err)
	}
	if got := groupTitle(t, pool, adminGroupID); got != "Equipe" {
		t.Fatalf("title = %q, want the original — the rename must not have been persisted", got)
	}
	if events := conversationEvents(t, pool, "dm_conversation_id", adminGroupID); len(events) != 0 {
		t.Fatalf("events = %v, want none: a refused rename announces nothing", events)
	}
}

// ── Rename versus workspace revocation: the rename gets there first ──────────
//
// The inverse order, and the reason this is a serialization rather than a
// permanent failure under contention: while the rename holds the membership FOR
// SHARE the revocation waits, the rename commits, and the revocation proceeds
// afterwards. No deadlock, and the rename that held authority at the
// serialization point survives.
//
// The store opens and commits its own transaction, so the window is reproduced
// here by taking the very locks PGXDMStore.RenameGroupConversation takes, in the
// order it takes them, and performing the same UPDATE inside it. The first test
// above is what exercises the real store path end to end; what this one has to
// hold still is the window itself.
func TestPGXDMStoreRenameGroupHoldsOffWorkspaceRevocationPostgreSQL(t *testing.T) {
	pool := newChannelAuthzPool(t)
	seedConversationAdminFixtures(t, pool)

	rename, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatalf("begin rename: %v", err)
	}
	defer func() { _ = rename.Rollback(context.Background()) }()

	// Step 1: the conversation row, exclusively — the rename writes it.
	var lockedID, title string
	if err := rename.QueryRow(t.Context(), `
		SELECT dc.id::text, COALESCE(dc.title, '')
		FROM chat.dm_conversations dc
		JOIN chat.workspaces w ON w.id = dc.workspace_id AND w.status = 'active'
		WHERE dc.id = $1::uuid AND dc.workspace_id = $2::uuid
		  AND dc.status = 'active' AND dc.type = 'group'
		FOR UPDATE OF dc`,
		adminGroupID, chanWorkspace,
	).Scan(&lockedID, &title); err != nil {
		t.Fatalf("lock conversation: %v", err)
	}
	// Step 2: the actor's participation.
	var participates bool
	if err := rename.QueryRow(t.Context(), `
		SELECT true FROM chat.dm_members dm
		WHERE dm.conversation_id = $1::uuid AND dm.user_id = $2::uuid AND dm.status = 'active'
		FOR SHARE OF dm`,
		adminGroupID, chanMember,
	).Scan(&participates); err != nil {
		t.Fatalf("lock participation: %v", err)
	}
	// Step 3: the actor's workspace membership — the row this finding is about.
	var authorized bool
	if err := rename.QueryRow(t.Context(), `
		SELECT true FROM chat.workspace_members wm
		JOIN chat.workspaces w ON w.id = wm.workspace_id AND w.status = 'active'
		WHERE wm.workspace_id = $1::uuid AND wm.user_id = $2::uuid AND wm.status = 'active'
		FOR SHARE OF wm`,
		chanWorkspace, chanMember,
	).Scan(&authorized); err != nil {
		t.Fatalf("lock workspace membership: %v", err)
	}

	revoked := make(chan error, 1)
	go func() {
		_, err := pool.Exec(context.Background(), `
			UPDATE chat.workspace_members SET status = 'suspended'
			WHERE workspace_id = $1 AND user_id = $2`,
			chanWorkspace, chanMember)
		revoked <- err
	}()
	waitForBlockedBackends(t, pool, 1)

	// Step 4: the write, still inside the authorized window.
	if _, err := rename.Exec(t.Context(), `
		UPDATE chat.dm_conversations SET title = $3, updated_at = now()
		WHERE id = $1::uuid AND workspace_id = $2::uuid AND status = 'active' AND type = 'group'`,
		adminGroupID, chanWorkspace, "Piloto NChat",
	); err != nil {
		t.Fatalf("rename group: %v", err)
	}
	if err := rename.Commit(t.Context()); err != nil {
		t.Fatalf("commit rename: %v", err)
	}

	err = <-revoked
	failOnDeadlock(t, err)
	if err != nil {
		t.Fatalf("revocation failed after the rename released its lock: %v", err)
	}
	if got := groupTitle(t, pool, adminGroupID); got != "Piloto NChat" {
		t.Fatalf("title = %q, want the rename that held authority to have survived", got)
	}
	var status string
	if err := pool.QueryRow(t.Context(), `
		SELECT status FROM chat.workspace_members WHERE workspace_id = $1 AND user_id = $2`,
		chanWorkspace, chanMember,
	).Scan(&status); err != nil {
		t.Fatalf("read membership: %v", err)
	}
	if status != "suspended" {
		t.Fatalf("membership status = %q, want the revocation to have completed afterwards", status)
	}
}

// ── Two self-leaves at once ─────────────────────────────────────────────────
//
// The defect: both transactions took the actor's chat.dm_members row FOR SHARE,
// both passed the check, and both then needed the exclusive lock the other was
// holding to run their UPDATE. PostgreSQL breaks that tie by aborting one with
// 40P01 — a 500, not a refusal.
//
// With the row taken FOR UPDATE from the start the second transaction simply
// queues. When the winner commits, READ COMMITTED re-evaluates the loser's
// predicate against the new row version, `status = 'active'` no longer holds,
// and the loser gets the same ErrForbidden a late second leave has always
// produced.
//
// Both are parked behind a third transaction holding the conversation row, so
// they are provably in flight together rather than incidentally overlapping.
func TestPGXDMStoreConcurrentLeaveGroupPostgreSQL(t *testing.T) {
	pool := newChannelAuthzPool(t)
	seedConversationAdminFixtures(t, pool)
	store := storage.NewPGXDMStore(pool)

	gate, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatalf("begin gate: %v", err)
	}
	defer func() { _ = gate.Rollback(context.Background()) }()
	var gatedID string
	if err := gate.QueryRow(t.Context(),
		`SELECT id::text FROM chat.dm_conversations WHERE id = $1::uuid FOR UPDATE`, adminGroupID,
	).Scan(&gatedID); err != nil {
		t.Fatalf("gate the conversation: %v", err)
	}

	results := make(chan error, 2)
	for range 2 {
		go func() {
			_, err := store.LeaveGroupConversation(
				context.Background(), chanWorkspace, adminGroupID, chanMember)
			results <- err
		}()
	}
	// Both leaves are queued on the conversation row before either is released,
	// so the contention below is on the membership row and nothing else.
	waitForBlockedBackends(t, pool, 2)
	if err := gate.Rollback(t.Context()); err != nil {
		t.Fatalf("release gate: %v", err)
	}

	var succeeded, refused int
	for range 2 {
		err := <-results
		failOnDeadlock(t, err)
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, domain.ErrForbidden):
			refused++
		default:
			t.Fatalf("unexpected leave error: %v", err)
		}
	}
	if succeeded != 1 || refused != 1 {
		t.Fatalf("succeeded = %d, refused = %d, want exactly one of each", succeeded, refused)
	}

	var status string
	var leftAt *string
	if err := pool.QueryRow(t.Context(), `
		SELECT status, left_at::text FROM chat.dm_members
		WHERE conversation_id = $1 AND user_id = $2`,
		adminGroupID, chanMember,
	).Scan(&status, &leftAt); err != nil {
		t.Fatalf("read membership: %v", err)
	}
	if status != "left" {
		t.Fatalf("status = %q, want left", status)
	}
	if leftAt == nil {
		t.Fatal("left_at is NULL, want the departure instant")
	}
	// The loser wrote nothing at all: one departure, one system message.
	events := conversationEvents(t, pool, "dm_conversation_id", adminGroupID)
	if len(events) != 1 || events[0] != string(domain.ConversationEventMemberLeft) {
		t.Fatalf("events = %v, want exactly one member-left event", events)
	}
}

// A participant whose workspace membership is already gone is refused outright,
// with no concurrency involved: the membership row this operation now holds is
// also read under the same predicate the join used before, so the sequential
// answer is unchanged.
func TestPGXDMStoreGroupMutationsRequireActiveWorkspaceMembershipPostgreSQL(t *testing.T) {
	pool := newChannelAuthzPool(t)
	seedConversationAdminFixtures(t, pool)
	store := storage.NewPGXDMStore(pool)

	if _, err := pool.Exec(t.Context(), `
		UPDATE chat.workspace_members SET status = 'suspended'
		WHERE workspace_id = $1 AND user_id = $2`,
		chanWorkspace, chanMember,
	); err != nil {
		t.Fatalf("suspend workspace membership: %v", err)
	}

	if _, err := store.RenameGroupConversation(t.Context(), storage.RenameGroupInput{
		WorkspaceID:    chanWorkspace,
		ConversationID: adminGroupID,
		CallerID:       chanMember,
		Title:          "Piloto NChat",
	}); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("rename error = %v, want ErrForbidden for a suspended workspace member", err)
	}
	if _, err := store.LeaveGroupConversation(
		t.Context(), chanWorkspace, adminGroupID, chanMember,
	); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("leave error = %v, want ErrForbidden for a suspended workspace member", err)
	}
	if got := groupTitle(t, pool, adminGroupID); got != "Equipe" {
		t.Fatalf("title = %q, want it untouched", got)
	}
	if events := conversationEvents(t, pool, "dm_conversation_id", adminGroupID); len(events) != 0 {
		t.Fatalf("events = %v, want none", events)
	}
}
