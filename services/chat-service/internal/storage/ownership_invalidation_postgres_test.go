package storage_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nicrepository/nchat/libs/go/platform/conversationownership"
	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

func invalidationSession(tx pgx.Tx) conversationownership.Session {
	return conversationownership.Session{
		Query: func(ctx context.Context, sql string, args ...any) (conversationownership.Rows, error) {
			return tx.Query(ctx, sql, args...)
		},
		Exec: func(ctx context.Context, sql string, args ...any) error {
			_, err := tx.Exec(ctx, sql, args...)
			return err
		},
	}
}

type invalidationPool interface {
	Begin(context.Context) (pgx.Tx, error)
}

// Lifecycle writers use this protocol; raw SQL cases deliberately skip the
// coordinator to exercise the existing guards rather than an invented endpoint.
func invalidateOwnership(ctx context.Context, pool invalidationPool, user, workspace, mutation string, coordinate bool) error {
	_, err := conversationownership.Retry(ctx, func() (bool, error) { return invalidateOwnershipOnce(ctx, pool, user, workspace, mutation, coordinate) })
	return err
}

func invalidateOwnershipOnce(ctx context.Context, pool invalidationPool, user, workspace, mutation string, coordinate bool) (bool, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, conversationownership.SerializableSQL); err != nil {
		return false, err
	}
	if _, err = tx.Exec(ctx, `SELECT chat.lock_user_ownership_conversations($1::uuid,NULLIF($2,'')::uuid)`, user, workspace); err != nil {
		return false, err
	}
	if err = lockInvalidationRecord(ctx, tx, user, workspace); err != nil {
		return false, err
	}
	if coordinate {
		if err = conversationownership.Invalidate(ctx, invalidationSession(tx), user, workspace); err != nil {
			return false, err
		}
	}
	if _, err = tx.Exec(ctx, mutation, user, workspace); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}

func lockInvalidationRecord(ctx context.Context, tx pgx.Tx, user, workspace string) error {
	var status string
	if workspace == "" {
		return tx.QueryRow(ctx, `SELECT status FROM auth.users WHERE id=$1 FOR UPDATE`, user).Scan(&status)
	}
	return tx.QueryRow(ctx, `SELECT status FROM chat.workspace_members WHERE user_id=$1 AND workspace_id=$2 FOR UPDATE`, user, workspace).Scan(&status)
}

const suspendAccountSQL = `UPDATE auth.users SET status='suspended' WHERE id=$1 AND $2::text IS NOT NULL`
const suspendWorkspaceSQL = `UPDATE chat.workspace_members SET status='suspended' WHERE user_id=$1 AND workspace_id=$2::uuid`
const removeWorkspaceSQL = `DELETE FROM chat.workspace_members WHERE user_id=$1 AND workspace_id=$2::uuid`

func TestOwnershipInvalidationEventsPostgreSQL(t *testing.T) {
	for _, tc := range []struct {
		name, workspace, sql string
		coordinate           bool
	}{
		{"account suspended", "", suspendAccountSQL, true},
		{"workspace suspended", ownershipWS, suspendWorkspaceSQL, false},
		{"workspace left", ownershipWS, `UPDATE chat.workspace_members SET status='left' WHERE user_id=$1 AND workspace_id=$2::uuid`, false},
		{"workspace deleted", ownershipWS, removeWorkspaceSQL, false},
		{"account locked", "", `UPDATE auth.users SET status='locked' WHERE id=$1 AND $2::text IS NOT NULL`, false},
		{"account deleted", "", `UPDATE auth.users SET status='deleted' WHERE id=$1 AND $2::text IS NOT NULL`, false},
		{"account soft deleted", "", `UPDATE auth.users SET deleted_at=now() WHERE id=$1 AND $2::text IS NOT NULL`, false},
		{"account hard deleted", "", `DELETE FROM auth.users WHERE id=$1 AND $2::text IS NOT NULL`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := ownershipPool(t)
			enableOwnership(t, pool)
			if err := invalidateOwnership(t.Context(), pool, ownershipA, tc.workspace, tc.sql, tc.coordinate); err != nil {
				t.Fatal(err)
			}
			assertOwnershipRole(t, pool, "channel", ownershipChannel, ownershipB, "owner")
			assertOwnershipRole(t, pool, "dm", ownershipDM, ownershipC, "owner")
			assertNoOrphans(t, pool)
		})
	}
}

