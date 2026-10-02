package storage_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nicrepository/nchat/libs/go/platform/conversationownership"
	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

const ownershipWS = "00000000-0000-0000-0000-000000000001"
const ownershipDM = "95300000-0000-4000-8000-000000000001"
const ownershipChannel = "95300000-0000-4000-8000-000000000002"
const ownershipA = "95300000-0000-4000-8000-00000000000a"
const ownershipB = "95300000-0000-4000-8000-00000000000b"
const ownershipC = "95300000-0000-4000-8000-00000000000c"

func ownershipPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	return ownershipPoolAt(t, false)
}

func ownershipPoolAt(t *testing.T, legacy bool) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("OWNERSHIP_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("OWNERSHIP_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	var name string
	if err := pool.QueryRow(t.Context(), `SELECT current_database()`).Scan(&name); err != nil {
		t.Fatal(err)
	}
	if name != "ownership_953_test" {
		t.Fatalf("refusing destructive test in %q", name)
	}
	ownershipExec(t, pool, `DROP SCHEMA IF EXISTS chat CASCADE; DROP SCHEMA IF EXISTS auth CASCADE;
 CREATE SCHEMA auth;
 CREATE TABLE auth.users (id UUID PRIMARY KEY,display_name TEXT,full_name TEXT,avatar_url TEXT,status TEXT NOT NULL DEFAULT 'active',deleted_at TIMESTAMPTZ);`)
	migrations := readAllChatUpMigrations(t)
	if legacy {
		migrations, _, _ = strings.Cut(migrations, readChatMigration(t, "000060_conversation_ownership_compatibility.up.sql"))
	}
	ownershipExec(t, pool, migrations)
	ownershipExec(t, pool, `INSERT INTO auth.users(id,display_name) VALUES ($1,'A'),($2,'B'),($3,'C')`, ownershipA, ownershipB, ownershipC)
	ownershipExec(t, pool, `INSERT INTO chat.workspace_members(workspace_id,user_id) VALUES ($1,$2),($1,$3),($1,$4)`, ownershipWS, ownershipA, ownershipB, ownershipC)
	ownershipExec(t, pool, `INSERT INTO chat.dm_conversations(id,workspace_id,type,created_by) VALUES ($1,$2,'group',$3)`, ownershipDM, ownershipWS, ownershipA)
	ownershipExec(t, pool, `INSERT INTO chat.dm_members(conversation_id,user_id,joined_at) VALUES ($1,$2,'2020-01-03'),($1,$3,'2020-01-02'),($1,$4,'2020-01-01')`, ownershipDM, ownershipA, ownershipB, ownershipC)
	ownershipExec(t, pool, `INSERT INTO chat.channels(id,workspace_id,type,slug,display_name,created_by) VALUES ($1,$2,'private','private-953','Private',$3)`, ownershipChannel, ownershipWS, ownershipA)
	ownershipExec(t, pool, `INSERT INTO chat.channel_members(channel_id,user_id,role,joined_at) VALUES ($1,$2,'member','2020-01-03'),($1,$3,'moderator','2020-01-02'),($1,$4,'member','2020-01-01') ON CONFLICT (channel_id,user_id) DO UPDATE SET joined_at=EXCLUDED.joined_at`, ownershipChannel, ownershipA, ownershipB, ownershipC)
	if !legacy {
		ownershipExec(t, pool, `SELECT chat.backfill_conversation_ownership()`)
	}
	return pool
}

