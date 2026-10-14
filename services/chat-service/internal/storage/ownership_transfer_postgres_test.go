package storage_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

func transferInput(kind string) storage.OwnershipMutation {
	input := roleInput(kind, domain.ConversationMember)
	input.Operation = "transfer"
	input.IdempotencyKey = "transfer-1045"
	if kind == "channel" {
		input.Scope.ConversationID = ownershipChannel
	}
	return input
}

func assertOwnershipRequests(t *testing.T, pool *pgxpool.Pool, want int) {
	t.Helper()
	var count int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM chat.ownership_requests`).Scan(&count); err != nil || count != want {
		t.Fatalf("requests=%d want=%d err=%v", count, want, err)
	}
}

func TestOwnershipTransferMatrixPostgreSQL(t *testing.T) {
	for _, kind := range []string{"dm", "channel"} {
		for _, before := range []domain.ConversationRole{domain.ConversationMember, domain.ConversationAdmin, domain.ConversationOwner} {
			for _, after := range []domain.ConversationRole{domain.ConversationAdmin, domain.ConversationMember} {
				t.Run(kind+"/"+string(before)+"/"+string(after), func(t *testing.T) { testTransferTransition(t, kind, before, after) })
			}
		}
	}
}

func testTransferTransition(t *testing.T, kind string, before, after domain.ConversationRole) {
	t.Helper()
	pool := ownershipPool(t)
	enableOwnership(t, pool)
	input := transferInput(kind)
	input.Role = after
	ownershipExec(t, pool, `SELECT chat.assign_ownership($1,$2,$3,$4,$5,'manual')`, kind, input.Scope.ConversationID, ownershipB, string(before), ownershipA)
	audit, outbox := roleEffects(t, pool)
	result, err := storage.NewPGXOwnershipStore(pool).Mutate(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	assertTransferResult(t, result)
	assertOwnershipRole(t, pool, kind, input.Scope.ConversationID, ownershipA, string(after))
	assertOwnershipRole(t, pool, kind, input.Scope.ConversationID, ownershipB, "owner")
	assertNoOrphans(t, pool)
	writes := 2
	if before == domain.ConversationOwner {
		writes = 1
	}
	assertTransferEffects(t, pool, audit+writes, outbox+1, 1)
}

func assertTransferResult(t *testing.T, result storage.OwnershipMutationResult) {
	t.Helper()
	if result.Role != domain.ConversationOwner || result.TargetUserID != ownershipB || result.Left {
		t.Fatalf("result=%+v", result)
	}
}

func assertTransferEffects(t *testing.T, pool *pgxpool.Pool, audit, outbox, requests int) {
	t.Helper()
	nextAudit, nextOutbox := roleEffects(t, pool)
	if nextAudit != audit || nextOutbox != outbox {
		t.Fatalf("effects audit=%d want=%d outbox=%d want=%d", nextAudit, audit, nextOutbox, outbox)
	}
	assertOwnershipRequests(t, pool, requests)
}

type transferDenial struct {
	name, sql, actor, target, workspace string
	want                                error
}

func TestOwnershipTransferRevalidationPostgreSQL(t *testing.T) {
	for _, kind := range []string{"dm", "channel"} {
		for _, scenario := range transferDenials(kind) {
			t.Run(kind+"/"+scenario.name, func(t *testing.T) { testTransferDenial(t, kind, scenario) })
		}
	}
}

func transferDenials(kind string) []transferDenial {
	resourceTable := "chat.dm_conversations"
	if kind == "channel" {
		resourceTable = "chat.channels"
	}
	return []transferDenial{
		{name: "target-removed", sql: `UPDATE chat.workspace_members SET status='left' WHERE user_id='` + ownershipB + `'`, want: domain.ErrNotFound},
		{name: "self", target: ownershipA, want: domain.ErrForbidden},
		{name: "actor-non-owner", actor: ownershipB, target: ownershipC, want: domain.ErrForbidden},
		{name: "cross-workspace", workspace: "00000000-0000-0000-0000-000000000002", want: domain.ErrNotFound},
		{name: "archived", sql: `UPDATE ` + resourceTable + ` SET status='archived' WHERE id='` + transferInput(kind).Scope.ConversationID + `'`, want: domain.ErrNotFound},
		{name: "workspace-inactive", sql: `UPDATE chat.workspaces SET status='disabled'`, want: domain.ErrNotFound},
		{name: "target-suspended", sql: `UPDATE auth.users SET status='suspended' WHERE id='` + ownershipB + `'`, want: domain.ErrNotFound},
		{name: "rollout-disabled", sql: `UPDATE chat.ownership_rollout SET enabled=false WHERE singleton`, want: domain.ErrForbidden},
	}
}

func testTransferDenial(t *testing.T, kind string, scenario transferDenial) {
	t.Helper()
	pool := ownershipPool(t)
	enableOwnership(t, pool)
	input := transferInput(kind)
	if scenario.sql != "" {
		ownershipExec(t, pool, scenario.sql)
	}
	if scenario.actor != "" {
		input.Scope.ActorID = scenario.actor
	}
	if scenario.target != "" {
		input.TargetUserID = scenario.target
	}
	if scenario.workspace != "" {
		input.Scope.WorkspaceID = scenario.workspace
	}
	audit, outbox := roleEffects(t, pool)
	_, err := storage.NewPGXOwnershipStore(pool).Mutate(t.Context(), input)
	if !errors.Is(err, scenario.want) {
		t.Fatalf("err=%v want=%v", err, scenario.want)
	}
	assertTransferEffects(t, pool, audit, outbox, 0)
	assertStoredActorRole(t, pool, kind)
}

func assertStoredActorRole(t *testing.T, pool *pgxpool.Pool, kind string) {
	t.Helper()
	var role string
	query := `SELECT ownership_role FROM chat.dm_members WHERE conversation_id=$1 AND user_id=$2`
	if kind == "channel" {
		query = `SELECT ownership_role FROM chat.channel_members WHERE channel_id=$1 AND user_id=$2`
	}
	if err := pool.QueryRow(t.Context(), query, transferInput(kind).Scope.ConversationID, ownershipA).Scan(&role); err != nil || role != "owner" {
		t.Fatalf("actor role=%s err=%v", role, err)
	}
}

func TestOwnershipTransferPublicResourcePostgreSQL(t *testing.T) {
	testTransferDenial(t, "channel", transferDenial{name: "public", sql: `UPDATE chat.channels SET type='public'`, want: domain.ErrNotFound})
}

func TestOwnershipTransferRollbackPostgreSQL(t *testing.T) {
	for _, kind := range []string{"dm", "channel"} {
		t.Run(kind, func(t *testing.T) { testTransferRollback(t, kind) })
	}
}

func testTransferRollback(t *testing.T, kind string) {
	t.Helper()
	pool := ownershipPool(t)
	enableOwnership(t, pool)
	table := "chat.dm_members"
	if kind == "channel" {
		table = "chat.channel_members"
	}
	// Target promotion and its audit/outbox happen first; fail the actor update.
	ownershipExec(t, pool, `CREATE FUNCTION chat.fail_transfer_actor() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.user_id='95300000-0000-4000-8000-00000000000a' THEN RAISE EXCEPTION 'injected actor failure'; END IF; RETURN NEW; END $$;
 CREATE TRIGGER fail_transfer_actor BEFORE UPDATE ON `+table+` FOR EACH ROW EXECUTE FUNCTION chat.fail_transfer_actor()`)
	audit, outbox := roleEffects(t, pool)
	input := transferInput(kind)
	_, err := storage.NewPGXOwnershipStore(pool).Mutate(t.Context(), input)
	if err == nil || !strings.Contains(err.Error(), "injected actor failure") {
		t.Fatalf("expected injected actor failure: %v", err)
	}
	assertOwnershipRole(t, pool, kind, input.Scope.ConversationID, ownershipA, "owner")
	targetRole := "member"
	if kind == "channel" {
		targetRole = "admin"
	}
	assertOwnershipRole(t, pool, kind, input.Scope.ConversationID, ownershipB, targetRole)
	nextAudit, nextOutbox := roleEffects(t, pool)
	if audit != nextAudit || outbox != nextOutbox {
		t.Fatal("rollback retained effects")
	}
	assertOwnershipRequests(t, pool, 0)
	assertNoOrphans(t, pool)
}

type transferOutcome struct {
	target string
	err    error
}

func TestOwnershipConcurrentTransfersPostgreSQL(t *testing.T) {
	for _, kind := range []string{"dm", "channel"} {
		t.Run(kind, func(t *testing.T) { testConcurrentTransfers(t, kind) })
	}
}

func testConcurrentTransfers(t *testing.T, kind string) {
	t.Helper()
	pool := ownershipPool(t)
	enableOwnership(t, pool)
	store := storage.NewPGXOwnershipStore(pool)
	first := transferInput(kind)
	second := first
	second.TargetUserID = ownershipC
	second.IdempotencyKey = "second-transfer"
	start := make(chan struct{})
	results := make(chan transferOutcome, 2)
	for _, input := range []storage.OwnershipMutation{first, second} {
		go func() {
			<-start
			_, err := store.Mutate(t.Context(), input)
			results <- transferOutcome{input.TargetUserID, err}
		}()
	}
	close(start)
	winners := assertTransferOutcomes(t, pool, first, results)
	if winners != 1 {
		t.Fatalf("winners=%d", winners)
	}
	assertOwnershipRole(t, pool, kind, first.Scope.ConversationID, ownershipA, "member")
	assertOwnershipRequests(t, pool, 1)
	assertNoOrphans(t, pool)
}

func assertTransferOutcomes(t *testing.T, pool *pgxpool.Pool, input storage.OwnershipMutation, results <-chan transferOutcome) int {
	t.Helper()
	kind := input.Scope.Kind
	winners := 0
	for range 2 {
		result := <-results
		role := "member"
		if kind == "channel" && result.target == ownershipB {
			role = "admin"
		}
		if result.err == nil {
			winners++
			role = "owner"
		} else if !errors.Is(result.err, domain.ErrForbidden) {
			t.Fatal(result.err)
		}
		assertOwnershipRole(t, pool, kind, input.Scope.ConversationID, result.target, role)
	}
	return winners
}

func assertTransferReplay(t *testing.T, pool *pgxpool.Pool, store *storage.PGXOwnershipStore, input storage.OwnershipMutation, first storage.OwnershipMutationResult) {
	t.Helper()
	audit, outbox := roleEffects(t, pool)
	result, err := store.Mutate(t.Context(), input)
	if err != nil || !result.Replayed {
		t.Fatalf("replay=%+v err=%v", result, err)
	}
	result.Replayed = false
	if result != first {
		t.Fatalf("replay response changed: first=%+v replay=%+v", first, result)
	}
	changedRole := input
	changedRole.Role = domain.ConversationAdmin
	changedTarget := input
	changedTarget.TargetUserID = ownershipC
	for _, conflict := range []storage.OwnershipMutation{changedRole, changedTarget} {
		if _, err := store.Mutate(t.Context(), conflict); !errors.Is(err, domain.ErrOwnershipConflict) {
			t.Fatalf("key reuse=%v", err)
		}
	}
	assertTransferEffects(t, pool, audit, outbox, 1)
}

func runOwnershipMutationRace(t *testing.T, first, second storage.OwnershipMutation) *pgxpool.Pool {
	t.Helper()
	pool := ownershipPool(t)
	enableOwnership(t, pool)
	store := storage.NewPGXOwnershipStore(pool)
	scope := storage.OwnershipScope{WorkspaceID: ownershipWS, Kind: "dm", ConversationID: ownershipDM, ActorID: ownershipA}
	results := make(chan error, 2)
	for _, input := range []storage.OwnershipMutation{first, second} {
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

	return pool
}

func assertTransferRemoveState(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	var status string
	if err := pool.QueryRow(t.Context(), `SELECT status FROM chat.dm_members WHERE conversation_id=$1 AND user_id=$2`, ownershipDM, ownershipB).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status == "left" {
		assertOwnershipRole(t, pool, "dm", ownershipDM, ownershipA, "owner")
		assertOwnershipRequests(t, pool, 0)
	} else {
		assertOwnershipRole(t, pool, "dm", ownershipDM, ownershipA, "member")
		assertOwnershipRole(t, pool, "dm", ownershipDM, ownershipB, "owner")
		assertOwnershipRequests(t, pool, 1)
	}
}