const invalidationThirdDM = "10470000-0000-4000-8000-000000000003"
const invalidationOtherDM = "10470000-0000-4000-8000-000000000004"
const invalidationOtherWS = "10470000-0000-4000-8000-000000000005"

func invalidationMultipleFixture(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ownershipExec(t, pool, `WITH workspace AS (INSERT INTO chat.workspaces(id,slug,name) VALUES($1,'other-1047','Other') RETURNING id) INSERT INTO chat.channels(workspace_id,slug,display_name,type,is_general) SELECT id,'geral','Geral','public',true FROM workspace`, invalidationOtherWS)
	ownershipExec(t, pool, `INSERT INTO chat.workspace_members(workspace_id,user_id) VALUES($1,$2),($1,$3)`, invalidationOtherWS, ownershipA, ownershipB)
	ownershipExec(t, pool, `INSERT INTO chat.dm_conversations(id,workspace_id,type,created_by) VALUES($1,$3,'group',$4),($2,$5,'group',$4)`, invalidationThirdDM, invalidationOtherDM, ownershipWS, ownershipA, invalidationOtherWS)
	ownershipExec(t, pool, `INSERT INTO chat.dm_members(conversation_id,user_id) VALUES($1,$3),($1,$4),($2,$3),($2,$4)`, invalidationThirdDM, invalidationOtherDM, ownershipA, ownershipB)
	ownershipExec(t, pool, `SELECT chat.assign_ownership('dm',$1,$2,'owner',NULL,'manual')`, invalidationThirdDM, ownershipB)
}

func TestOwnershipInvalidationMultipleAndRollbackPostgreSQL(t *testing.T) {
	for _, mode := range []string{"success", "no successor", "failure after promotion"} {
		t.Run(mode, func(t *testing.T) { testInvalidationMultiple(t, mode) })
	}
}