func ownershipExec(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(t.Context(), sql, args...); err != nil {
		t.Fatal(err)
	}
}
func enableOwnership(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	sql := readChatMigration(t, "../../scripts/db/ownership/activate.sql")
	ownershipExec(t, pool, ownershipActivationTransaction(t, sql))
}
func assertOwnershipRole(t *testing.T, pool *pgxpool.Pool, kind, id, user, role string) {
	t.Helper()
	var got string
	if err := pool.QueryRow(t.Context(), `SELECT role FROM chat.active_ownership_participants WHERE kind=$1 AND conversation_id=$2 AND user_id=$3`, kind, id, user).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != role {
		t.Fatalf("role=%q want=%q", got, role)
	}
}
func assertNoOrphans(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	var count int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM chat.orphaned_private_conversations`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("%d orphaned conversations", count)
	}
}
func assertOwnerConflict(t *testing.T, err error) {
	t.Helper()
	var pgerr *pgconn.PgError
	if !errors.As(err, &pgerr) || pgerr.Code != "P0953" {
		t.Fatalf("ownership conflict error=%v", err)
	}
}

func TestOwnershipCompatibilityAndBackfillPostgreSQL(t *testing.T) {
	pool := ownershipPool(t)
	var changed int
	if err := pool.QueryRow(t.Context(), `SELECT chat.backfill_conversation_ownership()`).Scan(&changed); err != nil {
		t.Fatal(err)
	}
	if changed != 0 {
		t.Fatal("backfill was not idempotent")
	}
	assertOwnershipRole(t, pool, "dm", ownershipDM, ownershipA, "owner")
	assertOwnershipRole(t, pool, "channel", ownershipChannel, ownershipB, "admin")
	var role string
	if err := pool.QueryRow(t.Context(), `SELECT role FROM chat.dm_members WHERE user_id=$1`, ownershipA).Scan(&role); err != nil {
		t.Fatal(err)
	}
	if role != "member" {
		t.Fatalf("legacy reader sees %q", role)
	}
	ownershipExec(t, pool, `UPDATE auth.users SET status='suspended' WHERE id=$1`, ownershipA)
	ownershipExec(t, pool, `SELECT chat.backfill_conversation_ownership()`)
	assertOwnershipRole(t, pool, "channel", ownershipChannel, ownershipB, "owner")
	assertOwnershipRole(t, pool, "dm", ownershipDM, ownershipC, "owner")
	assertNoOrphans(t, pool)
}

func TestOwnershipSuccessionAndRollbackPostgreSQL(t *testing.T) {
	pool := ownershipPool(t)
	enableOwnership(t, pool)
	ownershipExec(t, pool, `UPDATE chat.dm_members SET ownership_role='admin' WHERE user_id=$1`, ownershipB)
	ownershipExec(t, pool, `UPDATE auth.users SET status='suspended' WHERE id=$1`, ownershipA)
	assertOwnershipRole(t, pool, "dm", ownershipDM, ownershipB, "owner")
	assertOwnershipRole(t, pool, "channel", ownershipChannel, ownershipB, "owner")
	_, err := pool.Exec(t.Context(), `UPDATE chat.dm_members SET ownership_role='member' WHERE user_id=$1`, ownershipB)
	assertOwnerConflict(t, err)
	assertOwnershipRole(t, pool, "dm", ownershipDM, ownershipB, "owner")
	ownershipExec(t, pool, `UPDATE chat.workspace_members SET role='guest' WHERE user_id=$1`, ownershipC)
	_, err = pool.Exec(t.Context(), `UPDATE auth.users SET status='suspended' WHERE id=$1`, ownershipB)
	assertOwnerConflict(t, err)
	assertOwnershipRole(t, pool, "dm", ownershipDM, ownershipB, "owner")
	assertOwnershipRole(t, pool, "channel", ownershipChannel, ownershipB, "owner")
	ownershipExec(t, pool, `SELECT chat.assign_ownership('dm',$1,$2,'owner',$3,'manual')`, ownershipDM, ownershipC, ownershipB)
	ownershipExec(t, pool, `SELECT chat.assign_ownership('channel',$1,$2,'owner',$3,'manual')`, ownershipChannel, ownershipC, ownershipB)
	ownershipExec(t, pool, `UPDATE auth.users SET status='suspended' WHERE id=$1`, ownershipB)
	assertNoOrphans(t, pool)
	ownershipExec(t, pool, `UPDATE chat.dm_members SET status='left',left_at=now() WHERE user_id=$1`, ownershipC)
	ownershipExec(t, pool, `DELETE FROM chat.channel_members WHERE user_id=$1`, ownershipC)
	assertNoOrphans(t, pool)
}

func TestOwnershipTransferIdempotencyPostgreSQL(t *testing.T) {
	pool := ownershipPool(t)
	enableOwnership(t, pool)
	store := storage.NewPGXOwnershipStore(pool)
	scope := storage.OwnershipScope{WorkspaceID: ownershipWS, Kind: "dm", ConversationID: ownershipDM, ActorID: ownershipA}
	input := storage.OwnershipMutation{Scope: scope, TargetUserID: ownershipB, Role: domain.ConversationMember, Operation: "transfer", IdempotencyKey: "transfer-953"}
	result, err := store.Mutate(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	if result.Replayed {
		t.Fatal("first mutation replayed")
	}
	result, err = store.Mutate(t.Context(), input)
	if err != nil || !result.Replayed {
		t.Fatalf("replay=%+v err=%v", result, err)
	}
	input.TargetUserID = ownershipC
	if _, err = store.Mutate(t.Context(), input); !errors.Is(err, domain.ErrOwnershipConflict) {
		t.Fatalf("key reuse=%v", err)
	}
	assertOwnershipRole(t, pool, "dm", ownershipDM, ownershipA, "member")
	assertOwnershipRole(t, pool, "dm", ownershipDM, ownershipB, "owner")
	var count int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM chat.ownership_audit WHERE reason='transfer'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("duplicate audit: %d", count)
	}
	scope.WorkspaceID = "00000000-0000-0000-0000-000000000002"
	if _, err := store.Details(t.Context(), scope); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("cross-workspace=%v", err)
	}
}

func TestOwnershipConcurrentDeparturePostgreSQL(t *testing.T) {
	pool := ownershipPool(t)
	enableOwnership(t, pool)
	ownershipExec(t, pool, `SELECT chat.assign_ownership('dm',$1,$2,'owner',$3,'manual')`, ownershipDM, ownershipB, ownershipA)
	results := make(chan error, 2)
	store := storage.NewPGXOwnershipStore(pool)
	for _, user := range []string{ownershipA, ownershipB} {
		go func(user string) {
			_, err := store.Mutate(t.Context(), storage.OwnershipMutation{Scope: storage.OwnershipScope{WorkspaceID: ownershipWS, Kind: "dm", ConversationID: ownershipDM, ActorID: user}, Operation: "leave"})
			results <- err
		}(user)
	}
	for range 2 {
		err := <-results
		if err != nil {
			t.Fatal(err)
		}
	}
	assertNoOrphans(t, pool)
	assertOwnershipRole(t, pool, "dm", ownershipDM, ownershipC, "owner")
}

func TestOwnershipTransferAndLeavePostgreSQL(t *testing.T) {
	pool := ownershipPool(t)
	enableOwnership(t, pool)
	store := storage.NewPGXOwnershipStore(pool)
	input := storage.OwnershipMutation{Scope: storage.OwnershipScope{WorkspaceID: ownershipWS, Kind: "dm", ConversationID: ownershipDM, ActorID: ownershipA}, TargetUserID: strings.ToUpper(ownershipB), Role: domain.ConversationAdmin, Operation: "transfer-and-leave", IdempotencyKey: "leave-953"}
	result, err := store.Mutate(t.Context(), input)
	if err != nil || !result.Left || result.EventID == "" {
		t.Fatalf("atomic transfer and leave=%+v %v", result, err)
	}
	assertOwnershipRole(t, pool, "dm", ownershipDM, ownershipB, "owner")
	replayed, err := store.Mutate(t.Context(), input)
	if err != nil || !replayed.Replayed || replayed.EventID != result.EventID {
		t.Fatalf("removed actor retry=%+v %v", replayed, err)
	}
	if _, err = store.Details(t.Context(), input.Scope); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("departed actor access=%v", err)
	}
	var count int
	if err = pool.QueryRow(t.Context(), `SELECT count(*) FROM chat.ownership_outbox WHERE conversation_id=$1`, ownershipDM).Scan(&count); err != nil || count != 1 {
		t.Fatalf("one invalidation per transaction: count=%d err=%v", count, err)
	}
}

func TestOwnershipAuthorizationPostgreSQL(t *testing.T) {
	pool := ownershipPool(t)
	enableOwnership(t, pool)
	store := storage.NewPGXOwnershipStore(pool)
	ownershipExec(t, pool, `SELECT chat.assign_ownership('dm',$1,$2,'admin',$3,'manual')`, ownershipDM, ownershipB, ownershipA)
	scope := storage.OwnershipScope{WorkspaceID: ownershipWS, Kind: "dm", ConversationID: ownershipDM, ActorID: ownershipB}
	for _, input := range []storage.OwnershipMutation{
		{Scope: scope, Operation: "remove", TargetUserID: ownershipA},
		{Scope: scope, Operation: "role", TargetUserID: ownershipC, Role: domain.ConversationOwner},
	} {
		if _, err := store.Mutate(t.Context(), input); !errors.Is(err, domain.ErrForbidden) {
			t.Fatalf("admin forbidden action=%v", err)
		}
	}
	if _, err := store.Mutate(t.Context(), storage.OwnershipMutation{Scope: scope, Operation: "rename", Name: "Admin title"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Mutate(t.Context(), storage.OwnershipMutation{Scope: scope, Operation: "rename", Name: " "}); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("invalid metadata=%v", err)
	}
	if _, err := store.Mutate(t.Context(), storage.OwnershipMutation{Scope: scope, Operation: "remove", TargetUserID: ownershipC}); err != nil {
		t.Fatal(err)
	}
	assertNoOrphans(t, pool)
}

func TestOwnershipAccessAndOutboxPostgreSQL(t *testing.T) {
	pool := ownershipPool(t)
	enableOwnership(t, pool)
	ownershipExec(t, pool, `SELECT chat.assign_ownership('dm',$1,$2,'owner',$3,'manual')`, ownershipDM, ownershipB, ownershipA)
	ownershipExec(t, pool, `DELETE FROM chat.ownership_outbox`)
	// No succession is needed in the group; its invalidation must still publish.
	ownershipExec(t, pool, `UPDATE auth.users SET status='suspended' WHERE id=$1`, ownershipA)
	var count int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM chat.ownership_outbox WHERE published_at IS NULL`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("account invalidation count=%d err=%v", count, err)
	}
	store := storage.NewPGXOwnershipStore(pool)
	failure := errors.New("bus unavailable")
	if err := store.DispatchOwnershipChanges(t.Context(), func(context.Context, string, string, string) error { return failure }); !errors.Is(err, failure) {
		t.Fatalf("bus failure=%v", err)
	}
	published := 0
	if err := store.DispatchOwnershipChanges(t.Context(), func(context.Context, string, string, string) error { published++; return nil }); err != nil || published != 2 {
		t.Fatalf("outbox retry published=%d err=%v", published, err)
	}
	ownershipExec(t, pool, `UPDATE chat.workspace_members SET status='left' WHERE user_id=$1`, ownershipB)
	assertOwnershipRole(t, pool, "dm", ownershipDM, ownershipC, "owner")
	assertOwnershipRole(t, pool, "channel", ownershipChannel, ownershipC, "owner")
	assertNoOrphans(t, pool)
}

