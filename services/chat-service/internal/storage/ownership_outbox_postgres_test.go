package storage_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nicrepository/nchat/services/chat-service/internal/domain"
	"github.com/nicrepository/nchat/services/chat-service/internal/storage"
)

func ownershipOutboxInput(kind string) storage.OwnershipMutation {
	input := roleInput(kind, domain.ConversationAdmin)
	if kind == "channel" {
		input.Scope.ConversationID = ownershipChannel
		input.Role = domain.ConversationMember
	}
	return input
}

func pendingOwnershipDelivery(t *testing.T, pool *pgxpool.Pool, attempts int, failure string) {
	t.Helper()
	var count int
	err := pool.QueryRow(t.Context(), `SELECT count(*) FROM chat.ownership_outbox
 WHERE published_at IS NULL AND attempt_count=$1 AND last_failure=$2
 AND last_attempt_at IS NOT NULL AND next_attempt_at>last_attempt_at`, attempts, failure).Scan(&count)
	if err != nil || count != 1 {
		t.Fatalf("pending delivery=%d err=%v", count, err)
	}
}

func assertOwnershipEffectsUnchanged(t *testing.T, pool *pgxpool.Pool, audit, outbox int) {
	t.Helper()
	gotAudit, gotOutbox := roleEffects(t, pool)
	if gotAudit != audit || gotOutbox != outbox {
		t.Fatalf("delivery repeated domain effects: %d/%d -> %d/%d", audit, outbox, gotAudit, gotOutbox)
	}
}

func TestOwnershipOutboxTransactionPostgreSQL(t *testing.T) {
	for _, kind := range []string{"dm", "channel"} {
		t.Run(kind, func(t *testing.T) { testOwnershipOutboxTransaction(t, kind) })
	}
}

func testOwnershipOutboxTransaction(t *testing.T, kind string) {
	pool := ownershipPool(t)
	enableOwnership(t, pool)
	ownershipExec(t, pool, `DELETE FROM chat.ownership_outbox`)
	input := ownershipOutboxInput(kind)
	audit, outbox := roleEffects(t, pool)
	tx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(t.Context()) }()
	_, err = tx.Exec(t.Context(), `SELECT chat.assign_ownership($1,$2,$3,$4,$5,'manual')`, kind, input.Scope.ConversationID, ownershipB, string(input.Role), ownershipA)
	if err != nil {
		t.Fatal(err)
	}
	published := 0
	store := storage.NewPGXOwnershipStore(pool)
	if err := store.DispatchOwnershipChanges(t.Context(), func(context.Context, string, string, string) error { published++; return nil }); err != nil || published != 0 {
		t.Fatalf("uncommitted published=%d err=%v", published, err)
	}
	if err := tx.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertOwnershipEffectsUnchanged(t, pool, audit, outbox)
	assertOwnershipMutationCommitted(t, pool, input, audit, outbox)
	assertOwnershipRefetchAuthorization(t, pool, input.Scope)
}

func assertOwnershipMutationCommitted(t *testing.T, pool *pgxpool.Pool, input storage.OwnershipMutation, audit, outbox int) {
	t.Helper()
	if _, err := storage.NewPGXOwnershipStore(pool).Mutate(t.Context(), input); err != nil {
		t.Fatal(err)
	}
	nextAudit, nextOutbox := roleEffects(t, pool)
	if nextAudit != audit+1 || nextOutbox != outbox+1 {
		t.Fatalf("committed effects=%d/%d", nextAudit, nextOutbox)
	}
	var source, operation, result, conversationType string
	err := pool.QueryRow(t.Context(), `SELECT source,operation,result,conversation_type FROM chat.ownership_audit ORDER BY id DESC LIMIT 1`).Scan(&source, &operation, &result, &conversationType)
	wantType := "group"
	if input.Scope.Kind == "channel" {
		wantType = "private_channel"
	}
	if err != nil || source != "manual" || operation != "change_role" || result != "success" || conversationType != wantType {
		t.Fatalf("metadata=%s/%s/%s/%s err=%v", source, operation, result, conversationType, err)
	}
}

func assertOwnershipRefetchAuthorization(t *testing.T, pool *pgxpool.Pool, scope storage.OwnershipScope) {
	t.Helper()
	store := storage.NewPGXOwnershipStore(pool)
	scope.ActorID = ownershipB
	projection, err := store.Details(t.Context(), scope)
	if err != nil || len(projection.Members) == 0 {
		t.Fatalf("authorized refetch=%+v err=%v", projection, err)
	}
	other := scope
	other.WorkspaceID = "00000000-0000-0000-0000-000000000002"
	assertOwnershipReadDenied(t, store, other)
	other = scope
	other.ConversationID = "00000000-0000-0000-0000-000000000099"
	assertOwnershipReadDenied(t, store, other)
	ownershipExec(t, pool, `UPDATE chat.workspace_members SET status='left' WHERE user_id=$1`, ownershipB)
	assertOwnershipReadDenied(t, store, scope)
}