func testInvalidationMultiple(t *testing.T, mode string) {
	t.Helper()
	pool := ownershipPool(t)
	enableOwnership(t, pool)
	invalidationMultipleFixture(t, pool)
	switch mode {
	case "no successor":
		ownershipExec(t, pool, `UPDATE chat.workspace_members SET role='guest' WHERE workspace_id=$1 AND user_id<>$2`, ownershipWS, ownershipA)
	case "failure after promotion":
		ownershipExec(t, pool, `CREATE FUNCTION chat.fail_invalidation() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected invalidation failure'; END $$;
 CREATE TRIGGER fail_invalidation BEFORE UPDATE OF status ON chat.workspace_members FOR EACH ROW EXECUTE FUNCTION chat.fail_invalidation()`)
	}
	assertOwnershipRole(t, pool, "dm", invalidationThirdDM, ownershipB, "owner")
	audit, outbox := roleEffects(t, pool)
	err := invalidateOwnership(t.Context(), pool, ownershipA, ownershipWS, suspendWorkspaceSQL, true)
	if mode == "success" {
		if err != nil {
			t.Fatal(err)
		}
		assertOwnershipRole(t, pool, "channel", ownershipChannel, ownershipB, "owner")
		assertOwnershipRole(t, pool, "dm", ownershipDM, ownershipC, "owner")
		assertOwnershipRole(t, pool, "dm", invalidationThirdDM, ownershipB, "owner")
	} else {
		assertInvalidationRollback(t, pool, mode, err, audit, outbox)
	}
	assertOwnershipRole(t, pool, "dm", invalidationOtherDM, ownershipA, "owner")
	var extra int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM chat.ownership_audit WHERE conversation_id IN ($1,$2) AND reason='invalidation' AND new_role='owner'`, invalidationThirdDM, invalidationOtherDM).Scan(&extra); err != nil || extra != 0 {
		t.Fatalf("unnecessary or cross-workspace promotion=%d err=%v", extra, err)
	}
	assertNoOrphans(t, pool)
}

func assertInvalidationRollback(t *testing.T, pool *pgxpool.Pool, mode string, err error, audit, outbox int) {
	t.Helper()
	if mode == "no successor" {
		if conversationownership.SQLState(err) != "P0953" {
			t.Fatalf("err=%v", err)
		}
	} else if err == nil || !strings.Contains(err.Error(), "injected invalidation failure") {
		t.Fatalf("err=%v", err)
	}
	for _, scope := range []conversationownership.Scope{{Kind: "dm", ConversationID: ownershipDM}, {Kind: "channel", ConversationID: ownershipChannel}, {Kind: "dm", ConversationID: invalidationThirdDM}} {
		assertOwnershipRole(t, pool, scope.Kind, scope.ConversationID, ownershipA, "owner")
	}
	nextAudit, nextOutbox := roleEffects(t, pool)
	if nextAudit != audit || nextOutbox != outbox {
		t.Fatal("partial role effects survived rollback")
	}
	var status string
	if err := pool.QueryRow(t.Context(), `SELECT status FROM chat.workspace_members WHERE workspace_id=$1 AND user_id=$2`, ownershipWS, ownershipA).Scan(&status); err != nil || status != "active" {
		t.Fatalf("status=%s err=%v", status, err)
	}
}

func TestOwnershipInvalidationWorkspaceLifecyclePostgreSQL(t *testing.T) {
	pool := ownershipPool(t)
	enableOwnership(t, pool)
	beforeAudit, beforeOutbox := roleEffects(t, pool)
	ownershipExec(t, pool, `UPDATE chat.workspaces SET status='disabled' WHERE id=$1`, ownershipWS)
	var active int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM chat.active_ownership_participants WHERE workspace_id=$1`, ownershipWS).Scan(&active); err != nil || active != 0 {
		t.Fatalf("active=%d err=%v", active, err)
	}
	audit, outbox := roleEffects(t, pool)
	if audit != beforeAudit || outbox != beforeOutbox {
		t.Fatal("workspace disable promoted a successor")
	}
	ownershipExec(t, pool, `UPDATE chat.workspaces SET status='active' WHERE id=$1`, ownershipWS)
	assertNoOrphans(t, pool)
	ownershipExec(t, pool, `UPDATE chat.workspaces SET status='disabled' WHERE id=$1`, ownershipWS)
	ownershipExec(t, pool, `UPDATE auth.users SET status='suspended' WHERE id=$1`, ownershipA)
	_, err := pool.Exec(t.Context(), `UPDATE chat.workspaces SET status='active' WHERE id=$1`, ownershipWS)
	assertOwnerConflict(t, err)
}

func TestOwnershipInvalidationOrderedConcurrencyPostgreSQL(t *testing.T) {
	for _, scenario := range []string{"leave suspend", "transfer remove actor", "transfer remove target", "candidate suspended"} {
		for _, reverse := range []bool{false, true} {
			t.Run(scenario+"/"+map[bool]string{false: "first", true: "reverse"}[reverse], func(t *testing.T) { testInvalidationRace(t, scenario, reverse) })
		}
	}
}

type invalidationOperation func(context.Context, *successionBarrierPool) error