func TestOwnershipConcurrentSuspensionAndTransferPostgreSQL(t *testing.T) {
	pool := ownershipPool(t)
	enableOwnership(t, pool)
	store := storage.NewPGXOwnershipStore(pool)
	results := make(chan error, 2)
	go func() {
		_, err := store.Mutate(t.Context(), storage.OwnershipMutation{Scope: storage.OwnershipScope{WorkspaceID: ownershipWS, Kind: "dm", ConversationID: ownershipDM, ActorID: ownershipA}, TargetUserID: ownershipB, Role: domain.ConversationMember, Operation: "transfer", IdempotencyKey: "suspension-race"})
		results <- err
	}()
	go func() {
		_, err := conversationownership.Retry(t.Context(), func() (bool, error) {
			tx, err := pool.Begin(t.Context())
			if err != nil {
				return false, err
			}
			defer func() { _ = tx.Rollback(t.Context()) }()
			if _, err = tx.Exec(t.Context(), conversationownership.SerializableSQL); err != nil {
				return false, err
			}
			if _, err = tx.Exec(t.Context(), conversationownership.LockUserSQL, ownershipA); err != nil {
				return false, err
			}
			if _, err = tx.Exec(t.Context(), `UPDATE auth.users SET status='suspended' WHERE id=$1`, ownershipA); err != nil {
				return false, err
			}
			return true, tx.Commit(t.Context())
		})
		results <- err
	}()
	for range 2 {
		err := <-results
		if err != nil && !errors.Is(err, domain.ErrNotFound) {
			t.Fatal(err)
		}
	}
	assertNoOrphans(t, pool)
}

