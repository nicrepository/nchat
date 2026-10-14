package storage_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

func successionInput(kind, actor string) storage.OwnershipMutation {
	id := ownershipDM
	if kind == "channel" {
		id = ownershipChannel
	}
	return storage.OwnershipMutation{Scope: storage.OwnershipScope{WorkspaceID: ownershipWS, Kind: kind, ConversationID: id, ActorID: actor}, Operation: "leave"}
}

func successionMembers(t *testing.T, pool *pgxpool.Pool, kind string, setup string) {
	t.Helper()
	table := "chat.dm_members"
	if kind == "channel" {
		table = "chat.channel_members"
	}
	ownershipExec(t, pool, strings.ReplaceAll(setup, "MEMBERS", table))
}

type successionSelection struct {
	name, setup, want string
	promotions        int
}

func TestOwnershipSuccessionSelectionPostgreSQL(t *testing.T) {
	for _, kind := range []string{"dm", "channel"} {
		for _, scenario := range []successionSelection{
			{"owner remains", `UPDATE MEMBERS SET ownership_role='owner' WHERE user_id='` + ownershipB + `'`, ownershipB, 0},
			{"guest owner remains", `UPDATE MEMBERS SET ownership_role='owner' WHERE user_id='` + ownershipB + `'; UPDATE chat.workspace_members SET role='guest' WHERE user_id='` + ownershipB + `'`, ownershipB, 0},
			{"oldest admin", `UPDATE MEMBERS SET ownership_role='admin' WHERE user_id<>'` + ownershipA + `'`, ownershipC, 1},
			{"oldest member", `UPDATE MEMBERS SET ownership_role='member' WHERE user_id<>'` + ownershipA + `'`, ownershipC, 1},
			{"new admin beats old member", `UPDATE MEMBERS SET ownership_role='admin' WHERE user_id='` + ownershipB + `'`, ownershipB, 1},
			{"uuid tie", `UPDATE MEMBERS SET ownership_role='member',joined_at='2020-01-01' WHERE user_id<>'` + ownershipA + `'`, ownershipB, 1},
			{"departing excluded", `UPDATE MEMBERS SET joined_at='2010-01-01' WHERE user_id='` + ownershipA + `'; UPDATE MEMBERS SET ownership_role='member' WHERE user_id<>'` + ownershipA + `'`, ownershipC, 1},
		} {
			t.Run(kind+"/"+scenario.name, func(t *testing.T) {
				testSuccessionSelection(t, kind, scenario)
			})
		}
	}
}