func invalidationRaceOperations(scenario string) (invalidationOperation, invalidationOperation) {
	suspend := func(ctx context.Context, pool *successionBarrierPool) error {
		return invalidateOwnership(ctx, pool, ownershipA, "", suspendAccountSQL, true)
	}
	if scenario == "candidate suspended" {
		return suspend, func(ctx context.Context, pool *successionBarrierPool) error {
			return invalidateOwnership(ctx, pool, ownershipB, "", suspendAccountSQL, true)
		}
	}
	input := successionInput("dm", ownershipA)
	if scenario == "leave suspend" {
		return func(ctx context.Context, pool *successionBarrierPool) error {
			_, err := storage.NewPGXOwnershipStore(pool).Mutate(ctx, input)
			return err
		}, suspend
	}
	input.Role = domain.ConversationAdmin
	input.Operation = "transfer"
	input.TargetUserID = ownershipB
	input.IdempotencyKey = "1047-transfer"
	target := ownershipA
	if scenario == "transfer remove target" {
		target = ownershipB
	}
	return func(ctx context.Context, pool *successionBarrierPool) error {
			_, err := storage.NewPGXOwnershipStore(pool).Mutate(ctx, input)
			return err
		},
		func(ctx context.Context, pool *successionBarrierPool) error {
			return invalidateOwnership(ctx, pool, target, ownershipWS, removeWorkspaceSQL, false)
		}
}

func testInvalidationRace(t *testing.T, scenario string, reverse bool) {
	t.Helper()
	pool := ownershipPool(t)
	enableOwnership(t, pool)
	successionMembers(t, pool, "dm", `UPDATE MEMBERS SET ownership_role='admin' WHERE user_id='`+ownershipB+`'`)
	first, second := invalidationRaceOperations(scenario)
	if reverse {
		first, second = second, first
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	release, locked, attempted := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	firstPool := &successionBarrierPool{Pool: pool, reached: locked, release: release, after: true}
	secondPool := &successionBarrierPool{Pool: pool, reached: attempted}
	results := make(chan error, 2)
	go func() { results <- first(ctx, firstPool) }()
	awaitSuccessionBarrier(t, ctx, locked)
	go func() { results <- second(ctx, secondPool) }()
	awaitSuccessionBarrier(t, ctx, attempted)
	unblock()
	rejected := assertInvalidationRaceOutcomes(t, ctx, results, scenario)
	wantRejected := 0
	if reverse && scenario != "candidate suspended" {
		wantRejected = 1
	}
	if rejected != wantRejected {
		t.Fatalf("rejected=%d want=%d", rejected, wantRejected)
	}
	assertInvalidationRaceState(t, pool, scenario)
	assertNoOrphans(t, pool)
}

func assertInvalidationRaceOutcomes(t *testing.T, ctx context.Context, results <-chan error, scenario string) int {
	rejected := 0
	t.Helper()
	for range 2 {
		select {
		case err := <-results:
			if err != nil {
				rejected++
			}
			if err != nil && !allowedInvalidationRaceError(scenario, err) {
				t.Fatal(err)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	return rejected
}

func allowedInvalidationRaceError(scenario string, err error) bool {
	if scenario == "leave suspend" {
		return errors.Is(err, domain.ErrNotFound)
	}
	return strings.HasPrefix(scenario, "transfer") && (errors.Is(err, domain.ErrForbidden) || errors.Is(err, domain.ErrNotFound) || errors.Is(err, domain.ErrOwnershipConflict))
}

func assertInvalidationRaceState(t *testing.T, pool *pgxpool.Pool, scenario string) {
	t.Helper()
	want := ownershipB
	switch scenario {
	case "candidate suspended":
		want = ownershipC
	case "transfer remove target":
		want = ownershipA
	}
	assertOwnershipRole(t, pool, "dm", ownershipDM, want, "owner")
	if strings.HasPrefix(scenario, "transfer") {
		target := ownershipA
		if scenario == "transfer remove target" {
			target = ownershipB
		}
		var memberships int
		if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM chat.workspace_members WHERE workspace_id=$1 AND user_id=$2`, ownershipWS, target).Scan(&memberships); err != nil || memberships != 0 {
			t.Fatalf("removed membership count=%d err=%v", memberships, err)
		}
		return
	}
	var status string
	if err := pool.QueryRow(t.Context(), `SELECT status FROM auth.users WHERE id=$1`, ownershipA).Scan(&status); err != nil || status != "suspended" {
		t.Fatalf("account status=%s err=%v", status, err)
	}
}