func TestOwnershipActivationAndCompatibilityRollbackPostgreSQL(t *testing.T) {
	pool := ownershipPool(t)
	// A legacy writer can still insert only its original role value.
	ownershipExec(t, pool, `DELETE FROM chat.channel_members WHERE channel_id=$1 AND user_id=$2`, ownershipChannel, ownershipC)
	ownershipExec(t, pool, `INSERT INTO chat.channel_members(channel_id,user_id,role) VALUES($1,$2,'member')`, ownershipChannel, ownershipC)
	assertOwnershipRole(t, pool, "channel", ownershipChannel, ownershipC, "member")
	enableOwnership(t, pool)
	store := storage.NewPGXOwnershipStore(pool)
	scope := storage.OwnershipScope{WorkspaceID: ownershipWS, Kind: "channel", ConversationID: ownershipChannel, ActorID: ownershipA}
	details, err := store.Details(t.Context(), scope)
	if err != nil || !details.Enabled || len(details.Members) != 3 {
		t.Fatalf("compatibility reader=%+v %v", details, err)
	}
	// The compatibility binary reads normalized roles even if shadow data is
	// absent; a binary rollback preserves expanded constraints and both stores.
	ownershipExec(t, pool, `UPDATE chat.channel_members SET ownership_role=NULL WHERE channel_id=$1 AND user_id=$2`, ownershipChannel, ownershipA)
	assertOwnershipRole(t, pool, "channel", ownershipChannel, ownershipA, "owner")
	_, err = pool.Exec(t.Context(), readChatMigration(t, "000060_conversation_ownership_compatibility.down.sql"))
	if err == nil {
		t.Fatal("unsafe schema rollback was accepted")
	}
	assertNoOrphans(t, pool)
}

