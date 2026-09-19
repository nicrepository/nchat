package storage_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

// Issue #398 identifiers. Distinct prefixes from the sibling fixtures so a
// leftover row from another test can never satisfy an assertion here.
const (
	amWS       = "f1000000-0000-4000-8000-000000000001"
	amOtherWS  = "f2000000-0000-4000-8000-000000000001"
	amGeneral  = "f1000000-0000-4000-8000-000000000020"
	amGeneralB = "f2000000-0000-4000-8000-000000000021"
	amPrivate  = "f1000000-0000-4000-8000-000000000030"
	amArchived = "f1000000-0000-4000-8000-000000000031"
	amForeignC = "f2000000-0000-4000-8000-000000000032"
	amGroup    = "f1000000-0000-4000-8000-000000000040"

	amAdmin     = "f1000000-0000-4000-8000-000000000009"
	amActive1   = "f1000000-0000-4000-8000-00000000000b"
	amActive2   = "f1000000-0000-4000-8000-00000000000c"
	amActive3   = "f1000000-0000-4000-8000-00000000001a"
	amSuspended = "f1000000-0000-4000-8000-00000000000d"
	amDeleted   = "f1000000-0000-4000-8000-00000000000e"
	amForeignU  = "f1000000-0000-4000-8000-00000000000f"
)