func testSuccessionSelection(t *testing.T, kind string, scenario successionSelection) {
	t.Helper()
	pool := ownershipPool(t)
	enableOwnership(t, pool)
	successionMembers(t, pool, kind, scenario.setup)
	assertSuccessionBeforeDeparture(t, pool, kind)
	input := successionInput(kind, ownershipA)
	result, err := storage.NewPGXOwnershipStore(pool).Mutate(t.Context(), input)
	if err != nil || !result.Left || result.EventID == "" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	assertOwnershipRole(t, pool, kind, input.Scope.ConversationID, scenario.want, "owner")
	var count int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM chat.ownership_audit WHERE conversation_kind=$1 AND reason='succession' AND actor_user_id=$2`, kind, ownershipA).Scan(&count); err != nil || count != scenario.promotions {
		t.Fatalf("succession audit=%d err=%v", count, err)
	}
	assertNoOrphans(t, pool)
}

// A BEFORE trigger proves that the application promoted before departure; the
// existing AFTER trigger alone would otherwise make all final-state tests pass.
func assertSuccessionBeforeDeparture(t *testing.T, pool *pgxpool.Pool, kind string) {
	t.Helper()
	table, event, id := "chat.dm_members", "UPDATE OF status", "OLD.conversation_id"
	if kind == "channel" {
		table, event, id = "chat.channel_members", "DELETE", "OLD.channel_id"
	}
	ownershipExec(t, pool, `CREATE FUNCTION chat.assert_predeparture_owner() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF OLD.user_id='`+ownershipA+`' AND NOT EXISTS (SELECT 1 FROM chat.active_ownership_participants WHERE kind='`+kind+`' AND conversation_id=`+id+` AND user_id<>OLD.user_id AND role='owner') THEN RAISE EXCEPTION 'missing owner before departure'; END IF;
 IF TG_OP='DELETE' THEN RETURN OLD; END IF; RETURN NEW; END $$;
 CREATE TRIGGER assert_predeparture_owner BEFORE `+event+` ON `+table+` FOR EACH ROW EXECUTE FUNCTION chat.assert_predeparture_owner()`)
}

func TestOwnershipSuccessionEligibilityPostgreSQL(t *testing.T) {
	for _, kind := range []string{"dm", "channel"} {
		for _, setup := range []string{
			`UPDATE chat.workspace_members SET role='guest' WHERE user_id='` + ownershipB + `'`,
			`UPDATE auth.users SET status='suspended' WHERE id='` + ownershipB + `'`,
			`UPDATE auth.users SET status='locked' WHERE id='` + ownershipB + `'`,
			`UPDATE auth.users SET status='invited' WHERE id='` + ownershipB + `'`,
			`UPDATE auth.users SET status='deleted' WHERE id='` + ownershipB + `'`,
			`UPDATE auth.users SET deleted_at=now() WHERE id='` + ownershipB + `'`,
			`UPDATE chat.workspace_members SET status='suspended' WHERE user_id='` + ownershipB + `'`,
			`UPDATE chat.workspace_members SET status='left' WHERE user_id='` + ownershipB + `'`,
			inactiveSuccessionMember(kind, ownershipB),
		} {
			t.Run(kind+"/"+setup, func(t *testing.T) {
				pool := ownershipPool(t)
				enableOwnership(t, pool)
				successionMembers(t, pool, kind, `UPDATE MEMBERS SET ownership_role='admin' WHERE user_id='`+ownershipB+`'`)
				ownershipExec(t, pool, setup)
				input := successionInput(kind, ownershipA)
				if _, err := storage.NewPGXOwnershipStore(pool).Mutate(t.Context(), input); err != nil {
					t.Fatal(err)
				}
				assertOwnershipRole(t, pool, kind, input.Scope.ConversationID, ownershipC, "owner")
				assertNoOrphans(t, pool)
			})
		}
	}
}

func inactiveSuccessionMember(kind, user string) string {
	if kind == "dm" {
		return `UPDATE chat.dm_members SET status='left',left_at=now() WHERE user_id='` + user + `'`
	}
	return `DELETE FROM chat.channel_members WHERE user_id='` + user + `'`
}

func TestOwnershipSuccessionEmptyAndGuestRollbackPostgreSQL(t *testing.T) {
	for _, kind := range []string{"dm", "channel"} {
		for _, guests := range []bool{false, true} {
			t.Run(kind+"/"+map[bool]string{false: "empty", true: "guests"}[guests], func(t *testing.T) {
				testSuccessionEmptyAndGuests(t, kind, guests)
			})
		}
	}
}

func testSuccessionEmptyAndGuests(t *testing.T, kind string, guests bool) {
	t.Helper()
	pool := ownershipPool(t)
	enableOwnership(t, pool)
	if guests {
		ownershipExec(t, pool, `UPDATE chat.workspace_members SET role='guest' WHERE user_id<>$1`, ownershipA)
	} else {
		ownershipExec(t, pool, inactiveSuccessionMember(kind, ownershipB))
		ownershipExec(t, pool, inactiveSuccessionMember(kind, ownershipC))
	}
	audit, outbox := roleEffects(t, pool)
	_, err := storage.NewPGXOwnershipStore(pool).Mutate(t.Context(), successionInput(kind, ownershipA))
	if guests {
		if !errors.Is(err, domain.ErrOwnershipConflict) {
			t.Fatalf("err=%v", err)
		}
		assertStoredActorRole(t, pool, kind)
		nextAudit, nextOutbox := roleEffects(t, pool)
		if audit != nextAudit || outbox != nextOutbox {
			t.Fatal("conflict retained effects")
		}
	} else if err != nil {
		t.Fatal(err)
	}
	assertNoOrphans(t, pool)
}

func TestOwnershipChannelRejoinRenewsSeniorityPostgreSQL(t *testing.T) {
	pool := ownershipPool(t)
	enableOwnership(t, pool)
	ownershipExec(t, pool, `SELECT chat.assign_ownership('channel',$1,$2,'admin',$3,'manual')`, ownershipChannel, ownershipC, ownershipA)
	ownershipExec(t, pool, `DELETE FROM chat.channel_members WHERE channel_id=$1 AND user_id=$2`, ownershipChannel, ownershipC)
	ownershipExec(t, pool, `INSERT INTO chat.channel_members(channel_id,user_id,role) VALUES ($1,$2,'member')`, ownershipChannel, ownershipC)
	assertOwnershipRole(t, pool, "channel", ownershipChannel, ownershipC, "member")
	ownershipExec(t, pool, `UPDATE chat.channel_members SET ownership_role='member' WHERE channel_id=$1 AND user_id=$2`, ownershipChannel, ownershipB)
	if _, err := storage.NewPGXOwnershipStore(pool).Mutate(t.Context(), successionInput("channel", ownershipA)); err != nil {
		t.Fatal(err)
	}
	assertOwnershipRole(t, pool, "channel", ownershipChannel, ownershipB, "owner")
}

// The first transaction holds the actual conversation lock until the second
// transaction reaches its lock statement. Every retry uses a fresh transaction.
type successionBarrierPool struct {
	*pgxpool.Pool
	reached chan struct{}
	release <-chan struct{}
	after   bool
	once    sync.Once
}

func (p *successionBarrierPool) Begin(ctx context.Context) (pgx.Tx, error) {
	tx, err := p.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return &successionBarrierTx{Tx: tx, pool: p}, nil
}

type successionBarrierTx struct {
	pgx.Tx
	pool *successionBarrierPool
}

func (tx *successionBarrierTx) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	if !strings.Contains(sql, "SELECT chat.lock_ownership_conversation") && !strings.Contains(sql, "SELECT chat.lock_user_ownership_conversations") {
		return tx.Tx.Exec(ctx, sql, args...)
	}
	if !tx.pool.after {
		tx.pool.once.Do(func() { close(tx.pool.reached) })
	}
	tag, err := tx.Tx.Exec(ctx, sql, args...)
	if err == nil && tx.pool.after {
		tx.pool.once.Do(func() {
			close(tx.pool.reached)
			select {
			case <-tx.pool.release:
			case <-ctx.Done():
			}
		})
	}
	return tag, err
}

func TestOwnershipSuccessionOrderedConcurrencyPostgreSQL(t *testing.T) {
	for _, kind := range []string{"dm", "channel"} {
		for _, scenario := range []string{"two owners", "remove candidate"} {
			for _, reverse := range []bool{false, true} {
				t.Run(kind+"/"+scenario+"/"+map[bool]string{false: "first", true: "reverse"}[reverse], func(t *testing.T) { testSuccessionRace(t, kind, scenario, reverse) })
			}
		}
	}
}

func testSuccessionRace(t *testing.T, kind, scenario string, reverse bool) {
	t.Helper()
	pool := ownershipPool(t)
	enableOwnership(t, pool)
	first := successionInput(kind, ownershipA)
	second := successionInput(kind, ownershipB)
	if scenario == "two owners" {
		ownershipExec(t, pool, `SELECT chat.assign_ownership($1,$2,$3,'owner',$4,'manual')`, kind, first.Scope.ConversationID, ownershipB, ownershipA)
	} else {
		successionMembers(t, pool, kind, `UPDATE MEMBERS SET ownership_role='admin' WHERE user_id<>'`+ownershipA+`'; UPDATE MEMBERS SET joined_at='2022-01-01' WHERE user_id='`+ownershipC+`'`)
		second = successionInput(kind, ownershipA)
		second.Operation = "remove"
		second.TargetUserID = ownershipB
	}
	if reverse {
		first, second = second, first
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	release := make(chan struct{})
	locked, attempted := make(chan struct{}), make(chan struct{})
	firstPool := &successionBarrierPool{Pool: pool, reached: locked, release: release, after: true}
	secondPool := &successionBarrierPool{Pool: pool, reached: attempted}
	results := make(chan error, 2)
	go func() { _, err := storage.NewPGXOwnershipStore(firstPool).Mutate(ctx, first); results <- err }()
	awaitSuccessionBarrier(t, ctx, locked)
	go func() { _, err := storage.NewPGXOwnershipStore(secondPool).Mutate(ctx, second); results <- err }()
	awaitSuccessionBarrier(t, ctx, attempted)
	close(release)
	forbidden := collectSuccessionOutcomes(t, ctx, results)
	assertSuccessionRaceState(t, pool, kind, scenario, reverse, forbidden)
}

func collectSuccessionOutcomes(t *testing.T, ctx context.Context, results <-chan error) int {
	t.Helper()
	forbidden := 0
	for range 2 {
		select {
		case err := <-results:
			if errors.Is(err, domain.ErrForbidden) || errors.Is(err, domain.ErrNotFound) {
				forbidden++
			} else if err != nil {
				t.Fatal(err)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	return forbidden
}

func assertSuccessionRaceState(t *testing.T, pool *pgxpool.Pool, kind, scenario string, reverse bool, forbidden int) {
	t.Helper()
	want := ownershipC
	if scenario == "remove candidate" && !reverse {
		want = ownershipB
		if forbidden != 1 {
			t.Fatalf("forbidden=%d", forbidden)
		}
	} else if forbidden != 0 {
		t.Fatalf("forbidden=%d", forbidden)
	}
	assertOwnershipRole(t, pool, kind, successionInput(kind, ownershipA).Scope.ConversationID, want, "owner")
	if scenario == "remove candidate" && reverse {
		var count int
		if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM chat.ownership_audit WHERE target_user_id=$1 AND reason='succession' AND conversation_kind=$2`, ownershipB, kind).Scan(&count); err != nil || count != 0 {
			t.Fatalf("removed candidate promoted: count=%d err=%v", count, err)
		}
	}
	assertNoOrphans(t, pool)
}
func awaitSuccessionBarrier(t *testing.T, ctx context.Context, barrier <-chan struct{}) {
	t.Helper()
	select {
	case <-barrier:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}

func TestOwnershipDirectDemotionCommitPostgreSQL(t *testing.T) {
	for _, kind := range []string{"dm", "channel"} {
		t.Run(kind, func(t *testing.T) {
			pool := ownershipPool(t)
			enableOwnership(t, pool)
			tx, err := pool.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback(t.Context()) }()
			table, id := "chat.dm_members", "conversation_id"
			if kind == "channel" {
				table, id = "chat.channel_members", "channel_id"
			}
			if _, err = tx.Exec(t.Context(), `UPDATE `+table+` SET ownership_role='member' WHERE `+id+`=$1 AND user_id=$2`, successionInput(kind, ownershipA).Scope.ConversationID, ownershipA); err != nil {
				t.Fatal(err)
			}
			assertOwnerConflict(t, tx.Commit(t.Context()))
			assertStoredActorRole(t, pool, kind)
			assertNoOrphans(t, pool)
		})
	}
}