func TestOwnershipActivationRejectsGuestOnlyOrphanPostgreSQL(t *testing.T) {
	pool := ownershipPool(t)
	ownershipExec(t, pool, `UPDATE chat.workspace_members SET role='guest'`)
	ownershipExec(t, pool, `UPDATE chat.dm_members SET ownership_role='member'`)
	sql := readChatMigration(t, "../../scripts/db/ownership/activate.sql")
	_, err := pool.Exec(t.Context(), ownershipActivationTransaction(t, sql))
	if err == nil {
		t.Fatal("activation accepted a nonempty guest-only orphan")
	}
	var enabled bool
	if err := pool.QueryRow(t.Context(), `SELECT enabled FROM chat.ownership_rollout WHERE singleton`).Scan(&enabled); err != nil || enabled {
		t.Fatalf("partial activation enabled=%v err=%v", enabled, err)
	}
}

func TestOwnershipRejoinRenewsSeniorityPostgreSQL(t *testing.T) {
	pool := ownershipPool(t)
	enableOwnership(t, pool)
	ownershipExec(t, pool, `UPDATE chat.dm_members SET status='left',left_at=now() WHERE conversation_id=$1 AND user_id=$2`, ownershipDM, ownershipC)
	ownershipExec(t, pool, `UPDATE chat.dm_members SET status='active',left_at=NULL WHERE conversation_id=$1 AND user_id=$2`, ownershipDM, ownershipC)
	store := storage.NewPGXOwnershipStore(pool)
	_, err := store.Mutate(t.Context(), storage.OwnershipMutation{Scope: storage.OwnershipScope{WorkspaceID: ownershipWS, Kind: "dm", ConversationID: ownershipDM, ActorID: ownershipA}, Operation: "leave"})
	if err != nil {
		t.Fatal(err)
	}
	assertOwnershipRole(t, pool, "dm", ownershipDM, ownershipB, "owner")
}