// addMembersPostgres prepares a real schema with one workspace holding an active
// private channel, an archived channel and a group conversation, plus users in
// every eligibility state. Membership alone is never enough: the suspended and
// deleted users below are active *workspace members* whose accounts are not.
func addMembersPostgres(t *testing.T) (*pgxpool.Pool, context.Context) {
	t.Helper()
	dsn := os.Getenv("CHAT_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("CHAT_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)

	var databaseName string
	if err := pool.QueryRow(ctx, `SELECT current_database()`).Scan(&databaseName); err != nil {
		t.Fatalf("read current database: %v", err)
	}
	if !strings.HasSuffix(databaseName, "_test") {
		t.Fatalf("refusing destructive test against non-test database %q", databaseName)
	}
	if _, err := pool.Exec(ctx, `DROP SCHEMA IF EXISTS chat CASCADE`); err != nil {
		t.Fatalf("reset chat schema: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DROP SCHEMA IF EXISTS chat CASCADE`) })
	if _, err := pool.Exec(ctx, `
		CREATE SCHEMA IF NOT EXISTS auth;
		CREATE TABLE IF NOT EXISTS auth.users (
			id UUID PRIMARY KEY,
			email TEXT NOT NULL DEFAULT '',
			display_name TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT 'active',
			deleted_at TIMESTAMPTZ
		)`); err != nil {
		t.Fatalf("prepare auth schema: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		ALTER TABLE auth.users
			ADD COLUMN IF NOT EXISTS status TEXT NOT NULL DEFAULT 'active',
			ADD COLUMN IF NOT EXISTS deleted_at TIMESTAMPTZ,
			ADD COLUMN IF NOT EXISTS email TEXT NOT NULL DEFAULT '',
			ADD COLUMN IF NOT EXISTS display_name TEXT NOT NULL DEFAULT '',
			ADD COLUMN IF NOT EXISTS full_name TEXT,
			ADD COLUMN IF NOT EXISTS avatar_url TEXT`); err != nil {
		t.Fatalf("align auth.users columns: %v", err)
	}
	if _, err := pool.Exec(ctx, readAllChatUpMigrations(t)); err != nil {
		t.Fatalf("apply chat migrations: %v", err)
	}

	seeds := []struct {
		sql  string
		args []any
	}{
		{sql: `INSERT INTO auth.users (id, email, display_name, status, deleted_at) VALUES
			($1, 'a1@example.test', 'Active One',   'active',    NULL),
			($2, 'a2@example.test', 'Active Two',   'active',    NULL),
			($6, 'a3@example.test', 'Active Three', 'active',    NULL),
			($3, 'su@example.test', 'Suspended',    'suspended', NULL),
			($4, 'de@example.test', 'Deleted',      'active',    now()),
			($5, 'fo@example.test', 'Foreign',      'active',    NULL),
			($7, 'ad@example.test', 'Admin',        'active',    NULL)
			ON CONFLICT (id) DO UPDATE SET
				status = EXCLUDED.status, deleted_at = EXCLUDED.deleted_at`,
			args: []any{amActive1, amActive2, amSuspended, amDeleted, amForeignU, amActive3, amAdmin}},
		{sql: `INSERT INTO chat.workspaces (id, slug, name) VALUES
			($1, 'add-members-a', 'A'), ($2, 'add-members-b', 'B')`,
			args: []any{amWS, amOtherWS}},
		{sql: `INSERT INTO chat.channels (id, workspace_id, slug, display_name, type, is_general, status) VALUES
			($1, $5, 'geral',    'geral',    'public',  true,  'active'),
			($2, $6, 'geral',    'geral',    'public',  true,  'active'),
			($3, $5, 'privado',  'Privado',  'private', false, 'active'),
			($4, $5, 'arquivado','Arquivado','private', false, 'archived'),
			($7, $6, 'outro',    'Outro',    'private', false, 'active')`,
			args: []any{amGeneral, amGeneralB, amPrivate, amArchived, amWS, amOtherWS, amForeignC}},
		{sql: `INSERT INTO chat.workspace_members (workspace_id, user_id, role, status) VALUES
			($1, $3, 'member', 'active'), ($1, $4, 'member', 'active'),
			($1, $5, 'member', 'active'), ($1, $6, 'member', 'active'),
			($1, $8, 'member', 'active'), ($1, $9, 'admin',  'active'),
			($2, $7, 'member', 'active')`,
			args: []any{amWS, amOtherWS, amActive1, amActive2, amSuspended, amDeleted, amForeignU, amActive3, amAdmin}},
		{sql: `INSERT INTO chat.dm_conversations (id, workspace_id, type, status, created_by) VALUES
			($1, $2, 'group', 'active', $3)`,
			args: []any{amGroup, amWS, amActive1}},
		{sql: `INSERT INTO chat.dm_members (conversation_id, user_id, role, status) VALUES
			($1, $2, 'member', 'active')`,
			args: []any{amGroup, amActive1}},
	}
	// One transaction for every seed: the general-channel invariant is enforced by
	// deferred constraint triggers, so a workspace inserted in its own transaction
	// would fail at commit for not yet having its #geral.
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin seed: %v", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	for _, seed := range seeds {
		if _, err := tx.Exec(ctx, seed.sql, seed.args...); err != nil {
			t.Fatalf("seed add-members fixture: %v", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit seed: %v", err)
	}
	return pool, ctx
}

func countChannelMembers(t *testing.T, pool *pgxpool.Pool, ctx context.Context, channelID string) int {
	t.Helper()
	var total int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM chat.channel_members WHERE channel_id = $1`, channelID,
	).Scan(&total); err != nil {
		t.Fatalf("count channel members: %v", err)
	}
	return total
}

// ── Channel members ─────────────────────────────────────────────────────────

func TestPGXAddChannelMembersPostgreSQL(t *testing.T) {
	pool, ctx := addMembersPostgres(t)
	store := storage.NewPGXMemberStore(pool)

	t.Run("adds eligible members and reports the real total", func(t *testing.T) {
		result, err := store.AddChannelMembers(ctx, amWS, amPrivate, amAdmin, []string{amActive1, amActive2})
		if err != nil {
			t.Fatalf("AddChannelMembers: %v", err)
		}
		if result.Added != 2 || result.AlreadyMembers != 0 || result.TotalCount != 2 {
			t.Fatalf("result = %+v, want 2/0/2", result)
		}
		if got := countChannelMembers(t, pool, ctx, amPrivate); got != 2 {
			t.Fatalf("persisted rows = %d, want 2", got)
		}
	})

	// The PK (channel_id, user_id) plus ON CONFLICT DO NOTHING is what makes the
	// retry safe. No unique violation surfaces and no second row appears.
	t.Run("repeating the same request is idempotent", func(t *testing.T) {
		result, err := store.AddChannelMembers(ctx, amWS, amPrivate, amAdmin, []string{amActive1, amActive2})
		if err != nil {
			t.Fatalf("retry: %v", err)
		}
		if result.Added != 0 || result.AlreadyMembers != 2 {
			t.Fatalf("retry result = %+v, want 0 added / 2 already", result)
		}
		if got := countChannelMembers(t, pool, ctx, amPrivate); got != 2 {
			t.Fatalf("rows after retry = %d, want 2", got)
		}
	})

	t.Run("refuses ineligible users and writes nothing", func(t *testing.T) {
		before := countChannelMembers(t, pool, ctx, amPrivate)
		for name, userID := range map[string]string{
			"suspended workspace member":  amSuspended,
			"deleted account":             amDeleted,
			"member of another workspace": amForeignU,
			"user that does not exist":    "f1000000-0000-4000-8000-0000000000ff",
		} {
			t.Run(name, func(t *testing.T) {
				// Paired with an eligible user, so a partial write would be visible.
				_, err := store.AddChannelMembers(ctx, amWS, amPrivate, amAdmin, []string{userID, amActive1})
				if !errors.Is(err, domain.ErrForbidden) {
					t.Fatalf("err = %v, want ErrForbidden", err)
				}
			})
		}
		if got := countChannelMembers(t, pool, ctx, amPrivate); got != before {
			t.Fatalf("rows changed from %d to %d — the rollback did not hold", before, got)
		}
	})

	// Tenant isolation lives in the SQL join, not in a Go filter: the channel is
	// selected by (id, workspace_id) together, so a foreign channel UUID selects
	// nothing regardless of what the caller passed.
	t.Run("refuses a channel from another workspace", func(t *testing.T) {
		_, err := store.AddChannelMembers(ctx, amWS, amForeignC, amAdmin, []string{amActive1})
		if !errors.Is(err, domain.ErrForbidden) {
			t.Fatalf("err = %v, want ErrForbidden", err)
		}
		if got := countChannelMembers(t, pool, ctx, amForeignC); got != 0 {
			t.Fatalf("cross-workspace rows written: %d", got)
		}
	})

	t.Run("refuses an archived channel", func(t *testing.T) {
		_, err := store.AddChannelMembers(ctx, amWS, amArchived, amAdmin, []string{amActive1})
		if !errors.Is(err, domain.ErrForbidden) {
			t.Fatalf("err = %v, want ErrForbidden", err)
		}
		if got := countChannelMembers(t, pool, ctx, amArchived); got != 0 {
			t.Fatalf("rows written to an archived channel: %d", got)
		}
	})

	t.Run("refuses an empty list without touching the database", func(t *testing.T) {
		_, err := store.AddChannelMembers(ctx, amWS, amPrivate, amAdmin, nil)
		if !errors.Is(err, domain.ErrNoMembersRequested) {
			t.Fatalf("err = %v, want ErrNoMembersRequested", err)
		}
	})
}

// Two managers adding the same person at the same moment must converge on one
// row. This is the case a check-then-insert loses: both would observe "not a
// member" and one would then raise a unique violation.
//
// No sleeps: the goroutines are released together by closing a channel and
// joined by the WaitGroup, so the test is deterministic about what it asserts
// (one row, no error) without pretending to control the interleaving.
func TestPGXAddChannelMembersConcurrentPostgreSQL(t *testing.T) {
	pool, ctx := addMembersPostgres(t)
	store := storage.NewPGXMemberStore(pool)

	const writers = 8
	start := make(chan struct{})
	errs := make([]error, writers)
	added := make([]int, writers)

	var wg sync.WaitGroup
	wg.Add(writers)
	for i := 0; i < writers; i++ {
		go func(i int) {
			defer wg.Done()
			<-start
			result, err := store.AddChannelMembers(ctx, amWS, amPrivate, amAdmin, []string{amActive1})
			errs[i], added[i] = err, result.Added
		}(i)
	}
	close(start)
	wg.Wait()

	totalAdded := 0
	for i, err := range errs {
		if err != nil {
			t.Fatalf("writer %d failed: %v", i, err)
		}
		totalAdded += added[i]
	}
	// Exactly one writer may claim the insert; the rest see it as pre-existing.
	if totalAdded != 1 {
		t.Fatalf("sum of Added = %d, want exactly 1", totalAdded)
	}
	if got := countChannelMembers(t, pool, ctx, amPrivate); got != 1 {
		t.Fatalf("membership rows = %d, want exactly 1", got)
	}
}

// ── Group participants ──────────────────────────────────────────────────────

func countDMParticipants(t *testing.T, pool *pgxpool.Pool, ctx context.Context, conversationID string) int {
	t.Helper()
	var total int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM chat.dm_members WHERE conversation_id = $1 AND status = 'active'`,
		conversationID,
	).Scan(&total); err != nil {
		t.Fatalf("count dm participants: %v", err)
	}
	return total
}

func TestPGXAddGroupParticipantsPostgreSQL(t *testing.T) {
	pool, ctx := addMembersPostgres(t)
	store := storage.NewPGXDMStore(pool)

	input := func(userIDs ...string) storage.AddGroupParticipantsInput {
		return storage.AddGroupParticipantsInput{
			WorkspaceID: amWS, ConversationID: amGroup, CallerID: amActive1,
			UserIDs: userIDs,
		}
	}

	t.Run("adds an eligible participant", func(t *testing.T) {
		result, err := store.AddGroupParticipants(ctx, input(amActive2))
		if err != nil {
			t.Fatalf("AddGroupParticipants: %v", err)
		}
		if result.Added != 1 || result.TotalCount != 2 {
			t.Fatalf("result = %+v, want 1 added / total 2", result)
		}
	})

	t.Run("repeating the request adds nobody", func(t *testing.T) {
		result, err := store.AddGroupParticipants(ctx, input(amActive2))
		if err != nil {
			t.Fatalf("retry: %v", err)
		}
		if result.Added != 0 || result.AlreadyMembers != 1 {
			t.Fatalf("retry result = %+v, want 0 added / 1 already", result)
		}
		if got := countDMParticipants(t, pool, ctx, amGroup); got != 2 {
			t.Fatalf("participants after retry = %d, want 2", got)
		}
	})

	t.Run("refuses ineligible participants and rolls back", func(t *testing.T) {
		before := countDMParticipants(t, pool, ctx, amGroup)
		for name, userID := range map[string]string{
			"suspended":       amSuspended,
			"deleted account": amDeleted,
			"other workspace": amForeignU,
		} {
			t.Run(name, func(t *testing.T) {
				if _, err := store.AddGroupParticipants(ctx, input(userID)); !errors.Is(err, domain.ErrForbidden) {
					t.Fatalf("err = %v, want ErrForbidden", err)
				}
			})
		}
		if got := countDMParticipants(t, pool, ctx, amGroup); got != before {
			t.Fatalf("participants changed from %d to %d", before, got)
		}
	})

	// The conversation is located by (id, workspace_id) under the lock, so a
	// foreign conversation UUID finds no row at all.
	t.Run("refuses a conversation from another workspace", func(t *testing.T) {
		in := input(amActive1)
		in.WorkspaceID = amOtherWS
		if _, err := store.AddGroupParticipants(ctx, in); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound", err)
		}
	})

	// Groups have no fixed capacity, so a batch is bounded only by how many IDs
	// one request may carry. Repeating an existing participant is a no-op and a
	// duplicate ID costs nothing; neither can be refused for "capacity".
	t.Run("existing participants and duplicates are no-ops, never conflicts", func(t *testing.T) {
		before := countDMParticipants(t, pool, ctx, amGroup)

		// A pure retry of everyone already inside.
		retry, err := store.AddGroupParticipants(ctx, input(amActive1, amActive2))
		if err != nil {
			t.Fatalf("a retry must not fail: %v", err)
		}
		if retry.Added != 0 {
			t.Fatalf("Added = %d, want 0", retry.Added)
		}

		// A mixed batch adds only the newcomer.
		mixed, err := store.AddGroupParticipants(ctx, input(amActive2, amActive3))
		if err != nil {
			t.Fatalf("mixed batch: %v", err)
		}
		if mixed.Added != 1 || len(mixed.AddedUserIDs) != 1 || mixed.AddedUserIDs[0] != amActive3 {
			t.Fatalf("mixed batch result = %+v, want only %s added", mixed, amActive3)
		}
		if got := countDMParticipants(t, pool, ctx, amGroup); got != before+1 {
			t.Fatalf("participants = %d, want %d", got, before+1)
		}
	})

	t.Run("refuses a direct conversation", func(t *testing.T) {
		const directID = "f1000000-0000-4000-8000-000000000050"
		if _, err := pool.Exec(ctx, `
			INSERT INTO chat.dm_conversations (id, workspace_id, type, status, created_by, direct_pair_key)
			VALUES ($1, $2, 'direct', 'active', $3, 'pair-key-398')`,
			directID, amWS, amActive1); err != nil {
			t.Fatalf("seed direct conversation: %v", err)
		}
		in := input(amActive2)
		in.ConversationID = directID

		// A 1:1 has no row matching type = 'group', so the lock finds nothing —
		// the refusal is structural rather than a Go-side type comparison.
		if _, err := store.AddGroupParticipants(ctx, in); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound", err)
		}
		if got := countDMParticipants(t, pool, ctx, directID); got != 0 {
			t.Fatalf("participants written to a 1:1 conversation: %d", got)
		}
	})

	t.Run("refuses an archived conversation", func(t *testing.T) {
		if _, err := pool.Exec(ctx,
			`UPDATE chat.dm_conversations SET status = 'archived' WHERE id = $1`, amGroup,
		); err != nil {
			t.Fatalf("archive conversation: %v", err)
		}
		t.Cleanup(func() {
			_, _ = pool.Exec(context.Background(),
				`UPDATE chat.dm_conversations SET status = 'active' WHERE id = $1`, amGroup)
		})

		if _, err := store.AddGroupParticipants(ctx, input(amActive1)); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound", err)
		}
	})
}

// concurrentAdds releases every caller at once and collects what each one
// reported. The barrier is a closed channel and the join is a WaitGroup: no
// sleeps, and nothing pretends to control the interleaving. What is asserted is
// the invariant that must hold whatever the interleaving turns out to be.
func concurrentAdds(
	t *testing.T, store *storage.PGXDMStore, ctx context.Context, writers int, userIDs []string,
) []storage.AddMembersResult {
	t.Helper()
	start := make(chan struct{})
	results := make([]storage.AddMembersResult, writers)
	errs := make([]error, writers)

	var wg sync.WaitGroup
	wg.Add(writers)
	for i := 0; i < writers; i++ {
		go func(i int) {
			defer wg.Done()
			<-start
			results[i], errs[i] = store.AddGroupParticipants(ctx, storage.AddGroupParticipantsInput{
				WorkspaceID: amWS, ConversationID: amGroup, CallerID: amActive1,
				UserIDs: userIDs,
			})
		}(i)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("writer %d failed: %v", i, err)
		}
	}
	return results
}

// claimsOf returns how many times each user ID was reported as newly added
// across a set of concurrent results, and checks the per-result invariant that
// Added is exactly the length of the list it came with.
func claimsOf(t *testing.T, results []storage.AddMembersResult) map[string]int {
	t.Helper()
	claims := map[string]int{}
	for i, result := range results {
		if result.Added != len(result.AddedUserIDs) {
			t.Fatalf("writer %d: Added=%d disagrees with AddedUserIDs=%v",
				i, result.Added, result.AddedUserIDs)
		}
		for _, userID := range result.AddedUserIDs {
			claims[userID]++
		}
	}
	return claims
}

func activeMembershipRows(t *testing.T, pool *pgxpool.Pool, ctx context.Context, userID string) (total, active int) {
	t.Helper()
	if err := pool.QueryRow(ctx, `
		SELECT count(*), count(*) FILTER (WHERE status = 'active')
		FROM chat.dm_members
		WHERE conversation_id = $1 AND user_id = $2`, amGroup, userID,
	).Scan(&total, &active); err != nil {
		t.Fatalf("count membership rows: %v", err)
	}
	return total, active
}

// The case the count-before/count-after derivation loses.
//
// Without a ceiling there is no capacity to contend for, but "who did this call
// add" still has exactly one correct answer under concurrency: several writers
// adding the *same* person must produce one membership and exactly one claim
// between them. A pre-write read cannot give that — every writer would observe
// the person as absent and every one would claim the addition, which downstream
// becomes one "you were added" signal per writer for a single membership.
//
// Deriving it from the write's own RETURNING is what fixes it, and this is the
// test that says so.
func TestPGXAddGroupParticipantsConcurrentSameUserPostgreSQL(t *testing.T) {
	pool, ctx := addMembersPostgres(t)
	store := storage.NewPGXDMStore(pool)

	results := concurrentAdds(t, store, ctx, 6, []string{amActive2})

	totalAdded := 0
	for _, result := range results {
		totalAdded += result.Added
	}
	if totalAdded != 1 {
		t.Fatalf("sum of Added = %d, want exactly 1", totalAdded)
	}
	// The list must agree with the count, and name the user exactly once across
	// every writer — this is what the user-scoped fan-out is built from.
	if claims := claimsOf(t, results); claims[amActive2] != 1 {
		t.Fatalf("%s was claimed as added %d time(s) across writers, want 1",
			amActive2, claims[amActive2])
	}
	total, active := activeMembershipRows(t, pool, ctx, amActive2)
	if total != 1 || active != 1 {
		t.Fatalf("membership rows = %d (%d active), want exactly 1 active", total, active)
	}
}

// A participant who left and is being re-added concurrently: the reactivation is
// a single state transition, so only the transaction that performs it may report
// it. The others find the row already active and change nothing.
func TestPGXAddGroupParticipantsConcurrentReactivationPostgreSQL(t *testing.T) {
	pool, ctx := addMembersPostgres(t)
	store := storage.NewPGXDMStore(pool)

	// A former participant, currently out.
	if _, err := pool.Exec(ctx, `
		INSERT INTO chat.dm_members (conversation_id, user_id, role, status, left_at)
		VALUES ($1, $2, 'member', 'left', now())`, amGroup, amActive2); err != nil {
		t.Fatalf("seed departed participant: %v", err)
	}

	results := concurrentAdds(t, store, ctx, 6, []string{amActive2})

	totalAdded := 0
	for _, result := range results {
		totalAdded += result.Added
	}
	if totalAdded != 1 {
		t.Fatalf("sum of Added = %d for one reactivation, want exactly 1", totalAdded)
	}
	if claims := claimsOf(t, results); claims[amActive2] != 1 {
		t.Fatalf("%s was claimed %d time(s), want 1", amActive2, claims[amActive2])
	}
	total, active := activeMembershipRows(t, pool, ctx, amActive2)
	if total != 1 || active != 1 {
		t.Fatalf("membership rows = %d (%d active), want one reactivated row", total, active)
	}
	// The transition really happened: a reactivated row must not keep left_at.
	var leftAtSet bool
	if err := pool.QueryRow(ctx, `
		SELECT left_at IS NOT NULL FROM chat.dm_members
		WHERE conversation_id = $1 AND user_id = $2`, amGroup, amActive2,
	).Scan(&leftAtSet); err != nil {
		t.Fatalf("read left_at: %v", err)
	}
	if leftAtSet {
		t.Fatal("a reactivated participant still carries left_at")
	}
}

// Overlapping batches: each ID is claimed once in total, by whichever writer's
// statement actually created it, and never by both.
func TestPGXAddGroupParticipantsConcurrentOverlappingBatchesPostgreSQL(t *testing.T) {
	pool, ctx := addMembersPostgres(t)
	store := storage.NewPGXDMStore(pool)

	start := make(chan struct{})
	results := make([]storage.AddMembersResult, 2)
	errs := make([]error, 2)
	// amActive2 is in both batches; amActive3 and amAdmin in one each.
	batches := [][]string{{amActive2, amActive3}, {amActive2, amAdmin}}

	var wg sync.WaitGroup
	wg.Add(2)
	for i, batch := range batches {
		go func(i int, batch []string) {
			defer wg.Done()
			<-start
			results[i], errs[i] = store.AddGroupParticipants(ctx, storage.AddGroupParticipantsInput{
				WorkspaceID: amWS, ConversationID: amGroup, CallerID: amActive1,
				UserIDs: batch,
			})
		}(i, batch)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("writer %d failed: %v", i, err)
		}
	}
	claims := claimsOf(t, results)
	for _, userID := range []string{amActive2, amActive3, amAdmin} {
		if claims[userID] != 1 {
			t.Fatalf("%s was claimed %d time(s) across the two batches, want exactly 1",
				userID, claims[userID])
		}
	}
	// Nobody else may appear: a claim is only ever a row this write created.
	if len(claims) != 3 {
		t.Fatalf("claims = %v, want exactly the three requested users", claims)
	}
	// The seeded participant plus the three added.
	if got := countDMParticipants(t, pool, ctx, amGroup); got != 4 {
		t.Fatalf("participants = %d, want 4", got)
	}
}

// A sequential retry is the ordinary double click. It must add nobody, claim
// nobody, and leave the conversation exactly as it was — which is what stops it
// from producing a second round of events.
func TestPGXAddGroupParticipantsSequentialRetryPostgreSQL(t *testing.T) {
	pool, ctx := addMembersPostgres(t)
	store := storage.NewPGXDMStore(pool)

	input := storage.AddGroupParticipantsInput{
		WorkspaceID: amWS, ConversationID: amGroup, CallerID: amActive1,
		UserIDs: []string{amActive2, amActive3},
	}

	first, err := store.AddGroupParticipants(ctx, input)
	if err != nil {
		t.Fatalf("first add: %v", err)
	}
	if first.Added != 2 || len(first.AddedUserIDs) != 2 {
		t.Fatalf("first add = %+v, want 2 added", first)
	}
	before := countDMParticipants(t, pool, ctx, amGroup)

	retry, err := store.AddGroupParticipants(ctx, input)
	if err != nil {
		t.Fatalf("retry must not fail: %v", err)
	}
	if retry.Added != 0 || len(retry.AddedUserIDs) != 0 {
		t.Fatalf("retry = %+v, want no addition and no recipients", retry)
	}
	if retry.AlreadyMembers != 2 {
		t.Fatalf("AlreadyMembers = %d, want 2", retry.AlreadyMembers)
	}
	if retry.TotalCount != before {
		t.Fatalf("TotalCount = %d, want the unchanged %d", retry.TotalCount, before)
	}
	if got := countDMParticipants(t, pool, ctx, amGroup); got != before {
		t.Fatalf("participants changed from %d to %d on a retry", before, got)
	}
}

// Different users added concurrently must all succeed: there is no shared
// capacity for them to compete over.
func TestPGXAddGroupParticipantsConcurrentDifferentUsersPostgreSQL(t *testing.T) {
	pool, ctx := addMembersPostgres(t)
	store := storage.NewPGXDMStore(pool)

	candidates := []string{amActive2, amActive3, amAdmin}
	start := make(chan struct{})
	errs := make([]error, len(candidates))

	var wg sync.WaitGroup
	wg.Add(len(candidates))
	for i, userID := range candidates {
		go func(i int, userID string) {
			defer wg.Done()
			<-start
			_, errs[i] = store.AddGroupParticipants(ctx, storage.AddGroupParticipantsInput{
				WorkspaceID: amWS, ConversationID: amGroup, CallerID: amActive1,
				UserIDs: []string{userID},
			})
		}(i, userID)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("writer %d failed with no capacity to contend for: %v", i, err)
		}
	}
	// The seeded participant plus all three.
	if got := countDMParticipants(t, pool, ctx, amGroup); got != 4 {
		t.Fatalf("participants = %d, want 4", got)
	}
}

// ── Contextual candidate search (issue #398) ────────────────────────────────

func candidateIDs(t *testing.T, candidates []domain.DMCandidate) []string {
	t.Helper()
	ids := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		ids = append(ids, candidate.UserID)
	}
	return ids
}

func containsID(ids []string, want string) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}

// The defect this replaces: the panel's member preview is presence-filtered, so
// an *offline* current member was invisible to the client-side exclusion and
// came back as selectable. The SQL knows nothing about presence.
func TestPGXSearchChannelMemberCandidatesPostgreSQL(t *testing.T) {
	pool, ctx := addMembersPostgres(t)
	store := storage.NewPGXMemberStore(pool)

	// amActive1 and amActive2 are put in the channel; neither is "online"
	// anywhere in this test, which is precisely the case that used to leak.
	if _, err := store.AddChannelMembers(
		ctx, amWS, amPrivate, amAdmin, []string{amActive1, amActive2},
	); err != nil {
		t.Fatalf("seed channel members: %v", err)
	}

	t.Run("current members are excluded regardless of presence", func(t *testing.T) {
		got, err := store.SearchChannelMemberCandidates(ctx, amWS, amPrivate, amAdmin, "a", 50)
		if err != nil {
			t.Fatalf("SearchChannelMemberCandidates: %v", err)
		}
		ids := candidateIDs(t, got)
		for _, member := range []string{amActive1, amActive2} {
			if containsID(ids, member) {
				t.Fatalf("current member %s offered as a candidate: %v", member, ids)
			}
		}
	})

	t.Run("an eligible non-member is offered", func(t *testing.T) {
		got, err := store.SearchChannelMemberCandidates(ctx, amWS, amPrivate, amAdmin, "a", 50)
		if err != nil {
			t.Fatalf("SearchChannelMemberCandidates: %v", err)
		}
		if !containsID(candidateIDs(t, got), amActive3) {
			t.Fatalf("eligible non-member %s missing from candidates", amActive3)
		}
	})

	t.Run("ineligible users never appear", func(t *testing.T) {
		got, err := store.SearchChannelMemberCandidates(ctx, amWS, amPrivate, amAdmin, "", 50)
		if err != nil {
			t.Fatalf("SearchChannelMemberCandidates: %v", err)
		}
		ids := candidateIDs(t, got)
		for name, userID := range map[string]string{
			"suspended workspace member":  amSuspended,
			"deleted account":             amDeleted,
			"member of another workspace": amForeignU,
		} {
			if containsID(ids, userID) {
				t.Fatalf("%s appeared as a candidate: %v", name, ids)
			}
		}
	})

	t.Run("the caller is not offered to themselves", func(t *testing.T) {
		got, err := store.SearchChannelMemberCandidates(ctx, amWS, amPrivate, amAdmin, "", 50)
		if err != nil {
			t.Fatalf("SearchChannelMemberCandidates: %v", err)
		}
		if containsID(candidateIDs(t, got), amAdmin) {
			t.Fatal("the caller was offered as a candidate")
		}
	})

	// A channel from another tenant must not resolve, so its members are not
	// excluded and — more importantly — nothing about it is revealed.
	t.Run("a cross-workspace channel excludes nobody", func(t *testing.T) {
		got, err := store.SearchChannelMemberCandidates(ctx, amWS, amForeignC, amAdmin, "", 50)
		if err != nil {
			t.Fatalf("SearchChannelMemberCandidates: %v", err)
		}
		// Everyone eligible in *this* workspace is still a candidate: the foreign
		// channel's membership has no effect here.
		if !containsID(candidateIDs(t, got), amActive1) {
			t.Fatal("a cross-workspace channel wrongly excluded a local member")
		}
	})

	t.Run("ordering is deterministic and the limit is honoured", func(t *testing.T) {
		first, err := store.SearchChannelMemberCandidates(ctx, amWS, amPrivate, amAdmin, "", 1)
		if err != nil {
			t.Fatalf("SearchChannelMemberCandidates: %v", err)
		}
		second, err := store.SearchChannelMemberCandidates(ctx, amWS, amPrivate, amAdmin, "", 1)
		if err != nil {
			t.Fatalf("SearchChannelMemberCandidates: %v", err)
		}
		if len(first) != 1 || len(second) != 1 {
			t.Fatalf("limit not honoured: %d / %d", len(first), len(second))
		}
		if first[0].UserID != second[0].UserID {
			t.Fatalf("ordering is not stable: %s then %s", first[0].UserID, second[0].UserID)
		}
	})
}

// The group defect is sharper: the panel caps its participant list at 30, so in
// a larger group everyone past the 30th was offered as selectable.
func TestPGXSearchGroupParticipantCandidatesPostgreSQL(t *testing.T) {
	pool, ctx := addMembersPostgres(t)
	store := storage.NewPGXDMStore(pool)

	// Build a group with more participants than the panel preview can show, so
	// the ones beyond the cap are the interesting case.
	const beyondPreview = domain.MaxDMDetailsParticipants + 5
	extraIDs := make([]string, 0, beyondPreview)
	for i := 0; i < beyondPreview; i++ {
		userID := groupFixtureUUID(i)
		extraIDs = append(extraIDs, userID)
		if _, err := pool.Exec(ctx, `
			INSERT INTO auth.users (id, email, display_name, status)
			VALUES ($1, $2, $3, 'active')
			ON CONFLICT (id) DO NOTHING`,
			userID, "bulk"+userID[:8]+"@example.test", "Bulk "+userID[len(userID)-4:],
		); err != nil {
			t.Fatalf("seed bulk user: %v", err)
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO chat.workspace_members (workspace_id, user_id, role, status)
			VALUES ($1, $2, 'member', 'active')
			ON CONFLICT (workspace_id, user_id) DO NOTHING`, amWS, userID); err != nil {
			t.Fatalf("seed bulk membership: %v", err)
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO chat.dm_members (conversation_id, user_id, role, status)
			VALUES ($1, $2, 'member', 'active')
			ON CONFLICT (conversation_id, user_id) DO NOTHING`, amGroup, userID); err != nil {
			t.Fatalf("seed bulk participant: %v", err)
		}
	}

	t.Run("participants beyond the panel preview are still excluded", func(t *testing.T) {
		got, err := store.SearchGroupParticipantCandidates(ctx, amWS, amGroup, amActive1, "", 100)
		if err != nil {
			t.Fatalf("SearchGroupParticipantCandidates: %v", err)
		}
		ids := candidateIDs(t, got)
		// Every seeded participant, including those the 30-row preview could
		// never have shown, must be absent.
		for i, userID := range extraIDs {
			if containsID(ids, userID) {
				t.Fatalf("participant #%d (%s) offered as a candidate", i, userID)
			}
		}
	})

	t.Run("an eligible non-participant is offered", func(t *testing.T) {
		got, err := store.SearchGroupParticipantCandidates(ctx, amWS, amGroup, amActive1, "", 100)
		if err != nil {
			t.Fatalf("SearchGroupParticipantCandidates: %v", err)
		}
		if !containsID(candidateIDs(t, got), amActive2) {
			t.Fatal("an eligible non-participant is missing from candidates")
		}
	})

	// Someone who left is not an active participant, so offering them again is
	// correct: adding them back reactivates their row.
	t.Run("a participant who left is offered again", func(t *testing.T) {
		if _, err := pool.Exec(ctx, `
			UPDATE chat.dm_members SET status = 'left', left_at = now()
			WHERE conversation_id = $1 AND user_id = $2`, amGroup, extraIDs[0]); err != nil {
			t.Fatalf("mark participant as left: %v", err)
		}
		t.Cleanup(func() {
			_, _ = pool.Exec(context.Background(), `
				UPDATE chat.dm_members SET status = 'active', left_at = NULL
				WHERE conversation_id = $1 AND user_id = $2`, amGroup, extraIDs[0])
		})

		got, err := store.SearchGroupParticipantCandidates(ctx, amWS, amGroup, amActive1, "", 100)
		if err != nil {
			t.Fatalf("SearchGroupParticipantCandidates: %v", err)
		}
		if !containsID(candidateIDs(t, got), extraIDs[0]) {
			t.Fatal("a participant who left was not offered again")
		}
	})

	t.Run("ineligible users and the caller never appear", func(t *testing.T) {
		got, err := store.SearchGroupParticipantCandidates(ctx, amWS, amGroup, amActive1, "", 100)
		if err != nil {
			t.Fatalf("SearchGroupParticipantCandidates: %v", err)
		}
		ids := candidateIDs(t, got)
		for name, userID := range map[string]string{
			"suspended":       amSuspended,
			"deleted account": amDeleted,
			"other workspace": amForeignU,
			"the caller":      amActive1,
		} {
			if containsID(ids, userID) {
				t.Fatalf("%s appeared as a candidate", name)
			}
		}
	})

	t.Run("ordering is deterministic and the limit is honoured", func(t *testing.T) {
		first, err := store.SearchGroupParticipantCandidates(ctx, amWS, amGroup, amActive1, "", 1)
		if err != nil {
			t.Fatalf("SearchGroupParticipantCandidates: %v", err)
		}
		second, err := store.SearchGroupParticipantCandidates(ctx, amWS, amGroup, amActive1, "", 1)
		if err != nil {
			t.Fatalf("SearchGroupParticipantCandidates: %v", err)
		}
		if len(first) != 1 || len(second) != 1 {
			t.Fatalf("limit not honoured: %d / %d", len(first), len(second))
		}
		if first[0].UserID != second[0].UserID {
			t.Fatalf("ordering is not stable: %s then %s", first[0].UserID, second[0].UserID)
		}
	})
}

// groupFixtureUUID builds distinct valid UUIDs for the bulk group fixture.
func groupFixtureUUID(i int) string {
	const hex = "0123456789abcdef"
	return "f3000000-0000-4000-8000-0000000000" +
		string(hex[(i/16)%16]) + string(hex[i%16])
}

// The IDs the transaction reports are the ones it inserted — the fan-out
// audience — not the ones the request named.
func TestPGXAddMembersReportsOnlyTheInsertedUserIDsPostgreSQL(t *testing.T) {
	pool, ctx := addMembersPostgres(t)
	members := storage.NewPGXMemberStore(pool)

	first, err := members.AddChannelMembers(ctx, amWS, amPrivate, amAdmin, []string{amActive1})
	if err != nil {
		t.Fatalf("first add: %v", err)
	}
	if len(first.AddedUserIDs) != 1 || first.AddedUserIDs[0] != amActive1 {
		t.Fatalf("AddedUserIDs = %v, want [%s]", first.AddedUserIDs, amActive1)
	}

	// Repeat amActive1 and add amActive2: only amActive2 is newly a member.
	second, err := members.AddChannelMembers(ctx, amWS, amPrivate, amAdmin, []string{amActive1, amActive2})
	if err != nil {
		t.Fatalf("second add: %v", err)
	}
	if len(second.AddedUserIDs) != 1 || second.AddedUserIDs[0] != amActive2 {
		t.Fatalf("AddedUserIDs = %v, want only %s", second.AddedUserIDs, amActive2)
	}

	// A pure retry inserts nobody, so there is nobody to signal.
	retry, err := members.AddChannelMembers(ctx, amWS, amPrivate, amAdmin, []string{amActive1, amActive2})
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if len(retry.AddedUserIDs) != 0 {
		t.Fatalf("a pure retry produced recipients: %v", retry.AddedUserIDs)
	}
}

func TestPGXAddGroupParticipantsReportsOnlyTheNewParticipantsPostgreSQL(t *testing.T) {
	pool, ctx := addMembersPostgres(t)
	store := storage.NewPGXDMStore(pool)

	input := func(userIDs ...string) storage.AddGroupParticipantsInput {
		return storage.AddGroupParticipantsInput{
			WorkspaceID: amWS, ConversationID: amGroup, CallerID: amActive1,
			UserIDs: userIDs,
		}
	}

	first, err := store.AddGroupParticipants(ctx, input(amActive2))
	if err != nil {
		t.Fatalf("first add: %v", err)
	}
	if len(first.AddedUserIDs) != 1 || first.AddedUserIDs[0] != amActive2 {
		t.Fatalf("AddedUserIDs = %v, want [%s]", first.AddedUserIDs, amActive2)
	}

	// amActive2 is now a participant; only amActive3 is new.
	second, err := store.AddGroupParticipants(ctx, input(amActive2, amActive3))
	if err != nil {
		t.Fatalf("second add: %v", err)
	}
	if len(second.AddedUserIDs) != 1 || second.AddedUserIDs[0] != amActive3 {
		t.Fatalf("AddedUserIDs = %v, want only %s", second.AddedUserIDs, amActive3)
	}

	retry, err := store.AddGroupParticipants(ctx, input(amActive2, amActive3))
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if len(retry.AddedUserIDs) != 0 {
		t.Fatalf("a pure retry produced recipients: %v", retry.AddedUserIDs)
	}
}

// ── No fixed conversation capacity (product decision) ───────────────────────

// seedBulkMembers creates `count` eligible users and returns their IDs, adding
// them to the workspace. One statement per table rather than a loop of
// round-trips, so a large fixture stays fast.
func seedBulkMembers(t *testing.T, pool *pgxpool.Pool, ctx context.Context, prefix string, count int) []string {
	t.Helper()
	ids := make([]string, 0, count)
	for i := 0; i < count; i++ {
		ids = append(ids, bulkUUID(prefix, i))
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO auth.users (id, email, display_name, status)
		SELECT id, 'bulk-' || id::text || '@example.test', 'Bulk ' || id::text, 'active'
		FROM unnest($1::uuid[]) AS t(id)
		ON CONFLICT (id) DO NOTHING`, ids); err != nil {
		t.Fatalf("seed bulk users: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO chat.workspace_members (workspace_id, user_id, role, status)
		SELECT $1, id, 'member', 'active'
		FROM unnest($2::uuid[]) AS t(id)
		ON CONFLICT (workspace_id, user_id) DO NOTHING`, amWS, ids); err != nil {
		t.Fatalf("seed bulk workspace members: %v", err)
	}
	return ids
}

// bulkUUID builds a distinct valid UUID per index. The final group needs
// exactly 12 hex digits: nine zeros plus a three-digit counter.
func bulkUUID(prefix string, i int) string {
	const hex = "0123456789abcdef"
	return prefix + "-0000-4000-8000-000000000" +
		string(hex[(i/256)%16]) + string(hex[(i/16)%16]) + string(hex[i%16])
}

// A channel far larger than any former ceiling still accepts new members, and
// successive batches keep growing it. 25 is the size of one request, not the
// capacity of a conversation.
func TestPGXAddChannelMembersHasNoTotalCapacityPostgreSQL(t *testing.T) {
	pool, ctx := addMembersPostgres(t)
	store := storage.NewPGXMemberStore(pool)

	// Seed a channel with 80 members — well past 50 — directly.
	existing := seedBulkMembers(t, pool, ctx, "f4000000", 80)
	if _, err := pool.Exec(ctx, `
		INSERT INTO chat.channel_members (channel_id, user_id, role)
		SELECT $1, id, 'member' FROM unnest($2::uuid[]) AS t(id)`, amPrivate, existing); err != nil {
		t.Fatalf("seed existing channel members: %v", err)
	}

	newcomers := seedBulkMembers(t, pool, ctx, "f5000000", domain.MaxAddMembersPerRequest)

	// A full batch on top of an already-large channel: accepted, no capacity error.
	result, err := store.AddChannelMembers(ctx, amWS, amPrivate, amAdmin, newcomers)
	if err != nil {
		t.Fatalf("a large channel must still accept members: %v", err)
	}
	if result.Added != domain.MaxAddMembersPerRequest {
		t.Fatalf("Added = %d, want %d", result.Added, domain.MaxAddMembersPerRequest)
	}
	if result.TotalCount <= 80 {
		t.Fatalf("TotalCount = %d, want more than the 80 seeded", result.TotalCount)
	}

	// And another batch after that — successive requests keep growing it.
	more := seedBulkMembers(t, pool, ctx, "f6000000", 10)
	second, err := store.AddChannelMembers(ctx, amWS, amPrivate, amAdmin, more)
	if err != nil {
		t.Fatalf("a second batch must also be accepted: %v", err)
	}
	if second.Added != 10 {
		t.Fatalf("second batch Added = %d, want 10", second.Added)
	}

	// A pure retry of the whole set is still a no-op.
	retry, err := store.AddChannelMembers(ctx, amWS, amPrivate, amAdmin, more)
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if retry.Added != 0 || len(retry.AddedUserIDs) != 0 {
		t.Fatalf("retry added %d", retry.Added)
	}
}

// The same for groups, starting from a membership above the old 50 ceiling.
func TestPGXAddGroupParticipantsHasNoTotalCapacityPostgreSQL(t *testing.T) {
	pool, ctx := addMembersPostgres(t)
	store := storage.NewPGXDMStore(pool)

	existing := seedBulkMembers(t, pool, ctx, "f7000000", 60)
	if _, err := pool.Exec(ctx, `
		INSERT INTO chat.dm_members (conversation_id, user_id, role, status)
		SELECT $1, id, 'member', 'active' FROM unnest($2::uuid[]) AS t(id)`,
		amGroup, existing); err != nil {
		t.Fatalf("seed existing participants: %v", err)
	}
	before := countDMParticipants(t, pool, ctx, amGroup)
	if before <= 50 {
		t.Fatalf("fixture has %d participants, want more than 50", before)
	}

	input := func(userIDs ...string) storage.AddGroupParticipantsInput {
		return storage.AddGroupParticipantsInput{
			WorkspaceID: amWS, ConversationID: amGroup, CallerID: amActive1, UserIDs: userIDs,
		}
	}

	// A group already past 50 accepts a new participant — no ceiling error.
	result, err := store.AddGroupParticipants(ctx, input(amActive2))
	if err != nil {
		t.Fatalf("a group with %d participants must still accept one more: %v", before, err)
	}
	if result.Added != 1 {
		t.Fatalf("Added = %d, want 1", result.Added)
	}

	// A full 25-ID batch on top of that is accepted too.
	newcomers := seedBulkMembers(t, pool, ctx, "f8000000", domain.MaxAddMembersPerRequest)
	batch, err := store.AddGroupParticipants(ctx, input(newcomers...))
	if err != nil {
		t.Fatalf("a full batch must be accepted: %v", err)
	}
	if batch.Added != domain.MaxAddMembersPerRequest {
		t.Fatalf("Added = %d, want %d", batch.Added, domain.MaxAddMembersPerRequest)
	}

	// A retry of that same batch adds nobody, and still is not an error.
	retry, err := store.AddGroupParticipants(ctx, input(newcomers...))
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if retry.Added != 0 || len(retry.AddedUserIDs) != 0 {
		t.Fatalf("retry added %d", retry.Added)
	}
	if got := countDMParticipants(t, pool, ctx, amGroup); got != before+1+domain.MaxAddMembersPerRequest {
		t.Fatalf("participants = %d, want %d", got, before+1+domain.MaxAddMembersPerRequest)
	}
}
