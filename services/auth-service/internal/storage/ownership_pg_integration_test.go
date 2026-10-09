package storage_test

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nicrepository/nchat/services/auth-service/internal/domain"
	"github.com/nicrepository/nchat/services/auth-service/internal/storage"
)

// The actual status writer must roll back account status and session revocation
// when ownership cannot pass to an eligible participant.
func TestOwnershipAccountInvalidationPostgreSQL(t *testing.T) {
	pool := connectAuthTestDB(t)
	applyAuthMigrations(t, pool)
	applyChatMigrations(t, pool)
	a := insertActiveUser(t, pool, "ownership-a@example.test")
	b := insertActiveUser(t, pool, "ownership-b@example.test")
	const ws = "00000000-0000-0000-0000-000000000001"
	const conversation = "95300000-0000-4000-8000-000000000009"
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(t.Context(), sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO chat.workspace_members(workspace_id,user_id) VALUES($1,$2),($1,$3)`, ws, a, b)
	exec(`INSERT INTO chat.dm_conversations(id,workspace_id,type,created_by) VALUES($1,$2,'group',$3)`, conversation, ws, a)
	exec(`INSERT INTO chat.dm_members(conversation_id,user_id) VALUES($1,$2),($1,$3)`, conversation, a, b)
	exec(ownershipActivationSQL(t))
	// A BEFORE guard proves that the application reuses #1046 before the status
	// update; the existing AFTER invalidation guard alone would pass final-state checks.
	exec(`CREATE FUNCTION chat.assert_preinvalidation_owner() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF NEW.status='suspended' AND NOT EXISTS (SELECT 1 FROM chat.active_ownership_participants WHERE conversation_id='95300000-0000-4000-8000-000000000009' AND user_id<>OLD.id AND role='owner') THEN RAISE EXCEPTION 'missing owner before invalidation'; END IF;
 RETURN NEW; END $$;
 CREATE TRIGGER assert_preinvalidation_owner BEFORE UPDATE OF status ON auth.users FOR EACH ROW EXECUTE FUNCTION chat.assert_preinvalidation_owner()`)

	store := storage.NewPGXUserStore(pool)
	if _, err := store.UpdateUserStatus(t.Context(), a, "suspended"); err != nil {
		t.Fatal(err)
	}
	var owner string
	if err := pool.QueryRow(t.Context(), `SELECT user_id::text FROM chat.active_ownership_participants WHERE kind='dm' AND conversation_id=$1 AND role='owner'`, conversation).Scan(&owner); err != nil || owner != b {
		t.Fatalf("successor=%s err=%v", owner, err)
	}
	if _, err := store.UpdateUserStatus(t.Context(), a, "active"); err != nil {
		t.Fatal(err)
	}
	exec(`UPDATE chat.workspace_members SET role='guest' WHERE user_id=$1`, a)
	exec(`SELECT chat.assign_ownership('dm',$1,$2,'member',$3,'manual')`, conversation, a, b)
	exec(`INSERT INTO auth.user_sessions(user_id,refresh_token_hash,idle_expires_at) VALUES($1,'ownership-session-953',now()+interval '1 hour')`, b)
	if _, err := store.UpdateUserStatus(t.Context(), b, "suspended"); !errors.Is(err, domain.ErrConversationOwnershipConflict) {
		t.Fatalf("missing ownership conflict: %v", err)
	}
	var status string
	var revoked bool
	if err := pool.QueryRow(t.Context(), `SELECT u.status,s.revoked_at IS NOT NULL FROM auth.users u JOIN auth.user_sessions s ON s.user_id=u.id WHERE u.id=$1 AND s.refresh_token_hash='ownership-session-953'`, b).Scan(&status, &revoked); err != nil || status != "active" || revoked {
		t.Fatalf("partial invalidation status=%s revoked=%v error=%v", status, revoked, err)
	}
	exec(`UPDATE chat.workspace_members SET role='member' WHERE user_id=$1`, a)
	assertAccountInvalidationCommitRollback(t, pool, store, b, conversation)
}

// Parse the operator transaction with fixed test evidence.
func ownershipActivationSQL(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("../../../../scripts/db/ownership/activate.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(raw)
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

func assertAccountInvalidationCommitRollback(t *testing.T, pool *pgxpool.Pool, store *storage.PGXUserStore, b, conversation string) {
	t.Helper()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(t.Context(), sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	var owner string
	exec(`INSERT INTO auth.oidc_exchange_codes(provider,code_hash,access_value_encrypted,refresh_value_encrypted,bearer_scheme,expires_in,user_json,expires_at)
 VALUES('keycloak','1047-exchange','encrypted-access','encrypted-refresh','Bearer',3600,jsonb_build_object('id',$1::text),now()+interval '1 hour')`, b)
	exec(`CREATE FUNCTION auth.fail_invalidation_commit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected invalidation commit failure'; END $$;
 CREATE CONSTRAINT TRIGGER fail_invalidation_commit AFTER UPDATE ON auth.user_sessions DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION auth.fail_invalidation_commit()`)
	if _, err := store.UpdateUserStatus(t.Context(), b, "suspended"); err == nil || !strings.Contains(err.Error(), "injected invalidation commit failure") {
		t.Fatalf("err=%v", err)
	}
	var restored bool
	if err := pool.QueryRow(t.Context(), `SELECT u.status='active' AND s.revoked_at IS NULL AND c.used_at IS NULL FROM auth.users u JOIN auth.user_sessions s ON s.user_id=u.id JOIN auth.oidc_exchange_codes c ON c.user_json->>'id'=u.id::text WHERE u.id=$1 AND s.refresh_token_hash='ownership-session-953'`, b).Scan(&restored); err != nil || !restored {
		t.Fatalf("lifecycle/session/OIDC rollback=%v err=%v", restored, err)
	}
	if err := pool.QueryRow(t.Context(), `SELECT user_id::text FROM chat.active_ownership_participants WHERE kind='dm' AND conversation_id=$1 AND role='owner'`, conversation).Scan(&owner); err != nil || owner != b {
		t.Fatalf("roles survived rollback owner=%s err=%v", owner, err)
	}

}