func assertOwnershipReadDenied(t *testing.T, store *storage.PGXOwnershipStore, scope storage.OwnershipScope) {
	t.Helper()
	projection, err := store.Details(t.Context(), scope)
	if !errors.Is(err, domain.ErrNotFound) || projection.Enabled || len(projection.Members) != 0 {
		t.Fatalf("unauthorized refetch leaked projection=%+v err=%v", projection, err)
	}
}

func TestOwnershipOutboxPublisherPostgreSQL(t *testing.T) {
	for _, kind := range []string{"dm", "channel"} {
		t.Run(kind, func(t *testing.T) { testOwnershipOutboxPublisher(t, kind) })
	}
}

func testOwnershipOutboxPublisher(t *testing.T, kind string) {
	pool := ownershipPool(t)
	enableOwnership(t, pool)
	ownershipExec(t, pool, `DELETE FROM chat.ownership_outbox`)
	store := storage.NewPGXOwnershipStore(pool)
	if _, err := store.Mutate(t.Context(), ownershipOutboxInput(kind)); err != nil {
		t.Fatal(err)
	}
	ownershipExec(t, pool, `UPDATE chat.ownership_outbox SET created_at=now()-interval '2 minutes'`)
	audit, outbox := roleEffects(t, pool)
	failure := errors.New("bus unavailable")
	var retries, failures int
	observe := func(retry bool, err error) {
		if retry {
			retries++
		}
		if err != nil {
			failures++
		}
	}
	if err := store.DispatchOwnershipChanges(t.Context(), func(context.Context, string, string, string) error { return failure }, observe); !errors.Is(err, failure) {
		t.Fatalf("failure=%v", err)
	}
	pendingOwnershipDelivery(t, pool, 1, "publish_failed")
	pending, age, err := store.OwnershipOutboxBacklog(t.Context())
	if err != nil || pending != 1 || age < 120 {
		t.Fatalf("backlog=%d age=%f err=%v", pending, age, err)
	}
	// Control eligibility directly, so a slow test runner cannot expire the
	// one-second backoff before the assertion. The original schedule was checked above.
	ownershipExec(t, pool, `UPDATE chat.ownership_outbox SET next_attempt_at=now()+interval '1 hour'`)
	assertOwnershipRetry(t, pool, store, observe)
	if retries != 1 || failures != 1 {
		t.Fatalf("retry=%d failures=%d", retries, failures)
	}
	assertOwnershipEffectsUnchanged(t, pool, audit, outbox)
}

func assertOwnershipRetry(t *testing.T, pool *pgxpool.Pool, store *storage.PGXOwnershipStore, observe func(bool, error)) {
	t.Helper()
	published := 0
	publish := func(context.Context, string, string, string) error { published++; return nil }
	if err := store.DispatchOwnershipChanges(t.Context(), publish, observe); err != nil || published != 0 {
		t.Fatalf("backoff ignored: %d %v", published, err)
	}
	ownershipExec(t, pool, `UPDATE chat.ownership_outbox SET next_attempt_at=now()`)
	if err := store.DispatchOwnershipChanges(t.Context(), publish, observe); err != nil {
		t.Fatal(err)
	}
	if published != 1 {
		t.Fatalf("publish=%d", published)
	}
	assertOwnershipProcessed(t, pool, 2)
}

func assertOwnershipProcessed(t *testing.T, pool *pgxpool.Pool, attempts int) {
	t.Helper()
	var count int
	err := pool.QueryRow(t.Context(), `SELECT count(*) FROM chat.ownership_outbox WHERE published_at IS NOT NULL AND attempt_count=$1 AND last_failure IS NULL`, attempts).Scan(&count)
	if err != nil || count != 1 {
		t.Fatalf("processed=%d err=%v", count, err)
	}
	pending, age, err := storage.NewPGXOwnershipStore(pool).OwnershipOutboxBacklog(t.Context())
	if err != nil || pending != 0 || age != 0 {
		t.Fatalf("empty backlog=%d/%f err=%v", pending, age, err)
	}
}