func TestOwnershipSuccessionRollbackPostgreSQL(t *testing.T) {
	for _, kind := range []string{"dm", "channel"} {
		t.Run(kind, func(t *testing.T) {
			testSuccessionRollback(t, kind)
		})
	}
}

func testSuccessionRollback(t *testing.T, kind string) {
	t.Helper()
	pool := ownershipPool(t)
	enableOwnership(t, pool)
	table, event := "chat.dm_members", "UPDATE OF status"
	if kind == "channel" {
		table, event = "chat.channel_members", "DELETE"
	}
	ownershipExec(t, pool, `CREATE FUNCTION chat.fail_succession_departure() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected departure failure'; END $$;
 CREATE TRIGGER fail_succession_departure BEFORE `+event+` ON `+table+` FOR EACH ROW EXECUTE FUNCTION chat.fail_succession_departure()`)
	audit, outbox := roleEffects(t, pool)
	var eventsBefore, eventsAfter int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM chat.messages`).Scan(&eventsBefore); err != nil {
		t.Fatal(err)
	}
	input := successionInput(kind, ownershipA)
	_, err := storage.NewPGXOwnershipStore(pool).Mutate(t.Context(), input)
	if err == nil || !strings.Contains(err.Error(), "injected departure failure") {
		t.Fatalf("err=%v", err)
	}
	assertStoredActorRole(t, pool, kind)
	role := "member"
	if kind == "channel" {
		role = "admin"
	}
	assertOwnershipRole(t, pool, kind, input.Scope.ConversationID, ownershipB, role)
	assertOwnershipRole(t, pool, kind, input.Scope.ConversationID, ownershipC, "member")
	nextAudit, nextOutbox := roleEffects(t, pool)
	if audit != nextAudit || outbox != nextOutbox {
		t.Fatal("rollback retained audit/outbox")
	}
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM chat.messages`).Scan(&eventsAfter); err != nil || eventsBefore != eventsAfter {
		t.Fatalf("rollback retained event: before=%d after=%d err=%v", eventsBefore, eventsAfter, err)
	}
	assertNoOrphans(t, pool)
}