func TestOwnershipConcurrentMutationMatrixPostgreSQL(t *testing.T) {
	for _, scenario := range []struct {
		name          string
		first, second storage.OwnershipMutation
	}{
		{"transfer-remove", storage.OwnershipMutation{Operation: "transfer", TargetUserID: ownershipB, Role: domain.ConversationMember, IdempotencyKey: "race-transfer"}, storage.OwnershipMutation{Operation: "remove", TargetUserID: ownershipB}},
		{"promote-leave", storage.OwnershipMutation{Operation: "role", TargetUserID: ownershipB, Role: domain.ConversationOwner}, storage.OwnershipMutation{Operation: "leave"}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			pool := ownershipPool(t)
			enableOwnership(t, pool)
			store := storage.NewPGXOwnershipStore(pool)
			scope := storage.OwnershipScope{WorkspaceID: ownershipWS, Kind: "dm", ConversationID: ownershipDM, ActorID: ownershipA}
			results := make(chan error, 2)
			for _, input := range []storage.OwnershipMutation{scenario.first, scenario.second} {
				input.Scope = scope
				go func(input storage.OwnershipMutation) { _, err := store.Mutate(t.Context(), input); results <- err }(input)
			}
			for range 2 {
				err := <-results
				if err != nil && !errors.Is(err, domain.ErrForbidden) && !errors.Is(err, domain.ErrNotFound) {
					t.Fatal(err)
				}
			}
			assertNoOrphans(t, pool)
		})
	}
}

func TestOwnershipDeletionPreservesAuthorshipPostgreSQL(t *testing.T) {
	pool := ownershipPool(t)
	enableOwnership(t, pool)
	ownershipExec(t, pool, `UPDATE auth.users SET deleted_at=now() WHERE id=$1`, ownershipA)
	assertOwnershipRole(t, pool, "dm", ownershipDM, ownershipC, "owner")
	assertOwnershipRole(t, pool, "channel", ownershipChannel, ownershipB, "owner")
	var creator string
	if err := pool.QueryRow(t.Context(), `SELECT created_by::text FROM chat.dm_conversations WHERE id=$1`, ownershipDM).Scan(&creator); err != nil || creator != ownershipA {
		t.Fatalf("creator=%s err=%v", creator, err)
	}
	assertNoOrphans(t, pool)
}

func ownershipActivationTransaction(t *testing.T, sql string) string {
	t.Helper()
	_, transaction, found := strings.Cut(sql, "BEGIN;")
	if !found {
		t.Fatal("activation transaction missing")
	}
	transaction = strings.NewReplacer(
		":'legacy_retired'", "'true'",
		":'rollback_target_sha'", "'1043000000000000000000000000000000000000'",
		":'retirement_evidence'", "'test-1043'",
		"\\gset", ";",
	).Replace(transaction)
	return "BEGIN;" + transaction
}