func TestOwnershipOutboxCrashReplayPostgreSQL(t *testing.T) {
	pool := ownershipPool(t)
	enableOwnership(t, pool)
	ownershipExec(t, pool, `DELETE FROM chat.ownership_outbox`)
	store := storage.NewPGXOwnershipStore(pool)
	if _, err := store.Mutate(t.Context(), ownershipOutboxInput("dm")); err != nil {
		t.Fatal(err)
	}
	audit, outbox := roleEffects(t, pool)
	// Publish succeeds, then PostgreSQL rejects the acknowledgement transaction.
	ownershipExec(t, pool, `CREATE FUNCTION chat.fail_delivery_ack() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected post-publish failure'; END $$;
 CREATE CONSTRAINT TRIGGER fail_delivery_ack AFTER UPDATE ON chat.ownership_outbox DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION chat.fail_delivery_ack()`)
	published := 0
	publish := func(context.Context, string, string, string) error { published++; return nil }
	if err := store.DispatchOwnershipChanges(t.Context(), publish); err == nil {
		t.Fatal("acknowledgement failure ignored")
	}
	ownershipExec(t, pool, `DROP TRIGGER fail_delivery_ack ON chat.ownership_outbox`)
	if err := store.DispatchOwnershipChanges(t.Context(), publish); err != nil {
		t.Fatal(err)
	}
	if published != 2 {
		t.Fatalf("crash replay publishes=%d", published)
	}
	assertOwnershipProcessed(t, pool, 1)
	assertOwnershipEffectsUnchanged(t, pool, audit, outbox)
}

func TestOwnershipOutboxTwoReplicasPostgreSQL(t *testing.T) {
	pool := ownershipPool(t)
	enableOwnership(t, pool)
	ownershipExec(t, pool, `DELETE FROM chat.ownership_outbox`)
	first := storage.NewPGXOwnershipStore(pool)
	for _, kind := range []string{"dm", "channel"} {
		if _, err := first.Mutate(t.Context(), ownershipOutboxInput(kind)); err != nil {
			t.Fatal(err)
		}
	}
	audit, outbox := roleEffects(t, pool)
	lockedID, finish := holdOwnershipPublisher(t, first)
	defer finish()
	second := storage.NewPGXOwnershipStore(pool)
	var otherIDs []string
	if err := second.DispatchOwnershipChanges(t.Context(), func(_ context.Context, _, _, id string) error { otherIDs = append(otherIDs, id); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(otherIDs) != 1 || otherIDs[0] == lockedID {
		t.Fatalf("concurrent claim processed locked row: %v vs %s", otherIDs, lockedID)
	}
	finish()
	var processed int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM chat.ownership_outbox WHERE published_at IS NOT NULL AND attempt_count=1`).Scan(&processed); err != nil || processed != 2 {
		t.Fatalf("processed=%d err=%v", processed, err)
	}
	assertOwnershipEffectsUnchanged(t, pool, audit, outbox)
}

// Hold one row's lock until the second replica has drained the other row.
func holdOwnershipPublisher(t *testing.T, store *storage.PGXOwnershipStore) (string, func()) {
	t.Helper()
	entered := make(chan string, 1)
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- store.DispatchOwnershipChanges(t.Context(), func(ctx context.Context, _, _, id string) error {
			entered <- id
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()
	var id string
	select {
	case id = <-entered:
	case <-time.After(10 * time.Second):
		close(release)
		t.Fatal("publisher did not claim")
	}
	finished := false
	return id, func() {
		if finished {
			return
		}
		finished = true
		close(release)
		awaitOwnershipPublisher(t, done)
	}
}

func awaitOwnershipPublisher(t *testing.T, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		if err != nil {
			t.Error(err)
		}
	case <-time.After(10 * time.Second):
		t.Error("publisher did not finish")
	}
}

func TestOwnershipOutboxMetadataCompatibilityPostgreSQL(t *testing.T) {
	pool := ownershipPool(t)
	audit, outbox := roleEffects(t, pool)
	// All old-release INSERT shapes remain accepted without supplying new columns.
	for reason, source := range map[string]string{"manual": "manual", "transfer": "manual", "succession": "automatic_successor", "invalidation": "workspace_invalidation", "backfill": "backfill"} {
		var gotSource, result string
		err := pool.QueryRow(t.Context(), `INSERT INTO chat.ownership_audit(workspace_id,conversation_kind,conversation_id,target_user_id,new_role,reason)
 VALUES ($1,'dm',$2,$3,'owner',$4) RETURNING source,result`, ownershipWS, ownershipDM, ownershipB, reason).Scan(&gotSource, &result)
		if err != nil || gotSource != source || result != "success" {
			t.Fatalf("reason=%s source=%s result=%s err=%v", reason, gotSource, result, err)
		}
	}
	nextAudit, nextOutbox := roleEffects(t, pool)
	if nextAudit != audit+5 || nextOutbox != outbox+5 {
		t.Fatalf("legacy audit inserts did not enqueue atomically: %d/%d", nextAudit, nextOutbox)
	}
	ownershipExec(t, pool, readChatMigration(t, "000067_ownership_outbox_delivery.down.sql"))
	ownershipExec(t, pool, readChatMigration(t, "000067_ownership_outbox_delivery.up.sql"))
	assertOwnershipEffectsUnchanged(t, pool, nextAudit, nextOutbox)
}
